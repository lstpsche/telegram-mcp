# Incremental catch-up

Start with an explicit date window on a named scope:

```json
{"scope":"tgscope:v1:0123456789abcdef0123456789abcdef","since":"2026-09-01T00:00:00Z","until":"2026-09-02T00:00:00Z","limit":100}
```

Follow `next_cursor` until it is null, including empty pages. Repeat the original
scope, dates and limit on every page. Only after consuming the complete traversal,
retain `scope.catch_up.checkpoint` from its terminal response.

For the next window, supply that opaque checkpoint instead of `since`:

```json
{"scope":"tgscope:v1:0123456789abcdef0123456789abcdef","checkpoint":"RETURNED_CHECKPOINT","until":"2026-09-03T00:00:00Z","limit":100}
```

Replace the example scope ID and checkpoint with actual returned values. The new
window starts at the previous window's exclusive `until`, so consecutive windows
meet without a timestamp gap or overlap. `until` must be later than that boundary.
The response echoes the resolved `since` and `until`. Do not combine `since` and
`checkpoint`. If the resumed window paginates, repeat the original checkpoint,
new `until` and limit with each `next_cursor`. Save its replacement checkpoint
only after consuming all pages.

The server keeps no bookmarks or message content. Replaying a checkpoint is valid;
clients decide when to store or replace it. A failed request returns neither content
nor a new checkpoint. Completed future-ending windows do not issue checkpoints,
since messages may still arrive before their end. Existing explicit date-window
calls remain supported.

Checkpoints are signed, versioned, bound to the exact scope, eligible membership,
account epoch and policy revision, and expire after at most 30 days or the earliest
selected grant expiry. Changes to any scope, grants or read mode invalidate them.
An ordinary pagination cursor is not a checkpoint. Checkpoints can survive a daemon
restart with the same key, epoch and policy. Expired or invalid checkpoints require
starting an explicit date window; the server never silently restarts from a guessed
time. Checkpoints do not authorize a conversation or read receipt.

Coverage means traversal of the currently authorized sending-date window. Excluded
peers and withheld content remain excluded and reported through existing coverage;
completion does not establish their absence. This feature does not detect edits,
deletions, newly accessible old content or delayed messages with old timestamps.
It is not a transactional Telegram snapshot. Choose an explicit overlapping date
window when rechecking older material is useful.

The existing 100-candidate page limit, 20-peer scope limit, operation deadline,
content-free audit and no-receipt discovery behavior apply. Open message context
or an attachment separately when the full authorized content is needed.
