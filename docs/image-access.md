# Image access

Image access requires a ready authorized account and either Full read access
or an exact human grant with `--allow-images`. Full read access includes images
from all supported authors and message IDs in supported conversations. Add that standalone flag to the grant command documented in
[text access](text-access.md). Existing grants default to `images: false`;
replacing a grant without the flag disables images. The flag does not expand
its author, message range, expiry, or separate read-prefix authority.
`telegram-mcpctl grants` reports the current `images` setting.

`list_messages`, `get_message_context`, and `search_messages` include an
optional `image` object only for permitted images. It contains `handle`,
`kind` (`photo` or `document`), `mime_type`, `width`, `height`, and `size`
in bytes. Discovery downloads no image bytes. A permitted image can have empty
text or a caption; history/context can discover images with no caption.
Search still requires a nonempty query and follows Telegram's matching rules.
Search metadata has no read acknowledgment; history/context retain their
existing acknowledgment before delivery.

Call `open_image` with exactly `{ "handle": "<returned handle>" }` to retrieve
one image. The result contains a native MCP image block and the standard
metadata envelope, mirrored identically in text and structured content. The
single metadata item contains the exact message ID, author, date, and image
descriptor. Image bytes appear only in the native block. Consumers need native
image support; synthetic protocol tests do not establish that a particular
model can interpret an image. The native representation follows the
[MCP tools specification](https://modelcontextprotocol.io/specification/2025-11-25/server/tools).

Supported content is ordinary Telegram photos and static JPEG/PNG document
attachments. The largest complete photo rendition within the bounds is chosen
deterministically; failed retrieval never substitutes another rendition.
Files are limited to 1 MiB, 4 million pixels, and 4096 pixels per dimension.
The actual encoding, dimensions, complete decoded data, and container framing
must match. Truncated, appended, animated PNG, and malformed images fail.
The original bytes are returned without conversion or resizing.

Protected, expiring, view-once, spoiler, paid, live-photo, sticker, animation,
video, voice, round-video, alternate-document, and unsupported media are
excluded. Incoming unread media mentions are also excluded because they can
require a separate content-read action. Ordinary images use the authorized
history acknowledgment described by Telegram's
[read-state semantics](https://core.telegram.org/api/views). Delivery reports
`history_marked_read` through the source message; it does not claim human
viewing. Current authority must cover the entire affected dialog prefix, including
undisplayed messages.

Handles expire within five minutes or at grant expiry, whichever is earlier.
They bind the exact source, a keyed media identity digest, operation,
authorization epoch, and policy revision under a separate signing domain.
They contain no raw Telegram file ID, access hash, file reference, filename,
caption, or image bytes. An access-mode, grant or scope change invalidates previous handles.
An ordinary daemon restart preserves a handle only while its key, epoch,
revision, and fixed expiry remain valid. A handle never grants permission.

Every open checks current permission before source lookup, reauthorizes the
exact normalized message, downloads through explicit bounded DC pools, and
validates the bytes. The source is checked again after download and after the
hooked acknowledgment. Expiry, cancellation, readiness, and required audit
must still permit release. Any failure after a possible acknowledgment returns
`read_effect_uncertain` with no image. Telegram can change after a remote
check; these checks do not provide an atomic remote snapshot.

Image DC IDs are resolved through the selected environment's current SDK DC
options. Production media is not restricted to the three Test DCs. Unknown DCs
fail during pool resolution; message metadata never supplies a network endpoint.

Downloads use aligned sequential 64 KiB file requests, at most 17 application
file calls including one expired-reference renewal. Renewal refetches the same
message, requires unchanged image identity, and resumes the same offset.
Unexpected routing or CDN responses fail. Pool setup, authorization transfer,
update synchronization, and bounded transport retries are distinct from that
file-call count. The shared concurrency/rate/flood controls and 20-second
operation deadline still apply. Telegram documents file request alignment and
reference renewal in [file downloads](https://core.telegram.org/api/files) and
[file references](https://core.telegram.org/api/file_reference).

The complete image result is capped at 2 MiB, including native base64 data,
metadata mirrors, and framing, before acknowledgment. Metadata retains the
256 KiB text budget; inbound MCP frames retain their 64 KiB limit. Images,
filenames, file locations, and captions remain transient and are never cached,
indexed, logged, or written to temporary files. A connected client or provider
can retain delivered content under its own settings. Treat all visual content
as untrusted data.

Automated acceptance uses in-memory synthetic JPEG/PNG fixtures and the actual
stdio relay. Live account content acceptance, production eligibility, signing,
installation, and publication remain separate decisions.
