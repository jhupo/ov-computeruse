param(
  [string]$ServerUrl = "",
  [string]$Username = ""
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$Agent = Join-Path $Root "ov-agent-windows-amd64.exe"
if (!(Test-Path $Agent)) {
  $Agent = Join-Path $Root "ov-agent.exe"
}
if (!(Test-Path $Agent)) {
  throw "ov-agent executable not found next to install.ps1"
}

$InstallDir = Join-Path $env:LOCALAPPDATA "ov-computeruse\agent"
$Args = @("install")
if ($ServerUrl -ne "") {
  $Args += @("-server-url", $ServerUrl)
}
if ($Username -ne "") {
  $LoginUsername = $Username
} else {
  $LoginUsername = Read-Host "Username"
}
$SecurePassword = Read-Host "Password" -AsSecureString
$PasswordPtr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($SecurePassword)
$LoginFile = Join-Path ([System.IO.Path]::GetTempPath()) ("ov-agent-login-{0}.json" -f ([System.Guid]::NewGuid().ToString("N")))

try {
  $LoginPassword = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($PasswordPtr)
  $LoginJson = @{ username = $LoginUsername; password = $LoginPassword } | ConvertTo-Json -Compress
  $Utf8NoBom = New-Object System.Text.UTF8Encoding($false)
  [System.IO.File]::WriteAllText($LoginFile, $LoginJson, $Utf8NoBom)
  $Args += @("-login-file", $LoginFile)

  & $Agent @Args
  if ($LASTEXITCODE -ne 0) {
    throw "agent install failed"
  }
} finally {
  if ($PasswordPtr -ne [IntPtr]::Zero) {
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($PasswordPtr)
  }
  if (Test-Path $LoginFile) {
    Remove-Item -Force $LoginFile
  }
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item -Force $Agent (Join-Path $InstallDir "ov-agent.exe")

$TaskName = "ov-computeruse-agent"
$Action = New-ScheduledTaskAction -Execute (Join-Path $InstallDir "ov-agent.exe") -Argument "run"
$Trigger = New-ScheduledTaskTrigger -AtLogOn
$Principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive
Register-ScheduledTask -TaskName $TaskName -Action $Action -Trigger $Trigger -Principal $Principal -Force | Out-Null

Start-Process -FilePath (Join-Path $InstallDir "ov-agent.exe") -ArgumentList "run" -WindowStyle Hidden
Write-Host "ov-computeruse agent installed and started"
