# Sending messages

Telegram MCP provides three sending primitives through the existing daemon:
resolve one recipient, send one plain-text message, and inspect a retained
operation. It is an unofficial client using Telegram's API. Sending is disabled
by default and uses the same single user account as reads.

## Enable and disable

Inspect and change permission using the human CLI:

```sh
telegram-mcp sending
telegram-mcp sending enable --accept-account-wide-send
telegram-mcp sending disable
```

Inspection returns `{"enabled":true}` or `{"enabled":false}`. Enabling requires
the exact flag. It permits connected agents to resolve recipients and send as
your account, including new conversations, bots, supported groups and joined
channels where you have posting rights, without per-recipient confirmation.
Sending to a bot can trigger its behavior. This is an account-wide permission;
read scopes do not constrain it. Your MCP client's tool allowlists remain a
separate control.

Full read does not enable sending. Sending does not enable Full read, restricted
body/media access or read acknowledgments. It includes only bounded recipient
and reply-routing metadata preflight. Incidental message bodies returned by those
metadata RPCs stay inside the adapter and are discarded without delivery or
persistence.

Permission survives ordinary restarts. Logout, authorization-epoch replacement
and settings restore remove it. Disabling serializes with an active operation;
it may report busy while that bounded operation finishes. Successful disablement
prevents later send effects but cannot undo an accepted message. None of these
policy mutations is an MCP tool.

## Resolve one recipient

Call `resolve_recipient` with exactly one selector:

```json
{"username":"recipient"}
```

```json
{"phone":"+12025550123"}
```

```json
{"peer":"tgpeer:v1:user:834726192"}
```

These illustrate input shapes; substitute the intended recipient. A username
may have one leading `@`. It must be 1–32 ASCII letters, digits or underscores,
starting with a letter; short assigned usernames are supported. A phone number
requires `+` and 2–15 international digits, beginning with a
nonzero digit. The server never guesses
a country, imports contacts, joins a chat or tries another selector after a failed
lookup. Phone lookup privacy or reachability failures do not prove that a person
has no Telegram account. Shared phone lookups are spaced by at least three
seconds and required waits are returned to the caller.

The result contains a validated immutable `peer`, its kind, optional untrusted
display metadata and a fresh UUIDv7 `operation_id`. Known-peer resolution also
supplies a key to callers that already know the destination. Resolution creates
no outgoing operation record and sends nothing. A successful resolution is not a
promise that Telegram will permit a message.

## Send one message

Use the exact peer and operation ID from resolution:

```json
{
  "peer":"tgpeer:v1:user:834726192",
  "text":"The meeting starts at 10:00 UTC.",
  "operation_id":"<operation_id returned by resolve_recipient>"
}
```

The `operation_id` placeholder must be replaced with the returned UUIDv7. Client
integrations may instead generate a canonical lowercase RFC 9562 UUIDv7 when
creating an intended operation. UUIDv4s and arbitrary opaque keys are rejected.
One ID identifies one intended message, not every message with identical text.

`send_message` accepts only one strict `peer`, one `text`, one `operation_id`,
and optional `reply_to`. Text must be valid UTF-8 and contain non-whitespace text,
within 4096 UTF-16 units. Original bytes are preserved: no trimming, splitting,
personalization, Markdown/HTML interpretation or footer is added. Telegram's own
length restrictions can still reject a message with `message_too_long`.
Automatic link previews are disabled, native drafts are preserved and paid
messages are not authorized.

Supported destinations are Saved Messages, private users or bots, basic groups,
joined ordinary supergroups, exact forum topics and joined broadcast channels
where the account can post. Groups/topics use the logged-in user as author;
broadcast posts use the destination publisher. Known posting restrictions fail
during metadata preflight. Anonymous group admins cannot use the required
user-author identity; no alternate identity is substituted. Saved alternate author
settings are not a fallback. There is no join, custom author, attachment upload,
rich-text composer, schedule, edit, delete, forward or reaction operation.

To reply, supply a strict message ID in the same exact peer/topic. The adapter
checks the live message's actual containing peer and topic before dispatch,
without returning its body or acknowledging history. A missing or mismatched
target fails; do not remove `reply_to` and resend unthreaded as recovery. A forum
parent is not a send destination without an exact topic. Non-General topics use
their validated root for an ordinary topic message and retain the topic root on
replies. General uses Telegram's ordinary-supergroup routing. Deletion after
preflight remains a remote race; Telegram can accept a topic-preserving fallback
without an atomic guarantee that the original reply relationship survived.

