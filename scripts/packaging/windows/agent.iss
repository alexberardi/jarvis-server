; Jarvis Automation Agent Setup: the Windows installer of the desktop agent (01-no-cli-plan item
; 2.3). Per user: no administrator rights, no UAC. Built by scripts/build-windows-installers.sh.
;
; It installs into %LocalAppData%\Programs\Jarvis Automation Agent, a folder the user owns, so the
; agent can update itself there. The work is the agent's own `jarvis-agent install --bin`, run
; from the payload in {tmp} before Setup installs anything: it puts jarvis-agent.exe in the
; folder unless a newer one is there (an agent that updated itself: kept), then registers it for
; this user: the HKCU Run key (it starts at login), the Start menu and desktop shortcuts, the
; jarvis-agent:// link handler and the browsers' native-messaging host. Those start it through
; jarvis-agentw.exe, a windowless launcher, so no console window flashes. The Finish page starts
; the agent, or opens its setup page when this computer isn't added to Jarvis Automation yet.
; Uninstall runs `jarvis-agent uninstall`, and asks whether to remove the settings and sign-in
; too (default No; /PURGE says yes on a silent uninstall).

#include "common.iss"

#define AppName "Jarvis Automation Agent"
; The agent's app id (internal/agent/appid): the uninstall entry is
; HKCU\...\Uninstall\net.jarvisautomation.agent_is1, which the self-update rewrites
; (internal/agent/install UninstallKey).
#define AppId "net.jarvisautomation.agent"
#ifndef OutputName
  #define OutputName AppName + " Setup " + AppVersion
#endif

[Setup]
AppId={#AppId}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#Publisher}
AppPublisherURL={#ProjectURL}
AppSupportURL={#ProjectURL}/issues
AppUpdatesURL={#ProjectURL}/releases
VersionInfoVersion={#NumericVersion}
VersionInfoProductName={#AppName}
VersionInfoDescription={#AppName} Setup
; Per user: {autopf} is %LocalAppData%\Programs.
PrivilegesRequired=lowest
DefaultDirName={autopf}\{#AppName}
DisableDirPage=yes
DisableProgramGroupPage=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
WizardStyle=modern
Compression=lzma2
SolidCompression=yes
OutputDir={#OutputDir}
OutputBaseFilename={#OutputName}
UninstallDisplayName={#AppName}
; `jarvis-agent install --bin` stops a running agent it replaces; the launcher never stays running.
CloseApplications=no
RestartApplications=no
SetupLogging=yes

[Messages]
WelcomeLabel2=This installs [name/ver] for you on this computer.%n%nThe agent lets Jarvis Automation work on this computer: open apps, type, control your browser, read the screen. It starts when you sign in to Windows, and lives in the notification area.

[Tasks]
Name: desktopicon; Description: "Put a shortcut on the desktop"

[Files]
Source: "{#BinDir}\jarvis-agentw.exe"; DestDir: "{app}"; Flags: ignoreversion
; Not copied by Setup: `install --bin` places it (keeping a newer one) from {tmp}.
Source: "{#BinDir}\jarvis-agent.exe"; Flags: dontcopy

[Run]
Filename: "{app}\jarvis-agentw.exe"; Parameters: "start"; Description: "Start Jarvis Automation Agent"; Flags: postinstall nowait skipifsilent
; A silent install starts it as a login would: running if this computer is set up, otherwise
; waiting for setup in its notification-area icon.
Filename: "{app}\jarvis-agentw.exe"; Parameters: "start --login"; Flags: nowait; Check: WizardSilent

[UninstallDelete]
; jarvis-agent.exe and what the self-update keeps beside it: not Setup's files.
Type: filesandordirs; Name: "{app}"

[Code]
// PrepareToInstall runs `jarvis-agent install --bin` before Setup installs anything, so a
// failure stops Setup with its message. The launcher goes into place first: install points the
// login item, shortcuts and link handler at it when it is there.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Output: TExecOutput;
  Params: String;
begin
  Result := '';
  ExtractTemporaryFile('jarvis-agent.exe');
  ExtractTemporaryFile('jarvis-agentw.exe');
  if not ForceDirectories(ExpandConstant('{app}')) or
     not FileCopy(ExpandConstant('{tmp}\jarvis-agentw.exe'), ExpandConstant('{app}\jarvis-agentw.exe'), False) then
  begin
    Result := 'Could not write to ' + ExpandConstant('{app}');
    Exit;
  end;
  Params := 'install --bin "' + ExpandConstant('{app}\jarvis-agent.exe') + '" --no-start';
  if not WizardIsTaskSelected('desktopicon') then
    Params := Params + ' --desktop-shortcut=false';
  if RunCaptured(ExpandConstant('{tmp}\jarvis-agent.exe'), Params, Output) <> 0 then
    Result := '{#AppName} could not be installed:' + #13#10#13#10 + TailLines(Output.StdErr, 12);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  // After Setup wrote its uninstall entry: the version of the agent that runs.
  if CurStep = ssDone then
    RecordDisplayVersion(ExpandConstant('{app}\jarvis-agent.exe'), '{#AppId}');
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Output: TExecOutput;
  Purge: Boolean;
  Params: String;
begin
  if CurUninstallStep <> usUninstall then
    Exit;
  Purge := CmdLineHas('/PURGE');
  if not UninstallSilent then
    Purge := MsgBox('Also remove your {#AppName} settings and sign-in?' + #13#10#13#10 +
      'Choose No to keep them: installing the agent again picks up where you left off, already signed in.',
      mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES;
  Params := 'uninstall';
  if Purge then
    Params := Params + ' --purge';
  if RunCaptured(ExpandConstant('{app}\jarvis-agent.exe'), Params, Output) <> 0 then
    SuppressibleMsgBox('Removing {#AppName} reported a problem:' + #13#10#13#10 +
      TailLines(Output.StdErr, 12), mbError, MB_OK, IDOK);
end;
