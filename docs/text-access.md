# Text access

Telegram MCP is an unofficial client using Telegram's API. Text tools are
available only with an authorized local Test-DC account and an explicit human
grant. Production login is disabled. Authentication, peer discovery, eligibility
decisions and grant mutations belong to `telegram-mcpctl`; they are never MCP
tools.

The daemon requires an existing local authorization epoch before opening the
read runtime. If it reports `reauth_required` because the epoch is missing,
stop the daemon, run `telegram-mcpctl auth phone` or `telegram-mcpctl auth qr`,
then restart it. The human authentication command can reconcile an existing
surviving Telegram session without a new login. The daemon does not manufacture
an authorization epoch on its own.

Before enabling a grant, the operator must independently establish permission
for the intended AI use under applicable Telegram terms and the rights and
consent of affected people. A self-authored or consented profile records the
operator's stated basis. Selecting a profile or passing `--attest-eligible` does
not establish a contractual exemption or validate that basis. Disposable Test-DC
authentication and permission to process content are separate decisions.

Stop the daemon before running `telegram-mcpctl peers`. Discovery opens the
existing interactive account's session and scans at most the first 100 dialogs,
returning a JSON list of supported typed peer IDs and display titles. Unsupported
dialogs are filtered, so fewer than 100 results does not prove there are no more
supported dialogs. Discovery has no pagination. It does not return message bodies or grant access.
Telegram dialog discovery and update recovery can transiently receive message
objects in upstream responses; these are discarded and are not stored or
returned by discovery. Establish eligibility for discovery before invoking it.
Titles are untrusted display data; use the immutable typed ID for authority.
The current adapter supports ordinary Saved Messages, non-bot users and basic
groups. Channels, including megagroups, are unsupported pending per-channel
update synchronization. A required channel update recovery makes the read
runtime unavailable; use a disposable Test-DC account with only supported
dialogs for acceptance. Bot chats, Secret Chats and topics are unsupported.

Create a grant with every scope field explicit:

```sh
telegram-mcpctl grant \
  --peer tgpeer:v1:chat:123 \
  --author tgpeer:v1:user:456 \
  --min-id 100 \
  --max-id 120 \
  --read-through 120 \
  --expires-at 2026-09-06T12:00:00Z \
  --profile consented \
  --attest-eligible
```

Replace the example identifiers, range and expiry with independently verified
values. The peer and author require strict versioned IDs; usernames, titles,
links and Bot API ID encodings are not accepted. The author must be user-kind.
Message bounds are positive decimal IDs without leading zeros. The read
ceiling also accepts zero, which authorizes no acknowledgment. Expiry is an
RFC3339 timestamp in the future, at most 30 days from creation. Each grant
authorizes one exact author within one inclusive message range in one peer. `self-authored`
also requires the author to be the actual logged-in account. Saving a grant
replaces the previous grant for that peer and binds it to the current account
authorization epoch. Reauthentication that rotates the epoch or logout removes
old authority. At most 20 grants may be stored; revoke expired grants to free
space.

The `--read-through` ceiling grants a separate side effect: marking the dialog
prefix through that message ID as read, including messages not displayed in the
response. Consent to receive selected bodies alone does not authorize that
prefix. Set the ceiling only when the entire affected prefix is authorized.
An insufficient ceiling fails the request without releasing bodies.

Use `telegram-mcpctl grants` to inspect current unexpired grants and
`telegram-mcpctl revoke --peer tgpeer:v1:chat:123` to revoke one. These local
operations can run while the daemon is alive. They share an owner-only policy
lock with content requests. Contention returns an explicit busy error; retry
after the in-flight operation completes. Revocation succeeds only after any
request holding the lock has finished and the grant is removed. It cannot undo
a previous read acknowledgment or recall a previously delivered response.

The MCP tools `list_chats`, `list_messages`, `get_message_context`,
`search_messages` and `list_unread` use the same
policy boundary. Lists expose only granted peers. History and context recheck
author, range, expiry and eligibility after normalization. Protected, expiring,
forwarded, imported, quoted, media and service content is excluded. Filtering
and bounded truncation are reported as partial. Context never returns unrelated
neighbors when its target is unavailable or unauthorized. Every history/context
response describes its read effect; message bodies are released only after the
hooked read acknowledgment and durable update checkpoint succeed.

