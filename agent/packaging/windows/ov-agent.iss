#define MyAppName "ov-computeruse Agent"
#define MyAppVersion GetEnv("OV_AGENT_VERSION")
#if MyAppVersion == ""
#define MyAppVersion "dev"
#endif

[Setup]
AppId={{9D4E50E9-497A-4E5F-AE2B-5F0B9F5D4A4E}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher=ov-computeruse
DefaultDirName={localappdata}\ov-computeruse\agent
DefaultGroupName=ov-computeruse
DisableProgramGroupPage=yes
OutputDir=..\..\dist
OutputBaseFilename=ov-agent-setup
Compression=lzma
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=lowest
UninstallDisplayIcon={app}\ov-agent.exe

[Files]
Source: "..\..\dist\ov-agent-windows-amd64.exe"; DestDir: "{app}"; DestName: "ov-agent.exe"; Flags: ignoreversion
Source: "..\..\dist\ov-agent-windows-amd64.exe"; DestName: "ov-agent-windows-amd64.exe"; Flags: dontcopy

[Icons]
Name: "{group}\ov-computeruse Agent"; Filename: "{app}\ov-agent.exe"; Parameters: "run"

[Run]
Filename: "{app}\ov-agent.exe"; Parameters: "run"; Flags: nowait runhidden

[UninstallRun]
Filename: "schtasks.exe"; Parameters: "/Delete /TN ""ov-computeruse-agent"" /F"; Flags: runhidden

[Code]
var
  LoginPage: TInputQueryWizardPage;

procedure InitializeWizard;
begin
  LoginPage := CreateInputQueryPage(
    wpWelcome,
    'Login',
    'Bind this device before installing.',
    'Enter your ov-computeruse username and password. The installer will validate your account and local Codex credential before files are installed.'
  );
  LoginPage.Add('Username:', False);
  LoginPage.Add('Password:', True);
end;

function JsonEscape(Value: string): string;
begin
  Result := Value;
  StringChangeEx(Result, '\', '\\', True);
  StringChangeEx(Result, '"', '\"', True);
  StringChangeEx(Result, #13, '\r', True);
  StringChangeEx(Result, #10, '\n', True);
  StringChangeEx(Result, #9, '\t', True);
end;

function NextButtonClick(CurPageID: Integer): Boolean;
var
  ResultCode: Integer;
  TempAgent: string;
  LoginFile: string;
  LoginJSON: string;
  Params: string;
begin
  Result := True;
  if CurPageID = LoginPage.ID then
  begin
    if Trim(LoginPage.Values[0]) = '' then
    begin
      MsgBox('Username is required.', mbError, MB_OK);
      Result := False;
      exit;
    end;
    if Trim(LoginPage.Values[1]) = '' then
    begin
      MsgBox('Password is required.', mbError, MB_OK);
      Result := False;
      exit;
    end;

    ExtractTemporaryFile('ov-agent-windows-amd64.exe');
    TempAgent := ExpandConstant('{tmp}\ov-agent-windows-amd64.exe');
    LoginFile := ExpandConstant('{tmp}\ov-agent-login.json');
    LoginJSON := '{"username":"' + JsonEscape(LoginPage.Values[0]) + '","password":"' + JsonEscape(LoginPage.Values[1]) + '"}';
    if not SaveStringToFile(LoginFile, LoginJSON, False) then
    begin
      MsgBox('Unable to prepare secure local login handoff.', mbError, MB_OK);
      Result := False;
      exit;
    end;
    Params := 'install --login-file "' + LoginFile + '"';

    WizardForm.NextButton.Enabled := False;
    try
      if not Exec(TempAgent, Params, '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
      begin
        MsgBox('Unable to start agent binding.', mbError, MB_OK);
        Result := False;
        exit;
      end;
      if ResultCode <> 0 then
      begin
        MsgBox('Login, device, or Codex credential validation failed. Installation cannot continue.', mbError, MB_OK);
        Result := False;
        exit;
      end;
    finally
      DeleteFile(LoginFile);
      LoginPage.Values[1] := '';
      WizardForm.NextButton.Enabled := True;
    end;
  end;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultCode: Integer;
begin
  if CurStep = ssPostInstall then
  begin
    if not Exec('schtasks.exe', '/Create /SC ONLOGON /TN "ov-computeruse-agent" /TR "\"' + ExpandConstant('{app}\ov-agent.exe') + '\" run" /F', '', SW_HIDE, ewWaitUntilTerminated, ResultCode) then
    begin
      MsgBox('Unable to register agent startup task.', mbError, MB_OK);
      exit;
    end;
    if ResultCode <> 0 then
    begin
      MsgBox('Unable to register agent startup task.', mbError, MB_OK);
      exit;
    end;
  end;
end;
