# Tanvo Development Guide

> English | [简体中文](development.zh-CN.md)

> Related: [Architecture](architecture.md) · [Domain Model](domain.md) · [Parity](parity.md) · [Testing](testing.md) · [Roadmap](roadmap.md) · [AGENTS.md](../AGENTS.md)

---

## 1. Toolchain and Setup

Tool versions are pinned in `mise.toml`:

| Tool | Purpose |
| :--- | :--- |
| go | Server, libraries, providers, enterprise services, tools |
| node + pnpm | Nx, and later the web console (`apps/web`) and the TypeScript client generated from `libs/proto` |
| buf | Protobuf lint, format, breaking-change checks and code generation |
| golangci-lint | Go linting and formatting checks |
| sqlc | Query code in `libs/store` |
| typst | Invoice, credit note and receipt PDFs (`libs/documents`) and their golden tests |

```bash
mise install            # Install the pinned toolchain
pnpm install            # Install Nx
```

`pnpm nx` runs Nx under `mise exec` (the `nx` script of `package.json`), so every target uses the pinned tools whether or not the shell activates mise. Commands run outside Nx, such as `go test` in a module, run under `mise exec --` or in a shell that activates mise.

Rust is pinned by each Cargo workspace's `rust-toolchain.toml` (today only `libs/expression`), which rustup installs on the first `cargo` command there, together with the `wasm32-unknown-unknown` target; only rustup itself is needed. Go builds never need Rust: the WASM module built from it is committed.

Migrations are generated with Atlas used as a library from `libs/store`; no Atlas CLI is needed. Integration tests that need PostgreSQL, ClickHouse, Redpanda or Redis start containers on `DOCKER_HOST`, else on the engine of the Docker CLI's current context, else on the first engine found at the sockets of OrbStack, Docker Desktop, Colima, Rancher Desktop or Podman.

---

## 2. Monorepo and Nx

### 2.1 Projects
Nx orchestrates, caches and computes affected projects; every Go module and Cargo workspace still works on its own with `go` or `cargo`.

* Go projects are inferred by the local plugin `tools/nx-go`: every `go.mod` is a project, named after its directory (a module in a directory named `go` is named `<parent>-go`, e.g. `libs/expression/go` → `expression-go`), and the dependency graph comes from the in-repo `require` directives of `go.mod`.
* Cargo workspaces are inferred by `tools/nx-cargo`: every `Cargo.toml` with a `[workspace]` table is a project; `[workspace.metadata.nx] implicitDependencies` names the Nx projects it uses outside Cargo.
* Tags: `type:app|lib|provider|ee|tool` from the top-level directory, `lang:go|rust|ts`.

### 2.2 Inferred Targets

| Target | Go (`tools/nx-go`) | Rust (`tools/nx-cargo`) | Cached |
| :--- | :--- | :--- | :--- |
| `build` | `go build ./...` with `CGO_ENABLED=0` | `cargo build --locked --all-targets` | Yes |
| `test` | `go test ./...` | `cargo test --locked` | Yes |
| `bench` | `go test -run=NONE -bench=. -benchmem ./...` | `cargo bench --locked` | No |
| `lint` | `golangci-lint run --allow-parallel-runners ./...` | `cargo fmt --check && cargo clippy --locked --all-targets -- -D warnings` | Yes |
| `tidy-check` | `go mod tidy -diff` with `GOWORK=off` | — | Yes |
| `generate` | `buf generate` (projects with a `buf.yaml`) | — | No |
| `buf-lint` | `buf lint && buf format --diff --exit-code` (projects with a `buf.yaml`) | — | Yes |

A project adds or overrides targets with a `project.json` beside its `go.mod` or `Cargo.toml`. `libs/expression` adds `wasm`, which builds the release WASM module and copies it to `libs/expression/go/expression.wasm`; the module is committed, and CI checks that rebuilding it leaves no diff.

```bash
pnpm nx run-many -t build test lint tidy-check      # Everything
pnpm nx affected -t build test lint tidy-check      # Projects affected since main
pnpm nx run money:test                              # One target of one project
pnpm nx graph                                       # Dependency graph
```

---

## 3. Go Modules

* Module path `github.com/tanvoxyz/tanvo/<dir>`, `go 1.27.1` everywhere, every module listed in `go.work`.
* In-repo dependencies are declared in `go.mod` as `require … v0.0.0` plus a relative `replace`, so that each module tidies and builds with `GOWORK=off`:

  ```bash
  go mod edit -require=github.com/tanvoxyz/tanvo/libs/money@v0.0.0 -replace=github.com/tanvoxyz/tanvo/libs/money=../money
  GOWORK=off go mod tidy
  ```
* New module: create the directory, `go mod init github.com/tanvoxyz/tanvo/<dir>`, set `go 1.27.1`, add it to `go.work`, add its depguard rule to `.golangci.yml` (§5).
* Third-party libraries must have AGPL-compatible licenses and work without CGO.

---

## 4. Code Generation

