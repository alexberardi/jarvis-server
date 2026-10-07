# I0 spike (docs/install/00-installers.md §8), Windows rows. Runs on a GitHub-hosted
# windows-latest runner (no GPU) under Windows PowerShell 5.1, elevated. Not product code.
#
#   windows-spike.ps1 -Bin <dir with jarvisd.exe, svcwrap.exe, mdnsbrowse.exe>
#
# Rows: raw jarvisd.exe under the SCM; jarvisd as a service under NT SERVICE\jarvisd (via
# svcwrap, since jarvisd does not answer the SCM yet); a CPU llama-server engine under that
# account and what it binds; a >260-char model path with LongPathsEnabled 0 and 1; mDNS on
# 5353 next to the DNS Client service; stop behaviour. Output: C:\i0-results.
param([Parameter(Mandatory = $true)][string]$Bin)

$ErrorActionPreference = 'Continue'
$out = 'C:\i0-results'
New-Item -ItemType Directory -Force $out | Out-Null
Start-Transcript "$out\transcript.txt" | Out-Null
$results = New-Object System.Collections.ArrayList

function Row($name, $result, $evidence) {
    [void]$results.Add("| $name | $result | $evidence |")
    Write-Host "### ROW $name => $result :: $evidence"
}
function Section($t) { Write-Host "`n===== $t =====" }
function Procs($name) {
    Get-CimInstance Win32_Process -Filter "Name='$name'" | ForEach-Object {
        $o = Invoke-CimMethod -InputObject $_ -MethodName GetOwner
        [pscustomobject]@{ Pid = $_.ProcessId; Session = $_.SessionId; Owner = "$($o.Domain)\$($o.User)"; Cmd = $_.CommandLine }
    }
}
function Health($port) {
    try { (Invoke-RestMethod "http://127.0.0.1:$port/health" -TimeoutSec 3).status } catch { "" }
}
function WaitHealth($port, $secs) {
    $deadline = (Get-Date).AddSeconds($secs)
    while ((Get-Date) -lt $deadline) { if ((Health $port) -eq 'ok') { return $true }; Start-Sleep 1 }
    return $false
}

$pf = 'C:\Program Files\jarvisd'
$jh = 'C:\ProgramData\jarvisd'
$api = 'http://127.0.0.1:7704'
$authUrl = 'http://127.0.0.1:7701'

# ---------------------------------------------------------------------------------------------
Section 'host'
(Get-CimInstance Win32_OperatingSystem) | Select-Object Caption, Version, BuildNumber | Format-List
$psv = $PSVersionTable.PSVersion.ToString(); "PowerShell $psv"
$lpOrig = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem').LongPathsEnabled
"LongPathsEnabled=$lpOrig"
Get-Service Dnscache | Format-List Name, Status, StartType
"5353/udp before jarvisd:"
Get-NetUDPEndpoint -LocalPort 5353 -ErrorAction SilentlyContinue | ForEach-Object {
    "  {0}:{1} pid={2} {3}" -f $_.LocalAddress, $_.LocalPort, $_.OwningProcess, (Get-Process -Id $_.OwningProcess).ProcessName
}
"GPU: " + ((Get-CimInstance Win32_VideoController | ForEach-Object { $_.Name }) -join ', ')
if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) { nvidia-smi -L } else { "nvidia-smi: not present" }

New-Item -ItemType Directory -Force $pf | Out-Null
Copy-Item "$Bin\jarvisd.exe", "$Bin\svcwrap.exe", "$Bin\mdnsbrowse.exe" $pf
& "$pf\jarvisd.exe" version

