package attribution

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Role is how the values of a type relate.
type Role string

// Role values.
const (
	// RoleHierarchical is "hierarchical": the values form a tree under the
	// values of the parent type.
	RoleHierarchical Role = "hierarchical"
	// RoleFlat is "flat": the values are independent labels.
	RoleFlat Role = "flat"
)

// RoleValues returns every Role, in declaration order.
func RoleValues() []Role { return []Role{RoleHierarchical, RoleFlat} }

// Valid reports whether v is one of the declared Role values.
func (v Role) Valid() bool { return v == RoleHierarchical || v == RoleFlat }

// String returns the stored form of v.
func (v Role) String() string { return string(v) }

// ParseRole parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseRole(s string) (Role, error) {
	if v := Role(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("attribution.Role", s)
}
