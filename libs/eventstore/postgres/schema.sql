-- Event store schema (PostgreSQL). Idempotent: every statement can run again
-- on an existing schema. libs/store will own it as versioned migrations.
-- Every identifier of the event store is prefixed with the schema marker
-- (schemaMarker in schema.go), which Schema and Migrate replace with the
-- quoted PostgreSQL schema and a dot, or with nothing (see WithSchema). Partition names and advisory-lock keys
-- carry it too, so that two event stores of one database stay apart.

-- How the monthly partitions of events are split by hash of organization.
-- Set once, by the first migration; changing it needs a data migration.
CREATE TABLE IF NOT EXISTS __schema__.event_store_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    hash_partitions integer NOT NULL CHECK (hash_partitions BETWEEN 1 AND 256)
);

-- Ingestion order: ranks the events sharing a timestamp.
CREATE SEQUENCE IF NOT EXISTS __schema__.events_sequence AS bigint;

-- The events the queries read, one row per (organization, subscription,
-- code, transaction ID, timestamp). Partitioned by month of timestamp, each
-- month by hash of organization; a unique key must hold both partition
-- keys, so ingestion deduplication by (organization, subscription,
-- transaction ID) lives in event_transactions.
CREATE TABLE IF NOT EXISTS __schema__.events (
    organization_id text NOT NULL,
    subscription_id text NOT NULL,
    code text NOT NULL,
    "timestamp" timestamptz NOT NULL,
    transaction_id text NOT NULL,
    -- Property values as JSON strings.
    properties jsonb NOT NULL DEFAULT '{}'::jsonb,
    precise_total_amount_cents numeric,
    ingested_at timestamptz NOT NULL,
    sequence bigint NOT NULL,
    PRIMARY KEY (organization_id, subscription_id, code, "timestamp", transaction_id)
) PARTITION BY RANGE ("timestamp");

-- Charge filters and pricing group values select on properties by
-- containment (properties @> '{"region":"europe"}').
CREATE INDEX IF NOT EXISTS events_properties_idx ON __schema__.events USING gin (properties jsonb_path_ops);

-- The ingestion deduplication ledger: one row per deduplication key
-- (organization, external subscription, transaction ID), recording where
-- its event is (code, timestamp): the first accepted copy, or the latest
-- replacement. A ledger keyed by (organization, transaction ID) predates
-- this key; the event store refuses to run over it (pre-release: such an
-- event store is migrated again from scratch).
DO $$
BEGIN
    IF to_regclass('__schema__.event_transactions') IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM pg_attribute
        WHERE attrelid = to_regclass('__schema__.event_transactions') AND attname = 'subscription_id' AND NOT attisdropped
    ) THEN
        RAISE EXCEPTION 'event_transactions is keyed by (organization_id, transaction_id): drop the event store and migrate it again';
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS __schema__.event_transactions (
    organization_id text NOT NULL,
    subscription_id text NOT NULL,
    transaction_id text NOT NULL,
    code text NOT NULL,
    "timestamp" timestamptz NOT NULL,
    ingested_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, subscription_id, transaction_id)
) PARTITION BY HASH (organization_id);

-- Creates the hash partitions of event_transactions.
CREATE OR REPLACE FUNCTION __schema__.event_transactions_create_partitions() RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    n integer;
    created integer := 0;
BEGIN
    SELECT hash_partitions INTO STRICT n FROM __schema__.event_store_settings;
    PERFORM pg_advisory_xact_lock(hashtext('tanvo.event_store.__schema__.partitions'));
    FOR i IN 0 .. n - 1 LOOP
        IF to_regclass(format('__schema__.%I', format('event_transactions_%s', i))) IS NULL THEN
            EXECUTE format(
                'CREATE TABLE __schema__.%I PARTITION OF __schema__.event_transactions FOR VALUES WITH (MODULUS %s, REMAINDER %s)',
                format('event_transactions_%s', i), n, i);
            created := created + 1;
        END IF;
    END LOOP;
    RETURN created;
END
$$;

-- Creates the monthly partitions of events (and their hash partitions)
-- covering the months of from_time through to_time, UTC. Returns the number of
-- months created.
CREATE OR REPLACE FUNCTION __schema__.events_create_partitions(from_time timestamptz, to_time timestamptz) RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    n integer;
    month timestamp;
    name text;
    created integer := 0;
BEGIN
    SELECT hash_partitions INTO STRICT n FROM __schema__.event_store_settings;
    PERFORM pg_advisory_xact_lock(hashtext('tanvo.event_store.__schema__.partitions'));
    month := date_trunc('month', from_time AT TIME ZONE 'UTC');
    WHILE month <= to_time AT TIME ZONE 'UTC' LOOP
        name := format('events_%s', to_char(month, 'YYYYMM'));
        IF to_regclass(format('__schema__.%I', name)) IS NULL THEN
            EXECUTE format(
                'CREATE TABLE __schema__.%I PARTITION OF __schema__.events FOR VALUES FROM (%L) TO (%L) PARTITION BY HASH (organization_id)',
                name, month AT TIME ZONE 'UTC', (month + interval '1 month') AT TIME ZONE 'UTC');
            FOR i IN 0 .. n - 1 LOOP
                EXECUTE format(
                    'CREATE TABLE __schema__.%I PARTITION OF __schema__.%I FOR VALUES WITH (MODULUS %s, REMAINDER %s)',
                    format('%s_%s', name, i), name, n, i);
            END LOOP;
            created := created + 1;
        END IF;
        month := month + interval '1 month';
    END LOOP;
    RETURN created;
