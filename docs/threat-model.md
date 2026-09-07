# Telegram MCP threat model

- Status: account runtime and authorized text/image MCP implemented; live acceptance separate
- Date: 2026-09-05
- Scope: local single-account macOS, Linux and Windows architecture

## Security objectives

1. An MCP client can retrieve only explicitly authorized and content-eligible
   bounded results.
2. Authentication, consent, policy mutation, and account administration remain
   outside the model-facing protocol.
3. Telegram content is not persisted. Credentials remain in private local storage
   and never appear in logs, errors, tool instructions or process configuration.
4. Read-state side effects, freshness, partial results, and degradation are
   represented truthfully.
5. One daemon owns one account session and update state; no relay or client can
   create a fallback Telegram connection.

## Assets

- Telegram authorization/session bytes, `api_hash`, login code, and 2FA secret;
- integrity keys for cursors and resource handles;
- immutable peer policy, consent scope, policy revision, and authorization
  epoch;
- access hashes and update checkpoints;
- Telegram message/search/media content in transient memory;
- metadata audit integrity and operator understanding of side effects.

## Actors and assumptions

- The operator controls the OS account and performs interactive control-plane
  actions.
- Telegram and gotd are external/upstream trust dependencies.
- An MCP client or model may be buggy, compromised, or adversarial.
- Telegram content, peer metadata, and entities are hostile input and may
  contain prompt injection, malformed Unicode, or oversized structures.
- Other local processes running as the same OS user are **not** isolated by
  this design. They can potentially inspect or replace accessible files and
  invoke installed binaries. The binary split and permissions are defense in
  depth, not a cryptographic same-user boundary.

## Trust boundaries

```text
untrusted model/client
  -> canonical MCP stdio
byte-only relay
  -> owner-only local socket or Windows named pipe
single-account daemon
  -> schema/budget and application boundary
  -> default-deny policy before fetch and after normalization
  -> Telegram adapter / metadata store / private local secret file
  -> Telegram MTProto

human operator
  -> interactive control binary
  -> daemon-controlled privileged operations
```

## Threats and required controls

### Capability expansion through MCP

Threat: a client invokes authentication, raw RPCs, writes, peer enumeration, or
policy mutation.

Controls: deterministic static read-tool inventory; no control-plane MCP tools;
strict unknown-field rejection; typed IDs; no `resolve_peer` tool; hard budgets;
no dynamic write flag. Only status, six bounded text/metadata tools, and explicit image opening are registered.

### Mutable alias or confused-deputy authorization

Threat: a username/title/link changes or a cursor is replayed to reach a denied
peer.

Controls: store kinded immutable IDs only; preflight policy before Telegram I/O;
postfilter every normalized result; bind cursors/handles to operation, query,
authorization epoch, policy revision, and expiry; reauthorize every use. Access
hashes stay adapter-private.

### Prompt injection and data exfiltration

Threat: Telegram text changes instructions, tool schemas, logs, errors, or later
behavior.

Controls: content remains typed result data; never interpolate it into control
text; fixed error messages; closed structured logging fields; serialized size
caps; adversarial Unicode/entity/property tests; no local message index.

### False read-only semantics

Threat: history, context, voice, or round-media delivery advances Telegram state
after a result was already released or without disclosure.

Controls: distinguish search/unread metadata from body reads; assemble and
post-authorize first, including permission for the complete dialog read prefix
and any undisplayed messages it affects; acknowledge through the update-manager
affected-result hook second; wait for the durable checkpoint before body release;
return explicit `read_effect`. A separate owner-only policy lock serializes
revocation with the complete operation. Expiry bounds the upstream context and
is rechecked before and after acknowledgment. Any uncertain effect or required
audit failure releases no body.

### Secret disclosure or persistence

Threat: credentials appear in argv, environment, stdout, logs, SQLite, crash
output, or files accessible to other ordinary OS users.

Controls: interactive no-echo input comes directly from the OS console; 2FA uses
a locked wipe-on-use buffer. Credentials, sessions and integrity keys use atomic
private file writes, strict parsing, bounded reads and no-link validation. Unix
permissions and Windows ACLs restrict access to the owner. The byte-only relay
has no secrets; SQLite contains no secret/content columns.

