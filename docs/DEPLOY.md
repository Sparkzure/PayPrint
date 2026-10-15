# 部署说明

## 拓扑

```
  ┌──────────────┐        局域网         ┌───────────────────────────┐
  │  用户设备     │ ───────────────────▶ │  Web/API（linux/amd64）     │
  │ Android/浏览器│     HTTP + 签名       │  Go 单二进制 + SQLite + 前端 │
  └──────┬───────┘                       └────────────┬──────────────┘
          │                                            │ IPP (TCP 631)
          │ 微信扫码付款                                 ▼
          │                                 ┌───────────────────────────┐
          └───────────────────────────────▶ │  打印机（无线 / 网络，推荐） │
                                            │  支持 IPP / AirPrint       │
                                            └───────────────────────────┘
```

打印机**只有 USB、不支持网络打印**时，中间加一台 CUPS 网关（一台常开的小主机即可）：

```
  Web/API ──IPP──▶ CUPS 网关（linux/arm64 + USB 打印机）
```

职责边界：打印机（或 CUPS 网关）只负责执行打印；本项目负责账号、计价、支付与任务编排。
Web/API 与被投递的那一侧**在同一局域网内**，因此采用 Web 侧直接 push IPP 的模型，
被投递的那一侧不需要 agent、隧道或反向代理。

## 一、构建与打包

交付物是**一个二进制文件**：HTTP 接口、后台任务循环与前端页面全在里面。前端在构建时
被 `go:embed` 打进二进制（`server/internal/webui`），所以运行时**不需要任何额外的
资源目录**，也不存在"拷了二进制忘了拷前端，首页 404 而 `/healthz` 正常"这种事故。

```bash
make build        # 需要 Go 1.27+（首次构建需联网取前端产物）；产物 dist/payprint-server
```

