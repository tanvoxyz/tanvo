package memstore

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
)

// accessData holds the tables of package access. Built-in roles are not
// stored: they come from access.BuiltInRoles.
type accessData struct {
	users          *table[core.UserID, access.User]
	memberships    *table[core.MembershipID, access.Membership]
	roles          *table[core.RoleID, access.Role]
	invites        *table[core.InviteID, access.Invite]
	passwordResets *table[core.PasswordResetID, access.PasswordReset]
	apiKeys        *table[core.APIKeyID, access.APIKey]
}

func newAccessData() *accessData {
	return &accessData{
		users: newTable[core.UserID](func(u access.User) access.User {
			u.PasswordHash = slices.Clone(u.PasswordHash)
			return u
		}),
		memberships: newTable[core.MembershipID, access.Membership](nil),
		roles: newTable[core.RoleID](func(r access.Role) access.Role {
			r.Permissions = slices.Clone(r.Permissions)
			return r
		}),
		invites: newTable[core.InviteID](func(i access.Invite) access.Invite {
			i.TokenHash = slices.Clone(i.TokenHash)
			return i
		}),
		passwordResets: newTable[core.PasswordResetID](func(p access.PasswordReset) access.PasswordReset {
			p.TokenHash = slices.Clone(p.TokenHash)
			return p
		}),
		apiKeys: newTable[core.APIKeyID](func(k access.APIKey) access.APIKey {
			k.TokenHash = slices.Clone(k.TokenHash)
			k.Permissions = maps.Clone(k.Permissions)
			return k
		}),
	}
}

func (d *accessData) snapshot() *accessData {
	return &accessData{
		users:          d.users.snapshot(),
		memberships:    d.memberships.snapshot(),
		roles:          d.roles.snapshot(),
		invites:        d.invites.snapshot(),
		passwordResets: d.passwordResets.snapshot(),
		apiKeys:        d.apiKeys.snapshot(),
	}
}

var (
	_ access.UserRepository          = userRepo{}
	_ access.MembershipRepository    = membershipRepo{}
	_ access.RoleRepository          = roleRepo{}
	_ access.InviteRepository        = inviteRepo{}
	_ access.PasswordResetRepository = passwordResetRepo{}
	_ access.APIKeyRepository        = apiKeyRepo{}
)

// userRepo implements access.UserRepository.
type userRepo struct{ d *accessData }

func (r userRepo) emailTaken(u *access.User) bool {
	email := access.NormalizeEmail(u.Email)
	return r.d.users.any(func(x *access.User) bool { return x.ID != u.ID && x.Email == email })
}

func (r userRepo) Create(_ context.Context, u *access.User) error {
	if r.d.users.has(u.ID) {
		return conflict("user", "id")
	}
	if r.emailTaken(u) {
		return conflict("user", "email")
	}
	r.d.users.put(u.ID, *u)
	return nil
}

func (r userRepo) Update(_ context.Context, u *access.User) error {
	if !r.d.users.has(u.ID) {
		return notFound("user", u.ID)
	}
	if r.emailTaken(u) {
		return conflict("user", "email")
	}
	r.d.users.put(u.ID, *u)
	return nil
}

func (r userRepo) Get(_ context.Context, id core.UserID) (*access.User, error) {
	u, ok := r.d.users.get(id)
	if !ok {
		return nil, notFound("user", id)
	}
	return u, nil
}

func (r userRepo) GetByEmail(_ context.Context, email string) (*access.User, error) {
	email = access.NormalizeEmail(email)
	u, ok := r.d.users.first(func(x *access.User) bool { return x.Email == email })
	if !ok {
		return nil, notFound("user", stringer(email))
	}
	return u, nil
}

// membershipRepo implements access.MembershipRepository.
type membershipRepo struct{ d *accessData }

func (r membershipRepo) duplicate(m *access.Membership) bool {
	return m.Active() && r.d.memberships.any(func(x *access.Membership) bool {
		return x.ID != m.ID && x.OrganizationID == m.OrganizationID && x.UserID == m.UserID && x.Active()
	})
}

