# vaultsync-notify one-line installer for Windows (#90).
#
#   irm https://vaultsync.eu/notify.ps1 | iex
#
# Skeptical of irm|iex? Quite right — set $env:VAULTSYNC_NOTIFY_DRYRUN = '1'
# first to see every action without changing anything, or read this file:
#   https://github.com/psimaker/vaultsync/blob/main/notify/scripts/install.ps1
#
# What it does, in order:
#   1. Finds Syncthing's config.xml (%LOCALAPPDATA%\Syncthing — the same
#      location the helper binary probes; $env:SYNCTHING_CONFIG overrides).
#   2. Downloads the latest vaultsync-notify_windows_amd64.exe release and
#      verifies it against the release's SHA256SUMS (Get-FileHash).
#   3. Registers a per-user Scheduled Task: runs hidden at logon, restarts on
#      failure — no service wrapper, no admin rights needed.
#   4. Starts the task and runs the helper's --doctor preflight.
#
# Re-running this installer upgrades the helper (stops the task, replaces the
# binary, prints old -> new, restarts) — same contract as install.sh (#87).
#
# The helper sends only your Syncthing Device ID to the relay — never file
# names, folder names, or content.
#
# Environment overrides (all optional):
#   SYNCTHING_CONFIG          path to config.xml when auto-detection misses it
#   RELAY_URL                 relay endpoint (default: production relay)
#   VAULTSYNC_NOTIFY_DRYRUN   set to 1 to preview without changing anything