# ---------------------------------------------------------------------------------------------
Section 'row: raw jarvisd.exe registered with the SCM (no svc support)'
sc.exe --% create jarvisd-raw binPath= "\"C:\Program Files\jarvisd\jarvisd.exe\" serve" obj= "NT SERVICE\jarvisd-raw" start= demand
$t0 = Get-Date
$startOut = sc.exe start jarvisd-raw 2>&1 | Out-String
$elapsed = [int]((Get-Date) - $t0).TotalSeconds
$startOut
"sc start returned after ${elapsed}s"
Start-Sleep 3
$q = sc.exe query jarvisd-raw | Out-String; $q
$raw = @(Procs 'jarvisd.exe'); $raw | Format-List
$rawHealth = Health 7700
"health on 7700: '$rawHealth'"
"ServiceProfiles:"; Get-ChildItem C:\Windows\ServiceProfiles -Force -ErrorAction SilentlyContinue | ForEach-Object { "  $($_.FullName)" }
$rawHome = Get-ChildItem C:\Windows\ServiceProfiles -Directory -Force -ErrorAction SilentlyContinue |
    ForEach-Object { Join-Path $_.FullName '.jarvis' } | Where-Object { Test-Path $_ }
"raw home: $rawHome"
foreach ($p in $raw) { Stop-Process -Id $p.Pid -Force -ErrorAction SilentlyContinue }
sc.exe delete jarvisd-raw | Out-Null
$code = if ($startOut -match '1053') { '1053' } else { ($startOut -split "`n" | Select-Object -First 3) -join ' ' }
$st = if ($q -match 'RUNNING') { 'RUNNING' } else { 'not running' }
Row 'raw jarvisd.exe as SCM service' $(if ($q -match 'RUNNING') { 'yes' } else { 'no' }) "sc start -> $code after ${elapsed}s; state $st; process alive=$($raw.Count -gt 0) owner=$($raw[0].Owner) session=$($raw[0].Session); health='$rawHealth'; default home=$rawHome"
Start-Sleep 3

# ---------------------------------------------------------------------------------------------
Section 'row: jarvisd under NT SERVICE\jarvisd via svcwrap'
New-Item -ItemType Directory -Force $jh | Out-Null
$tok = -join ((1..24) | ForEach-Object { '{0:x2}' -f (Get-Random -Maximum 256) })
$pw = -join ((1..12) | ForEach-Object { '{0:x2}' -f (Get-Random -Maximum 256) })
Set-Content "$jh\i0.env" "JARVIS_HOME=$jh`r`nJARVIS_AUTH_ADMIN_TOKEN=$tok" -Encoding ASCII
sc.exe --% create jarvisd binPath= "\"C:\Program Files\jarvisd\svcwrap.exe\" C:\ProgramData\jarvisd\i0.env \"C:\Program Files\jarvisd\jarvisd.exe\" serve" obj= "NT SERVICE\jarvisd" start= demand
sc.exe sidtype jarvisd unrestricted
sc.exe qc jarvisd
icacls $jh /inheritance:r /grant:r "SYSTEM:(OI)(CI)F" "BUILTIN\Administrators:(OI)(CI)F" "NT SERVICE\jarvisd:(OI)(CI)M"
icacls $jh
sc.exe start jarvisd
$up = WaitHealth 7700 120
sc.exe query jarvisd
$svc = @(Procs 'jarvisd.exe'); $svc | Format-List
Get-Content "$jh\logs\jarvisd.log" -Tail 40
"5353/udp right after jarvisd started:"
Get-NetUDPEndpoint -LocalPort 5353 -ErrorAction SilentlyContinue | ForEach-Object {
    "  {0}:{1} pid={2} {3}" -f $_.LocalAddress, $_.LocalPort, $_.OwningProcess, (Get-Process -Id $_.OwningProcess).ProcessName
}
Row 'jarvisd as SCM service under NT SERVICE\jarvisd (svcwrap)' $(if ($up) { 'yes' } else { 'no' }) "health=$up owner=$($svc[0].Owner) session=$($svc[0].Session) home=$jh"

