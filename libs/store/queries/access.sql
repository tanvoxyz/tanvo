-- Users, memberships, roles, invites, password resets and API keys.
-- Users and password resets are global; the cross-organization lookups
-- (ListActiveMembershipsByUser, GetInviteByTokenHash, GetAPIKeyByTokenHash)
-- run with the row-level security scope opened to every organization.

-- name: CreateUser :execrows
INSERT INTO users (
  id, email, password_hash, name, last_sign_in_at, created_at, updated_at
) VALUES (
  @id, @email, @password_hash, @name, @last_sign_in_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateUser :execrows
UPDATE users SET
  id = @id, email = @email, password_hash = @password_hash, name = @name,
  last_sign_in_at = @last_sign_in_at, created_at = @created_at, updated_at = @updated_at
WHERE id = @id;

-- name: GetUser :one
SELECT * FROM users WHERE id = @id;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(@email::text);

-- name: CreateMembership :execrows
INSERT INTO memberships (
  organization_id, id, user_id, status, revoked_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @user_id, @status, @revoked_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateMembership :execrows
UPDATE memberships SET
  organization_id = @organization_id, id = @id, user_id = @user_id,
  status = @status, revoked_at = @revoked_at, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetMembership :one
SELECT * FROM memberships WHERE organization_id = @organization_id AND id = @id;

-- name: GetActiveMembershipByUser :one
SELECT * FROM memberships
WHERE organization_id = @organization_id AND user_id = @user_id AND status = 'active';

-- ListActiveMembershipsByUser is cross-organization (sign-in).
-- name: ListActiveMembershipsByUser :many
SELECT * FROM memberships
WHERE user_id = @user_id AND status = 'active'
ORDER BY id;

-- name: ListMemberships :many
SELECT * FROM memberships
WHERE memberships.organization_id = @organization_id
  AND (sqlc.narg(status)::text IS NULL OR memberships.status = sqlc.narg(status))
  AND (sqlc.narg(role_id)::uuid IS NULL OR EXISTS (
    SELECT 1 FROM membership_roles mr
    WHERE mr.organization_id = memberships.organization_id AND mr.membership_id = memberships.id
      AND mr.role_id = sqlc.narg(role_id) AND mr.deleted_at IS NULL
  ))
  AND (cardinality(@role_ids::uuid[]) = 0 OR EXISTS (
    SELECT 1 FROM membership_roles mr
    WHERE mr.organization_id = memberships.organization_id AND mr.membership_id = memberships.id
      AND mr.role_id = ANY(@role_ids::uuid[]) AND mr.deleted_at IS NULL
      AND NOT EXISTS (
        SELECT 1 FROM roles r
        WHERE r.organization_id = mr.organization_id AND r.id = mr.role_id AND r.deleted_at IS NOT NULL
      )
  ))
  AND (sqlc.narg(search)::text IS NULL OR EXISTS (
    SELECT 1 FROM users u
    WHERE u.id = memberships.user_id AND strpos(lower(u.email), lower(sqlc.narg(search))) > 0
  ))
  -- @recent lists the newest first, then by id (memberships_query.rb,
  -- base_query.rb:111-112); its cursor is (@after_at, @after).
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (memberships.created_at < sqlc.narg(after_at)::timestamptz
                                 OR (memberships.created_at = sqlc.narg(after_at)::timestamptz AND memberships.id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND memberships.id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN memberships.created_at END DESC, memberships.id
LIMIT @lim;

-- name: CountActiveMembershipsByRole :one
SELECT count(*) FROM memberships m
JOIN membership_roles mr ON mr.organization_id = m.organization_id AND mr.membership_id = m.id
WHERE m.organization_id = @organization_id AND mr.role_id = @role_id AND mr.deleted_at IS NULL AND m.status = 'active';

-- AddMembershipRoles adds the roles a membership does not have yet; kept
-- rows are left untouched.
-- name: AddMembershipRoles :exec
INSERT INTO membership_roles (organization_id, membership_id, role_id, created_at)
SELECT @organization_id, @membership_id, r, @created_at FROM unnest(@role_ids::uuid[]) AS r
ON CONFLICT DO NOTHING;

-- DiscardMembershipRoles soft-deletes the kept roles of a membership not in
-- role_ids. Rows of deleted roles stay kept, as they are not read (lago
-- default scopes on roles and membership_roles).
-- name: DiscardMembershipRoles :exec
UPDATE membership_roles SET deleted_at = @deleted_at
WHERE membership_roles.organization_id = @organization_id AND membership_roles.membership_id = @membership_id
  AND membership_roles.deleted_at IS NULL
  AND NOT (membership_roles.role_id = ANY(@role_ids::uuid[]))
  AND NOT EXISTS (
    SELECT 1 FROM roles r
    WHERE r.organization_id = membership_roles.organization_id AND r.id = membership_roles.role_id AND r.deleted_at IS NOT NULL
  );

-- ListMembershipRoles returns the kept roles, not deleted, of memberships
-- of the given organizations (ListActiveMembershipsByUser spans
-- organizations).
-- name: ListMembershipRoles :many
SELECT membership_roles.organization_id, membership_roles.membership_id, membership_roles.role_id FROM membership_roles
WHERE membership_roles.organization_id = ANY(@organization_ids::uuid[]) AND membership_roles.membership_id = ANY(@membership_ids::uuid[])
  AND membership_roles.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM roles r
    WHERE r.organization_id = membership_roles.organization_id AND r.id = membership_roles.role_id AND r.deleted_at IS NOT NULL
  )
ORDER BY membership_roles.organization_id, membership_roles.membership_id, membership_roles.role_id;

-- name: CreateRole :execrows
INSERT INTO roles (
  organization_id, id, code, name, description, permissions, created_at, updated_at
) VALUES (
  @organization_id, @id, @code, @name, @description, @permissions, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateRole :execrows
UPDATE roles SET
  organization_id = @organization_id, id = @id, code = @code, name = @name, description = @description,
  permissions = @permissions, created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: DiscardRole :execrows
UPDATE roles SET deleted_at = @deleted_at, updated_at = @deleted_at
WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetRole :one
SELECT * FROM roles WHERE organization_id = @organization_id AND id = @id AND deleted_at IS NULL;

-- name: GetRoleByCode :one
SELECT * FROM roles WHERE organization_id = @organization_id AND code = @code AND deleted_at IS NULL;

-- name: ListRoles :many
SELECT * FROM roles
WHERE organization_id = @organization_id AND deleted_at IS NULL
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: CreateInvite :execrows
INSERT INTO invites (
  organization_id, id, email, role_ids, token_hash, status, expires_at, accepted_at, revoked_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @email, @role_ids, @token_hash, @status, @expires_at, @accepted_at, @revoked_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateInvite :execrows
UPDATE invites SET
  organization_id = @organization_id, id = @id, email = @email, role_ids = @role_ids, token_hash = @token_hash,
  status = @status, expires_at = @expires_at, accepted_at = @accepted_at, revoked_at = @revoked_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: GetInvite :one
SELECT * FROM invites WHERE organization_id = @organization_id AND id = @id;

-- GetInviteByTokenHash is cross-organization (the invitee is not a member).
-- name: GetInviteByTokenHash :one
SELECT * FROM invites WHERE token_hash = @token_hash;

-- name: GetPendingInviteByEmail :one
SELECT * FROM invites
WHERE organization_id = @organization_id AND email = @email AND status = 'pending';

-- name: ListInvites :many
SELECT * FROM invites
WHERE organization_id = @organization_id
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (cardinality(@role_ids::uuid[]) = 0 OR role_ids && @role_ids::uuid[])
  AND (sqlc.narg(search)::text IS NULL OR strpos(lower(email), lower(sqlc.narg(search))) > 0)
  -- @recent lists the newest first, then by id (invites_query.rb,
  -- base_query.rb:111-112); its cursor is (@after_at, @after).
  AND (sqlc.narg(after)::uuid IS NULL
       OR (@recent::boolean AND (created_at < sqlc.narg(after_at)::timestamptz
                                 OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after))))
       OR (NOT @recent::boolean AND id > sqlc.narg(after)))
ORDER BY CASE WHEN @recent::boolean THEN created_at END DESC, id
LIMIT @lim;

-- name: CreatePasswordReset :execrows
INSERT INTO password_resets (
  id, user_id, token_hash, expires_at, used_at, created_at
) VALUES (
  @id, @user_id, @token_hash, @expires_at, @used_at, @created_at
)
ON CONFLICT DO NOTHING;

-- name: UpdatePasswordReset :execrows
UPDATE password_resets SET
  id = @id, user_id = @user_id, token_hash = @token_hash, expires_at = @expires_at,
  used_at = @used_at, created_at = @created_at
WHERE id = @id;

-- name: GetPasswordResetByTokenHash :one
SELECT * FROM password_resets WHERE token_hash = @token_hash;

-- name: DeleteExpiredPasswordResets :execrows
DELETE FROM password_resets WHERE expires_at < @before;

-- name: CreateAPIKey :execrows
INSERT INTO api_keys (
  organization_id, id, name, token_hash, prefix, permissions, expires_at, last_used_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @name, @token_hash, @prefix, @permissions, @expires_at, @last_used_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateAPIKey :execrows
UPDATE api_keys SET
  organization_id = @organization_id, id = @id, name = @name, token_hash = @token_hash, prefix = @prefix,
  permissions = @permissions, expires_at = @expires_at, last_used_at = @last_used_at,
  created_at = @created_at, updated_at = @updated_at
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteAPIKey :execrows
DELETE FROM api_keys WHERE organization_id = @organization_id AND id = @id;

-- name: GetAPIKey :one
SELECT * FROM api_keys WHERE organization_id = @organization_id AND id = @id;

-- GetAPIKeyByTokenHash is cross-organization (authentication).
-- name: GetAPIKeyByTokenHash :one
SELECT * FROM api_keys WHERE token_hash = @token_hash;

-- name: ListAPIKeys :many
SELECT * FROM api_keys
WHERE organization_id = @organization_id
  AND (sqlc.narg(active_at)::timestamptz IS NULL OR expires_at IS NULL OR expires_at > sqlc.narg(active_at))
  -- Oldest first, then by id (api_keys_resolver.rb:18); the cursor is
  -- (@after_at, @after).
  AND (sqlc.narg(after)::uuid IS NULL
       OR created_at > sqlc.narg(after_at)::timestamptz
       OR (created_at = sqlc.narg(after_at)::timestamptz AND id > sqlc.narg(after)))
ORDER BY created_at, id
LIMIT @lim;

-- name: CountNonExpiringAPIKeys :one
SELECT count(*) FROM api_keys WHERE organization_id = @organization_id AND expires_at IS NULL;

-- name: TouchAPIKeyLastUsed :execrows
UPDATE api_keys SET last_used_at = @last_used_at
WHERE organization_id = @organization_id AND id = @id;

-- name: RecordAPIKeyUses :execrows
-- Records the last uses of an organization's keys, keeping the latest per
-- key; the uses of keys deleted meanwhile are dropped.
INSERT INTO api_key_uses (organization_id, api_key_id, last_used_at)
SELECT k.organization_id, k.id, u.last_used_at
FROM (SELECT unnest(@ids::uuid[]) AS id, unnest(@last_used_ats::timestamptz[]) AS last_used_at) u
JOIN api_keys k ON k.organization_id = @organization_id AND k.id = u.id
ON CONFLICT (organization_id, api_key_id) DO UPDATE
SET last_used_at = GREATEST(api_key_uses.last_used_at, EXCLUDED.last_used_at);

-- name: ApplyAPIKeyUses :execrows
-- Moves the recorded uses of an organization's keys to their last_used_at.
WITH used AS (
  DELETE FROM api_key_uses u WHERE u.organization_id = @organization_id
  RETURNING u.api_key_id, u.last_used_at
)
UPDATE api_keys k SET last_used_at = GREATEST(k.last_used_at, used.last_used_at)
FROM used
WHERE k.organization_id = @organization_id AND k.id = used.api_key_id;

-- name: ListAPIKeyUseOrganizations :many
-- Across organizations: the organizations with recorded uses, for the
-- usage tracking clock job.
SELECT DISTINCT organization_id FROM api_key_uses ORDER BY organization_id;

-- Sessions, OIDC states and user devices are global, like users.

-- name: CreateSession :execrows
INSERT INTO sessions (
  id, user_id, token_hash, method, user_agent, ip, created_at, last_seen_at, expires_at, revoked_at
) VALUES (
  @id, @user_id, @token_hash, @method, @user_agent, @ip, @created_at, @last_seen_at, @expires_at, @revoked_at
)
ON CONFLICT DO NOTHING;

-- name: GetSessionByTokenHash :one
SELECT * FROM sessions WHERE token_hash = @token_hash;

-- name: TouchSession :execrows
UPDATE sessions SET last_seen_at = @last_seen_at WHERE id = @id;

-- name: RevokeSession :execrows
UPDATE sessions SET revoked_at = coalesce(revoked_at, @revoked_at::timestamptz) WHERE id = @id;

-- name: RevokeUserSessions :execrows
UPDATE sessions SET revoked_at = @revoked_at
WHERE user_id = @user_id AND revoked_at IS NULL AND id <> @keep;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < @before OR revoked_at < @before;

-- name: CreateOIDCState :execrows
INSERT INTO oidc_states (
  key_hash, method, intent, connection_id, email, invite_token_hash, organization_name,
  nonce, verifier, expires_at, created_at
) VALUES (
  @key_hash, @method, @intent, @connection_id, @email, @invite_token_hash, @organization_name,
  @nonce, @verifier, @expires_at, @created_at
)
ON CONFLICT DO NOTHING;

-- TakeOIDCState deletes the state and returns it; the repository refuses an
-- expired one.
-- name: TakeOIDCState :one
DELETE FROM oidc_states WHERE key_hash = @key_hash RETURNING *;

-- name: DeleteExpiredOIDCStates :execrows
DELETE FROM oidc_states WHERE expires_at < @before;

-- name: CreateUserDevice :execrows
INSERT INTO user_devices (
  id, user_id, fingerprint, browser, os, device_type, last_ip_address, last_logged_at, created_at, updated_at
) VALUES (
  @id, @user_id, @fingerprint, @browser, @os, @device_type, @last_ip_address, @last_logged_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateUserDevice :execrows
UPDATE user_devices SET
  browser = @browser, os = @os, device_type = @device_type,
  last_ip_address = @last_ip_address, last_logged_at = @last_logged_at, updated_at = @updated_at
WHERE id = @id AND user_id = @user_id;

-- name: GetUserDeviceByFingerprint :one
SELECT * FROM user_devices WHERE user_id = @user_id AND fingerprint = @fingerprint;

-- name: ListUserDevices :many
SELECT * FROM user_devices WHERE user_id = @user_id ORDER BY id;
