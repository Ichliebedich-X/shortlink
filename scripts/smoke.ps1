<#
.SYNOPSIS
    Integration smoke test against a running shortlink server.

.DESCRIPTION
    Covers:
      - concurrent creation of the same long URL (codes must be distinct)
      - each created code redirects with 302
      - invalid origin returns 400
      - missing code returns 404

    The server must be running. This script creates rows in the dev database.

.EXAMPLE
    .\scripts\smoke.ps1
#>
param(
    [string]$BaseUrl = 'http://127.0.0.1:8080',
    [int]$Count = 20
)

$ErrorActionPreference = 'Stop'
$BaseUrl = $BaseUrl.TrimEnd('/')
$Origin = "https://example.com/smoke-$(Get-Date -Format 'yyyyMMddHHmmss')"

Write-Host "Base URL: $BaseUrl"
Write-Host "Origin:   $Origin"
Write-Host "Concurrent creates: $Count"

# ---------- 1. Concurrent creates with the same long URL ----------
$results = 1..$Count | ForEach-Object -Parallel {
    $body = @{ origin = $using:Origin } | ConvertTo-Json -Compress
    try {
        $r = Invoke-WebRequest -Uri "$using:BaseUrl/api/v1/links" `
            -Method Post `
            -ContentType 'application/json' `
            -Body $body `
            -SkipHttpErrorCheck `
            -TimeoutSec 10
        $json = $r.Content | ConvertFrom-Json
        [pscustomobject]@{
            Status = [int]$r.StatusCode
            Code   = [string]$json.data.code
        }
    } catch {
        [pscustomobject]@{
            Status = 0
            Code   = ''
        }
    }
} -ThrottleLimit $Count

$ok = @($results | Where-Object { $_.Status -eq 200 -and $_.Code -ne '' })
$codes = @($ok | Select-Object -ExpandProperty Code)
$distinct = @($codes | Sort-Object -Unique).Count

Write-Host "Created OK: $($ok.Count) / $Count"
Write-Host "Distinct codes: $distinct / $($ok.Count)"

$failed = $false
if ($ok.Count -ne $Count) {
    Write-Host "FAIL: not all concurrent creates returned 200 with a code." -ForegroundColor Red
    $failed = $true
}
if ($distinct -ne $ok.Count) {
    Write-Host "FAIL: duplicate codes detected." -ForegroundColor Red
    $failed = $true
}

# ---------- 2. Every created code should redirect ----------
foreach ($code in ($codes | Select-Object -First 5)) {
    $status = curl.exe -s -o NUL -w "%{http_code}" "$BaseUrl/$code"
    if ($status -ne '302') {
        Write-Host "FAIL: GET /$code returned $status, want 302" -ForegroundColor Red
        $failed = $true
    }
}

# ---------- 3. Invalid origin -> 400 ----------
$badBody = '{"origin":"not-a-url"}'
$bad = Invoke-WebRequest -Uri "$BaseUrl/api/v1/links" `
    -Method Post `
    -ContentType 'application/json' `
    -Body $badBody `
    -SkipHttpErrorCheck `
    -TimeoutSec 10

if ([int]$bad.StatusCode -ne 400) {
    Write-Host "FAIL: invalid origin returned $($bad.StatusCode), want 400" -ForegroundColor Red
    $failed = $true
}

# ---------- 4. Missing code -> 404 ----------
$missing = curl.exe -s -o NUL -w "%{http_code}" "$BaseUrl/ZZZZZZ"
if ($missing -ne '404') {
    Write-Host "FAIL: missing code returned $missing, want 404" -ForegroundColor Red
    $failed = $true
}

Write-Host ""
if ($failed) {
    Write-Host "SMOKE TEST FAILED" -ForegroundColor Red
    exit 1
}

Write-Host "SMOKE TEST PASSED" -ForegroundColor Green
