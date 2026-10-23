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
    sequence UInt64
)
ENGINE = ReplacingMergeTree(sequence)
PARTITION BY toYYYYMM(timestamp)
ORDER BY (organization_id, code, subscription_id, timestamp, transaction_id)
;
