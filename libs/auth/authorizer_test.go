package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

func TestAPIResourceFor(t *testing.T) {
	tests := []struct {
		perm  permission.Permission
		res   permission.APIResource
		level permission.Level
		ok    bool
	}{
		{"customers:read", permission.APIResourceCustomer, permission.LevelRead, true},
		{"customers:create", permission.APIResourceCustomer, permission.LevelWrite, true},
		{"invoices:send", permission.APIResourceInvoice, permission.LevelWrite, true},
		{"billable_metrics:update", permission.APIResourceBillableMetric, permission.LevelWrite, true},
		{"addons:read", permission.APIResourceAddOn, permission.LevelRead, true},
		{"analytics:read", permission.APIResourceAnalytic, permission.LevelRead, true},
		{"audit_logs:view", permission.APIResourceActivityLog, permission.LevelWrite, true},
		{"billing_entities:read", permission.APIResourceBillingEntity, permission.LevelRead, true},
		{"charges:create", permission.APIResourcePlan, permission.LevelWrite, true},
		{"coupons:attach", permission.APIResourceAppliedCoupon, permission.LevelWrite, true},
		{"coupons:read", permission.APIResourceCoupon, permission.LevelRead, true},
		{"organization:taxes:view", permission.APIResourceTax, permission.LevelWrite, true},
		{"organization:invoices:update", permission.APIResourceOrganization, permission.LevelWrite, true},
		{"developers:manage", permission.APIResourceWebhookEndpoint, permission.LevelWrite, true},
		{"payment_requests:read", permission.APIResourcePaymentRequest, permission.LevelRead, true},
		{"product_categories:read", permission.APIResourceProductCategory, permission.LevelRead, true},
		{"roles:read", "", "", false},
		{"dunning_campaigns:read", "", "", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.perm), func(t *testing.T) {
			res, level, ok := APIResourceFor(tt.perm)
			if res != tt.res || level != tt.level || ok != tt.ok {
				t.Errorf("got = %s, %s, %v, want = %s, %s, %v", res, level, ok, tt.res, tt.level, tt.ok)
			}
		})
	}
}

func TestAuthorizerCheck(t *testing.T) {
	az := NewAuthorizer(nil)
	finance, _ := access.BuiltInRoleByID(roleID(permission.BuiltInRoleFinance))
	admin, _ := access.BuiltInRoleByID(roleID(permission.BuiltInRoleAdmin))
	active := &access.Membership{Status: access.MembershipStatusActive}
	revoked := &access.Membership{Status: access.MembershipStatusRevoked}
	custom := access.NewCustomRole(core.MustParseID[core.RoleID]("0190a000-0000-7000-8000-000000000077"), core.MustParseID[core.OrganizationID]("0190a000-0000-7000-8000-000000000001"), "dev", "Dev", []permission.Permission{"addons:read"}, time.Time{})
	mixed := []*access.Role{custom, finance}
	readCustomers := &access.APIKey{Permissions: map[permission.APIResource]permission.Level{permission.APIResourceCustomer: permission.LevelRead}}
	tests := []struct {
		name string
		p    Principal
		perm permission.Permission
		want error
	}{
		{name: "admin", p: Principal{Kind: PrincipalUser, Roles: []*access.Role{admin}, Membership: active}, perm: "roles:create"},
		{name: "finance granted", p: Principal{Kind: PrincipalUser, Roles: []*access.Role{finance}, Membership: active}, perm: "invoices:read"},
		{name: "finance denied", p: Principal{Kind: PrincipalUser, Roles: []*access.Role{finance}, Membership: active}, perm: "roles:create", want: ErrPermissionDenied},
		{name: "revoked membership", p: Principal{Kind: PrincipalUser, Roles: []*access.Role{admin}, Membership: revoked}, perm: "customers:read", want: ErrPermissionDenied},
		// spec/models/membership_spec.rb:124: a membership holds the union of its roles' permissions.
		{name: "mixed roles custom permission", p: Principal{Kind: PrincipalUser, Roles: mixed, Membership: active}, perm: "addons:read"},
		{name: "mixed roles predefined permission", p: Principal{Kind: PrincipalUser, Roles: mixed, Membership: active}, perm: "analytics:read"},
		{name: "mixed roles disabled permission", p: Principal{Kind: PrincipalUser, Roles: mixed, Membership: active}, perm: "coupons:attach", want: ErrPermissionDenied},
		{name: "no roles", p: Principal{Kind: PrincipalUser, Membership: active}, perm: "customers:read", want: ErrPermissionDenied},
		{name: "full access key", p: APIKeyPrincipal(&access.APIKey{}), perm: "customers:delete"},
		{name: "scoped key granted", p: APIKeyPrincipal(readCustomers), perm: "customers:read"},
		{name: "scoped key write denied", p: APIKeyPrincipal(readCustomers), perm: "customers:update", want: ErrPermissionDenied},
		{name: "scoped key other resource", p: APIKeyPrincipal(readCustomers), perm: "invoices:read", want: ErrPermissionDenied},
		{name: "scoped key unmapped permission", p: APIKeyPrincipal(readCustomers), perm: "roles:read", want: ErrPermissionDenied},
		{name: "portal", p: PortalPrincipal(PortalClaims{}), perm: "customers:read", want: ErrPermissionDenied},
		{name: "anonymous", perm: "customers:read", want: ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := az.Check(tt.p, tt.perm); !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
				t.Errorf("got = %v, want = %v", err, tt.want)
			}
		})
	}
	t.Run("CheckAPI", func(t *testing.T) {
		if err := az.CheckAPI(APIKeyPrincipal(readCustomers), permission.APIResourceCustomer, permission.LevelRead); err != nil {
			t.Errorf("got = %v, want = nil", err)
		}
		if err := az.CheckAPI(APIKeyPrincipal(readCustomers), permission.APIResourceCustomer, permission.LevelWrite); !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("got = %v, want = ErrPermissionDenied", err)
		}
		if err := az.CheckAPI(Principal{Kind: PrincipalUser}, permission.APIResourceCustomer, permission.LevelRead); !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("user = %v, want = ErrPermissionDenied", err)
		}
		if err := az.CheckAPI(Principal{}, permission.APIResourceCustomer, permission.LevelRead); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("anonymous = %v, want = ErrUnauthenticated", err)
		}
	})
	t.Run("CheckCustomer", func(t *testing.T) {
		ids := core.UUIDv7Generator{}
		org, cus := core.NewID[core.OrganizationID](ids), core.NewID[core.CustomerID](ids)
		p := PortalPrincipal(PortalClaims{OrganizationID: org, CustomerID: cus})
		if err := az.CheckCustomer(p, org, cus); err != nil {
			t.Errorf("own customer = %v", err)
		}
		if err := az.CheckCustomer(p, org, core.NewID[core.CustomerID](ids)); !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("other customer = %v", err)
		}
		if err := az.CheckCustomer(p, core.NewID[core.OrganizationID](ids), cus); !errors.Is(err, ErrPermissionDenied) {
			t.Errorf("other organization = %v", err)
		}
	})
}

