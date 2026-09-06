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

On macOS or Linux, install through our [Homebrew tap](https://github.com/lstpsche/homebrew-tap):

```sh
brew install lstpsche/tap/telegram-mcp
telegram-mcpctl install --version 0.2.0 --setup
```

The second command creates the private per-user installation and starts setup.
Use its printed relay path in your MCP client. The service runs from private
copies outside Homebrew's Cellar; `brew cleanup` does not remove them.

You can also use the guided installer below to select the right binary, verify
checksums, prepare private permissions and start setup. Neither option requires Go.

**macOS / Linux**

```sh
curl --fail --location --proto '=https' --proto-redir '=https' \
  -o install.sh https://github.com/lstpsche/telegram-mcp/releases/download/v0.2.0/install.sh
sh install.sh
```

**Windows PowerShell, without elevation**

```powershell
Invoke-WebRequest https://github.com/lstpsche/telegram-mcp/releases/download/v0.2.0/install.ps1 -OutFile install.ps1
.\install.ps1
```

The scripts download a checksum-verified control program, then verify
the full release and its payload before installing. Downloads and checksums come
from GitHub Releases; they establish integrity, not independent publisher identity.
Setup collects credentials directly through your OS console and asks you to
choose access. Read or inspect the downloaded script before running it if desired.

You can also download and prepare an archive manually. Each ZIP includes the
three executables, documentation, licenses and payload checksums.

| Platform | v0.2.0 archive |
| --- | --- |
| macOS Apple Silicon | [darwin-arm64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.2.0/telegram-mcp-0.2.0-darwin-arm64.zip) |
| macOS Intel | [darwin-amd64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.2.0/telegram-mcp-0.2.0-darwin-amd64.zip) |
| Linux ARM64 | [linux-arm64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.2.0/telegram-mcp-0.2.0-linux-arm64.zip) |
| Linux x86_64 | [linux-amd64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.2.0/telegram-mcp-0.2.0-linux-amd64.zip) |
| Windows ARM64 | [windows-arm64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.2.0/telegram-mcp-0.2.0-windows-arm64.zip) |
| Windows x86_64 | [windows-amd64](https://github.com/lstpsche/telegram-mcp/releases/download/v0.2.0/telegram-mcp-0.2.0-windows-amd64.zip) |

Extract into a **new private directory at a stable, absolute path**. Verify the archive hash against the release's `release.json` and the extracted files against `SHA256SUMS`. Follow the [platform installation guide](docs/installation.md) to prepare permissions: Unix requires a directory owned by you with mode `0700`; Windows requires owner-only ACLs. Run as your normal user, without `sudo` or an elevated Windows terminal.

Packages are unsigned and not notarized, so your OS may require approval before running a download. Native CI covers macOS, Linux, and Windows amd64. Windows desktop task startup/restart/stop remains unverified; Windows arm64 is cross-compiled but has not been exercised natively. See [distribution and verification](docs/distribution.md) for details.

Prefer to compile locally? See [Development](#development) and the [source installation instructions](docs/installation.md).

## Quick Start

You need a Telegram account, your application's API ID and API hash, and an MCP client. Obtain application credentials through [Telegram's development tools](https://my.telegram.org). Before using real conversations with an AI system, establish eligibility under Telegram's terms and the rights and consent of the people affected; the setup flag records your attestation, not an exemption.

The installer starts `telegram-mcpctl setup`. If you prepared an archive manually,
run `./telegram-mcpctl setup` (`.\telegram-mcpctl.exe setup` on Windows).

The guide handles:

1. **Account configuration and login.** Explicitly choose production or a Test DC,
   then phone or QR authentication. Existing recorded authorization is preserved
   when resuming; credentials and login codes are entered without echo.
2. **Access.** Keep existing settings, create an exact restricted grant, or
   explicitly enable Full read access. The restricted guide offers the newest
   Saved Message or a numbered conversation list, with author, range, expiry,
   images and read-receipt permission shown before confirmation. Signing in alone
   grants no content access. Scopes do not restrict Full read authority.
3. **Service startup.** Register startup at user login and wait for a responding,
   account-ready daemon. Completed steps remain if setup is interrupted.
4. **Client connection.** Print standard MCP JSON or register a new server using
   the installed Codex CLI after confirmation. Existing client entries are
   preserved. Reconnect your client and ask it to check Telegram status.

The default restricted grant is search-only unless you permit read receipts.
After granting the newest Saved Message, try searching for a word in that item.
Use `telegram-mcpctl access setup` to add or replace a grant later. See
[message access](docs/text-access.md) for the exact authority and read-effect rules.

For catch-up, create a scope containing the exact peer you granted. In v0.2.0,
use `telegram-mcpctl scope --name project --peer YOUR_PEER_ID`, replacing
`YOUR_PEER_ID` with the `tgpeer:v1:...` ID printed by access setup.
Current source builds also offer `telegram-mcpctl scope setup` and an optional
scope prompt during setup; these are not included in the v0.2.0 downloads yet.
The guide lets you select numbered peers from stored grants, preview the complete
membership, and confirm before saving. See [scope setup](docs/text-access.md).

Then ask your agent:

> Check Telegram status and list my scopes. For the scope named project, tell me
> how many peers are eligible, then catch up from 09:00 to 10:00 UTC on YYYY-MM-DD.

Replace the date and time window with one containing your test message.
A ready connection does not imply content permission. If no peers are eligible,
review `telegram-mcpctl grants` and renew expired grants through `access setup`.
If peers are eligible but no messages appear, check the granted author, message
range and requested time window, and inspect reported exclusions or partial
coverage. Catch-up does not mark chats read. See the
[human verification procedure](docs/account-runtime-verification.md) for a complete check.

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

## Updates

For Homebrew installations, run `brew update` and
`brew upgrade lstpsche/tap/telegram-mcp`, then run the explicit
`telegram-mcpctl upgrade --version ...` command shown by
`brew info lstpsche/tap/telegram-mcp`. Upgrading the formula alone does not
replace the running service. Uninstalling it leaves private account data and
service registration intact.

Managed installations keep stable `telegram-mcp` and `telegram-mcpctl` programs
under your OS user configuration directory in `Telegram MCP/install`:

| Platform | Control program |
| --- | --- |
| macOS | `~/Library/Application Support/Telegram MCP/install/telegram-mcpctl` |
| Linux | `${XDG_CONFIG_HOME:-$HOME/.config}/Telegram MCP/install/telegram-mcpctl` |
| Windows | `%APPDATA%\Telegram MCP\install\telegram-mcpctl.exe` |

Invoke that control program with `upgrade --version X.Y.Z`. It verifies the new
release before switching the service, preserves account data and old binaries,
and checks the new service responds. Reconnect your MCP client afterward; its
managed relay path stays the same. Downgrades are not supported. See
[installation and recovery](docs/installation.md) for interrupted upgrades and
adopting an existing manual installation.

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
