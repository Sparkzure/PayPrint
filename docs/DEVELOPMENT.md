# 开发说明

面向要改这个仓库的人。

**项目由两个仓库组成**：本仓库（`PayPrint`，Go 后端 + 内嵌前端产物的单二进制交付）与
前端仓库 `Sparkzure/PayPrint-Frontend`（React SPA，构建产物以 tar.gz 挂在它自己的
GitHub Release 上，本仓库构建时下载嵌入）。两者独立提交、独立发版；本地开发建议把
两个仓库并列检出（`PayPrint-Frontend` 放在本仓库旁边，冒烟测试会自动找到它）。

| 你想知道 | 看这里 |
|---|---|
| 怎么装、怎么用、有哪些配置 | [`../README.md`](../README.md) |
| 架构、本地开发、测试、打包、代码约定 | **本文** |
| 接口契约（签名算法、状态机、错误码全表） | [`API.md`](API.md) |
| 部署、打印机接入、备份、排障 | [`DEPLOY.md`](DEPLOY.md) |

## 技术栈与选型理由

| 层 | 选型 | 原因 |
|---|---|---|
| 后端 | Go 1.27 + chi | 单二进制、交叉编译简单；部署规模很小，不需要框架 |
| 数据库 | SQLite（`modernc.org/sqlite`，纯 Go）+ goose + sqlc | 纯 Go 避免 CGO 破坏多架构交叉编译；WAL + `busy_timeout` |
| 打印机交互 | IPP（`OpenPrinting/goipp`） | 直连支持 IPP/AirPrint 的网络打印机；打印机不支持网络时用 CUPS 网关转发。都不引入命令行依赖，不调用 `lp`/`lpr` |
| 前端 | SolidJS 1.9 + Hope UI（OpenList fork）+ Vite + TanStack Query for Solid | 与 OpenList 前端同栈同组件库；i18n zh/en 内置；主题由 Hope UI 双主题令牌驱动 |
| 签名 | ECDSA-P256 + SHA-256 | Android Keystore 与 WebCrypto 都能生成**不可导出**的私钥 |

后端与 worker 在**同一个进程**里：自助打印点的部署规模很小，单二进制少一个要运维的部件。
需要横向扩展时，任务表用 `ClaimJob`（带状态守卫的原子 UPDATE）做抢占，多实例是安全的，
只要让 API 实例把配置里的 `[worker] enabled` 设为 `false`。

那句话里的"安全"指的是**不会重复投递**，不是"不会互相等"：SQLite 是单写者数据库，WAL 允许
多个读者但只有一个写者，而认领任务、写状态、记账全是写。因此多实例共享同一把写锁与同一个
文件，写入压力上来之后表现为排队，而不是扩容。真要多实例，先把数据库换成支持多写者的引擎
（查询与迁移是 sqlc + goose 生成的，改动面落在 `internal/database` 与 `internal/store`）。

## 目录结构

```
server/
  cmd/server/            单可执行入口（HTTP + 后台循环）
  internal/config/       配置文件装载与校验；文件不存在时按内置模板生成
  internal/webui/        前端产物（//go:embed）+ 占位 dist/
  internal/database/     连接、PRAGMA、goose 迁移（内嵌进二进制）
  internal/database/migrations/   SQL 迁移（**必须 ASCII 注释**）
  internal/database/queries/      sqlc 查询定义
  internal/store/        sqlc 生成代码（不要手改）
  internal/domain/       状态枚举、打印参数、计价、错误码
  internal/auth/         密码散列、会话令牌、设备请求签名
  internal/ids/          ID 与随机串生成
  internal/pdfutil/      PDF 页数识别 + 图片→单页 PDF（零第三方依赖）
  internal/ipp/          IPP 客户端与参数映射（直连打印机或 CUPS 网关）
  internal/notifier/     支付触发源（可插拔）与官方查单
  internal/service/      业务逻辑与事务边界
  internal/service/collector.go  收款凭据的生命周期（独立于账号体系）
  internal/service/settings.go   运行时可改配置（原子快照，管理面板里改）
  internal/backup/               整站备份导出与还原（zip：库快照 + 上传文件）
  internal/httpapi/      路由、中间件、请求/响应编解码、内嵌前端的 SPA 托管
scripts/
  web.sh / web.ps1       下载前端产物到嵌入目录（从 PayPrint-Frontend 的 Release）
  build.sh / check.sh / smoke.sh / verify-boot.sh   POSIX 构建与自检（make 与 CI 用的就是这些）
  build.ps1 / check.ps1 / smoke.ps1 / verify-boot.ps1   Windows 等价脚本
  verify-isolation.ps1    仅本地：校验"工具链关在项目内"这个环境偏好
  install-service.sh      systemd 安装（bash，在目标机器上跑，见 DEPLOY.md）
deploy/                  部署产物：systemd 单元（装法写在单元文件头部）
docs/                    接口契约、部署、本文
.air.toml                后端热重载（air，`make dev`）
Makefile                 常用命令入口（薄封装 scripts/*.sh）
AGENTS.md                AI 协作与提交规范（Issue / PR / Conventional Commits）
Dockerfile / docker-compose.yml / entrypoint.sh   Docker 镜像（运行的就是那个单二进制）
.github/                 CI 与工作流（测试、发布说明、二进制与镜像发布）、Issue / PR 模板、FUNDING
renovate.json            依赖更新（Renovate）
LICENSE                  许可证全文
CONTRIBUTING.md          贡献指南（环境、构建、自检、PR 要求）
SECURITY.md              漏洞报告渠道与部署侧安全责任
CODE_OF_CONDUCT.md       行为准则
```

**仓库的构建入口是跨平台的**：`Makefile` + `scripts/*.sh`（Linux / macOS / Git Bash / WSL / CI），
`scripts/*.ps1` 是 Windows 上的等价物，两者做的事完全一样。两套脚本都只依赖 PATH 上的
`go`（不需要 Node：前端产物是取来的），本机若另外建了项目内隔离的 `.tools/` 工具链
（仓库旁或上一级都行），脚本会自动用它，但它**不是前提**。

