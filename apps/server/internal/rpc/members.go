package rpc

import (
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1/accessv1connect"
)

// Members serves tanvo.access.v1.MemberService over the access services
// (lago GraphQL memberships, roles and invites resolvers and mutations).
type Members struct {
	accessv1connect.UnimplementedMemberServiceHandler

	memberships *auth.Memberships
	roles       *auth.Roles
	invites     *auth.Invites
}

var _ accessv1connect.MemberServiceHandler = (*Members)(nil)

// NewMembers returns the service over the access services.
func NewMembers(memberships *auth.Memberships, roles *auth.Roles, invites *auth.Invites) *Members {
	return &Members{memberships: memberships, roles: roles, invites: invites}
}

var membershipStatuses = NewEnum(map[access.MembershipStatus]accessv1.MembershipStatus{
	access.MembershipStatusActive:  accessv1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE,
	access.MembershipStatusRevoked: accessv1.MembershipStatus_MEMBERSHIP_STATUS_REVOKED,
})

var inviteStatuses = NewEnum(map[access.InviteStatus]accessv1.InviteStatus{
	access.InviteStatusPending:  accessv1.InviteStatus_INVITE_STATUS_PENDING,
	access.InviteStatusAccepted: accessv1.InviteStatus_INVITE_STATUS_ACCEPTED,
	access.InviteStatusRevoked:  accessv1.InviteStatus_INVITE_STATUS_REVOKED,
})

// actorID returns the user making the request; zero for other principals.
func actorID(p auth.Principal) core.UserID {
	if p.User == nil {
		return core.UserID{}
	}
	return p.User.ID
}

// ListMemberships implements MemberService.ListMemberships (lago
// MembershipsResolver, app/queries/memberships_query.rb): the active
// memberships, newest first, with the number of admins.
func (s *Members) ListMemberships(ctx context.Context, req *accessv1.ListMembershipsRequest) (*accessv1.ListMembershipsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	roleIDs, err := s.roleFilter(ctx, p.OrganizationID, req.GetFilter())
	if err != nil {
		return nil, err
	}
	f := access.MembershipFilter{RoleIDs: roleIDs, Search: req.GetSearch()}
	page, err := s.memberships.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	admins := int32(page.AdminCount) //nolint:gosec // a member count
	return accessv1.ListMembershipsResponse_builder{
		Memberships:   Items(page.Page, membershipProto),
		NextPageToken: &page.NextToken,
		AdminCount:    &admins,
	}.Build(), nil
}

// GetMembership implements MemberService.GetMembership.
func (s *Members) GetMembership(ctx context.Context, req *accessv1.GetMembershipRequest) (*accessv1.GetMembershipResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.MembershipID]("membership_id", req.GetMembershipId())
	if err != nil {
		return nil, err
	}
	v, err := s.memberships.Describe(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.GetMembershipResponse_builder{Membership: membershipProto(v)}.Build(), nil
}

// UpdateMembership implements MemberService.UpdateMembership (lago
// Memberships::UpdateService): role_ids replaces the roles.
func (s *Members) UpdateMembership(ctx context.Context, req *accessv1.UpdateMembershipRequest) (*accessv1.UpdateMembershipResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetMembership()
	id, err := ParseID[core.MembershipID]("membership.id", b.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), []string{"role_ids"}, func(string) bool { return len(b.GetRoleIds()) > 0 })
	if err != nil {
		return nil, err
	}
	var m *access.Membership
	if slices.Contains(paths, "role_ids") {
		roleIDs, err := s.roleRefs(ctx, p.OrganizationID, "membership.role_ids", b.GetRoleIds())
		if err != nil {
			return nil, err
		}
		if m, err = s.memberships.UpdateRoles(ctx, p.OrganizationID, actorID(p), id, roleIDs); err != nil {
			return nil, Error(ctx, err)
		}
	}
	var v auth.MemberView
	if m != nil {
		v, err = s.memberships.View(ctx, m)
	} else {
		v, err = s.memberships.Describe(ctx, p.OrganizationID, id)
	}
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.UpdateMembershipResponse_builder{Membership: membershipProto(v)}.Build(), nil
}

