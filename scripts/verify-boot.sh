#!/usr/bin/env sh
#
# payprint: 启动自检，一台打印机都没有时，服务也必须能正常起来。
#
# 为什么这不是冒烟测试的一部分：冒烟测试是带着一台可用的假打印机启动服务的，
# 所以它永远发现不了"启动路径偷偷依赖打印机"这类回归。这个脚本故意反过来：
# 一个打印机都没有、打印机地址指向一个死端口。
#
# 它钉住的是这几件事：
#   1. 交付模型本身，把一个空目录当作目标机器上的部署目录（只放二进制），**从另一个工作
#      目录**启动它，payprint.conf 与 data/ 必须出现在二进制旁边，而工作目录里什么都不许
#      多出来，也不许依赖源码树里的任何东西
#   2. 进程照常起来、/healthz 有应答（库已迁移、接口在服务）
#   3. 首次种下的默认打印机状态是 offline，而不是假装 online（我们根本没联系过打印机）
#   4. 探测一个死地址返回 reachable:false 加原因，而不是 5xx
#   5. 运行参数可以就地改动，且 /meta 立刻反映出来
#
# 依赖：Go 1.27+、curl。构建用 --no-web：启动自检只关心 API 与配置，不取前端产物。
#
# 用法：sh scripts/verify-boot.sh

set -eu

command -v curl >/dev/null 2>&1 || { echo 'error: 需要 curl' >&2; exit 1; }

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
ROOT="$(dirname -- "$SCRIPT_DIR")"
ADMIN_TOKEN="boot-check-token"
PORT=18099
BASE="http://127.0.0.1:$PORT"

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/payprint-boot.XXXXXX")"
SERVER_PID=""
GEN_PID=""

cleanup() {
  for pid in "$GEN_PID" "$SERVER_PID"; do
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT INT TERM

FAILED=0
check() { # check <名称> <0|1> <细节>
  if [ "$2" -eq 0 ]; then
    echo "  ok   $1"
  else
    echo "  FAIL $1 - $3"
    FAILED=$((FAILED + 1))
  fi
}

has() { # has <文本> <固定字符串>，0 表示命中
  case "$1" in
    *"$2"*) return 0 ;;
    *) return 1 ;;
  esac
}

echo '[boot] 构建单二进制 ...'
BIN="$WORK_DIR/payprint-server"
sh "$SCRIPT_DIR/build.sh" --no-web -o "$BIN"

# ---------------------------------------------------------------------------
# 1. 交付承诺：部署目录里只有二进制这一个文件。
#    从别的工作目录启动它，它必须在自己旁边长出 payprint.conf 与 data/，
#    并且不往那个工作目录里写任何东西。缺少配置文件永远不该是启动错误。
#
# 生成出来的文件里是内置默认值（端口 :8080），所以这里绑定端口可能失败，
# 无所谓：配置文件与数据库在监听器起来之前就已经建好了，本步只断言文件；
# 进程无论成败都会被杀掉。
# ---------------------------------------------------------------------------
GEN_DIR="$WORK_DIR/release"      # 相当于目标机器上的 /opt/payprint
CWD_DIR="$WORK_DIR/elsewhere"    # 故意换一个工作目录
mkdir -p "$GEN_DIR" "$CWD_DIR"
cp "$BIN" "$GEN_DIR/payprint-server"

# 被测进程用私有的 TMPDIR：config.DefaultPath() 把"进程临时目录之下"一律判为
# go run 的临时构建目录；当 TMPDIR 被隔离工具链重定向时，放在临时目录下的假部署
# 目录会被误判，配置就会落进工作目录而不是二进制旁边。
CHILD_TMP="$WORK_DIR/childtmp"
mkdir -p "$CHILD_TMP"

# 故意不给 -config：默认位置必须由二进制自身推导出来。
( cd "$CWD_DIR" && TMPDIR="$CHILD_TMP" TMP="$CHILD_TMP" exec "$GEN_DIR/payprint-server" ) \
  >"$WORK_DIR/gen.out.log" 2>"$WORK_DIR/gen.err.log" &
GEN_PID=$!
sleep 2
kill "$GEN_PID" 2>/dev/null || true
wait "$GEN_PID" 2>/dev/null || true
GEN_PID=""

GEN_CONF="$GEN_DIR/payprint.conf"
if [ -f "$GEN_CONF" ]; then
  check '首次运行会把 payprint.conf 写在二进制旁边' 0 ''
else
  check '首次运行会把 payprint.conf 写在二进制旁边' 1 "$GEN_CONF 不存在"
fi
if [ -f "$CWD_DIR/payprint.conf" ]; then
  check '工作目录保持干净（没有东西落进源码树）' 1 "$CWD_DIR 里出现了 payprint.conf"
