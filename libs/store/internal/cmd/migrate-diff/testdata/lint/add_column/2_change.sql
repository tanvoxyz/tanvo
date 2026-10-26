ALTER TABLE fees ADD COLUMN currency text;
ALTER TABLE fees ADD COLUMN precise_amount numeric NOT NULL;
