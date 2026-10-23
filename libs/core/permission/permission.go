package permission

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Permission is a permission string "<resource>:<action>" granted to roles
// (domain §5.3), for example "customers:read" or "invoices:void". The
// resource may itself have several segments ("organization:members:read");
// the action is always the last segment.
type Permission string

// Resource returns everything before the last ":".
func (p Permission) Resource() string {
	i := strings.LastIndexByte(string(p), ':')
	if i < 0 {
		return ""
	}
	return string(p[:i])
}

// Action returns the last segment.
func (p Permission) Action() string {
	i := strings.LastIndexByte(string(p), ':')
	return string(p[i+1:])
}

// String returns p.
func (p Permission) String() string { return string(p) }

// WellFormed reports whether p has at least two non-empty segments of
// lower-case letters, digits and underscores separated by single colons.
func (p Permission) WellFormed() bool {
	parts := strings.Split(string(p), ":")
	if len(parts) < 2 {
		return false
	}
	for _, s := range parts {
		if s == "" {
			return false
		}
		for _, r := range s {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
				return false
			}
		}
	}
	return true
}

// Valid reports whether p is in the catalog.
func (p Permission) Valid() bool {
	_, ok := slices.BinarySearchFunc(catalog(), p, func(g grant, p Permission) int {
		return strings.Compare(string(g.permission), string(p))
	})
	return ok
}

// Parse returns s as a catalog permission, failing with
// [core.ErrInvalidValue].
func Parse(s string) (Permission, error) {
	if p := Permission(s); p.Valid() {
		return p, nil
	}
	return "", fmt.Errorf("%w: permission %q", core.ErrInvalidValue, s)
}

// All returns every permission of the catalog, sorted.
func All() []Permission {
	c := catalog()
	out := make([]Permission, len(c))
	for i, g := range c {
		out[i] = g.permission
	}
	return out
}

// Permissions returns the permissions of the built-in role r, sorted: every
// permission for admin, the catalog's grants for finance and manager.
func (r BuiltInRole) Permissions() []Permission {
	var out []Permission
	for _, g := range catalog() {
		if r.grants(g) {
			out = append(out, g.permission)
		}
	}
	return out
}

// Grants reports whether the built-in role r holds p.
func (r BuiltInRole) Grants(p Permission) bool {
	i, ok := slices.BinarySearchFunc(catalog(), p, func(g grant, p Permission) int {
		return strings.Compare(string(g.permission), string(p))
	})
	return ok && r.grants(catalog()[i])
}

func (r BuiltInRole) grants(g grant) bool {
	switch r {
	case BuiltInRoleAdmin:
		return true
	case BuiltInRoleFinance:
		return g.finance
	case BuiltInRoleManager:
		return g.manager
	default:
		return false
	}
}

// Allows reports whether an API key with level l may perform a request
// that needs level required: write includes read.
func (l Level) Allows(required Level) bool {
	switch l {
	case LevelWrite:
		return required == LevelRead || required == LevelWrite
	case LevelRead:
		return required == LevelRead
	default:
		return false
	}
}

// LevelForAction returns the API key level an action needs: [LevelRead]
// for "read", [LevelWrite] for every other action.
func LevelForAction(action string) Level {
	if action == "read" {
		return LevelRead
	}
	return LevelWrite
}
