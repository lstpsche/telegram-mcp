# Local installation and diagnostics

Telegram MCP can run as a per-user macOS LaunchAgent in a logged-in GUI session.
Installation registers existing executables; it does not download, copy, sign,
or replace them. Run these commands as your normal user, without `sudo`.

Build with Go 1.27.1 into a new private directory at a stable, absolute path.
Do not use a temporary directory or overwrite binaries referenced by an existing
installation. For a development build, this example applies ad-hoc signatures:

```sh
artifact_dir="$HOME/Applications/telegram-mcp-dev-$(date +%Y%m%d%H%M%S)"
(
set -eu
mkdir -p "$HOME/Applications"
mkdir -m 700 "$artifact_dir"
env GO111MODULE=on go build -o "$artifact_dir/telegram-mcp" ./cmd/telegram-mcp
env GO111MODULE=on go build -o "$artifact_dir/telegram-mcpd" ./cmd/telegram-mcpd
env GO111MODULE=on go build -o "$artifact_dir/telegram-mcpctl" ./cmd/telegram-mcpctl
/usr/bin/codesign --force --sign - "$artifact_dir/telegram-mcp"
/usr/bin/codesign --force --sign - "$artifact_dir/telegram-mcpd"
/usr/bin/codesign --force --sign - "$artifact_dir/telegram-mcpctl"
"$artifact_dir/telegram-mcpctl" service install --bin-dir "$artifact_dir"
"$artifact_dir/telegram-mcpctl" service start
"$artifact_dir/telegram-mcpctl" doctor
"$artifact_dir/telegram-mcpctl" agent-config
)
```

Start is asynchronous. If the first `doctor` reports an absent socket, allow
startup to finish and run it again before generating agent configuration.
Persistent absence requires the foreground diagnostic procedure below.

The binary directory must be owned by the current user with mode `0700`. Each
executable must be a regular, current-user-owned executable with no group or
other write permission. Paths must be absolute, canonical, and free of symlinks.
Installation verifies each executable with `codesign --verify --strict`.
A valid signature establishes artifact integrity, not shared Keychain access:
separately ad-hoc-signed control and daemon artifacts fail the native sharing
and rebuild checks. This development setup supports credential-free connectivity
checks; it is not qualified for shared account custody. Cross-command identity,
Developer ID signing, upgrades, and actual LaunchAgent Keychain access require
separate qualification described in [Keychain behavior](keychain.md).
For local account development, use the verified
[Apple Development signing recipe](development-signing.md). It supplies shared
control/daemon access across rebuilds using one pinned certificate, while the
relay retains a separate identity. Qualify the exact artifacts before installation.

The generated file is
`~/Library/LaunchAgents/dev.telegram-mcp.gateway.plist`. It is the installation
record, contains only local executable/environment configuration, and must
retain its generated contents, ownership, and `0600` permissions. Existing or
altered files are never overwritten. Parent directories must have safe ownership
and permissions. Install does not start a daemon in the current session;
`service start` explicitly submits it to `gui/<uid>`.

`agent-config` prints a standard MCP JSON configuration containing the exact
installed relay path. Copy that JSON into the client's configuration, or register
the same absolute relay path with your client's stdio setup command. It does not
edit client settings. The relay stays byte-only and never starts the daemon.
When the daemon stops or restarts, existing relay processes exit with an error.
Reconnect the client after `doctor` confirms the new daemon is responding.
The relay also exits on cancellation when stdin is idle or stdout is blocked;
it never reconnects itself or replays a request.

An unconfigured daemon can answer MCP `status` with `reauth_required` and
`message_reads: false`; credentials are not needed for this connectivity check.

Use the lifecycle commands explicitly:

```sh
"$artifact_dir/telegram-mcpctl" service stop
# Configure/authenticate through the existing interactive commands while stopped.
"$artifact_dir/telegram-mcpctl" service start
"$artifact_dir/telegram-mcpctl" service restart
"$artifact_dir/telegram-mcpctl" service stop
"$artifact_dir/telegram-mcpctl" service uninstall
```

