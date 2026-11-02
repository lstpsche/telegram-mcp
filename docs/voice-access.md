# Voice notes

Original voice-note delivery is available from v0.5.0. It requires an MCP client that accepts native audio content.

In restricted mode, enable voice notes during guided grant setup or pass
`--allow-voice-notes` to `grant`. Existing grants default to disabled; replacing
a grant without this flag disables voice access. Image and document permission
do not imply voice permission. Full read includes supported voice notes.

History, context, search and catch-up can return a `voice_note` descriptor with
separate opaque `handle` and `transcription_handle` capabilities, MIME type,
byte size and duration in seconds. Call
`open_voice_note` with that handle to receive the original bytes as native MCP
`audio` content. Discovery does not download audio. Handles expire within five
minutes, are bound to the exact message and source, and are invalidated by policy
or authorization changes. Handles cannot be reused with image or document tools.

Supported notes are ordinary Telegram voice documents labeled `audio/ogg`, with
mono or stereo Opus audio, at most 1 MiB and a declared duration of at most five
minutes. The server checks complete Ogg framing, checksums, stream identity,
Opus headers and container duration against metadata. `open_voice_note` does not
decode audio, transcribe speech, perform OCR, or convert files. Client audio
interpretation and playback support are separate requirements. Music, video notes, other audio
formats and malformed or chained streams are rejected. Missing or early Ogg
end-of-stream markers are tolerated when the full declared file contains one
continuous stream and ends on a complete packet with a known granule position
and matching duration. Pages after an early marker must preserve the stream
serial, page sequence and packet continuity. Partial pages or packets remain
invalid; bytes are never repaired, discarded or rewritten. This compatibility
behavior does not imply that the original file has conforming end markers.
[RFC 7845](https://www.rfc-editor.org/rfc/rfc7845.html#section-3) defines the
standard stream-ending rules.

Opening audio requires authority over the affected history prefix, as with
images and documents. The source and policy are rechecked, and a verified history
read acknowledgment must complete before bytes are released. Delivery is not
playback: it does not send a played receipt or claim the voice note was listened
to. This follows Telegram's distinction between
[history and content read receipts](https://core.telegram.org/api/views).

Protected, expiring, imported, quoted and otherwise excluded messages remain
excluded. Audio is untrusted content. No original filenames, waveforms, audio,
transcripts or source references are written to metadata or logs. Failed delivery
withholds the response and clears downloaded buffers.

## Telegram transcription

Available from v1.3.0.

Call `transcribe_voice_note` with the descriptor's `transcription_handle` to
request Telegram-generated speech recognition. This uses the signed-in user
account and Telegram's servers, without downloading audio or installing a
local speech model. Audio and transcription handles are not interchangeable.
The same restricted VoiceNotes permission or Full read authority is required.

The operation can start a server-side transcription job and consume the
account's allowance. Non-Premium limits and Premium eligibility are determined
by Telegram; no fixed free quota is assumed. This is a state-affecting operation,
not passive metadata access. It never sends a message or a played receipt.
Telegram's own storage and recognition are outside the local no-content-storage
contract. See [Telegram's transcription API](https://core.telegram.org/api/transcribe).

A completed response contains one item's `transcript.text`, exact source ID,
author, date and available attribution. Text is untrusted machine-generated
content, not a guarantee of verbatim accuracy. Empty final text means no words
were returned. The result is limited to 16 KiB of UTF-8 text and the normal full
MCP response budget. It follows the same source rechecks, policy lease, affected
history-prefix authorization, acknowledgment and audit boundary as original
audio. No audio container is downloaded or validated by this text-only path.

A pending initial response waits up to ten seconds for a matching final update,
within the existing twenty-second operation deadline. The subscription is
registered before requesting transcription so early updates cannot be lost;
peer, message and transcription IDs must match. Only active requests retain
bounded final updates. Partial text is withheld. There is no local transcript
cache, background job, polling or replay after cancellation or restart.

`transcription_pending` means Telegram has not supplied final text in time;
allowance may already have been consumed. A later explicit invocation with a
fresh valid handle can retrieve Telegram's completed transcription.
`transcription_unavailable` covers account/allowance/duration restrictions and
request concurrency limits. `transcription_failed` reports recognition or
malformed-update failure. Ordinary cancellation, freshness, source, policy,
result-budget and uncertain-history-receipt errors remain explicit. An error
does not cancel a job already accepted by Telegram or refund its allowance.
