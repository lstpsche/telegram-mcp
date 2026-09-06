# Local installation and diagnostics

Telegram MCP runs as a per-user background service on macOS, Linux and Windows.
The default uses private local credential files: no paid certificate, Keychain
unlock, vault password or repeated Telegram login is required after setup.
Read the [storage threat model](threat-model.md) before enabling account access.

Install into a new private directory at a stable, absolute path. Installation
registers existing executables; it does not download, copy, sign or replace them.
Do not overwrite binaries referenced by an existing installation. Run as your
normal user, without `sudo` or an elevated Windows terminal.

For macOS or Linux, build from source using Go 1.27.1:

```sh
artifact_dir="$HOME/Applications/telegram-mcp-local"
mkdir -p "$HOME/Applications"
mkdir -m 700 "$artifact_dir"
env GO111MODULE=on CGO_ENABLED=0 go build -o "$artifact_dir/telegram-mcp" ./cmd/telegram-mcp
env GO111MODULE=on CGO_ENABLED=0 go build -o "$artifact_dir/telegram-mcpd" ./cmd/telegram-mcpd
env GO111MODULE=on CGO_ENABLED=0 go build -o "$artifact_dir/telegram-mcpctl" ./cmd/telegram-mcpctl
"$artifact_dir/telegram-mcpctl" service install --bin-dir "$artifact_dir"
"$artifact_dir/telegram-mcpctl" service start
"$artifact_dir/telegram-mcpctl" doctor
"$artifact_dir/telegram-mcpctl" agent-config
```

No additional signing command is needed for these source builds. The directory
must be owned by the current user with mode `0700`; executables must be regular,
owned executable files without group or other write permission. Paths must be
absolute and canonical, without symlinks or writable ancestry.

For Windows PowerShell, create an owner-only directory before building. This
example deliberately creates a fresh directory and removes inherited access:

```powershell
$artifactDir = Join-Path $env:LOCALAPPDATA 'telegram-mcp-local'
New-Item -ItemType Directory -Path $artifactDir -ErrorAction Stop | Out-Null
$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
$acl = [System.Security.AccessControl.DirectorySecurity]::new()
$acl.SetOwner($sid)
$acl.SetAccessRuleProtection($true, $false)
$rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
  $sid, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
$acl.AddAccessRule($rule)
Set-Acl -LiteralPath $artifactDir -AclObject $acl -ErrorAction Stop
$env:GO111MODULE = 'on'
$env:CGO_ENABLED = '0'
go build -o "$artifactDir\telegram-mcp.exe" ./cmd/telegram-mcp
go build -o "$artifactDir\telegram-mcpd.exe" ./cmd/telegram-mcpd
go build -o "$artifactDir\telegram-mcpctl.exe" ./cmd/telegram-mcpctl
& "$artifactDir\telegram-mcpctl.exe" service install --bin-dir $artifactDir
& "$artifactDir\telegram-mcpctl.exe" service start
& "$artifactDir\telegram-mcpctl.exe" doctor
& "$artifactDir\telegram-mcpctl.exe" agent-config
```

Prebuilt archives and their platform verification requirements are described in
[distribution](distribution.md). Windows binaries and their parent directory
must retain an owner-only DACL; Unix `chmod` does not establish that protection.
Windows installation paths cannot contain `%`, which Task Scheduler expands as
an environment-variable marker.

The service starts at user login, using the following OS facility:

| Platform | Registration | Requirement |
| --- | --- | --- |
| macOS | `~/Library/LaunchAgents/dev.telegram-mcp.gateway.plist` | Logged-in GUI session; launchd |
| Linux | `~/.config/systemd/user/dev.telegram-mcp.gateway.service` | Running systemd user manager and `/usr/bin/systemctl` |
| Windows | Task Scheduler task `dev.telegram-mcp.gateway-<current-user-SID>` | Interactive user logon; Task Scheduler and Windows PowerShell |

Linux units are enabled for `default.target`. The unit location follows
`XDG_CONFIG_HOME` when set; otherwise it uses the path shown above. The generated
unit pins the installer's configuration and cache locations for the daemon. Windows uses a logon trigger bound
to the current user's SID and an interactive token at normal privilege; it stores
no Windows password. Its generated XML is retained in Telegram MCP's user
configuration directory. Windows service registration requires the local user's
permission to create scheduled tasks; organizational policy may prohibit it.
A Linux system without a systemd user manager can run the daemon in the foreground
or configure its own supervisor; the service command reports that failure.

