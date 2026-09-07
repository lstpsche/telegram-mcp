# Reaction summaries

Version 0.7.0 includes optional `reactions` in history, message context,
search and catch-up results. The summary contains `minimal`, `as_tags`, and an
ordered `counts` array. Each entry has `kind` and `count`:

- `emoji` includes the supplied Unicode `emoji` text.
- `custom_emoji` includes a lossless decimal `custom_emoji_id` string. It is an
  opaque display identifier, never a media handle or permission to fetch it.
- `paid` reports Telegram Stars, not the number of people who paid.

Counts are supplied snapshots that can change between reads. Do not sum them to
infer distinct people, agreement or sentiment. Missing `reactions` means no
snapshot was supplied; a present empty `counts` array preserves an explicit empty
snapshot. `minimal` preserves Telegram's reduced form, which omits personal
selection information; it is not a freshness or completeness guarantee.

`as_tags` distinguishes Saved Messages tags from ordinary reactions. In Saved
Messages it is true when Telegram supplies the tag flag or an empty reaction list.
Older Saved Messages with reactions and no tag flag remain ordinary reactions.
Tag names and account-wide tag counts are not fetched. Custom emoji appearance is
not resolved or invented.

Summaries reuse the containing message's authority, exclusions, response limits
and history/context acknowledgment. They do not authorize otherwise unsupported
messages, add a reaction-only lookup, or change attachment-open results. Search
and catch-up retain their existing snippets and do not acknowledge history reads.
There are no reaction writes, reactor-list requests, new tools, grants,
dependencies or stored reaction content. Reading a summary never issues a
reaction-read acknowledgment. Personal selections, recent reactors, paid
leaderboards and permission-to-list hints are omitted.

At most 100 unique entries are accepted. Emoji strings must be valid UTF-8 and at
most 128 bytes; counts must be nonnegative signed 32-bit integers. Empty/unknown
constructors, duplicate identities, malformed IDs/counts and oversized snapshots
fail closed. The existing complete-response budget applies before any history
receipt; summaries are not silently shortened to fit.

See Telegram's [message reactions](https://core.telegram.org/constructor/messageReactions),
[reaction counts](https://core.telegram.org/constructor/reactionCount),
[paid reactions](https://core.telegram.org/api/reactions#paid-reactions), and
[Saved Messages tags](https://core.telegram.org/api/saved-messages#tags) contracts.
