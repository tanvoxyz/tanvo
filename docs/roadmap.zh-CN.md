# Tanvo 路线图

> [English](roadmap.md) | 简体中文

> 相关文档：[架构](architecture.zh-CN.md) · [领域模型](domain.zh-CN.md) · [功能对齐](parity.zh-CN.md) · [开发指南](development.zh-CN.md) · [测试策略](testing.zh-CN.md) · [AGENTS.zh-CN.md](../AGENTS.zh-CN.md)

---

## 1. 推进原则

* **自底向上。** 先实现纯函数库，使用例最多的 lago-api spec 针对不含 I/O 的代码移植；每一层在下一层基于它构建之前就已完整并经过测试。
* **完整对齐。** lago-api 每项未废弃的功能都会实现（[功能对齐](parity.zh-CN.md)）；当一个阶段在功能对齐表中的各行都标记为完成时，该阶段才算完成，而不是其中一部分能工作就算完成。
* **始终可运行。** 从阶段 3 起，`apps/server` 可以启动，端到端测试覆盖已有的功能。

## 2. 阶段划分

| 阶段 | 内容 | 完成标准 | 状态 |
| :--- | :--- | :--- | :--- |
| 0. 基础 | Monorepo、Nx 插件、mise、lint 规则、许可证、文档、CI | `pnpm nx run-many -t build test lint` 在 CI 中通过 | 已完成 |
| 1. 纯函数库（L0/L1） | `libs/money`、`libs/period`、`libs/expression`（Rust → WASM，Go 宿主）、`libs/pricing`、`libs/metering`（含内存存储与 `storetest`）、`libs/tax` | 各自领域的每个纯 spec 均已移植，或列为跳过并写明原因 | 已完成 |
| 2. 领域核心（L2） | `libs/core`（实体、仓储接口、服务契约）、`libs/billing`（发票生成、费用、额度、优惠券、钱包、渐进式计费、贷项通知单、承诺消费、阈值、按比例计费、升级与降级） | 计费 spec 移植为基于内存仓储的纯决策 | 已完成 |
| 3. 基础设施（L3） | `libs/proto`（API 契约）、`libs/store`（PostgreSQL schema、sqlc、RLS）、`libs/eventstore`（PostgreSQL、ClickHouse）、`libs/ingest`（HTTP 批量、Kafka/Redpanda）、`libs/jobs`（River、调度）、`libs/webhooks`、`libs/documents`（Typst、Factur-X/UBL）、`libs/mail`、`libs/auth` | 一致性测试套件在每个实现上通过；文档的黄金测试通过 | 已完成 |
| 4. 服务端（L4） | `apps/server`：Connect + REST（Vanguard）、OpenAPI、角色 api/worker/consumer/all、配置、健康检查、指标、迁移 | 覆盖每个 API 资源及完整计费周期的端到端测试 | 已完成 |
| 5. 提供者 | 支付提供者（Stripe、Adyen、GoCardless、Cashfree、Flutterwave、Moneyhash），会计/CRM/税务集成，流式目的地 | 每个提供者基于录制 fixture 的测试；端到端的 webhook | 已完成 |
| 6. 控制台 | `apps/web`：基于 Connect API 的管理控制台、客户门户 | 功能覆盖与 API 一致 | 未开始 |
| 7. 企业版与云服务 | `ee/*` 服务（分析、助手、MCP、收入确认等）、cell、RLS 加固、Helm chart | 云部署运行端到端测试套件 | 进行中：`ee/license`、Data API 契约与令牌、`ee/analytics` 已完成（§5） |
| 8. 超越功能对齐 | §4 中借鉴自 OpenMeter 与 Flexprice 的改进 | 每项都有自己的设计决定、测试与功能对齐行（标为 Tanvo 新增） | 进行中：判定为必要的各项已完成（§4） |

## 3. 当前进度

