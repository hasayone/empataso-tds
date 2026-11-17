<#
.SYNOPSIS
  Local click and postback load runs against the engine, checked against ClickHouse.

.DESCRIPTION
  Local only. The generator is grafana/k6 in its own container on the
  emtds-infra_default network with CPU and memory limits. Clicks carry sub_id_12 =
  "lt-<Run>-<n>", so a run is counted and removed by that mark only.

  Actions:
    prepare        copy groups.json from the engine, sample real clicks from
                   ClickHouse and build targets.json and quiet_groups.json.
    engine-load    recreate the engine with the production limits
                   (-EngineMem, -EngineCpus, GOMEMLIMIT) and the docker
                   network as a trusted proxy, so X-Forwarded-For is honoured.
    engine-normal  recreate the engine with the plain local settings and delete
                   the override file of engine-load.
    engine-recreate recreate only the engine, with the override file of the
                   last engine-load when it is in the state directory, else
                   with the plain settings. It is what the engine chaos runs.
    clicks         one click run: -Rate and -Duration, or -Stages.
    chaos          a click run like clicks, with a fault from -ChaosTarget
                   (required) injected while the load runs.
    postbacks      -Count postbacks over clicks of -FromRun at -Rate per second,
                   only for campaigns without outbound postback templates.
    cleanup        delete every "lt-" row from events, conversions and
                   postback_log (lightweight DELETE limited to the last two
                   days) and
                   the Redis uniqueness (uniq*) and sticky (st:) keys of the
                   test addresses.
    job            print the Keitaro job state.

  Recreating the engine requires the Keitaro job to be idle (running and
  sync_running false). ClickHouse, Redis and cron are never recreated.
  The local admin login for that check comes from the environment variables
  EMTDS_LT_ADMIN_USER and EMTDS_LT_ADMIN_PASSWORD, admin and admin when unset.

  Chaos (actions clicks, chaos and postbacks, -ChaosTarget other than none).
  A background job waits -ChaosAt seconds after the k6 container starts, then:
    clickhouse  docker pause emtds-infra-clickhouse-1, docker unpause after -ChaosFor s
    redis       the same for emtds-infra-redis-1
    engine      recreates only the engine (engine-recreate); needs the Keitaro
                job idle, which is checked before the load starts
  Only pause and unpause are used: never stop, rm or down. The container is
  unpaused as soon as the fault job ends, again after the job is stopped, and in
  the finally block even after an error or Ctrl+C; each time State.Paused is
  checked. If the k6 container does not appear within 60 s, no fault is
  injected and the run ends with an error; so does a fault job that reports an error.

  After k6 the script waits at least 12 s for the engine to write what it holds:
  it polls GET /internal/metrics inside the engine container until
  events.pending is 0, for at most -SettleMax seconds. An engine without that
  endpoint gets the 12 s only. During the run the same endpoint is sampled every
  2 s for the peak of events.queue_len, events.pending, events.input_len and,
  when the engine reports it, events.spool.segments.

.PARAMETER Action
  One of the actions above.
.PARAMETER Run
  Run label, letters and digits.
.PARAMETER Rate
  Requests per second for clicks or postbacks.
.PARAMETER Duration
  Run length, a k6 duration such as 60s.
.PARAMETER Stages
  Step plan "rate:duration,..." for clicks instead of -Rate.
.PARAMETER FromRun
  Click run whose click ids receive the postbacks.
.PARAMETER Count
  Number of postbacks.
.PARAMETER EngineMem
  Engine memory limit for engine-load (1g or 2g, as install.sh sets).
.PARAMETER EngineCpus
  Engine CPU limit for engine-load.
.PARAMETER GenCpus
  CPU limit of the k6 container.
.PARAMETER ChaosTarget
  Fault to inject: none (default), clickhouse, redis or engine.
.PARAMETER ChaosAt
  Seconds from the start of the k6 container to the fault (default 20).
.PARAMETER ChaosFor
  Seconds a paused container stays paused (default 45). Not used by engine.
.PARAMETER SettleMax
  Longest wait, in seconds, for events.pending to reach 0 after k6 (default 180).

.OUTPUTS
  A JSON report on stdout and in <state>\<Run>.report.json, and a Markdown
  summary in <state>\<Run>.report.md. Peak queue figures come from
  <state>\<Run>.metrics.jsonl, the fault log from <state>\<Run>.chaos.json.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateSet('prepare', 'engine-load', 'engine-normal', 'engine-recreate', 'clicks', 'chaos', 'postbacks', 'cleanup', 'job')]
    [string]$Action,
    [string]$Run = '',
    [int]$Rate = 0,
    [string]$Duration = '60s',
    [string]$Stages = '',
    [string]$FromRun = '',
    [int]$Count = 2000,
    [ValidateSet('1g', '2g')]
    [string]$EngineMem = '2g',
    [string]$EngineCpus = '2',
    [string]$GenCpus = '4',
    [ValidateSet('none', 'clickhouse', 'redis', 'engine')]
    [string]$ChaosTarget = 'none',
    [ValidateRange(0, 3600)]
    [int]$ChaosAt = 20,
    [ValidateRange(1, 3600)]
    [int]$ChaosFor = 45,
    [ValidateRange(0, 3600)]
    [int]$SettleMax = 180
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Script:TdsDir = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$Script:StateDir = Join-Path $env:LOCALAPPDATA 'emtds-load'
$Script:K6Image = 'grafana/k6:latest'
$Script:Watched = @('emtds-engine', 'emtds-admin', 'emtds-infra-clickhouse-1', 'emtds-infra-redis-1')

