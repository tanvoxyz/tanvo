-- Event store schema (PostgreSQL). Idempotent: every statement can run again
-- on an existing schema. libs/store will own it as versioned migrations.

-- How the monthly partitions of events are split by hash of organization.
-- Set once, by the first migration; changing it needs a data migration.
CREATE TABLE IF NOT EXISTS event_store_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    hash_partitions integer NOT NULL CHECK (hash_partitions BETWEEN 1 AND 256)
);

-- Ingestion order: ranks the events sharing a timestamp.
CREATE SEQUENCE IF NOT EXISTS events_sequence AS bigint;

-- The events the queries read, one row per (organization, subscription,
-- code, transaction ID, timestamp). Partitioned by month of timestamp, each
-- month by hash of organization; a unique key must hold both partition
-- keys, so ingestion deduplication by (organization, transaction ID) lives
-- in event_transactions.
CREATE TABLE IF NOT EXISTS events (
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
CREATE INDEX IF NOT EXISTS events_properties_idx ON events USING gin (properties jsonb_path_ops);

-- The ingestion deduplication ledger: the first accepted event of each
-- (organization, transaction ID).
CREATE TABLE IF NOT EXISTS event_transactions (
    organization_id text NOT NULL,
    transaction_id text NOT NULL,
    "timestamp" timestamptz NOT NULL,
    ingested_at timestamptz NOT NULL,
    PRIMARY KEY (organization_id, transaction_id)
) PARTITION BY HASH (organization_id);

-- Creates the hash partitions of event_transactions.
CREATE OR REPLACE FUNCTION event_transactions_create_partitions() RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    n integer;
    created integer := 0;
BEGIN
    SELECT hash_partitions INTO STRICT n FROM event_store_settings;
    PERFORM pg_advisory_xact_lock(hashtext('tanvo.event_store.partitions'));
    FOR i IN 0 .. n - 1 LOOP
        IF to_regclass(format('event_transactions_%s', i)) IS NULL THEN
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF event_transactions FOR VALUES WITH (MODULUS %s, REMAINDER %s)',
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
CREATE OR REPLACE FUNCTION events_create_partitions(from_time timestamptz, to_time timestamptz) RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    n integer;
    month timestamp;
    name text;
    created integer := 0;
BEGIN
    SELECT hash_partitions INTO STRICT n FROM event_store_settings;
    PERFORM pg_advisory_xact_lock(hashtext('tanvo.event_store.partitions'));
    month := date_trunc('month', from_time AT TIME ZONE 'UTC');
    WHILE month <= to_time AT TIME ZONE 'UTC' LOOP
        name := format('events_%s', to_char(month, 'YYYYMM'));
        IF to_regclass(name) IS NULL THEN
            EXECUTE format(
                'CREATE TABLE %I PARTITION OF events FOR VALUES FROM (%L) TO (%L) PARTITION BY HASH (organization_id)',
                name, month AT TIME ZONE 'UTC', (month + interval '1 month') AT TIME ZONE 'UTC');
            FOR i IN 0 .. n - 1 LOOP
                EXECUTE format(
                    'CREATE TABLE %I PARTITION OF %I FOR VALUES WITH (MODULUS %s, REMAINDER %s)',
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
CREATE TABLE IF NOT EXISTS usage_bucket_definitions (
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
CREATE TABLE IF NOT EXISTS usage_buckets (
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
CREATE OR REPLACE FUNCTION usage_bucket_dimensions(props jsonb, keys text[], fold jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT coalesce(jsonb_object_agg(k,
        CASE WHEN fold ? k AND props->>k <> '' AND NOT (fold->k ? (props->>k))
            THEN to_jsonb('__tanvo_other_value__'::text) ELSE props->k END
    ) FILTER (WHERE props ? k), '{}'::jsonb) FROM unnest(keys) AS k
$$;

-- Drops the monthly partitions of events ending before the given time (a
-- retention policy). The ledger keeps the transaction IDs. Returns the
-- number of months dropped.
CREATE OR REPLACE FUNCTION events_drop_partitions(before timestamptz) RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    part record;
    dropped integer := 0;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('tanvo.event_store.partitions'));
    FOR part IN
        SELECT c.oid::regclass AS name, pg_get_expr(c.relpartbound, c.oid) AS bound
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = 'events'::regclass
    LOOP
        IF part.bound LIKE 'FOR VALUES FROM%'
            AND substring(part.bound FROM 'TO \(''([^'']+)''\)')::timestamptz <= before THEN
            EXECUTE format('DROP TABLE %s', part.name);
            -- The usage buckets of the dropped events go with them: a
            -- month starts and ends on bucket boundaries.
            DELETE FROM usage_buckets
            WHERE bucket >= substring(part.bound FROM 'FROM \(''([^'']+)''\)')::timestamptz
                AND bucket < substring(part.bound FROM 'TO \(''([^'']+)''\)')::timestamptz;
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
