# PayPrint: 常用开发命令的薄封装。
#
# 真正的逻辑都在 scripts/*.sh 里（前端已拆到 PayPrint-Frontend 仓库，这里只管后端），
# 这里只是一层入口，顺手把本机 / 交叉编译的常用组合固定下来。不想用 make 就直接读那两个脚本。
#
# 脚本统一用 `sh` 调用而不是直接执行：Windows 下检出的仓库没有可执行位，
# 用解释器调用在哪都不会踩到这个坑。
#
# Windows 用户请改用 scripts\*.ps1（见 docs/DEVELOPMENT.md）。

SHELL   := /bin/sh

BUILD   := sh scripts/build.sh
CHECK   := sh scripts/check.sh
SMOKE   := sh scripts/smoke.sh
WEB     := sh scripts/web.sh

VERSION ?= dev
# amd64 的微架构基线：v1 兼容所有 x86-64 机器，v3 更快但上不了老 CPU。
GOAMD64 ?= v1

.DEFAULT_GOAL := help

.PHONY: help build web run dev fmt vet test check smoke clean \
        release-linux-amd64 release-linux-arm64 release-linux-armv7 release-windows-amd64

help: ## 显示本帮助
	@printf 'PayPrint\n\n可用目标：\n'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  \033[36m%-26s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## 构建单二进制（前端产物自动取自 PayPrint-Frontend Release），产物在 dist/
	$(BUILD) --version $(VERSION)

web: ## 下载最新前端产物到嵌入目录（已存在则跳过，FORCE=1 强制重下）
	$(WEB) $(if $(FORCE),--force)

run: ## 跑刚构建出来的二进制（首次启动自动生成 payprint.conf 与 data/）
	./dist/payprint-server

dev: ## 后端热重载（air；没有就先 go install github.com/air-verse/air@latest）
	@if command -v air >/dev/null 2>&1; then air -c .air.toml; \
	elif [ -x ../.tools/bin/air ]; then ../.tools/bin/air -c .air.toml; \
	elif [ -x ../.tools/bin/air.exe ]; then ../.tools/bin/air.exe -c .air.toml; \
	else echo 'air 不在 PATH：先安装（go install github.com/air-verse/air@latest）或激活 ../.tools 工具链' >&2; exit 1; fi

fmt: ## 用 gofmt 就地格式化后端代码
	cd server && gofmt -w ./cmd ./internal

vet: ## go vet
	cd server && go vet ./...

test: ## 跑后端测试
	cd server && go test ./...

check: ## 格式 + vet + 测试 + 单二进制构建（提交前跑这个）
	$(CHECK)

smoke: ## 端到端冒烟：真实签名 + 真实 HTTP + 假 IPP 打印机，跑完整打印链路（需要 PayPrint-Frontend 检出）
	$(SMOKE)

clean: ## 删掉构建产物（dist/ 与 air 的 tmp/，不影响嵌入目录里的占位文件）
	rm -rf dist tmp

# ---- 交叉编译 ---------------------------------------------------------------
# 前端产物只需要取一次：这些目标先跑 web，再让 build 复用嵌入目录里的产物。

release-linux-amd64: web ## 交叉编译到 linux/amd64
	GOAMD64=$(GOAMD64) GOOS=linux GOARCH=amd64 $(BUILD) --version $(VERSION) -o dist/payprint-server-linux-amd64

release-linux-arm64: web ## 交叉编译到 linux/arm64（aarch64）
	GOOS=linux GOARCH=arm64 $(BUILD) --version $(VERSION) -o dist/payprint-server-linux-arm64

release-linux-armv7: web ## 交叉编译到 linux/arm/v7（32 位 ARM）
	GOOS=linux GOARCH=arm GOARM=7 $(BUILD) --version $(VERSION) -o dist/payprint-server-linux-armv7

release-windows-amd64: web ## 交叉编译到 windows/amd64
	GOAMD64=$(GOAMD64) GOOS=windows GOARCH=amd64 $(BUILD) --version $(VERSION) -o dist/payprint-server-windows-amd64.exe
