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
    version UInt64 MATERIALIZED if(replaced = 1, toUInt64(4611686018427387904) + sequence, toUInt64(toInt64(4611686018427387904) - toInt64(sequence)))
)
ENGINE = ReplacingMergeTree(version)
ORDER BY (organization_id, subscription_id, transaction_id)
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
    -- The bucket repair sweep selects the events by sequence.
    INDEX sequence_minmax sequence TYPE minmax GRANULARITY 1
)
ENGINE = ReplacingMergeTree(sequence, is_deleted)
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
