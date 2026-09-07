# Search filters

Use `search_messages` with exactly one authorized `peer` or named `scope`.
Combine `sender`, `since` and `until` with an optional text `query`,
`pinned_only` and `media_type`:

```json
{"peer":"tgpeer:v1:chat:42","sender":"tgpeer:v1:user:7","since":"2026-09-01T00:00:00Z","until":"2026-09-07T00:00:00Z","limit":20}
```

`sender` is an exact `author` ID returned by history or search: a user or a
publishing channel. It does not select forwarded origin, channel signatures or
linked discussion authors. Saved copies retain their containing-message author.
Usernames, self-chat IDs, basic-group IDs and topic IDs are not sender IDs.

`since` includes its timestamp; `until` excludes it. Either bound can be omitted.
Timestamps require whole seconds and an explicit RFC3339 timezone. Equivalent
zone offsets normalize to the same cursor binding. Dates refer to the containing
message, not the original forward, edit time or attachment metadata.

Filters narrow authorized candidates locally. Text/media/pin selectors use
Telegram search; without them, discovery traverses history, positioned at
`until` when supplied. This avoids relying on empty-query search date behavior
and does not require a separate dialog or access hash for a sender. Narrow
filters may require many pages. `limit` bounds fetched candidates, not matches;
continue even empty pages until `next_cursor` is null. Repeat the same selectors,
filters and limit with the cursor. No message index is stored.

Search returns permitted snippets and attachment metadata without read receipts.
Use a returned ID with `get_message_context` for an acknowledged full body.
Sender/date filters never expand grants, scope membership or media permissions.