func (r membershipRepo) Create(_ context.Context, m *access.Membership) error {
	if r.d.memberships.has(m.ID) {
		return conflict("membership", "id")
	}
	if r.duplicate(m) {
		return conflict("membership", "user_id")
	}
	r.d.memberships.put(m.ID, *m)
	return nil
}

func (r membershipRepo) Update(_ context.Context, m *access.Membership) error {
	cur, ok := r.d.memberships.get(m.ID)
	if !ok || cur.OrganizationID != m.OrganizationID {
		return notFound("membership", m.ID)
	}
	if r.duplicate(m) {
		return conflict("membership", "user_id")
	}
	r.d.memberships.put(m.ID, *m)
	return nil
}

func (r membershipRepo) Get(_ context.Context, org core.OrganizationID, id core.MembershipID) (*access.Membership, error) {
	m, ok := r.d.memberships.get(id)
	if !ok || m.OrganizationID != org {
		return nil, notFound("membership", id)
	}
	return m, nil
}

func (r membershipRepo) GetActiveByUser(_ context.Context, org core.OrganizationID, user core.UserID) (*access.Membership, error) {
	m, ok := r.d.memberships.first(func(x *access.Membership) bool {
		return x.OrganizationID == org && x.UserID == user && x.Active()
	})
	if !ok {
		return nil, notFound("membership", user)
	}
	return m, nil
}

func (r membershipRepo) ListActiveByUser(_ context.Context, user core.UserID) ([]*access.Membership, error) {
	return r.d.memberships.find(func(x *access.Membership) bool { return x.UserID == user && x.Active() }), nil
}

func (r membershipRepo) List(_ context.Context, org core.OrganizationID, f access.MembershipFilter, page core.PageRequest) (core.Page[*access.Membership], error) {
	rows := r.d.memberships.find(func(x *access.Membership) bool {
		return x.OrganizationID == org &&
			(f.Status == "" || x.Status == f.Status) &&
			(f.RoleID.IsZero() || x.RoleID == f.RoleID)
	})
	return paginate(rows, func(x *access.Membership) core.MembershipID { return x.ID }, page)
}

func (r membershipRepo) CountActiveByRole(_ context.Context, org core.OrganizationID, role core.RoleID) (int, error) {
	return r.d.memberships.count(func(x *access.Membership) bool {
		return x.OrganizationID == org && x.RoleID == role && x.Active()
	}), nil
}

// roleRepo implements access.RoleRepository.
type roleRepo struct{ d *accessData }

func (r roleRepo) codeTaken(role *access.Role) bool {
	return accessBuiltInCode(role.Code) || r.d.roles.any(func(x *access.Role) bool {
		return x.ID != role.ID && x.OrganizationID == role.OrganizationID && x.Code == role.Code
	})
}

// accessBuiltInCode reports whether code is a built-in role's code.
func accessBuiltInCode(code string) bool {
	return slices.ContainsFunc(access.BuiltInRoles(), func(b *access.Role) bool { return b.Code == code })
}

func (r roleRepo) Create(_ context.Context, role *access.Role) error {
	if _, builtIn := access.BuiltInRoleByID(role.ID); builtIn || r.d.roles.has(role.ID) {
		return conflict("role", "id")
	}
	if r.codeTaken(role) {
		return conflict("role", "code")
	}
	r.d.roles.put(role.ID, *role)
	return nil
}

func (r roleRepo) Update(_ context.Context, role *access.Role) error {
	cur, ok := r.d.roles.get(role.ID)
	if !ok || cur.OrganizationID != role.OrganizationID {
		return notFound("role", role.ID)
	}
	if r.codeTaken(role) {
		return conflict("role", "code")
	}
	r.d.roles.put(role.ID, *role)
	return nil
}

func (r roleRepo) Delete(_ context.Context, org core.OrganizationID, id core.RoleID) error {
	cur, ok := r.d.roles.get(id)
	if !ok || cur.OrganizationID != org {
		return notFound("role", id)
	}
	r.d.roles.del(id)
	return nil
}

func (r roleRepo) Get(_ context.Context, org core.OrganizationID, id core.RoleID) (*access.Role, error) {
	if b, ok := access.BuiltInRoleByID(id); ok {
		return b, nil
	}
	role, ok := r.d.roles.get(id)
	if !ok || role.OrganizationID != org {
		return nil, notFound("role", id)
	}
	return role, nil
}

