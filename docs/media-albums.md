# Media albums

Source after 0.5.0 includes `album_id` on supported album members in history,
context, search/catch-up and explicit attachment opens. Match this string to
associate members across pages. IDs include the exact containing conversation
or forum topic, so matching Telegram group numbers in different conversations
do not combine. The 64-bit group value is encoded losslessly as hexadecimal,
not a JSON number. Treat the ID as opaque grouping metadata, never authority.

Each member keeps its own message ID, author, date and attachment handle.
Captions remain in that member's `text`; search and catch-up expose the usual
bounded `snippet`. Captionless members remain captionless. Do not copy a caption
onto another member or infer a missing caption. Use message context for the
full authorized caption; attachment opens return media and provenance metadata.

Results remain flat and newest-first within a peer. Pagination limits count
messages, not albums. Pages, search matches, date windows and permissions can
show only part of an album; no total member count or completeness is claimed,
even when the surrounding result is not marked partial. Use existing pagination
or bounded context to inspect additional authorized messages. Grouping adds no
extra fetches, downloads, read receipts or background work.

Every member must independently satisfy current peer, author, range, content and
media policy. Unsupported or protected members and their captions remain absent.
An album ID cannot be used with a tool in place of a message ID or attachment
handle. Explicit opens download only the selected member and compare album
membership before and after download and acknowledgment. A membership change
withholds the result. No album index, cache or membership list is persisted.

Telegram's [message constructor](https://core.telegram.org/constructor/message)
defines the grouped-media identity used by this feature.
