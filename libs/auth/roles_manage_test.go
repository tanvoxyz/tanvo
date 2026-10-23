package auth

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestRolesCreate(t *testing.T) {
	tests := []struct {
		name   string
		source string
		in     RoleInput
		field  string
	}{
		{
			name: "creates the role", source: "spec/services/roles/create_service_spec.rb:20",
			in: RoleInput{Code: "developer", Name: "Developer", Description: ptr("Dev role"), Permissions: []permission.Permission{"plans:read", "addons:read"}},
		},
		{
			name: "nil name", source: "spec/services/roles/create_service_spec.rb:41",
			in: RoleInput{Code: "developer", Permissions: []permission.Permission{"addons:read"}}, field: "name",
		},
		{
			name: "reserved code", source: "spec/services/roles/create_service_spec.rb:58",
			in: RoleInput{Code: "admin", Name: "Admin", Permissions: []permission.Permission{"addons:read"}}, field: "code",
		},
		{
			name: "duplicate code", source: "app/models/role.rb validates :code, uniqueness",
			in: RoleInput{Code: "taken", Name: "Taken", Permissions: []permission.Permission{"addons:read"}}, field: "code",
		},
		{
			name: "no permissions", source: "app/models/role.rb validates :permissions, presence",
			in: RoleInput{Code: "developer", Name: "Developer"}, field: "permissions",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			roles := NewRoles(e.deps)
			if _, err := roles.Create(e.ctx, o.ID, RoleInput{Code: "taken", Name: "Taken", Permissions: []permission.Permission{"addons:read"}}); err != nil {
				t.Fatal(err)
			}
			before := e.log.count(EventRoleCreated)
			got, err := roles.Create(e.ctx, o.ID, tt.in)
			if fieldOf(err) != tt.field {
				t.Fatalf("field got = %q (%v), want = %q (%s)", fieldOf(err), err, tt.field, tt.source)
			}
			if n, want := e.log.count(EventRoleCreated)-before, map[bool]int{true: 1, false: 0}[tt.field == ""]; n != want {
				t.Errorf("%s logs got = %d, want = %d (%s)", EventRoleCreated, n, want, tt.source)
			}
			if tt.field != "" {
				return
			}
			if got.Code != "developer" || got.Name != "Developer" || *got.Description != "Dev role" || got.OrganizationID != o.ID {
				t.Errorf("role got = %+v", got)
			}
			if want := []permission.Permission{"addons:read", "plans:read"}; !slices.Equal(got.Permissions, want) {
				t.Errorf("permissions got = %v, want = %v", got.Permissions, want)
			}
			ev := e.log.events[len(e.log.events)-1]
			if ev.Resources["role_code"] != "developer" || !slices.Equal(ev.Resources["permissions"].([]string), []string{"addons:read", "plans:read"}) {
				t.Errorf("resources got = %v", ev.Resources)
			}
			found, err := roles.GetByCode(e.ctx, o.ID, "developer")
			if err != nil || found.ID != got.ID {
				t.Errorf("GetByCode got = %v, %v, want = %s", found, err, got.ID)
			}
		})
	}
}

