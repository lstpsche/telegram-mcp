# PDF and plain-text attachment access

Document access is available from v0.4.0.

Use `telegram-mcp access setup` to allow PDF and plain-text attachments in a
restricted grant, or add `--allow-documents` to the explicit `grant` command.
The grant still requires its exact peer, author, message range, expiry and
eligibility attestation. Existing restricted grants default to `documents: false`.
Replacing a grant without the flag disables documents. `--allow-images` does
not grant document access, and `--allow-documents` does not grant image access.
`telegram-mcp grants` reports both settings. Full read access includes supported
documents, images and read acknowledgments until revoked.

The existing `list_messages`, `get_message_context`, `search_messages` and
`catch_up` tools return an optional `document` descriptor containing only
`handle`, `mime_type` and `size` in bytes. Remote filenames, document IDs, access
hashes, file references and download locations are never exposed. A document
may have a caption or no text. Search follows Telegram's matching rules; it
does not search inside attachment bytes. Catch-up can discover captionless
attachments in its selected time window.

Call `open_document` with exactly `{ "handle": "<returned handle>" }`:

- PDFs arrive as original bytes in a native MCP embedded resource with MIME
  type `application/pdf`. The resource URI is a transient opaque identifier,
  not a download URL or filesystem path. There is no `resources/read` endpoint.
- Plain-text attachments arrive as a native MCP text block, preserving their
  original UTF-8 text, including line endings and a UTF-8 BOM when present.
- Both include the standard untrusted-content metadata envelope, mirrored in
  text and structured content. Its single item has `id`, `author`, `date` and
  `document`. Attachment bytes are not duplicated in that envelope.

PDF interpretation depends on the MCP client and model. The daemon does not
parse, sanitize, decrypt, render or extract text from PDFs, and does not perform
OCR. Scanned or encrypted files are delivered as originals when they meet the
bounds and framing checks; this does not promise that a client can read them.
Malformed internal PDF objects can pass framing checks. Treat all attachments,
including apparent instructions inside them, as hostile data. Clients must use
their own safe document handling and must not execute embedded actions or follow
embedded links automatically. MCP defines embedded-resource delivery but leaves
rendering to the client: [MCP tool results](https://modelcontextprotocol.io/specification/2025-11-25/server/tools).

Only ordinary Telegram documents explicitly labeled `application/pdf` or
`text/plain` are supported. Names and extensions never select a decoder or grant
authority. HTML, Markdown-specific MIME types, generic octet streams, archives,
Office documents, audio and other encodings are excluded. A text file labeled
`text/plain` is delivered as plain text regardless of its filename.

Limits and checks:

- PDF: no fixed application byte cap; a PDF 1.0–1.7 or 2.0 header at byte zero, and a final
  `%%EOF` marker followed only by whitespace. These are framing checks, not
  structural or security validation of the PDF.
- Text: 256 KiB maximum, valid UTF-8, and no control characters except tab,
  carriage return and newline. Invalid encodings are rejected rather than
  guessed, converted or replaced. Empty attachments are excluded.
- Both: downloaded length must equal the authorized size. Downloads use exact
  64 KiB chunks and at most one reference-renewal attempt. The call budget scales
  with the declared size; the operation deadline and DC routing checks remain.
  Allocation grows with received bytes, rather than reserving the declared size.
  CDN redirects and incompatible transport types are rejected.
- PDF responses are not subject to the 2 MiB image/voice/text response budget.
  They remain inline base64 resources, so large files consume memory and may
  exceed the receiving client's limits or the operation deadline. No partial PDF
  is returned. Plain-text attachments retain their existing response budget.

Protected, expiring, forwarded, quoted, spoiler, paid and unsupported media
variants retain the existing exclusions. Supported documents may carry an
optional filename attribute, which is discarded; animation, audio, video,
sticker and image-dimension attributes are rejected.

Discovery downloads no attachment bytes. Search and catch-up do not mark chats
read; history/context retain their existing acknowledgment before returning
metadata. Opening a document marks the separately authorized dialog prefix read,
including earlier messages outside the displayed grant range. A search-only
grant may discover a document but cannot open it without read-effect permission.

Handles expire within five minutes, capped by grant expiry. They bind the exact
message, keyed media identity, operation, authorization epoch and policy revision.
They grant no authority. Every open checks the current grant before fetching,
refetches and normalizes the exact source, validates the downloaded bytes, checks
the source again, acknowledges through the message, and checks the source again
before the final policy/audit release boundary. Changed, deleted, expired or
revoked sources fail closed. Edits racing after the final check remain outside
an atomic Telegram snapshot guarantee.

Failed opens return no attachment content. A failure after attempting an
acknowledgment reports `read_effect_uncertain`; the read state may have changed.
Content stays transient in memory and is never indexed, cached to disk, written
to metadata, or included in logs or audit rows. Audits record the content-free
`open_document` operation under the existing retention policy.