<#
.SYNOPSIS
  Reads the ClickHouse login from emtds-admin into the process environment. Nothing is printed or written.
#>
function Set-ChLogin {
    if ($env:EMTDS_LT_CHU) { return }
    $env:EMTDS_LT_CHU = (docker exec emtds-admin printenv EMTDS_CLICKHOUSE_USER).Trim()
    $env:EMTDS_LT_CHP = "$(docker exec emtds-admin printenv EMTDS_CLICKHOUSE_PASSWORD)".Trim()
}

<#
.SYNOPSIS
  Runs one ClickHouse query in database emtds and returns its text output. Throws on a non-zero exit.
#>
function Invoke-Ch {
    param([string]$Query, [string]$Format = 'TSV')
    Set-ChLogin
    $out = docker exec emtds-infra-clickhouse-1 clickhouse-client --user $env:EMTDS_LT_CHU --password $env:EMTDS_LT_CHP -d emtds --format $Format -q $Query
    if ($LASTEXITCODE -ne 0) { throw "clickhouse query failed: $Query" }
    return $out
}

<#
.SYNOPSIS
  Checks the run label: letters and digits only. Throws otherwise.
#>
function Assert-RunLabel {
    param([string]$Label)
    if ($Label -notmatch '^[A-Za-z0-9]+$') { throw "run label must be letters and digits: '$Label'" }
}

<#
.SYNOPSIS
  Returns the Keitaro job state from the local admin. The session login comes from
  EMTDS_LT_ADMIN_USER and EMTDS_LT_ADMIN_PASSWORD, admin and admin when unset.
#>
function Get-KeitaroJob {
    $user = if ($env:EMTDS_LT_ADMIN_USER) { $env:EMTDS_LT_ADMIN_USER } else { 'admin' }
    $password = if ($env:EMTDS_LT_ADMIN_PASSWORD) { $env:EMTDS_LT_ADMIN_PASSWORD } else { 'admin' }
    $body = @{ login = $user; password = $password } | ConvertTo-Json -Compress
    $s = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    Invoke-WebRequest -UseBasicParsing -WebSession $s -Method Post -Uri 'http://127.0.0.1:8090/api/login' `
        -ContentType 'application/json' -Body $body | Out-Null
    return (Invoke-WebRequest -UseBasicParsing -WebSession $s 'http://127.0.0.1:8090/api/keitaro/job').Content | ConvertFrom-Json
}

<#
.SYNOPSIS
  Throws unless the Keitaro transfer and the sync are both idle.
#>
function Assert-KeitaroIdle {
    $job = Get-KeitaroJob
    if ($job.running -or $job.sync_running) { throw 'Keitaro job is busy: wait for running and sync_running to be false' }
}

<#
.SYNOPSIS
  Recreates only the engine, optionally with the load override file, and waits until it is healthy.
#>
function Reset-Engine {
    param([string]$Override = '')
    Assert-KeitaroIdle
    $files = @('-f', 'deploy/docker-compose.app.yml')
    if ($Override) { $files += @('-f', $Override) }
    Push-Location $Script:TdsDir
    try {
        docker compose --env-file .env @files up -d --force-recreate --no-deps engine
        if ($LASTEXITCODE -ne 0) { throw 'engine recreate failed' }
    }
    finally { Pop-Location }
    Wait-EngineHealthy
}

<#
.SYNOPSIS
  Waits up to two minutes for the engine container to report healthy. Throws on timeout.
#>
function Wait-EngineHealthy {
    $deadline = (Get-Date).AddMinutes(2)
    while ((Get-Date) -lt $deadline) {
        if ((docker inspect -f '{{.State.Health.Status}}' emtds-engine 2>$null) -eq 'healthy') { return }
        Start-Sleep -Seconds 2
    }
    throw 'engine did not become healthy'
}

<#
.SYNOPSIS
  Writes the compose override with production-like engine limits and returns its path.
#>
function Write-LoadOverride {
    $gomem = if ($EngineMem -eq '1g') { '920MiB' } else { '1840MiB' }
    $path = Join-Path $Script:StateDir 'compose.load.yml'
    @"
services:
  engine:
    mem_limit: $EngineMem
    cpus: $EngineCpus
    environment:
      GOMEMLIMIT: $gomem
      EMTDS_TRUSTED_PROXIES: 127.0.0.1/32,::1/128,172.16.0.0/12
"@ | Set-Content -Encoding ascii -Path $path
    return $path
}

<#
.SYNOPSIS
  Returns the engine's event loss counters from /healthz as a hashtable.
#>
function Get-EngineLosses {
    $h = docker exec emtds-engine curl -sS -D - -o /dev/null http://127.0.0.1:8080/healthz 2>$null
    $res = @{ full = 0; queue = 0; insert = 0; late = 0 }
    foreach ($line in $h) {
        if ($line -match '^X-Events-Lost-Detail:\s*(.+)$') {
            foreach ($kv in ($Matches[1].Trim() -split ' ')) {
                $k, $v = $kv -split '='
                $res[$k] = [int64]$v
            }
        }
    }
    return $res
}

<#
.SYNOPSIS
  Recreates only the engine, with the override file of the last engine-load when it exists.
#>
function Invoke-EngineRecreate {
    $override = Join-Path $Script:StateDir 'compose.load.yml'
    if (Test-Path $override) { Reset-Engine -Override $override } else { Reset-Engine }
}

<#
.SYNOPSIS
  Reads the engine metrics snapshot from inside the engine container.
.OUTPUTS
  The parsed JSON, or $null when the engine does not answer or has no /internal/metrics.
#>
function Get-EngineMetrics {
    try {
        $raw = docker exec emtds-engine curl -sS --max-time 3 http://127.0.0.1:8080/internal/metrics 2>$null
        if ($LASTEXITCODE -ne 0 -or -not $raw) { return $null }
        $o = ($raw -join '') | ConvertFrom-Json
        if ($o -and $o.PSObject.Properties['events']) { return $o }
    }
    catch { Write-Verbose "engine metrics not read: $($_.Exception.Message)" }
    return $null
}

<#
.SYNOPSIS
  Starts sampling the engine metrics every 2 s into a file, one "<time><TAB><json>" line per sample. Returns the job.
#>
function Start-MetricsSampler {
    param([string]$Path)
    if (Test-Path $Path) { Remove-Item $Path }
    return Start-Job -ArgumentList $Path -ScriptBlock {
        param($path)
        while ($true) {
            $json = docker exec emtds-engine curl -sS --max-time 2 http://127.0.0.1:8080/internal/metrics 2>$null
            if ($LASTEXITCODE -eq 0 -and $json) { Add-Content -Path $path -Value ((Get-Date).ToString('o') + "`t" + ($json -join '')) }
            Start-Sleep -Seconds 2
        }
    }
}

