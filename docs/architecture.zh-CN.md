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
* **运维负担小**：最小安装是一个 `CGO_ENABLED=0` 的二进制、PostgreSQL 与 `typst` 可执行文件；Redis、Kafka、ClickHouse 与对象存储都是可选的。

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
| 缓存、限流 | 默认在进程内 / PostgreSQL；可选 **Redis 或 Valkey**（`go-redis/v9`） | 位于 `Cache` 与 `RateLimiter` 端口之后 |
| 表达式 | **lago-expression 移植到 Rust** → WASM，在 Go 中与浏览器中都运行于 **wazero** | 精确的小数语义，所有宿主共用一份实现（§7.3） |
| 文档 | **Typst**（子进程） | 内嵌电子发票 XML（Factur-X / ZUGFeRD）的 PDF/A-3；输出确定（§10） |
| 电子发票 | 在 Go 中生成 UBL 2.1 与 UN/CEFACT CII | Peppol BIS、Factur-X、XRechnung 配置 |
| 邮件 | SMTP（`net/smtp`，经由 `wneessen/go-mail`）、`html/template` | 提供商 API（SES 等）位于 `Mailer` 端口之后 |
| 出站 webhook | **Standard Webhooks** 签名（HMAC-SHA256，`webhook-id` / `webhook-timestamp` / `webhook-signature`） | 取代 Lago 的 JWT/HMAC 变体 |
| 认证 | API key（哈希存储）、OIDC（Google、Okta、Entra ID、任何 OIDC IdP）、邮箱 + 密码（argon2id）、会话 | `coreos/go-oidc`、`golang.org/x/oauth2` |
| 授权 | 按角色与按 API key 的权限字符串 | 用户与 key 使用同一套权限模型（§4.4） |
| 静态加密 | 对机密（提供者凭据、令牌）使用 AES-256-GCM 信封加密 | 密钥来自配置或 KMS |
| 可观测性 | `log/slog` + OpenTelemetry（trace、metrics、logs），Prometheus 端点 | 可经由 OTel 选用 Sentry |
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
│   └── auth/                     # [Go] API 密钥、会话、OIDC、密码哈希、权限
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
apps/server ──▶ billing ──▶ pricing / metering / tax / period / money
     │             │
     │             └──▶ core ──▶ money / period
     ├──▶ ingest ──▶ expression(go) / core
     ├──▶ store / eventstore / jobs / webhooks / documents / mail / auth ──▶ core
     ├──▶ providers/* ──▶ core
     └──▶ proto

ee/* ──▶ proto        (never any other in-repo module)
```

* `money`、`period`、`expression`、`pricing`、`metering` 与 `tax` 是纯函数库：它们只按图示顺序相互依赖，并依赖标准库或经过审查的第三方包，从不依赖 `core`、存储或 I/O（`tax` 位于接口之后的 VIES 客户端除外）。
* `core` 只依赖 `money` 与 `period`。
* `billing` 只通过 `core` 中的端口访问存储、事件、文档与提供者。
* 基础设施库依赖 `core`，但彼此之间互不依赖，只有一个例外：`ingest` 使用 `expression`。
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
* 所有协议上的 JSON 都使用 proto 字段名（**snake_case**）：Connect 的 JSON 编解码器与 Vanguard 的编解码器都配置了 `UseProtoNames`。
* 资源以 Tanvo ID 寻址；在 Lago 使用客户提供的标识符（`external_id`、`code`）之处，Tanvo 将其保留为唯一、可过滤的字段，并通过以前缀开头的 `{id}` 接受它们代替 ID（`external_id:acme-42`、`code:api_calls`）。
* 列表使用游标分页（`page_size`、`page_token`、`next_page_token`）与 AIP-160 风格的过滤器。
* 错误是带有 `google.rpc.ErrorInfo` / `BadRequest` 详情的 Connect 错误码；REST 将其映射为 HTTP 状态码并附带 JSON 正文。
* 写操作接受 `Idempotency-Key` 请求头；响应按组织与 key 保存 24 小时。

### 4.3 非 RPC 的 HTTP
提供者 webhook（`POST /webhooks/{provider}/{connection_id}`）、文档下载（`GET /documents/{id}`）、健康检查（`/healthz`、`/readyz`）与指标（`/metrics`）是普通的 `net/http` handler。

### 4.4 认证与授权
* **主体**：API key（限定于组织，以 SHA-256 哈希存储，只显示一次，可选过期时间，轮换时新旧并存一段时间，记录最近使用时间）或用户会话（组织成员身份）。
* **权限**：如 `customers:read`、`invoices:write` 这样的字符串。角色（内置与自定义）是权限的集合；API key 带有自己的一组权限。每个 RPC 在 proto 选项中声明所需的权限；由拦截器检查。
* **用户**以邮箱和密码（argon2id）或 OIDC（Google、Okta、Entra ID、通用）登录。会话是以哈希形式存储在 PostgreSQL 中的不透明令牌。
* **客户门户**：限定于一个客户的短期签名令牌。

---

## 5. 存储（libs/store）

### 5.1 只用 PostgreSQL
计费依赖精确的 `numeric` 运算、跨多张表的事务、advisory lock 与分区；PostgreSQL 是业务数据唯一支持的数据库。

### 5.2 Schema 与迁移
* Schema 以 SQL 声明在 `libs/store/schema/` 下（每个领域一个文件），是唯一事实来源。
* Atlas 据此计算版本化迁移，写入 `libs/store/migrations/`；已合并的迁移不得修改。
* 迁移通过 `tanvo migrate` 运行（在 Tanvo Cloud 中是一个 Kubernetes Job）；对于自托管安装，除非禁用，在启动时自动运行。

### 5.3 查询
* sqlc 基于 pgx/v5 驱动，从 `libs/store/queries/` 中的 SQL 生成类型安全的 Go。
* 可选的列表过滤器使用 `sqlc.narg` 参数；全文搜索使用 PostgreSQL 的 `tsvector` 列。
* `Store.InTx` 在一个事务中运行一个函数，并向它提供绑定到同一个 `pgx.Tx` 的仓储与 River 客户端。
* sqlc 与 pgx 类型从不离开 `libs/store`；对上层它暴露 `libs/core` 的仓储端口。

### 5.4 租户隔离
* 每张业务表都把 `organization_id` 作为主键或某个唯一索引的第一列，每个查询都按它过滤。
* Tanvo Cloud 另外启用绑定到 `SET LOCAL tanvo.organization_id` 的行级安全策略，作为纵深防御。

### 5.5 约定
* ID 是 UUIDv7；面向客户的标识符（`external_id`、`code`）在每个组织内唯一。
* 以最小货币单位表示的金额为 `bigint`；精确金额、单价与数量为 `numeric`。
* 枚举为带 `CHECK` 约束的 `text`，并有对应的 Go 字符串类型。
* 删除是显式的：有生命周期的资源带有状态（`terminated`、`archived`、`voided`）；在没有任何东西引用该行时才使用硬删除。不存在一刀切的软删除。
* 客户需要审计的变更作为领域事件记录在同一事务中的 `audit_events` 表里，该表也为 webhook 与活动日志提供数据。

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
        │ validate, normalize, deduplicate (organization_id, transaction_id)
        ├── mode "postgres": insert raw + enriched rows in one transaction
        └── mode "kafka":    produce to events.raw
                               └─ ingest consumers: enrich → events.enriched ─┬→ ClickHouse writer (batched)
                                                                              ├→ in-advance charges → billing
                                                                              └→ usage refresh signals
```
* 模式是每个安装的设置，在 Tanvo Cloud 中可按组织覆盖。
* SQS、Kinesis、HTTP 与 Kafka 来源的连接器写入 `events.raw`（Redpanda Connect 配置或内置的消费者）。

### 7.2 富化
富化按 `external_subscription_id` / `external_customer_id` 与时间戳把事件解析到其订阅，按 `code` 解析到计费指标，解析到匹配的计费项与计费项过滤器，对指标的表达式求值，提取聚合值与分组键，并**为每个事件写入一行富化数据**。计费项过滤器在查询用量和维护用量桶时匹配，而不是把一个事件展开成每个过滤器一行：lago-api 正是因此在 2026 年 9 月删除了展开表（`events_enriched_expanded`），因为否则目录一变就要重写已存储的事件，而且行数随过滤器数量增长。摄取消费者在内存中缓存目录，并通过变更通知刷新。

### 7.3 表达式（libs/expression）
* 语言即 lago-expression 的语言：小数运算、`event.timestamp`、`event.code`、`event.properties.<name>`、字符串、`concat`、`round`、`ceil`、`floor`、`least`、`greatest`。
* 它以 Rust 实现一次，编译为 `wasm32-unknown-unknown`。Go 包 `libs/expression/go` 在 wazero 上运行该模块，提供批量接口（每次调用处理多个事件），Web UI 加载同一个模块进行实时预览。

### 7.4 事件存储（libs/eventstore）
* `EventStore` 端口提供计量库所需的聚合查询：计数、求和、最大值、最新值、带增删语义的唯一计数、按时间加权的求和、分组变体、按比例变体与明细，全部以一个计费周期和过滤器为界。
* **PostgreSQL**：富化事件存放在按月份与组织哈希分区的表中。
* **ClickHouse**：`events_raw` 与 `events_enriched` 为按交易去重的 `ReplacingMergeTree` 表，由摄取消费者批量写入；活动日志、API 日志与安全日志也存放在这里。聚合值按 `Decimal(76, 26)` 读取，因此摄取时拒绝整数部分超过 50 位的数值属性，而不是让 ClickHouse 把它读成零；Kafka 的分区键为（组织，交易），使每个交易只由一个消费者去重。
* 按（订阅、计费项、计费项过滤器、分组键、**15 分钟** UTC 桶）预聚合的用量桶，在两种存储中为当前用量查询提供支撑，适用于可以跨桶合并的聚合（count、sum、max、latest）以及只需要总量的计费模型；其他聚合直接读取事件。15 分钟这个粒度能让每个真实的 UTC 偏移（如 +05:30、+05:45）下的日边界都落在桶边界上，因此任何时区的计费周期都是整数个桶（与 lago-api 的 `usage_buckets_15m` 一致）。

### 7.5 计量（libs/metering）
`metering` 在事件的 `iter.Seq` 上，或在 `EventStore` 返回的聚合结果上实现聚合语义（包括按比例计算、跨周期结转的循环指标、舍入），使同一套规则在内存中用移植的 lago-api 用例测试，在生产中则在数据库中运行。

---

## 8. 计费引擎（libs/billing）

* **周期**（`libs/period`）：自然周期计费与周年计费，从每周到每年的间隔，试用期，组织、计费主体或客户的时区，按比例计算的比率，预付与后付的边界。
* **定价**（`libs/pricing`）：每个计费模型都从聚合结果计算出金额及其明细；固定计费项与附加项使用同一套机制。
* **费用 → 发票**：引擎为订阅构建费用（基础费、固定计费项、用量计费项、承诺消费的补差），按规定的顺序应用税（内部或提供者）、优惠券、预付额度、贷项通知单与钱包额度，并生成总额精确的发票。
* **调度**：计算每个订阅的下一个计费时刻，并为其调度一个 River 任务；一个每小时运行的对账任务捕获任何遗漏。这取代了 Lago 每小时扫描所有订阅的做法。
* **生命周期**：带宽限期的草稿发票、定稿、编号、作废、重新生成、渐进式计费、用量阈值与一次性发票；见[领域模型](domain.zh-CN.md)。
* 一次计费运行的所有写入都与它触发的任务（文档、webhook、支付）在同一事务中完成。

---

## 9. 任务与调度（libs/jobs）

* **River** 运行所有后台任务；任务与需要它的状态变更在同一事务中入队。
* 队列按工作负载分开（`billing`、`payments`、`webhooks`、`documents`、`integrations`、`exports`、`default`），各有自己的并发度，使一个部署可以按队列运行 worker。
* 周期任务（对账、过期、催收、清理）是由 River 选举出的领导者运行的 River 周期任务；不需要单独的时钟进程。
* **租户公平**：任务带有其所属组织；通过按组织哈希对队列分片，以及在入队时使用令牌桶，限制每个组织的并发，使一个租户无法使其他租户饥饿。
* 逐事件的工作（富化、大量的预付计费项）在 Kafka 消费者中运行，而不在 River 中。
* 基于 Redis 的队列是以后的选项，位于同一个窄接口之后（经由 outbox 的事务内入队、延迟、周期任务与唯一任务）；只有在测量表明 PostgreSQL 成为瓶颈时才会加入。

---

## 10. 文档（libs/documents）

* 发票、贷项通知单、付款收据与付款请求由作为子进程运行的 **Typst** 渲染，以文档数据的 JSON 作为输入；模板与字体嵌入在二进制中，启动时写入一个私有目录。
* 输出为 PDF/A-3b；电子发票把其 XML（Factur-X / ZUGFeRD CII）作为附件嵌入。UBL 2.1 与 CII XML 也单独提供。
* 文档通过 `BlobStore` 端口存储（本地磁盘、S3 兼容存储、GCS）。
* 渲染在 `documents` 队列中运行；在 Tanvo Cloud 中由一个专门的 Deployment 运行该队列。

---

## 11. Webhook（libs/webhooks）

* **出站**：每个组织都有 webhook 端点；每个被订阅的领域事件都作为一个 River 任务投递，带有重试与指数退避，按 Standard Webhooks 规范签名，投递尝试会被记录且可以重放。
* **入站**：提供者 webhook 经过验证、存储，并由任务处理；失败会重试，并保留以供检查。
* 除非明确允许，否则拒绝私有网络目标（SSRF 防护）。

---

## 12. 提供者（providers/*）

每个提供者都是一个实现 `libs/core` 端口的 Go 模块：

| 端口 | 提供者 |
| :--- | :--- |
| `PaymentProvider`（客户、支付方式、结账、扣款、退款、webhook） | Stripe、Adyen、GoCardless、Cashfree、Flutterwave、Moneyhash |
| `TaxProvider`（报价、提交、作废） | Anrok、Avalara |
| `AccountingSync` / `CRMSync`（客户、发票、贷项通知单、付款、映射） | NetSuite、Xero、Salesforce、HubSpot |
| `IdentityProvider`（OIDC） | Google、Okta、Entra ID、通用 OIDC |

集成直接调用各厂商的 API；Tanvo 不依赖集成中介。

---

## 13. 部署

### 13.1 角色
单一二进制的 `tanvo serve` 可以运行任意角色组合：

| 角色 | 运行内容 |
| :--- | :--- |
| `api` | Connect/REST 服务器、提供者 webhook、文档下载 |
| `worker` | 为其指定的队列（`--queues`）运行 River worker |
| `consumer` | Kafka 消费者：富化、ClickHouse 写入器、预付计费项 |
| `all` | 全部（自托管安装的默认值） |

`tanvo migrate`、`tanvo admin …` 与 `tanvo version` 是单独的命令。

### 13.2 自托管
* 最小：`tanvo serve` + PostgreSQL。Typst 包含在容器镜像中。
* 可选：用于大量事件的 Redpanda/Kafka + ClickHouse，用于缓存的 Redis/Valkey，S3 兼容存储。
* 以容器镜像和 Docker Compose 文件分发；一个 Helm chart 同时服务于自托管 Kubernetes 与 Tanvo Cloud。

### 13.3 Tanvo Cloud
* Kubernetes：每个角色（以及每个繁重的队列）一个 Deployment，迁移作为 Job，`api` 与 `consumer` 水平自动扩缩。
* **Cell**：一个 cell 是一个 PostgreSQL 集群及其 River worker、Kafka topic 与 ClickHouse 数据库，服务一组组织。一个精简的控制平面把组织映射到 cell；增长时增加 cell。大客户可以拥有专属的 cell。
* Tanvo Cloud 由 Tanvo 自己计费。

---

## 14. 企业版（ee/）

Lago 高级许可证之下的功能（渐进式计费、钱包的高级规则、集成、电子发票等）属于 AGPL 核心，不设门槛。只有 Lago 作为私有云服务运行的部分，才作为 ELv2 下的企业版服务实现，每个都是一个独立进程，使用 `libs/proto` 契约与只读数据（PostgreSQL 视图或 ClickHouse）：

* 分析与仪表盘（收入、MRR、用量、回款、预测），取代 Lago 私有的 Data API 与 Superset。
* 计费助手：带有暴露 Tanvo API 的 MCP 服务器的 AI 智能体。
* 客户成功工具，以及[功能对齐 §1.50](parity.zh-CN.md#150-lago-的开关及其在-tanvo-中的归属)列出的其他私有服务。

自托管安装通过可离线验证的许可证密钥（Ed25519 签名）启用它们；Tanvo Cloud 按套餐启用。AGPL 核心不包含任何许可证检查。

---

## 15. 安全与可观测性

* 机密以 AES-256-GCM 信封加密；数据密钥由来自配置或 KMS 的密钥加密。
* 与安全相关的操作（登录、创建 key、角色变更）写入安全日志。
* 每个请求和任务都带有 trace；日志是带有组织 ID 与请求 ID 的结构化 `slog` 记录；指标通过 OpenTelemetry 与 `/metrics` 导出。
