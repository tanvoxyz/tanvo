# Unported spec cases

Spec cases of the usage buckets that are not ported, each with its reason. Paths are relative to the upstream `spec/` directory. Ported cases carry a `source:` reference in the tests; together the two lists cover every case of the files in `testdata/specs.json` (checked by `go run ./tools/speccov`).

Cases: 227. Ported: 116. Unported: 111.

## Reasons

- **cutover** (38): The comparison service, the duplicate counter and the rake task compare the RisingWave buckets with the events store to decide when an organization can be cut over to the buckets. Tanvo's buckets are written in the same write as the events and reads are exact at the window edges (parity §3, RisingWave row), so there is no second source to compare and no cutover; the conformance suites (buckettest, storetest bucket specs) and TestCurrentUsageFromBuckets check that the buckets and the events agree.
- **no-gate** (21): Tanvo ships realtime usage without lago's organization flag, kill switch, premium license or forced gate (parity §1.50 and the feature flag row): a store that keeps usage buckets serves them, and a store without them answers ErrBucketsUnavailable, which the billing side treats as "read the events" (libs/billing DefineUsageBuckets).
- **store-factory** (10): lago's provider builds an events store per aggregation (store_for), forwarding the window, the filters and the per-charge state, and picks its class and deduplication per organization (store_class, deduplicate, the store override). Tanvo has one event store per deployment (architecture §7.4), which always deduplicates at ingestion, and every aggregation hands it its whole metering.Request value, so there is no store to configure, share or pick.
- **sink-table** (7): These cases check the ActiveRecord model of the RisingWave sink table (its factory, table name, primary key, FINAL reads, read-only guard and nullable plan and wallet columns). Tanvo's bucket tables are owned and written by libs/eventstore, whose schema and behaviour are checked by the buckettest conformance suite on PostgreSQL and ClickHouse; buckets are keyed by dimensions, so there is no plan or target wallet column.
- **same-store** (6): lago reads its buckets from ClickHouse next to an events store it can fall back to. Tanvo's buckets live in the event store itself, so an outage of the bucket read is an outage of the events read too: the error is returned with %w rather than reported and swallowed, and every read of the store, the buckets' included, goes through the store's own retry and connection handling.
- **prefilter** (5): lago lists the charge filters the buckets hold usage for, so that a charge served whole skips the events store query that resolves the billing period's filters (serves_whole_charge_from_buckets?, precomputed_filter_ids). Tanvo has no such pre-filter query: every charge bucket is aggregated through the bucket gate, and the filters without units are dropped afterwards (usage.Options.SkipZeroUnitsFilters).
- **postgres-buckets** (4): lago keeps buckets only for organizations on the ClickHouse events store. Tanvo's PostgreSQL store keeps the same buckets in the same write as its events (buckettest runs on both stores), so a PostgreSQL organization is served from them too and the buckets cannot disagree with its events.
- **prefetch** (4): lago prefetches the buckets of every servable charge of the plan in one read keyed by charge and charge filter IDs (FetchBucketsService, charge_filter_ids_for). Tanvo's buckets are keyed by the metric's property dimensions, not by charge, and each charge bucket reads its own totals (metering.BucketRead), so there are no charge or filter IDs to list and no plan-wide read.
- **any-window** (3): lago's bucket store answers only the prefetched charges window and mints a plain store for any other one (for_window). Tanvo's metering.UsageBucketStore answers any window exactly, reading the partial buckets at its edges from the events (TestUsageBucketStoreCombines), so there is no sibling window to hand over.
- **ruby-state** (3): These cases check Ruby object mechanics with no Tanvo counterpart: the aggregation state the aggregators write on a store (aggregation_property, numeric_property) is part of metering.Request, and a metering.BucketTotal slice is a value the store returns, with no shared accumulator to freeze or copy.
- **served-empty** (3): UsageBucketSet#empty? backs the fallback of lago's provider when the set is empty, because a lagging RisingWave pipeline cannot be told apart from no usage (provider.rb:107-110). Tanvo's buckets are written in the same write as the events (parity §3, RisingWave row), so a ready definition with no rows is no usage and is served; a definition that is not ready yet answers ok=false and the events serve it (TestUsageBucketStoreAnswers).
- **query-shape** (2): These cases check the SQL lago emits against usage_buckets_15m (its sort order prefix, FINAL, delete marker and charge ID list). Tanvo's queries are per definition and dimension and are checked by their results in buckettest, not their text.
- **sink** (2): Delete markers are how the RisingWave sink retracts a row from the ReplacingMergeTree. Tanvo's stores maintain the buckets in the event write and correct a replaced event in place (buckettest "replace corrects the bucket"), so no retracted row exists to skip.
- **pipeline** (1): The default concerns the rows RisingWave writes without a precise amount. Tanvo's stores write every bucket with its amount, units and events count (buckettest), so no row lacks one.
- **segment** (1): A billing segment is priced through its product rate card (libs/billing/fees ProductSegment), which never sets metering.Request.UsageBuckets; only the charges of a plan reach the bucket gate (fees.servedFromBuckets), as in lago.
- **validation** (1): This case checks the charge properties validation refusing a latest metric on a graduated_percentage charge; Tanvo ports that validation in the charge validation (pricing.ValidateOptions.LatestAggregation, set by libs/billing/plans/charges.go), not in the bucket classification, which never sees such a charge.

