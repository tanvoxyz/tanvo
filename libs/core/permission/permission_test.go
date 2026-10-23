package permission

import (
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

func TestCatalogSorted(t *testing.T) {
	all := All()
	if !slices.IsSorted(all) {
		t.Fatalf("All() sorted got = false, want = true")
	}
	if len(all) != len(slices.Compact(slices.Clone(all))) {
		t.Fatalf("All() unique got = false, want = true")
	}
	for _, p := range all {
		if !p.WellFormed() {
			t.Errorf("%q WellFormed() got = false, want = true", p)
		}
	}
}

func TestBuiltInRoles(t *testing.T) {
	type tc struct {
		Name       string `yaml:"name"`
		Source     string `yaml:"source"`
		Role       string `yaml:"role"`
		All        bool   `yaml:"all"`
		Permission string `yaml:"permission"`
		Want       bool   `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "roles.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			r, err := ParseBuiltInRole(c.Role)
			if err != nil {
				t.Fatal(err)
			}
			if c.All {
				if got, want := r.Permissions(), All(); !slices.Equal(got, want) {
					t.Errorf("Permissions() got = %d permissions, want = %d", len(got), len(want))
				}
				return
			}
			if got := r.Grants(Permission(c.Permission)); got != c.Want {
				t.Errorf("Grants(%q) got = %v, want = %v (%s)", c.Permission, got, c.Want, c.Source)
			}
		})
	}
}

func TestPermissionParts(t *testing.T) {
	tests := []struct {
		p                Permission
		resource, action string
		wellFormed       bool
	}{
		{"customers:read", "customers", "read", true},
		{"organization:members:read", "organization:members", "read", true},
		{":read", "", "read", false},     // spec/models/role_spec.rb:183
		{"read:", "read", "", false},     // spec/models/role_spec.rb:192
		{"a::read", "a:", "read", false}, // spec/models/role_spec.rb:201
		{"", "", "", false},              // spec/models/role_spec.rb:174
	}
	for _, tt := range tests {
		if got := tt.p.Resource(); got != tt.resource {
			t.Errorf("%q Resource() got = %q, want = %q", tt.p, got, tt.resource)
		}
		if got := tt.p.Action(); got != tt.action {
			t.Errorf("%q Action() got = %q, want = %q", tt.p, got, tt.action)
		}
		if got := tt.p.WellFormed(); got != tt.wellFormed {
			t.Errorf("%q WellFormed() got = %v, want = %v", tt.p, got, tt.wellFormed)
		}
	}
	if _, err := Parse("customers:read"); err != nil {
		t.Errorf("Parse(customers:read) error got = %v, want = nil", err)
	}
	if _, err := Parse("customers:view"); err == nil {
		t.Errorf("Parse(customers:view) error got = nil, want = error")
	}
}

func TestLevel(t *testing.T) {
	tests := []struct {
		have, need Level
		want       bool
	}{
		{LevelRead, LevelRead, true},
		{LevelRead, LevelWrite, false},
		{LevelWrite, LevelRead, true},
		{LevelWrite, LevelWrite, true},
		{"", LevelRead, false},
	}
	for _, tt := range tests {
		if got := tt.have.Allows(tt.need); got != tt.want {
			t.Errorf("%q.Allows(%q) got = %v, want = %v", tt.have, tt.need, got, tt.want)
		}
	}
	if LevelForAction("read") != LevelRead || LevelForAction("void") != LevelWrite {
		t.Errorf("LevelForAction got = wrong level, want = read for read and write otherwise")
	}
}
