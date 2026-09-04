# TgContext

TgContext is a macOS-first, local context gateway for one Telegram user
account. It is an unofficial client using Telegram's API. MCP is a narrow,
read-first adapter to a persistent, policy-enforced daemon; it is not a raw
Telegram API surface.

Phase 1 provides the secure single-account daemon foundation and interactive
Telegram Test-DC authentication. It stores gotd session bytes and the API hash
in the native macOS login Keychain, stores metadata only in SQLite, and keeps
production login absent. It still exposes no MCP tools and processes no message
content.

## Safety boundary

- Develop with synthetic data. Test-DC login is available; production login is
  deliberately not implemented.
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

## Phase 1 operator flow

Build the two active binaries:

```sh
go build -o ./tmp/tg-contextctl ./cmd/tg-contextctl
go build -o ./tmp/tg-contextd ./cmd/tg-contextd
```

With the daemon stopped, configure only a Telegram Test DC. The API ID and API
hash are read directly from `/dev/tty`; neither is accepted through argv or the
environment:

```sh
./tmp/tg-contextctl configure --test-dc 2
./tmp/tg-contextctl auth phone
# or, from a logged-out session:
./tmp/tg-contextctl auth qr
./tmp/tg-contextctl status
```

Start the persistent owner after authentication:

```sh
./tmp/tg-contextd
```

Authentication, reconfiguration, and logout take the same exclusive account
lock as the daemon. Stop the daemon before running those commands. See
[Test-DC authentication](docs/authentication.md) for the complete flow and
[Phase 1 acceptance](docs/phase1-acceptance.md) for automated versus manual
evidence.

## Checks

```sh
go build ./cmd/...
go test ./...
go vet ./...
```

`tg-context-mcp` remains fail-closed until the MCP transport phase. The daemon
authenticates and reports process-local readiness only; it has no message read
path.