## Cases

### services/events/stores/usage_bucket_store_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 231 | "forwards the per-charge state the aggregators write" | ruby-state |
| 239 | "mints a plain store for a sibling window, which no bucket answers for" | any-window |

### services/events/stores/usage_bucket_set_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 23 | "is false when the set carries rows" | served-empty |
| 28 | when built with no rows > "is true" | served-empty |
| 43 | "lists the filters of that charge only" | prefetch |
| 47 | "is empty for a charge the buckets do not carry" | prefetch |
| 139 | "defaults the precise amount to zero, as the pipeline writes on every type but sum" | pipeline |
| 191 | "is frozen so a computation cannot rewrite the window it read" | ruby-state |
| 195 | "copies the rows, so the builder keeps writing to its own accumulators" | ruby-state |

### services/realtime_usage/fetch_buckets_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 84 | "ignores the buckets the sink marked as deleted" | sink |
| 237 | with a plan mixing aggregation types > "answers each charge by its own type, out of a single read" | prefetch |
| 275 | when clickhouse is unreachable > "fails, so the caller knows the buckets were not read rather than empty" | same-store |
| 292 | when clickhouse is unreachable > when the read raises #{error_class} > "fails rather than raising" | same-store |
| 297 | when clickhouse is unreachable > when the read raises #{error_class} > "reports it, as a silent fallback would hide an unreachable clickhouse" | same-store |
| 311 | when the read succeeds > "goes through the shared retry helper, like every other clickhouse read" | same-store |
| 322 | "seeks the sort order prefix and states the delete marker" | query-shape |
| 331 | "leaves out the charges the computation is not about" | query-shape |
| 401 | when the organization flag is off > it { expect(fetch).to be_success } | no-gate |
| 402 | when the organization flag is off > it { expect(fetch.usage_buckets).to be_nil } | no-gate |
| 408 | when the organization still reads the postgres events store > it { expect(fetch).to be_success } | postgres-buckets |
| 409 | when the organization still reads the postgres events store > it { expect(fetch.usage_buckets).to be_nil } | postgres-buckets |
| 415 | when the kill switch is off > it { expect(fetch).to be_success } | no-gate |
| 416 | when the kill switch is off > it { expect(fetch.usage_buckets).to be_nil } | no-gate |

