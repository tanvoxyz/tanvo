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
| 0. 基础 | Monorepo、Nx 插件、mise、lint 规则、许可证、文档、CI | `pnpm nx run-many -t build test lint` 在 CI 中通过 | 进行中 |
| 1. 纯函数库（L0/L1） | `libs/money`、`libs/period`、`libs/expression`（Rust → WASM，Go 宿主）、`libs/pricing`、`libs/metering`（含内存存储与 `storetest`）、`libs/tax` | 各自领域的每个纯 spec 均已移植，或列为跳过并写明原因 | 已完成 |
| 2. 领域核心（L2） | `libs/core`（实体、仓储接口、服务契约）、`libs/billing`（发票生成、费用、额度、优惠券、钱包、渐进式计费、贷项通知单、承诺消费、阈值、按比例计费、升级与降级） | 计费 spec 移植为基于内存仓储的纯决策 | 进行中：`core` 已完成，`billing` 逻辑已完成 |
| 3. 基础设施（L3） | `libs/proto`（API 契约）、`libs/store`（PostgreSQL schema、sqlc、RLS）、`libs/eventstore`（PostgreSQL、ClickHouse）、`libs/ingest`（HTTP 批量、Kafka/Redpanda）、`libs/jobs`（River、调度）、`libs/webhooks`、`libs/documents`（Typst、Factur-X/UBL）、`libs/mail`、`libs/auth` | 一致性测试套件在每个实现上通过；文档的黄金测试通过 | 进行中 |
| 4. 服务端（L4） | `apps/server`：Connect + REST（Vanguard）、OpenAPI、角色 api/worker/consumer/all、配置、健康检查、指标、迁移 | 覆盖每个 API 资源及完整计费周期的端到端测试 | 进行中 |
| 5. 提供者 | 支付提供者（Stripe、Adyen、GoCardless、Cashfree、Flutterwave、Moneyhash），会计/CRM/税务集成，流式目的地 | 每个提供者基于录制 fixture 的测试；端到端的 webhook | 未开始 |
| 6. 控制台 | `apps/web`：基于 Connect API 的管理控制台、客户门户 | 功能覆盖与 API 一致 | 未开始 |
| 7. 企业版与云服务 | `ee/*` 服务（分析、助手、MCP、收入确认等）、cell、RLS 加固、Helm chart | 云部署运行端到端测试套件 | 未开始 |
| 8. 超越功能对齐 | §4 中借鉴自 OpenMeter 与 Flexprice 的改进 | 每项都有自己的设计决定、测试与功能对齐行（标为 Tanvo 新增） | 未开始；在阶段 1–5 完成并测试之后开始 |

## 3. 当前进度

* 阶段 0：脚手架、Nx 插件、许可证、CI 与全部文档（含译本）已就位。
* 阶段 1 已完成：`libs/money`、`libs/period`（移植 623 个用例）、`libs/expression`（151 个用例，Rust → WASM，带输入限制）、`libs/pricing`（433 个用例）、`libs/metering`（478 个用例，含 `storetest` 一致性测试套件）与 `libs/tax`（264 个用例）。[功能对齐](parity.zh-CN.md)将它们的功能标为 `逻辑已完成`。
* 阶段 2：`libs/core` 已完成；`libs/billing` 逻辑已完成（约 1,480 个移植自 lago-api 的用例，含场景测试，其锁有基于 PostgreSQL 存储的测试；未移植的用例列在其 `SKIPPED.md` 文件中），[功能对齐](parity.zh-CN.md)将其已实现的功能标为 `逻辑已完成`；`apps/server` 运行其任务与周期任务，摄取为其提供预付计费项与用量信号；用量、生命周期与钱包告警根据订阅活动与钱包变化评估；除尚待实现的模块（合同、计费片段、定时订单、催收活动、支付、入站 webhook）外，v1.55.0 的每个时钟任务都已运行，目录缓存通过 PostgreSQL 通知在提交时失效。尚待接入：支付与文档。在支付编排（阶段 5）替换之前，`payments.create_invoice_payment` 不发起付款即完成（`apps/server/internal/app/billing.go`，`registerPending`）；其付款状态更新须在 credit 发票支付成功或失败时调用 `wallets.Service.HandleCreditInvoicePayment`，目前尚无调用方；文档任务在 `libs/documents` 接入 blob 存储之前会被取消。
* 阶段 3：`libs/proto`、`libs/eventstore`、`libs/store`（PostgreSQL 模式、迁移、RLS、事务性任务，以及在 memstore 和 PostgreSQL 上均通过的仓储一致性测试）与 `libs/jobs`（River、周期调度、租户公平）已完成；`libs/webhooks`、`libs/ingest`、`libs/auth` 与 `libs/mail` 与 `libs/documents`（Typst PDF/A-3b、Factur-X、UBL 与 CII，含 zh-CN 在内的十种语言）逻辑已完成；15 分钟用量桶在两种事件存储上增量维护，ClickHouse 上另有周期修复任务。核心整理已使领域模型与 v1.55.0 基线对齐：会话、登录状态、设备与多角色成员资格；采购订单号与 x402 字段；发票抵扣、缓存聚合与费用期间；计划、功能与权益的软删除；以及列表筛选及其排序。
* 阶段 4：`apps/server` 提供 `tanvo serve`（角色 api、worker、consumer、all）与 `tanvo migrate`，包含类型化配置、OpenTelemetry、健康与就绪检查、任务注册与周期调度，以及经 Connect 与 REST 的事件摄取，并有端到端测试覆盖；计费已端到端运行（经 EventService 摄取的事件由预付计费与计费时钟任务在期末计费）；其他 API 资源待完成。

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

不采用：带自动迁移的 Ent、uber/fx、Temporal、CEL 表达式、单一加密密钥、静态文件定义的角色、每行都有 `environment_id` 列、投递时才构建的 webhook 载荷、按客户路由事件。
