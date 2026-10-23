# Unported lago-api cases

The lago-api specs `spec/services/add_ons/{create,update,destroy,apply_taxes}_service_spec.rb`
are ported into `service_test.go`. Each ported case has a `// source:` comment.
This file lists the cases that were not ported, grouped by reason.

## Product analytics

- `create_service_spec.rb:35`: "calls SegmentTrackJob". Tanvo sends no product analytics.

## Typed inputs

- `apply_taxes_service_spec.rb:19`: "returns applied taxes". The taxes are part of the returned add-on, which the ported cases read.
- `apply_taxes_service_spec.rb:27`: a `nil` add-on. Taxes are set by `Create` and `Update`, which load the add-on first; the add-on-not-found cases of `Update` and `Delete` are ported.
- `update_service_spec.rb:87`: an add-on used by an applied add-on (a one-off invoice) still updates every field. Tanvo has no applied add-ons; `Update` never locks a field, as the ported update case shows.

## Transactions

- `destroy_service_spec.rb:37`: the fixed charges fail to be deleted, and the add-on stays. Every write runs in one transaction, so a failure rolls back both; the failure cannot be injected through the memstore.

## Soft delete

- `destroy_service_spec.rb:15,25` are ported as hard deletes: Tanvo deletes the add-on and its fixed charges, where lago sets `deleted_at`. Fees keep `add_on_id` without a foreign key, and the code is free again (`create_service_spec.rb:51` is ported).
