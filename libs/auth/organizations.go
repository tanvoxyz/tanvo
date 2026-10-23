package auth

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// OrganizationAccess is one organization a user is an active member of,
// for the sign-in responses and the current user (lago CurrentUser
// memberships).
type OrganizationAccess struct {
	// Organization is the organization.
	Organization *organization.Organization
	// Membership is the user's active membership.
	Membership *access.Membership
	// Roles are the membership's roles.
	Roles []*access.Role
}

// Get returns the user id, or core.ErrNotFound.
func (u *Users) Get(ctx context.Context, id core.UserID) (*access.User, error) {
	var out *access.User
	err := u.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Users.Get(ctx, id)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return out, nil
}

// Organizations returns the organizations user is an active member of,
// in membership order. A non-empty method keeps those that allow it.
func (u *Users) Organizations(ctx context.Context, user core.UserID, method organization.AuthenticationMethod) ([]OrganizationAccess, error) {
	var out []OrganizationAccess
	err := u.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ms, err := r.Memberships.ListActiveByUser(ctx, user)
		if err != nil {
			return fmt.Errorf("list memberships: %w", err)
		}
		for _, m := range ms {
			o, err := r.Organizations.Get(ctx, m.OrganizationID)
			if err != nil {
				return fmt.Errorf("get organization: %w", err)
			}
			if method != "" && !o.AllowsAuthentication(method) {
				continue
			}
			roles, err := membershipRoles(ctx, r, m)
			if err != nil {
				return err
			}
			out = append(out, OrganizationAccess{Organization: o, Membership: m, Roles: roles})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// InviteOrganization returns the organization of inv, for the
// invitation page.
func (s *Invites) InviteOrganization(ctx context.Context, inv *access.Invite) (*organization.Organization, error) {
	var out *organization.Organization
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Organizations.Get(ctx, inv.OrganizationID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get organization: %w", err)
	}
	return out, nil
}
