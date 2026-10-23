package auth

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
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
