# Tanvo

> [English](README.md) | 简体中文

Tanvo 是以 Go 为服务端核心的开源用量计费与计量平台：任意规模的事件摄取（PostgreSQL，或经 Kafka/Redpanda 写入 ClickHouse），全部定价模型，订阅、发票、额度、钱包、支付与集成，对外提供强类型的 Protobuf / Connect API 以及 REST 接口。它在现代技术栈上重新实现 [lago-api](https://github.com/getlago/lago-api) 的功能，不兼容 Lago。

状态：早期开发（P1 基础），见[路线图](docs/roadmap.zh-CN.md)。

## 文档

| 文档 | 内容 |
| :--- | :--- |
| [架构](docs/architecture.zh-CN.md) | 系统设计：技术选型、仓库结构、API、存储、事件、计费引擎、任务、部署、许可证 |
| [领域模型](docs/domain.zh-CN.md) | 实体、枚举、生命周期与计费规则 |
| [功能对齐](docs/parity.zh-CN.md) | lago-api 的每项功能及其在 Tanvo 中的状态 |
| [开发指南](docs/development.zh-CN.md) | 工具链、Nx、Go 与 Rust 约定、代码生成、CI |
| [测试策略](docs/testing.zh-CN.md) | 测试分层与移植 lago-api 测试 |
| [路线图](docs/roadmap.zh-CN.md) | 阶段划分、完成标准、当前进度 |
| [基准测试](docs/benchmarks.zh-CN.md) | 支撑设计决策的测量 |
| [AGENTS.zh-CN.md](AGENTS.zh-CN.md) | AI 编码代理与贡献者的工作规则 |

## 快速开始

```bash
mise install
pnpm install
pnpm nx run-many -t build test
```

## 许可证

[AGPL-3.0-or-later](LICENSE)；`libs/proto` 采用 Apache-2.0，`ee/` 采用 Elastic License 2.0。见 [NOTICE](NOTICE)。
