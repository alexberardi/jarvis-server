// Shared [Code] helpers of server.iss and agent.iss, included right after their [Code] line
// (Pascal only: no section header, no ; comments).

// CmdLineHas reports whether the setup or uninstall command line has the switch Name
// (case-insensitive), e.g. /PURGE for a silent uninstall that deletes the data.
function CmdLineHas(const Name: String): Boolean;
var
  I: Integer;
begin
  Result := False;
  for I := 1 to ParamCount do
    if CompareText(ParamStr(I), Name) = 0 then
    begin
      Result := True;
      Exit;
    end;
end;

procedure LogLines(const What: String; const Lines: TArrayOfString);
var
  I: Integer;
begin
  for I := 0 to GetArrayLength(Lines) - 1 do
    Log(What + ': ' + Lines[I]);
end;

// TailLines is the last Count lines of Lines, one per line.
function TailLines(const Lines: TArrayOfString; Count: Integer): String;
var
  I, First: Integer;
begin
  Result := '';
  First := GetArrayLength(Lines) - Count;
  if First < 0 then
    First := 0;
  for I := First to GetArrayLength(Lines) - 1 do
    Result := Result + Lines[I] + #13#10;
  Result := Trim(Result);
end;

// RunCaptured runs Exe with Params, hidden, waits, and logs its output. It returns the exit
// code, or -1 when Exe couldn't be started (Output.StdErr then says why).
function RunCaptured(const Exe, Params: String; var Output: TExecOutput): Integer;
var
  Code: Integer;
begin
  Log('Running "' + Exe + '" ' + Params);
  if not ExecAndCaptureOutput(Exe, Params, '', SW_HIDE, ewWaitUntilTerminated, Code, Output) then
  begin
    Log('Could not start it: ' + SysErrorMessage(Code));
    SetArrayLength(Output.StdErr, 1);
    Output.StdErr[0] := SysErrorMessage(Code);
    Result := -1;
    Exit;
  end;
  LogLines('stdout', Output.StdOut);
  LogLines('stderr', Output.StdErr);
  Log('Exit code ' + IntToStr(Code));
  Result := Code;
end;

// RecordDisplayVersion sets the uninstall entry's DisplayVersion to what Exe's `version` prints
// (without the "v"). The program may be newer than this installer: it updates itself, and
// running an older installer again keeps the newer one. jarvisd's updater and the agent's
// self-update keep it current afterwards.
procedure RecordDisplayVersion(const Exe, AppId: String);
var
  Output: TExecOutput;
  V: String;
begin
  if (RunCaptured(Exe, 'version', Output) <> 0) or (GetArrayLength(Output.StdOut) = 0) then
    Exit;
  V := Trim(Output.StdOut[0]);
  if (Length(V) > 1) and (V[1] = 'v') then
    V := Copy(V, 2, Length(V) - 1);
  if V <> '' then
    RegWriteStringValue(HKA, '{#UninstallKeyRoot}' + AppId + '_is1', 'DisplayVersion', V);
end;
