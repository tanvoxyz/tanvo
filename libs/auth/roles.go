package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Roles manages the custom roles of organizations (lago app/services/roles/).
// The caller checks the actor's permission (roles:delete, …) first.
type Roles struct {
	d Deps
}

// NewRoles returns the role service.
func NewRoles(d Deps) *Roles { return &Roles{d: d} }

// Delete soft-deletes the custom role id of org (lago
// app/services/roles/destroy_service.rb). An unknown or discarded role is
// "role_not_found"; a predefined role is [access.CodePredefinedRole]
// (destroy_service.rb:14); a role held by an active membership is
// [access.CodeRoleAssignedToMembers] (destroy_service.rb:15). A role held
// only by revoked memberships is discarded, and those memberships no
// longer list it. The discarded role's code can be reused.
func (s *Roles) Delete(ctx context.Context, org core.OrganizationID, id core.RoleID) (*access.Role, error) {
	var role *access.Role
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		role, err = r.Roles.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return notFound("role")
		}
		if err != nil {
			return fmt.Errorf("get role: %w", err)
		}
		n, err := r.Memberships.CountActiveByRole(ctx, org, id)
		if err != nil {
			return fmt.Errorf("count memberships: %w", err)
		}
		if err := access.CheckRoleDeletion(role, n); err != nil {
			return err
		}
		now := s.d.now()
		if err := r.Roles.Discard(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard role: %w", err)
		}
		role.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}
	// lago destroy_service.rb:33-40 passes no user.
	s.d.record(ctx, SecurityEvent{OrganizationID: org, Type: SecurityLogRole, Event: EventRoleDeleted, Resources: map[string]any{"role_code": role.Code}})
	return role, nil
}
