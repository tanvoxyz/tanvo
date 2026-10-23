-- Event store schema (ClickHouse). Idempotent; statements are separated by
-- lines holding only a semicolon.

-- The ledger of ingested events: one row per (organization, transaction
-- ID). ReplacingMergeTree keeps the row of the highest version, and the
-- version decreases with the ingestion sequence, so that merges keep the
-- first accepted event. The table is not partitioned: replacing merges
-- only collapse rows within a partition, and the key spans all time.
CREATE TABLE IF NOT EXISTS events_raw (
    organization_id String,
    transaction_id String,
    subscription_id String,
    code String,
    timestamp DateTime64(6, 'UTC'),
    properties Map(String, String),
    precise_total_amount_cents Nullable(Decimal(76, 26)),
    ingested_at DateTime64(6, 'UTC'),
    sequence UInt64,
    version UInt64 MATERIALIZED 18446744073709551615 - sequence
)
ENGINE = ReplacingMergeTree(version)
ORDER BY (organization_id, transaction_id)
;
-- The events the queries read: one row per (organization, code,
-- subscription, timestamp, transaction ID), the sorting key. The version is
-- the ingestion sequence, so that the latest copy (a re-enrichment or a
-- replacement) wins; queries read the table FINAL. Monthly partitions hold
-- every copy of a key, as the timestamp is part of it.
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
    -- The bucket repair sweep selects the events by sequence.
    INDEX sequence_minmax sequence TYPE minmax GRANULARITY 1
)
ENGINE = ReplacingMergeTree(sequence)
PARTITION BY toYYYYMM(timestamp)
ORDER BY (organization_id, code, subscription_id, timestamp, transaction_id)
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
-- of the events. A write recomputes the buckets it touched from
-- events_enriched FINAL, every row of a bucket with the same version: the
-- sum of the sequences of the bucket's events, which grows with every
-- event added or replaced, as events never leave their bucket. Reads take
-- the rows FINAL of the highest version of each bucket; rows a later
-- version no longer holds are tombstoned with it.
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
    version UInt256,
    is_deleted UInt8
)
ENGINE = ReplacingMergeTree(version, is_deleted)
PARTITION BY toYYYYMM(bucket)
ORDER BY (organization_id, code, subscription_id, definition_id, bucket, dimensions_key)
;
