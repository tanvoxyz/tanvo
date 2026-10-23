package integration

import (
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestAuthSettingsValidate(t *testing.T) {
	role := core.NewID[core.RoleID](core.UUIDv7Generator{})
	base := AuthSettings{ClientID: "c", Domain: "hooli.com", DefaultRoleID: role}
	with := func(f func(*AuthSettings)) AuthSettings {
		s := base
		f(&s)
		return s
	}
	tests := []struct {
		name string
		p    Provider
		s    AuthSettings
		want []string
	}{
		{name: "okta", p: ProviderOkta, s: with(func(s *AuthSettings) { s.OrganizationName = "hooli" }), want: []string{}},
		// source: spec/models/integrations/okta_integration_spec.rb:9
		{name: "okta without organization", p: ProviderOkta, s: base, want: []string{"organization_name: value_is_mandatory"}},
		{name: "okta refuses additional domains", p: ProviderOkta, s: with(func(s *AuthSettings) {
			s.OrganizationName, s.AdditionalDomains = "hooli", []string{"piper.com"}
		}), want: []string{"additional_domains: invalid_value"}},
		{name: "entra", p: ProviderEntraID, s: with(func(s *AuthSettings) {
			s.TenantID, s.AdditionalDomains = "tenant-1.example", []string{" Piper.com "}
		}), want: []string{}},
		// source: spec/models/integrations/entra_id_integration_spec.rb:9
		{name: "entra without tenant", p: ProviderEntraID, s: base, want: []string{"tenant_id: value_is_mandatory"}},
		{name: "entra tenant outside a url segment", p: ProviderEntraID, s: with(func(s *AuthSettings) { s.TenantID = "a/b" }), want: []string{"tenant_id: tenant_id_invalid"}},
		{name: "entra host outside a url segment", p: ProviderEntraID, s: with(func(s *AuthSettings) { s.TenantID, s.Host = "t", "evil.com/x" }), want: []string{"host: host_invalid"}},
		{name: "entra single-label additional domain", p: ProviderEntraID, s: with(func(s *AuthSettings) {
			s.TenantID, s.AdditionalDomains = "t", []string{"localhost"}
		}), want: []string{"additional_domains: invalid_format"}},
		{name: "oidc", p: ProviderOIDC, s: with(func(s *AuthSettings) {
			s.IssuerURL, s.AdditionalDomains, s.Scopes = "https://id.hooli.com", []string{"piper.com"}, []string{"openid", "email", "groups"}
			s.TokenAuthMethod, s.EmailClaim, s.NameClaim = TokenAuthClientSecretPost, "upn", "given_name"
			s.AuthorizationParams = map[string]string{"prompt": "login", "acr_values": "mfa"}
		}), want: []string{}},
		{name: "oidc without issuer", p: ProviderOIDC, s: base, want: []string{"issuer_url: value_is_mandatory"}},
		{name: "oidc issuer with a query", p: ProviderOIDC, s: with(func(s *AuthSettings) { s.IssuerURL = "https://id.hooli.com?x=1" }), want: []string{"issuer_url: issuer_url_invalid"}},
		{name: "oidc issuer not a url", p: ProviderOIDC, s: with(func(s *AuthSettings) { s.IssuerURL = "id.hooli.com" }), want: []string{"issuer_url: issuer_url_invalid"}},
		{name: "oidc refuses preset fields", p: ProviderOIDC, s: with(func(s *AuthSettings) {
			s.IssuerURL, s.OrganizationName, s.TenantID, s.Host = "https://id.hooli.com", "hooli", "t", "h.com"
		}), want: []string{"organization_name: invalid_value", "tenant_id: invalid_value", "host: invalid_value"}},
		{name: "oidc unknown token auth method", p: ProviderOIDC, s: with(func(s *AuthSettings) {
			s.IssuerURL, s.TokenAuthMethod = "https://id.hooli.com", "private_key_jwt"
		}), want: []string{"token_endpoint_auth_method: invalid_value"}},
		{name: "oidc malformed scope and claims", p: ProviderOIDC, s: with(func(s *AuthSettings) {
			s.IssuerURL, s.Scopes, s.EmailClaim, s.NameClaim = "https://id.hooli.com", []string{"a b"}, "e mail", "n\"x"
		}), want: []string{"scopes: invalid_format", "email_claim: invalid_format", "name_claim: invalid_format"}},
		{name: "oidc reserved authorization param", p: ProviderOIDC, s: with(func(s *AuthSettings) {
			s.IssuerURL, s.AuthorizationParams = "https://id.hooli.com", map[string]string{"Redirect_URI": "https://evil.test"}
		}), want: []string{"authorization_params: authorization_param_reserved"}},
		{name: "okta refuses generic fields", p: ProviderOkta, s: with(func(s *AuthSettings) {
			s.OrganizationName, s.IssuerURL, s.Scopes = "hooli", "https://id.hooli.com", []string{"groups"}
		}), want: []string{"issuer_url: invalid_value", "scopes: invalid_value"}},
		// source: spec/models/integrations/okta_integration_spec.rb:8
		// source: spec/models/integrations/okta_integration_spec.rb:10
		{name: "missing client, domain and role", p: ProviderOIDC, s: AuthSettings{IssuerURL: "https://id.hooli.com"}, want: []string{
			"client_id: value_is_mandatory", "domain: value_is_mandatory", "default_role_id: value_is_mandatory",
		}},
		{name: "not an auth provider", p: ProviderNetsuite, s: base, want: []string{"provider: provider_category_mismatch"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fieldErrors(tt.s.Validate(tt.p)); !slices.Equal(got, tt.want) {
				t.Errorf("Validate() got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestAuthSettingsDomains(t *testing.T) {
	i := &Integration{Category: CategoryAuth, Provider: ProviderEntraID}
	if err := i.SetAuthSettings(AuthSettings{Domain: " Hooli.COM", AdditionalDomains: []string{"piper.com", "PIPER.com", "", "hooli.com"}}); err != nil {
		t.Fatal(err)
	}
	s, err := i.AuthSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Domains(), []string{"hooli.com", "piper.com"}; !slices.Equal(got, want) {
		t.Errorf("Domains() got = %v, want = %v", got, want)
	}
	for _, c := range []struct {
		domain string
		want   bool
	}{{"HOOLI.com", true}, {"piper.com", true}, {"other.com", false}, {"", false}} {
		if got := i.ClaimsDomain(c.domain); got != c.want {
			t.Errorf("ClaimsDomain(%q) got = %v, want = %v", c.domain, got, c.want)
		}
	}
}
