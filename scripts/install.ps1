# Install or upgrade jarvisd, the single-binary Jarvis server, as a Windows service.
#
#   irm https://github.com/alexberardi/jarvis-server/releases/latest/download/install.ps1 | iex
#
# With options, run the downloaded file (Windows PowerShell 5.1 or later):
#   powershell -ExecutionPolicy Bypass -File install.ps1 [-Version vX.Y.Z] [-Yes] [-StopLegacy]
#       [-Force] [-Uninstall [-Purge]] [-BaseUrl URL]
# Under `irm | iex` the options come from JARVISD_VERSION, JARVISD_RELEASE_BASE and JARVISD_YES=1.
#
#   -Yes          apply the firewall fix without asking
#   -StopLegacy   stop the legacy Docker stack (docker stop + restart policy off; data is kept)
#   -Force        reinstall even when this version is installed
#   -Uninstall    remove the service, firewall rule, binary and PATH entry (data is kept)
#   -Purge        with -Uninstall: also delete %ProgramData%\jarvisd, after confirmation
#   -BaseUrl      fetch the release files from this URL instead of GitHub
#
# Thin on purpose (00-installers ID3): download, verify, place; `jarvisd service install`
# registers the service and `jarvisd doctor --fix` adds the firewall rule.
param(
    [string]$Version = $env:JARVISD_VERSION,
    [string]$BaseUrl = $env:JARVISD_RELEASE_BASE,
    [switch]$Yes = ($env:JARVISD_YES -eq '1'),
    [switch]$StopLegacy,
    [switch]$Force,
    [switch]$Uninstall,
    [switch]$Purge
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # the progress bar makes Invoke-WebRequest crawl on 5.1
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
$Repo = 'alexberardi/jarvis-server'
# The project's minisign key, exactly jarvisd's own trust root (internal/update/key.go
# ProjectPublicKey; a unit test keeps them equal). Not overridable.
$PubKey = 'RWRl8nxLAgizV2ZlGLPIfxp71+OvcD6PSbdoRy/evF9EbXxpwsQTB3/F'
$BaseUrl = "$BaseUrl".TrimEnd('/')
$Dir = Join-Path $env:ProgramFiles 'jarvisd'
$Bin = Join-Path $Dir 'jarvisd.exe'
$Prev = Join-Path $Dir 'jarvisd.prev.exe'

function Get-Url([string]$asset) {
    if ($BaseUrl) { return "$BaseUrl/$asset" }
    if ($Version) { return "https://github.com/$Repo/releases/download/$Version/$asset" }
    return "https://github.com/$Repo/releases/latest/download/$asset"
}

function Get-Asset([string]$asset, [string]$to) { Invoke-WebRequest -UseBasicParsing -Uri (Get-Url $asset) -OutFile $to }

# Self-elevate: the service, Program Files and the firewall need an Administrator.
$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    $self = $PSCommandPath
    if (-not $self) { # irm | iex: fetch this script again to run it elevated
        $self = Join-Path $env:TEMP 'jarvisd-install.ps1'
        Get-Asset 'install.ps1' $self
    }
    $argv = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$self`"")
    if ($Version) { $argv += '-Version'; $argv += $Version }
    if ($BaseUrl) { $argv += '-BaseUrl'; $argv += "`"$BaseUrl`"" }
    foreach ($k in 'Yes', 'StopLegacy', 'Force', 'Uninstall', 'Purge') {
        if ((Get-Variable $k).Value) { $argv += "-$k" }
    }
    Write-Host 'jarvisd installs as a Windows service: asking for Administrator rights...'
    $p = Start-Process powershell -Verb RunAs -ArgumentList $argv -Wait -PassThru
    if ($p.ExitCode -ne 0) { throw "the elevated installer failed (exit $($p.ExitCode))" }
    return
}

