# Dependency snapshot

Verified on 2026-09-04 against the Go module proxy and authoritative release
metadata:

| Dependency | Selected | Status and purpose |
|---|---:|---|
| Go | 1.27.1 | Current stable toolchain; selected by `go.mod` and `.go-version` |
| `github.com/gotd/td` | v0.161.0 | Current stable; future Telegram adapter, requires Go 1.25+ |
| `github.com/modelcontextprotocol/go-sdk` | v1.7.0 | Current stable official SDK; prerelease v1.8 builds are not selected |
| `modernc.org/sqlite` | v1.58.0 | Current stable pure-Go SQLite driver for metadata only |
| `github.com/keybase/go-keychain` | v0.0.1 | Evaluated and rejected; not retained in the module graph |

The Keychain implementation uses the macOS platform framework directly; see
[the Keychain decision and proof](keychain.md).

Primary references:

- <https://go.dev/dl/>
- <https://github.com/gotd/td/releases/tag/v0.161.0>
- <https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.7.0>
- <https://pkg.go.dev/modernc.org/sqlite@v1.58.0>
- <https://developer.apple.com/documentation/security/ksecusekeychain>
- <https://developer.apple.com/documentation/security/ksecattrsynchronizable>

Update one pin at a time, review its Go requirement and transitive changes,
rerun the full Phase 0 gates, and never select a prerelease implicitly.

