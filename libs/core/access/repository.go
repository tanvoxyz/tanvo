package access

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// UserRepository stores users. Users span organizations, so its methods
// are not organization-scoped.
type UserRepository interface {
	// Create inserts u; an email already used (case-insensitively) fails
	// with core.ErrConflict.
	Create(ctx context.Context, u *User) error
	// Update replaces the stored user u.ID with u.
	Update(ctx context.Context, u *User) error
	// Get returns the user id, or core.ErrNotFound.
	Get(ctx context.Context, id core.UserID) (*User, error)
	// GetByEmail returns the user with the normalized email, or
	// core.ErrNotFound.
	GetByEmail(ctx context.Context, email string) (*User, error)
}

// MembershipFilter narrows [MembershipRepository.List].
type MembershipFilter struct {
	// Status keeps memberships with that status; "" keeps all.
	Status MembershipStatus
	// RoleID keeps memberships having that role among their roles; zero
	// keeps all.
	RoleID core.RoleID
	// RoleIDs keeps memberships having one of these roles among their
	// roles; empty keeps all (lago MembershipsQuery role_ids).
	RoleIDs []core.RoleID
	// Search keeps memberships whose user's email contains it,
	// case-insensitively; "" keeps all (lago MembershipsQuery search_term).
	Search string
	// Order is the list order; [core.OrderRecent] lists by creation,
	// newest first, then by ID.
	Order core.ListOrder
}

// MembershipRepository stores memberships.
type MembershipRepository interface {
	// Create inserts m with its roles; a second active membership of the
	// same user in the organization fails with core.ErrConflict, an active
	// membership without roles with core.ErrInvalidValue.
	Create(ctx context.Context, m *Membership) error
	// Update replaces the stored membership m.ID with m. Roles of m not
	// stored are added; stored roles not in m are soft-deleted (lago
	// MembershipRole#discard); the roles of discarded roles are left alone.
	Update(ctx context.Context, m *Membership) error
	// Get returns the membership id of org, or core.ErrNotFound. Every read
	// returns the membership's kept roles that are not discarded roles.
	Get(ctx context.Context, org core.OrganizationID, id core.MembershipID) (*Membership, error)
	// GetActiveByUser returns the active membership of user in org, or
	// core.ErrNotFound.
	GetActiveByUser(ctx context.Context, org core.OrganizationID, user core.UserID) (*Membership, error)
	// ListActiveByUser returns the user's active memberships in every
	// organization, for sign-in; not organization-scoped.
	ListActiveByUser(ctx context.Context, user core.UserID) ([]*Membership, error)
	// List returns the organization's memberships in creation order.
	List(ctx context.Context, org core.OrganizationID, f MembershipFilter, page core.PageRequest) (core.Page[*Membership], error)
	// CountActiveByRole returns the number of active memberships of org
	// having role (with the admin role's ID: the active admins, lago
	// Organization#admin_membership_roles).
	CountActiveByRole(ctx context.Context, org core.OrganizationID, role core.RoleID) (int, error)
}

// RoleRepository stores custom roles and serves the built-in ones, which
// exist in every organization.
type RoleRepository interface {
	// Create inserts the custom role r; a code used by another role of
	// the organization fails with core.ErrConflict.
	Create(ctx context.Context, r *Role) error
	// Update replaces the stored custom role r.ID with r.
	Update(ctx context.Context, r *Role) error
	// Discard soft-deletes the custom role id of org at at (lago
	// Role#discard!): a discarded role is no longer found by Get,
	// GetByCode or List, no membership holds it any more, and its code can
	// be reused. A built-in, unknown or already discarded role is
	// core.ErrNotFound.
	Discard(ctx context.Context, org core.OrganizationID, id core.RoleID, at time.Time) error
	// Get returns the built-in role id or the kept custom role id of org,
	// or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.RoleID) (*Role, error)
	// GetByCode returns the built-in or kept custom role with that code.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Role, error)
	// List returns the built-in roles, then the organization's kept custom
	// roles in creation order.
	List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*Role], error)
}

// InviteFilter narrows [InviteRepository.List].
type InviteFilter struct {
	// Status keeps invites with that status; "" keeps all.
	Status InviteStatus
	// RoleIDs keeps invites granting one of these roles; empty keeps all
	// (lago InvitesQuery role_ids).
	RoleIDs []core.RoleID
	// Search keeps invites whose email contains it, case-insensitively;
	// "" keeps all (lago InvitesQuery search_term).
	Search string
	// Order is the list order; [core.OrderRecent] lists by creation,
	// newest first, then by ID.
	Order core.ListOrder
}

// InviteRepository stores invites.
type InviteRepository interface {
	// Create inserts i; a second pending invite for the same email in the
	// organization fails with core.ErrConflict.
	Create(ctx context.Context, i *Invite) error
	// Update replaces the stored invite i.ID with i; a second pending
	// invite for the same email or a token hash of another invite fails
	// with core.ErrConflict.
	Update(ctx context.Context, i *Invite) error
	// Get returns the invite id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.InviteID) (*Invite, error)
	// GetByTokenHash returns the invite with that token hash, in any
	// organization (the invitee is not yet a member).
	GetByTokenHash(ctx context.Context, tokenHash []byte) (*Invite, error)
	// GetPendingByEmail returns the pending invite of the normalized email
	// in org, or core.ErrNotFound.
	GetPendingByEmail(ctx context.Context, org core.OrganizationID, email string) (*Invite, error)
	// List returns the organization's invites in creation order.
	List(ctx context.Context, org core.OrganizationID, f InviteFilter, page core.PageRequest) (core.Page[*Invite], error)
}

