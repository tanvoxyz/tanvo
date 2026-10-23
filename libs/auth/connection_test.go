package auth

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func entra(id string, created time.Time, domains ...string) *Connection {
	return &Connection{
		ID: id, OrganizationID: core.NewID[core.OrganizationID](core.UUIDv7Generator{}), Method: organization.AuthenticationMethodEntraID,
		Issuer: EntraIssuer("", "tenant"), ClientID: "c", ClientSecret: "s", RedirectURL: "https://app.tanvo.test/auth/entra_id/callback",
		Domains: NormalizeDomains(domains), DefaultRoleID: roleID(permission.BuiltInRoleFinance), CreatedAt: created,
	}
}

func TestSelectConnection(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	older := entra("older", t0, "other.com", "hooli.com")
	primary := entra("primary", t0.Add(time.Hour), "hooli.com")
	newer := entra("newer", t0.Add(2*time.Hour), "third.com", "hooli.com")
	tests := []struct {
		name   string
		cands  []*Connection
		domain string
		want   string
	}{
		// source: spec/services/auth/entra_id/authorize_service_spec.rb:15
		{name: "primary domain", cands: []*Connection{primary}, domain: "hooli.com", want: "primary"},
		// source: spec/services/auth/entra_id/authorize_service_spec.rb:24
		{name: "additional domain", cands: []*Connection{older}, domain: "hooli.com", want: "older"},
		{name: "primary wins over an older additional domain", cands: []*Connection{older, primary, newer}, domain: "hooli.com", want: "primary"},
		// source: spec/services/auth/entra_id/authorize_service_spec.rb:53
		{name: "oldest additional domain", cands: []*Connection{newer, older}, domain: "hooli.com", want: "older"},
		// source: spec/services/auth/entra_id/authorize_service_spec.rb:60
		{name: "unclaimed domain", cands: []*Connection{older, primary}, domain: "nope.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectConnection(tt.cands, tt.domain)
			if (got == nil && tt.want != "") || (got != nil && got.ID != tt.want) {
				t.Errorf("got = %v, want = %s", got, tt.want)
			}
		})
	}
}

func TestConnectionValidate(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(c *Connection)
		field string
	}{
		{name: "valid"},
		{name: "missing issuer", edit: func(c *Connection) { c.Issuer = "" }, field: "issuer"},
		{name: "missing default role", edit: func(c *Connection) { c.DefaultRoleID = core.RoleID{} }, field: "default_role_id"},
		{name: "missing domain", edit: func(c *Connection) { c.Domains = nil }, field: "domain"},
		{name: "invalid additional domain", edit: func(c *Connection) { c.Domains = []string{"hooli.com", "localhost"} }, field: "additional_domains"},
		{name: "email password", edit: func(c *Connection) { c.Method = organization.AuthenticationMethodEmailPassword }, field: "method"},
		{name: "installation Google connection", edit: func(c *Connection) {
			*c = *GoogleConnection("c", "s", "https://app.tanvo.test/auth/google/callback")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := entra("x", time.Time{}, "hooli.com")
			if tt.edit != nil {
				tt.edit(c)
			}
			if got := fieldOf(c.Validate()); got != tt.field {
				t.Errorf("got = %q, want = %q", got, tt.field)
			}
		})
	}
}

func TestValidateEntraSettings(t *testing.T) {
	tests := []struct {
		host, tenant, field string
	}{
		{tenant: "3f2a-tenant"},
		{host: "login.microsoftonline.us", tenant: "t"},
		{field: "tenant_id"},
		{tenant: "a/b", field: "tenant_id"},
		{host: "evil.com/x", tenant: "t", field: "host"},
	}
	for _, tt := range tests {
		if got := fieldOf(ValidateEntraSettings(tt.host, tt.tenant)); got != tt.field {
			t.Errorf("ValidateEntraSettings(%q, %q) = %q, want = %q", tt.host, tt.tenant, got, tt.field)
		}
	}
	if got := EntraIssuer("", "t"); got != "https://login.microsoftonline.com/t/v2.0" {
		t.Errorf("EntraIssuer = %s", got)
	}
	if got := OktaIssuer("", "Hooli"); got != "https://hooli.okta.com" {
		t.Errorf("OktaIssuer = %s", got)
	}
	if got := OktaIssuer("sso.hooli.com", "Hooli"); got != "https://sso.hooli.com" {
		t.Errorf("OktaIssuer = %s", got)
	}
}

func TestDomainUniqueness(t *testing.T) {
	s := NewStaticConnections(entra("a", time.Time{}, "hooli.com", "piedpiper.com"))
	if err := s.Add(entra("b", time.Time{}, "PiedPiper.com")); code(err) != CodeDomainNotUnique || fieldOf(err) != "domain" {
		t.Errorf("primary taken = %s on %q", code(err), fieldOf(err))
	}
	if err := s.Add(entra("c", time.Time{}, "raviga.com", "hooli.com")); fieldOf(err) != "additional_domains" {
		t.Errorf("additional taken = %s on %q", code(err), fieldOf(err))
	}
	okta := entra("d", time.Time{}, "hooli.com")
	okta.Method = organization.AuthenticationMethodOkta
	if err := s.Add(okta); err != nil {
		t.Errorf("other method = %v, want = nil", err)
	}
	got, _ := s.ByDomain(t.Context(), organization.AuthenticationMethodEntraID, "HOOLI.com")
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("ByDomain = %v", got)
	}
	if NormalizeDomains([]string{" A.com", "a.com", "", "b.com"})[1] != "b.com" {
		t.Error("NormalizeDomains does not deduplicate")
	}
}

