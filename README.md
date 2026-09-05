# Telegram MCP

Telegram MCP connects AI agents to a local Telegram account runtime through
standard MCP stdio. It is an unofficial client using Telegram's API. The
product is designed for finding authorized conversations, retrieving message
context, and searching chats on one user account, initially on macOS.

## Available now

The MCP server exposes **`status`**, **`list_chats`**, **`list_messages`**,
**`get_message_context`**, **`search_messages`**, **`list_unread`**,
**`list_scopes`**, **`catch_up`**, and **`open_image`**.
No Telegram credentials are needed to connect and
inspect status. Text operations require a ready authorized account and an explicit
human authorization: restricted grants or Full read access. Restricted grants
require a separate image opt-in; Full read access includes supported images. Production configuration requires explicit eligibility
attestation; configuring an account grants no content access.

The account runtime supports interactive production and Test-DC phone/2FA and QR
authentication, native login-Keychain session and credential storage, exclusive
account ownership, metadata-only SQLite storage, and remote-first logout.
Real-account authentication and release-binary acceptance remain human checks.

For a dated briefing, discover a named scope with `list_scopes`, then call
`catch_up` with its ID and explicit `since`/`until` timestamps. It returns
authorized snippets and image references without marking chats read. Continue
through `next_cursor` until null; each eligible chat has explicit progress.

## Connect an agent

For per-user background operation, use the [installation and diagnostics
guide](docs/installation.md). The human CLI provides `service` lifecycle
commands, a read-only `doctor`, and `agent-config` with the exact installed
relay path. The following commands run the daemon in the foreground.

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
`production_login: true`. This is a static capability, not evidence of login or
eligibility. `message_reads` reports text-engine readiness;
individual reads still require current human authorization. Status itself reports Telegram
freshness as `unavailable` because it does not perform a freshness check.

The relay never starts a daemon automatically. If the daemon is absent, it
exits with a structured diagnostic on stderr. Its stdout carries only MCP
frames during normal operation. Connections remain open between requests.
An incomplete input frame expires one minute after its first byte arrives;
clients can reconnect by restarting the relay after a disconnect.

## Configure an account

Stop the daemon before configuration, authentication, or logout. They share
its exclusive account lock. After establishing eligibility for the intended
use, explicitly select production with the human attestation flag. Use the
[local signing recipe](docs/development-signing.md) and qualify the exact
control/daemon artifacts before entrusting them with account credentials.

```sh
./tmp/telegram-mcpctl configure --production --attest-eligible
./tmp/telegram-mcpctl auth phone
# Alternatively, from a logged-out session:
./tmp/telegram-mcpctl auth qr
./tmp/telegram-mcpctl status
./tmp/telegram-mcpd
```

For disposable Test-DC accounts, select `configure --test-dc 2` instead. There
is no default environment; changing environments requires logout first.

Credentials are read without echo directly from `/dev/tty`, never from argv or
the environment. API ID, API hash, environment, and Test DC are stored as one
atomic Keychain bundle. If an interrupted configuration leaves SQLite inconsistent, account
operations refuse it; stop the daemon and rerun configuration to recover.

See [authentication](docs/authentication.md),
[Keychain behavior](docs/keychain.md), and
[verification procedures](docs/account-runtime-verification.md).

## Authorized text workflows

- Enable Full read access with `telegram-mcpctl access full --accept-full-read`
  to discover and read supported conversations without per-chat or per-author
  grants. Inspect with `access`; disable with `access restricted`. It includes
  images and read acknowledgments until disabled or account authorization changes.
- In restricted mode, use the human `peers` command to select immutable conversation IDs, then
  create exact author/range/expiry grants with separate read-prefix authority.
  `saved-message` discovers just the newest Saved Messages reference without
  returning content or granting access.
- Discover authorized chats with `list_chats` and retrieve bounded text with
  `list_messages` or `get_message_context`.
- Group exact peers into human-managed named scopes and discover them with
  `list_scopes`; narrow chat discovery, unread counts, and search by scope ID.
- Search one authorized peer or scope for bounded snippets, continue with a signed cursor,
  and open a result with the existing context tool.
- Inspect whole-dialog unread counts and manual unread flags for authorized peers.
- Receive explicit freshness, partial-result, and read-receipt information.

See [text access](docs/text-access.md) for grant commands, eligibility
prerequisites, supported peers, and recovery behavior. Ordinary Saved Messages,
non-bot users, basic groups, and ordinary non-forum supergroups are supported.
Broadcast channels, topics, bot/anonymous authors, forwarded,
quoted, protected, expiring, and unsupported media content are excluded. The automated
workflow uses synthetic fixtures; live account content acceptance remains a
separate human check. See [image access](docs/image-access.md) for discovery,
native image delivery, permission, and byte/pixel limits.

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
the intended use. The operator must establish eligibility for each intended
use; local execution, configuration attestation, and an access grant alone do
not establish permission. These decisions must
precede real-data enablement, independently of technical implementation.

See the [architecture decisions](docs/adr/),
[protocol contracts](docs/contracts.md), and [threat model](docs/threat-model.md).

## Checks

```sh
go build ./cmd/...
go test ./...
go vet ./...
```
