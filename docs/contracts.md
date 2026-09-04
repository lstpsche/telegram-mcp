# Version 1 contracts

Phase 0 fixes transport-independent contracts. No MCP tool is registered yet.

## Typed references

Telegram ID spaces overlap, so every peer reference includes a version and
kind. The canonical forms are:

```text
tgpeer:v1:self:271946281
tgpeer:v1:user:834726192
tgpeer:v1:chat:482719
tgpeer:v1:channel:1948273619
tgmsg:v1:channel:1948273619:4827
```

All identifiers cross JSON as strings. Parsers reject signs, zero, leading
zeroes, whitespace, non-ASCII digits, overflow, unknown versions or kinds, and
extra segments. Peer IDs are bounded to a positive signed Telegram `long`;
message IDs are bounded to a positive Telegram `int`.

Usernames, titles, invite links, and Bot API `-100...` encodings are never
authority. Access hashes and generated gotd types remain inside
`internal/telegram`.

## Success envelope

Every successful result uses a versioned envelope:

```json
{
  "schema_version": "1",
  "request_id": "req_example_1",
  "freshness": {
    "telegram": "live",
    "checked_at": "2026-09-04T09:30:00Z"
  },
  "partial": false,
  "read_effect": {"kind": "none"},
  "items": [],
  "next_cursor": null,
  "warnings": [],
  "untrusted_content": true
}
```

Freshness is one of `live`, `recovering`, `stale`, `partial`, or `unavailable`.
Timestamps are RFC 3339 UTC. Warning values are stable codes, not free-form
Telegram-derived text.

Read effects are `none`, `history_marked_read`, or `content_marked_read`.
State-affecting effects require the message reference through which the state
was acknowledged. Future history/context paths must authorize and assemble the
bounded result, perform the hooked acknowledgment, and only then release
bodies. Search and unread metadata use distinct semantics.

## Errors

Tool errors contain only a schema version, request ID, stable category, fixed
content-free message, and—only for `rate_limited`—a bounded retry delay. Raw
Telegram errors, objects, peer titles, request payloads, and content are never
returned.

The initial stable categories are defined in `internal/model/envelope.go` and
cover readiness, authorization, unsupported or protected content, invalid or
expired capabilities, rate/freshness/availability failures, size budgets,
cancellation, and a sanitized internal fallback.

## Input and result bounds

- Unknown input fields and trailing JSON values are rejected.
- Default page size: 20.
- Hard page maximum: 100.
- Hard serialized textual result maximum: 256 KiB.
- Client-supplied limits never raise server count, byte, RPC, concurrency, or
  duration caps.
- Cursors and media handles will be opaque, signed, expiring,
  operation/query/authorization-epoch/policy-revision-bound, content-free, and
  reauthorized on every use. They never grant authority by themselves.