// RevokeMembership implements MemberService.RevokeMembership (lago
// Memberships::RevokeService): the last admin and one's own membership
// cannot be revoked.
func (s *Members) RevokeMembership(ctx context.Context, req *accessv1.RevokeMembershipRequest) (*accessv1.RevokeMembershipResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.MembershipID]("membership_id", req.GetMembershipId())
	if err != nil {
		return nil, err
	}
	m, err := s.memberships.Revoke(ctx, p.OrganizationID, actorID(p), id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	v, err := s.memberships.View(ctx, m)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.RevokeMembershipResponse_builder{Membership: membershipProto(v)}.Build(), nil
}

// CreateRole implements MemberService.CreateRole (lago Roles::CreateService).
func (s *Members) CreateRole(ctx context.Context, req *accessv1.CreateRoleRequest) (*accessv1.CreateRoleResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetRole()
	perms, err := parsePermissions("role.permissions", b.GetPermissions())
	if err != nil {
		return nil, err
	}
	r, err := s.roles.Create(ctx, p.OrganizationID, auth.RoleInput{
		Code:        b.GetCode(),
		Name:        b.GetName(),
		Description: Opt(b.HasDescription(), b.GetDescription()),
		Permissions: perms,
	})
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.CreateRoleResponse_builder{Role: roleProto(r)}.Build(), nil
}

