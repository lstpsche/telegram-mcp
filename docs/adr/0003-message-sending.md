# ADR-0003: Human-enabled message sending

- Status: accepted
- Date: 2026-11-17

## Context

Agents need ordinary Telegram sending primitives, including new recipients
identified by username or international phone number. The existing daemon owns
the only account session, enforces human policy and persists no message content.
Sending adds an irreversible external effect: a timeout or local failure can
occur after Telegram accepts a message. An unqualified retry can duplicate it.

## Decision

Keep the existing daemon, owner-only local transport and byte-only stdio relay.
Add `resolve_recipient`, `send_message` and `get_send_status` as static MCP tools.
Each resolves one recipient, sends one original plain-text message, or inspects
one retained local operation. Lists, campaigns, queues, scheduled work and
background retries are outside the server contract. Agents compose primitives
for their own workflows.

Sending is disabled by default. A human enables account-wide sending using
`sending enable --accept-account-wide-send`. This authority is independent of
read grants, Full read and scopes. It covers supported new conversations, bots,
groups, exact forum topics and joined broadcast channels where the account can
post. There is no per-recipient confirmation or MCP policy mutation. Logout,
authorization-epoch replacement and settings restore remove send authority.

Send permission includes bounded recipient metadata and exact reply-routing
preflight. A reply's typed ID is a claim, so the adapter verifies its actual peer
and topic using an exact-message lookup before dispatch. Telegram may include
incidental message bodies in metadata responses. Those bodies remain inside the
adapter, are discarded, and are neither exposed nor persisted. This narrow
permission does not authorize history delivery, body access, media or read
acknowledgments. It must not reuse a body-delivery workflow for preflight.

Aliases identify a requested lookup, never authority or operation identity.
Resolution validates the selected entity and returns a strict kinded peer ID.
Phone resolution requires an explicit international number, observes shared
three-second lookup spacing and never imports contacts. Failure does not prove
that a phone number has no Telegram account. No lookup joins a chat.

The send request preserves original text and specifies explicit author identity:
the logged-in user in groups/topics and the destination publisher for ordinary
broadcast posts. It disables automatic web previews, preserves native drafts,
permits no payment, and exposes no raw RPC flags. Exact forum-topic and reply
routing is validated. Deletion between preflight and send is a remote race;
an accepted message is not resent as a corrective fallback.

Every intended message has one canonical lowercase UUIDv7 operation ID. The
resolver can issue this key without reserving a row or initiating work; clients
can also generate valid UUIDv7s. Before dispatch, bind the key to its original
authorization epoch, exact peer/reply IDs, a domain-separated keyed digest of
the payload and a stable nonzero Telegram random ID. Persist dispatch before
network I/O, with no SQL transaction held across that I/O. Reuse of the key with
a changed payload fails before sending.

New dispatch and permitted retry require a key created within 24 hours, with at
most 60 seconds of future clock skew. Retain operation facts until seven days
after key creation, including across authorization changes. Keep at most 100,000
operations and 100,000 protocol-wait rows. Bounded cleanup removes expired facts;
capacity exhaustion rejects new work rather than evicting unexpired deduplication
records. Audit purge does not erase outgoing records. There is no content queue,
retention configuration or indefinite exactly-once guarantee.

Preserve positive acknowledgments before update synchronization can fail. A
valid server message ID establishes `sent`; provider time is included only when
actually supplied. Later persistence or synchronization errors must retain that
receipt. After cancellation, local receipt bookkeeping can use an independent
context capped at five seconds; no new network grace is added. Persistence
failure fences later sends. Once a send is invoked, any error without a positive
receipt is `uncertain`, including explicit rights, payment, rate or length
rejection. Hidden SDK reconnect retransmission can precede a returned error;
closed provider reasons and retry times cannot prove that no earlier effect
occurred. Only preflight failures with no send invocation establish `not_sent`.
Attempted failures use the top-level `send_effect_uncertain` category and retain
their closed diagnostic reason, including `message_too_long` for Telegram length
rejection. Preflight/admission validation can use `invalid_input`. Restart
reconciles facts and does not send queued work.

Persist absolute protocol waits by trusted environment, actual account, method
and applicable containing dialog. They survive same-account reauthentication
and permission toggles, but do not transfer to another account or environment.
Outgoing calls return required waits rather than sleeping through automatic
retries. Existing read-side username resolution observes the same method wait.

## Consequences

- Enabling send permission deliberately permits broad outbound effects. Prompt
  injection can cause disclosure or unwanted messages if an agent treats hostile
  Telegram content as instructions. Client tool allowlists are a separate control,
  not per-agent isolation supplied by the daemon.
- `sent` proves server acceptance, not recipient delivery or reading. Unavailable
  status and expired keys do not prove that a message was never sent.
- Metadata loss, another installation and deliberate clock rollback are outside
  the deduplication guarantee. There is no durable body from which to resume work.
- Human-only offline inspection can read retained facts even after sending is
  disabled or the daemon cannot start. It grants no authority and never retries.
- Synthetic integration checks, real-account qualification, installation,
  publication and client acceptance remain separate evidence.
