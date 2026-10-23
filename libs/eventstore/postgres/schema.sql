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
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