[CmdletBinding()]
param(
    [switch]$DryRun
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = 'Stop'

if ($env:VAULTSYNC_NOTIFY_DRYRUN -eq '1') { $DryRun = $true }

$RelayUrl = if ($env:RELAY_URL) { $env:RELAY_URL } else { 'https://relay.vaultsync.eu' }
$Repo = 'psimaker/vaultsync'
$AssetName = 'vaultsync-notify_windows_amd64.exe'
$TaskName = 'VaultSync Notify Helper'
$InstallDir = Join-Path $env:LOCALAPPDATA 'VaultSync'
$ExePath = Join-Path $InstallDir 'vaultsync-notify.exe'
$RunnerPath = Join-Path $InstallDir 'run-vaultsync-notify.ps1'
$LogPath = Join-Path $InstallDir 'vaultsync-notify.log'

function Write-Info([string]$Message) { Write-Host $Message }
function Write-Note([string]$Message) { Write-Host "WARN: $Message" -ForegroundColor Yellow }
# throw, not exit: under the documented `irm ... | iex` invocation the script
# runs in the CALLER's session — `exit` would close the user's PowerShell
# window and take the error message with it.
function Fail([string]$Message) {
    Write-Host "ERROR: $Message" -ForegroundColor Red
    throw 'vaultsync-notify install aborted — see the ERROR above.'
}

Write-Info 'VaultSync Cloud Relay — Windows helper installer'
if ($DryRun) { Write-Info '(dry run — nothing will be changed)' }

# Windows PowerShell 5.1 defaults to TLS 1.0 for Invoke-* — GitHub needs 1.2+.
if ($PSVersionTable.PSVersion.Major -lt 6) {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
}

# Only amd64 binaries are published (docker.yml release loop).
$arch = $env:PROCESSOR_ARCHITECTURE
if ($arch -ne 'AMD64') {
    Fail "Unsupported CPU architecture: $arch (prebuilt Windows binaries cover amd64 only). Build from source: notify/README.md."
}

# --- 1. Locate config.xml ----------------------------------------------------

$ConfigPath = $null
if ($env:SYNCTHING_CONFIG) {
    if (-not (Test-Path -LiteralPath $env:SYNCTHING_CONFIG)) {
        Fail "SYNCTHING_CONFIG is set to $($env:SYNCTHING_CONFIG) but no file exists there."
    }
    $ConfigPath = $env:SYNCTHING_CONFIG
} else {
    # Same location the helper binary probes on Windows (syncthing_config.go).
    $candidate = Join-Path $env:LOCALAPPDATA 'Syncthing\config.xml'
    if (Test-Path -LiteralPath $candidate) {
        $ConfigPath = $candidate
    }
}
if (-not $ConfigPath) {
    Fail @"
Could not find Syncthing's config.xml. Is Syncthing installed on THIS machine?
  - Standard location: %LOCALAPPDATA%\Syncthing\config.xml
  - If it lives elsewhere, set the variable and re-run:
      `$env:SYNCTHING_CONFIG = 'C:\path\to\config.xml'; irm https://vaultsync.eu/notify.ps1 | iex
  - Custom setups: https://github.com/$Repo/blob/main/notify/README.md
"@
}
Write-Info "Found Syncthing config: $ConfigPath"

# --- 2. Resolve the latest notify release ------------------------------------

# The repo also publishes app releases (v*); pick the newest notify-v* tag.
$releases = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases?per_page=30" -UseBasicParsing
$release = $releases | Where-Object { $_.tag_name -like 'notify-v*' } | Select-Object -First 1
if (-not $release) {
    Fail "Could not find a notify release on GitHub ($Repo). Check your network, or build from source: notify/README.md."
}
$tag = $release.tag_name
$version = $tag -replace '^notify-v', ''
Write-Info "Installing $AssetName from release $tag."

# Make the upgrade visible (#87/#90). Best effort: binaries older than the
# --version flag print nothing and the line is simply skipped.
if (Test-Path -LiteralPath $ExePath) {
    try {
        $oldVersion = & $ExePath --version 2>$null
        if ($oldVersion) { Write-Info "Currently installed: $oldVersion — installing $version." }
    } catch { }
}

# --- 3. Download + checksum verification -------------------------------------

$base = "https://github.com/$Repo/releases/download/$tag"
if ($DryRun) {
    Write-Info "[dry-run] would download: $base/$AssetName -> $ExePath (verified against $base/SHA256SUMS)"
} else {
    $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("vaultsync-notify-" + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $tmpExe = Join-Path $tmp $AssetName
        Invoke-WebRequest -Uri "$base/$AssetName" -OutFile $tmpExe -UseBasicParsing

        # Verify against the release checksums — abort on any mismatch.
        $sumsFile = Join-Path $tmp 'SHA256SUMS'
        Invoke-WebRequest -Uri "$base/SHA256SUMS" -OutFile $sumsFile -UseBasicParsing
        $expectedLine = Get-Content $sumsFile | Where-Object { $_ -match [regex]::Escape($AssetName) } | Select-Object -First 1
        if (-not $expectedLine) {
            Fail "SHA256SUMS carries no entry for $AssetName — aborting."
        }
        $expected = ($expectedLine -split '\s+')[0].ToLowerInvariant()
        $actual = (Get-FileHash -Path $tmpExe -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($expected -ne $actual) {
            Fail "Checksum mismatch for $AssetName — aborting. (expected $expected, got $actual)"
        }
        Write-Info 'Checksum verified.'

        if (-not (Test-Path -LiteralPath $InstallDir)) {
            New-Item -ItemType Directory -Path $InstallDir | Out-Null
        }
        # Stop a running task first so the exe is not locked (upgrade path).
        $existingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        if ($existingTask) {
            Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
            Start-Sleep -Seconds 2
        }
        Move-Item -Force -Path $tmpExe -Destination $ExePath
    } finally {
        Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
    }
}

# --- 4. Runner + Scheduled Task -----------------------------------------------

# Scheduled tasks cannot set environment variables, and running a console exe
# directly flashes a window at logon — a tiny hidden runner does both.
# Values land inside single-quoted PowerShell literals — double any embedded
# apostrophe (legal in Windows profile paths, e.g. C:\Users\O'Brien) or the
# generated runner fails to parse at every logon.
$escConfig = $ConfigPath -replace "'", "''"
$escRelay = $RelayUrl -replace "'", "''"
$escExe = $ExePath -replace "'", "''"
$escLog = $LogPath -replace "'", "''"
$runnerContent = @"
`$env:SYNCTHING_CONFIG = '$escConfig'
`$env:RELAY_URL = '$escRelay'
& '$escExe' 2>&1 | Out-File -FilePath '$escLog' -Encoding utf8
"@

if ($DryRun) {
    Write-Info "[dry-run] would write $RunnerPath and register the Scheduled Task '$TaskName' (run hidden at logon, restart on failure)"
} else {
    Set-Content -Path $RunnerPath -Value $runnerContent -Encoding UTF8

    $action = New-ScheduledTaskAction -Execute 'powershell.exe' `
        -Argument "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$RunnerPath`""
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
    $settings = New-ScheduledTaskSettingsSet `
        -RestartCount 10 `
        -RestartInterval (New-TimeSpan -Minutes 1) `
        -ExecutionTimeLimit (New-TimeSpan -Seconds 0) `
        -StartWhenAvailable `
        -Hidden
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
        -Settings $settings -Force | Out-Null
    Write-Info "Registered Scheduled Task '$TaskName' (runs hidden at logon; restarts on failure)."

    Start-ScheduledTask -TaskName $TaskName
    Write-Info 'Helper started.'
}

# --- 5. Doctor preflight -------------------------------------------------------

if ($DryRun) {
    Write-Info "[dry-run] would run: $ExePath --doctor"
    Write-Info 'Dry run complete — nothing was changed. Re-run without the dry-run flag to install.'
    return
}

Write-Info 'Running preflight checks (doctor)...'
$env:SYNCTHING_CONFIG = $ConfigPath
$env:RELAY_URL = $RelayUrl
& $ExePath --doctor
if ($LASTEXITCODE -ne 0) {
    Fail 'Preflight failed — see the messages above for the fix, then re-run this installer.'
}

Write-Info ''
Write-Info 'Done. The helper has sent a first wake-up — within a minute, VaultSync on'
Write-Info 'your iPhone shows "Cloud Relay active" (Relay tab). Nothing but your'
Write-Info 'Syncthing Device ID ever leaves this machine.'
Write-Info "Logs: $LogPath  |  Self-check any time: & '$ExePath' --doctor"
