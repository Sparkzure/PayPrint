# 贡献指南

感谢愿意花时间改进本项目。本文档说明怎么把环境跑起来、改动要满足什么要求，
以及提交 PR 前必须自己过一遍哪些检查。

- 参与本项目即表示你同意 [行为准则](CODE_OF_CONDUCT.md)。
- 本项目以 [AGPL-3.0](LICENSE) 发布。提交贡献即表示你有权以同一许可证提供这些代码。
- **发现安全问题不要开 issue**，请按 [SECURITY.md](SECURITY.md) 私下报告。

## 目录

- [开始之前](#开始之前)
- [环境准备](#环境准备)
- [构建与运行](#构建与运行)
- [提交前自检](#提交前自检)
- [代码约定](#代码约定)
- [文档](#文档)
- [提交与 PR](#提交与-pr)

## 开始之前

先读 [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md)。它记录了本项目的架构、目录结构、
关键设计约束（每条都对应代码里一个具体检查）与安全红线。**不了解这些约束就动手，
很容易"简化"掉一个本来在防事故的校验。**

改动前请确认自己要动的那块属于哪条约束，例如：

- 改打印放行 → 默认行为是「用户手动确认才出纸」，不要改回自动放行
- 改支付校验 → 金额一律整数分，客户端提交的金额永远不参与
- 改数据库 → 迁移文件必须用 ASCII 注释，生成代码 `server/internal/store/` 不要手改

## 环境准备

| 依赖 | 版本 | 说明 |
|---|---|---|
| Go | 1.27+ | 后端与 worker（前端已拆到 PayPrint-Frontend 仓库，本仓库不需要 Node） |
| Git | 任意近期版本 | — |

交叉编译不需要目标平台的编译器：`modernc.org/sqlite` 是纯 Go，`CGO_ENABLED=0` 即可。
端到端冒烟（`make smoke`）额外需要 [PayPrint-Frontend](https://github.com/Sparkzure/PayPrint-Frontend)
的检出与 Node 24+。

> **可选：项目内隔离的工具链。** 开发者本机若不想让 Go 缓存、npm 缓存写进用户目录，
> 可以自建一个 `.tools/` 目录放独立工具链并激活它（`scripts/*.ps1` 会自动识别并使用，
> 仓库旁或上一级都行）。
> 这是**个人环境偏好，不是本仓库的构建前提**：`.tools/` 不进仓库，缺少它时所有脚本
> 都会退回系统 PATH 上的工具。

## 构建与运行

构建产物是**一个自包含的二进制**：前端在构建时被 `//go:embed` 打进二进制，
运行时没有任何资源目录。

### 通用方式（Linux / macOS / Git Bash / WSL）

```bash
make build      # 取前端产物（自动下载）→ server/internal/webui/dist → go build，产物在 dist/
make run        # 直接跑刚构建出来的二进制（首启自动生成 payprint.conf 与 data/）
make dev        # 后端热重载（air）
```

`make` 只是对 `scripts/*.sh` 的薄封装，不想用 make 就直接跑脚本：

```bash
scripts/build.sh
scripts/check.sh
scripts/smoke.sh
```

### Windows（PowerShell）

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\check.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\smoke.ps1
```

两种方式做的事完全一样，选你顺手的那套。改后端想省掉重新构建流程时：

```bash
cd server && go run ./cmd/server    # 读的是同一个嵌入目录，前端产物需先取过一次
```

## 提交前自检

```bash
make check      # gofmt 检查 + go vet + go test + 单二进制构建（复用或自动下载前端产物）
make smoke      # 端到端：真实签名 + 真实 HTTP + 假 IPP 打印机，跑完整打印链路（需 PayPrint-Frontend 检出）
```

Windows 下等价于 `scripts\check.ps1` 与 `scripts\smoke.ps1`。

`smoke` 是唯一能发现「前后端各自正确、合起来验签失败」这类跨语言问题的检查，
也会断言「一个订单只出一次纸」这条不变量，**改了订单、支付、打印任务或签名相关的
代码，请务必跑一遍**。

## 代码约定

- **SQL 文件（`server/internal/database/**/*.sql`）必须用 ASCII 注释**：sqlc 的 SQLite
  parser 遇到中文注释会报语法错误，且报错位置具有误导性。
- **Go 文件名不要以 `_android.go` / `_linux.go` / `_arm64.go` 结尾**：这类后缀会被当作
  GOOS/GOARCH 构建约束，在其它平台上被**静默忽略**（症状是"标识符凭空 undefined"）。
- **`scripts/*.ps1` 必须保持纯 ASCII**：PowerShell 5.1 会把无 BOM 的 UTF-8 当 ANSI(GBK)
  读取，脚本里出现中文会直接解析失败。
- `server/internal/store/` 是 sqlc 生成代码，**不要手改**；改 schema 的流程见
  [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md#数据库)。
- **注释极简**：不写 doc comment 与解说性注释；设计取舍与校验理由写在文档里
  （`docs/DEVELOPMENT.md` 的「安全红线」、`docs/API.md` 的契约说明）。代码里只保留
  `//go:` 等指令注释；`server/internal/store/` 为 sqlc 生成代码，注释随 SQL 源走。
- 前端颜色只能取 Hope UI 主题令牌（前端仓 `src/app/theme.ts`），组件里不得出现十六进制色值；
  深色（黑蓝）/ 浅色（白蓝）两套主题都要验证。
- 改动前先读懂对应的安全校验（见 `docs/DEVELOPMENT.md` 的「安全红线」），
  不要为了「简化」去掉验签、金额比对、归属校验等约束。

## 文档

- 行为或接口有变化时**同步更新文档**：接口契约改 `docs/API.md`，
  部署与排障改 `docs/DEPLOY.md`，架构与开发流程改 `docs/DEVELOPMENT.md`，
  安装与使用改 `README.md`。
- 用户可见的文案（配置文件注释、管理面板标签与提示、启动日志、命令行帮助）
  属于实现的一部分，改了行为要顺手改它们。

## 提交与 PR

PR 与提交标题用 Conventional Commits：`type(scope): 摘要`。允许的 type：`feat` `fix`
`refactor` `perf` `docs` `style` `test` `build` `ci` `chore` `revert`；scope 必填，用主要
模块名（`server` / `web` / `docs` / `ci` / `deploy`），跨多个模块时用主要的那个并在正文
枚举；破坏性变更在 type 后加 `!`。校验工作流会对不合规的标题留言并标红，改对后提醒会自动收掉。

提交信息用祈使句、说清「改了什么」，必要时在正文写「为什么」。PR 按
[`.github/PULL_REQUEST_TEMPLATE.md`](.github/PULL_REQUEST_TEMPLATE.md) 的模板写，描述请包含：

1. **改了什么**，涉及用户可见行为时，说明前后的差异
2. **为什么**，关联的 issue 或背景
3. **怎么验证的**，跑了哪些检查，手工验证的步骤
4. **有哪些取舍**，特别是安全相关的地方，别让评审去猜

交付物默认是单个自包含二进制：不引入运行时资源目录与环境变量配置；Docker 只是这份二进制的
运行封装（Dockerfile 里跑的仍是同一套构建产物），不改变配置模型；
新增进程级配置项要加进 `config.Template()`（默认值的唯一定义处）。
