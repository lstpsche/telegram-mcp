# Link messages and previews

Source builds after 0.6.0 preserve messages with Telegram webpage previews.
History and context include the original `text` and an optional `link_preview`
with `state`, `manual`, and supplied `url`, `site_name`, `title`, and `description`.
The preview is separate from the message: Telegram's manual previews can refer
to a URL that does not occur in the text. A preview can also accompany empty text.

`state` reflects Telegram's supplied constructor: `available`, `pending`, or
`unavailable`. Pending and unavailable previews retain any supplied URL and the
original message text; they do not fabricate a title or description. These states
say nothing about page safety or reachability. Telegram controls preview content
and updates; this client does not refresh or cache pages.

Search and catch-up return `has_link_preview: true` with the existing bounded
snippet of the original message text. They do not include preview metadata or
substitute the preview description for the message. Use `get_message_context`
for the full authorized message and preview. Telegram controls search matching;
there is no local URL or page index.

URLs and all preview strings are untrusted display data. The server does not
visit URLs, render embeds, download preview photos/documents, expose cached-page
contents or raw webpage IDs/hashes, or treat Telegram's safety hint as approval.
No new tools, grants, network requests, dependencies or persisted content are
introduced. Ordinary containing-message policy, response limits and read receipts
apply. Protected, ephemeral, quoted, paid and other excluded content stays excluded.

Each supplied string must be valid UTF-8 and at most 4096 bytes. Malformed previews
fail with content-free errors; oversized complete responses fail before receipt
acknowledgment. No silent truncation is used for preview fields. A not-modified
page result cannot be resolved without a page cache and is rejected.

The adapter follows Telegram's [message media webpage](https://core.telegram.org/constructor/messageMediaWebPage),
[available page](https://core.telegram.org/constructor/webPage),
[pending page](https://core.telegram.org/constructor/webPagePending) and
[empty page](https://core.telegram.org/constructor/webPageEmpty) constructors.
