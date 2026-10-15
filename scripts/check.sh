#!/usr/bin/env sh
#
# payprint: 跑完所有本地检查，格式、vet、单元 + 集成测试，
# 最后做一次单二进制构建（构建会复用或自动下载前端产物）。
#
# 这是"提交前跑一遍"的那个命令，与 CI 的检查项一致。
#
# 依赖：Go 1.27+。首次构建需要网络下载 PayPrint-Frontend 的 dist 产物
# （或用 --web-dist 指定本地产物）。交叉编译目标平台照常由 GOOS/GOARCH 控制。
#
# 用法：sh scripts/check.sh

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
ROOT="$(dirname -- "$SCRIPT_DIR")"
FAILED=""

echo ''
echo '=== gofmt（server）==='
cd "$ROOT/server"
UNFORMATTED="$(gofmt -l ./cmd ./internal)"
if [ -n "$UNFORMATTED" ]; then
  echo '重新格式化的文件：'
  printf '  %s\n' $UNFORMATTED
  gofmt -w ./cmd ./internal
else
  echo '所有文件格式正确'
fi

echo ''
echo '=== go vet ==='
go vet ./... || FAILED="$FAILED go-vet"

echo ''
echo '=== go test ==='
go test ./... || FAILED="$FAILED go-test"

echo ''
echo '=== 单二进制构建（前端产物嵌入 + go build）==='
sh "$SCRIPT_DIR/build.sh" -o "$ROOT/dist/payprint-server" || FAILED="$FAILED build"

echo ''
if [ -n "$FAILED" ]; then
  echo "失败：$FAILED"
  exit 1
fi
echo '全部检查通过'