### services/realtime_usage/hourly_breakdown_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 139 | with a bucket the sink has retracted > "leaves the deleted row out of the filters, the hours and the ingestion timestamp" | sink |
| 202 | when clickhouse is unreachable > "fails rather than raising, and reports the outage" | same-store |
| 222 | when realtime usage is not enabled for the organization > "fails" | no-gate |

### models/clickhouse/usage_bucket_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 8 | "persists a record via the factory" | sink-table |
| 13 | "is usage_buckets_15m" | sink-table |
| 19 | "matches the ClickHouse table primary key" | sink-table |
| 27 | "reads with FINAL" | sink-table |
| 69 | "is read-only so the RisingWave sink stays the only writer" | sink-table |
| 73 | "refuses to save" | sink-table |
| 99 | "allows plan_id and target_wallet_code to be null" | sink-table |

### services/realtime_usage_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 15 | it { expect(enabled).to be(true) } | no-gate |
| 20 | when the organization flag is off > it { expect(enabled).to be(false) } | no-gate |
| 26 | when the organization still reads the postgres events store > "refuses, because the buckets and the postgres events would disagree" | postgres-buckets |
| 34 | when the kill switch is off > it { expect(enabled).to be(false) } | no-gate |
| 40 | when the kill switch is set to false > it { expect(enabled).to be(false) } | no-gate |
| 46 | when clickhouse is not available > it { expect(enabled).to be(false) } | no-gate |
| 52 | without a premium license > it { expect(enabled).to be(false) } | no-gate |
| 56 | with an active store override > "refuses, so the clickhouse migration keeps comparing two event stores" | no-gate |
| 67 | with a forced gate > "opens the organization flag and the kill switch" | no-gate |
| 78 | with a forced gate > without a premium license > "keeps refusing, because the license is not part of the gate" | no-gate |
| 88 | with a forced gate > when the organization still reads the postgres events store > "keeps refusing, because the store is not part of the gate" | no-gate |
| 98 | "opens the gate for the duration of the block only" | no-gate |
| 106 | "closes the gate when the block raises" | no-gate |
| 111 | "refuses to nest, so a forgotten gate cannot outlive its block" | no-gate |
| 151 | with a latest_agg metric on a graduated_percentage charge > "cannot exist, the charge properties validation rejecting the pair" | validation |

### services/events/stores/provider_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 30 | "returns a store configured for the metered item and the given window" | store-factory |
| 40 | "forwards the filters" | store-factory |
| 50 | "takes the window per call, so one provider serves aggregations on different windows" | store-factory |
| 57 | "mints one instance per call, so aggregators cannot share per-charge state" | store-factory |
| 129 | when realtime usage is disabled for the organization > "is false" | no-gate |
| 184 | "reads the buckets once, however many charges it is asked about" | prefetch |
| 205 | when the organization is not enabled for realtime usage > "reads events, without asking clickhouse for buckets" | no-gate |
| 214 | when the organization still reads the postgres events store > "reads events, because the buckets and the postgres events would disagree" | postgres-buckets |
| 222 | when the deployment kill switch is off > "reads events" | no-gate |
| 362 | when the window holds no bucket at all > "reads events, as an empty set is no proof the pipeline wrote this window" | served-empty |
| 397 | when the prefetch failed to read clickhouse > "reads events rather than raising, as an unreachable clickhouse makes usage slow, not broken" | same-store |
| 441 | when the store is asked for another window than the one prefetched > "reads events, as the buckets cover the window they were fetched for" | any-window |
| 452 | when the provider was built without a window > "reads events, having no window to have fetched buckets for" | any-window |
| 461 | with a billing segment > "reads events, because the buckets are keyed by charge" | segment |
| 475 | "is true only when the store serves every pricing bucket of that charge" | prefilter |
| 525 | with a charge the buckets cannot answer > "is false, without reading clickhouse" | prefilter |
| 542 | "returns the filters of the charge the buckets hold usage for, the default one as nil" | prefilter |
| 549 | when the buckets hold nothing for the charge > "returns no filter, which leaves every fee of the charge at zero units" | prefilter |
| 560 | when no bucket was fetched > "returns no filter" | prefilter |
| 568 | "resolves the postgres store by default" | store-factory |
| 577 | with a clickhouse organization > "resolves the clickhouse store" | store-factory |
| 583 | with an active store override > "honors the override" | store-factory |
| 592 | "is false when the organization does not deduplicate" | store-factory |
| 601 | when the organization deduplicates its clickhouse events > "is true" | store-factory |
| 607 | with an active store override > "honors the override" | store-factory |

