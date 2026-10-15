#!/usr/bin/env bash
#
# payprint: install the service into systemd so it starts at every boot.
#
# Run this ON the target machine (Linux + systemd), as root:
#
#   sudo bash install-service.sh --binary ./payprint-server --unit ./payprint.service
#
# Both sources have defaults for a repo checkout, so the short form works too:
#
#   sudo ./scripts/install-service.sh
#
# Preview everything without touching the machine (works anywhere, no root, no systemd):
#
#   bash scripts/install-service.sh --dry-run
#
# What it does, in order:
#   1. creates the system user + group named in the unit (no shell, no login)
#   2. installs the binary to the path in the unit's ExecStart
#   3. installs the unit to /etc/systemd/system/payprint.service
#   4. systemctl daemon-reload, enable, restart  ->  runs now, and on every boot
#   5. waits for /healthz and prints a summary
#
# Re-running it is an upgrade: the binary is replaced while payprint.conf (which holds
# your admin token) and data/ (database + uploads) are left alone.
#
# There is deliberately NO unit content in this file: deploy/systemd/payprint.service
# is the single source of truth, and the values the installer needs (user, paths) are
# read back from it, so the two can never drift apart.
#
# This runs on the device (Linux + systemd), not on Windows. It tolerates CRLF in the
# files it reads (a unit file that travelled through a Windows editor is common).

set -euo pipefail

SERVICE="payprint"
UNIT_DEST="/etc/systemd/system/${SERVICE}.service"
HEALTH_TIMEOUT=15

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname -- "$SCRIPT_DIR")"

BINARY_SRC=""
UNIT_SRC=""
DRY_RUN=0
UNINSTALL=0

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
step() { printf '\n== %s\n' "$*"; }

# run CMD...  print the command, then run it (unless --dry-run).
run() {
  printf '   $'
  printf ' %q' "$@"
  printf '\n'
  if [ "$DRY_RUN" -eq 1 ]; then return 0; fi
  "$@"
}

usage() {
  printf 'payprint: 把服务装进 systemd，并设为开机自启（需要 Linux + systemd + root）\n\n'
  printf '  用法：sudo bash install-service.sh [选项]\n\n'
  printf 'Options:\n'
  printf '  --binary PATH   binary to install (default: dist/... then ./payprint-server)\n'
  printf '  --unit PATH     unit file      (default: deploy/systemd/%s.service)\n' "$SERVICE"
  printf '  --dry-run       print what would happen, change nothing (no root needed)\n'
  printf '  --uninstall     disable the service and remove the unit file\n'
  printf '  -h, --help      this text\n'
}

# unit_value KEY  read KEY=... from the unit file (commented lines do not match).
# tr -d '\r' because the file may have come through a Windows editor: without it
# User=payprint would read as "payprint\r" and we would try to create that user.
unit_value() {
  sed -n "s/^$1=//p" "$UNIT_SRC" | head -n 1 | tr -d '\r'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --binary) [ $# -ge 2 ] || die "--binary needs a path"; BINARY_SRC="$2"; shift 2 ;;
    --unit)   [ $# -ge 2 ] || die "--unit needs a path";   UNIT_SRC="$2";   shift 2 ;;
    --dry-run)   DRY_RUN=1;   shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    -h|--help)   usage; exit 0 ;;
    *) die "unknown option: $1 (try --help)" ;;
  esac
done

# ---- locate the two inputs -------------------------------------------------
if [ -z "$UNIT_SRC" ]; then
  for candidate in \
    "$REPO_DIR/deploy/systemd/${SERVICE}.service" \
    "$SCRIPT_DIR/${SERVICE}.service" \
    "./${SERVICE}.service"
  do
    if [ -f "$candidate" ]; then UNIT_SRC="$candidate"; break; fi
  done
fi
[ -n "$UNIT_SRC" ] && [ -f "$UNIT_SRC" ] || die \
  "找不到单元文件：把 deploy/systemd/${SERVICE}.service 与脚本放在一起，或用 --unit 指定"

if [ "$UNINSTALL" -eq 0 ]; then
  if [ -z "$BINARY_SRC" ]; then
    for candidate in \
      "$REPO_DIR/dist/payprint-server" \
      "$SCRIPT_DIR/payprint-server" \
      "./payprint-server"
    do
      if [ -f "$candidate" ]; then BINARY_SRC="$candidate"; break; fi
    done
  fi
  [ -n "$BINARY_SRC" ] && [ -f "$BINARY_SRC" ] || die \
    "找不到二进制：用 --binary 指定（例如 --binary /tmp/payprint-server-linux-arm64）"
