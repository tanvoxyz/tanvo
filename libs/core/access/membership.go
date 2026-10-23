package access

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Rule codes of memberships (domain §5.2).
const (
	// CodeLastAdmin: the last active admin cannot be revoked or demoted.
	CodeLastAdmin = "last_admin"
	// CodeCannotRevokeOwnMembership: a user cannot revoke their own
	// membership.
	CodeCannotRevokeOwnMembership = "cannot_revoke_own_membership"
	// CodeAdminRoleRequiresAdmin: only an admin can grant the admin role.
	CodeAdminRoleRequiresAdmin = "admin_role_requires_admin"
	// CodeMembershipRevoked: a revoked membership cannot change.
	CodeMembershipRevoked = "membership_revoked"
)

// Membership links a user to an organization with a role (domain §5.2).
type Membership struct {
	// ID identifies the membership.
	ID core.MembershipID
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// UserID is the member; unique with OrganizationID among active
	// memberships.
	UserID core.UserID
	// RoleID is a built-in role or a custom role of the organization.
	RoleID core.RoleID
	// Status is active or revoked (terminal).
	Status MembershipStatus
	// RevokedAt is set on revocation.
	RevokedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewMembership returns an active membership.
func NewMembership(id core.MembershipID, org core.OrganizationID, user core.UserID, role core.RoleID, now time.Time) *Membership {
	return &Membership{ID: id, OrganizationID: org, UserID: user, RoleID: role, Status: MembershipStatusActive, CreatedAt: now, UpdatedAt: now}
}

// Validate checks the invariants of domain §5.2.
func (m *Membership) Validate() error {
	var v core.Validator
	v.Check(!m.OrganizationID.IsZero(), "organization_id", core.CodeRequired)
	v.Check(!m.UserID.IsZero(), "user_id", core.CodeRequired)
	v.Check(!m.RoleID.IsZero(), "role_id", core.CodeRequired)
	v.Enum("status", m.Status)
	v.Check((m.Status == MembershipStatusRevoked) == (m.RevokedAt != nil), "revoked_at", core.CodeInvalid)
	return v.Err()
}

// Active reports whether the membership is active.
func (m *Membership) Active() bool { return m.Status == MembershipStatusActive }

// CanTransitionMembership reports whether a membership may move from one
// status to another: only active → revoked.
func CanTransitionMembership(from, to MembershipStatus) bool {
	return from == MembershipStatusActive && to == MembershipStatusRevoked
}

// Revoke revokes the membership at now.
func (m *Membership) Revoke(now time.Time) error {
	if !CanTransitionMembership(m.Status, MembershipStatusRevoked) {
		return &core.TransitionError{Entity: "membership", From: string(m.Status), To: string(MembershipStatusRevoked)}
	}
	m.Status = MembershipStatusRevoked
	m.RevokedAt = &now
	m.UpdatedAt = now
	return nil
}

// CheckRevoke applies the rules of domain §5.2 to revoking target by the
// user actor: a user cannot revoke their own membership, and the last
// active admin cannot be revoked. targetIsAdmin tells whether target's
// role is admin; activeAdmins is the number of active admin memberships of
// the organization (target included).
func CheckRevoke(actor core.UserID, target *Membership, targetIsAdmin bool, activeAdmins int) error {
	if target.UserID == actor {
		return core.NewRuleError(CodeCannotRevokeOwnMembership, "a user cannot revoke their own membership")
	}
	if target.Active() && targetIsAdmin && activeAdmins <= 1 {
		return core.NewRuleError(CodeLastAdmin, "the last admin cannot be revoked")
	}
	return nil
}

// CheckRoleChange applies the rules of domain §5.2 to changing target's
// role: only an admin actor can grant the admin role, and the last active
// admin cannot be demoted. currentIsAdmin and newIsAdmin tell whether the
// current and new roles are admin; activeAdmins counts the organization's
// active admin memberships (target included).
func CheckRoleChange(actorIsAdmin bool, target *Membership, currentIsAdmin, newIsAdmin bool, activeAdmins int) error {
	if newIsAdmin && !currentIsAdmin && !actorIsAdmin {
		return core.NewRuleError(CodeAdminRoleRequiresAdmin, "only an admin can grant the admin role")
	}
	if target.Active() && currentIsAdmin && !newIsAdmin && activeAdmins <= 1 {
		return core.NewRuleError(CodeLastAdmin, "the last admin cannot be demoted")
	}
	return nil
}

// ChangeRole sets the membership's role at now; the caller checks
// [CheckRoleChange] first. A revoked membership cannot change.
func (m *Membership) ChangeRole(role core.RoleID, now time.Time) error {
	if !m.Active() {
		return core.NewRuleError(CodeMembershipRevoked, "a revoked membership cannot change role")
	}
	m.RoleID = role
	m.UpdatedAt = now
	return nil
}
