-- Create index "integrations_auth_domain_idx" to table: "integrations"
CREATE INDEX "integrations_auth_domain_idx" ON "integrations" ("provider", (lower((settings ->> 'domain'::text)))) WHERE (category = 'auth'::text);
-- Modify "integrations" table
ALTER TABLE "integrations" DROP CONSTRAINT "integrations_provider_check", ADD CONSTRAINT "integrations_provider_check" CHECK (provider = ANY (ARRAY['stripe'::text, 'adyen'::text, 'gocardless'::text, 'cashfree'::text, 'moneyhash'::text, 'flutterwave'::text, 'netsuite'::text, 'xero'::text, 'anrok'::text, 'avalara'::text, 'hubspot'::text, 'salesforce'::text, 'okta'::text, 'entra_id'::text, 'oidc'::text]));
-- Modify "invites" table: an invite carries the roles of the membership it
-- creates; existing invites keep their single role.
ALTER TABLE "invites" ADD COLUMN "role_ids" uuid[] NULL;
UPDATE "invites" SET "role_ids" = ARRAY["role_id"];
ALTER TABLE "invites" ALTER COLUMN "role_ids" SET NOT NULL, ADD CONSTRAINT "invites_role_ids_check" CHECK (cardinality(role_ids) > 0), DROP COLUMN "role_id";
-- Modify "roles" table
ALTER TABLE "roles" DROP CONSTRAINT "roles_code_key", ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "roles_code_key" to table: "roles"
CREATE UNIQUE INDEX "roles_code_key" ON "roles" ("organization_id", "code") WHERE (deleted_at IS NULL);
-- Create "oidc_states" table
CREATE TABLE "oidc_states" ("key_hash" bytea NOT NULL, "method" text NOT NULL, "intent" text NOT NULL, "connection_id" text NOT NULL, "email" text NOT NULL DEFAULT '', "invite_token_hash" bytea NULL, "organization_name" text NOT NULL DEFAULT '', "nonce" text NOT NULL, "verifier" text NOT NULL, "expires_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, PRIMARY KEY ("key_hash"), CONSTRAINT "oidc_states_intent_check" CHECK (intent = ANY (ARRAY['login'::text, 'sign_up'::text, 'accept_invite'::text])), CONSTRAINT "oidc_states_method_check" CHECK (method = ANY (ARRAY['google_oauth'::text, 'okta'::text, 'entra_id'::text, 'oidc'::text])));
-- Create index "oidc_states_expires_at_idx" to table: "oidc_states"
CREATE INDEX "oidc_states_expires_at_idx" ON "oidc_states" ("expires_at");
-- Modify "organizations" table
ALTER TABLE "organizations" ADD COLUMN "from_email_enabled" boolean NOT NULL DEFAULT false;
-- Create "api_key_uses" table
CREATE TABLE "api_key_uses" ("organization_id" uuid NOT NULL, "api_key_id" uuid NOT NULL, "last_used_at" timestamptz NOT NULL, PRIMARY KEY ("organization_id", "api_key_id"), CONSTRAINT "api_key_uses_api_key_fkey" FOREIGN KEY ("organization_id", "api_key_id") REFERENCES "api_keys" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "api_key_uses_organization_fkey" FOREIGN KEY ("organization_id") REFERENCES "organizations" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION);
-- Create "membership_roles" table
CREATE TABLE "membership_roles" ("organization_id" uuid NOT NULL, "id" bigint NOT NULL GENERATED ALWAYS AS IDENTITY, "membership_id" uuid NOT NULL, "role_id" uuid NOT NULL, "created_at" timestamptz NOT NULL, "deleted_at" timestamptz NULL, PRIMARY KEY ("organization_id", "id"), CONSTRAINT "membership_roles_membership_fkey" FOREIGN KEY ("organization_id", "membership_id") REFERENCES "memberships" ("organization_id", "id") ON UPDATE NO ACTION ON DELETE CASCADE);
-- Create index "membership_roles_membership_idx" to table: "membership_roles"
CREATE INDEX "membership_roles_membership_idx" ON "membership_roles" ("organization_id", "membership_id") WHERE (deleted_at IS NULL);
-- Create index "membership_roles_membership_role_key" to table: "membership_roles"
CREATE UNIQUE INDEX "membership_roles_membership_role_key" ON "membership_roles" ("organization_id", "membership_id", "role_id") WHERE (deleted_at IS NULL);
-- Create index "membership_roles_role_idx" to table: "membership_roles"
CREATE INDEX "membership_roles_role_idx" ON "membership_roles" ("organization_id", "role_id") WHERE (deleted_at IS NULL);
-- Copy the role of every membership into "membership_roles"
INSERT INTO "membership_roles" ("organization_id", "membership_id", "role_id", "created_at")
SELECT "organization_id", "id", "role_id", "created_at" FROM "memberships";
-- Drop index "memberships_role_idx" from table: "memberships"
DROP INDEX "memberships_role_idx";
-- Modify "memberships" table
ALTER TABLE "memberships" DROP COLUMN "role_id";
-- Create "sessions" table
CREATE TABLE "sessions" ("id" uuid NOT NULL, "user_id" uuid NOT NULL, "token_hash" bytea NOT NULL, "method" text NOT NULL, "user_agent" text NOT NULL DEFAULT '', "ip" text NOT NULL DEFAULT '', "created_at" timestamptz NOT NULL, "last_seen_at" timestamptz NOT NULL, "expires_at" timestamptz NOT NULL, "revoked_at" timestamptz NULL, PRIMARY KEY ("id"), CONSTRAINT "sessions_user_fkey" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "sessions_method_check" CHECK (method = ANY (ARRAY['email_password'::text, 'google_oauth'::text, 'okta'::text, 'entra_id'::text, 'oidc'::text])));
-- Create index "sessions_expires_at_idx" to table: "sessions"
CREATE INDEX "sessions_expires_at_idx" ON "sessions" ("expires_at");
-- Create index "sessions_token_hash_key" to table: "sessions"
CREATE UNIQUE INDEX "sessions_token_hash_key" ON "sessions" ("token_hash");
-- Create index "sessions_user_idx" to table: "sessions"
CREATE INDEX "sessions_user_idx" ON "sessions" ("user_id") WHERE (revoked_at IS NULL);
-- Create "user_devices" table
CREATE TABLE "user_devices" ("id" uuid NOT NULL, "user_id" uuid NOT NULL, "fingerprint" text NOT NULL, "browser" text NULL, "os" text NULL, "device_type" text NULL, "last_ip_address" text NULL, "last_logged_at" timestamptz NOT NULL, "created_at" timestamptz NOT NULL, "updated_at" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "user_devices_user_fkey" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ON UPDATE NO ACTION ON DELETE CASCADE, CONSTRAINT "user_devices_fingerprint_check" CHECK (fingerprint <> ''::text));
-- Create index "user_devices_fingerprint_key" to table: "user_devices"
CREATE UNIQUE INDEX "user_devices_fingerprint_key" ON "user_devices" ("user_id", "fingerprint");
-- Enable row-level security on table "api_key_uses"
ALTER TABLE api_key_uses ENABLE ROW LEVEL SECURITY;
-- Enable row-level security on table "membership_roles"
ALTER TABLE membership_roles ENABLE ROW LEVEL SECURITY;
-- Create policy "tenant_isolation" on table "api_key_uses"
CREATE POLICY tenant_isolation ON api_key_uses AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
-- Create policy "tenant_isolation" on table "membership_roles"
CREATE POLICY tenant_isolation ON membership_roles AS PERMISSIVE FOR ALL TO public USING (((organization_id = (NULLIF(NULLIF(current_setting('tanvo.organization_id'::text, true), ''::text), '*'::text))::uuid) OR (current_setting('tanvo.organization_id'::text, true) = '*'::text)));
