# Version 1 contracts

These transport-independent contracts define the MCP input and output boundary.
The daemon registers a static inventory: `status`, `list_chats`, `list_messages`,
`get_message_context`, `search_messages`, `list_unread`, `list_scopes`, `catch_up`, `open_image`, and `open_document`. Authentication and
policy mutations are human-only.

## Available MCP behavior

Invoking `telegram-mcp` without arguments starts the stdio relay. Explicit
subcommands enter the human control plane; MCP frames cannot select that mode.
The stdio relay forwards bytes to an owner-only Unix socket on macOS/Linux
or a user-restricted named pipe on Windows. The daemon uses
the pinned MCP SDK for initialization, tool discovery, calls, and cancellation.
Both endpoints verify the other process's OS user. The server accepts at most
eight connections, caps each input frame at 64 KiB, and limits each connection
to 20 incoming frames per second with a burst of four. The normalized JSON
encoding of a reflected request ID is capped at 1 KiB, including Unicode
escaping; oversized IDs close the connection before tool execution. Connections
may remain idle between requests. Once the first byte arrives, an incomplete
input frame expires after one minute. Blocked output writes expire after ten
seconds; reconnect after either timeout.

`status` accepts an empty object and returns the standard envelope. Its single
item reports `account_state`, `message_reads`, and
`production_login: true` as a static capability. It does not attest eligibility
or imply an active session. `account_state: ready` describes the account
runtime; `message_reads` reports the synchronized text-engine state, not a grant.
Telegram data freshness in status remains `unavailable` because status does not
check Telegram. This tool has no Telegram I/O
or read-receipt side effects. Its input/output schemas and inventory are static;
text and structured content represent the same result. Invalid input is returned
as a fixed `invalid_input` tool error without echoing request fields.

Text tools validate their static JSON schemas, reject duplicate or unknown
fields, and reject null where a string or integer is required. `list_chats`
accepts optional `scope`, `limit` (default 20, maximum 100), and `cursor`.
Restricted mode lists granted metadata (at most 20 grants); Full read access
paginates supported dialogs across main and archive, including filtered pages.
Unscoped `list_unread` in Full mode scans 100 dialogs per page. Both return
`next_cursor`; scopes and restricted lists reject cursors. Discovery tokens are
signed, fixed-expiry (15 minutes), and tool/limit/epoch/revision bound. `list_messages` requires `peer` and
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

`list_scopes` accepts an empty object and returns local scope IDs, names, and
eligible/excluded peer counts without Telegram I/O. Its freshness is
`unavailable`. Scope membership narrows current access authority; it cannot
authorize content or receipt effects. `list_unread` also accepts optional
`scope` and `cursor`. `search_messages` requires exactly one of `peer` or
`scope`, with optional `limit`, `cursor`, `unread_mentions_only`, `pinned_only`, `media_type`, `sender`,
`since`, `until`, `saved_peer` and `saved_tag`. Query is required and nonempty
unless another filter is active. Sender matches the returned author. Dates use
whole-second RFC3339 timestamps, with inclusive since and exclusive until;
either is optional. Saved source/tag filters require an exact self peer and
narrow saved copies, never their origins. See [search
filters](search-filters.md) and [Saved Messages
organization](saved-messages.md). Neither search nor unread acknowledges
history. Scoped search visits canonical peer IDs in ascending bytewise order and
returns newest messages first within each peer. It bounds fetched candidates,
including filtered ones, to the requested limit and backend lookups to 20 per
request. See [text access](text-access.md) for continuation and eligibility
semantics.

`catch_up` requires `scope`, `since`, and `until`, with optional `limit` and
`cursor`. Dates use RFC3339 with an explicit timezone and no fractional seconds.
The range is [since, until): start included, end excluded. Both must be positive
Unix timestamps within Telegram's signed 32-bit timestamp range. There is no
implicit current time. The response uses search-hit snippets and image
references, never full bodies or read receipts. `search_messages` can also
narrow by sender and optional date bounds.

Catch-up requires the `scope` coverage object and its `catch_up` field:
`since` and `until` normalized to UTC, and a `peers` array containing each
eligible peer's `peer` ID, `state`, `fetched` and `returned` counts.
States are `pending`, `in_progress` and `complete`. State spans the cursor
chain; counts describe only the current response. Excluded peer identities are
never exposed. Completion means traversal of the authorized window, not absence
of unsupported or excluded content.

