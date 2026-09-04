# Structured logging policy

`internal/logging` is the runtime logging boundary. It exposes fixed event IDs
and a closed set of metadata fields rather than free-form messages or raw
errors.

Allowed metadata includes component, operation, request ID, stable error
category, freshness state, version, counts, durations, and partial-result
flags. Token fields are length and character constrained and fail closed to the
literal `invalid`; unknown events become `operation.failed` with all fields
dropped.

Never log:

- message bodies, snippets, entities, quotes, replies, or captions;
- search queries, peer titles, usernames, invite links, or media filenames;
- login codes, 2FA passwords, `api_hash`, session bytes, integrity keys, or
  access hashes;
- raw Telegram objects, RPC payloads, raw upstream error strings, cursors, or
  resource handles.

Do not expose the underlying `slog.Logger` or add generic `message`, `error`, or
`fields` escape hatches. Add a stable event or metadata constructor only when a
real operational question requires it. `tg-context-mcp` diagnostics always go
to stderr; stdout is reserved for MCP frames.

Phase 1 gotd clients use the upstream no-op logger. Command failures collapse
upstream errors to fixed local categories before anything reaches stderr.
