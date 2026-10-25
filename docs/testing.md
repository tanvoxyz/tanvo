# Tanvo Testing Strategy

> English | [简体中文](testing.zh-CN.md)

> Related: [Architecture](architecture.md) · [Domain Model](domain.md) · [Parity](parity.md) · [Development](development.md) · [Roadmap](roadmap.md) · [AGENTS.md](../AGENTS.md)

---

## 1. Test Layers

| Layer | Scope | Where | Runs in |
| :--- | :--- | :--- | :--- |
| Unit tests | Pure logic without I/O: money, periods, expressions, charge models, aggregations, taxes, billing decisions. Table-driven, most cases ported from lago-api specs | `*_test.go` beside the code, data in `testdata/` | Every CI run |
| Conformance tests | One suite run against every implementation of an interface: the event stores of `libs/metering` (in-memory, PostgreSQL, ClickHouse), the repositories of `libs/core` (PostgreSQL), the job queue (River; Redis later) | Exported suites such as `libs/metering/storetest`, run by each implementation | Every CI run; container-backed ones on Linux |
| Integration tests | Real PostgreSQL, ClickHouse, Redpanda, Redis and Typst; provider clients against recorded HTTP fixtures | Beside the code, behind prerequisites (`t.Skip` with a reason when missing, and with `-short`) | CI on Linux runners; locally with a Docker engine |
| Golden tests | Rendered documents (Typst source, extracted PDF text, e-invoice XML), webhook payloads, OpenAPI output | `testdata/golden/`, updated with `-update` | Every CI run |
| End-to-end tests | `apps/server` in-process with containers: API calls through Connect and REST, ingestion, billing runs driven by a fake clock, webhooks received by a test endpoint | `apps/server/internal/e2e` | Every CI run on Linux |

Time-dependent logic takes a clock (`func() time.Time` or an interface) instead of calling `time.Now`; concurrency with timers is tested with `testing/synctest`.

---

## 2. Porting lago-api Specs

lago-api has about 2,700 RSpec files. Tanvo ports their **behavior**, not their structure: Rails factories, ActiveRecord and GraphQL are not reproduced.

### 2.1 Method
1. **Pure specs first.** Specs of computations (charge models, aggregations, dates, taxes, rounding, expressions, proration, credit application) become data files under the target library's `testdata/` (JSON or YAML), run by table-driven Go tests. This is why Tanvo is built bottom-up: the lowest libraries carry the most portable cases.
2. **Provenance.** Every case records `source: spec/<path>_spec.rb:<line>` (the line of its `it`/`context` block), so a reviewer can open the original. Cases written for Tanvo have no `source`.
3. **No silent drops.** A case that is not ported is listed in the same data file under `skipped` with its source and reason: the feature is deprecated ([Parity §Deprecated](parity.md)), the case asserts Rails or GraphQL behavior, or Tanvo deliberately behaves differently (with the decision referenced).
   For service specs, a module's `testdata/specs.json` names the spec files it owns, the reasons, and the skipped cases (`file:line` of a case or an enclosing block, or a whole file). `go run ./tools/speccov [-write] [module…]` checks that every case of those files is either cited by a `source:` line in the module's tests, testdata or shared conformance suites (`porttest`, `storetest`, `buckettest`), or skipped with a known reason; `-write` regenerates the module's `SKIPPED.md` from it.
4. **Service specs** (database, jobs, providers) are ported by hand into integration tests of the library that owns the behavior, with containers instead of factories; their fixtures (provider webhook payloads, API responses) are copied into `testdata/` with a `SOURCES.json` recording each file's origin.
5. **Request specs** (`spec/requests/api/**`) are ported as end-to-end cases of the equivalent Tanvo API, keyed by behavior rather than by Lago's URL or JSON shape.
6. **Upstream bugs.** When a spec encodes behavior that the inventory identified as a bug, the case is kept with the corrected expectation and a `note:` explaining the difference.

### 2.2 Mapping

| lago-api specs | Tanvo target |
| :--- | :--- |
| `spec/services/subscriptions/dates*/**`, `spec/services/utils/datetime*` | `libs/period` |
| lago-expression tests; `spec/**/*expression*` | `libs/expression` (shared by the Rust and Go tests) |
| `spec/services/charge_models/**`, `spec/services/charges/validators/**`, pure parts of `spec/services/fees/**` | `libs/pricing` |
| `spec/services/billable_metrics/**`, `spec/services/events/stores/**` (behavior), charge filter matching | `libs/metering` and its `storetest` suite |
| `spec/services/taxes/**`, applied taxes on fees and invoices | `libs/tax` |
| `spec/services/invoices/**`, `credit_notes/**`, `wallets/**`, `coupons/**`, `fees/**`, `subscriptions/**` | `libs/billing` (pure decisions) and `libs/core` services (integration) |
| `spec/services/payment_providers/**`, `integrations/**` | `providers/*` with recorded fixtures |
| `spec/services/webhooks/**`, `spec/serializers/**` | `libs/webhooks` (payload golden tests) |
| `spec/requests/api/**` | `apps/server/internal/e2e` |
| `spec/graphql/**` | Not ported: the web console uses the Connect API |

---

## 3. Test Infrastructure

* **Containers**: `libs/dockertest` starts the containers of every test on the local Docker engine through the Engine API (§3.1). `libs/store/pgtest` and the `TestMain` of the event store, job and ingestion tests start PostgreSQL 18, ClickHouse and Redpanda once per package (or use `TANVO_TEST_POSTGRES_DSN`, `TANVO_TEST_CLICKHOUSE_DSN`, `TANVO_TEST_KAFKA_BROKERS`). Each test gets its own database or schema.
* **Fake clock**: billing runs, River schedules and expirations are driven by a controllable clock in end-to-end tests.
* **HTTP fixtures**: provider clients are tested against an `httptest` server replaying recorded requests and responses; no test calls a real external service.
* **Golden files**: `go test ./... -update` rewrites golden files; the diff is reviewed like code.

### 3.1 Container Lifecycle

A package removes the containers it starts with `Container.Close`, from `TestMain` or `t.Cleanup`; a package that uses `pgtest` calls `pgtest.Main` from `TestMain`, and `pgtest.New` fails without it. Two independent mechanisms remove the containers of a test process that dies before its cleanup (a `-timeout` panic, SIGKILL, SIGINT):

* **Owner reaping**: every container carries the label `xyz.tanvo.dockertest` with its owner's hostname and process ID and the end of its lifetime. Before it starts a container, `Engine.Run` removes the labeled containers of the same host whose owner process is gone or whose lifetime has expired. A test binary orphaned by a killed `go test` still owns its containers until it exits.
* **Lifetime cap**: the image's entrypoint runs under a `/bin/sh` wrapper, PID 1, that sends the image's stop signal to it when the lifetime is over, then SIGKILL after 10 seconds; the wrapper then exits and the engine removes the container (`AutoRemove`). An image without a shell (`Spec.NoShell`) runs under the engine's init process instead, and a `busybox` sidecar sharing its PID namespace sends the signals. Every image used today (postgres, clickhouse-server, redpanda, redpanda connect, minio) has a shell.

| Variable | Default | Effect |
| :--- | :--- | :--- |
| `TANVO_TEST_CONTAINER_LIFETIME` | `2h` | Lifetime cap of every container, as a Go duration |
| `DOCKER_HOST` | — | Engine to use; otherwise the Docker CLI's current context, then the sockets of OrbStack, Docker Desktop, Colima, Rancher Desktop, rootless Docker and Podman |

`docker ps -a --filter label=xyz.tanvo.dockertest` lists the test containers.
