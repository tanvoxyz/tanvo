package access

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

// Rule codes of memberships (domain §5.2).
const (
	// CodeLastAdmin: the last active admin cannot be revoked or lose the
	// admin role.
	CodeLastAdmin = "last_admin"
	// CodeCannotRevokeOwnMembership: a user cannot revoke their own
	// membership.
	CodeCannotRevokeOwnMembership = "cannot_revoke_own_membership"
	// CodeCannotGrantAdmin: only an admin can grant the admin role.
	CodeCannotGrantAdmin = "cannot_grant_admin"
	// CodeMembershipRevoked: a revoked membership cannot change.
	CodeMembershipRevoked = "membership_revoked"
)

// Membership links a user to an organization with one or more roles
// (domain §5.2).
type Membership struct {
	// ID identifies the membership.
	ID core.MembershipID
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// UserID is the member; unique with OrganizationID among active
	// memberships.
	UserID core.UserID
	// RoleIDs are the membership's roles, sorted and unique, each a
	// built-in role or a kept custom role of the organization; an active
	// membership has at least one. The membership holds the union of their
	// permissions. A revoked membership can be left without roles when its
	// custom roles are deleted.
	RoleIDs []core.RoleID
	// Status is active or revoked (terminal).
	Status MembershipStatus
	// RevokedAt is set on revocation.
	RevokedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewMembership returns an active membership with roles, normalized by
// [NormalizeRoleIDs].
func NewMembership(id core.MembershipID, org core.OrganizationID, user core.UserID, roles []core.RoleID, now time.Time) *Membership {
	return &Membership{ID: id, OrganizationID: org, UserID: user, RoleIDs: NormalizeRoleIDs(roles), Status: MembershipStatusActive, CreatedAt: now, UpdatedAt: now}
}

// NormalizeRoleIDs returns ids sorted and without duplicates.
func NormalizeRoleIDs(ids []core.RoleID) []core.RoleID {
	out := slices.Clone(ids)
	slices.SortFunc(out, core.RoleID.Compare)
	return slices.Compact(out)
}

// validRoleIDs checks that ids is non-empty, sorted, unique and without
// zero IDs.
func validRoleIDs(v *core.Validator, ids []core.RoleID) {
	if !v.Check(len(ids) > 0, "role_ids", core.CodeRequired) {
		return
	}
	v.Check(!slices.Contains(ids, core.RoleID{}), "role_ids", core.CodeInvalid)
	v.Check(slices.IsSortedFunc(ids, core.RoleID.Compare) && len(slices.Compact(slices.Clone(ids))) == len(ids), "role_ids", core.CodeDuplicated)
}

// HasRole reports whether the membership has role.
func (m *Membership) HasRole(role core.RoleID) bool {
	_, found := slices.BinarySearchFunc(m.RoleIDs, role, core.RoleID.Compare)
	return found
}

// Admin reports whether one of the membership's roles is the built-in
// admin role (lago Membership#admin?, app/models/membership.rb:24-26).
func (m *Membership) Admin() bool { return HasAdmin(m.RoleIDs) }

// HasAdmin reports whether roles include the built-in admin role.
func HasAdmin(roles []core.RoleID) bool {
	return slices.Contains(roles, BuiltInRoleID(permission.BuiltInRoleAdmin))
}

// Validate checks the invariants of domain §5.2.
func (m *Membership) Validate() error {
	var v core.Validator
	v.Check(!m.OrganizationID.IsZero(), "organization_id", core.CodeRequired)
	v.Check(!m.UserID.IsZero(), "user_id", core.CodeRequired)
	if m.Active() || len(m.RoleIDs) > 0 {
		validRoleIDs(&v, m.RoleIDs)
	}
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
// user actor (lago Memberships::RevokeService, revoke_service.rb:16-17): a
// user cannot revoke their own membership, and an admin membership cannot
// be revoked when the organization has one active admin. activeAdmins is
// the number of active admin memberships of the organization. As in lago
// the check does not look at target's status.
func CheckRevoke(actor core.UserID, target *Membership, activeAdmins int) error {
	if target.UserID == actor {
		return core.NewRuleError(CodeCannotRevokeOwnMembership, "a user cannot revoke their own membership")
	}
	if target.Admin() && activeAdmins == 1 {
		return core.NewRuleError(CodeLastAdmin, "the last admin cannot be revoked")
	}
	return nil
}

// CheckRoleChange applies the rules of domain §5.2 to replacing target's
// roles with roles (lago Memberships::UpdateService,
// app/services/memberships/update_service.rb:19-20, 75-81): only an admin
// actor can assign the admin role, even to a membership that already has
// it, and the last active admin cannot lose it. activeAdmins counts the
// organization's active admin memberships (target included).
func CheckRoleChange(actorIsAdmin bool, target *Membership, roles []core.RoleID, activeAdmins int) error {
	newIsAdmin := HasAdmin(roles)
	if newIsAdmin && !actorIsAdmin {
		return core.NewRuleError(CodeCannotGrantAdmin, "only an admin can grant the admin role")
	}
	if target.Active() && target.Admin() && !newIsAdmin && activeAdmins <= 1 {
		return core.NewRuleError(CodeLastAdmin, "the last admin cannot lose the admin role")
	}
	return nil
}

// ChangeRoles replaces the membership's roles with roles, normalized by
// [NormalizeRoleIDs], at now; the caller checks [CheckRoleChange] first. A
// revoked membership cannot change.
func (m *Membership) ChangeRoles(roles []core.RoleID, now time.Time) error {
	if !m.Active() {
		return core.NewRuleError(CodeMembershipRevoked, "a revoked membership cannot change roles")
	}
	m.RoleIDs = NormalizeRoleIDs(roles)
	m.UpdatedAt = now
	return nil
}
