# AGENTS.md

> [English](AGENTS.md) | 简体中文

本文件是给 AI 编码代理（以及新加入的开发者）的仓库工作指南。本文件汇总规则，细节见下列文档；与这些文档冲突时以文档为准，并相应修正本文件。

| 文档 | 内容 |
| :--- | :--- |
| [docs/architecture.zh-CN.md](docs/architecture.zh-CN.md) | 系统设计：技术选型、仓库结构、依赖方向、API、存储、事件、计费引擎、任务、文档、部署、许可证 |
| [docs/domain.zh-CN.md](docs/domain.zh-CN.md) | 领域模型：实体、枚举、生命周期、计费规则 |
| [docs/parity.zh-CN.md](docs/parity.zh-CN.md) | lago-api 的每项功能、对应的 Tanvo 模块与状态；已废弃的功能不列入 |
| [docs/development.zh-CN.md](docs/development.zh-CN.md) | 工具链、Nx、Go 模块约定、代码生成、代码质量、CI |
| [docs/testing.zh-CN.md](docs/testing.zh-CN.md) | 测试分层、移植 lago-api 测试、测试基础设施 |
| [docs/roadmap.zh-CN.md](docs/roadmap.zh-CN.md) | 阶段划分、完成标准、当前进度 |

## 项目概览