Stop unloads the exact service in the current GUI session and waits for its
registration to disappear. The plist's `RunAtLoad` setting loads it again at the
next GUI login; uninstall removes that registration file permanently. Restart
unloads and bootstraps the job. There is no forced `kickstart -k`, automatic
restart loop, or automatic authentication retry. Launchd allows 20 seconds for
termination before its own timeout enforcement. Lifecycle operations have a
30-second deadline. A successful start means the launch request was accepted;
use `doctor` to establish whether MCP actually responds.

Uninstall requires the service to be unloaded and removes only the validated
plist. Stopping and uninstalling remain possible if an old binary was removed
or its signature became invalid; both still require the canonical owned plist.
It retains binaries, metadata, runtime files, and Keychain items.
Missing or already-running states are explicit errors rather than silent success.
A failed command can leave a partial OS operation; inspect the actual state
before retrying. Do not assume an error rolled back a submitted launch.

For an upgrade, prepare a new binary directory, qualify its signing and Keychain
identity, stop and uninstall the old registration, then install and start the
new directory. Update client configuration to its new relay path. The installer
does not restore old binaries automatically after an error. Retained metadata
uses forward-only migrations, so installing an older executable is not a general
recovery procedure.

`doctor` is read-only. It checks existing state/runtime directory permissions,
metadata and lock-file ownership, the socket's ownership and connected peer,
and a bounded MCP initialize/status exchange. It neither opens SQLite nor
acquires account locks, accesses Keychain, constructs a Telegram client, or
fetches Telegram data. File presence does not establish that a lock is held.
The result includes only fixed file names/states, socket state, local account
state, readiness booleans, and a fixed next-action hint. It does not print paths,
account identifiers, server instructions, grants, content, or remote error text.

The diagnostic JSON uses `keychain: "not_checked"` and
`metadata: "filesystem_only"` inside `runtime` to make its limits explicit.
An absent or refused socket yields an observed `absent` or `stale` state with
null account/readiness fields. Unsafe paths, inconclusive connections, malformed
or oversized replies, and timeouts fail without a partial JSON report. The MCP
probe lasts at most three seconds and reads at most 128 KiB total, with 32 KiB
per frame. It calls only `status`.

Exit status `0` means the installation is valid and MCP returned a valid status.
It does not mean content is authorized or the account is ready. Exit `1` means
a missing installation/socket or a failed check; exit `2` means invalid command
syntax. `next_action` is one of `install_service`, `check_service_startup`,
`inspect_account_readiness`, or `connect_agent`. Follow account recovery and
grant instructions before asking for content, even when connectivity succeeds.

The LaunchAgent directs stdout/stderr to `/dev/null` and retains no daemon log
file. To inspect startup failures, stop the service and run the installed
`telegram-mcpd` in a terminal; it emits the existing fixed, safe diagnostics to
stderr. If the service has already been unloaded, `service stop` reports that
fact. Start the service again after resolving the cause and stopping the
foreground process. Authentication remains interactive through `/dev/tty` and
must run while the daemon is stopped.

Automated service verification uses temporary files, a fake process runner,
and a local synthetic MCP server. Native synthetic Keychain checks additionally
qualify local Apple Development signing across commands and rebuilt upgrades.
To verify a real GUI-session installation without credentials, use a fresh
unconfigured account state. Start the service, check `doctor`, connect the
installed relay from an MCP client, list tools, and call `status`. Expect all
eight tools, `reauth_required`, and `message_reads: false`; data operations
report `not_ready`. Keep a relay connected during restart and verify that it
exits, then connect a new relay. Check stop, uninstall, and installation from a
new artifact directory; `agent-config` must name the new relay. After final
stop/uninstall, the job and socket must be absent and the account lock available.
Retained metadata and binaries are expected. Do not run this unconfigured check
against an account that already has configuration or authorization.

GUI-session connectivity does not establish account Keychain access under
launchd, disposable Test-DC workflows, production eligibility, or publication.
Those remain separate checks. Production login requires explicit eligibility
attestation and [interactive local authentication](../README.md#configure-an-account).
