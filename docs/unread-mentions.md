# Unread mentions

Use `search_messages` with exactly one authorized `peer` or named `scope` and
`unread_mentions_only: true`. Exact forum topics retain their own authority.

```json
{"peer":"tgpeer:v1:chat:42","unread_mentions_only":true,"limit":20}
```

Telegram supplies unread-mention candidates. The adapter retains mention/unread
evidence, and the reader intersects it with current grants and optional sender,
date, pin, attachment, reply and Saved Messages filters. An optional `query`
uses a literal case-insensitive substring of message text for this selector;
it does not search attachment bytes or use Telegram's ordinary text-search rules.

The limit counts fetched candidates, including excluded and nonmatching entries.
Continue empty pages while `next_cursor` is present, preserving the selector,
query, filters and limit. Cursors bind the selector, epoch and policy revision.
Unread state may change in other clients during traversal; this is not a snapshot.

Results contain ordinary authorized snippets and source references. Search does
not mark history or mentions read, clear mention counters or download media.
Opening context still requires its separate read-prefix permission. Existing
unsupported content exclusions apply, including unread-mentioned media variants
that the delivery path does not support.

Provider contract: [Telegram unread mentions](https://core.telegram.org/method/messages.getUnreadMentions).
