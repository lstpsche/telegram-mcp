# Version 1 contracts

These transport-independent contracts define the MCP input and output boundary.
The daemon registers a static inventory: `status`, `list_chats`, `list_messages`,
`get_message_context`, `search_messages`, and `list_unread`. Authentication and
policy mutations are human-only.

## Available MCP behavior

The stdio relay forwards bytes to an owner-only Unix socket. The daemon uses
the pinned MCP SDK for initialization, tool discovery, calls, and cancellation.
Both endpoints verify the other process's OS user. The server accepts at most
eight connections, caps each input frame at 64 KiB, and limits each connection
to 20 incoming frames per second with a burst of four. The normalized JSON
encoding of a reflected request ID is capped at 1 KiB, including Unicode
escaping; oversized IDs close the connection before tool execution. Idle reads expire after
one minute and blocked output writes after ten seconds; reconnect after expiry.

`status` accepts an empty object and returns the standard envelope. Its single
item reports `account_state`, `message_reads`, and
`production_login: false`. `account_state: ready` describes the account
runtime; `message_reads` reports the synchronized text-engine state, not a grant.
Telegram data freshness in status remains `unavailable` because status does not
check Telegram. This tool has no Telegram I/O
or read-receipt side effects. Its input/output schemas and inventory are static;
text and structured content represent the same result. Invalid input is returned
as a fixed `invalid_input` tool error without echoing request fields.

Text tools validate their static JSON schemas, reject duplicate or unknown
fields, and reject null where a string or integer is required. `list_chats`
accepts an optional `limit` (default 20, maximum 100) and returns only granted
peer metadata; at most 20 grants exist. `list_messages` requires `peer` and
accepts an optional typed exclusive `before` message and `limit`.
`get_message_context` requires `message` and accepts `before`/`after` neighbor
counts from 0 through 49, both defaulting to zero. A denied or absent target
returns no neighbors. History/context issue no cursor: `next_cursor` is null;
an explicit `before` selection is reauthorized on every call.

Text results contain typed string IDs, author IDs, UTC message dates and
untrusted text. Chat results contain typed IDs and untrusted titles. Filtering
and a full bounded window set `partial` with `partial_result`; no failed
upstream operation is replaced with a partial success. History and context
tools advertise read side effects rather than `readOnlyHint: true`.

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
was acknowledged. History/context authorize and assemble the bounded result,
perform the hooked acknowledgment, wait for its durable checkpoint, and only
then release bodies. Search snippets and unread metadata issue no acknowledgment;
opening a search hit with context enters the history acknowledgment flow.

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
with `read_effect_uncertain`. Never claim exactly-once delivery
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
cancellation, and a sanitized internal error.

## Input and result bounds

- Unknown input fields and trailing JSON values are rejected.
- Default page size: 20.
- Hard page maximum: 100.
- Hard serialized textual result maximum: 256 KiB, including structured/text
  mirrors, escaping, a bounded request ID and framing overhead. Preparation
  precedes acknowledgment.
- Text operations have a 20-second deadline, further shortened to grant expiry.
  Policy leases serialize requests against grant mutations without holding a
  database write transaction across Telegram I/O. A busy lease fails explicitly.
- A chat listing makes at most 40 application RPCs (metadata and state for each
  of 20 grants). History/context and receipt paths have fixed bounded RPC
  sequences; adapter calls additionally have 15-second deadlines. The existing
  concurrency/rate/flood bounds apply to transport attempts and recovery.
- Client-supplied limits never raise server count, byte, RPC, concurrency, or
  duration caps.
- Search cursors are signed, expiring and reauthorized. Future media handles
  must also be opaque, signed, expiring,
  operation/query/authorization-epoch/policy-revision-bound, content-free, and
  reauthorized on every use. They never grant authority by themselves.
