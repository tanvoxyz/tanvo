package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// MemberView is a membership with its user and roles.
type MemberView struct {
	// Membership is the membership.
	Membership *access.Membership
	// User is the member.
	User *access.User
	// Roles are the membership's roles.
	Roles []*access.Role
}

// Permissions returns the union of the permissions of the roles (lago
// Membership#permissions_hash).
func (v MemberView) Permissions() []permission.Permission { return access.UnionPermissions(v.Roles) }

func memberView(ctx context.Context, r port.Repos, m *access.Membership) (MemberView, error) {
	u, err := r.Users.Get(ctx, m.UserID)
	if err != nil {
		return MemberView{}, fmt.Errorf("get user: %w", err)
	}
	roles, err := membershipRoles(ctx, r, m)
	if err != nil {
		return MemberView{}, err
	}
	return MemberView{Membership: m, User: u, Roles: roles}, nil
}

// MemberPage is a page of active memberships.
type MemberPage struct {
	// Page holds the memberships.
	core.Page[MemberView]
	// AdminCount is the number of active admins of the organization (lago
	// memberships resolver metadata admin_count).
	AdminCount int
}

// List returns the active memberships of org (lago MembershipsQuery,
// app/queries/memberships_query.rb), newest first: f.Status and f.Order are
// ignored.
func (s *Memberships) List(ctx context.Context, org core.OrganizationID, f access.MembershipFilter, page core.PageRequest) (MemberPage, error) {
	f.Status, f.Order = access.MembershipStatusActive, core.OrderRecent
	f.Search = core.SearchTerm(f.Search)
	var out MemberPage
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.Memberships.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list memberships: %w", err)
		}
		out.NextToken = p.NextToken
		out.Items = make([]MemberView, 0, len(p.Items))
		for _, m := range p.Items {
			v, err := memberView(ctx, r, m)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, v)
		}
		out.AdminCount, err = r.Memberships.CountActiveByRole(ctx, org, access.BuiltInRoleID(permission.BuiltInRoleAdmin))
		if err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		return nil
	})
	if err != nil {
		return MemberPage{}, err
	}
	return out, nil
}

// Describe returns the membership id of org with its user and roles; an
// unknown one is "membership_not_found".
func (s *Memberships) Describe(ctx context.Context, org core.OrganizationID, id core.MembershipID) (MemberView, error) {
	var out MemberView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		m, err := r.Memberships.Get(ctx, org, id)
		if err != nil {
			return mapNotFound(err, "membership")
		}
		out, err = memberView(ctx, r, m)
		return err
	})
	return out, err
}

// View returns the view of m, read in its own transaction.
func (s *Memberships) View(ctx context.Context, m *access.Membership) (MemberView, error) {
	var out MemberView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = memberView(ctx, r, m)
		return err
	})
	return out, err
}

// List returns the pending invites of org (lago InvitesQuery,
// app/queries/invites_query.rb), newest first: f.Status and f.Order are
// ignored.
func (s *Invites) List(ctx context.Context, org core.OrganizationID, f access.InviteFilter, page core.PageRequest) (core.Page[*access.Invite], error) {
	f.Status, f.Order = access.InviteStatusPending, core.OrderRecent
	f.Search = core.SearchTerm(f.Search)
	var out core.Page[*access.Invite]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Invites.List(ctx, org, f, page)
		return err
	})
	if err != nil {
		return core.Page[*access.Invite]{}, fmt.Errorf("list invites: %w", err)
	}
	return out, nil
}

// Get returns the invite id of org; an unknown one is "invite_not_found".
func (s *Invites) Get(ctx context.Context, org core.OrganizationID, id core.InviteID) (*access.Invite, error) {
	var out *access.Invite
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Invites.Get(ctx, org, id)
		if err != nil {
			return mapNotFound(err, "invite")
		}
		return nil
	})
	return out, err
}

// Get returns the active key id of org; an unknown or expired key is
// "api_key_not_found" (lago ApiKey default_scope, app/models/api_key.rb).
func (s *APIKeys) Get(ctx context.Context, org core.OrganizationID, id core.APIKeyID) (*access.APIKey, error) {
	var out *access.APIKey
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = getKey(ctx, r, org, id)
		if err == nil && !out.Active(s.d.now()) {
			return notFound("api_key")
		}
		return err
	})
	return out, err
}

// List returns the active keys of org in creation order (lago
// ApiKeysResolver).
func (s *APIKeys) List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*access.APIKey], error) {
	now := s.d.now()
	var out core.Page[*access.APIKey]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.APIKeys.List(ctx, org, access.APIKeyFilter{ActiveAt: &now}, page)
		return err
	})
	if err != nil {
		return core.Page[*access.APIKey]{}, fmt.Errorf("list api keys: %w", err)
	}
	return out, nil
}

// mapNotFound maps core.ErrNotFound to "<entity>_not_found".
func mapNotFound(err error, entity string) error {
	if errors.Is(err, core.ErrNotFound) {
		return notFound(entity)
	}
	return fmt.Errorf("get %s: %w", entity, err)
}
