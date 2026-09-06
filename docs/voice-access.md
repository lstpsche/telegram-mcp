# Voice notes

Original voice-note delivery is available from v0.5.0. It requires an MCP client that accepts native audio content.

In restricted mode, enable voice notes during guided grant setup or pass
`--allow-voice-notes` to `grant`. Existing grants default to disabled; replacing
a grant without this flag disables voice access. Image and document permission
do not imply voice permission. Full read includes supported voice notes.

History, context, search and catch-up can return a `voice_note` descriptor with
an opaque handle, MIME type, byte size and duration in seconds. Call
`open_voice_note` with that handle to receive the original bytes as native MCP
`audio` content. Discovery does not download audio. Handles expire within five
minutes, are bound to the exact message and source, and are invalidated by policy
or authorization changes. Handles cannot be reused with image or document tools.

Supported notes are ordinary Telegram voice documents labeled `audio/ogg`, with
mono or stereo Opus audio, at most 1 MiB and a declared duration of at most five
minutes. The server checks complete Ogg framing, checksums, stream identity,
Opus headers and container duration against metadata. It does not decode audio,
transcribe speech, perform OCR, or convert files. Client audio interpretation
and playback support are separate requirements. Music, video notes, other audio
formats and malformed, chained or incomplete streams are rejected.

Opening audio requires authority over the affected history prefix, as with
images and documents. The source and policy are rechecked, and a verified history
read acknowledgment must complete before bytes are released. Delivery is not
playback: it does not send a played receipt or claim the voice note was listened
to. This follows Telegram's distinction between
[history and content read receipts](https://core.telegram.org/api/views).

Protected, expiring, forwarded, quoted and otherwise excluded messages remain
excluded. Audio is untrusted content. No original filenames, waveforms, audio,
transcripts or source references are written to metadata or logs. Failed delivery
withholds the response and clears downloaded buffers.
