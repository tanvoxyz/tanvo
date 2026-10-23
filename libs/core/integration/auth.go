package integration

import (
	"encoding/json"
	"fmt"
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
)

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
// secret is in its Secrets.
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
	// Issuer is the OpenID Connect issuer URL; required for generic OIDC.
	Issuer string `json:"issuer,omitempty"`
	// DefaultRoleID is the role of memberships created at first sign-in;
	// required ([Fix] domain §5.3).
	DefaultRoleID core.RoleID `json:"default_role_id,omitzero"`
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
// tenant for Entra ID, the issuer for generic OIDC, a default role, and
// the formats of additional domains, tenant and host.
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
		v.Required("issuer", s.Issuer)
	default:
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
