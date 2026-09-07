# Download the verified control program; it installs and checks the full release.
param([string]$Version = '1.1.0')
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($Version -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') { throw 'Use a stable X.Y.Z version.' }
$identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [System.Security.Principal.WindowsPrincipal]::new($identity)
if ($principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run as your normal user, without elevation.' }
$arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($arch) { 'x64' { $arch = 'amd64' }; 'arm64' { }; default { throw 'Unsupported Windows architecture.' } }
$bootstrapDir = Join-Path ([Environment]::GetFolderPath('UserProfile')) ('.telegram-mcp-bootstrap-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $bootstrapDir -ErrorAction Stop | Out-Null
try {
    $acl = [System.Security.AccessControl.DirectorySecurity]::new()
    $acl.SetOwner($identity.User)
    $acl.SetAccessRuleProtection($true, $false)
    $acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new($identity.User, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow'))
    Set-Acl -LiteralPath $bootstrapDir -AclObject $acl
    Add-Type -AssemblyName System.Net.Http
    function Save-ReleaseFile([string]$Url, [string]$Destination, [long]$Limit) {
        $handler = [System.Net.Http.HttpClientHandler]::new()
        $handler.AllowAutoRedirect = $false
        $client = [System.Net.Http.HttpClient]::new($handler)
        $client.Timeout = [TimeSpan]::FromMinutes(5)
        $deadline = [Threading.CancellationTokenSource]::new([TimeSpan]::FromMinutes(5))
        try {
            for ($redirect = 0; $redirect -le 5; $redirect++) {
                if (([Uri]$Url).Scheme -ne 'https') { throw 'Release downloads require HTTPS.' }
                $response = $client.GetAsync($Url, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead, $deadline.Token).GetAwaiter().GetResult()
                if ([int]$response.StatusCode -ge 300 -and [int]$response.StatusCode -lt 400) {
                    $next = [Uri]::new([Uri]$Url, $response.Headers.Location)
                    $response.Dispose()
                    $Url = $next.AbsoluteUri
                    continue
                }
                try {
                    $response.EnsureSuccessStatusCode() | Out-Null
                    $inputStream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
                    $outputStream = [IO.File]::Open($Destination, [IO.FileMode]::CreateNew)
                    try {
                        $buffer = [byte[]]::new(65536)
                        [long]$total = 0
                        while (($count = $inputStream.ReadAsync($buffer, 0, $buffer.Length, $deadline.Token).GetAwaiter().GetResult()) -gt 0) {
                            $total += $count
                            if ($total -gt $Limit) { throw 'Release download exceeds size limit.' }
                            $outputStream.Write($buffer, 0, $count)
                        }
                    } finally { $outputStream.Dispose(); $inputStream.Dispose() }
                    return
                } finally { $response.Dispose() }
            }
            throw 'Too many release redirects.'
        } finally { $deadline.Dispose(); $client.Dispose(); $handler.Dispose() }
    }
    $base = "https://github.com/lstpsche/telegram-mcp/releases/download/v$Version"
    $asset = "telegram-mcp-$Version-windows-$arch.exe"
    $checksums = Join-Path $bootstrapDir 'SHA256SUMS'
    $control = Join-Path $bootstrapDir 'control.exe'
    Save-ReleaseFile "$base/SHA256SUMS" $checksums 65536
    Save-ReleaseFile "$base/$asset" $control 134217728
    $lines = @(Get-Content -LiteralPath $checksums | Where-Object { $_ -cmatch ('^[a-f0-9]{64}  ' + [regex]::Escape($asset) + '$') })
    if ($lines.Count -ne 1) { throw 'Invalid release checksum list.' }
    $expected = $lines[0].Substring(0, 64)
    if ((Get-FileHash -LiteralPath $control -Algorithm SHA256).Hash.ToLowerInvariant() -cne $expected) { throw 'Control executable checksum mismatch.' }
    & $control install --version $Version --setup
    if ($LASTEXITCODE -ne 0) { throw 'Installation or setup failed; completed installation files were retained.' }
} finally { Remove-Item -LiteralPath $bootstrapDir -Recurse -Force -ErrorAction Stop }
