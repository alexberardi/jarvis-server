; Jarvis Automation Server Setup: the Windows installer of jarvisd (01-no-cli-plan item 2.3).
; Per machine (UAC). Built by scripts/build-windows-installers.sh with Inno Setup.
;
; The installer stays thin: everything is jarvisd's own `install-from-app` (00-installers §8.3),
; run from the payload in {tmp} before any file is installed. It puts jarvisd.exe in
; %ProgramFiles%\jarvisd, where its updater swaps it, unless a newer one is already there (an
; install that updated itself: kept, so an older installer never downgrades it); registers the
; service and its updater; opens the firewall for the local network when the box is ticked
; (`doctor --fix`: private networks, local subnet only); and writes the setup link to a file.
; If it fails (another program holds jarvisd's ports), Setup stops with its message and installs
; nothing. The Finish page opens the link. Data lives in %ProgramData%\jarvisd and stays on an
; upgrade. Uninstall runs `jarvisd service uninstall`, and asks whether to delete the data too
; (default No; /PURGE says yes on a silent uninstall).

#include "common.iss"

#define AppName "Jarvis Automation Server"
; jarvisd's reverse-DNS label: the uninstall entry is ...\Uninstall\net.jarvisautomation.jarvisd_is1,
; which the updater rewrites (internal/platform/service UninstallKey).
#define AppId "net.jarvisautomation.jarvisd"
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
; The path jarvisd's updater and install-from-app expect.
DefaultDirName={commonpf}\jarvisd
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableWelcomePage=no
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
WizardStyle=modern
Compression=lzma2
SolidCompression=yes
OutputDir={#OutputDir}
OutputBaseFilename={#OutputName}
UninstallDisplayName={#AppName}
; The service holds jarvisd.exe; install-from-app handles it, not the Restart Manager.
CloseApplications=no
RestartApplications=no
SetupLogging=yes

[Messages]
WelcomeLabel2=This installs [name/ver] on your computer.%n%nJarvis Automation runs in the background as a Windows service and starts with Windows. When Setup finishes, your browser opens to finish setting it up.

[Tasks]
Name: firewall; Description: "Let phones, voice nodes and other computers on my local network reach Jarvis Automation (local network only, never the internet)"
Name: desktopicon; Description: "Put a shortcut to Jarvis Automation on the desktop"; Flags: unchecked

[Files]
; Not copied by Setup: install-from-app places it (keeping a newer one) from {tmp}.
Source: "{#BinDir}\jarvisd.exe"; Flags: dontcopy

[Run]
Filename: "{code:FinishURL}"; Description: "Open Jarvis Automation in my browser to finish setting it up"; Flags: postinstall shellexec runasoriginaluser nowait skipifsilent

[UninstallDelete]
; jarvisd.exe and what the updater keeps beside it (jarvisd.prev.exe, its log): not Setup's files.
Type: filesandordirs; Name: "{app}"
Type: files; Name: "{autoprograms}\{#AppName}.url"
Type: files; Name: "{autodesktop}\{#AppName}.url"

[Code]
#include "common-code.iss"

var
  SetupURL: String; // what install-from-app printed: the setup link, or the admin once set up

// FinishURL is the link the Finish page opens.
function FinishURL(Param: String): String;
begin
  Result := SetupURL;
end;

// AdminURL is the admin on this computer, http://localhost:<port>/, the port taken from the
// link install-from-app wrote (whose host is this computer's LAN address, which can change).
function AdminURL(): String;
var
  Rest, Port: String;
  I: Integer;
begin
  Port := '7710';
  Rest := SetupURL;
  I := Pos('://', Rest);
  if I > 0 then
  begin
    Rest := Copy(Rest, I + 3, Length(Rest));
    I := Pos('/', Rest);
    if I > 0 then
      Rest := Copy(Rest, 1, I - 1);
    // The port follows the last colon (an IPv6 host has colons of its own, inside brackets).
    I := Length(Rest);
    while (I > 0) and (Rest[I] <> ':') and (Rest[I] <> ']') do
      I := I - 1;
    if (I > 0) and (Rest[I] = ':') and (I < Length(Rest)) then
      Port := Copy(Rest, I + 1, Length(Rest));
  end;
  Result := 'http://localhost:' + Port + '/';
end;

procedure WriteShortcut(const Path: String);
begin
  if not SaveStringToFile(Path, '[InternetShortcut]' + #13#10 + 'URL=' + AdminURL() + #13#10, False) then
    Log('Could not write ' + Path);
end;

// PrepareToInstall runs jarvisd's install-from-app before Setup installs anything, so a failure
// stops Setup with its message and leaves nothing behind.
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  Output: TExecOutput;
  Params, UrlFile: String;
  Link: AnsiString;
  Code: Integer;
begin
  Result := '';
  ExtractTemporaryFile('jarvisd.exe');
  UrlFile := ExpandConstant('{tmp}\setup-url.txt');
  Params := 'install-from-app --bin "' + ExpandConstant('{app}\jarvisd.exe') + '" --url-file "' + UrlFile + '"';
  if not WizardIsTaskSelected('firewall') then
    Params := Params + ' --no-firewall';
  Code := RunCaptured(ExpandConstant('{tmp}\jarvisd.exe'), Params, Output);
  if Code <> 0 then
  begin
    Result := 'Jarvis Automation could not be installed:' + #13#10#13#10 + TailLines(Output.StdErr, 12);
    Exit;
  end;
  if LoadStringFromFile(UrlFile, Link) then
    SetupURL := Trim(String(Link));
  if SetupURL = '' then
    SetupURL := AdminURL();
  Log('Setup link: ' + SetupURL);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
  begin
    WriteShortcut(ExpandConstant('{autoprograms}\{#AppName}.url'));
    if WizardIsTaskSelected('desktopicon') then
      WriteShortcut(ExpandConstant('{autodesktop}\{#AppName}.url'));
  end;
  // After Setup wrote its uninstall entry: the version of the jarvisd that runs.
  if CurStep = ssDone then
    RecordDisplayVersion(ExpandConstant('{app}\jarvisd.exe'), '{#AppId}');
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
    Purge := MsgBox('Also delete Jarvis Automation''s data?' + #13#10#13#10 +
      'That is every account, setting, memory, voice model and log, in ' + ExpandConstant('{commonappdata}\jarvisd') + '. ' +
      'Choose No to keep it: installing Jarvis Automation again picks up where you left off.',
      mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES;
  Params := 'service uninstall';
  if Purge then
    Params := Params + ' --purge --yes';
  if RunCaptured(ExpandConstant('{app}\jarvisd.exe'), Params, Output) <> 0 then
    SuppressibleMsgBox('Removing the Jarvis Automation service reported a problem:' + #13#10#13#10 +
      TailLines(Output.StdErr, 12), mbError, MB_OK, IDOK);
end;
