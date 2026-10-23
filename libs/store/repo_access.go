package store

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindAccess binds the repositories of the access aggregates.
func (t *txState) bindAccess(r *port.Repos) {
	r.Users = userRepo{t}
	r.Memberships = membershipRepo{t}
	r.Roles = roleRepo{t}
	r.Invites = inviteRepo{t}
	r.PasswordResets = passwordResetRepo{t}
	r.APIKeys = apiKeyRepo{t}
}

// userRepo implements access.UserRepository. Users are global: every
// method runs with queriesAll (the users table has no row-level security).
type userRepo struct{ t *txState }

var _ access.UserRepository = userRepo{}

func userParams(u *access.User) db.CreateUserParams {
	return db.CreateUserParams{
		ID:           u.ID.UUID(),
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		Name:         u.Name,
		LastSignInAt: optUTC(u.LastSignInAt),
		CreatedAt:    utc(u.CreatedAt),
		UpdatedAt:    utc(u.UpdatedAt),
	}
}

func userOf(r db.User) (*access.User, error) {
	return &access.User{
		ID:           idOf[core.UserID](r.ID),
		Email:        r.Email,
		PasswordHash: r.PasswordHash,
		Name:         r.Name,
		LastSignInAt: optUTC(r.LastSignInAt),
		CreatedAt:    utc(r.CreatedAt),
		UpdatedAt:    utc(r.UpdatedAt),
	}, nil
}

func (r userRepo) Create(ctx context.Context, u *access.User) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	n, err := q.CreateUser(ctx, userParams(u))
	return insertErr("user", n, err)
}

func (r userRepo) Update(ctx context.Context, u *access.User) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	// The email is unique.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateUser(ctx, db.UpdateUserParams(userParams(u)))
		return err
	})
	return updateErr("user", u.ID, n, err)
}

func (r userRepo) Get(ctx context.Context, id core.UserID) (*access.User, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	row, err := q.GetUser(ctx, id.UUID())
	if err != nil {
		return nil, getErr("user", id, err)
	}
	return userOf(row)
}

func (r userRepo) GetByEmail(ctx context.Context, email string) (*access.User, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	email = access.NormalizeEmail(email)
	row, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, getErr("user", email, err)
	}
	return userOf(row)
}

// membershipRepo implements access.MembershipRepository.
type membershipRepo struct{ t *txState }

var _ access.MembershipRepository = membershipRepo{}

func membershipParams(m *access.Membership) db.CreateMembershipParams {
	return db.CreateMembershipParams{
		OrganizationID: m.OrganizationID.UUID(),
		ID:             m.ID.UUID(),
		UserID:         m.UserID.UUID(),
		RoleID:         m.RoleID.UUID(),
		Status:         string(m.Status),
		RevokedAt:      optUTC(m.RevokedAt),
		CreatedAt:      utc(m.CreatedAt),
		UpdatedAt:      utc(m.UpdatedAt),
	}
}

func membershipOf(r db.Membership) (*access.Membership, error) {
	return &access.Membership{
		ID:             idOf[core.MembershipID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		UserID:         idOf[core.UserID](r.UserID),
		RoleID:         idOf[core.RoleID](r.RoleID),
		Status:         access.MembershipStatus(r.Status),
		RevokedAt:      optUTC(r.RevokedAt),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r membershipRepo) Create(ctx context.Context, m *access.Membership) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateMembership(ctx, membershipParams(m))
	return insertErr("membership", n, err)
}

func (r membershipRepo) Update(ctx context.Context, m *access.Membership) error {
	q, err := r.t.queries(ctx, m.OrganizationID)
	if err != nil {
		return err
	}
	// One active membership per user and organization.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateMembership(ctx, db.UpdateMembershipParams(membershipParams(m)))
		return err
	})
	return updateErr("membership", m.ID, n, err)
}

func (r membershipRepo) Get(ctx context.Context, org core.OrganizationID, id core.MembershipID) (*access.Membership, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetMembership(ctx, db.GetMembershipParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("membership", id, err)
	}
	return membershipOf(row)
}

