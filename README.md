<div align="center">

# payprint

<p><em>局域网自助打印：网页上传、扫码付款、用户确认后经 IPP 直投打印机出纸</em></p>

[![CI](https://github.com/Sparkzure/PayPrint/actions/workflows/ci.yml/badge.svg)](https://github.com/Sparkzure/PayPrint/actions/workflows/ci.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)

</div>

---

- 文档：[接口契约](docs/API.md)、[部署与运维](docs/DEPLOY.md)、[架构与贡献](docs/DEVELOPMENT.md)、
  [文档站](https://sparkzure.github.io/PayPrint-Docs/)、[官网](https://sparkzure.xyz)
- 社区：[参与贡献](CONTRIBUTING.md)、[AI 协作与提交规范](AGENTS.md)、[行为准则](CODE_OF_CONDUCT.md)
- 其他：[报告安全漏洞](SECURITY.md)、[许可证](LICENSE)

## 项目状态

尚未发布正式版本。1.0 之前接口、配置项与数据库结构仍可能变动。
这是 AI 练手项目，不保证长期可用性。

## 功能特性

- [x] 单二进制交付：HTTP 接口、后台任务循环与前端页面全在一个文件里
  - [x] 另有多架构 Docker 镜像（amd64 / arm64 / armv7），里面是同一个二进制
- [x] IPP 直投打印机（AirPrint / IPP Everywhere / Mopria 机型），不需要中间设备
  - [x] USB 打印机走 CUPS 网关转发，打印机地址精确到队列
  - [x] 双面、彩色、份数、页范围，服务端按张计价
  - [x] 打印机分时段在线：时段外不下单、不放行，投递顺延且不耗重试预算
- [x] 到账与出纸分离：收到钱只记账，用户点「确认打印」才出纸
  - [x] 两条到账入口且都要求身份：账号会话只报自己的单，收款凭据按打印机上报
  - [x] 打印机内金额唯一匹配，匹配不上或多单就转人工，绝不猜
  - [x] 官方查单二次核验（可配），放行前可强制核验
- [x] 设备身份：浏览器内不可导出的 ECDSA-P256 密钥、请求签名、nonce 防重放
- [x] 管理面板：账号、设备、打印机与价目、订单（退款、重推）、收款凭据、审计日志（CSV 导出）
- [x] 运行参数面板热更：确认时限、待支付时长、到账即出纸、自助注册开关等，保存即生效

## 快速开始

只需要一个二进制文件，拷过去运行，不需要预装 Go、Node 或数据库。每台打印机各自成站。

```bash
# 1. 把二进制放进一个可写目录，空跑一次：它会生成带注释的 payprint.conf 与 data/
/opt/payprint/payprint-server

# 2. 改两个必填项：[security] admin_token（运维令牌）与 [printer] default_printer_uri（打印机 IPP 地址）
nano /opt/payprint/payprint.conf

# 3. 正式跑起来；长期运行用脚本一行装成 systemd 服务（开机自启、崩溃拉起）
sudo bash scripts/install-service.sh --binary /opt/payprint/payprint-server --unit payprint.service

# 4. 在服务器本机用 http://localhost:8080 注册第一个账号，它会自动成为管理员

# 5. 面板里配好三件事：打印机页建打印机并测试连接、收款凭据页签发凭据、运行参数页按需调整
```

- 第 4 步要在服务器本机做：普通用户的注册与登录需要 https 或 localhost
  （设备密钥依赖 WebCrypto），原因与反向代理要求见
  [`docs/DEPLOY.md`](docs/DEPLOY.md) 的「用 HTTPS 访问」。
- 自己编译：`make build`（需要 Go 1.27+；前端产物自动取自
  [PayPrint-Frontend](https://github.com/Sparkzure/PayPrint-Frontend) 的 Release，不需要 Node）。
  交叉编译、Docker 与 systemd
  的完整做法见 [`docs/DEPLOY.md`](docs/DEPLOY.md) 的「构建与打包」「运行」「用 HTTPS 访问」。
- 部署目录最终是三样东西：二进制、`payprint.conf`、`data/`，整体可搬走。
  配置怎么写、每个键的默认值，见 [`docs/DEPLOY.md`](docs/DEPLOY.md) 的「配置参考」；
  打印机怎么接（直连或 CUPS 网关）见「打印机接入」。

## 使用流程

1. 打开部署时定下来的地址注册账号，或由管理员建号后登录
2. 「新建打印」上传 PDF 或图片（JPG/PNG，不超过 20 MB），服务端识别页数并计价
3. 选打印机与参数（份数、单双面、纸张、彩色、页范围），提交订单，进入「待支付」
4. 扫码付款。到账被上报之后，订单变成「待确认打印」
5. 回到订单页点「确认打印」，校验通过就出纸

要记住的一条规则：**收到付款还不会打印**。到账只把订单推进到「待确认打印」，
数据库里此刻没有任何打印任务。只有用户点「确认打印」才创建任务并出纸，
点下去时服务端会重新校验订单归属、确认时限、打印机此刻是否可用，以及（若配置了）官方查单结论。
超过确认时限（默认 30 分钟）没人确认，订单转待退款。无人值守想付款即出纸，
在「运行参数」里打开「到账即出纸」。

管理员在部署地址下的 `/admin`，走账号密码登录，不受设备限制，也不能打印；
收款端是打印点上持打印机凭据上报到账的那台机器或脚本，没有账号、读不到任何用户数据。
两者的账号与凭据管理都见 [`docs/DEPLOY.md`](docs/DEPLOY.md) 的「首次部署清单」。

## License

本项目以 [GNU Affero 通用公共许可证 3.0](LICENSE)（AGPL-3.0）发布：
可以自由使用、修改、分发，但把它改完跑成对外提供的网络服务时，必须开放修改后的源码。
