# API 契约

Base URL：`http://<host>:8080`，所有业务接口前缀 `/api/v1`。

## 通用约定

- 请求与响应体均为 UTF-8 JSON（文件上传除外，见下）。
- 单对象响应：`{"data": {...}}`
- 列表响应：`{"data": [...], "total": 12, "limit": 20, "offset": 0}`
- 错误响应：`{"error": {"code": "order_expired", "message": "订单已超时关闭，请重新下单"}}`
- 金额一律为**整数分**，字段名以 `_fen` 结尾。全链路不出现浮点数。
- 时间字段一律为 **unix 秒**。未发生的时刻为 `0`。

### 鉴权梯度

三条**互不相干**的身份线，这是本项目的骨架：

| 级别 | 要求 | 用于 |
|---|---|---|
| 公开 | 无 | `/healthz`、`/api/v1/meta` |
| 设备签名 | 四个签名头 | 注册 / 登录（此时设备还不存在，靠请求体里的公钥做 TOFU 固定） |
| 会话 | `Authorization: Bearer <token>` | 用户端**全部**接口：`/me`、订单、下单、取消、**确认打印**、到账上报、文件与上传、登出 |
| 管理员会话 | `Authorization: Bearer <token>`，且账号 `role=admin`，会话**无设备** | `/api/v1/admin/*`（**不受设备限制**） |
| 运维令牌 | `X-Admin-Token` | `/api/v1/admin/*`（脚本与首次建号，可与管理员会话并存） |
| 收款凭据 | `X-Collector-Token: <token>` | `POST /api/v1/collector/payment/report` |

