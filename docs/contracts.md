# Version 1 contracts

These transport-independent contracts define the intended MCP input and output
boundary. The daemon registers only the content-free `status` tool.

## Available MCP behavior

The stdio relay forwards bytes to an owner-only Unix socket. The daemon uses
the pinned MCP SDK for initialization, tool discovery, calls, and cancellation.
Both endpoints verify the other process's OS user. The server accepts at most
eight connections, caps each input frame at 64 KiB, and limits each connection
to 20 incoming frames per second with a burst of four. Idle reads expire after
one minute and blocked output writes after ten seconds; reconnect after expiry.

`status` accepts an empty object and returns the standard envelope. Its single
item reports `account_state`, `message_reads: false`, and
`production_login: false`. `account_state: ready` describes the account
runtime, not the availability of message tools. Telegram data freshness remains
`unavailable` because no read engine is exposed. This tool has no Telegram I/O
or read-receipt side effects. Its input/output schemas and inventory are static;
text and structured content represent the same result. Invalid input is returned
as a fixed `invalid_input` tool error without echoing request fields.

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

### Authorization for history side effects

Telegram's `messages.readHistory(max_id)` affects the dialog prefix through
that ID, not only messages included in the result. The grant must separately
authorize advancing that peer's read boundary, including undisplayed messages.
Author/date/message filtering of bodies does not narrow the upstream effect.
Fail closed if the actual boundary is not authorized. Recheck grant validity
before acknowledgment and serialize that decision with revocation.

Assemble and serialize the bounded candidate before acknowledgment; release
it only after the required update-aware acknowledgment succeeds. A disconnect
after successful acknowledgment does not undo the read effect. A timeout can
leave the upstream effect uncertain: return no body and disclose uncertainty
when content-reading tools are implemented. Never claim exactly-once delivery
or that an error guarantees no upstream side effect.

Content-read methods taking exact message ID lists have distinct semantics;
do not represent those as permission to advance an entire dialog. Search
snippets and unread metadata must retain their own non-history behavior.

[Telegram history acknowledgment](https://core.telegram.org/method/messages.readHistory)
defines the range semantics.

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
