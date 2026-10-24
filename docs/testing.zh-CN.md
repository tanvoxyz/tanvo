# Tanvo 测试策略

> [English](testing.md) | 简体中文

> 相关文档：[架构](architecture.zh-CN.md) · [领域模型](domain.zh-CN.md) · [功能对齐](parity.zh-CN.md) · [开发指南](development.zh-CN.md) · [路线图](roadmap.zh-CN.md) · [AGENTS.zh-CN.md](../AGENTS.zh-CN.md)

---

## 1. 测试分层

| 层次 | 范围 | 位置 | 运行时机 |
| :--- | :--- | :--- | :--- |
| 单元测试 | 不含 I/O 的纯逻辑：金额、周期、表达式、计费模型、聚合、税、计费决策。表驱动，大部分用例移植自 lago-api 的 spec | 代码旁的 `*_test.go`，数据放在 `testdata/` | 每次 CI |
| 一致性测试 | 同一套测试针对某个接口的每个实现运行：`libs/metering` 的事件存储（内存、PostgreSQL、ClickHouse）、`libs/core` 的仓储（PostgreSQL）、任务队列（River；以后还有 Redis） | 导出的测试套件，如 `libs/metering/storetest`，由每个实现运行 | 每次 CI；依赖容器的在 Linux 上运行 |
| 集成测试 | 真实的 PostgreSQL、ClickHouse、Redpanda、Redis 与 Typst；提供者客户端针对录制的 HTTP fixture | 代码旁，依赖前置条件（缺失时以及使用 `-short` 时 `t.Skip` 并写明原因） | Linux runner 上的 CI；本地需有 Docker 引擎 |
| 黄金测试 | 渲染出的文档（Typst 源码、提取的 PDF 文本、电子发票 XML）、webhook 负载、OpenAPI 输出 | `testdata/golden/`，用 `-update` 更新 | 每次 CI |
| 端到端测试 | 进程内的 `apps/server` 加容器：经由 Connect 与 REST 的 API 调用、摄取、由假时钟驱动的计费运行、由测试端点接收的 webhook | `apps/server/internal/e2e` | 每次 CI，在 Linux 上 |

依赖时间的逻辑接收一个时钟（`func() time.Time` 或一个接口），而不是调用 `time.Now`；带定时器的并发逻辑用 `testing/synctest` 测试。

---

## 2. 移植 lago-api 的 spec

lago-api 约有 2,700 个 RSpec 文件。Tanvo 移植的是它们的**行为**，而不是结构：不复刻 Rails 的 factory、ActiveRecord 与 GraphQL。

### 2.1 方法
1. **先移植纯 spec。** 计算类 spec（计费模型、聚合、日期、税、舍入、表达式、按比例计费、额度抵扣）转为目标库 `testdata/` 下的数据文件（JSON 或 YAML），由表驱动的 Go 测试运行。这正是 Tanvo 自底向上构建的原因：最底层的库承载最多可移植的用例。
2. **来源记录。** 每条用例记录 `source: spec/<path>_spec.rb:<line>`（其 `it`/`context` 块所在的行），以便评审者打开原文。为 Tanvo 编写的用例没有 `source`。
3. **不允许静默丢弃。** 未移植的用例在同一数据文件的 `skipped` 下列出，附带来源与原因：该功能已废弃（[功能对齐 §Deprecated](parity.zh-CN.md)）、该用例断言的是 Rails 或 GraphQL 的行为，或 Tanvo 有意采取不同行为（并引用相应决定）。
   对于服务 spec，模块的 `testdata/specs.json` 列出它负责的 spec 文件、原因以及跳过的用例（某个用例或其外层块的 `file:line`，或整个文件）。`go run ./tools/speccov [-write] [module…]` 检查这些文件的每条用例要么被该模块的测试、testdata 或 `porttest` 套件中的 `source:` 行引用，要么以已知原因跳过；`-write` 据此重新生成模块的 `SKIPPED.md`。
4. **服务 spec**（数据库、任务、提供者）由人工移植为拥有该行为的库中的集成测试，用容器代替 factory；其 fixture（提供者 webhook 负载、API 响应）复制到 `testdata/`，并附带记录每个文件来源的 `SOURCES.json`。
5. **请求 spec**（`spec/requests/api/**`）移植为对应 Tanvo API 的端到端用例，以行为而非 Lago 的 URL 或 JSON 结构为准。
6. **上游 bug。** 当某个 spec 编码的行为在清单中被认定为 bug 时，保留该用例，改为修正后的预期，并附上解释差异的 `note:`。

### 2.2 映射表

| lago-api spec | Tanvo 目标 |
| :--- | :--- |
| `spec/services/subscriptions/dates*/**`、`spec/services/utils/datetime*` | `libs/period` |
| lago-expression 的测试；`spec/**/*expression*` | `libs/expression`（Rust 与 Go 测试共用） |
| `spec/services/charge_models/**`、`spec/services/charges/validators/**`、`spec/services/fees/**` 的纯逻辑部分 | `libs/pricing` |
| `spec/services/billable_metrics/**`、`spec/services/events/stores/**`（行为）、计费项过滤器匹配 | `libs/metering` 及其 `storetest` 套件 |
| `spec/services/taxes/**`、费用与发票上的已应用税 | `libs/tax` |
| `spec/services/invoices/**`、`credit_notes/**`、`wallets/**`、`coupons/**`、`fees/**`、`subscriptions/**` | `libs/billing`（纯决策）与 `libs/core` 的服务（集成） |
| `spec/services/payment_providers/**`、`integrations/**` | `providers/*`，使用录制的 fixture |
| `spec/services/webhooks/**`、`spec/serializers/**` | `libs/webhooks`（负载的黄金测试） |
| `spec/requests/api/**` | `apps/server/internal/e2e` |
| `spec/graphql/**` | 不移植：Web 控制台使用 Connect API |

---

## 3. 测试基础设施

* **容器**：`libs/store/pgtest`、`libs/eventstore/chtest` 与 `libs/ingest/kafkatest` 在本地 Docker 引擎上为每个包各启动一次 PostgreSQL 18、ClickHouse 与 Redpanda（或使用 `TANVO_TEST_POSTGRES_DSN`、`TANVO_TEST_CLICKHOUSE_DSN`、`TANVO_TEST_KAFKA_BROKERS`）。每个测试有自己的数据库或 schema。
* **假时钟**：在端到端测试中，计费运行、River 调度与过期由一个可控的时钟驱动。
* **HTTP fixture**：提供者客户端针对一个回放录制请求与响应的 `httptest` 服务器测试；没有任何测试调用真实的外部服务。
* **黄金文件**：`go test ./... -update` 重写黄金文件；其差异像代码一样接受评审。
