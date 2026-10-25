# Unported spec cases

Spec cases of the attributed usage query that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 60. Ported: 52. Unported: 8.

## Reasons

- **page** (4): The page-number meta of the REST response: the API pages with opaque page tokens and returns total_size, the number of values (parity §1.42).
- **order-by** (2): The order_by parameter of the service, which neither the REST controller nor the GraphQL resolver passes: the basis orders (amount on the amount basis, events count otherwise), and the API has no order_by (parity §1.42).
- **cache** (1): The 1-hour Rails cache of the price lookups, keyed on the filters' last update: Tanvo computes each lookup per query from the current filters, which a query reads in the same transaction as the plan, so a lookup is never stale (parity §1.42).
- **feature-flag** (1): The account_tree feature flag: Tanvo has no feature flags (parity §1.50); the query is available wherever the organization's events are in the ClickHouse store.

## Cases

### services/usage_attributions/charge_price_lookup_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 75 | with a cache store > "reuses the lookup until a filter value changes" | cache |

### services/usage_attributions/query_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 221 | with the default basis > when ordered by amount > "returns a validation failure" | order-by |
| 274 | when ordered by events count > "ranks the values by their events" | order-by |
| 481 | when the feature flag is disabled > "returns a forbidden failure" | feature-flag |

### services/usage_attributions/page_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 12 | without groups > it { expect(page.total_pages).to eq(0) } | page |
| 18 | with an exact number of pages > it { expect(page.total_pages).to eq(2) } | page |
| 24 | with a partial last page > it { expect(page.total_pages).to eq(3) } | page |
| 39 | "converts the offset to a page" | page |
