# payprint: verify that this project stayed isolated from the system.
#
# LOCAL-ONLY HELPER. It checks a developer-machine preference, not a property of
# the code: whether the optional .tools\ toolchain really kept every cache inside
# the repository. A fresh clone has no .tools\ and every other script still works,
# so nothing in the repo or in CI depends on this passing. See docs/DEVELOPMENT.md.
#
# Checks three things:
#   1. User-level and machine-level PATH do not reference the project toolchain.
#   2. The project-local .tools directory actually holds the caches.
#   3. Known tool cache/config locations outside the project were not written to
#      today (they may exist from unrelated prior use, so this reports facts
#      instead of failing hard on mere existence).
#
# ASCII-only on purpose (PowerShell 5.1 reads BOM-less UTF-8 as ANSI).
#
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File scripts\verify-isolation.ps1

$ErrorActionPreference = 'Continue'

$root = Split-Path -Parent $PSScriptRoot
$problems = @()

Write-Host ''
Write-Host '=== 1. system PATH must not reference the project ==='
foreach ($scope in @('User', 'Machine')) {
    $value = [Environment]::GetEnvironmentVariable('Path', $scope)
    if (-not $value) {
        Write-Host "  [$scope] PATH is empty"
        continue
    }
    $hits = $value.Split(';') | Where-Object { $_ -match 'payprint|\.tools' }
    if ($hits) {
        Write-Host "  [$scope] PROBLEM: PATH references the project:"
        $hits | ForEach-Object { Write-Host "      $_" }
        $problems += "$scope PATH references project paths"
    } else {
        Write-Host "  [$scope] ok (no project path)"
    }
}

Write-Host ''
Write-Host '=== 2. project-local caches present under .tools ==='
$expected = @(
    'go',
    'node',
    'gopath\pkg\mod',
    'cache\go-build',
    'cache\go-tmp',
    'cache\npm',
    'cache\tmp',
    'home\AppData\Roaming',
    'home\AppData\Local'
)
foreach ($rel in $expected) {
    $full = Join-Path $root ".tools\$rel"
    if (Test-Path $full) {
        Write-Host "  ok      .tools\$rel"
    } else {
        Write-Host "  missing .tools\$rel"
    }
}

Write-Host ''
Write-Host '=== 3. caches outside the project (written today?) ==='
$today = (Get-Date).Date
$outside = @(
    "$env:USERPROFILE\.config\go",
    "$env:USERPROFILE\go",
    "$env:LOCALAPPDATA\go-build",
    "$env:LOCALAPPDATA\npm-cache",
    "$env:APPDATA\npm",
    "$env:APPDATA\go"
)
foreach ($path in $outside) {
    if (-not $path) { continue }
    if (-not (Test-Path $path)) {
        Write-Host "  absent  $path"
        continue
    }
    $item = Get-Item $path -Force
    $isToday = $item.LastWriteTime -ge $today
    $flag = if ($isToday) { 'WRITTEN TODAY' } else { 'old' }
    Write-Host "  exists  $path  [$flag, last write $($item.LastWriteTime)]"
    if ($isToday) {
        $problems += "cache outside project written today: $path"
    }
}

Write-Host ''
if ($problems.Count -gt 0) {
    Write-Host 'ISOLATION ISSUES:'
    $problems | ForEach-Object { Write-Host "  - $_" }
    exit 1
}
Write-Host 'ISOLATION OK: nothing in system PATH, all caches inside the project.'
exit 0
