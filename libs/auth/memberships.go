package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Memberships changes the roles of memberships and revokes them (lago
// Memberships::UpdateService and Memberships::RevokeService). The caller
// checks the actor's permission (organization:members:update or
// organization:members:delete) first.
type Memberships struct {
	d Deps
}

// NewMemberships returns the membership service.
func NewMemberships(d Deps) *Memberships { return &Memberships{d: d} }

// activeAdmins returns the number of active admin memberships of org (lago
// Organization#admin_membership_roles, app/models/organization.rb:40-41).
func activeAdmins(ctx context.Context, r port.Repos, org core.OrganizationID) (int, error) {
	n, err := r.Memberships.CountActiveByRole(ctx, org, access.BuiltInRoleID(permission.BuiltInRoleAdmin))
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// roleCodes returns the codes of roles.
func roleCodes(roles []*access.Role) []string {
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = r.Code
	}
	return out
}

// UpdateRoles replaces the roles of membership id of org with roleIDs on
// behalf of actor (lago app/services/memberships/update_service.rb).
// Unknown role IDs are ignored; none left fails with "role_not_found".
// Assigning admin needs an admin actor ([access.CodeCannotGrantAdmin]),
// and the last admin cannot lose the role ([access.CodeLastAdmin]).
func (s *Memberships) UpdateRoles(ctx context.Context, org core.OrganizationID, actor core.UserID, id core.MembershipID, roleIDs []core.RoleID) (*access.Membership, error) {
	var (
		m              *access.Membership
		email          string
		added, deleted []string
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		m, err = r.Memberships.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return notFound("membership")
		}
		if err != nil {
			return fmt.Errorf("get membership: %w", err)
		}
		// lago resolves the codes with Role.with_code(...).with_organization
		// and drops the unknown ones (update_service.rb:59-61).
		var newRoles []*access.Role
		for _, rid := range access.NormalizeRoleIDs(roleIDs) {
			ro, err := r.Roles.Get(ctx, org, rid)
			if errors.Is(err, core.ErrNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("get role: %w", err)
			}
			newRoles = append(newRoles, ro)
		}
		if len(newRoles) == 0 {
			return notFound("role")
		}
		newIDs := make([]core.RoleID, len(newRoles))
		for i, ro := range newRoles {
			newIDs[i] = ro.ID
		}
		actorIsAdmin := false
		if am, err := r.Memberships.GetActiveByUser(ctx, org, actor); err == nil {
			actorIsAdmin = am.Admin()
		} else if !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("get membership: %w", err)
		}
		admins, err := activeAdmins(ctx, r, org)
		if err != nil {
			return err
		}
		if err := access.CheckRoleChange(actorIsAdmin, m, newIDs, admins); err != nil {
			return err
		}
		oldRoles, err := membershipRoles(ctx, r, m)
		if err != nil {
			return err
		}
		oldCodes, newCodes := roleCodes(oldRoles), roleCodes(newRoles)
		for _, c := range newCodes {
			if !slices.Contains(oldCodes, c) {
				added = append(added, c)
			}
		}
		for _, c := range oldCodes {
			if !slices.Contains(newCodes, c) {
				deleted = append(deleted, c)
			}
		}
		if err := m.ChangeRoles(newIDs, s.d.now()); err != nil {
			return err
		}
		if err := r.Memberships.Update(ctx, m); err != nil {
			return fmt.Errorf("update membership: %w", err)
		}
		u, err := r.Users.Get(ctx, m.UserID)
		if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		email = u.Email
		return nil
	})
	if err != nil {
		return nil, err
	}
	// lago update_service.rb:39-57.
	entry := map[string]any{}
	if len(deleted) > 0 {
		entry["deleted"] = deleted
	}
	if len(added) > 0 {
		entry["added"] = added
	}
	s.d.record(ctx, SecurityEvent{OrganizationID: org, UserID: actor, Type: SecurityLogUser, Event: EventUserRoleEdited, Resources: map[string]any{"email": email, "roles": entry}})
	return m, nil
}

// Revoke revokes membership id of org on behalf of actor (lago
// app/services/memberships/revoke_service.rb): a user cannot revoke their
// own membership, and the last admin cannot be revoked. An unknown
// membership is "membership_not_found". As in lago (revoke_service.rb:15-20,
// membership.rb:28-31), revoking a revoked membership runs the same checks,
// keeps its revocation time and logs "user.deleted" again.
func (s *Memberships) Revoke(ctx context.Context, org core.OrganizationID, actor core.UserID, id core.MembershipID) (*access.Membership, error) {
	var (
		m     *access.Membership
		email string
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		m, err = r.Memberships.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return notFound("membership")
		}
		if err != nil {
			return fmt.Errorf("get membership: %w", err)
		}
		admins, err := activeAdmins(ctx, r, org)
		if err != nil {
			return err
		}
		if err := access.CheckRevoke(actor, m, admins); err != nil {
			return err
		}
		if m.Active() {
			if err := m.Revoke(s.d.now()); err != nil {
				return err
			}
			if err := r.Memberships.Update(ctx, m); err != nil {
				return fmt.Errorf("revoke membership: %w", err)
			}
		}
		u, err := r.Users.Get(ctx, m.UserID)
		if err != nil {
			return fmt.Errorf("get user: %w", err)
		}
		email = u.Email
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.d.record(ctx, SecurityEvent{OrganizationID: org, UserID: actor, Type: SecurityLogUser, Event: EventUserDeleted, Resources: map[string]any{"email": email}})
	return m, nil
}