// GetRole implements MemberService.GetRole (lago RoleResolver).
func (s *Members) GetRole(ctx context.Context, req *accessv1.GetRoleRequest) (*accessv1.GetRoleResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.roleLookup(p.OrganizationID).Find(ctx, "role_id", req.GetRoleId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.GetRoleResponse_builder{Role: roleProto(r)}.Build(), nil
}

// ListRoles implements MemberService.ListRoles (lago RolesResolver): the
// built-in roles, then the custom roles.
func (s *Members) ListRoles(ctx context.Context, req *accessv1.ListRolesRequest) (*accessv1.ListRolesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.roles.List(ctx, p.OrganizationID, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.ListRolesResponse_builder{Roles: Items(page, roleProto), NextPageToken: &page.NextToken}.Build(), nil
}

// roleUpdateFields are the update_mask paths of UpdateRole.
var roleUpdateFields = []string{"name", "description", "permissions"}

// UpdateRole implements MemberService.UpdateRole (lago Roles::UpdateService).
func (s *Members) UpdateRole(ctx context.Context, req *accessv1.UpdateRoleRequest) (*accessv1.UpdateRoleResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetRole()
	current, err := s.roleLookup(p.OrganizationID).Find(ctx, "role.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), roleUpdateFields, func(path string) bool {
		switch path {
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "permissions":
			return len(b.GetPermissions()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var u auth.RoleUpdate
	for _, path := range paths {
		switch path {
		case "name":
			u.Name = new(b.GetName())
		case "description":
			u.Description = new(b.GetDescription())
		case "permissions":
			perms, err := parsePermissions("role.permissions", b.GetPermissions())
			if err != nil {
				return nil, err
			}
			u.Permissions = &perms
		}
	}
	r, err := s.roles.Update(ctx, p.OrganizationID, current.ID, u)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.UpdateRoleResponse_builder{Role: roleProto(r)}.Build(), nil
}

// DeleteRole implements MemberService.DeleteRole (lago
// Roles::DestroyService): built-in roles and roles of active members cannot
// be deleted.
func (s *Members) DeleteRole(ctx context.Context, req *accessv1.DeleteRoleRequest) (*accessv1.DeleteRoleResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.roleLookup(p.OrganizationID).Find(ctx, "role_id", req.GetRoleId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	if _, err := s.roles.Delete(ctx, p.OrganizationID, r.ID); err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.DeleteRoleResponse_builder{}.Build(), nil
}

// CreateInvite implements MemberService.CreateInvite (lago
// Invites::CreateService); the response carries the accept URL, shown once.
func (s *Members) CreateInvite(ctx context.Context, req *accessv1.CreateInviteRequest) (*accessv1.CreateInviteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetInvite()
	roleIDs, err := s.roleRefs(ctx, p.OrganizationID, "invite.role_ids", b.GetRoleIds())
	if err != nil {
		return nil, err
	}
	created, err := s.invites.Create(ctx, auth.InviteRequest{OrganizationID: p.OrganizationID, Actor: actorID(p), Email: b.GetEmail(), RoleIDs: roleIDs})
	if err != nil {
		return nil, Error(ctx, err)
	}
	inv := inviteProto(created.Invite)
	inv.SetAcceptUrl(created.URL)
	return accessv1.CreateInviteResponse_builder{Invite: inv}.Build(), nil
}

// ListInvites implements MemberService.ListInvites (lago InvitesQuery):
// the pending invites, newest first.
func (s *Members) ListInvites(ctx context.Context, req *accessv1.ListInvitesRequest) (*accessv1.ListInvitesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	roleIDs, err := s.roleFilter(ctx, p.OrganizationID, req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.invites.List(ctx, p.OrganizationID, access.InviteFilter{RoleIDs: roleIDs, Search: req.GetSearch()}, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.ListInvitesResponse_builder{Invites: Items(page, inviteProto), NextPageToken: &page.NextToken}.Build(), nil
}

// UpdateInvite implements MemberService.UpdateInvite (lago
// Invites::UpdateService): role_ids replaces the roles.
func (s *Members) UpdateInvite(ctx context.Context, req *accessv1.UpdateInviteRequest) (*accessv1.UpdateInviteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetInvite()
	id, err := ParseID[core.InviteID]("invite.id", b.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), []string{"role_ids"}, func(string) bool { return len(b.GetRoleIds()) > 0 })
	if err != nil {
		return nil, err
	}
	var inv *access.Invite
	if slices.Contains(paths, "role_ids") {
		roleIDs, err := s.roleRefs(ctx, p.OrganizationID, "invite.role_ids", b.GetRoleIds())
		if err != nil {
			return nil, err
		}
		inv, err = s.invites.Update(ctx, p.OrganizationID, actorID(p), id, roleIDs)
		if err != nil {
			return nil, Error(ctx, err)
		}
	} else if inv, err = s.invites.Get(ctx, p.OrganizationID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.UpdateInviteResponse_builder{Invite: inviteProto(inv)}.Build(), nil
}

// RevokeInvite implements MemberService.RevokeInvite (lago
// Invites::RevokeService).
func (s *Members) RevokeInvite(ctx context.Context, req *accessv1.RevokeInviteRequest) (*accessv1.RevokeInviteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.InviteID]("invite_id", req.GetInviteId())
	if err != nil {
		return nil, err
	}
	inv, err := s.invites.Revoke(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.RevokeInviteResponse_builder{Invite: inviteProto(inv)}.Build(), nil
}

func (s *Members) roleLookup(org core.OrganizationID) Lookup[core.RoleID, *access.Role] {
	return Lookup[core.RoleID, *access.Role]{
		ByID:   func(ctx context.Context, id core.RoleID) (*access.Role, error) { return s.roles.Get(ctx, org, id) },
		ByCode: func(ctx context.Context, code string) (*access.Role, error) { return s.roles.GetByCode(ctx, org, code) },
	}
}

// roleRefs resolves role references (IDs or `code:<code>`). Unknown IDs
// are passed on, as the services ignore them (lago
// memberships/update_service.rb:23); unknown codes are "role_not_found".
func (s *Members) roleRefs(ctx context.Context, org core.OrganizationID, field string, refs []string) ([]core.RoleID, error) {
	out := make([]core.RoleID, 0, len(refs))
	for _, ref := range refs {
		r, err := ParseRef(field, ref)
		if err != nil {
			return nil, err
		}
		if r.ExternalID != "" {
			return nil, InvalidArgument("invalid reference", []FieldViolation{{Field: field, Reason: core.CodeInvalid, Description: "roles have no external_id"}})
		}
		if r.Code == "" {
			id, err := core.ParseID[core.RoleID](r.ID.String())
			if err != nil {
				return nil, err
			}
			out = append(out, id)
			continue
		}
		role, err := s.roles.GetByCode(ctx, org, r.Code)
		if err != nil {
			return nil, Error(ctx, err)
		}
		out = append(out, role.ID)
	}
	return out, nil
}

// roleFilter reads the role_id equalities of a membership or invite
// filter.
func (s *Members) roleFilter(ctx context.Context, org core.OrganizationID, filter string) ([]core.RoleID, error) {
	e, err := ParseFilter(filter)
	if err != nil {
		return nil, err
	}
	eq, err := Equalities(e, "role_id")
	if err != nil {
		return nil, err
	}
	if len(eq["role_id"]) == 0 {
		return nil, nil
	}
	return s.roleRefs(ctx, org, FilterField, eq["role_id"])
}

func parsePermissions(field string, values []string) ([]permission.Permission, error) {
	out := make([]permission.Permission, 0, len(values))
	var violations []FieldViolation
	for i, v := range values {
		perm, err := permission.Parse(v)
		if err != nil {
			violations = append(violations, FieldViolation{Field: fmt.Sprintf("%s[%d]", field, i), Reason: core.CodeInvalid, Description: "unknown permission"})
			continue
		}
		out = append(out, perm)
	}
	if len(violations) > 0 {
		return nil, InvalidArgument("invalid permissions", violations)
	}
	return out, nil
}

func userProto(u *access.User) *accessv1.User {
	if u == nil {
		return nil
	}
	return accessv1.User_builder{
		Id:           new(u.ID.String()),
		Email:        &u.Email,
		Name:         u.Name,
		LastSignInAt: TimestampPtr(u.LastSignInAt),
		CreatedAt:    Timestamp(u.CreatedAt),
	}.Build()
}

func membershipProto(v auth.MemberView) *accessv1.Membership {
	m := v.Membership
	return accessv1.Membership_builder{
		Id:          new(m.ID.String()),
		User:        userProto(v.User),
		RoleIds:     idStrings(m.RoleIDs),
		Status:      new(membershipStatuses.Proto(m.Status)),
		RevokedAt:   TimestampPtr(m.RevokedAt),
		CreatedAt:   Timestamp(m.CreatedAt),
		UpdatedAt:   Timestamp(m.UpdatedAt),
		Permissions: permissionNames(v.Permissions()),
	}.Build()
}

func roleProto(r *access.Role) *accessv1.Role {
	_, builtIn := r.BuiltIn()
	b := accessv1.Role_builder{
		Id:          new(r.ID.String()),
		Code:        &r.Code,
		Name:        &r.Name,
		Description: r.Description,
		Permissions: permissionNames(r.EffectivePermissions()),
		BuiltIn:     &builtIn,
	}
	if !r.CreatedAt.IsZero() {
		b.CreatedAt = Timestamp(r.CreatedAt)
	}
	if !r.UpdatedAt.IsZero() {
		b.UpdatedAt = Timestamp(r.UpdatedAt)
	}
	return b.Build()
}

func inviteProto(i *access.Invite) *accessv1.Invite {
	return accessv1.Invite_builder{
		Id:         new(i.ID.String()),
		Email:      &i.Email,
		RoleIds:    idStrings(i.RoleIDs),
		Status:     new(inviteStatuses.Proto(i.Status)),
		ExpiresAt:  Timestamp(i.ExpiresAt),
		AcceptedAt: TimestampPtr(i.AcceptedAt),
		RevokedAt:  TimestampPtr(i.RevokedAt),
		CreatedAt:  Timestamp(i.CreatedAt),
	}.Build()
}

func idStrings(ids []core.RoleID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func permissionNames(perms []permission.Permission) []string {
	out := make([]string, len(perms))
	for i, perm := range perms {
		out[i] = string(perm)
	}
	return out
}
