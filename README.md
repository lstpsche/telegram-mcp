# Telegram MCP

Telegram MCP connects AI agents to a local Telegram account runtime through
standard MCP stdio. It is an unofficial client using Telegram's API. The
product is designed for finding authorized conversations, retrieving message
context, and searching chats on one user account, initially on macOS.

## Available now

The MCP server exposes **`status`**, **`list_chats`**, **`list_messages`**, and
**`get_message_context`**. No Telegram credentials are needed to connect and
inspect status. Text operations require a ready Test-DC account and an explicit
human grant. Search, media, and production login are not implemented.

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
`production_login: false`. `message_reads` reports text-engine readiness;
individual reads still require current grants. Status itself reports Telegram
freshness as `unavailable` because it does not perform a freshness check.

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

## Authorized text workflows

- Use the human `peers` command to select immutable conversation IDs, then
  create exact author/range/expiry grants with separate read-prefix authority.
- Discover granted chats with `list_chats` and retrieve bounded text with
  `list_messages` or `get_message_context`.
- Receive explicit freshness, partial-result, and read-receipt information.

See [text access](docs/text-access.md) for grant commands, eligibility
prerequisites, supported peers, and recovery behavior. Ordinary Saved Messages,
non-bot users, and basic groups are supported. Supergroups, topics, forwarded,
quoted, protected, expiring, and media content are excluded. The automated
workflow uses synthetic fixtures; live Test-DC content acceptance remains a
separate human check. Search, unread metadata, and media remain future work.

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
