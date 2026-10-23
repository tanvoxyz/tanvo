package access

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

// Rule codes of roles (domain §5.3).
const (
	// CodeRoleInUse: a custom role used by a membership cannot be deleted.
	CodeRoleInUse = "role_in_use"
	// CodeBuiltInRoleImmutable: built-in roles cannot be changed or deleted.
	CodeBuiltInRoleImmutable = "built_in_role_immutable"
)

// Role is a named set of permissions (domain §5.3). Built-in roles (admin,
// finance, manager) are shared by every organization: their
// OrganizationID is zero and their IDs are fixed ([BuiltInRoleID]). Custom
// roles belong to one organization.
type Role struct {
	// ID identifies the role.
	ID core.RoleID
	// OrganizationID owns a custom role; zero for built-in roles.
	OrganizationID core.OrganizationID
	// Code is unique per organization, ≤ 100 characters of [a-z0-9_], and
	// not a built-in code.
	Code string
	// Name is required, ≤ 100 characters, whitespace collapsed.
	Name string
	// Description is ≤ 255 characters.
	Description *string
	// Permissions are the permissions of a custom role: non-empty, sorted,
	// unique, every one in the catalog. Built-in roles leave it empty and
	// take theirs from package permission.
	Permissions []permission.Permission
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// BuiltInRoleID returns the fixed identifier of a built-in role.
func BuiltInRoleID(r permission.BuiltInRole) core.RoleID {
	switch r {
	case permission.BuiltInRoleAdmin:
		return core.MustParseID[core.RoleID]("00000000-0000-7000-8000-000000000001")
	case permission.BuiltInRoleFinance:
		return core.MustParseID[core.RoleID]("00000000-0000-7000-8000-000000000002")
	case permission.BuiltInRoleManager:
		return core.MustParseID[core.RoleID]("00000000-0000-7000-8000-000000000003")
	default:
		return core.RoleID{}
	}
}

// BuiltInRoles returns the built-in roles, admin first.
func BuiltInRoles() []*Role {
	out := make([]*Role, 0, 3)
	for _, r := range permission.BuiltInRoleValues() {
		name := string(r)
		out = append(out, &Role{ID: BuiltInRoleID(r), Code: string(r), Name: strings.ToUpper(name[:1]) + name[1:]})
	}
	return out
}

// BuiltInRoleByID returns the built-in role with that ID.
func BuiltInRoleByID(id core.RoleID) (*Role, bool) {
	for _, r := range BuiltInRoles() {
		if r.ID == id {
			return r, true
		}
	}
	return nil, false
}

// NewCustomRole returns a custom role of org with a normalized name and
// sorted, deduplicated permissions.
func NewCustomRole(id core.RoleID, org core.OrganizationID, code, name string, perms []permission.Permission, now time.Time) *Role {
	return &Role{
		ID:             id,
		OrganizationID: org,
		Code:           code,
		Name:           NormalizeRoleName(name),
		Permissions:    NormalizePermissions(perms),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// NormalizeRoleName trims a role name and collapses inner whitespace.
func NormalizeRoleName(name string) string { return strings.Join(strings.Fields(name), " ") }

// NormalizePermissions returns perms sorted and without duplicates.
func NormalizePermissions(perms []permission.Permission) []permission.Permission {
	out := slices.Clone(perms)
	slices.Sort(out)
	return slices.Compact(out)
}

// BuiltIn returns the built-in role r is, if any.
func (r *Role) BuiltIn() (permission.BuiltInRole, bool) {
	b := permission.BuiltInRole(r.Code)
	if r.OrganizationID.IsZero() && b.Valid() {
		return b, true
	}
	return "", false
}

// Admin reports whether r is the built-in admin role.
func (r *Role) Admin() bool {
	b, ok := r.BuiltIn()
	return ok && b == permission.BuiltInRoleAdmin
}

// Grants reports whether r holds p.
func (r *Role) Grants(p permission.Permission) bool {
	if b, ok := r.BuiltIn(); ok {
		return b.Grants(p)
	}
	_, found := slices.BinarySearch(r.Permissions, p)
	return found
}

// EffectivePermissions returns the permissions r holds, sorted.
func (r *Role) EffectivePermissions() []permission.Permission {
	if b, ok := r.BuiltIn(); ok {
		return b.Permissions()
	}
	return slices.Clone(r.Permissions)
}

var roleCodePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// Validate checks the invariants of a custom role (domain §5.3). Built-in
// roles are always valid.
func (r *Role) Validate() error {
	if _, ok := r.BuiltIn(); ok {
		return nil
	}
	var v core.Validator
	v.Check(!r.OrganizationID.IsZero(), "organization_id", core.CodeRequired)
	if v.String("code", r.Code, 100) {
		if v.Check(roleCodePattern.MatchString(r.Code), "code", core.CodeInvalid) {
			v.Check(!permission.BuiltInRole(r.Code).Valid(), "code", core.CodeAlreadyExists)
		}
	}
	v.String("name", r.Name, 100)
	v.OptionalString("description", r.Description, 255)
	if v.Check(len(r.Permissions) > 0, "permissions", core.CodeRequired) {
		for _, p := range r.Permissions {
			if !v.Check(p.Valid(), "permissions", core.CodeInvalid) {
				break
			}
		}
		v.Check(slices.IsSorted(r.Permissions) && len(slices.Compact(slices.Clone(r.Permissions))) == len(r.Permissions), "permissions", core.CodeDuplicated)
	}
	return v.Err()
}

// CheckRoleUpdate refuses changes to built-in roles.
func CheckRoleUpdate(r *Role) error {
	if _, ok := r.BuiltIn(); ok {
		return core.NewRuleError(CodeBuiltInRoleImmutable, "built-in roles cannot be changed")
	}
	return nil
}

// CheckRoleDeletion refuses deleting a built-in role or a custom role used
// by activeMemberships memberships.
func CheckRoleDeletion(r *Role, activeMemberships int) error {
	if err := CheckRoleUpdate(r); err != nil {
		return err
	}
	if activeMemberships > 0 {
		return core.NewRuleError(CodeRoleInUse, "the role is used by a membership")
	}
	return nil
}
