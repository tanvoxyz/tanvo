# Tanvo 开发指南

> [English](development.md) | 简体中文

> 相关文档：[架构](architecture.zh-CN.md) · [领域模型](domain.zh-CN.md) · [功能对齐](parity.zh-CN.md) · [测试策略](testing.zh-CN.md) · [路线图](roadmap.zh-CN.md) · [AGENTS.zh-CN.md](../AGENTS.zh-CN.md)

---

## 1. 工具链与环境

工具版本由 `mise.toml` 锁定：

| 工具 | 用途 |
| :--- | :--- |
| go | 服务端、库、提供者、企业版服务、工具 |
| node + pnpm | Nx，以及后续的 Web 控制台（`apps/web`）和由 `libs/proto` 生成的 TypeScript 客户端 |
| buf | Protobuf 的 lint、格式化、破坏性变更检查与代码生成 |
| golangci-lint | Go 代码检查与格式检查 |
| sqlc | `libs/store` 中的查询代码 |
| typst | 发票、贷项通知单与收据的 PDF（`libs/documents`）及其黄金测试 |

```bash
mise install            # 安装锁定版本的工具链
pnpm install            # 安装 Nx
```

`pnpm nx` 在 `mise exec` 下运行 Nx（`package.json` 的 `nx` 脚本），因此无论 shell 是否激活了 mise，每个目标都使用锁定版本的工具。在 Nx 之外运行的命令，如在模块中运行 `go test`，经由 `mise exec --` 运行，或在激活了 mise 的 shell 中运行。

Rust 由每个 Cargo workspace 的 `rust-toolchain.toml` 锁定（目前只有 `libs/expression`），rustup 在该目录首次运行 `cargo` 时安装它以及 `wasm32-unknown-unknown` 目标；只需安装 rustup 本身。Go 的构建从不需要 Rust：由它构建的 WASM 模块已提交入库。

迁移由 `libs/store` 以库的形式使用 Atlas 生成；不需要 Atlas CLI。需要 PostgreSQL、ClickHouse、Redpanda 或 Redis 的集成测试依次在 `DOCKER_HOST`、Docker CLI 当前上下文的引擎、OrbStack / Docker Desktop / Colima / Rancher Desktop / Podman 的 socket 中找到的第一个引擎上启动容器。

---

## 2. Monorepo 与 Nx

### 2.1 项目
Nx 负责编排、缓存与计算受影响的项目；每个 Go 模块和 Cargo workspace 仍然可以单独用 `go` 或 `cargo` 工作。

* Go 项目由本地插件 `tools/nx-go` 推断：每个 `go.mod` 是一个项目，以其目录命名（位于名为 `go` 的目录中的模块命名为 `<parent>-go`，例如 `libs/expression/go` → `expression-go`），依赖图来自 `go.mod` 中的仓库内 `require` 指令。
* Cargo workspace 由 `tools/nx-cargo` 推断：每个含 `[workspace]` 表的 `Cargo.toml` 是一个项目；`[workspace.metadata.nx] implicitDependencies` 列出它在 Cargo 之外使用的 Nx 项目。
* 标签：由顶层目录得出的 `type:app|lib|provider|ee|tool`，以及 `lang:go|rust|ts`。

### 2.2 推断出的目标

| 目标 | Go（`tools/nx-go`） | Rust（`tools/nx-cargo`） | 缓存 |
| :--- | :--- | :--- | :--- |
| `build` | `go build ./...`，`CGO_ENABLED=0` | `cargo build --locked --all-targets` | 是 |
| `test` | `go test ./...` | `cargo test --locked` | 是 |
| `bench` | `go test -run=NONE -bench=. -benchmem ./...` | `cargo bench --locked` | 否 |
| `lint` | `golangci-lint run --allow-parallel-runners ./...` | `cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings` | 是 |
| `tidy-check` | `go mod tidy -diff`，`GOWORK=off` | — | 是 |
| `generate` | `buf generate`（仅限含 `buf.yaml` 的项目） | — | 否 |
| `buf-lint` | `buf lint && buf format --diff --exit-code`（仅限含 `buf.yaml` 的项目） | — | 是 |

