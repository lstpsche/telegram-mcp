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

`telegram-mcp` is a byte-only bridge between standard MCP stdio and that
socket. It owns no Telegram client, credential, policy decision, schema
translation, or daemon-spawn fallback. Its stdout contains MCP frames only.

`telegram-mcpctl` is the interactive human control plane for authentication,
policy, consent, lifecycle, and audit operations. Those operations have no MCP
equivalent.

The daemon must take its account lock before reading secrets or connecting.
Unix runtime directories and sockets are user-owned and mode `0700`/`0600`.
Windows uses private directory ACLs, exclusive locks and a user-restricted pipe.
Stale socket cleanup must use `lstat`, reject symlinks, non-sockets, and wrong
owners, and never remove an unresolved path.

`telegram-mcpctl` takes that same lock and authenticates directly only
while the daemon is stopped. This avoids adding a temporary credential-bearing
control protocol to the local transport. The daemon can start without account
configuration so an operator can verify client connectivity through the
content-free MCP status tool before authentication. Data tools remain
registered and report `not_ready` until the account read runtime is ready.

## Consequences

- Multiple MCP clients share one coherent Telegram runtime.
- Relay crashes cannot create another Telegram session or mutate authority.
- Same-user processes remain capable of invoking or tampering with local
  binaries; this split is defense in depth, not a cryptographic same-user
  sandbox.
- Direct HTTP remains absent until a concrete client requirement justifies a
  separate transport threat model.
