-- Access: users, memberships, roles, invites, password resets, API keys (domain §5).
--
-- Users and password resets are global (a user is not owned by an
-- organization), so they have no organization_id and no row-level security.
-- Built-in roles (admin, finance, manager) are not stored: they have fixed
-- IDs and are served by the repository, so role_id columns have no foreign
-- key (they hold a built-in ID or a custom role's ID).
-- A membership has one or more roles (membership_roles) and holds the union
-- of their permissions; an invite carries the roles of the membership it
-- creates (role_ids).

CREATE TABLE users (
  id uuid NOT NULL,
  email text NOT NULL,
  password_hash bytea,
  name text,
  last_sign_in_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT users_email_check CHECK (email <> '')
);

-- Emails are unique case-insensitively (domain §5.1).
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TABLE memberships (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  user_id uuid NOT NULL,
  status text NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT memberships_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT memberships_user_fkey FOREIGN KEY (user_id) REFERENCES users (id),
  CONSTRAINT memberships_status_check CHECK (status IN ('active', 'revoked'))
);

-- A user has at most one active membership per organization.
CREATE UNIQUE INDEX memberships_user_active_key ON memberships (organization_id, user_id) WHERE status = 'active';
-- Sign-in lists a user's active memberships across organizations.
CREATE INDEX memberships_user_idx ON memberships (user_id) WHERE status = 'active';

ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON memberships
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The roles of a membership (lago membership_roles). Rows are never
-- modified: removing a role from a membership soft-deletes its row
-- (deleted_at), and every read keeps only the rows not deleted. The
-- repository keeps at least one kept row per active membership.
CREATE TABLE membership_roles (
  organization_id uuid NOT NULL,
  id bigint GENERATED ALWAYS AS IDENTITY,
  membership_id uuid NOT NULL,
  role_id uuid NOT NULL,
  created_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT membership_roles_membership_fkey FOREIGN KEY (organization_id, membership_id) REFERENCES memberships (organization_id, id) ON DELETE CASCADE
);

-- A role is kept at most once per membership.
CREATE UNIQUE INDEX membership_roles_membership_role_key ON membership_roles (organization_id, membership_id, role_id) WHERE deleted_at IS NULL;
-- Loading the kept roles of memberships.
CREATE INDEX membership_roles_membership_idx ON membership_roles (organization_id, membership_id) WHERE deleted_at IS NULL;
-- Counting a role's active members (last admin, role deletion) and
-- filtering memberships by role.
CREATE INDEX membership_roles_role_idx ON membership_roles (organization_id, role_id) WHERE deleted_at IS NULL;

ALTER TABLE membership_roles ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON membership_roles
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Custom roles; codes never collide with the built-in codes (checked by the
-- repository and by roles_code_check). Deleting a role soft-deletes it
-- (deleted_at, lago roles.deleted_at): a deleted role is never read again,
-- grants nothing, and its code can be reused.
CREATE TABLE roles (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  description text,
  permissions text[] NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  deleted_at timestamptz,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT roles_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT roles_code_check CHECK (code NOT IN ('admin', 'finance', 'manager'))
);

-- Codes are unique among the organization's kept roles.
CREATE UNIQUE INDEX roles_code_key ON roles (organization_id, code) WHERE deleted_at IS NULL;

ALTER TABLE roles ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON roles
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE invites (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  email text NOT NULL,
  role_ids uuid[] NOT NULL,
  token_hash bytea NOT NULL,
  status text NOT NULL,
  expires_at timestamptz NOT NULL,
  accepted_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT invites_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT invites_status_check CHECK (status IN ('pending', 'accepted', 'revoked')),
  CONSTRAINT invites_role_ids_check CHECK (cardinality(role_ids) > 0)
);

-- Tokens are unique across organizations (an invite is found by its token
-- before the invitee is a member); one pending invite per email.
CREATE UNIQUE INDEX invites_token_hash_key ON invites (token_hash);
CREATE UNIQUE INDEX invites_email_pending_key ON invites (organization_id, email) WHERE status = 'pending';

ALTER TABLE invites ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON invites
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE password_resets (
  id uuid NOT NULL,
  user_id uuid NOT NULL,
  token_hash bytea NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT password_resets_user_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX password_resets_token_hash_key ON password_resets (token_hash);
CREATE INDEX password_resets_user_idx ON password_resets (user_id);
CREATE INDEX password_resets_expires_at_idx ON password_resets (expires_at);

-- permissions is a jsonb object mapping API resources to levels; an empty
-- object is full access.
CREATE TABLE api_keys (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  name text,
  token_hash bytea NOT NULL,
  prefix text NOT NULL,
  permissions jsonb NOT NULL DEFAULT '{}',
  expires_at timestamptz,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT api_keys_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT api_keys_prefix_check CHECK (prefix <> ''),
  CONSTRAINT api_keys_permissions_check CHECK (jsonb_typeof(permissions) = 'object')
);

-- Authentication finds a key by its token hash in any organization.
CREATE UNIQUE INDEX api_keys_token_hash_key ON api_keys (token_hash);

ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_keys
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- The last uses of API keys that the API processes recorded and the hourly
-- clock job has not applied to api_keys.last_used_at yet (lago-api's
-- api_key_last_used_<id> cache entries, read by
-- Clock::ApiKeys::TrackUsageJob). One row per key, keeping the latest use;
-- applying the uses deletes them.
CREATE TABLE api_key_uses (
  organization_id uuid NOT NULL,
  api_key_id uuid NOT NULL,
  last_used_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, api_key_id),
  CONSTRAINT api_key_uses_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT api_key_uses_api_key_fkey FOREIGN KEY (organization_id, api_key_id) REFERENCES api_keys (organization_id, id) ON DELETE CASCADE
);

ALTER TABLE api_key_uses ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON api_key_uses
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Sessions (domain §5.7) belong to users and are found by their token hash
-- before any organization is known, so they have no organization_id and no
-- row-level security, like users.
CREATE TABLE sessions (
  id uuid NOT NULL,
  user_id uuid NOT NULL,
  token_hash bytea NOT NULL,
  method text NOT NULL,
  user_agent text NOT NULL DEFAULT '',
  ip text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  PRIMARY KEY (id),
  CONSTRAINT sessions_user_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT sessions_method_check CHECK (method IN ('email_password', 'google_oauth', 'okta', 'entra_id', 'oidc'))
);

CREATE UNIQUE INDEX sessions_token_hash_key ON sessions (token_hash);
-- Revoking a user's sessions scans the unrevoked ones.
CREATE INDEX sessions_user_idx ON sessions (user_id) WHERE revoked_at IS NULL;
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- OpenID Connect sign-ins in progress (domain §5.7), keyed by the SHA-256 of
-- the state parameter and deleted when taken. They precede any user or
-- organization, so they have no row-level security. The invite token is kept
-- hashed, like in invites.
CREATE TABLE oidc_states (
  key_hash bytea NOT NULL,
  method text NOT NULL,
  intent text NOT NULL,
  connection_id text NOT NULL,
  email text NOT NULL DEFAULT '',
  invite_token_hash bytea,
  organization_name text NOT NULL DEFAULT '',
  nonce text NOT NULL,
  verifier text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (key_hash),
  CONSTRAINT oidc_states_method_check CHECK (method IN ('google_oauth', 'okta', 'entra_id', 'oidc')),
  CONSTRAINT oidc_states_intent_check CHECK (intent IN ('login', 'sign_up', 'accept_invite'))
);

CREATE INDEX oidc_states_expires_at_idx ON oidc_states (expires_at);

-- Known devices of users (domain §5.8), one per user agent fingerprint.
CREATE TABLE user_devices (
  id uuid NOT NULL,
  user_id uuid NOT NULL,
  fingerprint text NOT NULL,
  browser text,
  os text,
  device_type text,
  last_ip_address text,
  last_logged_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT user_devices_user_fkey FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
  CONSTRAINT user_devices_fingerprint_check CHECK (fingerprint <> '')
);

CREATE UNIQUE INDEX user_devices_fingerprint_key ON user_devices (user_id, fingerprint);