The local secret file is intentionally unencrypted to permit automatic restart
without a password or certificate. Readable copies expose account access; the
same OS user, administrators and offline disk access are outside this boundary.
Disk encryption can mitigate offline theft. Metadata backups exclude secrets.
Legacy Keychain migration is explicit, validates the complete bundle and required
session, publishes without overwriting and retains the source. Missing or corrupt
files never trigger an automatic storage fallback.

### Local socket and process attacks

Threat: a second daemon races account state, a symlink/socket is replaced, or a
relay silently spawns a new Telegram client.

Controls: account lock before secret prompting or access; private runtime
directory/socket; `lstat` and owner/type validation; length-checked path;
same-user stale socket cleanup only after positive `ECONNREFUSED`; fail-closed
handling of inconclusive dial errors; no relay fallback; cancellation-aware
accept loop. Both socket endpoints check peer credentials; the listener bounds
concurrent connections and the MCP boundary limits input frame size and rate,
incomplete frame duration, and blocked writes. Idle sessions remain connected
between requests; the input deadline starts when the first frame byte arrives.

### Stale or incomplete state represented as live

Threat: update gaps, `differenceTooLong`, missing access hashes, reauth, or
partial search fan-out yields an apparently complete response.

Controls: explicit freshness state and timestamp; capability degradation;
partial/warning/cursor metadata; authorization epoch invalidation; durable
metadata transactions; never infer completeness from a successful remote call.

### Resource exhaustion

Threat: large pages, media, fan-out, flood waits, or concurrent clients exhaust
memory, disk, connections, or time.

Controls: server-side item/byte/RPC/concurrency/deadline caps; bounded flood
waits; cancellation; no durable media/cache; 256 KiB textual result maximum;
default 20 and hard maximum 100 items.

### Supply-chain and binary-identity drift

Threat: dependency compromise or replacement of installed executables changes
account behavior.

Controls: stable dependency pins, checksums, private installation directories and
immutable versioned executable paths. Portable archives include hashes and build
metadata; hashes detect corruption but do not establish publisher authenticity.
Signing is optional and is not a prerequisite for credential access. Install from
a trusted source. Legacy macOS Keychain migration separately requires an identity
already allowed by the old items; it does not widen their ACLs.

## Excluded or deferred risk

Secret Chats, protected content, expiring media,
writes, HTTP, multi-account state, local content indexing, and broad experimental
eligibility are absent from v1. Adding any of them requires a separate threat
and contract plan.

Telegram's API and content-licensing terms are a release boundary, not a
security control. Test-DC and synthetic/self-authored development does not imply
permission for production/private data or publication. A dated release decision
is required. Intended AI-data eligibility must be resolved before enabling
content workflows; a configured peer grant is not itself proof of eligibility.

## Account runtime verification boundary

Automated checks prove lock-and-eligibility-before-prompt ordering,
second-owner exclusion,
private directory/file/socket permissions, fail-closed malicious socket paths,
safe stale-socket replacement, fail-closed inconclusive socket probes,
File-backed gotd session reconciliation, authorization-epoch
rotation/invalidation after startup and at runtime, bounded request scheduling,
sanitized command output, and cancellation cleanup. Live phone/2FA and QR login
still require a human account in the explicitly selected environment and are
recorded separately when run.

Synthetic tests cover default-deny grants, exact author/range/prefix authority,
revocation serialization, hostile input and content, mirrored result budgets,
hooked acknowledgment and durable checkpoint failures, and the stdio text
workflow. They do not establish live account acceptance, production eligibility,
native execution on every target OS or release readiness. Production DC construction requires
explicit human configuration and eligibility attestation. Metadata and the atomic
credential tuple must agree on environment before client creation. Production
sessions use a distinct local secret slot; either environment's session blocks
reconfiguration. Logout removes only the selected session. Legacy credential
bundles are accepted only as test credentials. No MCP tool can change these
controls.

