# AGENTS.md

> English | [简体中文](AGENTS.zh-CN.md)

This is the repository working guide for AI coding agents (and new developers). It summarizes the rules; the details live in the documents below. When this file conflicts with them, the documents win and this file must be corrected accordingly.

| Document | Contents |
| :--- | :--- |
| [docs/architecture.md](docs/architecture.md) | System design: technology choices, repository structure, dependency direction, API, storage, events, billing engine, jobs, documents, deployment, licensing |
| [docs/domain.md](docs/domain.md) | Domain model: entities, enumerations, lifecycles, billing rules |
| [docs/parity.md](docs/parity.md) | Every lago-api feature, its Tanvo module and status; deprecated features left out |
| [docs/development.md](docs/development.md) | Toolchain, Nx, Go module conventions, code generation, code quality, CI |
| [docs/testing.md](docs/testing.md) | Test layers, porting lago-api tests, test infrastructure |
| [docs/roadmap.md](docs/roadmap.md) | Phases, completion criteria, current status |

## Project Overview

Tanvo is an open-source usage-based billing and metering platform with a Go server at its core. It reimplements every non-deprecated feature of lago-api on a modern stack, without compatibility with Lago. The core is licensed under **AGPL-3.0-or-later**; `libs/proto` under Apache-2.0; `ee/` under the Elastic License 2.0 ([architecture §1.4](docs/architecture.md#14-license)).

The upstream sources used as references live outside this repository: lago-api at `~/Code/frameworks/lago/api` (Rails; the parity baseline is release **v1.55.0**, with no unreleased features; see [parity](docs/parity.md#alignment-baseline)), the events processor at `~/Code/frameworks/lago/events-processor` (Go), lago-expression at `github.com/getlago/lago-expression` (Rust).

## Environment and Common Commands

Tool versions are pinned in `mise.toml` (go, node, pnpm, buf, golangci-lint, sqlc, typst); Rust is pinned by each Cargo workspace's `rust-toolchain.toml`.

```bash
mise install            # Install the pinned toolchain
pnpm install            # Install Nx
pnpm nx run-many -t build test lint tidy-check buf-lint     # Check everything
pnpm nx affected -t build test lint tidy-check buf-lint     # Check affected projects only
pnpm nx show projects                              # List projects (name = directory name)
```

Every Go module also works without Nx: `cd libs/money && mise exec -- go test ./...`. Integration tests that need PostgreSQL, ClickHouse or Redpanda start containers on the local Docker engine (OrbStack, Docker Desktop, …).

## Go Conventions

### Modules and Dependencies
- Every Go project in `apps/*`, `libs/*`, `providers/*`, `ee/*` and `tools/*` is an independent module with module path `github.com/tanvoxyz/tanvo/<dir>`; the `go` directive is `1.27.1` everywhere.
- In-repo dependencies are declared **both** in `go.work` and in each `go.mod` as `require` + relative `replace`, so that `GOWORK=off go mod tidy` works:
  ```bash
  go mod edit -require=github.com/tanvoxyz/tanvo/libs/money@v0.0.0 -replace=github.com/tanvoxyz/tanvo/libs/money=../money
  GOWORK=off go mod tidy
  ```
- New module: create the directory → `go mod init github.com/tanvoxyz/tanvo/<dir>` → set the `go` directive to `1.27.1` → add it to `use` in `go.work`. Nx infers the project from `go.mod`.
- The dependency direction of [architecture §3.2](docs/architecture.md#32-dependency-direction) is enforced by depguard in `.golangci.yml`. **Only `apps/server` does wiring.** `ee/*` imports only `libs/proto`.

### Third-party Dependencies
- **No CGO**: everything builds with `CGO_ENABLED=0`; native capabilities come from child processes or WASM on wazero.
- Licenses must be compatible with AGPL-3.0 (MIT, BSD, ISC, Apache-2.0, MPL-2.0, LGPL, GPL-3.0, AGPL-3.0). GPL-2.0-only and proprietary libraries are not allowed.
- Do not add a library for what the standard library or `golang.org/x/*` already does.
- Decimals are `money.Decimal` (on `cockroachdb/apd/v3`); never `float64` for money or quantities.

### Code Style
- Formatting: gofumpt + goimports (this repository's imports in their own group), checked by golangci-lint.
- Log with `log/slog` (`*Context` variants when a context is available); never `fmt.Print*` for logging.
- Telemetry follows OpenTelemetry ([architecture §15](docs/architecture.md#15-security-and-observability)): libraries use only the OTel API and take providers through options; only `apps/server` imports the SDK and exporters; organization IDs go on spans and logs, never on metric attributes.
- `context.Context` is the first parameter of anything that blocks or does I/O.
- Wrap errors with `fmt.Errorf("...: %w", err)`; inspect with `errors.Is` / `errors.As`; never swallow errors.
- No mutable package-level state; inject dependencies through constructors.
- Prefer modern standard library features: `iter.Seq`, `slices`, `maps`, `cmp`, `net/http` routing patterns, `time` with explicit locations.
- Every package has a package comment (`doc.go`); every exported identifier has a doc comment.
- Times are `time.Time` in UTC at rest; time zones are applied explicitly with `*time.Location` from the IANA database.

## Rust Conventions
- Rust is used only where one implementation must run in several hosts (`libs/expression`: the server through WASM on wazero, the browser through wasm-bindgen).
- Edition 2024; `cargo fmt` and `cargo clippy -- -D warnings` must pass; no `unsafe` outside the WASM ABI boundary.

## Protobuf / Connect Conventions
- Sources in `libs/proto/tanvo/<domain>/v1/`, `edition = "2023"`, package `tanvo.<domain>.v1`.
- Go code with the Opaque API; Connect handlers with simple signatures.
- Public RPCs carry `google.api.http` annotations (AIP resource style) for Vanguard; JSON uses proto names (snake_case).
- After changing a `.proto`, run `pnpm nx run proto:generate` and commit `gen/`; never edit `gen/` by hand. `buf lint` and `buf breaking` must pass.
- Validate requests with protovalidate; mark mandatory fields `(buf.validate.field).required = true`.

## Storage Conventions (libs/store)
- The SQL schema in `libs/store/schema/` is the single source of truth; Atlas generates versioned migrations; **merged migrations are never modified**.
- Every business table has `organization_id`, and every query filters by it.
- Use `Store.InTx` for multi-step writes; enqueue jobs inside the same transaction.
- Only repository interfaces from `libs/core` are exposed upward; sqlc and pgx types stay inside `libs/store`.

## Testing Conventions
- Prefer table-driven tests; failure messages use `got = …, want = …`.
- Port lago-api test cases into `testdata/` files that record their source (`source: spec/services/charge_models/graduated_service_spec.rb:42`); see [docs/testing.md](docs/testing.md).
- Library tests are hermetic; integration tests that need containers are skipped with `t.Skip` when no Docker engine is reachable, and run in CI.

## Documentation
- Documents in `docs/` are written in English with a Simplified Chinese translation beside each (`x.md` and `x.zh-CN.md`), each starting with the language switch line. Change both in the same commit.
- Update [docs/parity.md](docs/parity.md) whenever a feature is implemented, and [docs/roadmap.md](docs/roadmap.md) when a phase's status changes.

## Commits
- Conventional Commits (`feat(money): …`, `fix(billing): …`, `docs: …`); one logical change per commit.
- No `Co-Authored-By` trailers.