# ---------------------------------------------------------------------------------------------
Section 'row: CPU llama-server engine under the virtual account; loopback only'
$H = $null
try {
    $body = @{ email = 'i0@example.com'; password = $pw } | ConvertTo-Json
    $s = Invoke-RestMethod -Method Post "$authUrl/auth/setup" -ContentType 'application/json' -Body $body
    $H = @{ Authorization = "Bearer $($s.access_token)" }
    $hw = Invoke-RestMethod "$api/v1/hardware" -Headers $H
    $hw.hardware | ConvertTo-Json -Depth 6
    $req = '{"repo":"Qwen/Qwen2.5-0.5B-Instruct-GGUF","file":"qwen2.5-0.5b-instruct-q4_k_m.gguf","kind":"llm","id":"qwen2.5-0.5b-i0","context_default":4096,"assign":["live"],"gpu_backend":"cpu"}'
    $inst = (Invoke-RestMethod -Method Post "$api/v1/models/install" -Headers $H -ContentType 'application/json' -Body $req).install
    $deadline = (Get-Date).AddSeconds(900)
    do { Start-Sleep 3; $i = Invoke-RestMethod "$api/v1/models/installs/$($inst.id)" -Headers $H }
    while ((Get-Date) -lt $deadline -and @('done', 'failed', 'cancelled') -notcontains $i.state)
    $i | ConvertTo-Json -Depth 4
} catch { "engine setup error: $_" }

function LiveLabel { ((Invoke-RestMethod "$api/v1/models/labels" -Headers $H).labels | Where-Object { $_.label -eq 'live' }) }
function WaitLive($modelId, $secs) {
    $deadline = (Get-Date).AddSeconds($secs)
    do { Start-Sleep 2; $l = LiveLabel } while ((Get-Date) -lt $deadline -and -not ($l.state -eq 'ready' -and $l.config.model -eq $modelId))
    return $l
}
function LiveEngine { (Invoke-RestMethod "$api/v1/hardware" -Headers $H).engines | Where-Object { $_.labels -contains 'live' } }
function Chat {
    $b = '{"model":"live","messages":[{"role":"user","content":"Reply with the single word OK."}],"max_tokens":16,"temperature":0}'
    try { (Invoke-RestMethod -Method Post "$api/v1/chat/completions" -Headers $AppH -ContentType 'application/json' -Body $b).choices[0].message.content } catch { "chat error: $_" }
}

$AppH = $null
try {
    $app = Invoke-RestMethod -Method Post "$authUrl/admin/app-clients" -Headers @{ 'X-Jarvis-Admin-Token' = $tok } -ContentType 'application/json' -Body '{"app_id":"i0-spike","name":"I0 spike"}'
    $AppH = @{ 'X-Jarvis-App-Id' = 'i0-spike'; 'X-Jarvis-App-Key' = $app.key }
} catch { "app client error: $_" }

$l = WaitLive 'qwen2.5-0.5b-i0' 180
"live: state=$($l.state) reason=$($l.reason) endpoint=$($l.endpoint.base_url)"
$reply = Chat; "chat reply: $reply"
$e = LiveEngine
$eng = @(Procs 'llama-server.exe'); $eng | Format-List
$listen = @()
if ($e.pid) {
    $listen = @(Get-NetTCPConnection -State Listen -OwningProcess $e.pid -ErrorAction SilentlyContinue | ForEach-Object { "$($_.LocalAddress):$($_.LocalPort)" })
    "engine pid=$($e.pid) listens on: $($listen -join ', ')"
    netstat -ano | Select-String "LISTENING\s+$($e.pid)\s*$"
}
$loopOnly = ($listen.Count -gt 0) -and -not ($listen | Where-Object { $_ -notmatch '^(127\.0\.0\.1|::1):' })
Row 'CPU llama-server under the virtual account (session 0)' $(if ($l.state -eq 'ready' -and $reply -match 'OK') { 'yes' } else { 'no' }) "install=$($i.state)/$($i.engine_flavour) live=$($l.state) chat='$reply' engine owner=$($eng[0].Owner) session=$($eng[0].Session)"
Row 'engines bind loopback only' $(if ($loopOnly) { 'yes' } else { 'no' }) "llama-server pid $($e.pid) LISTEN: $($listen -join ', ')"

