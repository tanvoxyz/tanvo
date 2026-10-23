-- Access: users, memberships, roles, invites, password resets, API keys (domain §5).
--
-- Users and password resets are global (a user is not owned by an
-- organization), so they have no organization_id and no row-level security.
-- Built-in roles (admin, finance, manager) are not stored: they have fixed
-- IDs and are served by the repository, so role_id columns have no foreign
-- key (they hold a built-in ID or a custom role's ID).

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
  role_id uuid NOT NULL,
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
CREATE INDEX memberships_role_idx ON memberships (organization_id, role_id);

ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON memberships
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

-- Custom roles; codes never collide with the built-in codes (checked by the
-- repository and by roles_code_check).
CREATE TABLE roles (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  code text NOT NULL,
  name text NOT NULL,
  description text,
  permissions text[] NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT roles_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT roles_code_key UNIQUE (organization_id, code),
  CONSTRAINT roles_code_check CHECK (code NOT IN ('admin', 'finance', 'manager'))
);

ALTER TABLE roles ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON roles
  USING (organization_id = NULLIF(NULLIF(current_setting('tanvo.organization_id', true), ''), '*')::uuid OR current_setting('tanvo.organization_id', true) = '*');

CREATE TABLE invites (
  organization_id uuid NOT NULL,
  id uuid NOT NULL,
  email text NOT NULL,
  role_id uuid NOT NULL,
  token_hash bytea NOT NULL,
  status text NOT NULL,
  expires_at timestamptz NOT NULL,
  accepted_at timestamptz,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (organization_id, id),
  CONSTRAINT invites_organization_fkey FOREIGN KEY (organization_id) REFERENCES organizations (id),
  CONSTRAINT invites_status_check CHECK (status IN ('pending', 'accepted', 'revoked'))
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