else
  check '工作目录保持干净（没有东西落进源码树）' 0 ''
fi

# 生成出来的文件必须是文档里那份模板：它既是用户读的文档，也是默认值的唯一定义处。
# 改了 config.go 里的段名而没同步这里的期望，正是这一步要抓的漂移。
if [ -f "$GEN_CONF" ]; then
  GEN_TEXT="$(cat "$GEN_CONF")"
  MISSING=""
  for section in '[server]' '[storage]' '[security]' '[printer]' '[worker]' '[payment]' 'admin_token'; do
    has "$GEN_TEXT" "$section" || MISSING="$MISSING $section"
  done
  if [ -z "$MISSING" ]; then
    check '生成的文件就是文档里那份模板' 0 ''
  else
    check '生成的文件就是文档里那份模板' 1 "缺少：$MISSING"
  fi
else
  check '生成的文件就是文档里那份模板' 1 '没有配置文件可查'
fi

# 配置里的相对路径以配置文件所在目录为基准（同样不是工作目录），
# 这才使得部署目录是一个自包含、可整体搬走的单位。
if [ -d "$GEN_DIR/data" ]; then
  check 'data/ 也建在配置文件旁边' 0 ''
else
  check 'data/ 也建在配置文件旁边' 1 "$GEN_DIR/data 不存在"
fi

# ---------------------------------------------------------------------------
# 2. 真实运行：全新的库，打印机地址指向一个没人监听的端口。
# ---------------------------------------------------------------------------
CONF="$WORK_DIR/payprint.conf"
cat > "$CONF" <<EOF
[server]
env = boot-check
http_addr = :$PORT
log_level = info

[storage]
db_path = $WORK_DIR/boot.db
storage_dir = $WORK_DIR/files

[security]
admin_token = $ADMIN_TOKEN

[printer]
default_printer_uri = ipp://127.0.0.1:9/ipp/print
EOF

"$BIN" -config "$CONF" >"$WORK_DIR/server.out.log" 2>"$WORK_DIR/server.err.log" &
SERVER_PID=$!

HEALTH=''
i=0
while [ "$i" -lt 25 ]; do
  sleep 0.4
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
  HEALTH="$(curl -fsS --max-time 2 "$BASE/healthz" 2>/dev/null || true)"
  if has "$HEALTH" '"status":"ok"'; then break; fi
  i=$((i + 1))
done

if has "$HEALTH" '"status":"ok"'; then
  check '没有任何打印机也能起来并应答 /healthz' 0 ''
else
  check '没有任何打印机也能起来并应答 /healthz' 1 '服务没有就绪'
  echo '[boot] 服务端日志末尾：' >&2
  tail -n 20 "$WORK_DIR/server.out.log" >&2 2>/dev/null || true
  tail -n 20 "$WORK_DIR/server.err.log" >&2 2>/dev/null || true
  exit 1
fi

AUTH_HEADER="X-Admin-Token: $ADMIN_TOKEN"

PRINTERS="$(curl -fsS -H "$AUTH_HEADER" "$BASE/api/v1/admin/printers")"
if has "$PRINTERS" '"status":"offline"'; then
  check '种下的打印机是 offline，不假装 online' 0 ''
else
  check '种下的打印机是 offline，不假装 online' 1 "$PRINTERS"
fi

PROBE="$(curl -fsS -X POST -H "$AUTH_HEADER" -H 'Content-Type: application/json' \
  -d '{"cups_uri":"ipp://127.0.0.1:9/ipp/print"}' "$BASE/api/v1/admin/printers/probe")"
if has "$PROBE" '"reachable":false' && has "$PROBE" '"error":"'; then
  check '探测死地址返回"不可达 + 原因"而不是 5xx' 0 ''
else
  check '探测死地址返回"不可达 + 原因"而不是 5xx' 1 "$PROBE"
fi

curl -fsS -X PUT -H "$AUTH_HEADER" -H 'Content-Type: application/json' \
  -d '{"set":{"release_ttl":"17m"}}' "$BASE/api/v1/admin/settings" >/dev/null

META="$(curl -fsS "$BASE/api/v1/meta")"
# 17m == 1020s：数值直接写在断言里，避免两边各算一遍。
if has "$META" '"release_ttl_seconds":1020'; then
  check '运行参数就地改完立刻生效（/meta）' 0 ''
else
  check '运行参数就地改完立刻生效（/meta）' 1 "$META"
fi

echo ''
if [ "$FAILED" -gt 0 ]; then
  echo "[boot] $FAILED 项检查失败"
  exit 1
fi
echo '[boot] 全部检查通过'
