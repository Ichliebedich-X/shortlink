<#
.SYNOPSIS
    Run one load test with hey, validate the numbers, and append the result to CSV.

.DESCRIPTION
    The server must already be running.

    This script only runs ONE scenario. For the "no cache" baseline,
    temporarily return errCacheMiss at the top of readCache, restart,
    run this script, then restore the code. The script does not edit source.

.EXAMPLE
    .\scripts\bench.ps1 -Scenario cache

.EXAMPLE
    .\scripts\bench.ps1 -Scenario nocache -Concurrency 100 -Duration 30s
#>
param(
    [string]$Scenario = 'cache',
    [string]$BaseUrl = 'http://127.0.0.1:8080',
    [string]$Code = '4',
    [string]$Duration = '30s',
    [int]$Concurrency = 100,
    [int]$ExpectedStatus = 302,
    [string]$OutFile = (Join-Path $PSScriptRoot 'bench-results.csv')
)

$ErrorActionPreference = 'Stop'

# ---------- 1. Locate hey ----------
$heyCmd = Get-Command hey.exe -ErrorAction SilentlyContinue
if (-not $heyCmd) {
    $heyCmd = Get-Command hey -ErrorAction SilentlyContinue
}

if ($heyCmd) {
    $hey = $heyCmd.Source
} else {
    $fallback = Join-Path $env:USERPROFILE 'go\bin\hey.exe'
    if (Test-Path $fallback) {
        $hey = $fallback
    } else {
        Write-Host "hey not found. Run: go install github.com/rakyll/hey@latest" -ForegroundColor Yellow
        exit 1
    }
}

# ---------- 2. Probe the server ----------
$url = "$($BaseUrl.TrimEnd('/'))/$Code"
$probe = curl.exe -s -o NUL -w "%{http_code}" $url

if ($probe -eq '000') {
    Write-Host "Cannot reach $url. Start the server first." -ForegroundColor Red
    exit 1
}

Write-Host "Scenario: $Scenario | code: $Code | concurrency: $Concurrency | duration: $Duration" -ForegroundColor Cyan
Write-Host "Warm-up status: $probe"

# ---------- 3. Run hey ----------
$raw = & $hey -disable-redirects -z $Duration -c $Concurrency $url 2>&1 | Out-String

# ---------- 4. Parse output ----------
function Get-Number([string]$text, [string]$pattern) {
    if ($text -match $pattern) {
        return [double]$Matches[1]
    }
    return $null
}

function Get-PercentileMs([string]$text, [string]$percent) {
    # hey prints a double percent sign: "50%% in 0.0011 secs"
    if ($text -match "(?m)^\s*$percent%%\s+in\s+([\d\.]+)\s+secs") {
        return [math]::Round([double]$Matches[1] * 1000, 2)
    }
    return $null
}

$qps         = Get-Number $raw 'Requests/sec:\s+([\d\.]+)'
$totalSec    = Get-Number $raw '(?m)^\s*Total:\s+([\d\.]+)\s+secs'
$avgSec      = Get-Number $raw '(?m)^\s*Average:\s+([\d\.]+)\s+secs'
$slowestSec  = Get-Number $raw '(?m)^\s*Slowest:\s+([\d\.]+)\s+secs'
$totalData   = Get-Number $raw 'Total data:\s+(\d+)\s+bytes'
$sizePerReq  = Get-Number $raw 'Size/request:\s+(\d+)\s+bytes'

$p50 = Get-PercentileMs $raw '50'
$p95 = Get-PercentileMs $raw '95'
$p99 = Get-PercentileMs $raw '99'

$statusMatches = [regex]::Matches($raw, '\[(\d{3})\]\s+(\d+)\s+responses')
$statusMap = @{}
$requests = 0
foreach ($m in $statusMatches) {
    $codeNum = [int]$m.Groups[1].Value
    $count = [int]$m.Groups[2].Value
    $statusMap[$codeNum] = $count
    $requests += $count
}
$statuses = ($statusMatches | ForEach-Object { "$($_.Groups[1].Value)=$($_.Groups[2].Value)" }) -join ' '

# ---------- 5. Validate the numbers ----------
$checks = @()
$valid = $true

if ($qps -and $totalSec -and $requests -gt 0) {
    $estimated = $qps * $totalSec
    $diff = [math]::Abs($estimated - $requests) / $requests
    $ok = $diff -lt 0.02
    $checks += "QPS*duration=$([math]::Round($estimated)) vs requests=$requests ($([math]::Round($diff * 100, 2))%)"
    if (-not $ok) { $valid = $false }
} else {
    $valid = $false
    $checks += 'QPS/duration/requests missing'
}

if ($totalData -and $sizePerReq -and $sizePerReq -gt 0 -and $requests -gt 0) {
    $estimated = $totalData / $sizePerReq
    $diff = [math]::Abs($estimated - $requests) / $requests
    $ok = $diff -lt 0.01
    $checks += "totalData/size=$([math]::Round($estimated)) vs requests=$requests ($([math]::Round($diff * 100, 2))%)"
    if (-not $ok) { $valid = $false }
} else {
    $checks += 'total data / size per request missing'
}

if ($qps -and $avgSec -and $qps -gt 0 -and $avgSec -gt 0) {
    $expectedMs = $Concurrency / $qps * 1000
    $actualMs = $avgSec * 1000
    $diff = [math]::Abs($expectedMs - $actualMs) / $actualMs
    $ok = $diff -lt 0.2
    $checks += "concurrency/QPS=$([math]::Round($expectedMs, 2))ms vs average=$([math]::Round($actualMs, 2))ms ($([math]::Round($diff * 100, 2))%)"
    if (-not $ok) { $valid = $false }
} else {
    $valid = $false
    $checks += 'QPS/average missing'
}

$statusOk = $statusMap.ContainsKey($ExpectedStatus) -and $statusMap[$ExpectedStatus] -eq $requests
$checks += "expectedStatus=$ExpectedStatus actual=[$statuses]"
if (-not $statusOk) { $valid = $false }

# ---------- 6. Append to CSV ----------
$row = [pscustomobject]@{
    Time            = (Get-Date).ToString('yyyy-MM-dd HH:mm:ss')
    Scenario        = $Scenario
    Code            = $Code
    Concurrency     = $Concurrency
    Duration        = $Duration
    QPS             = if ($qps) { [math]::Round($qps, 2) } else { $null }
    Avg_ms          = if ($avgSec) { [math]::Round($avgSec * 1000, 2) } else { $null }
    P50_ms          = $p50
    P95_ms          = $p95
    P99_ms          = $p99
    Slowest_ms      = if ($slowestSec) { [math]::Round($slowestSec * 1000, 2) } else { $null }
    Requests        = $requests
    SizePerRequest  = $sizePerReq
    StatusCodes     = $statuses
    Valid           = if ($valid) { 'yes' } else { 'no' }
    Notes           = ($checks -join '; ')
}

$row | Export-Csv -Path $OutFile -Append -NoTypeInformation -Encoding utf8

# ---------- 7. Print ----------
Write-Host ""
$row | Format-List
Write-Host "Saved to: $OutFile" -ForegroundColor Green

if ($valid) {
    Write-Host "Markdown row for README:" -ForegroundColor Cyan
    Write-Host ("| {0} | {1} | {2} | {3} ms | {4} ms |" -f $Scenario, $Concurrency, $row.QPS, $row.P50_ms, $row.P99_ms)
} else {
    Write-Host "INVALID data: one or more consistency checks failed." -ForegroundColor Red
    Write-Host "Raw hey output:" -ForegroundColor Yellow
    Write-Host $raw
}
