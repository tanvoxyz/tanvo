-- Event ingestion log (domain §10.1).

-- The append-only log of raw events (event.Log), which deduplicates them by
-- (organization, transaction_id) (domain §24 item 11). Aggregation does not
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
  CONSTRAINT events_transaction_id_key UNIQUE (organization_id, transaction_id)
);

ALTER TABLE events ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON events
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
