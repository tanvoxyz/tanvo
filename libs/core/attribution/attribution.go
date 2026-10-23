package attribution

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Limits of a type (lago usage_attribution_type.rb:13 and :24-26).
const (
	// MaxAttributionKeys bounds the attribution keys of a type.
	MaxAttributionKeys = 4
	// MaxLength bounds the code, the name and each attribution key.
	MaxLength = 255
)

// Rule codes of attribution types (lago's names).
const (
	// CodeForbiddenForFlatRole: a flat type has no parent.
	CodeForbiddenForFlatRole = "forbidden_for_flat_role"
	// CodeMustBeHierarchical: a parent type must be hierarchical.
	CodeMustBeHierarchical = "must_be_hierarchical"
	// CodeCannotFormACycle: a type cannot descend from itself.
	CodeCannotFormACycle = "cannot_form_a_cycle"
	// CodeUsageAlreadyAttributed: the code, role and parent of a type
	// with values are frozen.
	CodeUsageAlreadyAttributed = "usage_already_attributed"
	// CodeCannotBeFlatWithChildren: a type with child types stays
	// hierarchical.
	CodeCannotBeFlatWithChildren = "cannot_be_flat_with_children"
)

// Type is a kind of label usage is attributed to: a level of an account
// tree (organization, team, user) when hierarchical, an independent label
// (model, region) when flat. A deleted type keeps its row, so its
// children still name it.
type Type struct {
	// ID identifies the type.
	ID core.UsageAttributionTypeID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique among the organization's kept types.
	Code string
	// Name is the display name.
	Name *string
	// Description is free text.
	Description *string
	// AttributionKeys are the event attribution keys carrying the type's
	// values, in order of precedence; no other kept type of the
	// organization holds one of them.
	AttributionKeys []string
	// Role is how the values relate.
	Role Role
	// ParentID is the parent type of a hierarchical type; zero for a root.
	// The parent may have been deleted since.
	ParentID core.UsageAttributionTypeID
	// CreatedAt is when the type was created.
	CreatedAt time.Time
	// UpdatedAt is when the type last changed.
	UpdatedAt time.Time
	// DeletedAt is when the type was deleted; nil while kept.
	DeletedAt *time.Time
}

// NormalizeKeys returns keys trimmed, without blanks and duplicates, in
// their order (lago usage_attribution_type.rb:20-22).
func NormalizeKeys(keys []string) []string {
	out := []string{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// Validate checks the type's own fields; its keys must be normalized
// ([NormalizeKeys]). The checks against other types (code and keys
// taken, the parent's role, cycles) are the service's.
//
// source: app/models/usage_attribution_type.rb:24-60
func (t *Type) Validate() error {
	var v core.Validator
	v.Code("code", t.Code, MaxLength)
	v.OptionalString("name", t.Name, MaxLength)
	switch {
	case len(t.AttributionKeys) == 0:
		v.Add("attribution_keys", core.CodeRequired)
	case len(t.AttributionKeys) > MaxAttributionKeys,
		slices.ContainsFunc(t.AttributionKeys, func(k string) bool { return utf8.RuneCountInString(k) > MaxLength }):
		v.Add("attribution_keys", core.CodeTooLong)
	}
	if v.Enum("role", t.Role) {
		v.Check(t.Role != RoleFlat || t.ParentID.IsZero(), "parent_id", CodeForbiddenForFlatRole)
	}
	return v.Err()
}

// Value is a value of a type recorded for a customer: a node of its
// account tree under the parent value, or a flat label.
type Value struct {
	// ID identifies the value.
	ID core.UsageAttributionValueID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// TypeID is the value's type.
	TypeID core.UsageAttributionTypeID
	// CustomerID is the customer the value belongs to.
	CustomerID core.CustomerID
	// ParentID is the parent value; zero for none.
	ParentID core.UsageAttributionValueID
	// Value is the label, unique per customer and type, deleted values
	// included.
	Value string
	// LastSeenAt is when an event last carried the value.
	LastSeenAt *time.Time
	// CreatedAt is when the value was recorded.
	CreatedAt time.Time
	// UpdatedAt is when the value last changed.
	UpdatedAt time.Time
	// DeletedAt is when the value was deleted; nil while kept.
	DeletedAt *time.Time
}

// Validate checks the value's own fields.
//
// source: app/models/usage_attribution_value.rb:14
func (v *Value) Validate() error {
	var val core.Validator
	val.String("value", v.Value, MaxLength)
	return val.Err()
}
