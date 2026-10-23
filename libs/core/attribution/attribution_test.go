package attribution

import (
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func TestTypeValidate(t *testing.T) {
	type tc struct {
		Name     string   `yaml:"name"`
		Source   string   `yaml:"source"`
		Code     string   `yaml:"code"`
		Display  string   `yaml:"display"`
		Role     string   `yaml:"role"`
		Keys     []string `yaml:"keys"`
		Parent   bool     `yaml:"parent"`
		WantKeys []string `yaml:"want_keys"`
		Errors   []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "type.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			// source: see c.Source
			typ := &Type{Code: c.Code, Role: Role(c.Role), AttributionKeys: NormalizeKeys(c.Keys)}
			if c.Display != "" {
				typ.Name = &c.Display
			}
			if c.Parent {
				typ.ParentID = core.MustParseID[core.UsageAttributionTypeID]("0190a0a0-0000-7000-8000-000000000001")
			}
			if c.WantKeys != nil && !slices.Equal(typ.AttributionKeys, c.WantKeys) {
				t.Errorf("keys got = %q, want = %q (%s)", typ.AttributionKeys, c.WantKeys, c.Source)
			}
			if got := fieldErrors(typ.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("errors got = %q, want = %q (%s)", got, c.Errors, c.Source)
			}
		})
	}
}

func TestRole(t *testing.T) {
	// source: spec/models/usage_attribution_type_spec.rb:8
	if got := RoleValues(); !slices.Equal(got, []Role{"hierarchical", "flat"}) {
		t.Errorf("values got = %q, want = [hierarchical flat]", got)
	}
	for _, r := range RoleValues() {
		if p, err := ParseRole(r.String()); err != nil || p != r {
			t.Errorf("ParseRole(%q) got = %q, %v, want = %q", r, p, err, r)
		}
	}
	if _, err := ParseRole("tree"); err == nil {
		t.Error("ParseRole(tree) error got = nil, want = invalid")
	}
}

func TestValueValidate(t *testing.T) {
	// source: app/models/usage_attribution_value.rb:14 (the spec tests
	// associations only)
	for _, tt := range []struct {
		value string
		want  []string
	}{
		{"eng", []string{}},
		{"", []string{"value: value_is_mandatory"}},
		{strings.Repeat("v", 255), []string{}},
		{strings.Repeat("v", 256), []string{"value: value_is_too_long"}},
	} {
		v := &Value{Value: tt.value}
		if got := fieldErrors(v.Validate()); !slices.Equal(got, tt.want) {
			t.Errorf("Validate(%d characters) got = %q, want = %q", len(tt.value), got, tt.want)
		}
	}
}