Tanvo 是以 Go 为服务端核心的开源用量计费与计量平台。它在现代技术栈上重新实现 lago-api 所有未废弃的功能，不兼容 Lago。核心采用 **AGPL-3.0-or-later** 许可证；`libs/proto` 采用 Apache-2.0；`ee/` 采用 Elastic License 2.0（[架构 §1.4](docs/architecture.zh-CN.md#14-许可证)）。

用作参考的上游源码位于本仓库之外：lago-api 在 `~/Code/frameworks/lago/api`（Rails），事件处理器在 `~/Code/frameworks/lago/events-processor`（Go），lago-expression 在 `github.com/getlago/lago-expression`（Rust）。

## 环境与常用命令

工具版本由 `mise.toml` 锁定（go、node、pnpm、buf、golangci-lint、sqlc、typst）；Rust 由每个 Cargo workspace 的 `rust-toolchain.toml` 锁定。

```bash
mise install            # 安装锁定版本的工具链
pnpm install            # 安装 Nx
pnpm nx run-many -t build test lint tidy-check     # 检查全部
pnpm nx affected -t build test lint tidy-check     # 只检查受影响的项目
pnpm nx show projects                              # 列出项目（名称 = 目录名）
```

每个 Go 模块也能脱离 Nx 直接使用：`cd libs/money && mise exec -- go test ./...`。需要 PostgreSQL、ClickHouse 或 Redpanda 的集成测试在本地 Docker 引擎（OrbStack、Docker Desktop 等）上启动容器。

## Go 约定

### 模块与依赖
- `apps/*`、`libs/*`、`providers/*`、`ee/*` 与 `tools/*` 中的每个 Go 项目都是一个独立模块，模块路径为 `github.com/tanvoxyz/tanvo/<dir>`；`go` 指令统一为 `1.27.1`。
- 仓库内的依赖**同时**写进 `go.work` 和各自 `go.mod` 的 `require` + 相对路径 `replace`，使 `GOWORK=off go mod tidy` 能够工作：
  ```bash
  go mod edit -require=github.com/tanvoxyz/tanvo/libs/money@v0.0.0 -replace=github.com/tanvoxyz/tanvo/libs/money=../money
  GOWORK=off go mod tidy
  ```
- 新增模块：创建目录 → `go mod init github.com/tanvoxyz/tanvo/<dir>` → 把 `go` 指令设为 `1.27.1` → 加入 `go.work` 的 `use`。Nx 从 `go.mod` 推断出项目。
- [架构 §3.2](docs/architecture.zh-CN.md#32-依赖方向) 的依赖方向由 `.golangci.yml` 中的 depguard 强制。**只有 `apps/server` 负责装配。** `ee/*` 只导入 `libs/proto`。

### 第三方依赖
- **禁止 CGO**：一切都以 `CGO_ENABLED=0` 构建；原生能力来自子进程或 wazero 运行的 WASM。
- 许可证必须与 AGPL-3.0 兼容（MIT、BSD、ISC、Apache-2.0、MPL-2.0、LGPL、GPL-3.0、AGPL-3.0）。不允许 GPL-2.0-only 与专有库。
- 标准库或 `golang.org/x/*` 已能做到的，不引入第三方库。
- 小数使用 `money.Decimal`（基于 `cockroachdb/apd/v3`）；金额与数量绝不使用 `float64`。

### 代码风格
- 格式化：gofumpt + goimports（本仓库的导入单独分组），由 golangci-lint 检查。
- 日志使用 `log/slog`（有上下文时用 `*Context` 版本）；不用 `fmt.Print*` 输出日志。
- 任何会阻塞或执行 I/O 的函数，第一个参数都是 `context.Context`。
- 错误用 `fmt.Errorf("...: %w", err)` 包装，用 `errors.Is` / `errors.As` 判断；不要吞掉错误。
- 不使用包级可变状态；依赖通过构造函数注入。
- 优先使用现代标准库特性：`iter.Seq`、`slices`、`maps`、`cmp`、`net/http` 路由模式、带显式时区的 `time`。
- 每个包都有包注释（`doc.go`）；每个导出标识符都有文档注释。
- 时间在存储时为 UTC 的 `time.Time`；时区通过来自 IANA 数据库的 `*time.Location` 显式应用。

## Rust 约定
- Rust 只用于同一份实现必须在多个宿主中运行的场景（`libs/expression`：服务端通过 wazero 上的 WASM，浏览器通过 wasm-bindgen）。
- Edition 2024；`cargo fmt` 与 `cargo clippy -- -D warnings` 必须通过；WASM ABI 边界之外不使用 `unsafe`。

## Protobuf / Connect 约定
- 源文件在 `libs/proto/tanvo/<domain>/v1/`，使用 `edition = "2023"`，包名为 `tanvo.<domain>.v1`。
- Go 代码使用 Opaque API；Connect handler 使用 simple 签名。
- 公开 RPC 带有供 Vanguard 使用的 `google.api.http` 注解（AIP 资源风格）；JSON 使用 proto 字段名（snake_case）。
- 修改 `.proto` 后运行 `pnpm nx run proto:generate` 并提交 `gen/`；不要手动编辑 `gen/`。`buf lint` 与 `buf breaking` 必须通过。
- 用 protovalidate 校验请求；必填字段标注 `(buf.validate.field).required = true`。

## 存储约定（libs/store）
- `libs/store/schema/` 中的 SQL schema 是唯一事实来源；Atlas 生成版本化迁移；**已合并的迁移不得修改**。
- 每张业务表都有 `organization_id`，每个查询都按它过滤。
- 多步写入使用 `Store.InTx`；任务在同一事务中入队。
- 对上层只暴露 `libs/core` 中的仓储接口；sqlc 与 pgx 类型留在 `libs/store` 内部。

## 测试约定
- 测试优先写成表驱动测试；失败信息使用 `got = …, want = …` 格式。
- 把 lago-api 的测试用例移植到 `testdata/` 文件中，并记录其来源（`source: spec/services/charge_models/graduated_service_spec.rb:42`）；见 [docs/testing.zh-CN.md](docs/testing.zh-CN.md)。
- 库测试不依赖外部环境；需要容器的集成测试在无法连接 Docker 引擎时以 `t.Skip` 跳过，并在 CI 中运行。

## 文档
- `docs/` 中的文档以英文撰写，每份旁边附有简体中文译本（`x.md` 与 `x.zh-CN.md`），两者都以语言切换行开头。在同一个提交中同时修改两者。
- 每实现一项功能就更新 [docs/parity.zh-CN.md](docs/parity.zh-CN.md)，阶段状态变化时更新 [docs/roadmap.zh-CN.md](docs/roadmap.zh-CN.md)。

## 提交
- 使用 Conventional Commits（`feat(money): …`、`fix(billing): …`、`docs: …`）；每个提交只包含一个逻辑变更。
- 不加 `Co-Authored-By` trailer。