* 阶段 1–5，即 lago-api v1.55.0 的后端移植，已完成。每个未弃用的功能都有对应的 Tanvo 模块，并在[功能对齐](parity.zh-CN.md)中标为 `已完成`；例外是 v1.55.0 同样未接入的三行 x402（`逻辑已完成`），以及阶段 6、7 的控制台（`apps/web`）与企业版（`ee/*`）各行。
* 已移植领域的每个上游 spec 用例，要么带 `source:` 引用移植，要么写明原因跳过，由 `tools/speccov` 检查（0 个未覆盖）。
* `apps/server` 经 Connect 与 REST 提供每个 API 资源，运行任务与周期调度、PostgreSQL 与 Kafka 两种模式的摄取、文档、邮件、webhook、支付提供者、集成与流式目的地。基于 PostgreSQL、ClickHouse 与 Redpanda 容器的端到端场景覆盖计费周期、计费模型、渐进式计费、按比例计费、贷项通知单、催收、文档、摄取、表达式、用量桶与各提供者。
* `pnpm nx run-many -t buf-lint lint tidy-check build test` 在 CI 中对全部 38 个项目通过，生成代码与 WASM 模块均与源码一致。
* 阶段 8：§4 中判定为必要的各项已完成（Kafka 运维、迁移安全、周期计费前的摄取屏障）；其余各项推迟或不采用，理由见 §4。
* 阶段 6（控制台）尚未开始。阶段 7 已开始：`ee/license`、Data API 契约与服务令牌、`ee/analytics` 已完成（§5）；其他企业服务、cell 与 Helm chart 尚未开始。

## 4. 功能对齐之后：借鉴 OpenMeter 与 Flexprice

移植 lago-api 优先。以下各项来自 2026 年 10 月对 OpenMeter（`openmeterio/openmeter`，Apache-2.0）与 Flexprice（`flexprice/flexprice`）的研究，在移植完成并测试之后于阶段 8 实现。只借鉴思路：OpenMeter 的代码依赖 `float64`、Ent 与需要 CGO 的 Kafka 客户端，Flexprice 的计费逻辑（`internal/ee/`）采用专有许可证，其代码不得复制。

