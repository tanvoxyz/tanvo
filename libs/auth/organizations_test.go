package auth

import (
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

func TestUsersOrganizations(t *testing.T) {
	e := newEnv(t)
	u := e.user("foo@bar.com", "ILoveLago!")
	all := e.org()
	google := e.org(organization.AuthenticationMethodGoogleOAuth)
	revoked := e.org()
	e.member(u, all, permission.BuiltInRoleAdmin)
	e.member(u, google, permission.BuiltInRoleFinance)
	e.revoke(e.member(u, revoked, permission.BuiltInRoleAdmin))

	tests := []struct {
		method organization.AuthenticationMethod
		want   []core.OrganizationID
	}{
		{method: "", want: []core.OrganizationID{all.ID, google.ID}},
		{method: organization.AuthenticationMethodEmailPassword, want: []core.OrganizationID{all.ID}},
		{method: organization.AuthenticationMethodGoogleOAuth, want: []core.OrganizationID{all.ID, google.ID}},
	}
	for _, tt := range tests {
		got, err := e.users.Organizations(e.ctx, u.ID, tt.method)
		if err != nil {
			t.Fatalf("Organizations(%q): %v", tt.method, err)
		}
		ids := make([]core.OrganizationID, len(got))
		for i, a := range got {
			ids[i] = a.Organization.ID
			if a.Membership.OrganizationID != a.Organization.ID || len(a.Roles) != 1 {
				t.Errorf("Organizations(%q)[%d] got membership of %s with %d roles, want = its own with 1", tt.method, i, a.Membership.OrganizationID, len(a.Roles))
			}
		}
		if len(ids) != len(tt.want) {
			t.Errorf("Organizations(%q) got = %v, want = %v", tt.method, ids, tt.want)
			continue
		}
		for i := range ids {
			if ids[i] != tt.want[i] {
				t.Errorf("Organizations(%q) got = %v, want = %v", tt.method, ids, tt.want)
				break
			}
		}
	}

	if got, err := e.users.Get(e.ctx, u.ID); err != nil || got.Email != "foo@bar.com" {
		t.Errorf("Get got = %v, %v, want = foo@bar.com", got, err)
	}
	if _, err := e.users.Get(e.ctx, core.NewID[core.UserID](e.deps.IDs)); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Get(unknown) got = %v, want = ErrNotFound", err)
	}
}
