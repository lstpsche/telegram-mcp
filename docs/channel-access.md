# Broadcast channel access

Version 0.6.0 supports joined broadcast channels through the existing chat,
history, context, search, catch-up, unread and attachment tools. No joining,
username lookup, global search or writing is performed. Protected, restricted,
left, minimal, forbidden and incompatible channel entities are rejected.

`list_chats` marks broadcasts with `broadcast: true`. Use the returned
`tgpeer:v1:channel:<id>` as the conversation or a scope member. In a post,
`author` identifies the containing channel as publisher. `channel_post` carries
optional `sender` and `signature` values supplied by Telegram. An anonymous
post has an empty object. These values are untrusted attribution, not proof of
personal authorship, grant selectors or instructions. Forward origin metadata
remains separate.

Full read includes supported channel posts. Restricted grants use the same
channel ID for `--peer` and `--author`, with `--profile consented`. Existing
message bounds, expiry, eligibility and read-through permission apply. Guided
access setup accepts the channel ID in its author prompt. Self-authored grants
and grants for a displayed sender never authorize channel posts. Image,
document and voice permissions remain independent. Existing grants need no
migration.

History/context and attachment delivery call `channels.readHistory` for the
containing channel, verify its inbox read position and synchronize the common
checkpoint before releasing content. Search/catch-up/unread discovery issue no
receipt. No channel update continuity or atomic remote snapshot is claimed.
Sender/signature changes around a media download or receipt withhold the result.

Telegram distinguishes history read state from viewport-driven view counters
and exposure metrics. A headless MCP has no viewport; it does not fabricate
visibility, view increments, playback, ad impressions or clicks.

References: [channel authors](https://core.telegram.org/api/channel),
[read and view semantics](https://core.telegram.org/api/views),
[sponsored messages](https://core.telegram.org/api/sponsored-messages),
[API terms](https://core.telegram.org/api/terms).
