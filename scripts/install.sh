#!/bin/sh
# Install the AgentFlow binary from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/chenhg5/agentflow/main/scripts/install.sh | sh
#
# Environment:
#   AGENTFLOW_VERSION      tag to install, for example v0.0.3 (default: newest release, including previews)
#   AGENTFLOW_INSTALL_DIR  target directory (default: $HOME/.local/bin)
set -eu

REPO="chenhg5/agentflow"
INSTALL_DIR="${AGENTFLOW_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${AGENTFLOW_VERSION:-}"

fail() { echo "agentflow install: $*" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "unsupported OS $(uname -s); download a release archive from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac

if [ -z "$VERSION" ]; then
  # /releases/latest skips pre-releases, and every release before 1.0 is one.
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=1" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$VERSION" ] || fail "cannot resolve the newest release; set AGENTFLOW_VERSION"
fi

name="agentflow-$VERSION-$os-$arch"
base="https://github.com/$REPO/releases/download/$VERSION"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

echo "Installing AgentFlow $VERSION ($os/$arch) into $INSTALL_DIR"
curl -fsSL -o "$work/$name.tar.gz" "$base/$name.tar.gz" || fail "download failed: $base/$name.tar.gz"
curl -fsSL -o "$work/checksums.txt" "$base/checksums.txt" || fail "download failed: $base/checksums.txt"

expected=$(awk -v file="$name.tar.gz" '$2 == file { print $1 }' "$work/checksums.txt")
[ -n "$expected" ] || fail "no checksum for $name.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$work/$name.tar.gz" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$work/$name.tar.gz" | awk '{ print $1 }')
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $name.tar.gz"

tar -xzf "$work/$name.tar.gz" -C "$work"
mkdir -p "$INSTALL_DIR"
install -m 0755 "$work/$name/agentflow" "$INSTALL_DIR/agentflow"

echo "Installed $("$INSTALL_DIR/agentflow" version --format table 2>/dev/null | head -n 1 || echo "$INSTALL_DIR/agentflow")"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Add $INSTALL_DIR to PATH, for example: export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
echo "Next: agentflow doctor --format table"
