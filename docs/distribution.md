# Portable distribution

Telegram MCP is an unofficial client using Telegram's API. Packages contain
three executables and documentation. Each recipient configures their own local
account; credentials, sessions, permissions and metadata are never bundled.

Build with Go 1.27.1 from the repository root on macOS, Linux or Windows. The
packager cross-compiles pure Go binaries with cgo disabled. It requires no Apple
account, signing certificate, notarization service or paid membership.

```sh
mkdir -p tmp/releases
env GO111MODULE=on go run ./tools/package-release \
  --version 1.0.0 \
  --output-dir "$PWD/tmp/releases/telegram-mcp-1.0.0"
```

PowerShell equivalent:

```powershell
New-Item -ItemType Directory -Force tmp/releases
$env:GO111MODULE = 'on'
go run ./tools/package-release --version 1.0.0 --output-dir "$PWD\tmp\releases\telegram-mcp-1.0.0"
```

By default the command builds macOS, Linux and Windows for arm64 and amd64. Use
`--targets linux/amd64,windows/arm64` to select a subset. Each target has its own
ZIP and unpacked payload directory; Windows commands have an `.exe` suffix.
Archive paths use forward slashes and Unix executable permissions are preserved.

The default `--mode distribution` requires a clean checkout and rechecks the
source revision and working tree after building. For synthetic local testing,
`--mode development` permits dirty source and marks the build identity with
`-dirty`. Output directories must be new, absolute and canonical, with an existing
parent. A failed build retains its partial output but never writes a successful
`release.json`; retry with a fresh directory.

Each payload includes `SHA256SUMS` for its executables and documentation. The
outer `release.json` records the version, source commit, platform, ZIP name and
archive SHA-256 for every target. These checksums detect corruption; they do not
prove the publisher's identity. Compare them against a trusted publication
channel. The manifest's `unsigned-build-only` or `development-build-only`
qualification records packaging, not native platform acceptance.

The packager never executes target binaries, installs a service, accesses account
state, uploads artifacts or contacts a signing service. Cross-compilation alone
does not verify native filesystem permissions, local transport, background
startup or OS download controls. Run synthetic tests and the commands' `--version`
on each supported OS before qualifying a release for that OS.

Extract the matching archive into a new private directory, verify its checksums,
and follow [installation](installation.md) and [authentication](authentication.md).
Ordinary setup and upgrades use private local files and need no application-level
unlock after restart. Downloads are not notarized or publisher-signed, so macOS
or Windows may require an explicit OS security approval. A local source build is
also available; this tool does not disable those OS protections.

Existing macOS accounts whose secrets remain in Keychain require the explicit
[legacy migration](keychain.md). Standard packages cannot read that Keychain;
only the separately built [migration executable](development-signing.md) needs
the identity that owns those legacy items.
