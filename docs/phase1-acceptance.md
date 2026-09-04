# Phase 1 acceptance record

- Date: 2026-09-04
- Scope: secure single-account Test-DC runtime and authentication
- Production login: disabled in code

## Automated evidence

Run from the repository root:

```sh
go build ./cmd/...
go test ./...
go vet ./...
```

The suite covers:

- exclusive lock and authorization checks before any secret prompt;
- second-owner rejection and lock reuse after shutdown;
- `0700` state/runtime directories and `0600` lock/database/socket nodes;
- symlink, non-socket, wrong-permission, overlong, active, inconclusive-probe,
  and verified-stale socket paths;
- Keychain-to-gotd session not-found/store/load/existence/delete semantics;
- metadata-only configuration, Test-DC checks, epoch rotation, restart reuse,
  and logout invalidation;
- bounded request concurrency plus rate/flood middleware configuration;
- sanitized stdout/stderr and rejection of secret-bearing auth arguments;
- startup and post-start authorization loss, daemon cancellation, socket
  removal, database close, and lock release.

## Human Test-DC evidence

These checks require a human-owned Telegram application credential and a
pre-registered disposable Test-DC account. They are not simulated by the unit
suite and must not use production or private account data.

- [ ] Phone login succeeds on Test DC 1-3.
- [ ] Phone login with 2FA succeeds where enabled.
- [ ] Restart reuses the Keychain session without prompting.
- [ ] QR login succeeds from a Test-DC-authorized scanning client.
- [ ] `tg-contextctl status` records each method only after that method ran.
- [ ] Logout revokes remotely, deletes the Keychain session, and removes the
  active epoch.
- [ ] A launchd-like invocation can reuse the signed binary's Keychain item.

Passing both method checks does not enable production login. Production remains
a separate, explicit later-phase authorization and implementation decision.
