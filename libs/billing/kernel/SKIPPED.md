# Unported spec cases

Spec cases of the charge usage cache that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 47. Ported: 34. Unported: 13.

## Reasons

- **expire** (7): Explicit expiry is for event deletion and duplicate cleanup, which change usage without advancing the watermark: lago no longer deletes events when a metric is destroyed (docs/parity.md, deprecated features), and the event stores deduplicate at ingestion, so no Tanvo path removes events the cache aggregated. A replacement or re-enrichment advances the watermark (`written_at` in PostgreSQL, `enriched_at` in ClickHouse).
- **legacy** (2): Entry shapes of the shared Rails cache from before lazy validation; Tanvo's cache only ever holds encoded `kernel.UsageCacheEntry` values, and one it cannot decode is a miss.
- **go-types** (1): An interface method cannot be left unimplemented in Go.
- **hook** (1): A hook for other cache services: the charge cache keeps every computed value (lago ChargeCacheService does not override `cacheable?`); Tanvo has no other cache service.
- **key-segments** (1): `usageCacheKey` appends the full-usage segment after the filter segments independently of them; the filter segment is ported at charge_cache_service_spec.rb:21 and the full-usage one at :30.
- **untracked** (1): Every Tanvo cache entry carries its watermark; there is no cache service without lazy validation.

The cache is `kernel.UsageCache`, encoded into the installation's `port.Cache`, in process or on Redis or Valkey (architecture §8); `fees.Service` reads and fills it per charge bucket (libs/billing/fees/usage_cache.go), validating each entry against the store's ingestion watermark (`metering.Watermark`).

## Cases

### services/cache_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 88 | when the subclass rejects the value > "returns the value without caching it" | hook |
| 280 | "does not gate services that do not track the creation time" | untracked |
| 412 | when the wrapped value has no cached_at > "recomputes rather than assuming the entry is fresh" | legacy |
| 424 | when a legacy unwrapped value exists > "recomputes so the entry is rewritten in the new shape" | legacy |
| 442 | "deletes the cache" | expire |
| 450 | "creates an instance and calls expire_cache" | expire |
| 464 | "raises NotImplementedError when called on the base class" | go-types |

### services/subscriptions/charge_cache_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 38 | with full usage > with a charge filter > "returns the cache key with the charge filter" | key-segments |
| 47 | "deletes the cached value" | expire |
| 62 | "deletes both the current usage and the full usage entries" | expire |
| 82 | "expires cache for each (subscription, charge) pair across the supplied ids" | expire |
| 95 | "loads plan, charges and filters in a constant number of queries" | expire |
| 112 | "delegates to .expire_for_subscriptions with the subscription's id" | expire |
