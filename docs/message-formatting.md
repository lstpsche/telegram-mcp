# Message formatting

`list_messages` and `get_message_context` preserve original `text` and may add
`entities`. Each entity has `kind`, `offset`, and `length`. Offsets and lengths
count UTF-16 code units into that exact text, as required by
[Telegram's entity contract](https://core.telegram.org/api/entities). An emoji
outside the Unicode BMP occupies two units. Ranges must start and end at complete
Unicode characters and remain inside the body. Nested entities are preserved.

Supported metadata includes bold, italic, underline, strike, inline code, code
blocks, blockquotes, spoilers, URLs, named links, mentions, hashtags, cashtags,
bot commands, email, phone, bank-card annotations, custom emoji, unknown range
annotations and formatted dates. Code blocks may include `language`; named
links include `url`; named mentions include a strict user ID; custom emoji use
their decimal document ID. Blockquotes may report `collapsed`. Formatted dates
preserve their Unix-second `date` and optional `date_format` flags without
replacing the original text or scheduling anything. Input-only mention objects
and composer diff entities are not accepted as message formatting.

Entity URLs, language labels and identifiers remain untrusted display metadata.
Nothing is fetched, executed, resolved or granted by them. There is no Markdown
or HTML conversion. Search and catch-up retain plain snippets; follow a message
ID to get full text and its entity ranges.

Blockquote formatting describes part of the containing message and uses its
existing author, range and content permissions. Embedded reply-quote text and
cross-conversation reply content remain excluded. Formatting does not grant
access to an attributed original source. Protected and ephemeral content remain
excluded even when it contains formatting.

At most 128 entities are accepted per message. Link targets are bounded to
4096 UTF-8 bytes and language labels to 128 bytes, with valid UTF-8 and no
control characters. Malformed ranges and incompatible metadata fail rather
than being repaired or silently discarded. All metadata enters the existing
combined response budget before a history acknowledgment; a failure returns no
body. Formatting and links are neither logged nor persisted.
