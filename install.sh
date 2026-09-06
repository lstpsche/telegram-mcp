#!/bin/sh
# Download the verified control program; it installs and checks the full release.
set -eu
version=${1:-0.5.0}
if [ "$#" -gt 1 ] || ! printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  echo 'Usage: sh install.sh [X.Y.Z]' >&2
  exit 2
fi
if [ "$(id -u)" -eq 0 ]; then echo 'Run as your normal user, without sudo.' >&2; exit 1; fi
case "$(uname -s)" in Darwin) platform=darwin ;; Linux) platform=linux ;; *) echo 'Unsupported OS; use install.ps1 on Windows.' >&2; exit 1 ;; esac
case "$(uname -m)" in arm64|aarch64) arch=arm64 ;; x86_64) arch=amd64 ;; *) echo 'Unsupported architecture.' >&2; exit 1 ;; esac
umask 077
bootstrap_dir=$(mktemp -d "$HOME/.telegram-mcp-bootstrap.XXXXXXXX")
trap 'rm -r -- "$bootstrap_dir"' EXIT
trap 'exit 130' HUP INT TERM
base="https://github.com/lstpsche/telegram-mcp/releases/download/v$version"
asset="telegram-mcp-$version-$platform-$arch"
curl --fail --location --connect-timeout 15 --max-time 300 --proto '=https' --proto-redir '=https' --max-filesize 65536 --output "$bootstrap_dir/SHA256SUMS" "$base/SHA256SUMS"
curl --fail --location --connect-timeout 15 --max-time 300 --proto '=https' --proto-redir '=https' --max-filesize 134217728 --output "$bootstrap_dir/control" "$base/$asset"
expected=$(awk -v name="$asset" '$2 == name {print $1; count++} END {if (count != 1) exit 1}' "$bootstrap_dir/SHA256SUMS")
if ! printf '%s\n' "$expected" | grep -Eq '^[a-f0-9]{64}$'; then echo 'Invalid release checksum.' >&2; exit 1; fi
case "$platform" in darwin) actual=$(shasum -a 256 "$bootstrap_dir/control" | awk '{print $1}') ;; linux) actual=$(sha256sum "$bootstrap_dir/control" | awk '{print $1}') ;; esac
if [ "$actual" != "$expected" ]; then echo 'Control executable checksum mismatch.' >&2; exit 1; fi
chmod 700 "$bootstrap_dir/control"
"$bootstrap_dir/control" install --version "$version" --setup
