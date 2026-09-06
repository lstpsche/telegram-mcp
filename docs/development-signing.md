# Signing a legacy Keychain migration executable

Ordinary Telegram MCP builds and upgrades require no signing identity. This
procedure applies only to a macOS account whose credentials and sessions still
reside in the legacy Keychain adapter. The new runtime uses private unencrypted
local storage and does not fall back to Keychain.

Preserve the previous installed executables and the certificate/private key
that signed them. A new certificate, including a renewal, is a different
identity and does not establish access to the old items. No paid membership or
new certificate is required when the existing authorized identity is available.

Build a separate native cgo-enabled control executable with Go 1.27.1 and Xcode
command-line tools. Use `GOARCH=arm64` on Apple Silicon or `GOARCH=amd64` on Intel.
Use a new private output directory and the original certificate's SHA-1
fingerprint from Keychain Access. The fingerprint is public metadata; never
export the private key or put passwords into command arguments.

```sh
migration_dir="$HOME/Applications/telegram-mcp-keychain-migration"
mkdir -m 700 "$migration_dir"
env GO111MODULE=on GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 \
  go build -o "$migration_dir/telegram-mcpctl" ./cmd/telegram-mcpctl
identity=REPLACE_WITH_ORIGINAL_40_HEX_CERTIFICATE_FINGERPRINT
requirement="anchor apple generic and certificate leaf = H\"$identity\" and (identifier \"dev.telegram-mcp.control\" or identifier \"dev.telegram-mcp.daemon\")"
/usr/bin/codesign --force --sign "$identity" \
  --identifier dev.telegram-mcp.control --options runtime --timestamp=none \
  --requirements "=designated => $requirement" "$migration_dir/telegram-mcpctl"
/usr/bin/codesign --verify --strict -R "=$requirement" \
  "$migration_dir/telegram-mcpctl"
```

This requirement matches the legacy shared control/daemon recipe. It does not
repair items created under other requirements or signing identities. Validate
legacy access with the synthetic probe described in [Keychain migration](keychain.md)
before transferring real account custody.

Stop the existing daemon using its installed control executable. Then, in a
human terminal, explicitly accept the storage change:

```sh
"$migration_dir/telegram-mcpctl" migrate-keychain --accept-plaintext-storage
```

The command copies the supported credential/session/integrity records atomically
into private local storage. It refuses an existing destination and retains the
legacy Keychain items. It neither authenticates with Telegram nor deletes the
source records. Follow [installation](installation.md) to switch to the portable
runtime after successful migration. Do not overwrite active executables.

If the original signing identity is unavailable or Keychain access fails, this
procedure cannot establish access. Keep the original account state intact and
resolve access or use an explicit account recovery workflow. Creating a different
certificate does not recover the existing secrets.
