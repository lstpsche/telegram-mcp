# ADR-0001: Persistent daemon, private local transport, and byte relay

- Status: accepted
- Date: 2026-09-04

## Context

Telegram user authorization is stateful. Starting one Telegram client for every
MCP stdio process would duplicate connections, repeat authentication, divide
update state, and risk invalidating or racing a shared authorization key. A
local HTTP server would add authentication, token distribution, Origin, DNS
rebinding, and listener security without helping a same-machine Codex client.

Authentication and policy mutation are privileged human operations. Putting
them beside model-facing reads would turn a bounded context adapter into an
account administration surface.

## Decision

One `telegram-mcpd` process owns one account lock, Telegram authorization,
connection, update state, policy evaluation, metadata database, and local
secret-file access. It accepts newline-delimited MCP connections over an
owner-only Unix domain socket on macOS/Linux or a user-restricted named pipe
on Windows.

Without arguments, `telegram-mcp` first connects to that endpoint. If it is
proven absent or safely refused, the relay may ask the OS service manager to
start the existing, canonical, enabled per-user installation. It never enables,
installs or repairs a registration, directly spawns a daemon, or creates its own
Telegram client. Startup has one eight-second deadline and runs under the same
lifecycle lock as human maintenance. A private content-free launch timestamp
coalesces attempts for 30 seconds; an invocation never submits a second attempt.

Once connected, `telegram-mcp` is a byte-only bridge between standard MCP stdio
and that exact connection. It owns no credentials, policy decision or schema
translation. It does not consume MCP traffic before connection, reconnect a
broken session, or replay requests. Its stdout contains MCP frames only.

With explicit subcommands, `telegram-mcp` is the interactive human control plane
for authentication, policy, consent, lifecycle, and audit operations. Those
operations have no MCP equivalent. Human stop disables startup persistently;
only explicit human resume re-enables it. Managed upgrades preserve that choice.
If an interrupted activation has removed the previous registration, upgrade
installs disabled because the former enablement is unknown.

The daemon must take its account lock before reading secrets or connecting.
Unix runtime directories and sockets are user-owned and mode `0700`/`0600`.
Windows uses private directory ACLs, exclusive locks and a user-restricted pipe.
Stale socket cleanup must use `lstat`, reject symlinks, non-sockets, and wrong
owners, and never remove an unresolved path.

The human authentication command takes that same lock and authenticates directly only
while the daemon is stopped. This avoids adding a temporary credential-bearing
control protocol to the local transport. The daemon can start without account
configuration so an operator can verify client connectivity through the
content-free MCP status tool before authentication. Data tools remain
registered and report `not_ready` until the account read runtime is ready.

Offline human operations retain the lifecycle lock while they use the account.
This prevents a new relay from starting the service between human maintenance
operations. The account lock remains the final single-owner boundary and is
released before a service launch. Policy-only changes retain their existing
policy lease. Diagnostics and retained-send-status inspection do not start the
service or take its lifecycle lock.

The managed stable entry delegates to the release selected by the validated
service record for both human commands and MCP stdio. The record remains the
sole active-version selector. Older fixed entries require explicit migration
from a newly verified control program; a failed replacement cannot activate a
new registration. Versioned binaries remain immutable.

## Consequences

- Multiple MCP clients share one coherent Telegram runtime.
- Relay crashes cannot create another Telegram session or mutate authority.
- Multiple relays join the same startup; cancelling a relay does not stop a
  service already submitted to the OS.
- Transport connectivity does not establish account readiness. Startup failures
  remain visible and cannot trigger credential or checkpoint recovery.
- Same-user processes remain capable of invoking or tampering with local
  binaries; this split is defense in depth, not a cryptographic same-user
  sandbox.
- Direct HTTP remains absent until a concrete client requirement justifies a
  separate transport threat model.