The generated service configuration is the installation record and must retain
its canonical contents and private permissions. Existing or altered records are
never overwritten. If Linux or Windows registration fails after writing the
record, rerun the identical `service install --bin-dir ...`: it can register that
verified record only when the OS confirms no enabled registration exists.
A different binary directory requires explicit uninstall first. External failures
can leave partial registration; a failed command does not claim rollback.

`service start` submits a launch request. A successful return does not establish
MCP readiness; run `doctor` after startup. An unconfigured daemon can answer MCP
`status` with `reauth_required` and `message_reads: false` without credentials.
Configure and authenticate through the human commands while the daemon is stopped.
Legacy Keychain installations require the explicit [migration](keychain.md).

`agent-config` prints standard MCP JSON containing the exact installed relay
path. Copy it into the client's configuration or use that path with the client's
stdio setup command. It does not edit client settings. The relay stays byte-only
and never launches the daemon, reconnects or replays a request. When the daemon
restarts, reconnect the client after `doctor` reports a responding daemon.

Lifecycle commands are `service start`, `service stop`, `service restart` and
`service uninstall`. Stop affects the current login session; the retained
registration starts the daemon at the next login. Restart stops before starting.
No backend repeatedly retries failed account authentication. Launchd and systemd
allow 20 seconds for termination before their timeout enforcement. All lifecycle
operations have a 30-second deadline.

Windows Task Scheduler stop can terminate the process without a graceful Go
shutdown. The command waits for the task to stop and the account lock to become
available. Atomic credential publication and SQLite transactions protect committed
storage; this does not promise completion of an in-flight MCP request. An existing
relay may fail and must be reconnected. Uninstall also refuses a held account lock
on Linux and Windows. Do not start maintenance while another daemon is running.

Uninstall requires the service to be stopped. It removes only the validated
registration and its generated local file. It retains executables, metadata,
credentials, sessions and runtime files. Stop and uninstall remain possible when
an old executable has been removed or has unsafe permissions. Missing or already
running states are explicit errors.

For an upgrade, prepare a fresh binary directory, stop and uninstall the old
registration, then install and start the new directory. Update the MCP client's
relay path. The installer does not restore old binaries after an error. Metadata
migrations are forward-only; downgrading is not a general recovery procedure.

`doctor` is read-only. It checks filesystem metadata and private transport, then
performs a bounded MCP initialize/status exchange. It never opens SQLite, acquires
account locks, reads credential files, constructs a Telegram client or fetches
Telegram data. File presence does not prove that a lock is held. Output contains
fixed file names/states, transport state, account readiness booleans and a fixed
next-action hint, without paths, account identifiers, grants, content or remote
error text. `secrets: "not_checked"` and `metadata: "filesystem_only"` make those
limits explicit.

An absent or refused Unix socket produces `absent` or `stale` with null account
and readiness fields. Windows named pipes have no stale filesystem node. Unsafe
paths, inconclusive connections, malformed replies and timeouts fail without a
partial JSON report. The probe lasts at most three seconds, reads at most 128 KiB
in total and limits each frame to 32 KiB. It calls only `status`.

Exit `0` means the installation is valid and MCP returned valid status; it does
not establish content authorization. Exit `1` indicates missing installation,
unavailable transport or failed checks; exit `2` indicates invalid syntax.
`next_action` is `install_service`, `check_service_startup`,
`inspect_account_readiness` or `connect_agent`.

The launchd and systemd configurations discard daemon output. To diagnose startup,
stop the service and run the installed `telegram-mcpd` in a terminal to see its
safe stderr diagnostics. Windows Task Scheduler does not capture a log file.
Authentication uses the OS console and remains interactive only during setup or
explicit account reauthentication.

Automated service checks use temporary files, fake OS process runners and a local
synthetic MCP server. Native service-manager acceptance is separate: using fresh
unconfigured state, install/start, run `doctor`, connect the relay and call
`status`, restart with a connected relay, reconnect, then stop/uninstall. Verify
that only the expected registration is removed and retained metadata and binaries
remain. Do not run that fresh-state exercise against an existing account.
