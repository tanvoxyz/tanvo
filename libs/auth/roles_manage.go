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

// Security log events of custom roles (lago Roles::CreateService and
// Roles::UpdateService).
const (
	// EventRoleCreated: a custom role was created; the resources hold its
	// code and permissions.
	EventRoleCreated = "role.created"
	// EventRoleUpdated: a custom role changed; the resources hold its code
	// and the changed name, description and permissions.
	EventRoleUpdated = "role.updated"
)

// RoleInput is the input of [Roles.Create].
type RoleInput struct {
	// Code is the unique code.
	Code string
	// Name is the display name.
	Name string
	// Description is optional.
	Description *string
	// Permissions are the permissions the role holds; at least one.
	Permissions []permission.Permission
}

// RoleUpdate holds the fields of [Roles.Update] to change; nil fields are
// kept, as lago's params.slice(...).compact does.
type RoleUpdate struct {
	// Name replaces the name when set.
	Name *string
	// Description replaces the description when set.
	Description *string
	// Permissions replaces the permissions when set.
	Permissions *[]permission.Permission
}

func getRole(ctx context.Context, r port.Repos, org core.OrganizationID, id core.RoleID) (*access.Role, error) {
	role, err := r.Roles.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, notFound("role")
	}
	if err != nil {
		return nil, fmt.Errorf("get role: %w", err)
	}
	return role, nil
}

// Get returns the built-in role id or the custom role id of org; an
// unknown role is "role_not_found" (lago RoleResolver).
func (s *Roles) Get(ctx context.Context, org core.OrganizationID, id core.RoleID) (*access.Role, error) {
	var out *access.Role
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = getRole(ctx, r, org, id)
		return err
	})
	return out, err
}

// GetByCode returns the built-in or custom role of org with code.
func (s *Roles) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*access.Role, error) {
	var out *access.Role
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Roles.GetByCode(ctx, org, code)
		if errors.Is(err, core.ErrNotFound) {
			return notFound("role")
		}
		return err
	})
	return out, err
}

// List returns the built-in roles, then the organization's custom roles
// (lago RolesResolver).
func (s *Roles) List(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[*access.Role], error) {
	var out core.Page[*access.Role]
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Roles.List(ctx, org, page)
		return err
	})
	if err != nil {
		return core.Page[*access.Role]{}, fmt.Errorf("list roles: %w", err)
	}
	return out, nil
}

// Create creates a custom role of org (lago
// app/services/roles/create_service.rb): the code is unique among the
// organization's roles and not a built-in code, the name is required,
// and the permissions are catalog permissions, at least one. It logs
// "role.created".
func (s *Roles) Create(ctx context.Context, org core.OrganizationID, in RoleInput) (*access.Role, error) {
	var role *access.Role
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		role = access.NewCustomRole(core.NewID[core.RoleID](s.d.IDs), org, in.Code, in.Name, in.Permissions, s.d.now())
		role.Description = in.Description
		if err := role.Validate(); err != nil {
			return err
		}
		if _, err := r.Roles.GetByCode(ctx, org, role.Code); err == nil {
			return fieldError("code", core.CodeAlreadyExists)
		} else if !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("get role: %w", err)
		}
		if err := r.Roles.Create(ctx, role); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return fieldError("code", core.CodeAlreadyExists)
			}
			return fmt.Errorf("create role: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// lago create_service.rb:37-44 passes no user.
	s.d.record(ctx, SecurityEvent{OrganizationID: org, Type: SecurityLogRole, Event: EventRoleCreated, Resources: map[string]any{
		"role_code": role.Code, "permissions": permissionStrings(role.Permissions),
	}})
	return role, nil
}

// Update changes the name, description or permissions of the custom role
// id of org (lago app/services/roles/update_service.rb). A built-in role
// is [access.CodePredefinedRole]; an unknown one "role_not_found". It
// logs "role.updated" with the changed fields.
func (s *Roles) Update(ctx context.Context, org core.OrganizationID, id core.RoleID, u RoleUpdate) (*access.Role, error) {
	var (
		role *access.Role
		diff = map[string]any{}
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if role, err = getRole(ctx, r, org, id); err != nil {
			return err
		}
		if err := access.CheckRoleUpdate(role); err != nil {
			return err
		}
		if u.Name != nil {
			name := access.NormalizeRoleName(*u.Name)
			if name != role.Name {
				diff["name"] = map[string]any{"deleted": role.Name, "added": name}
			}
			role.Name = name
		}
		if u.Description != nil {
			if role.Description == nil || *role.Description != *u.Description {
				d := map[string]any{"added": *u.Description}
				if role.Description != nil {
					d["deleted"] = *role.Description
				}
				diff["description"] = d
			}
			role.Description = u.Description
		}
		if u.Permissions != nil {
			before, after := permissionStrings(role.Permissions), permissionStrings(access.NormalizePermissions(*u.Permissions))
			entry := map[string]any{}
			if deleted := slices.DeleteFunc(slices.Clone(before), func(p string) bool { return slices.Contains(after, p) }); len(deleted) > 0 {
				entry["deleted"] = deleted
			}
			if added := slices.DeleteFunc(slices.Clone(after), func(p string) bool { return slices.Contains(before, p) }); len(added) > 0 {
				entry["added"] = added
			}
			if len(entry) > 0 {
				diff["permissions"] = entry
			}
			role.Permissions = access.NormalizePermissions(*u.Permissions)
		}
		if err := role.Validate(); err != nil {
			return err
		}
		role.UpdatedAt = s.d.now()
		if err := r.Roles.Update(ctx, role); err != nil {
			return fmt.Errorf("update role: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res := map[string]any{"role_code": role.Code}
	for k, v := range diff {
		res[k] = v
	}
	s.d.record(ctx, SecurityEvent{OrganizationID: org, Type: SecurityLogRole, Event: EventRoleUpdated, Resources: res})
	return role, nil
}

func permissionStrings(ps []permission.Permission) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}
