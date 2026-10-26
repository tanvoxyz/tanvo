CREATE TABLE customers (id bigint PRIMARY KEY);
CREATE TABLE fees (id bigint PRIMARY KEY, customer_id bigint, amount numeric NOT NULL, note text, code text);
