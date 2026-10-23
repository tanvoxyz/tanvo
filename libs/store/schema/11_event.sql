-- Event ingestion log (domain §10.1).

-- The append-only log of raw events (event.Log), which deduplicates them by
-- (organization, external_subscription_id, transaction_id) (domain §24 item
-- 11): one transaction ID under two subscriptions is two events. Aggregation does not
-- read it: enriched events live in the event store (libs/eventstore). There
-- is no foreign key to subscriptions or metrics: an event names them by
-- external ID and code, which may not exist (post-validation errors, §10.2).
-- properties and attribution_labels are JSON objects of strings.
CREATE TABLE events (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  transaction_id text NOT NULL,
  code text NOT NULL,
  external_subscription_id text NOT NULL,
  timestamp timestamptz NOT NULL,
  properties jsonb NOT NULL,
  precise_total_amount numeric,
  attribution_labels jsonb NOT NULL,
  ingested_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT events_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT events_subscription_transaction_id_key UNIQUE (organization_id, external_subscription_id, transaction_id)
);

-- The event API looks events up by transaction ID alone.
CREATE INDEX events_transaction_id_idx ON events (organization_id, transaction_id);

ALTER TABLE events ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON events
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Cached aggregation states of charges (domain §10.3): the running state
-- saved after each pay-in-advance event and at the end of a period for
-- recurring time-weighted sums. charge_filter_id is NULL for a charge's
-- default bucket; grouped_by is the JSON object of pricing group values
-- (null for a key without a value); presentation_breakdowns is the JSON
-- array of {groups, value}. The aggregation values are NULL when a state
-- does not carry them (a recurring state holds only current_aggregation).
-- Like fees, the states reference charges and filters without foreign keys:
-- charges are deleted, their history is not.
CREATE TABLE cached_aggregations (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  external_subscription_id text NOT NULL,
  charge_id uuid NOT NULL,
  charge_filter_id uuid,
  timestamp timestamptz NOT NULL,
  event_transaction_id text,
  grouped_by jsonb NOT NULL,
  current_aggregation numeric,
  max_aggregation numeric,
  max_aggregation_with_proration numeric,
  presentation_breakdowns jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT cached_aggregations_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT cached_aggregations_external_subscription_id_check CHECK (external_subscription_id <> ''),
  CONSTRAINT cached_aggregations_grouped_by_check CHECK (jsonb_typeof(grouped_by) = 'object'),
  CONSTRAINT cached_aggregations_presentation_breakdowns_check CHECK (jsonb_typeof(presentation_breakdowns) = 'array')
);

CREATE INDEX cached_aggregations_lookup_idx ON cached_aggregations
  (organization_id, external_subscription_id, charge_id, timestamp DESC, created_at DESC);

ALTER TABLE cached_aggregations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON cached_aggregations
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
