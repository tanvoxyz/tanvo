package integration

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Field error codes of single sign-on settings (lago
// Integrations::OktaIntegration and EntraIdIntegration).
const (
	// CodeDomainNotUnique: another connection of the provider claims the
	// domain.
	CodeDomainNotUnique = "domain_not_unique"
	// CodeTenantIDInvalid: the Entra tenant is not a URL segment.
	CodeTenantIDInvalid = "tenant_id_invalid"
	// CodeHostInvalid: the host is not a URL segment.
	CodeHostInvalid = "host_invalid"
	// CodeIssuerURLInvalid: the issuer is not an absolute http(s) URL
	// without query or fragment.
	CodeIssuerURLInvalid = "issuer_url_invalid"
	// CodeAuthorizationParamReserved: an extra authorization parameter
	// would override one the OpenID Connect flow sets.
	CodeAuthorizationParamReserved = "authorization_param_reserved"
)

// TokenAuthMethod is how a client authenticates at the token endpoint
// (OpenID Connect Core §9).
type TokenAuthMethod string

// TokenAuthMethod values; "" takes the first the provider's discovery
// document lists of these two, client_secret_basic when it lists none.
const (
	// TokenAuthClientSecretBasic sends the credentials with HTTP Basic.
	TokenAuthClientSecretBasic TokenAuthMethod = "client_secret_basic"
	// TokenAuthClientSecretPost sends the credentials in the form body.
	TokenAuthClientSecretPost TokenAuthMethod = "client_secret_post"
)

// Valid reports whether m is "" or a declared value.
func (m TokenAuthMethod) Valid() bool {
	return m == "" || m == TokenAuthClientSecretBasic || m == TokenAuthClientSecretPost
}

// Defaults of generic OpenID Connect connections.
const (
	// DefaultEmailClaim is the ID token claim read as the email.
	DefaultEmailClaim = "email"
	// DefaultNameClaim is the ID token claim read as the display name.
	DefaultNameClaim = "name"
)

// DefaultScopes are the scopes of a generic connection without any.
var DefaultScopes = []string{"openid", "email", "profile"}

// reservedAuthParams are the authorization request parameters the flow
// sets itself (RFC 6749 §4.1.1, RFC 7636 §4.3, OpenID Connect Core
// §3.1.2.1).
var reservedAuthParams = []string{
	"client_id", "redirect_uri", "response_type", "response_mode", "scope", "state", "nonce",
	"code_challenge", "code_challenge_method", "login_hint", "request", "request_uri",
}

var (
	// scopeToken is a scope value (RFC 6749 §3.3: printable ASCII but
	// space, double quote and backslash).
	scopeToken = regexp.MustCompile(`^[\x21\x23-\x5b\x5d-\x7e]{1,128}$`)
	// claimName is a top-level claim name.
	claimName = regexp.MustCompile(`^[A-Za-z0-9_:.\-/]{1,128}$`)
	// paramName is an authorization request parameter name.
	paramName = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)
)

// ValidIssuerURL reports whether s is an absolute http or https URL with
// a host and no query, fragment or user info (OpenID Connect Discovery
// §2). Whether plain http is acceptable is the sign-in service's call.
func ValidIssuerURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" &&
		u.RawQuery == "" && u.Fragment == "" && u.User == nil && !u.ForceQuery && len(s) <= 2048
}

var (
	// domainFormat is a bare host name with at least two labels, so an
	// additional domain is never a single label or a whole TLD (lago
	// EntraIdIntegration::DOMAIN_FORMAT).
	domainFormat = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	// urlSegment is what the tenant and host may contain, as they are
	// interpolated into the provider's URLs (lago
	// tenant_id_and_host_format).
	urlSegment = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
)

// ValidDomain reports whether d is a lower-case host name with at least
// two labels and at most 253 characters.
func ValidDomain(d string) bool { return len(d) <= 253 && domainFormat.MatchString(d) }

// ValidURLSegment reports whether s may be interpolated into a provider
// URL (letters, digits, "." and "-").
func ValidURLSegment(s string) bool { return urlSegment.MatchString(s) }

