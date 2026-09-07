# Reply context

Version 0.6.0 includes optional `reply_to` references on supported messages,
search/catch-up hits and opened attachments. Each reference identifies an older
message in the exact containing conversation or forum topic. It is navigation,
not permission or proof that the parent remains available. Embedded quotes,
cross-conversation replies, scheduled and ephemeral replies remain excluded.
Thread-root metadata does not replace the immediate reply target.

Use `get_message_context` with `message` and optional `reply_depth` from 0 to 5
(default 0). Existing `before` and `after` select chronological neighbors;
`reply_depth` additionally follows the target's immediate parent chain. Their
combined bound, including the target, must not exceed 100. Parents already in
the authorized window are reused. Items remain newest-first and contain no
duplicates; no descendant or sibling replies are searched.

When depth is positive, the target includes `reply_chain` with `depth` (parents
included) and `state`:

- `complete`: the last included message has no supported parent reference.
- `depth_limit`: another reference exists beyond the requested depth.
- `unavailable`: a parent cannot be included. Missing, denied and excluded
  parents deliberately share this status; no reason or parent content is exposed.

Each parent is range-checked before its exact peer-scoped fetch and independently
checked against current author/content/media policy afterward. A reply never
widens grants or crosses topic authority. Operational errors discard the whole
result rather than being presented as unavailable parents. Missing or denied
initial targets still return an error without bodies.

All included messages share the existing operation deadline, response byte
budget, policy lease and audit. One history receipt through the highest included
ID succeeds before delivery; older parents do not extend that boundary. Source
edits and deletion can occur during traversal; no atomic Telegram snapshot is
claimed. Attachment opens compare reply references around download and receipt,
withholding bytes if the reference changes. No reply content or index is stored.

Use `search_messages` with `peer` and `reply_to` to discover direct replies to a
message. Use `thread_root` to find messages belonging to a returned Telegram
thread root; this includes nested replies but excludes the root itself. Both
filters accept strict message IDs from the exact selected conversation/topic,
can be combined with other search filters, and are unavailable with a scope.
They do not fetch or grant access to the referenced parent/root. A parent outside
the body grant can still identify replies that are inside it.

Authorized history/context messages and search/catch-up hits can include
`thread_root`. This is distinct from the immediate `reply_to`: in a forum it
identifies the containing topic, not a new subthread for every reply. In an
ordinary supergroup Telegram omits the top ID for a direct reply to the root;
that direct parent identifies the thread. Missing metadata remains unknown.
Embedded quotes and cross-conversation reply headers remain excluded.

Reply discovery returns bounded snippets without receipts. Ordinary supergroup
thread searches use Telegram's thread selector; direct-reply filters and other
intersections are checked locally on authorized candidates. Private-chat and
basic-group reply searches use the existing history/search traversal. Repeat all
filters and the page limit with `next_cursor`, including after an empty page.
The signed cursor binds both reply selectors. Follow a hit with
`get_message_context` for the acknowledged body and optional parent chain.

Channel posts can include `discussion_peer`, an untrusted navigation reference
supplied by Telegram. With Full read enabled, call `get_message_context` with
`resolve_discussion: true` to obtain `discussion_root` on the requested post:

```json
{"message":"tgmsg:v1:channel:42:20","resolve_discussion":true}
```

Then search the returned discussion peer and root:

```json
{"peer":"tgpeer:v1:channel:99","thread_root":"tgmsg:v1:channel:99:100","limit":50}
```

The examples use synthetic IDs; always use actual returned references. Resolution
requires a joined, supported broadcast and joined ordinary non-forum discussion
supergroup, with adapter metadata already discovered for both. Use `list_chats`
to discover joined conversations if peer metadata is missing. It does not join
a group or resolve arbitrary usernames. Restricted grants cannot enable this
lookup: Telegram's discussion-mapping RPC can return incidental messages without
author or message-range bounds. Direct reply/thread searches remain available
under exact restricted grants.

Resolution validates the destination and the root's channel-post origin, then
rechecks the source's eligibility and link. Missing comments, unsupported groups,
changed links, unavailable roots and operational failures return an error and
withhold the whole context result. A root must map to the exact requested channel
post; album posts whose returned root maps to a different member are not resolved.
The mapping is not an atomic snapshot and can change immediately afterward.

Only the source context's authorized history prefix is acknowledged. Incidental
discussion bodies, read counters and entities are discarded; no discussion-group
receipt is sent. The returned root is navigation, not permission to fetch its
body: channel-authored automatic forwards in groups remain excluded by the
existing author policy, while supported replies can be explored independently.
No reply index, mapping cache or new persisted content is introduced.

Provider semantics: [Telegram message threads](https://core.telegram.org/api/threads)
and [linked discussions](https://core.telegram.org/api/discussion).