func TestAuthorizerUserPrincipal(t *testing.T) {
	tests := []struct {
		name   string
		method organization.AuthenticationMethod
		setup  func(e *env, u *access.User) *organization.Organization
		want   error
		code   string
	}{
		{name: "active member", method: organization.AuthenticationMethodEmailPassword, setup: func(e *env, u *access.User) *organization.Organization {
			o := e.org()
			e.member(u, o, permission.BuiltInRoleManager)
			return o
		}},
		{name: "not a member", method: organization.AuthenticationMethodEmailPassword, setup: func(e *env, _ *access.User) *organization.Organization { return e.org() }, want: ErrPermissionDenied},
		{name: "revoked member", method: organization.AuthenticationMethodEmailPassword, setup: func(e *env, u *access.User) *organization.Organization {
			o := e.org()
			e.revoke(e.member(u, o, permission.BuiltInRoleManager))
			return o
		}, want: ErrPermissionDenied},
		// lago CurrentOrganization: the session's method must be allowed.
		{name: "method not allowed", method: organization.AuthenticationMethodGoogleOAuth, setup: func(e *env, u *access.User) *organization.Organization {
			o := e.org(organization.AuthenticationMethodOkta)
			e.member(u, o, permission.BuiltInRoleManager)
			return o
		}, code: CodeLoginMethodNotAuthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			u := e.user("gavin@hooli.com", "pw")
			o := tt.setup(e, u)
			_, sess, err := e.sessions.Issue(e.ctx, u.ID, tt.method, SessionMeta{})
			if err != nil {
				t.Fatal(err)
			}
			p, err := e.authz.UserPrincipal(e.ctx, sess, o.ID)
			switch {
			case tt.want != nil:
				if !errors.Is(err, tt.want) {
					t.Errorf("got = %v, want = %v", err, tt.want)
				}
			case tt.code != "":
				if code(err) != tt.code {
					t.Errorf("got = %s, want = %s", code(err), tt.code)
				}
			case err != nil:
				t.Fatal(err)
			default:
				if p.Kind != PrincipalUser || p.User.ID != u.ID || len(p.Roles) != 1 || p.Roles[0].ID != roleID(permission.BuiltInRoleManager) {
					t.Errorf("principal = %+v", p)
				}
				if err := e.authz.Check(p, "customers:create"); err != nil {
					t.Errorf("manager customers:create = %v", err)
				}
			}
		})
	}
}