END
$$;

-- Usage bucket definitions: the buckets the store maintains for an
-- organization's billable metric code (see metering.BucketDefinition). A
-- definition is inserted not ready, which makes every writer maintain its
-- buckets, then backfilled in chunks, then marked ready for reads.
CREATE TABLE IF NOT EXISTS __schema__.usage_bucket_definitions (
    organization_id text NOT NULL,
    code text NOT NULL,
    definition_id text NOT NULL,
    -- The aggregated property; '' for buckets counting events only.
    property text NOT NULL,
    -- The properties the rows keep apart, sorted.
    dimensions text[] NOT NULL,
    -- Per folded dimension, its declared values: the rows keep the others
    -- as one value (metering.BucketOtherValue).
    fold jsonb NOT NULL DEFAULT '{}',
    -- Whether the buckets hold every stored event, so reads may use them.
    ready boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, code, definition_id)
);

-- The 15-minute usage buckets: per external subscription, code,
-- definition, UTC bucket and combination of dimension values, the totals
-- of the events. Maintained in the transaction of every write.
CREATE TABLE IF NOT EXISTS __schema__.usage_buckets (
    organization_id text NOT NULL,
    subscription_id text NOT NULL,
    code text NOT NULL,
    definition_id text NOT NULL,
    bucket timestamptz NOT NULL,
    -- The dimension properties present on the events of the row, and the
    -- SHA-256 of their canonical text, which keys the row: property values
    -- may outgrow an index entry.
    dimensions jsonb NOT NULL,
    dimensions_hash bytea NOT NULL,
    -- Every event of the row.
    events_count bigint NOT NULL,
    precise_total_amount_cents numeric NOT NULL,
    last_event_at timestamptz NOT NULL,
    last_ingested_at timestamptz NOT NULL,
    -- The events carrying a numeric value of the property.
    value_count bigint NOT NULL,
    units numeric NOT NULL,
    max_value numeric,
    latest_value numeric,
    latest_at timestamptz,
    latest_sequence bigint,
    value_precise_total_amount_cents numeric NOT NULL,
    target_wallet_code text GENERATED ALWAYS AS (dimensions->>'target_wallet_code') STORED,
    PRIMARY KEY (organization_id, subscription_id, code, definition_id, bucket, dimensions_hash)
);

-- The properties among keys, as a JSON object of strings, with the values
-- of a folded key its fold does not declare replaced by one value.
CREATE OR REPLACE FUNCTION __schema__.usage_bucket_dimensions(props jsonb, keys text[], fold jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT coalesce(jsonb_object_agg(k,
        CASE WHEN fold ? k AND props->>k <> '' AND NOT (fold->k ? (props->>k))
            THEN to_jsonb('__tanvo_other_value__'::text) ELSE props->k END
    ) FILTER (WHERE props ? k), '{}'::jsonb) FROM unnest(keys) AS k
$$;

-- Drops the monthly partitions of events ending at or before the given
-- time (a retention policy), with their usage buckets. The ledger keeps the
-- transaction IDs. Returns the number of months dropped. The months are
-- read from the partition names events_YYYYMM, which
-- events_create_partitions gives them, not from the text of their bounds
-- (which follows the DateStyle and TimeZone of the session); other
-- partitions are left alone. Dropping a partition takes an ACCESS
-- EXCLUSIVE lock on it and on events until the transaction ends: it waits
-- for the running reads and writes of events, and blocks the later ones
-- meanwhile.
CREATE OR REPLACE FUNCTION __schema__.events_drop_partitions(before timestamptz) RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    part record;
    month_start timestamptz;
    month_end timestamptz;
    dropped integer := 0;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('tanvo.event_store.__schema__.partitions'));
    FOR part IN
        SELECT c.oid::regclass AS name, substring(c.relname FROM '^events_([0-9]{6})$') AS yyyymm
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = '__schema__.events'::regclass
    LOOP
        CONTINUE WHEN part.yyyymm IS NULL;
        -- Month arithmetic on timestamp, then UTC: on timestamptz it would
        -- follow the TimeZone of the session.
        month_start := make_timestamp(substr(part.yyyymm, 1, 4)::integer, substr(part.yyyymm, 5, 2)::integer, 1, 0, 0, 0) AT TIME ZONE 'UTC';
        month_end := (make_timestamp(substr(part.yyyymm, 1, 4)::integer, substr(part.yyyymm, 5, 2)::integer, 1, 0, 0, 0) + interval '1 month') AT TIME ZONE 'UTC';
        IF month_end <= before THEN
            EXECUTE format('DROP TABLE %s', part.name);
            -- The usage buckets of the dropped events go with them: a
            -- month starts and ends on bucket boundaries.
            DELETE FROM __schema__.usage_buckets
            WHERE bucket >= month_start AND bucket < month_end;
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