<#
.SYNOPSIS
  Reads a metrics sample file and returns the peak queue figures and the sample count.
  spool_segments stays null while the engine does not report events.spool.segments.
#>
function Get-MetricsPeaks {
    param([string]$Path)
    $peak = [ordered]@{ samples = 0; queue_len = 0; pending = 0; input_len = 0; spool_segments = $null }
    if (-not (Test-Path $Path)) { return $peak }
    foreach ($line in Get-Content $Path) {
        try { $o = ($line -split "`t", 2)[-1] | ConvertFrom-Json } catch { continue }
        if (-not $o -or -not $o.PSObject.Properties['events']) { continue }
        $e = $o.events
        $peak.samples++
        $peak.queue_len = [math]::Max([int64]$peak.queue_len, [int64]$e.queue_len)
        $peak.pending = [math]::Max([int64]$peak.pending, [int64]$e.pending)
        $peak.input_len = [math]::Max([int64]$peak.input_len, [int64]$e.input_len)
        $spool = $e.PSObject.Properties['spool']
        if ($spool -and $spool.Value -and $spool.Value.PSObject.Properties['segments']) {
            $cur = if ($null -eq $peak.spool_segments) { 0 } else { $peak.spool_segments }
            $peak.spool_segments = [math]::Max([int64]$cur, [int64]$spool.Value.segments)
        }
    }
    return $peak
}

<#
.SYNOPSIS
  Waits for the engine to write what it still holds after the load.
  At least 12 s; then, while the engine reports events.pending above 0, up to -SettleMax s.
  An engine without /internal/metrics gets the 12 s only.
#>
function Wait-EngineSettled {
    $sw = [Diagnostics.Stopwatch]::StartNew()
    Start-Sleep -Seconds 12
    $probe = Get-EngineMetrics
    $available = [bool]$probe
    $timedOut = $false
    while ($probe -and [int64]$probe.events.pending -gt 0) {
        if ($sw.Elapsed.TotalSeconds -ge $SettleMax) { $timedOut = $true; break }
        Start-Sleep -Seconds 2
        $probe = Get-EngineMetrics
    }
    $left = if ($probe) { [int64]$probe.events.pending } else { $null }
    return [ordered]@{ waited_s = [math]::Round($sw.Elapsed.TotalSeconds); metrics_available = $available; pending_left = $left; timed_out = $timedOut }
}

<#
.SYNOPSIS
  Returns the container the chaos target pauses, or an empty string for engine and none.
#>
function Get-ChaosContainer {
    param([string]$Target)
    switch ($Target) {
        'clickhouse' { return 'emtds-infra-clickhouse-1' }
        'redis' { return 'emtds-infra-redis-1' }
        default { return '' }
    }
}

<#
.SYNOPSIS
  Unpauses the chaos container and checks State.Paused, up to three tries.
  Safe to call when it is not paused; never throws, and warns when the container stays paused.
  It only unpauses: nothing here stops or removes a container.
#>
function Resume-Chaos {
    $container = Get-ChaosContainer $ChaosTarget
    if (-not $container) { return }
    $prev = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        for ($attempt = 0; $attempt -lt 3; $attempt++) {
            if ((docker inspect -f '{{.State.Paused}}' $container 2>$null) -ne 'true') { return }
            docker unpause $container 2>&1 | Out-Null
            Start-Sleep -Milliseconds 500
        }
        if ((docker inspect -f '{{.State.Paused}}' $container 2>$null) -eq 'true') {
            Write-Warning "$container is still paused: run docker unpause $container"
        }
    }
    finally { $ErrorActionPreference = $prev }
}

