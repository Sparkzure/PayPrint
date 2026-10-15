<!--
PR 标题：
- 用 Conventional Commits：`type(scope): 摘要`
- 允许的 type：feat / fix / refactor / perf / docs / style / test / build / ci / chore / revert
- scope 必填（校验工作流会检查），跨多个模块用主要的那个并在正文枚举
- 破坏性变更在 type 后加 `!`，例如 `feat(server)!: 调整订单状态机`
-->

## 摘要

<!--
改了什么，为什么需要改。
涉及用户可见行为时，说明前后的差异；涉及配置、API、数据库结构或兼容性时明确指出。
-->

## 影响面

- [ ] 此 PR 包含破坏性变更
- [ ] 此 PR 修改了公开 API、配置项、数据库结构或迁移行为
- [ ] 此 PR 需要同步文档（`README.md` / `docs/*`）

## 关联 Issue

<!--
用 `Closes #123`、`Fixes #123` 或 `Relates to #123`。
没有关联 issue 就删掉本节。
-->

## 验证

<!--
写实际执行过的命令、平台与手工验证步骤。
如果未执行，请说明原因。不要声称没做过的验证。
-->

- [ ] `scripts\check.ps1`（或 `make check`）：格式 + vet + 全部测试 + 单二进制构建
- [ ] `scripts\smoke.ps1`（或 `make smoke`）：改了订单、支付、打印任务或设备签名时必跑
- [ ] 手工验证：

## 检查清单

- [ ] 我已读过 [CONTRIBUTING](https://github.com/Sparkzure/PayPrint/blob/main/CONTRIBUTING.md) 与 [AGENTS.md](https://github.com/Sparkzure/PayPrint/blob/main/AGENTS.md)
- [ ] 我没有去掉或放松 `docs/DEVELOPMENT.md`「安全红线」里的校验
- [ ] 相关文档已同步更新（接口契约 / 部署 / 架构 / 使用说明，按实际改动）
- [ ] 变更代码已按 `gofmt` 与项目格式化约定处理

## AI 使用声明

<!--
使用了实质性 AI 辅助请勾选并写明工具与范围。
拼写修正、自动补全、格式建议、文字润色这类轻微辅助无需披露。
不适用就删掉本节。故意隐瞒可能被视为信任与合规问题。
-->

- [ ] 此 PR 包含 AI 辅助内容

使用的工具与范围：
