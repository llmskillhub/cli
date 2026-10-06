#!/bin/sh
# Install llmsh.
#
#   curl -fsSL https://raw.githubusercontent.com/llmskillhub/cli/main/install.sh | sh
#
# Downloads the release binary for this machine, checks it against the
# published SHA256SUMS, and puts it on your PATH. Reads the checksum file from
# the same release as the binary: a checksum served from somewhere an attacker
# would also have to compromise is worth having, and one they serve themselves
# is not.
set -eu

REPO="${LLMSH_REPO:-llmskillhub/cli}"
BIN_DIR="${LLMSH_BIN_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "llmsh: no build for $arch" >&2; exit 1 ;;
esac
case "$os" in
  darwin|linux) ;;
  *) echo "llmsh: no build for $os — see https://github.com/$REPO/releases" >&2; exit 1 ;;
esac

tag="${LLMSH_VERSION:-}"
if [ -z "$tag" ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
        sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
fi
[ -n "$tag" ] || { echo "llmsh: could not find a release" >&2; exit 1; }

version="${tag#v}"
file="llmsh_${version}_${os}_${arch}"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "llmsh $version ($os/$arch)"
curl -fsSL "$base/$file" -o "$tmp/llmsh"
curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"

want=$(grep " $file\$" "$tmp/SHA256SUMS" | awk '{print $1}')
if [ -z "$want" ]; then
  echo "llmsh: $file is not listed in SHA256SUMS; refusing to install" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/llmsh" | awk '{print $1}')
else
  got=$(shasum -a 256 "$tmp/llmsh" | awk '{print $1}')
fi
if [ "$want" != "$got" ]; then
  echo "llmsh: checksum mismatch — refusing to install" >&2
  echo "  expected $want" >&2
  echo "  got      $got" >&2
  exit 1
fi

mkdir -p "$BIN_DIR"
mv "$tmp/llmsh" "$BIN_DIR/llmsh"
chmod +x "$BIN_DIR/llmsh"

echo "installed $BIN_DIR/llmsh"
case ":$PATH:" in
  *":$BIN_DIR:"*) "$BIN_DIR/llmsh" version ;;
  *) echo "note: $BIN_DIR is not on your PATH" ;;
esac
