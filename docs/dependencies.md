# Dependency snapshot

Verified on 2026-09-04 against the Go module proxy and authoritative release
metadata:

| Dependency | Selected | Status and purpose |
|---|---:|---|
| Go | 1.27.1 | Current stable toolchain; selected by `go.mod` and `.go-version` |
| `github.com/gotd/td` | v0.161.0 | Current stable; Telegram account runtime, requires Go 1.25+ |
| `github.com/gotd/contrib` | v0.25.0 | Bounded per-invocation flood waits and request rate limiter, aligned with gotd v0.161.0 |
| `github.com/awnumar/memguard` | v0.23.0 | Locked, wipe-on-use 2FA buffers through gotd `srpguard` |
| `github.com/modelcontextprotocol/go-sdk` | v1.7.0 | Official MCP server and client SDK; prerelease v1.8 builds are not selected |
| `github.com/google/jsonschema-go` | v0.4.3 | Schema inference for structured status results; shares the SDK's existing pin |
| `golang.org/x/term` | v0.45.0 | Direct no-echo `/dev/tty` input |
| `rsc.io/qr` | v0.2.0 | Local terminal QR rendering without printing the token URI |
| `modernc.org/sqlite` | v1.58.0 | Current stable pure-Go SQLite driver for metadata only |
| `github.com/keybase/go-keychain` | v0.0.1 | Evaluated and rejected; not retained in the module graph |

The optional legacy Keychain migration adapter uses Security.framework directly; see
[the Keychain decision and proof](keychain.md).

Primary references:

- <https://go.dev/dl/>
- <https://github.com/gotd/td/releases/tag/v0.161.0>
- <https://github.com/gotd/contrib/releases/tag/v0.25.0>
- <https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.7.0>
- <https://pkg.go.dev/modernc.org/sqlite@v1.58.0>
- <https://developer.apple.com/documentation/security/ksecusekeychain>
- <https://developer.apple.com/documentation/security/ksecattrsynchronizable>

Update one pin at a time, review its Go requirement and transitive changes,
rerun the full repository gates, and never select a prerelease implicitly.
