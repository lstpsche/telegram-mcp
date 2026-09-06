# Local credential storage and legacy Keychain migration

The default runtime stores credentials, Telegram sessions and cursor integrity
keys in `secrets.json` under its private state directory. The file is unencrypted.
Unix directories/files require owner-only permissions; Windows uses restricted
ACLs. Unsafe links, permissive access and malformed stores are refused. Updates
publish a complete file atomically under an exclusive lock. No vault password,
code-signing certificate or paid developer account is required for normal use.

The API ID, API hash, environment and Test DC form one versioned credential
bundle. Before constructing a Telegram client, the application verifies that
this bundle agrees with SQLite configuration. Test and production sessions occupy
separate fixed slots. Either session blocks reconfiguration. Login codes, phone
numbers, 2FA passwords and QR tokens are never persisted. Logout revokes remotely
before deleting the selected session; credentials and the integrity key remain.

A daemon restart reuses the local session. Missing or malformed credentials fail
explicitly, without reading Keychain or substituting another backend. A missing
integrity-key slot can be initialized only in an existing local store; this
invalidates old cursors. The file is excluded from metadata backups and release
archives. Anyone able to read it can obtain account access; OS user permissions
are the boundary, and full-disk encryption helps protect offline storage.

gotd requires the API hash as a Go string for the client's lifetime. The adapter
makes this immutable copy only inside `internal/telegram` and clears temporary
byte slices. This cannot guarantee erasure of all runtime or filesystem copies.

## Import an existing macOS installation

Stop the daemon and retain the old binaries and Keychain items until the new
runtime has been verified. Build the control command with cgo on macOS and sign
it using the identity and designated requirement already trusted by the old
items; see [legacy development signing](development-signing.md). Ordinary
portable archives deliberately do not contain this native adapter.

```sh
telegram-mcpctl migrate-keychain --accept-plaintext-storage
```

This explicit human operation reads the four fixed items under
`dev.telegram-mcp.gateway`: `default.credentials`, `default.session`,
`production.session`, and `default.cursor-integrity`. It verifies the credential
bundle against the existing metadata and requires the active session when an
authorization epoch exists. It writes all imported values atomically, refuses
any existing destination store, and retains every source item. It does not
connect to Telegram, reset grants/scopes, or change the authorization epoch.

A lost signing identity or denied Keychain access cannot be bypassed by the
migration command. Keep the working old installation until access is recovered.
Do not broaden Keychain ACLs or copy secrets through terminal output. A cgo-free
or non-macOS build reports that native migration is unavailable.

The legacy adapter uses Security.framework directly, restricts operations to the
current user's unlocked login keychain and disables synchronization and UI. It
never invokes `/usr/bin/security`. The deprecated login-keychain APIs remain
isolated to migration and its synthetic qualification tools.

## Probe

The isolated probe creates one random, non-sensitive account name and random
secret bytes. A parent process stores the item; a second invocation of the same
binary, with null stdin and a minimal launchd-like environment, reads, updates,
reads, deletes, and verifies deletion. The parent verifies deletion again. Both
processes clear their Go byte slices, and a deferred cleanup removes the
temporary item after failures.

Run it on macOS without Telegram credentials:

```sh
env GO111MODULE=on go build -o tmp/keychain-probe ./tools/keychain-probe
codesign --force --sign - tmp/keychain-probe
env -i HOME="$HOME" LANG=C PATH=/usr/bin:/bin TMPDIR=/tmp \
  ./tmp/keychain-probe </dev/null
```

Expected output contains only the static success statement. It never prints an
item name or secret.

## Legacy binary identity qualification

This optional legacy adapter matrix remains useful when changing the native
Keychain adapter. It is not required by the file-backed runtime or portable
release archives. It builds two cgo-enabled commands for synthetic probe actions;
the migration signing recipe separately needs only the control executable.

The same-artifact probe cannot establish sharing between the control command
and daemon or access after rebuilding either executable. To exercise those
identities, build the actual commands into two distinct private directories.
Use changed build metadata to ensure the upgrade artifacts differ:

```sh
(
set -eu
umask 077
mkdir -p tmp
for variant in current upgrade; do
  mkdir -m 700 "tmp/keychain-$variant"
  for command in telegram-mcpctl telegram-mcpd; do
    env GO111MODULE=on go build \
      -ldflags "-X github.com/lstpsche/telegram-mcp/internal/buildinfo.Version=synthetic-$variant" \
      -o "tmp/keychain-$variant/$command" "./cmd/$command"
    codesign --force --sign - "tmp/keychain-$variant/$command"
  done
done
env GO111MODULE=on go build -o tmp/keychain-probe ./tools/keychain-probe
codesign --force --sign - tmp/keychain-probe
env -i HOME="$HOME" LANG=C PATH=/usr/bin:/bin TMPDIR=/tmp \
  ./tmp/keychain-probe \
  --bin-dir "$PWD/tmp/keychain-current" \
  --upgrade-bin-dir "$PWD/tmp/keychain-upgrade" </dev/null
)
```

Both directories must be canonical, owned by the current user, and mode `0700`,
with safe ancestors. Each command must be an owned regular executable without
group/other write permissions and must pass `codesign --verify --strict`.
The probe verifies these properties and SHA-256 hashes before and after the
exercise. Use the intended signed distribution artifacts instead of ad-hoc
builds when qualifying a release. Do not replace artifacts while probing them.

The eight checks cover unchanged control/daemon restarts, sharing in both
directions, each command's upgrade, and sharing between the upgraded commands.
Each check creates a fresh random synthetic item, reads and updates it through
the second artifact, verifies the exact replacement through the creator, then
deletes it through the creator and verifies absence. Cleanup has an independent
deadline after a worker failure; an uncertain cleanup stops further checks.
Keep the creator binaries until cleanup is verified. An interrupted parent or
failed cleanup can leave a synthetic item; the probe cannot promise cleanup
after process termination.

The commands' `--keychain-probe ACTION TOKEN` entry point runs before account
or daemon construction. It accepts only fixed actions and a 32-character
lowercase hexadecimal token. Service `dev.telegram-mcp.keychain.qualification`,
the derived account name, and non-sensitive fixture bytes are fixed by code.
It cannot select real account items, accept credentials, construct a Telegram
client, or mutate policy. This human diagnostic is absent from the relay and
MCP tool surface. Workers have null stdin, a minimal environment, ten-second
deadlines and bounded output; results contain fixed categories and numeric
native statuses without item names, values, paths or raw errors.

The JSON report includes artifact hashes and each check's result and cleanup
status. Exit `0` requires all eight checks, verified cleanup and unchanged
artifacts. Any failure exits `1`; invalid qualification syntax exits `2`.
A `worker_timeout` reports a subprocess deadline, not a native access denial.

Separate ad-hoc-signed artifacts currently pass unchanged restarts but fail all
six sharing/upgrade checks with native status `-25293` (`errSecAuthFailed`).
With legacy interaction disabled, these failures return promptly and cleanup
is verified. They establish that this signing arrangement is unsuitable for
shared account custody. Do not approve Keychain prompts or broaden item ACLs
to make the checks pass. For local development, an Apple Development identity
with a certificate-pinned shared designated requirement passes all eight checks;
the [development signing recipe](development-signing.md) keeps the control and
daemon identifiers distinct and excludes the relay. This qualification applies
to rebuilt artifacts using the same certificate and requirement. Certificate
rotation and migration of items created under other requirements remain separate.