<#
.SYNOPSIS
  Starts the fault job when -ChaosTarget is not none. Returns the job, or $null.
  The job waits for the k6 container, sleeps -ChaosAt s, then pauses the target
  for -ChaosFor s or recreates the engine, and writes what it did to the log file.
  For the engine the Keitaro job must be idle; that is checked here, before the load starts.
#>
function Start-Chaos {
    param([string]$LogPath)
    if (Test-Path $LogPath) { Remove-Item $LogPath }
    if ($ChaosTarget -eq 'none') { return $null }
    if ($ChaosTarget -eq 'engine') { Assert-KeitaroIdle }
    $jobArgs = @($ChaosTarget, (Get-ChaosContainer $ChaosTarget), $ChaosAt, $ChaosFor, "emtds-load-$Run", $LogPath, $PSCommandPath)
    return Start-Job -ArgumentList $jobArgs -ScriptBlock {
        param($target, $container, $at, $hold, $k6Name, $log, $script)
        $info = [ordered]@{ target = $target; at_s = $at; for_s = $hold; started_at = $null; ended_at = $null; actual_s = $null; error = $null }
        try {
            $deadline = (Get-Date).AddSeconds(60)
            while ((Get-Date) -lt $deadline -and -not (docker ps -q --filter "name=$k6Name")) { Start-Sleep -Milliseconds 500 }
            if (-not (docker ps -q --filter "name=$k6Name")) { throw "k6 container $k6Name did not start within 60 s: no fault was injected" }
            Start-Sleep -Seconds $at
            $sw = [Diagnostics.Stopwatch]::StartNew()
            $info.started_at = (Get-Date).ToString('o')
            if ($container) {
                docker pause $container | Out-Null
                if ($LASTEXITCODE -ne 0) { throw "docker pause $container failed" }
                Start-Sleep -Seconds $hold
                docker unpause $container | Out-Null
                if ($LASTEXITCODE -ne 0) { throw "docker unpause $container failed" }
            }
            else {
                & powershell -NoProfile -ExecutionPolicy Bypass -File $script -Action engine-recreate | Out-Null
                if ($LASTEXITCODE -ne 0) { throw 'engine-recreate failed' }
            }
            $info.actual_s = [math]::Round($sw.Elapsed.TotalSeconds, 1)
        }
        catch { $info.error = $_.Exception.Message }
        finally {
            $info.ended_at = (Get-Date).ToString('o')
            if ($container) { docker unpause $container 2>&1 | Out-Null }
            $info | ConvertTo-Json | Set-Content -Encoding utf8 -Path $log
        }
    }
}

<#
.SYNOPSIS
  Waits for the fault job to finish, removes it, unpauses the chaos container and returns what
  the job logged, or $null when there was no job. Throws when the job logged an error or left no log.
#>
function Stop-Chaos {
    param($Job, [string]$LogPath)
    if ($Job) {
        Wait-Job $Job -Timeout ($ChaosAt + $ChaosFor + 300) | Out-Null
        Stop-Job $Job -ErrorAction SilentlyContinue
        Remove-Job $Job -Force -ErrorAction SilentlyContinue
        Resume-Chaos
    }
    if (-not (Test-Path $LogPath)) {
        if ($Job) { throw 'chaos job left no log: the fault state is unknown' }
        return $null
    }
    $info = Get-Content -Raw $LogPath | ConvertFrom-Json
    if ($info.error) { throw "chaos failed: $($info.error)" }
    return $info
}

<#
.SYNOPSIS
  Starts sampling docker stats of the watched containers every 2 s into a CSV. Returns the job.
#>
function Start-StatsSampler {
    param([string]$CsvPath)
    if (Test-Path $CsvPath) { Remove-Item $CsvPath }
    return Start-Job -ArgumentList $CsvPath, $Script:Watched -ScriptBlock {
        param($csv, $names)
        while ($true) {
            $ts = (Get-Date).ToString('HH:mm:ss')
            docker stats --no-stream --format '{{.Name}};{{.CPUPerc}};{{.MemUsage}}' @names 2>$null |
                ForEach-Object { "$ts;$_" } | Add-Content -Path $csv
            Start-Sleep -Seconds 2
        }
    }
}

<#
.SYNOPSIS
  Converts a docker memory figure such as 220.9MiB or 1.2GiB to MiB.
#>
function ConvertTo-MiB {
    param([string]$Text)
    if ($Text -notmatch '^([\d.]+)\s*([KMG]i?B|B)$') { return 0 }
    $n = [double]$Matches[1]
    switch -Regex ($Matches[2]) {
        '^G' { return $n * 1024 }
        '^M' { return $n }
        '^K' { return $n / 1024 }
        default { return $n / 1MB }
    }
}

<#
.SYNOPSIS
  Stops the sampler and returns peak and mean CPU and peak memory per container.
