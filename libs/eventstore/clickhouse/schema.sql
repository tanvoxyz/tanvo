-- Event store schema (ClickHouse). Idempotent; statements are separated by
-- lines holding only a semicolon.

-- The ledger of ingested events: one row per deduplication key
-- (organization, external subscription, transaction ID), recording where
-- the event's current copy is (code, timestamp) and its sequence.
-- ReplacingMergeTree keeps the row of the highest version: the first copy
-- written by Store.Write (the version decreases with the sequence below
-- 2^62), unless Store.Replace replaced it (the version increases with the
-- sequence above 2^62). The sequences, nanoseconds of the writers' clocks,
-- stay below 2^62 until 2116. The table is not partitioned: replacing
-- merges only collapse rows within a partition, and the key spans all
-- time.
CREATE TABLE IF NOT EXISTS events_raw (
    organization_id String,
    subscription_id String,
    transaction_id String,
    code String,
    timestamp DateTime64(6, 'UTC'),
    properties Map(String, String),
    precise_total_amount_cents Nullable(Decimal(76, 26)),
    ingested_at DateTime64(6, 'UTC'),
    sequence UInt64,
    replaced UInt8 DEFAULT 0,
    -- The API request the event arrived with (metering.EventMetadata).
    user_agent String DEFAULT '',
    ip_address String DEFAULT '',
    version UInt64 MATERIALIZED if(replaced = 1, toUInt64(4611686018427387904) + sequence, toUInt64(toInt64(4611686018427387904) - toInt64(sequence)))
)
ENGINE = ReplacingMergeTree(version)
ORDER BY (organization_id, subscription_id, transaction_id)
;
-- The request metadata columns, for ledgers created before them.
ALTER TABLE events_raw ADD COLUMN IF NOT EXISTS user_agent String DEFAULT '' AFTER replaced
;
ALTER TABLE events_raw ADD COLUMN IF NOT EXISTS ip_address String DEFAULT '' AFTER user_agent
;
-- The events the queries read: one row per (organization, code,
-- subscription, timestamp, transaction ID), the sorting key. The version is
-- the ingestion sequence, so that the latest copy (a re-enrichment or a
-- replacement) wins; queries read the table FINAL. The copy a replacement
-- moved to another code or timestamp is tombstoned (is_deleted), which
-- FINAL hides. Monthly partitions hold every copy of a key, as the
-- timestamp is part of it.
CREATE TABLE IF NOT EXISTS events_enriched (
    organization_id String,
    code String,
    subscription_id String,
    timestamp DateTime64(6, 'UTC'),
    transaction_id String,
    properties Map(String, String),
    precise_total_amount_cents Nullable(Decimal(76, 26)),
    ingested_at DateTime64(6, 'UTC'),
    sequence UInt64,
    is_deleted UInt8 DEFAULT 0,
    -- The value enrichment gave the event (metering.Event.Value), as a
    -- decimal, and when the copy was written; the enriched events API
    -- returns them.
    value Nullable(String),
    decimal_value Nullable(Decimal(38, 26)) DEFAULT toDecimal128OrZero(value, 26),
    enriched_at DateTime64(6, 'UTC') DEFAULT now64(6),
    -- The attribution labels of the event, by attribution type code, that
    -- attributed usage groups by (metering.AttributedQuery); nothing
    -- writes them yet.
    attribution_labels Map(String, String),
    -- The bucket repair sweep selects the events by sequence.
    INDEX sequence_minmax sequence TYPE minmax GRANULARITY 1
)
ENGINE = ReplacingMergeTree(sequence, is_deleted)
PARTITION BY toYYYYMM(timestamp)
ORDER BY (organization_id, code, subscription_id, timestamp, transaction_id)
;
-- The value and enrichment columns, for tables created before them.
ALTER TABLE events_enriched ADD COLUMN IF NOT EXISTS value Nullable(String) AFTER is_deleted
;
ALTER TABLE events_enriched ADD COLUMN IF NOT EXISTS decimal_value Nullable(Decimal(38, 26)) DEFAULT toDecimal128OrZero(value, 26) AFTER value
;
ALTER TABLE events_enriched ADD COLUMN IF NOT EXISTS enriched_at DateTime64(6, 'UTC') DEFAULT now64(6) AFTER decimal_value
;
ALTER TABLE events_enriched ADD COLUMN IF NOT EXISTS attribution_labels Map(String, String) AFTER enriched_at
;
-- Usage bucket definitions: the buckets the store maintains for an
-- organization's billable metric code (see metering.BucketDefinition). A
-- definition is written not ready, its buckets are backfilled, then it is
-- written ready; reads use ready definitions only, while writers maintain
-- every definition. The version is the store's sequence.
CREATE TABLE IF NOT EXISTS usage_bucket_definitions (
    organization_id String,
    code String,
    definition_id String,
    property String,
    dimensions Array(String),
    -- Per folded dimension, its declared values: the rows keep the others
    -- as one value (metering.BucketOtherValue).
    fold Map(String, Array(String)),
    ready UInt8,
    is_deleted UInt8,
    version UInt64
)
ENGINE = ReplacingMergeTree(version, is_deleted)
ORDER BY (organization_id, code, definition_id)
;
-- The 15-minute usage buckets: per code, external subscription,
-- definition, UTC bucket and combination of dimension values, the totals
-- of the events. version counts the writes of a bucket: each write reads
-- the highest version of the bucket's rows and writes the rows it changes
-- one above, either merging its new events into the current rows or
-- recomputing them from events_enriched FINAL (see maintain.go); reads take
-- the rows FINAL. writer identifies the write, so that a write detects
-- another one at its version; max_sequence is the highest sequence of the
-- events of the bucket a row holds, so that a merge detects a recompute
-- that already holds its events. Rows a recompute no longer holds are
-- tombstoned with its version.
CREATE TABLE IF NOT EXISTS usage_buckets_15m (
    organization_id String,
    code String,
    subscription_id String,
    definition_id String,
    bucket DateTime64(6, 'UTC'),
    dimensions Map(String, String),
    dimensions_key String,
    events_count UInt64,
    precise_total_amount_cents Decimal(76, 26),
    last_event_at DateTime64(6, 'UTC'),
    last_ingested_at DateTime64(6, 'UTC'),
    value_count UInt64,
    units Decimal(76, 26),
    max_value Nullable(Decimal(76, 26)),
    latest_value Nullable(Decimal(76, 26)),
    latest_at Nullable(DateTime64(6, 'UTC')),
    latest_sequence UInt64,
    value_precise_total_amount_cents Decimal(76, 26),
    target_wallet_code String ALIAS dimensions['target_wallet_code'],
    max_sequence UInt64,
    version UInt64,
    writer UInt64,
    is_deleted UInt8
)
ENGINE = ReplacingMergeTree(version, is_deleted)
PARTITION BY toYYYYMM(bucket)
ORDER BY (organization_id, code, subscription_id, definition_id, bucket, dimensions_key)
;
-- The runs of Store.RepairRecentBuckets: each completed run's start, in
-- the clock of the ingestion sequences (nanoseconds), so that the next
-- run repairs the events stored since, minus a margin.
CREATE TABLE IF NOT EXISTS bucket_repairs (
    started UInt64,
    finished DateTime64(6, 'UTC')
)
ENGINE = MergeTree
ORDER BY started
TTL finished + INTERVAL 30 DAY
;
-- The messages the ingest consumers could not process, kept for
-- inspection and replay: each dead letter of the dead letter topic, with
-- the event's keys when its message has them (empty otherwise), the
-- original message and why it failed. subscription_id is the external
-- subscription ID, as in the event tables.
CREATE TABLE IF NOT EXISTS events_dead_letter (
    organization_id String,
    subscription_id String,
    code String,
    transaction_id String,
    timestamp DateTime64(6, 'UTC'),
    ingested_at DateTime64(6, 'UTC'),
    failed_at DateTime64(6, 'UTC'),
    event String,
    error_code String,
    error_message String,
    topic String,
    partition Int32,
    offset Int64
)
ENGINE = MergeTree
ORDER BY (organization_id, subscription_id, code, transaction_id, timestamp, ingested_at)
;
-- The audit logs (parity §1.45) of installations running ClickHouse: the
-- security, API and activity logs moved here from PostgreSQL, which holds
-- them in the transaction of their change until they are moved
-- (LogStore). The columns follow lago's log tables; maps hold each
-- top-level key with its value in JSON. ReplacingMergeTree keeps one row
-- per log, so that a move retried after its insert stores no duplicate;
-- the sorting key puts the logs of an organization in time order for the
-- lists, newest first. Reads are FINAL. There is no TTL: retention is
-- applied by the queries, as in lago.
CREATE TABLE IF NOT EXISTS security_logs (
    organization_id String,
    log_id String,
    user_id Nullable(String),
    api_key_id Nullable(String),
    log_type String,
    log_event String,
    device_info Map(String, String),
    resources Map(String, String),
    logged_at DateTime64(6, 'UTC'),
    created_at DateTime64(6, 'UTC'),
    INDEX idx_log_id log_id TYPE bloom_filter(0.001) GRANULARITY 1
)
ENGINE = ReplacingMergeTree
ORDER BY (organization_id, logged_at, log_id)
;
CREATE TABLE IF NOT EXISTS api_logs (
    organization_id String,
    request_id String,
    api_key_id String,
    api_version String,
    client String,
    request_body Map(String, String),
    request_response Map(String, String),
    request_path String,
    request_origin String,
    http_method Enum8('get' = 1, 'post' = 2, 'put' = 3, 'delete' = 4, 'patch' = 5),
    http_status Int32,
    logged_at DateTime64(6, 'UTC'),
    created_at DateTime64(6, 'UTC'),
    INDEX idx_request_id request_id TYPE bloom_filter(0.001) GRANULARITY 1
)
ENGINE = ReplacingMergeTree
ORDER BY (organization_id, logged_at, request_id)
;
CREATE TABLE IF NOT EXISTS activity_logs (
    organization_id String,
    activity_id String,
    user_id Nullable(String),
    api_key_id Nullable(String),
    external_customer_id Nullable(String),
    external_subscription_id Nullable(String),
    activity_type String,
    activity_source Enum8('api' = 1, 'front' = 2, 'system' = 3),
    activity_object Map(String, String),
    activity_object_changes Map(String, String),
    resource_id String,
    resource_type String,
    logged_at DateTime64(6, 'UTC'),
    created_at DateTime64(6, 'UTC'),
    INDEX idx_activity_id activity_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_external_customer_id external_customer_id TYPE bloom_filter(0.001) GRANULARITY 1,
    INDEX idx_external_subscription_id external_subscription_id TYPE bloom_filter(0.001) GRANULARITY 1
)
ENGINE = ReplacingMergeTree
ORDER BY (organization_id, logged_at, activity_id)
;
