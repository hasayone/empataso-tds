<#
.SYNOPSIS
  Единая точка запуска тестов emtds в Docker (лимиты CPU/RAM, слоты, кэш результатов).

.PARAMETER Run
  Команда sh внутри образа test.

.PARAMETER Suite
  Пресет: full, unit, ui, integration.

.PARAMETER Force
  Игнорировать кэш завершённых прогонов.

.PARAMETER Cleanup
  Удалить завершённые контейнеры с label emtds.role=test.

.PARAMETER Status
  Занятые слоты и docker stats.

.ENV
  EMTDS_TEST_CPUS, EMTDS_TEST_SLOTS, EMTDS_TEST_MEMORY, EMTDS_TEST_GOMAXPROCS, EMTDS_TEST_IMAGE.

.OUTPUTS
  Exit code тестов; в логе GOTEST_EXIT и GOTEST_MEMPEAK_BYTES при наличии.
#>

[CmdletBinding(DefaultParameterSetName = 'Run')]
param(
    [Parameter(ParameterSetName = 'Run')]
    [string]$Run = '',

    [Parameter(ParameterSetName = 'Run')]
    [ValidateSet('full', 'unit', 'ui', 'integration')]
    [string]$Suite = 'full',

    [Parameter(ParameterSetName = 'Run')]
    [switch]$Force,

    [Parameter(ParameterSetName = 'Cleanup')]
    [switch]$Cleanup,

    [Parameter(ParameterSetName = 'Status')]
    [switch]$Status
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Script:TdsDir = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

$Script:StateDir = Join-Path $env:LOCALAPPDATA 'emtds-test'
$Script:SlotsDir = Join-Path $Script:StateDir 'slots'
$Script:CacheDir = Join-Path $Script:StateDir 'cache'
$Script:InflightDir = Join-Path $Script:StateDir 'inflight'
$Script:ResultsDir = Join-Path $Script:StateDir 'results'
$Script:BuildLockPath = Join-Path $Script:StateDir 'build.lock'
$Script:GoCacheVolume = 'emtds-test-gocache'
$Script:MaxCacheEntries = 20

<#
.SYNOPSIS
  Читает переменную окружения как число с плавающей точкой или возвращает значение по умолчанию.
#>
function Get-EnvDouble {
    param([string]$Name, [double]$Default)
    $v = [Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrWhiteSpace($v)) { return $Default }
    return [double]$v
}

<#
.SYNOPSIS
  Читает переменную окружения как целое или возвращает значение по умолчанию.
#>
function Get-EnvInt {
    param([string]$Name, [int]$Default)
    $v = [Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrWhiteSpace($v)) { return $Default }
    return [int]$v
}

<#
.SYNOPSIS
  Читает строковую переменную окружения или возвращает значение по умолчанию.
#>
function Get-EnvString {
    param([string]$Name, [string]$Default)
    $v = [Environment]::GetEnvironmentVariable($Name)
    if ([string]::IsNullOrWhiteSpace($v)) { return $Default }
    return $v
}

<#
.SYNOPSIS
  Вычисляет SHA-256 от текста в виде hex-строки.
#>
function Get-Sha256Hex {
    param([string]$Text)
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($Text)
    $hash = [System.Security.Cryptography.SHA256]::Create().ComputeHash($bytes)
    return ([BitConverter]::ToString($hash) -replace '-', '').ToLowerInvariant()
}

<#
.SYNOPSIS
  Создаёт каталоги состояния слотов, кэша и результатов.
#>
function Ensure-StateDirs {
    foreach ($d in @($Script:StateDir, $Script:SlotsDir, $Script:CacheDir, $Script:InflightDir, $Script:ResultsDir)) {
        if (-not (Test-Path $d)) {
            New-Item -ItemType Directory -Path $d -Force | Out-Null
        }
    }
}

<#
.SYNOPSIS
  Возвращает sh-команду для пресета Suite.
#>
function Get-SuiteCommand {
    param([string]$Name)
    switch ($Name) {
        'unit' {
            return 'go vet ./... && go test -race -count=1 ./...'
        }
        'ui' {
            return 'go test -race -count=1 -tags=uitest ./internal/admin/...'
        }
        'integration' {
            return 'go test -tags=integration -count=1 ./internal/store/'
        }
        default {
            return 'go vet ./... && go test -race -count=1 ./... && go test -race -count=1 -tags=uitest ./internal/admin/...'
        }
    }
}

<#
.SYNOPSIS
  Определяет итоговую sh-команду из -Run или -Suite.
#>
function Get-ResolvedRunCommand {
    if (-not [string]::IsNullOrWhiteSpace($Run)) {
        return $Run.Trim()
    }
    return (Get-SuiteCommand -Name $Suite)
}

<#
.SYNOPSIS
  Округление вверх до целого.
#>
function Get-CeilingInt {
    param([double]$Value)
    return [int][Math]::Ceiling($Value)
}

<#
.SYNOPSIS
  Проверяет, занят ли lock-файл другим процессом (эксклюзивное открытие).
#>
function Test-LockFileHeld {
    param([string]$Path)
    try {
        $fs = [System.IO.File]::Open($Path, [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
        $fs.Dispose()
        return $false
    }
    catch {
        return $true
    }
}

<#
.SYNOPSIS
  Удаляет завершённые тестовые контейнеры с label emtds.role=test.
#>
function Invoke-CleanupTestContainers {
    $ids = docker ps -aq --filter 'label=emtds.role=test' 2>$null
    if (-not $ids) { return }
    foreach ($id in ($ids -split "`n")) {
        $id = $id.Trim()
        if (-not $id) { continue }
        $st = (docker inspect -f '{{.State.Status}}' $id 2>$null)
        if ($st -in @('exited', 'created', 'dead')) {
            docker rm -f $id 2>$null | Out-Null
        }
    }
}

<#
.SYNOPSIS
  Показывает состояние слотов и docker stats для тестовых контейнеров.
#>
function Show-Status {
    Ensure-StateDirs
    Write-Host '=== Слоты ==='
    $maxSlots = Get-EnvInt -Name 'EMTDS_TEST_SLOTS' -Default 2
    for ($i = 0; $i -lt $maxSlots; $i++) {
        $lp = Join-Path $Script:SlotsDir ("slot{0}.lock" -f $i)
        $held = Test-LockFileHeld -Path $lp
        Write-Host ("slot{0}: {1}" -f $i, $(if ($held) { 'занят' } else { 'свободен' }))
    }
    Write-Host ''
    Write-Host '=== docker ps (emtds.role=test) ==='
    docker ps --filter 'label=emtds.role=test' --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}'
    Write-Host ''
    Write-Host '=== docker stats (one-shot) ==='
    $running = docker ps -q --filter 'label=emtds.role=test'
    if ($running) {
        docker stats --no-stream $running
    }
    else {
        Write-Host '(нет запущенных тест-контейнеров)'
    }
}

<#
.SYNOPSIS
  Считает число запущенных контейнеров с label emtds.role=test.
#>
function Get-RunningTestContainerCount {
    $n = 0
    $out = docker ps -q --filter 'label=emtds.role=test' 2>$null
    if ($out) { $n += @($out -split "`n" | Where-Object { $_.Trim() }).Count }
    return $n
}

<#
.SYNOPSIS
  Ждёт, пока число тест-контейнеров станет меньше лимита слотов.
#>
function Wait-ForSlotCapacity {
    param([int]$MaxSlots)
    $lastMsg = [DateTime]::MinValue
    while ((Get-RunningTestContainerCount) -ge $MaxSlots) {
        $now = Get-Date
        if (($now - $lastMsg).TotalSeconds -ge 30) {
            Write-Host 'жду слот… (лимит по числу контейнеров)'
            $lastMsg = $now
        }
        Start-Sleep -Seconds 5
    }
}

<#
.SYNOPSIS
  Захватывает эксклюзивный слот и возвращает дескриптор lock-файла.
#>
function Open-TestSlot {
    param([int]$MaxSlots)
    Ensure-StateDirs
    $lastMsg = [DateTime]::MinValue
    while ($true) {
        Wait-ForSlotCapacity -MaxSlots $MaxSlots
        for ($n = 0; $n -lt $MaxSlots; $n++) {
            $path = Join-Path $Script:SlotsDir ("slot{0}.lock" -f $n)
            try {
                $fs = [System.IO.File]::Open($path, [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
                return @{ Index = $n; Stream = $fs; Path = $path }
            }
            catch {
                continue
            }
        }
        $now = Get-Date
        if (($now - $lastMsg).TotalSeconds -ge 30) {
            Write-Host 'жду слот…'
            $lastMsg = $now
        }
        Start-Sleep -Seconds 5
    }
}

<#
.SYNOPSIS
  Собирает образ test под блокировкой и возвращает image ID.
#>
function Invoke-LockedBuild {
    param([string]$TdsPath, [string]$Tag)
    Ensure-StateDirs
    $lastMsg = [DateTime]::MinValue
    while ($true) {
        try {
            $bl = [System.IO.File]::Open($Script:BuildLockPath, [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
            break
        }
        catch {
            $now = Get-Date
            if (($now - $lastMsg).TotalSeconds -ge 30) {
                Write-Host 'жду блокировку сборки образа…'
                $lastMsg = $now
            }
            Start-Sleep -Seconds 2
        }
    }
    try {
        Push-Location $TdsPath
        $imageId = (docker build -q -f Dockerfile --target test -t $Tag . 2>&1 | Select-Object -Last 1)
        if ($LASTEXITCODE -ne 0) {
            throw "docker build failed with exit $LASTEXITCODE"
        }
        $imageId = $imageId.Trim()
        if (-not $imageId) {
            throw 'docker build не вернул image ID'
        }
        return $imageId
    }
    finally {
        $bl.Dispose()
        Pop-Location
    }
}

<#
.SYNOPSIS
  Оставляет в кэше не более MaxCacheEntries последних результатов.
#>
function Trim-ResultCache {
    $files = @(Get-ChildItem -Path $Script:CacheDir -Filter '*.json' -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending)
    if ($files.Count -le $Script:MaxCacheEntries) { return }
    $files | Select-Object -Skip $Script:MaxCacheEntries | ForEach-Object { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue }
}

<#
.SYNOPSIS
  Путь к JSON-файлу кэша по ключу dedupe.
#>
function Get-CachePath {
    param([string]$Key)
    return Join-Path $Script:CacheDir ($Key + '.json')
}

<#
.SYNOPSIS
  Читает кэш результата прогона или возвращает $null.
#>
function Read-ResultCache {
    param([string]$Key)
    $p = Get-CachePath -Key $Key
    if (-not (Test-Path $p)) { return $null }
    try {
        return Get-Content -Raw -Path $p | ConvertFrom-Json
    }
    catch {
        return $null
    }
}

<#
.SYNOPSIS
  Сохраняет exit code и путь к логу в кэш результатов.
#>
function Write-ResultCache {
    param([string]$Key, [int]$ExitCode, [string]$OutputPath)
    $p = Get-CachePath -Key $Key
    $obj = @{
        exitCode   = $ExitCode
        outputFile = $OutputPath
        finishedAt = (Get-Date).ToString('o')
    }
    $obj | ConvertTo-Json -Compress | Set-Content -Path $p -Encoding UTF8
    Trim-ResultCache
}

<#
.SYNOPSIS
  Печатает кэшированный лог и завершает процесс с сохранённым exit code.
#>
function Invoke-ReplayCachedResult {
    param([string]$Key)
    $cached = Read-ResultCache -Key $Key
    if ($cached -and (Test-Path $cached.outputFile)) {
        Write-Host "(кэш результата, ключ $($Key.Substring(0, 12))…)"
        Get-Content -Raw -Path $cached.outputFile | Write-Host
        exit [int]$cached.exitCode
    }
}

<#
.SYNOPSIS
  Захватывает inflight-lock или ждёт чужой прогон и отдаёт его результат.
#>
function Open-InflightLock {
    param(
        [string]$Key,
        [string]$InflightPath,
        [switch]$SkipCache
    )
    $lastMsg = [DateTime]::MinValue
    while ($true) {
        if (-not $SkipCache) {
            $cached = Read-ResultCache -Key $Key
            if ($cached -and (Test-Path $cached.outputFile)) {
                Invoke-ReplayCachedResult -Key $Key
            }
        }
        try {
            $fs = [System.IO.File]::Open($InflightPath, [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
            return $fs
        }
        catch {
            $now = Get-Date
            if (($now - $lastMsg).TotalSeconds -ge 30) {
                Write-Host 'жду завершения такого же прогона…'
                $lastMsg = $now
            }
            Start-Sleep -Seconds 5
        }
    }
}

<#
.SYNOPSIS
  Читает учётные данные ClickHouse из emtds-admin в env процесса (без записи на диск).
#>
function Set-IntegrationProcessEnv {
    $user = (docker exec emtds-admin printenv EMTDS_CLICKHOUSE_USER 2>$null)
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($user)) {
        throw 'Не удалось прочитать EMTDS_CLICKHOUSE_USER и EMTDS_CLICKHOUSE_PASSWORD из emtds-admin (контейнер должен быть Up).'
    }
    $pass = (docker exec emtds-admin printenv EMTDS_CLICKHOUSE_PASSWORD 2>$null)
    $env:EMTDS_CLICKHOUSE_USER = $user.Trim()
    $env:EMTDS_CLICKHOUSE_PASSWORD = "$pass".Trim()
}

<#
.SYNOPSIS
  Удаляет временные переменные ClickHouse из env процесса.
#>
function Clear-IntegrationProcessEnv {
    Remove-Item Env:EMTDS_CLICKHOUSE_USER -ErrorAction SilentlyContinue
    Remove-Item Env:EMTDS_CLICKHOUSE_PASSWORD -ErrorAction SilentlyContinue
}

<#
.SYNOPSIS
  Оборачивает пользовательскую sh-команду хвостом с exit code и пиком памяти cgroup.
#>
function Build-WrapperShell {
    param([string]$Inner)
    $tail = '; ec=$?; if [ -f /sys/fs/cgroup/memory.peak ]; then echo GOTEST_MEMPEAK_BYTES=$(cat /sys/fs/cgroup/memory.peak); elif [ -f /sys/fs/cgroup/memory.max.usage_in_bytes ]; then echo GOTEST_MEMPEAK_BYTES=$(cat /sys/fs/cgroup/memory.max.usage_in_bytes); fi; echo GOTEST_EXIT=$ec; exit $ec'
    return ($Inner + $tail)
}

if ($Cleanup) {
    Invoke-CleanupTestContainers
    exit 0
}

if ($Status) {
    Show-Status
    exit 0
}

Ensure-StateDirs

$cpus = Get-EnvDouble -Name 'EMTDS_TEST_CPUS' -Default 1.0
$maxSlots = Get-EnvInt -Name 'EMTDS_TEST_SLOTS' -Default 2
$memSpec = Get-EnvString -Name 'EMTDS_TEST_MEMORY' -Default '4g'
$gomax = Get-EnvInt -Name 'EMTDS_TEST_GOMAXPROCS' -Default (Get-CeilingInt -Value $cpus)
$buildTag = Get-EnvString -Name 'EMTDS_TEST_IMAGE' -Default 'emtds-src'

$runCmd = Get-ResolvedRunCommand
$isIntegration = ($Suite -eq 'integration') -or ($runCmd -match '(?i)-tags=integration')

$imageId = Invoke-LockedBuild -TdsPath $Script:TdsDir -Tag $buildTag
$dedupeKey = Get-Sha256Hex -Text ($runCmd + "`0" + $imageId)
$inflightPath = Join-Path $Script:InflightDir ($dedupeKey + '.lock')
$resultOut = Join-Path $Script:ResultsDir ("{0}-{1}.log" -f $dedupeKey.Substring(0, 12), $PID)

if (-not $Force -and -not $isIntegration) {
    Invoke-ReplayCachedResult -Key $dedupeKey
}

$skipInflightCache = [bool]($Force -or $isIntegration)
$inflightStream = Open-InflightLock -Key $dedupeKey -InflightPath $inflightPath -SkipCache:$skipInflightCache

$slot = $null
$containerName = ''
$integrationEnvSet = $false
$exitCode = 1

try {
    $slot = Open-TestSlot -MaxSlots $maxSlots
    $containerName = ('emtds-test-{0}-{1}' -f $slot.Index, $PID)

    $wrapped = Build-WrapperShell -Inner $runCmd
    $dockerArgs = @(
        'run',
        '--rm',
        '--name', $containerName,
        '--label', 'emtds.role=test',
        '--label', 'emtds.owner=cursor-agent',
        ('--label', ('emtds.slot={0}' -f $slot.Index)),
        '--cpus', ([string]$cpus),
        '-e', ('GOMAXPROCS={0}' -f $gomax),
        '-e', ('GOFLAGS=-p={0}' -f $gomax),
        '-v', ('{0}:/root/.cache/go-build' -f $Script:GoCacheVolume),
        '-w', '/src'
    )

    if ($memSpec -and $memSpec -ne '0') {
        $dockerArgs += '--memory', $memSpec
        $dockerArgs += '--memory-swap', $memSpec
    }

    if ($isIntegration) {
        Set-IntegrationProcessEnv
        $integrationEnvSet = $true
        $dockerArgs += '-e', 'EMTDS_CLICKHOUSE_ADDR=clickhouse:9000'
        $dockerArgs += '-e', 'EMTDS_CLICKHOUSE_USER'
        $dockerArgs += '-e', 'EMTDS_CLICKHOUSE_PASSWORD'
        $dockerArgs += '--network', 'emtds-infra_default'
    }

    $dockerArgs += $imageId
    $dockerArgs += 'sh', '-c', $wrapped

    $logBuilder = New-Object System.Text.StringBuilder
    $prevEap = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & docker @dockerArgs 2>&1 | ForEach-Object {
        $l = $_.ToString()
        Write-Host $l
        [void]$logBuilder.AppendLine($l)
    }
    $exitCode = $LASTEXITCODE
    $ErrorActionPreference = $prevEap

    if ($exitCode -eq 137) {
        Write-Host 'OOM: контейнер убит (exit 137). Увеличьте EMTDS_TEST_MEMORY.' -ForegroundColor Red
    }
    else {
        $fullLog = $logBuilder.ToString()
        if ($fullLog -match 'GOTEST_EXIT=(\d+)') {
            $exitCode = [int]$Matches[1]
        }
    }

    [System.IO.File]::WriteAllText($resultOut, $logBuilder.ToString(), [System.Text.UTF8Encoding]::new($false))

    if (-not $isIntegration) {
        Write-ResultCache -Key $dedupeKey -ExitCode $exitCode -OutputPath $resultOut
    }
}
finally {
    if ($containerName) {
        try { docker rm -f $containerName 2>$null | Out-Null } catch { }
    }
    if ($integrationEnvSet) {
        Clear-IntegrationProcessEnv
    }
    if ($slot -and $slot.Stream) {
        $slot.Stream.Dispose()
    }
    if ($inflightStream) {
        $inflightStream.Dispose()
        Remove-Item -Force $inflightPath -ErrorAction SilentlyContinue
    }
}

exit $exitCode