func (r roleRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*access.Role, error) {
	for _, b := range access.BuiltInRoles() {
		if b.Code == code {
			return b, nil
		}
	}
	role, ok := r.d.roles.first(func(x *access.Role) bool { return x.OrganizationID == org && x.Code == code })
	if !ok {
		return nil, notFound("role", stringer(code))
	}
	return role, nil
}

func (r roleRepo) List(_ context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*access.Role], error) {
	// Built-in IDs sort before every UUIDv7, so the rows stay in key order.
	rows := append(access.BuiltInRoles(), r.d.roles.find(func(x *access.Role) bool { return x.OrganizationID == org })...)
	return paginate(rows, func(x *access.Role) core.RoleID { return x.ID }, page)
}

// inviteRepo implements access.InviteRepository.
type inviteRepo struct{ d *accessData }

func (r inviteRepo) duplicate(i *access.Invite) bool {
	return i.Status == access.InviteStatusPending && r.d.invites.any(func(x *access.Invite) bool {
		return x.ID != i.ID && x.OrganizationID == i.OrganizationID && x.Email == i.Email && x.Status == access.InviteStatusPending
	})
}

func (r inviteRepo) Create(_ context.Context, i *access.Invite) error {
	if r.d.invites.has(i.ID) {
		return conflict("invite", "id")
	}
	if r.duplicate(i) {
		return conflict("invite", "email")
	}
	if r.d.invites.any(func(x *access.Invite) bool { return bytes.Equal(x.TokenHash, i.TokenHash) }) {
		return conflict("invite", "token")
	}
	r.d.invites.put(i.ID, *i)
	return nil
}

func (r inviteRepo) Update(_ context.Context, i *access.Invite) error {
	cur, ok := r.d.invites.get(i.ID)
	if !ok || cur.OrganizationID != i.OrganizationID {
		return notFound("invite", i.ID)
	}
	if r.duplicate(i) {
		return conflict("invite", "email")
	}
	r.d.invites.put(i.ID, *i)
	return nil
}

func (r inviteRepo) Get(_ context.Context, org core.OrganizationID, id core.InviteID) (*access.Invite, error) {
	i, ok := r.d.invites.get(id)
	if !ok || i.OrganizationID != org {
		return nil, notFound("invite", id)
	}
	return i, nil
}

func (r inviteRepo) GetByTokenHash(_ context.Context, tokenHash []byte) (*access.Invite, error) {
	i, ok := r.d.invites.first(func(x *access.Invite) bool { return bytes.Equal(x.TokenHash, tokenHash) })
	if !ok {
		return nil, notFound("invite", stringer("token"))
	}
	return i, nil
}

func (r inviteRepo) GetPendingByEmail(_ context.Context, org core.OrganizationID, email string) (*access.Invite, error) {
	email = access.NormalizeEmail(email)
	i, ok := r.d.invites.first(func(x *access.Invite) bool {
		return x.OrganizationID == org && x.Email == email && x.Status == access.InviteStatusPending
	})
	if !ok {
		return nil, notFound("invite", stringer(email))
	}
	return i, nil
}

func (r inviteRepo) List(_ context.Context, org core.OrganizationID, f access.InviteFilter, page core.PageRequest) (core.Page[*access.Invite], error) {
	rows := r.d.invites.find(func(x *access.Invite) bool {
		return x.OrganizationID == org && (f.Status == "" || x.Status == f.Status)
	})
	return paginate(rows, func(x *access.Invite) core.InviteID { return x.ID }, page)
}

// passwordResetRepo implements access.PasswordResetRepository.
type passwordResetRepo struct{ d *accessData }

func (r passwordResetRepo) Create(_ context.Context, p *access.PasswordReset) error {
	if r.d.passwordResets.has(p.ID) {
		return conflict("password_reset", "id")
	}
	if r.d.passwordResets.any(func(x *access.PasswordReset) bool { return bytes.Equal(x.TokenHash, p.TokenHash) }) {
		return conflict("password_reset", "token")
	}
	r.d.passwordResets.put(p.ID, *p)
	return nil
}