有两条已立即采用，因为它们本就是 lago-api 当前的设计：每个事件一行富化记录、在查询时匹配计费项过滤器，以及 15 分钟的用量桶（[架构 §7.2、§7.4](architecture.zh-CN.md#72-富化)）。

| # | 改进 | 来源 | 落点 |
| :--- | :--- | :--- | :--- |
| 1 | 每次计费用量查询都带一个写入时间截止点并记录在发票上，使刷新、渐进式发票与重新生成可复现，迟到事件的处理可预期 | OpenMeter | `eventstore`、`billing` |
| 2 | 增量计价：按价格组成部分计费“本期累计金额减去已开票金额”，档位变化时生成明确的冲销行 | OpenMeter | `pricing`、`billing` |
| 3 | 每笔费用有确定的键与唯一索引；计费运行比较目标状态与已存状态，使重试与对账安全 | OpenMeter | `billing`、`store` |
| 4 | 预览与执行共用同一条代码路径与同一种请求 | Flexprice | `billing` |
| 5 | 发票的每个外部步骤（税、渲染、同步、支付）有可重试的内部子状态，校验问题记录在发票上 | OpenMeter | `core`、`billing` |
| 6 | 余额检查点（累计用量、钱包实时余额、告警）带宽限窗口，从迟到事件影响的时间点起作废 | OpenMeter、Flexprice | `billing` |
| 7 | 额度按到期顺序消耗，到期释放也按同样顺序 | OpenMeter、Flexprice | `billing` |
| 8 | Kafka 运维：每个消费组一个死信 topic、回填与吵闹租户的独立通道、声明式 topic、消费延迟指标、分区键等于去重键 | OpenMeter、Flexprice | `ingest` |
| 9 | 拒绝已开票期间并记录审计的事件删除 API，以及解释某个事件为何计费或未计费的事件追踪 | Flexprice | `ingest`、`apps/server` |
| 10 | 只读副本与写后固定：请求、消息或任务一旦写入，其后的读取都走主库 | Flexprice | `store` |
| 11 | CI 中的 Atlas 迁移 lint、从模板克隆的测试数据库、长期失败后自动停用的 webhook 端点 | OpenMeter | `store`、`webhooks` |
| 12 | 含税价格、客户免税、窗口化聚合（每个窗口取最大值或求和，如 GPU 小时或峰值并发） | Flexprice | `pricing`、`tax`、`metering` |

2026 年 10 月的决定。只有测量或具体的故障表明有必要时才实现一项；其余各项等待这样的依据。每项新增都记录在[功能对齐 §5](parity.zh-CN.md#5-tanvo-新增)中，并注明移植更新的 lago 版本时要检查什么。

| # | 决定 |
| :--- | :--- |
| 1 | **部分完成。** 真正的缺口是计费边界上的 Kafka 积压：Kafka 模式组织的周期计费现在会等待摄取屏障（[架构 §7.1](architecture.zh-CN.md#71-摄取路径)）。记录在每张发票上的截止点推迟：lago 的草稿刷新有意纳入迟到事件 |
| 2 | **不采用。** 它会改变 lago 按整个周期计算的金额 |
| 3 | **不需要。** 发票已有唯一键，费用以原子方式替换；改为加入 lago 的预付费用唯一索引，并按订阅限定（[功能对齐 §4](parity.zh-CN.md)） |
| 4 | **已经如此。** 预览在一个会回滚的事务中运行计费代码 |
| 5 | **推迟**到控制台（阶段 6），由它展示这些子状态 |
| 6 | **目前不需要。** 如果性能分析表明这些余额计算慢，先为用量桶加入加权求和与去重计数状态 |
| 7 | **不需要。** lago 按整个钱包过期，其消耗顺序已移植 |
| 8 | **已完成：**消费者指标（按结果统计的记录数、延迟、重试时长）、被拒事件的隔离与死信重放。**已经如此：**声明式主题、分区键等于去重键。**不采用：**每个消费者组一个死信主题（死信记录了其来源主题）。**推迟：**回填与吵闹租户的独立通道、按主题的配置 |
| 9 | **推迟** |
| 10 | **推迟**到有部署需要只读副本时 |
| 11 | **已完成：**CI 中的迁移检查（Atlas 分析器与 PostgreSQL 锁检查）以及迁移与查询代码的一致性检查、迁移的锁超时、用于并发索引的无事务文件（[开发指南 §4.1](development.zh-CN.md)）。**已经如此：**从模板克隆测试数据库。**推迟：**停用持续失败的 webhook 端点，lago 从不这样做 |
| 12 | **不采用。** 客户免税已与 lago 对齐；含税价格与窗口聚合会加入 lago 没有的 API 与金额 |

清单之外的一项测量也带来了改动：River 的取任务冷却期把每个队列分片限制在每秒 50 个任务，因此现在为 25 ms 且可调（[基准测试 §1](benchmarks.zh-CN.md#1-任务队列river-与-redis-队列)）。

不采用：带自动迁移的 Ent、uber/fx、Temporal、CEL 表达式、单一加密密钥、静态文件定义的角色、每行都有 `environment_id` 列、投递时才构建的 webhook 载荷、按客户路由事件。

## 5. 阶段 7 计划：Lago 未开源的部分

2026 年 10 月对 lago-api v1.55.0 及其前端做了一次梳理，把 Lago 不开源的功能分为三类。阶段 7 排期后才开始；在此之前，本节就是计划。

**有门控但代码开源。** 30 个 `Organization::PREMIUM_INTEGRATIONS`、`License.premium?` 与功能开关所控制的代码都在 lago-api 之中：渐进式计费、累计用量、预计用量、预览、自动催收、手动付款、收据、付款预授权、NetSuite、Xero、HubSpot、Salesforce、Avalara、Okta、Entra ID、自定义角色、API 密钥权限、安全日志、多开票主体、收入分成、订单表单、指向钱包的事件、发件地址与水印。这些已全部移植到 AGPL 核心，且不设门控（[功能对齐 §1.50](parity.zh-CN.md#150-lago-的开关及其在-tanvo-中的归属)）。`forecasted_usage` 在 v1.55.0 中不控制任何东西，不移植（[功能对齐 §2](parity.zh-CN.md#2-已废弃且不移植)）。

**以自包含设计取代。** 这类是 Lago 自己托管、而自托管的 Tanvo 不应依赖的服务：许可证服务器（`LAGO_LICENSE_URL`）、Nango、OAuth 代理、Segment 跟踪，以及 CS 管理工具及其 Slack 通知。它们已被取代：核心中没有许可证检查，改为直连各提供方的客户端，回调由 `apps/server` 提供，安装实例不发送遥测，并以 `tanvo admin` 取代管理工具（[功能对齐 §3.2](parity.zh-CN.md#32-组件)）。

**逻辑在 lago-api 之外。** 公开的只有客户端一侧：请求过滤条件、响应字段、权限与环境变量。这些功能成为 `ee/` 下的企业服务。每个服务是独立进程，只导入 `libs/proto` 与 `ee/license`（[架构 §14](architecture.zh-CN.md#14-企业版ee)）。Lago 的计算逻辑无从阅读，因此口径由 Tanvo 自行定义。每个口径写入[领域模型](domain.zh-CN.md)，并用 Tanvo 自己的用例测试，因为没有可移植的 lago 测试。

| 顺序 | 服务 | 取代 Lago 的 | 计划 |
| :--- | :--- | :--- | :--- |
| 1 | `ee/license`（库） | `LAGO_LICENSE` 与许可证服务器 | **已完成。** 离线校验的 Ed25519 签名密钥（`tanvo1.<载荷>.<签名>`），以编译进 `ee/license` 的公钥校验，载有被许可方、到期时间与所启用的服务。每个 `ee/*` 服务在启动时及每天校验一次（`license.Checker`），没有有效密钥时拒绝启动。`tanvo-license` 生成签名密钥、签发与查看许可证；生产签名密钥保存在仓库之外。Tanvo Cloud 按套餐签发密钥 |
| 2 | `libs/proto` 中的契约 | Data API 的 REST 接口，以及 GraphQL 中的 `FinanceAssistant*` 与 `AiConversation*` | `tanvo.dataapi.v1`（**已完成**）与 `tanvo.assistant.v1` 两个包，采用 Apache-2.0。核心签发短期签名的服务令牌（Data API 部分**已完成**：`AnalyticsService.CreateDataApiToken`，Ed25519，`tanvo.access.v1.ServiceTokenClaims`，[架构 §14.1](architecture.zh-CN.md#141-分析服务的部署)）。令牌载明组织、成员或 API 密钥及其权限（`data_api:read`、`ai_conversations:create`）。服务用核心的公钥校验令牌，从不接收 API 密钥 |
| 3 | `ee/mcp` | `LAGO_MCP_SERVER_URL` 所指的 MCP 服务器 | 由 `libs/proto` 描述符生成：每个公开 RPC 一个工具，输入模式取自请求消息，描述取自文档注释。除非令牌授予写权限，否则隐藏写操作工具。支持 Streamable HTTP 与 stdio，以调用者的令牌调用核心的 Connect API |
| 4 | `ee/analytics` | Data API（`LAGO_DATA_API_URL`）、Superset 与收入确认仪表盘 | **已完成**（图表等待控制台）。即 `tanvo-analytics` 服务，由 ingress 在 `/v1/data_api/` 下提供（[架构 §14.1](architecture.zh-CN.md#141-分析服务的部署)）。它以专用的 PostgreSQL 角色（`tanvo admin grant-analytics`）读取核心的导出视图（`exports_*`，[功能对齐 §1.43](parity.zh-CN.md#143-数据导出)），配置了副本时读副本；只写入自己的 `analytics` 模式：每个订阅的 MRR 区间、每笔费用的确认计划与套餐目录，组织数据变化时按组织重建。它以 lago 的过滤条件与时间粒度回答 Data API 报表：MRR 及其变动、按套餐的 MRR、收入流及按客户与按套餐的收入、预付额度、用量、用量金额与已开票用量，另有收入确认（按期间的已开票、已确认与递延收入）。定义见[领域 §25](domain.zh-CN.md#25-data-api-报表)。金额保持精确。图表由控制台绘制（阶段 6），不嵌入 Superset。测试在 PostgreSQL 中基于预置数据的组织运行各报表 |
| 5 | `ee/assistant` | 财务助手（`LAGO_FINANCE_ASSISTANT_URL`）与 AI 对话（Mistral 与 `lib/lago_mcp_client`） | 基于可插拔模型提供方的智能体循环：Anthropic、OpenAI、Mistral，或任何兼容 OpenAI 的端点。工具来自 `ee/mcp`，数值问题由 `ee/analytics` 回答。会话与消息按成员存于它自己的模式中，回答通过 Connect 服务端流式推送。结果可导出为 CSV，存入对象存储，链接 7 天有效，与 lago 相同。测试回放录制的模型响应 |
| 6 | `ee/pipeline` | Lago 的数据管道 | 把导出视图及其删除记录增量复制到客户的数据仓库：S3（Parquet）、BigQuery、Snowflake 与 ClickHouse（[功能对齐 §1.43](parity.zh-CN.md#143-数据导出)） |

核心只需要第 1、2 项：令牌签发与契约。服务 3 至 6 互不依赖，只有助手要用 MCP 服务器。它们可以按上述顺序交给不同的代理分别实现。