func TestConnectionFromIntegration(t *testing.T) {
	ids := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](ids)
	role := roleID(permission.BuiltInRoleFinance)
	tests := []struct {
		name      string
		p         integration.Provider
		s         integration.AuthSettings
		wantErr   bool
		method    organization.AuthenticationMethod
		issuer    string
		fallback  string
		wantHosts []string
	}{
		{
			name: "okta default host", p: integration.ProviderOkta,
			s:      integration.AuthSettings{ClientID: "c", Domain: "Hooli.com", OrganizationName: "Hooli", DefaultRoleID: role},
			method: organization.AuthenticationMethodOkta, issuer: "https://hooli.okta.com", wantHosts: []string{"hooli.com"},
		},
		{
			name: "entra with additional domains", p: integration.ProviderEntraID,
			s: integration.AuthSettings{
				ClientID: "c", Domain: "hooli.com", AdditionalDomains: []string{"Pied.Piper.com"}, TenantID: "tenant", DefaultRoleID: role,
			},
			method: organization.AuthenticationMethodEntraID, issuer: "https://login.microsoftonline.com/tenant/v2.0",
			fallback: "preferred_username", wantHosts: []string{"hooli.com", "pied.piper.com"},
		},
		{
			name: "generic oidc", p: integration.ProviderOIDC,
			s:      integration.AuthSettings{ClientID: "c", Domain: "hooli.com", IssuerURL: "https://id.hooli.com", DefaultRoleID: role},
			method: organization.AuthenticationMethodOIDC, issuer: "https://id.hooli.com", wantHosts: []string{"hooli.com"},
		},
		{name: "missing default role", p: integration.ProviderOIDC, s: integration.AuthSettings{ClientID: "c", Domain: "hooli.com", IssuerURL: "https://id.hooli.com"}, wantErr: true},
		{name: "entra without tenant", p: integration.ProviderEntraID, s: integration.AuthSettings{ClientID: "c", Domain: "hooli.com", DefaultRoleID: role}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := integration.CategoryOf(tt.p)
			i := &integration.Integration{ID: core.NewID[core.IntegrationID](ids), OrganizationID: org, Category: c, Provider: tt.p, CreatedAt: time.Now()}
			if err := i.SetAuthSettings(tt.s); err != nil {
				t.Fatal(err)
			}
			got, err := ConnectionFromIntegration(i, "secret", "https://app.tanvo.test/callback")
			if (err != nil) != tt.wantErr {
				t.Fatalf("ConnectionFromIntegration() error got = %v, want error = %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.Method != tt.method || got.Issuer != tt.issuer || got.EmailFallbackClaim != tt.fallback || !slices.Equal(got.Domains, tt.wantHosts) ||
				got.ID != i.ID.String() || got.OrganizationID != org || got.DefaultRoleID != role || got.ClientSecret != "secret" {
				t.Errorf("ConnectionFromIntegration() got = %+v, want method %s, issuer %s, domains %v", got, tt.method, tt.issuer, tt.wantHosts)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("Validate() error got = %v, want = nil", err)
			}
		})
	}
}

func TestIntegrationConnections(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	role := roleID(permission.BuiltInRoleFinance)
	add := func(code string, s integration.AuthSettings) *integration.Integration {
		i := &integration.Integration{
			ID: core.NewID[core.IntegrationID](e.deps.IDs), OrganizationID: o.ID, Code: code, Name: code,
			Category: integration.CategoryAuth, Provider: integration.ProviderEntraID, CreatedAt: e.clock.Now(), UpdatedAt: e.clock.Now(),
		}
		if err := i.SetAuthSettings(s); err != nil {
			t.Fatal(err)
		}
		e.tx(func(ctx context.Context, r port.Repos) error { return r.Integrations.Create(ctx, i) })
		return i
	}
	good := add("entra", integration.AuthSettings{ClientID: "c", Domain: "hooli.com", AdditionalDomains: []string{"piper.com"}, TenantID: "t", DefaultRoleID: role})
	add("broken", integration.AuthSettings{ClientID: "c", Domain: "broken.com", DefaultRoleID: role})
	res := IntegrationConnections{
		Tx:     e.store,
		Secret: func(context.Context, *integration.Integration) (string, error) { return "secret", nil },
		RedirectURL: func(m organization.AuthenticationMethod) string {
			return "https://app.tanvo.test/auth/" + string(m) + "/callback"
		},
	}
	tests := []struct {
		name    string
		method  organization.AuthenticationMethod
		domain  string
		want    []string
		wantErr bool
	}{
		{name: "primary domain", method: organization.AuthenticationMethodEntraID, domain: "HOOLI.com", want: []string{good.ID.String()}},
		{name: "additional domain", method: organization.AuthenticationMethodEntraID, domain: "piper.com", want: []string{good.ID.String()}},
		{name: "other provider", method: organization.AuthenticationMethodOkta, domain: "hooli.com"},
		{name: "google has no integrations", method: organization.AuthenticationMethodGoogleOAuth, domain: "hooli.com"},
		{name: "unusable integration", method: organization.AuthenticationMethodEntraID, domain: "broken.com", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := res.ByDomain(e.ctx, tt.method, tt.domain)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ByDomain() error got = %v, want error = %v", err, tt.wantErr)
			}
			ids := make([]string, 0, len(got))
			for _, c := range got {
				ids = append(ids, c.ID)
			}
			if !slices.Equal(ids, tt.want) && (len(ids) > 0 || len(tt.want) > 0) {
				t.Errorf("ByDomain() got = %v, want = %v", ids, tt.want)
			}
		})
	}
}