fi

# ---- read the layout back out of the unit ----------------------------------
# 服务以哪个用户跑、装到哪个目录、哪里必须可写，全都以单元文件为准：
# 安装脚本自己再写一遍这些值，迟早会和单元文件对不上。
SVC_USER="$(unit_value User)";             SVC_USER="${SVC_USER:-payprint}"
BIN_DEST="$(unit_value ExecStart)"
[ -n "$BIN_DEST" ] || die "单元文件里没有 ExecStart=$BIN_DEST"
BIN_DEST="${BIN_DEST%% *}"                 # 去掉可能的参数（例如 -config ...）
DEPLOY_DIR="$(dirname -- "$BIN_DEST")"
CONF_FILE="$DEPLOY_DIR/payprint.conf"
READ_WRITE="$(unit_value ReadWritePaths)"

# ---- sanity: the unit's assumptions must be satisfiable --------------------
if [ "$UNINSTALL" -eq 0 ] && [ -n "$READ_WRITE" ]; then
  case " $READ_WRITE " in
    *" $DEPLOY_DIR "*|*" $DEPLOY_DIR"*) ;;
    *) printf 'warning: 单元文件的 ReadWritePaths 不含 %s，服务将无法生成 payprint.conf 与 data/\n' \
         "$DEPLOY_DIR" >&2 ;;
  esac
fi

if [ "$UNINSTALL" -eq 1 ]; then
  step "卸载 $SERVICE（保留数据）"
else
  # 需要 root 才能写 /opt 与 /etc/systemd/system；--dry-run 只读，不要求。
  if [ "$DRY_RUN" -eq 0 ] && [ "$(id -u)" -ne 0 ]; then
    die "需要 root：sudo bash $0 --binary $BINARY_SRC --unit $UNIT_SRC"
  fi
  if [ "$DRY_RUN" -eq 0 ]; then
    command -v systemctl >/dev/null 2>&1 || die "这台机器上没有 systemctl（本脚本只支持 systemd）"
  fi
  step "安装 $SERVICE"
  printf '   二进制   %s\n' "$BINARY_SRC"
  printf '   单元文件 %s\n' "$UNIT_SRC"
  printf '   安装到   %s（以 %s 身份运行）\n' "$DEPLOY_DIR" "$SVC_USER"
  if [ -f "$CONF_FILE" ]; then
    printf '   配置     %s 已存在，保留不动\n' "$CONF_FILE"
  else
    printf '   配置     首次启动时在 %s 旁边生成\n' "$DEPLOY_DIR"
  fi
fi

# ---- 1. user + group -------------------------------------------------------
if [ "$UNINSTALL" -eq 0 ]; then
  step "1/5 系统账号"
  if command -v getent >/dev/null 2>&1 && getent group "$SVC_USER" >/dev/null 2>&1; then
    printf '   组 %s 已存在\n' "$SVC_USER"
  else
    run groupadd -r "$SVC_USER"
  fi
  if id -u "$SVC_USER" >/dev/null 2>&1; then
    printf '   用户 %s 已存在\n' "$SVC_USER"
  else
    run useradd -r -g "$SVC_USER" -d "$DEPLOY_DIR" -s /usr/sbin/nologin "$SVC_USER"
  fi
  run install -d -o "$SVC_USER" -g "$SVC_USER" -m 0755 "$DEPLOY_DIR"
fi

# ---- 2. binary -------------------------------------------------------------
if [ "$UNINSTALL" -eq 0 ]; then
  step "2/5 二进制"
  # 先停服务：正在运行的二进制在 Linux 上换不掉（ETXTBSY）。首次安装时服务还不存在，
  # 这条会失败，忽略即可。
  run systemctl stop "$SERVICE" 2>/dev/null || true
  # 二进制可能已经在目标位置（先手工拷进去再装服务是常见顺序），这时跳过复制、
  # 只把属主与权限校正；否则 install 会以 "are the same file" 失败。
  src_real="$(readlink -f -- "$BINARY_SRC" 2>/dev/null || true)"
  dst_real="$(readlink -f -- "$BIN_DEST" 2>/dev/null || true)"
  if [ -n "$src_real" ] && [ "$src_real" = "$dst_real" ]; then
    printf '   已经在 %s，跳过复制\n' "$BIN_DEST"
    run chown "$SVC_USER:$SVC_USER" "$BIN_DEST"
    run chmod 0755 "$BIN_DEST"
  else
    run install -m 0755 -o "$SVC_USER" -g "$SVC_USER" "$BINARY_SRC" "$BIN_DEST"
  fi
