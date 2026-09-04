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
`SecKeychainGetPath`, and `SecKeychainGetStatus`) and the noninteractive
`kSecUseAuthenticationUIFail` value are deprecated by Apple. They are used here
because the storage contract requires a named, unlocked login keychain and a
fail-closed CLI path. Release qualification must reevaluate this against the
minimum supported macOS version and a signed application identity; do not
silently replace it with an implicit or synchronizing store.

The account runtime uses one service, `dev.telegram-mcp.gateway`, with two
fixed accounts: `default.session` for gotd sessions and `default.credentials`
for a versioned bundle containing the API ID, API hash, and Test DC. Keychain
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
explicit reauthentication.

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
go build -o tmp/keychain-probe ./tools/keychain-probe
codesign --force --sign - tmp/keychain-probe
env -i HOME="$HOME" LANG=C PATH=/usr/bin:/bin TMPDIR=/tmp \
  ./tmp/keychain-probe </dev/null
```

Expected output contains only the static success statement. It never prints an
item name or secret.

## Native API evidence and signing consequence

On 2026-09-04, Go 1.27.1 produced an unsigned x86_64 Mach-O. After applying an
ad-hoc signature, `codesign -dvvv` reported `Signature=adhoc` and no team
identifier. The noninteractive cross-process store/read/update/delete probe
then passed against the unlocked login keychain, and `otool -L` confirmed
direct CoreFoundation and Security.framework linkage.

This proves the native API behavior for one unchanged development artifact. It
does not prove stable ACL identity across rebuilds or upgrades. Developer ID
signing, expected access across an upgraded binary, launchd installation, and
locked-keychain recovery remain explicit release gates.
