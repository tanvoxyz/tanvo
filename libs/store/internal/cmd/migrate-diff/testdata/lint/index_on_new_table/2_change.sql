CREATE TABLE wallets (id bigint PRIMARY KEY, customer_id bigint NOT NULL REFERENCES customers (id), code text NOT NULL);
CREATE UNIQUE INDEX wallets_code ON wallets (code);
