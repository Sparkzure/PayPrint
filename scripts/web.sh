#!/usr/bin/env sh
#
# payprint: fetch the frontend bundle from the PayPrint-Frontend GitHub release
# and unpack it into the go:embed directory (server/internal/webui/dist).
#
# The frontend lives in its own repository (Sparkzure/PayPrint-Frontend). Its
# release workflow attaches the built SPA as payprint-web-<tag>.tar.gz (with a
# .sha256 sidecar). This script downloads the newest (or a pinned) release and
# unpacks it, so a backend checkout can produce the single self-contained
# binary without a Node toolchain.
#
# Already unpacked? build.sh skips this step unless --force / --refresh is used.
#
# Usage: sh scripts/web.sh [--version TAG] [--out DIR] [--force]
#        WEB_VERSION=v0.1.2 sh scripts/web.sh

set -eu

REPO="Sparkzure/PayPrint-Frontend"
ASSET_PREFIX="payprint-web"

OUT=""
TAG="${WEB_VERSION:-latest}"
FORCE=0

usage() {
  cat <<EOF
payprint: download the frontend dist from $REPO releases

Usage: sh scripts/web.sh [options]

  --version TAG   release tag to fetch (default: latest; env WEB_VERSION also works)
  --out DIR       unpack destination (default: server/internal/webui/dist)
  --force         re-download even if the destination already has index.html
  -h, --help      show this help
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || { echo "error: $1 needs a tag" >&2; exit 2; }; TAG="$2"; shift 2 ;;
    --out)     [ $# -ge 2 ] || { echo "error: $1 needs a path" >&2; exit 2; }; OUT="$2"; shift 2 ;;
    --force|--refresh) FORCE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option $1 (try --help)" >&2; exit 2 ;;
  esac
done

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
ROOT="$(dirname -- "$SCRIPT_DIR")"
if [ -z "$OUT" ]; then OUT="$ROOT/server/internal/webui/dist"; fi

command -v curl >/dev/null 2>&1 || { echo 'error: curl is required' >&2; exit 1; }
command -v tar  >/dev/null 2>&1 || { echo 'error: tar is required' >&2; exit 1; }

if [ "$FORCE" -eq 0 ] && [ -f "$OUT/index.html" ]; then
  echo "[web] $OUT/index.html already present, skip download (use --force to refresh)"
  exit 0
fi

if [ "$TAG" = "latest" ]; then
  TAG="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')"
  [ -n "$TAG" ] || { echo "error: cannot resolve the latest $REPO release" >&2; exit 1; }
fi

ASSET="$ASSET_PREFIX-$TAG.tar.gz"
URL="https://github.com/$REPO/releases/download/$TAG/$ASSET"
TMP="$(mktemp "${TMPDIR:-/tmp}/payprint-web.XXXXXX.tar.gz")"
trap 'rm -f "$TMP"' EXIT

echo "[web] downloading $URL"
curl -fsSL -o "$TMP" "$URL"

# Verify against the sidecar checksum when the release provides one.
if curl -fsSL -o "$TMP.sha256" "$URL.sha256" 2>/dev/null; then
  echo "$TMP  $(sed 's/ .*//' "$TMP.sha256")" | sha256sum -c - >/dev/null \
    || { echo "error: checksum mismatch for $ASSET" >&2; exit 1; }
  echo '[web] sha256 ok'
else
  echo '[web] no checksum sidecar, skipping verification'
fi

echo "[web] unpacking into $OUT"
mkdir -p "$OUT"
# Drop the previous bundle but keep dot-files: the .gitkeep placeholder is what
# makes "go build" work in a checkout where the frontend was never fetched.
for entry in "$OUT"/* "$OUT"/.[!.]*; do
  [ -e "$entry" ] || continue
  case "${entry##*/}" in
    .gitkeep) : ;;
    *) rm -rf "$entry" ;;
  esac
done
tar -xzf "$TMP" -C "$OUT"

[ -f "$OUT/index.html" ] || { echo "error: $ASSET does not contain index.html" >&2; exit 1; }
echo "[web] done ($TAG)"
