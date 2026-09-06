# Local installation and diagnostics

Telegram MCP runs as a per-user background service on macOS, Linux and Windows.
The default uses private local credential files: no paid certificate, Keychain
unlock, vault password or repeated Telegram login is required after setup.
Read the [storage threat model](threat-model.md) before enabling account access.

Homebrew users on macOS or Linux can install with:

```sh
brew install lstpsche/tap/telegram-mcp
telegram-mcp install --version 0.3.0 --setup
```

Homebrew supplies checksum-pinned platform binaries. The second command creates
private copies outside the Cellar, then starts the human setup guide. Use the
stable relay path printed by setup or
`agent-config`, not the Homebrew symlink, in your MCP client. Do not run `setup`
directly from the Cellar or use `brew services`: the private installation and
Telegram MCP's service manager own the runtime.

After `brew upgrade lstpsche/tap/telegram-mcp`, run the managed `upgrade --version`
command shown in `brew info lstpsche/tap/telegram-mcp`, then reconnect clients.
Homebrew upgrade/cleanup/uninstall does not mutate private credentials, grants or
service registration. If removing the runtime, stop and uninstall its service
using its private control program; removing the formula alone leaves it running.
Other taps distribute unrelated projects called `telegram-mcp`; use the fully
qualified name and inspect any existing same-name installation before replacing it.

For a managed release installation, download `install.sh` (macOS/Linux) or
`install.ps1` (Windows) from the desired GitHub release and run it as your normal
user. The script verifies a standalone control executable, which downloads and
verifies the full archive before starting guided setup. The scripts accept an
explicit stable version; release assets default to their packaged version.
The source scripts default to 0.3.0. They do not require Go.

The equivalent control command is `telegram-mcp install --version 0.3.0 --setup`.
Omit `--setup` to prepare files without touching account or service state.
Managed versions live under the OS user configuration directory at
`Telegram MCP/install/versions/X.Y.Z`. Existing versions are checked in full and
never overwritten. Both archive and payload checksums must match; downloads,
file counts and extracted sizes are bounded. HTTPS and release checksums provide
transport and file integrity, not independent publisher authentication.

For manual archive installation, follow the requirements below.

Install into a new private directory at a stable, absolute path. Installation
registers existing executables; it does not download, copy, sign or replace them.
Do not overwrite binaries referenced by an existing installation. Run as your
normal user, without `sudo` or an elevated Windows terminal.

After preparing the binaries, run `telegram-mcp setup` from that directory.
The interactive guide selects production or an explicit Test DC, collects API
credentials and phone/QR authentication, offers access settings, starts the
service and waits for account readiness. Obtain your own API ID and hash at
https://my.telegram.org before starting. Input is hidden and comes directly
from the OS console.

Setup preserves recorded authentication and access settings when resuming.
It asks before stopping an existing service or enabling Full read access.
If interrupted, completed changes remain; inspect `status` and `doctor`, then
run setup again. A running foreground daemon must be stopped separately.
The guide can print standard MCP JSON or, after showing the proposed command,
register a new `telegram` server with the installed Codex CLI. An existing
Codex registration is not overwritten. Reconnect clients after service startup.

For macOS or Linux, build from source using Go 1.27.1:

```sh
artifact_dir="$HOME/Applications/telegram-mcp-local"
mkdir -p "$HOME/Applications"
mkdir -m 700 "$artifact_dir"
env GO111MODULE=on CGO_ENABLED=0 go build -o "$artifact_dir/telegram-mcp" ./cmd/telegram-mcp
env GO111MODULE=on CGO_ENABLED=0 go build -o "$artifact_dir/telegram-mcpd" ./cmd/telegram-mcpd
"$artifact_dir/telegram-mcp" service install --bin-dir "$artifact_dir"
"$artifact_dir/telegram-mcp" service start
"$artifact_dir/telegram-mcp" doctor
"$artifact_dir/telegram-mcp" agent-config
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
& "$artifactDir\telegram-mcp.exe" service install --bin-dir $artifactDir
& "$artifactDir\telegram-mcp.exe" service start
& "$artifactDir\telegram-mcp.exe" doctor
& "$artifactDir\telegram-mcp.exe" agent-config
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

Managed installations publish a stable regular `telegram-mcp` entry directly
under `Telegram MCP/install`. Use it for human subcommands and, without arguments,
for MCP stdio. It delegates to the version selected by the
validated service registration; no version-pointer file or symlink is used.
Without arguments the stable entry only transfers bytes to the existing account
endpoint. It is not overwritten during upgrades. `agent-config` selects this stable relay path.

Run `telegram-mcp upgrade --version X.Y.Z` through the stable control program.
The command verifies a complete release and both program version identities
before stopping the old service. It installs and starts the new registration,
waits for a responding MCP server, and retains old binaries and account data.
Managed downgrades are refused because metadata migrations are forward-only.
After a failed activation, inspect `doctor`; rerunning the same upgrade can
continue from an absent or already-selected registration. No rollback or
content-readiness claim is made on failure. Existing manual installations can
be explicitly adopted through this command; update client paths once to the
stable relay. Subsequent managed upgrades need only client reconnection.

For a manual upgrade, prepare a fresh binary directory, stop and uninstall the old
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

On Windows, use an ordinary, non-elevated user shell. Do not run setup or the
daemon with Run as administrator: elevated tokens can assign group ownership
to SQLite auxiliary files, outside the per-user storage contract.

Windows CI runs the full suite, console input and real task registration/removal
as a standard user, including a Unicode installation path. Its secondary logon
has no interactive desktop session, so it cannot qualify scheduled-process
startup. In a disposable non-elevated Windows desktop login with no existing
Telegram MCP installation, run
`$env:TELEGRAM_MCP_NATIVE_SERVICE_TEST='1'; go test -count=1 -v ./internal/cli ./internal/service`
to additionally verify task start, restart and stop. Desktop startup and reboot
acceptance remain separate from the hosted CI checks.
