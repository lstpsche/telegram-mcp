# Telegram MCP repository rules

## Scope and product boundary

- The product name is Telegram MCP. It serves AI agents through standard
  MCP stdio. Describe it as an unofficial client using Telegram's API; do not
  use official logos or imply Telegram affiliation. The daemon and relay
  support that product. Agent workflows define the acceptance
  criteria; infrastructure alone does not establish MCP usability.
- Preserve the daemon -> owner-only local transport -> byte-only stdio relay shape.
  Authentication, policy, consent, and lifecycle mutations stay in the human
  control plane and must not become MCP tools.
- Keep v1 single-account, local, read-first, and portable across macOS, Linux and Windows. Do not add writes,
  HTTP listeners, raw MTProto tools, message indexing, embeddings, multi-account
  state, broadcast channels, bot chats, or Secret Chats without a new plan and
  threat review.
- Test-DC authentication, production login, real-data use, publication,
  deployment, and acceptance are separate decisions. Production configuration
  requires explicit human eligibility attestation; Test DC selection is explicit.
  Credentials remain interactive and local. Text access requires exact restricted
  grants or human-enabled Full read access. Restricted images require an additional
  opt-in; Full read access includes supported images and read acknowledgments. Automated acceptance
  uses synthetic local fixtures; implementation does not authorize real-data use.

## Trust and data rules

- Treat every Telegram-derived string and entity as hostile data. Never place
  content, snippets, queries, credentials, session bytes, or media in logs,
  errors, schemas, filenames, or persisted metadata. Access hashes may exist
  only in adapter-owned, authorization-epoch-bound metadata; never emit them
  through public models, logs, errors, filenames, or MCP.
- Authority is based on strict, versioned, kinded IDs. Never authorize by
  mutable usernames, titles, links, or Bot API encodings.
- Policy is default-deny and must run both before a fetch and after
  normalization. Access hashes and generated Telegram types remain inside
  `internal/telegram`.
- History/context delivery is state-affecting. Bodies must not be released
  until the required hooked read acknowledgment succeeds. Authorize the actual
  dialog prefix affected by that acknowledgment, including undisplayed
  messages; a content grant alone does not authorize that wider side effect.
- Cursors and resource handles are never authorization. They must be signed,
  expiring, operation/query/epoch/policy-bound, and reauthorized on every use.
- Store credentials, sessions and integrity keys in private unencrypted local
  files, with owner-only Unix permissions or Windows ACLs. Reject unsafe paths
  and malformed data; never silently switch storage backends. Legacy macOS
  Keychain migration is an explicit human operation using Security.framework,
  with no source deletion or invocation of `/usr/bin/security`.
- Without arguments, `telegram-mcp` stdout is reserved for MCP frames. Explicit
  human subcommands use the control-plane output format. Send diagnostics to
  stderr through the safe structured logging boundary.

## Engineering rules

- Use Go 1.27.1 and keep dependency pins stable unless a researched update is
  intentional. Do not select prerelease dependencies implicitly.
- Prefer deletion, simplification, standard-library/platform capabilities, and
  existing packages before adding abstractions, dependencies, flags, or state.
- Add narrow interfaces only at external seams that need fakes: Telegram I/O,
  metadata transactions, clock/randomness, and secrets.
- Keep migrations forward-only, transactional, checksum-verified, and free of
  message/search/media content columns.
- Run `gofmt`, `go build ./cmd/...`, `go test ./...`, and `go vet ./...` for
  every change. Keychain-adapter changes additionally require the
  noninteractive macOS probe documented in `docs/keychain.md`; live account
  acceptance remains a separate human-run gate.

## Committed content and change descriptions

- Never include delivery-stage labels or numbers, active ticket names or IDs,
  task IDs, plan bookkeeping, or implementation chronology in committed
  filenames, code, comments, test fixtures, CLI output, or documentation.
- Keep execution plans, progress records, and review working notes under the
  gitignored `tmp/` directory or in an external tracker. Do not force-add them.
- Documentation describes behavior, current limitations, contracts, operations,
  and decision rationale. Distinguish implemented behavior from intended
  behavior without referring to the work breakdown that produced it.
- Commit subjects and bodies describe the changes and their purpose. Never
  include delivery-stage labels, ticket names or IDs, task IDs, or plan status.
- Review the complete staged diff, including filenames and fixtures, and the
  proposed commit message for these rules before every commit. Replace work
  labels with behavior-based names; do not merely move them to another file.