# ---------------------------------------------------------------------------------------------
Section 'row: model path longer than MAX_PATH'
$short = (Invoke-RestMethod "$api/v1/models/installed" -Headers $H).models | Where-Object { $_.id -eq 'qwen2.5-0.5b-i0' }
$src = $short.path
$owner = 'o' * 39; $repo = 'r' * 96
$dir = "$jh\models\$owner--$repo\" + ('s' * 60)
$file = "$dir\" + ('f' * 80) + '.gguf'
"long path length: $($file.Length)"
[void][System.IO.Directory]::CreateDirectory("\\?\$dir")
[System.IO.File]::Copy("\\?\$src", "\\?\$file", $true)
"copied: $([System.IO.File]::Exists("\\?\$file"))"
try {
    $reg = @{ path = $file; kind = 'llm'; id = 'longpath-i0' } | ConvertTo-Json
    Invoke-RestMethod -Method Post "$api/v1/models/installed" -Headers $H -ContentType 'application/json' -Body $reg | ConvertTo-Json -Depth 4
} catch { "register error: $_" }

"8.3 names: $(fsutil 8dot3name query C: | Out-String)"
Add-Type -Namespace I0 -Name Native -MemberDefinition @'
[DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
public static extern uint GetShortPathName(string lpszLongPath, System.Text.StringBuilder lpszShortPath, uint cchBuffer);
'@
$sb = New-Object System.Text.StringBuilder 1024
$n = [I0.Native]::GetShortPathName("\\?\$file", $sb, 1024)
$shortPath = $sb.ToString() -replace '^\\\\\?\\', ''
"8.3 short path ($n chars): $shortPath"
# Workarounds a service could apply when handing the path to the engine.
$variants = [ordered]@{ 'longpath-unc' = "\\?\$file" }
if ($n -gt 0 -and $shortPath.Length -lt 260 -and $shortPath -ne $file) { $variants['longpath-83'] = $shortPath }
foreach ($id in $variants.Keys) {
    try {
        $reg = @{ path = $variants[$id]; kind = 'llm'; id = $id } | ConvertTo-Json
        Invoke-RestMethod -Method Post "$api/v1/models/installed" -Headers $H -ContentType 'application/json' -Body $reg | Out-Null
        "registered $id -> $($variants[$id])"
    } catch { "register $id error: $_" }
}

function TryModel($id, $lp) {
    Section "long path: model $id with LongPathsEnabled=$lp"
    Set-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem' -Name LongPathsEnabled -Value $lp
    # Go back to the short model first so the long one starts a fresh engine process.
    Invoke-RestMethod -Method Put "$api/v1/models/labels" -Headers $H -ContentType 'application/json' -Body '{"live":{"model":"qwen2.5-0.5b-i0"}}' | Out-Null
    [void](WaitLive 'qwen2.5-0.5b-i0' 120)
    try {
        Invoke-RestMethod -Method Put "$api/v1/models/labels" -Headers $H -ContentType 'application/json' -Body ('{"live":{"model":"' + $id + '"}}') | Out-Null
    } catch { "labels PUT error: $_" }
    $l = WaitLive $id 120
    $r = if ($l.state -eq 'ready') { Chat } else { '' }
    "live: state=$($l.state) reason=$($l.reason) model_path=$($l.config.model_path) chat='$r'" | Write-Host
    $e = LiveEngine
    "engine output (tail):" | Write-Host
    $e.output | Select-Object -Last 12 | Write-Host
    return "$id LongPathsEnabled=${lp}: live=$($l.state) chat='$r'"
}
$lpResults = @()
$lpResults += TryModel 'longpath-i0' 0
$lpResults += TryModel 'longpath-i0' 1
foreach ($id in $variants.Keys) { $lpResults += TryModel $id 0 }
Set-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\FileSystem' -Name LongPathsEnabled -Value $lpOrig
$lpOk = ($lpResults[0] -match 'live=ready') -and ($lpResults[1] -match 'live=ready')
Row "model path of $($file.Length) chars, plain" $(if ($lpOk) { 'yes' } else { 'no' }) ((($lpResults | Select-Object -First 2) -join '; ') + "; runner default LongPathsEnabled=$lpOrig")
Row "model path of $($file.Length) chars, workarounds" 'see evidence' (($lpResults | Select-Object -Skip 2) -join '; ')

# ---------------------------------------------------------------------------------------------
Section 'row: mDNS next to the DNS Client service'
$mlog = Select-String -Path "$jh\logs\jarvisd.log" -Pattern 'mdns' | ForEach-Object { $_.Line }
$mlog
"5353/udp with jarvisd running:"
$owners = Get-NetUDPEndpoint -LocalPort 5353 -ErrorAction SilentlyContinue | ForEach-Object {
    "{0}:{1} pid={2} {3}" -f $_.LocalAddress, $_.LocalPort, $_.OwningProcess, (Get-Process -Id $_.OwningProcess).ProcessName
}
$owners
"Dnscache: $((Get-Service Dnscache).Status)"
$winRes = try { (Resolve-DnsName "$env:COMPUTERNAME.local" -Type A -ErrorAction Stop | ForEach-Object { $_.IPAddress }) -join ',' } catch { "error: $_" }
"Windows resolver (Dnscache mDNS) for $env:COMPUTERNAME.local: $winRes"
$browse = & "$pf\mdnsbrowse.exe" -t 6s 2>&1 | Out-String
$browse
$seen = $browse -match 'port=7700'
$advertised = ($mlog -join ' ') -match 'mdns advertising'
Row 'mDNS: bind 5353 next to Dnscache, seen by a browser' $(if ($advertised -and $seen) { 'yes' } else { 'no' }) "advertising=$advertised; browse saw port 7700=$seen; Windows resolver $env:COMPUTERNAME.local -> $winRes; 5353 owners: $((($owners | ForEach-Object { ($_ -split ' ')[-1] }) | Sort-Object -Unique) -join ', ')"

# ---------------------------------------------------------------------------------------------
Section 'row: stop'
# Stop with an engine running, to see whether killing jarvisd orphans it.
Invoke-RestMethod -Method Put "$api/v1/models/labels" -Headers $H -ContentType 'application/json' -Body '{"live":{"model":"qwen2.5-0.5b-i0"}}' | Out-Null
[void](WaitLive 'qwen2.5-0.5b-i0' 120)
$before = @(Procs 'llama-server.exe')
sc.exe stop jarvisd
Start-Sleep 8
sc.exe query jarvisd
$leftJ = @(Procs 'jarvisd.exe'); $leftL = @(Procs 'llama-server.exe')
"left after stop: jarvisd=$($leftJ.Count) llama-server=$($leftL.Count)"
$leftL | Format-List
Row 'stop leaves no engine behind (svcwrap kills jarvisd)' $(if ($leftL.Count -eq 0) { 'yes' } else { 'no' }) "llama-server before=$($before.Count) after=$($leftL.Count); jarvisd after=$($leftJ.Count)"
foreach ($p in $leftJ + $leftL) { Stop-Process -Id $p.Pid -Force -ErrorAction SilentlyContinue }
sc.exe delete jarvisd | Out-Null
Copy-Item "$jh\logs\jarvisd.log" "$out\jarvisd.log" -ErrorAction SilentlyContinue

Row 'GPU (CUDA/Vulkan) in session 0' 'untested' "hosted runner has no GPU ($((Get-CimInstance Win32_VideoController | ForEach-Object { $_.Name }) -join ', '))"

# ---------------------------------------------------------------------------------------------
$table = @('| Row | Result | Evidence |', '|---|---|---|') + $results
$table | Set-Content "$out\results.md"
if ($env:GITHUB_STEP_SUMMARY) { $table | Add-Content $env:GITHUB_STEP_SUMMARY }
$table
Stop-Transcript | Out-Null
exit 0
