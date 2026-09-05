# Account runtime verification

- Scope: single-account Test-DC runtime, authentication, and MCP connectivity
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
- atomic credential bundles, failure/cancellation between stores, refusal of
  inconsistent configuration, and recovery by reconfiguration;
- metadata-only configuration, Test-DC checks, epoch rotation, restart reuse,
  and logout invalidation;
- bounded request concurrency plus rate/flood middleware configuration;
- sanitized stdout/stderr and rejection of secret-bearing auth arguments;
- startup and post-start authorization loss, daemon cancellation, socket
  removal, database close, and lock release;
- MCP initialization, static tool discovery, sanitized status, strict inputs,
  bounded frames, concurrent relays, disconnects, and cancellation through SDK
  and independent JSON-RPC clients.

## Human acceptance procedure

Automated checks do not establish that the following checks have passed on a
real account or a release binary. Record results against the exact binary in
external acceptance notes; historical database method observations are not
release approval.

These checks require a human-owned Telegram application credential and a
pre-registered disposable Test-DC account. They are not simulated by the unit
suite and must not use production or private account data.

- Phone login succeeds on Test DC 1-3.
- Phone login with 2FA succeeds where enabled.
- Restart reuses the Keychain session without prompting.
- QR login succeeds from a Test-DC-authorized scanning client.
- `telegram-mcpctl status` records each method only after that method ran.
- Logout revokes remotely, deletes the Keychain session, and removes the
  active epoch.
- A launchd-like invocation can reuse the signed binary's Keychain item.

Passing both method checks does not enable production login. Production remains
a separate, explicit authorization and implementation decision.

MCP consumer acceptance additionally requires registering the built relay in a
real agent client, discovering the six static tools, and verifying unavailable
message access before login. After separately establishing eligibility and a
scoped grant, discover the allowed chat, read history and zero-neighbor context,
and verify the actual read receipt in another Telegram client. Revoke the grant
and verify the next call is denied. Restart and verify epoch-bound metadata and
grants are reused only with the same authorization. Automated synthetic wire
tests do not substitute for this client acceptance.

Synthetic coverage also verifies exact-peer search -> signed continuation ->
context, snippet bounds, unread metadata without receipts, cursor expiry and
query/peer/limit/epoch/policy mismatch, regrant invalidation, and preservation
of audit records during migration. The human acceptance workflow should search
a known eligible message, open its context, observe the resulting receipt, and
check unread metadata separately. Search/unread calls must not issue receipts.

Synthetic recovery tests run the pinned updates manager against encoded RPC
fixtures and SQLite metadata. They cover sliced startup and restart from the
last accepted checkpoint; post-start gap completion, failure and cancellation;
unrecoverable, malformed, non-progressing and oversized differences; failed
hash/checkpoint writes; rejection of later writes after failure; bounded
recovery calls; and complete versus minimal or zero hash refresh across
restart. Corrupt checkpoints fail without replacement RPCs.

Reader and stdio tests additionally verify live search continuation after
service reconstruction, edited/protected/deleted context targets, account
rotation/logout invalidation, and readiness transitions for all six tools.
Unavailable data tools release no result and cause no fetch or receipt. These
fixtures establish local behavior, not real network reconnect or Telegram
client acceptance.
