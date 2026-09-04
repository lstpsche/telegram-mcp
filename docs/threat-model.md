# Telegram MCP threat model

- Status: account runtime and content-free MCP implemented; content controls specified
- Date: 2026-09-04
- Scope: local single-account macOS v1 architecture

## Security objectives

1. An MCP client can retrieve only explicitly authorized and content-eligible
   bounded results.
2. Authentication, consent, policy mutation, and account administration remain
   outside the model-facing protocol.
3. Telegram content and credentials do not become durable local copies, logs,
   errors, tool instructions, or ambient process configuration.
4. Read-state side effects, freshness, partial results, and degradation are
   represented truthfully.
5. One daemon owns one account session and update state; no relay or client can
   create a fallback Telegram connection.

## Assets

- Telegram authorization/session bytes, `api_hash`, login code, and 2FA secret;
- integrity keys for cursors and resource handles;
- immutable peer policy, consent scope, policy revision, and authorization
  epoch;
- access hashes and update checkpoints;
- Telegram message/search/media content in transient memory;
- metadata audit integrity and operator understanding of side effects.

## Actors and assumptions

- The operator controls the macOS account and performs interactive control-plane
  actions.
- Telegram and gotd are external/upstream trust dependencies.
- An MCP client or model may be buggy, compromised, or adversarial.
- Telegram content, peer metadata, and entities are hostile input and may
  contain prompt injection, malformed Unicode, or oversized structures.
- Other local processes running as the same macOS user are **not** isolated by
  this design. They can potentially inspect or replace accessible files and
  invoke installed binaries. The binary split and permissions are defense in
  depth, not a cryptographic same-user boundary.

## Trust boundaries

```text
untrusted model/client
  -> canonical MCP stdio
byte-only relay
  -> owner-only Unix socket
single-account daemon
  -> schema/budget and application boundary
  -> default-deny policy before fetch and after normalization
  -> Telegram adapter / metadata store / native Keychain
  -> Telegram MTProto

human operator
  -> interactive control binary
  -> daemon-controlled privileged operations
```

## Threats and required controls

### Capability expansion through MCP

Threat: a client invokes authentication, raw RPCs, writes, peer enumeration, or
policy mutation.

Controls: deterministic static read-tool inventory; no control-plane MCP tools;
strict unknown-field rejection; typed IDs; no `resolve_peer` tool; hard budgets;
no dynamic write flag. The runtime registers only the content-free status tool.

### Mutable alias or confused-deputy authorization

Threat: a username/title/link changes or a cursor is replayed to reach a denied
peer.

Controls: store kinded immutable IDs only; preflight policy before Telegram I/O;
postfilter every normalized result; bind cursors/handles to operation, query,
authorization epoch, policy revision, and expiry; reauthorize every use. Access
hashes stay adapter-private.

### Prompt injection and data exfiltration

Threat: Telegram text changes instructions, tool schemas, logs, errors, or later
behavior.

Controls: content remains typed result data; never interpolate it into control
text; fixed error messages; closed structured logging fields; serialized size
caps; adversarial Unicode/entity/property tests; no local message index.

### False read-only semantics

Threat: history, context, voice, or round-media delivery advances Telegram state
after a result was already released or without disclosure.

Controls: distinguish search/unread metadata from body reads; assemble and
post-authorize first, including permission for the complete dialog read prefix
and any undisplayed messages it affects; acknowledge through the update-manager
affected-result hook second; release bodies only on success; return explicit `read_effect`.

### Secret disclosure or persistence

Threat: credentials appear in argv, environment, stdout, logs, SQLite, crash
output, or a fallback file; synchronized Keychain items leave the device.

Controls: interactive no-echo input comes directly from `/dev/tty`; 2FA uses a
locked wipe-on-use buffer; native Security.framework only; unlocked-login-
keychain check; explicit non-synchronizing attribute; noninteractive UI
failure; no file backend; byte-only relay has no secrets; metadata schema
contains no secret/content columns.

### Local socket and process attacks

Threat: a second daemon races account state, a symlink/socket is replaced, or a
relay silently spawns a new Telegram client.

Controls: account lock before secret prompting or access; private runtime
directory/socket; `lstat` and owner/type validation; length-checked path;
same-user stale socket cleanup only after positive `ECONNREFUSED`; fail-closed
handling of inconclusive dial errors; no relay fallback; cancellation-aware
accept loop. Both socket endpoints check peer credentials; the listener bounds
concurrent connections and the MCP boundary limits input frames and rate, idle
reads, and blocked writes.

### Stale or incomplete state represented as live

Threat: update gaps, `differenceTooLong`, missing access hashes, reauth, or
partial search fan-out yields an apparently complete response.

Controls: explicit freshness state and timestamp; capability degradation;
partial/warning/cursor metadata; authorization epoch invalidation; durable
metadata transactions; never infer completeness from a successful remote call.

### Resource exhaustion

Threat: large pages, media, fan-out, flood waits, or concurrent clients exhaust
memory, disk, connections, or time.

Controls: server-side item/byte/RPC/concurrency/deadline caps; bounded flood
waits; cancellation; no durable media/cache; 256 KiB textual result maximum;
default 20 and hard maximum 100 items.

### Supply-chain and binary-identity drift

Threat: dependency compromise, prerelease selection, unsigned replacement, or
changed code identity breaks or broadens Keychain access.

Controls: explicit stable pins and checksums; compile selected gotd/MCP packages;
direct platform Keychain API; vulnerability checks at release; signed/notarized
artifacts; upgrade ACL proof. The ad-hoc signing probe proves only one unchanged
development binary, not release identity.

## Excluded or deferred risk

Broadcast channels, bot chats, Secret Chats, protected content, expiring media,
writes, HTTP, multi-account state, local content indexing, and broad experimental
eligibility are absent from v1. Adding any of them requires a separate threat
and contract plan.

Telegram's API and content-licensing terms are a release boundary, not a
security control. Test-DC and synthetic/self-authored development does not imply
permission for production/private data or publication. A dated release decision
is required. Intended AI-data eligibility must be resolved before enabling
content workflows; a configured peer grant is not itself proof of eligibility.

## Account runtime verification boundary

Automated checks prove lock-and-eligibility-before-prompt ordering,
second-owner exclusion,
private directory/file/socket permissions, fail-closed malicious socket paths,
safe stale-socket replacement, fail-closed inconclusive socket probes,
Keychain-backed gotd session reconciliation, authorization-epoch
rotation/invalidation after startup and at runtime, bounded request scheduling,
sanitized command output, and cancellation cleanup. Live phone/2FA and QR login
still require a human Test-DC account and are recorded separately when run.

The account runtime does not provide policy enforcement, message reads, MCP
content tools, update recovery, production eligibility, signing across upgrades,
or release readiness. Production DC construction is absent even after both Test-DC method
checks pass.