// AuthSettings are the settings of a single sign-on integration (category
// auth): Okta, Entra ID or a generic OpenID Connect provider (domain
// §17.1). They are stored as the integration's Settings; the client
// secret is in its Secrets. Okta and Entra ID derive their issuer from
// their own fields; the generic provider names it and may tune the
// request and the claims read.
type AuthSettings struct {
	// ClientID is the OAuth client identifier; required.
	ClientID string `json:"client_id,omitempty"`
	// Domain is the primary email domain routed to the connection;
	// required.
	Domain string `json:"domain,omitempty"`
	// AdditionalDomains are more email domains routed to the connection
	// (Entra ID and generic OIDC), lower-case host names.
	AdditionalDomains []string `json:"additional_domains,omitempty"`
	// OrganizationName is the Okta organization; required for Okta, it
	// names the default host "<organization_name>.okta.com".
	OrganizationName string `json:"organization_name,omitempty"`
	// Host overrides the provider host (Okta custom domains, Entra
	// national clouds).
	Host string `json:"host,omitempty"`
	// TenantID is the Entra ID tenant; required for Entra ID.
	TenantID string `json:"tenant_id,omitempty"`
	// DefaultRoleID is the role of memberships created at first sign-in;
	// required ([Fix] domain §5.3).
	DefaultRoleID core.RoleID `json:"default_role_id,omitzero"`

	// The fields below configure generic OIDC only.

	// IssuerURL is the OpenID Connect issuer; required. Endpoints and
	// keys are discovered from IssuerURL + "/.well-known/openid-configuration".
	IssuerURL string `json:"issuer_url,omitempty"`
	// TokenAuthMethod is how the client authenticates at the token
	// endpoint; "" follows the discovery document.
	TokenAuthMethod TokenAuthMethod `json:"token_endpoint_auth_method,omitempty"`
	// Scopes are the requested scopes; empty is [DefaultScopes]. "openid"
	// is always requested.
	Scopes []string `json:"scopes,omitempty"`
	// EmailClaim is the claim read as the email; "" is [DefaultEmailClaim].
	EmailClaim string `json:"email_claim,omitempty"`
	// NameClaim is the claim read as the display name; "" is
	// [DefaultNameClaim].
	NameClaim string `json:"name_claim,omitempty"`
	// AllowUnverifiedEmail accepts identities whose email_verified claim
	// is missing or false; by default it must be true.
	AllowUnverifiedEmail bool `json:"allow_unverified_email,omitempty"`
	// AuthorizationParams are extra authorization request parameters
	// (prompt, acr_values, …); the flow's own parameters are refused.
	AuthorizationParams map[string]string `json:"authorization_params,omitempty"`
}

// Generic reports whether any generic OIDC field is set.
func (s AuthSettings) Generic() bool {
	return s.IssuerURL != "" || s.TokenAuthMethod != "" || len(s.Scopes) > 0 || s.EmailClaim != "" ||
		s.NameClaim != "" || s.AllowUnverifiedEmail || len(s.AuthorizationParams) > 0
}