- **身份核心是账号**：设备签名只出现在注册与登录这两道门上（那两道门在生产"这台设备属于
  哪个账号"）。下单、取消、确认打印、到账上报、上传都只认会话：订单与到账挂在 `user_id` 上，
  设备是账号的附属物，只在出单时作为**快照**写进订单审计（接口见 [管理员](#管理员) 一节的审计日志）。
  因此**同一账号换一台设备也能确认自己的订单**，订单也不随设备解绑而失效或改属。
- **令牌是持有者凭证**：既然这些接口不再要求设备签名，拿到令牌的一方就能从任何机器以该
  账号行事。会话记录它登录的那台设备（设备被停用 / 解绑会让它作废），能加以保护的只有
  会话有效期与"服务只在可信网络内可达"。
- **管理员不占设备**：管理员账号走管理面板登录（`POST /api/v1/admin/login`），既不创建也
  不使用设备记录，它的会话没有 `device_id`。所以同一台机器可以同时承载"一个成员账号的
  绑定"和"任意多个管理员账号"，bootstrap 出来的那个管理员也不会占掉该设备的指纹。
  "一台设备只能绑一个账号"这条规则约束的是**成员账号**，且只在设备在库期间成立：
  管理员解绑会把这台设备彻底释放并让出指纹，那之后同一台硬件按新设备处理（见 [账号与设备](#账号与设备)）。
- **管理员能管一切，但不能打印**：`role=admin` 的会话访问用户端接口一律返回
  `403 admin_cannot_consume`（见 [管理员](#管理员)）。
- **收款端不接入用户体系**：它是一份 API 凭据，没有账号、没有会话、没有设备签名，
  只能调收款上报那一个接口（见 [收款凭据与到账匹配](#收款凭据与到账匹配)）。
- **上传只依赖会话鉴权**（`multipart` 的 body 是几十 MB，为算摘要整块读进内存不划算）；
  文件本身也只对该账号可见。

## 打印是怎么被放行的

``` 
下单 ──▶ awaiting_payment ──(收到支付消息并通过校验链)──▶ awaiting_release
                                                              │
                                            用户手动确认打印 ───┘
                                                              │
                                                              ▼
                                                    queued ──▶ printing ──▶ completed
```

三句话说明这条链路的取舍：

1. **收到支付消息不会出纸**。它只把订单推进到"待确认打印"，
   此刻数据库里**没有任何打印任务**。
2. **只有用户确认打印才会创建打印任务**（`POST /api/v1/orders/{id}/print`）。
   这是唯一能让打印机出纸的用户入口。
3. 确认时会再做一轮完整校验（归属 / 设备一致性 / 状态 / 时限 / 打印机可用性 /
   支付核验）。**宁可多等几百毫秒，也不出一张不该出的纸。**

无人值守、无需确认的场景可以在「运行参数」里打开「到账即出纸」（`auto_release_on_payment`），
跳过第 2 步，但默认是关闭的。

## 收款凭据与到账匹配

**收款端**是部署在打印点的那台机器（或一个脚本）。它替该打印机的**所有**用户
上报到账，但**它不是账号、也不是设备**，它持有的只是一份绑定打印机的 API 凭据。

```
管理员在面板签发凭据（选打印机） ─▶ 把令牌填进收款端 ─▶ 收款端带 X-Collector-Token 上报
```

- 凭据表在 `collector_credentials`：一行 = 一个打印机 + 一份令牌散列。
  库里**只有散列**，明文只在签发那一刻返回一次，丢了只能吊销重建。
- 凭据**只绑打印机，不绑机器、不绑账号**：换一台手机继续收款是现场常态，
  绑硬件只会制造"换机即停收"。打印机边界同时就是到账匹配的边界。
- 吊销是软删除（`revoked_at` / `revoke_reason`），立刻生效，记录保留以便事后对账。
- 设备与收款彻底无关：解绑设备、停用账号、重装系统都不影响凭据。

### 到账是怎么和订单对上的

到账通知里**没有我们的订单号**，只有金额和时间。因此收款端上报时可以不带
`order_no`，由服务端在**该凭据绑定的打印机**内匹配。匹配分两步：

1. **按金额**筛出候选：`amount_fen` 完全相等、且**仍能吸收这笔钱**的订单，
   `awaiting_payment`（未过支付时限）或 `awaiting_release`（此前已确认过的到账）。
   后者让"同一笔到账换了 event_id 再报一次"能走幂等分支，
   而不是伪装成一笔"没对上账"的钱。
2. **按规格 + 单价**再核对（可选）：收款端若上报了 `duplex` / `color_mode` /
   `unit_price_fen`，则候选必须与订单固化的规格、每张单价一致；没报则退化为只按金额匹配。

| 候选数 | 结果 |
|---|---|
| 1 | 进入后续校验链 |
| 0 | `409 payment_unmatched`；若同金额订单只是超时了，则返回 `409 order_expired` 并指认订单号 |
| ≥2 | `409 payment_ambiguous`，转人工 |

> **≥2 笔时宁可不放行**：判错意味着 A 付的钱印了 B 的文件。
>
> 下单侧已经把这个歧义堵住了：同一打印机内，若待支付区已有一笔相同金额的订单，
> 后来者下单会被拒（`409 amount_duplicate`，文案「金额与其他用户重复，请等待」）。
> 因此金额本身通常就能唯一定位订单，规格 / 单价是额外的保险。
>
> 上报体里带 `order_no` 可以精确命中、彻底避开歧义（服务端优先按它处理）；
> 收款通知本身不提供该字段，"用户转账时备注订单号"是可选的增强手段。

## 请求签名

**只有 `POST /api/v1/auth/register` 与 `POST /api/v1/auth/login` 需要签名。**
这两道门在生产"这台设备属于哪个账号"（绑定或复用一台设备），必须证明请求来自持有该
设备私钥的那台机器。其余接口（订单、确认打印、到账上报、文件……）只认会话：设备不是
它们的依赖，带上这些头也会被忽略。

### 请求头

| 头 | 必填 | 说明 |
|---|---|---|
| `X-Device-Fingerprint` | 是 | 设备指纹。不可信，仅用于定位设备记录 |
| `X-Device-Timestamp` | 是 | unix 秒。与服务器时间偏差超过 `security.signature_skew`（默认 5 分钟）即拒 |
| `X-Device-Nonce` | 是 | 每次请求唯一，建议 24 字节随机数的 base64（长度 16–256） |
| `X-Device-Signature` | 是 | 签名，见下 |
| `X-Device-Public-Key` | 否 | base64(SPKI DER) 的 ECDSA-P256 公钥。**仅在注册 / 首次登录时必须提供**；设备已登记后再次提供且与库中不一致会被判定为设备伪造 |
| `X-Device-Platform` | 否 | 平台标识，如 `android`。含非 ASCII 时必须百分号编码 |
| `X-Device-App-Version` | 否 | 客户端版本 |
| `X-Device-Label` | 否 | 设备显示名。含非 ASCII（如中文）时**必须**百分号编码 |

> HTTP 头字段只能承载 latin-1 字节。把中文直接放进 `X-Device-Label` 会让浏览器端的
> `fetch` 抛 `TypeError`，因此约定用 `encodeURIComponent` 编码后再放进来，
> 服务端会还原。

### 签名明文

7 行，用 `\n` 连接，**最后一行之后不带换行**：

```
payprint/v1
<METHOD，大写>
<请求路径，不含 query>
<规范化后的 query>
<unix 秒级时间戳>
<nonce>
<请求体的 SHA-256，十六进制小写>
```

规范化 query 的规则（必须与服务端 `auth.CanonicalQuery` 一致）：

1. 解析成键值对
2. **键升序**，同一键的多个**值也升序**
3. 每个键与值按 `application/x-www-form-urlencoded` 转义（空格 → `+`），
   转义集合与 Go 的 `url.QueryEscape` 相同：仅 `A-Za-z0-9-_.~` 不转义
4. 用 `&` 连接

> 不要用 `encodeURIComponent` 代替第 3 步：两者对 `!`、`'`、`(`、`)`、`*`、`~`
> 的处理不同，会导致一端转义另一端不转义。前端实现见前端仓库（PayPrint-Frontend）的 `src/lib/sign.ts`。

无 query 时该行为空串；空请求体的摘要为
`e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。

### 签名与公钥编码

- 算法：**ECDSA-P256 + SHA-256**
- 公钥：base64 编码的 SPKI(PKIX) DER，即 Android `KeyPair.getPublic().getEncoded()` 的原始字节
- 签名：base64，支持两种形态
  - ASN.1 DER（Android Keystore 默认输出）
  - 紧凑 `r||s`（64 字节，WebCrypto 输出）

两者都按 SHA-256 摘要校验，服务端按长度自动识别。

### 伪代码

```ts
const canonical = [
  'payprint/v1',
  method.toUpperCase(),
  path,
  canonicalQuery(params),
  String(timestamp),
  nonce,
  await sha256Hex(bodyBytes),
].join('\n')

const signature = base64(await crypto.subtle.sign(
  { name: 'ECDSA', hash: 'SHA-256' }, privateKey, utf8(canonical)))
```

> 请求体必须**先序列化成字节**，用同一份字节算摘要并签名，再原样发出。
> "签名用一份、发送用另一份"（例如两次 `JSON.stringify`）会造成偶发验签失败。

## 幂等与重放

- `X-Device-Nonce` 落库并带唯一约束，**重放请求在 INSERT 阶段就被拒**，
  返回 `409 nonce_replayed`。nonce 只在验签通过后才写，避免攻击者用垃圾请求灌满表。
- **nonce 按设备指纹记账，验签通过即消耗，与设备是否已登记无关，也与这次请求成功与否无关。**
  也就是说：一次失败的注册（用户名被占）、一次密码错误的登录，同样会消耗掉它的 nonce。
  客户端必须**每次请求生成新 nonce**；重试时原样重发签名会被判 `nonce_replayed`，
  这时应当用新的时间戳与 nonce 重新签名，而不是重试同一个字节序列。
  （早期实现把 nonce 写在注册/登录事务里，导致失败请求的 nonce 永不落库、可被无限重放。）
- 支付上报的 `event_id` 是幂等键：重复上报回放既有结论，**不会产生第二次打印**。

## 端点

### 公开

#### `GET /healthz`

```json
{"data": {"status": "ok", "database": "ok", "migration_version": 3, "time": 1790000000}}
```

数据库不可用时返回 `503`，`status` 为 `degraded`。

#### `GET /api/v1/meta`

下发前端需要的能力清单与枚举，避免前端硬编码取值。

```json
{"data": {
  "app": "payprint",
  "registration_open": true,
  "supported_paper_sizes": ["A4","A5","A3","Letter"],
  "supported_n_up": [1,2,4,6,9],
  "supported_duplex": [{"value":"one_sided","label":"单面"}, ...],
  "supported_color_modes": [...],
  "supported_orientations": [...],
  "supported_qualities": [...],
  "supported_channels": ["wechat","alipay"],
  "payment_report_sources": ["collector_api","wechat_android"],
  "order_ttl_seconds": 900,
  "signature_skew_seconds": 300,
  "signing_scheme": "payprint/v1",
  "upload_max_bytes": 52428800,
  "order_statuses": [{"value":"awaiting_release","label":"待确认打印","tone":"warning"}, ...],
  "max_copies": 999,

  "auto_release_on_payment": false,
  "require_payment_verification": false,
  "release_ttl_seconds": 1800
}}
```

最后三项是**打印放行策略**，客户端据此决定要不要显示"确认打印"入口：

| 字段 | 含义 |
|---|---|
| `auto_release_on_payment` | `false`（默认）表示支付确认后必须由用户手动确认才出纸 |
| `require_payment_verification` | `true` 表示确认打印前会先做一次官方支付核验，用户会多等几秒 |
| `release_ttl_seconds` | "待确认打印"的存活时间；超时订单会转成待退款。`0` 表示不回收 |

### 账号与设备

#### `POST /api/v1/auth/register`：设备签名

```json
{"username": "alice", "password": "至少8位"}
```

设备信息通过签名头传递，`X-Device-Public-Key` **必填**。

成功 `201`：

```json
{"data": {
  "token": "...", "expires_at": 1790600000,
  "user":  {"id":"...","username":"alice","role":"user","status":"active","total_spent_fen":0,"created_at":1790000000},
  "device": {"id":"...","label":"我的设备","platform":"android","app_version":"1.0.0","status":"active","status_text":"已绑定","tone":"success","bound_at":1790000000,"last_seen_at":1790000000,"created_at":1790000000,"current":true}
}}
```

> **首个注册账号（bootstrap）例外**：空库后的第一个注册账号自动成为管理员，**不绑定设备**，
> 因此它的响应里**没有 `device` 字段**，令牌也是一条无 `device_id` 的会话。该指纹保持
> 未被占用，同一台机器之后仍可注册成员账号。管理员在用户端一律 `403 admin_cannot_consume`，
> 请改从 `/admin` 使用（见 [管理员](#管理员)）。

失败：

| 状态 | code | 场景 |
|---|---|---|
| 409 | `device_already_used` | 该设备已在库中（参与过任何**成员**账号）。管理员解绑会把这台设备彻底释放，那之后可再注册 |
| 409 | `username_taken` | 用户名被占用 |
| 403 | `device_key_mismatch` | 同一指纹换了一把密钥 |
| 403 | `device_revoked` | 设备被管理员停用 |
| 403 | `registration_closed` | 未开放注册 |
| 400 | `validation_error` | 用户名或密码不合规 |

#### `POST /api/v1/auth/login`：设备签名

```json
{"username": "alice", "password": "..."}
```

设备已在库中时 `X-Device-Public-Key` 可省略（服务端用已固定的公钥验签）；
设备首次登录（`AllowNew`）时必须提供。

成功 `200`，结构与注册相同。

| 状态 | code | 场景 |
|---|---|---|
| 401 | `invalid_credentials` | 用户名不存在或密码错误（两者返回同一码，避免用户名枚举） |
| 401 | `signature_expired` | 时间戳超出窗口 |
| 409 | `nonce_replayed` | nonce 重复 |
| 409 | `device_bound_to_other_account` | 该设备已绑定其他账号 |
| 403 | `device_key_mismatch` / `device_revoked` / `account_banned` | |
| 403 | `invalid_public_key` | 设备已解绑，需重新提供公钥 |

> **人工解绑后的行为**：解绑 = **彻底释放**，这台硬件在服务端不再存在：公钥固定、
> 归属、会话与重放记录全部清空，指纹也让了出去。因此它下次**注册或登录都会被当成
> 一台全新设备**（恢复出厂后 Keystore 换新密钥也不需要任何额外处理），旧的设备行
> 只作为订单/到账事件的历史锚点保留，任何接口都不再返回它。

#### `POST /api/v1/auth/logout`：会话

请求体为空。返回 `{"data": {"ok": true}}`。

#### `GET /api/v1/me`：会话

```json
{"data": {
  "user": {...}, "device": {...}, "total_spent_fen": 600,
  "payment": {"report_path":"/api/v1/payment/report","report_source":"wechat_android","channels":["wechat","alipay"]}
}}
```

管理员会话访问这个接口会返回 `403 admin_cannot_consume`：管理员不参与消费流程。
收款端也不在这里，它没有会话（见 [收款凭据与到账匹配](#收款凭据与到账匹配)）。

#### `GET /api/v1/devices`：会话

返回当前账号名下的设备数组，`current` 标记本次请求的发起设备。

> **设备解绑只能由管理员执行**（`POST /api/v1/admin/devices/{deviceID}/release`）。
> 用户端**不提供自助解绑**：设备在库期间只能绑定一个成员账号，这是防刷号 / 防冒用的关键，
> 解除权收归运维并留痕（见 [管理员](#管理员)）。设备丢失、恢复出厂、换机、
> 二手转让都请联系管理员。

管理员解绑是**彻底释放**，一个事务里做完四件事并写服务端日志：

1. 删除该设备的全部会话（不是软吊销，行直接消失，令牌立刻失效）；
2. 清空该指纹的重放账本（`device_nonces`）；
3. 把设备退成 `status=released`：清空归属与公钥固定、清掉设备描述字段，并把**指纹让出去**
   （行上改成一个墓碑值）；
4. 保留该行本身：订单与到账事件的外键指着它，它还是"什么时候、因为什么被释放"的留痕。

因此解绑后的硬件**下一次注册或登录都会被识别为新设备**：注册会新建账号并绑定一台新设备，
登录会新建一台设备并绑定到原账号。已释放的行不再是设备，`GET /api/v1/admin/devices` 与
用户侧的 `GET /api/v1/devices` 都不会返回它。释放不可撤销（指纹已经不在系统里了）。

副作用要说清楚：订单记的是**下单那台设备**的 ID，而重登后是一台新设备，所以解绑前该设备
留下的"待确认打印"订单无法再由它确认，会按确认时限走超时退款。

管理员**停用**（`POST /api/v1/admin/devices/{deviceID}/block`，`status=revoked`）是另一种处置：
设备在停用期间无法登录、其签名请求一律被拒，它同样会让该设备原有的会话立刻失效。与解绑不同
的是**停用保留归属与公钥固定**，因此它是可逆的，`POST /api/v1/admin/devices/{deviceID}/restore`
会把它放回停用前的状态：有归属的回到 `active`（用户重新登录即可继续使用），没有归属的
（早期版本停用时会清空归属，`status=unbound`）需要重新登录绑定账号，且它仍然占着自己的指纹；
已释放的设备不能被停用或恢复。

管理端的设备列表（`GET /api/v1/admin/devices`）每条都带 `user_id` / `username`，**按账号分组
展示**是管理面板的做法，接口本身仍是平铺数组。管理员账号不占设备，所以列表里不会出现
管理员分组；历史上被管理员占用的那批已在迁移 00011 里解绑（`status=unbound`，迁移 00013
又按新语义让出了指纹）。

**与收款无关**：收款凭据绑的是打印机，不绑设备，因此解绑设备不会影响任何收款端。

### 文件

#### `POST /api/v1/files`：仅会话（`multipart/form-data`）

| 字段 | 必填 | 说明 |
|---|---|---|
| `file` | 是 | **PDF 或图片**（JPG / PNG，按文件头判定）。图片会被转成**单页 PDF** 后入库（≤20 MB） |
| `page_count` | 否 | 页数。**仅在服务端无法自动识别页数时才会被采用**（会记 WARN），上限 5000 页；图片不走这条（页数恒为 1） |

成功 `201`：

```json
{"data": {"id":"...","name":"报告.pdf","content_type":"application/pdf","size_bytes":12345,"page_count":3,"created_at":1790000000}}
```

| 状态 | code | 场景 |
|---|---|---|
| 415 | `unsupported_file_type` | 既不是 PDF 也不是受支持的图片（按文件头判定） |
| 413 | `file_too_large` | 超过 `security.max_upload_bytes` |
| 400 | `empty_file` | 空文件 |
| 400 | `too_many_pages` | `page_count` 超过 5000 页上限 |
| 400 | `validation_error` | 无法识别页数且未提供 `page_count` |

> 该接口**不做设备签名**：multipart body 可能几十 MB，为算摘要整块读进内存不划算。
> 文件本身只对所属账号可见（读取路径内嵌归属校验）。
>
> **图片打印**：JPG / PNG 上传后会转成单页 PDF，JPEG（灰度 / RGB）以 `DCTDecode`
> 原样嵌入，PNG 解码合成白底后以 `FlateDecode` 嵌入，两者都只用 Go 标准库实现，
> 不引入第三方依赖。页数记为 `detected=1`；之后完全复用 PDF 通路
> （投递 / 预览 / 页范围 / 计费），不另开链路。

**`page_count` 是有上限的，而且是安全边界**：页数既决定价格，也决定投递时能否走
"整篇打印"优化，同时还是页范围展开的上界。因此：

- 只有**服务端自己数出来**的页数（`page_count_source = detected`）才允许走
  "整篇打印"优化；采用客户端上报值时记 `declared`，投递一定会下发 `page-ranges`，
  保证**打印范围与计费范围严格一致**。
- 上限 5000 页。超出时上传直接拒绝（`too_many_pages`），页范围请求也会被拒
  （`page_range_too_large`），页范围是逐页展开成集合的，没有上界就能用一次
  请求把服务端内存打满。注意"整篇打印"（留空 `page_range`）走的是另一条分支，
  不逐页展开，因此不受该上界影响。

#### `GET /api/v1/files/{fileID}/content`：会话

把上传原件回给它的属主，用于**打印预览**。仅属主可见（归属校验内嵌，别人的文件与
不存在的文件都返回 `404 file_not_found`）。响应是文件流（不是 JSON 信封）：
`Content-Type: application/pdf`、`Content-Disposition: inline`，并支持 `Range`。

> 落盘的一律是 PDF（图片在上传阶段已转成单页 PDF），所以预览统一是 PDF。
> 前端因为要带 `Authorization` 头，必须先 `fetch` 成 Blob 再交给 `<iframe>`，
> 直接把接口地址塞进 `<iframe src>` 是带不上令牌的。

#### `GET /api/v1/files?limit=20`：会话

最近上传的文件列表。

### 打印机

#### `GET /api/v1/printers?live=true`：会话

`live=true` 时会逐个探测打印机实时状态（打印机数量应为个位数，探测是串行的）。

```json
{"data": [{
  "id":"...","code":"P01","name":"默认打印机","location":"自助打印点",
  "status":"online","status_text":"在线","tone":"success",
  "price_single_bw_fen":100,"price_single_color_fen":300,
  "price_duplex_bw_fen":200,"price_duplex_color_fen":600,
  "price_single_bw_text":"1.00","price_single_color_text":"3.00",
  "price_duplex_bw_text":"2.00","price_duplex_color_text":"6.00",
  "supports_color":true,"supports_duplex":true,"paper_sizes":["A4","A3"],"max_copies":99,
  "online_windows":[{"days":[1,2,3,4,5],"start":"08:00","end":"22:00"}],
  "online_windows_text":"周一至周五 08:00-22:00","within_schedule":true,
  "live":{"reachable":true,"state":"idle","state_reasons":["none"],"state_message":"","accepting_jobs":true,"make_and_model":"..."}
}],"total":1,"limit":1,"offset":0}
```

注意：**不会回显 `cups_uri`**（内网地址，对客户端没有意义）。该字段名沿用历史命名，
值可以是任意 IPP 打印机地址：直连无线打印机时形如 `ipp://<打印机IP>/ipp/print`，
走 CUPS 网关时形如 `ipp://<网关IP>:631/printers/<队列名>`。

#### 在线时段（`online_windows`）

打印机可以只在某些时段营业：`online_windows` 是一串每周时段，
`{"days":[1,2,3,4,5],"start":"08:00","end":"22:00"}`（`days` 用 ISO 工作日编号，
**周一 = 1 ... 周日 = 7**；`end` 早于 `start` 表示跨过午夜，此时 `days` 指开始的那一天）。

- **空数组 = 全天在线**，这是默认值，也是没有配置过的打印机的行为。
- `within_schedule` 是"此刻是否在时段内"；`online_windows_text` 是可直接展示的文案
  （如「周一至周五 08:00-22:00」或「全天在线」）。
- 时段外该打印机**按离线对待**：不能下单（`printer_closed`）、不能确认打印
  （同样 `printer_closed`）；已经确认、但还没投递给打印机的任务会一直等到开门再投递
  （订单停留在 `queued`，任务保持 `pending`）。
- 判定用的是**服务器本地时间**，时间区间为**含开始、不含结束**。
- `POST /api/v1/admin/printers` 是**覆盖更新**（与 `paper_sizes` 等字段一致）：
  请求里省略 `online_windows` 等于清空时段、回到全天在线，改别的字段时请把它一起带上。

#### `GET /api/v1/printers/{printerID}/status`：会话

返回上文的 `live` 对象。探测失败时 `reachable=false` 且 `error` 带原因，
**不返回错误状态码**（探测失败是正常状态，不是请求失败）。

### 订单

#### `POST /api/v1/orders`：会话

身份是**账号**：订单挂在 `user_id` 上，可选的设备信息只作为快照写进订单审计
（审计日志见 [管理员](#管理员) 一节），不影响下单，也不决定谁能确认打印。

```json
{
  "file_id": "...",
  "printer_id": "...",
  "title": "可选，默认取文件名",
  "is_test": false,
  "params": {
    "copies": 2, "duplex": "one_sided", "color_mode": "monochrome",
    "paper_size": "A4", "page_range": "1-3,7", "n_up": 1,
    "orientation": "portrait", "quality": "normal", "fit_to_page": true
  }
}
```

`params` 字段可省略（按默认值补齐）：

| 字段 | 取值 | 默认 |
|---|---|---|
| `copies` | 1–999 | `1` |
| `duplex` | `one_sided` / `long_edge` / `short_edge` | `one_sided` |
| `color_mode` | `monochrome` / `color` | `monochrome` |
| `paper_size` | `A4` / `A5` / `A3` / `Letter` | `A4` |
| `page_range` | 空 = 全部；支持 `3`、`2-5`、`8-`、逗号组合 | 空 |
| `n_up` | 1 / 2 / 4 / 6 / 9 | `1` |
| `orientation` | `portrait` / `landscape` | `portrait` |
| `quality` | `draft` / `normal` / `high` | `normal` |
| `fit_to_page` | bool | `true` |

**金额由服务端计算并固化**，客户端提交的任何金额都不参与。计价口径（**按「张」**，
单价随规格分四档）：

```
pages_total     = 选中页数（已按页范围裁剪）
sides_per_copy  = ceil(pages_total / n_up)
sheets_per_copy = 双面 ? ceil(sides/2) : sides
total_fen       = 规格单价(每张) × sheets_per_copy × copies

规格单价 = 按 (单双面, 黑白/彩色) 取四档之一：
           price_single_bw_fen / price_single_color_fen /
           price_duplex_bw_fen / price_duplex_color_fen
```

> **免费单**：打印机单价为 0、或该账号被管理员开启「免费打印」时，金额记 0，
> 直接进入 `awaiting_release`，仍然需要用户手动点「确认打印」才出纸。
> 免费单不落在待支付区，因此不参与「同打印机同金额唯一」的约束。

成功 `201` 返回订单视图（见下）。

| 状态 | code | 场景 |
|---|---|---|
| 404 | `file_not_found` | 文件不存在或不属于当前账号 |
| 404 | `printer_not_found` | 打印机不存在或已停用 |
| 409 | `printer_offline` | 打印机离线 |
| 409 | `printer_closed` | 该打印机配置了在线时段，而此刻不在时段内（响应文案里带时段） |
| 400 | `param_unsupported` | 参数组合不被该打印机支持（纸张 / 彩色 / 双面 / 份数上限）；**打印机未配置纸张也走这里** |
| 400 | `nothing_to_print` | 页范围选择结果为空 |
| 409 | `conflict` | 订单状态异常 |
| 409 | `amount_duplicate` | 该打印机待支付区**已有相同金额**的订单（「金额与其他用户重复，请等待」） |

> **纸张严格以打印机配置为准**：可选纸张 = 该打印机 `paper_sizes`。打印机没配置纸张时
> **拒绝下单**（`param_unsupported`，提示"尚未配置可用纸张"），而不是回退到"全部纸张"
>，否则用户以为选的是 A4，实际却可能打出别的规格。管理面板保存打印机时必须给出至少一种纸张。

> 单价为 0 的打印机、以及被管理员开启「免费打印」的账号，都会**建单即免支付**：
> 金额记 0 并直接进入 `awaiting_release`，省掉一个永远无法完成的待支付订单。
> 这类订单不落在待支付区，因此不受 `amount_duplicate` 约束。

#### `GET /api/v1/orders?limit=20&offset=0`：会话

列表响应，元素为订单视图（列表项不含 `params` / `cost` 明细）。

#### `GET /api/v1/orders/{orderID}`：会话

订单视图，含 `params` 与 `cost` 明细。归属校验内嵌在查询里，
别人的订单返回 `404 order_not_found`（与不存在不可区分）。

#### `POST /api/v1/orders/{orderID}/cancel`：会话

仅 `awaiting_payment` 可取消。已收到钱的订单**不能**在这里取消（那会变成
"订单没了但钱还在账上"），返回 `409 order_not_payable`，需走退款流程。

#### `POST /api/v1/orders/{orderID}/print`：会话

**确认打印：唯一能让打印机出纸的用户入口。** 请求体为空。

成功 `200` 返回订单视图，`status = "queued"`、`job_status = "pending"`、
`just_released = true`。

放行前服务端按顺序校验（任何一步失败都**不会**创建打印任务）：

| 顺序 | 检查 | 失败返回 |
|---|---|---|
| 1 | 会话有效（中间件完成） | 401 |
| 2 | 订单存在且属于当前账号（查询内嵌 `user_id`，越权与不存在不可区分） | 404 `order_not_found` |
| 3 | 已放行过 → 幂等返回现状（双击 / 重放安全） | 200 |
| 4 | 状态必须是 `awaiting_release`（等价于"支付已通过完整校验链"） | 409 `order_not_releasable` |
| 5 | 还在确认时限内（`paid_at + release_ttl`）；超时就地关闭为待退款 | 409 `release_window_closed` |
| 6 | 打印机此刻真的可用（数据库状态 + **在线时段** + **实时探测打印机**） | 409 `printer_closed` / `printer_offline` |
| 7 | 支付核验（配置了官方查单则同步查一次） | 409 `payment_unverified` |

几点说明：

- 放行不看设备：订单属于**账号**，同一账号换一台设备也能确认自己的订单。
- 第 6 步失败**不改变订单状态**，订单仍是"待确认打印"，用户可以等打印点恢复（或营业时段
  重新开放）后重试。
- 第 7 步只做一次同步查单：`paid` 且金额一致才放行；结论矛盾（未支付/已关闭/已退款）
  直接拒绝并把订单转成 `disputed`；查不通时按 `REQUIRE_PAYMENT_VERIFICATION` 决定。
- 只有在全部通过之后，才在**同一个事务**内创建打印任务并把订单推进到 `queued`。

### 订单视图

```json
{"data": {
  "id": "...", "order_no": "OC20261009153012A1B2",
  "status": "awaiting_payment", "status_text": "待支付", "tone": "neutral",
  "amount_fen": 600, "amount_text": "6.00", "currency": "CNY",
  "title": "报告.pdf", "is_free": false,

  "printer_id": "...", "printer_code": "P01", "printer_name": "默认打印机", "printer_location": "自助打印点",
  "file_id": "...", "file_name": "报告.pdf", "file_page_count": 3,

  "params": {"copies":2,"duplex":"one_sided","color_mode":"monochrome","paper_size":"A4","page_range":"","n_up":1,"orientation":"portrait","quality":"normal","fit_to_page":true},
  "cost": {"unit_price_fen":100,"sides_per_copy":3,"sheets_per_copy":3,"copies":2,"pages_total":3,
           "total_fen":600,"currency":"CNY","color_mode":"monochrome","n_up":1,"duplex":"one_sided",
           "page_ranges":[{"from":1,"to":3}],"explanation":"¥1.00/张 × 3 张/份 × 2 份 = 6.00 元"},

  "job_status": "", "job_status_text": "", "job_tone": "",
  "job_state": "", "cups_job_id": 0, "job_attempts": 0, "job_error": "",

  "created_at": 1790000000, "updated_at": 1790000000, "expires_at": 1790000900,
  "paid_at": 0, "completed_at": 0, "close_reason": "",

  "can_pay": true, "can_cancel": true, "can_release": false,
  "remaining_seconds": 900
}}
```

三个动作标志由服务端判定，前端不要自己推导状态机：

| 状态 | `can_pay` | `can_cancel` | `can_release` |
|---|---|---|---|
| `awaiting_payment` | true | true | false |
| `awaiting_release` | false | false | **true** |
| `queued` / `printing` / `completed` | false | false | false |

`remaining_seconds` 的含义随状态变化：待支付是支付剩余时间，
待确认打印是**确认打印**的剩余时间（`paid_at + release_ttl`）。
其余状态为 `0`，客户端据此隐藏倒计时。

`just_released` 只在"确认打印"这个动作的响应里出现，用于给出针对性提示。

### 支付

#### `POST /api/v1/payment/report?source=wechat_android`：会话

身份是**账号会话**：到账和订单一样挂在账号上，服务端按 `order.user_id` 判归属。
（现场那台替整个打印机上报的机器走的是收款凭据，见下一节。）

请求体（`wechat_android` 触发源）：

```json
{
  "event_id": "and-1730000000-ab12cd34ef56",
  "channel": "wechat",
  "order_no": "可选：带上即精确命中",
  "out_trade_no": "微信交易号（有就带）",
  "amount_fen": 600,
  "paid_at": 1790000100,
  "timestamp": 1790000105,
  "nonce": "可选",
  "device_fingerprint": "可选"
}
```

必填只有 `event_id` 与 `amount_fen`，但**这条接口必须带 `order_no`**：
账号上报说的是"我这一单付了"，归属只能落在它自己的订单上。
"只报金额与时间、由服务端匹配"是另一条路，属于收款凭据，见下面一节。

> `timestamp` / `nonce` 只用于留痕与幂等；`device_fingerprint` 是上报方的自述，
> 仅记日志，不参与任何判定（这条路径的身份是账号）。

响应 `200`：

```json
{"data": {
  "accepted": true, "duplicate": false,
  "order_id": "...", "order_no": "OC20261009153012A1B2",
  "order_status": "awaiting_release", "status_text": "待确认打印",
  "job_queued": false, "can_release": true, "verify_pending": true
}}
```

`order_no` 是**服务端匹配出来的订单号**（上报体里没带时也一定会回填），
客户端应记录它以便排障。

> **`job_queued: false` 是默认行为**：上报成功只代表支付被确认，
> 订单停在"待确认打印"，**不会**产生打印任务。客户端应据此引导用户去点
> 「确认打印」。只有在服务端开着「到账即出纸」（`auto_release_on_payment`）时它才是 `true`。

校验链（顺序即优先级，任何一步失败都在**状态变更之前**返回）：

| 顺序 | 检查 | 失败返回 |
|---|---|---|
| 1 | `event_id` 幂等 | `200` + `duplicate: true`（回放既有结论，**先于匹配**） |
| 2 | 会话有效（中间件完成） | 401 |
| 3 | 定位订单 + 归属授权（见下） | 403 / 404 / 409 |
| 4 | 支付此前已确认（`awaiting_release` 或已放行） | `200` + `duplicate: true` |
| 5 | 状态为可支付 | 409 `order_not_payable` |
| 6 | 订单未超时 | 409 `order_expired`（同时把订单关闭为 `expired`） |
| 7 | 金额整数分比对 | 409 `amount_mismatch` |
| 8 | 渠道合法 | 400 `unknown_channel` |

第 3 步"定位订单 + 归属授权"有两条路径，**它先于"是否已确认过支付"这一步**，
所以越权上报不会因为订单已支付就变成一次"幂等成功"：

| 发起方 | 规则 | 失败返回 |
|---|---|---|
| 账号 | 订单必须属于这个账号（这条接口里 `order_no` 必填） | 403 `order_not_owned` |
| 收款凭据 | 走另一个入口，见下一节 | — |

全部通过后，在**同一个事务**内完成：写事件 → 改订单为 `awaiting_release` →
写记账流水 → 创建官方查单任务（**不含**创建打印任务）。

被拒的上报会以 `status=rejected` 落库留痕（写失败只记日志，不影响拒绝结果）。
其中 `payment_unmatched` / `payment_ambiguous` / `order_expired` 三类意味着
**"钱可能已经收了但没出纸"**，必须人工跟进，不能当作普通参数错误忽略。

#### `POST /api/v1/collector/payment/report`：收款凭据

收款端唯一能调的接口。认证方式是 `X-Collector-Token: <明文令牌>`，
**没有会话、没有设备签名、没有账号**。

```json
{
  "event_id": "and-1730000000-ab12cd34ef56",
  "channel": "wechat",
  "amount_fen": 600,
  "paid_at": 1790000100,
  "duplex": "one_sided",
  "color_mode": "monochrome",
  "unit_price_fen": 100
}
```

`order_no` 可带可不带：带上是精确命中（更强、歧义更少），不带则由服务端在
**该凭据绑定的打印机**内按金额 + 时间匹配，候选必须唯一。

`duplex` / `color_mode` / `unit_price_fen` 也都是**可选**的：带上后，服务端会在
金额候选里再按规格与每张单价核对一次；不带则只按金额匹配（在「同打印机同金额唯一」
的前提下，金额本身通常已足够定位）。

| 状态 | code | 场景 |
|---|---|---|
| 401 | `collector_credential_invalid` | 令牌缺失、无效或已吊销。**不落任何事件**：这不是一笔可疑的到账，而是一次没有身份的调用，让它进对账记录只会污染人工待办 |
| 403 | `order_not_owned` | 带了 `order_no`，但那笔订单不属于该凭据绑定的打印机 |
| 409 | `payment_ambiguous` | 同打印机同金额候选不止一笔，转人工 |
| 409 | `payment_unmatched` | 打印机内找不到可对应的订单 |
| 409 | `order_expired` | 同金额订单已超时，响应里会指认订单号 |

响应体与 `POST /api/v1/payment/report` 完全一致。

> 事件的 `source` 记为 `collector_api`，与设备上报（`wechat_android`）分开记账：
> 事后对账时"这条到账是谁报的"必须一眼看得出来。

### 记账

#### `GET /api/v1/ledger?limit=50`：会话

```json
{"data": {
  "total_spent_fen": 600,
  "entries": [{
    "id":"...","order_id":"...","entry_type":"payment","type_text":"支出","tone":"neutral",
    "amount_fen":600,"amount_text":"6.00","total_spent_fen":600,"channel":"wechat",
    "memo":"OC20261009153012A1B2 支付到账（wechat_android）","created_at":1790000100
  }]
}}
```

账号**不持有余额**：每单独立支付。`total_spent_fen` 是记账累计值（支出为正、退款为负），
即这个账号一共花掉多少钱；它只用于对账展示，不参与下单校验。每条流水的 `total_spent_fen`
是这一笔之后的累计值。

### 管理员

`/api/v1/admin/*` 有两条**等价**的准入路径：

| 路径 | 谁用 | 受设备限制 |
|---|---|---|
| 管理员会话：`Authorization: Bearer <token>`，且账号 `role=admin` | 管理面板 | **不受** |
| 运维令牌：`X-Admin-Token: <token>` | 脚本 / CI / 首次建号 | 不涉及 |

两条路都会把"是谁在操作"带进上下文：管理动作因此能在进程日志里落到具体账号
（走令牌时记为 `admin-token`，走会话时是账号名）。
**没配运维令牌不影响面板**；反过来，面板里建不出第一个管理员，只能靠令牌建。

#### `POST /api/v1/admin/login`：公开

```json
{"username": "owner", "password": "..."}
```

**不要求设备签名，也不创建、不绑定任何设备**：管理员会从自己的电脑、手机或打印点的
机器上进面板。响应：

```json
{"data": {"token": "...", "expires_at": 1790000000,
          "admin": {"id": "...", "username": "owner", "role": "admin", "status": "active"}}}
```

| 状态 | code | 场景 |
|---|---|---|
| 401 | `invalid_credentials` | 用户名不存在或密码错误（同一个错误，不泄露账号是否存在） |
| 403 | `not_admin_account` | 密码正确但该账号不是管理员（同样只在密码校验之后返回，避免枚举） |
| 403 | `account_banned` | 账号已封禁 |

#### `GET /api/v1/admin/me`：管理员

面板启动时用它自检令牌。走运维令牌时是 `{"via_token": true}`（有权限，但没有账号身份），
走会话时带上 `admin`。

#### `POST /api/v1/admin/logout`：管理员

吊销当前管理员会话；用运维令牌进来时没有会话可吊销，同样返回成功。

#### 端点一览

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/admin/orders?limit=50` | 全站最近订单（带打印机与归属账号） |
| `GET` | `/api/v1/admin/devices?limit=100` | 全站设备（带归属账号） |
| `GET` | `/api/v1/admin/users?limit=200` | 账号列表 |
| `GET` | `/api/v1/admin/stats` | 账号数量、任务状态计数与**当前生效**的运行参数 |
| `POST` | `/api/v1/admin/users` | 建号，body `{"username","password","role"}` |
| `POST` | `/api/v1/admin/users/{userID}/ban` | 封禁账号（理由必填），body `{"reason":"..."}` |
| `POST` | `/api/v1/admin/users/{userID}/unban` | 解封账号（理由必填），body `{"reason":"..."}` |
| `POST` | `/api/v1/admin/users/{userID}/free-print` | 免费打印开关，body `{"enabled":true,"reason":"..."}`（理由必填） |
| `GET` | `/api/v1/admin/settings` | 运行时可改的配置项（含当前值、默认值与来源） |
| `PUT` | `/api/v1/admin/settings` | 修改配置，body `{"set":{...},"reset":[...]}` |
| `POST` | `/api/v1/admin/printers/probe` | 连通性探测，body `{"cups_uri":"..."}` 或 `{"printer_id":"..."}` |
| `GET` | `/api/v1/admin/backup` | 导出整站备份（zip 文件流，见下） |
| `GET` | `/api/v1/admin/restore/pending` | 是否有待应用的还原备份 |
| `POST` | `/api/v1/admin/restore` | 上传备份（multipart `file`），校验通过后暂存 |
| `POST` | `/api/v1/admin/restore/cancel` | 丢弃待应用的备份 |
| `GET` | `/api/v1/admin/collectors?limit=100` | 收款设备（凭据）列表（**不含明文令牌**） |
| `POST` | `/api/v1/admin/collectors` | 新增收款设备（签发凭据），body `{"printer_code","label"}`，**响应含一次性明文令牌** |
| `DELETE` | `/api/v1/admin/collectors/{collectorID}` | 吊销收款设备凭据（软删除），body `{"reason":"..."}` |
| `DELETE` | `/api/v1/admin/collectors/{collectorID}/purge` | **彻底删除**收款设备凭据（硬删除），body `{"reason":"..."}` |
| `POST` | `/api/v1/admin/devices/{deviceID}/release` | 人工解绑 = **彻底释放**（换机 / 恢复出厂 / 二手转让的救济通道）：清空归属、公钥固定、会话与重放记录，并让出指纹；这台硬件下次注册或登录按新设备处理。不可撤销，body `{"reason":"..."}` |
| `POST` | `/api/v1/admin/devices/{deviceID}/block` | 停用设备（保留归属与公钥固定，可恢复；停用期间无法再登录），body `{"reason":"..."}` |
| `POST` | `/api/v1/admin/devices/{deviceID}/restore` | 恢复被停用的设备（有归属回 `active`，无归属回 `unbound`），body `{"reason":"..."}` |
| `GET` | `/api/v1/admin/payment-events?status=&limit=50&offset=0` | 到账事件（`?status=unresolved` 是"钱可能收了但没出纸"的待办队列） |
| `GET` | `/api/v1/admin/audits?limit=100&offset=0` | 审计日志（订单日志）：账号、设备快照、订单编号、金额与打印内容 |
| `GET` | `/api/v1/admin/audits/export` | 导出**全部**审计日志（CSV 文件流，带 BOM） |
| `POST` | `/api/v1/admin/orders/{orderID}/retry` | 重推打印任务（只对 `pending` / `failed` / `needs_review` 有效） |
| `POST` | `/api/v1/admin/orders/{orderID}/refund` | 人工退款，body `{"reason":"..."}` |
| `GET` | `/api/v1/admin/printers` | 全部打印机（含已停用） |
| `POST` | `/api/v1/admin/printers` | 新增或按 `code` 更新打印机（**覆盖更新**；`online_windows` 见「打印机」一节） |
| `POST` | `/api/v1/admin/printers/{printerID}/status` | 切换状态，body `{"status":"online"}` |
| `DELETE` | `/api/v1/admin/printers/{printerID}` | **级联硬删除**打印机及其全部关联数据（订单/任务/记账/到账事件/核验/收款凭据），body 可选 `{"reason":"..."}` |

人工退款会写一条负数记账流水并把订单置为 `refunded`，与状态变更同事务；
`(order_id, entry_type)` 唯一约束保证重复退款被拦下。

**退款会同时拦下还没投递给打印机的打印任务**（任务置为 `cancelled`）。
允许退款的状态里包含 `queued` / `printing`，那些订单恰好是有任务的；少了这一步就会出现
"钱退给用户了、纸照样出来"。如果任务**已经**交给打印机（`submitting` / `submitted` /
`printing`），退款照做（钱不能压着），但任务状态不会被改写成"已取消"，
那只会在事后误导排障的人，服务端会记一条 ERROR 日志。

#### `POST /api/v1/admin/orders/{orderID}/retry`

只对**真的停下来了**的任务有效：`pending` / `failed` / `needs_review`。
`submitting` / `submitted` / `printing` / `completed` / `cancelled` 一律返回
`409 conflict`，把在途任务置回待处理等于把同一份文件再投一次，直接多出一份纸。
`submitting` 且确实卡住（进程中断）的情况由后台按租约自动回收，不由人工重推。

#### `GET /api/v1/admin/payment-events`

到账事件列表，按时间倒序。`status` 可选：

| 值 | 含义 |
|---|---|
| 留空 | 全站最近事件 |
| `accepted` | 已放行 |
| `rejected` | 协议层面被拒（渠道不认识、不是本打印机订单等），看一眼即可 |
| `duplicate` | 重复上报，按幂等处理 |
| `unresolved` | **钱可能已经收到，但没有任何订单因此被放行**，需要人工核销或退款 |

`unresolved` 是运维每天要清零的队列，包含：

| 落库原因 | 场景 |
|---|---|
| `payment_unmatched` | 到账在该打印机找不到任何待支付订单 |
| `payment_ambiguous` | 同打印机同金额的候选订单不止一笔，无法确定归属 |
| `order_expired` | 到账晚于订单的支付时限（`order_id` 会一起带出来，便于退款） |
| `amount_mismatch` | 到账金额与所指名订单的金额不一致 |

```json
{"data": [{
  "id":"...","event_id":"and-...","source":"wechat_android","channel":"wechat",
  "order_id":"...","order_no":"OC2026...",           // 没匹配到订单时 order_id 缺省
  "user_id":"...","username":"alice",                // 到账挂在账号上；没归属时两者缺省
  "out_trade_no":"","amount_fen":600,"amount_text":"6.00",
  "status":"unresolved","status_text":"待人工处理","tone":"warning",
  "reason":"到账时间已超出订单 OC2026... 的支付时限",
  "reported_at":1790000100,"created_at":1790000105
}],"total":1,"limit":50,"offset":0}
```

这个入口之所以必须存在：`order_id` 为空的到账**不属于任何订单**，
通过订单与记账接口都看不到它（没能归属到订单的到账连账号也没有，`user_id` 为空）。

#### `GET /api/v1/admin/audits?limit=100&offset=0`：管理员

**审计日志 = 订单日志。** 一条订单一条记录，内容是下单那一刻的快照：

| 字段 | 含义 |
|---|---|
| `user_id` / `username` | 账号（身份核心） |
| `device_label` / `device_fingerprint` | 下单那台设备的快照 |
| `order_id` / `order_no` | 订单编号 |
| `amount_fen` / `amount_text` | 金额（下单时固化的应付金额，免费单为 0） |
| `file_name` | 打印文件名 |
| `color_mode` / `color_text` | 黑白 / 彩色 |
| `duplex` / `duplex_text` | 单面 / 双面（长边 / 短边） |
| `copies` | 打印份数 |
| `total_pages` | 打印总页数（每份面数 × 份数） |
| `created_at` | 下单时间 |

三点必须说清楚：

- **只有订单**：解绑设备、封禁账号、改运行参数这类动作不在审计日志里。它们各自把原因
  留在自己那一行上（账号行的 `ban_reason`、设备行的 `revoke_reason`、记账流水的摘要），
  服务端日志里也有记录，不需要第二份。
- **全部是快照**：设备被解绑或删除（指纹会被让出）、打印机被级联删除（订单随之消失）、
  账号改名，都不会改动或抹掉已经写下的记录：表上没有任何外键，正是为了这一点。
- **写入在下单的同一个事务里**：写不进去就一起回滚，因此不存在"有订单、没审计"的状态。

#### `GET /api/v1/admin/audits/export`：管理员

把**全部**审计日志导出成 CSV（`text/csv; charset=utf-8` + `Content-Disposition: attachment`，
带 UTF-8 BOM 以便 Excel 直接打开）。

列依次为：账号、设备名称、设备指纹、订单编号、金额(元)、打印文件名、色彩、单双面、
打印份数、打印总页数、下单时间。服务端分页取数、边翻边写，内存占用与记录条数无关。

管理面板的「审计日志」页就是这两个接口：预览最近 100 条 + 导出全部。

#### `POST /api/v1/admin/users`

自助注册（`/auth/register`）只能建普通账号。**管理员只能从这里建**，
否则任何人都能把自己提成管理员。

| 字段 | 说明 |
|---|---|
| `role` | 留空 = `user`；建管理员必须显式传 `admin`（避免"忘了传所以悄悄提权"） |

> 收款端**不在这里**：它是 API 凭据，没有账号（见下面的「收款设备」）。

#### `GET /api/v1/admin/collectors`

收款设备（凭据）列表。**不含明文令牌**，服务端只有散列，想回显也回显不出来。

```json
{"data": [{"id":"...","printer_id":"prn_a","printer_code":"A","printer_name":"一楼",
  "label":"A 点收款机","created_at":1790000000,"last_used_at":1790000100,
  "revoked":false,"status_text":"生效中","tone":"success"}],
 "total":1,"limit":100,"offset":0}
```

#### `POST /api/v1/admin/collectors`

签发一份收款凭据。`printer_code` **必填**：凭据必须归属某个打印机，否则它的到账匹配
就没有边界，可能把钱算到别的打印机头上；`label` 留空时用「打印机名 + 收款端」。

响应 `201`，**带一次性明文令牌**：

```json
{"data": {"id":"...","printer_code":"A","label":"A 点收款机","revoked":false,
          "token":"<明文，只此一次>"}}
```

| 状态 | code | 场景 |
|---|---|---|
| 404 | `printer_not_found` | 打印机不存在 |
| 400 | `validation_error` | `printer_code` 为空 |

#### `DELETE /api/v1/admin/collectors/{collectorID}`

吊销凭据（软删除，记录保留以便事后对账）。body 可选：`{"reason":"换机"}`。

| 状态 | code | 场景 |
|---|---|---|
| 404 | `collector_credential_not_found` | 凭据不存在，或此前已被吊销 |

#### `DELETE /api/v1/admin/collectors/{collectorID}/purge`

**彻底删除**凭据（硬删除，记录不再存在）。用于"打印机选错 / 重复签发"这类本就不该存在的
凭据；body 可选：`{"reason":"..."}`。行被删了，所以删除动作与原因只留在服务端日志里。

> 若只是想停用、日后还要核对有争议的到账，请用上面的**吊销**（软删除），删除之后
> `collector_credential_not_found`，列表里再也看不到它。

| 状态 | code | 场景 |
|---|---|---|
| 404 | `collector_credential_not_found` | 凭据不存在 |

> **换硬件的正确顺序**：吊销旧凭据 → 新机器填新凭据。凭据绑的是打印机，所以解绑设备、
> 换账号、重装系统都不影响收款，这正是把收款从设备上拆出来的原因。

#### `POST /api/v1/admin/users/{userID}/ban` 与 `/unban`

封禁 / 解封账号，body `{"reason":"..."}`，**理由必填**。

封禁理由落在**账号行上**（`ban_reason` / `banned_at` / `banned_by`，账号列表直接展示
"为什么被封"），解封理由记入服务端日志：这条信息比做决定的人活得更久，所以不能只写进日志。

封禁**立刻生效**：该账号登录会被拒（`account_banned`），它名下设备发来的带签名请求
也会被拒（会话绑定与设备校验都会查账号状态），所以不需要额外吊销会话。

| 状态 | code | 场景 |
|---|---|---|
| 404 | `user_not_found` | 账号不存在 |
| 400 | `validation_error` | 理由为空 |
| 409 | `conflict` | 已经是这个状态；或试图封禁**自己**；或试图封禁**最后一个启用中的管理员** |

后两条是刻意加的：面板里没有"账号体系之外"的复位通道，把最后一个管理员封掉等于永久锁死管理入口。

#### `POST /api/v1/admin/users/{userID}/free-print`

打开 / 关闭某个账号的**免费打印**，body `{"enabled":true,"reason":"..."}`，**理由必填**。

开启后该账号下单即**免支付**：金额记 0，直接进入 `awaiting_release`，仍然需要用户
手动点「确认打印」才出纸（"用户确认才出纸"这条原则不因免费而放宽）。响应 `200` 返回
更新后的账号视图（含 `free_print`）。理由记入服务端日志（开关状态本身在账号行上）。

#### `GET /api/v1/admin/backup`

导出整站备份，`Content-Type: application/zip` 的文件流（不是 JSON 信封，
这是几十 MB 起步的文件，浏览器要能直接存盘）。内容：

| 条目 | 内容 |
|---|---|
| `manifest.json` | 格式版本、数据库结构版本、创建时间、文件数 |
| `payprint.db` | 数据库一致性快照（`VACUUM INTO`，不会复制到半个事务） |
| `files/...` | 上传的全部原件 |

账号（含封禁理由）、设备绑定、订单与记账、打印机与价目、收款凭据、运行参数、订单审计
都在库文件里，网页上能看到 / 能改的一切都在这份备份里。

#### `POST /api/v1/admin/restore`

上传备份（multipart，字段名 `file`，上限 2 GiB）。**上传只做校验与解包**：

- 不是 zip、缺清单或缺库文件、格式版本不认识、数据库结构比当前服务新 → `400 validation_error`
  （错误信息里带原因）；
- 通过 → 解包进暂存区，返回 `{"staged": true, "restart_required": true, ...}`，随后服务
  优雅退出；systemd 会把它拉起来，启动时在打开数据库
  **之前**完成换装。

为什么要重启而不能原地换库：连接池里全是打开的句柄、worker 正在写，硬换等于让一半请求
读旧库、一半读新库。换下的旧数据保留为 `*.pre-restore`，给"还原错了想反悔"留一条手工退路。

| 状态 | code | 场景 |
|---|---|---|
| 400 | `validation_error` | 不是 zip / 缺清单或库文件 / 格式不认识 / 结构比当前服务新 |
| 404 | `user_not_found` |，（无） |

#### `GET /api/v1/admin/restore/pending` 与 `POST /api/v1/admin/restore/cancel`

报告是否有待应用的备份（面板据此显示横幅），以及在上传错了的时候把它丢弃。
取消是安全操作：暂存区还没碰过任何线上数据。

#### `GET /api/v1/admin/settings`

运行时可改的配置项列表。每项带当前值、内置默认值、以及**是否已被覆盖**：

```json
{"data": [{
  "key":"release_ttl","label":"确认打印时限","group":"打印放行","kind":"duration",
  "value":"20m","default":"30m","overridden":true,
  "help":"「待确认打印」的存活时间...","min":0,"max":86400
}]}
```

`value` / `default` 一律是字符串（`"true"` / `"20m"` / `"5"`），`kind` 决定面板用什么控件。

#### `PUT /api/v1/admin/settings`

```json
{"set": {"release_ttl": "20m", "auto_release_on_payment": "false"},
 "reset": ["open_registration"]}
```

- `set` 覆盖（落库），`reset` 删除覆盖（回到内置默认值）。两者可同时给：面板上「改两项、
  还原一项」是一次保存动作。
- **任何一项不合法就整体拒绝**（`400 validation_error`），不做部分写入，否则面板会出现
  「一半生效、一半没生效」，而回显的还是保存前的整组值。
- 写成功后立刻重新装载并生效，**不需要重启进程**；响应是更新后的完整列表。

刻意**不在**这张清单里的：密钥（`security.admin_token`）、安全窗口（`security.signature_skew` /
`security.nonce_ttl`）、路径类（数据库 / 存储 / 前端目录）。理由见
[`docs/DEPLOY.md`](DEPLOY.md) 的「配置参考」一节。

#### `POST /api/v1/admin/printers/probe`

探测打印机连通性，用来回答「这个地址到底通不通」。

| 请求体 | 用途 |
|---|---|
| `{"cups_uri":"ipp://192.168.1.50:631/printers/hp"}` | 探测一个**还没保存**的地址（改完先验再存） |
| `{"printer_id":"prn_a"}` | 探测已登记的那台（含已停用，停用中的打印机最需要先测通地址再启用） |

非 `ipp://` / `ipps://` 开头返回 `400`；**打不通不是错误**：返回 `200` 且
`{"reachable": false, "error": "..."}`，把原因照原样交给运维，好把「地址写错」和
「网关没起」「网络不通」分开。

#### `DELETE /api/v1/admin/printers/{printerID}`

**级联硬删除**一个打印机。这是破坏性操作：该打印机下的订单、打印任务、记账流水、到账事件、
核验任务与收款凭据都会**一并永久删除**（外键要求先删子表），历史对账数据随之消失。
若只是想停止使用，请改用状态 `disabled`（停用）。

响应回传各类被清除的行数：

```json
{"data": {"code":"P01","name":"默认打印机","deleted_orders":12,"deleted_print_jobs":12,
          "deleted_ledger_entries":12,"deleted_payment_events":10,
          "deleted_order_verifications":8,"deleted_collector_credentials":1}}
```

| 状态 | code | 场景 |
|---|---|---|
| 404 | `printer_not_found` | 打印机不存在 |

> 删除动作、清除的行数与原因会写入**服务端日志**（审计日志只记订单，不收这类处置）。
> 管理面板要求先输入打印机编码二次确认，把"删错站"挡在误触之外。

## 状态枚举

### 订单 `status`

| 值 | 文案 | 语义色 | 说明 |
|---|---|---|---|
| `awaiting_payment` | 待支付 | neutral | 等待设备上报支付消息 |
| `awaiting_release` | 待确认打印 | warning | 支付已通过完整校验链，**还没有打印任务**，等用户确认 |
| `queued` | 排队中 | primary | 用户已确认，打印任务已入队 |
| `printing` | 打印中 | primary | 已提交给打印机（仍不代表已出纸） |
| `completed` | 已完成 | success | 打印机报告作业完成 |
| `failed` | 打印失败 | danger | 免费订单或无需退款的失败 |
| `refund_pending` | 待退款 | warning | 已收钱但没出纸（含"一直不确认打印"超时），等人工退款 |
| `disputed` | 异常待核 | warning | 官方查单与设备上报矛盾 |
| `refunded` | 已退款 | warning | 已写退款流水 |
| `expired` | 已超时 | neutral | 超过支付时限 |
| `cancelled` | 已取消 | neutral | 用户主动取消 |

`awaiting_release` 用**警告色**而不是主色：钱已经收了、纸还没出，既需要用户动作、
又带超时，是最需要被注意到的一个中间态。

### 打印任务 `job_status`

任务**只在用户确认打印后才存在**，所以订单处于 `awaiting_payment` /
`awaiting_release` 时，`job_status` 是空串（不是 `pending`）。

| 值 | 文案 | 说明 |
|---|---|---|
| `pending` | 排队中 | 待投递 |
| `submitting` | 投递中 | 已被 worker 抢占 |
| `submitted` | 已入队 | 打印机已接受，**不代表已打印** |
| `printing` | 打印中 | `job-state=processing` |
| `completed` | 已完成 | `job-state=completed` |
| `failed` | 投递失败 | 终态失败 |
| `needs_review` | 待人工处理 | 重试耗尽、核验矛盾、任务多次被中断，或在途超过 `stuck_job_ttl` 一直没有进展 |
| `interrupted` | 打印中断 | 打印机把作业停住了（IPP `processing-stopped`：卡纸、缺纸、被暂停）。**不是终态**，故障清除后通常自行继续，因此仍在回查范围里 |
| `cancelled` | 已取消 | 订单被退款/异常时，任务在投递给打印机 **之前**就被就地停掉 |

`cancelled` 与 `failed` 刻意分开：`failed` 是"试过了没成功"，`cancelled` 是"有意不打了"。
混成一个状态会让排障的人分不清该查网络还是该查退款记录。只有还没投出去（`pending`）的
任务能被取消；已经交给打印机的只能记 `needs_review`，谎报成"已取消"没有意义。

### 语义状态色

`tone` 字段在前端映射为统一的颜色，列表 / 详情 / 通知三处共用同一套口径：

```
neutral 中性灰、primary 主色、success 成功、danger 危险、warning 警告
```

## 错误码全表

| code | HTTP | 含义 |
|---|---|---|
| `internal_error` | 500 | 服务内部错误（内部原因只进日志） |
| `validation_error` | 400 | 请求参数不合法 |
| `not_found` | 404 | 资源不存在 |
| `unauthorized` | 401 | 未登录或会话失效 |
| `forbidden` | 403 | 无权限 |
| `conflict` | 409 | 与当前状态冲突 |
| `invalid_credentials` | 401 | 用户名或密码不正确 |
| `username_taken` | 409 | 用户名被占用 |
| `registration_closed` | 403 | 未开放注册 |
| `account_banned` | 403 | 账号被封禁 |
| `device_already_used` | 409 | 该设备已在库中（参与过其他**成员**账号）；管理员解绑彻底释放后即可再注册（管理员不占设备） |
| `device_bound_to_other_account` | 409 | 设备已绑定其他账号 |
| `device_revoked` | 403 | 设备被停用 |
| `device_not_found` | 404 | 设备未登记 |
| `device_key_mismatch` | 403 | 设备公钥与首次登记不一致 |
| `missing_signature` | 401 | 缺少签名信息 |
| `invalid_signature` | 401 | 验签失败 |
| `signature_expired` | 401 | 时间戳超出窗口 |
| `nonce_replayed` | 409 | 请求已被处理过 |
| `invalid_public_key` | 400 | 公钥格式不正确 |
| `file_not_found` | 404 | 文件不存在或不属于当前账号 |
| `file_too_large` | 413 | 文件超过上限 |
| `unsupported_file_type` | 415 | 仅支持 PDF 与图片（JPG / PNG） |
| `empty_file` | 400 | 空文件 |
| `order_not_found` | 404 | 订单不存在 |
| `printer_not_found` | 404 | 打印机不存在或未启用 |
| `printer_offline` | 409 | 打印机离线 |
| `printer_closed` | 409 | 打印机配置了在线时段，此刻不在时段内（下单与确认打印都走这个码） |
| `order_not_payable` | 409 | 订单当前状态不可支付 |
| `order_not_releasable` | 409 | 订单不在"待确认打印"，不能放行打印 |
| `payment_unverified` | 409 | 放行前的支付核验未通过 |
| `release_window_closed` | 409 | 超过确认打印时限（订单已转待退款） |
| `order_expired` | 409 | 订单已超时 |
| `amount_duplicate` | 409 | 同打印机待支付区已有相同金额的订单（「金额与其他用户重复，请等待」） |
| `param_unsupported` | 400 | 打印机不支持该参数组合 |
| `nothing_to_print` | 400 | 页范围选择结果为空 |
| `amount_mismatch` | 409 | 上报金额与订单金额不一致 |
| `order_not_owned` | 403 | 订单不属于该账号（收款凭据则为"不属于它绑定的打印机"） |
| `unknown_channel` | 400 | 不支持的支付渠道 |
| `report_needs_order_no` | 403 | 没有收款凭据又没带 `order_no`：设备上报必须指名订单 |
| `collector_credential_invalid` | 401 | 收款凭据缺失、无效或已吊销 |
| `collector_credential_not_found` | 404 | 收款凭据不存在或此前已被吊销 |
| `payment_unmatched` | 409 | 该笔到账未匹配到任何待支付订单，需人工处理 |
| `payment_ambiguous` | 409 | 同打印机同金额的候选订单不止一笔，无法确定归属 |
| `admin_cannot_consume` | 403 | 管理员账号不能下单 / 上传 / 确认打印 |
| `admin_use_console` | 403 | 管理员账号走了用户端登录入口（密码正确后才返回） |
| `not_admin_account` | 403 | 非管理员账号走了管理面板登录入口（同上） |
| `session_has_no_device` | 403 | 当前会话没有绑定设备（管理员会话），不能访问消费端接口（如 `/me`） |
| `user_not_found` | 404 | 账号不存在 |
| `too_many_pages` | 400 | 文件页数（含客户端上报的 `page_count`）超过 5000 页上限 |
| `page_range_too_large` | 400 | 请求的页范围超出单次打印上限（页范围会逐页展开，必须夹住） |

客户端侧还会出现 `network_error`（后端不可达）与 `bad_response`（响应无法解析），
它们不来自服务端。
