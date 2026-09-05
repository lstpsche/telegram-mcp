# Local development signing

Use an existing personal **Apple Development** identity for local builds that
share account secrets. A self-signed certificate is insufficient: the legacy
Keychain also applies partition checks, and code outside Apple's recognized
signing families receives a partition tied to its code hash. Rebuilding changes
that hash. Apple Development signing supplies a stable team partition.

The control command and daemon keep distinct signing identifiers but share one
designated requirement. It requires an Apple anchor, the exact signing
certificate, and one of those two identifiers. The relay has its own identifier
and default requirement. This uses the Keychain adapter's existing item access
behavior; no trusted-application list, partition list or existing item is edited.

In Keychain Access, select the intended personal Apple Development certificate
under My Certificates and copy its SHA-1 fingerprint, removing spaces and
colons. The fingerprint is public metadata. The matching private key must
already be available to `codesign`; never export it or put a Keychain password
in a command. Xcode's account certificate management can provision an Apple
Development identity if one is absent. Do not select a company identity unless
its owner has authorized that use.

Run this from the repository root. Replace the fingerprint placeholder. The
recipe creates fresh private directories, builds two versions with different
bytes, signs the actual commands, verifies the required signing family and
shared requirement, and runs the native synthetic access matrix:

```sh
(
set -eu
umask 077
signing_identity='REPLACE_WITH_40_HEX_SHA1'
case "$signing_identity" in
  ''|*[!0-9A-Fa-f]*) echo 'Use a 40-character certificate fingerprint' >&2; exit 2 ;;
esac
test "${#signing_identity}" -eq 40

repository_root="$(pwd -P)"
mkdir -p "$repository_root/tmp"
artifact_root="$repository_root/tmp/development-signing-$(date +%Y%m%d%H%M%S)"
mkdir -m 700 "$artifact_root"
shared_requirement="anchor apple generic and certificate leaf = H\"$signing_identity\" and (identifier \"dev.telegram-mcp.control\" or identifier \"dev.telegram-mcp.daemon\")"
development_requirement='anchor apple generic and certificate leaf[field.1.2.840.113635.100.6.1.12] exists'

for variant in current upgrade; do
  binary_dir="$artifact_root/$variant"
  mkdir -m 700 "$binary_dir"
  for command in telegram-mcp telegram-mcpctl telegram-mcpd; do
    env GO111MODULE=on go build \
      -ldflags "-X github.com/lstpsche/telegram-mcp/internal/buildinfo.Version=local-$variant" \
      -o "$binary_dir/$command" "./cmd/$command"
  done
  /usr/bin/codesign --force --sign "$signing_identity" \
    --identifier dev.telegram-mcp.relay --options runtime --timestamp=none \
    "$binary_dir/telegram-mcp"
  /usr/bin/codesign --verify --strict -R "=$development_requirement" \
    "$binary_dir/telegram-mcp"
  for role in control daemon; do
    case "$role" in
      control) command=telegram-mcpctl ;;
      daemon) command=telegram-mcpd ;;
    esac
    /usr/bin/codesign --force --sign "$signing_identity" \
      --identifier "dev.telegram-mcp.$role" --options runtime --timestamp=none \
      --requirements "=designated => $shared_requirement" "$binary_dir/$command"
    /usr/bin/codesign --verify --strict \
      -R "=$development_requirement and ($shared_requirement)" "$binary_dir/$command"
  done
done

env GO111MODULE=on go build -o "$artifact_root/keychain-probe" ./tools/keychain-probe
/usr/bin/codesign --force --sign - "$artifact_root/keychain-probe"
env -i HOME="$HOME" LANG=C PATH=/usr/bin:/bin TMPDIR=/tmp \
  "$artifact_root/keychain-probe" \
  --bin-dir "$artifact_root/current" \
  --upgrade-bin-dir "$artifact_root/upgrade" </dev/null
)
```

The leading `=` in each requirement argument tells `codesign` to parse literal
source; without it, the argument is interpreted as a filename. Every verification
must succeed. Do not install artifacts from an interrupted or failed recipe.

The expected matrix result is `ok: true`, eight successful checks, unchanged
artifact hashes, and `cleanup: "verified_absent"` for every check. This covers
noninteractive read/update/delete through both commands, unchanged restarts,
and rebuilt upgrades using the same certificate and requirement. An unrelated
identifier signed by the same certificate and an ad-hoc signature using the
control identifier remain outside the shared requirement.

These temporary directories are qualification artifacts. For an installation,
build and sign into a new stable private directory using the same signing
identity and requirements, qualify the exact artifacts against the previous
version, then follow [installation and upgrade operations](installation.md).
Never overwrite or re-sign binaries used by an existing installation.

The certificate pin deliberately makes replacement or renewal a different
identity. Preserve the original certificate/private key and executable artifacts
while they own account items. Certificate rotation and access to items created
under older ad-hoc or individual requirements need separate recovery work;
this procedure neither migrates nor deletes those items. It does not establish
access while the login keychain is locked, real LaunchAgent execution, Telegram
account acceptance, or permission to distribute these local builds. Developer ID
distribution and notarization have their own requirements.

Apple documents [sharing a designated requirement](https://developer.apple.com/library/archive/documentation/Security/Conceptual/CodeSigningGuide/Procedures/Procedures.html).
The additional partition behavior is visible in Apple's
[Security implementation](https://github.com/apple-oss-distributions/Security/blob/main/securityd/src/clientid.cpp).
