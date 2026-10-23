-- Idempotency keys (architecture §4.2): the response of a write called
-- with an Idempotency-Key header, kept 24 hours per organization and key.
-- A row without a response is a request still running. Expired rows are
-- replaced by a new request with the same key and purged by a clock job.
CREATE TABLE idempotency_keys (
  organization_id uuid NOT NULL,
  key text NOT NULL,
  procedure text NOT NULL,
  request_hash bytea NOT NULL,
  response bytea,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, key),
  CONSTRAINT idempotency_keys_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT idempotency_keys_key_check CHECK (octet_length(key) BETWEEN 1 AND 255)
);

-- The purge clock job scans the expired rows of every organization.
CREATE INDEX idempotency_keys_expires_at_idx ON idempotency_keys (expires_at);

ALTER TABLE idempotency_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON idempotency_keys
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