**只有一个交付物**：`make build`（即 `scripts/build.sh` / `scripts/build.ps1`）产出单个二进制。前端不是运行时资源目录，
而是被 `//go:embed all:dist` 打进二进制（`server/internal/webui/dist/`，由 `scripts/web.sh`
从 PayPrint-Frontend 的最新 Release 下载、或用 `--web-dist` 指定本地产物，不进仓库）。
仓库里留了一个 `dist/.gitkeep`，好让"还没取前端产物"时的 `go build` / `go test` 也能通过，
不然 `go:embed` 会因为匹配不到文件而直接编译失败（得到的二进制没有页面，接口照常）。

## 关键设计约束

以下每一条都对应代码里的一个具体检查。改动前请先读懂它防的是什么。

### 1. 身份核心是账号，设备只是账号的附属物

**一切数据以账号为身份核心**：订单（`orders.user_id`）、到账（`payment_events.user_id`）、
订单审计（`order_audits.user_id`）都挂在账号上，授权也只看账号会话。设备**不参与**这些
路径：下单、取消、确认打印、到账上报、上传都只挂 `requireSession`（+ `requireConsumer`），
一处都不要求设备签名，为此删掉了 `requireDevice` 中间件。设备签名只剩注册与登录两道门。

设备在数据里只以两种形态出现，而且都不是其他功能的依赖：

- 会话上记一台机器（`sessions.device_id`）：登录态是"这台机器上的那条会话"，它随设备
  被停用 / 解绑而作废；
