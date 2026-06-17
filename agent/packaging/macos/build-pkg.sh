#!/usr/bin/env sh
set -eu

ARCH="${1:-arm64}"
VERSION="${OV_AGENT_VERSION:-dev}"
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
DIST="$ROOT/dist"
PKGROOT="$DIST/pkgroot-$ARCH"
SCRIPTS="$DIST/scripts-$ARCH"

rm -rf "$PKGROOT" "$SCRIPTS"
mkdir -p "$PKGROOT/usr/local/ov-computeruse/agent" "$SCRIPTS"
cp "$DIST/ov-agent-darwin-$ARCH" "$PKGROOT/usr/local/ov-computeruse/agent/ov-agent"
chmod 755 "$PKGROOT/usr/local/ov-computeruse/agent/ov-agent"
cp "$ROOT/packaging/macos/postinstall" "$SCRIPTS/postinstall"
chmod 755 "$SCRIPTS/postinstall"

pkgbuild \
  --root "$PKGROOT" \
  --scripts "$SCRIPTS" \
  --identifier "com.ov-computeruse.agent" \
  --version "$VERSION" \
  "$DIST/ov-agent-darwin-$ARCH.pkg"
