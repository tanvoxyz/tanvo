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
| 2. 领域核心（L2） | `libs/core`（实体、仓储接口、服务契约）、`libs/billing`（发票生成、费用、额度、优惠券、钱包、渐进式计费、贷项通知单、承诺消费、阈值、按比例计费、升级与降级） | 计费 spec 移植为基于内存仓储的纯决策 | 进行中：`core` |
| 3. 基础设施（L3） | `libs/proto`（API 契约）、`libs/store`（PostgreSQL schema、sqlc、RLS）、`libs/eventstore`（PostgreSQL、ClickHouse）、`libs/ingest`（HTTP 批量、Kafka/Redpanda）、`libs/jobs`（River、调度）、`libs/webhooks`、`libs/documents`（Typst、Factur-X/UBL）、`libs/mail`、`libs/auth` | 一致性测试套件在每个实现上通过；文档的黄金测试通过 | 未开始 |
| 4. 服务端（L4） | `apps/server`：Connect + REST（Vanguard）、OpenAPI、角色 api/worker/consumer/all、配置、健康检查、指标、迁移 | 覆盖每个 API 资源及完整计费周期的端到端测试 | 未开始 |
| 5. 提供者 | 支付提供者（Stripe、Adyen、GoCardless、Cashfree、Flutterwave、Moneyhash），会计/CRM/税务集成，流式目的地 | 每个提供者基于录制 fixture 的测试；端到端的 webhook | 未开始 |
| 6. 控制台 | `apps/web`：基于 Connect API 的管理控制台、客户门户 | 功能覆盖与 API 一致 | 未开始 |
| 7. 企业版与云服务 | `ee/*` 服务（分析、助手、MCP、收入确认等）、cell、RLS 加固、Helm chart | 云部署运行端到端测试套件 | 未开始 |

## 3. 当前进度

* 阶段 0：脚手架、Nx 插件、许可证、CI 与全部文档（含译本）已就位。
* 阶段 1 已完成：`libs/money`、`libs/period`（移植 623 个用例）、`libs/expression`（151 个用例，Rust → WASM，带输入限制）、`libs/pricing`（433 个用例）、`libs/metering`（478 个用例，含 `storetest` 一致性测试套件）与 `libs/tax`（264 个用例）。[功能对齐](parity.zh-CN.md)将它们的功能标为 `逻辑已完成`。
* 阶段 2：`libs/core` 正在构建，随后是 `libs/billing`。