Catch-up positions history at the upper date and traverses descending IDs within
current authority. Date checks exclude newer messages and stop each peer once a
validated sending timestamp precedes the start. Timestamp evidence from excluded
service/content entries can establish that boundary; undated empty entries cannot.
Every fetched candidate consumes the budget, including out-of-window entries.
This avoids relying on empty-query search date filters, which basic groups can
ignore. No history acknowledgment is issued by catch-up.

Catch-up uses the same canonical peer order, candidate and response budgets,
fixed cursor expiry, and authority invalidation as scoped search. The cursor
also binds both dates and the operation. Equivalent timezone representations
can continue the same window. Follow continuation even on empty pages. A terminal
response may still be partial because content or peers were excluded. This is
live traversal, not a cross-chat chronological snapshot.

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

Local scope IDs have the form `tgscope:v1:0123456789abcdef0123456789abcdef`:
exactly 32 lowercase hexadecimal characters after the version prefix. Human
names are mutable labels; only the stable ID is accepted as an MCP selector.

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

Scoped chat listing, unread and search add an optional `scope` object containing
`id`, `total_peers`, `eligible_peers`, `excluded_peers`, `queried_peers`, and
`completed_peers`. Counts are bounded to 20. Queried peers are this request's
lookups; completed peers are the exhausted search prefix across pages, or the
processed peers for chat/unread. Exclusions set `partial` with a warning; they
do not disclose excluded peer identities. Unscoped results omit this object.

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
- Text operations have a 20-second deadline, further shortened to restricted-grant expiry.
  Policy leases serialize requests against access-mode, grant and scope mutations without holding a
  database write transaction across Telegram I/O. A busy lease fails explicitly.
- A restricted/scoped chat listing makes at most 40 application RPCs (metadata
  and state for each of 20 peers). Account-wide discovery uses one bounded
  dialog RPC and common state synchronization per page. History/context and receipt paths have fixed bounded RPC
  sequences; adapter calls additionally have 15-second deadlines. The existing
  concurrency/rate/flood bounds apply to transport attempts and recovery.
- Client-supplied limits never raise server count, byte, RPC, concurrency, or
  duration caps.
- Search cursors and media handles are signed, expiring, epoch/revision-bound,
  and reauthorized on every use. Search binds its query; media handles bind
  the exact message and keyed media identity. Neither grants authority.
- `open_image` accepts only a handle string (1–4096 characters). Its single
  metadata item contains `id`, `author`, `date`, and `image`; an additional
  native image block carries the original JPEG/PNG bytes. Permitted history
  and search items optionally include the same descriptor. See
  [image access](image-access.md) for fields and receipt semantics.
- Images are limited to 1 MiB and 4 million pixels, with each dimension at
  most 4096. The complete image result is bounded to 2 MiB before receipt,
  while metadata mirrors retain the 256 KiB limit. No input limit is raised.

Full read access is a human-only, authorization-epoch-bound policy setting.
It authorizes all supported authors and positive message IDs, images, PDF/plain-text attachments, and
whole-prefix read effects in supported dialogs. It has no expiry; transient
requests, cursors and handles retain their deadlines. Absence means restricted
mode. Disablement restores preserved exact grants. Every mode change increments
the existing policy revision; logout and epoch rotation clear the setting.

Ordinary non-forum supergroups and joined broadcasts use channel-kind IDs. Inaccessible/protected conversations remain excluded. Broadcast publisher authority and attribution follow [channel access](channel-access.md). Forum content
requires an exact topic identity, as described in [topic access](topic-access.md). Live history/search responses
must include the exact permitted entity and a positive channel pts, but channel
pts is not stored or used as proof of continuous channel updates. Supergroup
receipts require a successful `channels.readHistory` RPC, exact dialog readback at
or beyond the requested boundary, and common checkpoint synchronization before
release. This is a live RPC/readback contract, not a channel subscription or
atomic remote snapshot.

