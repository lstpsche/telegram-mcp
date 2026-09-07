# Telegram message links

`get_message_context` accepts a strict message ID or a supported HTTPS Telegram
message link in `message` and in each entry of `messages`.

```json
{"message":"https://t.me/c/42/20"}
```

Supported hosts are exactly `t.me`, `telegram.me` and `telegram.dog`. Supported
paths are `/c/CHANNEL/MESSAGE`, `/USERNAME/MESSAGE`, and the corresponding forum
forms `/c/CHANNEL/TOPIC/MESSAGE` and `/USERNAME/TOPIC/MESSAGE`. An optional
`thread=TOPIC` query can supply the forum topic instead of the path segment.
The optional bare `single` flag is accepted; the requested message remains the
target, including when it belongs to an album. Neighbors are controlled by the
explicit `before` and `after` options.

Numeric links use existing adapter metadata and the same exact grants as strict
IDs. If metadata is missing, discover joined chats first with `list_chats`.
Public username links require Full read before Telegram name resolution; only
supported joined channels and supergroups are accepted. The resolved immutable
peer and message are independently authorized. Usernames are mutable navigation,
never permission. No conversation is joined, no web page is visited, and no URL
or username is stored in logs or metadata.

Forum links must explicitly identify their topic; a parent conversation grant
never grants topic content. Topic-only links, ordinary non-forum thread selectors,
linked-comment selectors, media timestamps, invite/action links, `tg:` URIs,
fragments, encoded path components and other query parameters are not supported.
They fail rather than silently opening another message. Use strict returned IDs
for private user chats, Saved Messages and basic groups. Linked discussions remain
available through [discussion resolution](reply-context.md).

Authorized messages and search/catch-up hits in channels, supergroups and forum
topics include `url`: a canonical numeric `https://t.me/c/...?...` citation that
avoids mutable usernames and round-trips through context input. Private user
chats and basic groups omit it because Telegram does not define this message-link
form for them. A citation grants no access and its destination may later become
unavailable. Existing policy, exclusions, response budgets and read receipts apply.

[Telegram message-link specification](https://core.telegram.org/api/links#message-links)
