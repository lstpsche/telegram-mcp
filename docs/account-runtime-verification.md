# Account runtime verification

The runtime supports one explicitly configured production or Test-DC account.
Automated evidence uses synthetic data and establishes no real-account login
or content acceptance.

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
- Local-store-to-gotd session not-found/store/load/existence/delete semantics;
- atomic credential bundles, failure/cancellation between stores, refusal of
  inconsistent configuration, and recovery by reconfiguration;
- metadata-only environment configuration, method checks, epoch rotation, restart reuse,
  and logout invalidation;
- bounded request concurrency plus rate/flood middleware configuration;
- cancelled flood waits release their invocation without retaining per-method
  delays on fresh requests after the server's requested wait has elapsed;
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

For first-use acceptance, have someone unfamiliar with the project follow the
README from a clean OS user profile and record the OS, architecture, exact
version, installation method, elapsed time, and points where outside help was
needed. Keep credentials and Telegram content out of those notes.

1. Install using the documented release or Homebrew path and complete setup
   with an explicitly selected Test DC and disposable account. Confirm that the
   printed relay path matches the client registration.
2. Grant one known self-authored Saved Message with no image permission and a
   read-through ceiling of zero. Record the exact peer locally, then create a
   `project` scope containing that peer. Use `scope --name project --peer ID`
   or the guided `scope setup` command.
3. In the actual agent client, check `status`, discover `project` with
   `list_scopes`, and verify one eligible peer. Search for a known word in the
   granted message, then use `catch_up` with an explicit UTC window containing
   it. Inspect coverage and exclusions. Search and catch-up must not mark it read.
4. Attempt to open context under the search-only grant and confirm denial with
   no body released. A later receipt-enabled test needs separate explicit
   permission for the affected dialog prefix; verify the actual receipt in a
   Telegram client when performing that test.
5. Restart the service and reconnect the agent. Confirm the scope and grant are
   retained. Revoke the grant and confirm the scope reports the peer as excluded
   and content is unavailable. A scope must not restore revoked authority.
6. On a source build, rerun `scope setup`, choose the existing name, and decline
   the final replacement preview. Confirm unchanged membership and stable ID.
   Then confirm a replacement and verify the same ID with exactly the selected
   members. Resuming setup with scope setup skipped must retain existing scopes.

This procedure is a human acceptance gate, not a claim that it has passed.
Synthetic CLI tests cover fresh configuration, restricted-grant-to-scope setup,
client handoff, replacement identity, cancellation, invalid input and local
metadata errors; they do not establish fresh-person usability.

These checks require a human-owned Telegram application credential and an
existing account in the selected environment. Use disposable Test-DC fixtures
for test acceptance. Ordinary-account acceptance requires separately established
eligibility, explicit production configuration, and a narrowly scoped data plan.
Enter every credential through the local no-echo terminal, never through an agent.
Verify the exact control/daemon artifacts and their checksums before adding
credentials. Published packages are unsigned; do not record them as signed or
notarized acceptance.

- Explicit configuration selects the intended environment before prompting.
- Phone login succeeds in the selected environment.
- Phone login with 2FA succeeds where enabled.
- Restart reuses the local session without prompting.
- QR login succeeds from a scanning client authorized in the same environment.
- `telegram-mcp status` records each method only after that method ran.
- Logout revokes remotely, deletes the local session, and removes the
  active epoch.
- A background service invocation can reuse the local session without prompting.

Method checks are bound to the configured environment. Passing them establishes
neither content eligibility nor authority; configuration and grants remain
separate human actions. Production capability alone does not establish live
network behavior or release acceptance.

MCP consumer acceptance additionally requires registering the built relay in a
real agent client, discovering the advertised tools, and verifying unavailable
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
restart. Corrupt checkpoints fail without replacement RPCs. Mixed channel/common
fixtures exercise the actual updates manager during recovery, live delivery,
and restart: common checkpoints and outer sequence advance without channel RPCs
or retained channel metadata. Oversized channel events still count against
recovery limits. Migration tests preserve existing test authorization, grants,
and common checkpoints while removing obsolete channel metadata. Account tests
verify legacy test credentials, explicit environment validation, isolated session
items, restart reuse, logout, and rejection of mixed stores before client creation.

Reader and stdio tests additionally verify live search continuation after
service reconstruction, edited/protected/deleted context targets, account
rotation/logout invalidation, and readiness transitions for the text and metadata tools.
Unavailable data tools release no result and cause no fetch or receipt. These
fixtures establish local behavior, not real network reconnect or Telegram
client acceptance.

Synthetic image tests cover explicit permission, safe captionless metadata,
source-bound handle invalidation, exact-source replacement/deletion, bounded
reference renewal and DC pools, JPEG/PNG corruption and pixel/byte limits,
receipt and audit failures, and native image content through the stdio relay.
The client test opens a scoped photo and a no-caption PNG larger than the input
frame limit while preserving source identity and exact delivered bytes.

Combined human acceptance additionally requires a named scope with at least two
eligible dialogs, scoped text continuation, exact context, a permitted photo
and JPEG/PNG attachment, and a known visual question answered by the actual
agent. Check declared receipts in another client and verify denial after scope
edits, image revocation, source replacement/deletion, and restart with changed
authority. Synthetic native blocks alone do not prove visual interpretation.
See [image access](image-access.md).