项目通过放在其 `go.mod` 或 `Cargo.toml` 旁的 `project.json` 添加或覆盖目标。`libs/expression` 添加了 `wasm`，它构建发布版 WASM 模块并复制到 `libs/expression/go/expression.wasm`；该模块提交入库，CI 检查重新构建后没有差异。

```bash
pnpm nx run-many -t build test lint tidy-check      # 全部
pnpm nx affected -t build test lint tidy-check      # 相对 main 受影响的项目
pnpm nx run money:test                              # 单个项目的单个目标
pnpm nx graph                                       # 依赖图
```

---

## 3. Go 模块

* 模块路径为 `github.com/tanvoxyz/tanvo/<dir>`，统一为 `go 1.27.1`，每个模块都列在 `go.work` 中。
* 仓库内的依赖在 `go.mod` 中写为 `require … v0.0.0` 加上相对路径 `replace`，使每个模块都能在 `GOWORK=off` 下 tidy 与构建：

  ```bash
  go mod edit -require=github.com/tanvoxyz/tanvo/libs/money@v0.0.0 -replace=github.com/tanvoxyz/tanvo/libs/money=../money
  GOWORK=off go mod tidy
  ```
* 新增模块：创建目录，`go mod init github.com/tanvoxyz/tanvo/<dir>`，设置 `go 1.27.1`，加入 `go.work`，并在 `.golangci.yml` 中添加它的 depguard 规则（§5）。
* 第三方库必须具有与 AGPL 兼容的许可证，并能在没有 CGO 的情况下工作。

---

## 4. 代码生成

| 源 | 生成器 | 输出 | 命令 |
| :--- | :--- | :--- | :--- |
| `libs/proto/tanvo/**/*.proto` | buf 远程插件（版本固定在 `buf.gen.yaml` 中）：protoc-gen-go（Opaque API）、connect-go v2、connect-openapi（`sudorandom/protoc-gen-connect-openapi`，OpenAPI 3.1）、protoc-gen-es v2 | `libs/proto/gen/` | `pnpm nx run proto:generate` |
| `libs/store/schema/*.sql` | Atlas（以库的形式） | `libs/store/migrations/` | `pnpm nx run store:migrate-diff` |
| `libs/store/queries/*.sql` | sqlc | `libs/store/internal/db/` | `pnpm nx run store:sqlc` |
| `libs/expression/crates/**` | cargo（wasm32-unknown-unknown，release） | `libs/expression/go/expression.wasm` | `pnpm nx run expression:wasm` |

生成的文件提交入库，从不手工编辑；CI 重新生成它们，有差异即失败。

---

## 5. 代码质量

