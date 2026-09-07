# Media search

Find supported attachments using `search_messages` with a `media_type`:

```json
{"peer":"tgpeer:v1:chat:42","media_type":"pdf","limit":20}
```

Use exactly one `peer` or human-configured `scope`. Exact forum topics use the
same topic IDs and authority as ordinary search. An optional `query` narrows
Telegram's search results; it does not search inside PDFs, text files or audio.
With a media filter, query may be omitted or empty. A nonempty query is required
only when no filter is active; see [sender/date filters](search-filters.md) and
[Saved Messages](saved-messages.md).

| `media_type` | Matches |
| --- | --- |
| `photo` | Supported Telegram photos |
| `image_file` | Supported static JPEG/PNG images sent as files |
| `pdf` | Supported PDF attachments |
| `text_file` | Supported UTF-8 plain text, Markdown, CSV and JSON attachments |
| `voice_note` | Supported original Ogg/Opus voice notes |

A photo and an image sent as a file belong to different categories. Text-file
search finds attachments, not ordinary text messages. Discovery relies on
Telegram's metadata; bytes and encoding are validated when a handle is opened.
Existing media permissions, source restrictions and size/duration limits apply.
Full read includes supported media; restricted grants need the corresponding
image, document or voice-note opt-in.

Combine `media_type` with `pinned_only: true` to find pinned attachments. Results
retain the usual snippets and media handles, with no downloads or read receipts.
Use `open_image`, `open_document` or `open_voice_note` for delivery under the
existing source revalidation and read-acknowledgment rules.

Repeat the same peer or scope, normalized query, media type, pin filter and limit
with `next_cursor`. Changing a filter invalidates the cursor. Scoped results
remain canonical peer-ID order and newest-first within each peer. Continue until
`next_cursor` is null, even if a page is empty. The limit counts fetched
candidates, including filtered entries; pin state and results may change between
calls.

Telegram's [document filter](https://core.telegram.org/constructor/inputMessagesFilterDocument)
is broader than PDF, text-file or image-file eligibility. The reader checks each
returned attachment after policy and media validation. Photo and voice searches
use their corresponding provider filters. When combined with pins, Telegram's
pin filter selects candidates and the reader checks the media type. Media-type filtering introduces no extra search stream or local content index. An empty result does not prove the conversation has no attachments.

Use `filename_query` to match a literal, case-insensitive substring of an
attachment filename, for example `{"peer":"tgpeer:v1:chat:42","filename_query":"report"}`.
The optional filename is returned on supported permitted document, image-file
and voice-note descriptors. It remains untrusted display text, never a path or
file-type authority. A file named report.exe may still be a declared PDF;
existing MIME and byte validation decide how it can be opened. Ordinary photos
and attachments without a filename do not match.

Combine the selector with any existing peer/scope/batch search filters. When
filename_query is the only selector, existing bounded history traversal finds
candidates. Captions and file contents do not participate in this filter.
Continue empty pages; repeat the same normalized filename query with each cursor.
Queries are trimmed, lowercased and bounded to 256 Unicode characters/1024 bytes.
Discovery downloads no bytes and performs no receipts. A changed filename
invalidates an earlier media handle; discover a fresh handle before opening.