func (r membershipRepo) GetActiveByUser(ctx context.Context, org core.OrganizationID, user core.UserID) (*access.Membership, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetActiveMembershipByUser(ctx, db.GetActiveMembershipByUserParams{OrganizationID: org.UUID(), UserID: user.UUID()})
	if err != nil {
		return nil, getErr("membership", user, err)
	}
	return membershipOf(row)
}

// ListActiveByUser is not organization-scoped: it serves sign-in.
func (r membershipRepo) ListActiveByUser(ctx context.Context, user core.UserID) ([]*access.Membership, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListActiveMembershipsByUser(ctx, user.UUID())
	if err != nil {
		return nil, fmt.Errorf("list memberships of user %s: %w", user, mapErr(err))
	}
	return convAll(rows, membershipOf)
}

func (r membershipRepo) List(ctx context.Context, org core.OrganizationID, f access.MembershipFilter, page core.PageRequest) (core.Page[*access.Membership], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*access.Membership]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*access.Membership]{}, err
	}
	rows, err := q.ListMemberships(ctx, db.ListMembershipsParams{
		OrganizationID: org.UUID(),
		Status:         optString(string(f.Status)),
		RoleID:         optUUID(f.RoleID),
		After:          after,
		Lim:            lim,
	})
	if err != nil {
		return core.Page[*access.Membership]{}, fmt.Errorf("list memberships: %w", mapErr(err))
	}
	return pageOf(rows, page, membershipOf, func(m *access.Membership) string { return m.ID.String() })
}

func (r membershipRepo) CountActiveByRole(ctx context.Context, org core.OrganizationID, role core.RoleID) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.CountActiveMembershipsByRole(ctx, db.CountActiveMembershipsByRoleParams{OrganizationID: org.UUID(), RoleID: role.UUID()})
	if err != nil {
		return 0, fmt.Errorf("count memberships: %w", mapErr(err))
	}
	return int(n), nil
}

// roleRepo implements access.RoleRepository. Built-in roles are not
// stored: they come from access.BuiltInRoles.
type roleRepo struct{ t *txState }

var _ access.RoleRepository = roleRepo{}

// accessBuiltInRoleByCode returns the built-in role with that code.
func accessBuiltInRoleByCode(code string) (*access.Role, bool) {
	for _, b := range access.BuiltInRoles() {
		if b.Code == code {
			return b, true
		}
	}
	return nil, false
}

func roleParams(role *access.Role) db.CreateRoleParams {
	return db.CreateRoleParams{
		OrganizationID: role.OrganizationID.UUID(),
		ID:             role.ID.UUID(),
		Code:           role.Code,
		Name:           role.Name,
		Description:    role.Description,
		Permissions:    enumStrings(role.Permissions),
		CreatedAt:      utc(role.CreatedAt),
		UpdatedAt:      utc(role.UpdatedAt),
	}
}