func (r passwordResetRepo) Update(_ context.Context, p *access.PasswordReset) error {
	if !r.d.passwordResets.has(p.ID) {
		return notFound("password_reset", p.ID)
	}
	r.d.passwordResets.put(p.ID, *p)
	return nil
}

func (r passwordResetRepo) GetByTokenHash(_ context.Context, tokenHash []byte) (*access.PasswordReset, error) {
	p, ok := r.d.passwordResets.first(func(x *access.PasswordReset) bool { return bytes.Equal(x.TokenHash, tokenHash) })
	if !ok {
		return nil, notFound("password_reset", stringer("token"))
	}
	return p, nil
}

func (r passwordResetRepo) DeleteExpired(_ context.Context, before time.Time) (int, error) {
	return r.d.passwordResets.deleteWhere(func(x *access.PasswordReset) bool { return x.ExpiresAt.Before(before) }), nil
}

// apiKeyRepo implements access.APIKeyRepository.
type apiKeyRepo struct{ d *accessData }

func (r apiKeyRepo) Create(_ context.Context, k *access.APIKey) error {
	if r.d.apiKeys.has(k.ID) {
		return conflict("api_key", "id")
	}
	if r.d.apiKeys.any(func(x *access.APIKey) bool { return bytes.Equal(x.TokenHash, k.TokenHash) }) {
		return conflict("api_key", "token")
	}
	r.d.apiKeys.put(k.ID, *k)
	return nil
}

func (r apiKeyRepo) Update(_ context.Context, k *access.APIKey) error {
	cur, ok := r.d.apiKeys.get(k.ID)
	if !ok || cur.OrganizationID != k.OrganizationID {
		return notFound("api_key", k.ID)
	}
	if r.d.apiKeys.any(func(x *access.APIKey) bool { return x.ID != k.ID && bytes.Equal(x.TokenHash, k.TokenHash) }) {
		return conflict("api_key", "token")
	}
	r.d.apiKeys.put(k.ID, *k)
	return nil
}

func (r apiKeyRepo) Delete(_ context.Context, org core.OrganizationID, id core.APIKeyID) error {
	cur, ok := r.d.apiKeys.get(id)
	if !ok || cur.OrganizationID != org {
		return notFound("api_key", id)
	}
	r.d.apiKeys.del(id)
	return nil
}

func (r apiKeyRepo) Get(_ context.Context, org core.OrganizationID, id core.APIKeyID) (*access.APIKey, error) {
	k, ok := r.d.apiKeys.get(id)
	if !ok || k.OrganizationID != org {
		return nil, notFound("api_key", id)
	}
	return k, nil
}

func (r apiKeyRepo) GetByTokenHash(_ context.Context, tokenHash []byte) (*access.APIKey, error) {
	k, ok := r.d.apiKeys.first(func(x *access.APIKey) bool { return bytes.Equal(x.TokenHash, tokenHash) })
	if !ok {
		return nil, notFound("api_key", stringer("token"))
	}
	return k, nil
}

func (r apiKeyRepo) List(_ context.Context, org core.OrganizationID, f access.APIKeyFilter, page core.PageRequest) (core.Page[*access.APIKey], error) {
	rows := r.d.apiKeys.find(func(x *access.APIKey) bool {
		return x.OrganizationID == org && (f.ActiveAt == nil || x.Active(*f.ActiveAt))
	})
	return paginate(rows, func(x *access.APIKey) core.APIKeyID { return x.ID }, page)
}

func (r apiKeyRepo) CountNonExpiring(_ context.Context, org core.OrganizationID) (int, error) {
	return r.d.apiKeys.count(func(x *access.APIKey) bool { return x.OrganizationID == org && x.NonExpiring() }), nil
}

func (r apiKeyRepo) TouchLastUsed(_ context.Context, org core.OrganizationID, id core.APIKeyID, at time.Time) error {
	k, ok := r.d.apiKeys.get(id)
	if !ok || k.OrganizationID != org {
		return notFound("api_key", id)
	}
	k.Touch(at)
	r.d.apiKeys.put(id, *k)
	return nil
}
