# Telegram MCP

[![CI](https://github.com/lstpsche/telegram-mcp/actions/workflows/checks.yml/badge.svg)](https://github.com/lstpsche/telegram-mcp/actions/workflows/checks.yml)
[![Release](https://img.shields.io/github/v/release/lstpsche/telegram-mcp)](https://github.com/lstpsche/telegram-mcp/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

An [MCP](https://modelcontextprotocol.io) server that lets AI agents search your Telegram conversations, retrieve context, and catch up on what happened while you were away. Written in Go, with binaries for macOS, Linux, and Windows.

Telegram MCP is an **unofficial client using Telegram's API**, with no Telegram affiliation. It connects to one user account and gives agents read access that you control through a local CLI.

## What It Can Do

| Workflow | Capabilities |
| --- | --- |
| **Catch up** | Get a briefing across a named group of chats for an explicit date range |
| **Search** | Find messages in an authorized conversation or across a named scope |
| **Read context** | Open message history and the conversation around a search result |
| **View images** | Deliver supported JPEG/PNG photos and document attachments directly to the agent |
| **Navigate** | Discover authorized chats, named scopes, and unread counts |
| **Control access** | Choose exact, expiring grants or explicitly enable Full read access |

For example, after setting up access and a scope named `project`:

> Catch me up on the project chats from September 1 through September 5. Include decisions and unresolved questions, and cite the messages behind them.

> Search the project chats for discussion of the release date, then open the surrounding context.

> Show me which authorized conversations have unread messages.

Search, catch-up, and unread inspection do not mark chats read. **Opening history, message context, or an image can mark messages read**, including earlier messages in the affected conversation. These operations require permission for that read effect as well as permission to return content.

## Installation

Download a ZIP from [GitHub Releases](https://github.com/lstpsche/telegram-mcp/releases/latest). Each archive includes the three executables, setup documentation, licenses, and `SHA256SUMS`.

| Platform | v0.1.0 archive |
| --- | --- |
| macOS Apple Silicon | [darwin-arm64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.1.0/telegram-mcp-0.1.0-darwin-arm64.zip) |
| macOS Intel | [darwin-amd64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.1.0/telegram-mcp-0.1.0-darwin-amd64.zip) |
| Linux ARM64 | [linux-arm64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.1.0/telegram-mcp-0.1.0-linux-arm64.zip) |
| Linux x86_64 | [linux-amd64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.1.0/telegram-mcp-0.1.0-linux-amd64.zip) |
| Windows ARM64 | [windows-arm64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.1.0/telegram-mcp-0.1.0-windows-arm64.zip) |
| Windows x86_64 | [windows-amd64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.1.0/telegram-mcp-0.1.0-windows-amd64.zip) |

Extract into a **new private directory at a stable, absolute path**. Verify the archive hash against the release's `release.json` and the extracted files against `SHA256SUMS`. Follow the [platform installation guide](docs/installation.md) to prepare permissions: Unix requires a directory owned by you with mode `0700`; Windows requires owner-only ACLs. Run as your normal user, without `sudo` or an elevated Windows terminal.

Packages are unsigned and not notarized, so your OS may require approval before running a download. Native CI covers macOS, Linux, and Windows amd64. Windows desktop task startup/restart/stop remains unverified; Windows arm64 is cross-compiled but has not been exercised natively. See [distribution and verification](docs/distribution.md) for details.

Prefer to compile locally? See [Development](#development) and the [source installation instructions](docs/installation.md).

## Quick Start

You need a Telegram account, your application's API ID and API hash, and an MCP client. Obtain application credentials through [Telegram's development tools](https://my.telegram.org). Before using real conversations with an AI system, establish eligibility under Telegram's terms and the rights and consent of the people affected; the setup flag records your attestation, not an exemption.

The commands below use macOS/Linux syntax from the prepared binary directory. On Windows, use `.\telegram-mcpctl.exe` in place of `./telegram-mcpctl` after completing the [Windows permission setup](docs/installation.md).

1. **Configure and sign in.** Keep the daemon stopped during configuration and authentication. Credentials and login codes are entered interactively without echo.

   ```sh
   ./telegram-mcpctl configure --production --attest-eligible
   ./telegram-mcpctl auth phone
   ```

   QR login is also available through `auth qr`. For disposable test accounts, select `configure --test-dc 2` instead. See [authentication](docs/authentication.md) for phone, QR, 2FA, and recovery.

2. **Choose what the agent may read.** Restricted mode is the default: follow the [grant setup](docs/text-access.md) to authorize specific conversations, authors, message ranges, and expiry times. Signing in alone grants no content access.

   If you intentionally want account-wide access to supported conversations, enable Full read access:

   ```sh
   ./telegram-mcpctl access full --accept-full-read
   ```

   This includes supported images and read acknowledgments, and exposes returned content to your connected agent and model provider. Restore restricted access with `./telegram-mcpctl access restricted`.

3. **Start the background service.** This registers the binaries in the current directory for startup at user login. Keep that directory in place.

   ```sh
   ./telegram-mcpctl service install --bin-dir "$PWD"
   ./telegram-mcpctl service start
   ./telegram-mcpctl doctor
   ./telegram-mcpctl agent-config
   ```

4. **Connect your MCP client.** Copy the configuration printed by `agent-config`, or use the examples below. Ask your agent to check Telegram status and list the conversations it can access.

You can also connect an unconfigured daemon and call `status` without Telegram credentials. It reports that authentication is required; content tools become usable only after authentication and access setup.

## Client Setup

For Codex, register the absolute path to the installed relay:

```sh
codex mcp add telegram -- /absolute/path/to/telegram-mcp
```

For clients using `mcpServers` JSON configuration, such as Cursor or Claude Desktop:

```json
{
  "mcpServers": {
    "telegram": {
      "command": "/absolute/path/to/telegram-mcp"
    }
  }
}
```

On Windows, use the absolute path to `telegram-mcp.exe`; JSON paths need escaped backslashes. `telegram-mcpctl agent-config` prints the correct configuration for your installation.

The client connects through standard **MCP stdio**. Start the daemon separately: the relay does not launch it automatically. After restarting or upgrading the daemon, reconnect your MCP client.

## Tools

| Tool | Purpose |
| --- | --- |
| `status` | Check account and message-engine readiness without fetching Telegram content |
| `list_chats` | Discover conversations allowed by your current access settings |
| `list_messages` | Read a bounded page of authorized history |
| `get_message_context` | Retrieve context around a specific message |
| `search_messages` | Search an authorized conversation or named scope |
| `list_unread` | Inspect unread counts and manual unread flags |
| `list_scopes` | Discover human-managed groups of conversations |
| `catch_up` | Retrieve snippets and image references across a scope for an explicit time window |
| `open_image` | Open an authorized image as native MCP image content |

Named scopes let you group conversations for a project or recurring briefing. Manage them with the human `scope`, `scopes`, and `unscope` commands; agents discover their IDs through `list_scopes`. Scopes narrow existing access rather than granting new permissions. See [scope and access setup](docs/text-access.md).

For paginated tools, continue while `next_cursor` is non-null, even when a page is empty. Catch-up reports progress for each eligible chat. Responses include freshness and read-effect information so agents can distinguish a complete traversal from a partial one.

## Supported Content

Supported conversations are Saved Messages, non-bot private chats, basic groups, and ordinary non-forum supergroups. Supported images are ordinary JPEG/PNG photos and static document attachments.

Broadcast channels, forums/topics, bot chats, Secret Chats, and bot/anonymous authors are excluded. Forwarded, quoted, protected, expiring, and unsupported media content is also filtered. A supported conversation can therefore contain messages that the server will not return.

The MCP surface is read-first: sending, editing, deleting, account administration, authentication, and access changes are not agent tools. See [message access](docs/text-access.md) and [image access](docs/image-access.md) for exact boundaries.

## How It Works

```text
AI client → telegram-mcp → private local transport → telegram-mcpd → Telegram API
```

| Executable | Role |
| --- | --- |
| `telegram-mcp` | Byte-only stdio relay launched by your MCP client |
| `telegram-mcpd` | Background daemon that owns the session and enforces access policy |
| `telegram-mcpctl` | Human CLI for authentication, access, services, and maintenance |

The local transport is a user-checked Unix socket on macOS/Linux or a SID-checked named pipe on Windows. A single daemon owns the account session. Background startup uses launchd, systemd's user manager, or a Windows per-user logon task. There is no HTTP listener.

## Privacy and Access

- **Your access settings control reads.** Restricted grants are the default. Full read access is an explicit human choice; restricted image access requires an additional opt-in.
- **Messages stay out of local storage.** Telegram MCP does not persist message bodies, search content, or image bytes. SQLite stores operational metadata and access settings. Your MCP client or model provider may retain returned results under its own settings.
- **Sessions are local and unencrypted.** Credentials and sessions use private files protected by your OS account. Anyone able to read those files can obtain the session; same-user processes and administrators are outside the isolation boundary. Ordinary restarts need no recurring unlock or login.
- **Telegram content is untrusted data.** Returned text and images must not be treated as instructions or granted authority over your agent.
- **Read access can have a read effect.** History, context, and image delivery require authorization for the affected read prefix, which may include messages omitted from the response.

Telegram's [API terms](https://core.telegram.org/api/terms) and [content licensing terms](https://telegram.org/tos/content-licensing) apply to the intended use. Local execution, account login, and an access grant do not themselves establish permission for AI processing.

Read the [threat model](docs/threat-model.md) for the security boundaries. Existing macOS Keychain installations require an explicit [legacy migration](docs/keychain.md); ordinary runtime access uses private files.

## Operations and Documentation

| Guide | Covers |
| --- | --- |
| [Installation and diagnostics](docs/installation.md) | Platform setup, service start/stop/restart, upgrades, `doctor`, client reconnection |
| [Authentication](docs/authentication.md) | Production and Test DC, phone/QR login, 2FA, logout and recovery |
| [Message access](docs/text-access.md) | Restricted grants, Full read access, named scopes and read receipts |
| [Image access](docs/image-access.md) | Supported images, permissions and delivery limits |
| [Metadata maintenance](docs/metadata-maintenance.md) | Settings backup/restore and audit retention; backups exclude credentials and do not restore access authority |
| [Distribution](docs/distribution.md) | Source packaging, checksums and platform verification |
| [Architecture decisions](docs/adr/) | Design rationale and tradeoffs |
| [Protocol contracts](docs/contracts.md) | Request, response and failure behavior |

## Development

Use **Go 1.27.1**, pinned in `go.mod` and `.go-version`. Ensure your shell has not set `GO111MODULE=off`.

```sh
git clone https://github.com/lstpsche/telegram-mcp.git
cd telegram-mcp
go build -o ./tmp/telegram-mcp ./cmd/telegram-mcp
go build -o ./tmp/telegram-mcpd ./cmd/telegram-mcpd
go build -o ./tmp/telegram-mcpctl ./cmd/telegram-mcpctl
```

Run `./tmp/telegram-mcpd` in a terminal for foreground development; point your MCP client at the built relay. On Windows, add `.exe` to each build output path. Use the [installation guide](docs/installation.md) for a persistent service.

```sh
go build ./cmd/...
go test ./...
go vet ./...
```

Format changed Go files with `gofmt`. Automated tests use synthetic fixtures and require no Telegram credentials. Real-account checks and Windows desktop lifecycle checks are separate, explicitly enabled procedures.

## Contributing

Bug reports and pull requests are welcome. [Open an issue](https://github.com/lstpsche/telegram-mcp/issues) with your OS/architecture, binary version (`--version`), reproduction steps, and the diagnostic you received. Keep credentials, sessions, private messages, and account identifiers out of reports.

For a larger change, discuss the use case in an issue first. Keep contributions within the single-account, local, read-first scope, preserve the access and storage boundaries, and run the development checks above. See [AGENTS.md](AGENTS.md) for repository conventions.

## License

[MIT](LICENSE). Dependency licenses and notices are included in [THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt).
