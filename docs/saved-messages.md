# Saved Messages organization

Use the exact Saved Messages `self` peer returned by `list_chats`. Authorized
history, context and search results expose `saved_peer` when Telegram supplies
its grouping ID. Existing `reactions` expose tags when `as_tags` is true.

Pass those values back to `search_messages` to find saved copies:

```json
{"peer":"tgpeer:v1:self:7","saved_peer":"tgpeer:v1:channel:42","saved_tag":{"kind":"emoji","emoji":"📌"},"limit":20}
```

`saved_peer` and `saved_tag` can be used separately or together. A custom-emoji
tag uses `{"kind":"custom_emoji","custom_emoji_id":"123"}`. Copy the kind and
emoji or custom-emoji ID from a returned positive reaction count; omit `count`.
One tag is accepted per search. Ordinary reactions (`as_tags: false`), absent
reaction data and zero counts do not match a tag.

The source selects Telegram's saved-dialog grouping, which may differ from the
forwarded author. Notes grouped under yourself use your `self` peer. Telegram's
anonymous source group is `tgpeer:v1:user:2666000`; it does not identify a hidden
author. Missing grouping metadata remains absent and cannot match a source
filter. No source is inferred from forward names, origin IDs or links. See
[Telegram's Saved Messages API](https://core.telegram.org/api/saved-messages).

Combine these filters with `query`, `pinned_only`, `media_type`, `sender`, `since`
and `until`. The query can be omitted when a filter is set. Sender and dates
refer to the containing saved copy. Saved-specific filters require an exact
Saved Messages peer; they are not accepted with a scope or another conversation.

Filtering uses bounded history or search candidates. Continue empty pages until
`next_cursor` is null, preserving every filter and `limit`. Narrow selections
can require many pages. This provides navigation through authorized saved
content, not a complete source/tag catalog or custom tag-name lookup.

All access checks use the containing Saved Messages peer, message range and
sender. A source ID grants no access to its original conversation and causes no
origin lookup. Restricted self-authored grants still exclude forwarded copies;
consented grants and Full read include otherwise supported copies. Search emits
no read receipts. Opening history, context or media retains the existing
acknowledgment contract. No tags, pins, messages or saved-dialog order are changed,
and no message or organization index is stored.
