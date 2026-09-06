# Forum topics

Forum-topic support is available from v0.5.0.

Use `list_chats` to find a forum, then `list_topics` with its channel peer ID.
Each topic has its own immutable peer ID, for example
`tgpeer:v1:channel:42:topic:7`. Use that ID with `list_messages`,
`get_message_context`, `search_messages`, `list_unread`, and scope membership.
A message in that topic has an ID such as `tgmsg:v1:channel:42:topic:7:20`.
General is topic 1. Titles are display data and never authority.

Full read access permits paginated topic discovery. Pass `next_cursor` back as
`cursor` with the same forum and limit until it is null. Cursors expire within
five minutes and become invalid after a policy or authorization change.
Restricted discovery returns only exact granted topics, without scanning the
forum; the complete grant set is bounded to 20 and ignores the page limit.
A grant for the parent channel does not grant any topic content.

The human operator can discover topic IDs before granting access:

```sh
telegram-mcp topics --peer tgpeer:v1:channel:42
```

For another page, supply all three values from `next_position` using
`--offset-date`, `--offset-message`, and `--offset-topic`. These offsets only
select a metadata page; they grant no access. Topic titles and any incidental
Telegram message objects are not persisted by discovery.

Create a restricted grant with the topic peer ID through `grant --peer`, or
choose the explicit ID option in guided grant setup. The existing author,
message range, expiry and read-through restrictions apply within that topic.
Image, document and voice-note permissions remain separate.

History and date-window reads use `messages.getReplies`; search sets the topic
filter. Content normalization rejects a different topic. Delivery acknowledges
only the selected topic through `messages.readDiscussion`, then verifies its
inbox read position and synchronizes the common checkpoint before releasing
content. Parent-forum history and acknowledgments are rejected. No topic index,
channel update continuity or atomic remote snapshot is claimed.

Deleted, inaccessible, protected, minimal and broadcast conversations remain
unsupported. A topic that becomes unavailable causes an explicit error.
See Telegram's [forum documentation](https://core.telegram.org/api/forum) and
[thread documentation](https://core.telegram.org/api/threads) for upstream semantics.