func roleOf(r db.Role) (*access.Role, error) {
	return &access.Role{
		ID:             idOf[core.RoleID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Code:           r.Code,
		Name:           r.Name,
		Description:    r.Description,
		Permissions:    enumsOf[permission.Permission](r.Permissions),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r roleRepo) Create(ctx context.Context, role *access.Role) error {
	if _, builtIn := access.BuiltInRoleByID(role.ID); builtIn {
		return conflict("role", "id")
	}
	if _, builtIn := accessBuiltInRoleByCode(role.Code); builtIn {
		return conflict("role", "code")
	}
	q, err := r.t.queries(ctx, role.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateRole(ctx, roleParams(role))
	return insertErr("role", n, err)
}

func (r roleRepo) Update(ctx context.Context, role *access.Role) error {
	if _, builtIn := access.BuiltInRoleByID(role.ID); builtIn {
		return notFound("role", role.ID)
	}
	q, err := r.t.queries(ctx, role.OrganizationID)
	if err != nil {
		return err
	}
	if _, builtIn := accessBuiltInRoleByCode(role.Code); builtIn {
		return conflict("role", "code")
	}
	// Codes are unique per organization.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateRole(ctx, db.UpdateRoleParams(roleParams(role)))
		return err
	})
	return updateErr("role", role.ID, n, err)
}

func (r roleRepo) Delete(ctx context.Context, org core.OrganizationID, id core.RoleID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteRole(ctx, db.DeleteRoleParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("role", id, n, err)
}

func (r roleRepo) Get(ctx context.Context, org core.OrganizationID, id core.RoleID) (*access.Role, error) {
	if b, ok := access.BuiltInRoleByID(id); ok {
		return b, nil
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetRole(ctx, db.GetRoleParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("role", id, err)
	}
	return roleOf(row)
}

func (r roleRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*access.Role, error) {
	if b, ok := accessBuiltInRoleByCode(code); ok {
		return b, nil
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetRoleByCode(ctx, db.GetRoleByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("role", code, err)
	}
	return roleOf(row)
}

// List pages through the built-in roles, then the custom ones: the
// built-in IDs sort before every UUIDv7, so one keyset covers both.
func (r roleRepo) List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*access.Role], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*access.Role]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*access.Role]{}, err
	}
	roles := slices.DeleteFunc(access.BuiltInRoles(), func(b *access.Role) bool {
		u := b.ID.UUID()
		return after != nil && bytes.Compare(u[:], after[:]) <= 0
	})
	if rest := lim - int32(len(roles)); rest > 0 { // at most 3 built-in roles
		rows, err := q.ListRoles(ctx, db.ListRolesParams{OrganizationID: org.UUID(), After: after, Lim: rest})
		if err != nil {
			return core.Page[*access.Role]{}, fmt.Errorf("list roles: %w", mapErr(err))
		}
		custom, err := convAll(rows, roleOf)
		if err != nil {
			return core.Page[*access.Role]{}, err
		}
		roles = append(roles, custom...)
	}
	return pageOf(roles, page,
		func(x *access.Role) (*access.Role, error) { return x, nil },
		func(x *access.Role) string { return x.ID.String() })
}

// inviteRepo implements access.InviteRepository.
type inviteRepo struct{ t *txState }

var _ access.InviteRepository = inviteRepo{}

func inviteParams(i *access.Invite) db.CreateInviteParams {
	return db.CreateInviteParams{
		OrganizationID: i.OrganizationID.UUID(),
		ID:             i.ID.UUID(),
		Email:          i.Email,
		RoleID:         i.RoleID.UUID(),
		TokenHash:      i.TokenHash,
		Status:         string(i.Status),
		ExpiresAt:      utc(i.ExpiresAt),
		AcceptedAt:     optUTC(i.AcceptedAt),
		RevokedAt:      optUTC(i.RevokedAt),
		CreatedAt:      utc(i.CreatedAt),
		UpdatedAt:      utc(i.UpdatedAt),
	}
}

func inviteOf(r db.Invite) (*access.Invite, error) {
	return &access.Invite{
		ID:             idOf[core.InviteID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Email:          r.Email,
		RoleID:         idOf[core.RoleID](r.RoleID),
		TokenHash:      r.TokenHash,
		Status:         access.InviteStatus(r.Status),
		ExpiresAt:      utc(r.ExpiresAt),
		AcceptedAt:     optUTC(r.AcceptedAt),
		RevokedAt:      optUTC(r.RevokedAt),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r inviteRepo) Create(ctx context.Context, i *access.Invite) error {
	q, err := r.t.queries(ctx, i.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateInvite(ctx, inviteParams(i))
	return insertErr("invite", n, err)
}

func (r inviteRepo) Update(ctx context.Context, i *access.Invite) error {
	q, err := r.t.queries(ctx, i.OrganizationID)
	if err != nil {
		return err
	}
	// One pending invite per email, and unique tokens.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateInvite(ctx, db.UpdateInviteParams(inviteParams(i)))
		return err
	})
	return updateErr("invite", i.ID, n, err)
}

func (r inviteRepo) Get(ctx context.Context, org core.OrganizationID, id core.InviteID) (*access.Invite, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetInvite(ctx, db.GetInviteParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("invite", id, err)
	}
	return inviteOf(row)
}

// GetByTokenHash is not organization-scoped: the invitee is not a member.
func (r inviteRepo) GetByTokenHash(ctx context.Context, tokenHash []byte) (*access.Invite, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	row, err := q.GetInviteByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, getErr("invite", "token", err)
	}
	return inviteOf(row)
}

