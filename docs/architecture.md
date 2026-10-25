# Tanvo Architecture Blueprint

> English | [简体中文](architecture.zh-CN.md)

> Related: [Domain Model](domain.md) · [Feature Parity](parity.md) · [Development](development.md) · [Testing](testing.md) · [Roadmap](roadmap.md) · [AGENTS.md](../AGENTS.md)

> Tanvo is an open-source usage-based billing and metering platform with a Go server at its core. It reimplements the functionality of [lago-api](https://github.com/getlago/lago-api) — metering, pricing, subscriptions, invoicing, credits, payments, integrations — on a modern stack. It is not compatible with Lago's API or data; wherever Lago carries legacy designs, Tanvo takes the best design available today.

---

## 1. Positioning and Principles

### 1.1 Goals
* **Feature parity with lago-api**: every lago-api feature that is not deprecated is implemented; [Feature Parity](parity.md) tracks each one.
* **One codebase, two deployments**: the same binary runs a single-tenant self-hosted installation (Tanvo plus PostgreSQL) and the multi-tenant Tanvo Cloud on Kubernetes; the difference lies only in configuration and in which implementations are wired behind the ports (§13).
* **Correct money**: amounts are exact decimals end to end; every state change and the work it triggers commit atomically (§6, §9).
* **Strongly typed contracts**: Protobuf + Connect for every API, with a resource-style REST/JSON surface transcoded from the same contracts (§4).
* **Scale by design**: high-volume event ingestion through Kafka/Redpanda into ClickHouse, horizontal workers, and cells for Tanvo Cloud (§7, §13).
* **Small operational footprint**: the minimal installation is one `CGO_ENABLED=0` binary, PostgreSQL and the `typst` executable; Redis, Kafka, ClickHouse and object storage are optional.

### 1.2 Non-goals
* API, webhook or database compatibility with Lago, or migration tooling from Lago (may come later as a separate tool).
* Legacy Lago features that Lago itself deprecated (listed in [Feature Parity §2](parity.md)).
* Databases other than PostgreSQL for business data.

### 1.3 Engineering Principles
1. **Bottom-up**: pure-function libraries (money, periods, expressions, pricing, metering, tax) come first, each complete with tests ported from lago-api before the next layer is built on top.
2. **No CGO**: native capabilities come from child processes (`typst`) or WASM modules on wazero (`libs/expression`).
3. **Libraries are usable on their own**: every Go library is an independent module that `go test`s without Nx; Nx only orchestrates and caches.
4. **Contract first**: `libs/proto` is the single source of truth for every API and cross-process boundary.
5. **Ports and adapters**: `libs/core` defines the domain and the ports; infrastructure (`store`, `eventstore`, `jobs`, providers) implements them; only `apps/server` wires.
6. **Tenant scoping everywhere**: every business row carries `organization_id`, and every query is scoped by it (§5.4).

### 1.4 License
Tanvo is derived from lago-api and lago-expression, both distributed under the **GNU AGPL-3.0**; the repository's code is therefore licensed under **AGPL-3.0-or-later**, with these exceptions:

| Path | License | Why |
| :--- | :--- | :--- |
| everything not listed below | AGPL-3.0-or-later | Derived from Lago |
| `libs/proto` (contracts, generated code) and published client SDKs | Apache-2.0 | Designed from scratch; clients and enterprise services link them |
| `ee/` (enterprise services) | Elastic License 2.0 | Source-available enterprise features (§14) |

Rules that follow from this:
* Code under `ee/` runs as **separate processes** and talks to the core only through `libs/proto` contracts and read-only data views; it **never imports an AGPL Go package or crate** (enforced by depguard, §3.2).
* `libs/proto` contains only contracts designed for Tanvo, never code ported from Lago.
* Third-party dependencies must be compatible with AGPL-3.0: MIT, BSD, ISC, Apache-2.0, MPL-2.0, LGPL, GPL-3.0, AGPL-3.0. GPL-2.0-only and proprietary libraries (River Pro, Sidekiq Pro, …) are not allowed.
* Contributions are accepted under a CLA so that the project can keep its licensing structure.
* `NOTICE` records the upstream copyrights and the changes.

---

## 2. Technology Choices

| Area | Choice | Notes |
| :--- | :--- | :--- |
| Server language | **Go (latest stable, currently 1.27)** | `iter.Seq`, `log/slog`, `testing/synctest`, `testing.B.Loop`, go.mod `tool` directive |
| Contracts and RPC | **Protobuf (edition 2023) + buf + Connect** (`connect-go`; `protobuf-es` / `connect-es` for TypeScript) | `buf lint` + `buf breaking`; **protovalidate** for request validation |
| REST | **[Vanguard](https://github.com/connectrpc/vanguard-go)** transcoding from `google.api.http` annotations | One implementation serves Connect, gRPC, gRPC-Web and REST/JSON (§4.2) |
| API reference | **protoc-gen-connect-openapi** + Scalar | OpenAPI 3.1 from the same annotations |
| HTTP | Standard library `net/http` (routing patterns, `http.Protocols` for h2c) | Provider webhooks and file downloads are plain HTTP handlers |
| Database | **PostgreSQL 17+** | `pgx/v5` native driver, numeric for decimals, partitioned tables, advisory locks |
| Queries and migrations | **sqlc** (pgx) + **Atlas** (declarative schema → versioned migrations) | Explicit SQL, generated type-safe Go; no ORM (§5) |
| Identifiers | **UUIDv7** | Time-ordered, index friendly |
| Decimals | **`cockroachdb/apd/v3`** wrapped by `libs/money` | Arbitrary precision, explicit rounding (§6) |
| Background jobs | **River** (PostgreSQL) | Transactional enqueue, unique and periodic jobs, leader election; Redis backend is a later option (§9) |
| Event streaming | **Kafka API (Redpanda / Kafka)** with **franz-go** | Optional high-throughput ingestion path (§7) |
| Event analytics store | **ClickHouse** (`clickhouse-go/v2`) | Optional; PostgreSQL partitions are the default (§7.4) |
| Cache, rate limits | In-process / PostgreSQL by default; **Redis or Valkey** optional (`go-redis/v9`) | Behind the `Cache` and `RateLimiter` ports |
| Expressions | **lago-expression ported to Rust** → WASM, run on **wazero** in Go and in the browser | Exact decimal semantics, one implementation for every host (§7.3) |
| Documents | **Typst** (child process) | PDF/A-3 with embedded e-invoice XML (Factur-X / ZUGFeRD); deterministic output (§10) |
| E-invoicing | UBL 2.1 and UN/CEFACT CII generated in Go | Peppol BIS, Factur-X, XRechnung profiles |
| Email | SMTP (`net/smtp` via `wneessen/go-mail`), `html/template` | Provider APIs (SES, …) behind the `Mailer` port |
| Outbound webhooks | **Standard Webhooks** signatures (HMAC-SHA256, `webhook-id` / `webhook-timestamp` / `webhook-signature`) | Replaces Lago's JWT/HMAC variants |
| Authentication | API keys (hashed), OIDC (Google, Okta, Entra ID, any OIDC IdP), email + password (argon2id), sessions | `coreos/go-oidc`, `golang.org/x/oauth2` |
| Authorization | Permission strings per role and per API key | Same permission model for users and keys (§4.4) |
| Encryption at rest | AES-256-GCM envelope encryption for secrets (provider credentials, tokens) | Keys from configuration or a KMS |
| Observability | **OpenTelemetry** (traces, metrics, logs) over OTLP, `log/slog` bridged to OTel logs; optional Prometheus endpoint | Standard `OTEL_*` configuration; other backends (Sentry, …) through the OTel Collector (§15) |
| Command line and settings | **Cobra** + **koanf** | Flags, `TANVO_*` environment variables, `tanvo.toml` |
| Web UI | React + Vite + TanStack Router/Query + connect-es (`apps/web`, later phase) | Uses the same Connect contracts |

Engineering tooling — Nx, mise, buf, golangci-lint, CI — is described in [Development](development.md).

---

## 3. Repository Structure

### 3.1 Directory Structure

```text
tanvo/
├── go.work                       # Aggregates all Go modules
├── nx.json  package.json  pnpm-workspace.yaml  mise.toml
├── LICENSE  NOTICE
│
├── apps/
│   ├── server/                   # [Go] cmd/tanvo: wiring, configuration, roles, Connect services, REST, provider webhooks
│   └── web/                      # [TS] Admin UI and customer portal (later phase)
│
├── libs/
│   ├── proto/                    # [Go + TS] Contracts (Apache-2.0): *.proto, buf config, generated code
│   ├── money/                    # [Go] Decimals, currencies, amounts, rounding, allocation
│   ├── period/                   # [Go] Billing intervals, billing periods, anniversaries, time zones, proration ratios
│   ├── expression/               # [Rust → WASM] Event expressions; Go host package on wazero (go/)
│   ├── pricing/                  # [Go] Charge models: standard, graduated, graduated %, package, percentage, volume, dynamic; filters, grouping
│   ├── metering/                 # [Go] Aggregations: count, sum, max, unique count, weighted sum, latest; prorated and recurring variants
│   ├── tax/                      # [Go] Tax application, VAT identifiers, VIES client
│   ├── core/                     # [Go] Domain model and ports; no infrastructure dependencies
│   ├── billing/                  # [Go] Billing engine: fees, invoices, coupons, credits, wallets, credit notes, progressive billing, commitments
│   ├── store/                    # [Go] PostgreSQL schema (Atlas), sqlc queries, repository implementations
│   ├── eventstore/               # [Go] Event stores: PostgreSQL partitions and ClickHouse; aggregation queries
│   ├── ingest/                   # [Go] Event ingestion: validation, deduplication, enrichment, Kafka producer and consumers
│   ├── jobs/                     # [Go] River setup, job kinds, periodic schedules, per-tenant fairness
│   ├── webhooks/                 # [Go] Outbound webhook delivery and signing; inbound webhook storage and retries
│   ├── documents/                # [Go] Typst rendering, document templates, e-invoice XML
│   ├── mail/                     # [Go] Email templates and the Mailer port's SMTP implementation
│   └── auth/                     # [Go] API keys, sessions, OIDC, password hashing, permissions
│
├── providers/
│   ├── stripe/ adyen/ gocardless/ cashfree/ flutterwave/ moneyhash/   # [Go] Payment providers
│   ├── anrok/ avalara/                                                 # [Go] Tax providers
│   └── netsuite/ xero/ salesforce/ hubspot/                            # [Go] Accounting and CRM integrations
│
├── ee/                           # Enterprise services (ELv2), separate processes (§14)
│
├── tools/
│   ├── nx-go/  nx-cargo/         # Local Nx plugins
│   └── testport/                 # Extracts lago-api rspec cases into testdata
│
└── docs/
```

Directories are created when their step of the [roadmap](roadmap.md) starts.

### 3.2 Dependency Direction

```text
apps/server ──▶ billing ──▶ pricing / metering / tax / period / money / expression(go)
     │             │
     │             └──▶ core ──▶ money / period
     ├──▶ ingest ──▶ expression(go) / metering / core
     ├──▶ store / eventstore / jobs / webhooks / documents / mail / auth ──▶ core
     ├──▶ providers/* ──▶ core
     └──▶ proto

ee/* ──▶ proto        (never any other in-repo module)
```

* `money`, `period`, `expression`, `pricing`, `metering` and `tax` are pure libraries: they depend only on each other in the order shown and on the standard library or vetted third-party packages, never on `core`, storage or I/O (except `tax`'s VIES client behind an interface).
* `core` depends only on `money` and `period`.
* `billing` reaches storage, events, documents and providers only through ports in `core`; it evaluates metric expressions with `expression` (a pure library).
* Infrastructure libraries depend on `core` but not on each other, with one exception: `ingest` uses `expression` (a pure library, like `metering`, whose event type it writes). Integration tests may use another library's backends; the depguard rules exempt test files.
* Only `apps/server` wires implementations to ports.
* The rules are enforced by depguard in `.golangci.yml`; changing them starts with this section.

---

## 4. Contracts and API (libs/proto, apps/server)

### 4.1 Contracts
* Sources live in `libs/proto/tanvo/<domain>/v1/` with `edition = "2023"`; packages are `tanvo.<domain>.v1`.
* Go code uses the Opaque API; Connect handlers use simple signatures.
* Every field that must be present is marked `(buf.validate.field).required = true`; protovalidate runs in an interceptor.
* Read-only RPCs carry `idempotency_level = NO_SIDE_EFFECTS`.
* Money is a message `{ string currency; string amount; }` with the amount as a decimal string; quantities are decimal strings; timestamps are `google.protobuf.Timestamp`; dates are `google.type.Date`.

### 4.2 REST through Vanguard
* Every public RPC has a `google.api.http` annotation following Google AIP resource conventions: `GET /v1/customers/{customer_id}`, `POST /v1/customers`, `PATCH /v1/customers/{customer.id}`, custom methods for lifecycle actions (`POST /v1/subscriptions/{id}:terminate`, `POST /v1/invoices/{id}:void`).
* Vanguard serves REST/JSON, Connect, gRPC and gRPC-Web from one `http.Handler`.
* JSON uses the proto field names (**snake_case**) on every protocol: the Connect JSON codec and Vanguard's codec are configured with `UseProtoNames`. The REST `update_mask` query parameter takes the proto field names too (`?update_mask=short_name,email_settings.invoice_finalized`): protojson accepts only lowerCamelCase paths in a `google.protobuf.FieldMask`, so a middleware in front of Vanguard (`rpc.RESTFieldMasks`) rewrites each snake_case path segment to lowerCamelCase; lowerCamelCase paths are accepted as they are.
* Numbers in event properties stay exact over JSON. `Event.properties` is a `google.protobuf.Struct`, whose numbers are doubles, so both JSON codecs (`apps/server/internal/rpc`) rewrite each number in the properties of a request's events as a string of its exact decimal text in plain notation (`1e3` → `"1000"`) before protojson decodes it; that is the text the intake stores for a number. Binary protobuf and gRPC clients send doubles, and send strings for values a double cannot hold (integers above 2^53, more than 15 significant digits).
* Resources are addressed by Tanvo IDs; where Lago used customer-provided identifiers (`external_id`, `code`), Tanvo keeps them as unique, filterable fields and accepts them in place of an ID through an `{id}` that starts with a prefix (`external_id:acme-42`, `code:api_calls`).
* Lists use cursor pagination (`page_size`, `page_token`, `next_page_token`) and AIP-160 style filters.
* Errors are Connect error codes with `google.rpc.ErrorInfo` / `BadRequest` details; REST maps them to HTTP status codes with a JSON body.
* Writes accept an `Idempotency-Key` header ([§4.5](#45-idempotency)).

### 4.3 Non-RPC HTTP
Provider webhooks (`POST /webhooks/{provider}/{connection_id}`), document downloads (`GET /documents/{id}`), health (`/healthz`, `/readyz`) and metrics (`/metrics`) are plain `net/http` handlers.

### 4.4 Authentication and Authorization
* **Principals**: an API key (organization-scoped, hashed with SHA-256, shown once, with optional expiry, rotation with overlap, last-used tracking) or a user session (organization membership).
* **Permissions**: strings such as `customers:read`, `invoices:write`. Roles (built-in and custom) are sets of permissions; an API key carries its own set. Every RPC declares the permission it needs in a proto option; an interceptor checks it.
* **Users** sign in with email and password (argon2id) or OIDC (Google, Okta, Entra ID, generic). Sessions are opaque tokens stored hashed in PostgreSQL (`sessions`), revocable and purged once expired ([domain §5.7](domain.md#57-session-and-sign-in-state)).
* **OIDC** uses the authorization code flow with state, nonce and PKCE. The flow's state is a single-use row of `oidc_states`, keyed by the SHA-256 of the `state` parameter, so any replica can finish a sign-in. Google is one installation-wide connection configured from the environment; Okta, Entra ID and generic connections are `auth` integrations of an organization ([domain §17.1](domain.md#171-integration)) whose claimed email domains route sign-ins, looked up across organizations.
* **OIDC client**: one standards-based client in libs/auth (go-oidc and x/oauth2) serves every connection; Google, Okta and Entra ID are presets that fix the issuer, scopes and claim rules, and a generic `oidc` connection (a Tanvo extension) takes any OpenID provider by `issuer_url`.
  * *Discovery*: `/.well-known/openid-configuration` must report exactly the configured issuer, offer the `code` response type and an accepted signing algorithm, and use https for the issuer and every endpoint unless `TANVO_OIDC_ALLOW_PRIVATE` (dev mode). Documents are cached per issuer for 1 hour and kept when a refetch fails.
  * *Keys*: the JWKS is cached for 1 hour; a token with an unknown `kid` refetches it at most once a minute, so key rotation needs no restart and forged `kid`s cannot flood the provider.
  * *Network*: apps/server injects the SSRF-guarded client of libs/webhooks (private, loopback and link-local addresses refused unless `TANVO_OIDC_ALLOW_PRIVATE`; redirects never followed); every response is capped at 1 MiB and every request at 10 seconds.
  * *ID tokens*: signatures in the RS, ES and PS families or EdDSA (never `none`, never HMAC); `iss`, `aud`, `azp` when there are several audiences, `exp`, `nbf` and `iat` with 30 seconds of leeway, and the nonce compared in constant time. The token endpoint auth method is the connection's, else the first of `client_secret_basic` and `client_secret_post` in discovery.
  * *Identity*: the email and name come from configurable claims (default `email`, `name`); generic connections require `email_verified` to be true unless `allow_unverified_email`, while the presets keep their behaviour (refused only when false). Userinfo is fetched when the email or a required `email_verified` is missing; its `sub` must match.
  * *Configuration*: creating or updating a generic connection fetches discovery once and fails with a field error on `issuer_url` (`https_required`, `issuer_unreachable`, `issuer_mismatch`, `discovery_incomplete`) or `token_endpoint_auth_method` (`unsupported_by_provider`). Extra authorization parameters may not override protocol parameters (`state`, `nonce`, `redirect_uri`, …).
* **Devices**: every sign-in records the client in `user_devices` (fingerprint = SHA-256 of the user agent); apps/server parses the user agent into browser, OS and device type. A new device is written to the security log ([domain §5.8](domain.md#58-user-device)).
* **Emails** from libs/auth are `mail.send` jobs carrying data, not links: a password reset carries its token, and libs/mail builds the URL from the application URL.
* **Customer portal**: short-lived signed tokens scoped to one customer (HMAC-SHA256, 12 hours, key derived from `TANVO_SECRET_KEY`), sent as the bearer credential of `portal` RPCs; a token is accepted only while its customer exists and is not deleted.
* **Credentials on the wire**: every credential is `Authorization: Bearer <token>`. An API key acts in its organization. A session acts in the organization named by the `X-Tanvo-Organization` header, where the user must have an active membership and the organization must allow the session's sign-in method; without the header it can call only the RPCs marked `authenticated` (current user, sign-out, server information, data export download) and fails elsewhere with `PermissionDenied` / `organization_required`.
* **Access rules**: the `tanvo.common.v1.access` option of each RPC is one of `anonymous`, `portal`, `authenticated` or a `permission` (with an `api_resource` when API keys may call it). An API key needs `read` on the resource for `NO_SIDE_EFFECTS` RPCs and `write` otherwise; a refusal is `PermissionDenied` with the reason `<level>_action_not_allowed_for_<resource>`.
* **Session purge**: the hourly clock job `clock.purge_sessions` deletes the sessions expired or revoked more than 24 hours earlier.

### 4.5 Idempotency
* A unary RPC with side effects called with an `Idempotency-Key` header (at most 255 bytes) by an API key or a session acting in an organization is run once per organization and key. The interceptor, after authentication and validation, claims the key in `idempotency_keys` with the procedure and the SHA-256 of the deterministic request encoding; the successful response is stored and replayed to every retry for 24 hours after the first request.
* A retry with another RPC or request fails with `InvalidArgument` / `idempotency_key_reused`; one sent while the first is running fails with `Aborted` / `idempotency_key_in_progress` (a claim expires after 5 minutes, so a crashed request frees its key). A failed request releases the key, so the client can retry it.
* `NO_SIDE_EFFECTS`, streaming and anonymous RPCs ignore the header. The hourly clock job `clock.purge_idempotency_keys` enqueues a per-organization `idempotency.purge` job that deletes the expired records.

### 4.6 Server Layering
* **Domain services** live in the libraries: `libs/billing/<area>` (catalog, customers, subscriptions, invoices, …) and `libs/auth`. A service takes `kernel.Deps` (or `auth.Deps`), runs each operation in one `Store.InTx` with its jobs and webhooks, validates with `core.Validator` (field errors carry Lago-style codes such as `value_already_exist`), and returns domain types. It knows nothing of proto or HTTP and is tested on `memstore`.
* **RPC handlers** (`apps/server/internal/rpc`) are thin: they read the principal, convert the request with the shared helpers (references, filters, pagination, update masks, money, times, enums), call one service method, convert the result and map errors with `rpc.Error`. They hold no business rules.
* **Wiring** (`apps/server/internal/app/api.go`) builds the services over the store and registers every handler behind the shared interceptors: telemetry, panic recovery, authentication and authorization, protovalidate, idempotency.
* **Tests**: services port Lago's service specs; handlers port its request specs against a real server (`apps/server/internal/rpctest`), over REST and Connect. The step-by-step playbook is in [development §8](development.md#8-rpc-playbook) and the `rpc` package comment.

---

## 5. Storage (libs/store)

### 5.1 PostgreSQL Only
Billing relies on exact `numeric` arithmetic, transactions spanning many tables, advisory locks and partitioning; PostgreSQL is the only supported database for business data.

### 5.2 Schema and Migrations
* The schema is declared in SQL under `libs/store/schema/` (one file per domain) and is the single source of truth.
* Atlas computes versioned migrations from it into `libs/store/migrations/`; merged migrations are never modified.
* The schema also declares the read-only export views (`exports_*`, [Feature Parity §1.43](parity.md#143-data-exports)), the contract of the warehouse sync. They are `security_invoker`, so that the reader's row-level security applies. The open-source Atlas planner ignores views, row-level security and some constraint changes, so `migrate-diff` (`libs/store/internal/cmd/migrate-diff`) diffs them itself: a changed view, or one reading a table whose planned change it would block, is dropped before the table changes and created after them.
* Migrations run with `tanvo migrate` (a Kubernetes Job in Tanvo Cloud) and, for self-hosted installations, automatically at startup unless disabled.
* The same database holds two migration histories, so that jobs can be inserted in the business transaction: Atlas owns the `public` schema, and River's own migrations (rivermigrate) own the `river` schema. River's tables stay out of Atlas so that Atlas's diff, lint and drift checks see only Tanvo's schema and a River upgrade brings its migrations without copying them by hand. `tanvo migrate` is the single entry point: it applies Atlas's migrations, then River's, each under its own advisory lock, and both are idempotent.
* The PostgreSQL event store (§7) has a third history in its own schema (`eventstore` by default): its migrations are idempotent DDL applied by the same command, which also creates the partitions of the coming months; the ClickHouse schema is applied by `tanvo migrate` too when ClickHouse is configured.
* Migrations are expand-only within a release, so that the previous release keeps running during a rolling deploy; they are applied before the new release rolls out. At startup a process checks that the schemas are applied and refuses to start otherwise: every River migration, and every Atlas migration of the release with its checksum (`store.CheckSchema`; versions of a newer release are accepted, as during a rolling deploy), are applied, and the event store exists.

### 5.3 Queries
* sqlc generates type-safe Go from SQL in `libs/store/queries/` against the pgx/v5 driver.
* Optional list filters use `sqlc.narg` parameters; full-text search uses PostgreSQL `tsvector` columns.
* `Store.InTx` runs a function in one transaction and gives it repositories and the River client bound to the same `pgx.Tx`.
* sqlc and pgx types never leave `libs/store`; upward it exposes the repository ports of `libs/core`.

### 5.4 Tenancy
* Every business table has `organization_id` as the first column of its primary key or of a unique index, and every query filters by it.
* The exceptions are the x402 settlement's payment digest and transaction hash, which are unique across all organizations so that two organizations sharing a payout address are never both credited for one transfer ([Domain §24](domain.md#24-design-decisions), item 14), and the invoice's x402 payment token, a column with a unique index that nothing sets or reads yet, as in v1.55.0 ([Domain §11.1](domain.md#111-fields)). Reads still filter by organization.
* Tanvo Cloud additionally enables row-level security policies bound to `SET LOCAL tanvo.organization_id`, as defense in depth.

### 5.5 Conventions
* IDs are UUIDv7; customer-facing identifiers (`external_id`, `code`) are unique per organization.
* Amounts in minor units are `bigint`; precise amounts, unit prices and quantities are `numeric`.
* Enumerations are `text` with `CHECK` constraints, mirrored by Go string types.
* Deletion is explicit: resources with a lifecycle have a status (`terminated`, `archived`, `voided`); hard deletes are used where nothing references the row. There is no blanket soft delete.
* Changes that clients are notified of are recorded as domain events in the `domain_events` table in the same transaction, which feeds webhooks; activity logs are written to `activity_logs` by the activity producer (`auditlog.ActivityLogger`, through `kernel.Deps.ActivityLogs`), also in the same transaction, and moved to ClickHouse when the installation runs it (§7.4).

---

## 6. Money and Numbers (libs/money)

* `money.Decimal` is an immutable decimal on `apd.Decimal`, with a fixed operation context (precision 40, explicit rounding modes: half-up, half-even, up, down, ceiling, floor).
* `money.Currency` covers ISO 4217 (code, minor unit exponent) plus custom pricing units (§ Pricing units in [Domain](domain.md)).
* `money.Amount` pairs a currency with a decimal; conversion to minor units rounds explicitly.
* Allocation (splitting an amount across lines without losing a cent) and proportional distribution (coupon and credit distribution across fees) live here.
* Every rounding in the billing engine names its mode and its step; nothing rounds implicitly.

---

## 7. Events and Metering

### 7.1 Ingestion Paths
```text
POST /v1/events, /v1/events:batch
        │ validate, normalize, deduplicate (organization_id, external_subscription_id, transaction_id)
        ├── mode "postgres": insert raw + enriched rows in one transaction
        └── mode "kafka":    produce to events.raw
                               └─ ingest consumers: enrich → events.enriched ─┬→ ClickHouse writer (batched)
                                                                              ├→ in-advance charges → billing
                                                                              └→ usage refresh signals
```
* The mode is a per-installation setting with per-organization overrides in Tanvo Cloud.
* Each event keeps the metadata of its API request, the `User-Agent` and the client address: in the raw log (`public.events.metadata`) on the PostgreSQL path, and in ClickHouse's `events_raw` on the Kafka path, which carries it in the raw and enriched messages. lago-api keeps it only in PostgreSQL (its Kafka payload leaves it out); the API returns it as the event's `user_agent` and `ip_address`. The client address is resolved like Rails' `request.remote_ip`: the nearest `X-Forwarded-For` (then `Client-Ip`) address that is not a trusted proxy, the headers being read only when the peer is one; trusted are the loopback and private ranges and `TANVO_TRUSTED_PROXIES`.
* Connectors for HTTP, SQS and Kinesis sources produce to `events.raw`: Redpanda Connect configurations in `libs/ingest/connectors/`, keying their records as the API does. The intake consumer evaluates the metric's expression of their events, which the API evaluates at intake.
* A message the consumers cannot process goes to `events.dead_letter` with the reason (`ingest.DeadLetter`); a consumer of that topic (group `<prefix>-dead-letters`) stores each one in ClickHouse's `events_dead_letter`, with the event's keys, for inspection and replay. A failed store is retried until it succeeds, so no dead letter is itself dead-lettered.
* **Usage refresh signals** carry the customer, the subscriptions and the event watermark. Signals are sent after the events are stored, and the usage buckets are maintained in the same write, so a signal never runs ahead of the buckets (on replicated ClickHouse the refresh reads with sequential consistency). Each signal goes to billing, in the ingestion transaction on the PostgreSQL path and through the `usage.refresh` consumer on the Kafka path: it schedules the activity of each subscription (lifetime usage, usage thresholds and alerts, one delayed job per subscription and minute, so a signal that arrives while a run is in progress lands in the next minute's job; a failed run is retried at once, four attempts in all, like lago-api's `ProcessSubscriptionActivityJob`), flags the customer `awaiting_wallet_refresh` and enqueues the refresh of its wallets (one pending job per customer). The wallet refresh does not wait for the buckets. lago-api's realtime wallet refresh (`app/services/wallets/realtime_refresh_service.rb:9-13, 31-34, 70-94`) polls its buckets for up to 5 seconds before refreshing, because its trigger and its bucket upsert are two unordered sinks of the same stream epoch, and a refresh on buckets that have not caught up would write a stale balance and clear the flag the sweep selects on. It gives up at once on a watermark more than 30 seconds old, since that means the consumer is behind rather than ClickHouse, and leaves the customer flagged for the sweep. Only the wallet refresh does this; the subscription activity has no such check. Tanvo sends the signal after the bucket write, so the buckets of a signal are always caught up, the refresh never reads a stale balance, and no signal needs to be deferred to the sweep. The 5-minute sweeps of the flagged lifetime usages and customers stay authoritative ([Domain §14.1](domain.md#141-wallet)).
* **In-advance charges** are billed after the event is stored, since their fee aggregates over the stored events: in the ingestion transaction on the PostgreSQL path, and by the ClickHouse writer for the records it stored on the Kafka path. Either enqueues one billing job per event, unique on its subscription and transaction ID. lago-api bills them from a separate topic, 15 seconds later to let ClickHouse merge (`app/consumers/events_charged_in_advance_consumer.rb`, `app/services/events/stores/clickhouse_store.rb:11`).

### 7.2 Enrichment
Enrichment resolves an event to its subscription(s) by `external_subscription_id` / `external_customer_id` and timestamp and to the billable metric by `code`, evaluates the metric's expression, extracts the aggregated value, the grouping keys and the precise amount, and writes **one enriched row per event**, with no charge or filter column. It looks up the metric's charges only to decide whether the event goes to in-advance billing. Charge filters are matched from the raw properties when usage is queried and when usage buckets are maintained, not by expanding the event into one row per filter: lago-api dropped its expanded table (`events_enriched_expanded`) in September 2026 for this reason, since a catalog change would otherwise require rewriting stored events and the row count grew with the number of filters. The API and ingest consumers keep an in-memory cache of the catalog (metrics, charges, subscriptions). Every store transaction that changes it sends `pg_notify('tanvo_catalog_changes', …)` before it commits, so the notification is delivered on commit and never for a rolled-back write; each process LISTENs and evicts the organization's entries, purges the whole cache whenever it (re)connects, and the writing process evicts at once after commit. A TTL bounds staleness should a notification still be missed. A renamed subscription evicts its old external ID too. LISTEN holds one connection per process in session state, so `TANVO_DATABASE_URL` must reach PostgreSQL directly or through a pooler in session mode; PgBouncer in transaction mode does not deliver notifications to a listener. ClickHouse and Kafka modes read the catalog from PostgreSQL as well, so the same channel covers them.

Since the enriched row holds no charge or filter, a catalog change rewrites no stored event. Events ingested before a metric existed, or enriched under another definition of it, are enriched again by `tanvo events reprocess`: it lists the active subscriptions of an organization whose plan gained a charge or a charge filter after they started and, with `--reprocess`, enqueues a re-enrichment job per subscription. The job enriches the subscription's stored events again, in batches, and stores them back through `eventstore.Replacer` on the organization's event store (PostgreSQL or ClickHouse); it neither bills in advance nor signals a usage refresh.

### 7.3 Expressions (libs/expression)
* The language is lago-expression's: decimal arithmetic, `event.timestamp`, `event.code`, `event.properties.<name>`, strings, `concat`, `round`, `ceil`, `floor`, `least`, `greatest`.
* It is implemented once in Rust and compiled to `wasm32-unknown-unknown`. The Go package `libs/expression/go` runs the module on wazero with a batch interface (many events per call), and the web UI loads a wasm-bindgen build of the same crate (`libs/expression/js`: `parseExpression`, `evaluateExpression`, as in lago-expression's `expression-js`) for live previews.

### 7.4 Event Stores (libs/eventstore)
* The `EventStore` port offers the aggregation queries the metering library needs: counts, sums, max, latest, unique counts with add/remove semantics, weighted sums over time, grouped variants, prorated variants and breakdowns, all bounded by a billing period and filters.
* **PostgreSQL**: enriched events in a table partitioned by month and hash of organization.
* **ClickHouse**: `events_raw` (the ledger, deduplicated by (organization, external subscription, transaction ID)) and `events_enriched` as `ReplacingMergeTree` tables, written in batches by the ingest consumers (`events_enriched` also keeps the value enrichment gave each event, as a decimal, and when it was written, which the enriched events API returns, and the attribution labels of the event, which the attributed usage query groups by, parity §1.42). Aggregated values are read as `Decimal(76, 26)`, so ingestion rejects a numeric property with more than 50 integer digits rather than let ClickHouse read it as zero. `events.raw` is keyed by the deduplication key and `events.enriched` by (organization, external subscription), so every copy of a key is ordered on one partition and one writer consumer at a time stores a subscription's events and maintains its buckets: the writer sees the first copy of a key before the later ones and no resolution between writers is needed. This caps one subscription's throughput at one writer consumer, and an organization's events without a subscription share one key. Lago produces raw events without a key (fec71c637); the keys are a deliberate difference.
* **Audit logs** (parity §1.45): without ClickHouse, activity, API and security logs are stored in PostgreSQL (`23_audit_log.sql`). With ClickHouse (`TANVO_CLICKHOUSE_URL`), those tables hold each log from the transaction of its change until a worker moves it (`store.ArchiveLogs`: up to 1,000 logs of each kind per transaction, claimed with `FOR UPDATE SKIP LOCKED`, written to ClickHouse, then deleted) to the `security_logs`, `api_logs` and `activity_logs` tables of `clickhouse.LogStore`. These have lago's columns (maps hold each top-level key with its value in JSON) and are `ReplacingMergeTree` tables sorted by (organization, logged_at, ID), so a move retried after its insert stores no duplicate; they have no TTL, as retention is applied by the queries, as in lago. The log repositories (`auditlog.Archive`, `store.WithLogArchive`) create logs in PostgreSQL and read both stores, merging pages by (logged_at, ID) with one page token format, so a log is readable at once and while it moves. Lago produces the logs to Kafka after commit, for ClickHouse's Kafka engine tables; Tanvo writes them in the transaction and needs no Kafka for them.
* **Usage buckets** of **15 minutes** (UTC) back current-usage queries in both stores. A bucket definition names a metric code, the property that is aggregated and the dimensions (the filter, pricing-group and presentation keys of its charges, and always `target_wallet_code`); a bucket row is keyed by (organization, external subscription ID, code, definition, bucket, dimension values), and the values of filter keys outside the metric's declared values fold into one "other" value, which bounds the rows and keeps charge filters exact, as they only take declared values. Rows store `units`, `events_count`, `precise_total_amount`, the last event and ingestion instants. Charge filters are matched on the dimensions at read time, so changing a charge never makes the buckets stale.
* Buckets serve the aggregations that can be combined across buckets (count, sum, max, latest) and the charge models that only need their totals (standard, graduated, package, volume, graduated percentage, dynamic); in-advance, prorated and recurring charges, charges that accept a target wallet and the other aggregations read the events. A query reads the whole buckets inside its window and the partial buckets at its two edges from the events, so its result equals the event path exactly (lago-api widens the window to whole buckets, which is safe for it only because its buckets carry the internal subscription; Tanvo's external subscription IDs are shared across upgrades).
* Buckets are maintained in the write that stores the events, so there is no lag to wait for. On PostgreSQL every write updates the touched buckets in its transaction under advisory locks; replacing an event rebuilds its buckets; a new definition is backfilled in chunks while writers already maintain it, and serves reads once ready. On ClickHouse each write merges its events into the current rows of the touched buckets one version up (replacements recompute them from `events_enriched FINAL`), checks after its insert for conflicting writes, and recomputes a conflicting bucket; reads keep the highest version. On replicated tables the maintenance inserts with `insert_quorum` and reads with `select_sequential_consistency`. A periodic `RepairBuckets` sweep, required for correctness, recomputes the buckets of recently stored events: it repairs a write that failed between storing events and updating buckets, and conflicts a background merge hid from the checks. It runs every 10 minutes (`clock.repair_usage_buckets`) with its watermark, the writers' clock, a margin (10 minutes) before the start of the previous sweep; each completed sweep records its start in ClickHouse (`bucket_repairs`, kept 30 days), so the watermark survives restarts and leader changes, and the first sweep repairs every bucket. A write's cost is proportional to its batch and the rows of its buckets, not to the events already in them.
* `BucketStats` reports rows per definition; the hourly `clock.report_usage_bucket_stats` job records them in histograms (by store, never by organization) and logs a warning for a definition with more than 10 000 rows in one bucket of one subscription. Dropping an oversized definition, which sends its reads back to the events instead of rejecting writes, is to be automated once billing manages the definitions.

### 7.5 Metering (libs/metering)
`metering` implements the aggregation semantics (including proration, recurring metrics carried over periods, rounding) over an `iter.Seq` of events or over aggregates returned by an `EventStore`, so that the same rules are tested in memory with ported lago-api cases and run in the database in production.

---

## 8. Billing Engine (libs/billing)

* **Periods** (`libs/period`): calendar and anniversary billing, weekly to yearly intervals, trial periods, time zones of the organization, billing entity or customer, proration ratios, in-advance and in-arrears boundaries.
* **Pricing** (`libs/pricing`): every charge model computes an amount and its details from an aggregation result; fixed charges and add-ons use the same machinery.
* **Fees → invoices**: the engine builds fees for subscriptions (base fee, fixed charges, usage charges, commitments' true-ups), applies taxes (internal or provider), coupons, prepaid credits, credit notes and wallet credits in a defined order, and produces invoices with exact totals.
* **Scheduling**: each subscription's next billing instant is computed and a River job is scheduled for it; an hourly reconciliation job catches anything missed. This replaces Lago's hourly scans of every subscription.
* **Lifecycle**: draft invoices with a grace period, finalization, numbering, voiding, regeneration, progressive billing, usage thresholds and one-off invoices; see [Domain](domain.md).
* **Current-usage cache**: the fees of each charge bucket (a charge, or one of its filters, for one subscription) computed for a current usage are kept in an in-process LRU (`kernel.MemoryUsageCache`, `TANVO_USAGE_CACHE_ENTRIES` entries) until the end of the charges period. Full usage has its own entries. An entry is validated lazily when it is read: it records the ingestion watermark of the events it aggregated (`metering.Watermark`: the latest `ingested_at`, or the latest write of a replaced or re-enriched copy, `written_at` in PostgreSQL and `enriched_at` in ClickHouse), and it is recomputed once a later event is stored. An aggregation whose latest event is less than one second old is not kept, since a sibling written at the same instant may not have been visible to it. The key holds the charge's `updated_at` and a fingerprint of the filter, so catalog changes miss the old entries. Reads answered from the usage buckets, reads capped by a timestamp, previews and the daily usage history are never cached. The cache is per process, as caches are by default (§2), and needs no Redis: this is safe because validation needs no invalidation message, only the store's watermark, so a process never serves an entry older than an event it can read. The cost is one cold cache per API replica.
* All writes of one billing run happen in a transaction with the jobs they trigger (documents, webhooks, payments).

---

## 9. Jobs and Scheduling (libs/jobs)

* **River** runs every background job; jobs are enqueued in the same transaction as the state change that requires them.
* `libs/store` inserts jobs through River's `InsertTx` on its own `pgx.Tx` (`store.WithJobInserter`), so a rollback leaves no job; without an inserter they go to the `job_outbox` table for a relay. River's tables live in their own `river` schema, migrated by River (they hold every organization's jobs and carry no row-level security). A unique key is scoped by organization and kind.
* **Retries**: an error that no rule of its kind classifies is not retried, since a job may have had effects outside its transaction (a payment, an email, a provider call) that a second run would repeat. Each kind declares the errors safe to retry, with their attempts and backoff (`ErrRetriable` 20 times, lock and sequence conflicts, provider rate limits). A job that stops retrying with an error is left `cancelled` in River, kept for inspection and manual retry, and counts as a failure in monitoring.
* Queues separate workloads (`billing`, `payments`, `webhooks`, `webhooks_high_priority`, `documents`, `integrations`, `exports`, `alerts`, `alerts_high_priority`, `wallets`, `streaming`, `default`), each with its own concurrency, so that a deployment can run workers per queue; `TANVO_JOBS_QUEUE_CONCURRENCY` overrides the concurrency of any of them. The subscription activity and lifetime usage alerts run on `alerts`, the wallet alerts on `alerts_high_priority`, the fan-out and first attempt of `alert.triggered` webhooks on `webhooks_high_priority` (`app/jobs/send_webhook_job.rb`; its retries on `webhooks`), the wallet balance refresh on `wallets` and the streaming destination deliveries on `streaming`, as in lago-api (`app/jobs/usage_monitoring/process_subscription_activity_job.rb`, `process_lifetime_usage_alert_job.rb`, `process_wallet_alerts_job.rb`, `app/jobs/customers/refresh_wallet_job.rb`, `app/jobs/deliver_event_job.rb`), so that a backlog of usage refreshes does not delay billing. lago-api uses these queues only when `SIDEKIQ_ALERTS` or `SIDEKIQ_WALLETS` is set and otherwise falls back to `default` or `low_priority`; Tanvo always uses them, since a deployment that does not size them separately still works them with the defaults.
* Periodic jobs (reconciliation, expirations, dunning, cleanups) are River periodic jobs inserted by its elected leader; no separate clock process is needed. They run on their own `clock` queue, unsharded and without token buckets, worked by every process that carries the schedule. A periodic job only scans and dispatches: it enqueues the due work as jobs of the organizations concerned on the workload queues, with the tick's time as their "now" and a unique key so that ticks over a backlog add no duplicates. A backlog on a workload queue therefore does not delay the schedule, and a failure in one organization does not stop the others. Retention cleanups of tables under row-level security dispatch one job per organization as well (webhook deliveries); the others run their batched deletes in the periodic job itself. Besides lago-api's schedule, Tanvo has its own periodic jobs: the webhook sweep and the usage bucket repair and report (§7.4, §11).
* **Tenant fairness**: jobs carry the organization; per-organization concurrency is bounded by sharding queues by organization hash and by token buckets at enqueue time, so that one tenant cannot starve others. The token buckets are per process, so with several API replicas an organization's effective rate is the sum of theirs.
* Per-event work (enrichment, the ClickHouse write) runs in the Kafka consumers, not in River; the consumers enqueue the per-event billing (in-advance charges) and the per-subscription activity as River jobs.
* A Redis-backed queue is a later option behind the same narrow interface (transactional enqueue through an outbox, delays, periodic and unique jobs); it is added only if measurements show PostgreSQL as the bottleneck.

---

## 10. Documents (libs/documents)

* Invoices, credit notes, payment receipts and payment requests are rendered by **Typst** run as a child process with the document's data as JSON input; templates and fonts are embedded in the binary and written to a private directory at startup.
* Output is PDF/A-3b; e-invoices embed their XML (Factur-X / ZUGFeRD CII) as an attachment. UBL 2.1 and CII XML are also served on their own.
* Fonts: the binary embeds Inter (SIL OFL) for the Latin-script locales. The templates fall back to Noto Sans CJK SC / Noto Sans SC for zh-CN and Noto Sans CJK TC / Noto Sans TC for zh-TW, which are not embedded: the renderer searches the system font directories (or `Options.FontPaths`), and the container image installs `fonts-noto-cjk`. A zh-CN or zh-TW document rendered without one of these fonts fails with `documents.ErrMissingFont` rather than producing missing glyphs. Text in other scripts that no font covers (customer data) still renders, as a plain PDF with a warning.
* Documents are stored through the `BlobStore` port (`libs/documents/blob`): the local disk, any S3-compatible service (AWS S3, MinIO, R2) or Google Cloud Storage through its S3 interoperability API (`TANVO_BLOB_BACKEND`). Every stored file has a permanent link, `TANVO_API_URL/files/<token>` signed with a key derived from `TANVO_SECRET_KEY`, that redirects to a short-lived download URL (`TANVO_BLOB_URL_TTL`): a presigned URL of the bucket, or `TANVO_API_URL/blobs/…` for the local disk. The links are what the API, the emails and the integrations hand out (invoice `file_url`, HubSpot's invoice file, the payment request email, Flutterwave's logo). Without a backend no document is rendered: the documents jobs succeed without files and the emails go without attachments or links.
* `libs/billing/docgen` drives generation. The `documents` queue runs `documents.generate_invoice`, `documents.generate_credit_note` and `documents.generate_payment_receipt`: each renders the UBL XML when the billing entity issues e-invoices and it is missing, then the PDF unless `TANVO_DISABLE_PDF_GENERATION` (with the CII XML embedded for e-invoicing), stores both, emits `invoice.generated`, `credit_note.generated` or `payment_receipt.generated` with its activity log, and, when asked, enqueues the document's email. Existing files are kept unless the job is forced (the admin regeneration), which replaces them and deletes the old ones. A failure of the renderer or the blob store is retried 6 times; a draft or a missing document fails for good.
* `mail.send_document` (on `default`) renders a missing PDF, composes the invoice, credit note or payment receipt email with the PDF attached (unless PDFs are disabled) and its link, honours the billing entity's email settings and the resend's recipients, sends it and records `email.sent` (sent, resent or failed, with the email and the document). A receipt whose files are not ready is retried 8 times, an SMTP 4xx reply 25 times, a timeout or broken connection 6 times; the failure is logged only after the last attempt. The payment request email links each invoice's PDF, rendering it first. Completed data exports are stored in the blob store and emailed to their requester.
* Rendering runs in the `documents` queue; in Tanvo Cloud a dedicated Deployment runs that queue. Typst (`TANVO_TYPST_PATH`) is required whenever a blob backend is configured.

---

## 11. Webhooks (libs/webhooks)

* **Outbound**: each organization has webhook endpoints; every domain event that is subscribed is delivered as a River job with retries and exponential backoff, signed per the Standard Webhooks specification, with delivery attempts recorded and replayable.
* Deliveries are kept 90 days: the daily clean-up (`clock.clean_webhooks`) enqueues one purge job per organization with endpoints, which deletes its deliveries last updated before the cutoff, 1 000 per transaction, under the organization's row-level security. A sweep every 5 minutes (`clock.sweep_webhooks`) enqueues the due deliveries whose job was lost.
* **Inbound**: provider webhooks are verified, stored and processed by jobs; failures are retried and kept for inspection.
* Private network targets are refused unless explicitly allowed (SSRF protection).
* **Streaming destinations** (parity §1.40): an operator-configured destination (`streaming_destinations`, written in SQL; there is no API, as in lago-api) claims event types, one destination per type and organization, and may exclude plans from the full-usage event. When a customer's wallet balances are refreshed, `libs/billing/streaming` produces `customer_usage.refreshed.v1` (inline after commit, from the usage the refresh computed) and `customer_full_usage.refreshed.v1` (a `streaming.deliver_event` job on the `streaming` queue, one attempt). A delivery lock per organization, customer and type (`streaming_delivery_locks`, held 10 minutes) drops a refresh while a delivery waits and logs it `superseded`. Records go through the `port.StreamProducer` port, implemented by `libs/webhooks/kinesis`: a Kinesis `PutRecord` client signed with SigV4, assuming each destination's role through STS from the workers' ambient AWS identity (environment, web identity, container or instance credentials), through `TANVO_STREAMING_INTERMEDIATE_ROLE_ARN` when set, with the destination's partition key. Every delivery is logged with `log/slog` with an outcome: `delivered`, `skipped`, `failed`, `superseded`, `throttled` or `dropped`.

---

## 12. Providers (providers/*)

Each provider is a Go module implementing ports of `libs/core`:

| Port | Providers |
| :--- | :--- |
| `PaymentProvider` (customers, payment methods, checkout, charges, refunds, webhooks) | Stripe, Adyen, GoCardless, Cashfree, Flutterwave, Moneyhash |
| `integration.TaxGateway` (quotes, reports, voids, negations, refunds) | Anrok, Avalara |
| `integration.AccountingGateway` / `CRMGateway` (contacts, items, invoices, credit notes, payments, CRM records) | NetSuite, Xero, HubSpot; Salesforce (connection only) |
| `IdentityProvider` (OIDC) | Google, Okta, Entra ID, generic OIDC |
| `x402.Facilitators` (verify, settle, supported) and `x402.ChainReaders` (EVM and Solana JSON-RPC) | Coinbase CDP (`providers/x402`) |

Integrations call the vendors' APIs directly; Tanvo does not depend on an integration broker.

### 12.1 Payment providers

`port.PaymentProvider` is `payment.Gateway`, composed of six capabilities: `ConnectionGateway` (validation, webhook endpoint registration and refresh), `CustomerGateway` (create, update, remove), `MethodGateway` (list, default, details, set default, detach, setup URL), `ChargeGateway` (payment, authorization, status, cancel, payment URL and its expiry, reference update), `RefundGateway` and `WebhookGateway` (signature verification and parsing into typed `payment.Event`s); `FundingGateway` is optional and found by type assertion. apps/server builds a `port.PaymentProviderSet` keyed by vendor.

* Every call takes the `payment.Connection` (organization, integration, settings and decrypted secrets); adapters hold no per-connection state.
* Idempotency keys come from the domain: a payment's from its payable and payment ID (`PaymentParams.IdempotencyKey`), a refund's is the credit note ID (mandatory), a customer's is its ID and update time.
* Errors are `*payment.ProviderError` with a `payment.ErrorKind` and a class, `payment.ErrRejected` (final: card declined, invalid request, not found) or `payment.ErrRetryable` (rate limits, outages, conflicts; the caller's job retries with the same key); `payment.ErrAuthentication` marks a connection whose credentials fail. Errors of the adapter itself (missing country, no payment method) pass through unclassified.
* Adapters keep provider objects out of the domain: results carry the provider's status, the mapped `payment.State` and, for `requires_action`, the next-action URL. Webhook events carry `payment.Refs` read from the metadata (`tanvo_*` keys); resolving them against records is apps/server's.
* Each adapter rate-limits per connection, and instruments its HTTP client with the injected OTel providers (spans carry the organization and integration IDs; metrics do not).

### 12.2 Tax, accounting and CRM integrations

`port.IntegrationProviders` is a `port.IntegrationProviderSet` keyed by vendor. Every adapter is an `integration.Gateway` (vendor, connection validation) and implements the gateway of its category: `TaxGateway` (Anrok, Avalara: quotes, reports, voids, negations and refunds of tax transactions, Avalara's company lookup), `AccountingGateway` (NetSuite, Xero: contacts, items and accounts, invoices, credit notes and payments; NetSuite's subsidiaries through `SubsidiaryLister`) or `CRMGateway` (HubSpot: the custom objects and properties, companies or contacts, invoice and subscription records and their associations). Salesforce's adapter only validates the connection: the Salesforce package reads Tanvo's API. `OAuthGateway` connects Xero, HubSpot and GoCardless's payment connections by OAuth 2.0 with the installation's applications (§13.5).

* `libs/billing/integrations` orchestrates them. Credentials are sealed with the installation's secret box (`auth.SecretBox`) and never returned, except the webhook secrets Tanvo generates for GoCardless and Flutterwave. `StartOAuth` keeps a random state and the redirect URI with the connection's secrets until `CompleteOAuth` exchanges the code; the adapters store refreshed grants at once through the service (`integration.ConnectionStore`).
* A connection queues its follow-up jobs in its transaction: items fetched for accounting, objects deployed and the account saved for HubSpot, the webhook endpoint registered for payment providers.
* The syncs are queued with the webhooks that start them: `integrations.Service.Triggers` wraps the webhook emitter of the billing and payments services, so `invoice.created`, `credit_note.created`, `subscription.started` and the others queue the provider's sync in the same transaction. Every synced record is an `integration.Resource` (the provider's ID per invoice, credit note, payment or subscription).
* `integrations.Taxes` is the billing engine's `kernel.TaxProvider`: drafts and previews are quoted (Anrok's answers kept in an in-process cache), finalized invoices and credit notes reported, voided invoices voided or negated.
* Each job kind has the retry rules of its lago job (`integrations.RetryRules`: request limits, provider failures, timeouts after which NetSuite is searched before the record is created again, incomplete documents); other errors fail the job and deliver `integration.provider_error` or the customer error webhooks.
* Errors are `*integration.ProviderError` with an `integration.ErrorKind`; `ErrRequestLimit` and `ErrRetryable` classify what is retried, `ErrAuthentication` a connection whose credentials fail. The API answers a provider's refusal with FailedPrecondition and the provider's code, and an operation the provider lacks with Unimplemented.
* The adapters share `providers/apiclient`: one HTTP client per vendor, a rate limiter per connection (per tenant for Xero) that waits rather than fails, and the injected OTel providers.

---

## 13. Deployment

### 13.1 Roles
The single binary `tanvo serve` runs any combination of roles:

| Role | Runs |
| :--- | :--- |
| `api` | Connect/REST server, provider webhooks, document downloads |
| `worker` | River workers for the queues it is given (`--queues`) |
| `consumer` | Kafka consumers: enrichment, ClickHouse writer with in-advance charges, usage refresh |
| `all` | Everything (default for self-hosted installations); `consumer` only when Kafka brokers are configured |

`tanvo migrate`, `tanvo admin …`, `tanvo events reprocess` (§7.2) and `tanvo version` are separate commands.

### 13.2 Self-hosted
* Minimal: `tanvo serve` + PostgreSQL. Typst and the `fonts-noto-cjk` package (zh-CN and zh-TW documents, §10) are included in the container image; installations outside it need both.
* Optional: Redpanda/Kafka + ClickHouse for high event volumes, Redis/Valkey for caching, S3-compatible storage.
* Distributed as a container image and a Docker Compose file; a Helm chart serves both self-hosted Kubernetes and Tanvo Cloud.

### 13.3 Tanvo Cloud
* Kubernetes: one Deployment per role (and per heavy queue), migrations as a Job, horizontal autoscaling of `api` and `consumer`.
* **Cells**: a cell is a PostgreSQL cluster with its River workers, Kafka topics and ClickHouse database, serving a set of organizations. A thin control plane maps organizations to cells; growth adds cells. A large customer can get a dedicated cell.
* Tanvo Cloud is billed by Tanvo itself.

### 13.4 Process Wiring
`apps/server` is the only module that builds concrete implementations; `internal/app` wires one component per file (`postgres.go`, `migrate.go`, `jobs.go`, `eventstore.go`, `ingest.go`, `kafka.go`, `webhooks.go`, `mail.go`, `api.go`, `health.go`). At startup a process:

1. Loads and validates the configuration (§13.5), failing with every problem at once, and installs telemetry (§15).
2. Opens the PostgreSQL pool and, when `TANVO_CLICKHOUSE_URL` is set, the ClickHouse connection.
3. Applies the schemas (`TANVO_MIGRATE_ON_START`, the self-hosted default) or checks them and refuses to start (§5.2). The PostgreSQL event store lives in its own schema (`TANVO_EVENTSTORE_SCHEMA`, `eventstore`), apart from the store's raw event log (`public.events`); the event store qualifies every table, function and lock key with that schema (`postgres.WithSchema`), on the main pool and inside the ingestion transaction alike (`store.InPgxTx` hands the store transaction's `pgx.Tx` to the event store), so no connection changes its `search_path`.
4. Builds the store with River as its job inserter (jobs commit with the business transaction), registers the job kinds whose services are wired, and creates the River client: workers on the configured queues plus the clock queue for the `worker` role, insert-only otherwise.
5. Selects each organization's event store from its ingestion mode: PostgreSQL in postgres mode, ClickHouse in kafka mode.
6. Creates the Kafka producer when an organization ingests through Kafka or the `consumer` role runs, and for that role the enrichment (`<prefix>-enrich`: raw → enriched) and ClickHouse writer (`<prefix>-write`: enriched → ClickHouse, usage signals) consumer groups.
7. Serves HTTP on every role: `/healthz` (liveness), `/readyz` (PostgreSQL, the event store pool, ClickHouse and Kafka when used; unavailable while shutting down), `/metrics` when enabled, and on the `api` role the Connect handlers and their REST transcoding under `/v1/` (Vanguard), behind interceptors for telemetry, panic recovery, API key authentication and authorization by each RPC's access option, and protovalidate.

On SIGINT or SIGTERM it shuts down within `TANVO_SHUTDOWN_TIMEOUT`: readiness turns unavailable, the HTTP server drains, workers finish their jobs, consumers commit and stop, the API role records the API key uses it collected (also recorded every minute, in `api_key_uses`; the hourly `clock.track_api_key_usage` job applies them to the keys per organization), and the connections close.

### 13.5 Configuration
Settings are `TANVO_*` environment variables over an optional TOML file (`--config` or `TANVO_CONFIG`) whose keys are the variable names without the prefix, in lower case; the environment wins. Unknown `TANVO_*` variables and file keys are logged as warnings. Lists are comma-separated. OpenTelemetry is configured by the standard `OTEL_*` variables (§15).

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `TANVO_ROLE` | `all` | Roles of `tanvo serve` (`api`, `worker`, `consumer`, `all`); `--role` overrides it. `all` includes `consumer` only when Kafka brokers are set |
| `TANVO_ENVIRONMENT` | | `deployment.environment.name` |
| `TANVO_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `TANVO_LOG_FORMAT` | `json` | `json` or `text` |
| `TANVO_HTTP_ADDR` | `:8080` | Listen address (API, health, metrics) |
| `TANVO_HTTP_READ_HEADER_TIMEOUT` | `10s` | Bound on reading request headers |
| `TANVO_HTTP_MAX_BODY_BYTES` | `8388608` | Bound on a request body |
| `TANVO_TRUSTED_PROXIES` | empty | Extra proxies (addresses or CIDR ranges, comma-separated) trusted to report the client address, besides the loopback and private ranges |
| `TANVO_SHUTDOWN_TIMEOUT` | `30s` | Bound on the graceful shutdown |
| `TANVO_METRICS_ENDPOINT` | `false` | Serve metrics in the Prometheus format at `/metrics` |
| `TANVO_DATABASE_URL` | required | PostgreSQL URL (`postgres://…`) |
| `TANVO_DATABASE_MAX_CONNS` | pgx default | Pool size |
| `TANVO_DATABASE_ROW_LEVEL_SECURITY` | `false` | Set the tenant for the row-level security policies (§5.4) |
| `TANVO_MIGRATE_ON_START` | `true` | Apply the migrations at startup; otherwise check them |
| `TANVO_JOBS_SCHEMA` | `river` | Schema of River's tables |
| `TANVO_WORKER_QUEUES` | every queue | Queues of the `worker` role; `--queues` overrides it |
| `TANVO_JOBS_DISABLE_PERIODIC` | `false` | Keep the workers out of the periodic schedule |
| `TANVO_JOBS_CLOCK_CONCURRENCY` | library default | Concurrency of the clock queue |
| `TANVO_JOBS_QUEUE_CONCURRENCY` | library defaults | Comma-separated `queue=n` overrides of the workload queues' concurrency (1 to 10000), e.g. `alerts=20,wallets=5` |
| `TANVO_SUBSCRIPTION_TERMINATION_ALERT_DAYS` | `15,45` | Days before its ending date a subscription gets `subscription.termination_alert` (comma-separated); empty sends none |
| `TANVO_DAILY_USAGE_SCHEDULING_JITTER` | 30 minutes | Window the daily usage computations of one hourly run are spread over |
| `TANVO_USAGE_CACHE_ENTRIES` | `100000` | Charge buckets of current usage kept per server process (§8); `0` turns the cache off |
| `TANVO_DISABLE_EVENTS_VALIDATION` | false | Leaves the hourly post-validation of events (`events.errors`) out of the schedule |
| `TANVO_INGEST_MODE` | `postgres` | Default ingestion mode, `postgres` or `kafka` |
| `TANVO_INGEST_KAFKA_ORGANIZATIONS` | | Organizations ingesting through Kafka whatever the mode |
| `TANVO_INGEST_POSTGRES_ORGANIZATIONS` | | Organizations ingesting in PostgreSQL whatever the mode |
| `TANVO_INGEST_ENRICH_CONCURRENCY` | `4` | Enrichment concurrency of a consumer |
| `TANVO_EVENTSTORE_SCHEMA` | `eventstore` | Schema of the PostgreSQL event store |
| `TANVO_EVENTSTORE_HASH_PARTITIONS` | `4` | Hash partitions of the PostgreSQL event store, fixed at creation |
| `TANVO_CLICKHOUSE_URL` | | ClickHouse DSN (`clickhouse://…`); required by kafka ingestion and the `consumer` role |
| `TANVO_KAFKA_BROKERS` | | Seed brokers; required by kafka ingestion and the `consumer` role |
| `TANVO_KAFKA_CLIENT_ID` | `tanvo` | Kafka client ID |
| `TANVO_KAFKA_TLS` | `false` | Connect with TLS |
| `TANVO_KAFKA_SASL_MECHANISM` | | `plain`, `scram-sha-256` or `scram-sha-512` |
| `TANVO_KAFKA_SASL_USERNAME`, `TANVO_KAFKA_SASL_PASSWORD` | | SASL credentials |
| `TANVO_KAFKA_TOPIC_PREFIX` | | Prefix of the topic names (`events.raw`, `events.enriched`, `events.dead_letter`, `usage.refresh`) |
| `TANVO_KAFKA_CONSUMER_GROUP_PREFIX` | `tanvo` | Prefix of the consumer groups |
| `TANVO_KAFKA_CREATE_TOPICS` | `false` | Create missing topics at startup |
| `TANVO_KAFKA_TOPIC_PARTITIONS` | `6` | Partitions of created topics |
| `TANVO_KAFKA_TOPIC_REPLICAS` | `-1` | Replication factor of created topics (broker default) |
| `TANVO_WEBHOOKS_ALLOW_PRIVATE` | `false` | Let deliveries reach private addresses and plain HTTP |
| `TANVO_OIDC_ALLOW_PRIVATE` | `false` | Let OIDC providers be reached at private addresses and over plain HTTP (dev mode) |
| `TANVO_STREAMING_INTERMEDIATE_ROLE_ARN` | | Role the workers assume before each streaming destination's role (role chaining); unset assumes the destination roles directly |
| `TANVO_STREAMING_KINESIS_ENDPOINT` | | Kinesis endpoint overriding the regional one (tests, LocalStack, VPC endpoints) |
| `TANVO_STREAMING_STS_ENDPOINT` | | STS endpoint overriding the regional one |
| `TANVO_WEBHOOKS_TIMEOUT` | `30s` | Bound on one delivery attempt |
| `TANVO_API_URL` | | Public API URL; payment providers deliver their webhooks to `TANVO_API_URL/webhooks/{provider}/{connection_id}`, registered at the provider when a connection is created; unset registers none |
| `TANVO_GOCARDLESS_CLIENT_ID` | | Client ID of the GoCardless partner application merchants connect to by OAuth; unset, GoCardless cannot be connected |
| `TANVO_GOCARDLESS_CLIENT_SECRET` | | Its client secret |
| `TANVO_GOCARDLESS_SANDBOX` | `false` | Connect merchants through the GoCardless sandbox |
| `TANVO_XERO_CLIENT_ID` | | Client ID of the Xero app merchants connect their organizations to by OAuth; unset, Xero cannot be connected |
| `TANVO_XERO_CLIENT_SECRET` | | Its client secret |
| `TANVO_HUBSPOT_CLIENT_ID` | | Client ID of the HubSpot app merchants connect their portals to by OAuth; unset, HubSpot cannot be connected |
| `TANVO_HUBSPOT_CLIENT_SECRET` | | Its client secret |
| `TANVO_ANROK_DRAFT_TAXES_CACHE_TTL` | `24h` | How long Anrok's taxes of a draft invoice are kept for the same request; `0` turns the cache off |
| `TANVO_ANALYTICS_CACHE_TTL` | `4h` | How long a computed analytics report is reused, per server process; `0` turns the cache off |
| `TANVO_APP_URL` | | Web console URL used in email links |
| `TANVO_MAIL_FROM` | | Sender address; required with `TANVO_SMTP_HOST` |
| `TANVO_SMTP_HOST` | | SMTP server; empty logs and drops emails |
| `TANVO_SMTP_PORT` | `587` | SMTP port |
| `TANVO_SMTP_USERNAME`, `TANVO_SMTP_PASSWORD` | | SMTP credentials |
| `TANVO_SMTP_SECURITY` | `starttls` | `starttls`, `starttls_optional`, `tls` or `none` |
| `TANVO_BLOB_BACKEND` | | Blob store of the documents, logos, quote files and data exports: `local`, `s3` or `gcs`; empty stores nothing and renders no document. Requires `TANVO_SECRET_KEY` and `TANVO_API_URL` |
| `TANVO_BLOB_LOCAL_ROOT` | | Directory of the `local` backend |
| `TANVO_BLOB_ENDPOINT` | `https://storage.googleapis.com` for `gcs` | Service URL of the `s3` backend (required), e.g. `https://s3.eu-west-1.amazonaws.com` |
| `TANVO_BLOB_REGION` | `auto` for `gcs` | Signing region (required by `s3`) |
| `TANVO_BLOB_BUCKET` | | Bucket of the `s3` and `gcs` backends |
| `TANVO_BLOB_ACCESS_KEY_ID`, `TANVO_BLOB_SECRET_ACCESS_KEY` | | Access key and secret (HMAC keys for `gcs`) |
| `TANVO_BLOB_SESSION_TOKEN` | | Session token of temporary credentials |
| `TANVO_BLOB_PATH_STYLE` | `false` | Path-style bucket addressing (MinIO) |
| `TANVO_BLOB_PREFIX` | | Key prefix inside the bucket |
| `TANVO_BLOB_URL_TTL` | `15m` | Lifetime of the download URLs the permanent file links redirect to (at most 7 days) |
| `TANVO_TYPST_PATH` | `typst` in `PATH` | Typst executable rendering the PDFs |
| `TANVO_DOCUMENTS_FONT_PATHS` | system font directories | Font directories for the scripts the embedded font does not cover (Noto CJK for zh-CN and zh-TW) |
| `TANVO_DISABLE_PDF_GENERATION` | `false` | Render no PDF and attach none to emails; e-invoice XML is still made |
| `TANVO_DISABLE_SIGNUP` | `false` | turns off self sign-up |
| `TANVO_GOOGLE_AUTH_CLIENT_ID`, `TANVO_GOOGLE_AUTH_CLIENT_SECRET` | | OAuth client of Google sign-in, redirected to `TANVO_APP_URL/auth/google/callback`; unset disables it |
| `TANVO_SECRET_KEY` | | installation secret, base64 of at least 32 bytes; HKDF-SHA256 derives the key signing customer portal tokens and the AES-256-GCM key sealing integration secrets (SSO client secrets and x402 connection credentials) from it. Unset disables the customer portal and the SSO connections, and fails every x402 facilitator call; changing it invalidates portal links and makes sealed secrets undecryptable |

---

## 14. Enterprise Edition (ee/)

Features behind Lago's premium license (progressive billing, wallets' advanced rules, integrations, e-invoicing, …) are part of the AGPL core with no gate. Only what Lago runs as private cloud services is implemented as enterprise services under ELv2, each a separate process that uses `libs/proto` contracts and read-only data (PostgreSQL views or ClickHouse):

* Analytics and dashboards (revenue, MRR, usage, collections, forecasts), replacing Lago's private Data API and Superset.
* The billing assistant: an AI agent with an MCP server exposing Tanvo's API.
* Warehouse sync: copying the core's read-only export views, with their deletion tombstones, to customer data warehouses, replacing Lago's private data pipeline ([Feature Parity §1.43](parity.md#143-data-exports)).
* Customer-success tooling and the other private services listed in [Feature Parity §1.50](parity.md#150-lago-gates-and-their-tanvo-placement).

Self-hosted installations enable them with an offline-verifiable license key (Ed25519-signed); Tanvo Cloud enables them per plan. The AGPL core contains no license checks.

---

## 15. Security and Observability

* Secrets are encrypted with AES-256-GCM envelopes; data keys are encrypted by a key from configuration or a KMS. Today `auth.SecretBox` seals integration secrets with one AES-256-GCM key derived from `TANVO_SECRET_KEY` (HKDF-SHA256), with the organization ID as additional data; envelopes and KMS keys come later.
* Security-relevant actions (sign-ins, key creation, role changes) are written to the security log.
* **Observability follows OpenTelemetry** for traces, metrics and logs:
  * Libraries depend only on the OTel API and take their `TracerProvider` and `MeterProvider` through constructor options, no-op by default; only `apps/server` imports the SDK and the exporters (enforced by depguard) and installs the global providers.
  * The SDK is configured by the standard `OTEL_*` environment variables (`OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_EXPORTER_OTLP_*`, `OTEL_TRACES_SAMPLER`, …). OTLP over gRPC or HTTP is the default exporter; a Prometheus `/metrics` endpoint is an optional metric reader for pull-based setups.
  * The resource carries `service.name` (`tanvo`), `service.version`, `service.instance.id`, `deployment.environment.name` and the process role (`tanvo.role`); Kubernetes attributes come from the environment or the Collector.
  * Instrumentation uses maintained instrumentation libraries and the semantic conventions: `otelhttp` and a Connect interceptor emitting the RPC semantic conventions for the API, `otelpgx` for PostgreSQL, `kotel` for franz-go, River's OpenTelemetry middleware, spans around ClickHouse statements (`clickhouse.WithTracerProvider`: `db.system.name`, `db.operation.name`, `db.collection.name` and the parameterized `db.query.text`) and provider calls. Names and units follow the semantic conventions (`http.server.request.duration` in seconds); Tanvo's own instruments and attributes use the `tanvo.` prefix (`tanvo.jobs.duration`, `tanvo.ingest.events`).
  * Context propagates as W3C Trace Context and Baggage, also across asynchronous boundaries: Kafka record headers and job metadata carry the `traceparent`. A consumer or job span is a child of the producing span when it handles one request's work, and has a span link to it for batches, fan-outs and periodic jobs.
  * Logs are `slog` records written as JSON to stdout and bridged to OTel logs (`otelslog`), carrying `trace_id` and `span_id`; the organization, request and job IDs are attributes.
  * The organization ID is an attribute of spans and logs (`tanvo.organization.id`) but never of metrics: metric attributes are bounded (role, queue, kind, status, provider). Per-tenant analysis uses traces and exemplars.
  * API keys, credentials, payment details, personal data and event properties are never written to span, metric or log attributes.
  * Other backends (Sentry, Datadog, …) are reached through the OTel Collector, not through vendor SDKs.
  * Health (`/healthz`, `/readyz`) is separate from telemetry: readiness checks PostgreSQL, the event store pool and, when used, ClickHouse and the Kafka connection (§13.4).
