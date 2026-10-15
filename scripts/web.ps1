# payprint: fetch the frontend bundle from the PayPrint-Frontend GitHub release
# and unpack it into the go:embed directory (server\internal\webui\dist).
#
# The frontend lives in its own repository (Sparkzure/PayPrint-Frontend). Its
# release workflow attaches the built SPA as payprint-web-<tag>.tar.gz (with a
# .sha256 sidecar). This script downloads the newest (or a pinned) release and
# unpacks it, so a backend checkout can produce the single self-contained
# binary without a Node toolchain.
#
# Already unpacked? build.ps1 skips this step unless -Force is used.
#
# ASCII-only on purpose (PowerShell 5.1 reads BOM-less UTF-8 as ANSI).
#
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File scripts\web.ps1
#        [-WebVersion v0.1.2] [-Out path] [-Force]

param(
    # Release tag to fetch. Defaults to $env:WEB_VERSION, then "latest".
    [string]$WebVersion = '',
    # Unpack destination. Defaults to server\internal\webui\dist.
    [string]$Out = '',
    # Re-download even if the destination already has index.html.
    [switch]$Force
)

$ErrorActionPreference = 'Stop'

$repo = 'Sparkzure/PayPrint-Frontend'
$assetPrefix = 'payprint-web'

$root = Split-Path -Parent $PSScriptRoot
if (-not $Out) { $Out = Join-Path $root 'server\internal\webui\dist' }

$tag = $WebVersion
if (-not $tag) { $tag = $env:WEB_VERSION }
if (-not $tag) { $tag = 'latest' }

if (-not $Force -and (Test-Path (Join-Path $Out 'index.html'))) {
    Write-Host "[web] $Out\index.html already present, skip download (use -Force to refresh)"
    exit 0
}

if ($tag -eq 'latest') {
    $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest"
    $tag = $rel.tag_name
    if (-not $tag) { throw "cannot resolve the latest $repo release" }
}

$asset = "$assetPrefix-$tag.tar.gz"
$url = "https://github.com/$repo/releases/download/$tag/$asset"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("payprint-web-" + [IO.Path]::GetRandomFileName() + ".tar.gz")

Write-Host "[web] downloading $url"
Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing

# Verify against the sidecar checksum when the release provides one.
try {
    $shaUrl = "$url.sha256"
    $shaFile = "$tmp.sha256"
    Invoke-WebRequest -Uri $shaUrl -OutFile $shaFile -UseBasicParsing
    $expected = (Get-Content $shaFile -Raw).Trim().Split(' ')[0].Trim()
    $actual = (Get-FileHash -Path $tmp -Algorithm SHA256).Hash.ToLower()
    if ($actual -ne $expected) { throw "checksum mismatch for $asset" }
    Write-Host '[web] sha256 ok'
} catch [System.Net.WebException] {
    Write-Host '[web] no checksum sidecar, skipping verification'
}

Write-Host "[web] unpacking into $Out"
New-Item -ItemType Directory -Force -Path $Out | Out-Null
# Drop the previous bundle but keep dot-files: the .gitkeep placeholder is what
# makes "go build" work in a checkout where the frontend was never fetched.
Get-ChildItem -Force -Path $Out | Where-Object { $_.Name -notlike '.*' } |
    ForEach-Object { Remove-Item -Recurse -Force -Path $_.FullName }
tar -xzf $tmp -C $Out
Remove-Item -Force $tmp -ErrorAction SilentlyContinue

if (-not (Test-Path (Join-Path $Out 'index.html'))) {
    throw "$asset does not contain index.html"
}
Write-Host "[web] done ($tag)"
