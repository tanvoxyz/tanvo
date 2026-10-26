# Tanvo 架构蓝图

> [English](architecture.md) | 简体中文

> 相关文档：[领域模型](domain.zh-CN.md) · [功能对齐](parity.zh-CN.md) · [开发指南](development.zh-CN.md) · [测试策略](testing.zh-CN.md) · [路线图](roadmap.zh-CN.md) · [AGENTS.zh-CN.md](../AGENTS.zh-CN.md)

> Tanvo 是以 Go 为服务端核心的开源用量计费与计量平台。它在现代技术栈上重新实现 [lago-api](https://github.com/getlago/lago-api) 的功能——计量、定价、订阅、开票、额度、支付、集成。它不兼容 Lago 的 API 或数据；凡是 Lago 沿用旧有设计之处，Tanvo 都采用当下最好的设计。

---

## 1. 定位与原则

### 1.1 目标
* **与 lago-api 功能对齐**：lago-api 每项未废弃的功能都会实现；[功能对齐](parity.zh-CN.md)逐项跟踪。
* **一套代码，两种部署**：同一个二进制既运行单租户的自托管安装（Tanvo 加 PostgreSQL），也运行 Kubernetes 上的多租户 Tanvo Cloud；区别只在于配置，以及端口背后装配的是哪些实现（§13）。
* **金额正确**：金额自始至终是精确小数；每次状态变更及其触发的工作原子地提交（§6、§9）。
* **强类型契约**：所有 API 都使用 Protobuf + Connect，并有从同一份契约转码而来的资源风格 REST/JSON 接口（§4）。
* **为规模而设计**：经由 Kafka/Redpanda 写入 ClickHouse 的大规模事件摄取、可水平扩展的 worker，以及 Tanvo Cloud 的 cell（§7、§13）。
* **运维负担小**：最小安装是一个 `CGO_ENABLED=0` 的二进制、PostgreSQL 与 `typst` 可执行文件；Redis 或 Valkey、Kafka、ClickHouse 与对象存储都是可选的。

### 1.2 非目标
* 与 Lago 的 API、webhook 或数据库兼容，或提供从 Lago 迁移的工具（以后可能作为单独的工具提供）。
* Lago 自己已废弃的旧功能（列于[功能对齐 §2](parity.zh-CN.md)）。
* 业务数据使用 PostgreSQL 以外的数据库。

### 1.3 工程原则
1. **自底向上**：先实现纯函数库（金额、周期、表达式、定价、计量、税），每个库连同从 lago-api 移植的测试一起完成，再在其上构建下一层。
2. **无 CGO**：原生能力来自子进程（`typst`）或 wazero 运行的 WASM 模块（`libs/expression`）。
3. **库可独立使用**：每个 Go 库都是独立模块，无需 Nx 即可 `go test`；Nx 只做编排与缓存。
4. **契约先行**：`libs/proto` 是每个 API 与跨进程边界的唯一事实来源。
5. **端口与适配器**：`libs/core` 定义领域与端口；基础设施（`store`、`eventstore`、`jobs`、提供者）实现它们；只有 `apps/server` 负责装配。
6. **处处按租户限定**：每一行业务数据都带有 `organization_id`，每个查询都以它限定范围（§5.4）。

### 1.4 许可证
Tanvo 派生自 lago-api 与 lago-expression，两者都以 **GNU AGPL-3.0** 发布；因此本仓库的代码采用 **AGPL-3.0-or-later** 许可证，以下例外：

| 路径 | 许可证 | 原因 |
| :--- | :--- | :--- |
| 下面未列出的一切 | AGPL-3.0-or-later | 派生自 Lago |
| `libs/proto`（契约、生成的代码）与发布的客户端 SDK | Apache-2.0 | 从零设计；客户端与企业版服务链接它们 |
| `ee/`（企业版服务） | Elastic License 2.0 | 源码可见的企业版功能（§14） |

由此得出的规则：
* `ee/` 下的代码以**独立进程**运行，只通过 `libs/proto` 契约与只读数据视图与核心通信；它**从不导入 AGPL 的 Go 包或 crate**（由 depguard 强制，§3.2）。
* `libs/proto` 只包含为 Tanvo 设计的契约，从不包含从 Lago 移植的代码。
* 第三方依赖必须与 AGPL-3.0 兼容：MIT、BSD、ISC、Apache-2.0、MPL-2.0、LGPL、GPL-3.0、AGPL-3.0。不允许 GPL-2.0-only 与专有库（River Pro、Sidekiq Pro 等）。
* 贡献在 CLA 下接受，使项目能保持其许可证结构。
* `NOTICE` 记录上游版权及所做的修改。

---

## 2. 技术选型

| 维度 | 选型 | 说明 |
| :--- | :--- | :--- |
| 服务端语言 | **Go（跟随最新稳定版，当前 1.27）** | `iter.Seq`、`log/slog`、`testing/synctest`、`testing.B.Loop`、go.mod `tool` 指令 |
| 契约与 RPC | **Protobuf（edition 2023）+ buf + Connect**（`connect-go`；TypeScript 端 `protobuf-es` / `connect-es`） | `buf lint` + `buf breaking`；**protovalidate** 做请求校验 |
| REST | 基于 `google.api.http` 注解的 **[Vanguard](https://github.com/connectrpc/vanguard-go)** 转码 | 一份实现同时提供 Connect、gRPC、gRPC-Web 与 REST/JSON（§4.2） |
| API 参考 | **protoc-gen-connect-openapi** + Scalar | 从同一批注解生成 OpenAPI 3.1 |
| HTTP | 标准库 `net/http`（路由模式，h2c 用 `http.Protocols`） | 提供者 webhook 与文件下载是普通 HTTP handler |
| 数据库 | **PostgreSQL 17+** | `pgx/v5` 原生驱动，小数用 numeric，分区表，advisory lock |
| 查询与迁移 | **sqlc**（pgx）+ **Atlas**（声明式 schema → 版本化迁移） | 显式 SQL，生成类型安全的 Go；不用 ORM（§5） |
| 标识符 | **UUIDv7** | 按时间排序，对索引友好 |
| 小数 | 由 `libs/money` 封装的 **`cockroachdb/apd/v3`** | 任意精度，显式舍入（§6） |
| 后台任务 | **River**（PostgreSQL） | 事务内入队、唯一任务与周期任务、领导者选举；Redis 后端是以后的选项（§9） |
| 事件流 | **Kafka API（Redpanda / Kafka）**，使用 **franz-go** | 可选的高吞吐摄取路径（§7） |
| 事件分析存储 | **ClickHouse**（`clickhouse-go/v2`） | 可选；默认使用 PostgreSQL 分区（§7.4） |
| 缓存 | 默认在进程内；可选 **Redis 或 Valkey**（`valkey-go`） | 位于 `Cache` 端口之后，实现在 `libs/cache`（§13.6） |
| 表达式 | **lago-expression 移植到 Rust** → WASM，在 Go 中与浏览器中都运行于 **wazero** | 精确的小数语义，所有宿主共用一份实现（§7.3） |
| 文档 | **Typst**（子进程） | 内嵌电子发票 XML（Factur-X / ZUGFeRD）的 PDF/A-3；输出确定（§10） |
| 电子发票 | 在 Go 中生成 UBL 2.1 与 UN/CEFACT CII | Peppol BIS、Factur-X、XRechnung 配置 |
| 邮件 | SMTP（`net/smtp`，经由 `wneessen/go-mail`）、`html/template` | 提供商 API（SES 等）位于 `Mailer` 端口之后 |
| 出站 webhook | **Standard Webhooks** 签名（HMAC-SHA256，`webhook-id` / `webhook-timestamp` / `webhook-signature`） | 取代 Lago 的 JWT/HMAC 变体 |
| 认证 | API key（哈希存储）、OIDC（Google、Okta、Entra ID、任何 OIDC IdP）、邮箱 + 密码（argon2id）、会话 | `coreos/go-oidc`、`golang.org/x/oauth2` |
| 授权 | 按角色与按 API key 的权限字符串 | 用户与 key 使用同一套权限模型（§4.4） |
| 静态加密 | 对机密（提供者凭据、令牌）使用 AES-256-GCM 信封加密 | 密钥来自配置或 KMS |
| 可观测性 | **OpenTelemetry**（trace、metrics、logs），经 OTLP 导出，`log/slog` 桥接到 OTel logs；可选 Prometheus 端点 | 标准 `OTEL_*` 配置；其他后端（Sentry 等）经由 OTel Collector 接入（§15） |
| 命令行与设置 | **Cobra** + **koanf** | 命令行参数、`TANVO_*` 环境变量、`tanvo.toml` |
| Web UI | React + Vite + TanStack Router/Query + connect-es（`apps/web`，后期阶段） | 使用同一套 Connect 契约 |

工程工具（Nx、mise、buf、golangci-lint、CI）见[开发指南](development.zh-CN.md)。

---

## 3. 仓库结构

### 3.1 目录结构

```text
tanvo/
├── go.work                       # 汇总所有 Go 模块
├── nx.json  package.json  pnpm-workspace.yaml  mise.toml
├── LICENSE  NOTICE
│
├── apps/
│   ├── server/                   # [Go] cmd/tanvo：装配、配置、角色、Connect 服务、REST、提供方 webhook
│   └── web/                      # [TS] 管理界面与客户门户（后期阶段）
│
├── libs/
│   ├── proto/                    # [Go + TS] 契约（Apache-2.0）：*.proto、buf 配置、生成代码
│   ├── money/                    # [Go] 十进制数、货币、金额、舍入、分摊
│   ├── period/                   # [Go] 计费间隔、计费周期、周年日、时区、按比例计算系数
│   ├── expression/               # [Rust → WASM] 事件表达式；基于 wazero 的 Go 宿主包（go/）
│   ├── pricing/                  # [Go] 计费模型：标准、阶梯累进、阶梯百分比、按包、百分比、按量、动态；过滤器、分组
│   ├── metering/                 # [Go] 聚合：计数、求和、最大值、去重计数、加权求和、最新值；按比例与循环变体
│   ├── tax/                      # [Go] 税费计算、增值税识别号、VIES 客户端
│   ├── core/                     # [Go] 领域模型与端口；不依赖基础设施
│   ├── billing/                  # [Go] 计费引擎：费用、发票、优惠券、额度、钱包、贷项通知单、渐进式计费、承诺消费
│   ├── store/                    # [Go] PostgreSQL 模式（Atlas）、sqlc 查询、仓储实现
│   ├── eventstore/               # [Go] 事件存储：PostgreSQL 分区与 ClickHouse；聚合查询
│   ├── ingest/                   # [Go] 事件摄取：校验、去重、富化、Kafka 生产者与消费者
│   ├── jobs/                     # [Go] River 配置、任务类型、周期调度、租户间公平
│   ├── webhooks/                 # [Go] 出站 webhook 投递与签名；入站 webhook 存储与重试
│   ├── documents/                # [Go] Typst 渲染、文档模板、电子发票 XML
│   ├── mail/                     # [Go] 邮件模板与 Mailer 端口的 SMTP 实现
│   ├── auth/                     # [Go] API 密钥、会话、OIDC、密码哈希、权限
│   ├── cache/                    # [Go] Cache 端口的实现：进程内 LRU、Redis/Valkey
│   └── dockertest/               # [Go] 基于 Docker Engine API 的测试容器：生命周期上限、回收属主已死亡的容器
│
├── providers/
│   ├── stripe/ adyen/ gocardless/ cashfree/ flutterwave/ moneyhash/   # [Go] 支付提供方
│   ├── anrok/ avalara/                                                 # [Go] 税务提供方
│   └── netsuite/ xero/ salesforce/ hubspot/                            # [Go] 会计与 CRM 集成
│
├── ee/                           # 企业版服务（ELv2），独立进程（§14）
│
├── tools/
│   ├── nx-go/  nx-cargo/         # 本地 Nx 插件
│   └── testport/                 # 将 lago-api 的 rspec 用例提取到 testdata
│
└── docs/
```

各目录在[路线图](roadmap.zh-CN.md)中对应步骤开始时才创建。

### 3.2 依赖方向

```text
apps/server ──▶ billing ──▶ pricing / metering / tax / period / money / expression(go)
     │             │
     │             └──▶ core ──▶ money / period
     ├──▶ ingest ──▶ expression(go) / metering / core
     ├──▶ store / eventstore / jobs / webhooks / documents / mail / auth / cache ──▶ core
     ├──▶ providers/* ──▶ core
     └──▶ proto

ee/* ──▶ proto        (never any other in-repo module)
```

* `money`、`period`、`expression`、`pricing`、`metering` 与 `tax` 是纯函数库：它们只按图示顺序相互依赖，并依赖标准库或经过审查的第三方包，从不依赖 `core`、存储或 I/O（`tax` 位于接口之后的 VIES 客户端除外）。
* `core` 只依赖 `money` 与 `period`。
* `billing` 只通过 `core` 中的端口访问存储、事件、文档与提供者；它用 `expression`（纯库）对指标表达式求值。
* 基础设施库依赖 `core`，但彼此之间互不依赖，只有一个例外：`ingest` 使用 `expression`（与 `metering` 一样是纯库，ingest 写入的是它的事件类型）。集成测试可以使用其他库的后端；depguard 规则对测试文件豁免。
* `dockertest` 是一个不导入任何仓库内模块的叶子测试辅助库；只有测试文件、`store/pgtest` 与 store 的工具导入它。
* 只有 `apps/server` 把实现装配到端口上。
* 这些规则由 `.golangci.yml` 中的 depguard 强制；要修改它们，先修改本节。

---

## 4. 契约与 API（libs/proto、apps/server）

### 4.1 契约
* 源文件位于 `libs/proto/tanvo/<domain>/v1/`，使用 `edition = "2023"`；包名为 `tanvo.<domain>.v1`。
* Go 代码使用 Opaque API；Connect handler 使用 simple 签名。
* 每个必须存在的字段都标注 `(buf.validate.field).required = true`；protovalidate 在拦截器中运行。
* 只读 RPC 带有 `idempotency_level = NO_SIDE_EFFECTS`。
* 金额是消息 `{ string currency; string amount; }`，其中 amount 为小数字符串；数量是小数字符串；时间戳是 `google.protobuf.Timestamp`；日期是 `google.type.Date`。

### 4.2 经由 Vanguard 的 REST
* 每个公开 RPC 都有遵循 Google AIP 资源约定的 `google.api.http` 注解：`GET /v1/customers/{customer_id}`、`POST /v1/customers`、`PATCH /v1/customers/{customer.id}`，生命周期操作使用自定义方法（`POST /v1/subscriptions/{id}:terminate`、`POST /v1/invoices/{id}:void`）。
* Vanguard 从一个 `http.Handler` 同时提供 REST/JSON、Connect、gRPC 与 gRPC-Web。
* 所有协议上的 JSON 都使用 proto 字段名（**snake_case**）：Connect 的 JSON 编解码器与 Vanguard 的编解码器都配置了 `UseProtoNames`。REST 的 `update_mask` 查询参数同样使用 proto 字段名（`?update_mask=short_name,email_settings.invoice_finalized`）：protojson 只接受 `google.protobuf.FieldMask` 中的 lowerCamelCase 路径，因此 Vanguard 前的一个中间件（`rpc.RESTFieldMasks`）将每个 snake_case 路径段改写为 lowerCamelCase；lowerCamelCase 路径原样接受。
* 事件属性中的数字经 JSON 传输时保持精确。`Event.properties` 是 `google.protobuf.Struct`，其数字为 double，因此两个 JSON 编解码器（`apps/server/internal/rpc`）在 protojson 解码之前，把请求中各事件属性里的每个数字改写为其精确十进制文本（普通记数法，`1e3` → `"1000"`）的字符串；这正是摄取层为数字存储的文本。二进制 protobuf 与 gRPC 客户端发送 double，对 double 无法容纳的值（大于 2^53 的整数、超过 15 位有效数字）应发送字符串。
* 资源以 Tanvo ID 寻址；在 Lago 使用客户提供的标识符（`external_id`、`code`）之处，Tanvo 将其保留为唯一、可过滤的字段，并通过以前缀开头的 `{id}` 接受它们代替 ID（`external_id:acme-42`、`code:api_calls`）。
* 列表使用游标分页（`page_size`、`page_token`、`next_page_token`）与 AIP-160 风格的过滤器。
* 错误是带有 `google.rpc.ErrorInfo` / `BadRequest` 详情的 Connect 错误码；REST 将其映射为 HTTP 状态码并附带 JSON 正文。
* 写操作接受 `Idempotency-Key` 请求头（[§4.5](#45-幂等)）。

### 4.3 非 RPC 的 HTTP
提供者 webhook（`POST /webhooks/{provider}/{connection_id}`）、文档下载（`GET /documents/{id}`）、健康检查（`/healthz`、`/readyz`）与指标（`/metrics`）是普通的 `net/http` handler。

### 4.4 认证与授权
* **主体**：API key（限定于组织，以 SHA-256 哈希存储，只显示一次，可选过期时间，轮换时新旧并存一段时间，记录最近使用时间）或用户会话（组织成员身份）。
* **权限**：如 `customers:read`、`invoices:write` 这样的字符串。角色（内置与自定义）是权限的集合；API key 带有自己的一组权限。每个 RPC 在 proto 选项中声明所需的权限；由拦截器检查。
* **用户**以邮箱和密码（argon2id）或 OIDC（Google、Okta、Entra ID、通用）登录。会话是以哈希形式存储在 PostgreSQL（`sessions`）中的不透明令牌，可吊销，过期后清理（[领域 §5.7](domain.zh-CN.md#57-会话与登录状态)）。
* **OIDC** 使用带 state、nonce 和 PKCE 的授权码流程。流程状态是 `oidc_states` 中的一次性行，以 `state` 参数的 SHA-256 为键，因此任一副本都能完成登录。Google 是整个实例共用的一个连接，由环境变量配置；Okta、Entra ID 和通用连接是组织的 `auth` 集成（[领域 §17.1](domain.zh-CN.md#171-集成)），以其声明的邮箱域名跨组织路由登录。
* **OIDC 客户端**：libs/auth 中一个基于标准的客户端（go-oidc 与 x/oauth2）服务所有连接；Google、Okta 与 Entra ID 是固定了 issuer、scope 与声明规则的预设，通用 `oidc` 连接（Tanvo 扩展）以 `issuer_url` 接入任意 OpenID 提供者。
  * *发现*：`/.well-known/openid-configuration` 报告的 issuer 必须与配置完全相同，必须提供 `code` 响应类型与一种可接受的签名算法，并且除非设置 `TANVO_OIDC_ALLOW_PRIVATE`（开发模式），issuer 与所有端点都必须使用 https。发现文档按 issuer 缓存 1 小时，重新获取失败时保留旧文档。
  * *密钥*：JWKS 缓存 1 小时；带未知 `kid` 的令牌会触发重新获取，至多每分钟一次，因此密钥轮换无需重启，伪造的 `kid` 也无法冲击提供者。
  * *网络*：apps/server 注入 libs/webhooks 的防 SSRF 客户端（除非设置 `TANVO_OIDC_ALLOW_PRIVATE`，否则拒绝私有、回环与链路本地地址；从不跟随重定向）；每个响应至多 1 MiB，每个请求至多 10 秒。
  * *ID 令牌*：签名限于 RS、ES、PS 系列或 EdDSA（从不接受 `none`，从不接受 HMAC）；校验 `iss`、`aud`、多受众时的 `azp`、`exp`、`nbf` 与 `iat`（容差 30 秒），nonce 以常数时间比较。令牌端点认证方式取连接的配置，否则取发现文档中 `client_secret_basic` 与 `client_secret_post` 里最先出现的一个。
  * *身份*：邮箱与姓名取自可配置的声明（默认 `email`、`name`）；通用连接要求 `email_verified` 为真，除非设置了 `allow_unverified_email`，而预设保留原有行为（仅在为假时拒绝）。缺少邮箱或所需的 `email_verified` 时获取 userinfo，其 `sub` 必须一致。
  * *配置*：创建或更新通用连接时获取一次发现文档，失败时在 `issuer_url`（`https_required`、`issuer_unreachable`、`issuer_mismatch`、`discovery_incomplete`）或 `token_endpoint_auth_method`（`unsupported_by_provider`）上返回字段错误。附加授权参数不得覆盖协议参数（`state`、`nonce`、`redirect_uri` 等）。
* **设备**：每次登录都会在 `user_devices` 中记录客户端（指纹为 user agent 的 SHA-256）；apps/server 把 user agent 解析为浏览器、操作系统和设备类型。新设备会写入安全日志（[领域 §5.8](domain.zh-CN.md#58-用户设备)）。
* **邮件**：libs/auth 发出的是携带数据而非链接的 `mail.send` 任务：密码重置携带其令牌，由 libs/mail 根据应用 URL 构建链接。
* **客户门户**：限定于一个客户的短期签名令牌（HMAC-SHA256，12 小时，密钥由 `TANVO_SECRET_KEY` 派生），作为 `portal` RPC 的 bearer 凭据发送；仅当其客户存在且未被删除时令牌才被接受。
* **线上凭据**：所有凭据都以 `Authorization: Bearer <token>` 发送。API key 在其所属组织中操作。会话在 `X-Tanvo-Organization` 请求头指定的组织中操作，用户必须在该组织拥有有效成员身份，且该组织必须允许会话的登录方式；不带该请求头时，会话只能调用标记为 `authenticated` 的 RPC（当前用户、登出、服务器信息、数据导出下载），其他 RPC 以 `PermissionDenied` / `organization_required` 失败。
* **访问规则**：每个 RPC 的 `tanvo.common.v1.access` 选项是 `anonymous`、`portal`、`authenticated` 或一个 `permission`（API key 可调用时另带 `api_resource`）之一。API key 调用 `NO_SIDE_EFFECTS` RPC 需要该资源的 `read`，其他 RPC 需要 `write`；拒绝时返回 `PermissionDenied`，reason 为 `<level>_action_not_allowed_for_<resource>`。
* **会话清理**：每小时运行的时钟任务 `clock.purge_sessions` 删除过期或吊销超过 24 小时的会话。

### 4.5 幂等
* 由 API key 或在某组织中操作的会话、带 `Idempotency-Key` 请求头（至多 255 字节）调用的有副作用的一元 RPC，按组织与 key 只执行一次。该拦截器在认证与校验之后，以过程名和请求确定性编码的 SHA-256 在 `idempotency_keys` 中占用该 key；成功的响应会被保存，并在首个请求之后 24 小时内重放给每次重试。
* 以其他 RPC 或其他请求重试，返回 `InvalidArgument` / `idempotency_key_reused`；首个请求仍在运行时发来的重试返回 `Aborted` / `idempotency_key_in_progress`（占用 5 分钟后过期，因此崩溃的请求会释放其 key）。失败的请求会释放 key，客户端可以重试。
* `NO_SIDE_EFFECTS`、流式与匿名 RPC 忽略该请求头。每小时运行的时钟任务 `clock.purge_idempotency_keys` 为各组织入队 `idempotency.purge` 任务，删除过期记录。

### 4.6 服务端分层
* **领域服务**位于各个库中：`libs/billing/<area>`（catalog、customers、subscriptions、invoices 等）与 `libs/auth`。服务接收 `kernel.Deps`（或 `auth.Deps`），每个操作在一个 `Store.InTx` 中连同其任务与 webhook 一起执行，用 `core.Validator` 校验（字段错误携带 Lago 风格的错误码，如 `value_already_exist`），并返回领域类型。服务不了解 proto 或 HTTP，在 `memstore` 上测试。
* **RPC handler**（`apps/server/internal/rpc`）保持轻薄：读取主体，用共享辅助函数（引用、过滤器、分页、更新掩码、金额、时间、枚举）转换请求，调用一个服务方法，转换结果，并用 `rpc.Error` 映射错误。它们不包含业务规则。
* **装配**（`apps/server/internal/app/api.go`）在 store 之上构建服务，并把每个 handler 注册在共享拦截器之后：遥测、panic 恢复、认证与授权、protovalidate、幂等。
* **测试**：服务移植 Lago 的 service spec；handler 针对真实服务器（`apps/server/internal/rpctest`）通过 REST 与 Connect 移植其 request spec。分步指南见[开发 §8](development.zh-CN.md#8-rpc-指南)与 `rpc` 包注释。

---

## 5. 存储（libs/store）

### 5.1 只用 PostgreSQL
计费依赖精确的 `numeric` 运算、跨多张表的事务、advisory lock 与分区；PostgreSQL 是业务数据唯一支持的数据库。

### 5.2 Schema 与迁移
* Schema 以 SQL 声明在 `libs/store/schema/` 下（每个领域一个文件），是唯一事实来源。
* Atlas 据此计算版本化迁移，写入 `libs/store/migrations/`；已合并的迁移不得修改。
* Schema 也声明只读的导出视图（`exports_*`，[功能对齐 §1.43](parity.zh-CN.md#143-数据导出)），即数据仓库同步的契约。它们是 `security_invoker` 视图，因此读取者的行级安全生效。开源的 Atlas 规划器会忽略视图、行级安全与部分约束变更，因此由 `migrate-diff`（`libs/store/internal/cmd/migrate-diff`）自行比较：变更过的视图，或读取了某表而会阻止该表计划变更的视图，在表变更前删除、之后重建。
* 迁移通过 `tanvo migrate` 运行（在 Tanvo Cloud 中是一个 Kubernetes Job）；对于自托管安装，除非禁用，在启动时自动运行。
* 同一个数据库中有两套迁移历史，以便任务能在业务事务中插入：Atlas 管理 `public` schema，River 自己的迁移（rivermigrate）管理 `river` schema。River 的表不纳入 Atlas，使 Atlas 的 diff、lint 与漂移检查只看到 Tanvo 的 schema，River 升级时也无需手工复制它的迁移。`tanvo migrate` 是唯一入口：先应用 Atlas 的迁移，再应用 River 的，各自在自己的 advisory lock 下执行，并且都是幂等的。
* PostgreSQL 事件存储（§7）在自己的 schema（默认 `eventstore`）中有第三套迁移历史：它的迁移是幂等的 DDL，由同一命令应用，并同时创建未来几个月的分区；配置了 ClickHouse 时，`tanvo migrate` 也会应用 ClickHouse 的 schema。
* 一个版本内的迁移只做扩展，使滚动部署期间旧版本仍能运行；迁移在新版本上线之前应用。进程启动时检查 schema 是否已应用，否则拒绝启动：River 的每个迁移，以及本版本的每个 Atlas 迁移及其校验和（`store.CheckSchema`；接受更新版本的迁移，如滚动部署期间）都已应用，且事件存储存在。
* 应用迁移不得阻断正在运行的版本的写入。`store.Migrate` 在事务中运行每个文件，并设置 `lock_timeout`（`TANVO_MIGRATE_LOCK_TIMEOUT`，默认 5 秒）：排在长事务之后、拿不到锁的语句会直接失败，而不是让该表后续的所有加锁请求排在它后面；整个文件回滚，可以重试。文件头带有 `-- atlas:txmode none` 的文件在事务之外逐条执行语句，从而能够 `CONCURRENTLY` 地创建和删除索引（这些语句不受 `lock_timeout` 约束，因为它们从不阻塞写入；其余语句仍受约束）。只有在每条语句都成功之后才记录其 revision，因此失败的文件会从第一条语句重新执行：其语句是幂等的（`IF NOT EXISTS`、`IF EXISTS`），并且在同名的 `CREATE INDEX CONCURRENTLY` 重新执行之前，会删除失败的并发构建遗留的无效索引。
* CI 对分支新增的迁移强制执行这些规则：`store:migrate-check` 在 schema 变更缺少对应迁移时失败；`store:migrate-lint` 重放与 `$NX_BASE` 的合并基点中没有的迁移，并逐条分析语句。它运行 Atlas 的破坏性、向后不兼容、数据相关与约束删除分析器，以及自己的锁检查：已有表的索引必须在非事务文件中 `CONCURRENTLY` 地创建和删除（PG101–PG104）；向已有表添加的 check 或外键必须是 `NOT VALID`，并在之后的语句中验证（PG301）；拒绝列类型变更（PG302）与对已有列新增 `NOT NULL`（PG303）。经过评审并接受的发现，可在语句之前用 `-- atlas:nolint <分析器或代码>` 豁免，或写在文件头中豁免整个文件。已合并的迁移不再重新 lint。

### 5.3 查询
* sqlc 基于 pgx/v5 驱动，从 `libs/store/queries/` 中的 SQL 生成类型安全的 Go。
* 可选的列表过滤器使用 `sqlc.narg` 参数；全文搜索使用 PostgreSQL 的 `tsvector` 列。
* `Store.InTx` 在一个事务中运行一个函数，并向它提供绑定到同一个 `pgx.Tx` 的仓储与 River 客户端。
* sqlc 与 pgx 类型从不离开 `libs/store`；对上层它暴露 `libs/core` 的仓储端口。

### 5.4 租户隔离
* 每张业务表都把 `organization_id` 作为主键或某个唯一索引的第一列，每个查询都按它过滤。
* 例外是 x402 结算的支付摘要和交易哈希，它们在所有组织间唯一，使共用收款地址的两个组织绝不会为同一笔转账都入账（[领域模型 §24](domain.zh-CN.md#24-设计决定)，第 14 条）；以及发票的 x402 支付令牌，它是一个带唯一索引的列，与 v1.55.0 一样尚无任何代码设置或读取它（[领域模型 §11.1](domain.zh-CN.md#111-字段)）。读取仍按组织过滤。
* Tanvo Cloud 另外启用绑定到 `SET LOCAL tanvo.organization_id` 的行级安全策略，作为纵深防御。

### 5.5 约定
* ID 是 UUIDv7；面向客户的标识符（`external_id`、`code`）在每个组织内唯一。
* 以最小货币单位表示的金额为 `bigint`；精确金额、单价与数量为 `numeric`。
* 枚举为带 `CHECK` 约束的 `text`，并有对应的 Go 字符串类型。
* 删除是显式的：有生命周期的资源带有状态（`terminated`、`archived`、`voided`）；在没有任何东西引用该行时才使用硬删除。不存在一刀切的软删除。
* 需要通知客户的变更作为领域事件记录在同一事务中的 `domain_events` 表里，该表为 webhook 提供数据；活动日志由活动日志生产者（`auditlog.ActivityLogger`，经 `kernel.Deps.ActivityLogs`）同样在同一事务中写入 `activity_logs`，安装配置了 ClickHouse 时再移入 ClickHouse（§7.4）。

---

## 6. 金额与数字（libs/money）

* `money.Decimal` 是基于 `apd.Decimal` 的不可变小数，使用固定的运算上下文（精度 40，显式舍入模式：half-up、half-even、up、down、ceiling、floor）。
* `money.Currency` 覆盖 ISO 4217（代码、最小单位指数），以及自定义定价单位（见[领域模型](domain.zh-CN.md)中的 §定价单位）。
* `money.Amount` 把一种货币与一个小数配对；转换为最小货币单位时显式舍入。
* 分摊（把一个金额拆分到多行而不丢失一分钱）与按比例分配（优惠券与额度在各费用间的分配）都在这里。
* 计费引擎中的每次舍入都指明其模式与步长；没有任何隐式舍入。

---

## 7. 事件与计量

### 7.1 摄取路径
```text
POST /v1/events, /v1/events:batch
        │ validate, normalize, deduplicate (organization_id, external_subscription_id, transaction_id)
        ├── mode "postgres": insert raw + enriched rows in one transaction
        └── mode "kafka":    produce to events.raw
                               └─ ingest consumers: enrich → events.enriched ─┬→ ClickHouse writer (batched)
                                                                              ├→ in-advance charges → billing
                                                                              └→ usage refresh signals
```
* 模式是每个安装的设置，在 Tanvo Cloud 中可按组织覆盖。
* 每个事件保留其 API 请求的元数据，即 `User-Agent` 与客户端地址：PostgreSQL 路径上存于原始日志（`public.events.metadata`），Kafka 路径上存于 ClickHouse 的 `events_raw`，并由原始消息与富化消息携带。lago-api 只在 PostgreSQL 中保留它（其 Kafka 消息不含元数据）；API 以事件的 `user_agent` 与 `ip_address` 返回它。客户端地址按 Rails 的 `request.remote_ip` 解析：取 `X-Forwarded-For`（其次 `Client-Ip`）中离服务器最近且不是受信任代理的地址，且仅当对端本身是受信任代理时才读取这些头；受信任的是回环与私有地址段以及 `TANVO_TRUSTED_PROXIES`。
* HTTP、SQS 与 Kinesis 来源的连接器写入 `events.raw`：即 `libs/ingest/connectors/` 中的 Redpanda Connect 配置，其记录键与 API 相同。这些事件的指标表达式由摄入消费者计算，而 API 在摄入时即已计算。
* 消费者无法处理的消息连同原因进入 `events.dead_letter`（`ingest.DeadLetter`）：永远不会成功的消息立即进入；被重试的消息（其分区回退到它，间隔一秒）按摄入时间超过 12 小时后进入（`kafka.MaxRetryAge`）。该主题的消费者（组 `<prefix>-dead-letters`）把每条死信连同事件的键存入 ClickHouse 的 `events_dead_letter`，以便检查与重放。存储失败会一直重试直到成功，因此死信本身不会再进入死信主题。
* **被拒绝的事件。** ClickHouse 写入器把拉取到的一批记录（最多 1,000 条）作为一次写入存储。当存储拒绝该批中的某个值时（错误包装 `core.ErrInvalidValue`：没有组织的事件、驱动无法转换为其列类型的值，例如超出 `Decimal(76, 26)` 的小数，或由某个值引起的服务端异常，例如 `CANNOT_PARSE_TEXT` 或 `DECIMAL_OVERFLOW`；见 `clickhouse` 的 `rejected`），写入器把该批对半拆分、分别写入，直到找出被单独拒绝的记录：这些记录以 `reject_event` 进入死信，其余记录正常存储。连接失败、超时、过载（`MEMORY_LIMIT_EXCEEDED`、`TOO_MANY_PARTS` 等）以及 schema 或语句错误都不属于拒绝：它们会重试该批中尚未存储的部分。拆分后的各半与任何写入一样去重，因此隔离保持了去重键的恰好一次存储；一条被拒绝的记录约花费 2·log₂(批大小) 次写入，而不是让其分区停滞 12 小时后再把整批送入死信。
* **重放。** `tanvo events replay-dead-letters --organization <id> [--from <RFC 3339>] [--to <RFC 3339>] [--error-code <code>] [--replay] [--batch-size 500]` 按错误代码与来源主题统计该组织尚未重放的死信，可只统计在 [`--from`, `--to`) 内失败且具有某个代码的死信；不带 `--replay` 时为试运行。带 `--replay` 时，它把每条死信的原始消息重新生产到其记录的主题，键与其生产者的键相同（`events.raw` 用 `ingest.PartitionKey`，`events.enriched` 用 `ingest.SubscriptionPartitionKey`，`usage.refresh` 用组织与客户；其他主题的死信或缺少组织的死信被跳过），并在该页被确认后把重放记录到 ClickHouse 的 `events_dead_letter_replays`（按原始消息的主题、分区与偏移量），因此一条死信（或被存储两次的死信）只重放一次（`kafka.Replayer`）。一次重放只处理在它开始之前失败的死信：再次失败的消息会以新的偏移量再次进入死信，等待下一次重放。重放是幂等的：写入器保留去重键的首个副本，预付计费在该键上唯一，因此在生产与记录之间中断后再次运行的重放，每个事件仍只存储与计费一次。
* **用量刷新信号**携带客户、订阅和事件水位。信号在事件存储之后才发出，而用量桶在同一次写入中维护，因此信号不会领先于用量桶（在多副本 ClickHouse 上，刷新以顺序一致性读取）。每个信号都交给计费：PostgreSQL 路径在摄取事务中完成，Kafka 路径经由 `usage.refresh` 消费者完成。它调度每个订阅的活动（生命周期用量、用量阈值与告警，每个订阅每分钟一个延迟任务，因此在某次运行进行中到达的信号会落入下一分钟的任务；失败的运行会立即重试，共四次，与 lago-api 的 `ProcessSubscriptionActivityJob` 相同），把客户标记为 `awaiting_wallet_refresh` 并入队其钱包的刷新（每个客户一个待处理任务）。钱包刷新不等待用量桶。lago-api 的实时钱包刷新（`app/services/wallets/realtime_refresh_service.rb:9-13, 31-34, 70-94`）在刷新前最多轮询其用量桶 5 秒，因为它的触发信号与用量桶写入是同一流处理批次（epoch）中两个互不排序的输出，而在用量桶尚未追上时刷新会写入过期余额，并清除扫描所依据的标记。水位超过 30 秒时它立即放弃，因为这说明落后的是消费者而不是 ClickHouse，并让客户保持标记，交由扫描处理。只有钱包刷新这样做；订阅活动没有这项检查。Tanvo 在用量桶写入之后才发出信号，因此信号对应的用量桶总是已追上，刷新绝不会读到过期余额，也没有信号需要推迟给扫描。对已标记的生命周期用量与客户的 5 分钟扫描仍是权威来源（[领域模型 §14.1](domain.zh-CN.md#141-钱包)）。
* **预付计费项**在事件存储之后计费，因为其费用基于已存储的事件聚合：PostgreSQL 路径在摄取事务中完成，Kafka 路径由 ClickHouse 写入者为其写入的记录完成。两者都为每个事件入队一个计费任务，按订阅与 transaction ID 唯一。lago-api 从单独的主题计费，并延迟 15 秒以等待 ClickHouse 合并（`app/consumers/events_charged_in_advance_consumer.rb`、`app/services/events/stores/clickhouse_store.rb:11`）。
* **摄取屏障。** 在 Kafka 路径上，事件在 API 接受之后过一段时间才被存储，因此立即运行的周期计费可能漏掉仍在主题中的已结束周期的事件。所以经 Kafka 摄取的组织的周期计费（`billing.bill_subscriptions`）会等到计费时刻之前生产到 `events.raw` 的事件都已存储（`kafka.Barrier`）：在该时刻 30 秒之后，它列出该时刻的 raw 偏移量，并等待富化消费者组在每个分区上提交越过它们；然后取 `events.enriched` 的末尾偏移量（富化消费者只在其富化记录被确认之后才提交 raw 记录，因此这些偏移量覆盖了这些事件的所有富化记录），并等待写入消费者组提交越过它们。两次检查之间任务推迟 15 秒。每个进程在内存中按时刻保存富化偏移量；没有它们的进程会取更新的偏移量，等待更久，但不会少等。超过 `TANVO_BILLING_INGEST_BARRIER_MAX_WAIT`（默认 1 小时；`0` 关闭屏障）后，计费照常运行，记录错误日志并计数 `tanvo.billing.ingest_barrier.overruns`；屏障检查失败视为未到达。时间戳早于该时刻但在其之后才收到的事件不会等待，与 PostgreSQL 路径相同；处于宽限期的草稿发票在刷新时仍会纳入它们。

### 7.2 富化
富化按 `external_subscription_id` / `external_customer_id` 与时间戳把事件解析到其订阅，按 `code` 解析到计费指标，对指标的表达式求值，提取聚合值、分组键与精确金额，并**为每个事件写入一行富化数据**，不含计费项或过滤器列。它只为判断事件是否进入预付计费而查找计费指标的计费项。计费项过滤器在查询用量和维护用量桶时根据原始属性匹配，而不是把一个事件展开成每个过滤器一行：lago-api 正是因此在 2026 年 9 月删除了展开表（`events_enriched_expanded`），因为否则目录一变就要重写已存储的事件，而且行数随过滤器数量增长。API 与摄取消费者在内存中缓存目录（指标、计费项、订阅）。每个改变目录的存储事务都在提交之前调用 `pg_notify('tanvo_catalog_changes', …)`，因此通知在提交时送达，回滚的写入不会发出通知；每个进程都 LISTEN 并逐出该组织的条目，每次（重新）连接时清空整个缓存，写入进程在提交后也会立即逐出。万一仍漏掉通知，TTL 限定了过期时长。外部 ID 被修改的订阅也会驱逐其旧外部 ID。LISTEN 在每个进程中以会话状态占用一个连接，因此设置了 `TANVO_DATABASE_DIRECT_URL` 时它在该连接上运行（§13.2）。ClickHouse 与 Kafka 模式同样从 PostgreSQL 读取目录，因此同一通道也覆盖它们。

由于富化行不带计费项或过滤器，目录变更不会改写任何已存储的事件。在指标存在之前摄取的事件，或按指标的其他定义富化的事件，由 `tanvo events reprocess` 重新富化：它列出某组织中套餐在订阅开始后新增了计费项或计费项过滤器的活跃订阅，带 `--reprocess` 时为每个订阅入队一个重新富化任务。该任务分批重新富化订阅已存储的事件，并通过 `eventstore.Replacer` 存回该组织的事件存储（PostgreSQL 或 ClickHouse）；它既不做预付计费，也不发用量刷新信号。

### 7.3 表达式（libs/expression）
* 语言即 lago-expression 的语言：小数运算、`event.timestamp`、`event.code`、`event.properties.<name>`、字符串、`concat`、`round`、`ceil`、`floor`、`least`、`greatest`。
* 它以 Rust 实现一次，编译为 `wasm32-unknown-unknown`。Go 包 `libs/expression/go` 在 wazero 上运行该模块，提供批量接口（每次调用处理多个事件），Web UI 加载同一 crate 的 wasm-bindgen 构建（`libs/expression/js`：`parseExpression`、`evaluateExpression`，与 lago-expression 的 `expression-js` 一致）进行实时预览。

### 7.4 事件存储（libs/eventstore）
* `EventStore` 端口提供计量库所需的聚合查询：计数、求和、最大值、最新值、带增删语义的唯一计数、按时间加权的求和、分组变体、按比例变体与明细，全部以一个计费周期和过滤器为界。
* **PostgreSQL**：富化事件存放在按月份与组织哈希分区的表中。
* **ClickHouse**：`events_raw`（账本，按（组织，外部订阅，事务 ID）去重）与 `events_enriched` 为 `ReplacingMergeTree` 表，由摄取消费者批量写入（`events_enriched` 还保存富化赋予每个事件的值、其小数形式及写入时间，供富化事件 API 返回；还保存事件的归属标签，归属用量查询按其分组，见 parity §1.42）。聚合值按 `Decimal(76, 26)` 读取，因此摄取时拒绝整数部分超过 50 位的数值属性，而不是让 ClickHouse 把它读成零。`events.raw` 以去重键为分区键，`events.enriched` 以（组织，外部订阅）为分区键，因此一个键的所有副本在一个分区上保持有序，同一时刻只有一个写入消费者存储某个订阅的事件并维护其桶：写入者先看到一个键的第一个副本，再看到之后的副本，写入者之间无需解析副本。这把单个订阅的吞吐量限制在一个写入消费者，且一个组织中没有订阅的事件共用一个键。Lago 生产原始事件时不带键（fec71c637）；这些键是刻意的差异。
* **审计日志**（对照表 §1.45）：没有 ClickHouse 时，活动日志、API 日志与安全日志存放在 PostgreSQL（`23_audit_log.sql`）。配置了 ClickHouse（`TANVO_CLICKHOUSE_URL`）时，这些表从变更所在的事务起暂存每条日志，直到工作进程把它移入 `clickhouse.LogStore` 的 `security_logs`、`api_logs` 与 `activity_logs` 表（`store.ArchiveLogs`：每个事务每类最多 1,000 条，以 `FOR UPDATE SKIP LOCKED` 认领，写入 ClickHouse 后删除）。这些表采用 lago 的列（映射列保存每个顶层键及其 JSON 值），为按（组织，logged_at，ID）排序的 `ReplacingMergeTree` 表，因此插入后重试的移动不会存下重复；表没有 TTL，保留期与 lago 一样由查询施加。日志仓储（`auditlog.Archive`、`store.WithLogArchive`）在 PostgreSQL 中创建日志并读取两个存储，以同一种分页令牌按（logged_at，ID）合并分页，因此日志写入后立即可读，移动期间亦然。Lago 在提交后把日志生产到 Kafka，供 ClickHouse 的 Kafka 引擎表消费；Tanvo 在事务中写入日志，日志无需 Kafka。
* **15 分钟**（UTC）的**用量桶**在两种存储中为当前用量查询提供支撑。桶定义指明指标代码、被聚合的属性与维度（其计费项的过滤键、定价分组键与展示键，并始终包含 `target_wallet_code`）；桶的行以（组织、外部订阅 ID、代码、定义、桶、维度取值）为键，过滤键中不属于指标已声明取值的值折叠为同一个"其他"值，从而限定行数，并保持计费项过滤器的精确性，因为过滤器只接受已声明的取值。行存储 `units`、`events_count`、`precise_total_amount`，以及最后事件与摄取时刻。计费项过滤器在读取时按维度匹配，因此修改计费项永远不会使用量桶过期。
* 用量桶服务于可以跨桶合并的聚合（count、sum、max、latest）以及只需要总量的计费模型（standard、graduated、package、volume、graduated percentage、dynamic）；预付、按比例、周期性计费项、接受目标钱包的计费项以及其他聚合读取事件。查询读取窗口内的整桶，窗口两端的部分桶则从事件读取，因此结果与事件路径完全相同（lago-api 把窗口扩大到整桶，这对它安全只是因为它的桶记录内部订阅；Tanvo 的外部订阅 ID 在升级前后共用）。
* 用量桶在存储事件的同一次写入中维护，因此没有需要等待的延迟。在 PostgreSQL 上，每次写入在其事务中、在 advisory lock 下更新涉及的桶；替换事件会重建其桶；新定义在写入者已开始维护它的同时分块回填，就绪后才服务读取。在 ClickHouse 上，每次写入把其事件合并进涉及桶的当前行，版本加一（替换则从 `events_enriched FINAL` 重算），插入后检查冲突的写入，并重算冲突的桶；读取保留最高版本。在多副本表上，维护以 `insert_quorum` 插入、以 `select_sequential_consistency` 读取。周期性的 `RepairBuckets` 扫描是正确性所必需的，它重算最近存储事件的桶：修复在存储事件与更新桶之间失败的写入，以及被后台合并对检查隐藏的冲突。它每 10 分钟运行一次（`clock.repair_usage_buckets`），水位（写入者的时钟）取上次扫描开始时间之前一段余量（10 分钟）；每次完成的扫描把其开始时间记录在 ClickHouse 中（`bucket_repairs`，保留 30 天），使水位在重启与领导者切换后依然有效，首次扫描修复所有桶。一次写入的成本与其批次及其桶的行数成正比，而不随桶中已有事件数增长。
* `BucketStats` 报告每个定义的行数；每小时运行的 `clock.report_usage_bucket_stats` 任务将其记入直方图（按存储区分，从不按组织），并封禁行数过多的定义（见下）。
* **定义的生命周期**（Tanvo 自有设计：lago-api v1.55.0 没有定义，它的 RisingWave sink 按计费项给桶加键）。定义只由需要它的读取创建（`billing.define_usage_buckets`；每小时用量在请求内自行定义）。每个定义记录其定义时间，以及最近一次由其桶回答读取的时间，每个进程每小时至多记录一次（PostgreSQL 为 `read_at` 列；ClickHouse 为 `usage_bucket_reads` 表，保留 400 天）。规则是只删除没有任何读取能用到的定义，或重建代价低的定义：每次删除都是安全的，因为仍需要该定义的读取会回到事件路径并重新定义它，代价是一次回填。
  * **清扫**：`billing.sweep_usage_buckets`（`Engine.SweepUsageBuckets`）在以下情况删除组织的定义：没有未删除的指标使用其代码（`metric_deleted`）；指标的聚合属性或过滤器已变更，读取不会再构建该定义（`definition_outdated`，`metering.BucketDefinitionCurrent`）；该指标没有任何未删除的计费项由桶提供（`no_bucket_charges`）；或在 `TANVO_USAGE_BUCKETS_IDLE_TTL`（默认 45 天，长于一个月度周期）内没有读取用到它，自最近一次读取或定义时起算（`idle`）。每日的 `clock.sweep_usage_buckets`（UTC 02:40）为每个拥有定义的组织分发一次清扫（每个组织唯一），并自行删除组织在已不再摄取的存储中遗留的定义。更改代码、聚合方式、属性、过滤器或循环属性的指标更新，以及指标删除，会在各自的事务中为该组织加入清扫任务；计费项和计划的删除留给每日清扫处理。
  * **行数过多的定义**：在单个订阅的单个桶中超过 `TANVO_USAGE_BUCKETS_MAX_ROWS_PER_BUCKET` 行（默认 10 000）的定义，其维度是无界的。定义任务在回填后检查它，每小时报告检查所有定义；超过上限时定义被封禁（`BlockBuckets`）：删除定义并记录封禁（`usage_bucket_blocks`）。它的读取回到事件路径（没有事件路径的每小时用量返回 `feature_unavailable`），再次定义它会以 `metering.ErrBucketsBlocked` 失败，定义任务将其视为已完成，因此定义不会被循环重建。闲置规则不适用于封禁；封禁随其指标或由桶提供的计费项一起消失（前三条规则），`DropBuckets` 也会解除封禁。`0` 关闭上限，此时报告仅在超过 10 000 行时警告。
  * **并发**：每个定义都有一个代（PostgreSQL：序列值；ClickHouse：其首次写入的版本）。在 PostgreSQL 上，删除以排他方式获取定义锁，并在一个事务中删除定义及其行；回填在每个分块中检查自己的代，且只把自己的代标记为就绪，因此就绪标记永远不会撤销删除；读取在其行所在的快照中检查同一代的就绪定义。在 ClickHouse 上，删除在定义的所有版本之上写入墓碑，使回填随后写入的就绪版本落败，并删除被删除各代的行；每一代的行使用各自的键（`definition_id.generation`），因此重新定义的定义永远不会读到被删除代的写入者留下的行；读取在读完行后检查同一代是否仍然就绪。在两种存储中，与删除重叠的读取都由事件路径回答，绝不会由部分桶回答。

### 7.5 计量（libs/metering）
`metering` 在事件的 `iter.Seq` 上，或在 `EventStore` 返回的聚合结果上实现聚合语义（包括按比例计算、跨周期结转的循环指标、舍入），使同一套规则在内存中用移植的 lago-api 用例测试，在生产中则在数据库中运行。

---

## 8. 计费引擎（libs/billing）

* **周期**（`libs/period`）：自然周期计费与周年计费，从每周到每年的间隔，试用期，组织、计费主体或客户的时区，按比例计算的比率，预付与后付的边界。
* **定价**（`libs/pricing`）：每个计费模型都从聚合结果计算出金额及其明细；固定计费项与附加项使用同一套机制。
* **费用 → 发票**：引擎为订阅构建费用（基础费、固定计费项、用量计费项、承诺消费的补差），按规定的顺序应用税（内部或提供者）、优惠券、预付额度、贷项通知单与钱包额度，并生成总额精确的发票。
* **调度**：计算每个订阅的下一个计费时刻，并为其调度一个 River 任务；一个每小时运行的对账任务捕获任何遗漏。这取代了 Lago 每小时扫描所有订阅的做法。
* **生命周期**：带宽限期的草稿发票、定稿、编号、作废、重新生成、渐进式计费、用量阈值与一次性发票；见[领域模型](domain.zh-CN.md)。
* **当前用量缓存**：为当前用量计算出的每个计费桶（某订阅的一个计费项或其某个过滤器）的费用保存在用量缓存中（`kernel.UsageCache`，基于 §13.6 的 `port.Cache`，进程内为 `TANVO_USAGE_CACHE_ENTRIES` 个条目），直到计费项周期结束。完整用量有自己的条目。条目在读取时惰性校验：它记录所聚合事件的摄取水位（`metering.Watermark`：最新的 `ingested_at`，或被替换、重新富化的副本的最新写入时间，PostgreSQL 中为 `written_at`，ClickHouse 中为 `enriched_at`），一旦存入更晚的事件就重新计算。最新事件不足一秒的聚合不会保留，因为同一时刻写入的兄弟事件可能对它不可见。键包含计费项的 `updated_at` 和过滤器的指纹，因此目录变更不会命中旧条目。由用量桶回答的读取、按时间戳截断的读取、预览以及每日用量历史从不缓存。校验只需要存储的水位，不需要失效消息，因此无论缓存位于每个进程中（每个 API 副本各有一个冷缓存），还是位于共享的 Redis 或 Valkey 服务器上（§13.6，每个副本都复用任一副本算出的费用），结果都是精确的。
* 一次计费运行的所有写入都与它触发的任务（文档、webhook、支付）在同一事务中完成。

---

## 9. 任务与调度（libs/jobs）

* **River** 运行所有后台任务；任务与需要它的状态变更在同一事务中入队。
* `libs/store` 通过 River 的 `InsertTx` 在自己的 `pgx.Tx` 上插入任务（`store.WithJobInserter`），因此回滚后不会留下任务；没有设置插入器时，任务写入 `job_outbox` 表，由中继转发。River 的表位于它自己的 `river` 模式中，由 River 迁移（这些表保存所有组织的任务，不带行级安全）。唯一键按组织和任务类型限定。
* **重试**：没有被该任务类型的任何规则归类的错误不会重试，因为任务可能已经在事务之外产生了效果（一次付款、一封邮件、一次服务商调用），再运行一次会重复这些效果。每种任务类型声明哪些错误可以安全重试，以及次数和退避方式（`ErrRetriable` 20 次、锁冲突与序号冲突、服务商限流）。带错误停止重试的任务在 River 中保持 `cancelled` 状态，保留下来供排查和手动重试，并在监控中计为失败。
* 队列按工作负载分开（`billing`、`payments`、`webhooks`、`webhooks_high_priority`、`documents`、`integrations`、`exports`、`alerts`、`alerts_high_priority`、`wallets`、`streaming`、`default`），各有自己的并发度，使一个部署可以按队列运行 worker；`TANVO_JOBS_QUEUE_CONCURRENCY` 可覆盖其中任一队列的并发度。订阅活动与生命周期用量告警运行在 `alerts` 上，钱包告警运行在 `alerts_high_priority` 上，`alert.triggered` Webhook 的分发与首次尝试运行在 `webhooks_high_priority` 上（`app/jobs/send_webhook_job.rb`；其重试运行在 `webhooks` 上），钱包余额刷新运行在 `wallets` 上，流式目的地投递运行在 `streaming` 上，与 lago-api 相同（`app/jobs/usage_monitoring/process_subscription_activity_job.rb`、`process_lifetime_usage_alert_job.rb`、`process_wallet_alerts_job.rb`、`app/jobs/customers/refresh_wallet_job.rb`、`app/jobs/deliver_event_job.rb`），这样用量刷新的积压不会拖慢计费。lago-api 只在设置了 `SIDEKIQ_ALERTS` 或 `SIDEKIQ_WALLETS` 时使用这些队列，否则回退到 `default` 或 `low_priority`；Tanvo 始终使用它们，因为未单独配置它们的部署仍以默认值处理它们。
* 周期任务（对账、过期、催收、清理）是由 River 选举出的领导者插入的 River 周期任务；不需要单独的时钟进程。它们运行在独立的 `clock` 队列上，不分片、不使用令牌桶，由每个携带调度表的进程消费。周期任务只做扫描和分发：它把到期的工作作为相关组织的任务放入各工作负载队列，以本次触发的时间作为这些任务的"当前时间"，并带上唯一键，使积压期间的多次触发不会产生重复任务。因此工作负载队列的积压不会推迟调度，一个组织的失败也不会阻止其他组织。受行级安全约束的表的保留期清理同样按组织分发任务（webhook 投递）；其余清理的分批删除直接在周期任务中执行。除 lago-api 的调度表外，Tanvo 还有自己的周期任务：webhook 扫描，以及用量桶的修复、报告与清扫（§7.4、§11）。
* **租户公平**：任务带有其所属组织；通过按组织哈希对队列分片，以及在入队时使用令牌桶，限制每个组织的并发，使一个租户无法使其他租户饥饿。令牌桶按进程计，因此有多个 API 副本时，一个组织的实际速率是各副本速率之和。集成同步任务按组织共用 5 个并发槽位（`port.Slots`），使一个组织的积压不会占住其他组织的槽位。槽位是 TTL 为 30 秒的租约，持有者每 10 秒续租一次，因此崩溃的持有者的槽位会自行释放：在 PostgreSQL 中是 `concurrency_slots` 表的行，设置 `TANVO_CACHE_URL` 时是缓存服务器上的有序集合（§13.6）。没有空闲槽位的任务推迟一秒；后端无应答而取不到槽位的任务记录警告并推迟五秒，两者都不消耗尝试次数。
* 逐事件的工作（富化、ClickHouse 写入）在 Kafka 消费者中运行，而不在 River 中；消费者把逐事件的计费（预付计费项）与按订阅的活动作为 River 任务入队。
* **取任务**：River 每个取任务冷却期最多取一次某个队列，取走与空闲 worker 数相同的任务，因此在一个进程中，一个分片每个冷却期最多运行其 worker 数个任务，无论任务多轻。冷却期为 25 ms（`TANVO_JOBS_FETCH_COOLDOWN`；River 自己的默认值 100 ms 会把 5 个 worker 的分片限制在每秒 50 个任务）；更短的冷却期会使取任务与插入互相争用。`TANVO_JOBS_SHARD_CONCURRENCY` 设置队列每个分片的 worker 数，即一个组织的任务在一个进程中的上限。
* 基于 Redis 的队列是以后的选项，位于同一个窄接口之后（经由 outbox 的事务内入队、延迟、周期任务与唯一任务）；只有在测量表明 PostgreSQL 成为瓶颈时才会加入。目前的测量（[基准测试 §1](benchmarks.zh-CN.md#1-任务队列river-与-redis-队列)）表明，调好冷却期的 River 在吞吐上与 Valkey Streams 队列相当，两者都受限于入队的 PostgreSQL 事务；而事务性的 Redis 队列（经由 outbox）的尾部延迟与 River 相同。

---

## 10. 文档（libs/documents）

* 发票、贷项通知单、付款收据与付款请求由作为子进程运行的 **Typst** 渲染，以文档数据的 JSON 作为输入；模板与字体嵌入在二进制中，启动时写入一个私有目录。
* 输出为 PDF/A-3b；电子发票把其 XML（Factur-X / ZUGFeRD CII）作为附件嵌入。UBL 2.1 与 CII XML 也单独提供。
* 字体：二进制为拉丁字母语言嵌入 Inter（SIL OFL）。模板对 zh-CN 回退到 Noto Sans CJK SC / Noto Sans SC，对 zh-TW 回退到 Noto Sans CJK TC / Noto Sans TC，这些字体不嵌入：渲染器搜索系统字体目录（或 `Options.FontPaths`），容器镜像安装 `fonts-noto-cjk`。在没有这些字体的情况下渲染 zh-CN 或 zh-TW 文档会以 `documents.ErrMissingFont` 失败，而不是输出缺字的 PDF。其他没有字体覆盖的文字（客户数据）仍会渲染，生成带警告的普通 PDF。
* 文档通过 `BlobStore` 端口（`libs/documents/blob`）存储：本地磁盘、任意 S3 兼容服务（AWS S3、MinIO、R2），或通过 S3 互操作 API 使用 Google Cloud Storage（`TANVO_BLOB_BACKEND`）。每个已存储的文件都有一个永久链接 `TANVO_API_URL/files/<token>`，以由 `TANVO_SECRET_KEY` 派生的密钥签名，重定向到一个短时有效的下载 URL（`TANVO_BLOB_URL_TTL`）：存储桶的预签名 URL，或本地磁盘的 `TANVO_API_URL/blobs/…`。API、邮件与集成对外提供的都是这些链接（发票 `file_url`、HubSpot 的发票文件、付款请求邮件、Flutterwave 的 logo）。没有配置后端时不渲染任何文档：文档任务成功完成但不产生文件，邮件不带附件和链接。
* 生成由 `libs/billing/docgen` 驱动。`documents` 队列运行 `documents.generate_invoice`、`documents.generate_credit_note` 与 `documents.generate_payment_receipt`：每个任务在开票主体开具电子发票且 UBL XML 缺失时渲染它，然后除非设置了 `TANVO_DISABLE_PDF_GENERATION`，渲染 PDF（电子发票时嵌入 CII XML），存储两者，发出 `invoice.generated`、`credit_note.generated` 或 `payment_receipt.generated` 及其活动日志，并在需要时入队该文档的邮件。已有文件会被保留，除非任务被强制执行（管理员重新生成），此时替换文件并删除旧文件。渲染器或 blob 存储的失败重试 6 次；草稿或不存在的文档直接失败。
* `mail.send_document`（在 `default` 队列）渲染缺失的 PDF，组装发票、贷项通知单或付款收据邮件，附上 PDF（除非禁用了 PDF）及其链接，遵循开票主体的邮件设置与重发时指定的收件人，发送邮件并记录 `email.sent`（sent、resent 或 failed，含邮件与文档信息）。文件尚未就绪的收据重试 8 次，SMTP 4xx 回复重试 25 次，超时或连接中断重试 6 次；失败只在最后一次尝试后记录。付款请求邮件链接每张发票的 PDF，必要时先渲染它。已完成的数据导出存入 blob 存储，并通过邮件发送给发起人。
* 渲染在 `documents` 队列中运行；在 Tanvo Cloud 中由一个专门的 Deployment 运行该队列。只要配置了 blob 后端，就需要 Typst（`TANVO_TYPST_PATH`）。

---

## 11. Webhook（libs/webhooks）

* **出站**：每个组织都有 webhook 端点；每个被订阅的领域事件都作为一个 River 任务投递，带有重试与指数退避，按 Standard Webhooks 规范签名，投递尝试会被记录且可以重放。
* 投递保留 90 天：每日清理（`clock.clean_webhooks`）为每个有端点的组织入队一个清除任务，在该组织的行级安全范围内删除最后更新早于截止时间的投递，每个事务 1 000 条。每 5 分钟一次的扫描（`clock.sweep_webhooks`）为丢失了任务的到期投递重新入队。
* **入站**：提供者 webhook 经过验证、存储，并由任务处理；失败会重试，并保留以供检查。
* 除非明确允许，否则拒绝私有网络目标（SSRF 防护）。
* **流式目的地**（parity §1.40）：由运维人员配置的目的地（`streaming_destinations`，以 SQL 写入；与 lago-api 相同，没有 API）认领事件类型，每个组织每种类型一个目的地，并可以把某些计划排除在全量用量事件之外。客户的钱包余额刷新时，`libs/billing/streaming` 产生 `customer_usage.refreshed.v1`（提交后内联产生，复用刷新计算出的用量）与 `customer_full_usage.refreshed.v1`（`streaming` 队列上的 `streaming.deliver_event` 任务，只尝试一次）。每个组织、客户与类型一把投递锁（`streaming_delivery_locks`，持有 10 分钟），在一次投递等待期间到来的刷新被丢弃并记为 `superseded`。记录经由 `port.StreamProducer` 端口发出，由 `libs/webhooks/kinesis` 实现：一个以 SigV4 签名的 Kinesis `PutRecord` 客户端，从 worker 的环境 AWS 身份（环境变量、web identity、容器或实例凭证）出发，经 STS 承担每个目的地的角色，设置了 `TANVO_STREAMING_INTERMEDIATE_ROLE_ARN` 时先经过该中间角色，并使用目的地的分区键。每次投递都以 `log/slog` 记录一个结果：`delivered`、`skipped`、`failed`、`superseded`、`throttled` 或 `dropped`。

---

## 12. 提供者（providers/*）

每个提供者都是一个实现 `libs/core` 端口的 Go 模块：

| 端口 | 提供者 |
| :--- | :--- |
| `PaymentProvider`（客户、支付方式、结账、扣款、退款、webhook） | Stripe、Adyen、GoCardless、Cashfree、Flutterwave、Moneyhash |
| `integration.TaxGateway`（报价、申报、作废、冲销、退款） | Anrok、Avalara |
| `integration.AccountingGateway` / `CRMGateway`（联系人、商品、发票、贷项通知单、付款、CRM 记录） | NetSuite、Xero、HubSpot；Salesforce（仅连接） |
| `IdentityProvider`（OIDC） | Google、Okta、Entra ID、通用 OIDC |
| `x402.Facilitators`（verify、settle、supported）与 `x402.ChainReaders`（EVM 与 Solana JSON-RPC） | Coinbase CDP（`providers/x402`） |

集成直接调用各厂商的 API；Tanvo 不依赖集成中介。

### 12.1 支付提供者

`port.PaymentProvider` 即 `payment.Gateway`，由六项能力组成：`ConnectionGateway`（校验、注册与刷新 webhook 端点）、`CustomerGateway`（创建、更新、移除）、`MethodGateway`（列出、默认、详情、设为默认、解绑、设置 URL）、`ChargeGateway`（付款、预授权、状态、取消、付款 URL 及其过期、更新参考）、`RefundGateway` 与 `WebhookGateway`（签名校验并解析为类型化的 `payment.Event`）；`FundingGateway` 是可选能力，通过类型断言发现。apps/server 按厂商构建 `port.PaymentProviderSet`。

* 每次调用都传入 `payment.Connection`（组织、集成、设置与解密后的 secrets）；适配器不保存按连接的状态。
* 幂等键来自领域：付款的幂等键由付款对象与付款 ID 得出（`PaymentParams.IdempotencyKey`），退款的幂等键是贷项通知单 ID（必填），客户的幂等键是其 ID 与更新时间。
* 错误为带 `payment.ErrorKind` 与类别的 `*payment.ProviderError`：`payment.ErrRejected`（终结：卡被拒、请求无效、不存在）或 `payment.ErrRetryable`（限流、服务不可用、冲突；由调用方的任务以同一幂等键重试）；`payment.ErrAuthentication` 表示连接的凭据失效。适配器自身的错误（缺少国家、没有支付方式）不经分类直接返回。
* 适配器不把提供者对象带入领域：结果包含提供者状态、映射后的 `payment.State`，以及 `requires_action` 的下一步 URL。Webhook 事件带有从元数据（`tanvo_*` 键）读出的 `payment.Refs`；按记录解析它们由 apps/server 负责。
* 每个适配器按连接限流，并用注入的 OTel 提供者为其 HTTP 客户端埋点（span 带组织与集成 ID；指标不带）。

### 12.2 税务、会计与 CRM 集成

`port.IntegrationProviders` 是按厂商索引的 `port.IntegrationProviderSet`。每个适配器都是 `integration.Gateway`（厂商、连接校验），并实现其类别的网关：`TaxGateway`（Anrok、Avalara：税务交易的报价、申报、作废、冲销与退款，以及 Avalara 的公司查找）、`AccountingGateway`（NetSuite、Xero：联系人、商品与科目、发票、贷项通知单与付款；NetSuite 的子公司通过 `SubsidiaryLister`）或 `CRMGateway`（HubSpot：自定义对象与属性、公司或联系人、发票与订阅记录及其关联）。Salesforce 适配器只校验连接：Salesforce 软件包读取 Tanvo 的 API。`OAuthGateway` 以安装的应用（§13.5）通过 OAuth 2.0 连接 Xero、HubSpot 以及 GoCardless 的支付连接。

* 由 `libs/billing/integrations` 编排。凭据以安装的密钥盒（`auth.SecretBox`）密封，从不返回，Tanvo 为 GoCardless 与 Flutterwave 生成的 webhook 密钥除外。`StartOAuth` 在连接的密钥中保存随机的 state 与重定向 URI，直到 `CompleteOAuth` 兑换授权码；适配器通过服务（`integration.ConnectionStore`）立即保存刷新后的授权。
* 连接在其事务中排入后续任务：会计集成获取商品，HubSpot 部署对象并保存账户，支付提供者注册 webhook 端点。
* 同步随触发它们的 webhook 一起排入：`integrations.Service.Triggers` 包装计费与支付服务的 webhook 发送器，因此 `invoice.created`、`credit_note.created`、`subscription.started` 等会在同一事务中排入提供者的同步。每条已同步的记录都是一个 `integration.Resource`（每张发票、贷项通知单、付款或订阅在提供者处的 ID）。
* `integrations.Taxes` 是计费引擎的 `kernel.TaxProvider`：为草稿与预览询价（Anrok 的答复保存在缓存中，§13.6），申报已定稿的发票与贷项通知单，作废或冲销已作废发票的税额。
* 每种任务类型具有其 lago 任务的重试规则（`integrations.RetryRules`：请求上限、提供者故障、超时后先在 NetSuite 中查找再重新创建记录、文档尚不完整）；其他错误令任务失败，并发送 `integration.provider_error` 或客户错误 webhook。
* 错误为带 `integration.ErrorKind` 的 `*integration.ProviderError`；`ErrRequestLimit` 与 `ErrRetryable` 标明可重试的错误，`ErrAuthentication` 标明凭据失效的连接。API 以 FailedPrecondition 及提供者的错误码回应提供者的拒绝，以 Unimplemented 回应提供者不具备的操作。
* 适配器共用 `providers/apiclient`：每个厂商一个 HTTP 客户端、每个连接一个限流器（Xero 按租户）且等待而非失败，有缓存服务器时跨进程在其上计数（§13.6），以及注入的 OTel 提供者。

---

## 13. 部署

### 13.1 角色
单一二进制的 `tanvo serve` 可以运行任意角色组合：

| 角色 | 运行内容 |
| :--- | :--- |
| `api` | Connect/REST 服务器、提供者 webhook、文档下载 |
| `worker` | 为其指定的队列（`--queues`）运行 River worker |
| `consumer` | Kafka 消费者：富化、ClickHouse 写入器（含预付计费项）、用量刷新 |
| `all` | 全部（自托管安装的默认值）；只有配置了 Kafka broker 时才包含 `consumer` |

`tanvo migrate`、`tanvo admin …`、`tanvo events reprocess`（§7.2）、`tanvo events replay-dead-letters`（§7.1）与 `tanvo version` 是单独的命令。

### 13.2 自托管
* 最小：`tanvo serve` + PostgreSQL。Typst 与 `fonts-noto-cjk` 软件包（zh-CN 与 zh-TW 文档，§10）包含在容器镜像中；在镜像之外安装时两者都需要。
* 可选：用于大量事件的 Redpanda/Kafka + ClickHouse，用于各副本共享缓存的 Redis/Valkey（§13.6），S3 兼容存储。
* **位于 PgBouncer 之后。** 当 `TANVO_DATABASE_DIRECT_URL` 直连 PostgreSQL 时，`TANVO_DATABASE_URL` 可以指向事务模式的 PgBouncer（或其他连接池）。主连接池上运行的都是事务范围的操作：行级安全设置使用 `set_config(…, true)`，计费锁是 `pg_advisory_xact_lock`，`pg_notify` 在写入事务内发送。会话状态都在直连池（"direct"）上：目录变更的 `LISTEN` 连接（§7.2）、River 的通知监听器（River 经主连接池查询，在直连连接上监听，因此新任务仍会立即被取走），以及以会话级咨询锁串行化的迁移；每个服务进程最多保持四个直连连接。集成同步的并发槽位（§9）是以单条语句写入的租约，因此经由连接池。pgx 按连接缓存具名预处理语句，因此 PgBouncer 必须为 1.21 或更高版本且 `max_prepared_statements` 大于零；更旧的连接池需在 `TANVO_DATABASE_URL` 中加 `default_query_exec_mode=exec`，每次查询多一次往返。PgBouncer 的 `default_pool_size` 覆盖各主连接池（每个进程 `TANVO_DATABASE_MAX_CONNS`）；直连连接另计入 PostgreSQL 的 `max_connections`。
* 以容器镜像和 Docker Compose 文件分发；一个 Helm chart 同时服务于自托管 Kubernetes 与 Tanvo Cloud。

### 13.3 Tanvo Cloud
* Kubernetes：每个角色（以及每个繁重的队列）一个 Deployment，迁移作为 Job，`api` 与 `consumer` 水平自动扩缩。
* **Cell**：一个 cell 是一个 PostgreSQL 集群及其 River worker、Kafka topic 与 ClickHouse 数据库，服务一组组织。一个精简的控制平面把组织映射到 cell；增长时增加 cell。大客户可以拥有专属的 cell。
* Tanvo Cloud 由 Tanvo 自己计费。

### 13.4 进程装配
`apps/server` 是唯一构建具体实现的模块；`internal/app` 每个组件一个文件完成装配（`postgres.go`、`migrate.go`、`jobs.go`、`eventstore.go`、`ingest.go`、`kafka.go`、`webhooks.go`、`mail.go`、`api.go`、`health.go`）。进程启动时：

1. 加载并校验配置（§13.5），一次报告全部问题，并安装遥测（§15）。
2. 打开 PostgreSQL 连接池；设置了 `TANVO_CLICKHOUSE_URL` 时打开 ClickHouse 连接。
3. 应用 schema（`TANVO_MIGRATE_ON_START`，自托管默认开启），或检查 schema 并在不符时拒绝启动（§5.2）。PostgreSQL 事件存储位于自己的 schema（`TANVO_EVENTSTORE_SCHEMA`，默认 `eventstore`），与 store 的原始事件日志（`public.events`）分开；事件存储以该 schema 限定其每个表、函数与锁键（`postgres.WithSchema`），在主连接池和摄取事务中都是如此（`store.InPgxTx` 把 store 事务的 `pgx.Tx` 交给事件存储），因此没有连接会改变 `search_path`。
4. 以 River 作为任务插入器构建 store（任务随业务事务一同提交），注册已装配服务的任务类型，并创建 River 客户端：`worker` 角色在配置的队列与 clock 队列上运行 worker，其他角色只插入。
5. 按组织的摄取模式选择其事件存储：postgres 模式用 PostgreSQL，kafka 模式用 ClickHouse。
6. 当有组织经 Kafka 摄取或运行 `consumer` 角色时创建 Kafka 生产者；`consumer` 角色还运行富化（`<prefix>-enrich`：raw → enriched）与 ClickHouse 写入（`<prefix>-write`：enriched → ClickHouse，用量信号）两个消费者组。
7. 每个角色都提供 HTTP：`/healthz`（存活）、`/readyz`（PostgreSQL、事件存储连接池，以及使用时的 ClickHouse 与 Kafka；关闭期间返回不可用）、启用时的 `/metrics`；`api` 角色另提供 Connect handler 及其在 `/v1/` 下的 REST 转码（Vanguard），依次经过遥测、panic 恢复、API key 认证与按各 RPC 访问选项的授权、protovalidate 校验等拦截器。

收到 SIGINT 或 SIGTERM 后，进程在 `TANVO_SHUTDOWN_TIMEOUT` 内关闭：就绪检查变为不可用，HTTP 服务器排空请求，worker 完成手头任务，消费者提交并停止，API 角色记录它收集的 API key 使用记录（平时每分钟记录一次，存于 `api_key_uses`；每小时运行的 `clock.track_api_key_usage` 任务按组织把它们应用到密钥上），最后关闭连接。

### 13.5 配置
配置项是 `TANVO_*` 环境变量，可叠加一个可选的 TOML 文件（`--config` 或 `TANVO_CONFIG`），其键为去掉前缀的小写变量名；环境变量优先。未知的 `TANVO_*` 变量与文件键会记录为警告。列表以逗号分隔。OpenTelemetry 由标准 `OTEL_*` 变量配置（§15）。

| 变量 | 默认值 | 含义 |
| :--- | :--- | :--- |
| `TANVO_ROLE` | `all` | `tanvo serve` 的角色（`api`、`worker`、`consumer`、`all`）；`--role` 优先。只有设置了 Kafka broker 时 `all` 才包含 `consumer` |
| `TANVO_ENVIRONMENT` | | `deployment.environment.name` |
| `TANVO_LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error` |
| `TANVO_LOG_FORMAT` | `json` | `json` 或 `text` |
| `TANVO_HTTP_ADDR` | `:8080` | 监听地址（API、健康检查、指标） |
| `TANVO_HTTP_READ_HEADER_TIMEOUT` | `10s` | 读取请求头的时限 |
| `TANVO_HTTP_MAX_BODY_BYTES` | `8388608` | 请求体大小上限 |
| `TANVO_TRUSTED_PROXIES` | 空 | 除回环与私有地址段外，额外受信任、可报告客户端地址的代理（地址或 CIDR 段，逗号分隔） |
| `TANVO_SHUTDOWN_TIMEOUT` | `30s` | 优雅关闭的时限 |
| `TANVO_METRICS_ENDPOINT` | `false` | 在 `/metrics` 以 Prometheus 格式提供指标 |
| `TANVO_DATABASE_URL` | 必填 | PostgreSQL URL（`postgres://…`） |
| `TANVO_DATABASE_MAX_CONNS` | pgx 默认值 | 连接池大小 |
| `TANVO_DATABASE_DIRECT_URL` | — | 绕过任何连接池的直连（会话）PostgreSQL URL，供需要会话状态的功能使用：目录变更与 River 的 `LISTEN` 连接，以及迁移（`tanvo migrate`、`TANVO_MIGRATE_ON_START`）。`TANVO_DATABASE_URL` 经由事务模式的 PgBouncer 时设置（§13.2）；为空时由 `TANVO_DATABASE_URL` 提供一切。其连接池容纳两个监听器与两个迁移连接 |
| `TANVO_DATABASE_ROW_LEVEL_SECURITY` | `false` | 为行级安全策略设置租户（§5.4） |
| `TANVO_MIGRATE_ON_START` | `true` | 启动时应用迁移；否则只检查 |
| `TANVO_MIGRATE_LOCK_TIMEOUT` | `5s` | 迁移语句等待锁的时长，超过则迁移失败，以免让正在运行的版本的写入排在它后面（§5.2）；`0` 表示无限等待 |
| `TANVO_JOBS_SCHEMA` | `river` | River 表所在的 schema |
| `TANVO_WORKER_QUEUES` | 全部队列 | `worker` 角色的队列；`--queues` 优先 |
| `TANVO_JOBS_DISABLE_PERIODIC` | `false` | 不让 worker 参与周期调度 |
| `TANVO_JOBS_CLOCK_CONCURRENCY` | 库默认值 | clock 队列的并发度 |
| `TANVO_JOBS_QUEUE_CONCURRENCY` | 库默认值 | 以逗号分隔的 `queue=n`，覆盖工作负载队列的并发度（1 到 10000），例如 `alerts=20,wallets=5` |
| `TANVO_JOBS_SHARD_CONCURRENCY` | 队列并发度分摊到各分片 | 以逗号分隔的 `queue=n`，每个分片的 worker 数（1 到 10000），取代分摊到各分片的并发度，例如 `billing=10`（§9） |
| `TANVO_JOBS_FETCH_COOLDOWN` | `25ms` | 两次取同一队列之间的最短间隔（1 ms 到 10 s）；一个分片每个冷却期最多运行其 worker 数个任务（§9） |
| `TANVO_JOBS_FETCH_POLL_INTERVAL` | `1s` | 没有插入通知时取队列的频率（从冷却期到 1 h） |
| `TANVO_SUBSCRIPTION_TERMINATION_ALERT_DAYS` | `15,45` | 订阅在结束日期前多少天收到 `subscription.termination_alert`（逗号分隔）；为空则不发送 |
| `TANVO_DAILY_USAGE_SCHEDULING_JITTER` | 30 分钟 | 每小时一次运行中的每日用量计算所分散的时间窗口 |
| `TANVO_USAGE_CACHE_ENTRIES` | `100000` | 没有缓存服务器时每个服务进程保留的当前用量计费桶数（§8、§13.6）；`0` 关闭缓存 |
| `TANVO_USAGE_BUCKETS_MAX_ROWS_PER_BUCKET` | `10000` | 单个订阅单个桶的行数超过该值时封禁用量桶定义，其读取留在事件路径（§7.4）；`0` 不封禁 |
| `TANVO_USAGE_BUCKETS_IDLE_TTL` | `1080h`（45 天） | 用量桶定义无读取超过该时长后由每日清扫删除（§7.4）；`0` 保留，否则至少为 `24h` |
| `TANVO_DISABLE_EVENTS_VALIDATION` | false | 将每小时的事件后置校验（`events.errors`）移出调度 |
| `TANVO_INGEST_MODE` | `postgres` | 默认摄取模式，`postgres` 或 `kafka` |
| `TANVO_INGEST_KAFKA_ORGANIZATIONS` | | 无论默认模式都经 Kafka 摄取的组织 |
| `TANVO_BILLING_INGEST_BARRIER_MAX_WAIT` | `1h` | Kafka 模式组织的周期计费等待计费时刻之前收到的事件的最长时间（§7.1），最多 `168h`；`0` 立即计费 |
| `TANVO_INGEST_POSTGRES_ORGANIZATIONS` | | 无论默认模式都在 PostgreSQL 中摄取的组织 |
| `TANVO_INGEST_ENRICH_CONCURRENCY` | `4` | 单个消费者的富化并发度 |
| `TANVO_EVENTSTORE_SCHEMA` | `eventstore` | PostgreSQL 事件存储的 schema |
| `TANVO_EVENTSTORE_HASH_PARTITIONS` | `4` | PostgreSQL 事件存储的哈希分区数，创建时确定 |
| `TANVO_CLICKHOUSE_URL` | | ClickHouse DSN（`clickhouse://…`）；kafka 摄取与 `consumer` 角色必需 |
| `TANVO_KAFKA_BROKERS` | | 种子 broker；kafka 摄取与 `consumer` 角色必需 |
| `TANVO_KAFKA_CLIENT_ID` | `tanvo` | Kafka 客户端 ID |
| `TANVO_KAFKA_TLS` | `false` | 使用 TLS 连接 |
| `TANVO_KAFKA_SASL_MECHANISM` | | `plain`、`scram-sha-256` 或 `scram-sha-512` |
| `TANVO_KAFKA_SASL_USERNAME`、`TANVO_KAFKA_SASL_PASSWORD` | | SASL 凭据 |
| `TANVO_KAFKA_TOPIC_PREFIX` | | topic 名称前缀（`events.raw`、`events.enriched`、`events.dead_letter`、`usage.refresh`） |
| `TANVO_KAFKA_CONSUMER_GROUP_PREFIX` | `tanvo` | 消费者组名称前缀 |
| `TANVO_KAFKA_CREATE_TOPICS` | `false` | 启动时创建缺失的 topic |
| `TANVO_KAFKA_TOPIC_PARTITIONS` | `6` | 所建 topic 的分区数 |
| `TANVO_KAFKA_TOPIC_REPLICAS` | `-1` | 所建 topic 的副本数（broker 默认值） |
| `TANVO_WEBHOOKS_ALLOW_PRIVATE` | `false` | 允许投递到私有地址与明文 HTTP |
| `TANVO_OIDC_ALLOW_PRIVATE` | `false` | 允许经由私有地址与明文 HTTP 访问 OIDC 提供者（开发模式） |
| `TANVO_STREAMING_INTERMEDIATE_ROLE_ARN` | | worker 在承担每个流式目的地的角色之前先承担的角色（角色链）；未设置则直接承担目的地角色 |
| `TANVO_STREAMING_KINESIS_ENDPOINT` | | 覆盖区域端点的 Kinesis 端点（测试、LocalStack、VPC 端点） |
| `TANVO_STREAMING_STS_ENDPOINT` | | 覆盖区域端点的 STS 端点 |
| `TANVO_WEBHOOKS_TIMEOUT` | `30s` | 单次投递尝试的时限 |
| `TANVO_API_URL` | | API 的公网 URL；支付服务商把 webhook 发到 `TANVO_API_URL/webhooks/{provider}/{connection_id}`，创建连接时向服务商注册；未设置则不注册 |
| `TANVO_GOCARDLESS_CLIENT_ID` | | 商户通过 OAuth 连接的 GoCardless 合作伙伴应用的客户端 ID；未设置时无法连接 GoCardless |
| `TANVO_GOCARDLESS_CLIENT_SECRET` | | 该应用的客户端密钥 |
| `TANVO_GOCARDLESS_SANDBOX` | `false` | 通过 GoCardless 沙箱连接商户 |
| `TANVO_XERO_CLIENT_ID` | | 商户通过 OAuth 连接其组织的 Xero 应用的客户端 ID；未设置时无法连接 Xero |
| `TANVO_XERO_CLIENT_SECRET` | | 其客户端密钥 |
| `TANVO_HUBSPOT_CLIENT_ID` | | 商户通过 OAuth 连接其门户的 HubSpot 应用的客户端 ID；未设置时无法连接 HubSpot |
| `TANVO_HUBSPOT_CLIENT_SECRET` | | 其客户端密钥 |
| `TANVO_ANROK_DRAFT_TAXES_CACHE_TTL` | `24h` | 相同请求下 Anrok 对草稿发票的税额保留多久；`0` 关闭缓存 |
| `TANVO_ANALYTICS_CACHE_TTL` | `4h` | 计算出的分析报表复用多久（§13.6）；`0` 关闭缓存 |
| `TANVO_CACHE_URL` | | 所有缓存与服务商限流所用的 Redis 或 Valkey 服务器（§13.6）：`redis://`、`rediss://`、`valkey://`、`valkeys://` 或 `unix://`；不设置时缓存留在每个进程中 |
| `TANVO_CACHE_PREFIX` | `tanvo:` | 缓存服务器上每个键的前缀，供共用一台服务器的多个安装区分 |
| `TANVO_CACHE_CLIENT_SIDE_TTL` | `0` | 对缓存服务器条目做客户端缓存（RESP3 跟踪）的最长时间；`0` 关闭 |
| `TANVO_CACHE_SENTINEL_USERNAME`、`TANVO_CACHE_SENTINEL_PASSWORD` | | 带 `master_set` 的 `TANVO_CACHE_URL` 所用 Sentinel 的凭据，与 URL 中服务器自身的凭据相互独立 |
| `TANVO_API_KEY_CACHE_TTL` | `1h` | 事件摄取与即时费用估算所用的 API 密钥在缓存服务器上保留多久，最长到密钥过期；`0` 关闭该缓存，没有缓存服务器时也关闭 |
| `TANVO_APP_URL` | | 邮件链接使用的 Web 控制台 URL |
| `TANVO_MAIL_FROM` | | 发件地址；设置 `TANVO_SMTP_HOST` 时必填 |
| `TANVO_SMTP_HOST` | | SMTP 服务器；为空时邮件只记录日志并丢弃 |
| `TANVO_SMTP_PORT` | `587` | SMTP 端口 |
| `TANVO_SMTP_USERNAME`、`TANVO_SMTP_PASSWORD` | | SMTP 凭据 |
| `TANVO_SMTP_SECURITY` | `starttls` | `starttls`、`starttls_optional`、`tls` 或 `none` |
| `TANVO_BLOB_BACKEND` | | 文档、logo、报价文件与数据导出的 blob 存储：`local`、`s3` 或 `gcs`；为空时不存储任何内容，也不渲染文档。需要 `TANVO_SECRET_KEY` 与 `TANVO_API_URL` |
| `TANVO_BLOB_LOCAL_ROOT` | | `local` 后端的目录 |
| `TANVO_BLOB_ENDPOINT` | `gcs` 为 `https://storage.googleapis.com` | `s3` 后端的服务 URL（必填），例如 `https://s3.eu-west-1.amazonaws.com` |
| `TANVO_BLOB_REGION` | `gcs` 为 `auto` | 签名区域（`s3` 必填） |
| `TANVO_BLOB_BUCKET` | | `s3` 与 `gcs` 后端的存储桶 |
| `TANVO_BLOB_ACCESS_KEY_ID`、`TANVO_BLOB_SECRET_ACCESS_KEY` | | 访问密钥及其密文（`gcs` 使用 HMAC 密钥） |
| `TANVO_BLOB_SESSION_TOKEN` | | 临时凭证的会话令牌 |
| `TANVO_BLOB_PATH_STYLE` | `false` | 以路径方式寻址存储桶（MinIO） |
| `TANVO_BLOB_PREFIX` | | 存储桶内的键前缀 |
| `TANVO_BLOB_URL_TTL` | `15m` | 永久文件链接重定向到的下载 URL 的有效期（最多 7 天） |
| `TANVO_TYPST_PATH` | `PATH` 中的 `typst` | 渲染 PDF 的 Typst 可执行文件 |
| `TANVO_DOCUMENTS_FONT_PATHS` | 系统字体目录 | 嵌入字体未覆盖的文字所用的字体目录（zh-CN 与 zh-TW 的 Noto CJK） |
| `TANVO_DISABLE_PDF_GENERATION` | `false` | 不渲染 PDF，邮件也不附带 PDF；电子发票 XML 仍会生成 |
| `TANVO_DISABLE_SIGNUP` | `false` | 关闭自助注册 |
| `TANVO_GOOGLE_AUTH_CLIENT_ID`、`TANVO_GOOGLE_AUTH_CLIENT_SECRET` | | Google 登录的 OAuth 客户端，回调到 `TANVO_APP_URL/auth/google/callback`；未设置则关闭 |
| `TANVO_SECRET_KEY` | | 安装密钥，至少 32 字节的 base64；用 HKDF-SHA256 从中派生签发客户门户令牌的密钥，以及加密集成密钥（SSO 客户端密钥与 x402 连接凭据）的 AES-256-GCM 密钥。未设置则关闭客户门户与 SSO 连接，并使每次 x402 结算方调用失败；更换后门户链接失效，已加密的密钥无法解密 |


### 13.6 缓存
所有缓存都位于同一个端口 `port.Cache` 之后（存取字节、带 TTL；尽力而为，失败即视为未命中），实现在 `libs/cache`。后端的选择是整个安装的一个开关：

* 不设置 `TANVO_CACHE_URL` 时，每个缓存都是进程内 LRU（`cache.Memory`）。不共享任何内容，每个副本各自预热自己的缓存。
* 设置 `TANVO_CACHE_URL` 后，所有缓存都位于这一台 Redis 或 Valkey 服务器上（`cache.Valkey`，基于 `valkey-go`，可选客户端缓存），由所有服务进程共享。该 URL 也可以连接集群（在 `addr` 中给出更多种子地址）或由 Sentinel 管理的服务器（`master_set`），使用 `rediss://` 或 `valkeys://` 时走 TLS。

每种用途保留自己的命名空间（`<prefix><name>:<key>`）和自己的开关：

| 缓存 | 内容 | 开关 | 没有缓存服务器时 |
| :--- | :--- | :--- | :--- |
| `usage` | 当前用量各计费桶的费用，按摄取水位校验（§8） | `TANVO_USAGE_CACHE_ENTRIES=0` | 进程内，`TANVO_USAGE_CACHE_ENTRIES` 个条目 |
| `analytics` | 计算出的分析报表；一个客户同一类报表通过版本令牌一起失效 | `TANVO_ANALYTICS_CACHE_TTL=0` | 进程内 |
| `anrok-draft-taxes` | Anrok 对草稿发票的税额，按请求摘要作键（§12） | `TANVO_ANROK_DRAFT_TAXES_CACHE_TTL=0` | 进程内 |
| `api-keys` | 事件摄取与即时费用估算 RPC 所用的 API 密钥，按令牌的 SHA-256 作键，从不使用密钥本身 | `TANVO_API_KEY_CACHE_TTL=0` | 关闭 |

* **正确性不依赖后端。** 每个缓存值要么在读取时校验（用量水位、分析版本），要么在其变更处删除。API 密钥缓存是例外，它需要每个进程都看到删除：在一个副本上轮换或销毁的密钥必须立即在其他副本上失效。因此它只在共享服务器上运行；服务器无法删除密钥的条目时，更新、轮换或销毁密钥都会失败且不做任何改变（`core.ErrUnavailable`，Connect `unavailable`）：条目在变更事务的最后一步删除，提交后再删除一次。其条目最长保留到密钥过期，每次读取时都会再次检查过期时间。
* **版本。** 计费缓存用同一个编解码器编码条目（`libs/billing/internal/cachecodec`）。每个键都包含条目 Go 类型的指纹，因此滚动升级期间，条目结构不同的进程永远不会读取彼此的条目。
* **击穿。** 未命中时需要计算的缓存（当前用量的分桶费用、分析报表、Anrok 草稿税额报价、API 密钥查找）在进程内把同一个键的并发未命中合并为一次计算（`golang.org/x/sync/singleflight`，每个缓存一个组）。执行计算的调用方保留其结果，其余调用方各自解码一份副本；用量费用如同命中时一样获得新的标识与时间戳。用量计算只在读取到同一摄取水位的调用方之间共享。等待中的调用方其上下文结束时立即停止等待；共享的计算因其他调用方的上下文而中断时，它自己重新计算。等待一秒后，等待者自行计算：调用方在各自的事务内等待，占着一个池连接，也可能持有共享计算所需的锁（其事件存储读取还要另取一个连接），这个上限把这种循环等待变成延迟而不是死锁。跨进程时，同一个键每个副本最多计算一次。
* **压缩。** 在共享服务器上，每个值带一个字节的头部：1 KiB 及以上的值若压缩后更小，则以 DEFLATE（BestSpeed）压缩存储，否则原样存储。头部未知或解码失败的值（例如旧版本写入的值）视为未命中，计入 `tanvo.cache.operation="decode"`，并由下一次写入替换。进程内缓存原样存储值，计数器是普通整数。
* **可用性。** 缓存不是就绪检查（`/readyz`）的依赖。服务器不可达不会导致启动失败：客户端在后台退避重连，在此之前每次读取都未命中，每次写入都被丢弃。以 Redis 或 Valkey 为唯一副本的功能在此期间失败，而不是退回另一个会分开计数的后端：API 密钥变更、服务商调用（请求限额错误，会重试）与并发槽位（任务推迟），每次失败都计入 `tanvo.cache.errors` 以便告警。
* **遥测。** `tanvo.cache.reads`（属性 `tanvo.cache.name`、`tanvo.cache.result`）与 `tanvo.cache.errors`（`tanvo.cache.operation`），以及客户端经 `valkeyotel` 产生的 span 与指标。
* **不缓存的内容。**
  * 订阅计费项的价格查找由计费引擎已加载的计费项构建。
  * 列表使用游标分页，因此没有需要缓存的计数。
  * 任务留在 PostgreSQL 上的 River 中（§9）。
* **计数器。** 服务商的限流（§12）在同一台服务器上计数，键位于 `rate-limits:` 之下，每个固定窗口一次原子递增（`Valkey.Incr`），因此所有副本合起来遵守每个厂商的限额。与缓存不同，递增失败是错误，调用以请求限额错误失败，五秒后重试（`apiclient.CodeUncounted`）：设置了共享服务器时，它是唯一的计数。
* **槽位。** 并发槽位（`port.Slots`，§9）是同一台服务器上的租约：每个槽位名在 `slots:` 之下一个有序集合，成员是随机的持有者令牌，分值是按服务器时钟计的到期时间，由单键 Lua 脚本修改（兼容集群）。设置 `TANVO_CACHE_URL` 时它们是唯一的槽位，不退回 PostgreSQL；未设置时它们是 `concurrency_slots` 表的行。

---

## 14. 企业版（ee/）

Lago 高级许可证之下的功能（渐进式计费、钱包的高级规则、集成、电子发票等）属于 AGPL 核心，不设门槛。只有 Lago 作为私有云服务运行的部分，才作为 ELv2 下的企业版服务实现，每个都是一个独立进程，使用 `libs/proto` 契约与只读数据（PostgreSQL 视图或 ClickHouse）：

* 分析与仪表盘（MRR 变动、收入流、预付额度、用量、收入确认），取代 Lago 私有的 Data API 与 Superset。
* 把 Tanvo API 暴露为工具的 MCP 服务器，由 `libs/proto` 描述符生成。
* 计费助手：基于可插拔模型提供方、使用该 MCP 服务器的 AI 智能体，取代 Lago 的财务助手与 AI 对话。
* 数据仓库同步：把核心的只读导出视图连同其删除墓碑复制到客户的数据仓库，取代 Lago 私有的数据管道（[功能对齐 §1.43](parity.zh-CN.md#143-数据导出)）。
* 客户成功工具，以及[功能对齐 §1.50](parity.zh-CN.md#150-lago-的开关及其在-tanvo-中的归属)列出的其他私有服务。

自托管安装通过可离线验证的许可证密钥（Ed25519 签名）启用它们；Tanvo Cloud 按套餐启用。AGPL 核心不包含任何许可证检查，只签发这些服务所接受的短期令牌。各服务的顺序与设计见[路线图 §5](roadmap.zh-CN.md#5-阶段-7-计划lago-未开源的部分)。

---

## 15. 安全与可观测性

* 机密以 AES-256-GCM 信封加密；数据密钥由来自配置或 KMS 的密钥加密。目前 `auth.SecretBox` 用一个由 `TANVO_SECRET_KEY` 派生（HKDF-SHA256）的 AES-256-GCM 密钥加密集成机密，并以组织 ID 作为附加数据；信封加密与 KMS 密钥在后续实现。
* 与安全相关的操作（登录、创建 key、角色变更）写入安全日志。
* **可观测性遵循 OpenTelemetry**，覆盖 trace、metrics 与 logs：
  * 库只依赖 OTel API，经构造函数选项接收 `TracerProvider` 与 `MeterProvider`，默认为 no-op；只有 `apps/server` 引入 SDK 与导出器（由 depguard 强制）并安装全局 provider。
  * SDK 由标准 `OTEL_*` 环境变量配置（`OTEL_SERVICE_NAME`、`OTEL_RESOURCE_ATTRIBUTES`、`OTEL_EXPORTER_OTLP_*`、`OTEL_TRACES_SAMPLER` 等）。默认导出器是基于 gRPC 或 HTTP 的 OTLP；Prometheus `/metrics` 端点是可选的指标读取器，用于拉取式部署。
  * resource 带有 `service.name`（`tanvo`）、`service.version`、`service.instance.id`、`deployment.environment.name` 与进程角色（`tanvo.role`）；Kubernetes 属性来自环境或 Collector。
  * 埋点使用维护中的埋点库并遵循语义约定：API 用 `otelhttp` 与一个遵循 RPC 语义约定的 Connect 拦截器，PostgreSQL 用 `otelpgx`，franz-go 用 `kotel`，River 用其 OpenTelemetry 中间件，ClickHouse 语句（`clickhouse.WithTracerProvider`：`db.system.name`、`db.operation.name`、`db.collection.name` 与参数化的 `db.query.text`）与 provider 调用外包 span。名称与单位遵循语义约定（`http.server.request.duration`，单位为秒）；Tanvo 自己的指标与属性使用 `tanvo.` 前缀（`tanvo.jobs.duration`、`tanvo.ingest.records`）。
  * 摄取消费者（§7.1）除 kotel 的客户端指标外还上报：`tanvo.ingest.records`（计数器，`{record}`），按消费者组（`messaging.consumer.group.name`）、结果（`tanvo.ingest.outcome`：`done`、`retry` 或 `dead`）以及后两者的 `error.type`（死信代码，限定在已知代码内，其余为 `other`）统计记录数；`tanvo.ingest.consumer.lag`（仪表，`{record}`），消费者组在某个主题（`messaging.destination.name`）上尚未提交的记录数，由运行该消费者的每个进程每 15 秒用 kadm 读取，因此同一组的各序列一致，聚合时取最大值；`tanvo.ingest.retry.age`（仪表，`s`），消费者正在重试的最早记录自摄入以来的时长，没有时为零，可在 12 小时上限把它送入死信之前告警。
  * worker 上报 `tanvo.billing.ingest_barrier.overruns`（计数器，`{job}`）：Kafka 模式组织在最长等待结束时未到达摄取屏障而运行的周期计费（§7.1）；任何增加都值得告警。
  * 上下文以 W3C Trace Context 与 Baggage 传播，异步边界也不例外：Kafka 记录头与任务元数据携带 `traceparent`。消费者或任务处理单个请求的工作时，其 span 是生产方 span 的子 span；处理批量、分发与周期任务时，则以 span link 关联。
  * 日志是以 JSON 写到 stdout 的 `slog` 记录，并桥接到 OTel logs（`otelslog`），带有 `trace_id` 与 `span_id`；组织、请求与任务 ID 作为属性。
  * 组织 ID 是 span 与日志的属性（`tanvo.organization.id`），但绝不作为指标属性：指标属性的取值有界（角色、队列、任务类型、状态、provider）。按租户的分析使用 trace 与 exemplar。
  * API key、凭据、支付信息、个人数据与事件属性绝不写入 span、指标或日志的属性。
  * 其他后端（Sentry、Datadog 等）经由 OTel Collector 接入，而不是厂商 SDK。
  * 健康检查（`/healthz`、`/readyz`）与遥测分开：就绪检查 PostgreSQL、事件存储连接池，以及使用时的 ClickHouse 与 Kafka 连接（§13.4）。
