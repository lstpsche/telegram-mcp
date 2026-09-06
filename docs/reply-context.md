# Reply context

Source after 0.5.0 includes optional `reply_to` references on supported messages,
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
