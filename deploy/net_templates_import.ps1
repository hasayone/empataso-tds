<#
.SYNOPSIS
  Rebuilds internal/admin/net_templates.json from a Keitaro template export.

.DESCRIPTION
  Source is a JSON file of the shape {"templates":[{name, offer_param, params, instruction}]}.
  It stays outside the repository: the raw file holds foreign hosts and path tokens.
  The catalog keeps only what the tracker can use:
    - name: trimmed, never renamed;
    - offer_query: offer_param as it is;
    - postback_url: {origin}/postback? plus subid, status, payout, currency and tid of params,
      each with the macro of the network as it is. A value that is a comma list is a set of
      alternatives, not a macro, and is left out. Without a subid macro there is no postback_url.
      The foreign host, the path token and the "from" marker are never copied;
    - instruction: the text of the template as it is, outer whitespace trimmed;
    - conversions: every *_status value of params as it is, and "status" when it is a list.
  The catalog holds only the exported names, in the order of the export; there are no own presets.

.PARAMETER Source
  Path of the exported JSON.

.PARAMETER Target
  Catalog to write. Defaults to internal/admin/net_templates.json next to this script's tree.

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File deploy\net_templates_import.ps1 -Source D:\Downloads\keitaro_templates_333.json
#>
param(
    [Parameter(Mandatory = $true)][string]$Source,
    [string]$Target = ''
)

$ErrorActionPreference = 'Stop'
if ($Target -eq '') {
    $Target = Join-Path (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)) 'internal\admin\net_templates.json'
}
$utf8 = New-Object System.Text.UTF8Encoding($false)
$raw = [System.IO.File]::ReadAllText($Source, [System.Text.Encoding]::UTF8) | ConvertFrom-Json
if (-not $raw.templates) { throw 'no templates array in the source' }

# Order of the query parameters of the rewritten postback address.
$postbackKeys = @('subid', 'status', 'payout', 'currency', 'tid')

function Get-Param($params, [string]$key) {
    $prop = $params.PSObject.Properties[$key]
    if ($null -eq $prop) { return $null }
    return ([string]$prop.Value).Trim()
}

function New-Entry($tpl) {
    $entry = [ordered]@{ name = ([string]$tpl.name).Trim() }
    $query = ([string]$tpl.offer_param).Trim()
    if ($query -ne '') { $entry.offer_query = $query }
    $params = $tpl.params
    $pairs = @()
    if ($params) {
        $subid = Get-Param $params 'subid'
        if ($subid -and $subid -notmatch ',') {
            foreach ($key in $postbackKeys) {
                $value = Get-Param $params $key
                if ($value -and $value -notmatch ',' -and $value -notmatch 'https?:') { $pairs += ($key + '=' + $value) }
            }
        }
    }
    if ($pairs.Count -gt 0) { $entry.postback_url = '{origin}/postback?' + ($pairs -join '&') }
    $text = ([string]$tpl.instruction).Trim()
    if ($text -ne '') { $entry.instruction = $text }
    $conv = [ordered]@{}
    if ($params) {
        foreach ($prop in $params.PSObject.Properties) {
            $value = ([string]$prop.Value).Trim()
            if ($prop.Name -match '_status$' -and $value -ne '') { $conv[$prop.Name] = $value }
        }
        $status = Get-Param $params 'status'
        if ($status -and $status -match ',') { $conv['status'] = $status }
    }
    if ($conv.Count -gt 0) { $entry.conversions = $conv }
    if ($entry.Contains('offer_query') -or $entry.Contains('postback_url') -or $entry.Contains('instruction') -or $entry.Contains('conversions')) {
        $entry.source = 'keitaro-json'
    }
    return $entry
}

$entries = @($raw.templates | ForEach-Object { New-Entry $_ })
$names = @{}
foreach ($e in $entries) {
    $key = $e.name.ToLowerInvariant()
    if ($names.ContainsKey($key)) { throw ('duplicate name: ' + $e.name) }
    $names[$key] = $true
}
$lines = @()
foreach ($e in $entries) { $lines += (ConvertTo-Json $e -Compress -Depth 5) }
$json = "[`n" + ($lines -join ",`n") + "`n]`n"
$json = $json.Replace('\u0026', '&').Replace('\u0027', "'").Replace('\u003c', '<').Replace('\u003e', '>')
[System.IO.File]::WriteAllText($Target, $json, $utf8)
Write-Output ('templates written: ' + $lines.Count)
