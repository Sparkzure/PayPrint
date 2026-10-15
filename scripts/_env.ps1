# payprint: activate the project-local toolchain, when it exists.
#
# .tools/ holds a self-contained Go + Node toolchain with every cache redirected
# inside the repository (see docs/DEVELOPMENT.md). It is a LOCAL convenience for
# developers who do not want caches in %APPDATA% / %LOCALAPPDATA% -- it is NOT a
# build requirement and it is never published (.gitignore drops it).
#
# When .tools/env.ps1 is missing -- the normal case for a fresh clone -- this
# file does nothing and the scripts fall back to whatever go / npm / node are on
# PATH. That is why every script can be run straight from a GitHub checkout.
#
# Dot-source this instead of .tools\env.ps1 directly:
#     . (Join-Path $PSScriptRoot '_env.ps1')
#
# ASCII-only on purpose (PowerShell 5.1 reads BOM-less UTF-8 as ANSI).

# Lookup order: a .tools next to the repo root, then one level up (the shared
# workspace layout where PayPrint and PayPrint-Frontend sit beside one .tools).
$repoRoot = Split-Path -Parent $PSScriptRoot
$localEnv = Join-Path $repoRoot '.tools\env.ps1'
if (-not (Test-Path $localEnv)) {
    $localEnv = Join-Path (Split-Path -Parent $repoRoot) '.tools\env.ps1'
}
if (Test-Path $localEnv) {
    . $localEnv | Out-Null
}