fi

if [ "$UNINSTALL" -eq 1 ]; then
  # 顺序要紧：先 disable --now（它既停服务又删掉开机自启的软链），此时单元文件还在，
  # systemd 才认得它；删文件放在后面。
  step "1/2 systemd"
  if [ "$DRY_RUN" -eq 0 ] && ! systemctl cat "$SERVICE" >/dev/null 2>&1; then
    printf '   服务没有装过，跳过 disable\n'
  else
    run systemctl disable --now "$SERVICE"
  fi
  step "2/2 单元文件"
  run rm -f "$UNIT_DEST"
  run systemctl daemon-reload
  printf '\n已卸载。%s 里的配置与数据会保留，确认不再需要后自行删除。\n' "$DEPLOY_DIR"
  exit 0
fi

# ---- 3. unit file ----------------------------------------------------------
step "3/5 单元文件"
run install -m 0644 "$UNIT_SRC" "$UNIT_DEST"

# ---- 4. enable -------------------------------------------------------------
# enable 建立开机自启的软链（WantedBy=multi-user.target），restart 让新二进制立刻生效。
step "4/5 systemd"
run systemctl daemon-reload
run systemctl enable "$SERVICE"
run systemctl restart "$SERVICE"

# ---- 5. verify -------------------------------------------------------------
step "5/5 验证"

if [ "$DRY_RUN" -eq 1 ]; then
  printf '   (dry-run：没有真的启用服务，也没有等 /healthz)\n'
  printf '\n--dry-run 结束。去掉这个参数就会真的执行上面的命令。\n'
  exit 0
fi

# 监听地址从配置里读（可能有人把端口改过），读不到就按默认的 8080 试。
CONF_ADDR="$(sed -n 's/^[[:space:]]*http_addr[[:space:]]*=[[:space:]]*//p' "$CONF_FILE" 2>/dev/null |
  head -n 1 | tr -d '\r' | tr -d '[:space:]')"
case "$CONF_ADDR" in
  :*) CONF_ADDR="127.0.0.1${CONF_ADDR}" ;;
  0.0.0.0*) CONF_ADDR="127.0.0.1${CONF_ADDR#0.0.0.0}" ;;
  "") CONF_ADDR="127.0.0.1:8080" ;;
esac

printf '   systemctl is-enabled -> %s\n' "$(systemctl is-enabled "$SERVICE" 2>&1 || true)"
printf '   systemctl is-active  -> %s\n' "$(systemctl is-active "$SERVICE" 2>&1 || true)"

healthy=0
if command -v curl >/dev/null 2>&1; then
  i=0
  while [ "$i" -lt "$HEALTH_TIMEOUT" ]; do
    if curl -fsS --max-time 2 "http://${CONF_ADDR}/healthz" >/dev/null 2>&1; then
      healthy=1
      break
    fi
    i=$((i + 1))
    sleep 1
  done
  if [ "$healthy" -eq 1 ]; then
    printf '   /healthz            -> ok（http://%s/healthz）\n' "$CONF_ADDR"
  else
    printf '   /healthz            -> 还没就绪，看日志：journalctl -u %s -n 50\n' "$SERVICE"
  fi
else
  printf '   没有 curl，跳过 /healthz 检查\n'
fi

# 运维令牌是命令行通道的钥匙，空着就提一句（面板本身不受影响）。
if grep -qE '^[[:space:]]*admin_token[[:space:]]*=[[:space:]]*$' "$CONF_FILE" 2>/dev/null; then
  printf '\n提示：%s 里的 admin_token 还是空的，命令行运维接口处于关闭状态。\n' "$CONF_FILE"
  printf '      不需要就忽略；需要就填一个（openssl rand -base64 32）再 systemctl restart %s。\n' "$SERVICE"
fi

printf '\n完成。服务已设为开机自启，崩溃会自动拉起。\n'
printf '日志：journalctl -u %s -f\n' "$SERVICE"
