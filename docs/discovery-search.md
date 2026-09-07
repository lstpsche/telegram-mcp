# Chat and topic search

Use an optional `query` on `list_chats` or `list_topics` to find authorized
conversations by title. Matching trims surrounding query whitespace, lowercases
Unicode text and performs a literal substring search. It is not fuzzy search,
username resolution, message search or permission to open a matching result.

```json
{"query":"backend","limit":100}
```

The example is a `list_chats` request. To search topics, call `list_topics` with
an exact parent forum ID and `query`. Use a returned immutable peer ID to read
or search content; titles remain untrusted display data. Chat search can also
use an existing `scope` to narrow discovery.

A supplied query must contain non-whitespace text, at most 1024 input UTF-8 bytes
and 256 characters after normalization. Omit it to list without title filtering.

Full read discovery retains its bounded candidate pagination. `limit` caps
scanned candidates, not matching results; a page can contain no matches and still
have `next_cursor`. Repeat the same normalized query, peer/scope selection and
limit while continuing until `next_cursor` is null. Cursors contain only a keyed
query digest, retain their original expiry and are invalidated by authority
changes. Existing unfiltered cursor encodings remain supported.

Restricted discovery examines only currently authorized grants; it never scans
other chats or infers topic access from a parent grant. Scoped/restricted chat
listing retains its candidate limit and partial reporting. Restricted topic
listing filters the complete bounded set of exact topic grants. Neither search
form acknowledges history, downloads media or stores titles or queries. Provider,
validation, expiry and required audit failures return errors without results.