#>
function Stop-StatsSampler {
    param($Job, [string]$CsvPath)
    Stop-Job $Job | Out-Null
    Remove-Job $Job -Force | Out-Null
    $out = [ordered]@{}
    if (-not (Test-Path $CsvPath)) { return $out }
    $rows = Get-Content $CsvPath | ForEach-Object {
        $p = $_ -split ';'
        [pscustomobject]@{ Name = $p[1]; Cpu = [double]($p[2].TrimEnd('%')); Mem = (ConvertTo-MiB (($p[3] -split '/')[0].Trim())) }
    }
    foreach ($g in ($rows | Group-Object Name)) {
        $cpu = $g.Group | Measure-Object Cpu -Maximum -Average
        $mem = $g.Group | Measure-Object Mem -Maximum
        $out[$g.Name] = [ordered]@{ cpu_peak_pct = [math]::Round($cpu.Maximum, 1); cpu_avg_pct = [math]::Round($cpu.Average, 1); mem_peak_mib = [math]::Round($mem.Maximum) }
    }
    return $out
}

<#
.SYNOPSIS
  Runs a k6 script in a limited container on emtds-infra_default with the state directory as /data.
#>
function Invoke-K6 {
    param([string]$Script, [string[]]$EnvPairs)
    $envArgs = @()
    foreach ($e in $EnvPairs) { $envArgs += @('-e', $e) }
    $scripts = Join-Path $Script:TdsDir 'deploy\loadtest'
    docker run --rm --name "emtds-load-$Run" --label emtds.role=load --network emtds-infra_default `
        --cpus $GenCpus --memory 2g -v "${Script:StateDir}:/data" -v "${scripts}:/scripts:ro" @envArgs `
        $Script:K6Image run --quiet --no-color "/scripts/$Script"
}

<#
.SYNOPSIS
  Reads a k6 metric value from a summary, or 0 when the metric is absent.
#>
function Get-Metric {
    param($Summary, [string]$Name, [string]$Field = 'count')
    $m = $Summary.metrics.PSObject.Properties[$Name]
    if (-not $m) { return 0 }
    $v = $m.Value.values.PSObject.Properties[$Field]
    if (-not $v) { return 0 }
    return $v.Value
}

<#
.SYNOPSIS
  Returns a field of a report section, or $null when it is absent. Works on hashtables and parsed JSON.
#>
function Get-Field {
    param($Obj, [string]$Name)
    if ($null -eq $Obj) { return $null }
    if ($Obj -is [Collections.IDictionary]) { if ($Obj.Contains($Name)) { return $Obj[$Name] } return $null }
    $p = $Obj.PSObject.Properties[$Name]
    if ($p) { return $p.Value }
    return $null
}

<#
.SYNOPSIS
  Formats a section as "key=value, key=value", or "n/a" when it is absent.
#>
function Format-Pairs {
    param($Obj)
    if ($null -eq $Obj) { return 'n/a' }
    $names = if ($Obj -is [Collections.IDictionary]) { @($Obj.Keys) } else { @($Obj.PSObject.Properties.Name) }
    return (($names | ForEach-Object { "$_=$(Get-Field $Obj $_)" }) -join ', ')
}

<#
.SYNOPSIS
  Describes the fault of a run for the Markdown summary.
#>
function Format-Chaos {
    param($Chaos)
    if ($null -eq $Chaos) { return 'none' }
    $text = "$(Get-Field $Chaos 'target') at $(Get-Field $Chaos 'at_s') s, for $(Get-Field $Chaos 'for_s') s; "
    $text += "started $(Get-Field $Chaos 'started_at'), ended $(Get-Field $Chaos 'ended_at'), took $(Get-Field $Chaos 'actual_s') s"
    $err = Get-Field $Chaos 'error'
    if ($err) { $text += "; ERROR: $err" }
    return $text
}

<#
.SYNOPSIS
  Saves the Markdown summary of a report as <Run>.report.md in the state directory.
  The table holds what was sent, answered, stored and lost, latency, CPU and memory,
  the fault, the queue peaks, landings_served and outbound_sent. A figure the run does not have reads n/a.
