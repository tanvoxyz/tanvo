# Tanvo

> English | [简体中文](README.zh-CN.md)

Tanvo is an open-source usage-based billing and metering platform with a Go server at its core: event ingestion at any scale (PostgreSQL, or Kafka/Redpanda into ClickHouse), every pricing model, subscriptions, invoices, credits, wallets, payments and integrations, behind strongly typed Protobuf / Connect APIs with a REST surface. It reimplements the functionality of [lago-api](https://github.com/getlago/lago-api) on a modern stack, without compatibility with Lago.

Status: early development (P1 foundations). See the [roadmap](docs/roadmap.md).

## Documentation

| Document | Contents |
| :--- | :--- |
| [Architecture](docs/architecture.md) | System design: technology choices, repository structure, API, storage, events, billing engine, jobs, deployment, licensing |
| [Domain Model](docs/domain.md) | Entities, enumerations, lifecycles and billing rules |
| [Feature Parity](docs/parity.md) | Every lago-api feature and its Tanvo status |
| [Development](docs/development.md) | Toolchain, Nx, Go and Rust conventions, code generation, CI |
| [Testing](docs/testing.md) | Test layers and porting lago-api tests |
| [Roadmap](docs/roadmap.md) | Phases, completion criteria, current status |
| [AGENTS.md](AGENTS.md) | Working rules for AI coding agents and contributors |

## Quick Start

```bash
mise install
pnpm install
pnpm nx run-many -t build test
```

## License

[AGPL-3.0-or-later](LICENSE); `libs/proto` is Apache-2.0 and `ee/` is under the Elastic License 2.0. See [NOTICE](NOTICE).
