# Batch message context

Use `get_message_context` with `messages` to open several search results in one
MCP call. Supply either `message` or `messages`, never both.

```json
{
  "messages": ["tgmsg:v1:chat:42:20", "tgmsg:v1:channel:99:25"],
  "before": 2,
  "after": 1,
  "reply_depth": 1
}
```

IDs above are synthetic. Use returned IDs or supported [message links](message-links.md).
The same options apply to each target. At most 20 distinct targets are accepted;
`target count × (1 + before + after + reply_depth)` must not exceed 100.
Missing, denied or excluded targets fail the entire call. Different links or IDs
that resolve to the same target are duplicates and rejected.

`items` contains deduplicated messages, ordered by canonical peer ID and then
newest first. `contexts` preserves request order: each entry contains `target`,
its newest-first `messages` references and `partial`. References point into
`items`; overlapping windows share one body. Conflicting observations of the
same message reject the batch rather than presenting an inconsistent copy.
Excluded neighbors and unavailable reply parents retain the existing partial
context behavior. There is no atomic Telegram snapshot or batch pagination.

The entire batch shares one policy lease, a 20-second operation deadline,
grant-expiry checks and the existing 256 KiB complete mirrored response budget.
Every target's grant and range are checked before history fetches. The response
is assembled and serialized before any read acknowledgment. Smaller batches or
fewer neighbors may be needed for large messages.

`read_effect.kind` is `history_marked_read` and `through_message_ids` contains
one acknowledged boundary per exact conversation or forum topic. Whole-prefix
permission is required through each boundary, including undisplayed earlier
messages. Acknowledgments are sequential and coalesced to the highest returned
message in each conversation. No bodies are released unless every required
receipt and audit succeeds. Failure after any possible receipt reports
`read_effect_uncertain`: some conversations may already have been marked read.

Single-target calls using `message` retain their flat `items` response and
singular `read_effect.through_message_id`. No new tool, database migration,
content cache or credential permission is introduced. [Reply context](reply-context.md)
and media permissions apply to both input forms.