There is no list parameter, server batch sender, durable outgoing queue or
background retry. An agent can compose individual calls for a multi-recipient
workflow, subject to permission, Telegram restrictions and explicit outcomes.
No universal sending throughput is promised.

## Inspect outcomes and retry safely

`send_message` and `get_send_status` return one operation in the common versioned
envelope. It includes `operation_id`, `peer`, `state`, and, when known,
`message_id`, Telegram-supplied `sent_at`, fixed `failure_code` and `retry_at`.
The envelope's `checked_at` is inspection time, not a fabricated sending time.

| State | Meaning |
| --- | --- |
| `not_sent` | Dispatch did not reach a send invocation, for example because a preflight check failed |
| `in_flight` | A dispatch has been recorded and its outcome is still pending |
| `sent` | A valid Telegram message ID proves server acceptance; recipient delivery and reading are unknown |
| `uncertain` | An attempt may have sent, but no valid retained receipt establishes acceptance |

After a lost response, retain the original peer, text, reply target and operation
ID. Inspect it with:

```json
{"operation_id":"<original operation_id>"}
```

`get_send_status` requires send permission and a usable local store, but does not
require Telegram readiness, perform history reads or retry anything. Inspecting
an uncertain operation is a successful query whose item remains `uncertain`.
Only current-epoch operations are disclosed through MCP. Missing or pruned status
returns `operation_unavailable`, never `not_sent`.

Repeating `send_message` with the same ID and exact payload returns an already
confirmed receipt without another send. A changed peer, text, reply or send option
returns `operation_conflict` before Telegram I/O. Only an eligible unchanged
`not_sent` operation may be retried within its dispatch window and after any
required wait. Once a send is invoked, any error without a positive receipt is
`uncertain`, including explicit permission, payment, rate-limit or length errors.
The SDK can reconnect and retransmit before an acknowledgment, so a later error
cannot rule out earlier acceptance. The fixed failure reason and retry time are
retained in the operation item for diagnosis; they do not authorize a retry of an
uncertain operation. The top-level error category is `send_effect_uncertain`,
even when the item's reason is a specific rejection such as `message_too_long`.
Do not automatically resolve again, replace the operation ID or infer that an
error means no message was sent. A new key requests a new operation and can
duplicate an uncertain one.

New dispatch and permitted retry are limited to 24 hours after the UUIDv7's
creation time, allowing at most 60 seconds of future clock skew. Status facts are
retained until seven days after key creation. Expired keys cannot initiate a new
send, including after their records are pruned. A retained confirmed receipt can
still be returned after the dispatch window. Expiration never proves an uncertain
message was unsent. There is no indefinite exactly-once guarantee; metadata loss,
another installation and deliberate clock rollback are outside the guarantee.

The store caps retained operations and protocol-wait rows at 100,000 each.
Bounded cleanup removes expired rows; full capacity rejects new work before
Telegram I/O rather than evicting unexpired operation facts. Message bodies,
usernames, phone numbers and display names are not stored in this ledger. Only
exact IDs, keyed payload digests, stable random IDs, states, receipts and fixed
diagnostics needed for correctness are retained. Audit purge does not erase them.

Provider waits use an exact UTC `retry_at` when needed; an error contains either
that field or the existing bounded `retry_after_seconds`, never both. Applicable
method/account/dialog waits survive permission toggles and same-account
reauthentication. The server does not sleep through an outgoing retry loop.

A synchronous failure sets MCP `isError: true` while preserving known operation
facts. A later local failure can accompany `state: sent` and a valid receipt.
Cancellation stops the invocation; local receipt bookkeeping may continue for
at most five seconds, without more network grace. A canceled update enqueue
alone does not fail the shared read runtime. Incomplete synchronization still
fences new sends until restart; retained status and confirmed receipt replay
remain available. Restart reconciles metadata and does not resume outgoing work.

## Human offline inspection

```sh
telegram-mcp sending status --operation-id UUID
```

This human-only command reads an existing private metadata store without
initialization, migrations, credentials, Telegram access or account/policy locks.
It works when sending is disabled, after logout, or when the daemon cannot start.
It reports retained facts and never renews authority or retries a message.
Missing metadata, an incompatible schema or a missing/pruned operation makes
status unavailable rather than establishing non-delivery.

Synthetic checks do not establish real-account reachability, phone privacy
behavior, throughput or client acceptance. Installing a build, authenticating,
enabling sending and qualifying it with real recipients remain separate actions.
