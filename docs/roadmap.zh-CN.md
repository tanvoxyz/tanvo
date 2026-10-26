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
| 7. 企业版与云服务 | `ee/*` 服务（分析、助手、MCP、收入确认等）、cell、RLS 加固、Helm chart | 云部署运行端到端测试套件 | 未开始 |
| 8. 超越功能对齐 | §4 中借鉴自 OpenMeter 与 Flexprice 的改进 | 每项都有自己的设计决定、测试与功能对齐行（标为 Tanvo 新增） | 进行中：判定为必要的各项已完成（§4） |

## 3. 当前进度

* 阶段 1–5，即 lago-api v1.55.0 的后端移植，已完成。每个未弃用的功能都有对应的 Tanvo 模块，并在[功能对齐](parity.zh-CN.md)中标为 `已完成`；例外是 v1.55.0 同样未接入的三行 x402（`逻辑已完成`），以及阶段 6、7 的控制台（`apps/web`）与企业版（`ee/*`）各行。
* 已移植领域的每个上游 spec 用例，要么带 `source:` 引用移植，要么写明原因跳过，由 `tools/speccov` 检查（0 个未覆盖）。
* `apps/server` 经 Connect 与 REST 提供每个 API 资源，运行任务与周期调度、PostgreSQL 与 Kafka 两种模式的摄取、文档、邮件、webhook、支付提供者、集成与流式目的地。基于 PostgreSQL、ClickHouse 与 Redpanda 容器的端到端场景覆盖计费周期、计费模型、渐进式计费、按比例计费、贷项通知单、催收、文档、摄取、表达式、用量桶与各提供者。
* `pnpm nx run-many -t buf-lint lint tidy-check build test` 在 CI 中对全部 38 个项目通过，生成代码与 WASM 模块均与源码一致。
* 阶段 8：§4 中判定为必要的各项已完成（Kafka 运维、迁移安全、周期计费前的摄取屏障）；其余各项推迟或不采用，理由见 §4。
* 阶段 6（控制台）与阶段 7（企业版与云服务）尚未开始。

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
