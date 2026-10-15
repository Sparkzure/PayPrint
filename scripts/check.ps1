# payprint: run every local check (format, vet, unit+integration tests, then
# the single-binary build, which reuses or auto-downloads the frontend bundle).
#
# This is the "run before you commit" command; it mirrors .github/workflows/ci.yml.
#
# Requires Go 1.27+. The first build needs network access to fetch the frontend
# dist from the PayPrint-Frontend release (or point build.ps1 at a local -WebDist).
# The optional project-local toolchain in ..\.tools\ is picked up automatically
# when present (see _env.ps1).
#
# ASCII-only on purpose (PowerShell 5.1 reads BOM-less UTF-8 as ANSI).
#
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File scripts\check.ps1

$ErrorActionPreference = 'Continue'

$root = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot '_env.ps1')

$failed = @()

Write-Host ''
Write-Host '=== gofmt (server) ==='
Push-Location (Join-Path $root 'server')
$unformatted = & gofmt -l ./cmd ./internal
if ($unformatted) {
    Write-Host 'reformatting:'
    $unformatted | ForEach-Object { Write-Host "  $_" }
    & gofmt -w ./cmd ./internal
} else {
    Write-Host 'all files formatted'
}

Write-Host ''
Write-Host '=== go vet ==='
& go vet ./...
if ($LASTEXITCODE -ne 0) { $failed += 'go vet' }

Write-Host ''
Write-Host '=== go test ==='
& go test ./...
if ($LASTEXITCODE -ne 0) { $failed += 'go test' }
Pop-Location

Write-Host ''
Write-Host '=== single binary build (frontend embedded + go build) ==='
# Nested process on purpose: build.ps1 throws/exits on failure, and running it
# in-process would let that kill this whole script (and skip the summary below).
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'build.ps1') `
    -Out (Join-Path $root 'dist\payprint-server.exe')
if ($LASTEXITCODE -ne 0) { $failed += 'build.ps1' }

Write-Host ''
if ($failed.Count -gt 0) {
    Write-Host ("FAILED: " + ($failed -join ', '))
    exit 1
}
Write-Host 'ALL CHECKS PASSED'
exit 0