| Source | Generator | Output | Command |
| :--- | :--- | :--- | :--- |
| `libs/proto/tanvo/**/*.proto` | buf remote plugins (pinned in `buf.gen.yaml`): protoc-gen-go (Opaque API), connect-go v2, connect-openapi (`sudorandom/protoc-gen-connect-openapi`, OpenAPI 3.1), protoc-gen-es v2 | `libs/proto/gen/` | `pnpm nx run proto:generate` |
| `libs/store/schema/*.sql` | Atlas (as a library) | `libs/store/migrations/` | `pnpm nx run store:migrate-diff` |
| `libs/store/queries/*.sql` | sqlc | `libs/store/internal/db/` | `pnpm nx run store:sqlc` |
| `libs/expression/crates/**` | cargo (wasm32-unknown-unknown, release) | `libs/expression/go/expression.wasm` | `pnpm nx run expression:wasm` |

Generated files are committed and never edited by hand; CI regenerates them and fails on a diff.

---

## 5. Code Quality

* `golangci-lint` with the root `.golangci.yml`: the standard linters plus `depguard`, `errorlint`, `misspell`, `nolintlint`, `unconvert` and `usestdlibvars`; formatters `gofumpt` and `goimports` with this repository as the local prefix.
* **depguard** enforces the dependency direction of [Architecture §3.2](architecture.md#32-dependency-direction). Each layer has a rule naming what it may not import; a new module gets a rule in the same commit. The most important ones:
  * `libs/money` imports nothing in-repo; `libs/period` and `libs/expression/go` only `money`; `libs/pricing`, `libs/metering` and `libs/tax` only `money`, `period` and each other.
  * `libs/billing` does not import `store`, `eventstore`, `jobs`, `documents` or `webhooks`; it works on interfaces of `libs/core`.
  * Infrastructure libraries do not import `billing`.
  * `ee/*` imports only `libs/proto`.
* Rust: `cargo fmt`, `cargo clippy -D warnings`, `unsafe` only in the WASM ABI module.
* Protobuf: `buf lint`, `buf format`, and `buf breaking` against `main` once the API is published.

---

## 6. Continuous Integration

`.github/workflows/ci.yml` runs on pull requests and on `main`:

1. Set up mise (pinned tools) and the Go, pnpm and Cargo caches.
2. `pnpm nx affected -t build test lint tidy-check buf-lint` (`run-many` on `main`).
3. Regenerate (`proto:generate`, `store:sqlc`, `expression:wasm`) and fail on `git diff`.
4. Integration tests with PostgreSQL, ClickHouse and Redpanda containers (Linux runners).

---

## 7. Commits and Pull Requests

* Conventional Commits: `feat(pricing): …`, `fix(billing): …`, `docs: …`, `chore: …`; the scope is the project name.
* One logical change per commit; documents and their translations change in the same commit.
* No `Co-Authored-By` trailers.
* A feature commit updates [Parity](parity.md); a phase change updates [Roadmap](roadmap.md).
* Contributions require the CLA ([Architecture §1.4](architecture.md#14-license)).

---

## 8. RPC Playbook

The layering is [architecture §4.6](architecture.md#46-server-layering); `BillableMetricService` (`libs/billing/metrics`, `apps/server/internal/rpc/metrics.go`, `metrics_test.go`) is the reference implementation. To implement a service:

1. **Read the Lago sources** at the v1.55.0 parity target: the controller, its services, serializer and request spec. List every deviation with its `file:line`.
2. **Write the domain service** in `libs/billing/<area>` (or the owning library): a `Service` over `kernel.Deps` with one method per operation, each in one `Store.InTx`, validating with `core.Validator` and emitting webhooks through `Deps.Webhooks` and jobs through `Repos.Jobs` in the same transaction. Port the service specs to its tests on `memstore`, with `source:` lines.
3. **Write the handler** in `apps/server/internal/rpc/<service>.go`: embed the generated `Unimplemented…Handler`, read the principal with `principal(ctx)`, and use the helpers:
   * references: `Lookup{ByID, ByCode, ByExternalID}.Find(ctx, field, ref)` and `ParseID` for fields that take only IDs;
   * lists: `Page(size, token)`, `Items(page, convert)`, `ParseFilter` + `Equalities` + `FilterEnum` / `FilterBool` / `FilterSingle`;
   * updates: `UpdatePaths(mask, allowed, set)` with the `Has…` presence of the resource;
   * values: `Opt`, `OptInt`, `Timestamp` / `Time`, `Date` / `FromDate`, `Money` / `FromMoney`, `Decimal` / `ParseDecimal`, `Metadata`, `Struct`, `NewEnum`;
   * errors: return `Error(ctx, err)`; prefix a service's field paths onto the request message with `validationViolations` as `metricError` does.
4. **Register** the handler in `newAPI` (`apps/server/internal/app/api.go`); the interceptors apply the access option, validation and idempotency.
5. **Port the request spec** to `apps/server/internal/rpc/<service>_test.go` with `rpctest`: `s := rpctest.New(t)` starts the API on a fresh database with an organization, a full-access key (`s.APIKey`), an admin user and its session (`s.Session`); create fixtures with `s.Fixtures` (billingtest), call `s.REST(t, cred, method, path, body)` or a generated client over `s.Connect(cred)`, and read the emitted webhooks with `s.Webhooks(t)`. Cover the `requires API permission` examples with keys created with restricted permissions. Run with `TANVO_STORE_TEST_SCHEMA=source` to load the schema sources instead of the migrations.
6. **Update** [Parity](parity.md) and its translation.
