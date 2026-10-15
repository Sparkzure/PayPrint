# Agent Instructions（AI 协作约定）

本文件同时约束人类贡献者与 AI 助手。人类贡献者的完整流程见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## Issue

创建 issue 前先看 `.github/ISSUE_TEMPLATE/` 里的模板：

- 选最合适的模板，按模板结构填写全部必填项。
- 模板标注"可选"的段落，不适用就删掉。
- 不要编造复现步骤、日志、截图或预期行为。

**安全问题不开 issue**，按 [SECURITY.md](SECURITY.md) 的渠道私下报告。

## Pull Request

创建 PR 前先读 `.github/PULL_REQUEST_TEMPLATE.md`：

- 按模板结构填写，不适用的小节删掉。
- 标题用 Conventional Commits（见下），PR 标题校验工作流会检查。
- 写清验证证据：实际执行过哪些检查、手工验证了什么；没跑就说明原因。
- 不要声称没做过的验证、审查或测试。

## Git 提交

- 标题格式 `type(scope): subject`，祈使句，描述真实改动。
- 允许的 type：`feat` `fix` `refactor` `perf` `docs` `style` `test` `build` `ci` `chore` `revert`。
- scope 必填，用主要模块或功能名（例如 `server`、`web`、`docs`、`ci`、`deploy`）；
  跨多个模块时用主要的那个，并在正文里枚举其余。
- 破坏性变更在 type 后加 `!`，例如 `feat(server)!: 调整订单状态机`。
- 正文用简短的 Markdown 列表，一条说一件事。
- 不要写 diff 里不存在的改动，也不要描述没有发生的重构、修复或测试。

AI 辅助的提交最多带一条 `Co-authored-by`，且必须对应实际使用的助手，例如：

- `Co-authored-by: Claude <81847+claude@users.noreply.github.com>`

不是上面列出的助手就不要加，也**不要编造**一个；拿不准就请人类协作者提供确切的署名行。

## 全自动贡献

全自动提交不算正常的社区参与。若提交来自自动化代理，或提交账号主要通过自动化代理参与
项目讨论而没有实质的人工复核，维护者可以不接受该贡献，或要求改为人工复核后再提交。
维护者保留独立接受、拒绝、修改或重写任何贡献的权利；接受某个贡献不代表接受提交方式。

## 本项目的硬约束（改代码前必读）

完整版在 [CONTRIBUTING.md](CONTRIBUTING.md) 与 [`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md)
（含「安全红线」一节）。AI 最容易踩的几条：

- **提交前必须跑自检**：`scripts\check.ps1`（或 `make check`）。改了订单、支付、打印任务或
  设备签名的代码，再加 `scripts\smoke.ps1`（或 `make smoke`）：跨语言签名与"一个订单只出
  一次纸"这类不变量只有冒烟能验。
- **交付物默认是单个自包含二进制**：不引入运行时资源目录与环境变量配置；Docker 只是它的
  运行封装（见 `Dockerfile`），不改变配置模型。新增进程级配置项要加进 `config.Template()`
  （默认值的唯一定义处）。
- **文档是中文**：行为或接口变了要同步 `README.md` / `docs/API.md` / `docs/DEPLOY.md` /
  `docs/DEVELOPMENT.md`；用户可见文案（配置注释、面板标签、日志）也算实现的一部分。
- **不要为了"简化"去掉安全校验**：设备验签、金额整数分比对、归属校验、在途任务不可回退、
  到账候选不唯一不放行。
- 生成代码 `server/internal/store/` 不要手改；迁移 SQL 的注释必须 ASCII；Go 文件名避开
  `_android.go` 这类 GOOS/GOARCH 后缀；`scripts/*.ps1` 保持纯 ASCII。
- **代码注释极简（2026-10-13 用户确认）**：不写 doc comment 与解说性注释，设计取舍与安全
  红线的理由写在文档里（`docs/DEVELOPMENT.md` 的「安全红线」、`docs/API.md` 的契约说明）。
  代码里只保留 `//go:` 等指令注释；`internal/store/` 的注释随 SQL 源走。
- **构建与部署描述只讲目标平台与架构**（`linux/arm64（aarch64）`），不点名具体硬件；
  部署目标叫"目标机器"，"设备"在本项目里专指绑定了指纹与公钥的那台客户端。
