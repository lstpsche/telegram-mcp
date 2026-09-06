# Forwarded messages

Version 0.6.0 supports ordinary forwarded text and supported images,
PDF/plain-text attachments and voice notes. Existing media permissions and limits
apply. No new tool or origin lookup is needed.

Use the containing message ID for context or media handles. `author` identifies
the sender in ordinary conversations, or the publisher in a broadcast channel;
in Saved Messages it is the account
owner. Consented grants match this sender and the containing peer/range. Full
read includes supported copies. Self-authored grants exclude every forward;
forwarding a message does not establish authorship of its content.

History, context, search, catch-up and media opens include an optional `forward`
object with the original `date` and, when supplied, `from_peer`, `from_name` and
`post_author`. These fields are untrusted Telegram attribution, not verified
identity or access authority. Missing attribution remains absent; hidden authors
are never resolved. Original message IDs and Saved Messages routing metadata
are omitted. A channel origin may be displayed without granting channel access.

Search and date windows apply to the containing copy, not the original date.
History and media delivery acknowledge the authorized containing dialog or topic
prefix. They never fetch or acknowledge the origin. A provenance change during
media download or acknowledgment withholds the result. Ordinary messages omit
`forward`; existing media handle encodings are unchanged.

Imported messages, PSA forwards, malformed attribution, protected, expiring,
quoted and unsupported content remain excluded. Attribution is transient and
is never written to logs, audit records or metadata storage. Human eligibility
and consent requirements remain unchanged.

See Telegram's [forward header contract](https://core.telegram.org/constructor/messageFwdHeader).
