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

A commit that implements a feature changes its status here in the same commit ([Development](development.md)).

---

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
| Organization settings read and update API | `app/controllers/api/v1/organizations_controller.rb` | apps/server | planned | |
| Organization slug (used in document numbers and receipts) | `app/models/organization.rb` | libs/core | planned | |
| Audit log retention period setting (`audit_logs_period`) | `app/models/organization.rb` | libs/core | planned | |
| Maximum wallets per customer setting | `app/models/organization.rb` | libs/billing | planned | |
| Allowed authentication methods (email/password, Google, Okta, Entra ID) per organization | `app/models/organization.rb`, `app/services/organizations/update_service.rb` | libs/auth | planned | Okta and Entra ID become OIDC connections; generic OIDC added |
| Email to admins when authentication methods change | `app/mailers/organization_mailer.rb` | libs/mail | planned | |
| Per-organization HMAC key for webhook signing | `app/models/organization.rb` | libs/webhooks | planned | Replaced by per-endpoint Standard Webhooks secrets (§3) |
| Organization bootstrap at first start | `config/initializers/` (`LAGO_CREATE_ORG`, `LAGO_ORG_USER_*`) | apps/server | planned | `tanvo admin bootstrap` command instead of environment variables |
| Event store selection per organization (PostgreSQL or ClickHouse) | `app/models/organization.rb` (`clickhouse_events_store`) | libs/eventstore | planned | Installation setting with per-organization override |

### 1.3 Billing Entities

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Billing entity CRUD (legal identity, address, logo, currency, time zone, locale) | `app/models/billing_entity.rb`, `app/services/billing_entities/` | libs/core | planned | Archive instead of Lago's no-op destroy |
| Default billing entity per organization and per-customer assignment | `app/models/billing_entity.rb`, `app/services/customers/` | libs/core | planned | |
| Number of billing entities per organization (1, 2 or unlimited by plan) | `app/models/organization.rb` (`MULTI_ENTITIES_MAX`) | libs/core | planned | No limit in the core |
| Document numbering per customer or per billing entity | `app/services/billing_entities/change_invoice_numbering_service.rb` | libs/billing | planned | |
| Document number prefix | `app/models/billing_entity.rb` | libs/billing | planned | |
| Invoice grace period, payment term, invoice footer | `app/models/billing_entity.rb` | libs/billing | planned | |
| Issuing date anchor and adjustment settings | `app/models/billing_entity.rb` | libs/billing | planned | |
| Finalize zero-amount invoices setting | `app/models/billing_entity.rb` | libs/billing | planned | |
| EU tax management flag and automatic EU taxes | `app/services/billing_entities/`, `app/services/taxes/auto_generate_service.rb` | libs/tax | planned | |
| E-invoicing enablement for supported countries (France, Germany) | `app/models/billing_entity.rb` | libs/documents | planned | |
| Email settings (which documents are emailed) and from-address | `app/models/billing_entity.rb` (`email_settings`) | libs/mail | planned | |
| Taxes applied to a billing entity (apply, remove, manage) | `app/services/billing_entities/taxes/` | libs/tax | planned | |
| Applied dunning campaign per billing entity | `app/services/billing_entities/update_applied_dunning_campaign_service.rb` | libs/billing | planned | |
| Billing entity API | `app/controllers/api/v1/billing_entities_controller.rb` | apps/server | planned | |

### 1.4 Users, Memberships, Roles and Invites

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Users and memberships (active, revoked) across organizations | `app/models/user.rb`, `app/models/membership.rb` | libs/auth | planned | |
| Predefined roles admin, finance, manager | `app/models/role.rb`, `config/permissions.yml` | libs/auth | planned | |
| Custom roles with permission sets | `app/services/roles/` | libs/auth | planned | |
| Permission catalog (every resource and action) | `config/permissions.yml` | libs/auth | planned | Permission strings `resource:action` declared per RPC |
| Membership rules: cannot revoke own membership, cannot remove last admin, only admins grant admin | `app/services/memberships/` | libs/auth | planned | |
| Invites with token, roles, accept, revoke, update | `app/models/invite.rb`, `app/services/invites/` | libs/auth | planned | |
| Password reset by email (30-minute token) | `app/services/password_resets/` | libs/auth | planned | |
| Sign-up toggle for self-hosted installations | `LAGO_DISABLE_SIGNUP`, `app/services/users_service.rb` | apps/server | planned | Configuration option |

### 1.5 Authentication and Sessions

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Email and password sign-in and registration | `app/services/users_service.rb`, `app/graphql/mutations/` | libs/auth | planned | argon2id hashes; opaque sessions instead of JWT (§3) |
| Google sign-in, sign-up and invite acceptance | `app/services/auth/google_service.rb` | libs/auth | planned | Through the OIDC identity provider |
| Okta sign-in (authorize, token, userinfo, state check) | `app/services/auth/okta/` | libs/auth | planned | OIDC connection; state stored server-side |
| Entra ID sign-in with tenant and additional domains | `app/services/auth/entra_id/` | libs/auth | planned | OIDC connection |
| Generic OIDC provider | — | libs/auth | planned | New in Tanvo |
| Okta and Entra ID integration configuration (client, domain, host) | `app/models/integrations/okta_integration.rb`, `entra_id_integration.rb` | libs/auth | planned | |
| Refuse removing the last enabled sign-in method | `app/services/integrations/okta/destroy_service.rb` | libs/auth | planned | |
| Known devices and new-device security log entry | `app/models/user_device.rb`, `app/services/user_devices/` | libs/auth | planned | |
| OAuth callback for GoCardless connection | `app/services/payment_providers/gocardless_service.rb` (`LAGO_OAUTH_PROXY_URL`) | providers/gocardless | planned | Callback served by `apps/server`; no proxy |