### services/realtime_usage/compare_usage_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 70 | it .events_count | cutover |
| 71 | it .duplicates_by_code | cutover |
| 72 | it .duplicates_count | cutover |
| 77 | "computes both usages over the same timestamp, without the charge cache and without taxes" | cutover |
| 88 | "computes both usages over the charges the buckets serve only" | cutover |
| 94 | "reports a match and how many comparable charges the buckets served" | cutover |
| 102 | "closes the forced gate once the comparison is over" | cutover |
| 109 | "reports the duplicate density of the compared window" | cutover |
| 120 | when the bucket usage differs from the events store usage > "reports the difference on the leaf, with both values" | cutover |
| 138 | when the same units were aggregated over another number of events > "reports the difference, the event count being served with the units" | cutover |
| 151 | when the units differ over an unchanged event count and the window holds duplicates > "reports the difference and labels it as a possible re-sent transaction id" | cutover |
| 163 | when the units differ over an unchanged event count without a duplicate to explain it > "reports a mismatch rather than a cutover risk" | cutover |
| 176 | when the duplicates of the window belong to another metric > "reports a mismatch rather than a cutover risk" | cutover |
| 187 | when the charge breaks its usage down by presentation group keys > "reports the charge as not served rather than comparing it with itself" | cutover |
| 198 | when an absent group value is empty on one side and nil on the other > "matches the leaves rather than reporting two half-empty ones" | cutover |
| 217 | when the catch-all leaf of a charge mixing filters with group keys is empty > "reports it as a mismatch, the stream writing that bucket like any other" | cutover |
| 233 | when the subscription received events during the comparison > "compares once more before reporting the difference" | cutover |
| 254 | when the bucket read fails during the comparison > "fails the run rather than comparing the events store with itself" | cutover |
| 263 | when the organization still reads the postgres events store > "reports the run as declined without computing any usage" | cutover |
| 274 | when the buckets hold nothing for the window > "reports the run as declined" | cutover |
| 283 | when the plan has no comparable charge > "reports that there was nothing to compare" | cutover |

### services/realtime_usage/count_duplicate_events_service_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 27 | "counts the events the subscription sent twice under the same transaction id" | cutover |
| 37 | "survives the merge that collapses the re-sent rows of the enriched table" | cutover |
| 45 | "reports no duplicate when every transaction id is unique" | cutover |
| 57 | with several metrics in the window > "reports the duplicates of each metric on its own code" | cutover |
| 67 | "ignores the events of another metric and the ones outside the window" | cutover |

### lib/tasks/recipes/realtime_usage_rake_spec.rb

| Line | Case | Reason |
| ---: | :--- | :--- |
| 39 | it .rows | cutover |
| 40 | it .differences | cutover |
| 41 | it .cutover_risks | cutover |
| 42 | it .eligible_charges_count | cutover |
| 43 | it .served_charges_count | cutover |
| 44 | it .duplicate_events_count | cutover |
| 45 | it .declined_reason | cutover |
| 46 | it .rechecked | cutover |
| 68 | "compares the given subscription and writes one CSV row per leaf" | cutover |
| 99 | when a leaf mismatches > "reports the mismatch" | cutover |
| 107 | when the only difference may be explained by a re-sent transaction id > "reports it rather than calling the run clean" | cutover |
| 115 | when nothing was served from the buckets > "says the run proves nothing about parity" | cutover |
