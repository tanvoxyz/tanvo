-- Data exports and parts (domain §20).

CREATE TABLE data_exports (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  membership_id uuid NOT NULL,
  format text NOT NULL,
  resource_type text NOT NULL,
  -- The list filters of the resource, as the client sent them.
  resource_query json,
  status text NOT NULL,
  file text,
  started_at timestamptz,
  completed_at timestamptz,
  expires_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT data_exports_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT data_exports_membership_fkey FOREIGN KEY (organization_id, membership_id) REFERENCES memberships (organization_id, id),
  CONSTRAINT data_exports_format_check CHECK (format IN ('csv')),
  CONSTRAINT data_exports_resource_type_check CHECK (resource_type IN ('invoices', 'invoice_fees', 'credit_notes', 'credit_note_items')),
  CONSTRAINT data_exports_status_check CHECK (status IN ('pending', 'processing', 'completed', 'failed'))
);

CREATE INDEX data_exports_membership_idx ON data_exports (organization_id, membership_id);
-- The file cleanup job scans the expired files of every organization.
CREATE INDEX data_exports_expires_at_idx ON data_exports (expires_at) WHERE status = 'completed' AND file IS NOT NULL;

ALTER TABLE data_exports ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON data_exports
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- One batch of rows of an export; deleted with the export.
CREATE TABLE data_export_parts (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  data_export_id uuid NOT NULL,
  index integer NOT NULL,
  -- The exported documents are of several types: no foreign key.
  object_ids uuid[] NOT NULL,
  completed boolean NOT NULL DEFAULT false,
  csv_lines text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT data_export_parts_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT data_export_parts_data_export_fkey FOREIGN KEY (organization_id, data_export_id) REFERENCES data_exports (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT data_export_parts_index_key UNIQUE (organization_id, data_export_id, index),
  CONSTRAINT data_export_parts_index_check CHECK (index >= 0)
);

ALTER TABLE data_export_parts ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON data_export_parts
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