// PasswordResetRepository stores password reset tokens; they belong to
// users, so its methods are not organization-scoped.
type PasswordResetRepository interface {
	// Create inserts p.
	Create(ctx context.Context, p *PasswordReset) error
	// Update replaces the stored reset p.ID with p (to mark it used); a
	// token hash of another reset fails with core.ErrConflict.
	Update(ctx context.Context, p *PasswordReset) error
	// GetByTokenHash returns the reset with that token hash, or
	// core.ErrNotFound.
	GetByTokenHash(ctx context.Context, tokenHash []byte) (*PasswordReset, error)
	// DeleteExpired removes the resets expired before t and returns how
	// many.
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

// APIKeyFilter narrows [APIKeyRepository.List].
type APIKeyFilter struct {
	// ActiveAt, when set, keeps keys not expired at that instant.
	ActiveAt *time.Time
}

// APIKeyRepository stores API keys.
type APIKeyRepository interface {
	// Create inserts k; a token hash already used fails with
	// core.ErrConflict.
	Create(ctx context.Context, k *APIKey) error
	// Update replaces the stored key k.ID with k.
	Update(ctx context.Context, k *APIKey) error
	// Delete removes the key id of org.
	Delete(ctx context.Context, org core.OrganizationID, id core.APIKeyID) error
	// Get returns the key id of org, or core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.APIKeyID) (*APIKey, error)
	// GetByTokenHash returns the key with that token hash in any
	// organization, for authentication, or core.ErrNotFound.
	GetByTokenHash(ctx context.Context, tokenHash []byte) (*APIKey, error)
	// List returns the organization's keys in creation order.
	List(ctx context.Context, org core.OrganizationID, f APIKeyFilter, page core.PageRequest) (core.Page[*APIKey], error)
	// CountNonExpiring returns the organization's keys without expiry.
	CountNonExpiring(ctx context.Context, org core.OrganizationID) (int, error)
	// TouchLastUsed sets the key's last_used_at to at.
	TouchLastUsed(ctx context.Context, org core.OrganizationID, id core.APIKeyID, at time.Time) error
	// RecordUses records the last uses of org's keys, keeping the latest
	// recorded use of each; uses of unknown keys are dropped. They reach
	// the keys' last_used_at with [APIKeyRepository.ApplyUses].
	RecordUses(ctx context.Context, org core.OrganizationID, uses map[core.APIKeyID]time.Time) error
	// ApplyUses moves the recorded uses of org's keys to their
	// last_used_at, which only moves forward, and returns how many keys
	// were updated.
	ApplyUses(ctx context.Context, org core.OrganizationID) (int, error)
	// UseOrganizations returns the organizations with recorded uses. It is
	// not organization-scoped: it feeds the usage tracking clock job.
	UseOrganizations(ctx context.Context) ([]core.OrganizationID, error)
}

// SessionRepository stores user sessions; they belong to users, so its
// methods are not organization-scoped.
type SessionRepository interface {
	// Create inserts s; a token hash already used fails with
	// core.ErrConflict.
	Create(ctx context.Context, s *Session) error
	// GetByTokenHash returns the session with that token hash, or
	// core.ErrNotFound.
	GetByTokenHash(ctx context.Context, tokenHash []byte) (*Session, error)
	// Touch sets the session's last_seen_at to at, or fails with
	// core.ErrNotFound.
	Touch(ctx context.Context, id core.SessionID, at time.Time) error
	// Revoke sets the session's revoked_at to at unless it is already
	// revoked, or fails with core.ErrNotFound.
	Revoke(ctx context.Context, id core.SessionID, at time.Time) error
	// RevokeByUser revokes every unrevoked session of user except keep
	// (zero keeps none) and returns how many.
	RevokeByUser(ctx context.Context, user core.UserID, keep core.SessionID, at time.Time) (int, error)
	// DeleteExpired removes the sessions that expired or were revoked
	// before before, and returns how many.
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

// OIDCStateRepository stores the states of OpenID Connect sign-ins in
// progress; they belong to no organization.
type OIDCStateRepository interface {
	// Create inserts s; a key hash already used fails with
	// core.ErrConflict.
	Create(ctx context.Context, s *OIDCState) error
	// Take removes and returns the state with that key hash; a missing
	// state, or one expired at now, is core.ErrNotFound (and removed).
	Take(ctx context.Context, keyHash []byte, now time.Time) (*OIDCState, error)
	// DeleteExpired removes the states expired before before and returns
	// how many.
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

// UserDeviceRepository stores the known devices of users; they belong to
// users, so its methods are not organization-scoped.
type UserDeviceRepository interface {
	// Create inserts d; a second device of the user with the same
	// fingerprint fails with core.ErrConflict.
	Create(ctx context.Context, d *UserDevice) error
	// Update writes the client description and the last sign-in of device
	// d.ID of user d.UserID; the fingerprint and creation are immutable.
	Update(ctx context.Context, d *UserDevice) error
	// GetByFingerprint returns the user's device with that fingerprint,
	// or core.ErrNotFound.
	GetByFingerprint(ctx context.Context, user core.UserID, fingerprint string) (*UserDevice, error)
	// ListByUser returns the user's devices in creation order.
	ListByUser(ctx context.Context, user core.UserID) ([]*UserDevice, error)
}