### 1.6 API Keys and API Conventions

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Multiple named API keys per organization | `app/models/api_key.rb`, `app/services/api_keys/` | libs/auth | planned | Stored as SHA-256 hashes and shown once |
| Per-key permissions by resource (read, write) | `app/models/api_key.rb` (`RESOURCES`) | libs/auth | planned | Same permission strings as roles |
| Key expiry and rotation | `app/services/api_keys/rotate_service.rb` | libs/auth | planned | Rotation keeps the old key valid for an overlap window |
| Refuse destroying the last non-expiring key | `app/services/api_keys/destroy_service.rb` | libs/auth | planned | |
| Last-used tracking | `app/jobs/clock/api_keys/track_usage_job.rb` | libs/auth | planned | |
| Emails when keys are created, rotated or destroyed | `app/mailers/api_key_mailer.rb` | libs/mail | planned | |
| Idempotency of write requests | `app/models/idempotency_record.rb`, `app/services/idempotency_records/` | apps/server | planned | `Idempotency-Key` header; responses kept 24 hours |
| Health and readiness endpoints | `config/routes.rb` (`/health`, `/ready`) | apps/server | planned | `/healthz`, `/readyz` |
| Prometheus metrics endpoint | `config/initializers/yabeda.rb` | apps/server | planned | OpenTelemetry and `/metrics` |
| Current version endpoint | `app/graphql/resolvers/version_resolver.rb` | apps/server | planned | |
| Rate limiting of calls to third-party APIs | `config/initializers/throttling.rb` | providers/* | planned | Per-provider limits (HubSpot 110/10s, Xero 60/min and 5000/day, NetSuite 10/s, Anrok 9/s, Avalara 10/s) |

### 1.7 Customers

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Customer create or update by external id (upsert) | `app/services/customers/upsert_from_api_service.rb` | libs/core | planned | Explicit create and update RPCs plus an upsert RPC; field presence distinguishes absent from null |
| Customer type (company, individual) and account type (customer, partner) | `app/models/customer.rb` | libs/core | planned | |
| Partner accounts and self-billed invoices | `app/models/customer.rb`, `app/services/invoices/` | libs/billing | planned | |
| Sequential id and slug per organization | `app/models/customer.rb` | libs/core | planned | |
| Billing and shipping addresses | `app/models/customer.rb` | libs/core | planned | |
| Tax identification number, legal name and number | `app/models/customer.rb` | libs/core | planned | |
| Default currency: a preference for new billing objects, not a constraint | `app/services/customers/update_currency_service.rb` | libs/core | planned | Set by the first billing object when absent; an explicit update can change it at any time |
| Several currencies per customer: subscriptions in their plan's currency; wallets, applied coupons and one-off invoices in their own; invoices split per currency | `app/services/subscriptions/organization_billing_service.rb` (`group_by_currency`), `app/services/wallets/create_service.rb`, `app/services/applied_coupons/create_service.rb`, `app/services/invoices/create_one_off_service.rb` | libs/billing | planned | Wallets and fixed-amount coupons apply only to invoices in their currency; dunning counts attempts per currency (§1.35) |
| Time zone with fallback to billing entity and organization | `app/models/customer.rb` | libs/period | planned | Fallback is customer → billing entity → organization everywhere (§4) |
| Grace period, payment term, document locale with fallback | `app/models/customer.rb` | libs/billing | planned | |
| Finalize zero-amount invoice: inherit, skip, finalize | `app/models/customer.rb` | libs/billing | planned | |
| Invoice issuing date anchor and adjustment overrides | `app/models/customer.rb` | libs/billing | planned | |
| Customer metadata (max 5, key 20 chars, value 100 chars, display on invoice) | `app/models/metadata/customer_metadata.rb` | libs/core | planned | |
| Editability rules once subscribed or invoiced | `app/models/customer.rb` (`editable?`) | libs/core | planned | |
| Customer list filters (search term, metadata, countries, states, zipcodes, currencies, customer and account type, billing entity, has tax id) | `app/queries/customers_query.rb` | libs/store | planned | |
| Customer deletion with relations terminated | `app/services/customers/destroy_service.rb`, `app/jobs/customers/terminate_relations_job.rb` | libs/billing | planned | Subscriptions terminated, wallets terminated, draft invoices finalized |
| Payment provider connection per customer (provider customer id, sync with provider) | `app/services/payment_provider_customers/create_or_update_batch_service.rb` | libs/core | planned | Several connections per customer with a default (§3) |
| Integration customers (accounting, CRM, tax) batch create and update | `app/services/integration_customers/create_or_update_batch_service.rb` | libs/core | planned | |
| Checkout URL to collect a payment method | `app/services/customers/generate_checkout_url_service.rb` | libs/billing | planned | |
| Customer portal URL | `app/services/customer_portal/generate_url_service.rb` | libs/auth | planned | |
| Locks to serialize per-customer operations (prepaid credit, payment method, credit note, coupon, billing schedule) | `app/services/customers/lock_service.rb` | libs/store | logic done | PostgreSQL advisory locks |
| Invoice custom sections selected or skipped per customer | `app/services/customers/manage_invoice_custom_sections_service.rb` | libs/billing | planned | |
| Exclude from dunning and applied dunning campaign | `app/models/customer.rb` | libs/billing | planned | |
| x402 agent address | `app/models/customer.rb` (`x402_agent_address`) | libs/core | planned | See §1.36 |

### 1.8 Billable Metrics

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Billable metric CRUD (name, code, description, field name) | `app/models/billable_metric.rb`, `app/services/billable_metrics/` | libs/core | planned | |
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
| Evaluate expression API (test an expression against a sample event) | `app/services/billable_metrics/evaluate_expression_service.rb` | libs/expression | logic done | |
| Field locks once a metric is used by a plan | `app/services/billable_metrics/update_service.rb` | libs/core | planned | |
| Destroy metric: removes charges and alerts, refreshes drafts | `app/services/billable_metrics/destroy_service.rb` | libs/billing | planned | Events are kept (§2); refused while a rate card's product filter uses the metric (`referenced_by_product_filter`) |
| Metric filters (key with allowed values) and batch update | `app/services/billable_metric_filters/create_or_update_batch_service.rb` | libs/core | planned | Removing a value trims dependent charge filters; refused when a rate card's product filter uses the value or key (`referenced_by_product_filter`), as Lago blocks rather than cascades |

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
| Plan CRUD (name, code, description, invoice display name, metadata) | `app/models/plan.rb`, `app/services/plans/` | libs/core | planned | |
| Intervals weekly, monthly, quarterly, semiannual, yearly | `app/models/plan.rb` | libs/period | logic done | |
| Subscription fee amount and currency | `app/models/plan.rb` | libs/billing | planned | |
| Subscription fee paid in advance or in arrears | `app/models/plan.rb` (`pay_in_advance`) | libs/billing | planned | |
| Trial period in days | `app/models/plan.rb` | libs/period | logic done | |
| Bill charges monthly on yearly, semiannual and quarterly plans | `app/models/plan.rb` (`bill_charges_monthly`) | libs/period | logic done | |
| Bill fixed charges monthly | `app/models/plan.rb` (`bill_fixed_charges_monthly`) | libs/period | logic done | |
| Taxes applied to a plan | `app/services/plans/apply_taxes_service.rb` | libs/tax | planned | |
| Yearly amount used to classify upgrades and downgrades | `app/models/plan.rb` (`yearly_amount_cents`) | libs/billing | planned | |
| Plan overrides per subscription (child plans) | `app/services/plans/override_service.rb` | libs/billing | planned | |
| Field locks once a plan has subscriptions | `app/services/plans/update_service.rb` | libs/core | planned | |
| Cascade plan changes to overridden child plans | `app/services/plans/update_service.rb` (`cascade_updates`), `app/jobs/plans/` | libs/billing | planned | |
| Update the amount of existing subscriptions | `app/jobs/plans/update_amount_job.rb` | libs/billing | planned | |
| Plan deletion: prepare (pending deletion) then destroy with subscriptions terminated | `app/services/plans/prepare_destroy_service.rb`, `app/services/plans/destroy_service.rb` | libs/billing | planned | Archived status rather than soft delete |
| Plan webhooks `plan.created`, `plan.updated`, `plan.deleted` | `app/services/webhooks/plans/` | libs/webhooks | planned | |

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
| Pay-in-advance charges | `app/models/charge.rb` (`pay_in_advance`) | libs/billing | planned | |
| Invoiceable flag for in-advance charges (fees without invoices) | `app/models/charge.rb` (`invoiceable`) | libs/billing | planned | |
| Regroup paid in-advance fees on the next invoice | `app/models/charge.rb` (`regroup_paid_fees`) | libs/billing | planned | |
| Minimum amount per charge with true-up fee | `app/models/charge.rb` (`min_amount_cents`), `app/services/fees/create_true_up_service.rb` | libs/billing | planned | |
| Prorated charges (recurring metrics) | `app/models/charge.rb` (`prorated`) | libs/pricing | logic done | |
| Charges that may target a wallet | `app/models/charge.rb` (`accepts_target_wallet`) | libs/billing | planned | |
| Charge code generation | `app/services/charges/` | libs/core | planned | |
| Pricing group keys (aggregate per key) | `app/models/charge.rb` (`pricing_group_keys`) | libs/pricing | logic done | |
| Presentation group keys (at most two, display only) | `app/models/charge.rb` (`presentation_group_keys`) | libs/billing | planned | |
| Charge invoice display name | `app/models/charge.rb` | libs/billing | planned | |
| Taxes per charge | `app/services/charges/apply_taxes_service.rb` | libs/tax | planned | |
| Charges API nested under plans and subscriptions | `app/controllers/api/v1/plans/charges_controller.rb`, `app/controllers/api/v1/subscriptions/charges_controller.rb` | apps/server | planned | |

### 1.12 Charge Filters

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Charge filters with their own properties | `app/models/charge_filter.rb`, `app/services/charge_filters/` | libs/metering | logic done | |
| All-values wildcard (`__ALL_FILTER_VALUES__`) | `app/models/charge_filter_value.rb` | libs/metering | logic done | |
| Filter precedence (most specific filter wins) | `app/services/events/billing_period_filters/matching_and_ignored_service.rb` | libs/metering | logic done | |
| Event matching to filters, default bucket for unmatched events | `app/services/charge_filters/event_matching_service.rb` | libs/metering | logic done | |
| Minimizing ignored filters for billing-period queries | `app/services/charge_filters/` (`MinimizeIgnoredFilters`) | libs/metering | logic done | |
| Filter code (slug plus hash) | `app/models/charge_filter.rb` | libs/core | planned | |
| Cascade filter changes to child plans | `app/services/charge_filters/create_or_update_batch_service.rb` | libs/billing | planned | Coded path only (§2) |

### 1.13 Fixed Charges and Add-ons

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Add-on CRUD (amount, currency, invoice display name, taxes) | `app/models/add_on.rb`, `app/services/add_ons/` | libs/core | planned | |
| Fixed charges on plans (add-on based, units) | `app/models/fixed_charge.rb`, `app/services/fixed_charges/` | libs/billing | planned | |
| Fixed charge models standard, graduated, volume | `app/services/fixed_charges/` | libs/pricing | logic done | |
| Pay-in-advance fixed charges | `app/services/invoices/create_pay_in_advance_fixed_charges_service.rb` | libs/billing | planned | |
| Prorated fixed charges | `app/models/fixed_charge.rb` (`prorated`) | libs/period | logic done | |
| Units overrides per subscription | `app/services/fixed_charges/override_service.rb` | libs/billing | planned | |
| Apply unit changes immediately or at next period | `app/models/fixed_charge.rb` (`apply_units_immediately`) | libs/billing | planned | |
| Fixed charge events recording unit changes over time | `app/models/fixed_charge_event.rb`, `app/services/fixed_charges/emit_events_service.rb` | libs/billing | planned | |
| Cascade fixed charge changes to child plans | `app/jobs/fixed_charges/` | libs/billing | planned | |
| Taxes per fixed charge | `app/services/fixed_charges/` | libs/tax | planned | |
| Fixed charges API nested under plans and subscriptions | `app/controllers/api/v1/plans/fixed_charges_controller.rb` | apps/server | planned | |

### 1.14 Product Catalog (v2)

Lago carries two catalogs: the plan/charge catalog above and a newer product catalog behind the `product_catalog` flag. Tanvo implements one catalog whose model covers both (§3): every row here and in §1.10–§1.13 is in scope.

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Product categories | `app/models/product_category.rb` | libs/core | planned | |
| Products (metered, fixed) | `app/models/product.rb` | libs/core | planned | |
| Product filters and filter values | `app/models/product_filter.rb` | libs/pricing | planned | |
| Rate cards (billing timing, proration, display on invoice, regroup, pricing unit, locks) | `app/models/rate_card.rb` | libs/core | planned | |
| Rate card rates on a timeline (`effective_from`) | `app/models/rate_card_rate.rb`, `app/services/rate_card_rates/create_service.rb` | libs/pricing | planned | Append-only after the active rate; an arrears `effective_from` is local midnight, one rate per day; a backdated rate is refused (organization time zone) |
| Rate models and their compatibility with product kinds | `app/services/rate_card_rates/` (`ModelCompatibility`) | libs/pricing | planned | |
| Billing interval count and unit per rate | `app/models/rate_card_rate.rb` | libs/period | planned | |
| Range normalization | `app/services/rate_card_rates/` (`NormalizeRanges`) | libs/pricing | planned | |
| Prorated graduated rates filled per event (decimal tier bounds; removals empty the top tiers first) | `app/services/charge_models/prorated_adjacent_graduated_service.rb` | libs/pricing | planned | |
| Catalog plans and plan rate cards | `app/models/catalog_plan.rb`, `app/models/plan_rate_card.rb` | libs/core | planned | |
| Rate phases (ramps) and their invariants | `app/models/rate_phase.rb` | libs/billing | planned | |
| Rate overrides | `app/models/rate_override.rb` | libs/billing | planned | |
| Catalog code format | `app/models/concerns/` (`CatalogCodeFormat`) | libs/core | planned | |
| Catalog API with cursor pagination | `app/controllers/api/v2/` | apps/server | planned | Cursor pagination everywhere; no beta header |

### 1.15 Pricing Units

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Pricing units (name, code, short name of at most 3 characters, description) | `app/models/pricing_unit.rb`, `app/services/pricing_units/` | libs/core | planned | Public API, not only the admin UI |
| Charges priced in a pricing unit with a conversion rate | `app/models/applied_pricing_unit.rb` | libs/pricing | logic done | |
| Pricing unit usage recorded on fees | `app/models/pricing_unit_usage.rb` | libs/billing | planned | |

### 1.16 Entitlements

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Features (code, name, description) | `app/models/entitlement/feature.rb`, `app/services/entitlement/` | libs/core | planned | |
| Privileges of type integer, string, boolean, select | `app/models/entitlement/privilege.rb` | libs/core | planned | |
| Plan entitlements and privilege values | `app/services/entitlement/plan_entitlements_update_service.rb` | libs/core | planned | |
| Subscription entitlement overrides and removals | `app/services/entitlement/subscription_entitlements_update_service.rb` | libs/core | planned | |
| Resolved entitlements of a subscription | `app/services/entitlement/` | libs/core | planned | |
| Feature webhooks `feature.created`, `feature.updated`, `feature.deleted` | `app/services/webhooks/features/` | libs/webhooks | planned | |
| Features and entitlements API | `app/controllers/api/v1/features_controller.rb`, `app/controllers/api/v1/plans/entitlements_controller.rb`, `app/controllers/api/v1/subscriptions/entitlements_controller.rb` | apps/server | planned | |

### 1.17 Event Ingestion

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Single event ingestion | `app/services/events/create_service.rb` | libs/ingest | planned | |
| Batch ingestion (up to 100 events) | `app/services/events/create_batch_service.rb` | libs/ingest | planned | |
| Event timestamp parsing (seconds, fractional seconds, defaults to now) | `app/services/events/common_factory.rb` | libs/ingest | planned | |
| Event fields: transaction id, external subscription id, code, properties, precise total amount | `app/models/event.rb` | libs/ingest | planned | |
| `external_contract_id` alias of `external_subscription_id` on ingestion and event filters | `app/controllers/api/v1/events_controller.rb` | libs/ingest | planned | The explicit `external_subscription_id` wins when both are sent |
| Deduplication by organization and transaction id | `app/services/events/create_service.rb` | libs/ingest | planned | Lago also scopes by external subscription id; Tanvo dedups per organization (§3) |
| Request metadata on events (user agent, IP) | `app/services/events/create_service.rb` | libs/ingest | planned | |
| Event validation (metric code, subscription, numeric field) | `app/services/events/validate_creation_service.rb` | libs/ingest | planned | |
| Post-validation of ingested events with `events.errors` webhook | `app/services/events/post_validation_service.rb` | libs/ingest | planned | Validation errors also returned synchronously where possible |
| Retrieve an event and list events | `app/controllers/api/v1/events_controller.rb` | apps/server | planned | |
| List enriched events | `app/controllers/api/v1/events_controller.rb` (`events_enriched`) | apps/server | planned | |
| Fee estimate for an in-advance event | `app/services/fees/estimate_pay_in_advance_service.rb` | libs/billing | planned | |
| Instant fee estimate and batch instant estimate | `app/services/fees/estimate_instant/` | libs/billing | planned | |
| Enrichment: subscription, metric, expression, aggregated value, grouping keys, precise amount; charge lookup only to route in-advance events | `app/services/events/enrich_service.rb`, events-processor `processors/` | libs/ingest | planned | No charge or filter on the enriched row; filters are matched from the raw properties at query or bucket time |
| Enriched events with operation type (add/remove) | `app/models/enriched_event.rb` | libs/ingest | planned | |
| Re-enrichment after catalog changes | `app/services/events/re_enrich_all_service.rb` | libs/ingest | planned | |
| Post-processing: usage monitoring, wallet refresh, target wallets, in-advance charges | `app/services/events/post_process_service.rb` | libs/ingest | planned | |
| Events targeting a specific wallet | `app/services/events/` (`target_wallet_code`) | libs/billing | planned | |
| In-advance charge fee creation from an event with source dedup | `app/services/fees/create_pay_in_advance_service.rb`, `app/services/events/pay_in_advance_service.rb` | libs/billing | planned | |
| Kafka ingestion mode with consumers | events-processor `processors/` | libs/ingest | planned | Built into the `consumer` role |
| Dead-letter topic for unprocessable events | events-processor `processors/dlq` | libs/ingest | planned | |
| Source connectors for HTTP, SQS, Kinesis | events-processor connectors | libs/ingest | planned | Kafka also supported |
| Catalog cache in consumers | events-processor (Badger cache, Debezium) | libs/ingest | planned | Change notifications instead of CDC (§3) |

### 1.18 Event Stores

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| PostgreSQL event store (events and enriched events partitioned) | `app/services/events/stores/postgres_store.rb` | libs/eventstore | logic done | Native partitions (§3) |
| ClickHouse event store with FINAL deduplication and retries | `app/services/events/stores/clickhouse_store.rb` | libs/eventstore | logic done | |
| Store queries: count, sum, max, latest, unique count, weighted sum, grouped, prorated, breakdown, last event, distinct values | `app/services/events/stores/base_store.rb` | libs/eventstore | logic done | |
| Billing-period filters on store queries | `app/services/events/stores/` | libs/eventstore | logic done | |
| ClickHouse raw, enriched and dead-letter tables | `db/clickhouse_migrate/` | libs/eventstore | planned | |
| Pre-aggregated usage buckets (15 minutes in Lago) | `app/models/clickhouse/usage_bucket.rb`, `app/services/events/stores/usage_bucket_store.rb` | libs/eventstore | planned | 15-minute buckets built by Tanvo itself (§3); units, events count, precise total amount (serves dynamic charges), last event and ingestion instants, target wallet code; in-advance, prorated, recurring and target-wallet charges read the events |
| Cached aggregations for recurring metrics | `app/models/cached_aggregation.rb` | libs/metering | logic done | |

### 1.19 Usage

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Current usage of a subscription | `app/services/invoices/customer_usage_service.rb` | libs/billing | planned | |
| Projected usage to the end of the period | `app/services/invoices/customer_usage_service.rb` (`with_projection`), `app/services/fees/projection_service.rb` | libs/billing | planned | From each current-usage fee: standard, graduated, package and volume reprice the projected units, other models scale the amount; ratio over the charges period ([Domain §10.3](domain.md#103-usage)) |
| Past usage per period | `app/queries/past_usage_query.rb` | libs/billing | planned | |
| Usage filters (by metric, by filter, full usage) | `app/services/invoices/customer_usage_service.rb` | libs/billing | planned | |
| Charge-level usage cache | `app/services/subscriptions/charge_cache_service.rb` | libs/billing | planned | Lazy validation: entries carry the latest ingested event and are recomputed when a newer one exists, never deleted on ingest; full usage has its own key |
| Realtime usage refresh | `app/services/subscriptions/` (`realtime_usage`) | libs/billing | planned | Usage buckets make it the default |
| Hourly usage breakdown | `app/graphql/resolvers/subscriptions/hourly_usage_resolver.rb` | libs/eventstore | planned | |
| Lifetime usage (historical, invoiced, current) | `app/models/lifetime_usage.rb`, `app/services/lifetime_usages/` | libs/billing | planned | |
| External historical usage amount | `app/controllers/api/v1/subscriptions/lifetime_usages_controller.rb` | libs/billing | planned | |
| Lifetime usage refresh | `app/jobs/clock/refresh_lifetime_usages_job.rb` | libs/billing | planned | |
| Granular lifetime usage per metric | `app/services/lifetime_usages/` | libs/billing | planned | |
| Daily usages with diff and history fill | `app/models/daily_usage.rb`, `app/services/daily_usages/` | libs/billing | planned | |
| Subscription activity processing (which subscriptions changed) | `app/services/usage_monitoring/process_subscription_activity_service.rb` | libs/billing | planned | |

### 1.20 Subscriptions

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Statuses pending, active, terminated, canceled, incomplete | `app/models/subscription.rb` | libs/core | planned | |
| Billing time calendar or anniversary | `app/models/subscription.rb` | libs/period | logic done | |
| Subscription start date (past or future) and ending date | `app/services/subscriptions/create_service.rb` | libs/billing | planned | |
| Subscription name and external id | `app/models/subscription.rb` | libs/core | planned | |
| Plan overrides at subscription creation | `app/services/subscriptions/create_service.rb`, `app/services/plans/override_service.rb` | libs/billing | planned | |
| Per-subscription charges, filters and fixed charges endpoints | `app/controllers/api/v1/subscriptions/` | apps/server | planned | |
| Upgrade and downgrade by yearly amount (upgrade immediate, downgrade at period end) | `app/services/subscriptions/plan_upgrade_service.rb` | libs/billing | planned | |
| Termination with credit note choice (credit, skip, refund, offset) | `app/services/subscriptions/terminate_service.rb` | libs/billing | planned | |
| Termination invoice choice (generate, skip) | `app/services/subscriptions/terminate_service.rb` | libs/billing | planned | |
| Refuse termination while taxes are pending | `app/services/subscriptions/terminate_service.rb` | libs/billing | planned | |
| Cancellation of pending subscriptions | `app/services/subscriptions/terminate_service.rb` | libs/billing | planned | |
| Cancellation reasons payment failed, timeout, manual | `app/models/subscription.rb` | libs/core | planned | |
| Payment method per subscription | `app/models/subscription.rb` | libs/billing | planned | |
| Consolidate invoices across subscriptions | `app/models/subscription.rb` | libs/billing | planned | |
| Progressive billing disabled per subscription | `app/models/subscription.rb` | libs/billing | planned | |
| Purchase order number | `app/models/subscription.rb` | libs/core | planned | |
| Billing entity override per subscription | `app/models/subscription.rb` | libs/core | planned | |
| Skip daily usage computation | `app/models/subscription.rb` | libs/billing | planned | |
| Activation rules: require payment, with timeout | `app/models/subscription/activation_rule.rb`, `app/services/subscriptions/activation_rules/` | libs/billing | planned | |
| Activation rule statuses (inactive, pending, satisfied, not applicable, declined, failed, expired) | `app/models/subscription/activation_rule.rb` | libs/core | planned | |
| Activation gating invoice paid before activation | `app/services/subscriptions/activate_service.rb` | libs/billing | planned | |
| Payment pre-authorization at subscription creation (Stripe) | `app/services/payment_providers/stripe/payments/authorize_service.rb` | providers/stripe | planned | |
| Activation of pending subscriptions at start date | `app/jobs/clock/activate_subscriptions_job.rb` | libs/billing | planned | Scheduled per subscription (§3) |
| Termination at ending date | `app/jobs/clock/terminate_ended_subscriptions_job.rb` | libs/billing | planned | |
| Expiry of incomplete subscriptions | `app/jobs/clock/expire_incomplete_subscriptions_job.rb` | libs/billing | planned | |
| Termination alerts before ending date (default 15 and 45 days) | `app/jobs/clock/subscriptions_to_be_terminated_job.rb` | libs/billing | planned | |
| Trials across upgrades (initial start date kept) | `app/models/subscription.rb` (`trial_end_date`) | libs/period | logic done | |
| Trial end detection and free-trial billing | `app/jobs/clock/free_trial_subscriptions_biller_job.rb` | libs/billing | planned | |
| Periodic billing selection (which subscriptions bill today) | `app/services/subscriptions/organization_billing_service.rb` | libs/billing | planned | Per-subscription scheduled jobs plus reconciliation (§3) |
| Grouping of subscriptions billed together on one invoice | `app/services/subscriptions/organization_billing_service.rb` | libs/billing | planned | |
| Subscription list filters (status, plan, customer, overridden) | `app/queries/subscriptions_query.rb` | libs/store | planned | |
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
| Invoice type subscription | `app/services/invoices/subscription_service.rb` | libs/billing | planned | |
| Invoice type credit (wallet top-up) | `app/services/invoices/paid_credit_service.rb` | libs/billing | planned | |
| Invoice type one-off (add-ons) | `app/services/invoices/create_one_off_service.rb` | libs/billing | planned | |
| Invoice type advance charges | `app/services/invoices/advance_charges_service.rb` | libs/billing | planned | |
| Invoice type progressive billing | `app/services/invoices/progressive_billing_service.rb` | libs/billing | planned | |
| In-advance charge invoices | `app/services/invoices/create_pay_in_advance_charge_service.rb` | libs/billing | planned | |
| Statuses draft, finalized, voided, failed, pending, generating, open, closed | `app/models/invoice.rb` | libs/core | planned | |
| Payment status (pending, succeeded, failed) and tax status | `app/models/invoice.rb` | libs/core | planned | |
| Computed amounts (fees, coupons, taxes, credit notes, prepaid credits, progressive deductions, totals, due, paid) | `app/models/invoice.rb`, `app/services/invoices/compute_amounts_from_fees.rb` | libs/billing | planned | |
| Tax invoice label in Australia, UAE, New Zealand, Indonesia, Singapore | `app/models/invoice.rb` | libs/documents | planned | |
| Numbering per customer (`PREFIX-CCC-NNN`) | `app/models/invoice.rb` (numbering) | libs/billing | planned | |
| Numbering per billing entity (`PREFIX-YYYYMM-NNN`) | `app/models/invoice.rb` (numbering) | libs/billing | planned | |
| Draft numbering (`PREFIX-DRAFT`) | `app/models/invoice.rb` | libs/billing | planned | |
| Invoice subscriptions with invoicing reason | `app/models/invoice_subscription.rb` | libs/billing | planned | |
| Fee calculation order for a billing run | `app/services/invoices/calculate_fees_service.rb` | libs/billing | planned | |
| Transition to final status with zero-amount setting | `app/services/invoices/transition_to_final_status_service.rb` | libs/billing | planned | |
| Retry generating failed subscription invoices | `app/services/invoices/retry_service.rb`, `app/jobs/clock/retry_generating_subscription_invoices_job.rb` | libs/billing | planned | |
| Issuing date rules (anchor and adjustment matrix) | `app/services/invoices/` (issuing date) | libs/billing | planned | |
| Payment due date from the payment term (due on receipt, net, end of month, net end of month, days end of month, day of month), resolved customer → billing entity and snapshotted with its source | `app/models/invoice.rb`, `db/structure.sql` (`payment_term`, `payment_term_source`) | libs/billing | planned | Lago has the columns and the rules on branches; no `net_payment_term` alias ([Domain §24](domain.md#24-design-decisions), item 12) |
| Purchase order number on invoices (from grouped subscriptions, wallet transactions or wallets, one-off requests), kept on regeneration | `app/services/invoices/create_generating_service.rb`, `app/services/invoices/paid_credit_service.rb`, `app/services/invoices/regenerate_from_voided_service.rb` | libs/billing | planned | |
| Grace period with draft invoices | `app/services/invoices/subscription_service.rb` | libs/billing | planned | |
| Draft refresh | `app/services/invoices/refresh_draft_service.rb`, `app/jobs/clock/refresh_draft_invoices_job.rb` | libs/billing | planned | |
| Automatic finalization at grace period end | `app/jobs/clock/finalize_invoices_job.rb` | libs/billing | planned | |
| Manual finalize and finalize all drafts | `app/services/invoices/refresh_draft_and_finalize_service.rb` | libs/billing | planned | |
| Void with credit and refund amounts | `app/services/invoices/void_service.rb` | libs/billing | planned | |
| Re-credit coupons, wallets and credit notes on void | `app/services/invoices/void_service.rb` | libs/billing | planned | |
| Void provider taxes on void | `app/services/invoices/provider_taxes/void_service.rb` | libs/billing | planned | |
| Regenerate an invoice from a voided one, with preview | `app/services/invoices/regenerate_from_voided_service.rb` | libs/billing | planned | |
| Retry failed provider tax computation | `app/services/invoices/provider_taxes/pull_taxes_and_apply_service.rb` | libs/billing | planned | |
| Finalize invoices held for VIES checks | `app/services/invoices/finalize_pending_vies_invoice_service.rb` | libs/billing | planned | |
| Progressive billing deductions and excess credit note | `app/services/invoices/progressive_billing_service.rb` | libs/billing | planned | |
| Invoice preview: proposal, projection, termination, plan change | `app/services/invoices/preview_service.rb` | libs/billing | planned | |
| Dispute lost | `app/services/payments/lose_dispute_service.rb` | libs/billing | planned | |
| Delete draft invoice | `app/services/invoices/delete_service.rb` | libs/billing | planned | Refused once synced externally; deletes its draft credit notes; the period is not billed again |
| Update payment status and metadata (max 5, key 20, value 140) | `app/services/invoices/update_service.rb` | libs/billing | planned | |
| Mark invoices overdue | `app/services/invoices/payments/mark_overdue_service.rb` | libs/billing | planned | |
| Item metadata on invoice items (at most 50 keys) | `app/models/` (`item_metadata`) | libs/billing | planned | |
| Invoice settlements (payment, credit note) | `app/models/invoice_settlement.rb` | libs/billing | planned | |
| Invoice list filters and search | `app/queries/invoices_query.rb` | libs/store | planned | Includes the purchase order number, case-insensitive |
| Invoice custom sections (manual, system generated) and resolution rules | `app/models/invoice_custom_section.rb`, `app/services/invoice_custom_sections/` | libs/billing | planned | Public API, not only the admin UI |
| Download PDF and XML | `app/services/invoices/generate_pdf_service.rb` | libs/documents | planned | |
| Resend invoice email | `app/services/emails/resend_service.rb` | libs/mail | planned | |
| Payment URL for an invoice | `app/services/invoices/payments/generate_payment_url_service.rb` | libs/billing | planned | |
| Store Salesforce id on an invoice | `app/controllers/api/v1/invoices_controller.rb` (`sync_salesforce_id`) | providers/salesforce | planned | |
| Invoice webhooks `invoice.created`, `one_off_created`, `paid_credit_added`, `generated`, `drafted`, `ready_to_finalize`, `voided`, `deleted`, `resynced` | `app/services/webhooks/invoices/` | libs/webhooks | planned | |
| Invoice payment webhooks `invoice.payment_status_updated`, `payment_overdue`, `payment_failure`, `payment_dispute_lost` | `app/services/webhooks/invoices/`, `app/services/webhooks/payment_providers/` | libs/webhooks | planned | |

### 1.24 Fees

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Fee types charge, add-on, subscription, credit, commitment, fixed charge, product | `app/models/fee.rb` | libs/core | planned | |
| Fee payment status (pending, succeeded, failed, refunded) for in-advance fees | `app/models/fee.rb` | libs/core | planned | |
| Subscription fees | `app/services/fees/subscription_service.rb` | libs/billing | planned | |
| Charge fees (including filters and groups) | `app/services/fees/charge_service.rb` | libs/billing | planned | |
| Fixed charge fees | `app/services/fees/fixed_charge_service.rb` | libs/billing | planned | |
| Add-on and one-off fees | `app/services/fees/one_off_service.rb` | libs/billing | planned | |
| Credit fees for wallet top-ups | `app/services/fees/paid_credit_service.rb` | libs/billing | planned | |
| Commitment fees | `app/services/fees/commitments/` | libs/billing | planned | |
| Adjusted fees on draft invoices (units, unit amount, display name) | `app/models/adjusted_fee.rb`, `app/services/adjusted_fees/` | libs/billing | planned | |
| Fee taxes | `app/models/fee/applied_tax.rb` | libs/tax | planned | |
| Presentation breakdowns of fees | `app/models/` (`presentation_breakdowns`) | libs/billing | planned | |
| Fees API (list, retrieve, update payment status, delete unattached) | `app/controllers/api/v1/fees_controller.rb` | apps/server | planned | |
| Fee webhook `fee.created` (in-advance) | `app/services/webhooks/fees/pay_in_advance_created_service.rb` | libs/webhooks | planned | |

### 1.25 Taxes

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Tax CRUD (name, code, rate, description) | `app/models/tax.rb`, `app/services/taxes/` | libs/tax | planned | |
| Taxes on billing entities, customers, plans, charges, fixed charges, add-ons, commitments | join tables in `app/models/` | libs/tax | planned | |
| Tax resolution order (explicit, add-on, charge, fixed charge, commitment, plan, customer, billing entity) | `app/services/fees/apply_taxes_service.rb` | libs/tax | logic done | |
| Rate card taxes overriding plan, customer and billing entity taxes | `app/models/rate_card.rb` (`rate_cards_taxes`) | libs/tax | planned | A rate card's taxes are its charge's taxes in the one catalog |
| Invoice applied taxes with taxable base and allocation | `app/services/invoices/apply_taxes_service.rb` | libs/tax | logic done | |
| Credit note applied taxes | `app/services/credit_notes/apply_taxes_service.rb` | libs/tax | logic done | |
| Provider-computed taxes (Anrok, Avalara) with special codes and base rate | `app/services/invoices/apply_provider_taxes_service.rb` | libs/tax | logic done | |
| EU VAT rates per country with exceptions | `app/services/taxes/` (ibericode rates) | libs/tax | logic done | |
| Automatic EU taxes for an organization | `app/services/taxes/auto_generate_service.rb` | libs/tax | logic done | |
| EU tax decision per customer (domestic, intra-EU B2B reverse charge, B2C, outside EU) | `app/services/customers/eu_auto_taxes_service.rb` | libs/tax | logic done | |
| VIES VAT number check with retries and backoff | `app/services/customers/vies_check_service.rb` | libs/tax | logic done | |
| Invoice held until a pending VIES check completes | `app/models/pending_vies_check.rb` | libs/billing | planned | |
| VIES webhook `customer.vies_check` | `app/services/webhooks/customers/vies_check_service.rb` | libs/webhooks | planned | |
| Tax provider error webhooks `customer.tax_provider_error`, `fee.tax_provider_error` | `app/services/webhooks/integrations/taxes/` | libs/webhooks | planned | |

### 1.26 Coupons

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Coupon CRUD with fixed amount or percentage | `app/models/coupon.rb`, `app/services/coupons/` | libs/billing | planned | |
| Frequency once, recurring (with duration), forever | `app/models/coupon.rb` | libs/billing | planned | |
| Reusable coupons | `app/models/coupon.rb` | libs/billing | planned | |
| Limit to plans or billable metrics | `app/models/coupon_target.rb` | libs/billing | planned | |
| Expiration by date and termination job | `app/jobs/clock/terminate_coupons_job.rb` | libs/billing | planned | |
| Applied coupons with amount, percentage and frequency overrides | `app/services/applied_coupons/create_service.rb` | libs/billing | planned | |
| Application order and amount math on invoices | `app/services/credits/applied_coupons_service.rb`, `app/services/credits/applied_coupon_service.rb` | libs/billing | planned | |
| Re-credit of coupons on void | `app/services/applied_coupons/recredit_service.rb` | libs/billing | planned | |
| Applied coupon termination | `app/services/applied_coupons/terminate_service.rb` | libs/billing | planned | |

### 1.27 Credits and Application Order

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Credits table linking coupons, credit notes and progressive billing to invoices | `app/models/credit.rb` | libs/billing | planned | |
| Credit note credits on invoices | `app/services/credits/credit_note_service.rb` | libs/billing | planned | |
| Prepaid credits from wallets allocated by wallet priority and allowed fee types | `app/services/credits/applied_prepaid_credits_service.rb` | libs/billing | planned | |
| Progressive billing credits | `app/services/credits/progressive_billing_service.rb` | libs/billing | planned | |
| Application order: progressive billing, coupons, taxes, credit notes, prepaid credits | `app/services/invoices/subscription_service.rb`, `app/services/invoices/calculate_fees_service.rb` | libs/billing | planned | |

### 1.28 Credit Notes

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Credit note creation with credit, refund and offset amounts | `app/services/credit_notes/create_service.rb` | libs/billing | planned | |
| Credit status (available, consumed, voided) and refund status (pending, succeeded, failed) | `app/models/credit_note.rb` | libs/core | planned | |
| Reasons duplicated charge, product unsatisfactory, order change, order cancellation, fraudulent charge, other | `app/models/credit_note.rb` | libs/core | planned | |
| Draft, finalized statuses | `app/models/credit_note.rb` | libs/core | planned | |
| Numbering `INVOICE-CNnnn` | `app/models/credit_note.rb` | libs/billing | planned | |
| Credit note items per fee | `app/models/credit_note_item.rb` | libs/billing | planned | |
| Taxes with allocation and rounding adjustment | `app/services/credit_notes/apply_taxes_service.rb` | libs/tax | logic done | |
| Validation of creditable and refundable amounts | `app/services/credit_notes/validate_service.rb` | libs/billing | planned | All checks enforced, including those Lago never calls (§4) |
| Offsets settling an invoice | `app/services/credit_notes/create_service.rb`, `app/models/invoice_settlement.rb` | libs/billing | planned | |
| Refunds through payment providers | `app/services/credit_notes/refunds/` | libs/billing | planned | See §1.35 |
| Credit note from subscription termination | `app/services/credit_notes/create_from_termination.rb` | libs/billing | planned | |
| Credit note from progressive billing excess | `app/services/credit_notes/create_from_progressive_billing_invoice.rb` | libs/billing | planned | |
| Estimate a credit note | `app/services/credit_notes/estimate_service.rb` | libs/billing | planned | |
| Void credit note (remaining credit) | `app/services/credit_notes/void_service.rb` | libs/billing | planned | |
| Update refund status | `app/services/credit_notes/update_service.rb` | libs/billing | planned | |
| Refresh draft credit notes with their invoice | `app/services/credit_notes/refresh_draft_service.rb` | libs/billing | planned | |
| Re-credit on invoice void | `app/services/credit_notes/recredit_service.rb` | libs/billing | planned | |
| Credit note metadata | `app/models/metadata/` | libs/billing | planned | |
| Credit note list filters, including the invoice's purchase order number | `app/queries/credit_notes_query.rb` | libs/store | planned | |
| Credit note webhooks `credit_note.created`, `credit_note.generated`, `credit_note.provider_refund_failure` | `app/services/webhooks/credit_notes/` | libs/webhooks | planned | One event name (§4) |

### 1.29 Wallets

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Wallet CRUD (name, code, currency, rate amount, expiration) | `app/models/wallet.rb`, `app/services/wallets/` | libs/billing | planned | The currency is the wallet's own, default the customer's |
| Purchase order number on wallets, wallet transactions and recurring rules, copied to credit invoices | `app/services/wallets/create_service.rb`, `app/services/wallet_transactions/create_from_params_service.rb`, `app/models/recurring_transaction_rule.rb` | libs/billing | planned | |
| Several wallets per customer (default maximum 6) with priority 1–50 | `app/models/wallet.rb` | libs/billing | planned | |
| Top-up minimum and maximum | `app/models/wallet.rb` | libs/billing | planned | |
| Allowed fee types per wallet | `app/models/wallet.rb` (`allowed_fee_types`) | libs/billing | planned | |
| Wallet targets (billable metrics a wallet pays for) | `app/models/wallet_target.rb` | libs/billing | planned | |
| Traceable wallets (consumptions and fundings) | `app/models/wallet_transaction_consumption.rb` | libs/billing | planned | |
| Wallet transactions: paid, granted, voided; statuses pending, settled, failed; source manual, interval, threshold | `app/models/wallet_transaction.rb`, `app/services/wallet_transactions/` | libs/billing | planned | |
| Targeted void of one inbound grant (`voided_transaction_id`) | `app/services/wallet_transactions/void_service.rb`, `app/services/wallet_transactions/validate_service.rb` | libs/billing | planned | Traceable wallets only; whole remainder or a capped amount |
| Invoice requires successful payment before credits are granted | `app/models/wallet_transaction.rb` | libs/billing | planned | |
| Recurring top-up rules: interval or threshold, fixed or target, grants target top-up | `app/models/recurring_transaction_rule.rb`, `app/services/wallets/recurring_transaction_rules/` | libs/billing | planned | |
| Interval top-ups | `app/jobs/clock/create_interval_wallet_transactions_job.rb` | libs/billing | planned | |
| Threshold top-ups with backoff | `app/services/wallets/threshold_top_up_service.rb` | libs/billing | planned | |
| Termination of expired rules | `app/jobs/clock/terminate_recurring_transaction_rules_job.rb` | libs/billing | planned | |
| Ongoing balance from current usage, cascaded across wallets | `app/services/wallets/balance/refresh_ongoing_usage_service.rb`, `app/services/wallets/balance/allocate_ongoing_usage_by_wallets_service.rb` | libs/billing | planned | Application order, each wallet up to its balance, overflow on the last; a threshold-rule wallet absorbs all |
| Ongoing balance refresh | `app/jobs/clock/refresh_wallets_ongoing_balance_job.rb`, `app/services/wallets/realtime_refresh_service.rb` | libs/billing | planned | Also from usage refresh signals, waiting up to 5 s for the bucket watermark; the 5-minute sweep stays authoritative |
| Wallet expiration | `app/jobs/clock/terminate_wallets_job.rb` | libs/billing | planned | |
| Payment URL for a wallet transaction | `app/services/wallet_transactions/payments/generate_payment_url_service.rb` | libs/billing | planned | |
| Wallet and transaction metadata (at most 15 keys) | `app/models/metadata/` | libs/billing | planned | |
| Wallet webhooks `wallet.created`, `updated`, `terminated`, `depleted_ongoing_balance` | `app/services/webhooks/wallets/` | libs/webhooks | planned | |
| Wallet transaction webhooks `wallet_transaction.created`, `updated`, `payment_failure` | `app/services/webhooks/wallet_transactions/` | libs/webhooks | planned | |

### 1.30 Commitments

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Minimum commitment per plan with override per subscription | `app/models/commitment.rb`, `app/services/commitments/` | libs/billing | planned | |
| True-up fee when usage is below the commitment | `app/services/commitments/minimum/calculate_true_up_fee_service.rb` | libs/billing | planned | |
| In-advance and in-arrears commitment periods | `app/services/commitments/` | libs/billing | planned | |
| Proration coefficient for partial periods | `app/services/commitments/` | libs/billing | planned | |
| Commitment in invoice previews | `app/services/invoices/preview_service.rb` | libs/billing | planned | |
| Taxes on commitments | `app/services/commitments/apply_taxes_service.rb` | libs/tax | planned | |

### 1.31 Progressive Billing and Usage Thresholds

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Usage thresholds on plans or subscriptions (exclusive) | `app/models/usage_threshold.rb`, `app/services/usage_thresholds/` | libs/billing | planned | |
| At most one recurring threshold | `app/services/usage_thresholds/update_service.rb` | libs/billing | planned | |
| Applied usage thresholds | `app/models/applied_usage_threshold.rb` | libs/billing | planned | |
| Threshold override per subscription | `app/services/usage_thresholds/override_service.rb` | libs/billing | planned | |
| Threshold crossing check on usage refresh | `app/services/lifetime_usages/check_thresholds_service.rb` | libs/billing | planned | |
| Progressive billing invoice on crossing | `app/services/invoices/progressive_billing_service.rb` | libs/billing | planned | |

### 1.32 Usage Monitoring Alerts

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Alert type current usage amount | `app/models/usage_monitoring/alert.rb` | libs/billing | planned | |
| Alert types billable metric current usage amount and units | `app/models/usage_monitoring/` | libs/billing | planned | |
| Alert types lifetime usage amount and billable metric lifetime usage units | `app/models/usage_monitoring/` | libs/billing | planned | |
| Alert types wallet balance amount, credits balance, ongoing balance amount, credits ongoing balance | `app/models/usage_monitoring/` | libs/billing | planned | |
| Up to 20 thresholds per alert, recurring threshold | `app/models/usage_monitoring/alert.rb` | libs/billing | planned | |
| Triggered alerts | `app/models/usage_monitoring/triggered_alert.rb` | libs/billing | planned | |
| Alert evaluation from subscription activity | `app/services/usage_monitoring/process_subscription_activity_service.rb` | libs/billing | planned | Under a row lock on the alert, which alert edits also take |
| Alerts API on subscriptions and wallets, with batch create | `app/controllers/api/v1/subscriptions/alerts_controller.rb`, `app/controllers/api/v1/customers/wallets/alerts_controller.rb` | apps/server | planned | |
| Webhook `alert.triggered` (high priority) | `app/services/webhooks/usage_monitoring/alert_triggered_service.rb` | libs/webhooks | planned | |

### 1.33 Payments

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Payment providers per organization with code and name | `app/models/payment_providers/base_provider.rb` | libs/core | planned | Several connections per provider |
| Provider status mapping to pending, processing, succeeded, failed | `app/models/payment_providers/*.rb` (`determine_payment_status`) | providers/* | planned | |
| Success redirect URL | `app/models/payment_providers/base_provider.rb` | libs/core | planned | |
| Provider customer creation | `app/services/payment_providers/create_customer_factory.rb` | providers/* | planned | |
| Provider deletion (unlinks customers) | `app/services/payment_providers/destroy_service.rb` | libs/billing | planned | |
| Several provider and integration connections per customer, one default per category | `app/services/payment_provider_customers/`, `app/services/integration_customers/` | libs/core | planned | Completed; Lago allows one at a time |
| Connection choice per billing object (specific, skip, inherit) | `app/services/billing_object_connections/` | libs/billing | planned | |
| Payment methods (from provider, default, details, destroy) | `app/services/payment_methods/` | libs/billing | planned | |
| Payment method precedence (override, subscription, wallet transaction, rule, wallet, customer default) | `app/services/payment_methods/determine_service.rb` | libs/billing | planned | |
| Manual payment method skips automatic charge | `app/services/payment_methods/determine_service.rb` | libs/billing | planned | |
| Payment types provider, manual, x402 | `app/models/payment.rb` | libs/core | planned | |
| Manual payments with reference (at most 40 characters) | `app/services/payments/manual_create_service.rb` | libs/billing | planned | |
| Automatic invoice payment with in-flight guard and attempt counter | `app/services/invoices/payments/create_service.rb` | libs/billing | planned | One in-flight provider payment per payable |
| Zero-amount invoices marked paid | `app/services/invoices/payments/create_service.rb` | libs/billing | planned | |
| Delay of automatic payment when a payment link is open | `app/services/invoices/payments/create_service.rb` (`CHECKOUT_AUTO_PAYMENT_DELAY`) | libs/billing | planned | |
| Payment reference text | `app/services/invoices/payments/create_service.rb` | libs/billing | planned | |
| Payment intents (payment links with expiry, reuse) | `app/models/payment_intent.rb`, `app/services/payment_intents/` | libs/billing | planned | |
| Retry invoice payment and retry all | `app/services/invoices/payments/retry_service.rb`, `retry_batch_service.rb` | libs/billing | planned | |
| Cancel abandoned payments requiring action | `app/services/invoices/payments/cancel_abandoned_service.rb` | libs/billing | planned | |
| Payment success effects: paid totals, invoice payment status, accounting sync | `app/services/invoices/payments/*_service.rb` | libs/billing | planned | |
| Payment failure webhooks (invoice, wallet transaction, payment request) | `app/services/invoices/payments/deliver_error_webhook_service.rb` | libs/webhooks | planned | |
| Payment webhooks `payment.succeeded`, `payment.requires_action` | `app/services/webhooks/payments/` | libs/webhooks | planned | |
| Provider webhooks `payment_provider.error`, `customer.payment_provider_created`, `customer.payment_provider_error`, `customer.checkout_url_generated` | `app/services/webhooks/payment_providers/` | libs/webhooks | planned | |
| Payments API (list, retrieve, create manual) and per-customer payments | `app/controllers/api/v1/payments_controller.rb` | apps/server | planned | |
| Payment methods API (list, delete, set default) | `app/controllers/api/v1/customers/payment_methods_controller.rb` | apps/server | planned | |

### 1.34 Payment Providers

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Stripe connection (secret key) with webhook endpoint registered automatically | `app/services/payment_providers/stripe_service.rb`, `app/jobs/payment_providers/stripe/register_webhook_job.rb` | providers/stripe | planned | |
| Stripe 3DS support and terms-of-service consent | `app/models/payment_providers/stripe_provider.rb` | providers/stripe | planned | |
| Stripe customers with idempotency and email fallback | `app/services/payment_provider_customers/stripe_service.rb` | providers/stripe | planned | |
| Stripe payment method types (card, SEPA debit, US bank account, BACS debit, Link, Boleto, crypto, customer balance) | `app/models/payment_provider_customers/stripe_customer.rb` | providers/stripe | planned | |
| Stripe checkout in setup mode | `app/services/payment_provider_customers/stripe_service.rb` | providers/stripe | planned | |
| Stripe bank transfer funding instructions as a system invoice section | `app/services/payment_provider_customers/stripe/sync_funding_instructions_service.rb` | providers/stripe | planned | |
| Stripe payment intents (off session, idempotent, bank transfer per currency) | `app/services/payment_providers/stripe/payments/create_service.rb` | providers/stripe | planned | |
| Stripe shared payment token | `app/services/payment_providers/stripe/payments/create_service.rb` | providers/stripe | planned | No flag |
| Stripe payment link (checkout in payment mode) and expiry | `app/services/invoices/payments/stripe_service.rb` | providers/stripe | planned | |
| Stripe pre-authorization | `app/services/payment_providers/stripe/payments/authorize_service.rb` | providers/stripe | planned | |
| Stripe card details on payments | `app/services/payments/set_payment_method_data_service.rb` | providers/stripe | planned | |
| Stripe webhooks: setup intent succeeded, payment intent succeeded, failed, canceled, payment method detached, refund updated, customer updated, dispute closed, cash balance funded | `app/services/payment_providers/stripe/handle_event_service.rb` | providers/stripe | planned | |
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
| Cancel payment (Stripe, Adyen, GoCardless) | `app/services/payment_providers/cancel_payment_service.rb` | providers/* | planned | |
| Update payment reference (Stripe) | `app/services/payment_providers/update_payment_reference_service.rb` | providers/stripe | planned | |

### 1.35 Payment Requests, Dunning, Receipts and Refunds

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Payment requests grouping overdue invoices (same currency and billing entity) | `app/services/payment_requests/create_service.rb` | libs/billing | planned | |
| Payment request amount net of credit note offsets | `app/services/payment_requests/create_service.rb` | libs/billing | planned | |
| Payment request charged through the provider, or emailed | `app/services/payment_requests/payments/create_service.rb`, `app/mailers/payment_request_mailer.rb` | libs/billing | planned | |
| Payment request status updates | `app/services/payment_requests/update_service.rb` | libs/billing | planned | |
| Payment requests API and per-customer list | `app/controllers/api/v1/payment_requests_controller.rb` | apps/server | planned | |
| Payment request webhooks `payment_request.created`, `payment_status_updated`, `payment_failure` | `app/services/webhooks/payment_requests/` | libs/webhooks | planned | |
| Dunning campaigns (thresholds per currency, days between attempts, maximum attempts, BCC emails) | `app/models/dunning_campaign.rb`, `app/services/dunning_campaigns/` | libs/billing | planned | Public API, not only the admin UI |
| Campaign applied to a billing entity or customer, with exclusion | `app/services/dunning_campaigns/process_customer_service.rb` | libs/billing | planned | |
| Threshold changes reset customers that no longer match | `app/services/dunning_campaigns/update_service.rb` | libs/billing | planned | |
| Dunning processing: per-currency attempts, threshold check, payment request | `app/services/dunning_campaigns/bulk_process_service.rb`, `process_attempt_service.rb` | libs/billing | planned | |
| Dunning webhook `dunning_campaign.finished` | `app/services/webhooks/dunning_campaigns/finished_service.rb` | libs/webhooks | planned | |
| Payment receipts for succeeded payments, numbered per customer | `app/services/payment_receipts/create_service.rb` | libs/billing | planned | |
| Payment receipt PDF and XML, emailed per billing entity settings | `app/services/payment_receipts/generate_pdf_service.rb`, `generate_xml_service.rb` | libs/documents | planned | |
| Payment receipts API and resend email | `app/controllers/api/v1/payment_receipts_controller.rb` | apps/server | planned | |
| Payment receipt webhooks `payment_receipt.created`, `payment_receipt.generated` | `app/services/webhooks/payment_receipts/` | libs/webhooks | planned | |
| Refunds for credit notes through Stripe, GoCardless, Adyen | `app/services/credit_notes/refunds/` | providers/* | planned | |
| Refund reason mapping to provider reasons | `app/services/credit_notes/refunds/stripe_service.rb` | providers/stripe | planned | |
| Refund status updates from provider webhooks | `app/services/credit_notes/refunds/` (`update_status`) | libs/billing | planned | |

### 1.36 x402 Agent Payments

Lago's x402 support is unfinished: only the gate check is reachable. Tanvo ports the whole design and completes it.

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| x402 connections (facilitator Coinbase CDP, payout addresses, networks, USDC, auto-create customers) | `app/models/x402/connection.rb` | libs/core | planned | With a management API |
| Networks Base, Base Sepolia, Solana mainnet and devnet; USDC assets per network | `app/services/x402/network.rb`, `app/services/x402/asset.rb` | libs/core | planned | |
| Gate check: can an agent use a metric now, or what must it pay | `app/services/x402/gate_checks/check_service.rb` | libs/billing | planned | Passes when balance − ongoing usage covers 1,000 estimated calls, until Lago's reservation counter replaces that floor |
| Gate check route check: the parent plan and its charges must be able to gate a prepaid wallet | `app/services/x402/gate_checks/route_check_service.rb` | libs/billing | planned | |
| `x402` API key scope for the gate check endpoint | `app/models/api_key.rb` | libs/auth | planned | |
| x402-enabled wallets (`x402_enabled`) | `app/models/wallet.rb` | libs/core | planned | |
| Invoice payment link bound to an x402 connection (`x402_connection_id`, `x402_payment_token`) | `app/models/invoice.rb` | libs/core | planned | Token unique when set; connection of the same organization |
| Payment requirements from the facilitator | `app/services/x402/payment_requirements_service.rb` | libs/billing | planned | |
| Facilitator client (verify, settle, supported) with per-request JWT | `app/services/x402/facilitator/coinbase_cdp_adapter.rb` | providers/* | planned | New provider module `providers/x402` |
| Settlements (verify, settle, reconcile) | `app/services/x402/settlements/` | libs/billing | planned | Wired to jobs; unused in Lago |
| On-chain reconciliation through JSON-RPC (EVM, Solana) | `app/services/x402/chain/` | providers/* | planned | |
| Payment digest for idempotency | `app/services/x402/payments/compute_digest_service.rb` | libs/billing | planned | The digest and transaction hash are unique across all organizations, the one cross-tenant unique index (Lago 18a741762) |

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
| Item mappings for add-ons and billable metrics, optionally per billing entity | `app/services/integration_mappings/` | libs/core | planned | |
| Collection mappings (fallback item, coupon, subscription fee, minimum commitment, tax, prepaid credit, credit note, account, currencies) | `app/services/integration_collection_mappings/` | libs/core | planned | |
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
| Retry tax reporting and tax voiding | `app/graphql/mutations/integrations/` | libs/billing | planned | |
| Integration retry policy (bad gateway, request limit, throttling, timeouts) | `app/jobs/integrations/` | libs/jobs | planned | |
| Integration webhooks `customer.accounting_provider_created`, `customer.accounting_provider_error`, `customer.crm_provider_created`, `customer.crm_provider_error`, `integration.provider_error` | `app/services/webhooks/integrations/` | libs/webhooks | planned | |
| One tax integration per customer | `app/models/integration_customers/base_customer.rb` | libs/core | planned | |

### 1.38 Outbound Webhooks

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Webhook endpoints (up to 10 per organization, unique URL, name) | `app/models/webhook_endpoint.rb`, `app/services/webhook_endpoints/` | libs/webhooks | planned | |
| Event type subscription per endpoint (all by default) | `app/models/webhook_endpoint.rb`, `config/webhook_event_types.yml` | libs/webhooks | logic done | |
| Event type catalog with descriptions and categories | `config/webhook_event_types.yml` | libs/webhooks | planned | |
| Delivery with retries and backoff | `app/services/webhooks/send_http_service.rb` | libs/webhooks | logic done | |
| Signed payloads | `app/models/webhook.rb` | libs/webhooks | logic done | Standard Webhooks (§3) |
| Private address blocking with an opt-out | `lib/lago_http_client/` (`AddressGuard`) | libs/webhooks | logic done | |
| Delivery records with response, status, retries | `app/models/webhook.rb` | libs/webhooks | logic done | |
| Manual retry of a delivery | `app/services/webhooks/retry_service.rb` | libs/webhooks | logic done | |
| Delivery list with filters (status, event type, search) | `app/graphql/resolvers/webhooks_resolver.rb` | libs/webhooks | planned | |
| Delivery cleanup after 90 days | `app/jobs/clock/webhooks_cleanup_job.rb` | libs/webhooks | logic done | |
| Webhook endpoints API | `app/controllers/api/v1/webhook_endpoints_controller.rb` | apps/server | planned | |
| Event group billable metrics: `billable_metric.created`, `updated`, `deleted` | `app/services/webhooks/billable_metrics/` | libs/webhooks | planned | |
| Event group plans: `plan.created`, `updated`, `deleted` | `app/services/webhooks/plans/` | libs/webhooks | planned | |
| Event group features: `feature.created`, `updated`, `deleted` | `app/services/webhooks/features/` | libs/webhooks | planned | |
| Event group customers: `customer.created`, `updated`, `vies_check` | `app/services/webhooks/customers/` | libs/webhooks | planned | |
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
| Destinations claiming event types (one destination per type) | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | planned | With a management API (§3) |
| Event types `customer_usage.refreshed.v1`, `customer_full_usage.refreshed.v1` | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | planned | |
| Plans excluded from full-usage events | `app/models/streaming_destinations/base_destination.rb` | libs/webhooks | planned | |
| Kinesis destination with assumed role and partition key | `app/models/streaming_destinations/kinesis_destination.rb`, `lib/lago/kinesis/producer.rb` | libs/webhooks | planned | |
| Usage-refreshed envelope with wallets | `app/services/event_destinations/` | libs/webhooks | planned | |
| Delivery deduplication and logging (delivered, skipped, failed, superseded, dropped) | `app/jobs/deliver_event_job.rb`, `app/services/event_destinations/delivery_logger.rb` | libs/webhooks | planned | |

### 1.41 Quotes, Order Forms and Orders

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Quotes and quote versions with statuses | `app/models/quote.rb`, `app/models/quote_version.rb`, `app/services/quotes/`, `app/services/quote_versions/` | libs/billing | planned | |
| Order types subscription creation, subscription amendment, one-off, with validators | `app/services/quote_versions/validators/` | libs/billing | planned | |
| Mention variables in quote content | `app/services/quotes/` | libs/documents | planned | |
| Deal expiration | `app/services/quotes/` (`DealExpiration`) | libs/billing | planned | |
| Order forms (created, signed, expired, voided) | `app/models/order_form.rb`, `app/services/order_forms/` | libs/billing | planned | |
| Order form expiry | `app/jobs/clock/expire_order_forms_job.rb` | libs/billing | planned | |
| Orders and scheduled execution | `app/models/order.rb`, `app/jobs/clock/execute_scheduled_orders_job.rb` | libs/billing | planned | |

### 1.42 Usage Attribution

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Usage attribution types, hierarchical and flat (`account_tree` flag) | `app/models/usage_attribution_type.rb`, `app/controllers/api/v1/usage_attribution_types_controller.rb` | libs/core | planned | |
| Attribution values | `app/models/usage_attribution_value.rb` | libs/core | planned | |
| Attributed usage query | `app/controllers/api/v1/customers/attributed_usage_controller.rb` | libs/billing | planned | Lago has no population path; Tanvo defines one |

### 1.43 Data Exports

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| CSV exports of invoices, invoice fees, credit notes, credit note items with the list filters | `app/services/data_exports/` | libs/billing | planned | Public API |
| Exports split into parts and combined | `app/models/data_export_part.rb` | libs/jobs | planned | |
| Export files expire after 7 days | `app/models/data_export.rb` | libs/billing | planned | |
| Export ready email | `app/mailers/data_export_mailer.rb` | libs/mail | planned | |
| Export views (`exports_*`) of invoices, fees, credit notes, wallets, subscriptions, alerts, daily usages and the other business tables | `db/views/exports_*.sql` | libs/store | planned | Read-only PostgreSQL views, with the deletion records (§1.49), the contract of the warehouse sync |
| Warehouse sync of the export views | Lago's private data pipeline | ee/pipeline | planned | Separate process reading the views (Architecture §14) |

### 1.44 Analytics

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Gross revenue | `app/services/analytics/gross_revenues_service.rb` | libs/billing | planned | Core API |
| MRR | `app/services/analytics/mrrs_service.rb` | libs/billing | planned | Core API |
| Invoice collection | `app/services/analytics/invoice_collections_service.rb` | libs/billing | planned | Core API |
| Invoiced usage | `app/services/analytics/invoiced_usages_service.rb` | libs/billing | planned | Core API |
| Overdue balance | `app/services/analytics/overdue_balances_service.rb` | libs/billing | planned | Core API |
| Analytics cache (4 hours) | `app/services/analytics/` | libs/billing | planned | |
| MRR by plan, prepaid credits, revenue streams by customer and plan, usage and aggregated amounts | Data API (`app/services/data_api/`) | ee/analytics | planned | |
| Embedded dashboards | Superset embedding (`app/services/` superset) | ee/analytics | planned | |
| Forecasted usage | frontend-only premium flag | ee/analytics | planned | |
| Revenue recognition | frontend-only premium flag | ee/analytics | planned | |

### 1.45 Logs

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Activity logs (resource changes from API, admin UI, system) | `app/services/utils/activity_log.rb` | libs/core | planned | Fed by `audit_events` |
| Activity log retention per organization | `app/models/organization.rb` | libs/eventstore | planned | |
| API logs of write requests | `app/services/utils/api_log.rb` | apps/server | planned | |
| Security logs (sign-ins, users, roles, API keys, integrations, webhook endpoints) with 90-day retention | `app/services/utils/security_log.rb` | libs/auth | planned | |
| Email sent activity | `app/services/utils/activity_log.rb` | libs/mail | planned | |
| Logs API with filters | `app/controllers/api/v1/activity_logs_controller.rb`, `api_logs_controller.rb`, `security_logs_controller.rb` | apps/server | planned | |
| Logs stored in ClickHouse, or PostgreSQL without ClickHouse | `db/clickhouse_migrate/` | libs/eventstore | planned | Lago requires ClickHouse |

### 1.46 Customer Portal

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Portal access token scoped to one customer | `app/services/customer_portal/` | libs/auth | planned | Short-lived signed tokens |
| Portal customer profile view and update | `app/graphql/resolvers/customer_portal/` | apps/server | planned | |
| Portal invoices list and download | `app/graphql/resolvers/customer_portal/` | apps/server | planned | |
| Portal subscriptions and current usage | `app/graphql/resolvers/customer_portal/` | apps/server | planned | |
| Portal wallets and wallet top-up | `app/graphql/mutations/customer_portal/` | apps/server | planned | |
| Portal overdue balance and invoice collection | `app/graphql/resolvers/customer_portal/` | apps/server | planned | |
| Portal user interface | Lago front-end | apps/web | planned | |

### 1.47 Documents, E-invoicing and Email

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Invoice PDF (current template version 4 with partials) | `app/views/templates/invoices/v4/` | libs/documents | planned | Typst templates (§3) |
| Credit note PDF | `app/views/templates/credit_notes/` | libs/documents | planned | |
| Payment receipt PDF | `app/views/templates/payment_receipts/` | libs/documents | planned | |
| Payment request email | `app/mailers/payment_request_mailer.rb`, `app/views/payment_request_mailer/requested.slim` | libs/mail | planned | Also rendered as a document |
| Document locales de, en, es, fr, it, nb, pt-BR, sv, zh-TW | `config/locales/` | libs/documents | planned | |
| Branding watermark | `app/views/templates/` | libs/documents | planned | No watermark; nothing to remove |
| UBL invoices (EN 16931, XRechnung 3.0, Peppol BIS 3) | `app/serializers/e_invoices/invoices/ubl/` | libs/documents | planned | |
| CII invoices embedded as Factur-X / ZUGFeRD | `app/serializers/e_invoices/invoices/cii/` | libs/documents | planned | |
| E-invoice code lists | `app/serializers/e_invoices/` | libs/documents | planned | |
| E-invoices for credit notes and receipts | `app/serializers/e_invoices/credit_notes/`, `app/serializers/e_invoices/payments/` | libs/documents | planned | |
| Document emails (invoice, credit note, receipt, payment request) | `app/mailers/` | libs/mail | planned | |
| Resend document email | `app/services/emails/resend_service.rb` | libs/mail | planned | |
| SMTP delivery and custom from address | `config/environments/production.rb` | libs/mail | planned | |

### 1.48 Scheduled Jobs

Each Lago clock job becomes a River periodic job or a job scheduled at the instant it is due ([Architecture §9](architecture.md#9-jobs-and-scheduling-libsjobs)). `libs/jobs` defines every entry of the schedule; a row is done when the library that owns its work registers the handler.

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Activate subscriptions and contracts, refresh drafts (every 5 minutes) | `clock.rb` | libs/jobs | in progress | Activation scheduled per subscription |
| Subscription activity processing (every minute) | `clock.rb` | libs/jobs | in progress | |
| Refresh lifetime usages and wallet ongoing balances (every 5 minutes) | `clock.rb` | libs/jobs | in progress | Wallets are also refreshed from usage refresh signals (§1.29) |
| Terminate ended subscriptions, validate events (hourly :05) | `clock.rb` | libs/jobs | in progress | |
| Subscription billing (hourly :10) | `clock.rb` (`SubscriptionsBiller`) | libs/jobs | in progress | Per-subscription jobs plus hourly reconciliation |
| Billing segments create and process (hourly :12, :17) | `clock.rb` | libs/jobs | in progress | |
| API key usage, daily usages (hourly :15) | `clock.rb` | libs/jobs | in progress | |
| Expire incomplete subscriptions, finalize invoices (hourly :20) | `clock.rb` | libs/jobs | in progress | |
| Mark invoices overdue (hourly :25) | `clock.rb` | libs/jobs | in progress | |
| Retry generating invoices, terminate coupons (hourly :30) | `clock.rb` | libs/jobs | in progress | |
| Free trial billing (hourly :35) | `clock.rb` | libs/jobs | in progress | |
| Expire order forms, cancel abandoned payments (hourly :40) | `clock.rb` | libs/jobs | in progress | |
| Terminate wallets, execute scheduled orders, dunning (hourly :45) | `clock.rb` | libs/jobs | in progress | |
| Termination alerts, terminate recurring rules (hourly :50) | `clock.rb` | libs/jobs | in progress | |
| Interval wallet top-ups (hourly :55) | `clock.rb` | libs/jobs | in progress | |
| Retry failed invoices, retry inbound webhooks (every 15 minutes) | `clock.rb` | libs/jobs | in progress | |
| Webhook, inbound webhook, deletion record cleanups (daily) | `clock.rb` | libs/jobs | in progress | |
| Subscription refresh queue consumption (every 10 seconds) | `clock.rb` | libs/jobs | planned | Change notifications in the consumer |
| Queues per workload | `config/sidekiq/` | libs/jobs | logic done | River queues sharded by organization, with per-organization token buckets |

### 1.49 Platform Services

| Feature | Lago source | Tanvo module | Status | Notes |
| :--- | :--- | :--- | :--- | :--- |
| Encryption of provider secrets and integration credentials | `app/models/concerns/secrets_storable.rb` | libs/store | planned | AES-256-GCM envelopes |
| Deletion records for downstream sync | `db/structure.sql` (`record_deletions`, `record_deletion()`), `db/views/exports_record_deletions_v01.sql`, `app/jobs/clock/record_deletions_cleanup_job.rb` | libs/store | planned | Deferred `AFTER DELETE` triggers on fees, fee taxes, invoice subscriptions and taxes (refreshing a draft recreates those rows); exported to the warehouse sync (§1.43); deleted after one month; deletion events also in `audit_events` |
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
| `from_email`, `remove_branding_watermark` | premium integration | core | |
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
| Triggered alert `resolved` and seeded kinds | `app/models/usage_monitoring/triggered_alert.rb` | Never produced. Lago is building `alert.resolved` (296436eac, a84ccd69c, 8fa46d700); revisit when it is reachable |
| Admin REST controllers (local only) | `app/controllers/admin/` | Replaced by `tanvo admin` |
| `customers.vat_rate`, `billing_entities.vat_rate` | `db/structure.sql` | Replaced by taxes |
| `billing_entities.archived_at`, `destroyBillingEntity` no-op mutation | `app/models/billing_entity.rb`, `app/graphql/mutations/billing_entities/` | Unused |
| GraphQL `providerCustomer` and the per-provider `*Customer` fields; singular `provider_customer` input | `app/graphql/types/customers/` | Replaced by connection lists |
| `RetryViesCheckJob` | `app/jobs/customers/retry_vies_check_job.rb` | Superseded by the VIES check job with backoff |
| `invoice_footer` assigned on a customer | `app/services/customers/update_service.rb` | There is no such column (§4) |
| `subscriptions.incompleted_at` and `cancelation_reason` spelled with one l | `db/structure.sql` | Duplicates of the correctly named columns |
| `overriden` filter alias | `app/controllers/api/v1/subscriptions_controller.rb` | Misspelled alias of `overridden` |
| `plan_overrides.usage_thresholds` | `app/services/subscriptions/create_service.rb` | Replaced by subscription usage thresholds |
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
| `dunning_campaigns.applied_to_organization` | `app/models/dunning_campaign.rb` (`ignored_columns`) | Replaced by billing entity assignment |
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
| Separate Google, Okta and Entra ID implementations | One OIDC identity provider with presets plus generic OIDC | One code path |
| Customer portal tokens from `MessageVerifier` (12 hours) | Short-lived signed tokens scoped to one customer | Narrower exposure |
| Webhook signatures JWT RS256 (default) or HMAC with the organization key | Standard Webhooks signatures with per-endpoint secrets and rotation | Standard, verifiable with existing libraries |
| Webhook payloads gzipped in ActiveStorage | Payloads stored with the delivery record | No blob store needed for deliveries |
| Sidekiq queues and the clockwork clock process | River jobs and periodic jobs run by an elected leader | Transactional enqueue, no extra process |
| Hourly scans of every subscription for billing and activation | Per-subscription jobs at the due instant plus hourly reconciliation | Work proportional to what is due |
| `pg_partman` partitions | Native partitions managed by Tanvo migrations and jobs | No extension |
| RisingWave `usage_buckets_15m` | 15-minute usage buckets maintained by the ingest consumers in PostgreSQL or ClickHouse | No extra system |
| Separate events-processor service with Debezium CDC and a Badger cache | Ingest consumers in the server's `consumer` role with an in-memory catalog cache refreshed by change notifications | Fewer moving parts |
| Kafka pipeline for activity, API and security logs | `audit_events` and log writers into ClickHouse or PostgreSQL | Works without Kafka |
| Deduplication by organization, external subscription id and transaction id | Deduplication by organization and transaction id | Transaction ids are unique per organization |
| Nango as an integration broker | Direct vendor API clients in `providers/*` | No third-party broker in the data path |
| Gotenberg HTML-to-PDF | Typst child process producing PDF/A-3b | Single binary, archival PDF |
| pdfcpu to embed Factur-X XML | Typst PDF/A-3 attachments | One rendering step |
| Two catalogs (plans and charges, product catalog behind a flag) | One catalog covering both (§1.14) | No parallel models |
| Tier ranges v1 (inclusive, next starts at +1) and v2 (adjacent) | Adjacent ranges only | One convention |
| Single payment provider per customer, multi-connection unfinished | Several connections per customer with defaults per category | Completes Lago's design |
| Streaming destinations without an API, Kinesis only | Destinations managed through the API; Kinesis first, connector interface for more | Configurable without database access |
| Inbound webhook storage only for Stripe and Moneyhash | Every provider's webhooks are stored and retried | Uniform reliability |
| Data API and Superset | `ee/analytics` | Cloud-only in Lago |
| Mistral-based finance assistant and AI conversations | `ee/assistant` with an MCP server | Cloud-only in Lago |
| CS admin and admin REST | `tanvo admin` commands; support tooling in `ee/` | No unauthenticated local endpoints |
| `LAGO_CREATE_ORG` bootstrap variables | `tanvo admin bootstrap` | Explicit command |
| OAuth proxy for provider callbacks | Callbacks served by `apps/server` | Self-contained |
| `throttling` gem with Redis | Per-provider token buckets | Works without Redis |
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