* `golangci-lint` 使用根目录的 `.golangci.yml`：标准检查器外加 `depguard`、`errorlint`、`misspell`、`nolintlint`、`unconvert` 与 `usestdlibvars`；格式化器 `gofumpt` 与 `goimports`，以本仓库作为本地前缀。
* **depguard** 强制执行[架构 §3.2](architecture.zh-CN.md#32-依赖方向) 的依赖方向。每一层有一条规则，列出它不得导入的内容；新模块在同一个提交中获得其规则。最重要的几条：
  * `libs/money` 不导入任何仓库内模块；`libs/period` 与 `libs/expression/go` 只导入 `money`；`libs/pricing`、`libs/metering` 与 `libs/tax` 只导入 `money`、`period` 以及彼此。
  * `libs/billing` 不导入 `store`、`eventstore`、`jobs`、`documents` 或 `webhooks`；它基于 `libs/core` 的接口工作。
  * 基础设施库不导入 `billing`。
  * `ee/*` 只导入 `libs/proto`。
* Rust：`cargo fmt`、`cargo clippy -D warnings`，`unsafe` 只出现在 WASM ABI 模块中。
* Protobuf：`buf lint`、`buf format`，API 发布后再对照 `main` 运行 `buf breaking`。

---

## 6. 持续集成

`.github/workflows/ci.yml` 在 PR 和 `main` 上运行：

1. 配置 mise（锁定版本的工具）以及 Go、pnpm 与 Cargo 的缓存。
2. `pnpm nx affected -t build test lint tidy-check buf-lint`（在 `main` 上为 `run-many`）。
3. 重新生成（`proto:generate`、`store:sqlc`、`expression:wasm`），`git diff` 有差异即失败。
4. 使用 PostgreSQL、ClickHouse 与 Redpanda 容器的集成测试（Linux runner）。

---

## 7. 提交与 PR

* Conventional Commits：`feat(pricing): …`、`fix(billing): …`、`docs: …`、`chore: …`；scope 为项目名。
* 每个提交只包含一个逻辑变更；文档及其译本在同一个提交中修改。
* 不加 `Co-Authored-By` trailer。
* 功能提交更新[功能对齐](parity.zh-CN.md)；阶段变化时更新[路线图](roadmap.zh-CN.md)。
* 贡献需要签署 CLA（[架构 §1.4](architecture.zh-CN.md#14-许可证)）。

---

## 8. RPC 指南

分层见[架构 §4.6](architecture.zh-CN.md#46-服务端分层)；`BillableMetricService`（`libs/billing/metrics`、`apps/server/internal/rpc/metrics.go`、`metrics_test.go`）是参考实现。实现一个服务的步骤：

1. **阅读 Lago 源码**（对齐目标 v1.55.0）：控制器、其服务、序列化器与 request spec。列出每一处偏离及其 `file:line`。
2. **编写领域服务**，放在 `libs/billing/<area>`（或所属的库）中：一个基于 `kernel.Deps` 的 `Service`，每个操作一个方法，各自在一个 `Store.InTx` 中执行，用 `core.Validator` 校验，并在同一事务中通过 `Deps.Webhooks` 发出 webhook、通过 `Repos.Jobs` 入队任务。把 service spec 移植为它在 `memstore` 上的测试，并带 `source:` 行。
3. **编写 handler**，放在 `apps/server/internal/rpc/<service>.go`：嵌入生成的 `Unimplemented…Handler`，用 `principal(ctx)` 读取主体，并使用以下辅助函数：
   * 引用：`Lookup{ByID, ByCode, ByExternalID}.Find(ctx, field, ref)`，只接受 ID 的字段用 `ParseID`；
   * 列表：`Page(size, token)`、`Items(page, convert)`、`ParseFilter` + `Equalities` + `FilterEnum` / `FilterBool` / `FilterSingle`；
   * 更新：`UpdatePaths(mask, allowed, set)`，以资源的 `Has…` 判断字段是否出现；
   * 值：`Opt`、`OptInt`、`Timestamp` / `Time`、`Date` / `FromDate`、`Money` / `FromMoney`、`Decimal` / `ParseDecimal`、`Metadata`、`Struct`、`NewEnum`；
   * 错误：返回 `Error(ctx, err)`；如 `metricError` 那样，用 `validationViolations` 把服务的字段路径加上请求消息的前缀。
4. **注册** handler 到 `newAPI`（`apps/server/internal/app/api.go`）；拦截器负责访问选项、校验与幂等。
5. **移植 request spec** 到 `apps/server/internal/rpc/<service>_test.go`，使用 `rpctest`：`s := rpctest.New(t)` 在全新数据库上启动 API，并带有一个组织、一个完全访问的 key（`s.APIKey`）、一个管理员用户及其会话（`s.Session`）；用 `s.Fixtures`（billingtest）创建测试数据，调用 `s.REST(t, cred, method, path, body)` 或基于 `s.Connect(cred)` 的生成客户端，用 `s.Webhooks(t)` 读取发出的 webhook。用受限权限创建的 key 覆盖 `requires API permission` 示例。设置 `TANVO_STORE_TEST_SCHEMA=source` 时加载 schema 源文件而非迁移。
6. **更新** [对齐表](parity.zh-CN.md) 及其译文。
