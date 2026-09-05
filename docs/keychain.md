# Native macOS Keychain boundary

## Decision

Telegram MCP uses a small cgo adapter over Security.framework. It stores generic
password items in the current user's unlocked login keychain, sets
`kSecAttrSynchronizable` to false, and restricts searches to that keychain. It
fails if the default keychain is not `login.keychain`/`login.keychain-db`, if it
is locked, or if an operation would require UI. There is no file backend and no
invocation of `/usr/bin/security`.

The evaluated `github.com/keybase/go-keychain` v0.0.1 candidate was rejected.
Its public item API exposes neither `kSecUseKeychain` nor
`kSecUseDataProtectionKeychain`. Although it exposes `kSecAttrAccessible`,
macOS does not support that attribute for a non-synchronizing legacy Keychain
item. The candidate therefore cannot both select the named login keychain and
request Data Protection Keychain accessibility. The adapter deliberately uses
the named legacy login keychain's lock state instead. Removing the wrapper also
avoids retaining an unnecessary dependency.

The explicit login-keychain APIs (`SecKeychainCopyDefault`,
`SecKeychainGetPath`, `SecKeychainGetStatus`, and
`SecKeychainSetUserInteractionAllowed`) and the noninteractive
`kSecUseAuthenticationUIFail` value are deprecated by Apple. They are used here
because the storage contract requires a named, unlocked login keychain and a
fail-closed CLI path. Release qualification must reevaluate this against the
minimum supported macOS version and a signed application identity; do not
silently replace it with an implicit or synchronizing store.

Before opening the keychain, the adapter disables legacy Keychain interaction
process-wide with `SecKeychainSetUserInteractionAllowed(false)`. Failure to set
that control aborts the operation. It stays disabled because every caller
requires noninteractive access. The per-query UI-fail value alone does not
prevent the legacy backend from waiting for ACL interaction. Neither control
grants access or changes an item's trusted applications.

The account runtime uses one service, `dev.telegram-mcp.gateway`, with three
fixed accounts: `default.session` for gotd sessions and `default.credentials`
for a versioned bundle containing the API ID, API hash, and Test DC;
`default.cursor-integrity` holds an independent 32-byte search-cursor key. Keychain
replaces the credential bundle atomically. SQLite stores only non-secret
API ID/DC metadata. Before creating a Telegram client, the application verifies
that the two stores agree and uses the complete Keychain bundle as input.

If a configuration write is interrupted, inconsistent metadata causes account
operations to fail before any Telegram client is constructed. Stop the daemon
and run `telegram-mcpctl configure --test-dc N` again to recover. Existing
sessions and authorization epochs continue to prevent reconfiguration.
The implementation does not claim that SQLite and Keychain share a transaction.

Login codes, phone numbers, 2FA passwords, and QR tokens are never stored.
Logout deletes the session item; the credential bundle remains available for
explicit reauthentication. The cursor key also survives logout and restart;
authorization-epoch binding invalidates old cursors after account changes.
The daemon loads or initializes this key under the account lock only after a
recorded epoch exists. A missing item initializes a new key and invalidates old
cursors; malformed or inaccessible items fail without replacement. No key is
stored in SQLite or files.

gotd's client constructor requires the API hash as a Go string and retains it
for that client's lifetime. Telegram MCP performs this unavoidable immutable copy
only inside `internal/telegram`; it is never logged, returned, or persisted
outside Keychain. The temporary byte slice read from Keychain is still cleared
immediately after construction.

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

## Binary identity qualification

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

The [local installer](installation.md) verifies executable signatures and
registers a LaunchAgent, but never reads or changes Keychain items. Actual
LaunchAgent execution and locked-keychain recovery remain separate checks.
Developer ID distribution needs qualification against its own signed artifacts;
local Apple Development results do not establish distribution readiness.

Apple's [macOS Keychain overview](https://developer.apple.com/documentation/technotes/tn3137-on-mac-keychains)
distinguishes the legacy file keychain from Data Protection Keychain access
groups. [Code-signing requirements](https://developer.apple.com/library/archive/technotes/tn2206/)
describe identity-based access decisions. Sharing a signing team alone must
not be assumed to grant access under the legacy item's ACL.
