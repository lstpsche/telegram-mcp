# Batch search

Pass `searches` to `search_messages` to run several ordinary searches in one
agent roundtrip. Each entry accepts the same peer or scope, filters, limit and
cursor as a single search. Do not mix `searches` with top-level selectors.

```json
{
  "searches": [
    {"peer": "tgpeer:v1:chat:42", "query": "meeting", "limit": 20},
    {"peer": "tgpeer:v1:chat:42", "media_type": "pdf", "limit": 10}
  ]
}
```

The response's `items` contains deduplicated hits in first-seen order. The
`searches` array follows input order; each entry contains `messages` (IDs in
`items`), `partial`, `next_cursor`, and scope coverage when applicable. The
response's top-level `next_cursor` is null. Continue each search independently
with its returned cursor and the same selector, filters and limit, either alone
or in another batch. Empty pages may still have a continuation.

A batch accepts 1–10 searches whose limits sum to at most 100 candidates. Each
omitted limit defaults to 20. Filtered candidates consume that budget. All
searches share a 20-second deadline, at most 20 peer lookups, one policy lease,
and the existing 256 KiB combined structured/text response budget. Scope traversal
can pause at a peer boundary to reserve lookups for subsequent searches.

Search does not acknowledge history or clear unread mentions. Every search must
succeed before any results are released. An upstream failure, expired authority,
invalid cursor, oversized response, or conflicting observations of the same
message withholds the entire batch. Independent temporary media handles alone
are not conflicting observations. A batch is not an atomic Telegram snapshot;
messages can change between requests.
