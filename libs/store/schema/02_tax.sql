-- Taxes (domain §7.7). The owners of a tax (billing entities, customers,
-- plans, charges, fixed charges, add-ons, commitments) reference it through
-- join tables that cascade when the tax is deleted.

CREATE TABLE taxes (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  rate numeric NOT NULL,
  description text,
  auto_generated boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT taxes_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT taxes_code_key UNIQUE (organization_id, code),
  CONSTRAINT taxes_rate_check CHECK (rate >= 0 AND rate <= 100)
);

ALTER TABLE taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
