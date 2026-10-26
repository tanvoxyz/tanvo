-- The analytics schema (architecture §14.1): data derived from the core's
-- export views, rebuilt per organization. No object here depends on a
-- core object, so that the core's migrations never meet it.

-- The organizations built, with the watermark of the core data they were
-- built from.
CREATE TABLE analytics.refresh_state (
  organization_id uuid NOT NULL,
  watermark timestamptz NOT NULL,
  refreshed_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id)
);

-- The plans seen, deleted ones kept: the export views drop deleted plans.
CREATE TABLE analytics.plans (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  billing_interval text NOT NULL,
  deleted boolean NOT NULL DEFAULT false,
  PRIMARY KEY (organization_id, id)
);

CREATE INDEX plans_code_idx ON analytics.plans (organization_id, code);

-- Each subscription's MRR in minor units over spans of days: from
-- start_date to the day before end_date, open-ended when end_date is null.
CREATE TABLE analytics.mrr_intervals (
  organization_id uuid NOT NULL,
  subscription_id uuid NOT NULL,
  customer_id uuid NOT NULL,
  plan_id uuid NOT NULL,
  billing_entity_id uuid,
  currency text NOT NULL,
  start_date date NOT NULL,
  end_date date,
  amount numeric NOT NULL,
  CONSTRAINT mrr_intervals_dates_check CHECK (end_date IS NULL OR end_date > start_date)
);

CREATE INDEX mrr_intervals_org_dates_idx ON analytics.mrr_intervals (organization_id, start_date, end_date);

-- The net revenue of each fee of a finalized invoice, to recognize
-- straight-line from start_date to the day before end_date.
CREATE TABLE analytics.recognition_schedules (
  organization_id uuid NOT NULL,
  fee_id uuid NOT NULL,
  invoice_id uuid,
  customer_id uuid NOT NULL,
  subscription_id uuid,
  plan_id uuid,
  billing_entity_id uuid,
  currency text NOT NULL,
  amount numeric NOT NULL,
  issuing_date date NOT NULL,
  start_date date NOT NULL,
  end_date date NOT NULL,
  PRIMARY KEY (organization_id, fee_id),
  CONSTRAINT recognition_schedules_dates_check CHECK (end_date > start_date)
);

CREATE INDEX recognition_schedules_org_issuing_idx ON analytics.recognition_schedules (organization_id, issuing_date);
CREATE INDEX recognition_schedules_org_start_idx ON analytics.recognition_schedules (organization_id, start_date);