func (r inviteRepo) GetPendingByEmail(ctx context.Context, org core.OrganizationID, email string) (*access.Invite, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	email = access.NormalizeEmail(email)
	row, err := q.GetPendingInviteByEmail(ctx, db.GetPendingInviteByEmailParams{OrganizationID: org.UUID(), Email: email})
	if err != nil {
		return nil, getErr("invite", email, err)
	}
	return inviteOf(row)
}

func (r inviteRepo) List(ctx context.Context, org core.OrganizationID, f access.InviteFilter, page core.PageRequest) (core.Page[*access.Invite], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*access.Invite]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*access.Invite]{}, err
	}
	rows, err := q.ListInvites(ctx, db.ListInvitesParams{
		OrganizationID: org.UUID(), Status: optString(string(f.Status)), After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*access.Invite]{}, fmt.Errorf("list invites: %w", mapErr(err))
	}
	return pageOf(rows, page, inviteOf, func(i *access.Invite) string { return i.ID.String() })
}

// passwordResetRepo implements access.PasswordResetRepository. Resets are
// global: every method runs with queriesAll.
type passwordResetRepo struct{ t *txState }

var _ access.PasswordResetRepository = passwordResetRepo{}

func passwordResetParams(p *access.PasswordReset) db.CreatePasswordResetParams {
	return db.CreatePasswordResetParams{
		ID:        p.ID.UUID(),
		UserID:    p.UserID.UUID(),
		TokenHash: p.TokenHash,
		ExpiresAt: utc(p.ExpiresAt),
		UsedAt:    optUTC(p.UsedAt),
		CreatedAt: utc(p.CreatedAt),
	}
}

func passwordResetOf(r db.PasswordReset) (*access.PasswordReset, error) {
	return &access.PasswordReset{
		ID:        idOf[core.PasswordResetID](r.ID),
		UserID:    idOf[core.UserID](r.UserID),
		TokenHash: r.TokenHash,
		ExpiresAt: utc(r.ExpiresAt),
		UsedAt:    optUTC(r.UsedAt),
		CreatedAt: utc(r.CreatedAt),
	}, nil
}

func (r passwordResetRepo) Create(ctx context.Context, p *access.PasswordReset) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	n, err := q.CreatePasswordReset(ctx, passwordResetParams(p))
	return insertErr("password_reset", n, err)
}

func (r passwordResetRepo) Update(ctx context.Context, p *access.PasswordReset) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdatePasswordReset(ctx, db.UpdatePasswordResetParams(passwordResetParams(p)))
		return err
	})
	return updateErr("password_reset", p.ID, n, err)
}

func (r passwordResetRepo) GetByTokenHash(ctx context.Context, tokenHash []byte) (*access.PasswordReset, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	row, err := q.GetPasswordResetByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, getErr("password_reset", "token", err)
	}
	return passwordResetOf(row)
}

func (r passwordResetRepo) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return 0, err
	}
	n, err := q.DeleteExpiredPasswordResets(ctx, utc(before))
	if err != nil {
		return 0, fmt.Errorf("delete expired password resets: %w", mapErr(err))
	}
	return int(n), nil
}

// apiKeyRepo implements access.APIKeyRepository.
type apiKeyRepo struct{ t *txState }

var _ access.APIKeyRepository = apiKeyRepo{}

func apiKeyParams(k *access.APIKey) (db.CreateAPIKeyParams, error) {
	perms := k.Permissions
	if perms == nil {
		perms = map[permission.APIResource]permission.Level{}
	}
	b, err := encodeJSON(perms)
	if err != nil {
		return db.CreateAPIKeyParams{}, fmt.Errorf("api key %s: %w", k.ID, err)
	}
	return db.CreateAPIKeyParams{
		OrganizationID: k.OrganizationID.UUID(),
		ID:             k.ID.UUID(),
		Name:           k.Name,
		TokenHash:      k.TokenHash,
		Prefix:         k.Prefix,
		Permissions:    b,
		ExpiresAt:      optUTC(k.ExpiresAt),
		LastUsedAt:     optUTC(k.LastUsedAt),
		CreatedAt:      utc(k.CreatedAt),
		UpdatedAt:      utc(k.UpdatedAt),
	}, nil
}

