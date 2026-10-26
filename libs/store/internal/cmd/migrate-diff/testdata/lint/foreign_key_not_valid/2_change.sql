ALTER TABLE fees ADD CONSTRAINT fees_customer_fk FOREIGN KEY (customer_id) REFERENCES customers (id) NOT VALID;
ALTER TABLE fees VALIDATE CONSTRAINT fees_customer_fk;