function Invoke-Native([string]$exe) {
    # Runs a program with the remaining arguments and throws on a non-zero exit.
    & $exe @args
    if ($LASTEXITCODE -ne 0) { throw "$([IO.Path]::GetFileName($exe)) $($args -join ' ') failed (exit $LASTEXITCODE)" }
}

function Get-NativeOutput([string]$exe) {
    # stdout as one string, stderr dropped (redirected stderr becomes an error record in 5.1,
    # which 'Stop' would turn into a throw); $LASTEXITCODE is the program's.
    $ErrorActionPreference = 'Continue'
    & $exe @args 2>$null | Out-String
}

function Confirm-Step([string]$question) {
    # Yes unless answered n; -Yes says yes; no console to ask says no.
    if ($Yes) { return $true }
    if ([Console]::IsInputRedirected -or -not [Environment]::UserInteractive) { return $false }
    $a = Read-Host "$question [Y/n]"
    return -not ($a -match '^[nN]')
}

function Get-LegacyContainers {
    # The legacy Docker stack (ID7), by the same rule as `jarvisd doctor` (internal/doctor
    # LegacyContainer): a running container named jarvis-*, or in the Compose project "jarvis",
    # a "jarvis-*" project, or a project whose files are in .jarvis\compose. Nothing else.
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { return }
    $fmt = '{{.Names}};{{.Label "com.docker.compose.project"}};{{.Label "com.docker.compose.project.working_dir"}}'
    foreach ($line in ((Get-NativeOutput docker ps --format $fmt) -split "`r?`n")) {
        $f = @($line.Trim() -split ';', 3)
        if ($f.Count -lt 3 -or -not $f[0]) { continue }
        $wd = $f[2].Replace('\', '/').TrimEnd('/')
        if ($f[0] -like 'jarvis-*' -or $f[1] -eq 'jarvis' -or $f[1] -like 'jarvis-*' -or $wd -like '*/.jarvis/compose') { $f[0] }
    }
}

function Stop-Legacy([string[]]$names) {
    # Restart policy off, then stop; never down/rm (its data is kept).
    if (-not $names) { return }
    Write-Host "Stopping the legacy stack: $($names -join ' ')"
    Invoke-Native docker update --restart=no @names | Out-Null
    Invoke-Native docker stop @names | Out-Null
}

function Set-MachinePath([bool]$present) {
    $path = [Environment]::GetEnvironmentVariable('Path', 'Machine')
    $parts = @($path -split ';' | Where-Object { $_ -and ($_.TrimEnd('\') -ne $Dir) })
    if ($present) { $parts += $Dir }
    [Environment]::SetEnvironmentVariable('Path', ($parts -join ';'), 'Machine')
    if ($present -and ($env:Path -split ';') -notcontains $Dir) { $env:Path = "$env:Path;$Dir" }
}

if ($Uninstall) {
    if (-not (Test-Path $Bin)) { throw "jarvisd is not installed in $Dir" }
    $flags = @('service', 'uninstall')
    if ($Purge) { $flags += '--purge'; if ($Yes) { $flags += '--yes' } }
    & $Bin @flags
    if ($LASTEXITCODE -ne 0) {
        if ($Purge) { throw 'uninstall stopped; nothing more was removed' }
        Write-Warning 'the service was not removed cleanly'
    }
    for ($i = 0; $i -lt 10 -and (Test-Path $Dir); $i++) { # the exe stays locked until the process is gone
        try { Remove-Item -Recurse -Force $Dir } catch { Start-Sleep 1 }
    }
    if (Test-Path $Dir) { throw "could not remove $Dir" }
    Set-MachinePath $false
    Write-Host 'jarvisd removed.'
    return
}

if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64' -or $env:PROCESSOR_ARCHITEW6432) {
    throw "jarvisd ships for 64-bit x86 Windows only (this is $env:PROCESSOR_ARCHITECTURE $env:PROCESSOR_ARCHITEW6432)"
}

$tmp = Join-Path $env:TEMP ('jarvisd-install-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    # SHA256SUMS names the archive, which carries the version: no API call for "latest".
    $sums = Join-Path $tmp 'SHA256SUMS'
    Get-Asset 'SHA256SUMS' $sums
    $entry = Get-Content $sums | Where-Object { $_.Trim() } | ForEach-Object {
        $h, $n = -split $_
        [pscustomobject]@{ Hash = $h; Name = "$n".TrimStart('*') }
    } | Where-Object { $_.Name -like 'jarvisd-*-windows-amd64.zip' } | Select-Object -First 1
    if (-not $entry) { throw 'the release has no jarvisd for windows-amd64' }
    $rel = $entry.Name.Substring(8, $entry.Name.Length - 8 - '-windows-amd64.zip'.Length)
    if ($Version -and $Version -ne $rel) { throw "asked for $Version but the release files are for $rel" }
    $Version = $rel

    $cur = $null
    if (Test-Path $Bin) {
        $cur = (Get-NativeOutput $Bin version).Trim()
        if (-not $cur) { $cur = 'unknown' }
        # jarvisd is already here, so stopping the legacy stack can't leave neither.
        if ($StopLegacy) { Stop-Legacy @(Get-LegacyContainers) }
        Get-NativeOutput $Bin service status | Out-Null
        if ($cur -eq $Version -and -not $Force -and $LASTEXITCODE -eq 0) {
            Write-Host "jarvisd $Version is already installed and running."
            & $Bin setup-link
            return
        }
        # A jarvisd with its own upgrade takes over from here: it checks the signature itself
        # with the key built into the installed binary (mandatory, no minisign needed), checks
        # free disk, snapshots the database, swaps, waits for the health gate, rolls back.
        if ($cur -ne $Version -and ((Get-NativeOutput $Bin help) -match '(?m)^  upgrade')) {
            Write-Host "Upgrading jarvisd $cur -> $Version with jarvisd upgrade..."
            $env:JARVISD_RELEASE_BASE = $BaseUrl
            Invoke-Native $Bin upgrade --version $Version
            return
        }
        # Installed but not running: reinstall the service on the binary already here; nothing
        # to download (A10 F21: the version is checked before any archive is fetched).
        if ($cur -eq $Version -and -not $Force) {
            Write-Host "jarvisd $Version is installed but not running; reinstalling its service (nothing to download)."
            & $Bin service install
            if ($LASTEXITCODE -eq 0) { Get-NativeOutput $Bin service status --wait 90s | Out-Null }
            if ($LASTEXITCODE -ne 0) { throw "the jarvisd service is not healthy; see 'jarvisd service status' and $env:ProgramData\jarvisd\logs\jarvisd.log" }
            Write-Host "jarvisd $Version is running."
            & $Bin setup-link
            $global:LASTEXITCODE = 0
            return
        }
    }

    # Signature, for a fresh install (or a jarvisd too old to upgrade itself): the downloaded
    # binary can't vouch for itself, so this needs the minisign tool. With minisign installed
    # the signature is required; without it the checksum alone, with a warning
    # (JARVISD_REQUIRE_SIGNATURE=1 refuses that).
    $minisign = Get-Command minisign -ErrorAction SilentlyContinue
    $sig = "$sums.minisig"
    if ($minisign) {
        try { Get-Asset 'SHA256SUMS.minisig' $sig } catch { throw 'the release has no SHA256SUMS.minisig; not installing an unsigned release' }
        Get-NativeOutput $minisign.Source -Vq -P $PubKey -m $sums -x $sig | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'SHA256SUMS signature is INVALID; not installing' }
        Write-Host 'SHA256SUMS signature verified.'
    } elseif ($env:JARVISD_REQUIRE_SIGNATURE -eq '1') {
        throw 'JARVISD_REQUIRE_SIGNATURE=1 but minisign is not installed'
    } else {
        Write-Warning 'minisign is not installed, so the release signature is not checked (checksums only); install minisign for a verified first install. Upgrades are verified by jarvisd itself.'
    }

    Write-Host "Downloading jarvisd $Version for windows-amd64..."
    $zip = Join-Path $tmp $entry.Name
    Get-Asset $entry.Name $zip
    if ((Get-FileHash -Algorithm SHA256 $zip).Hash -ne $entry.Hash) { throw "checksum mismatch for $($entry.Name) (corrupt or tampered download)" }
    Unblock-File $zip
    Expand-Archive -Path $zip -DestinationPath $tmp
    $new = Join-Path $tmp "jarvisd-$Version-windows-amd64\jarvisd.exe"
    Unblock-File $new
    if ((Get-NativeOutput $new version).Trim() -ne $Version) { throw 'the downloaded jarvisd does not run here' }

    if ($cur) {
        Get-NativeOutput $Bin service stop | Out-Null # releases the exe
        Copy-Item -Force $Bin $Prev
    } else {
        # A fresh install next to the legacy stack (ID7): jarvisd needs its ports.
        # Its GPUs too (the legacy llama-servers hold whole cards): -StopLegacy stops the stack
        # even when its ports are free.
        $held = (Get-NativeOutput $new doctor --json) -match '"name": "ports"'
        if ($held -or $StopLegacy) {
            $legacy = @(Get-LegacyContainers)
            if ($held -and -not $legacy) { throw "another program holds jarvisd's ports (7700-7712, 7030-7031, 1884, 9883); stop it first (Get-NetTCPConnection -State Listen -LocalPort 7700 names it)" }
            if (-not $StopLegacy) {
                throw ("the legacy Jarvis Docker stack is running ($($legacy -join ' ')) and holds jarvisd's ports.`n" +
                    "Re-run with -StopLegacy to stop it (docker stop + restart policy off; its data is kept).`n" +
                    "To go back to it later: jarvisd service stop; docker start $($legacy -join ' ')")
            }
            Stop-Legacy $legacy
        }
    }

    Write-Host "Installing $Bin..."
    New-Item -ItemType Directory -Force -Path $Dir | Out-Null
    Copy-Item -Force $new $Bin
    Set-MachinePath $true
    # Reinstalling the service rewrites it, starts the new binary and waits for /health. An
    # upgrade that doesn't come up goes back to the previous binary.
    & $Bin service install
    if ($LASTEXITCODE -eq 0) { Get-NativeOutput $Bin service status --wait 90s | Out-Null }
    if ($LASTEXITCODE -ne 0) {
        if ($cur) {
            Write-Warning "jarvisd $Version did not come up; going back to $cur"
            Get-NativeOutput $Bin service stop | Out-Null
            Copy-Item -Force $Prev $Bin
            Get-NativeOutput $Bin service install | Out-Null
        }
        throw "the jarvisd service is not healthy; see 'jarvisd service status' and $env:ProgramData\jarvisd\logs\jarvisd.log"
    }

    # Firewall (ID5): ask, default yes; without a console only with -Yes.
    if ((Get-NativeOutput $Bin doctor --json) -match '"fix_cmds"') {
        if (Confirm-Step 'Allow nodes and phones on your private network to reach jarvisd through Windows Defender Firewall?') {
            Get-NativeOutput $Bin doctor --fix | Out-Null
            if ($LASTEXITCODE -ne 0) { Write-Warning "the firewall fix failed; run 'jarvisd doctor --fix' as Administrator to see why" }
        } else {
            Write-Warning 'firewall left as is; nodes and phones cannot reach jarvisd until you run (as Administrator): jarvisd doctor --fix'
        }
    }

    Write-Host ''
    Write-Host (Get-NativeOutput $Bin doctor)
    Write-Host "jarvisd $Version is running."
    & $Bin setup-link
    Write-Host "Logs: $env:ProgramData\jarvisd\logs\jarvisd.log"
    Write-Host 'Manage: jarvisd service status | jarvisd service restart (as Administrator) | install.ps1 -Uninstall'
    $global:LASTEXITCODE = 0
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