#>
function Write-ReportMd {
    param($Report)
    $r = $Report
    $lat = Get-Field $r 'latency_ms'
    $metrics = Get-Field $r 'engine_metrics'
    $peak = Get-Field $metrics 'peak'
    $settle = Get-Field $r 'settle'
    $stored = Get-Field $r 'stored_in_ch'
    if ($null -eq $stored) { $stored = Get-Field $r 'stored_conversions' }
    $lost = Get-Field $r 'lost_answered_not_stored'
    if ($null -eq $lost) { $lost = Get-Field $r 'lost_200_not_stored' }
    $res = Get-Field $r 'resources'
    $resText = 'n/a'
    if ($res) {
        $names = if ($res -is [Collections.IDictionary]) { @($res.Keys) } else { @($res.PSObject.Properties.Name) }
        $resText = ($names | ForEach-Object { $x = Get-Field $res $_; "$_ cpu peak $(Get-Field $x 'cpu_peak_pct')% avg $(Get-Field $x 'cpu_avg_pct')%, mem peak $(Get-Field $x 'mem_peak_mib') MiB" }) -join '; '
    }
    $rows = @(
        @('Plan', (Get-Field $r 'plan')),
        @('Sent', (Get-Field $r 'sent')),
        @('Answered', (Format-Pairs (Get-Field $r 'answered'))),
        @('Failed', (Format-Pairs (Get-Field $r 'failed'))),
        @('Stored', $stored),
        @('Lost (answered, not stored)', $lost),
        @('Latency ms p50 / p95 / p99 / max', "$(Get-Field $lat 'p50') / $(Get-Field $lat 'p95') / $(Get-Field $lat 'p99') / $(Get-Field $lat 'max')"),
        @('CPU and memory', $resText),
        @('Chaos', (Format-Chaos (Get-Field $r 'chaos'))),
        @('Peak events.queue_len / pending / input_len', "$(Get-Field $peak 'queue_len') / $(Get-Field $peak 'pending') / $(Get-Field $peak 'input_len')"),
        @('Peak events.spool.segments', (Get-Field $peak 'spool_segments')),
        @('Engine losses delta', (Format-Pairs (Get-Field $r 'engine_losses_delta'))),
        @('Settle wait', $(if ($settle) { "$(Get-Field $settle 'waited_s') s, pending left $(Get-Field $settle 'pending_left'), timed out $(Get-Field $settle 'timed_out')" } else { 'n/a' })),
        @('landings_served', (Get-Field $r 'landings_served')),
        @('outbound_sent', (Get-Field $r 'outbound_sent'))
    )
    $md = @("# Run $Run", '', '| Metric | Value |', '|---|---|')
    foreach ($row in $rows) {
        $v = if ($null -eq $row[1] -or "$($row[1])" -eq '') { 'n/a' } else { "$($row[1])" -replace '\|', '/' }
        $md += "| $($row[0]) | $v |"
    }
    $md | Set-Content -Encoding utf8 -Path (Join-Path $Script:StateDir "$Run.report.md")
}

<#
.SYNOPSIS
  Prints a report and saves it as <Run>.report.json and <Run>.report.md in the state directory.
#>
function Write-Report {
    param($Report)
    $json = $Report | ConvertTo-Json -Depth 6
    $json | Set-Content -Encoding utf8 -Path (Join-Path $Script:StateDir "$Run.report.json")
    Write-ReportMd $Report
    $json
}

<#
.SYNOPSIS
  Runs one k6 script with the fault job, the docker stats sampler and the engine metrics sampler around it.
  Returns what the run measured: wall time, the fault log, the settle wait, the engine metrics
  before and after with the queue peaks, and the CPU and memory per container.
  The finally block always stops the jobs and unpauses the chaos container.
#>
function Invoke-Loaded {
    param([string]$K6Script, [string[]]$EnvPairs)
    $csv = Join-Path $Script:StateDir "$Run.stats.csv"
    $samples = Join-Path $Script:StateDir "$Run.metrics.jsonl"
    $chaosLog = Join-Path $Script:StateDir "$Run.chaos.json"
    $stats = $null; $sampler = $null; $chaosJob = $null
    $mBefore = Get-EngineMetrics
    try {
        $stats = Start-StatsSampler -CsvPath $csv
        $sampler = Start-MetricsSampler -Path $samples
        $chaosJob = Start-Chaos -LogPath $chaosLog
        $sw = [Diagnostics.Stopwatch]::StartNew()
        Invoke-K6 -Script $K6Script -EnvPairs $EnvPairs | Out-Host
        $sw.Stop()
        $chaos = Stop-Chaos -Job $chaosJob -LogPath $chaosLog
        $chaosJob = $null
        Resume-Chaos
        $settle = Wait-EngineSettled
        $res = Stop-StatsSampler -Job $stats -CsvPath $csv
        $stats = $null
        Stop-Job $sampler -ErrorAction SilentlyContinue
        Remove-Job $sampler -Force -ErrorAction SilentlyContinue
        $sampler = $null
    }
    finally {
        foreach ($j in @($chaosJob, $stats, $sampler)) {
            if ($j) { Stop-Job $j -ErrorAction SilentlyContinue; Remove-Job $j -Force -ErrorAction SilentlyContinue }
        }
        Resume-Chaos
    }
    $mAfter = Get-EngineMetrics
    $restarted = $mBefore -and $mAfter -and ((Get-Field $mBefore 'started_at') -ne (Get-Field $mAfter 'started_at'))
    return [ordered]@{
        wall_s = [math]::Round($sw.Elapsed.TotalSeconds); chaos = $chaos; settle = $settle; resources = $res
        engine_metrics = [ordered]@{ before = $mBefore; after = $mAfter; engine_restarted = [bool]$restarted; peak = (Get-MetricsPeaks -Path $samples) }
    }
}

<#
.SYNOPSIS
  Returns the growth of the engine loss counters between two /healthz readings.
  After an engine restart the counters start over, so the growth is unknown and $null is returned.
#>
function Get-LossDelta {
    param($Before, $After, [bool]$Restarted)
    if ($Restarted) { return $null }
    $d = [ordered]@{ full = $After.full - $Before.full; queue = $After.queue - $Before.queue; insert = $After.insert - $Before.insert; late = $After.late - $Before.late }
    if (@($d.Values | Where-Object { $_ -lt 0 }).Count -gt 0) { return $null }
    return $d
}

<#
.SYNOPSIS
  Copies groups.json out of the engine, samples real clicks and builds the k6 inputs.
