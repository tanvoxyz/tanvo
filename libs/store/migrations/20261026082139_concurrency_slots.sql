-- Create "concurrency_slots" table
CREATE TABLE "concurrency_slots" ("name" text NOT NULL, "slot" integer NOT NULL, "holder" uuid NOT NULL, "expires_at" timestamptz NOT NULL, PRIMARY KEY ("name", "slot"), CONSTRAINT "concurrency_slots_name_check" CHECK (name <> ''::text), CONSTRAINT "concurrency_slots_slot_check" CHECK (slot >= 0));
