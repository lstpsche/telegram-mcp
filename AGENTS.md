# TgContext repository rules

## Scope and product boundary

- The product name is TgContext. Describe it as an unofficial client using
  Telegram's API; do not use Telegram branding or logos.
- Preserve the daemon -> owner-only Unix socket -> byte-only stdio relay shape.
  Authentication, policy, consent, and lifecycle mutations stay in the human
  control plane and must not become MCP tools.
- Keep v1 single-account, local, read-first, and macOS-first. Do not add writes,
  HTTP listeners, raw MTProto tools, message indexing, embeddings, multi-account
  state, broadcast channels, bot chats, or Secret Chats without a new plan and
  threat review.
- Production login, real-data use, publication, deployment, and acceptance are
  separate decisions. Phase 0 uses no Telegram credentials or real content.

## Trust and data rules

- Treat every Telegram-derived string and entity as hostile data. Never place
  content, snippets, queries, credentials, session bytes, access hashes, or
  media in logs, errors, schemas, filenames, or persisted metadata.
- Authority is based on strict, versioned, kinded IDs. Never authorize by
  mutable usernames, titles, links, or Bot API encodings.
- Policy is default-deny and must eventually run both before a fetch and after
  normalization. Access hashes and generated Telegram types remain inside
  `internal/telegram`.
- History/context delivery is state-affecting. Bodies must not be released
  until the required hooked read acknowledgment succeeds.
- Cursors and resource handles are never authorization. They must be signed,
  expiring, operation/query/epoch/policy-bound, and reauthorized on every use.
- The Keychain adapter must use Security.framework directly, target the current
  user's unlocked login keychain, avoid synchronization and file fallbacks, and
  never invoke `/usr/bin/security`.
- `tg-context-mcp` stdout is reserved for MCP frames. Send diagnostics to
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
  Phase 0 changes. Keychain changes additionally require the noninteractive
  macOS probe documented in `docs/keychain.md`.

