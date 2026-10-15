#!/usr/bin/env sh
#
# payprint: 端到端冒烟测试驱动（Linux / macOS / Git Bash / WSL / CI）。
#
# 做四件事：构建单二进制（--no-web，冒烟只走 API 与 IPP 链路，不需要前端产物）→
# 写一份临时配置 → 在另一个端口起服务 → 用 PayPrint-Frontend 仓库里的
# scripts/smoke.mjs 对着它把主链路真跑一遍，最后停掉服务。
# 配置里的数据库与上传目录都指向临时目录，不会碰开发机上真实的那一份。
#
# 冒烟脚本本身是 Node 写的（用真实签名实现跑真实 HTTP，并起一台假 IPP 打印机），
# 这个 shell 脚本只负责把服务端拉起来、把环境变量递进去。它复用的是前端仓库里
# 真实的签名实现（src/lib/sign.ts），这正是"跨语言一致性"检查的价值所在。
#
# 依赖：Go 1.27+、Node 24+（smoke.mjs 直接 import .ts，需要类型剥离）、curl，
# 以及前端仓库（默认找 ../PayPrint-Frontend，可用 SMOKE_WEB_DIR 指定别处）。
#
# 用法：sh scripts/smoke.sh [--port 18080] [--keep-data]
#       SMOKE_PORT=18081 sh scripts/smoke.sh

set -eu

PORT="${SMOKE_PORT:-18080}"
KEEP_DATA=0

while [ $# -gt 0 ]; do
  case "$1" in
    --port)      [ $# -ge 2 ] || { echo 'error: --port 需要一个端口号' >&2; exit 2; }; PORT="$2"; shift 2 ;;
    --keep-data) KEEP_DATA=1; shift ;;
    -h|--help)
      cat <<'EOF'
payprint: 端到端冒烟测试

用法：sh scripts/smoke.sh [选项]

  --port N       服务端监听的端口（默认 18080，也可用环境变量 SMOKE_PORT）
  --keep-data    结束后保留临时目录（数据库、日志），方便排查
  -h, --help     显示本帮助
EOF
      exit 0 ;;
    *) echo "error: 未知选项 $1（试试 --help）" >&2; exit 2 ;;
  esac
done

command -v curl >/dev/null 2>&1 || { echo 'error: 需要 curl' >&2; exit 1; }

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
ROOT="$(dirname -- "$SCRIPT_DIR")"
ADMIN_TOKEN="smoke-admin-token"

RUN_DIR="$(mktemp -d "${TMPDIR:-/tmp}/payprint-smoke.XXXXXX")"
SERVER_PID=""

cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [ "$KEEP_DATA" -eq 1 ]; then
    echo "[smoke] 临时目录保留在 $RUN_DIR"
  else
    rm -rf "$RUN_DIR"
  fi
}
trap cleanup EXIT INT TERM

EXE="$RUN_DIR/payprint-server"
CONF="$RUN_DIR/payprint.conf"
STDOUT_LOG="$RUN_DIR/server.out.log"
STDERR_LOG="$RUN_DIR/server.err.log"

echo '[smoke] 构建单二进制（--no-web：冒烟只走 API 与 IPP，不取前端产物）...'
sh "$SCRIPT_DIR/build.sh" --no-web -o "$EXE"

# 端到端脚本在前端仓库里（它 import 前端真实的签名实现）。查找顺序：
# SMOKE_WEB_DIR 环境变量 → 仓库旁边的 PayPrint-Frontend → 仓库内的检出（CI）。
WEB_DIR="${SMOKE_WEB_DIR:-}"
if [ -z "$WEB_DIR" ]; then
  for candidate in "$ROOT/../PayPrint-Frontend" "$ROOT/PayPrint-Frontend"; do
    if [ -f "$candidate/scripts/smoke.mjs" ]; then WEB_DIR="$candidate"; break; fi
  done
fi
if [ -z "$WEB_DIR" ] || [ ! -f "$WEB_DIR/scripts/smoke.mjs" ]; then
  echo 'error: 找不到前端仓库（PayPrint-Frontend）。' >&2
  echo '       克隆它到本仓库旁边，或用 SMOKE_WEB_DIR 指向其检出目录。' >&2
  exit 1
fi

# 用配置文件而不是环境变量：二进制只认 -config，文件是进程级配置的唯一来源。
# worker 在这里是开着的（间隔调短），否则冒烟脚本里那条真实打印链路走不通：
# Node 脚本会起一台假 IPP 打印机、通过运维接口登记它，然后等订单变成 completed，
# 只有 IPP 投递与作业状态回查都正常，这一步才会成功。
cat > "$CONF" <<EOF
[server]
env = test
http_addr = 127.0.0.1:$PORT
log_level = warn

[storage]
db_path = $RUN_DIR/smoke.db
storage_dir = $RUN_DIR/files

[security]
admin_token = $ADMIN_TOKEN

[worker]
enabled = true
interval = 500ms
EOF

echo "[smoke] 在 127.0.0.1:$PORT 启动服务 ..."
"$EXE" -config "$CONF" >"$STDOUT_LOG" 2>"$STDERR_LOG" &
SERVER_PID=$!

ready=0
i=0
while [ "$i" -lt 60 ]; do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    break
  fi
  if curl -fsS --max-time 2 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  i=$((i + 1))
  sleep 0.4
done

if [ "$ready" -eq 0 ]; then
  echo '[smoke] 服务没有就绪；服务端日志末尾：' >&2
  tail -n 20 "$STDERR_LOG" >&2 2>/dev/null || true
  tail -n 20 "$STDOUT_LOG" >&2 2>/dev/null || true
  exit 1
fi

# 通过运行时参数接口把作业状态回查调快（下限 2s）。
# 这一步顺带验证了「面板里改的参数不用重启就生效」。
curl -fsS -X PUT "http://127.0.0.1:$PORT/api/v1/admin/settings" \
  -H "X-Admin-Token: $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"set":{"ipp_poll_interval":"2s"}}' >/dev/null

echo '[smoke] 跑 Node 端到端脚本 ...'
rc=0
(
  cd "$WEB_DIR"
  SMOKE_BASE="http://127.0.0.1:$PORT" SMOKE_ADMIN_TOKEN="$ADMIN_TOKEN" \
    SMOKE_NO_WEB=1 node scripts/smoke.mjs
) || rc=$?

if [ "$rc" -ne 0 ]; then
  echo ''
  echo '[smoke] --- 服务端日志（末尾 30 行）---' >&2
  tail -n 30 "$STDOUT_LOG" >&2 2>/dev/null || true
fi

exit "$rc"
