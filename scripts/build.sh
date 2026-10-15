#!/usr/bin/env sh
#
# payprint: build the single deliverable file.
#
# Two steps, in order:
#   1. make sure the frontend bundle is in the go:embed directory
#      (server/internal/webui/dist): reuse what is there, or download the newest
#      PayPrint-Frontend release via scripts/web.sh
#   2. go build -> ONE binary carrying the API, the worker and the whole frontend
#
# There is no runtime asset directory: the frontend is embedded, so the only
# thing to copy to a device is this one file (plus the config it writes on
# first start).
#
# Cross-compiling from any host works because modernc.org/sqlite is pure Go and
# CGO stays off:
#   GOOS=linux GOARCH=arm64 sh scripts/build.sh -o dist/payprint-server-linux-arm64
#   GOOS=linux GOARCH=arm   GOARM=7 sh scripts/build.sh -o dist/payprint-server-linux-armv7
#
# Requires Go 1.27+. No Node.js needed: the frontend comes prebuilt from its
# own repository's release. Building the frontend yourself is a frontend-repo
# concern (see PayPrint-Frontend); point --web-dist at its dist/ if you want a
# locally built bundle.
#
# Usage: sh scripts/build.sh [-o 输出路径] [--version 版本号]
#                            [--web-dist PATH] [--no-web] [--refresh-web]

set -eu

OUT=""
VERSION="${VERSION:-dev}"
GIT_COMMIT="${GIT_COMMIT:-}"
BUILT_AT="${BUILT_AT:-}"
WEB_DIST=""
NO_WEB=0
REFRESH_WEB=0

usage() {
  cat <<'EOF'
payprint: build the single binary deliverable

Usage: sh scripts/build.sh [options]

  -o, --out PATH        output path (default dist/payprint-server[.exe])
      --version VER     version written into the startup log (default dev; env VERSION works too)
      --web-dist PATH   use this frontend dist instead of downloading a release
      --refresh-web     re-download the frontend dist even if one is already embedded
      --no-web          build without touching the frontend (whatever is in the
                        embed dir gets in; a fresh checkout builds a UI-less binary)
  -h, --help            show this help

Environment: GOOS / GOARCH / GOARM select the cross-compile target.
GIT_COMMIT / BUILT_AT override the version metadata (auto-detected from git otherwise).
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    -o|--out)      [ $# -ge 2 ] || { echo "error: $1 需要一个路径" >&2; exit 2; }; OUT="$2"; shift 2 ;;
    --version)     [ $# -ge 2 ] || { echo "error: $1 需要一个版本号" >&2; exit 2; }; VERSION="$2"; shift 2 ;;
    --web-dist)    [ $# -ge 2 ] || { echo "error: $1 需要一个路径" >&2; exit 2; }; WEB_DIST="$2"; shift 2 ;;
    --refresh-web) REFRESH_WEB=1; shift ;;
    --no-web)      NO_WEB=1; shift ;;
    -h|--help)     usage; exit 0 ;;
    *) echo "error: 未知选项 $1（试试 --help）" >&2; exit 2 ;;
  esac
done

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
ROOT="$(dirname -- "$SCRIPT_DIR")"
EMBED_DIR="$ROOT/server/internal/webui/dist"

# ---- 1. 前端产物 -----------------------------------------------------------

if [ "$NO_WEB" -eq 0 ]; then
  if [ -n "$WEB_DIST" ]; then
    [ -f "$WEB_DIST/index.html" ] || { echo "error: $WEB_DIST/index.html 不存在" >&2; exit 1; }
    echo "[build] 使用本地前端产物 $WEB_DIST"
    mkdir -p "$EMBED_DIR"
    for entry in "$EMBED_DIR"/* "$EMBED_DIR"/.[!.]*; do
      [ -e "$entry" ] || continue
      case "${entry##*/}" in
        .gitkeep) : ;;
        *) rm -rf "$entry" ;;
      esac
    done
    cp -R "$WEB_DIST"/. "$EMBED_DIR"/
  elif [ "$REFRESH_WEB" -eq 1 ] || [ ! -f "$EMBED_DIR/index.html" ]; then
    sh "$SCRIPT_DIR/web.sh" $([ "$REFRESH_WEB" -eq 1 ] && echo --force)
  else
    echo "[build] 复用已嵌入的前端产物（--refresh-web 可重新下载）"
  fi
else
  echo '[build] --no-web：跳过前端产物'
fi

# ---- 2. go build -----------------------------------------------------------

if [ -z "$OUT" ]; then
  target_os="${GOOS:-}"
  if [ -z "$target_os" ]; then
    case "$(uname -s)" in
      MINGW*|MSYS*|CYGWIN*) target_os=windows ;;
      *)                    target_os=linux ;;
    esac
  fi
  case "$target_os" in
    windows) OUT="$ROOT/dist/payprint-server.exe" ;;
    *)       OUT="$ROOT/dist/payprint-server" ;;
  esac
fi
mkdir -p "$(dirname -- "$OUT")"

if [ -z "$GIT_COMMIT" ]; then
  GIT_COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
fi
if [ -z "$BUILT_AT" ]; then
  BUILT_AT="$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || echo unknown)"
fi

echo "[build] go build -> $OUT"
(
  cd "$ROOT/server"
  CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION -X main.gitCommit=$GIT_COMMIT -X main.builtAt=$BUILT_AT" \
    -o "$OUT" ./cmd/server
)

size="$(wc -c < "$OUT" | tr -d ' ')"
echo "[build] 完成：$OUT（$((size / 1024 / 1024)) MB，${GOOS:-本机}，version=$VERSION commit=$GIT_COMMIT）"
