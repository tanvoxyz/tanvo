# Tanvo Feature Parity

> English | [简体中文](parity.zh-CN.md)

> Related: [Architecture](architecture.md) · [Domain Model](domain.md) · [Development](development.md) · [Testing](testing.md) · [Roadmap](roadmap.md) · [AGENTS.md](../AGENTS.md)

> This is the authoritative checklist of lago-api features and their Tanvo counterparts. Every non-deprecated lago-api feature has a row in §1. Lago's deprecated or legacy parts are in §2, the parts Tanvo replaces with a different design are in §3, and the upstream bugs that Tanvo deliberately does not reproduce are in §4. Lago source paths are relative to the lago-api repository (`~/Code/frameworks/lago/api`) unless they name another repository.

---

## How to read this document

* **Feature**: one row per behavior that can be tested on its own (each charge model, each aggregation type, each webhook event group, each provider capability).
* **Lago source**: the main file or directory implementing it; tests are ported from the matching `spec/` path ([Testing §2](testing.md)).
* **Tanvo module**: the module that owns the logic ([Architecture §3](architecture.md#3-repository-structure)). API surfaces live in `libs/proto` and `apps/server`; persistence in `libs/store`. Those are implied and not repeated unless they are the main work.
* **Status**: `done` (implemented with ported tests), `logic done` (the computation is implemented with ported tests in its library; persistence, API or jobs are still to come), `in progress` (the owning library is being built), `planned` (not started).
* **Notes**: where Tanvo deliberately differs from Lago. A row without a note behaves as in Lago, apart from the API conventions that apply everywhere (§3.1).

Lago gates many features behind `License.premium?`, `organization.premium_integrations` or `organization.feature_flags`. Tanvo has no license checks in the AGPL core and no feature flags for shipped features. Every billing, payment, integration and access feature is part of the core. Only features that Lago runs as private cloud services become separate enterprise services under `ee/` ([Architecture §14](architecture.md#14-enterprise-edition-ee)). §1.50 maps each Lago gate.

## Alignment baseline

The parity target is the lago-api release **v1.55.0** (commit `25b8b7b09`, the latest Lago release when this baseline was set). Code that exists only on Lago's `main` or on unreleased branches is not a target. When Lago publishes a newer release, Tanvo moves the baseline to it in one deliberate pass and updates this section.

There are no exceptions: unreleased Lago features (for example the structured payment terms on its `feat-payment-terms-*` branches, whose columns v1.55.0 already contains but does not read) are not implemented until a release ships them.

## 1. Feature Checklist

### 1.1 Money and Numbers

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Exact decimal arithmetic for amounts and quantities | `BigDecimal` throughout `app/services/` | libs/money | done | `money.Decimal` on `cockroachdb/apd/v3`; never `float64` |
| ISO 4217 currencies (all currencies Lago accepts) with minor-unit exponents | `config/initializers/money.rb`, `app/models/concerns/currencies.rb` | libs/money | done | |
| Minor-unit conversion (cents ↔ decimal amounts) | `money-rails` usage in models | libs/money | done | |
| Rounding half-up to currency precision | `app/services/` rounding via `.round` | libs/money | done | Rounding mode is explicit at every call site |
| Largest-remainder allocation of an amount across parts | `app/services/credit_notes/apply_taxes_service.rb`, `app/services/invoices/` tax allocation | libs/money | done | Used for tax and credit allocation |
| Precise amounts beyond minor units (`precise_amount_cents`, `precise_unit_amount`) | `app/models/fee.rb` | libs/money | done | Stored as `numeric` |
| Wallet credit precision: credits floored to 5 decimal places | `app/services/wallet_transactions/` | libs/money | done | |

### 1.2 Organizations

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Organization profile (name, legal name and number, address, email, tax id, logo) | `app/models/organization.rb`, `app/services/organizations/update_service.rb` | libs/core | planned | |
| Organization time zone, default currency, document locale | `app/models/organization.rb` | libs/core | planned | Defaults; billing entities and customers override them |
| Organization settings read and update API | `app/controllers/api/v1/organizations_controller.rb` | apps/server | in progress | `OrganizationService` (`auth.OrganizationSettings`) reads the organization and updates its slug, email, address, logo and authentication methods; the address is merged field by field. A logo upload is refused until a blob store is wired. The other profile fields of `organizations_controller.rb` are not exposed yet |
| Organization slug (used in document numbers and receipts) | `app/models/organization.rb` | libs/core, libs/auth | done | Normalized and checked against the reserved and taken slugs when it changes (`app/services/organizations/update_service.rb`) |
| Audit log retention period setting (`audit_logs_period`) | `app/models/organization.rb` | libs/core, libs/auth | logic done | Applied to the activity and API log queries (`activity_logs_query.rb`, `api_logs_query.rb`); not updatable through the API, as in v1.55.0 |
| Maximum wallets per customer setting | `app/models/organization.rb` | libs/billing | logic done | |
| Allowed authentication methods (email/password, Google, Okta, Entra ID) per organization | `app/models/organization.rb`, `app/services/organizations/update_service.rb` | libs/auth, apps/server | done | Okta and Entra ID become OIDC connections; generic OIDC (`oidc`) is added as a Tanvo extension. Changing them needs a user holding `authentication_methods:update`, which v1.55.0 defines (`config/permissions.yml`) but does not check on the server (`app/graphql/types/organizations/update_organization_input.rb:8`); API keys are refused, since the REST API does not take them |
| Email to admins when authentication methods change | `app/mailers/organization_mailer.rb` | libs/mail | logic done | |
| Per-organization HMAC key for webhook signing | `app/models/organization.rb` | libs/webhooks | planned | Replaced by per-endpoint Standard Webhooks secrets (§3) |
| Organization bootstrap at first start | `config/initializers/` (`LAGO_CREATE_ORG`, `LAGO_ORG_USER_*`) | apps/server | planned | `tanvo admin bootstrap` command instead of environment variables |
| Event store selection per organization (PostgreSQL or ClickHouse) | `app/models/organization.rb` (`clickhouse_events_store`) | libs/eventstore | planned | Installation setting with per-organization override |

### 1.3 Billing Entities

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Billing entity CRUD (legal identity, address, logo, currency, time zone, locale) | `app/models/billing_entity.rb`, `app/services/billing_entities/` | libs/billing | in progress | Create, get, list and update in `libs/billing/billingentities` with the ported specs. Archiving is not served: v1.55.0 never archives (its destroy mutation is a no-op, `app/graphql/mutations/billing_entities/destroy.rb:17-20`). Taxes, invoice custom sections and the dunning campaign are accepted on creation too; logos need a blob store, which the server does not wire yet |
| Default billing entity per organization and per-customer assignment | `app/models/billing_entity.rb`, `app/services/customers/` | libs/core, libs/billing | in progress | The first active entity created becomes the default (`app/models/organization.rb` `default_billing_entity`); a customer takes the entity given by ID or `code:` on create or update, else the default; an archived entity is `billing_entity_not_found` (`upsert_from_api_service.rb:154`) |
| Number of billing entities per organization (1, 2 or unlimited by plan) | `app/models/organization.rb` (`MULTI_ENTITIES_MAX`) | libs/billing | done | No limit in the core: the plan check of `app/services/billing_entities/create_service.rb:20` is dropped |
| Document numbering per customer or per billing entity | `app/services/billing_entities/change_invoice_numbering_service.rb` | libs/billing | logic done | |
| Document number prefix | `app/models/billing_entity.rb` | libs/billing | logic done | |
| Invoice grace period, payment term, invoice footer | `app/models/billing_entity.rb` | libs/billing | logic done | |
| Issuing date anchor and adjustment settings | `app/models/billing_entity.rb` | libs/billing | logic done | |
| Finalize zero-amount invoices setting | `app/models/billing_entity.rb` | libs/billing | logic done | |
| EU tax management flag and automatic EU taxes | `app/services/billing_entities/`, `app/services/taxes/auto_generate_service.rb` | libs/billing | done | Enabling it on an EU entity creates or updates the EU taxes (`tanvo_eu_*`, auto-generated) in the same transaction |
| E-invoicing enablement for supported countries (France, Germany) | `app/models/billing_entity.rb` | libs/documents | logic done | |
| Email settings (which documents are emailed) and from-address | `app/models/billing_entity.rb` (`email_settings`) | libs/mail | in progress | |
| Taxes applied to a billing entity (apply, remove, manage) | `app/services/billing_entities/taxes/` | libs/billing | done | Replaced through `tax_ids` (IDs or `code:`), flagging the entity's drafts for refresh in the same transaction. Codes match exactly; v1.55.0 matches them case-insensitively (`manage_taxes_service.rb:31-32`) |
| Applied dunning campaign per billing entity | `app/services/billing_entities/update_applied_dunning_campaign_service.rb` | libs/billing | done | A change resets the attempts of the entity's customers falling back to its campaign |
| Billing entity API | `app/controllers/api/v1/billing_entities_controller.rb` | apps/server | done | `BillingEntityService` Create, Get, List, Update; ArchiveBillingEntity is unimplemented (see CRUD). The drafts' issuing dates move in the update's transaction instead of a job (`update_invoice_issuing_date_settings_service.rb:21-22`) |

### 1.4 Users, Memberships, Roles and Invites

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Users and memberships (active, revoked) across organizations | `app/models/user.rb`, `app/models/membership.rb` | libs/auth, apps/server | done | `MemberService` lists memberships (newest first, with the admin count, searchable on email and filtered on role) and reads one (`memberships_resolver.rb`) |
| Multi-role memberships: union of the roles' permissions, at least one role | `app/models/membership.rb`, `app/models/membership_role.rb` | libs/core, libs/store, libs/auth | logic done | `membership_roles` table with soft-deleted rows; `Membership.RoleIDs`, `access.AnyGrants` |
| Predefined roles admin, finance, manager | `app/models/role.rb`, `config/permissions.yml` | libs/auth | logic done | |
| Custom roles with permission sets | `app/services/roles/` | libs/auth, apps/server | done | `MemberService` CreateRole, GetRole, ListRoles, UpdateRole and DeleteRole (`auth.Roles`), with `role.*` security logs; roles are referenced by ID or `code:<code>`; predefined roles cannot be changed |
| Custom role deletion: soft delete, refused for predefined roles and roles of active members | `app/services/roles/destroy_service.rb`, `app/models/role.rb` | libs/core, libs/store, libs/auth | logic done | `auth.Roles.Delete` with the `role.deleted` security log; deleted roles are hidden from lookups, listings and memberships, and their codes can be reused |
| Permission catalog (every resource and action) | `config/permissions.yml` | libs/auth, apps/server | done | The catalog is `libs/core/permission`; roles and API keys take its permission strings. No RPC lists it (v1.55.0 has none) |
| Membership rules: cannot revoke own membership, cannot remove last admin, only admins grant admin | `app/services/memberships/` | libs/auth, apps/server | done | `auth.Memberships` (UpdateRoles, Revoke) with `user.role_edited` and `user.deleted` security logs, served by `MemberService.UpdateMembership` and `RevokeMembership`; revoking a revoked membership succeeds unchanged |
| Invites with token, roles, accept, revoke, update | `app/models/invite.rb`, `app/services/invites/` | libs/auth, apps/server | done | Several roles per invite (`role_ids`), stored as IDs rather than codes. `MemberService` creates (returning the accept URL once), lists, updates and revokes invites; `AuthService` accepts them |
| Password reset by email (30-minute token) | `app/services/password_resets/` | libs/auth, apps/server | done | `AuthService.RequestPasswordReset` and `ResetPassword`; an unknown email succeeds without an email instead of failing with `user` `not_found` (`app/services/password_resets/create_service.rb:14`), so the RPC does not reveal accounts |
| Sign-up toggle for self-hosted installations | `LAGO_DISABLE_SIGNUP`, `app/services/users_service.rb` | apps/server | done | `TANVO_DISABLE_SIGNUP` |

### 1.5 Authentication and Sessions

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Email and password sign-in and registration | `app/services/users_service.rb`, `app/graphql/mutations/` | libs/auth, apps/server | done | argon2id hashes; opaque sessions instead of JWT (§3). `AuthService` (sign-up, password and OIDC sign-in, invites, current user, sign-out); a session acts in the organization of the `X-Tanvo-Organization` header ([Architecture §4.4](architecture.md#44-authentication-and-authorization)); expired sessions are purged by the hourly `clock.purge_sessions` job |
| Google sign-in, sign-up and invite acceptance | `app/services/auth/google_service.rb` | libs/auth | logic done | Through the OIDC identity provider |
| Okta sign-in (authorize, token, userinfo, state check) | `app/services/auth/okta/` | libs/auth | logic done | OIDC connection; single-use state in PostgreSQL (`oidc_states`, 10 minutes instead of 90 seconds) |
| Entra ID sign-in with tenant and additional domains | `app/services/auth/entra_id/` | libs/auth | logic done | OIDC connection |
| Okta and Entra ID integration configuration (client, domain, host) | `app/models/integrations/okta_integration.rb`, `entra_id_integration.rb` | libs/core, libs/auth | logic done | `integration.AuthSettings` (plus a required default role); domains are routed case-insensitively across organizations (`IntegrationRepository.ListByDomain`, `auth.IntegrationConnections`). `SsoConnectionService` (`auth.SSOConnections`) ports the Okta and Entra ID create, update and destroy services: create enables the method; destroy disables it and is refused with `enabled_authentication_methods_required` when no other method remains; a blank or masked (`••••••••…`) secret keeps the stored one; claimed domains must be unique per provider. Each change writes the `integration.*` security log. The client secret is sealed with AES-256-GCM (key derived from `TANVO_SECRET_KEY`, bound to the organization) and is never returned. The service needs `TANVO_SECRET_KEY`; without it the RPCs are Unimplemented |
| Generic OIDC provider (issuer discovery, any standard OpenID provider) | none (Tanvo extension beyond the v1.55.0 baseline, by the user's decision) | libs/core, libs/auth, apps/server | logic done | Provider `oidc`, method `oidc`, callback `/auth/oidc/callback`. One standards-based client serves Google, the Okta and Entra ID presets and generic connections. Configured by `issuer_url`: discovery must report exactly that issuer, uses https outside dev mode and is cached (1 hour, kept on a failed refetch); the JWKS is cached for 1 hour and refetched on an unknown `kid` at most once a minute. Provider requests go through the SSRF-guarded client (private and loopback addresses refused unless `TANVO_OIDC_ALLOW_PRIVATE`), with a 10-second timeout and 1 MiB responses. ID tokens: RS, ES and PS families and EdDSA only (never `none` or HMAC); `iss`, `aud`, `azp` with several audiences, `exp`, `nbf`, `iat` with 30 seconds of leeway, and the nonce. Per connection: client id and sealed secret, token endpoint auth method (`client_secret_basic` or `client_secret_post`, default from discovery), scopes (default `openid email profile`), email and name claims, `email_verified` required unless `allow_unverified_email`, extra authorization parameters (protocol parameters refused), claimed domains and default role. Userinfo fills missing claims. Create and update fetch discovery once and fail with `issuer_url` / `https_required`, `issuer_unreachable`, `issuer_mismatch`, `discovery_incomplete` or `token_endpoint_auth_method` / `unsupported_by_provider` |
| Refuse removing the last enabled sign-in method | `app/services/integrations/okta/destroy_service.rb` | libs/auth | logic done | |
| Known devices and new-device security log entry | `app/models/user_device.rb`, `app/services/user_devices/` | libs/core, libs/store, libs/auth, apps/server | logic done | `access.UserDevice`; apps/server parses the user agent into browser, OS and device type (`Utils::DeviceInfo`) for every sign-in; the security log is not wired yet |
| OAuth callback for GoCardless connection | `app/services/payment_providers/gocardless_service.rb` (`LAGO_OAUTH_PROXY_URL`) | providers/gocardless | planned | Callback served by `apps/server`; no proxy |

### 1.6 API Keys and API Conventions

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Multiple named API keys per organization | `app/models/api_key.rb`, `app/services/api_keys/` | libs/auth, apps/server | done | Stored as SHA-256 hashes and shown once. `ApiKeyService` (sessions only); reads and lists return the active keys, listed oldest first (`api_keys_resolver.rb:18`) |
| Per-key permissions by resource (read, write) | `app/models/api_key.rb` (`RESOURCES`) | libs/auth, apps/server | done | Same permission strings as roles; no grant means full access |
| Key expiry and rotation | `app/services/api_keys/rotate_service.rb` | libs/auth, apps/server | done | Rotation keeps the old key valid for an overlap window; `RotateApiKey` returns the new key |
| Refuse destroying the last non-expiring key | `app/services/api_keys/destroy_service.rb` | libs/auth, apps/server | done | `DeleteApiKey` expires the key now |
| Last-used tracking | `app/jobs/clock/api_keys/track_usage_job.rb` | libs/auth | done | The API role collects uses in memory and records them every minute in `api_key_uses` (lago writes the Rails cache on each request, `app/controllers/api/base_controller.rb:59`); the hourly :15 clock job enqueues one job per organization with recorded uses, which moves them to `last_used_at` (never backwards) |
| Emails when keys are created, rotated or destroyed | `app/mailers/api_key_mailer.rb` | libs/mail | logic done | Sent to the organization's admins by the `mail.send` job (apps/server) |
| Idempotency of write requests | `app/models/idempotency_record.rb`, `app/services/idempotency_records/` | apps/server | done | `Idempotency-Key` header on every RPC with side effects; responses kept 24 hours ([Architecture §4.5](architecture.md#45-idempotency)). Lago's records are internal keys of a few services; the client header is Tanvo's |
| Health and readiness endpoints | `config/routes.rb` (`/health`, `/ready`) | apps/server | done | `/healthz`, `/readyz`; readiness checks PostgreSQL, the event store and, when used, ClickHouse and Kafka |
| Prometheus metrics endpoint | `config/initializers/yabeda.rb` | apps/server | done | OpenTelemetry (OTLP by default) and an optional `/metrics` reader (`TANVO_METRICS_ENDPOINT`) |
| Current version endpoint | `app/graphql/resolvers/version_resolver.rb` | apps/server | done | `ServerService.GetServerInfo`: version and revision from the build |
| Rate limiting of calls to third-party APIs | `config/initializers/throttling.rb` | providers/* | planned | Per-provider limits (HubSpot 110/10s, Xero 60/min and 5000/day, NetSuite 10/s, Anrok 9/s, Avalara 10/s) |

### 1.7 Customers

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Customer create or update by external id (upsert) | `app/services/customers/upsert_from_api_service.rb` | libs/billing, apps/server | done | `CustomerService` over libs/billing/customers: `CreateCustomer` only creates (an external ID a live customer has is `external_id` `value_already_exist`), `UpdateCustomer` updates by ID or `external_id:` and, with `allow_missing`, upserts (lago's POST upserts, `upsert_from_api_service.rb:21`); field presence and `update_mask` distinguish absent from cleared. Null bytes are stripped and emails sanitized. A change to `external_id` or `account_type` of a customer that is no longer editable is ignored, as in lago (`update_service.rb:103-109`, `upsert_from_api_service.rb:81`). Timezone, grace period and partner accounts are not premium-gated. Provider sync of the customer waits for the provider clients (§3) |
| Customer type (company, individual) and account type (customer, partner) | `app/models/customer.rb` | libs/core, apps/server | done | |
| Partner accounts and self-billed invoices | `app/models/customer.rb`, `app/services/invoices/` | libs/billing | logic done | |
| Sequential id and slug per organization | `app/models/customer.rb` | libs/billing | done | As in lago, the sequence is per organization and counts deleted customers (`app/models/customer.rb:140-141`), and the slug's prefix is the organization's document number prefix (`app/models/customer.rb:424-430`, default from `app/models/organization.rb:319-320`) |
| Billing and shipping addresses | `app/models/customer.rb` | libs/core, apps/server | done | Countries are upper-cased |
| Tax identification number, legal name and number | `app/models/customer.rb` | libs/core, apps/server | done | |
| Default currency: a preference for new billing objects, not a constraint | `app/services/customers/update_currency_service.rb` | libs/core | logic done | Set by the first billing object when absent; an explicit update can change it at any time |
| Several currencies per customer: subscriptions in their plan's currency; wallets, applied coupons and one-off invoices in their own; invoices split per currency | `app/services/subscriptions/organization_billing_service.rb` (`group_by_currency`), `app/services/wallets/create_service.rb`, `app/services/applied_coupons/create_service.rb`, `app/services/invoices/create_one_off_service.rb` | libs/billing | logic done | Wallets and fixed-amount coupons apply only to invoices in their currency; dunning counts attempts per currency (§1.35) |
| Time zone with fallback to billing entity and organization | `app/models/customer.rb` | libs/period | planned | Fallback is customer → billing entity → organization everywhere (§4) |
| Grace period, payment term, document locale with fallback | `app/models/customer.rb` | libs/billing | logic done | |
| Finalize zero-amount invoice: inherit, skip, finalize | `app/models/customer.rb` | libs/billing | logic done | |
| Invoice issuing date anchor and adjustment overrides | `app/models/customer.rb` | libs/billing | logic done | |
| Customer metadata (max 5, key 20 chars, value 100 chars, display on invoice) | `app/models/metadata/customer_metadata.rb` | libs/core, apps/server | done | Served on the customer; list filter `metadata.<key>` |
| Editability rules once subscribed or invoiced | `app/models/customer.rb` (`editable?`) | libs/core, libs/billing | done | Subscriptions, invoices, wallets or fixed-amount applied coupons make a customer non-editable |
| Customer list filters (search term, metadata, countries, states, zipcodes, currencies, customer and account type, billing entity, has tax id) | `app/queries/customers_query.rb` | libs/store, apps/server | done | Store queries in both stores with the ported specs in porttest; `ListCustomers` filters on `external_id`, `billing_entity_id` (IDs or `code:`, one that does not exist is not found), `customer_type`, `has_customer_type`, `account_type`, `currency`, `billing_address.country`/`state`/`zipcode`, `has_tax_identification_number`, `metadata.<key>`, `deleted` (the GraphQL `with_deleted`) and `active_subscriptions_count_from`/`_to` (the GraphQL arguments, `customers_query.rb:148-162`: equal bounds keep that count, two bounds the range inclusive, a lone bound is exclusive), with `search`, newest first by default |
| Customer deletion with relations terminated | `app/services/customers/destroy_service.rb`, `app/jobs/customers/terminate_relations_job.rb` | libs/billing, apps/server | done | `DeleteCustomer` archives the customer and queues `customers.terminate_relations`, which terminates its active and pending subscriptions, applied coupons and active wallets and queues the finalization of its drafts 5 minutes later. The job needs registering with the billing job dispatch (`customers.Service.HandleJob`). As in lago, a pending VIES check is not cancelled. Invoice search terms are computed from the customer at query time, so lago's `Customers::RefreshInvoicesSearchTermsJob` (`update_service.rb:158-160`) has nothing to refresh |
| Payment provider connection per customer (provider customer id, sync with provider) | `app/services/payment_provider_customers/create_or_update_batch_service.rb` | libs/billing, apps/server | in progress | `UpsertCustomerConnection`, `ListCustomerConnections` and `DeleteCustomerConnection` with a payment integration: one payment connection per customer (`only_one_connection_allowed`, as v1.55.0's batch service, `create_or_update_batch_service.rb:23-30`), always the default; deletion discards it. The manual connection code and the set-as-default mutation are not served; the provider customer is not created in the provider until the provider clients are wired |
| Integration customers (accounting, CRM, tax) batch create and update | `app/services/integration_customers/create_or_update_batch_service.rb` | libs/billing, apps/server | done | The same RPCs with an accounting, tax or CRM integration: several per customer, the first of a category is its default and `is_default` moves it (`set_as_default_service.rb`). Partner accounts are refused with `partner_account` (lago succeeds without a connection, `create_or_update_batch_service.rb:16`); an external customer ID is required unless syncing (lago skips the connection) |
| Checkout URL to collect a payment method | `app/services/customers/generate_checkout_url_service.rb` | libs/billing, apps/server | in progress | `GenerateCheckoutUrl` checks the payment connection (`no_linked_payment_provider`) and is Unimplemented until the provider clients are wired (`customers.WithCheckout`) |
| Customer portal URL | `app/services/customer_portal/generate_url_service.rb` | libs/auth, apps/server | done | `GeneratePortalUrl` returns the URL and its expiry (12 hours); `app.NewPortalTokens` derives the signing key from `TANVO_SECRET_KEY`; deleted customers get `customer_not_found`; without a secret key the RPC is Unimplemented |
| Locks to serialize per-customer operations (prepaid credit, payment method, credit note, coupon, billing schedule) | `app/services/customers/lock_service.rb` | libs/store | logic done | PostgreSQL advisory locks |
| Invoice custom sections selected or skipped per customer | `app/services/customers/manage_invoice_custom_sections_service.rb` | libs/billing, apps/server | done | Skipping clears the selection, a selection clears the skip, both together are `skip_sections_and_selected_ids_sent_together`; unknown sections are ignored as lago's `where` |
| Exclude from dunning and applied dunning campaign | `app/models/customer.rb` | libs/billing, apps/server | done | A campaign clears the exclusion and an exclusion the campaign; partners are always excluded; any change resets the attempts (`update_service.rb`) |
| x402 agent address | `app/models/customer.rb` (`x402_agent_address`) | libs/core | planned | See §1.36. Not in the v1.55.0 API params or serializer; `x402_agent_address` and `payment_method_type` on the customer are Unimplemented |

### 1.8 Billable Metrics

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Billable metric CRUD (name, code, description, field name) | `app/models/billable_metric.rb`, `app/services/billable_metrics/` | libs/billing, apps/server | done | `BillableMetricService`, the reference service of the RPC playbook. Paths take an ID or `code:<code>` (lago takes the code), so that IDs also work after a code change. Lists are newest first and also filter on `aggregation_type`, `recurring` and `plan_id` and search name and code (lago `BillableMetricsQuery`, exposed in GraphQL only), which the console needs. The weighted interval must be `seconds` on `weighted_sum_agg` metrics and is kept unchecked on the others, as in lago (`app/models/billable_metric.rb:59-61`). Validation failures are `InvalidArgument` (HTTP 400) rather than 422, and refused deletions are `FailedPrecondition` rather than 405, following the gRPC status model |
| Aggregation `count_agg` | `app/services/billable_metrics/aggregations/count_service.rb` | libs/metering | logic done | |
| Aggregation `sum_agg` | `app/services/billable_metrics/aggregations/sum_service.rb` | libs/metering | logic done | |
| Aggregation `max_agg` | `app/services/billable_metrics/aggregations/max_service.rb` | libs/metering | logic done | |
| Aggregation `unique_count_agg` with add and remove operations | `app/services/billable_metrics/aggregations/unique_count_service.rb` | libs/metering | logic done | |
| Aggregation `weighted_sum_agg` over a weighted interval | `app/services/billable_metrics/aggregations/weighted_sum_service.rb` | libs/metering | logic done | |
| Aggregation `latest_agg` | `app/services/billable_metrics/aggregations/latest_service.rb` | libs/metering | logic done | |
| Recurring metrics carried over billing periods (not allowed for count, max, latest) | `app/models/billable_metric.rb`, `app/services/billable_metrics/aggregations/` | libs/metering | logic done | |
| Prorated sum | `app/services/billable_metrics/prorated_aggregations/sum_service.rb` | libs/metering | logic done | |
| Prorated unique count | `app/services/billable_metrics/prorated_aggregations/unique_count_service.rb` | libs/metering | logic done | |
| Breakdown of prorated sum and unique count | `app/services/billable_metrics/breakdown/` | libs/metering | logic done | |
| Grouped aggregation by pricing group keys | `app/services/billable_metrics/aggregations/base_service.rb` | libs/metering | logic done | |
| Per-event aggregation for in-advance charges | `app/services/billable_metrics/aggregations/` (`per_event_aggregation`) | libs/metering | logic done | |
| Rounding of the aggregated value (round, floor, ceil with precision) | `app/services/billable_metrics/aggregations/apply_rounding_service.rb` | libs/metering | logic done | |
| Expression attached to a metric | `app/models/billable_metric.rb` (`expression`) | libs/expression | logic done | |
| Evaluate expression API (test an expression against a sample event) | `app/services/billable_metrics/evaluate_expression_service.rb` | libs/billing, apps/server | done | `POST /v1/billable_metrics:evaluate_expression`, which needs write access as in lago (`spec/requests/api/v1/billable_metrics_controller_spec.rb:307`). Numbers are rendered as Rails renders a BigDecimal (`2.0`); `event.timestamp` is the integer of seconds the Ruby binding passes as a number (`1710496800.0`). Property values are converted to text as Ruby's `to_s` does (`evaluate_expression_service.rb:26`): `nil` is empty, objects and arrays are `inspect`ed (`{"a" => 1}`) and floats follow `Float#to_s` (`1.0e+16`). JSON numbers reach the server as doubles, so integral ones below 10^16 are written as integers, and object keys are written in sorted order |
| Field locks once a metric is used by a plan | `app/services/billable_metrics/update_service.rb` | libs/billing | done | |
| Destroy metric: removes charges and alerts, refreshes drafts | `app/services/billable_metrics/destroy_service.rb` | libs/billing | done | The metric is discarded (`deleted_at`, `destroy_service.rb:33`): reads exclude it and its code is free again among live metrics (partial unique index). Events, and coupon and wallet targets are kept with it (§2). Its alerts are discarded in the same transaction, their thresholds kept (`destroy_service.rb:35`). Its charges and their charge filters are discarded in the same transaction; its metric filters are discarded by the `billing.destroy_metric_filters` job (`BillableMetricFilters::DestroyAllJob`, `destroy_service.rb:41`), and the ingest caches of the metric are expired. The draft refresh is flagged in that transaction. The deletion is refused while a product filter value selects one of its filters (`billable_metric referenced_by_product_filter`, `destroy_service.rb:20-23`) |
| Metric filters (key with allowed values) and batch update | `app/services/billable_metric_filters/create_or_update_batch_service.rb` | libs/billing | done | Removing a value trims dependent charge filters. Duplicate values are dropped as in lago (`create_or_update_batch_service.rb:42`); an empty value list is refused as in lago (`billable_metric_filter.rb:17`), and so is an empty value. The draft invoices are flagged for refresh in the same transaction (`create_or_update_batch_service.rb:67`), so the flag commits with the change it reflects. A replacement that removes a filter or a value selected by a product filter value is refused (`filters referenced_by_product_filter`, `create_or_update_batch_service.rb:20,32,85-96`); a value selecting the whole filter blocks only the filter's removal |

### 1.9 Expressions

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Decimal arithmetic (+, -, *, /) and parentheses | lago-expression `expression-core` | libs/expression | logic done | |
| Event fields: `event.timestamp`, `event.code`, `event.properties.<name>` | lago-expression `expression-core` | libs/expression | logic done | |
| String literals and `concat` | lago-expression `expression-core` | libs/expression | logic done | |
| `round`, `ceil`, `floor` with precision | lago-expression `expression-core` | libs/expression | logic done | |
| `least`, `greatest` | lago-expression `expression-core` | libs/expression | logic done | |
| Validation of an expression at metric creation | `app/services/billable_metrics/create_service.rb` | libs/expression | logic done | |
| Evaluation during ingestion to produce the aggregated field | `app/services/events/calculate_expression_service.rb` | libs/expression | logic done | Batch evaluation over WASM on wazero |
| Same evaluator in the browser for previews | lago-expression `expression-js` | libs/expression | planned | wasm-bindgen build |

### 1.10 Plans

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Plan CRUD (name, code, description, invoice display name, metadata) | `app/models/plan.rb`, `app/services/plans/` | libs/billing, apps/server | done | `PlanService` (Create, Get, List, Update, Delete) on `plans.Service`, with usage thresholds and the minimum commitment. Paths take an ID or `code:<code>`; lists hold parent plans, pending deletion included, newest first, and search name and code. Metadata is replaced as a whole. The REST counts (`active_subscriptions_count`, `draft_invoices_count`) are computed as lago's GraphQL does (`app/models/plan.rb:124-149`) rather than 0 (`app/serializers/v1/plan_serializer.rb`). No premium or progressive-billing gate (§19); the product-catalog legacy-field gate is dropped, as there is one catalog (`app/services/plans/create_service.rb:24-31`). `update_subscriptions_amount` is `Unimplemented`: v1.55.0 has no such parameter |
| Intervals weekly, monthly, quarterly, semiannual, yearly | `app/models/plan.rb` | libs/period | logic done | |
| Subscription fee amount and currency | `app/models/plan.rb` | libs/billing | logic done | |
| Subscription fee paid in advance or in arrears | `app/models/plan.rb` (`pay_in_advance`) | libs/billing | logic done | |
| Trial period in days | `app/models/plan.rb` | libs/period | logic done | |
| Bill charges monthly on yearly, semiannual and quarterly plans | `app/models/plan.rb` (`bill_charges_monthly`) | libs/period | logic done | |
| Bill fixed charges monthly | `app/models/plan.rb` (`bill_fixed_charges_monthly`) | libs/period | logic done | |
| Taxes applied to a plan | `app/services/plans/apply_taxes_service.rb` | libs/billing | done | `tax_ids` on the plan and on its minimum commitment, by ID or `code:<code>`; an unknown tax is not found |
| Yearly amount used to classify upgrades and downgrades | `app/models/plan.rb` (`yearly_amount_cents`) | libs/billing | logic done | |
| Plan overrides per subscription (child plans) | `app/services/plans/override_service.rb` | libs/billing | done | `plans.Service.Override` copies the plan without its taxes and metadata, overrides every charge and fixed charge, and creates the given usage thresholds and minimum commitment; it emits no webhook |
| Field locks once a plan has subscriptions | `app/services/plans/update_service.rb` | libs/billing | done | A change of code, interval, pay in advance, currency, trial period or bill monthly settings on a plan with subscriptions is refused with `value_is_immutable` rather than ignored (`app/services/plans/update_service.rb:39-49`) |
| Cascade plan changes to overridden child plans | `app/services/plans/update_service.rb` (`cascade_updates`), `app/jobs/plans/` | libs/billing | done | `cascade_updates` enqueues `billing.update_plan_amount` for each child plan still at the old amount, on the billing queue |
| Update the amount of existing subscriptions | `app/jobs/plans/update_amount_job.rb` | libs/billing | done | `plans.Service.UpdateAmount` skips a plan whose amount is no longer the expected one. An amount change cancels the pending downgrades that became upgrades and upgrades the pending subscriptions whose previous plan's yearly amount is not above the new one, with their activation rules |
| Plan deletion: prepare (pending deletion) then destroy with subscriptions terminated | `app/services/plans/prepare_destroy_service.rb`, `app/services/plans/destroy_service.rb` | libs/billing | logic done | `plans.Service` (PrepareDestroy, Destroy, the `billing.destroy_plan` job): every deleted plan is kept with the `deleted` status and its code is free again; entitlements are soft-deleted; pending subscriptions are canceled without a reason (domain §7.2). `PlanService.DeletePlan` runs PrepareDestroy and returns the plan pending deletion |
| Plan webhooks `plan.created`, `plan.updated`, `plan.deleted` | `app/services/webhooks/plans/` | libs/billing | done | Emitted in the transaction of the change; an update also flags the draft invoices of the plan's subscriptions for refresh |

### 1.11 Charges and Charge Models

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Charge model standard (unit price) | `app/services/charge_models/standard_service.rb` | libs/pricing | logic done | |
| Charge model graduated (tiers priced separately, flat fee per tier) | `app/services/charge_models/graduated_service.rb` | libs/pricing | logic done | |
| Charge model package (package size, free units) | `app/services/charge_models/package_service.rb` | libs/pricing | logic done | |
| Charge model percentage (rate, fixed amount per event, free units per events and per total, per-transaction min and max) | `app/services/charge_models/percentage_service.rb` | libs/pricing | logic done | |
| Charge model volume (whole volume priced at the reached tier) | `app/services/charge_models/volume_service.rb` | libs/pricing | logic done | |
| Charge model graduated percentage | `app/services/charge_models/graduated_percentage_service.rb` | libs/pricing | logic done | |
| Charge model dynamic (price from the event's `precise_total_amount_cents`) | `app/services/charge_models/dynamic_service.rb` | libs/pricing | logic done | |
| Grouped charges (one amount per pricing group) | `app/services/charge_models/grouped_service.rb` | libs/pricing | logic done | |
| Incremental in-advance computation per event | `app/services/charges/apply_pay_in_advance_charge_model_service.rb` | libs/pricing | logic done | |
| Projected amount at end of period | `app/services/charge_models/` (projection) | libs/pricing | logic done | |
| Amount details per tier and per unit for display | `app/services/charge_models/` (`amount_details`) | libs/pricing | logic done | |
| Tier range validation (adjacent ranges, last open) | `app/services/charges/validators/` | libs/pricing | logic done | One range convention; Lago's v1 inclusive +1 ranges not kept (§3) |
| Default properties per charge model | `app/services/charge_models/build_default_properties_service.rb` | libs/pricing | logic done | |
| Pay-in-advance charges | `app/models/charge.rb` (`pay_in_advance`) | libs/billing | logic done | |
| Invoiceable flag for in-advance charges (fees without invoices) | `app/models/charge.rb` (`invoiceable`) | libs/billing | logic done | |
| Regroup paid in-advance fees on the next invoice | `app/models/charge.rb` (`regroup_paid_fees`) | libs/billing | logic done | |
| Minimum amount per charge with true-up fee | `app/models/charge.rb` (`min_amount_cents`), `app/services/fees/create_true_up_service.rb` | libs/billing | logic done | |
| Prorated charges (recurring metrics) | `app/models/charge.rb` (`prorated`) | libs/pricing | logic done | |
| Charges that may target a wallet | `app/models/charge.rb` (`accepts_target_wallet`) | libs/billing | planned | |
| Charge code generation | `app/services/charges/generate_code_service.rb` | libs/billing | done | A plan create or update, and the copy of a new charge onto override plans, generate a missing code from the metric's code, suffixed one past the largest numeric suffix among the plan's original charges; the charges API requires a code as `charge.rb:50` does |
| Pricing group keys (aggregate per key) | `app/models/charge.rb` (`pricing_group_keys`) | libs/pricing | logic done | |
| Presentation group keys (at most two, display only) | `app/models/charge.rb` (`presentation_group_keys`) | libs/billing | done | Stored in the charge properties and cascaded to overrides even when their prices were customized; filter properties drop them, as `charge_filters/create_or_update_batch_service.rb` does |
| Charge invoice display name | `app/models/charge.rb` | libs/billing | logic done | |
| Taxes per charge | `app/services/charges/apply_taxes_service.rb` | libs/billing | done | `tax_ids` replace the charge's taxes; an unknown tax is not found. A charge override starts without taxes, as `charges/override_service.rb` duplicates none |
| Charges API nested under plans and subscriptions | `app/controllers/api/v1/plans/charges_controller.rb`, `app/controllers/api/v1/subscriptions/charges_controller.rb` | apps/server | done | `ChargeService` with `plan_id` or `subscription_id`; charges and plans are found by ID or code. Updates take a field mask: unset properties are kept unless the model changes. A subscription's update moves it to an override plan and updates its override of the charge (`subscriptions/update_or_override_charge_service.rb`); creating or deleting a subscription's charge is unimplemented, as v1.55.0 has only index, show and update there. `accepts_target_wallet` is not stored |

### 1.12 Charge Filters

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Charge filters with their own properties | `app/models/charge_filter.rb`, `app/services/charge_filters/` | libs/metering | logic done | |
| All-values wildcard (`__ALL_FILTER_VALUES__`) | `app/models/charge_filter_value.rb` | libs/metering | logic done | |
| Filter precedence (most specific filter wins) | `app/services/events/billing_period_filters/matching_and_ignored_service.rb` | libs/metering | logic done | |
| Event matching to filters, default bucket for unmatched events | `app/services/charge_filters/event_matching_service.rb` | libs/metering | logic done | |
| Minimizing ignored filters for billing-period queries | `app/services/charge_filters/` (`MinimizeIgnoredFilters`) | libs/metering | logic done | |
| Filter code (slug plus hash) | `app/models/charge_filter.rb` | libs/billing | done | A new filter takes the code of its values free on the charge; a kept filter keeps its code. The charge filter API is served by `ChargeService` on plans and subscriptions, including `GetChargeFilter` (`config/routes/plan_nested_api.rb:7`, `shared_api.rb:68`) |
| Cascade filter changes to child plans | `app/services/charge_filters/create_or_update_batch_service.rb` | libs/billing | done | Coded path only (§2): one job per created, updated or destroyed filter finds the overrides' copies by code; customized copies keep their prices and take only the pricing group keys (`charge_filters/cascade_service.rb`). The charge create and update cascades select the children with active or pending subscriptions and enqueue one batch job per 20 of them (`charges/create_children_job.rb`, `update_children_job.rb`); the update batch runs on the billing queue without the advisory lock |

### 1.13 Fixed Charges and Add-ons

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Add-on CRUD (amount, currency, invoice display name, taxes) | `app/models/add_on.rb`, `app/services/add_ons/` | libs/billing, apps/server | done | `AddOnService` over libs/billing/addons; taxes are `tax_ids` (IDs or `code:`). Deleting an add-on deletes it and its fixed charges; v1.55.0 discards both (`destroy_service.rb:17-18`). The list is searchable by name or code, as the GraphQL resolver is |
| Fixed charges on plans (add-on based, units) | `app/models/fixed_charge.rb`, `app/services/fixed_charges/` | libs/billing, apps/server | done | `plans.Service` create, get, list (parents, newest first), update and delete. Updates are partial: unset fields keep their values, where v1.55.0 resets units and properties from the params (`update_service.rb:31-38`). The API create requires a code; a plan create or update generates one from the add-on's code (`generate_code_service.rb`) |
| Fixed charge models standard, graduated, volume | `app/services/fixed_charges/` | libs/pricing | logic done | |
| Pay-in-advance fixed charges | `app/services/invoices/create_pay_in_advance_fixed_charges_service.rb` | libs/billing | done | `invoicing.Service.InAdvanceFixedCharges` bills the units applied immediately at one instant for an active or gated subscription, on an invoice created even without fees; a gated subscription's invoice stays open and goes to payment without invoice.created or document, and one of zero amount satisfies the payment rule and activates the subscription (`lifecycle.Service.ResolveSubscriptionStatus`) |
| Prorated fixed charges | `app/models/fixed_charge.rb` (`prorated`) | libs/period | logic done | |
| Units overrides per subscription | `app/services/fixed_charges/override_service.rb`, `app/services/subscriptions/fixed_charge_units_overrides/write_service.rb` | libs/billing, libs/store | done | A units-only change on a subscription of an original plan is kept as a `fixed_charge_units_overrides` row; any other change moves the subscription to an override plan, promoting its units overrides (`update_or_override_fixed_charge_service.rb`) |
| Apply unit changes immediately or at next period | `app/models/fixed_charge.rb` (`apply_units_immediately`) | libs/billing | logic done | |
| Fixed charge events recording unit changes over time | `app/models/fixed_charge_event.rb`, `app/services/fixed_charges/emit_events_service.rb` | libs/billing | done | Events carry the instant of the change and whether it applies immediately, from which the fees derive the effective time, where v1.55.0 stores the deferred timestamp (`emit_events_service.rb`). Subscription creation's `Subscriptions::EmitFixedChargeEventsService` is §1.15's |
| Cascade fixed charge changes to child plans | `app/jobs/fixed_charges/` | libs/billing | done | Jobs create, update and destroy the copies on live child plans, in batches of 20. A child plan that already has (or lacks) the copy is skipped, where v1.55.0 fails `not_found` (`cascade_child_plan_update_service.rb:28`) |
| Taxes per fixed charge | `app/services/fixed_charges/` | libs/tax | done | `tax_ids` (IDs or `code:`); overrides start without the parent's taxes, as v1.55.0 (`override_service.rb`) |
| Fixed charges API nested under plans and subscriptions | `app/controllers/api/v1/plans/fixed_charges_controller.rb`, `app/controllers/api/v1/subscriptions/fixed_charges_controller.rb` | apps/server | done | `FixedChargeService`; a subscription's list and get show its own units. Subscription-scope create and delete are Unimplemented (the subscription controller has index, show and update). No premium or legacy-billing gate (`Api::ForbidsLegacyBilling`); delete returns an empty response |

### 1.14 Product Catalog (v2)

Lago carries two catalogs: the plan/charge catalog above and a newer product catalog behind the `product_catalog` flag. Tanvo implements one catalog whose model covers both (§3): every row here and in §1.10–§1.13 is in scope.

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Product categories | `app/models/product_category.rb` | libs/core, libs/billing/products, apps/server | done | `ProductService` category RPCs. Deletion is refused while attached and discards (`deleted_at`) the category with its products, their filters and values, rate cards and rates, as `product_categories/destroy_service.rb` does; codes are unique among kept rows and lookups by ID still return a discarded row. `products_count` is returned |
| Products (metered, fixed) | `app/models/product.rb` | libs/core, libs/billing/products, apps/server | done | A metered product sells a metric, a fixed one none; the type is immutable. Code and category are locked once attached; deletion discards it with its filters, their values, its rate cards and their rates. `filters_count` is returned |
| Product filters and filter values | `app/models/product_filter.rb` | libs/core, libs/billing/products, apps/server | done | Values select a metric filter key and one value, or the whole filter (no value); listed oldest first with lago's name-or-code search. A metric filter or value a product filter selects cannot be removed (§1.8). Changed values discard the kept ones and insert the new ones; deletion discards the filter with its values and the rate cards scoped to it and their rates. Values are locked once a contract prices the filter (`attached_to_subscriptions`), which never holds until contracts exist (§1.22) |
| Rate cards (billing timing, proration, display on invoice, regroup, pricing unit, locks) | `app/models/rate_card.rb` | libs/core, libs/billing/products, apps/server | done | `RateCardService`. Lists filter by product, product filter, category and uncategorized; `rates_count` and `active_rate` are returned, as in `v2/rate_card_serializer.rb`. Deletion discards the card with its rates. Rates other than pending ones are frozen once a contract applies the card, which never holds until contracts exist (§1.22) |
| Rate card rates on a timeline (`effective_from`) | `app/models/rate_card_rate.rb`, `app/services/rate_card_rates/create_service.rb` | libs/core, libs/billing/products, apps/server | done | Append-only after the active rate; `effective_from` is not before today in the organization time zone and is midnight UTC on an arrears card. Each rate carries its `RateStatus` (pending, active, terminated: `rate_card_rate.rb:76-89`); `ListRates` is paginated. Only pending rates can be deleted; deletion discards the rate |
| Rate models and their compatibility with product kinds | `app/services/rate_card_rates/` (`ModelCompatibility`) | libs/core, libs/billing/products | done | |
| Billing interval count and unit per rate | `app/models/rate_card_rate.rb` | libs/core, libs/billing/products | done | |
| Range normalization | `app/services/rate_card_rates/` (`NormalizeRanges`) | libs/billing/products | done | Bounds are exact decimals, so bounds lago compares as floats are accepted; range errors are reported under `properties` |
| Prorated graduated rates filled per event (decimal tier bounds; removals empty the top tiers first) | `app/services/charge_models/prorated_adjacent_graduated_service.rb` | libs/pricing | done | |
| Catalog plans and plan rate cards | `app/models/catalog_plan.rb`, `app/models/plan_rate_card.rb` | libs/core, libs/billing/products, apps/server | done | `PlanRateCardService`. Lago's catalog plan is Tanvo's plan (§3); a plan rate card is lago's applied rate card. A plan a contract uses is locked (`plan_locked`, `CatalogPlan#attached_to_contracts?`); contracts come with §1.22, so until then no plan is locked. Deletion discards the plan rate card with its phases; deleting a plan discards its plan rate cards (`catalog_plans/destroy_service.rb`). The plan rate card carries its `rate_phases` (GraphQL `PlanAppliedRateCard`) and `rate_phases_count` |
| Rate phases (ramps) and their invariants | `app/models/rate_phase.rb` | libs/billing/products, apps/server | done | Phase RPCs on a plan rate card, and nested `rate_phases` replaced by position on create and update; nested errors are reported under `rate_phases`. A replaced ramp discards the old phases; a single phase is created, moved or deleted in place, and deletion discards it and renumbers the later ones |
| Rate overrides | `app/models/rate_override.rb` | libs/billing/products, apps/server | done | Part of their phase; errors are reported under `rate_override`. Structural fields cannot be sent, so `not_overridable` never occurs |
| Catalog code format | `app/models/concerns/` (`CatalogCodeFormat`) | libs/core | done | |
| Catalog API with cursor pagination | `app/controllers/api/v2/` | apps/server | done | Cursor pagination everywhere; no beta header. Delete RPCs return the deleted record. Activity logs are not recorded for catalog objects |

### 1.15 Pricing Units

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Pricing units (name, code, short name of at most 3 characters, description) | `app/models/pricing_unit.rb`, `app/services/pricing_units/` | libs/billing, apps/server | done | Public API, not only the admin UI: `PricingUnitService` over libs/billing/pricingunits, without the premium check of v1.55.0 ([architecture §14](architecture.md#14-enterprise-edition-ee)). The code is immutable and there is no delete, as in v1.55.0; the list searches name and code (`search`, lago `search_term` in `pricing_units_resolver.rb`) |
| Charges priced in a pricing unit with a conversion rate | `app/models/applied_pricing_unit.rb` | libs/pricing | logic done | |
| Pricing unit usage recorded on fees | `app/models/pricing_unit_usage.rb` | libs/billing | logic done | |

### 1.16 Entitlements

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Features (code, name, description) | `app/models/entitlement/feature.rb`, `app/services/entitlement/` | libs/core, libs/billing, apps/server | done | `entitlements.Service` and `FeatureService`. The repositories soft-delete features (`deleted_at`) as `feature_destroy_service.rb:22-27` does. Lists are newest first and search code, name and description (`features_query.rb:18-30`). Update merges the given privileges by code (`partial: true`, `features_controller.rb:65-69`) |
| Privileges of type integer, string, boolean, select | `app/models/entitlement/privilege.rb` | libs/core, libs/billing | done | The repositories soft-delete privileges (`deleted_at`) with the feature, or when a feature update no longer lists them (`feature_update_service.rb:99-111`). Select options are `select_options` (lago `config`); a value type cannot change. `DELETE /features/:code/privileges/:code` (`features/privileges_controller.rb`) is `FeatureService.DeleteFeaturePrivilege` |
| Plan entitlements and privilege values | `app/services/entitlement/plan_entitlements_update_service.rb` | libs/core, libs/billing | done | Values are strings validated by type; booleans are stored as `true`/`false`. Errors: `<code>_privilege_value` `value_is_invalid` or `value_not_in_select_options`, as in lago |
| Subscription entitlement overrides and removals | `app/services/entitlement/subscription_entitlements_update_service.rb` | libs/core, libs/billing | done | Overrides on a child plan's subscription resolve against the parent plan, as in lago |
| Resolved entitlements of a subscription | `app/services/entitlement/` | libs/core, libs/billing | done | Each value carries a `source` (plan or subscription) in place of lago's `plan_value` and `override_value` |
| Feature webhooks `feature.created`, `feature.updated`, `feature.deleted` | `app/services/webhooks/features/` | libs/billing, libs/webhooks | done | Feature updates and deletes also send `plan.updated` for each plan that grants the feature, and subscription entitlement changes `subscription.updated`, as in lago |
| Features and entitlements API | `app/controllers/api/v1/features_controller.rb`, `app/controllers/api/v1/plans/entitlements_controller.rb`, `app/controllers/api/v1/subscriptions/entitlements_controller.rb` | apps/server | done | `FeatureService` and `EntitlementService`. Plan entitlements are replaced with `PUT` (lago `POST`) and upserted one feature at a time (lago `PATCH` takes a map of features). Subscriptions are looked up by ID (any status) or `external_id:` (active); lago's `subscription_status` filter is not exposed. Subscription entitlements have no single read and no replace, as in lago (`GetEntitlement` and `ReplaceEntitlements` are Unimplemented for them). Delete returns no body and a privilege delete returns the one entitlement (lago returns the collection) |

### 1.17 Event Ingestion

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Single event ingestion | `app/services/events/create_service.rb` | libs/ingest | done | `EventService.IngestEvent`, `POST /v1/events`; end-to-end test in apps/server |
| Batch ingestion (up to 100 events) | `app/services/events/create_batch_service.rb` | libs/ingest | logic done | Served by `EventService.IngestEvents`; no end-to-end test yet |
| Event timestamp parsing (seconds, fractional seconds, defaults to now) | `app/services/events/common_factory.rb` | libs/ingest | logic done | |
| Event fields: transaction id, external subscription id, code, properties, precise total amount | `app/models/event.rb` | libs/ingest | logic done | JSON property numbers are kept exact, decimals included (lago's JSON parsing keeps integers exact and turns decimals into floats); binary protobuf and gRPC clients send doubles, and strings for exact values ([Architecture §4.2](architecture.md#42-rest-through-vanguard)) |
| `external_contract_id` alias of `external_subscription_id` on ingestion and event filters | `app/controllers/api/v1/events_controller.rb` | libs/ingest | logic done | The explicit `external_subscription_id` wins when both are sent |
| Deduplication by organization, external subscription id and transaction id | `app/services/events/create_service.rb`, `app/services/events/validate_creation_service.rb`, `db/structure.sql` (`index_unique_transaction_id`) | libs/ingest, libs/store, libs/eventstore | logic done | One key in every store; Lago's ClickHouse store deduplicates at query time and also keys on code and timestamp, where Tanvo treats a re-send at another timestamp as a duplicate |
| Request metadata on events (user agent, IP) | `app/services/events/create_service.rb` | libs/ingest | done | Stored in `public.events.metadata` on the PostgreSQL path and in ClickHouse `events_raw` on the Kafka path, carried by the raw and enriched messages; lago's Kafka payload leaves it out (`app/services/events/kafka_producer_service.rb:36`) and its ClickHouse events have none (`app/models/clickhouse/events_raw.rb:20`). Returned as the event's `user_agent` and `ip_address` (lago exposes them in GraphQL only, `app/graphql/types/events/object.rb:20`); the address is resolved like `request.remote_ip` (`app/controllers/api/v1/events_controller.rb:219`) through ActionDispatch::RemoteIp's default trusted proxies plus `TANVO_TRUSTED_PROXIES`, except that the forwarding headers are read only from a trusted peer and a `Client-Ip` that contradicts `X-Forwarded-For` is ignored instead of failing the request (actionpack `remote_ip.rb:151`) |
| Event validation (metric code, subscription, numeric field) | `app/services/events/validate_creation_service.rb` | libs/ingest | in progress | |
| Post-validation of ingested events with `events.errors` webhook | `app/services/events/post_validation_service.rb` | libs/billing, libs/store | done | `billing.Engine.ValidateEvents`, run hourly at :05 for each organization with webhook endpoints and events created in the previous clock hour; one `events.errors` webhook (object type `events_errors`) lists the transaction IDs with an invalid code, a missing or non-numeric aggregation property, or invalid filter values, by the rules of `db/views/last_hour_events_mv_v04.sql` (an event whose code names only discarded metrics is skipped). A direct query on `events (organization_id, created_at)` replaces the materialized view; `missing_group_key`, never filled in v1.55.0, is left out. Like lago, which validates only the events it stores in PostgreSQL (`app/services/events/create_service.rb:37`), events of the Kafka pipeline are not post-validated. `TANVO_DISABLE_EVENTS_VALIDATION` (lago `LAGO_DISABLE_EVENTS_VALIDATION`) unschedules it. Ingestion still rejects what it can check synchronously |
| Retrieve an event and list events | `app/controllers/api/v1/events_controller.rb`, `app/queries/events_query.rb` | libs/ingest, apps/server | done | `EventService.GetEvent` (`GET /v1/events/{transaction_id}`) and `ListEvents` (`GET /v1/events`); the filter restricts `code`, `external_subscription_id` (=) and `timestamp` (>=, <=), and `timestamp_from_started_at` starts at the latest subscription's start. `GetEvent` also takes an `external_subscription_id`, beyond lago, to pick among events sharing a transaction ID; without it the first ingested is returned (`events_controller.rb:55`). Ties of the PostgreSQL list break on the event ID, lago on the transaction ID (`events_query.rb:22`); on ClickHouse the list returns the current copy of each event (lago reads `events_raw` without deduplication, `events_query.rb:24,50`), and ClickHouse events have no ID |
| List enriched events | `app/controllers/api/v1/events_controller.rb` (`events_enriched`) | apps/server | planned | |
| Fee estimate for an in-advance event | `app/services/fees/estimate_pay_in_advance_service.rb` | libs/billing, apps/server | done | `EventService.EstimateFees` (`POST /v1/events:estimate_fees`), in libs/billing/estimates. A missing `external_subscription_id` is rejected by request validation (400), where lago answers 404 (`estimate_pay_in_advance_service.rb:16`). The response is the estimated fee (charge, filter, amount, taxes, units, unit amount, events count, grouping), not lago's full fee serializer with applied taxes (`events_controller.rb:160`); a tax provider failure fails the estimate |
| Instant fee estimate and batch instant estimate | `app/services/fees/estimate_instant/` | libs/billing, apps/server | done | `EventService.EstimateInstantFees` (`POST /v1/events:estimate_instant_fees`) serves both lago endpoints, as a batch. The unit amount is in currency units, where lago's `precise_unit_amount` is in cents (`estimate_instant/base_service.rb:39,78`); a units value that is not a number is a validation error, where lago raises (`base_service.rb:28`); expression failures are ignored as in lago |
| Enrichment: subscription, metric, expression, aggregated value, grouping keys, precise amount; charge lookup only to route in-advance events | `app/services/events/enrich_service.rb`, events-processor `processors/` | libs/ingest | logic done | No charge or filter on the enriched row; filters are matched from the raw properties at query or bucket time |
| Enriched events with operation type (add/remove) | `app/models/enriched_event.rb` | libs/ingest | logic done | |
| Re-enrichment after catalog changes | `app/services/events/re_enrich_all_service.rb` | libs/ingest | planned | |
| Post-processing: usage monitoring, wallet refresh, target wallets, in-advance charges | `app/services/events/post_process_service.rb` | libs/ingest | in progress | Usage monitoring, wallet refresh and in-advance charges wired in both modes: in the ingestion transaction on PostgreSQL; on Kafka the write consumer enqueues the in-advance jobs of the events it stored, and a `usage.refresh` consumer passes the signals to billing. Lago bills in-advance events from a separate topic 15 s later (`app/consumers/events_charged_in_advance_consumer.rb`, `app/services/events/stores/clickhouse_store.rb:11`) |
| Events targeting a specific wallet | `app/services/events/` (`target_wallet_code`) | libs/billing | planned | |
| In-advance charge fee creation from an event with source dedup | `app/services/fees/create_pay_in_advance_service.rb`, `app/services/events/pay_in_advance_service.rb` | libs/billing | logic done | |
| Kafka ingestion mode with consumers | events-processor `processors/` | libs/ingest | logic done | Built into the `consumer` role: enrichment and ClickHouse writer groups wired; no end-to-end test yet |
| Dead-letter topic for unprocessable events | events-processor `processors/dlq` | libs/ingest | logic done | |
| Source connectors for HTTP, SQS, Kinesis | events-processor connectors | libs/ingest | planned | Kafka also supported |
| Catalog cache in consumers | events-processor (Badger cache, Debezium) | libs/ingest, apps/server | done | Change notifications instead of CDC (§3). Every committed change to a metric, a charge or a subscription sends `pg_notify('tanvo_catalog_changes', …)` inside the writing transaction, so it is delivered on commit only; each API and consumer process LISTENs and evicts the organization's cached entries, and purges the whole cache on every (re)connect since notifications may have been missed; the writing process also evicts at once after commit. The TTL stays as a fallback. All ingestion modes read the catalog from PostgreSQL, so ClickHouse and Kafka need nothing more. Lago's Segment tracking of metrics is not ported (no product analytics in the core) |

### 1.18 Event Stores

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| PostgreSQL event store (events and enriched events partitioned) | `app/services/events/stores/postgres_store.rb` | libs/eventstore | logic done | Native partitions (§3) |
| ClickHouse event store with FINAL deduplication and retries | `app/services/events/stores/clickhouse_store.rb` | libs/eventstore | logic done | |
| Store queries: count, sum, max, latest, unique count, weighted sum, grouped, prorated, breakdown, last event, distinct values | `app/services/events/stores/base_store.rb` | libs/eventstore | logic done | |
| Billing-period filters on store queries | `app/services/events/stores/` | libs/eventstore | logic done | |
| ClickHouse raw, enriched and dead-letter tables | `db/clickhouse_migrate/` | libs/eventstore | planned | |
| Pre-aggregated usage buckets (15 minutes in Lago) | `app/models/clickhouse/usage_bucket.rb`, `app/services/events/stores/usage_bucket_store.rb` | libs/eventstore | logic done | 15-minute buckets keyed by dimension values, maintained in the event write on PostgreSQL and ClickHouse; exact window edges read from the events; undeclared filter values folded; in-advance, prorated, recurring and target-wallet charges read the events; `RepairBuckets` runs every 10 minutes from the start of the previous run, kept in ClickHouse; `BucketStats` is reported hourly (histograms and a warning past 10 000 rows per bucket); dropping an oversized definition is not automated yet |
| Cached aggregations for recurring metrics | `app/models/cached_aggregation.rb` | libs/metering | done | Stored by invoicing (a new row per pay-in-advance event, at most one per charge, group and instant for recurring states) and read by fees and current usage (domain §10.3). Lookups filter by charge filter only when one is set and, while an event is priced, drop states without an event transaction (`app/services/billable_metrics/aggregations/base_service.rb:223-224`); aggregation values are nullable. No `current_amount` (custom aggregations are not ported) |

### 1.19 Usage

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Current usage of a subscription | `app/services/invoices/customer_usage_service.rb` | libs/billing | logic done | |
| Projected usage to the end of the period | `app/services/invoices/customer_usage_service.rb` (`with_projection`), `app/services/fees/projection_service.rb` | libs/billing | logic done | From each current-usage fee: standard, graduated, package and volume reprice the projected units, other models scale the amount; ratio over the charges period ([Domain §10.3](domain.md#103-usage)) |
| Past usage per period | `app/queries/past_usage_query.rb` | libs/billing | logic done | |
| Usage filters (by metric, by filter, full usage) | `app/services/invoices/customer_usage_service.rb` | libs/billing | logic done | |
| Charge-level usage cache | `app/services/subscriptions/charge_cache_service.rb` | libs/billing | planned | Lazy validation: entries carry the latest ingested event and are recomputed when a newer one exists, never deleted on ingest; full usage has its own key |
| Realtime usage refresh | `app/services/subscriptions/` (`realtime_usage`) | libs/billing | planned | Usage buckets make it the default |
| Hourly usage breakdown | `app/graphql/resolvers/subscriptions/hourly_usage_resolver.rb` | libs/metering | logic done | Series built from the charge filters (`HourlySeriesFor`), other values under `__other__`; the window starts no earlier than the subscription |
| Lifetime usage (historical, invoiced, current) | `app/models/lifetime_usage.rb`, `app/services/lifetime_usages/` | libs/billing | logic done | |
| External historical usage amount | `app/controllers/api/v1/subscriptions/lifetime_usages_controller.rb` | libs/billing | logic done | |
| Customer usage API: current, projected and past usage | `app/controllers/api/v1/customers/usage_controller.rb`, `app/controllers/api/v1/customers/projected_usage_controller.rb` | apps/server | done | `UsageService.GetCurrentUsage` (`include_projection` in place of the projected route, not gated by `projected_usage_enabled`, `projected_usage_controller.rb:7`) and `ListPastUsage`, over libs/billing/subscriptions. Grouped usage sits under each filter (lago nests filters under groups, `app/serializers/v1/customers/charge_usage_serializer.rb`), and filter values are joined with commas. `filter_by` and `group_by_attributions` are Unimplemented (`app/controllers/concerns/api/v1/customers/usage_filters.rb`, `shared_api.rb:25`); lago's `full_usage`, `charge_id`/`charge_code`, `skip_grouping` and `filter_by_presentation` parameters and the presentation breakdowns are not exposed. `GetHourlyUsage` is Unimplemented (GraphQL only in v1.55.0, `app/graphql/types/query_type.rb:130`) and so is `ListDailyUsage` (no daily usage API in v1.55.0) |
| Lifetime usage API | `app/controllers/api/v1/subscriptions/lifetime_usages_controller.rb`, `app/serializers/v1/lifetime_usage_serializer.rb` | apps/server | done | `SubscriptionService.GetLifetimeUsage` and `UpdateLifetimeUsage`; an `external_id:` reference selects the active subscription. The amount is a decimal in major units, refused when finer than the currency's minor unit; thresholds give `remaining_amount` (lago `completion_ratio`) and `refreshed_at` replaces `from_datetime`/`to_datetime`. The thresholds are empty when progressive billing is disabled on the subscription (lago `has_progressive_billing?` ignores the flag). `include_metrics` has no counterpart and is ignored |
| Lifetime usage refresh | `app/jobs/clock/refresh_lifetime_usages_job.rb` | libs/billing | done | Like lago, the sweep takes the records whose invoiced usage is flagged (`app/jobs/clock/refresh_lifetime_usages_job.rb:9-19`); the current usage is refreshed by the usage signals |
| Granular lifetime usage per metric | `app/services/lifetime_usages/` | libs/billing | planned | |
| Daily usages with diff and history fill | `app/models/daily_usage.rb`, `app/services/daily_usages/` | libs/billing | done | Hourly sweep over the organizations entering a new day in their time zone, spread over `TANVO_DAILY_USAGE_SCHEDULING_JITTER` (30 minutes by default); one computation per subscription and local day (`app/services/daily_usages/compute_all_service.rb:142-148`); filled from invoices with the subscription's own fees only (`app/services/daily_usages/fill_from_invoice_service.rb:61-62`); the history rebuild (`lib/tasks/daily_usages.rake`) is the `billing.rebuild_daily_usage_history` job, which optionally deletes the stored rows in batches of 10,000 before enqueuing the fills; snapshots name discarded metrics and charge filters as lago does (`app/models/fee.rb:18,33`); no premium gate |
| Subscription activity processing (which subscriptions changed) | `app/services/usage_monitoring/process_subscription_activity_service.rb` | libs/billing | done | A delayed job per subscription and minute instead of the `subscription_activities` table: a signal that arrives during a run lands in the next minute's job, where lago's row, deleted only after processing, absorbs it (`app/services/usage_monitoring/track_subscription_activity_service.rb:23-26`); a failure is retried at once, four attempts in all (`app/jobs/usage_monitoring/process_subscription_activity_job.rb`) |

### 1.20 Subscriptions

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Statuses pending, active, terminated, canceled, incomplete | `app/models/subscription.rb` | libs/core | planned | |
| Billing time calendar or anniversary | `app/models/subscription.rb` | libs/period | logic done | |
| Subscription start date (past or future) and ending date | `app/services/subscriptions/create_service.rb` | libs/billing | logic done | |
| Subscription name and external id | `app/models/subscription.rb` | libs/core | planned | |
| Plan overrides at subscription creation | `app/services/subscriptions/create_service.rb`, `app/services/plans/override_service.rb` | libs/billing, apps/server | done | `plan_overrides` on create, update and plan change. On create, a units-only override of a new subscription writes units overrides instead of a child plan (`create_service.rb:171-174, 284-294`), and a current subscription on the same plan keeps its plan. On update, a child plan is updated in place: its currency and trial are left as they are (`app/services/plans/update_service.rb:40-49`), every fixed charge is kept with the given ones overlaid, a fixed charge key that is not the child's is not found (`app/services/subscriptions/update_service.rb:260-288`), and the given charges replace the child's, an unknown key creating a charge and the ones left out removed (`plans/update_service.rb:245-276`). A parent plan is overridden, with the subscription's units overrides folded into the child plan. A plan change with overrides overrides the requested plan, whose amount decides between upgrade and downgrade (`create_service.rb:136-148`); a subscription starting in the future moves to the requested plan itself (`plan_upgrade_service.rb:77-80`, `plan_downgrade_service.rb:82-84`). No premium or legacy-billing gate (`create_service.rb:37-41`). Activation records each fixed charge's units in force, the subscription's own units included, one second after the start, except from incomplete (`app/services/subscriptions/activate_service.rb:38,74,104,130,174-179`, `emit_fixed_charge_events_service.rb`); so does a back-dated creation (`create_service.rb:201-207`) |
| Per-subscription charges, filters and fixed charges endpoints | `app/controllers/api/v1/subscriptions/` | apps/server | done | ChargeService and FixedChargeService on `subscription_id`: list, get and update (overriding the plan on first change), filter CRUD; create and delete have no v1.55.0 endpoint and return `unimplemented` |
| Upgrade and downgrade by yearly amount (upgrade immediate, downgrade at period end) | `app/services/subscriptions/plan_upgrade_service.rb` | libs/billing | logic done | |
| Termination with credit note choice (credit, skip, refund, offset) | `app/services/subscriptions/terminate_service.rb` | libs/billing | logic done | The in-advance base fee counts as invoiced only through rows that bill it (subscription starting, periodic, terminating); lago matches any row on the period's boundaries, in-advance charge and progressive billing rows included (`app/services/subscriptions/terminate_service.rb:183`) |
| Termination invoice choice (generate, skip) | `app/services/subscriptions/terminate_service.rb` | libs/billing | logic done | An event at exactly `terminated_at` belongs to no period of the terminated subscription (domain §10.2); lago bills it (`app/models/event.rb:18`) |
| Refuse termination while taxes are pending | `app/services/subscriptions/terminate_service.rb` | libs/billing | logic done | |
| Cancellation of pending subscriptions | `app/services/subscriptions/terminate_service.rb` | libs/billing | logic done | No cancellation reason; the canceled subscription emits `subscription.terminated`, and a canceled next subscription emits nothing (domain §9.2) |
| Cancellation reasons payment failed, timeout, manual | `app/models/subscription.rb` | libs/core | planned | |
| Payment method per subscription | `app/models/subscription.rb` | libs/billing | logic done | |
| Consolidate invoices across subscriptions | `app/models/subscription.rb` | libs/billing | logic done | |
| Progressive billing disabled per subscription | `app/models/subscription.rb` | libs/billing | logic done | |
| Purchase order number | `app/models/subscription.rb` | libs/core | planned | |
| Billing entity override per subscription | `app/models/subscription.rb` | libs/core | planned | |
| Skip daily usage computation | `app/models/subscription.rb` | libs/billing | done | |
| Activation rules: require payment, with timeout | `app/models/subscription/activation_rule.rb`, `app/services/subscriptions/activation_rules/` | libs/billing | logic done | A payment status of succeeded or failed on the open gating invoice resolves the rule (`Invoices::UpdateService#handle_payment_gated_activation`, `payment/resolve_service.rb`) in the `billing.resolve_payment_gate` job: success finalizes the invoice (`invoice.created`, document) and activates the subscription once every rule is fulfilled; failure closes the invoice and cancels the subscription with `payment_failed`. Reached from manual payments; provider payment updates reach it once the providers exist |
| Activation rule statuses (inactive, pending, satisfied, not applicable, declined, failed, expired) | `app/models/subscription/activation_rule.rb` | libs/core | planned | |
| Activation gating invoice paid before activation | `app/services/subscriptions/activate_service.rb` | libs/billing | logic done | |
| Payment pre-authorization at subscription creation (Stripe) | `app/services/payment_providers/stripe/payments/authorize_service.rb` | providers/stripe | planned | |
| Activation of pending subscriptions at start date | `app/jobs/clock/activate_subscriptions_job.rb` | libs/billing | logic done | Scheduled per subscription (§3) |
| Termination at ending date | `app/jobs/clock/terminate_ended_subscriptions_job.rb` | libs/billing | logic done | |
| Expiry of incomplete subscriptions | `app/jobs/clock/expire_incomplete_subscriptions_job.rb` | libs/billing | done | Expires when the activation rule's expiry is at or before the instant |
| Termination alerts before ending date (default 15 and 45 days) | `app/jobs/clock/subscriptions_to_be_terminated_job.rb` | libs/billing | done | Days set by `TANVO_SUBSCRIPTION_TERMINATION_ALERT_DAYS`; at most one alert per subscription and UTC day |
| Trials across upgrades (initial start date kept) | `app/models/subscription.rb` (`trial_end_date`) | libs/period | logic done | |
| Trial end detection and free-trial billing | `app/jobs/clock/free_trial_subscriptions_biller_job.rb` | libs/billing | logic done | |
| Periodic billing selection (which subscriptions bill today) | `app/services/subscriptions/organization_billing_service.rb` | libs/billing | logic done | Per-subscription scheduled jobs plus reconciliation (§3) |
| Grouping of subscriptions billed together on one invoice | `app/services/subscriptions/organization_billing_service.rb` | libs/billing | logic done | |
| Subscription list filters (status, plan, customer, overridden) | `app/queries/subscriptions_query.rb` | libs/store | logic done | Store queries in both stores with the ported specs in porttest; plan filters leave out deleted plans; the recent order is subscription_at, then created_at (both descending), then id ascending; served by `ListSubscriptions` |
| Subscriptions API | `app/controllers/api/v1/subscriptions_controller.rb`, `app/serializers/v1/subscription_serializer.rb` | apps/server | done | `SubscriptionService` over libs/billing/subscriptions: create (a customer referenced by `external_id:` is created with the default billing entity), get, list, update, change plan, terminate, cancel. A plan change on an existing external ID is its own RPC (lago reuses create, `create_service.rb:59`). List defaults to active subscriptions (`app/controllers/concerns/subscription_index.rb:11`); `GetSubscription` by `external_id:` takes the active, else incomplete, else pending one, without lago's `status` parameter. Terminating by `external_id:` takes the active subscription, `CancelSubscription` the pending, else incomplete one (lago `terminate` with `status=pending`, `subscriptions_controller.rb:151`). On-termination options are accepted on create (lago `create_params` does not permit them). Moving `subscription_at` to the past emits no in-advance fixed charge events (lago `EmitFixedChargeEventsService`). Plan overrides and usage thresholds are supported (§1.20, §1.31). Rate cards and the billing anchor date are Unimplemented (v2 contracts, `config/routes.rb:44-53`, §1.22), and so is `ListBillingSegments` (no route in v1.55.0). The connection and Stripe pre-authorization (`subscriptions_controller.rb:23-71`) parameters are not exposed |
| Subscription webhooks `subscription.started`, `updated`, `terminated`, `canceled`, `incomplete`, `termination_alert`, `trial_ended`, `usage_threshold_reached` | `app/services/webhooks/subscriptions/` | libs/webhooks | planned | |

### 1.21 Billing Periods and Dates

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Weekly period boundaries, calendar and anniversary | `app/services/subscriptions/dates/weekly_service.rb` | libs/period | logic done | |
| Monthly period boundaries, calendar and anniversary (end-of-month anniversaries) | `app/services/subscriptions/dates/monthly_service.rb` | libs/period | logic done | |
| Quarterly period boundaries | `app/services/subscriptions/dates/quarterly_service.rb` | libs/period | logic done | |
| Semiannual period boundaries | `app/services/subscriptions/dates/semiannual_service.rb` | libs/period | logic done | |
| Yearly period boundaries | `app/services/subscriptions/dates/yearly_service.rb` | libs/period | logic done | |
| Charge period boundaries, including monthly charges on longer plans | `app/services/subscriptions/dates_service.rb` (`charges_from_datetime`, `charges_to_datetime`) | libs/period | logic done | Contiguous half-open periods (§4) |
| Fixed charge period boundaries | `app/services/subscriptions/dates_service.rb` | libs/period | logic done | |
| Time zone application to boundaries | `app/services/subscriptions/dates_service.rb` | libs/period | logic done | IANA zones, explicit `*time.Location` |
| Single-day price and number of days in period | `app/services/subscriptions/dates_service.rb` | libs/period | logic done | |
| Termination-day boundaries | `app/services/subscriptions/dates_service.rb` | libs/period | logic done | |
| Subscription fee proration (first period, termination, upgrade, full period) | `app/services/fees/subscription_service.rb` | libs/period | logic done | |
| Billing period boundaries for invoice subscriptions | `app/models/billing_period_boundaries.rb` | libs/period | logic done | |
| Date differences across time zones | `app/services/utils/datetime.rb` (`date_diff_with_timezone`) | libs/period | logic done | |
| Previous and next billing dates | `app/services/subscriptions/dates_service.rb` | libs/period | logic done | |

### 1.22 Contracts and Billing Segments

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Contracts with statuses pending, active, terminated, canceled | `app/models/contract.rb`, `app/services/contracts/` | libs/core | planned | |
| Contract rate cards | `app/models/contract_rate_card.rb`, `app/services/contract_rate_cards/` | libs/billing | planned | |
| Contract billing math: intervals, calendar, days, cycles, phases, segments, cycle walker, schedule | `app/services/billing/` | libs/period | planned | |
| Contract activation | `app/jobs/clock/activate_contracts_job.rb` | libs/billing | planned | |
| Billing segments creation | `app/services/billing_segments/`, `app/jobs/clock/create_billing_segments_job.rb` | libs/billing | planned | |
| Billing segments processing with overlap exclusion | `app/jobs/clock/process_billing_segments_job.rb` | libs/billing | planned | |
| Contracts API | `app/controllers/api/v2/contracts_controller.rb` | apps/server | planned | |
| In-advance contracts billed on creation | `app/services/contracts/create_service.rb`, `app/jobs/billing_segments/schedule_job.rb` | libs/billing | planned | The subscription's billing job is enqueued with it ([Domain §21.4](domain.md#214-contracts-product-catalog)) |
| Terminate an active contract, cancel a pending one, never delete | `app/services/contracts/terminate_service.rb` | libs/billing | planned | Subscription transitions (domain §9.2) |
| Edit allowlist of active contracts (`contract_locked`) | `app/services/contracts/update_service.rb` | libs/billing | planned | Applied to subscriptions priced by rate cards |
| Contract end date as the only end of its rate cards | `app/models/contract_rate_card.rb` | libs/billing | planned | The subscription's `ending_at` |
| Effective billing anchor date | `app/models/contract.rb` | libs/core | planned | |
| Rate phases copied onto contract cards, editable while pending | `app/services/contracts/materialize_rate_cards_service.rb`, `app/services/rate_phases/` | libs/billing | planned | |
| Applied rate card updates: units and anchor, the anchor reseeding the billing clock | `app/services/contract_rate_cards/update_service.rb`, `app/services/contract_rate_cards/reset_billing_clock_service.rb` | libs/billing | planned | Recomputes `next_billing_at` |
| Contract invoice custom sections | `app/services/invoice_custom_sections/` | libs/billing | planned | |
| Resumable rate card schedules (resume from the latest persisted cycle, deduplicated segments) | `app/services/billing/rate_cards/cycle_walker.rb`, `app/services/billing/rate_cards/build_schedule_service.rb` | libs/period | planned | |
| Fee reuse across retries by boundary identity (invoice, card, microsecond UTC boundaries) | `app/services/fees/charge_service.rb`, `app/models/billing_period_boundaries.rb` | libs/billing | planned | |
| In-advance metered segments priced per event | `app/services/events/pay_in_advance_billing_segment_resolver.rb` | libs/billing | planned | Never priced on the billing tick |

### 1.23 Invoices

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Invoice type subscription | `app/services/invoices/subscription_service.rb` | libs/billing | logic done | |
| Invoice type credit (wallet top-up) | `app/services/invoices/paid_credit_service.rb` | libs/billing | logic done | Settlement on payment is `wallets.Service.HandleCreditInvoicePayment` (lago `Invoices::PrepaidCreditJob`), called by manual payments (`payments.Service.CreateManual`); provider payments still wait for the payment-status update of the payments orchestration (`app/services/invoices/update_service.rb:113-117`). Deviation: voiding the invoice fails the pending purchase and no later payment settles it; lago leaves it pending and grants the credits if the voided invoice is paid (`app/services/invoices/void_service.rb:30-48`, `app/jobs/invoices/prepaid_credit_job.rb:20-25`) |
| Invoice type one-off (add-ons) | `app/services/invoices/create_one_off_service.rb`, `app/controllers/api/v1/invoices_controller.rb` (`create`) | libs/billing, apps/server | done | `InvoiceService.CreateInvoice`; add-ons, taxes, the billing entity and custom sections by ID or code. The request takes a purchase order number (`create_one_off_service.rb:129`, `invoices_controller.rb:18`); the applied custom sections are stored on the invoice (`applied_invoice_custom_sections`, `Invoices::ApplyInvoiceCustomSectionsService`) and served as `applied_invoice_custom_sections`. Lago's `metadata` on create is not taken. Subscription, progressive, in-advance (charges and fixed charges), advance charges and paid credit invoices store their sections too, at lago's call sites (`subscription_service.rb:61`, `progressive_billing_service.rb:36`, `advance_charges_service.rb:102`, `create_pay_in_advance_charge_service.rb:32`, `create_pay_in_advance_fixed_charges_service.rb:41`, `paid_credit_service.rb:29`): the subscriptions' selections and skips decide them, as `apply_invoice_custom_sections_service.rb`; refreshing a draft removes them and applies the customer's again (`refresh_draft_service.rb:63`, `:129`). Wallets and wallet transactions select no sections yet, so a paid credit invoice takes the customer's. Pending VIES finalization applies the customer's sections again (`finalize_pending_vies_invoice_service.rb:25`) |
| Invoice type advance charges | `app/services/invoices/advance_charges_service.rb` | libs/billing | logic done | |
| Invoice type progressive billing | `app/services/invoices/progressive_billing_service.rb` | libs/billing | logic done | |
| In-advance charge invoices | `app/services/invoices/create_pay_in_advance_charge_service.rb` | libs/billing | logic done | |
| Statuses draft, finalized, voided, failed, pending, generating, open, closed | `app/models/invoice.rb` | libs/core | planned | |
| Payment status (pending, succeeded, failed) and tax status | `app/models/invoice.rb` | libs/core | planned | |
| Computed amounts (fees, coupons, taxes, credit notes, prepaid credits, progressive deductions, totals, due, paid) | `app/models/invoice.rb`, `app/services/invoices/compute_amounts_from_fees.rb` | libs/billing | logic done | |
| Tax invoice label in Australia, UAE, New Zealand, Indonesia, Singapore | `app/models/invoice.rb` | libs/documents | logic done | |
| Numbering per customer (`PREFIX-CCC-NNN`) | `app/models/invoice.rb` (numbering) | libs/billing | logic done | |
| Numbering per billing entity (`PREFIX-YYYYMM-NNN`) | `app/models/invoice.rb` (numbering) | libs/billing | logic done | |
| Draft numbering (`PREFIX-DRAFT`) | `app/models/invoice.rb` | libs/billing | logic done | |
| Invoice subscriptions with invoicing reason | `app/models/invoice_subscription.rb` | libs/billing | logic done | |
| Fee calculation order for a billing run | `app/services/invoices/calculate_fees_service.rb` | libs/billing | logic done | |
| Transition to final status with zero-amount setting | `app/services/invoices/transition_to_final_status_service.rb` | libs/billing | logic done | |
| Retry generating failed subscription invoices | `app/services/invoices/retry_service.rb`, `app/jobs/clock/retry_generating_subscription_invoices_job.rb` | libs/billing | done | The periodic bill first commits the generating invoice, then generates it in its own transaction: a failure is retried once with the same invoice (5 minutes later on a billing day, 3 seconds otherwise), a failed retry records an `invoice_generation_error`, and a transient error goes back to the job's retry policy (`app/jobs/bill_subscription_job.rb:16-60`) |
| Issuing date rules (anchor and adjustment matrix) | `app/services/invoices/` (issuing date) | libs/billing | logic done | |
| Payment due date from the net payment term (days), resolved customer → billing entity and snapshotted on the invoice | `app/models/customer.rb` (`applicable_net_payment_term`), `app/services/invoices/create_generating_service.rb`, `app/services/invoices/refresh_draft_and_finalize_service.rb`, `app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb`, `app/services/invoices/regenerate_from_voided_service.rb` | libs/billing | logic done | The unread `payment_term` and `payment_term_source` columns are not modelled ([Domain §24](domain.md#24-design-decisions), item 12); finalization, tax completion and regeneration read the current term; a customer's or billing entity's term change recomputes the due dates of its drafts in the update's transaction (`update_invoice_payment_due_date_service.rb`), on the API path too (lago only from GraphQL, `update_service.rb:96-98`) |
| Purchase order number on invoices (from grouped subscriptions, wallet transactions or wallets, one-off requests), inherited or replaced on regeneration; mixed numbers refused on one bill | `app/services/invoices/create_generating_service.rb`, `app/services/invoices/paid_credit_service.rb`, `app/services/invoices/regenerate_from_voided_service.rb` | libs/billing | logic done | Regeneration inherits the voided invoice's number unless the request gives one (blank clears); periodic runs and plan changes split by number |
| Grace period with draft invoices | `app/services/invoices/subscription_service.rb` | libs/billing | logic done | |
| Draft refresh | `app/services/invoices/refresh_draft_service.rb`, `app/jobs/clock/refresh_draft_invoices_job.rb` | libs/billing | logic done | |
| Automatic finalization at grace period end | `app/jobs/clock/finalize_invoices_job.rb` | libs/billing | logic done | A draft finalized by the sweep is issued on the sweep's local date, and numbered for the sweep's month, even when its job runs late; lago redates and numbers with the worker's clock (`app/services/invoices/refresh_draft_and_finalize_service.rb:77`, `app/models/invoice.rb:738`), the two always agreeing |
| Manual finalize and finalize all drafts | `app/services/invoices/refresh_draft_and_finalize_service.rb`, `app/services/invoices/finalize_batch_service.rb` | libs/billing, apps/server | done | `FinalizeInvoice` (a non-draft is not found, as `invoices.draft.find_by`), `FinalizeAllInvoices` enqueues one finalization per draft and returns the count; `RefreshInvoice` refreshes a visible subscription invoice |
| Void with credit and refund amounts | `app/services/invoices/void_service.rb` | libs/billing, apps/server | done | `InvoiceService.VoidInvoice` |
| Re-credit coupons, wallets and credit notes on void | `app/services/invoices/void_service.rb` | libs/billing | logic done | |
| Void provider taxes on void | `app/services/invoices/provider_taxes/void_service.rb` | libs/billing | logic done | |
| Regenerate an invoice from a voided one, with preview | `app/services/invoices/regenerate_from_voided_service.rb`, `app/services/invoices/build_regeneration_preview_service.rb` | libs/billing, apps/server | done | `InvoiceService.RegenerateInvoice`, lago's `regenerateFromVoided` mutation (`app/graphql/mutations/invoices/regenerate_from_voided.rb`). Only the listed fees are regenerated, a fee listed twice twice (`regenerate_from_voided_service.rb:196-226`): a `fee_id` copies that fee as pending, taxes and coupons zeroed, `original_fee_id` set to the root of the chain, its presentation breakdowns copied when the units are unchanged; without one, or with an unknown one, a fee is built for `subscription_id`, `charge_id` and `charge_filter_id` (`subscription`, `charge` or `charge_filter` not found otherwise). Units reprice the fee by its charge model, units without a price on a percentage or prorated graduated charge give `invalid_charge_model`, a fee already adjusted gives `adjusted_fee` `already_exists`; the display name of a charge fee always comes from the adjustment. `fixed_charge_id`, `add_on_id` and `description` are accepted and ignored, as lago ignores them. The invoice subscriptions are copied and the voided ones marked with `regenerated_invoice_id`; a progressive invoice copies its applied usage thresholds; custom sections are applied. `validate_only` is lago's `Invoices::BuildRegenerationPreviewService`: the voided invoice as it stands, taxed again by the current rules, totals recomputed, nothing stored, fees and purchase order number ignored. The external sync check is not made (drafts are never synced) |
| Retry failed provider tax computation | `app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb`, `app/services/invoices/retry_service.rb` | libs/billing | logic done | `invoicing.Service.Retry` reopens a failed invoice (open, as Tanvo has no pending status) with its taxes pending and queues `billing.pull_provider_taxes` (`retry_service.rb:13-41`). `PullTaxes` applies the taxes of an open or draft invoice whose taxes are pending and fails with `integration_customer` not found for a customer the provider does not tax. It keeps the progressive billing credits and coupons applied when the invoice was held, computes only the taxes and totals, then applies credit notes and prepaid credits (not on drafts or one-off invoices); the progressive credit notes are issued when the invoice is held. A draft stays a draft and emits `invoice.ready_to_finalize`; a gated invoice asks for draft taxes and only gets its payment, and a zero total satisfies its payment rule. A provider error stores its code and message in the `tax_error` detail (`tax_error`, `tax_error_message`, `pull_taxes_and_apply_service.rb:164-178`). A provider error whose message tells of its API limit or of resource contention (`kernel.ErrTaxContention`, `app/services/integrations/aggregator/taxes/base_service.rb:68,113`) leaves the invoice unchanged and fails the job, which retries it 6 times, polynomially longer; voiding taxes does the same. `RetryAllInvoices` (`POST /v1/invoices:retry_all`) queues one `billing.retry_invoices` job with every failed invoice and returns how many, and the job retries them one by one, stopping at the first failure (`retry_batch_service.rb`, `Invoices::RetryAllJob`). Every 15 minutes, `billing.retry_tax_failed_invoices` retries the failed invoices whose `tax_error_message` contains `API limit`, case-insensitively, ignoring failed retries (`app/jobs/clock/retry_failed_invoices_job.rb`). An invoice's provider taxes are deferred: the provider is called outside the transaction (standalone in-advance fees, previews and current usage call it inline, as lago does); the answer is applied in a second one, and the provider is asked again when the fees changed in between; lago applies the earlier answer (`app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb:40`) |
| Finalize invoices held for VIES checks | `app/services/invoices/finalize_pending_vies_invoice_service.rb`, `app/jobs/invoices/finalize_pending_vies_invoice_job.rb` | libs/billing, apps/server | done | After a successful check `customers.vies_check` queues `billing.finalize_pending_vies_invoice` for each open invoice of the customer whose `tax_status` is pending (`app/jobs/customers/vies_check_job.rb:46-57`) through `customers.VIESInvoiceFinalizer`, which the invoicing service implements and the server wires with `customers.WithVIESInvoiceFinalizer`. Under the invoice lock the job leaves alone an invoice that is not open with pending taxes, of a provider-taxed customer, or whose customer's check is in progress again; otherwise it applies the customer's custom sections again (`kernel.ApplyCustomSections` with no IDs and no resources, `finalize_pending_vies_invoice_service.rb:25`), keeps the progressive billing credits and coupons applied when it was held and computes only its taxes and totals (`finalize_pending_vies_invoice_service.rb:24`; fees taxed when created, the explicit taxes of one-off lines, keep their taxes), applies credit notes and prepaid credits, redates the invoice and finalizes it with `invoice.created` or `invoice.one_off_created`, its document and its payment. A payment-gated invoice of zero satisfies its payment rule; a gated invoice stays open and only gets its payment |
| Progressive billing deductions and excess credit note | `app/services/invoices/progressive_billing_service.rb` | libs/billing | logic done | |
| Invoice preview: proposal, projection, termination, plan change | `app/services/invoices/preview_service.rb`, `app/services/invoices/preview_context_service.rb`, `app/services/invoices/preview/`, `app/controllers/api/v1/invoices_controller.rb` | libs/billing, apps/server | done | `InvoiceService.PreviewInvoice`. The simulated changes (an ad-hoc or overridden customer, its integration customers, terminations, plan changes, coupons built from attributes and applied coupons) are written in a rolled-back transaction instead of in memory. A request without `customer_id` previews a customer that does not exist yet; `coupon_ids` (references to no coupon are ignored) are an addition beside the `coupons` given by attributes. Accepted differences: validation errors name the request fields (`subscription_ids` and `plan_id` instead of `subscriptions` and `base`, `integration_not_found` instead of `anrok_integration_not_found`), and integrations are referenced by ID or code without `integration_type`; the simulated records carry generated IDs where lago leaves new records without one (the generated external IDs are not shown); there is no premium check (`app/services/invoices/preview_service.rb:19`, `:22`) |
| Dispute lost | `app/services/invoices/lose_dispute_service.rb` | libs/billing, apps/server | done | `InvoiceService.LoseInvoiceDispute`: finalized or voided only; an earlier loss keeps its instant; emits `invoice.payment_dispute_lost` and voids the provider taxes. The Hubspot invoice update is not enqueued; the loss reported by a payment provider (`app/services/payments/lose_dispute_service.rb`) comes with the provider adapters |
| Delete draft invoice | `app/services/invoices/delete_service.rb` | libs/billing, apps/server | done | `InvoiceService.DeleteInvoice`: drafts only (`not_deletable`), not synced to an external system (`invoice_synced_to_external_system`, an integration resource exists); its draft credit notes move to the terminal, invisible `deleted` status (`CreditNotes::DeleteService`, `delete_service.rb:41-49`), any other refuses the deletion (`credit_note_not_deletable`); the invoice moves to the terminal, invisible `deleted` status with its fees and subscription periods kept (`Invoice#mark_as_deleted`), so the period is not billed again; `invoice.deleted` is emitted |
| Update payment status and metadata (max 5, key 20, value 140) | `app/services/invoices/update_service.rb` | libs/billing, apps/server | done | `InvoiceService.UpdateInvoice` over `payments.Service.UpdateInvoice`: fees take the payment status, a succeeded payment clears the overdue flag, resets the dunning counters of a campaign request and expires the checkout link, a credit invoice settles its wallet purchase (`update_service.rb:113-117`). The Hubspot update and the subscription gate resolution are not enqueued |
| Mark invoices overdue | `app/services/invoices/payments/mark_overdue_service.rb` | libs/billing | done | Finalized, unpaid invoices past their due date, without a lost dispute |
| Item metadata on invoice items (at most 50 keys) | `app/models/` (`item_metadata`) | libs/billing | planned | |
| Invoice settlements (payment, credit note) | `app/models/invoice_settlement.rb` | libs/billing | planned | |
| Invoice list filters and search | `app/queries/invoices_query.rb` | libs/store, apps/server | done | Includes the purchase order number, case-insensitive; store queries in both stores with the ported specs in porttest; `ListInvoices` takes them as an AIP filter, with search and the customer as parent |
| Invoice custom sections (manual, system generated) and resolution rules | `app/models/invoice_custom_section.rb`, `app/services/invoice_custom_sections/` | libs/billing, apps/server | in progress | Public API, not only the admin UI: `InvoiceCustomSectionService` over libs/billing/customsections creates, gets (ID or code), updates, deletes and lists the manual sections by name, as the GraphQL mutations and resolver of v1.55.0 (`app/graphql/resolvers/invoice_custom_sections_resolver.rb`). The code stays editable, as in `update_input.rb`; deleting discards the section (`deleted_at`; its code is free again) and removes it from the billing entity, customer and subscription selections (`destroy_service.rb`, `deselect_all_service.rb`); sections carry the unprinted `description`; the list takes no filter or search, as in v1.55.0. Resolution rules are still to come |
| Download PDF and XML | `app/services/invoices/generate_pdf_service.rb` | libs/documents | logic done | `DownloadInvoice` returns the rendered PDF of a finalized invoice, else enqueues its rendering; the XML rendering is not enqueued yet |
| Resend invoice email | `app/services/emails/resend_service.rb` | libs/mail | logic done | |
| Payment URL for an invoice | `app/services/invoices/payments/generate_payment_url_service.rb` | libs/billing | in progress | The checks are done (`invoicing.Service.PaymentURL`: a linked provider, not GoCardless, a payable invoice); `GenerateInvoicePaymentUrl` then answers Unimplemented until the provider checkout adapters exist. `RetryInvoicePayment` (`app/services/invoices/payments/retry_service.rb`) is done, with the `payment_method_type` and `payment_method_id` override (`invoices_controller.rb:327-334`) carried on the payment job; `RetryInvoice` (`app/services/invoices/retry_service.rb`) reopens the failed invoice with its taxes pending and queues their pull (§ retry failed provider tax computation) |
| Store Salesforce id on an invoice | `app/controllers/api/v1/invoices_controller.rb` (`sync_salesforce_id`) | providers/salesforce | planned | |
| Invoice webhooks `invoice.created`, `one_off_created`, `paid_credit_added`, `generated`, `drafted`, `ready_to_finalize`, `voided`, `deleted`, `resynced` | `app/services/webhooks/invoices/` | libs/webhooks | planned | |
| Invoice payment webhooks `invoice.payment_status_updated`, `payment_overdue`, `payment_failure`, `payment_dispute_lost` | `app/services/webhooks/invoices/`, `app/services/webhooks/payment_providers/` | libs/webhooks | planned | |

### 1.24 Fees

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Fee types charge, add-on, subscription, credit, commitment, fixed charge, product | `app/models/fee.rb` | libs/core | planned | |
| Fee payment status (pending, succeeded, failed, refunded) for in-advance fees | `app/models/fee.rb` | libs/core | done | Set through `FeeService.UpdateFee` on fees attached to no invoice; the instant of the new status is stamped and the others are cleared (`app/services/fees/update_service.rb:45-62`) |
| Subscription fees | `app/services/fees/subscription_service.rb` | libs/billing | logic done | |
| Charge fees (including filters and groups) | `app/services/fees/charge_service.rb` | libs/billing | logic done | |
| Fixed charge fees | `app/services/fees/fixed_charge_service.rb` | libs/billing | logic done | |
| Add-on and one-off fees | `app/services/fees/one_off_service.rb` | libs/billing | logic done | |
| Credit fees for wallet top-ups | `app/services/fees/paid_credit_service.rb` | libs/billing | logic done | The unit amount drops the fraction of a cent of the wallet rate, as lago's bigint `unit_amount_cents` does (`app/models/wallet_transaction.rb:99`); the precise unit amount is that unit amount (`paid_credit_service.rb:40`). A void of the credit invoice fails the purchase (deviation, see the credit invoice row) |
| Commitment fees | `app/services/fees/commitments/` | libs/billing | logic done | |
| Adjusted fees on draft invoices (units, unit amount, display name) | `app/models/adjusted_fee.rb`, `app/services/adjusted_fees/` | libs/billing, apps/server | done | An adjustment applies to the fee of the same charges (or fixed charges) boundaries, as lago's `matching_charge_boundaries` (`app/models/adjusted_fee.rb:35-39`). `FeeService.AdjustFee` (`POST /v1/invoices/{invoice_id}/fees:adjust`) and `FeeService.DeleteFeeAdjustment` (`POST /v1/fees/{fee_id}:delete_adjustment`) serve lago's GraphQL `CreateAdjustedFee` and `DestroyAdjustedFee`. The mode is explicit, where lago infers it from the units and unit price given (`app/services/adjusted_fees/create_service.rb:35-36`); no premium gate (`create_service.rb:84`); a non-draft invoice fails with `not_draft`, where lago answers `forbidden`. Without a fee, the adjustment is stored unlinked with the invoice subscription's boundaries and the refresh creates the fee, where lago first creates an empty fee (`create_service.rb:97-157`); the adjustment is not re-linked to the regenerated fee, it matches by key |
| Fee taxes | `app/models/fee/applied_tax.rb` | libs/tax | planned | |
| Presentation breakdowns of fees | `app/models/` (`presentation_breakdowns`) | libs/billing | in progress | Stored on the fee (lago has a `presentation_breakdowns` table) and printed on invoices with null values; billing does not compute them yet |
| Fees API (list, retrieve, update payment status, delete unattached) | `app/controllers/api/v1/fees_controller.rb`, `app/queries/fees_query.rb` | apps/server | done | `FeeService` (`/v1/fees`). The list filter takes `fee_type`, `payment_status`, `currency`, `customer_id` and `subscription_id` (an ID or `external_id:<id>`), `billable_metric_code`, `event_transaction_id` and the `created_at`, `succeeded_at`, `failed_at` and `refunded_at` ranges; the list is ordered oldest first, then by ID (`app/queries/fees_query.rb:33`). Deleting a fee discards it (`deleted_at`) and returns it (`app/services/fees/destroy_service.rb:17`); every fee read skips discarded fees, as the default scope does (`app/models/fee.rb:9`). An invoiced fee fails with `invoiced_fee` as a failed precondition (400), where lago answers 405 |
| Fee webhook `fee.created` (in-advance) | `app/services/webhooks/fees/pay_in_advance_created_service.rb` | libs/webhooks | planned | |

### 1.25 Taxes

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Tax CRUD (name, code, rate, description) | `app/models/tax.rb`, `app/services/taxes/` | libs/billing, apps/server | done | `TaxService` over libs/billing/taxes; changing or deleting a tax flags the affected drafts for refresh. The list is by name, filtered on `auto_generated` and `applied_to_organization` and searchable (`taxes_query.rb`). Deleting discards the tax and removes its links, as v1.55.0 does. A tax carries `applied_to_organization` and `applied_to_billing_entities_codes` (`serializers/v1/tax_serializer.rb`); on delete they are those loaded before the links are removed (`taxes/destroy_service.rb:25`) |
| Taxes on billing entities, customers, plans, charges, fixed charges, add-ons, commitments | join tables in `app/models/` | libs/tax | planned | |
| Tax resolution order (explicit, add-on, charge, fixed charge, commitment, plan, customer, billing entity) | `app/services/fees/apply_taxes_service.rb` | libs/tax | logic done | |
| Rate card taxes overriding plan, customer and billing entity taxes | `app/models/rate_card.rb` (`rate_cards_taxes`) | libs/tax | planned | A rate card's taxes are its charge's taxes in the one catalog |
| Invoice applied taxes with taxable base and allocation | `app/services/invoices/apply_taxes_service.rb` | libs/tax | logic done | The provider's taxable base is stored on the invoice applied tax and printed on invoices (`app/models/invoice/applied_tax.rb:31-41`) |
| Credit note applied taxes | `app/services/credit_notes/apply_taxes_service.rb` | libs/tax | logic done | |
| Provider-computed taxes (Anrok, Avalara) with special codes and base rate | `app/services/invoices/apply_provider_taxes_service.rb` | libs/tax | logic done | |
| EU VAT rates per country with exceptions | `app/services/taxes/` (ibericode rates) | libs/tax | logic done | |
| Automatic EU taxes for an organization | `app/services/taxes/auto_generate_service.rb` | libs/tax | logic done | |
| EU tax decision per customer (domestic, intra-EU B2B reverse charge, B2C, outside EU) | `app/services/customers/eu_auto_taxes_service.rb` | libs/tax | logic done | |
| VIES VAT number check with retries and backoff | `app/services/customers/vies_check_service.rb`, `app/jobs/customers/vies_check_job.rb` | libs/tax, libs/billing | done | `tax.VIESClient` calls the VIES SOAP service as valvat 2.0.1 does (malformed and GB numbers are not checked, redirects are errors). A customer write that needs a check saves a `pending_vies_checks` row and queues `customers.vies_check` on the default queue; a territory or a decision without VIES deletes the row. The job applies the decided EU tax (left out when the tax does not exist), flags drafts for refresh and deletes the row; a VIES failure records the attempt and retries after 5, 5, 10, 20, 40 minutes, then hourly. An unknown fault or an unreachable VIES fails the job. Retries are keyed per attempt, since the queue keeps a unique job while it runs |
| Invoice held until a pending VIES check completes | `app/models/pending_vies_check.rb`, `app/services/invoices/ensure_completed_vies_check_service.rb` | libs/billing | done | The pending check is stored (`pending_vies_checks`, one per customer, keyed by organization and customer). Where lago calls `Invoices::ComputeTaxesAndTotalsService` (subscription invoices, their refresh and finalization, pay-in-advance charges and fixed charges, one-off, progressive billing and regenerated invoices), an invoice of a customer taxed by no provider, whose billing entity manages EU VAT and who has a pending check, keeps its fees untaxed and waits with `tax_status` pending: open (gated or not) when finalizing, a draft otherwise. Nothing pulls its taxes; it is not announced, a new draft still emits `invoice.drafted`. The explicit taxes of one-off lines are applied to their fees at once, as lago applies them when it creates the fees. Previews, advance charges and standalone fees are not held, as in lago |
| VIES webhook `customer.vies_check` | `app/services/webhooks/customers/vies_check_service.rb` | libs/billing, libs/webhooks | done | The payload is the customer with `vies_check`: the VIES lookup, or `valid: false`, `valid_format` and, on a failure, `error` |
| Tax provider error webhooks `customer.tax_provider_error`, `fee.tax_provider_error` | `app/services/webhooks/integrations/taxes/` | libs/webhooks | planned | |

### 1.26 Coupons

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Coupon CRUD with fixed amount or percentage | `app/models/coupon.rb`, `app/services/coupons/`, `app/controllers/api/v1/coupons_controller.rb`, `app/queries/coupons_query.rb` | libs/billing, apps/server | done | `CouponService`. Deletion discards the coupon (`deleted_at`, `coupon.rb:67`): its status is kept, reads and applications ignore it and its code is free again. Lists filter on `status`, search name and code, and sort active first, then by expiration (`coupon.rb:68-76`). A code conflict is `value_already_exist` on `code`. A coupon carries `limited_plans`, `limited_billable_metrics` and the codes of its parent plans and live metrics (`serializers/v1/coupon_serializer.rb`) |
| Frequency once, recurring (with duration), forever | `app/models/coupon.rb` | libs/billing | done | |
| Reusable coupons | `app/models/coupon.rb` | libs/billing | done | Reusable by default (`coupons/create_service.rb:22`) |
| Limit to plans or billable metrics | `app/models/coupon_target.rb` | libs/billing | done | By ID or `code:`; missing targets are `plans_not_found` or `billable_metrics_not_found`. An update changes a limitation only when it is in the update mask (or set without one), where lago replaces both lists on every update (`coupons/update_service.rb:68-69, 101-120`) |
| Expiration by date and termination job | `app/jobs/clock/terminate_coupons_job.rb` | libs/billing | logic done | |
| Applied coupons with amount, percentage and frequency overrides | `app/services/applied_coupons/create_service.rb`, `app/controllers/api/v1/applied_coupons_controller.rb`, `app/queries/applied_coupons_query.rb` | libs/billing, apps/server | done | `ApplyCoupon` and `ListAppliedCoupons`, also under `/v1/customers/{customer_id}/applied_coupons`. Lists exclude deleted customers, keep deleted coupons, filter on `status` and `coupon_id` (IDs or `code:`) and are newest first. A `customer_id` that matches no customer is not found on both routes. An applied coupon carries its coupon's code, name, description, status and deletion, the customer's external ID and the coupon's expiration; lists include its credits with the invoice payment status (`serializers/v1/applied_coupon_serializer.rb`, `includes: %i[credits]`), without the credit `item`, which is the coupon itself |
| Application order and amount math on invoices | `app/services/credits/applied_coupons_service.rb`, `app/services/credits/applied_coupon_service.rb` | libs/billing | logic done | |
| Re-credit of coupons on void | `app/services/applied_coupons/recredit_service.rb` | libs/billing | logic done | |
| Applied coupon termination | `app/services/applied_coupons/terminate_service.rb`, `app/controllers/api/v1/customers/applied_coupons_controller.rb` | libs/billing, apps/server | done | `TerminateAppliedCoupon` by applied coupon ID, without the customer in the path |

### 1.27 Credits and Application Order

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Credits table linking coupons, credit notes and progressive billing to invoices | `app/models/credit.rb` | libs/billing | logic done | |
| Credit note credits on invoices | `app/services/credits/credit_note_service.rb` | libs/billing | logic done | |
| Prepaid credits from wallets allocated by wallet priority and allowed fee types | `app/services/credits/applied_prepaid_credits_service.rb` | libs/billing | logic done | |
| Progressive billing credits | `app/services/credits/progressive_billing_service.rb` | libs/billing | logic done | |
| Application order: progressive billing, coupons, taxes, credit notes, prepaid credits | `app/services/invoices/subscription_service.rb`, `app/services/invoices/calculate_fees_service.rb` | libs/billing | logic done | |

### 1.28 Credit Notes

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Credit note creation with credit, refund and offset amounts | `app/services/credit_notes/create_service.rb` | libs/billing | logic done | |
| Credit status (available, consumed, voided) and refund status (pending, succeeded, failed) | `app/models/credit_note.rb` | libs/core | planned | |
| Reasons duplicated charge, product unsatisfactory, order change, order cancellation, fraudulent charge, other | `app/models/credit_note.rb` | libs/core | planned | |
| Draft, finalized statuses | `app/models/credit_note.rb` | libs/core | planned | |
| Numbering `INVOICE-CNnnn` | `app/models/credit_note.rb` | libs/billing | logic done | |
| Credit note items per fee | `app/models/credit_note_item.rb` | libs/billing | logic done | |
| Taxes with allocation and rounding adjustment | `app/services/credit_notes/apply_taxes_service.rb` | libs/tax | logic done | |
| Validation of creditable and refundable amounts | `app/services/credit_notes/validate_service.rb` | libs/billing | logic done | All checks enforced, including those Lago never calls (§4) |
| Offsets settling an invoice | `app/services/credit_notes/create_service.rb`, `app/models/invoice_settlement.rb` | libs/billing | logic done | |
| Refunds through payment providers | `app/services/credit_notes/refunds/` | libs/billing | logic done | See §1.35 |
| Credit note from subscription termination | `app/services/credit_notes/create_from_termination.rb` | libs/billing | logic done | |
| Credit note from progressive billing excess | `app/services/credit_notes/create_from_progressive_billing_invoice.rb` | libs/billing | logic done | |
| Estimate a credit note | `app/services/credit_notes/estimate_service.rb` | libs/billing | logic done | |
| Void credit note (remaining credit) | `app/services/credit_notes/void_service.rb` | libs/billing | logic done | |
| Update refund status | `app/services/credit_notes/update_service.rb` | libs/billing | logic done | |
| Refresh draft credit notes with their invoice | `app/services/credit_notes/refresh_draft_service.rb` | libs/billing | logic done | |
| Re-credit on invoice void | `app/services/credit_notes/recredit_service.rb` | libs/billing | logic done | |
| Credit note metadata | `app/models/metadata/` | libs/billing | logic done | |
| Credit note list filters, including the invoice's purchase order number | `app/queries/credit_notes_query.rb` | libs/store | done | Store queries in both stores with the ported specs in porttest; `ListCreditNotes` filters on credit, refund and reason statuses, currency, billing entity, self-billed, invoice and purchase order numbers, type, the customer (ID or external ID), issuing date and amount (minor units) ranges, with a search |
| Credit notes API | `app/controllers/api/v1/credit_notes_controller.rb` | apps/server | in progress | `CreditNoteService` Create, Estimate (with the maximum creditable, refundable and offsettable amounts of `types/credit_notes/estimate.rb`), Get, List, Update (refund status, metadata), Void and Download over libs/billing/creditnotes; only finalized notes are served. Download enqueues the document job and answers empty until the PDF exists, then returns its path without a signed URL. ResendCreditNote and RetryCreditNoteSync are not served yet (mail and integration wiring) |
| Credit note webhooks `credit_note.created`, `credit_note.generated`, `credit_note.provider_refund_failure` | `app/services/webhooks/credit_notes/` | libs/webhooks | planned | One event name (§4) |

### 1.29 Wallets

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Wallet CRUD (name, code, currency, rate amount, expiration) | `app/models/wallet.rb`, `app/services/wallets/` | libs/billing | logic done | The currency is the wallet's own, default the customer's |
| Purchase order number on wallets, wallet transactions and recurring rules, copied to credit invoices | `app/services/wallets/create_service.rb`, `app/services/wallet_transactions/create_from_params_service.rb`, `app/models/recurring_transaction_rule.rb` | libs/billing | logic done | Blank is none; a rule run takes the rule's, else the wallet's |
| Several wallets per customer (default maximum 6) with priority 1–50 | `app/models/wallet.rb` | libs/billing | logic done | |
| Top-up minimum and maximum | `app/models/wallet.rb` | libs/billing | logic done | |
| Allowed fee types per wallet | `app/models/wallet.rb` (`allowed_fee_types`) | libs/billing | logic done | |
| Wallet targets (billable metrics a wallet pays for) | `app/models/wallet_target.rb` | libs/billing | logic done | |
| Traceable wallets (consumptions and fundings) | `app/models/wallet_transaction_consumption.rb` | libs/billing | logic done | |
| Wallet transactions: paid, granted, voided; statuses pending, settled, failed; source manual, interval, threshold | `app/models/wallet_transaction.rb`, `app/services/wallet_transactions/` | libs/billing | logic done | |
| Targeted void of one inbound grant (`voided_transaction_id`) | `app/services/wallet_transactions/void_service.rb`, `app/services/wallet_transactions/validate_service.rb` | libs/billing | logic done | Traceable wallets only; whole remainder or a capped amount |
| Invoice requires successful payment before credits are granted | `app/models/wallet_transaction.rb` | libs/billing | logic done | |
| Recurring top-up rules: interval or threshold, fixed or target, grants target top-up | `app/models/recurring_transaction_rule.rb`, `app/services/wallets/recurring_transaction_rules/` | libs/billing | logic done | |
| Interval top-ups | `app/jobs/clock/create_interval_wallet_transactions_job.rb` | libs/billing | logic done | The rule is read again under the wallet lock; lago computes the top-up from the rule listed by the clock job (`app/services/wallets/create_interval_wallet_transactions_service.rb:8`) |
| Threshold top-ups with backoff | `app/services/wallets/threshold_top_up_service.rb` | libs/billing | logic done | |
| Termination of expired rules | `app/jobs/clock/terminate_recurring_transaction_rules_job.rb` | libs/billing | logic done | |
| Ongoing balance from current usage, cascaded across wallets | `app/services/wallets/balance/refresh_ongoing_usage_service.rb`, `app/services/wallets/balance/allocate_ongoing_usage_by_wallets_service.rb` | libs/billing | logic done | Application order, each wallet up to its balance, overflow on the last; a threshold-rule wallet absorbs all |
| Ongoing balance refresh | `app/jobs/clock/refresh_wallets_ongoing_balance_job.rb`, `app/services/wallets/realtime_refresh_service.rb` | libs/billing | logic done | Also from usage refresh signals, sent once the buckets are stored; the 5-minute sweep stays authoritative; the usage is computed under no lock, as in lago (`app/jobs/customers/refresh_wallet_job.rb:47`), but the balances are written under the customer's `prepaid_credits` lock and the usage computed again when an invoice consumed credits meanwhile; lago writes them unlocked |
| Wallet expiration | `app/jobs/clock/terminate_wallets_job.rb` | libs/billing | logic done | |
| Payment URL for a wallet transaction | `app/services/wallet_transactions/payments/generate_payment_url_service.rb` | libs/billing | in progress | The checks (a pending purchase with an invoice) are done (`wallets.Service.PaymentURLInvoice`); `GenerateWalletTransactionPaymentUrl` then answers Unimplemented until the invoice payment URL of the payment providers exists (`app/services/invoices/payments/generate_payment_url_service.rb`) |
| Wallet and transaction metadata (at most 15 keys) | `app/models/metadata/` | libs/billing | logic done | |
| Wallet webhooks `wallet.created`, `updated`, `terminated`, `depleted_ongoing_balance` | `app/services/webhooks/wallets/` | libs/webhooks | planned | |
| Wallet transaction webhooks `wallet_transaction.created`, `updated`, `payment_failure` | `app/services/webhooks/wallet_transactions/` | libs/webhooks | planned | |
| Wallets and wallet transactions API | `app/controllers/api/v1/wallets_controller.rb`, `app/controllers/api/v1/customers/wallets_controller.rb`, `app/controllers/api/v1/wallet_transactions_controller.rb`, `app/controllers/concerns/wallet_actions.rb` | apps/server | in progress | `WalletService` Create, Get, List (`currency` and `billing_entity_id` filters), Update, Terminate, CreateWalletTransaction, GetWalletTransaction, ListWalletTransactions (`transaction_type`, `transaction_status`, `status` and `metadata.<key>` filters), ListWalletTransactionConsumptions (consumptions of an inbound transaction, fundings of an outbound one) and ReplaceRecurringTransactionRules. Lists are oldest first; Lago lists newest first. Wallets carry their currently active rules. Not served yet: wallet metadata (not modeled), `transaction_name`, `transaction_metadata`, `transaction_priority` and `purchase_order_number` on create, `voided_transaction_id`, `priority` and `purchase_order_number` on transactions, invoice custom sections and connections (no request fields) |

### 1.30 Commitments

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Minimum commitment per plan with override per subscription | `app/models/commitment.rb`, `app/services/commitments/` | libs/billing | logic done | |
| True-up fee when usage is below the commitment | `app/services/commitments/minimum/calculate_true_up_fee_service.rb` | libs/billing | logic done | |
| In-advance and in-arrears commitment periods | `app/services/commitments/` | libs/billing | logic done | |
| Proration coefficient for partial periods | `app/services/commitments/` | libs/billing | logic done | |
| Commitment in invoice previews | `app/services/invoices/preview_service.rb` | libs/billing | logic done | |
| Taxes on commitments | `app/services/commitments/apply_taxes_service.rb` | libs/tax | planned | |

### 1.31 Progressive Billing and Usage Thresholds

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Usage thresholds on plans or subscriptions (exclusive) | `app/models/usage_threshold.rb`, `app/services/usage_thresholds/` | libs/billing, apps/server | done | `Subscription.usage_thresholds` on create and update replaces the subscription's own thresholds, discards the child plan's and flags the lifetime usage for recalculation (`app/services/subscriptions/update_usage_thresholds_service.rb`); with `plan_overrides.usage_thresholds` both are refused as `incompatible_params`. On read it holds the applicable thresholds (lago `applicable_usage_thresholds`, `app/models/subscription.rb:305-309`) |
| At most one recurring threshold | `app/services/usage_thresholds/update_service.rb` | libs/billing | logic done | |
| Applied usage thresholds | `app/models/applied_usage_threshold.rb` | libs/billing | logic done | |
| Threshold override per subscription | `app/services/usage_thresholds/override_service.rb` | libs/billing | logic done | |
| Threshold crossing check on usage refresh | `app/services/lifetime_usages/check_thresholds_service.rb` | libs/billing | logic done | |
| Progressive billing invoice on crossing | `app/services/invoices/progressive_billing_service.rb` | libs/billing | logic done | |

### 1.32 Usage Monitoring Alerts

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Alert type current usage amount | `app/models/usage_monitoring/alert.rb` | libs/billing | done | |
| Alert types billable metric current usage amount and units | `app/models/usage_monitoring/` | libs/billing | done | |
| Alert types lifetime usage amount and billable metric lifetime usage units | `app/models/usage_monitoring/` | libs/billing | done | No premium gate |
| Alert types wallet balance amount, credits balance, ongoing balance amount, credits ongoing balance | `app/models/usage_monitoring/` | libs/billing | done | Evaluated after each wallet balance change; no premium gate |
| Up to 20 thresholds per alert, recurring threshold | `app/models/usage_monitoring/alert.rb` | libs/billing | done | |
| Triggered alerts | `app/models/usage_monitoring/triggered_alert.rb` | libs/billing | done | |
| Alert evaluation from subscription activity | `app/services/usage_monitoring/process_subscription_activity_service.rb` | libs/billing | done | Under a row lock on the alert, which alert edits also take. A subscription's activity is a delayed job per minute (unique per subscription and minute) enqueued by the usage signals, instead of the `subscription_activities` table swept every minute (`app/services/usage_monitoring/track_subscription_activity_service.rb:23-32`, `app/jobs/clock/process_all_subscription_activities_job.rb`); every subscription is tracked, as there are no premium gates |
| Alerts API on subscriptions and wallets, with batch create | `app/controllers/api/v1/subscriptions/alerts_controller.rb`, `app/controllers/api/v1/customers/wallets/alerts_controller.rb` | apps/server | in progress | `AlertService` Create, BatchCreate (all or none, errors under `alerts[i]`), Get, List, Update, Delete and DeleteAlerts (every alert of the owner, locked in ID order, then their thresholds deleted and the alerts discarded; no webhook) over libs/billing/alerts; alerts are named by ID or `code:`, subscriptions by ID or `external_id:`, wallets by ID. Delete discards the alert (`deleted_at`) and deletes its thresholds; its triggered alerts are kept. A decreasing (wallet) alert starts from the wallet's current value. Lists are oldest first; Lago lists newest first |
| Webhook `alert.triggered` (high priority) | `app/services/webhooks/usage_monitoring/alert_triggered_service.rb` | libs/webhooks | logic done | Emitted on each trigger; no delivery priority yet |

### 1.33 Payments

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Payment providers per organization with code and name | `app/models/payment_providers/base_provider.rb` | libs/core, libs/billing, apps/server | done | Several connections per provider, managed by `IntegrationService` (§1.37); a code is unique among the organization's live payment providers (`index_payment_providers_on_code_and_organization_id`) |
| Provider status mapping to pending, processing, succeeded, failed | `app/models/payment_providers/*.rb` (`determine_payment_status`) | providers/* | in progress | Stripe done (`stripe.StatusOf`, lago `StripeProvider` status lists); `payment.PaymentResult` also carries the provider status and the next-action URL of `requires_action`; a missing `next_action.type` is read as `redirect_to_url` |
| Success redirect URL | `app/models/payment_providers/base_provider.rb` | libs/core | planned | |
| Provider customer creation | `app/services/payment_providers/create_customer_factory.rb` | providers/* | in progress | Port `payment.CustomerGateway` (create, update, remove; `payment.SyncActionOf` decides the sync); Stripe done |
| Provider deletion (unlinks customers) | `app/services/payment_providers/destroy_service.rb` | libs/billing | done | `DeleteIntegration` discards the payment provider and its live provider customers in one transaction (`destroy_service.rb:19-22`); its code can be reused |
| Several provider and integration connections per customer, one default per category | `app/services/payment_provider_customers/`, `app/services/integration_customers/` | libs/billing | in progress | Integration connections: several per category with one default. Payment connections: one per customer, as v1.55.0 (`payment_provider_customers/create_or_update_batch_service.rb:23-30`) |
| Connection choice per billing object (specific, skip, inherit) | `app/services/billing_object_connections/` | libs/billing | planned | |
| Payment methods (from provider, default, details, destroy) | `app/services/payment_methods/` | libs/billing | in progress | Provider side done: `payment.MethodGateway` (list, default, details, set default, detach, setup URL) in providers/stripe. Detaching detaches at Stripe; lago only discards the record (`app/services/payment_methods/destroy_service.rb`) Destroy (soft delete, default cleared) and set default are done in `payments.Service`; syncing methods from the provider is not. |
| Payment method precedence (override, subscription, wallet transaction, rule, wallet, customer default) | `app/services/payment_methods/determine_service.rb` | libs/billing | planned | |
| Manual payment method skips automatic charge | `app/services/payment_methods/determine_service.rb` | libs/billing | planned | |
| Payment types provider, manual, x402 | `app/models/payment.rb` | libs/core | planned | |
| Manual payments with reference (at most 40 characters) | `app/services/payments/manual_create_service.rb` | libs/billing | done | `payments.Service.CreateManual`. `created_at` is `paid_at` (`manual_create_service.rb:34`). Accepted deviation: the receipt is issued in the payment's transaction, not by `PaymentReceipts::CreateJob`. |
| Automatic invoice payment with in-flight guard and attempt counter | `app/services/invoices/payments/create_service.rb` | libs/billing | planned | One in-flight provider payment per payable |
| Zero-amount invoices marked paid | `app/services/invoices/payments/create_service.rb` | libs/billing | logic done | |
| Delay of automatic payment when a payment link is open | `app/services/invoices/payments/create_service.rb` (`CHECKOUT_AUTO_PAYMENT_DELAY`) | libs/billing | planned | |
| Payment reference text | `app/services/invoices/payments/create_service.rb` | libs/billing | planned | |
| Payment intents (payment links with expiry, reuse) | `app/models/payment_intent.rb`, `app/services/payment_intents/` | libs/billing | planned | |
| Retry invoice payment and retry all | `app/services/invoices/payments/retry_service.rb`, `retry_batch_service.rb` | libs/billing | done | `RetryAllInvoicePayments` (`POST /v1/invoices:retry_all_payments`) queues one `billing.retry_invoice_payments` job with every finalized invoice whose payment is pending or failed and that is ready for payment processing, and returns how many; the job retries each payment with `RetryPayment`, stopping at the first failure (`app/services/invoices/payments/retry_batch_service.rb`, `Invoices::Payments::RetryAllJob`). Like `FinalizeAllInvoices`, both retry-all RPCs return the queued count, not the invoices |
| Cancel abandoned payments requiring action | `app/services/invoices/payments/cancel_abandoned_service.rb` | libs/billing | planned | |
| Payment success effects: paid totals, invoice payment status, accounting sync | `app/services/invoices/payments/*_service.rb` | libs/billing | in progress | Manual payments update paid totals, invoice and fee payment status, overdue and dunning state, and settle credit invoices; accounting sync is not done. |
| Payment failure webhooks (invoice, wallet transaction, payment request) | `app/services/invoices/payments/deliver_error_webhook_service.rb` | libs/webhooks | planned | |
| Payment webhooks `payment.succeeded`, `payment.requires_action` | `app/services/webhooks/payments/` | libs/webhooks | planned | |
| Provider webhooks `payment_provider.error`, `customer.payment_provider_created`, `customer.payment_provider_error`, `customer.checkout_url_generated` | `app/services/webhooks/payment_providers/` | libs/webhooks | planned | |
| Payments API (list, retrieve, create manual) and per-customer payments | `app/controllers/api/v1/payments_controller.rb` | apps/server | done | The amount is a decimal string in major units, as `Money` in the other APIs. Filters `customer_id`, `invoice_id` and `currency`, and `search` (`payments_query.rb`: provider payment ID, reference, the payment ID, the invoice number without an invoice filter, the customer's name, first and last name, external ID and email without a customer filter). Ordered newest first, then by ID (`base_query.rb:111`). The customer route's `customer_id` lists that customer's payments and an unknown one is NotFound (`customers/payments_controller_spec.rb:13`), over Connect as over REST; `/v1/payments` narrows to a customer with the `customer_id` filter term, which lists nothing for an unknown customer (`payments_query_spec.rb:200`). |
| Payment methods API (list, delete, set default) | `app/controllers/api/v1/customers/payment_methods_controller.rb` | apps/server | done | Delete returns an empty response (lago renders the method). |

### 1.34 Payment Providers

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Stripe connection (secret key) with webhook endpoint registered automatically | `app/services/payment_providers/stripe_service.rb`, `app/jobs/payment_providers/stripe/register_webhook_job.rb` | providers/stripe | logic done | Settings `success_redirect_url`, `webhook_endpoint_id` (stored, not returned), `supports_3ds`, `require_terms_of_service_consent`; the webhook signing secret is kept in the connection's secrets, not settings (lago keeps it in settings, `base_provider.rb:30`). The endpoint is per connection (`/webhooks/stripe/{connection_id}`) on stripe-go's pinned API version, without the per-call version override (`register_webhook_service.rb:8`); two extra events are subscribed (`payment_intent.requires_action`, `payment_method.attached`). Client-side token-bucket rate limit per connection and key mode; stripe-go retries connection errors and 429 `lock_timeout` in process, everything else is retried by the caller's job as in lago (`create_service.rb:50-56`). Unported spec cases with reasons: providers/stripe/SKIPPED.md |
| Stripe 3DS support and terms-of-service consent | `app/models/payment_providers/stripe_provider.rb` | providers/stripe | logic done | `supports_3ds` and `require_terms_of_service_consent` are set and updated through `IntegrationService` (`stripe_service.rb:20-48`). Expiring the open payment intents when the consent changes (`ExpirePaymentIntentsJob`) and re-registering the webhook on a code change wait for the Stripe adapter (phase 5) |
| Stripe customers with idempotency and email fallback | `app/services/payment_provider_customers/stripe_service.rb` | providers/stripe | logic done | The email-lookup fallback after an idempotency conflict is skipped for a customer without email (lago searches with a blank email, `stripe_service.rb:168`) |
| Stripe payment method types (card, SEPA debit, US bank account, BACS debit, Link, Boleto, crypto, customer balance) | `app/models/payment_provider_customers/stripe_customer.rb` | providers/stripe | logic done | |
| Stripe checkout in setup mode | `app/services/payment_provider_customers/stripe_service.rb` | providers/stripe | logic done | |
| Stripe bank transfer funding instructions as a system invoice section | `app/services/payment_provider_customers/stripe/sync_funding_instructions_service.rb` | providers/stripe | logic done | Gateway call done; the invoice section is libs/documents |
| Stripe payment intents (off session, idempotent, bank transfer per currency) | `app/services/payment_providers/stripe/payments/create_service.rb` | providers/stripe | logic done | Metadata keys are `tanvo_*`; a method Stripe no longer has is reported as `StaleMethodID` for the caller to discard |
| Stripe shared payment token | `app/services/payment_providers/stripe/payments/create_service.rb` | providers/stripe | logic done | Deviation: no `stripe_shared_payment_token` feature flag (lago `create_service.rb:149`); feature gating is not part of the core |
| Stripe payment link (checkout in payment mode) and expiry | `app/services/invoices/payments/stripe_service.rb` | providers/stripe | logic done | Invoice checkout metadata also carries the payable type and ID, as payment request links do in lago, so the succeeded webhook resolves both the same way |
| Stripe pre-authorization | `app/services/payment_providers/stripe/payments/authorize_service.rb` | providers/stripe | logic done | Choosing the method (`authorize_service.rb` `find_provider_method_id`) and the delayed cancel are the caller's |
| Stripe card details on payments | `app/services/payments/set_payment_method_data_service.rb` | providers/stripe | logic done | |
| Stripe webhooks: setup intent succeeded, payment intent succeeded, failed, canceled, payment method detached, refund updated, customer updated, dispute closed, cash balance funded | `app/services/payment_providers/stripe/handle_event_service.rb` | providers/stripe | logic done | `ParseWebhook` returns typed `payment.Event`s with `payment.Refs`; record lookups stay in apps/server. An unknown payable type is `payment.ErrUnknownPayable` (rejected) with no event, and an unmapped refund status `payment.ErrUnknownStatus`; the handler acknowledges either with a warning and updates nothing, as lago's NameError and validation failure update nothing (`webhooks/base_service.rb` `payment_service_klass`) |
| Adyen connection (API key, merchant account, live prefix, HMAC key) | `app/models/payment_providers/adyen_provider.rb` | providers/adyen | planned | |
| Adyen checkout as zero-amount pre-authorization link | `app/services/payment_provider_customers/adyen_service.rb` | providers/adyen | planned | |
| Adyen payments with stored payment method | `app/services/payment_providers/adyen/payments/create_service.rb` | providers/adyen | planned | |
| Adyen payment links | `app/services/invoices/payments/adyen_service.rb` | providers/adyen | planned | |
| Adyen webhooks: authorisation, cancellation, refund, refund failed, chargeback | `app/services/payment_providers/adyen/handle_event_service.rb` | providers/adyen | planned | |
| GoCardless connection by OAuth | `app/services/payment_providers/gocardless_service.rb` | providers/gocardless | planned | |
| GoCardless customers, mandate setup through billing requests | `app/services/payment_provider_customers/gocardless_service.rb` | providers/gocardless | planned | |
| GoCardless payments against the latest active mandate | `app/services/payment_providers/gocardless/payments/create_service.rb` | providers/gocardless | planned | |
| GoCardless webhooks: payments, refunds, mandates | `app/services/payment_providers/gocardless/handle_event_service.rb` | providers/gocardless | planned | |
| Cashfree connection and payment links | `app/services/invoices/payments/cashfree_service.rb` | providers/cashfree | planned | |
| Cashfree webhook (payment link paid) with signature check | `app/services/payment_providers/cashfree/` | providers/cashfree | planned | |
| Flutterwave connection and payment links | `app/services/invoices/payments/flutterwave_service.rb` | providers/flutterwave | planned | |
| Flutterwave webhook with transaction verification | `app/services/payment_providers/flutterwave/` | providers/flutterwave | planned | |
| Moneyhash connection (API key, flow id, signature key fetched) | `app/services/payment_providers/moneyhash_service.rb` | providers/moneyhash | planned | |
| Moneyhash customers and card tokenization checkout | `app/services/payment_provider_customers/moneyhash_service.rb` | providers/moneyhash | planned | |
| Moneyhash merchant-initiated recurring payments and payment links | `app/services/payment_providers/moneyhash/payments/create_service.rb` | providers/moneyhash | planned | |
| Moneyhash webhooks: intent processed and expired, purchase failed, pending authentication, successful, card token events | `app/services/payment_providers/moneyhash/` | providers/moneyhash | planned | |
| Cancel payment (Stripe, Adyen, GoCardless) | `app/services/payment_providers/cancel_payment_service.rb` | providers/* | in progress | Stripe done: `CancelPayment` returns the new state, and a zero result for an intent already final (`cancel_service.rb` logs and succeeds) |
| Update payment reference (Stripe) | `app/services/payment_providers/update_payment_reference_service.rb` | providers/stripe | logic done | |

### 1.35 Payment Requests, Dunning, Receipts and Refunds

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Payment requests grouping overdue invoices (same currency and billing entity) | `app/services/payment_requests/create_service.rb` | libs/billing | done | `payments.Service.CreateRequest`. Deviations: generating invoices are excluded (lago excludes deleted, `create_service.rb:107`); accepted deviation: an empty `invoice_ids` is a validation error (lago 404). The amount is a decimal string in major units, as `Money`. No email is sent yet. |
| Payment request amount net of credit note offsets | `app/services/payment_requests/create_service.rb` | libs/billing | done | |
| Payment request charged through the provider, or emailed | `app/services/payment_requests/payments/create_service.rb`, `app/mailers/payment_request_mailer.rb` | libs/billing | planned | |
| Payment request status updates | `app/services/payment_requests/update_service.rb` | libs/billing | planned | |
| Payment requests API and per-customer list | `app/controllers/api/v1/payment_requests_controller.rb` | apps/server | done | Filters `customer_id`, `payment_status`, `currency` and `billing_entity_id` (an ID or `code:<code>`, as the other lists; lago `billing_entity_codes`), an unknown billing entity being NotFound (`payment_requests_controller_spec.rb:163`); a code matches only live billing entities where lago also matches archived ones. Ordered newest first, then by ID (`base_query.rb:111`). The customer route's `customer_id` lists that customer's requests and an unknown one is NotFound (`customers/payment_requests_controller_spec.rb:15`), over Connect as over REST; `/v1/payment_requests` narrows to a customer with the `customer_id` filter term, which lists nothing for an unknown customer (`payment_requests_controller_spec.rb:118`). |
| Payment request webhooks `payment_request.created`, `payment_status_updated`, `payment_failure` | `app/services/webhooks/payment_requests/` | libs/webhooks | planned | |
| Dunning campaigns (thresholds per currency, days between attempts, maximum attempts, BCC emails) | `app/models/dunning_campaign.rb`, `app/services/dunning_campaigns/` | libs/billing, apps/server | done | Public API, not only the admin UI: `DunningCampaignService` over libs/billing/dunning. The list is by name, searchable on name and code and filtered on currency and `applied_to_organization` (`dunning_campaigns_query.rb`); `customers_count` and `applied_to_organization` follow `types/dunning_campaigns/object.rb`. `applied_to_organization` on create or update applies the campaign to the default billing entity, or removes it, resetting the customers falling back to it (`create_service.rb:19-24`, `update_service.rb:98-105`). Thresholds carry their ID and are matched by it on update; those left out are removed, down to none (`update_service.rb:43-66`). Deleting resets and unapplies the campaign's customers and billing entities (`destroy_service.rb`). Accepted gap: no premium or `auto_dunning` checks |
| Campaign applied to a billing entity or customer, with exclusion | `app/services/dunning_campaigns/process_customer_service.rb` | libs/billing | done | The customer's own campaign, else its billing entity's; excluded and archived customers and deleted campaigns are skipped, and an attempt is dropped when its campaign no longer applies (`process_attempt_service.rb:45-53`) |
| Threshold changes reset customers that no longer match | `app/services/dunning_campaigns/update_service.rb` | libs/billing | done | Customers excluded from dunning are left out, as they are never dunned |
| Dunning processing: per-currency attempts, threshold check, payment request | `app/services/dunning_campaigns/bulk_process_service.rb`, `process_attempt_service.rb` | libs/billing | done | `dunning.Service.ProcessCampaigns`, `ProcessCustomer` and `ProcessAttempt`, run by the hourly clock (:45) per organization with overdue invoices of customers open to dunning, then one job per customer and one per dunned threshold and billing entity. Archived customers are skipped, as lago's default scope does (`customer.rb:139`). Accepted gaps: no premium or `auto_dunning` checks, and the payment request is neither emailed nor charged through the provider until those job kinds exist (see payment request charging above) |
| Dunning webhook `dunning_campaign.finished` | `app/services/webhooks/dunning_campaigns/finished_service.rb` | libs/billing | done | Emitted on the customer when every dunned currency reached the maximum attempts; the payload follows `dunning_campaign_finished_serializer.rb`, deprecated `customer_external_id` included |
| Payment receipts for succeeded payments, numbered per customer | `app/services/payment_receipts/create_service.rb` | libs/billing | done | An existing receipt is returned (lago returns nil). |
| Payment receipt PDF and XML, emailed per billing entity settings | `app/services/payment_receipts/generate_pdf_service.rb`, `generate_xml_service.rb` | libs/documents | logic done | |
| Payment receipts API and resend email | `app/controllers/api/v1/payment_receipts_controller.rb` | apps/server | in progress | List (filter `invoice_id`, newest first, then by ID, `base_query.rb:111`), get and download are done; resend stays unimplemented until document emails are wired. |
| Payment receipt webhooks `payment_receipt.created`, `payment_receipt.generated` | `app/services/webhooks/payment_receipts/` | libs/webhooks | planned | |
| Refunds for credit notes through Stripe, GoCardless, Adyen | `app/services/credit_notes/refunds/` | providers/* | in progress | Port `payment.RefundGateway`, keyed by the mandatory credit note ID; Stripe done |
| Refund reason mapping to provider reasons | `app/services/credit_notes/refunds/stripe_service.rb` | providers/stripe | logic done | Stripe `canceled` refunds map to failed and `requires_action` to pending; lago copies the status and fails validation on either (`credit_note.rb:62`). Any other status is `payment.ErrUnknownStatus`, which callers do not persist, as lago's validation failure (`credit_notes/refunds/stripe_service_spec.rb:360`) |
| Refund status updates from provider webhooks | `app/services/credit_notes/refunds/` (`update_status`) | libs/billing | logic done | |

### 1.36 x402 Agent Payments

In v1.55.0 only the gate check is reachable (`config/routes/shared_api.rb:174`): the route check, the balance check and the payment requirements, over connections configured outside the API. The settlement, payment digest and chain services exist but nothing calls them. Tanvo implements that scope and leaves the settlement services unwired, as v1.55.0 does.

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| x402 connections (facilitator Coinbase CDP, payout addresses, networks, USDC, auto-create customers) | `app/models/x402/connection.rb` | libs/core | planned | No management API: v1.55.0 routes none |
| Networks Base, Base Sepolia, Solana mainnet and devnet; USDC assets per network | `app/services/x402/network.rb`, `app/services/x402/asset.rb` | libs/core | planned | |
| Gate check: can an agent use a metric now, or what must it pay | `app/services/x402/gate_checks/check_service.rb` | libs/billing | planned | Passes when balance − ongoing usage covers 1,000 estimated calls (`DEGRADED_CALL_HEADROOM`) |
| Gate check route check: the parent plan and its charges must be able to gate a prepaid wallet | `app/services/x402/gate_checks/route_check_service.rb` | libs/billing | planned | |
| `x402` API key scope for the gate check endpoint, and the `x402_connection` scope | `app/models/api_key.rb` | libs/auth | planned | No v1.55.0 endpoint uses `x402_connection` |
| x402-enabled wallets (`x402_enabled`) | `app/models/wallet.rb` | libs/core | planned | Stored; not settable through the wallet API, as in Lago |
| Invoice x402 columns (`x402_connection_id`, `x402_payment_token`) | `app/models/invoice.rb` | libs/core | planned | Columns only: nothing in v1.55.0 sets or reads them. The token has a unique index across all organizations (`db/structure.sql:10561`); the connection belongs to the same organization |
| Payment requirements from the facilitator | `app/services/x402/payment_requirements_service.rb` | libs/billing | planned | Reads the facilitator's `supported` kinds for the Solana fee payer |
| Facilitator client (verify, settle, supported) with per-request JWT | `app/services/x402/facilitator/coinbase_cdp_adapter.rb` | providers/* | planned | New provider module `providers/x402`; only `supported` is reachable, through the payment requirements |
| Settlements (verify, settle) | `app/models/x402/settlement.rb`, `app/services/x402/settlements/` | libs/billing | planned | Unwired, as in v1.55.0: no API, job or reconciliation calls them |
| On-chain readers through JSON-RPC (EVM, Solana) | `app/services/x402/chain/` | providers/* | planned | Unwired, as in v1.55.0: no reconciliation reads them |
| Payment digest for idempotency | `app/services/x402/payments/compute_digest_service.rb` | libs/billing | planned | Unwired, as in v1.55.0. The digest and transaction hash are unique across all organizations (Lago 18a741762) |

### 1.37 Accounting, CRM and Tax Integrations

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| NetSuite connection (account, token-based auth, RESTlet endpoint) | `app/models/integrations/netsuite_integration.rb` | providers/netsuite | planned | Direct API calls, no Nango (§3) |
| NetSuite subsidiaries | `app/services/integrations/aggregator/subsidiaries_service.rb` | providers/netsuite | planned | |
| NetSuite invoice reconciliation by transaction id | `app/services/integrations/aggregator/invoices/reconcile_service.rb` | providers/netsuite | planned | |
| NetSuite tax nexus, tax type and tax code mappings | `app/models/integration_collection_mappings/netsuite_collection_mapping.rb` | providers/netsuite | planned | |
| Xero connection | `app/models/integrations/xero_integration.rb` | providers/xero | planned | |
| Accounting sync settings: invoices, credit notes, payments | `app/models/integrations/` | libs/billing | planned | |
| Fetch provider items and accounts | `app/services/integrations/aggregator/items_service.rb`, `accounts_service.rb` | providers/* | planned | |
| Item mappings for add-ons and billable metrics, optionally per billing entity | `app/services/integration_mappings/` | libs/core, libs/billing, apps/server | done | `IntegrationService` List, Upsert and Delete integration mappings over libs/billing/integrations |
| Collection mappings (fallback item, coupon, subscription fee, minimum commitment, tax, prepaid credit, credit note, account, currencies) | `app/services/integration_collection_mappings/` | libs/core, libs/billing, apps/server | done | `IntegrationService` List, Upsert and Delete collection mappings |
| Accounting contacts create and update | `app/services/integrations/aggregator/contacts/` | providers/* | planned | |
| Invoice sync to accounting | `app/services/integrations/aggregator/invoices/create_service.rb` | providers/* | planned | |
| Credit note sync to accounting | `app/services/integrations/aggregator/credit_notes/create_service.rb` | providers/* | planned | |
| Payment sync to accounting | `app/services/integrations/aggregator/payments/create_service.rb` | providers/* | planned | |
| Synced resource records (external ids of invoices, payments, credit notes, subscriptions) | `app/models/integration_resource.rb` | libs/core | planned | |
| Manual resync of an invoice or credit note | `app/graphql/mutations/integrations/sync_invoice.rb` | libs/billing | planned | |
| HubSpot connection with custom objects and properties deployed | `app/services/integrations/hubspot/` | providers/hubspot | planned | |
| HubSpot companies or contacts per customer | `app/services/integrations/aggregator/companies/`, `contacts/` | providers/hubspot | planned | |
| HubSpot invoice and subscription records with associations | `app/services/integrations/aggregator/invoices/hubspot/`, `subscriptions/hubspot/` | providers/hubspot | planned | |
| Salesforce connection (instance) and invoice id sync | `app/services/integrations/salesforce/` | providers/salesforce | planned | |
| Anrok connection and tax quotes for drafts, previews and usage | `app/services/integrations/aggregator/taxes/invoices/create_draft_service.rb` | providers/anrok | planned | |
| Anrok draft tax cache | `app/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service.rb` | providers/anrok | planned | |
| Avalara connection with company lookup | `app/services/integrations/aggregator/taxes/avalara/fetch_company_id_service.rb` | providers/avalara | planned | |
| Finalized invoice tax reporting | `app/services/integrations/aggregator/taxes/invoices/create_service.rb` | providers/* | planned | |
| Tax void or negate on invoice void | `app/services/integrations/aggregator/taxes/invoices/void_service.rb`, `negate_service.rb` | providers/* | planned | |
| Credit note tax reporting | `app/services/integrations/aggregator/taxes/credit_notes/create_service.rb` | providers/* | planned | |
| Retry tax reporting and tax voiding | `app/graphql/mutations/integrations/` | libs/billing | logic done | |
| Integration retry policy (bad gateway, request limit, throttling, timeouts) | `app/jobs/integrations/` | libs/jobs | planned | |
| Integration webhooks `customer.accounting_provider_created`, `customer.accounting_provider_error`, `customer.crm_provider_created`, `customer.crm_provider_error`, `integration.provider_error` | `app/services/webhooks/integrations/` | libs/webhooks | planned | |
| One tax integration per customer | `app/models/integration_customers/base_customer.rb` | libs/core | planned | |
| Integration connections API (create, get, list, update, delete) | `app/services/integrations/`, `app/graphql/mutations/integrations/` | libs/billing, apps/server | done | `IntegrationService` over libs/billing/integrations. The code, name, sync settings and credentials can be updated; codes are unique per organization among the live payment providers and among the other integrations, as lago's two tables. Delete discards a payment provider (§1.33) and removes the others (`integrations/destroy_service.rb`). Items are listed by external name (`integration_items_query.rb:10`). Credentials are never returned; Anrok's `external_account_id` is derived from the API key (`types/integrations/anrok.rb:20-24`). The `integration.created`, `updated` and `deleted` security logs are written in the change's transaction through the `integrations.SecurityLogger` that apps/server injects, with lago's `integration_name` and `integration_type` and, on an update, the changed `code`, `name` and `settings` (sync flags included, secrets never) as `{deleted, added}`; the SSO connections write theirs in libs/auth |

### 1.38 Outbound Webhooks

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Webhook endpoints (up to 10 per organization, unique URL, name) | `app/models/webhook_endpoint.rb`, `app/services/webhook_endpoints/` | libs/webhooks | done | `webhooks.Endpoints` create, update, delete; the limit is refused with the rule `webhook_endpoints_limit_reached` (FailedPrecondition) where lago answers a 422. Event types are an explicit list or empty for all; the `["*"]` pattern is not accepted. Standard Webhooks secrets replace `signature_algo` (§3). The `webhook_endpoint.created`, `updated` and `deleted` security logs are written in the change's transaction through the `webhooks.SecurityLogger` that apps/server injects, with lago's `webhook_url` resources (`{deleted, added}` on a URL change) and no `signature_algo` |
| Event type subscription per endpoint (all by default) | `app/models/webhook_endpoint.rb`, `config/webhook_event_types.yml` | libs/webhooks | logic done | |
| Event type catalog with descriptions and categories | `config/webhook_event_types.yml` | libs/webhooks | done | `ListWebhookEventTypes` |
| Delivery with retries and backoff | `app/services/webhooks/send_http_service.rb` | libs/webhooks | logic done | |
| Signed payloads | `app/models/webhook.rb` | libs/webhooks | logic done | Standard Webhooks (§3) |
| Private address blocking with an opt-out | `lib/lago_http_client/` (`AddressGuard`) | libs/webhooks | logic done | |
| Delivery records with response, status, retries | `app/models/webhook.rb` | libs/webhooks | logic done | |
| Manual retry of a delivery | `app/services/webhooks/retry_service.rb` | libs/webhooks | logic done | |
| Delivery list with filters (status, event type, search) | `app/graphql/resolvers/webhooks_resolver.rb` | libs/webhooks | done | `ListWebhookDeliveries` filters by status, event type, `http_status` (a code, a class such as `5xx`, a range such as `200-204`, or `timeout`) and `updated_at` bounds (`from_date`, `to_date`), and searches the delivery and object IDs (`app/queries/webhooks_query.rb`). `timeout` keeps the failed deliveries without a response (`webhooks_query.rb:70-72`) |
| Delivery cleanup after 90 days | `app/jobs/clock/webhooks_cleanup_job.rb` | libs/webhooks | done | The daily clock job enqueues one purge job per organization with endpoints, deleting 1 000 deliveries per transaction under the organization's row-level security; lago deletes every organization's rows from the clock job (`app/jobs/clock/webhooks_cleanup_job.rb:19`). Like lago, retention is counted from `updated_at`, on the index `(organization_id, updated_at) INCLUDE (id)` |
| Webhook endpoints API | `app/controllers/api/v1/webhook_endpoints_controller.rb` | apps/server | done | `WebhookService` endpoint CRUD, deliveries Get, List and Retry. Delete answers an empty body where lago renders the endpoint. Lago's `/webhooks/public_key` and `json_public_key` are not served (§3). The signing secret is returned on create only |
| Event group billable metrics: `billable_metric.created`, `updated`, `deleted` | `app/services/webhooks/billable_metrics/` | libs/billing, libs/webhooks | done | Emitted by the metric service in its transaction |
| Event group plans: `plan.created`, `updated`, `deleted` | `app/services/webhooks/plans/` | libs/webhooks | planned | |
| Event group features: `feature.created`, `updated`, `deleted` | `app/services/webhooks/features/` | libs/billing, libs/webhooks | done | Emitted by the entitlement service in its transaction |
| Event group customers: `customer.created`, `updated`, `vies_check` | `app/services/webhooks/customers/` | libs/billing, libs/webhooks | done | Emitted by the customer service in its transaction; `customer.vies_check` by the VIES check job |
| Event group customer connections: `customer.payment_provider_created`, `payment_provider_error`, `checkout_url_generated`, `accounting_provider_created`, `accounting_provider_error`, `crm_provider_created`, `crm_provider_error`, `tax_provider_error` | `app/services/webhooks/payment_providers/`, `app/services/webhooks/integrations/` | libs/webhooks | planned | |
| Event group subscriptions: `subscription.started`, `updated`, `terminated`, `canceled`, `incomplete`, `termination_alert`, `trial_ended`, `usage_threshold_reached` | `app/services/webhooks/subscriptions/` | libs/webhooks | planned | |
| Event group invoices: `invoice.created`, `one_off_created`, `paid_credit_added`, `generated`, `drafted`, `ready_to_finalize`, `voided`, `deleted`, `resynced` | `app/services/webhooks/invoices/` | libs/webhooks | planned | |
| Event group invoice payments: `invoice.payment_status_updated`, `payment_overdue`, `payment_failure`, `payment_dispute_lost` | `app/services/webhooks/invoices/` | libs/webhooks | planned | |
| Event group fees: `fee.created`, `fee.tax_provider_error` | `app/services/webhooks/fees/`, `app/services/webhooks/integrations/taxes/` | libs/webhooks | planned | |
| Event group events: `events.errors` | `app/services/webhooks/events/validation_errors_service.rb` | libs/webhooks | planned | `event.error` not ported (§2) |
| Event group credit notes: `credit_note.created`, `generated`, `provider_refund_failure` | `app/services/webhooks/credit_notes/` | libs/webhooks | planned | |
| Event group wallets: `wallet.created`, `updated`, `terminated`, `depleted_ongoing_balance` | `app/services/webhooks/wallets/` | libs/webhooks | planned | |
| Event group wallet transactions: `wallet_transaction.created`, `updated`, `payment_failure` | `app/services/webhooks/wallet_transactions/` | libs/webhooks | planned | |
| Event group payments: `payment.succeeded`, `requires_action`, `payment_provider.error` | `app/services/webhooks/payments/`, `app/services/webhooks/payment_providers/` | libs/webhooks | planned | |
| Event group payment requests: `payment_request.created`, `payment_status_updated`, `payment_failure` | `app/services/webhooks/payment_requests/` | libs/webhooks | planned | |
| Event group payment receipts: `payment_receipt.created`, `generated` | `app/services/webhooks/payment_receipts/` | libs/webhooks | planned | |
| Event group dunning: `dunning_campaign.finished` | `app/services/webhooks/dunning_campaigns/` | libs/webhooks | planned | |
| Event group integrations: `integration.provider_error` | `app/services/webhooks/integrations/provider_error_service.rb` | libs/webhooks | planned | |
| Event group alerts: `alert.triggered` | `app/services/webhooks/usage_monitoring/` | libs/webhooks | planned | |
| Event group quotes: `quote.created`, `approved`, `voided` | `app/services/webhooks/quotes/` | libs/webhooks | planned | No premium gate |
| Event group order forms: `order_form.created`, `signed`, `expired`, `voided` | `app/services/webhooks/order_forms/` | libs/webhooks | planned | No premium gate |
| Event group orders: `order.created`, `executed` | `app/services/webhooks/orders/` | libs/webhooks | planned | No premium gate |

### 1.39 Inbound Webhooks

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Provider webhook endpoints per provider connection | `app/controllers/webhooks_controller.rb` | apps/server | planned | `POST /webhooks/{provider}/{connection_id}` |
| Signature verification per provider (Stripe, Adyen HMAC, GoCardless, Cashfree, Flutterwave, Moneyhash) | `app/services/payment_providers/*/validate_incoming_webhook_service.rb` | providers/* | planned | |
| Durable storage before processing (pending, processing, succeeded, failed) | `app/models/inbound_webhook.rb`, `app/services/inbound_webhooks/` | libs/webhooks | logic done | Every provider, not only Stripe and Moneyhash |
| Processing window and retry of stuck webhooks | `app/services/inbound_webhooks/process_service.rb`, `app/jobs/clock/inbound_webhooks_retry_job.rb` | libs/webhooks | logic done | |
| Inbound webhook cleanup after 90 days | `app/jobs/clock/inbound_webhooks_cleanup_job.rb` | libs/webhooks | planned | |

### 1.40 Streaming Destinations

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Destinations claiming event types (one destination per type) | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | planned | Configured by the operator: v1.55.0 has no API to create them, and neither does Tanvo |
| Event types `customer_usage.refreshed.v1`, `customer_full_usage.refreshed.v1` | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | planned | |
| Plans excluded from full-usage events | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | planned | |
| Kinesis destination with assumed role and partition key | `app/models/streaming_destinations/kinesis_destination.rb`, `lib/lago/kinesis/producer.rb` | libs/webhooks | planned | |
| Usage-refreshed envelope with wallets | `app/services/event_destinations/` | libs/webhooks | planned | |
| Delivery deduplication and logging (delivered, skipped, failed, superseded, dropped) | `app/jobs/deliver_event_job.rb`, `app/services/event_destinations/delivery_logger.rb` | libs/webhooks | planned | |

### 1.41 Quotes, Order Forms and Orders

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Quotes and quote versions with statuses | `app/models/quote.rb`, `app/models/quote_version.rb`, `app/services/quotes/`, `app/services/quote_versions/` | libs/billing, apps/server | done | `QuoteService` (`quotes.Service`): create, get, list, update owners, add image, and versions get, list, update, clone, approve, void (`quotes_controller.rb`, `quote_versions_controller.rb`, `quotes/versions_controller.rb`, GraphQL `Mutations::Quotes::*`, `Mutations::QuoteVersions::*`). Owners are membership IDs rather than user IDs. There is no premium gate or feature flag. Images and signed documents need a blob store; without one they are refused with `blob_store_not_configured`. A lock error is returned as is rather than as `concurrency_conflict` (`quotes/lock_service.rb`) |
| Order types subscription creation, subscription amendment, one-off, with validators | `app/services/quote_versions/validators/` | libs/billing | done | `quotes/validators` (JSON Schema structural validators and business validators) |
| Mention variables in quote content | `app/services/quotes/` | libs/billing, libs/documents | in progress | Computed and snapshotted at approval (`compute_mention_variables_service.rb`); the localizer (`mention_variables_localizer.rb`) belongs to documents and is not ported |
| Deal expiration | `app/services/quotes/` (`DealExpiration`) | libs/billing | done | Bounds the order form's `expires_at` and the order's `execute_at` (`after_deal_expiration`) |
| Order forms (created, signed, expired, voided) | `app/models/order_form.rb`, `app/services/order_forms/` | libs/billing, apps/server | done | `OrderFormService`: get, list (filter and search), sign (with a signed document data URI, execution mode and `execute_at`), void with a cascade to the quote version (`order_forms_controller.rb`) |
| Order form expiry | `app/jobs/clock/expire_order_forms_job.rb` | libs/billing | done | Generated order forms whose expiry date, in the customer's time zone, is reached; voids the quote version |
| Orders and scheduled execution | `app/models/order.rb`, `app/jobs/clock/execute_scheduled_orders_job.rb` | libs/billing, apps/server | done | `OrderService`: get, list, update (execution mode, `execute_at`), execute (`orders_controller.rb`, `Orders::*ExecuteService`). An execution creates the subscriptions, coupons and wallets, changes the amended subscription's plan to an override plan, or bills a one-off invoice. A failure rolls back, then marks the order failed with the error codes. The hourly clock enqueues one `billing.execute_order` per due order. The mode `execute_in_lago` is `EXECUTION_MODE_EXECUTE` |

### 1.42 Usage Attribution

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Usage attribution types, hierarchical and flat (`account_tree` flag) | `app/models/usage_attribution_type.rb`, `app/controllers/api/v1/usage_attribution_types_controller.rb` | libs/core, libs/billing, apps/server | done | `UsageAttributionService` Create, Get, List, Update and Delete over libs/billing/usageattribution. Types are read by ID or `code:<code>`, and the parent is a reference (`parent_id`) where lago takes `parent_code`. The `account_tree` flag is not a gate (`usage_attribution_types_controller.rb:118`). The list filter `root = false` (types with a kept parent) goes beyond lago, which reads only `roots: true` (`usage_attribution_types_query.rb:12`). Delete answers an empty body where lago renders the deleted type (`usage_attribution_types_controller.rb:43-52`) |
| Attribution values | `app/models/usage_attribution_value.rb` | libs/core, libs/store | done | Stored, read and deleted with their type; as in v1.55.0, nothing records them from events, so they only freeze the code, role and parent of their type |
| Attributed usage query | `app/controllers/api/v1/customers/attributed_usage_controller.rb` | libs/billing | planned | Read side only, as in v1.55.0: the query groups by `events_enriched.attribution_labels`, which nothing writes, so attributed usage is empty |

### 1.43 Data Exports

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| CSV exports of invoices, invoice fees, credit notes, credit note items with the list filters | `app/services/data_exports/` | libs/billing, apps/server | in progress | `DataExportService` Export* RPCs over libs/billing/exports; members only (lago's mutations need a user), with the `export.created` security log. The CSV columns follow `app/services/data_exports/csv/`; `progressive_billing_credit_amount_cents` is always written (no premium gate, `csv/invoices.rb:16`). The invoice filter takes the fields of lago's invoice export filters; the credit note filter also takes `customer_id` and `customer_external_id`. Add-on fee dates are the UTC dates of the stored boundaries. The security log is written in the export's transaction (`create_service.rb:28`). The `exports.*` jobs run on the exports queue; no blob store is wired yet, so the parts are rendered but the file is not stored and the combine job only logs it. The response carries the export's `id` and `status`, as lago's mutation result |
| Exports split into parts and combined | `app/models/data_export_part.rb` | libs/billing | logic done | Parts of 20 objects; the combine job is unique per export |
| Export files expire after 7 days | `app/models/data_export.rb` | libs/billing | logic done | |
| Export ready email | `app/mailers/data_export_mailer.rb` | libs/mail | logic done | |
| Export views (`exports_*`) of invoices, fees, credit notes, wallets, subscriptions, alerts, daily usages and the other business tables | `db/views/exports_*.sql` | libs/store | planned | Read-only PostgreSQL views, with the deletion records (§1.49), the contract of the warehouse sync |
| Warehouse sync of the export views | Lago's private data pipeline | ee/pipeline | planned | Separate process reading the views (Architecture §14) |

### 1.44 Analytics

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Gross revenue | `app/services/analytics/gross_revenues_service.rb` | libs/billing, libs/store, apps/server | done | `AnalyticsService`. Over PostgreSQL by the SQL of `app/models/analytics/`, by month (UTC), currency and billing entity (`billing_entity_id`); a fee paid in advance outside invoices counts for its subscription's billing entity. Amounts are summed exactly and truncated where lago truncates a float sum. An unknown billing entity is NotFound where lago ignores it (`app/controllers/api/v1/analytics/base_controller.rb:31-33`). No premium gate; filtered by `customer_id` (lago `external_customer_id`) |
| MRR | `app/services/analytics/mrrs_service.rb` | libs/billing, libs/store, apps/server | done | `AnalyticsService`. Over PostgreSQL by the SQL of `app/models/analytics/`, by month (UTC) and currency; amounts are summed exactly and truncated. An unknown billing entity is NotFound where lago ignores it (`app/controllers/api/v1/analytics/base_controller.rb:31-33`). No premium gate; fees with zero billed months are skipped; no customer filter, as lago |
| Invoice collection | `app/services/analytics/invoice_collections_service.rb` | libs/billing, libs/store, apps/server | done | `AnalyticsService`. Over PostgreSQL by the SQL of `app/models/analytics/`, by month (UTC), currency and payment status; amounts are summed exactly and truncated. An unknown billing entity is NotFound where lago ignores it (`app/controllers/api/v1/analytics/base_controller.rb:31-33`). No premium gate; no customer filter, as lago; `is_customer_tin_empty` keeps the customers without (or with) a tax identification number |
| Invoiced usage | `app/services/analytics/invoiced_usages_service.rb` | libs/billing, libs/store, apps/server | done | `AnalyticsService`. Over PostgreSQL by the SQL of `app/models/analytics/`, by month (UTC), currency and billable metric; amounts are summed exactly and truncated. An unknown billing entity is NotFound where lago ignores it (`app/controllers/api/v1/analytics/base_controller.rb:31-33`). No premium gate; no customer filter, as lago |
| Overdue balance | `app/services/analytics/overdue_balances_service.rb` | libs/billing, libs/store, apps/server | done | `AnalyticsService`. Over PostgreSQL by the SQL of `app/models/analytics/`, by payment due month (UTC), currency and billing entity (`billing_entity_id`); amounts are summed exactly and truncated. An unknown billing entity is NotFound where lago ignores it (`app/controllers/api/v1/analytics/base_controller.rb:31-33`). No premium gate; filtered by `customer_id` (lago `external_customer_id`) and `is_customer_tin_empty` |
| Analytics cache (4 hours) | `app/services/analytics/` | libs/billing | planned | Reports are computed on every request: no cache layer exists yet, so results are always current where lago's can be up to 4 hours old |
| MRR by plan, prepaid credits, revenue streams by customer and plan, usage and aggregated amounts | Data API (`app/services/data_api/`) | ee/analytics | planned | |
| Embedded dashboards | Superset embedding (`app/services/` superset) | ee/analytics | planned | |
| Forecasted usage | frontend-only premium flag | ee/analytics | planned | |
| Revenue recognition | frontend-only premium flag | ee/analytics | planned | |

### 1.45 Logs

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Activity logs (resource changes from API, admin UI, system) | `app/services/utils/activity_log.rb` | libs/core, libs/billing, libs/auth | in progress | Stored in PostgreSQL and queried by `auth.AuditLogs` (`activity_logs_query.rb`). `auditlog.ActivityProducer` declares the 75 activity types and 20 resource types of `clickhouse/activity_log.rb` and writes a log inside the business transaction through `kernel.Deps.LogActivity`, with the actor of the request (API key, user, or system for jobs; `activity_log.rb` `activity_source`, `user_id`) and the changed attributes of `*.updated` types, `updated_at` excepted. The object is the domain value in snake_case, not the v1 serializer. Lago produces after commit to Kafka. Only billing entities record theirs so far (`billing_entities.created`, `billing_entities.updated`) |
| Activity log retention per organization | `app/models/organization.rb` | libs/auth | done | Applied to listings, not to reading one entry (`activity_logs_query.rb`, `activity_logs_controller.rb`) |
| API logs of write requests | `app/services/utils/api_log.rb` | apps/server | done | The `APILogs` interceptor records every API key call with side effects, except the event and log services (`api_loggable.rb` `skip_audit_logs!`), off the request path: the call queues the log in a bounded buffer that a writer stores in PostgreSQL in batches, one transaction each, instead of through Kafka. A log that does not fit in the buffer, or whose batch fails, is dropped and counted by the `tanvo.api_log.dropped` metric (`reason` overflow or write_error); a reused request ID keeps the first log. Connect calls are logged as `post` with the procedure as path; an error response is `{code, message}` |
| Security logs (sign-ins, users, roles, API keys, integrations, webhook endpoints) with 90-day retention | `app/services/utils/security_log.rb` | libs/auth, apps/server | done | Written by the services through `auth.NewRepoSecurityLogger`, with the user and, outside API keys, the client device; entries older than 90 days are neither listed nor read (`clickhouse/security_log.rb:12`). No premium gate |
| Email sent activity | `app/services/utils/activity_log.rb` | libs/mail | logic done | |
| Logs API with filters | `app/controllers/api/v1/activity_logs_controller.rb`, `api_logs_controller.rb`, `security_logs_controller.rb` | apps/server | done | `LogService`: the filters of `activity_logs_query.rb`, `api_logs_query.rb` and `security_logs_query.rb` as filter expressions, `logged_at` bounds included; listing security logs needs an upper bound (`security_logs_query.rb:17`) |
| Logs stored in ClickHouse, or PostgreSQL without ClickHouse | `db/clickhouse_migrate/` | libs/eventstore | planned | Lago requires ClickHouse |

### 1.46 Customer Portal

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Portal access token scoped to one customer | `app/services/customer_portal/` | libs/auth, apps/server | done | Short-lived signed tokens, sent as the bearer credential of the portal RPCs. The `Auth` interceptor verifies them with `auth.PortalAuthenticator`, which also requires the customer to exist and not be deleted (lago `CustomerPortalUser`, `Customer.find_by`) |
| Portal customer profile view and update | `app/graphql/resolvers/customer_portal/` | apps/server | planned | |
| Portal invoices list and download | `app/graphql/resolvers/customer_portal/` | apps/server | planned | |
| Portal subscriptions and current usage | `app/graphql/resolvers/customer_portal/` | apps/server | planned | |
| Portal wallets and wallet top-up | `app/graphql/mutations/customer_portal/` | apps/server | planned | |
| Portal overdue balance and invoice collection | `app/graphql/resolvers/customer_portal/` | apps/server | planned | |
| Portal user interface | Lago front-end | apps/web | planned | |

### 1.47 Documents, E-invoicing and Email

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Invoice PDF (current template version 4 with partials) | `app/views/templates/invoices/v4/` | libs/documents | logic done | Typst templates (§3) |
| Credit note PDF | `app/views/templates/credit_notes/` | libs/documents | logic done | |
| Payment receipt PDF | `app/views/templates/payment_receipts/` | libs/documents | logic done | |
| Payment request email | `app/mailers/payment_request_mailer.rb`, `app/views/payment_request_mailer/requested.slim` | libs/mail | logic done | Also rendered as a document |
| Document locales de, en, es, fr, it, nb, pt-BR, sv, zh-TW | `config/locales/` | libs/documents | logic done | Also zh-CN, a Tanvo addition; Chinese documents need a Noto CJK font (fonts-noto-cjk), else rendering fails with `ErrMissingFont` |
| Branding watermark | `app/views/templates/` | libs/documents | logic done | No watermark; nothing to remove |
| UBL invoices (EN 16931, XRechnung 3.0, Peppol BIS 3) | `app/serializers/e_invoices/invoices/ubl/` | libs/documents | logic done | |
| CII invoices embedded as Factur-X / ZUGFeRD | `app/serializers/e_invoices/invoices/cii/` | libs/documents | logic done | |
| E-invoice code lists | `app/serializers/e_invoices/` | libs/documents | logic done | |
| E-invoices for credit notes and receipts | `app/serializers/e_invoices/credit_notes/`, `app/serializers/e_invoices/payments/` | libs/documents | logic done | |
| Document emails (invoice, credit note, receipt, payment request) | `app/mailers/` | libs/mail | logic done | |
| Resend document email | `app/services/emails/resend_service.rb` | libs/mail | logic done | |
| SMTP delivery and custom from address | `config/environments/production.rb` | libs/mail | logic done | |

### 1.48 Scheduled Jobs

Each Lago clock job becomes a River periodic job or a job scheduled at the instant it is due ([Architecture §9](architecture.md#9-jobs-and-scheduling-libsjobs)). `libs/jobs` defines every entry of the schedule; a row is done when the library that owns its work registers the handler. Every periodic job runs on the `clock` queue and dispatches per-organization jobs to the workload queues; some Lago clock jobs (subscription and contract activation, coupon and wallet termination, free trial billing) instead do the work of every organization in the clock job.

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Activate subscriptions and contracts, refresh drafts (every 5 minutes) | `clock.rb` | libs/jobs | in progress | Subscription activation and draft refresh wired; contracts to come |
| Subscription activity processing (every minute) | `clock.rb` | libs/jobs | done | Replaced by a delayed job per subscription and minute (§1.32); the clock kind is not registered |
| Refresh lifetime usages and wallet ongoing balances (every 5 minutes) | `clock.rb` | libs/jobs | done | Same selection as lago: lifetime usages with the invoiced usage flagged, customers flagged `awaiting_wallet_refresh` with an active wallet (`app/jobs/clock/refresh_lifetime_usages_job.rb:9-19`, `app/jobs/clock/refresh_wallets_ongoing_balance_job.rb:10`); lago's `without_tax_errors` has no equivalent until error details exist. Wallets are also refreshed from usage refresh signals (§1.29) |
| Terminate ended subscriptions, validate events (hourly :05) | `clock.rb` | libs/jobs | done | Both wired; event validation enqueues one job per organization and clock hour |
| Subscription billing (hourly :10) | `clock.rb` (`SubscriptionsBiller`) | libs/jobs | done | One billing job per organization with active subscriptions and tick, which enqueues one job per subscription due |
| Billing segments create and process (hourly :12, :17) | `clock.rb` | libs/jobs | in progress | |
| API key usage, daily usages (hourly :15) | `clock.rb` | libs/jobs | done | One daily usage job per organization and tick |
| Expire incomplete subscriptions, finalize invoices (hourly :20) | `clock.rb` | libs/jobs | done | |
| Mark invoices overdue (hourly :25) | `clock.rb` | libs/jobs | done | |
| Retry generating invoices, terminate coupons (hourly :30) | `clock.rb` | libs/jobs | done | One job per organization with a subscription invoice generating for more than a day without an `invoice_generation_error` (`app/jobs/clock/retry_generating_subscription_invoices_job.rb:9-27`) |
| Free trial billing (hourly :35) | `clock.rb` | libs/jobs | done | |
| Expire order forms, cancel abandoned payments (hourly :40) | `clock.rb` | libs/jobs | in progress | Order form expiry wired; abandoned payments are cancelled with the payment providers (phase 5) |
| Terminate wallets, execute scheduled orders, dunning (hourly :45) | `clock.rb` | libs/jobs | done | Wallet termination, scheduled orders and dunning wired |
| Termination alerts, terminate recurring rules (hourly :50) | `clock.rb` | libs/jobs | done | Recurring rules terminated with the wallets, by one job |
| Interval wallet top-ups (hourly :55) | `clock.rb` | libs/jobs | done | |
| Retry failed invoices, retry inbound webhooks (every 15 minutes) | `clock.rb` | libs/jobs | in progress | One `billing.retry_tax_failed_invoices` per organization with a failed invoice whose `tax_error_message` contains `API limit` (`app/jobs/clock/retry_failed_invoices_job.rb:8-14`); the inbound webhook retry is not wired yet |
| Webhook, inbound webhook, deletion record cleanups (daily) | `clock.rb` | libs/jobs | in progress | Webhook delivery and deletion record cleanups wired, one purge job per organization; inbound webhook cleanup to come |
| Subscription refresh queue consumption (every 10 seconds) | `clock.rb` | libs/jobs | done | Replaced by the `usage.refresh` consumer, which passes each signal to billing at once (subscription activity and wallet refresh) |
| Queues per workload | `config/sidekiq/` | libs/jobs | logic done | River queues sharded by organization, with per-organization token buckets; periodic jobs on an unsharded `clock` queue |

### 1.49 Platform Services

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Encryption of provider secrets and integration credentials | `app/models/concerns/secrets_storable.rb` | libs/store | planned | AES-256-GCM envelopes |
| Deletion records for downstream sync | `db/structure.sql` (`record_deletions`, `record_deletion()`), `db/views/exports_record_deletions_v01.sql`, `app/jobs/clock/record_deletions_cleanup_job.rb` | libs/store, libs/billing | in progress | Tombstones (`export.RecordDeletion`) of deleted fees, invoice subscriptions, invoice applied taxes and credit note applied taxes (fee taxes are a column of their fee). Lago writes them with deferred `AFTER DELETE` triggers; the schema has no triggers, so the store notes the rows a statement may delete and writes the tombstones of those gone as the last statement before the commit, all stamped with one `clock_timestamp()`: a rollback writes none and a late committer is stamped later, as with the triggers. The memstore compares the tracked rows before and after the commit. `billing.Engine.CleanRecordDeletions` deletes them one calendar month after, 10 000 per transaction, daily at 01:20. The `exports_record_deletions` view comes with the export views (§1.43); deletion events also in `audit_events` |
| Error details on invoices (not provided, tax error, tax voiding error, invoice generation error) | `app/models/error_detail.rb` | libs/core | planned | |
| Outbound HTTP client with timeouts, retries and SSRF guard | `lib/lago_http_client/` | libs/webhooks | planned | Shared by providers through `libs/core` port |
| Change history of business objects | PaperTrail (`app/models/concerns/paper_trail_traceable.rb`) | libs/core | planned | `audit_events` |

### 1.50 Lago Gates and Their Tanvo Placement

| Lago gate | Kind | Tanvo placement | Notes |
| :--- | :--- | :--- | :--- |
| `License.premium?` (plan overrides, adjusted fees, payment requests, Anrok, analytics, preview, bulk dunning) | license | core | |
| `progressive_billing`, `lifetime_usage`, `granular_lifetime_usage`, `projected_usage`, `preview` | premium integration | core | |
| `netsuite`, `xero`, `hubspot`, `salesforce`, `avalara`, `anrok` | premium integration | core | providers/* |
| `okta`, `entra_id` | premium integration | core | libs/auth |
| `auto_dunning`, `manual_payments`, `issue_receipts`, `beta_payment_authorization` | premium integration | core | |
| `api_permissions`, `custom_roles`, `security_logs` | premium integration | core | |
| `multi_entities_pro`, `multi_entities_enterprise` | premium integration | core | No entity limit |
| `from_email`, `remove_branding_watermark` | premium integration | core | `from_email` is the organization setting `from_email_enabled` |
| `events_targeting_wallets` | premium integration | core | |
| `order_forms` | premium integration and flag | core | |
| `revenue_share` | premium integration | core | Partner accounts and self-billing |
| `revenue_analytics` | premium integration | core | Daily usages and analytics APIs in the core; dashboards in ee/analytics |
| `analytics_dashboards`, `forecasted_usage`, `revenue_recognition` | premium integration (frontend only) | ee/analytics | |
| Data API, Superset | private cloud service | ee/analytics | |
| Finance assistant, AI conversations | private cloud service | ee/assistant | With an MCP server |
| CS admin tooling | private cloud service | ee | Organization toggles become `tanvo admin` commands |
| Data pipeline (warehouse sync of `exports_*` views) | private cloud service | ee/pipeline | The views (§1.43) and deletion records (§1.49) are in the core |
| Feature flags `postgres_enriched_events`, `multi_connection`, `product_catalog`, `realtime_usage`, `account_tree`, `x402_payments`, `stripe_shared_payment_token`, `skip_credit_invoice_auto_payment_delay` | feature flag | core | Shipped without flags; the last becomes a billing entity setting |
| Organization column `custom_aggregation` | column flag | not ported | Gates `custom_agg`, deprecated (§2) |

---

## 2. Deprecated and Not Ported

Items that Lago itself marks deprecated, keeps only for old data, or carries as dead code. Tanvo has no Lago data to read, so none of them are ported. Ported specs that exercise them are listed under `skipped` with a reference to this section ([Testing §2](testing.md)).

| Item | Evidence | Reason |
| :--- | :--- | :--- |
| `custom_agg` aggregation and the custom charge model | `app/services/billable_metrics/aggregations/custom_service.rb` ("NOTE: DEPRECATED service"); `app/services/charge_models/custom_service.rb` only reads its result | Deprecated by Lago; runs customer Ruby code in a sandbox (`LagoUtils::RubySandbox`). Expressions (§1.9) and the dynamic charge model cover per-event computation |
| `recurring_count_agg` aggregation | `app/models/billable_metric.rb` (value 4 removed from the enum) | Removed by Lago; recurring is a flag on other aggregations |
| Groups and group properties | `app/models/group.rb`, `app/models/group_property.rb`; `cached_aggregations.group_id`, `fees.group_id` | Replaced by billable metric filters and charge filters |
| `grouped_by` charge property | `app/models/charge.rb`, `app/services/charges/` | Alias of `pricing_group_keys` |
| Billable metric index counters | `app/models/billable_metric.rb` ("DEPRECATED since 2024-11-22") | Unused |
| `billable_metrics.properties` | `db/structure.sql` | Unused column |
| Uncoded charge filter cascade path | `app/services/charge_filters/create_or_update_batch_service.rb` | Kept for filters created before codes existed |
| `organizations.api_key` | `db/structure.sql` | Replaced by API keys |
| Ignored organization columns `clickhouse_aggregation`, `pre_filter_events` | `app/models/organization.rb` (`ignored_columns`) | Removed settings |
| Organization-level document settings (`eu_tax_management`, `email_settings`, per-organization numbering, grace period, payment term) | `app/models/organization.rb`, `app/jobs/invoices/update_grace_period_from_billing_entity_job.rb` | Moved to billing entities |
| `taxes.applied_to_organization` | `app/models/tax.rb` | Compatibility shim for organization-level taxes |
| Data API base controller | `app/controllers/data_api/base_controller.rb` | Dead code |
| Flags `wallet_traceability`, `x402_reservation_counter_disabled`, `fixed_charge_usage_delta_migration` | `app/models/organization.rb` | Unused or migration-only flags |
| Triggered alert `resolved` and seeded kinds | `app/models/usage_monitoring/triggered_alert.rb` | Never produced in v1.55.0: the triggered alert's `kind` (`triggered`, `resolved`, `seeded`), `in_alarm_thresholds` and `fully_resolved` columns exist, and nothing writes a resolved or seeded alert or sends `alert.resolved` |
| Admin REST controllers (local only) | `app/controllers/admin/` | Replaced by `tanvo admin` |
| `customers.vat_rate`, `billing_entities.vat_rate` | `db/structure.sql` | Replaced by taxes |
| `billing_entities.archived_at`, `destroyBillingEntity` no-op mutation | `app/models/billing_entity.rb`, `app/graphql/mutations/billing_entities/` | Unused |
| GraphQL `providerCustomer` and the per-provider `*Customer` fields; singular `provider_customer` input | `app/graphql/types/customers/` | Replaced by connection lists |
| `RetryViesCheckJob` | `app/jobs/customers/retry_vies_check_job.rb` | Superseded by the VIES check job with backoff |
| `invoice_footer` assigned on a customer | `app/services/customers/update_service.rb` | There is no such column (§4) |
| `subscriptions.incompleted_at` and `cancelation_reason` spelled with one l | `db/structure.sql` | Duplicates of the correctly named columns |
| `overriden` filter alias | `app/controllers/api/v1/subscriptions_controller.rb` | Misspelled alias of `overridden` |
| `already_billed_on_day_one?` guard | `app/services/subscriptions/organization_billing_service.rb` | Workaround for old scheduling; per-subscription scheduling makes it unnecessary |
| `events.customer_id`, `events.subscription_id` | `db/structure.sql` | Events reference external ids |
| `quantified_events` | `app/models/quantified_event.rb` | Replaced by recurring aggregations |
| Dropped tables `events_enriched_expanded`, `events_aggregated` | `db/clickhouse_migrate/` | Dropped by Lago |
| Enriched store migration paths | `app/services/events/stores/` (`enriched_store_*`) | Migration-only |
| `Events::CreateBatchJob` | `app/jobs/events/create_batch_job.rb` | Unused |
| Event deletion when a billable metric is destroyed | `app/jobs/billable_metrics/delete_events_job.rb` (body is `true`), `app/services/events/delete_for_metric_service.rb` (no caller) | lago stopped deleting events (d76413e50); events are retained by the store's retention policy |
| `event.error` webhook | `config/webhook_event_types.yml` (`deprecated: true`) | Replaced by `events.errors` |
| Subscription refresh queue version argument | `app/jobs/clock/consume_subscription_refreshed_queue_job.rb` | Compatibility argument |
| `LAGO_REDIS_CACHE_*` settings, `LAGO_REDIS_STORE_TLS` fallback | `config/` | Unused or deprecated configuration |
| Quote `share_token` | `app/models/quote.rb` (`ignored_columns`) | Ignored column |
| Invoice type `add_on` | `app/models/invoice.rb` | Replaced by `one_off` |
| `applied_add_ons`, `fees.applied_add_on_id` | `app/models/applied_add_on.rb` | Replaced by one-off invoices |
| `fees.negative_amount_cents`, `duplicated_in_advance` | `db/structure.sql` | Unused |
| `invoices.organization_sequential_id` | `app/models/invoice.rb` | Replaced by billing-entity sequences |
| Preview contexts `refresh`, `draft` | `app/services/invoices/preview_service.rb` (`DEPRECATED_CONTEXTS`) | Deprecated contexts |
| `PrepaidCreditJob` default argument | `app/jobs/invoices/prepaid_credit_job.rb` | Compatibility argument |
| `UpdateAllEuTaxesJob` | `app/jobs/taxes/update_all_eu_taxes_job.rb` | Dead job |
| Invoice PDF templates v1–v3 | `app/views/templates/invoices/v1.slim`–`v3.slim` | Only kept for old invoices |
| `/download` endpoint aliases | `config/routes.rb` | Aliases |
| `dunning_campaigns.applied_to_organization` column | `app/models/dunning_campaign.rb` (`ignored_columns`) | The API flag reads and sets the default billing entity's campaign (§1.35) |
| Integration resource type `sales_order_deprecated` | `app/models/integration_resource.rb` | Deprecated by Lago |
| `payments.invoice_id` | `db/structure.sql` | Replaced by polymorphic payable |
| `organizations.webhook_url` | `db/structure.sql` | Replaced by webhook endpoints |
| `webhooks.payload` column | `app/models/webhook.rb` | Fallback for rows before payloads moved to storage |
| `GET /api/v1/webhooks/public_key` | `app/controllers/api/v1/webhooks_controller.rb` ("Deprecated") | Replaced by `json_public_key`; Tanvo has no signing key to publish (§3) |
| `SendWebhookJob` `webhook_id` argument | `app/jobs/send_webhook_job.rb` (marked temporary) | Temporary path |
| NetSuite `legacy_script` setting | `app/models/integrations/netsuite_integration.rb` | Old RESTlet |
| Refund reason `subscription_activation_expired` | `app/models/refund.rb` | No producer; Tanvo adds it only when activation refunds are implemented |
| Multi-connection backfill task | `lib/tasks/multi_customer_connections.rake` | Migration-only |

---

## 3. Replaced by Different Design

### 3.1 Conventions that apply everywhere

| Lago | Tanvo | Reason |
| :--- | :--- | :--- |
| GraphQL for the admin UI plus REST v1/v2 for clients | One Connect API with REST through Vanguard ([Architecture §4](architecture.md#4-contracts-and-api-libsproto-apps-server)) | One contract for every client; the admin UI uses the public API |
| Resources addressed by `external_id` or `code` in paths | Tanvo IDs, with `external_id:` and `code:` prefixed IDs accepted | Stable IDs; customer identifiers still work |
| UUIDv4 primary keys | UUIDv7 | Index locality |
| Offset pagination | Cursor pagination with AIP-160 filters | Stable pages under concurrent writes |
| `idempotency_records` keyed by request contents | `Idempotency-Key` header, responses kept 24 hours | Standard client-controlled idempotency |
| Soft delete on `deleted_at` (Discard) | Lifecycle statuses and explicit deletes | No hidden rows in every query |
| PaperTrail versions | `audit_events` in the same transaction, feeding webhooks and activity logs | One record of change |
| Rails attribute encryption | AES-256-GCM envelopes with configuration or KMS keys | Key rotation, no framework coupling |
| Feature flags and premium gates | Everything shipped in the core; cloud services in `ee/` (§1.50) | Open core without license checks |
| Lago license server | Offline Ed25519 license key, only for `ee/` | No phone-home |
| Segment tracking | Removed | No telemetry from installations |
| ActionCable GraphQL subscriptions | Connect server streaming | Authenticated streams |

### 3.2 Components

| Lago | Tanvo | Reason |
| :--- | :--- | :--- |
| JWT HS256 user sessions renewed by header | Opaque session tokens stored hashed | Revocable sessions |
| API key value stored in clear | SHA-256 hash, shown once, rotation with overlap | Credential hygiene |
| Separate Google, Okta and Entra ID implementations | One OIDC identity provider with presets, plus generic OIDC | One code path |
| Customer portal tokens from `MessageVerifier` (12 hours) | Short-lived signed tokens scoped to one customer | Narrower exposure |
| Webhook signatures JWT RS256 (default) or HMAC with the organization key | Standard Webhooks signatures with per-endpoint secrets and rotation | Standard, verifiable with existing libraries |
| Webhook payloads gzipped in ActiveStorage | Payloads stored with the delivery record | No blob store needed for deliveries |
| Sidekiq queues and the clockwork clock process | River jobs and periodic jobs run by an elected leader | Transactional enqueue, no extra process |
| Hourly scans of every subscription for billing and activation | Per-subscription jobs at the due instant plus hourly reconciliation | Work proportional to what is due |
| `pg_partman` partitions | Native partitions managed by Tanvo migrations and jobs | No extension |
| RisingWave `usage_buckets_15m` | 15-minute usage buckets maintained by the ingest consumers in PostgreSQL or ClickHouse | No extra system |
| Separate events-processor service with Debezium CDC and a Badger cache | Ingest consumers in the server's `consumer` role with an in-memory catalog cache refreshed by change notifications | Fewer moving parts |
| Kafka pipeline for activity, API and security logs | `audit_events` and log writers into ClickHouse or PostgreSQL | Works without Kafka |
| Nango as an integration broker | Direct vendor API clients in `providers/*` | No third-party broker in the data path |
| Gotenberg HTML-to-PDF | Typst child process producing PDF/A-3b | Single binary, archival PDF |
| pdfcpu to embed Factur-X XML | Typst PDF/A-3 attachments | One rendering step |
| Two catalogs (plans and charges, product catalog behind a flag) | One catalog covering both (§1.14) | No parallel models |
| Tier ranges v1 (inclusive, next starts at +1) and v2 (adjacent) | Adjacent ranges only | One convention |
| Single payment provider per customer, multi-connection unfinished | Several connections per customer with defaults per category | Completes Lago's design |
| Inbound webhook storage only for Stripe and Moneyhash | Every provider's webhooks are stored and retried | Uniform reliability |
| Data API and Superset | `ee/analytics` | Cloud-only in Lago |
| Mistral-based finance assistant and AI conversations | `ee/assistant` with an MCP server | Cloud-only in Lago |
| CS admin and admin REST | `tanvo admin` commands; support tooling in `ee/` | No unauthenticated local endpoints |
| `LAGO_CREATE_ORG` bootstrap variables | `tanvo admin bootstrap` | Explicit command |
| OAuth proxy for provider callbacks | Callbacks served by `apps/server` | Self-contained |
| `throttling` gem with Redis | Per-provider token buckets | Works without Redis |
| API key last use written to the Rails cache per request, copied hourly | Uses collected per API process, recorded every minute in `api_key_uses`, applied hourly per organization | Works without Redis; per-organization work under row-level security |
| Yabeda metrics | OpenTelemetry and `/metrics` | Standard instrumentation |

---

## 4. Upstream Bugs to Not Reproduce

Ported tests encode the corrected behavior and cite the Lago location.

| Lago behavior | Lago source | Tanvo behavior |
| :--- | :--- | :--- |
| Credit note refund failure webhook payload says `credit_note.refund_failure` while the event key and catalog say `credit_note.provider_refund_failure`, so endpoint filtering and payload disagree | `app/services/webhooks/credit_notes/payment_provider_refund_failure_service.rb`, `config/webhook_event_types.yml` | One event name, `credit_note.provider_refund_failure`, in the payload and the filter |
| Adyen webhook base service reads `notificationItems` from an item the handler already unwrapped | `app/services/payment_providers/adyen/webhooks/base_service.rb` | Each notification item is parsed once |
| Moneyhash provider references `live_prefix`, which it never defines | `app/models/payment_providers/moneyhash_provider.rb` | No undefined settings; the environment comes from the connection |
| Moneyhash payment URL amount `total_due_amount_cents.div(100).to_f` truncates cents | `app/services/invoices/payments/moneyhash_service.rb` | Exact decimal amount from `libs/money` |
| Reuse of a processing invoice payment matches `total_amount_cents` while payments are created for `total_due_amount_cents` | `app/services/invoices/payments/create_service.rb` | Matches on the amount due |
| Flutterwave and Moneyhash charge `total_amount_cents` while other providers charge the amount due | `app/services/invoices/payments/flutterwave_service.rb`, `moneyhash_service.rb` | Every provider charges the amount due |
| Invoice payment status has no processing state; processing payments are shown as pending | `app/services/payment_requests/payments/create_service.rb` (code note) | Invoice payment status includes `processing` |
| Okta and Entra ID sign-in create a membership without a role | `app/services/auth/okta/`, `app/services/auth/entra_id/` | Memberships always have a role; OIDC sign-up assigns the configured default role |
| Google sign-in error code `invite_email_mistmatch` | `app/services/auth/google_service.rb` | `invite_email_mismatch` |
| GraphQL ActionCable channel accepts unauthenticated connections | `app/channels/graphql_channel.rb` | Streams require a session or API key |
| Analytics resolvers check premium inconsistently | `app/graphql/resolvers/analytics/` | No checks; one permission per analytics RPC |
| Taxes update and destroy mutations require `taxes:create` | `app/graphql/mutations/taxes/` | `taxes:update` and `taxes:delete` |
| Subscription time zone SQL falls back from customer to organization, skipping the billing entity | `app/models/subscription.rb` (`*_in_timezone_sql`) | Customer → billing entity → organization, the same as in Ruby code |
| Charge period end leaves a one-second gap (`charges_from` TODO) | `app/services/subscriptions/dates_service.rb` | Half-open periods `[from, to)` with no gap or overlap |
| Customer update assigns `invoice_footer`, which has no column | `app/services/customers/update_service.rb` | Field not accepted on customers; footer is a billing entity setting |
| Coupons limited to billable metrics are not applied in invoice previews (TODO) | `app/services/invoices/preview_service.rb` | Previews apply every coupon as a real invoice would |
| Credit note validators `valid_invoice_type?` and `valid_global_amount?` are defined but never called | `app/services/credit_notes/validate_service.rb` | Both checks enforced |
| Event rows are hard-deleted when enqueuing fails because the unique index ignores `deleted_at` | `app/services/events/create_service.rb` | Event write and follow-up work commit in one transaction; nothing to undo |
| Webhook cleanup deletes delivery rows but not their stored payload blobs | `app/jobs/clock/webhooks_cleanup_job.rb` | Payloads are deleted with their deliveries |
| Cashfree checkout URL silently returns nothing | `app/services/payment_provider_customers/cashfree_service.rb` | Unsupported operations return an explicit `unimplemented` error |
| EU territory postcode patterns match anywhere in the zipcode, so Lisbon `1940123` gets Madeira's VAT rate | `app/services/customers/eu_auto_taxes_service.rb` | Patterns match the start of the normalized zipcode |
