-- Audit logs (parity §1.45): append-only, listed most recent first by
-- logged_at, then by id. They keep the IDs of users and API keys without
-- foreign keys, so deleting an API key keeps its history.

-- Security logs of access changes (sign-ins, invites, roles, API keys).
CREATE TABLE security_logs (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  user_id uuid,
  api_key_id uuid,
  log_type text NOT NULL,
  log_event text NOT NULL,
  resources jsonb,
  device_info jsonb,
  logged_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT security_logs_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id)
);

CREATE INDEX security_logs_logged_at_idx ON security_logs (organization_id, logged_at DESC, id);

ALTER TABLE security_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON security_logs
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- API logs of the write requests made with API keys; id is the request ID.
CREATE TABLE api_logs (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  api_key_id uuid NOT NULL,
  client text NOT NULL,
  http_method text NOT NULL,
  http_status integer NOT NULL,
  request_origin text NOT NULL,
  request_path text NOT NULL,
  request_body jsonb,
  request_response jsonb,
  api_version text NOT NULL,
  logged_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT api_logs_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT api_logs_http_method_check CHECK (http_method IN ('get', 'post', 'put', 'delete', 'patch'))
);

CREATE INDEX api_logs_logged_at_idx ON api_logs (organization_id, logged_at DESC, id);

ALTER TABLE api_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_logs
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Activity logs of resource changes.
CREATE TABLE activity_logs (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  user_id uuid,
  api_key_id uuid,
  activity_type text NOT NULL,
  activity_source text NOT NULL,
  activity_object jsonb,
  activity_object_changes jsonb,
  resource_id uuid NOT NULL,
  resource_type text NOT NULL,
  external_customer_id text,
  external_subscription_id text,
  logged_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT activity_logs_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT activity_logs_activity_source_check CHECK (activity_source IN ('api', 'front', 'system'))
);

CREATE INDEX activity_logs_logged_at_idx ON activity_logs (organization_id, logged_at DESC, id);

ALTER TABLE activity_logs ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON activity_logs
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
