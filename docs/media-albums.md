# Media albums

Version 0.6.0 includes `album_id` on supported album members in history,
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
or bounded context to inspect additional authorized messages. Grouping alone adds no
extra fetches, downloads, read receipts or background work.

Every member must independently satisfy current peer, author, range, content and
media policy. Unsupported or protected members and their captions remain absent.
An album ID cannot be used with a tool in place of a message ID or attachment
handle. Explicit opens download only the selected member and compare album
membership before and after download and acknowledgment. A membership change
withholds the result. No album index, cache or membership list is persisted.

Telegram's [message constructor](https://core.telegram.org/constructor/message)
defines the grouped-media identity used by this feature.

## Expand an album around a message

```json
{"message":"tgmsg:v1:chat:42:20","expand_album":true}
```

`expand_album` defaults to false. When enabled, context scans at least nine older
and nine newer candidates around each target, expanding either side further when
`before` or `after` requests more. It returns authorized members sharing the
exact target's album ID plus the explicitly requested chronological neighbors.
A non-album target returns just its requested context. No media bytes are downloaded.

The target's `album_context` contains `state: "bounded"` and `messages`, a
newest-first list of the returned album member IDs. This is not a total membership
list or proof of completeness: unsupported, denied, deleted or more distant
members may be absent. Bounded album expansion marks the context partial. A target
without an album has `state: "not_album"` and an empty member list.

The expanded candidate bound, plus reply depth, counts against the shared
100-candidate budget. With no other options, each target reserves 19 candidates,
so one batch can expand at most five targets. Existing neighbor positions count
withheld candidates too; expansion does not replace a denied neighbor with an
older message. Use larger before/after bounds when more surrounding context is
needed, staying within the shared budget.

Every returned member passes its own grant and media checks. The final returned
context determines the read-through boundary; unrelated scanned neighbors do not
extend it. The whole batch is prepared before any receipt. Receipt, expiry and
required-audit failures still withhold all bodies. No album index or checkpoint
is created.