// NormalizeDomains lower-cases and trims domains, dropping blanks and
// duplicates (lago EntraIdIntegration#additional_domains=).
func NormalizeDomains(domains []string) []string {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// Domains returns the claimed email domains, lower-case, the primary one
// first (lago EntraIdIntegration#domains).
func (s AuthSettings) Domains() []string {
	return NormalizeDomains(append([]string{s.Domain}, s.AdditionalDomains...))
}

// Validate checks the settings of a provider p of category auth: the
// client and the domain always, the organization name for Okta, the
// tenant for Entra ID, the issuer and the request and claim options for
// generic OIDC (refused for the others), a default role, and the formats
// of additional domains, tenant and host.
func (s AuthSettings) Validate(p Provider) error {
	var v core.Validator
	if c, ok := CategoryOf(p); !ok || c != CategoryAuth {
		v.Add("provider", CodeProviderCategoryMismatch)
		return v.Err()
	}
	v.Required("client_id", s.ClientID)
	v.Required("domain", strings.TrimSpace(s.Domain))
	v.Check(!s.DefaultRoleID.IsZero(), "default_role_id", core.CodeRequired)
	switch p {
	case ProviderOkta:
		v.Required("organization_name", s.OrganizationName)
		v.Check(len(s.AdditionalDomains) == 0, "additional_domains", core.CodeInvalid)
	case ProviderEntraID:
		if v.Required("tenant_id", s.TenantID) {
			v.Check(ValidURLSegment(s.TenantID), "tenant_id", CodeTenantIDInvalid)
		}
	case ProviderOIDC:
		s.validateGeneric(&v)
	default:
	}
	if p != ProviderOIDC && s.Generic() {
		for _, f := range []struct {
			name string
			set  bool
		}{
			{"issuer_url", s.IssuerURL != ""},
			{"token_endpoint_auth_method", s.TokenAuthMethod != ""},
			{"scopes", len(s.Scopes) > 0},
			{"email_claim", s.EmailClaim != ""},
			{"name_claim", s.NameClaim != ""},
			{"allow_unverified_email", s.AllowUnverifiedEmail},
			{"authorization_params", len(s.AuthorizationParams) > 0},
		} {
			v.Check(!f.set, f.name, core.CodeInvalid)
		}
	}
	if s.Host != "" {
		v.Check(ValidURLSegment(s.Host), "host", CodeHostInvalid)
	}
	for _, d := range NormalizeDomains(s.AdditionalDomains) {
		if !v.Check(ValidDomain(d), "additional_domains", core.CodeInvalidFormat) {
			break
		}
	}
	return v.Err()
}

// validateGeneric checks the generic OIDC fields; the presets' own
// fields are refused.
func (s AuthSettings) validateGeneric(v *core.Validator) {
	if v.Required("issuer_url", s.IssuerURL) {
		v.Check(ValidIssuerURL(s.IssuerURL), "issuer_url", CodeIssuerURLInvalid)
	}
	v.Check(s.OrganizationName == "", "organization_name", core.CodeInvalid)
	v.Check(s.TenantID == "", "tenant_id", core.CodeInvalid)
	v.Check(s.Host == "", "host", core.CodeInvalid)
	v.Check(s.TokenAuthMethod.Valid(), "token_endpoint_auth_method", core.CodeInvalid)
	v.Check(len(s.Scopes) <= 20, "scopes", core.CodeInvalid)
	for _, sc := range s.Scopes {
		if !v.Check(scopeToken.MatchString(sc), "scopes", core.CodeInvalidFormat) {
			break
		}
	}
	if s.EmailClaim != "" {
		v.Check(claimName.MatchString(s.EmailClaim), "email_claim", core.CodeInvalidFormat)
	}
	if s.NameClaim != "" {
		v.Check(claimName.MatchString(s.NameClaim), "name_claim", core.CodeInvalidFormat)
	}
	v.Check(len(s.AuthorizationParams) <= 20, "authorization_params", core.CodeInvalid)
	for _, k := range slices.Sorted(maps.Keys(s.AuthorizationParams)) {
		switch {
		case !paramName.MatchString(k) || len(s.AuthorizationParams[k]) > 1024:
			v.Add("authorization_params", core.CodeInvalidFormat)
		case slices.Contains(reservedAuthParams, strings.ToLower(k)):
			v.Add("authorization_params", CodeAuthorizationParamReserved)
		default:
			continue
		}
		break
	}
}

// AuthSettings decodes the integration's Settings as single sign-on
// settings.
func (i *Integration) AuthSettings() (AuthSettings, error) {
	var s AuthSettings
	if len(i.Settings) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(i.Settings, &s); err != nil {
		return AuthSettings{}, fmt.Errorf("integration %s auth settings: %w", i.ID, core.ErrInvalidValue)
	}
	return s, nil
}

// SetAuthSettings replaces the integration's Settings with s, its domains
// normalized.
func (i *Integration) SetAuthSettings(s AuthSettings) error {
	s.Domain = strings.ToLower(strings.TrimSpace(s.Domain))
	s.AdditionalDomains = NormalizeDomains(s.AdditionalDomains)
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode auth settings: %w", err)
	}
	i.Settings = b
	return nil
}

// ClaimsDomain reports whether the integration's auth settings claim the
// email domain, case-insensitively.
func (i *Integration) ClaimsDomain(domain string) bool {
	s, err := i.AuthSettings()
	if err != nil {
		return false
	}
	return slices.Contains(s.Domains(), strings.ToLower(strings.TrimSpace(domain)))
}
