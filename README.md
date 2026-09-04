# Telegram MCP

Telegram MCP connects AI agents to a local Telegram account runtime through
standard MCP stdio. It is an unofficial client using Telegram's API. The
product is designed for finding authorized conversations, retrieving message
context, and searching chats on one user account, initially on macOS.

## Available now

The MCP server exposes one tool: **`status`**. Agents can connect, discover it,
and inspect sanitized account state. No Telegram credentials are needed to
verify this connection. Message reads, search, media, and production login are
not implemented yet.

The account runtime supports interactive Test-DC phone/2FA and QR
authentication, native login-Keychain session and credential storage, exclusive
account ownership, metadata-only SQLite storage, and remote-first logout.
Real-account authentication and release-binary acceptance remain human checks.

## Connect an agent

Use Go 1.27.1, as selected by `go.mod` and `.go-version`. A parent-shell
`GO111MODULE=off` override must not be set.

```sh
go build -o ./tmp/telegram-mcp ./cmd/telegram-mcp
go build -o ./tmp/telegram-mcpd ./cmd/telegram-mcpd
go build -o ./tmp/telegram-mcpctl ./cmd/telegram-mcpctl
./tmp/telegram-mcpd
```

Keep the daemon running. In another terminal, register the relay with Codex
using the absolute path to the built executable:

```sh
codex mcp add telegram -- /absolute/path/to/telegram-mcp
```

For MCP clients using JSON configuration, the equivalent stdio registration is:

```json
{
  "mcpServers": {
    "telegram": {
      "command": "/absolute/path/to/telegram-mcp"
    }
  }
}
```

Ask the agent to call `status`. An unconfigured account reports
`account_state: reauth_required`, `message_reads: false`, and
`production_login: false`. Account `ready` does not imply message tools are
available. Telegram data freshness remains `unavailable` until a read engine
is implemented.

The relay never starts a daemon automatically. If the daemon is absent, it
exits with a structured diagnostic on stderr. Its stdout carries only MCP
frames during normal operation. Idle connections expire after a minute;
clients can reconnect by restarting the relay.

## Configure a Test-DC account

Stop the daemon before configuration, authentication, or logout. They share
its exclusive account lock. Use only disposable Test-DC accounts.

```sh
./tmp/telegram-mcpctl configure --test-dc 2
./tmp/telegram-mcpctl auth phone
# Alternatively, from a logged-out session:
./tmp/telegram-mcpctl auth qr
./tmp/telegram-mcpctl status
./tmp/telegram-mcpd
```

Credentials are read without echo directly from `/dev/tty`, never from argv or
the environment. API ID, API hash, and Test DC are stored as one atomic Keychain
bundle. If an interrupted configuration leaves SQLite inconsistent, account
operations refuse it; stop the daemon and rerun configuration to recover.

See [authentication](docs/authentication.md),
[Keychain behavior](docs/keychain.md), and
[verification procedures](docs/account-runtime-verification.md).

## Intended content workflows

- Discover authorized chats and retrieve bounded history or message context.
- Search authorized conversations and follow a result into its context.
- Inspect unread metadata and retrieve supported media within server limits.
- Receive explicit freshness, partial-result, and read-receipt information.

Authentication and access grants belong to the human operator. Sending,
editing, deleting, and account administration are outside the read-first MCP
scope. Content authorization must run before fetching and after normalization.
History delivery must separately authorize the actual dialog read boundary,
which can include messages excluded from the returned bodies.

## Data boundary

Telegram content is untrusted result data, never instructions, logs, errors,
schemas, or persisted message content. Telegram MCP does not retain message,
search, or media content. A connected client or model provider can retain
returned results under its own settings. Same-user processes are outside the
cryptographic isolation boundary.

Telegram's [API terms](https://core.telegram.org/api/terms) and
[content licensing terms](https://telegram.org/tos/content-licensing) constrain
the intended use. Production eligibility remains unresolved; local execution
and an access grant alone do not establish permission. These decisions must
precede real-data enablement, independently of technical implementation.

See the [architecture decisions](docs/adr/),
[protocol contracts](docs/contracts.md), and [threat model](docs/threat-model.md).

## Checks

```sh
go build ./cmd/...
go test ./...
go vet ./...
```