Ordinary non-forum supergroups and joined broadcasts are read through live RPCs. Telegram defines channel pts independently
from common pts/qts and outer seq. The adapter removes channel pts events and
channel entities before the pinned updates manager sees live batches or validated
common differences, retaining the accepted common checkpoint and enclosing
sequence. It does not manufacture channel checkpoints or successful channel
recovery responses. Common recovery failures still fail closed. See Telegram's
[update sequence contract](https://core.telegram.org/api/updates).

## Search continuation and unread disclosure

Search targets one authorized peer, or a human-managed scope intersected with
current authority, before Telegram I/O and is post-filtered
with the existing author/range/eligibility/content checks. Only bounded snippets
leave this path, with no history receipt. Following a result into full context
requires the independent whole-prefix acknowledgment authorization.

Search cursors use domain-separated HMAC-SHA256 and a distinct locally stored
key. Their query digest is also keyed, preventing offline dictionary checks
against a visible cursor payload. Version, signature, canonical encoding,
expiry, operation, peer or scope, query, item limit, epoch and policy revision are checked
before fetching. Persistent access-mode, grant and scope change triggers prevent revoke/regrant from
reviving earlier tokens. Cursors contain no message or query text; no search
state or content is stored in SQLite. Valid replay is navigation, not authority.

Unread metadata describes the whole granted dialog, including messages outside
the body's author/range. The operator contract explicitly grants this metadata
scope. Responses expose only IDs, counts and manual flags; incidental upstream
top messages/drafts are discarded. Missing or failed peer lookups never become
invented zero counts or successful partial results.

Named scopes use local names and stable random IDs, never Telegram titles or
aliases, and contain only exact supported peer IDs. They confer no authority.
The policy lease covers membership resolution, current grants, each fetch,
normalization, serialization and required audit. Missing, expired and revoked
grants are counted as exclusions before I/O; excluded member identities are
absent from MCP results. A required peer failure releases no earlier snippets
or invented counts. Scope/member edits invalidate the shared policy revision;
authorization rotation or logout deletes all scopes. A scoped cursor also binds
the eligible membership digest and earliest selected grant expiry. Traversal
is bounded by 20 peers and 100 fetched candidates, including filtered entries,
under the existing operation deadline and complete response byte budget.

## Image disclosure and hostile encodings

Images require Full read access or an explicit grant bit, default false for
migrated and new restricted grants.
Discovery exposes safe metadata only after the existing author/range/content
checks. Signed five-minute handles contain a keyed image identity digest and
exact message reference; policy revision and epoch changes invalidate them.
No raw file location, reference, filename, or access hash crosses the adapter.

Opening reauthorizes before I/O, refetches the exact source before downloading,
and checks it again after download and after the hooked receipt. Replaced,
deleted, newly protected, or unsupported sources release no bytes. A single
reference renewal must preserve the same source identity; a failed rendition
is never replaced by another. Explicit DC pools use the shared request controls;
unexpected migration and CDN responses fail. See [image access](image-access.md).

Byte limits precede allocation and pixel limits precede full decoding. Exact
JPEG/PNG framing rejects trailing content; animated PNG chunks are denied.
Captionless images do not bypass permission. Unread media mentions and variants
requiring additional effects are excluded. The complete native result budget
is checked before acknowledgment, then expiry/readiness/cancellation are checked
through the final audit boundary. A possible effect followed by failure returns
no image and reports uncertainty. No remote snapshot or human-view claim is made.

## Account-wide authority and supergroup reads

Full read access trades per-author/range isolation for a single explicit human
opt-in. It includes supported future conversations/messages, images, PDF/plain-text attachments, and the
whole dialog prefix affected by receipts. Only the local control plane can
change it; there is no access-mode MCP tool or participant consent workflow.
The setting records account authority, not rights over others' content. Existing
content, resource, and peer-subtype exclusions still apply after normalization.

Absence of the epoch-bound singleton means restricted mode; migration never
creates broad authority. Epoch mismatch and storage failure fail closed.
Mode changes share the content lease through final audit and release and advance
the existing policy revision. Disable/re-enable cannot revive earlier handles
or cursors. Exact grants remain available after disablement. Previously delivered
content and completed receipts cannot be recalled. Connected agents/providers
can retain results outside this process's boundary.

Discovery fetches one bounded main/archive dialog page, discards incidental
bodies and drafts, and emits only supported typed IDs, titles or unread counts.
Excluded dialogs may form a signed continuation position; their access hashes
stay in adapter-owned epoch metadata. Empty filtered pages can have continuation.
Tokens bind operation, page size, epoch, revision and a fixed expiry and are
checked again after audit. No failed page becomes empty success. Pagination is
live and can shift when Telegram dialogs change.

Telegram's requested dialog limit is not a response-size guarantee. The adapter
caps each returned dialog/message/user/chat vector at 200 before metadata writes,
validates dialog identities and pinned ordering, then consumes at most the
requested page size. Continuation uses the last consumed dialog, including an
excluded one, so overfetch does not skip entries. The signed pinned-boundary bit
distinguishes refetching a pinned prefix from date-based ordinary pagination;
ordinary requests exclude already visited pins. A missing or unpinned
boundary fails explicitly. No response or pinned-list cache is introduced.

Supergroup lookups validate `Megagroup` and reject broadcast/minimal,
forbidden, left, restricted and protected entities both before content fetch and
on the returned page. Bot-authored messages are allowed under the same exact author/range grants or
Full read authority as human-authored messages. Anonymous/channel authors remain
excluded. Private bot dialogs use the same user-kind identity and read policy.
`channels.readHistory` does not return common affected pts. An error-free RPC
(either Boolean value) alone is insufficient: an exact subsequent dialog must
report an inbox read position at least as high as requested. Common checkpoint
synchronization is also required. A possible effect followed by any failure returns
`read_effect_uncertain` without bodies or media.

Supergroup content is not cached, indexed or subscribed to. Independent channel
pts is not persisted or represented as gap-free. The common updates projection
continues to discard channel events/entities without manufacturing recovery
success. Live page validation and receipt readback establish the narrower
freshness contract; Telegram may edit/delete content after the last observation.
Synthetic adapter and MCP tests prove routing, subtype rejection, readback,
revocation and native-image delivery. Live account acceptance remains separate.

Human metadata recovery imports only bounded, strictly validated scope selections
and audit retention settings. It never loads a backup SQLite file or restores
credentials, access hashes, authorization epochs, grants or checkpoints. Restore
holds the account lock and policy lease, resets all content authority, replaces
scope identities, and advances the current revision in one transaction. Matching
environments prevent accidental production/Test DC mixing; the operator still
chooses the current account explicitly. Private file permissions protect exported
membership metadata from other users, not from processes running as the owner.

Audit age/count retention executes in the required insertion transaction; a
failure rolls back insertion and the reader withholds content. Human purge is
explicit and affects audit history only. Logical deletion does not establish
forensic erasure of SQLite pages, WAL or external snapshots.

Portable release archives contain executables and documentation, never account
state. Native platform execution and real-account acceptance are separate from
cross-compilation. See [metadata maintenance](metadata-maintenance.md) and
[distribution](distribution.md).

## Original document disclosure

PDF and plain-text disclosure is a separate restricted grant bit, default false
for new and migrated grants. Full read includes supported documents. Existing
image opt-ins do not authorize document content. Captions and captionless
document descriptors follow the same author/range/exclusion checks. Discovery
never downloads bytes. Document handles have a separate signing domain and
operation, with epoch, policy revision, expiry and keyed exact-source identity.
Cross-use with image handles is rejected before Telegram access.

The downloader reuses bounded media transport and exact-source normalization.
Only declared PDF/plain-text MIME types and inert filename-only attribute sets
are accepted; filenames are discarded, never used as paths or trust signals.
Text encoding is not guessed. Exact byte counts, a chunk count derived from
source size, one renewal and deadlines constrain downloads. Allocation grows
with received chunks. PDFs have no fixed application byte or response cap;
inline base64 delivery consumes memory proportional to file size and may exceed
client limits. Images, voice notes and plain text retain their byte/output caps.
No document parser, OCR engine, external converter, shell command, external URL
fetch or additional dependency is introduced. Attachment bytes remain transient.

PDF framing checks do not establish that a PDF is valid or safe. Original PDFs
may contain JavaScript, embedded files, actions, links, encryption, malicious
objects or hostile instructions. The daemon neither interprets nor sanitizes
these structures. Safe rendering and avoiding execution/network side effects
are client responsibilities; embedded-resource support varies by client.
Plain text is also untrusted model input, never an instruction from the server.

The current grant and actual acknowledgment prefix are authorized before fetch.
Byte validation and unchanged-source checks happen before acknowledgment, and
source identity and policy/audit checks happen again before release. Any failure
withholds content and clears the downloaded buffer; failures after a receipt
attempt report uncertain read effects. Telegram edits are not an atomic
snapshot, and process-memory clearing is not a forensic erasure guarantee.

## Topic isolation and original voice delivery

Topic identity includes both the channel and positive topic ID. Exact grants,
scopes, message references, cursors and media handles preserve both components.
Parent-forum grants cannot expand to topic content. Topic reads use scoped RPCs
and reject cross-topic messages; receipts use `messages.readDiscussion` with
exact topic readback. Whole-forum acknowledgments are rejected. Metadata-only
discovery discards incidental message bodies and stores no topic titles.

Voice authority is separate from images and documents and defaults off for
existing restricted grants. Native audio delivery reuses bounded downloads,
source revalidation, policy leases and verified history receipts. The Ogg/Opus
parser checks framing and headers within the byte bound; it is not a codec or
a guarantee that a client will interpret audio. No playback receipt is sent.
Audio and embedded metadata remain hostile input. No transcription provider,
media cache, filenames or waveform persistence is introduced.

Private bot chats reuse the private-user discovery, identity, author, policy and
read-receipt boundaries. Bot flags do not grant authority. Keyboard/button markup
is omitted from delivered messages; the server never invokes callbacks, sends
commands, starts a bot or follows its links. Message text and supported media
remain hostile data. Bot-account authentication is still unsupported.

## Forwarded copies and origin metadata

A forwarded copy is authorized by its containing peer, message ID and sender.
Saved Messages copies use the account owner as the containing sender. Consented
grants and Full read can expose the copy; self-authored grants cannot, even when
the origin claims the current user. Origin metadata never selects grants, causes
a source fetch, resolves an entity, or produces an authorized source handle.
A channel origin does not enable broadcast-channel history.

The optional forward object carries bounded untrusted date, origin peer/name
and signature. Hidden names are not resolved or used to infer identity. Import,
PSA and malformed headers are excluded. Protected, ephemeral, quoted and unsafe
media checks remain. Media delivery compares attribution around the download
and receipt, withholding changed metadata and bytes. The forward object is never
persisted, and names participate in the existing response budget.

## Broadcast publisher authority

Only joined, complete, unrestricted, unprotected broadcast entities are accepted.
The containing channel is the publisher and the public author of its posts;
optional sender IDs and signatures are untrusted display attribution. A post
flag alone never upgrades a group into a broadcast. Each response must contain
the matching eligible channel entity. Restricted grants require the same channel
as peer and author with a consented profile. Self-authored grants and grants for
a displayed person never authorize channel posts. Full read includes supported
posts. Group send-as messages remain excluded; forum-topic authority is unchanged.

Existing media permissions, source validation, budgets, policy leases and
verified whole-prefix channel receipts apply. Sender/signature changes during
media delivery withhold bytes. No joining, global lookup, independent channel
checkpoint, source lookup or body cache is introduced. History receipts do not
claim viewport visibility, view-counter increments or ad impressions.

Telegram requires official sponsored-message support in channel-capable apps.
The current headless interface cannot establish screen presentation or genuine
impressions. Sponsored presentation remains an unresolved publication contract;
synthetic source qualification is not proof of compliance or a release decision.
See [channel access](channel-access.md) for the exact boundary.

## Reply navigation

Reply IDs describe an older message inside the exact containing peer/topic and
never authorize a parent fetch. Bounded parent traversal validates range before
I/O and author/content/media policy afterward under the existing lease. Missing,
denied and excluded parents have one public unavailable state; network and
freshness failures discard the result. Embedded quote content and cross-peer
reply metadata remain excluded. Reply chains reuse existing receipt and output
budgets, and media source checks also compare reply IDs. See [reply context](reply-context.md).

Reply exploration adds exact-peer `reply_to` and `thread_root` search selectors,
not grants. Both bind into signed search cursors and are checked against each
policy-authorized candidate; filtered candidates still consume the page budget.
Forum roots stay inside exact topic authority. Untrusted thread references are
validated for peer and older-ID consistency before release; media revalidation
also compares thread and discussion-peer attribution.

Optional channel discussion resolution uses the existing context operation and
requires Full read before source I/O because `messages.getDiscussionMessage`
cannot restrict incidental returned messages by grant range or author. The source
post passes current content policy; the destination must independently be a
joined supported ordinary supergroup before mapping I/O. Returned mapping IDs
must belong to that group and the root must identify the exact source post.
The source link and eligibility are rechecked before preparing the response.
Incidental bodies are neither delivered nor persisted. All failures discard the
context response, and only the source history prefix receives an acknowledgment.
No mapping grants destination body access or changes group membership. See
[reply context](reply-context.md) for supported workflows and limitations.

## Album membership

Album IDs bind grouping metadata to the exact containing peer/topic and preserve
Telegram's 64-bit value without JSON numeric rounding. Grouping is never a grant
or a reason to fetch siblings. Every member and caption follows existing policy;
filtered members contribute no metadata, count or copied caption. Media opens
compare membership around downloads and receipts. Page completion never proves
album completeness. No album state is persisted. See [media albums](media-albums.md).

## Poll content

Poll questions and options are hostile text under containing-message authority.
Only supplied aggregate counts cross the adapter boundary. Voting tokens, hashes,
voter identities, personal selections and quiz solutions are not exposed or
persisted. No additional RPC or side effect is introduced. Unknown counts remain
absent, and malformed mappings fail without content-bearing diagnostics. Existing
complete-response budgeting precedes history/context acknowledgment. See [polls](polls.md).

## Webpage previews

Preview metadata is hostile content authorized by the containing message. URLs
are not parsed, visited, or treated as access authority; manual previews may refer
to a different URL from the message. Telegram's safety hints are not exposed as
endorsements. Only bounded supplied display fields cross the adapter boundary;
embedded media, cached pages, raw IDs/hashes and embeds do not. There is no page
cache or additional network operation. Unknown and malformed constructors fail
closed. Existing post-normalization policy, complete-response limits and receipt
gates apply. See [link previews](link-previews.md).

## Aggregate reactions

Reaction summaries are hostile metadata under containing-message authority.
Unicode labels and custom emoji IDs never authorize media or peer fetches. Only
supplied aggregate counts and reduced/tag semantics are exposed; personal choices,
reactor identities, paid leaderboards and listing hints are omitted. No reaction
writes, reaction-read receipts, extra RPC or content persistence are introduced.
Unknown or malformed aggregates fail closed and response budgeting precedes
history acknowledgment. See [reaction summaries](reactions.md).

## Pinned-message discovery

Pins can contain hostile instructions or fall outside current access grants.
`search_messages` uses Telegram's pinned filter within the exact authorized
peer or topic and existing message-ID bounds. Policy still runs before fetching
and after normalization, and a pin grants no additional access. The reader
filters candidates that are no longer pinned while counting them against the
page budget and preserving continuation. Peer and scope cursors bind the filter
and normalized query alongside existing authority and expiry fields. Pin state
is not persisted and can change between calls. Search emits bounded snippets
without read acknowledgments; opening context retains the acknowledged-body
boundary. Pins never change the untrusted status of message content. See
[pinned-message discovery](pinned-messages.md).

## Media search filters

Provider media categories are broader than supported attachment types and do
not establish permission or safe content. Search selects photo, document or
voice candidates, then applies existing message policy, source validation and
exact descriptor matching. PDF, text-file and image-file filters share the
provider document category; no filenames are inspected or returned. Combining
pins and media uses the provider pin filter and the same local intersection.

Media filters are bound into both peer and scope cursors. Nonmatching, excluded
and denied candidates consume the page budget and preserve continuation without
releasing content or replacing a failed RPC with an empty success. Existing
media permissions and byte/duration limits remain; search introduces no download,
interpretation, receipt, index, persistence or additional authority. See
[media search](media-search.md).

## Sender, date and Saved Messages selectors

Sender and time bounds narrow candidates after containing-message policy checks.
Sender means the normalized author, not forwarded attribution or a channel
signature. Date-only and other searches without text/media/pin selectors use
history traversal with validated ordering; empty-query search dates are not
trusted. Dates remain transient, and peer/scope cursors bind normalized bounds
and sender alongside existing authority and expiry. Every fetched candidate
consumes the page budget, including nonmatches and excluded content.

Saved source grouping is copied only from supplied saved_peer_id on otherwise
supported authorized Saved Messages. It is a strict kinded non-topic ID; self
references must identify the containing account. Missing source metadata is
unknown. Hidden authors are not resolved or inferred. A grouping ID never
selects a grant, resolves access hashes or causes original-conversation reads.
Only exact self-peer searches accept saved_peer/saved_tag, excluding scopes.
Reaction tags require validated as_tags data and a positive count; paid and
ordinary reactions do not match. The saved-tag cursor binding is keyed so tag
text is not exposed in cursor payloads. Source and tag discovery uses authorized
message results, never an unrestricted catalog across excluded saved content.
No new persistence, acknowledgments, writes or authentication surface is added.