- 订单审计里的**快照**（`order_audits.device_label` / `device_fingerprint`）：写下单那一刻
  的机器信息，之后解绑、停用甚至删除设备都不会改动它（见 [`API.md`](API.md#管理员) 一节的审计日志接口）。

> 反过来说：**订单与到账不再引用设备**（迁移 00014 删掉了 `orders.device_id`，并把
> `payment_events.device_id` 换成 `user_id`）。曾经"必须是下单那台设备才能确认打印"的
> 限制随之取消：同一账号换一台设备也能确认自己的订单。

下面这条规则约束的是**设备绑定这个功能本身**，不是别的功能：

设备**在库期间**一个指纹只能绑一个**成员**账号：只要参与过任何成员账号的注册或登录，该设备
就不能再用于**注册**新账号，也不能绑到别的账号上。约束落在 `devices.fingerprint UNIQUE`，
用于兜底并发注册竞态（`INSERT` 冲突即判定设备已被占用，见 `internal/service/auth.go`）。

**解绑是唯一的出路，而且是彻底的**：`AdminReleaseDevice` 在一个事务里删掉该设备的会话、
清掉该指纹的重放账本，再把行退成 `status=released` 并**让出指纹**（改成 `released:<id>`），
归属与公钥固定一并清空。于是这台硬件下次注册或登录会被当成全新设备，换机 / 恢复出厂 /
二手转让都不再需要人肉通道。那一行保留下来只是"这台设备被释放过"的痕迹（原因在
`revoke_reason`、时间在 `revoked_at`），**它已经不被任何数据引用**，也不再是设备：按指纹
查不到、不在任何设备列表里，停用与恢复也会拒绝它。

**管理员账号不在这条规则里，也不占设备**：管理员走 `AdminLogin`（不建设备行、会话
`device_id` 为 NULL），而 `Register` 在角色是 `admin` 时同样**不创建**设备行。于是同一台
机器可以同时有"一个成员账号的绑定"和"任意多个管理员账号"，bootstrap 出来的管理员也不会
把自己的机器锁成"已使用"。

> 这条规则的由来（踩过一次）：bootstrap 管理员以前和别人一样绑设备，于是运维用来首次
> 注册的那台机器**永远**无法再注册成员账号（`device_already_used`），登录也会被
> `device_bound_to_other_account` 挡住。迁移 00011 把历史上被管理员占用的设备行解开归属，
> 迁移 00013 又按新语义让出了指纹（`status=released`，行本身保留为历史锚点）。改动这一带
> 时注意：`Register` / `Login` / `createDevice` / `AuthenticateDevice` 都建立在"按指纹
> 最多查到一行设备"的假设上，别让管理员重新走进去，也别让 `released` 的行被查到。

### 2. 设备信任根不是指纹

Android 10+ 拿不到 IMEI，`ANDROID_ID` 也可伪造，因此指纹**不可信**，只作为索引。
真正的信任根是设备侧**不可导出**的 ECDSA-P256 密钥对：

- Android：Keystore 内生成的密钥，`KeyPair.getPublic().getEncoded()` 作为公钥登记
- 浏览器：WebCrypto 生成的密钥，私钥以 `extractable: false` 存进 IndexedDB
  （前端仓库 `src/lib/device.ts`）

公钥在设备首次接触时固定（TOFU），之后**不可更换**；换密钥即判定为设备伪造并拒绝。
人工解绑是唯一例外，且解绑会**清空公钥固定并让出指纹**（否则恢复出厂的设备会永久变砖，
而换人用的二手机又永远只能登回旧账号）。

### 3. 注册 / 登录签名 + nonce 防重放

**签名只用在注册与登录**（`AuthenticateDevice` 的调用点）：那两道门在生产设备绑定，
必须证明请求来自持有设备私钥的那台机器。其余接口只认会话，不再要求签名（见第 1 条）。

签名明文见 [`API.md`](API.md#请求签名)：`method`、`path`、`query`、时间戳、`nonce`、
`body 摘要` 全部纳入签名，任何一项被篡改都会验签失败。

nonce 按**设备指纹**记账（`device_nonces.fingerprint`，不是 `device_id`），
**验签通过即消耗，与设备是否已登记、这次请求成功与否都无关**。这样"用户名被占用""密码
错误"这类失败请求也不会留下可重复放行的签名。nonce 只在验签通过后才写，避免攻击者用
垃圾请求灌满表。

### 4. 支付上报不可信

对外只开一个上报接口。**上报内容不作为信任根**，确认支付前必须全部通过：

```
幂等(event_id) → 会话有效 → 定位订单 + 归属授权（按 order.user_id）
→ 状态可支付 → 未超时 → 金额整数分比对（以本地订单为准）→ 渠道合法
```

- **金额一律整数分**，订单金额由服务端按打印机价目算出并固化，客户端提交的任何金额不参与。
- 全部通过后，**写事件、改订单、记账、建兜底查单任务在同一个事务内完成**
  （`internal/service/payment.go`）。注意是"记账"，**不是**建打印任务。

### 5. 支付确认不等于出纸

这是本项目的核心产品决策：

```
下单 ──▶ 待支付 ──(收到到账并通过校验链)──▶ 待确认打印
                                                │
                                用户手动确认打印 ┘
                                                ▼
                                      排队中 ──▶ 打印中 ──▶ 已完成
```

- 收到到账消息只把订单推进到 `awaiting_release`，此刻 `print_jobs` 里**没有这一行**。
- `OrderService.Release` 是唯一能创建打印任务的入口。它的校验链（顺序即优先级）：

  | 顺序 | 检查 | 失败返回 |
  |---|---|---|
  | 1 | 会话有效（中间件完成） | 401 |
  | 2 | 订单属于当前账号（查询内嵌 `user_id`，越权与不存在不可区分） | 404 `order_not_found` |
  | 3 | 已放行过 → 幂等返回（双击安全） | 200 |
  | 4 | 状态为 `awaiting_release` | 409 `order_not_releasable` |
  | 5 | 还在确认时限内，否则就地关闭为待退款 | 409 `release_window_closed` |
  | 6 | 打印机**此刻**真的可用（数据库状态 + 实时探测打印机） | 409 `printer_offline` |
  | 7 | 支付核验（配置了官方查单就同步查一次，结论矛盾直接拒绝并转异常待核） | 409 / 403 |

  **不校验设备**：身份是账号，同一账号换一台设备也能确认自己的订单（设备不是这条路径的
  依赖，见第 1 条约束）。

  第 6、7 步是**刻意保留**的性能开销：放行意味着真的要出纸，多花几百毫秒换一次权威确认
  是划算的。第 6 步失败**不改变订单状态**，用户可以在打印点恢复后重试。
- 并发安全靠三重防护：同事务内 `enqueue` + 带 `awaiting_release` 守卫的 `MarkOrderQueued`，
  外加 `print_jobs.order_id UNIQUE`。
- 超过「确认打印时限」（运行时配置 `release_ttl`，默认 `30m`）未确认 → 转 `refund_pending` 并告警，避免钱挂在账上而纸不出来。

### 6. Print-Job 只代表入队

`Print-Job` 成功只说明打印机（或 CUPS 网关）接受了任务，不代表已经出纸。因此：

- 投递走重试队列（`print_jobs`），失败按指数退避重试
- 周期性回查 `job-state`，直到进入终态。作业被打印机停住（`processing-stopped`，
  例如卡纸）**不是终态**，记 `interrupted` 并继续回查，等它自行恢复
- 进程在"已认领、未写终态"之间退出会留下 `submitting` 孤儿任务，由 `WORKER_LEASE`
  超时后自动回收重投（`RecoverOrphans`）。回收用的是 compare-and-swap，不会抢走
  慢但活着的 worker
- 任务终态与订单终态必须在**同一个事务**里落定，否则会留下"任务已失败、订单还在排队中"
  这种谁也接不下去的中间态
- **在途任务不能永远在途**：回查只能看见"打印机还回话"的任务，打印机断电、掉网，或者作业
  卡住不动（卡纸没人清）时它永远等不到终态，任务留在在途、订单留在"打印中"，钱收了纸没出来
  而界面上没有提示。`EscalateStuckJobs` 按 `stuck_job_ttl`（运行时配置，默认 1 小时）把这类
  任务转 `needs_review`、订单转待退款并打告警。判据是 `print_jobs.inflight_since`（进入当前
  在途状态的时刻），**不是** `updated_at`：后者每轮回查都会更新，看不出"没人联系得上打印机"

### 7. 触发源可插拔

"支付触发源"被抽象成 `notifier.Trigger` 接口：账号会话上报与收款凭据上报各是一个实现，
将来换一个上报渠道只需多注册一个实现，支付校验链完全不用动。

**兜底**：微信渠道会异步调官方查单接口二次核验（v2 XML + MD5 验签，真实现）。
未配置商户信息时使用 `NopQuerier`，它明确返回 `unknown` 而不是假装成功，
绝不会把"没查过"当成"已确认"。

### 8. 三条身份线：用户 / 管理员 / 收款

这是本项目的骨架，三者互不相干，任何改动都不能让它们互相借道。

| | 用户 | 管理员 | 收款端 |
|---|---|---|---|
| 身份 | 账号（设备是它的附属物） | 账号（`role=admin`） | 打印机级 API 凭据 |
| 鉴权 | 会话；设备签名只用于注册 / 登录 | 管理员会话，或 `X-Admin-Token` | `X-Collector-Token` |
| 受设备限制 | 登录态绑设备（会话属于某台机器） | **否**（会话的 device_id 为空） | 不涉及 |
| 能做 | 下单、上传、确认打印 | 管账号 / 设备 / 打印机 / 订单 / 凭据 / 审计日志 | 只上报到账 |
| 不能做 | 管理 | **打印**（`admin_cannot_consume`） | 其它一切 |

**用户线**：账号是身份核心，订单、到账、审计都挂在 `user_id` 上（约束 1）。设备签名只
出现在注册与登录：那两道门在生产"这台设备属于哪个账号"。普通用户的会话**必须**绑设备，
`AuthenticateSession` 会显式拒绝"没有设备的普通用户会话"：登录态就是这台机器上的凭据，
停用 / 解绑设备时它会随之作废。

**管理线**：管理员登录**不受设备限制**（`sessions.device_id` 为空），因为人是会换机器的，
而管理动作与打印无关。代价是少了设备密钥这一重因素，所以管理密码必须足够强、管理面只在
局域网可达。管理员**不能打印**：消费端整组对 `role=admin` 返回 403，这层挡的不是攻击
（它本来就持有合法凭证），而是"管理密码泄露即免费用纸"这条升级路径。

**收款线**：收款端**不在用户体系里**（见 `collector_credentials` 与 `CollectorService`）：
没有账号、没有密码、没有设备绑定，只有一份绑定打印机的凭据。它只能调一个接口，
所以"一个用来收钱的身份同时能下单"这种误用从结构上就不存在。

微信收款通知里**没有我们的订单号**，因此收款凭据可以只报金额与时间，由服务端在**该凭据
绑定的打印机**内按「金额完全相等 + 时间窗」匹配。匹配刻意保守，**候选必须唯一**：

| 候选数 | 结果 |
|---|---|
| 1 | 进入后续校验链 |
| 0 | `payment_unmatched`；若同金额订单只是超时了，则 `order_expired` 并指认订单号 |
| ≥2 | `payment_ambiguous`，转人工 |

宁可不放行也不猜：判错意味着 A 付的钱印了 B 的文件。

### 9. 运行时可改的配置是一份不可变快照

`auto_release_on_payment`、各类时限、投递超时这些"现场要按实际情况调"的参数存在
`app_settings` 表里，可以在管理面板改并且**立刻生效**。

实现上有几个不能省的点：

- 生效值**不是**原地改 `*config.Config`：那是个被多处并发读的结构体，原地写会产生数据竞争
  （race detector 会报；32 位平台上 int64 还可能读到半个字）。它是一份
  `atomic.Pointer[RuntimeSettings]`，读的人拿整份快照、写的人整份替换，
  读路径无锁且永远自洽，也不会出现"A 服务用旧值、B 服务用新值"。
- **只存被覆盖的键**：没动过的项继续跟随内置默认值，所以"这个值到底哪来的"永远清楚，
  加一项新配置也不需要数据迁移。
- 库里有**不认识的键**或**非法值**时只记日志、回退默认，**绝不阻断启动**：
  新版本写过、又回退到旧版本是真实会发生的。
- 修改是**整组提交**：任何一项不合法就全部拒绝，不留半套配置（否则面板会显示"一半生效"）。
- 后台循环的间隔用函数而不是定值传入，**每轮重新求值**，否则"改了回查间隔"要等重启才生效。
- `New` 里先装一份内置默认值的快照，再去读库：这样"还没 Reload"的路径不会拿到
  TTL=0 这种危险默认值。

### 10. 启动与打印机解耦

打印机还没接上、地址还没定，服务也必须能正常起来：账号、上传、下单、收款、管理面板都与
打印机无关，**只有"出纸"这一步**依赖它。

- 首次启动种下的默认打印机状态是 `offline`，而且**不探测**。我们根本没联系过打印机，
  声称"在线"既是骗界面，也会让用户走到最后一步才被拒。
- `Bootstrap` 失败只记日志，**不中断启动**，它只是给运维一个能配的起点，
  不该决定整个服务能不能起来。
- 打印机地址是**数据**不是配置：在管理面板里改（消费端不回显这个字段），改完点「测试连接」
  当场验证。探测失败返回 `reachable: false` 加原因，而不是 5xx，运维要能把
  "地址写错"和"打印机没开 / 网关没起 / 网络不通"分开。

### 11. 还原备份必须"暂存 + 重启应用"，不能原地换库

备份是一个 zip：数据库一致性快照（`VACUUM INTO`）+ 上传文件。导出随时可做；
**还原**是覆盖性操作，它只能在进程启动、打开数据库**之前**完成换装。

- 原地换库做不到安全：连接池里全是打开的句柄，worker 正在写，硬换等于让一半请求
  读旧库、一半读新库，而且 SQLite 的 WAL/SHM 属于旧库，套在新库上会直接损坏数据。
- 所以流程是：上传 → 校验（格式、清单、**结构版本不能比当前服务新**）→ 解包进暂存区 →
  服务优雅退出 → systemd 拉起 → 启动第一步 `ApplyStagedRestore`。
- 换下的旧数据保留为 `*.pre-restore`：还原错了还能手工退回去。
- 校验错误（不是 zip、缺清单、结构更新）必须返回 **400** 而不是 500：
  "你传的不是备份"是调用方的错，靠 `backup.ErrInvalid` 让 HTTP 层能分清这两种失败。
- 暂存目录必须与数据库、上传目录**同一块文件系统**（都放在 DB 的父目录下），
  否则换装时的 rename 会退化成跨盘拷贝，中途断电两边都不完整。
- 解包用**白名单**（manifest.json / payprint.db / files/...）而不是黑名单挡 zip-slip：
  我们本来就知道合法的名字长什么样。

### 12. 交付是一个二进制，配置是一份文件

这两条是一体两面：既然只交付一个文件，就不该再要求运维去设一堆环境变量、
再去凑齐"前端产物目录"。

**单个二进制**：前端被 `//go:embed all:dist` 打进二进制（`internal/webui`），
由 `httpapi` 直接从内嵌文件系统提供，未命中的路径回退到 `index.html`（SPA 深链接）。
于是"WEB_DIR 指错"这个失败模式从结构上消失了；只剩"构建时压根没把前端拷进去"，
那种情况下 `webui.Built()` 会返回 false，启动日志立刻点出来。

**配置文件**（`internal/config`）的几个刻意选择：

- **默认位置跟着二进制走**（`os.Executable()` 所在目录，见 `DefaultPath`），不是工作目录：
  生产上 systemd 用什么 `WorkingDirectory` 拉起不该影响它读哪份配置，开发时又常在仓库根目录
  敲命令，按工作目录找会让配置散进源码树，还会出现"同一个二进制、换个目录就换一份配置"。
  于是"建个空目录、只放二进制、跑一次"就得到一个自包含的部署目录。
- **`go run` / `go test` 例外**：它们的二进制在临时构建目录里（`go-build...`），往那儿写配置
  下次运行就没了，这种情况退回工作目录（`isEphemeralDir`）。
- **文件不存在就按内置模板生成一份再继续启动**，生成失败（只读目录、权限不足）只记警告、
  **不阻断启动**：默认值本身是能跑的，不该因为写不进一个文件就把服务拦在门外。
- **模板是默认值的唯一定义处**：`defaults()` 就是把 `Template()` 解析一遍再套到空结构体上。
  默认值在代码里再抄一份，迟早会与生成出来的文件对不上。
- **只覆盖出现在文件里的键**：删掉一行 = 回到默认值，而不是被当成空值。改动面因此永远可见。
- **认不出的键只警告，值写错直接报错并指出键名**：前者是版本回退的真实场景
  （旧文件里留着新版本的键），后者静默按默认值跑是最难排查的一类事故。
- **相对路径以配置文件所在目录为基准**（不是进程工作目录）："二进制 + payprint.conf + data/"
  于是可以作为一个整体被搬走，systemd 的 `WorkingDirectory` 也就不再是隐式契约。
- **不再读环境变量**：配置只有一个来源，日志第一行报出本次读的是哪个文件。
  之前"某次 `export` 悄悄盖掉配置文件"这类问题连排查入口都没有。

### 13. 审计日志只有订单，且全部是快照

审计日志（`order_audits`，管理面板「审计日志」页 + `GET /api/v1/admin/audits`）是**订单日志**：

- 一条订单一条记录，在 `OrderService.Create` 的**同一个事务**里写入；写不进去就一起回滚，
  所以不存在"有订单、没审计"的状态。
- 内容只有：账号、设备（名称 + 指纹快照）、订单编号、金额、打印文件名、打印类型
  （色彩 / 单双面 / 份数 / 总页数）、下单时间。
- **表上没有任何外键**，所有字段都是下单那一刻的快照：设备被解绑 / 删除、打印机被级联删除
  （订单随之消失）、账号改名，都不会改动或抹掉已写下的记录。

写入之外的动作（解绑设备、封禁账号、改运行参数、退款、人工重推……）**不进审计日志**：
它们各自把原因留在自己那一行上（`users.ban_reason`、`devices.revoke_reason`、
`ledger_entries.memo`、`orders.close_reason`），并写一行结构化的进程日志（`slog`）。
`audit_logs` 这张通用操作日志表已随迁移 00014 删除，别再把它加回来。

## 状态机

### 订单

```
awaiting_payment ──支付──▶ awaiting_release ──用户确认──▶ queued ──▶ printing ──▶ completed
      │                          │                          │
   取消/超时                 超时未确认                  投递失败
      ▼                          ▼                          ▼
  cancelled / expired      refund_pending              refund_pending / failed
```

`refunded` / `disputed` / `expired` 是人工或核验路径的终态，见
[`API.md`](API.md#订单-status) 的全表。

### 打印任务

```
pending ──认领──▶ submitting ──投递成功──▶ submitted ──▶ printing ──▶ completed
   ▲                  │                        │
   │              投递失败/进程退出          回查发现中止
   └──重试────────────┘                        ▼
                                          failed（终态失败）
                                          needs_review（待人工）
                                          cancelled（订单被退款，就地取消）
```

`cancelled` 与 `failed` 刻意分开：`failed` 是"试过了没成功"，`cancelled` 是"有意不打了"。
**只有还没投出去（`pending`）的任务能被取消**；已经交给打印机的只能记 `needs_review`，
谎报成"已取消"只会误导排障的人。

两种"卡住"各有出路，没有哪种状态是没人接的：还没交出去就卡住的由租约回收重投
（`RecoverOrphans`），交出去之后再没有进展的由 `stuck_job_ttl` 转人工（`EscalateStuckJobs`）。

### 到账事件（`payment_events`）

判定结果有四种，其中 `unresolved` 是"钱可能收了但没出纸"，必须人工跟进：
`accepted` / `rejected`（协议噪音）/ `duplicate`（幂等）/ `unresolved`。

## 本地开发

需要 Go 1.27+；前端联调需要 Node 24+（在前端仓库里，见下）。`go` 在 PATH 上就能开工，
不需要任何额外配置。

### 启动

```bash
make build      # 取前端产物（自动下载或复用）→ go build（产物是单个文件 dist/payprint-server）
make run        # 跑起来（首启自动生成 payprint.conf、建库、跑迁移，并登记一台默认打印机）
make help       # 看全部目标
```

首次 `make build` 会从 PayPrint-Frontend 的最新 Release 下载 dist（需要网络）；
本地已经取过就复用。想换本地产物或强制刷新：

```bash
sh scripts/web.sh --force                     # 重新下载最新 Release
sh scripts/build.sh --web-dist ../PayPrint-Frontend/dist   # 用前端仓库本地构建的产物
sh scripts/build.sh --no-web                  # 不取产物（无页面的二进制，接口照常）
```

**Windows** 用等价的 PowerShell 脚本（参数名换成 `-WebDist` / `-NoWeb` / `-RefreshWeb`）：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1
```

打开 <http://127.0.0.1:8080>。健康检查 `GET /healthz`。

改后端不想重新构建的话，用热重载（air）或 `go run`，两者读的都是**同一个嵌入目录**，
只要前端产物取过一次：

```bash
make dev                      # air 热重载：改 Go 代码自动重编译重启（.air.toml）
cd server && go run ./cmd/server
# 注意：go run / air 的二进制在构建目录里，配置与 data/ 会退回**当前目录**（这里就是 server/，
# 都在 .gitignore 里）。跑真正构建出来的二进制时，它们落在二进制旁边。
```

想看"第一次运行到底是什么样"（配置文件是自己长出来的），把 `payprint.conf` 删掉再启动一次。

### 前端开发（在 PayPrint-Frontend 仓库里）

```bash
cd ../PayPrint-Frontend
npm ci
npm run dev     # http://localhost:5173，/api 代理到 127.0.0.1:8080
```

后端照常 `make run`（或 `make dev`）；vite 的代理指向 `127.0.0.1:8080`，
可用 `VITE_API_TARGET` 环境变量改。想把本地构建的前端打进二进制，
直接 `sh scripts/build.sh --web-dist ../PayPrint-Frontend/dist`。

### 可选：把工具链关在项目内

如果不想让 Go 构建缓存写进 `%APPDATA%`、npm 缓存写进 `%LOCALAPPDATA%`（用户目录在 C 盘时
这很实际），可以自备一套 Go + Node 放进 `.tools/`（仓库旁或上一级都行，
脚本按这个顺序找），激活后本会话内的缓存全部重定向到项目内。

```powershell
. ..\.tools\env.ps1    # PowerShell（注意开头的点号 = dot-source）
```

`scripts/*.ps1` 检测到 `.tools/env.ps1` 就自动使用，检测不到就退回 PATH 上的工具，
**这是个人环境偏好，不是本仓库的构建前提**：`.tools/` 被 `.gitignore` 排除，
别人 clone 下来的仓库里根本没有它。`scripts/verify-isolation.ps1` 负责核对这个偏好
（只对本地有意义，CI 不跑它）。

## 校验与自检

`make check` 就是 CI 跑的那套；`make smoke` 是本地/CI 都能跑、但 CI 只在一次性的
干净环境里跑的那套。

```bash
make check       # 格式 + vet + 全部测试 + 单二进制构建（复用或自动下载前端产物）
make build       # 只构建：取前端产物 → 嵌入目录 → 单二进制
make smoke       # 端到端冒烟：起临时服务（自带一份临时 payprint.conf）+ 一台假 IPP 打印机
sh scripts/verify-boot.sh   # 校验"第一次运行会自己长出配置文件"与"一台打印机都不可达时也能启动"
```

`make smoke` 需要 PayPrint-Frontend 的检出（端到端脚本在前端仓库里，
import 它真实的签名实现）；默认找旁边的 `../PayPrint-Frontend`，
找不到时用 `SMOKE_WEB_DIR` 指定。冒烟与自检的构建都走 `--no-web`：
它们只打 API 与 IPP 链路，不需要网络下载前端产物。

Windows 等价命令：把上面的 `make xxx` / `sh scripts/xxx.sh` 换成
`powershell -NoProfile -ExecutionPolicy Bypass -File scripts\xxx.ps1`。

`verify-boot.sh` / `verify-boot.ps1` 单独存在是有原因的：

- 冒烟测试是**带着一台可用的假打印机**启动服务的，所以它永远发现不了"启动路径偷偷依赖打印机"
  这类回归。这个脚本故意反过来，一个打印机都没有、打印机地址指向一个死端口，然后断言：
  进程照常起来、种子打印机标成 `offline` 而不是 `online`、探测死地址返回"不可达 + 原因"
  而不是 5xx、运行参数改完立刻在 `/meta` 上生效。
- 它还断言**交付模型本身**：把一个空目录当作目标机器上的部署目录（只放二进制），**从另一个工作
  目录**启动它，然后要求 `payprint.conf` 与 `data/` 出现在二进制旁边、而工作目录里什么都不许
  多出来。这是"一个二进制拷过去就能跑"这句话的可执行版本，也顺带锁住了 `db_path` 这类相对
  路径的解析基准（配置文件所在目录，而不是工作目录）。
- 冒烟与自检都用 `-config` 指向临时目录里的配置，因此**不会碰开发机上真实的那份**。

`scripts/install-service.sh`（把服务装成 systemd 服务、开机自启）是 bash，只能在目标机器上跑。
开发机没有 systemd，能做的两件事恰好也够用：

```bash
bash -n scripts/install-service.sh                     # 语法检查
bash scripts/install-service.sh --dry-run --binary <某个存在的文件>
```

`--dry-run` 会把要执行的命令逐条打印出来（建账号、装二进制与单元文件、`enable`、`restart`、
等 `/healthz`），一行也不改。它**不内嵌单元文件内容**，而是从
`deploy/systemd/payprint.service` 里读 `User` / `ExecStart` / `ReadWritePaths`，
单元文件因此是唯一来源，脚本与它不会对不上；`--dry-run` 也就顺带验证了"单元文件里的布局
与脚本的假设一致"。解析时统一 `tr -d '\r'`：单元文件经过 Windows 编辑器往返是常态，
不处理的话 `User=payprint` 会被读成 `payprint\r`，接着就会去建一个带回车符的用户名。

冒烟脚本（`PayPrint-Frontend/scripts/smoke.mjs`，在前端仓库里）做了三件别的手段做不到的事：

1. **用前端真实的签名实现跑真实 HTTP**：用 Node 的 WebCrypto 生成设备密钥，
   直接 `import` 前端仓库的 `src/lib/sign.ts`，这是唯一能发现"前后端各自正确、合起来验签失败"
   这种跨语言问题的手段，TypeScript 编译和 Go 单测都发现不了——前端仓库分离后，
   这条检查顺便把"两个仓库的签名协议没对齐"也挡住了。
2. **走真实 IPP**：启动 `scripts/fake-ipp.mjs`（极简假打印机，也在前端仓库里）并通过运维接口登记它，
   于是 Print-Job 提交与 job-state 回查都不是打桩。
3. **逐项断言**（脚本末尾会打印总数）：健康检查、SPA 回退、注册与公钥固定、换密钥冒充、
   中文设备名编码、上传与页数识别、服务端计价、支付校验链与幂等、
   **"到账之后不得自动出纸"**、确认打印（含越权被拒）、**完整打印链路直到订单完成**、
   假打印机只收到一次 Print-Job、记账、越权读订单/解绑设备、
   **管理员免设备登录且不能打印**、**收款凭据的签发/金额匹配/吊销即失效**、
   收尾路径（退款 / 重推守卫 / 到账待办）、
   **运行参数改完即生效且非法值整体拒绝**、**打印机连通性探测**、**账号封禁/解封（理由必填且落库）**、
   **备份导出与无效备份拒收**。

其中"假打印机只收到一次 Print-Job"是最有价值的不变量断言：它在**打印机一侧**验证了
"一个订单只会出一次纸"。

## 数据库

迁移用 goose（**内嵌进二进制**，启动时自动执行），查询用 sqlc 生成。

### 加一个字段

```
1. 新增 server/internal/database/migrations/000NN_xxx.sql（-- +goose Up / Down）
2. 改 server/internal/database/queries/*.sql
3. cd server && sqlc generate
```

- 迁移文件的注释**必须 ASCII**，见下面的坑。
- 生成代码 `internal/store/` 不要手改。
- 需要让某列为 `*string` 而不是 `interface{}` 时，在 `server/sqlc.yaml` 的
  `overrides` 里声明（已有 `devices.user_id`、`sessions.device_id`、
  `payment_events.order_id`、`payment_events.user_id` 几例）。
  `payment_events.user_id` 是用 `ALTER TABLE ADD COLUMN` 加上的，sqlc 无法从迁移里
  推断它的类型，漏了 override 就会塌成 `interface{}`。
  `sessions.device_id` 那一例尤其不能省：管理员会话没有设备，NULL 是有意义的值，
  塌成空串就会让"这个会话是不是设备会话"变得无法判断。
- 加索引/唯一约束时想清楚它兜底的是哪个并发竞态，本项目的几个关键不变量
  （设备唯一、一个订单一个任务、一笔支付一条记账）都是靠唯一约束兜底的。

## 打包与发布

### 一个二进制，换平台只换参数

```bash
make build                  # 本机（产物 dist/payprint-server）
make release-linux-arm64    # linux/arm64，即 aarch64
make release-linux-armv7    # linux/arm/v7，32 位 ARM
make release-linux-amd64    # linux/amd64，即 x86_64
```

版本号用 `make build VERSION=0.1.0` 传入（写进启动日志；git 提交号与构建时间自动检测，
也可用 `GIT_COMMIT` / `BUILT_AT` 覆盖）。不想用 make 就直接调脚本：

```bash
VERSION=0.1.0 sh scripts/build.sh
GOOS=linux GOARCH=arm64 sh scripts/build.sh -o dist/payprint-server-linux-arm64
```

**Windows** 等价：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1 -Version 0.1.0
powershell ... -File scripts\build.ps1 -GOOS linux -GOARCH arm64 -Out dist\payprint-server-linux-arm64
powershell ... -File scripts\build.ps1 -GOOS linux -GOARCH arm -GOARM 7 -Out dist\payprint-server-linux-armv7
```

纯 Go 依赖（`modernc.org/sqlite`）意味着**关掉 CGO 就能交叉编译**，
amd64 / arm64 / armv7 都不需要目标平台的编译器。

**前端产物必须先到位**（嵌入目录里有 `index.html`，或让脚本去下载）。`build` 脚本会自动处理
（`scripts/web.sh` 下载 → 拷进 `server/internal/webui/dist` → `go build`，CGO 固定为 0）。
手工操作时对应两步：

```bash
sh scripts/web.sh                 # 从 PayPrint-Frontend 最新 Release 下载并解包进嵌入目录
cd server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w -X main.version=0.1.0" -o ../dist/payprint-server-linux-arm64 ./cmd/server
```

> **微架构基线**：Go 工具链默认按较新的微架构编译。要兼容很老的设备，构建 amd64 时设
> `GOAMD64=v1`（`make` 里默认就是 v1），构建 arm/v7 时用 `GOARM=7`（即 `GOARCH=arm`）。
> arm64 不需要额外设置。
>
> 拷前端那一步漏掉也不会构建失败，但会得到一个**没有前端**的二进制：接口都正常、首页 404，
> 启动日志里会有一条警告点出这一点（`webui.Built()`）。

### 交付与升级

交付物就是那一个文件：

```bash
scp dist/payprint-server-linux-arm64 user@<目标机器>:/opt/payprint/payprint-server
chmod +x /opt/payprint/payprint-server
systemctl restart payprint
```

不想手工出包：在 GitHub 推 tag 并发布 Release 后，`release.yml` 会自动交叉编译
linux/amd64、linux/arm64、linux/armv7 与 windows/amd64 四个平台，打成 zip
（内含平台原生二进制，附 `.sha256` 校验文件）并上传到该 Release
（版本号取 tag 名，经 `-ldflags` 写进启动日志）。本地出包用 `make release-*`。

升级 = 覆盖文件 + 重启；数据库与上传文件在 `data/`，schema 由 goose 在启动时自动升到当前
版本。**别顺手把打包里的旧 `payprint.conf` 也覆盖上去**，配置文件是进程级配置的唯一事实
来源，线上改过的值都在那里面。

## 开发注意（都是踩过的坑）

- **SQL 文件（`internal/database/**/*.sql`）必须用 ASCII 注释。** sqlc 的 SQLite parser
  遇到中文注释会直接报语法错误，且报错位置具有误导性。Go 代码里的中文注释不受影响。
- **Go 文件名不要以 `_android.go` / `_linux.go` / `_arm64.go` 结尾。** 这类后缀会被 Go
  当作 `GOOS`/`GOARCH` 构建约束，在其它平台上被**静默忽略**（症状是"常量凭空消失"）。
- **`scripts/*.ps1` 必须保持纯 ASCII。** PowerShell 5.1 会把无 BOM 的 UTF-8 当 ANSI(GBK)
  读取，脚本里出现中文会直接解析失败。（`scripts/*.sh` 没有这个限制，中文注释与输出都正常。）
- **`scripts/*.sh` 必须保持 LF 换行。** CRLF 会让 bash 在解释 shebang 与参数时出各种怪问题；
  `.gitattributes` 已把仓库内文本统一为 LF，别在自己的编辑器里把它改回 CRLF。
- **前端颜色只能从 CSS 变量取**（前端仓库 `src/styles/index.css`），变量里存的是
  **空格分隔的 RGB 通道**，这样 Tailwind 的 `bg-primary/10` 这类透明度修饰符才能工作。
  组件里不得出现十六进制色值。
- **日志中文在 Windows 控制台显示为乱码**是代码页问题，不是程序问题；
  生产环境（Linux）正常。
- **`server/internal/webui/dist/` 里那个点开头的占位文件不能删。** `go:embed all:dist`
  在目录为空时会直接编译失败；`all:` 前缀就是为了把点开头的文件也算进来。
  构建脚本清目录时会绕过点开头的文件，也是同一个原因。

## 安全红线（不要为了"简化"去掉）

| 不变量 | 为什么 |
|---|---|
| 客户端提交的金额永远不参与计价 | 否则"金额整数分比对"这条校验就失去唯一事实来源 |
| 归属校验必须写进 SQL 的 `WHERE` | 越权与"不存在"返回同一个结果，避免探测 |
| 到账匹配候选不唯一时不放行 | 判错意味着 A 付的钱印了 B 的文件 |
| 打印任务只能由 `Release` 创建 | 它是唯一出纸入口，校验链就挂在这里 |
| 在途任务（`submitting`/`submitted`/`printing`）不得被改回 `pending` | 否则同一份文件会被投递两次，多出一张纸 |
| 页数来源 `detected` / `declared` 必须区分 | 只有服务端自己数出的页数才允许"整篇打印"优化 |
| 记账（收到钱）与建任务（用户确认）是两个事务 | 产品决策：到账不出纸，确认才出纸 |
| nonce 验签通过即消耗 | 失败请求也不能留下可重放的签名 |
| 由**角色/身份**判断动作，不要靠"他现在恰好没权限" | 例如管理员不能消费必须显式拦（`requireConsumer`），不能指望某条路径顺带挡下，那是一条会随实现变化而失效的巧合 |
| 订单 / 到账 / 审计只认账号，不得重新引入设备依赖 | 设备是账号的附属物；一旦某个功能又要求"必须同一台机器"，就把设备的生命周期（解绑、停用、换机）绑到了它身上 |
| 审计日志只记订单，且全部字段是快照 | 它是对账凭据：混进其他动作会让人分不清"这是账还是操作记录"；引用别的表会让设备解绑 / 打印机删除改写历史 |
| 收款凭据必须校验订单归属打印机 | 否则一个打印机的收款端能凭 `order_no` 认领另一个打印机的订单，把钱算到别人头上 |
| 运行时配置只能通过快照读写 | 原地改 `*config.Config` 会产生数据竞争，而后台循环与 HTTP 处理器都在并发读它 |
| 启动流程不得依赖打印机可达 | 打印机晚几天才装好是常态，账号/上传/下单/收款不该跟着停摆 |
| 不能停用最后一个启用中的管理员 | 面板之外没有复位通道，停掉它等于永久锁死管理入口 |

## 已知限制（技术侧）

- **除注册与登录外，所有接口都只要求会话**（`POST /api/v1/files` 也一样）。上传的 body 是
  几十 MB 的 multipart，为算摘要整块读进内存不划算，而文件本身只对该账号可见；其余接口
  的身份是账号，设备不参与（约束 1）。
- **令牌是持有者凭证**：既然下单 / 确认打印不再要求设备签名，拿到令牌的一方就能从任何
  机器上以该账号行事（"令牌被盗也无法在别处使用"这条保证随之取消，见约束 1 的取舍）。
  会话记录它登录的那台设备，设备被停用 / 解绑会让它作废；能加以保护的只有会话 TTL
  （`session_ttl`）、内网部署与 HTTPS。
- **PDF 页数识别是启发式的**：`internal/pdfutil` 统计 `/Type /Page` 出现次数，
  无法处理页树被对象流压缩的 PDF。这类文件会要求客户端显式提供页数，并被记为
  `page_count_source = declared`，投递时**一定**下发页范围（禁用整篇打印优化），
  所以最坏后果是少打几页而不是少付钱多出纸。要彻底解决需引入完整 PDF 解析库（如 pdfcpu），
  代价是二进制体积与交叉编译复杂度显著上升。
- **图片转出的 PDF 是固定 A4**（`pdfutil.pageBoxFor`，横图给横向 A4、竖图给纵向 A4），
  图片等比缩放后居中（`pdfutil.fitWithin`）。A4 尺寸是**写死的常量**，不跟随打印机配置的
  纸张，转换发生在**上传时**，那一刻用户还没选纸。想按用户选的纸生成页面，就得把转换
  推迟到投递时，而原图在上传后就已经删掉（只留 PDF），那是另一个量级的改动。
  好在 `print-scaling=fit` 仍然会下发，所以选 A3 时只是"把一张 A4 放大"。
- **官方查单只实现了微信 v2（XML + MD5）**，v3 的 APIv3 密钥 + 平台证书方案留作后续升级。