The Boolean result itself is not a success flag. Like Telegram's
[TDLib read handler](https://github.com/tdlib/td/blob/master/td/telegram/MessagesManager.cpp),
the adapter distinguishes RPC errors from either returned Boolean; exact
read-position readback remains required before content release.

## Document delivery

`open_document` accepts only a current signed document handle. Discovery in
history, context, search and catch-up uses the optional `document` object with
`handle`, `mime_type` and `size`. Restricted grants require the separate
`documents` bit; Full read includes supported documents. Image permission does
not imply document permission. The forward migration preserves existing grants,
audit rows and retention, defaulting restricted document permission to false.

Opening returns one metadata item (`id`, `author`, `date`, `document`) and one
native content block: an embedded PDF resource or a plain-text block. It has
state-affecting, non-idempotent annotations because delivery requires the
separately authorized whole-prefix read acknowledgment. Exact-source checks and
the final policy/audit boundary precede content release, including after the
acknowledgment. Handles use a separate operation and signing domain from images.
The embedded URI grants no authority and has no resource-read endpoint.

PDFs are original files with framing checks, without a fixed application byte cap.
They are not parsed or sanitized.
Text requires valid UTF-8 and rejects controls except tab/CR/LF. All attachments
remain hostile input. See [document access](document-access.md) for byte limits,
client compatibility, safe handling, read effects and failure semantics.

## Topics and voice notes

[Topic access](topic-access.md) defines exact topic identities, discovery and
topic-only receipts. Parent grants do not authorize descendants.
[Voice access](voice-access.md) defines the separate permission and native audio
delivery contract. Voice delivery reports a history read effect, never playback.

## Reply context

Messages, search hits and media results may include `reply_to`. The existing
context tool accepts `reply_depth` (0–5, default 0) and reports bounded ancestor
coverage on the target. Exact authorization, unavailable-parent behavior and
combined bounds are defined in [reply context](reply-context.md).

Reply discovery accepts exact-peer `reply_to` and `thread_root` filters on
`search_messages`, returning authorized snippets without receipts and binding
both selectors into continuation tokens. `get_message_context` optionally accepts
`resolve_discussion` under Full read and returns a validated `discussion_root`
reference on the source channel post. Its receipt affects only the source context;
linked group bodies require a separate authorized read. See
[reply context](reply-context.md) for bounds and unavailable cases.

## Media albums

Supported media members may include a conversation-scoped `album_id`. Captions
remain on their original messages; grouping does not alter pagination, grants
or attachment downloads. See [media albums](media-albums.md) for partial album
semantics and source validation.

## Poll snapshots

History/context may include `poll`; search/catch-up use `has_poll` and a question
snippet. Optional aggregate counts preserve absent versus zero. Existing text
authority, response budgets and read receipts apply; no voter lookup or voting
is available. See [read-only polls](polls.md).

## Link previews

History/context preserve original message text alongside optional `link_preview`.
Its `state` is `available`, `pending`, or `unavailable`; `manual` distinguishes
previews that can refer to another URL. Supplied URL, site name, title and
description are bounded untrusted display data. Search/catch-up include only
`has_link_preview` and the original-text snippet. No page or preview media is
fetched. See [link messages and previews](link-previews.md).

## Reaction summaries

History/context/search/catch-up may include `reactions` with `minimal`, `as_tags`
and bounded ordered `counts`. Entries distinguish Unicode emoji, opaque custom
emoji identifiers and paid Stars. Missing is not zero, tags are not endorsements,
and counts do not establish distinct-person totals. Existing message authority
and receipt boundaries apply. See [reaction summaries](reactions.md).

History/context/search/catch-up include `pinned: true` when Telegram reports a
message as pinned; false is omitted. Pin state is transient metadata, never
authority or instruction priority. Pinned-only search uses Telegram's pinned
filter and rechecks the normalized pin state after policy filtering. Both peer
and scope cursors bind `pinned_only`; switching the filter invalidates a cursor
before fetching. See [pinned-message discovery](pinned-messages.md).

`search_messages.media_type` accepts `photo`, `image_file`, `pdf`, `text_file`,
or `voice_note`. It matches only supported media with current permission and a
validated descriptor. The filter intersects `pinned_only` when both are set.
Both cursor kinds bind the media type. Unsupported, unauthorized and nonmatching
candidates still consume the fetched-candidate budget; empty pages can have
continuation. Search never downloads or interprets attachment contents. See
[media search](media-search.md).

## Batched context and message links

`get_message_context` accepts mutually exclusive `message` and `messages` inputs.
Batch results add `contexts` entries (`target`, `messages`, `partial`) associating
each target with IDs in the deduplicated `items` array. Batch read effects use
`through_message_ids`, one boundary per exact peer/topic. Single input preserves
`through_message_id`. Both forms keep schema version 1 and the same serialized
response budget. See [batch context](batch-context.md).

Context inputs also accept supported HTTPS Telegram message links. Numeric links
map to strict IDs; public usernames require Full read and resolve only joined
supported publishers before exact authorization. Message and search-hit `url`
fields are optional generated numeric citations, never authority. See
[message links](message-links.md) for accepted forms and explicit exclusions.

## Discovery title queries

`list_chats` and `list_topics` accept optional `query` for normalized literal
substring matching over authorized titles. Candidate limits and original
continuation positions remain unchanged after filtering. Dialog/topic cursors
bind a keyed normalized-query digest; an absent query preserves prior encoding.
See [discovery search](discovery-search.md).
