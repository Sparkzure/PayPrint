# Boot check: the service must come up even when CUPS is completely absent.
#
# Why this is its own script and not part of smoke.ps1: the smoke test starts the
# server *with* a working fake IPP printer, so it can never catch a startup path
# that assumes the print gateway is reachable. Here there is no printer at all
# and the printer address points at a dead port on purpose.
#
# What it pins down:
#   1. the delivery promise: drop the binary alone into an EMPTY directory, run
#      it from a different working directory, and payprint.conf + data/ appear
#      next to the binary. Nothing is written into the working directory, and
#      nothing from the source tree is needed at runtime
#   2. the process starts and /healthz answers (DB migrated, API serving)
#   3. the seeded default printer is marked offline, not "online" (we never
#      talked to CUPS, so claiming otherwise would be lying to the UI)
#   4. probing a dead address returns reachable:false plus a reason, not a 5xx
#   5. a runtime setting can be changed in place and /meta reflects it at once
#
# Keep this file pure ASCII: PowerShell 5.1 reads a BOM-less UTF-8 file as GBK
# and non-ASCII text breaks parsing (see docs/DEVELOPMENT.md).
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot '_env.ps1')

$bin = Join-Path $root 'dist\payprint-server.exe'
$dataDir = Join-Path $env:TEMP 'payprint-boot-check'
$port = 18099
$base = "http://127.0.0.1:$port"

if (Test-Path $dataDir) { Remove-Item $dataDir -Recurse -Force }
New-Item -ItemType Directory -Path $dataDir | Out-Null

Write-Host '[boot] building the single binary (-NoWeb: boot check needs no frontend bundle) ...'
& powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'build.ps1') -Out $bin -NoWeb
if ($LASTEXITCODE -ne 0) { throw 'build failed' }

$failed = 0
function Check($name, $ok, $detail) {
  if ($ok) { Write-Host "  ok   $name" }
  else { Write-Host "  FAIL $name - $detail"; $script:failed++ }
}

# ---------------------------------------------------------------------------
# 1. The delivery promise. A deployment directory holds ONE file -- the binary.
#    Started from a different working directory, it must grow payprint.conf and
#    data/ right next to itself and write nothing into that working directory.
#    A missing config file must never be a startup error.
#
# The generated file holds the built-in defaults (port :8080), so binding may
# fail here if something already listens on 8080 -- that is fine: the config and
# the DB are created before the listener ever comes up, and this step only
# asserts on the files. The process is killed either way.
# ---------------------------------------------------------------------------
$genDir = Join-Path $dataDir 'release'      # stands in for /opt/payprint on a device
$cwdDir = Join-Path $dataDir 'elsewhere'    # a different working directory on purpose
New-Item -ItemType Directory -Path $genDir | Out-Null
New-Item -ItemType Directory -Path $cwdDir | Out-Null
Copy-Item $bin (Join-Path $genDir 'payprint-server.exe')

# No -config on purpose: the default location must be derived from the binary.
# The child gets a private TEMP: config.DefaultPath() treats any directory under
# the process' temp dir as an ephemeral "go run" build dir, and the isolated
# .tools toolchain redirects TEMP -- without this, our fake deployment dir (which
# lives under $env:TEMP) would be misclassified and the config would land in the
# working directory instead of next to the binary.
$childTmp = Join-Path $dataDir 'childtmp'
New-Item -ItemType Directory -Path $childTmp | Out-Null
$oldTemp = $env:TEMP
$oldTmp = $env:TMP
$env:TEMP = $childTmp
$env:TMP = $childTmp
$genProc = Start-Process -FilePath (Join-Path $genDir 'payprint-server.exe') `
  -WorkingDirectory $cwdDir -PassThru -NoNewWindow `
  -RedirectStandardOutput (Join-Path $dataDir 'gen.out.log') `
  -RedirectStandardError (Join-Path $dataDir 'gen.err.log')
$env:TEMP = $oldTemp
$env:TMP = $oldTmp
Start-Sleep -Milliseconds 1500
if (-not $genProc.HasExited) { $genProc.Kill(); $genProc.WaitForExit() }

$genConf = Join-Path $genDir 'payprint.conf'
Check 'first run writes payprint.conf next to the binary' (Test-Path $genConf) $genConf
Check 'the working directory stays untouched (nothing lands in the source tree)' `
  (-not (Test-Path (Join-Path $cwdDir 'payprint.conf'))) $cwdDir