#>
function Invoke-Prepare {
    docker cp emtds-engine:/var/lib/emtds/groups.json (Join-Path $Script:StateDir 'groups.json') | Out-Null
    $tsv = Invoke-Ch -Format 'TSVRaw' -Query "SELECT group_id, ip, replaceAll(useragent, '\t', ' '), country FROM events WHERE ts > now() - INTERVAL 30 DAY AND ip != '' AND useragent != '' AND NOT startsWith(sub_id_12, 'lt-') ORDER BY rand() LIMIT 40000"
    $tsv | Set-Content -Encoding utf8 -Path (Join-Path $Script:StateDir 'sample.tsv')
    node (Join-Path $Script:TdsDir 'deploy\loadtest\targets.mjs') (Join-Path $Script:StateDir 'groups.json') (Join-Path $Script:StateDir 'sample.tsv') $Script:StateDir
    if ($LASTEXITCODE -ne 0) { throw 'targets.mjs failed' }
}

<#
.SYNOPSIS
  Runs one click load, with the fault of -ChaosTarget when it is set, and reports sent,
  answered by class, stored, lost, latency, resources, the fault and the queue peaks.
#>
function Invoke-Clicks {
    Assert-RunLabel $Run
    if (-not $Stages -and $Rate -le 0) { throw 'set -Rate or -Stages' }
    if ($Action -eq 'chaos' -and $ChaosTarget -eq 'none') { throw 'set -ChaosTarget clickhouse, redis or engine' }
    $before = Get-EngineLosses
    $outBefore = [int64](Invoke-Ch "SELECT count() FROM postback_log WHERE direction = 'out'")
    $envs = @("RUN=$Run", "DURATION=$Duration")
    if ($Stages) { $envs += "STAGES=$Stages" } else { $envs += "RATE=$Rate" }
    $m = Invoke-Loaded -K6Script 'clicks.js' -EnvPairs $envs
    $after = Get-EngineLosses
    $outSent = [int64](Invoke-Ch "SELECT count() FROM postback_log WHERE direction = 'out'") - $outBefore
    $sum = Get-Content -Raw (Join-Path $Script:StateDir "$Run.summary.json") | ConvertFrom-Json
    $stored = [int64](Invoke-Ch "SELECT uniqExact(sub_id_12) FROM events WHERE ts > now() - INTERVAL 1 DAY AND startsWith(sub_id_12, 'lt-$Run-')")
    $landings = [int64](Invoke-Ch "SELECT count() FROM events WHERE ts > now() - INTERVAL 1 DAY AND startsWith(sub_id_12, 'lt-$Run-') AND redirect = 'landing'")
    if ($landings -gt 0) { Write-Warning "$landings clicks of run $Run were served a landing: rebuild targets with prepare" }
    $c2 = Get-Metric $sum 'clicks_2xx'; $c3 = Get-Metric $sum 'clicks_3xx'; $c4 = Get-Metric $sum 'clicks_4xx'
    $c5 = Get-Metric $sum 'clicks_5xx'; $c0 = Get-Metric $sum 'clicks_neterr'
    $answered = $c2 + $c3 + $c4
    $sent = $answered + $c5 + $c0
    Write-Report ([ordered]@{
            run = $Run; plan = $(if ($Stages) { $Stages } else { "$Rate/s x $Duration" }); wall_s = $m.wall_s
            sent = $sent; not_sent_by_k6 = (Get-Metric $sum 'dropped_iterations')
            answered = [ordered]@{ '2xx' = $c2; '3xx' = $c3; '4xx' = $c4 }; failed = [ordered]@{ '5xx' = $c5; no_answer = $c0 }
            stored_in_ch = $stored; landings_served = $landings; outbound_sent = $outSent
            lost_answered_not_stored = [math]::Max(0, $answered - $stored)
            latency_ms = [ordered]@{ p50 = [math]::Round((Get-Metric $sum 'click_ms' 'med'), 1); p95 = [math]::Round((Get-Metric $sum 'click_ms' 'p(95)'), 1); p99 = [math]::Round((Get-Metric $sum 'click_ms' 'p(99)'), 1); max = [math]::Round((Get-Metric $sum 'click_ms' 'max'), 1) }
            engine_losses_delta = (Get-LossDelta $before $after $m.engine_metrics.engine_restarted)
            chaos = $m.chaos; settle = $m.settle; engine_metrics = $m.engine_metrics
            resources = $m.resources
        })
}

<#
.SYNOPSIS
  Sends -Count postbacks over quiet-campaign clicks of -FromRun and reports what was stored.