func apiKeyOf(r db.ApiKey) (*access.APIKey, error) {
	var perms map[permission.APIResource]permission.Level
	if err := decodeJSON(r.Permissions, &perms); err != nil {
		return nil, fmt.Errorf("api key %s permissions: %w", r.ID, err)
	}
	if len(perms) == 0 {
		perms = nil
	}
	return &access.APIKey{
		ID:             idOf[core.APIKeyID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Name:           r.Name,
		TokenHash:      r.TokenHash,
		Prefix:         r.Prefix,
		Permissions:    perms,
		ExpiresAt:      optUTC(r.ExpiresAt),
		LastUsedAt:     optUTC(r.LastUsedAt),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r apiKeyRepo) Create(ctx context.Context, k *access.APIKey) error {
	q, err := r.t.queries(ctx, k.OrganizationID)
	if err != nil {
		return err
	}
	p, err := apiKeyParams(k)
	if err != nil {
		return err
	}
	n, err := q.CreateAPIKey(ctx, p)
	return insertErr("api_key", n, err)
}

func (r apiKeyRepo) Update(ctx context.Context, k *access.APIKey) error {
	q, err := r.t.queries(ctx, k.OrganizationID)
	if err != nil {
		return err
	}
	p, err := apiKeyParams(k)
	if err != nil {
		return err
	}
	// Token hashes are unique.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateAPIKey(ctx, db.UpdateAPIKeyParams(p))
		return err
	})
	return updateErr("api_key", k.ID, n, err)
}

func (r apiKeyRepo) Delete(ctx context.Context, org core.OrganizationID, id core.APIKeyID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DeleteAPIKey(ctx, db.DeleteAPIKeyParams{OrganizationID: org.UUID(), ID: id.UUID()})
	return updateErr("api_key", id, n, err)
}

func (r apiKeyRepo) Get(ctx context.Context, org core.OrganizationID, id core.APIKeyID) (*access.APIKey, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetAPIKey(ctx, db.GetAPIKeyParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("api_key", id, err)
	}
	return apiKeyOf(row)
}

// GetByTokenHash is not organization-scoped: it authenticates a request.
func (r apiKeyRepo) GetByTokenHash(ctx context.Context, tokenHash []byte) (*access.APIKey, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	row, err := q.GetAPIKeyByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, getErr("api_key", "token", err)
	}
	return apiKeyOf(row)
}

func (r apiKeyRepo) List(ctx context.Context, org core.OrganizationID, f access.APIKeyFilter, page core.PageRequest) (core.Page[*access.APIKey], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*access.APIKey]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*access.APIKey]{}, err
	}
	rows, err := q.ListAPIKeys(ctx, db.ListAPIKeysParams{
		OrganizationID: org.UUID(), ActiveAt: optUTC(f.ActiveAt), After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*access.APIKey]{}, fmt.Errorf("list api keys: %w", mapErr(err))
	}
	return pageOf(rows, page, apiKeyOf, func(k *access.APIKey) string { return k.ID.String() })
}

func (r apiKeyRepo) CountNonExpiring(ctx context.Context, org core.OrganizationID) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.CountNonExpiringAPIKeys(ctx, org.UUID())
	if err != nil {
		return 0, fmt.Errorf("count api keys: %w", mapErr(err))
	}
	return int(n), nil
}

func (r apiKeyRepo) TouchLastUsed(ctx context.Context, org core.OrganizationID, id core.APIKeyID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	at = utc(at)
	n, err := q.TouchAPIKeyLastUsed(ctx, db.TouchAPIKeyLastUsedParams{OrganizationID: org.UUID(), ID: id.UUID(), LastUsedAt: &at})
	return updateErr("api_key", id, n, err)
}
