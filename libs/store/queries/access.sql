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
  organization_id, id, user_id, role_id, status, revoked_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @user_id, @role_id, @status, @revoked_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateMembership :execrows
UPDATE memberships SET
  organization_id = @organization_id, id = @id, user_id = @user_id, role_id = @role_id,
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
WHERE organization_id = @organization_id
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(role_id)::uuid IS NULL OR role_id = sqlc.narg(role_id))
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: CountActiveMembershipsByRole :one
SELECT count(*) FROM memberships
WHERE organization_id = @organization_id AND role_id = @role_id AND status = 'active';

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
WHERE organization_id = @organization_id AND id = @id;

-- name: DeleteRole :execrows
DELETE FROM roles WHERE organization_id = @organization_id AND id = @id;

-- name: GetRole :one
SELECT * FROM roles WHERE organization_id = @organization_id AND id = @id;

-- name: GetRoleByCode :one
SELECT * FROM roles WHERE organization_id = @organization_id AND code = @code;

-- name: ListRoles :many
SELECT * FROM roles
WHERE organization_id = @organization_id
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: CreateInvite :execrows
INSERT INTO invites (
  organization_id, id, email, role_id, token_hash, status, expires_at, accepted_at, revoked_at, created_at, updated_at
) VALUES (
  @organization_id, @id, @email, @role_id, @token_hash, @status, @expires_at, @accepted_at, @revoked_at, @created_at, @updated_at
)
ON CONFLICT DO NOTHING;

-- name: UpdateInvite :execrows
UPDATE invites SET
  organization_id = @organization_id, id = @id, email = @email, role_id = @role_id, token_hash = @token_hash,
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
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
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
  AND (sqlc.narg(after)::uuid IS NULL OR id > sqlc.narg(after))
ORDER BY id
LIMIT @lim;

-- name: CountNonExpiringAPIKeys :one
SELECT count(*) FROM api_keys WHERE organization_id = @organization_id AND expires_at IS NULL;

-- name: TouchAPIKeyLastUsed :execrows
UPDATE api_keys SET last_used_at = @last_used_at
WHERE organization_id = @organization_id AND id = @id;