#>
function Invoke-Postbacks {
    Assert-RunLabel $Run
    Assert-RunLabel $FromRun
    if ($Rate -le 0) { throw 'set -Rate' }
    $quiet = Get-Content -Raw (Join-Path $Script:StateDir 'quiet_groups.json') | ConvertFrom-Json
    $ids = ($quiet | ForEach-Object { "'" + ($_ -replace "'", "") + "'" }) -join ','
    $cids = @(Invoke-Ch "SELECT cid FROM events WHERE ts > now() - INTERVAL 1 DAY AND startsWith(sub_id_12, 'lt-$FromRun-') AND group_id IN ($ids) AND cid != '' LIMIT $Count")
    if ($cids.Count -lt $Count) { throw "only $($cids.Count) quiet clicks in run $FromRun, need $Count" }
    ConvertTo-Json -InputObject $cids -Compress | Set-Content -Encoding ascii -Path (Join-Path $Script:StateDir "$Run.cids.json")
    $before = [int64](Invoke-Ch "SELECT count() FROM postback_log WHERE direction = 'out'")
    $m = Invoke-Loaded -K6Script 'postbacks.js' -EnvPairs @("RUN=$Run", "RATE=$Rate", "DURATION=$Duration", "DATA=/data/$Run.cids.json")
    $sum = Get-Content -Raw (Join-Path $Script:StateDir "$Run.summary.json") | ConvertFrom-Json
    $stored = [int64](Invoke-Ch "SELECT uniqExact(tid) FROM conversions WHERE startsWith(tid, 'lt-$Run-')")
    $logged = [int64](Invoke-Ch "SELECT count() FROM postback_log WHERE direction = 'in' AND ts > now() - INTERVAL 1 HOUR AND subid IN (SELECT cid FROM events WHERE ts > now() - INTERVAL 1 DAY AND startsWith(sub_id_12, 'lt-$FromRun-'))")
    $out = [int64](Invoke-Ch "SELECT count() FROM postback_log WHERE direction = 'out'") - $before
    $ok = Get-Metric $sum 'pb_200'
    Write-Report ([ordered]@{
            run = $Run; from_run = $FromRun; plan = "$Count at $Rate/s"; wall_s = $m.wall_s
            sent = $ok + (Get-Metric $sum 'pb_404') + (Get-Metric $sum 'pb_4xx_other') + (Get-Metric $sum 'pb_5xx') + (Get-Metric $sum 'pb_neterr')
            answered = [ordered]@{ '200' = $ok; '404' = (Get-Metric $sum 'pb_404'); '4xx_other' = (Get-Metric $sum 'pb_4xx_other') }
            failed = [ordered]@{ '5xx' = (Get-Metric $sum 'pb_5xx'); no_answer = (Get-Metric $sum 'pb_neterr') }
            stored_conversions = $stored; lost_200_not_stored = [math]::Max(0, $ok - $stored)
            postback_log_in = $logged; outbound_sent = $out
            latency_ms = [ordered]@{ p50 = [math]::Round((Get-Metric $sum 'pb_ms' 'med'), 1); p95 = [math]::Round((Get-Metric $sum 'pb_ms' 'p(95)'), 1); p99 = [math]::Round((Get-Metric $sum 'pb_ms' 'p(99)'), 1); max = [math]::Round((Get-Metric $sum 'pb_ms' 'max'), 1) }
            chaos = $m.chaos; settle = $m.settle; engine_metrics = $m.engine_metrics
            resources = $m.resources
        })
}

<#
.SYNOPSIS
  Deletes every load-test row and the Redis uniqueness keys of the test addresses.
#>
function Invoke-Cleanup {
    $before = Invoke-Ch "SELECT count() FROM events WHERE ts > now() - INTERVAL 2 DAY AND startsWith(sub_id_12, 'lt-')"
    Invoke-Ch "DELETE FROM postback_log WHERE ts > now() - INTERVAL 2 DAY AND subid IN (SELECT cid FROM events WHERE ts > now() - INTERVAL 2 DAY AND startsWith(sub_id_12, 'lt-'))" | Out-Null
    Invoke-Ch "DELETE FROM conversions WHERE startsWith(tid, 'lt-')" | Out-Null
    Invoke-Ch "DELETE FROM events WHERE ts > now() - INTERVAL 2 DAY AND startsWith(sub_id_12, 'lt-')" | Out-Null
    $after = Invoke-Ch "SELECT count() FROM events WHERE ts > now() - INTERVAL 2 DAY AND startsWith(sub_id_12, 'lt-')"
    $targets = Get-Content -Raw (Join-Path $Script:StateDir 'targets.json') | ConvertFrom-Json
    $ips = [Collections.Generic.HashSet[string]]::new([string[]]@($targets | ForEach-Object { $_.ip }))
    $keys = @('uniq*:*', 'st:*' | ForEach-Object { docker exec emtds-infra-redis-1 redis-cli --scan --pattern $_ }) |
        Where-Object { $_ -and $ips.Contains(($_ -split ':', 3)[2]) }
    for ($i = 0; $i -lt $keys.Count; $i += 500) {
        $chunk = $keys[$i..([math]::Min($i + 499, $keys.Count - 1))]
        docker exec emtds-infra-redis-1 redis-cli unlink @chunk | Out-Null
    }
    [ordered]@{ events_lt_before = [int64]$before; events_lt_after = [int64]$after; redis_keys_removed = $keys.Count } | ConvertTo-Json
}

New-Item -ItemType Directory -Force -Path $Script:StateDir | Out-Null
switch ($Action) {
    'prepare' { Invoke-Prepare }
    'engine-load' { Reset-Engine -Override (Write-LoadOverride) }
    'engine-normal' {
        Reset-Engine
        Remove-Item -ErrorAction SilentlyContinue -Path (Join-Path $Script:StateDir 'compose.load.yml')
    }
    'engine-recreate' { Invoke-EngineRecreate }
    'clicks' { Invoke-Clicks }
    'chaos' { Invoke-Clicks }
    'postbacks' { Invoke-Postbacks }
    'cleanup' { Invoke-Cleanup }
    'job' { Get-KeitaroJob | Select-Object running, sync_running | ConvertTo-Json }
}
