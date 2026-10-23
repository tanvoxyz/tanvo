-- Record deletions (parity §1.49): the tombstones of the deleted rows of
-- the tracked tables (export.TrackedTables), for the downstream sync of the
-- export views. The store writes them as the deleting transaction commits,
-- for the rows it deleted that are gone at the commit, all with the
-- commit's clock_timestamp(), so a sync whose cursor on updated_at passed a
-- tombstone never misses one committed before it. They are deleted one
-- month after the deletion (the clean_record_deletions clock job).
CREATE TABLE record_deletions (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  record_table text NOT NULL,
  record_id uuid NOT NULL,
  deleted_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT record_deletions_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT record_deletions_record_table_check CHECK (record_table IN ('fees', 'invoice_subscriptions', 'invoice_applied_taxes', 'credit_note_applied_taxes'))
);

CREATE INDEX record_deletions_deleted_at_idx ON record_deletions (organization_id, deleted_at);
-- The cleanup scan of every organization.
CREATE INDEX record_deletions_expiry_idx ON record_deletions (deleted_at);
-- The incremental sync reads by updated_at.
CREATE INDEX record_deletions_updated_at_idx ON record_deletions (organization_id, updated_at);

ALTER TABLE record_deletions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON record_deletions
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