$genText = ''
if (Test-Path $genConf) { $genText = Get-Content $genConf -Raw }
# Every section the loader knows about must be in the generated file: it is both the
# documentation users read and the only definition of the defaults. Renaming a section
# in config.go without regenerating this expectation is exactly the drift this catches.
$missing = @('\[server\]', '\[storage\]', '\[security\]', '\[printer\]', '\[worker\]', '\[payment\]', 'admin_token') |
  Where-Object { $genText -notmatch $_ }
Check 'the generated file is the documented template' `
  ($genText -and $missing.Count -eq 0) "generated config is missing: $($missing -join ', ')"

# Relative paths inside the config resolve against the config file's own
# directory -- also not the working directory -- which is what makes the
# deployment directory a self-contained, relocatable unit.
Check 'data/ is created next to the config file too' `
  (Test-Path (Join-Path $genDir 'data')) (Join-Path $genDir 'data')

# ---------------------------------------------------------------------------
# 2. The real run: a brand new DB, and a printer address pointing at a port that
#    nothing listens on.
# ---------------------------------------------------------------------------
$conf = Join-Path $dataDir 'payprint.conf'
@(
  '[server]'
  'env = boot-check'
  "http_addr = :$port"
  'log_level = info'
  ''
  '[storage]'
  "db_path = $(Join-Path $dataDir 'boot.db')"
  "storage_dir = $(Join-Path $dataDir 'files')"
  ''
  '[security]'
  'admin_token = boot-check-token'
  ''
  '[printer]'
  'default_printer_uri = ipp://127.0.0.1:9/ipp/print'
) | Set-Content -Path $conf -Encoding ASCII

$outLog = Join-Path $dataDir 'server.out.log'
$errLog = Join-Path $dataDir 'server.err.log'
$proc = Start-Process -FilePath $bin -ArgumentList "-config `"$conf`"" `
  -RedirectStandardOutput $outLog -RedirectStandardError $errLog -PassThru

try {
  $health = $null
  for ($i = 0; $i -lt 25; $i++) {
    Start-Sleep -Milliseconds 400
    if ($proc.HasExited) { break }
    try {
      $health = Invoke-RestMethod -Uri "$base/healthz" -TimeoutSec 2
      if ($health.data.status -eq 'ok') { break }
    } catch { }
  }

  if ($null -eq $health -or $health.data.status -ne 'ok') {
    Write-Host '[boot] server did not come up; last log lines:'
    if (Test-Path $outLog) { Get-Content $outLog | Select-Object -Last 20 }
    if (Test-Path $errLog) { Get-Content $errLog | Select-Object -Last 20 }
    exit 1
  }
  Check 'starts with no CUPS and serves /healthz' $true ''
  Write-Host "       (database=$($health.data.database), migration=$($health.data.migration_version))"

  $headers = @{ 'X-Admin-Token' = 'boot-check-token' }

  $printers = Invoke-RestMethod -Uri "$base/api/v1/admin/printers" -Headers $headers
  $seeded = $printers.data | Where-Object { $_.code -eq 'P01' }
  Check 'seeded printer is offline, not pretending to be online' `
    ($seeded.status -eq 'offline') "status=$($seeded.status)"

  $probe = Invoke-RestMethod -Uri "$base/api/v1/admin/printers/probe" -Method Post `
    -Headers $headers -ContentType 'application/json' `
    -Body '{"cups_uri":"ipp://127.0.0.1:9/ipp/print"}'
  Check 'probing a dead address reports unreachable with a reason (not 5xx)' `
    ($probe.data.reachable -eq $false -and $probe.data.error) `
    ($probe.data | ConvertTo-Json -Compress)

  $set = Invoke-RestMethod -Uri "$base/api/v1/admin/settings" -Method Put `
    -Headers $headers -ContentType 'application/json' `
    -Body '{"set":{"release_ttl":"17m"}}'
  $ttl = $set.data | Where-Object { $_.key -eq 'release_ttl' }
  Check 'runtime setting can be changed in place' `
    ($ttl.value -eq '17m' -and $ttl.overridden -eq $true) "value=$($ttl.value)"

  $meta = Invoke-RestMethod -Uri "$base/api/v1/meta"
  Check 'the new value is live immediately (/meta)' `
    ([int]($meta.data.release_ttl_seconds / 60) -eq 17) `
    "release_ttl_seconds=$($meta.data.release_ttl_seconds)"
} finally {
  if (-not $proc.HasExited) { $proc.Kill(); $proc.WaitForExit() }
}

if ($failed -gt 0) { Write-Host "[boot] $failed check(s) failed"; exit 1 }
Write-Host '[boot] ALL CHECKS PASSED'
