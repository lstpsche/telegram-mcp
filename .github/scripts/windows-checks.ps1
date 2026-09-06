$ErrorActionPreference = 'Stop'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
# Start-Process inherits the runner's environment even with LoadUserProfile.
$profileKey = "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\$($identity.User.Value)"
$env:USERPROFILE = Get-ItemPropertyValue $profileKey -Name ProfileImagePath
$env:APPDATA = Join-Path $env:USERPROFILE 'AppData\Roaming'
$env:LOCALAPPDATA = Join-Path $env:USERPROFILE 'AppData\Local'
# Private-state fixtures must live beneath this account's private profile.
$env:TEMP = Join-Path $env:LOCALAPPDATA 'Temp'
$env:TMP = $env:TEMP
New-Item -ItemType Directory -Path $env:TEMP -Force | Out-Null
$env:GIT_CONFIG_GLOBAL = Join-Path $env:TEMP 'gitconfig'
& git config --global --add safe.directory $env:GITHUB_WORKSPACE
if ($LASTEXITCODE -ne 0) { throw 'Configuring checkout ownership failed' }
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Native checks require a non-administrative token'
}
$probe = New-TemporaryFile
try {
    $owner = (Get-Acl $probe.FullName).GetOwner([Security.Principal.SecurityIdentifier])
    if (!$owner.Equals($identity.User)) { throw 'Native checks require current-user file ownership' }
} finally {
    Remove-Item $probe.FullName
}
foreach ($command in @(
    @('build', './cmd/...'),
    @('test', '-count=1', '-timeout=15m', './...'),
    @('vet', './...'),
    @('run', './cmd/telegram-mcp', '--version'),
    @('run', './cmd/telegram-mcpctl', '--version'),
    @('run', './cmd/telegram-mcpd', '--version')
)) {
    & go @command
    if ($LASTEXITCODE -ne 0) { throw "go $command failed" }
}

$env:TELEGRAM_MCP_NATIVE_SERVICE_TEST = '1'
& go test -count=1 -v -timeout=5m ./internal/cli
if ($LASTEXITCODE -ne 0) { throw 'Native console checks failed' }
# A secondary logon is not an interactive desktop session for Task Scheduler.
$env:TELEGRAM_MCP_NATIVE_SERVICE_TEST = 'registration'
& go test -count=1 -v -timeout=5m ./internal/service
if ($LASTEXITCODE -ne 0) { throw 'Native task registration checks failed' }
Remove-Item Env:TELEGRAM_MCP_NATIVE_SERVICE_TEST
