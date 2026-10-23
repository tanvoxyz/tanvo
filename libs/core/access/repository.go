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
	// RoleID keeps memberships with that role; zero keeps all.
	RoleID core.RoleID
}

// MembershipRepository stores memberships.
type MembershipRepository interface {
	// Create inserts m; a second active membership of the same user in
	// the organization fails with core.ErrConflict.
	Create(ctx context.Context, m *Membership) error
	// Update replaces the stored membership m.ID with m.
	Update(ctx context.Context, m *Membership) error
	// Get returns the membership id of org, or core.ErrNotFound.
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
	// with role (with the admin role's ID: the active admins).
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
	// Delete removes the custom role id of org.
	Delete(ctx context.Context, org core.OrganizationID, id core.RoleID) error
	// Get returns the built-in role id or the custom role id of org, or
	// core.ErrNotFound.
	Get(ctx context.Context, org core.OrganizationID, id core.RoleID) (*Role, error)
	// GetByCode returns the built-in or custom role with that code.
	GetByCode(ctx context.Context, org core.OrganizationID, code string) (*Role, error)
	// List returns the built-in roles, then the organization's custom
	// roles in creation order.
	List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*Role], error)
}

// InviteFilter narrows [InviteRepository.List].
type InviteFilter struct {
	// Status keeps invites with that status; "" keeps all.
	Status InviteStatus
}

// InviteRepository stores invites.
type InviteRepository interface {
	// Create inserts i; a second pending invite for the same email in the
	// organization fails with core.ErrConflict.
	Create(ctx context.Context, i *Invite) error
	// Update replaces the stored invite i.ID with i.
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
	// Update replaces the stored reset p.ID with p (to mark it used).
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
}
