# payprint: build the single deliverable file.
#
# Two steps, in order:
#   1. make sure the frontend bundle is in the go:embed directory
#      (server\internal\webui\dist): reuse what is there, or download the newest
#      PayPrint-Frontend release via web.ps1
#   2. go build -> ONE binary carrying the API, the worker and the whole frontend
#
# There is no runtime asset directory: the frontend is embedded, so the only
# thing to copy to a device is this one file (plus the config it writes on
# first start).
#
# Cross-compiling from any host works because modernc.org/sqlite is pure Go and
# CGO stays off:
#   scripts\build.ps1 -GOOS linux -GOARCH arm64 -Out dist\payprint-server-linux-arm64
#   scripts\build.ps1 -GOOS linux -GOARCH arm -GOARM 7 -Out dist\payprint-server-linux-armv7
#
# Requires Go 1.27+ only. No Node.js needed: the frontend comes prebuilt from
# its own repository's release. Building the frontend yourself is a frontend-repo
# concern (see PayPrint-Frontend); point -WebDist at its dist\ if you want a
# locally built bundle.
#
# The optional project-local toolchain in ..\.tools\ is picked up automatically
# when present (see _env.ps1), but nothing here depends on it.
#
# ASCII-only on purpose (PowerShell 5.1 reads BOM-less UTF-8 as ANSI).
#
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1

param(
    # Output path. Defaults to dist\payprint-server[.exe] for the target OS.
    [string]$Out = '',
    # Written into the startup log via -ldflags.
    [string]$Version = 'dev',
    # Git commit written into the startup log. Auto-detected from git when empty.
    [string]$GitCommit = '',
    # Build timestamp written into the startup log (UTC). Auto-generated when empty.
    [string]$BuiltAt = '',
    # Use this frontend dist instead of downloading a release.
    [string]$WebDist = '',
    # Re-download the frontend dist even if one is already embedded.
    [switch]$RefreshWeb,
    # Build without touching the frontend (whatever is in the embed dir gets in;
    # a fresh checkout builds a UI-less binary).
    [switch]$NoWeb,
    [string]$GOOS = '',
    [string]$GOARCH = '',
    [string]$GOARM = ''
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot '_env.ps1')

$embedDir = Join-Path $root 'server\internal\webui\dist'

# ---- 1. frontend bundle ----------------------------------------------------

if (-not $NoWeb) {
    if ($WebDist) {
        if (-not (Test-Path (Join-Path $WebDist 'index.html'))) {
            throw "frontend bundle not found: $WebDist\index.html"
        }
        Write-Host "[build] using local frontend dist: $WebDist"
        New-Item -ItemType Directory -Force -Path $embedDir | Out-Null
        Get-ChildItem -Force -Path $embedDir | Where-Object { $_.Name -notlike '.*' } |
            ForEach-Object { Remove-Item -Recurse -Force -Path $_.FullName }
        Copy-Item -Path (Join-Path $WebDist '*') -Destination $embedDir -Recurse -Force
    } elseif ($RefreshWeb -or -not (Test-Path (Join-Path $embedDir 'index.html'))) {
        $webArgs = @()
        if ($RefreshWeb) { $webArgs += '-Force' }
        & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'web.ps1') @webArgs
        if ($LASTEXITCODE -ne 0) { throw 'fetching the frontend dist failed' }
    } else {
        Write-Host '[build] reusing the embedded frontend dist (-RefreshWeb re-downloads)'
    }
} else {
    Write-Host '[build] -NoWeb: skipping the frontend bundle'
}

# ---- 2. go build -----------------------------------------------------------

$targetOS = $GOOS
if (-not $targetOS) {
    if ($env:OS -eq 'Windows_NT') { $targetOS = 'windows' } else { $targetOS = 'linux' }
}
if (-not $Out) {
    $name = if ($targetOS -eq 'windows') { 'payprint-server.exe' } else { 'payprint-server' }
    $Out = Join-Path $root "dist\$name"
}
$outDir = Split-Path -Parent $Out
if ($outDir) { New-Item -ItemType Directory -Force -Path $outDir | Out-Null }

if (-not $GitCommit) {
    $GitCommit = 'unknown'
    try {
        $GitCommit = (git -C $root rev-parse --short HEAD 2>$null)
        if (-not $GitCommit) { $GitCommit = 'unknown' }
    } catch { }
}
if (-not $BuiltAt) {
    $BuiltAt = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
}

Write-Host "[build] go build -> $Out"
Push-Location (Join-Path $root 'server')
try {
    $env:CGO_ENABLED = '0'
    if ($GOOS) { $env:GOOS = $GOOS }
    if ($GOARCH) { $env:GOARCH = $GOARCH }
    if ($GOARM) { $env:GOARM = $GOARM }
    & go build -trimpath `
        -ldflags "-s -w -X main.version=$Version -X main.gitCommit=$GitCommit -X main.builtAt=$BuiltAt" `
        -o $Out ./cmd/server
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
} finally {
    Pop-Location
}

$size = (Get-Item $Out).Length
Write-Host ("[build] OK: {0} ({1:N1} MB, {2}, version={3} commit={4})" -f $Out, ($size / 1MB), $targetOS, $Version, $GitCommit)
