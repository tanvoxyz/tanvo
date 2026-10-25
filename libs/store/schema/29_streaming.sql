-- Streaming destinations and their delivery locks (parity §1.40).
--
-- A destination is an operator-configured sink, such as an Amazon Kinesis
-- data stream, that receives the refreshed usage of an organization's
-- customers. The operator inserts and updates the rows directly; the
-- constraints below hold the rules of streaming.Destination.Validate.

-- A destination: its transport, the event types it claims, whether it is
-- active, the plans excluded from full-usage events, and the settings of
-- its transport. An event type is claimed by at most one destination of an
-- organization, active or not.
CREATE TABLE streaming_destinations (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  type text NOT NULL,
  active boolean NOT NULL DEFAULT false,
  event_types text[] NOT NULL,
  customer_full_usage_excluded_plan_codes text[] NOT NULL DEFAULT '{}',
  -- Kinesis settings.
  stream_arn text NOT NULL DEFAULT '',
  region text NOT NULL DEFAULT '',
  role_arn text NOT NULL DEFAULT '',
  external_id text NOT NULL DEFAULT '',
  partition_key text NOT NULL DEFAULT 'customer_external_id',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT streaming_destinations_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT streaming_destinations_type_check CHECK (type IN ('kinesis')),
  CONSTRAINT streaming_destinations_event_types_check CHECK (
    cardinality(event_types) > 0
    AND event_types <@ ARRAY['customer_usage.refreshed.v1', 'customer_full_usage.refreshed.v1']::text[]
  ),
  CONSTRAINT streaming_destinations_kinesis_check CHECK (
    type <> 'kinesis' OR (stream_arn <> '' AND region <> '' AND role_arn <> '')
  ),
  CONSTRAINT streaming_destinations_partition_key_check CHECK (partition_key IN ('customer_external_id'))
);

-- One claim of each event type per organization.
CREATE UNIQUE INDEX streaming_destinations_customer_usage_key ON streaming_destinations (organization_id)
  WHERE 'customer_usage.refreshed.v1' = ANY (event_types);
CREATE UNIQUE INDEX streaming_destinations_customer_full_usage_key ON streaming_destinations (organization_id)
  WHERE 'customer_full_usage.refreshed.v1' = ANY (event_types);

ALTER TABLE streaming_destinations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON streaming_destinations
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The locks of the delivery of one event type for one customer: the
-- enqueue lock, held from the enqueue of a delivery job until it starts,
-- and the runtime lock, held while it runs. Each lock is held until its
-- instant, so a lost worker releases it when it expires.
CREATE TABLE streaming_delivery_locks (
  organization_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  event_type text NOT NULL,
  enqueued_until timestamptz,
  running_until timestamptz,
  PRIMARY KEY (organization_id, customer_id, event_type),
  CONSTRAINT streaming_delivery_locks_customer_fkey FOREIGN KEY (organization_id, customer_id)
    REFERENCES customers (organization_id, id) ON DELETE CASCADE
);

ALTER TABLE streaming_delivery_locks ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON streaming_delivery_locks
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
