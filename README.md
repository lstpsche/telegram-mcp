# TgContext

TgContext is a macOS-first, local context gateway for one Telegram user
account. It is an unofficial client using Telegram's API. MCP is a narrow,
read-first adapter to a persistent, policy-enforced daemon; it is not a raw
Telegram API surface.

Phase 0 establishes contracts and buildable seams only. The repository does
not yet connect to Telegram, authenticate an account, expose MCP tools, or
process real message data.

## Safety boundary

- Develop with synthetic data. Test-DC and production login are later,
  separately authorized milestones.
- Telegram content is untrusted data and must never enter instructions, logs,
  errors, schemas, or persistent metadata.
- Policy and authentication mutations belong to the interactive control plane,
  never the model-facing MCP surface.
- Message, search, and media content is not persisted by default.
- A process running as the same macOS user is outside the cryptographic
  isolation boundary; the daemon/relay/control split is defense in depth.

See [the threat model](docs/threat-model.md) and
[architecture decisions](docs/adr/) before extending the scaffold.

## Toolchain

The repository selects Go 1.27.1 through `go.mod` and `.go-version`. Ensure a
parent-shell `GO111MODULE=off` override is not set, then verify:

```sh
go version
go env GO111MODULE GOTOOLCHAIN
```

## Phase 0 checks

```sh
go build ./cmd/...
go test ./...
go vet ./...
```

The three product commands intentionally fail closed in Phase 0. They support
`--help` and `--version`, but have no Telegram or MCP runtime behavior yet.

