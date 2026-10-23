# Unported lago-api cases

The lago-api specs `spec/services/entitlement/**` are ported into the `*_test.go`
files of this package, and the request specs `spec/requests/api/v1/features_controller_spec.rb`,
`spec/requests/api/v1/plans/entitlements*` and `spec/requests/api/v1/subscriptions/entitlements*`
into `apps/server/internal/rpc/features_test.go` and `entitlements_test.go`. Each ported
case has a `source:` line. This file lists the cases that were not ported, grouped by reason.

## Not in Tanvo

- Activity logs (`Utils::ActivityLog.produce` expectations in every service spec). Tanvo has no activity log producer for catalog changes.
- `send_webhook: false` contexts (`feature_update_service_spec.rb`, `plan_entitlements_update_service-*_spec.rb`). Only lago's GraphQL plan mutations pass it; Tanvo always sends the webhooks.
- GraphQL resolvers and mutations (`spec/graphql/**/entitlement*`, `spec/graphql/**/feature*`).
- `bullet` N+1 query checks in the request specs.

## Behavior covered elsewhere or not applicable

- `subscription_entitlements_update_service-full_spec.rb` and the `partial: false` contexts of `subscription_entitlement_core_update_service_spec.rb`: no v1.55.0 caller uses full mode for subscriptions (`subscriptions/entitlements_controller.rb:21-25` passes `partial: true`), so the service has no subscription replace.
- `subscription_entitlement_update_service_spec.rb`: the per-feature wrapper of the core update service; its cases are those of `subscription_entitlement_core_update_service_spec.rb`, which are ported.
- "already discarded" contexts of `feature_destroy_service_spec.rb` and `privilege_destroy_service_spec.rb`: a discarded feature or privilege is not found by the repositories, so the service returns `feature` or `privilege` not found (ported as the not-found cases).
- Type casts of values given as JSON numbers, booleans or arrays (`plans/entitlements_controller_spec.rb:184` sends `[12, 13]`): values are strings in the API, so the invalid case is ported with an invalid string.
- `subscriptions/entitlements_controller_spec.rb:82-120`: the `subscription_status` and `status` parameters. The API looks a subscription up by ID in any status or by `external_id:` when active.
- `plans/entitlements_controller_spec.rb:43` and `plans/entitlements/privileges_controller_spec.rb:64`: child plans are never resolved as the parent (a domain test checks that a child plan is not found).
