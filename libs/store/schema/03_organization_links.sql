-- The default taxes and invoice custom sections of billing entities (domain
-- §4.2, §11.9, §22.10), in their order. Deleting a tax or a section removes
-- it from the defaults.

CREATE TABLE billing_entity_taxes (
  organization_id uuid NOT NULL,
  billing_entity_id uuid NOT NULL,
  tax_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, billing_entity_id, tax_id),
  CONSTRAINT billing_entity_taxes_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT billing_entity_taxes_tax_fkey FOREIGN KEY (organization_id, tax_id) REFERENCES taxes (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX billing_entity_taxes_tax_idx ON billing_entity_taxes (organization_id, tax_id);

ALTER TABLE billing_entity_taxes ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billing_entity_taxes
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE billing_entity_invoice_custom_sections (
  organization_id uuid NOT NULL,
  billing_entity_id uuid NOT NULL,
  invoice_custom_section_id uuid NOT NULL,
  position integer NOT NULL,
  PRIMARY KEY (organization_id, billing_entity_id, invoice_custom_section_id),
  CONSTRAINT billing_entity_invoice_custom_sections_billing_entity_fkey FOREIGN KEY (organization_id, billing_entity_id) REFERENCES billing_entities (organization_id, id) ON DELETE CASCADE,
  CONSTRAINT billing_entity_invoice_custom_sections_section_fkey FOREIGN KEY (organization_id, invoice_custom_section_id) REFERENCES invoice_custom_sections (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX billing_entity_invoice_custom_sections_section_idx ON billing_entity_invoice_custom_sections (organization_id, invoice_custom_section_id);

ALTER TABLE billing_entity_invoice_custom_sections ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON billing_entity_invoice_custom_sections
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');
