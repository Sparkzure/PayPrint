# payprint: end-to-end smoke test driver (Windows / PowerShell).
#
# Builds the single binary, writes a throw-away config file, starts the server on a
# separate port, runs the Node signing smoke test against it, then stops the server.
# The config points at a throw-away data directory so nothing touches the real one.
#
# Requires Go 1.27+, Node 24+ (the smoke script imports .ts directly) and the
# PayPrint-Frontend checkout (sibling directory by default, or -WebDir / the
# SMOKE_WEB_DIR environment variable).
#
# This file is intentionally ASCII-only: PowerShell 5.1 reads BOM-less UTF-8 as
# ANSI, and non-ASCII characters would break parsing.
#
# Usage:  powershell -NoProfile -ExecutionPolicy Bypass -File scripts\smoke.ps1
#         powershell ... -File scripts\smoke.ps1 -Port 18081 -KeepData

param(
    [int]$Port = 18080,
    [switch]$KeepData,
    # Path to the PayPrint-Frontend checkout (holds scripts\smoke.mjs).
    # Defaults to $env:SMOKE_WEB_DIR, then a sibling PayPrint-Frontend directory.
    [string]$WebDir = ''
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot '_env.ps1')

$runDir = Join-Path $env:TEMP 'payprint-smoke'
if (-not $KeepData) {
    Remove-Item -Recurse -Force $runDir -ErrorAction SilentlyContinue
}
New-Item -ItemType Directory -Force -Path $runDir | Out-Null

$exe = Join-Path $root 'dist\payprint-server.exe'

Write-Host '[smoke] building the single binary (-NoWeb: the smoke test drives the API and IPP only) ...'
# Nested process: build.ps1 exits non-zero on failure and must not kill this script.
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'build.ps1') -Out $exe -NoWeb
if ($LASTEXITCODE -ne 0) {
    Write-Host '[smoke] server build FAILED'
    exit 1
}

# The end-to-end script lives in the frontend repo (it imports the real signing
# implementation). Lookup order: -WebDir -> SMOKE_WEB_DIR -> sibling checkout.
$webDir = $WebDir
if (-not $webDir) { $webDir = $env:SMOKE_WEB_DIR }
if (-not $webDir) {
    $sibling = Join-Path (Split-Path -Parent $root) 'PayPrint-Frontend'
    if (Test-Path (Join-Path $sibling 'scripts\smoke.mjs')) { $webDir = $sibling }
    $inTree = Join-Path $root 'PayPrint-Frontend'
    if (-not $webDir -and (Test-Path (Join-Path $inTree 'scripts\smoke.mjs'))) { $webDir = $inTree }
}
if (-not $webDir -or -not (Test-Path (Join-Path $webDir 'scripts\smoke.mjs'))) {
    Write-Host '[smoke] PayPrint-Frontend checkout not found.'
    Write-Host '        Clone it next to this repository or pass -WebDir / set SMOKE_WEB_DIR.'
    exit 1
}

# Config file instead of environment variables: the binary takes -config, and the
# file is the only source of process-level settings. throw-away data dir; the worker
# IS enabled here (with a short interval) so that the smoke test can exercise the
# real print path: the Node script starts a fake IPP printer, registers it through
# the admin API, and then waits for the order to reach "completed" -- which only
# happens if IPP submission and job-state polling both work.
#
# Runtime settings (IPP poll interval etc.) are NOT in this file: the fast poll
# interval is pushed through the settings API once the server is up.
$conf = Join-Path $runDir 'payprint.conf'
@(
    '[server]'
    'env = test'
    "http_addr = 127.0.0.1:$Port"
    'log_level = warn'
    ''
    '[storage]'
    "db_path = $(Join-Path $runDir 'smoke.db')"
    "storage_dir = $(Join-Path $runDir 'files')"
    ''
    '[security]'
    'admin_token = smoke-admin-token'
    ''
    '[worker]'
    'enabled = true'
    'interval = 500ms'
) | Set-Content -Path $conf -Encoding ASCII

$stdout = Join-Path $runDir 'server.out.log'
$stderr = Join-Path $runDir 'server.err.log'

Write-Host "[smoke] starting server on 127.0.0.1:$Port"
$proc = Start-Process -FilePath $exe -ArgumentList "-config `"$conf`"" -PassThru -NoNewWindow `
    -RedirectStandardOutput $stdout -RedirectStandardError $stderr

$exitCode = 1
try {
    $ready = $false
    for ($i = 0; $i -lt 60; $i++) {
        try {
            Invoke-WebRequest -Uri "http://127.0.0.1:$Port/healthz" -UseBasicParsing -TimeoutSec 2 | Out-Null
            $ready = $true
            break
        } catch {
            Start-Sleep -Milliseconds 400
        }
    }

    if (-not $ready) {
        Write-Host '[smoke] server did not become ready'
        Get-Content $stderr -ErrorAction SilentlyContinue | Select-Object -Last 20
        exit 1
    }

    # Speed up job-state polling via the runtime settings API (min is 2s).
    # This also exercises "panel settings take effect without a restart".
    $settingsBody = '{"set":{"ipp_poll_interval":"2s"}}'
    Invoke-RestMethod -Method Put -Uri "http://127.0.0.1:$Port/api/v1/admin/settings" `
        -Headers @{ 'X-Admin-Token' = 'smoke-admin-token' } `
        -ContentType 'application/json' -Body $settingsBody | Out-Null

    $env:SMOKE_BASE = "http://127.0.0.1:$Port"
    $env:SMOKE_ADMIN_TOKEN = 'smoke-admin-token'
    Push-Location $webDir
    node scripts/smoke.mjs
    $exitCode = $LASTEXITCODE
    Pop-Location

    if ($exitCode -ne 0) {
        Write-Host ''
        Write-Host '[smoke] --- server log (last 30 lines) ---'
        Get-Content $stdout -ErrorAction SilentlyContinue | Select-Object -Last 30
    }
} finally {
    if ($proc -and -not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force
    }
}

exit $exitCode
