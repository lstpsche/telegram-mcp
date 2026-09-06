$ErrorActionPreference = 'Stop'
# Hosted runners disable UAC. Use a distinct standard account rather than a
# restricted administrator token, including for Task Scheduler registration.
$log = 'Microsoft-Windows-TaskScheduler/Operational'
& wevtutil.exe sl $log /e:true
if ($LASTEXITCODE -ne 0) { throw 'Enabling scheduler diagnostics failed' }
$started = Get-Date
$name = 'telegram-mcp-ci'
$password = ConvertTo-SecureString ([Guid]::NewGuid().ToString('N') + '!aA1') -AsPlainText -Force
$user = New-LocalUser -Name $name -Password $password
try {
    Add-LocalGroupMember -SID 'S-1-5-32-545' -Member $user
    $root = Join-Path $env:RUNNER_TEMP 'telegram-mcp-user'
    New-Item -ItemType Directory -Path $root | Out-Null
    & icacls.exe $root /grant "*$($user.SID):(OI)(CI)F"
    if ($LASTEXITCODE -ne 0) { throw 'Granting the check directory failed' }
    & icacls.exe $env:GITHUB_WORKSPACE /grant "*$($user.SID):(OI)(CI)RX" /T /Q
    if ($LASTEXITCODE -ne 0) { throw 'Granting checkout access failed' }
    $credential = [PSCredential]::new("$env:COMPUTERNAME\$name", $password)
    $stdout = Join-Path $root 'stdout.log'
    $stderr = Join-Path $root 'stderr.log'
    $script = Join-Path $env:GITHUB_WORKSPACE '.github/scripts/windows-checks.ps1'
    $process = Start-Process (Get-Command pwsh).Source -Credential $credential -LoadUserProfile -PassThru -Wait `
        -WorkingDirectory $env:GITHUB_WORKSPACE -ArgumentList "-NoProfile -File `"$script`"" `
        -Environment @{ TEMP = $root; TMP = $root; GOCACHE = "$root\go-build"; GOPATH = "$root\go" } `
        -RedirectStandardOutput $stdout -RedirectStandardError $stderr
    Get-Content $stdout
    Get-Content $stderr
    if ($process.ExitCode -ne 0) {
        Get-WinEvent -FilterHashtable @{ LogName = $log; StartTime = $started } |
            Where-Object { $_.ToXml().Contains($user.SID.Value) } |
            ForEach-Object { $_.ToXml() }
        throw "Native checks exited with $($process.ExitCode)"
    }
} finally {
    Remove-LocalUser -Name $name
}