No message bodies, titles, search queries or media are persisted in metadata.
Human output quotes display strings as JSON so embedded terminal control
characters remain data. Diagnostics do not echo submitted arguments or raw
Telegram failures. Keep credentials in the existing interactive terminal
prompts; there are no credential-bearing command-line options.

Implementation tests use synthetic data. Live Test-DC content access and agent
acceptance require a separately authorized human-run check and are not implied
by a successful local build or test suite.

## Search and unread metadata

`search_messages` accepts one exact `peer`, a `query`, optional `limit` (default
20, maximum 100), and optional `cursor`. Query whitespace is trimmed; the
remaining text must contain 1–256 Unicode characters. The original input must
occupy at most 1024 UTF-8 bytes. The tool returns safe authorized snippets of at
most 240 Unicode characters, typed message/author IDs, UTC dates and
`snippet_truncated`.
It does not acknowledge history. Follow a returned message ID with
`get_message_context` to request the full body under the existing receipt
contract. There is no account-wide search or named-scope fan-out.

Repeat the same peer, normalized query and limit with `next_cursor` to continue
searching older messages. A full fetched window can return an empty filtered
page with a continuation; follow the cursor instead of assuming no matches.
The cursor advances past all fetched messages, including excluded bodies.
It anchors the upper ID at the first page and expires within 15 minutes or at
grant expiry, whichever is earlier. Each continuation retains that expiry.
Results remain live: edits, deletions and changing search matches are not a
snapshot, and a full last window may require one final empty request.

Cursors are versioned, signed with an independent Keychain key, and bound to
operation, peer, keyed query digest, item limit, authorization epoch and durable
policy revision. They contain no query or body text and are not permission.
Any grant insert, replacement or removal invalidates earlier search cursors,
even when the same grant is recreated. Reusing a valid cursor is permitted;
expired, modified or mismatched cursors fail before Telegram search I/O.

`list_unread` accepts no arguments and inspects all current grants (at most 20).
It returns `peer`, `unread_count` and `unread_mark` for dialogs with a positive
count or a manual unread flag. These are **whole-dialog metadata**, including
messages outside the grant's body-author/range restriction. No bodies, titles,
top-message IDs or inferred dates are exposed. Existing grants authorize this
peer metadata as well as their scoped text. The read-through ceiling is not
needed for search or unread metadata because neither acknowledges history.

Each unread lookup targets one granted peer. Telegram's response can contain an
incidental top message or draft; the adapter discards it. Any peer, freshness,
expiry, cancellation or required-audit failure rejects the entire result rather
than returning incomplete counts. With at most 20 grants, unread listing needs
no pagination. Search and unread retain the 20-second operation deadline and
complete 256 KiB response budget; unread uses at most 80 application RPCs before
bounded transport retries. Required update recovery is independently bounded.

The runtime becomes available only after startup update recovery and a live
checkpoint comparison complete. Later reads also wait for the durable common
checkpoint to catch up with a live state observation. Status reports whether
startup completed and the runtime remains healthy; it does not promise an
instant response while a later gap is being recovered.

Recovery allows at most eight application difference RPCs per completed chain,
with a 15-second deadline per RPC. Each response allows at most 100 new and
encrypted messages combined, 100 other updates, 200 users and 200 chats.
Unrecoverable gaps, invalid states, regressing sequences, non-progressing slices
and oversized responses stop the runtime before their metadata is accepted.
Complete recovered user entities refresh existing access hashes; minimal
entities and zero hashes cannot replace them. Incidental content is discarded.

A checkpoint or recovery failure makes the runtime unavailable for the rest of
that process. Stop and restart the daemon after resolving the cause. Restart
resumes the last accepted durable checkpoint with the same authorization; it
does not skip an unrecoverable gap or replace a corrupt checkpoint with current
remote state. Subsequent metadata writes after a latched failure are rejected.
Logout or authorization rotation clears checkpoints, hashes and grants.
Unexpired search cursors survive an ordinary restart only while the signing
key, epoch and policy binding remain unchanged; context still refetches and
reauthorizes its target, including any edits or deletion.