func TestRolesUpdate(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		missing   bool
		builtIn   bool
		update    RoleUpdate
		code      string
		field     string
		wantName  string
		wantDesc  string
		resources map[string]any
	}{
		{
			name: "updates the role", source: "spec/services/roles/update_service_spec.rb:16",
			update:   RoleUpdate{Name: ptr("New Name"), Description: ptr("New description")},
			wantName: "New Name", wantDesc: "New description",
			resources: map[string]any{
				"role_code":   "developer",
				"name":        map[string]any{"deleted": "Developer", "added": "New Name"},
				"description": map[string]any{"deleted": "Dev role", "added": "New description"},
			},
		},
		{
			name: "partial params", source: "spec/services/roles/update_service_spec.rb:30",
			update:   RoleUpdate{Name: ptr("Only Name")},
			wantName: "Only Name", wantDesc: "Dev role",
		},
		{
			name: "permissions diff", source: "app/services/roles/update_service.rb:31-48",
			update:   RoleUpdate{Permissions: &[]permission.Permission{"plans:read", "coupons:read"}},
			wantName: "Developer", wantDesc: "Dev role",
			resources: map[string]any{
				"role_code":   "developer",
				"permissions": map[string]any{"deleted": []string{"addons:read"}, "added": []string{"coupons:read"}},
			},
		},
		{
			name: "invalid params", source: "spec/services/roles/update_service_spec.rb:39",
			update: RoleUpdate{Name: ptr("")}, field: "name",
		},
		{
			name: "nil role", source: "spec/services/roles/update_service_spec.rb:57",
			missing: true, update: RoleUpdate{Name: ptr("x")}, code: "role_not_found",
		},
		{
			name: "predefined role", source: "spec/services/roles/update_service_spec.rb:71",
			builtIn: true, update: RoleUpdate{Name: ptr("x")}, code: access.CodePredefinedRole,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			roles := NewRoles(e.deps)
			created, err := roles.Create(e.ctx, o.ID, RoleInput{Code: "developer", Name: "Developer", Description: ptr("Dev role"), Permissions: []permission.Permission{"addons:read", "plans:read"}})
			if err != nil {
				t.Fatal(err)
			}
			id := created.ID
			switch {
			case tt.missing:
				id = core.NewID[core.RoleID](e.deps.IDs)
			case tt.builtIn:
				id = roleID(finance)
			}
			_, err = roles.Update(e.ctx, o.ID, id, tt.update)
			if tt.field != "" {
				if fieldOf(err) != tt.field {
					t.Fatalf("field got = %q (%v), want = %q (%s)", fieldOf(err), err, tt.field, tt.source)
				}
			} else if code(err) != tt.code {
				t.Fatalf("code got = %q, want = %q (%s)", code(err), tt.code, tt.source)
			}
			ok := err == nil
			if n, want := e.log.count(EventRoleUpdated), map[bool]int{true: 1, false: 0}[ok]; n != want {
				t.Errorf("%s logs got = %d, want = %d (%s)", EventRoleUpdated, n, want, tt.source)
			}
			e.tx(func(ctx context.Context, r port.Repos) error {
				got, err := r.Roles.Get(ctx, o.ID, created.ID)
				if err != nil {
					return err
				}
				wantName, wantDesc := tt.wantName, tt.wantDesc
				if !ok {
					wantName, wantDesc = "Developer", "Dev role"
				}
				if got.Name != wantName || *got.Description != wantDesc {
					t.Errorf("role got = %q/%q, want = %q/%q (%s)", got.Name, *got.Description, wantName, wantDesc, tt.source)
				}
				return nil
			})
			if tt.resources != nil {
				ev := e.log.events[len(e.log.events)-1]
				if got, want := toJSON(t, ev.Resources), toJSON(t, tt.resources); got != want {
					t.Errorf("resources got = %s, want = %s", got, want)
				}
			}
		})
	}
}

func TestRolesUpdateKeepsInvites(t *testing.T) {
	// source: spec/services/roles/update_service_spec.rb:89
	e := newEnv(t)
	o := e.org()
	roles := NewRoles(e.deps)
	role, err := roles.Create(e.ctx, o.ID, RoleInput{Code: "developer", Name: "Developer", Permissions: []permission.Permission{"addons:read"}})
	if err != nil {
		t.Fatal(err)
	}
	inv := access.NewInvite(core.NewID[core.InviteID](e.deps.IDs), o.ID, "x@hooli.com", []core.RoleID{role.ID}, []byte("h"), e.clock.Now().Add(time.Hour), e.clock.Now())
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Invites.Create(ctx, inv) })
	if _, err := roles.Update(e.ctx, o.ID, role.ID, RoleUpdate{Name: ptr("Engineer")}); err != nil {
		t.Fatal(err)
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		got, err := r.Invites.Get(ctx, o.ID, inv.ID)
		if err != nil {
			return err
		}
		if !slices.Equal(got.RoleIDs, []core.RoleID{role.ID}) {
			t.Errorf("invite roles got = %v, want = %v", got.RoleIDs, []core.RoleID{role.ID})
		}
		return nil
	})
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