这一条命令做两件事：把前端产物放进嵌入目录（从 PayPrint-Frontend 的最新 Release
自动下载，取过一次就复用）→ `go build`。前端源码与构建在
[PayPrint-Frontend](https://github.com/Sparkzure/PayPrint-Frontend) 仓库，本仓库不需要 Node。
产物约 14 MB。Windows 上用 `powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1`，
效果一样（完整说明见 [`DEVELOPMENT.md`](DEVELOPMENT.md) 的「打包与发布」）。

换目标平台就是换一个 release 目标。纯 Go 依赖（`modernc.org/sqlite`）+ `CGO_ENABLED=0`，
交叉编译不需要目标平台的编译器：

```bash
make release-linux-arm64    # linux/arm64，即 aarch64
make release-linux-armv7    # linux/arm/v7，32 位 ARM
make release-linux-amd64    # linux/amd64，即 x86_64
```

> **微架构基线**：Go 工具链默认按较新的微架构编译。要兼容很老的设备，
> 构建 amd64 时设 `GOAMD64=v1`（`make` 目标里默认就是 v1）、构建 arm/v7 时用 `GOARM=7`。
> arm64 不需要额外设置。

不想用脚本时，手工等价的做法是：

```bash
sh scripts/web.sh    # 下载前端产物并解包进嵌入目录 server/internal/webui/dist/
cd server && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../dist/payprint-server ./cmd/server
```

嵌入目录里没有 `index.html` 时，得到的是一个**没有前端**的二进制：接口都正常、首页 404，
不过启动日志里会有一条警告直接指出这一点。

## 二、运行（单二进制 + systemd）

目标机器上不需要 Go / Node，也不需要装别的运行时，**也不需要这个仓库的目录结构**：
建一个空目录，把二进制放进去就行。运行一次之后，那里长成这样：

```
/opt/payprint/payprint-server     # 部署前只有这一个文件（前端在里面）
/opt/payprint/payprint.conf       # 首次启动按默认值生成，之后按需改
/opt/payprint/data/              # 数据库 + 上传文件，备份这一个目录就够
```

```bash
/opt/payprint/payprint-server     # 前台跑；Ctrl+C 停
/opt/payprint/payprint-server -config /etc/payprint/payprint.conf   # 配置放到别处时
```

首次启动会在**二进制所在目录**生成 `payprint.conf`（0600，因为它会放运维令牌），
并在旁边建出 `data/`，然后开始监听 `:8080`。**启动不依赖打印机**，也**不依赖**
配置文件预先存在。

几个容易踩的点：

- **配置认的是二进制的位置，不是工作目录**：从哪个目录敲命令、systemd 的
  `WorkingDirectory` 设成什么，都不影响它读哪份配置。只有 `go run` / `go test` 这种
  二进制落在临时构建目录里的情况会退回工作目录（临时目录里的配置下次运行就没了）。
- **`printer.default_printer_uri` 要用局域网地址**，不能写 `localhost`，本服务与打印机
  （或 CUPS 网关）通常不是同一台机器。它只影响首次启动种下的那台打印机，之后的打印机在
  面板「打印机」页维护。
- **相对路径以配置文件所在目录为基准**：`db_path = data/payprint.db` 指的是
  `/opt/payprint/data/payprint.db`，与进程的工作目录无关，所以整个目录可以整体搬走。
- **`-config` 指到别处时记得路径基准也变了**：那时 `db_path` / `storage_dir` 相对
  新的配置目录解析，建议直接写绝对路径，并保证那个目录可写。
- **改完配置要重启进程**：配置只在启动时读一次（`systemctl restart payprint`）。

### systemd（开机自启）

**推荐用安装脚本**，它把下面「账号 → 二进制 → 单元文件 → enable」整条链走完并验证：

```bash
# 在目标机器上执行（需要 root）；--dry-run 可以只看它打算做什么，不改任何东西
sudo bash scripts/install-service.sh --binary /tmp/payprint-server-linux-arm64 --unit payprint.service
```

脚本做五件事：建 `payprint` 系统账号 → 装二进制到 `/opt/payprint/` → 装单元文件 →
`daemon-reload` + `enable` + `restart`（**enable 就是开机自启**）→ 等 `/healthz` 回话并打印摘要。
**重复执行等于升级**：二进制被替换，`payprint.conf`（含运维令牌）与 `data/` 原样保留。
`sudo bash scripts/install-service.sh --uninstall` 反向操作（停服务、去掉自启、删单元文件，数据保留）。

单元文件在仓库里（安装脚本用的就是这一份，不另存副本）：
[`deploy/systemd/payprint.service`](../deploy/systemd/payprint.service)。

不想用脚本就手工装，等价于脚本里的步骤：

```bash
groupadd -r payprint
useradd -r -g payprint -s /usr/sbin/nologin payprint
install -d -o payprint -g payprint /opt/payprint
install -m 0755 payprint-server /opt/payprint/payprint-server
install -m 0644 deploy/systemd/payprint.service /etc/systemd/system/

# 首次启动会自动生成 /opt/payprint/payprint.conf 与 /opt/payprint/data/
systemctl daemon-reload && systemctl enable --now payprint
systemctl status payprint
journalctl -u payprint -f          # Ctrl+C 只退出查看，不会停服务
```

装完可以用这几条确认「开机自启」真的生效了：

```bash
systemctl is-enabled payprint      # 期望 enabled
systemctl is-active  payprint      # 期望 active
systemctl show -p WantedBy payprint  # 期望 multi-user.target（这条链就是开机自启）
```

单元里的两个要点：

- `WorkingDirectory` **不决定**配置文件位置（配置跟着二进制走），它只是常规做法；
  要把配置放到 `/etc/payprint/` 得改用
  `ExecStart=/opt/payprint/payprint-server -config /etc/payprint/payprint.conf`，
  并把那个目录加进 `ReadWritePaths`，同时把 `db_path` / `storage_dir` 写成绝对路径。
- `ProtectSystem=strict` 下只有 `ReadWritePaths` 里的目录可写。默认给的是部署目录
  `/opt/payprint`，配置文件与 `data/` 都在它下面（这也是**别把二进制装进
  `/usr/local/bin` 这类只读目录**的原因：配置与数据都会跟着它，需要写权限）。

这台机器常年开机，**日志也要限体积**。journald 默认占磁盘的 10%，SD 卡上太宽松：

```bash
# /etc/systemd/journald.conf
SystemMaxUse=200M
```

### Docker

不想装 systemd、或在 NAS 上跑，用镜像 `ghcr.io/sparkzure/payprint:latest`
（amd64 / arm64 / armv7，里面是同一个单二进制）：

```bash
docker run -d --name payprint --restart always -p 8080:8080 \
  -e TZ=Asia/Shanghai \
  -v /opt/payprint/data:/opt/payprint/data \
  ghcr.io/sparkzure/payprint:latest
```

配置与数据都在挂载目录里（`payprint.conf` 由入口脚本软链进去，数据库与上传文件也在其中），
备份它等于备份整站；`TZ` 要跟打印点一致，在线时段按容器本地时间判定。
升级就是拉新镜像重建容器，数据不动。打印机在宿主机所在局域网时，默认桥接网络即可直连。
镜像的构建与发布由 `beta_docker.yml`（main 分支出 `beta` 标签）与 `release_docker.yml`
（正式 tag）负责，悬空的无 tag 层用 `clean_untagged_docker.yml` 手动清理。

### 配置参考

配置分两层，互不重复：

1. **进程级 / 引导级配置**：`payprint.conf`（首次启动自动生成），改动后**重启进程**生效。
2. **运行时配置**：面板里可改的项，覆盖值存数据库并随数据备份还原，不进 `payprint.conf`
   （保存即生效，不重启），默认值与说明见「七、日常与应急操作」的运行参数表。

文件不存在时服务会按内置默认值生成一份带注释的配置**再继续启动**，默认位置是**二进制所在目录**
（与进程工作目录无关）。想放到别处就显式指定 `-config`。写法很窄：

```ini
[server]                 # 段
http_addr = :8080        # 键 = 值
cors_origins = a,b       # 逗号分隔即列表
# 井号或分号之后是注释；值两端成对的引号会被去掉
```

- **删掉某一行 = 回到内置默认值**（不是变成空值）。
- 值里出现 ` #` 或 ` ;`（前面带空格）之后的部分被当注释；其余位置的 `#` 是普通字符。
- 相对路径（`db_path` / `storage_dir`）以**配置文件所在目录**为基准解析。
- 认不出的键只记警告（版本回退时旧文件里可能留着新版本的键），但**值写错会直接拒绝启动并指出键名**。

#### `[server]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `env` | `production` | 仅用于日志标识 |
| `http_addr` | `:8080` | 监听地址 |
| `log_level` | `info` | `debug` / `info` / `warn` / `error` |
| `cors_origins` | `http://localhost:5173,http://127.0.0.1:5173` | 允许跨域的前端来源（逗号分隔）。生产同源部署可留空；开发时前端跑在 vite（5173）需要这两项 |

#### `[storage]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `db_path` | `data/payprint.db` | SQLite 路径（相对本文件所在目录） |
| `storage_dir` | `data/files` | 上传文件目录，与数据库一起备份即可 |

#### `[security]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `session_ttl` | `720h` | 会话有效期 |
| `signature_skew` | `5m` | 设备签名时间戳允许偏移 |
| `nonce_ttl` | `15m` | nonce 保留时长，**必须 ≥ 2 × signature_skew**（启动时校验） |
| `max_upload_bytes` | `52428800` | 单文件上限（50 MiB） |
| `admin_token` | 空 | 运维令牌，给脚本与救急用；留空则命令行通道关闭（面板不受影响） |

#### `[printer]`

打印机（IPP）相关参数。里面的地址可以是直连打印机的，也可以是 CUPS 网关上的队列。

| 键 | 默认值 | 说明 |
|---|---|---|
| `default_printer_uri` | `ipp://127.0.0.1:631/ipp/print` | **只在首次启动、打印机表为空时**用来种一台待配置打印机，之后在面板「打印机」页维护。无线打印机填 `ipp://<打印机IP>/ipp/print`，走 CUPS 网关则填 `ipp://<网关IP>:631/printers/<队列名>` |
| `default_printer_name` | `默认打印机` | 同上 |
| `user_name` | `payprint` | 上报给打印机的 `requesting-user-name` |
| `job_format` | `application/pdf` | 文档格式，目前只支持 PDF |
| `retry_base` / `retry_max` | `30s` / `10m` | 投递失败时的指数退避区间 |

请求超时、重试次数与回查间隔属于运维现场调参，放在面板里。

#### `[worker]`

| 键 | 默认值 | 说明 |
|---|---|---|
| `enabled` | `true` | 关闭后打印任务不会自动投递（纯 API 实例或测试用） |
| `interval` | `5s` | 派发 / 核验 / 回收循环间隔 |
| `batch` | `20` | 单轮处理条数 |
| `lease` | `2m` | 任务租约；超过此时长仍未写终态的任务会被判为孤儿并回收重投 |

#### `[payment]`

到账核验的官方兜底查单（目前实现的是微信 v2 查单）。

| 键 | 默认值 | 说明 |
|---|---|---|
| `query_enabled` | `false` | 打开后必须同时提供下面三项，否则启动失败 |
| `app_id` / `mch_id` / `api_key` | 空 | 商户信息 |
| `query_url` | `https://api.mch.weixin.qq.com/pay/orderquery` | 查单地址 |
| `timeout` / `max_attempts` | `10s` / `5` | 单次查单超时与兜底重试次数 |

## 三、打印机接入

打印机地址（`cups_uri`）接受任何 IPP 打印机地址，两种接法：

### A. 直连无线打印机（推荐）

打印机支持无线 / 网络打印（AirPrint / IPP Everywhere / Mopria）时，不需要任何中间设备：

1. 在打印机上开好无线网络，记下它的 IP
2. 打印机地址填 `ipp://<打印机IP>/ipp/print`（部分机型要带端口 `:631`）
3. 从 Web/API 机器验证：

   ```bash
   ipptool -tv ipp://192.168.1.60/ipp/print get-printer-attributes.test
   ```

### B. CUPS 网关（打印机不支持网络时）

打印机只有 USB、或压根不提供 IPP 服务时，用一台常开的机器接上打印机并装 CUPS，
由它把 IPP 转成 USB 打印。**用系统 CUPS，不要用容器**（USB 打印机依赖 udev、驱动与设备直通，
容器化只会增加故障面）。这台网关是标准 CUPS 主机，不在本项目的管理范围内，
下面只列与本打印机直接相关的几条：

1. `cupsd` **只监听局域网网卡**（`Listen 192.168.1.50:631`）
2. `Allow` 限定到实际网段，**不要** `Allow all`
3. `WebInterface No`（关掉管理界面）
4. 用 `lpadmin` 建队列后，打印机地址填 `ipp://192.168.1.50:631/printers/<队列名>`
   （**必须精确到队列**，不能只写主机或 `/ipp/print`，否则多台打印机会打到同一个队列上），
   并**一定要从 Web 侧验证一次连通性**：

   ```bash
   # 在 Web/API 机器上执行
   ipptool -tv ipp://192.168.1.50:631/printers/office get-printer-attributes.test
   ```

两种接法都请在部署阶段先单独验通：不通时应用侧只会表现为"投递失败并不断重试"。

## 四、首次部署清单

1. 启动服务 → 看启动日志：第一行会报出本次读的配置文件路径，后面确认
   `migration_version` 与 `payment_sources`
2. `curl http://<host>:8080/healthz` 返回 `database: ok`
3. 按真实打印机改价目（**面板的「打印机」页更省事**）：

   ```bash
   curl -X POST http://<host>:8080/api/v1/admin/printers \
     -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' \
     -d '{"code":"P01","name":"一楼黑白机","cups_uri":"ipp://192.168.1.60/ipp/print",
          "status":"online",
          "price_single_bw_fen":100,"price_single_color_fen":300,
          "price_duplex_bw_fen":200,"price_duplex_color_fen":600,
          "supports_color":true,"supports_duplex":true,
          "paper_sizes":["A4"],"max_copies":99}'
   ```

   `cups_uri` 填打印机自己的 IPP 地址（走 CUPS 网关时填 `ipp://<网关IP>:631/printers/<队列名>`）。

   四个 `price_*_fen` 都是**每张**（纸张）单价、单位分；不支持的档位填 0。

   打印点不是 24 小时开放时，在面板的「打印机」页给打印机配**在线时段**
   （例如 `online_windows: [{"days":[1,2,3,4,5],"start":"08:00","end":"22:00"}]`）：
   时段之外打印机按离线对待，不能下单也不能确认打印，已排队但还没投递的任务会等到开门再投递。
   不配 = 全天在线。注意这个接口是**覆盖更新**：用命令行改别的字段时如果不带上
   `online_windows`，已配置的时段会被清空（面板保存会自动带上）。
4. 打开首页，**注册一个普通用户账号**（会自动绑定当前浏览器设备）。
   这一步要在 `https://` 或 `http://localhost` 下做：设备密钥依赖 WebCrypto，
   用局域网 IP 的 http 地址会失败，原因与反代要求见「八、用 HTTPS 访问」一节
5. `GET /api/v1/printers?live=true` 确认打印机 `live.reachable = true`
6. 走一遍完整链路：上传 → 下单 → 支付上报 → **用户确认打印** → 确认出纸
7. 在 `payprint.conf` 的 `[security]` 段设置 `admin_token`，重启进程
   （`systemctl restart payprint`），确认 `/api/v1/admin/stats` 可访问
8. 决定打印放行策略（默认"支付确认后等用户手动放行"）：
   - 「确认打印时限」：用户一直不确认时的回收时限，默认 `30m`，超时转待退款（管理面板可改）
   - 「放行前强制官方核验」：要不要在放行前强制官方查单确认（管理面板可改）
   - 「到账即出纸」：无人值守时打开，跳过手动确认（管理面板可改）
9. **建一个管理员账号并签发收款凭据**（管理面板与正式链路的支付上报都靠它们，见下）

### 管理面板与收款凭据

**建管理员**，两条路。

**路 A（推荐）：空库后的第一个注册账号自动成为管理员。** 提权窗口只在库为空的那一次：
部署完成、服务可访问后**立刻**注册你的账号，注册完浏览器会直接带你进 `/admin`
（这条会话属于管理面板，用户端进不去）。这个账号**不占设备绑定**，
同一台机器之后仍可注册成员账号。前提是你能抢到第一个注册位；
若服务暴露在他人可访问的网络上且无法立刻注册，请走路 B。

**路 B：借运维令牌建号**（只能由管理员创建管理员）：

```bash
curl -X POST http://<host>:8080/api/v1/admin/users \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"username":"owner","password":"<强密码>","role":"admin"}'
```

之后打开 `http://<host>:8080/admin` 登录即可。**管理员登录不受设备限制**，
但**不能打印**（下单 / 上传 / 确认打印一律 `403 admin_cannot_consume`）。

**签发收款凭据**：正式链路的上报靠它。在收款端装好之前，链路只能靠网页端的
"我已支付，手动补报"按钮推进；上线的最后一步是给现场那台机器（或脚本）
签发一份凭据，**它是 API 凭据，不是账号**（面板的「收款凭据」页点两下即可）。
三条要点：明文令牌只出现一次，服务端只存散列，丢了只能吊销后重签；
凭据绑打印机不绑机器，换机继续收款是常态，打印机边界同时就是到账匹配的边界；
吊销立刻生效，凭据泄露时靠它兜底：

```bash
# ① 签发（响应里的 token 只此一次，立刻填进收款端）
curl -X POST http://<host>:8080/api/v1/admin/collectors \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"printer_code":"P01","label":"A 点收款机"}'

# ② 复核
curl "http://<host>:8080/api/v1/admin/collectors" -H "X-Admin-Token: $TOKEN"
# 该凭据应为 revoked=false、printer_code 为 P01；last_used_at 会随上报更新
```

两条要写进现场运维守则的约束：

- **收款端只持凭据**：没有账号、读不到任何用户数据，只能上报到账。
- **同一打印机、同一时刻尽量不要出现两笔同金额的待支付订单**。微信收款通知里没有订单号，
  服务端按「打印机 + 金额 + 到账时间」匹配，两笔同金额并存时会转人工
  （`payment_ambiguous`），钱不会丢，但需要人来核销。

## 五、备份与恢复

**首选：管理面板的「备份与还原」页。** 它导出一个 zip（数据库一致性快照 + 全部上传文件），
也能从这样的 zip 还原（上传校验通过后，服务退出重启，启动时在打开数据库之前完成换装；
换下的旧数据保留为 `*.pre-restore`）。命令行等价做法：

```bash
# 导出（内容与面板下载的完全一致）
curl -o payprint-backup.zip -H "X-Admin-Token: $TOKEN" http://<host>:8080/api/v1/admin/backup
```

> 还原建议只在面板里做：它涉及"停服务、换库、清 WAL"三步，面板把它们编排好了，
> 手工做漏一步（尤其是忘删 `-wal`/`-shm`）会直接损坏数据库。

SQLite 处于 **WAL 模式**，直接 `cp` 数据库文件会拿到不一致的快照。
两种手工做法（只在面板不可用时用，路径取配置文件里的 `db_path`，默认
`/opt/payprint/data/payprint.db`）：

```bash
# 方式一（推荐，不停服务）
sqlite3 /opt/payprint/data/payprint.db ".backup '/backup/payprint-$(date +%F).db'"

# 方式二：停服务后把 db / db-wal / db-shm 三个文件一起复制
systemctl stop payprint
cp /opt/payprint/data/payprint.db* /backup/
systemctl start payprint
```

上传的 PDF 在 `storage_dir`（默认 `data/files`）下按账号分目录，与数据库一起备份即可
（丢失文件只会让历史订单无法重推，不影响账号与账务）。`payprint.conf` 也建议一起备份
，运维令牌在里面，重装时不用重新签发。

手工恢复：把 `.db` 放回 `db_path`（并确认 `-wal`/`-shm` 已删除），
启动时 goose 会自动把 schema 升到当前版本。

## 六、多实例

任务抢占靠 `print_jobs` 上的 `ClaimJob`（`status='pending'` 的条件更新 +
受影响行数判定），因此**多实例是安全的**。但推荐：

- API 实例：配置文件里 `[worker] enabled = false`
- 只让一个实例：`[worker] enabled = true`

注意 SQLite 是单写者模型，多实例写同一个库文件在高并发下会退化成串行。
如果打印点规模真的变大，应换 PostgreSQL（sqlc 与 SQL 都留了这个余地）。

## 七、日常与应急操作

面板里能做的都在面板里做（`http://<host>:8080/admin`）。这一节是面板之外的命令行等价做法，
给脚本和面板打不开的场合用。所有请求都要带 `X-Admin-Token: $TOKEN`（值取自
`[security] admin_token`），或管理员会话的 Bearer 令牌。打印机与价目的增改见
「四、首次部署清单」第 3 步。

### 收款凭据

签发与复核见「四、首次部署清单」的「管理面板与收款凭据」一节。吊销是软删，留痕且立刻生效：

```bash
curl -X DELETE http://<host>:8080/api/v1/admin/collectors/<凭据ID> \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' -d '{"reason":"换机"}'
```

### 运行参数（保存即生效，不用重启）

```bash
# 查看：带当前值、内置默认值，以及是否已被面板覆盖
curl http://<host>:8080/api/v1/admin/settings -H "X-Admin-Token: $TOKEN"

# 修改：一次提交一组，任一项不合法则整组拒绝
curl -X PUT http://<host>:8080/api/v1/admin/settings \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"set":{"release_ttl":"20m","max_pending_orders_per_user":"3"}}'

# 把某几项还原成内置默认值
curl -X PUT http://<host>:8080/api/v1/admin/settings \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"reset":["release_ttl"]}'
```

面板里改过就以面板为准（会标注「已在面板覆盖」，也可一键恢复默认）。全部运行参数：

| 面板上的名称 | 默认值 | 说明 |
|---|---|---|
| 到账即出纸 | `false` | `false` 时收到到账只推进到「待确认打印」，**必须用户手动确认才出纸**；`true` 则校验通过后立即投递 |
| 放行前强制官方核验 | `false` | `true` 时确认打印前必须拿到官方查单的明确「已支付」结论，否则拒绝（可重试）。需先配好微信商户信息 |
| 确认打印时限 | `30m` | 「待确认打印」的存活时间，超时转待退款并告警。`0` = 不回收 |
| 待支付时长 | `15m` | 下单后多久没收到到账就自动关闭 |
| 单人待支付订单上限 | `0`（不限） | 一个账号同时最多几笔「待支付」订单，超出后下单被拒 |
| 开放自助注册 | `true` | 关闭后只能由管理员在面板里建号。首次部署后应立即注册管理员，再按需关闭 |
| IPP 请求超时 | `20s` | 打印机慢或网络差时可调大 |
| 投递最大重试次数 | `5` | 只影响新建的任务 |
| 作业状态回查间隔 | `15s` | 调小更及时，代价是多探测几次打印机 |
| 在途任务超时转人工 | `1h` | 任务停在「已入队 / 打印中 / 打印中断」超过这个时长、且打印机不再报告任何进展时，转「待人工处理」、订单转「待退款」并打告警。必须大于最慢的一次打印耗时，否则正常的长任务会被判成卡住。`0` = 不回收 |

密钥类（`admin_token`）、安全窗口类（`signature_skew` / `nonce_ttl`）与路径类（数据库、上传目录）
刻意不放进面板：前两者放开会削弱安全保证，后者改了只会得到「一半读旧路径、一半读新路径」的状态。

### 账号

```bash
# 建号（role 可为 user / admin）
curl -X POST http://<host>:8080/api/v1/admin/users \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"username":"someone","password":"<强密码>","role":"user"}'

# 封禁 / 解封。理由必填：封禁理由落在账号行上，解封理由写进服务端日志，都立刻生效
curl -X POST http://<host>:8080/api/v1/admin/users/<userID>/ban \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"reason":"恶意占用打印资源"}'
curl -X POST http://<host>:8080/api/v1/admin/users/<userID>/unban \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' \
  -d '{"reason":"账号交接完成"}'
```

### 订单、审计日志与设备

```bash
# 列表：订单 / 审计日志（附账号与设备快照）/ 设备（附归属账号）
curl "http://<host>:8080/api/v1/admin/orders?limit=50"   -H "X-Admin-Token: $TOKEN"
curl "http://<host>:8080/api/v1/admin/audits?limit=100"  -H "X-Admin-Token: $TOKEN"
curl "http://<host>:8080/api/v1/admin/devices?limit=100" -H "X-Admin-Token: $TOKEN"

# 导出全部审计日志（CSV 流，带 BOM，Excel 直接打开）
curl -OJ http://<host>:8080/api/v1/admin/audits/export -H "X-Admin-Token: $TOKEN"

# 退款：写一条负数记账流水，与状态变更同事务
curl -X POST http://<host>:8080/api/v1/admin/orders/<orderID>/refund \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' -d '{"reason":"用户申请"}'

# 重推打印任务：只对真的停下来的任务有效，在途任务会被拒，避免重复出纸
curl -X POST http://<host>:8080/api/v1/admin/orders/<orderID>/retry -H "X-Admin-Token: $TOKEN"
```

设备的两个人工作业（换机、恢复出厂、二手转让时用）：

```bash
# 解绑：彻底释放。归属、公钥固定、会话与重放记录一并清空，指纹让出，
# 这台硬件下次注册或登录按新设备处理
curl -X POST http://<host>:8080/api/v1/admin/devices/<deviceID>/release \
  -H "X-Admin-Token: $TOKEN" -H 'Content-Type: application/json' -d '{"reason":"换手机"}'

# 停用：保留归属与公钥固定，所以能原样恢复（有归属回「已绑定」，无归属回「已解绑」）
curl -X POST http://<host>:8080/api/v1/admin/devices/<deviceID>/block   -H "X-Admin-Token: $TOKEN"
curl -X POST http://<host>:8080/api/v1/admin/devices/<deviceID>/restore -H "X-Admin-Token: $TOKEN"
```

### 到账待办

归属不了的到账（`payment_unmatched`、`payment_ambiguous`、`order_expired`、`amount_mismatch`）
记为 `unresolved`。它们意味着钱可能已经收了但没出纸，必须人工核销：

```bash
curl "http://<host>:8080/api/v1/admin/payment-events?status=unresolved" -H "X-Admin-Token: $TOKEN"
```

## 八、用 HTTPS 访问

部署初期的自查都在服务器本机用 `http://localhost:8080` 做。要让别人用手机或自己的电脑访问，
得在前面加一层 HTTPS 反向代理，否则普通用户连注册和登录都做不完。这不是建议，是硬条件。

原因不在服务端，在浏览器。设备身份是一把浏览器里的 ECDSA-P256 私钥，靠 WebCrypto 生成和签名，
而 WebCrypto 只在安全上下文里存在。安全上下文指 https 地址，或者 localhost。
用 `http://192.168.1.10:8080` 打开时它压根不存在，注册页会直接顶回「当前环境无法生成设备密钥」。

受这条限制的只有用户端的注册与登录这两道门，它们要设备签名。管理面板登录走的是密码，
不用设备密钥，http 下一样进得去。但既然用户端非 HTTPS 不可，反代这一步就躲不掉。

服务端自己不碰 TLS：二进制用的是 `ListenAndServe()`，配置里没有证书项，也不发 HSTS。
证书和 TLS 全在反向代理那边。有个后果要知道：已经在 http 下登录过的浏览器还能接着用，
会话没过期就行，但退出登录、换浏览器或会话过期之后就登不回来了，所以别把这当能凑合的状态。

### 反代要满足的几条

证书的域名必须和用户输入的地址一致。公开 CA 不给内网 IP 签证书，所以要么给这台机器一个内网域名
（内部 DNS 指过来），要么用 mkcert 这类本地 CA 签一张，再在每台客户端上装一次根证书。

路径、query 和请求体都不能改写。签名覆盖了 method、path、规范化 query，还有原始请求体的 SHA-256，
代理只要动了其中一样，比如加个尾斜杠或做一次 rewrite，验签就会失败，而错误只说明签名不匹配，
不会告诉你是代理改的。

这些请求头要放行：`Authorization`、`X-Collector-Token`、`X-Admin-Token`，以及
`X-Device-Fingerprint`、`X-Device-Timestamp`、`X-Device-Nonce`、`X-Device-Signature`、
`X-Device-Public-Key`、`X-Device-Platform`、`X-Device-App-Version`、`X-Device-Label`。

请求体上限要放大，等响应的超时也要放宽。单文件上限默认 50 MiB，nginx 默认只放 1 MB 过，
上传会被挡在代理层；大文件进到服务端后还要识别页数、算价，nginx 默认等 60 秒就要回一个结果，
容易变成 504，建议放宽到 5 分钟。

配好之后建议把 `http_addr` 改成 `127.0.0.1:8080`，只让本机的反代连得上它。

访问日志里的客户端 IP 会全变成 `127.0.0.1`，这是刻意的：`clientIP()` 只取 TCP 对端地址，
不信 `X-Forwarded-For`（转发头可以伪造，写进日志就是假证据）。要看真实来源得去反代的日志里看。

一份最小可用的 nginx 配置：

```nginx
server {
    listen 443 ssl;
    server_name payprint.lan;            # 要与用户访问的域名一致

    ssl_certificate     /etc/ssl/payprint/fullchain.pem;
    ssl_certificate_key /etc/ssl/payprint/privkey.pem;

    client_max_body_size 64m;           # nginx 默认 1m，上传会挡在这里
    proxy_read_timeout   300s;          # 默认 60s，大文件出结果慢，放宽一些

    location / {
        proxy_pass http://127.0.0.1:8080;
        # 不改写路径、query 与请求体：签名覆盖了这三样
    }
}
```

## 九、排障

| 现象 | 排查方向 |
|---|---|
| 用户注册被拒 `device_already_used` | 该设备已在库中（参与过其他账号）。若确属换机/二手设备，由管理员 `POST /api/v1/admin/devices/{id}/release` 解绑：解绑会彻底释放该设备并让出指纹，之后同一台硬件可重新注册或登录 |
| 用户登录被拒 `device_bound_to_other_account` | 同上 |
| `device_key_mismatch` | 同一指纹换了密钥。恢复出厂后 Keystore 重新生成密钥属正常，**先解绑**再登录即可（解绑会清空公钥固定，并让这台硬件按新设备重新登记） |
| 支付上报一直 `amount_mismatch` | 设备上报的金额与订单不符。核对打印机价目是否被改过、设备是否读到了错误的通知金额。注意：**以订单金额为准**，改价不会追溯历史订单 |
| 上报被拒 `collector_credential_invalid` | 凭据缺失、无效或已吊销。到管理面板「收款凭据」页确认状态；丢了明文只能吊销后重新签发 |
| 上报被拒 `payment_unmatched` | 这笔到账在该打印机找不到任何待支付订单：可能是金额填错、扫错码，也可能是订单已超时（后者会返回 `order_expired` 并指出订单号）。事件已落库（`payment_events`，`status=rejected`），按金额和 `reported_at` 人工核销 |
| 上报被拒 `payment_ambiguous` | 该打印机有**多笔同金额**的待支付订单，无法判断这笔钱属于谁。人工指定后处理（退款或手工核销）。这是"同金额并存"运营约束的直接体现，见上面部署清单 |
| 启动日志说"已登记默认打印机，但它的地址还没验证过" | 正常：启动**不探测、也不依赖**打印机。到管理面板「打印机」页改地址并点「测试连接」 |
| 「测试连接」报不可达 | 探测结果里带原因（连接被拒 / 超时 / 队列不存在），据此区分"地址写错"与"打印机没开、网关没起、网络不通"。这只影响投递，账号与订单不受影响 |
| 管理员下单报 `admin_cannot_consume` | 设计如此：管理员只做管理，不能打印。下单请用普通用户账号 |
| 改了「运行参数」却"没生效" | 面板保存即生效；若你是在 `payprint.conf` 里改同名项，那是无效的，这一层配置**只在数据库里**。到「运行参数」页看当前生效值，或点「恢复默认」 |
| 改了 `payprint.conf` 却没生效 | 配置只在进程启动时读一次。`systemctl restart payprint`（或前台跑时重启） |
| 收款端突然不能再上报 | 依次查三件事：凭据是否被吊销（面板「收款凭据」页能看到状态与最后使用时间）、那台机器上的收款端是否还在跑、网络是否可达。凭据与设备/账号无关，解绑设备不会影响它 |
| 订单停在"待确认打印"（`awaiting_release`） | 这是**正常中间态**：钱已确认，等用户点「确认打印」。如果用户说没看到按钮，检查客户端是否用了旧版本（不认 `can_release` 字段） |
| 用户点确认打印报 `printer_offline` | 打印机离线/暂停/探测不通。**订单状态未变**，用户可在打印点恢复后重试。在 Web 机器上用 `ipptool` 直连 `printer-uri` 定位 |
| 用户在别的机器上点确认打印 | 正常：订单属于**账号**，换一台设备重新登录后照样能确认自己的订单（不再要求"必须是下单那台设备"） |
| 用户点确认打印报 `release_window_closed` | 超过「确认打印时限」（运行参数页）未确认，订单已转待退款。要么人工退款，要么调大这个值 |
| 大量订单转入 `refund_pending` 且原因是"未确认打印" | 说明用户付完钱就走了。要么调大「确认打印时限」，要么在客户端把确认入口做得更醒目 |
| 想改成"支付即出纸" | 面板「运行参数」打开「到账即出纸」。注意无人值守时出纸可能被他人顺手拿走 |
| 订单停在 `queued` 但不出纸 | worker 没在跑或投递一直失败。看 `job_status` / `job_error`；确认配置文件里 `[worker] enabled = true` 且至少有一个实例开了 worker |
| 任务状态为"打印中断"（`interrupted`） | 打印机把作业停住了（IPP `processing-stopped`）：卡纸、缺纸、被暂停。**这不是终态**，故障排除后作业通常会自行继续、订单走到 `completed`。服务端在作业刚进入该状态时打过一条 ERROR 日志；`/admin/stats` 里 `interrupted` 的计数就是"有多少张纸卡住了"，长期不为 0 说明现场没人处理 |
| 日志频繁出现"已回收被中断的打印任务" | 有 worker 进程在认领任务后异常退出（OOM、被 kill、被 systemd 重启）。回收本身是对的（否则订单会永久卡住），但频繁出现说明 worker 不稳，要查内存与重启策略 |
| 日志出现"退款时打印任务已投递、无法收回" | 退款时任务已经交给打印机，**这一单多出了一份纸**（钱退了、纸出来了）。需要人工确认，并检查是不是在 `printing` 状态下才点的退款 |
| `POST /admin/orders/{id}/retry` 返回 409 | 任务在途或已完成，拒绝重推，这是防"重复出纸"的守卫。真卡在 `submitting` 的孤儿由后台按 `WORKER_LEASE` 自动回收，不需要人工重推 |
| 上传报 `too_many_pages` / 下单报 `page_range_too_large` | 页数上限是 5000 页（页范围会被逐页展开，必须有上界）。缩小页范围即可；"整篇打印"不受该上界影响 |
| 一单只付了 1 页却打出整份文件 | 不应发生：只有服务端自己数出页数（`print_files.page_count_source = detected`）时才允许"整篇打印"优化；客户端上报的页数一律照发 `page-ranges`。若确实出现，把该 `page_count_source` 与 PDF 一起留档反馈 |
| `job_status=needs_review` | 重试耗尽或官方查单矛盾。修好网络后用 `POST /api/v1/admin/orders/{id}/retry` 重推 |
| 订单 `disputed` | 官方查单与设备上报矛盾（"上报说已付、官方说没付"）。这是最高优先级告警，需要人工核实是否被伪造 |
| 订单 `refund_pending` | 已收钱但没出纸，需人工退款：`POST /api/v1/admin/orders/{id}/refund` |
| 打印顺序错乱 / 重复出纸 | `print_jobs.order_id` 唯一约束保证一个订单最多投递一次；若仍重复，检查是否有多个 worker 实例共用了不同的数据库文件 |
| 日志中文在 Windows 控制台显示为乱码 | PowerShell 代码页问题，不是程序问题。生产环境（Linux）日志为正常 UTF-8 |
| 配置文件压根没被读到 | 默认读**二进制同目录**下的 `payprint.conf`。启动日志第一行的 `config=...` 报的就是本次实际读的那份 |
| 配置文件写不进去（二进制放在只读目录） | 生成失败只警告、启动照常（用内置默认值），但 `data/` 默认同在目录下，数据库随后会打不开。把二进制放进可写目录，或用 `-config` 指到可写目录并给 `db_path` 写绝对路径 |
| 启动报 `exec format error` | 拿错架构的二进制。`uname -m` 看本机架构（`armv7l` / `aarch64` / `x86_64`），换对应文件 |
| 找不到管理员登录页 | 管理员入口是 `/admin`，不是首页 |
| 面板提示「该账号不是管理员」 | 该账号 `role` 是 `user`。用管理员账号登录，或用运维令牌调 `POST /api/v1/admin/users` 建一个 `role: admin` 的账号 |
| `curl` 调 `/api/v1/admin/*` 返回 403 | `admin_token` 为空（命令行通道关闭），或没带 `X-Admin-Token` 头。面板本身不受影响 |
| 换人使用时还停在上一个人的账号里 | 登录态按浏览器保存，不会自动登出。在「账户」页点「退出登录」，或用无痕窗口给访客 |
| 用户说付款了，订单还停在「待支付」 | 没有任何到账上报进来：收款端没签发凭据、凭据已吊销、那台机器不在线。先让用户在订单页点「我已支付」补报，再查收款端 |
