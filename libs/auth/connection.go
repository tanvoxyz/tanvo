package auth

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
)

// Field error codes of connections (lago Integrations::OktaIntegration and
// EntraIdIntegration).
const (
	// CodeDomainNotUnique: another connection of the method claims the
	// domain.
	CodeDomainNotUnique = "domain_not_unique"
	// CodeTenantIDInvalid: the Entra tenant is not a URL segment.
	CodeTenantIDInvalid = "tenant_id_invalid"
	// CodeHostInvalid: the host is not a host name.
	CodeHostInvalid = "host_invalid"
)

// DefaultEntraHost is the Entra ID sign-in host of the commercial cloud.
const DefaultEntraHost = "login.microsoftonline.com"

// GoogleIssuer is Google's OpenID Connect issuer.
const GoogleIssuer = "https://accounts.google.com"

// Connection is one OpenID Connect identity provider configuration. Google
// is one connection for the installation (no organization, no domains);
// Okta, Entra ID and generic connections belong to an organization and
// claim email domains, which route sign-ins to them.
type Connection struct {
	// ID identifies the connection (the integration's ID, or "google").
	ID string
	// OrganizationID owns the connection; zero for the installation's
	// Google connection.
	OrganizationID core.OrganizationID
	// Method is the sign-in method recorded on sessions.
	Method organization.AuthenticationMethod
	// Issuer is the OpenID Connect issuer URL, from which endpoints and
	// keys are discovered.
	Issuer string
	// ClientID is the OAuth client identifier.
	ClientID string
	// ClientSecret is the OAuth client secret.
	ClientSecret string
	// RedirectURL is the callback URL registered at the provider.
	RedirectURL string
	// Domains are the claimed email domains, lower-case, the primary one
	// first (Entra additional domains follow it).
	Domains []string
	// DefaultRoleID is the role of memberships auto-provisioned at
	// sign-in (domain §5.3 [Fix]); required for organization connections.
	DefaultRoleID core.RoleID
	// Scopes are requested in addition to "openid"; nil requests "email"
	// and "profile".
	Scopes []string
	// EmailFallbackClaim is read when the identity has no email claim
	// ("preferred_username" for Entra ID).
	EmailFallbackClaim string
	// CreatedAt orders connections claiming the same domain.
	CreatedAt time.Time
}

var (
	domainFormat = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	urlSegment   = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)
)

// GoogleConnection returns the installation's Google connection.
func GoogleConnection(clientID, clientSecret, redirectURL string) *Connection {
	return &Connection{
		ID: "google", Method: organization.AuthenticationMethodGoogleOAuth, Issuer: GoogleIssuer,
		ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
	}
}

// OktaIssuer returns the issuer of an Okta org authorization server: host,
// or "<organizationName>.okta.com" when host is empty (lago
// OktaIntegration#host).
func OktaIssuer(host, organizationName string) string {
	if host == "" {
		host = strings.ToLower(organizationName) + ".okta.com"
	}
	return "https://" + host
}

// EntraIssuer returns the v2.0 issuer of an Entra ID tenant; host defaults
// to [DefaultEntraHost].
func EntraIssuer(host, tenantID string) string {
	if host == "" {
		host = DefaultEntraHost
	}
	return "https://" + host + "/" + tenantID + "/v2.0"
}

// ValidateEntraSettings checks the tenant and host Entra ID URLs are built
// from (lago tenant_id_and_host_format).
func ValidateEntraSettings(host, tenantID string) error {
	var v core.Validator
	if v.Required("tenant_id", tenantID) {
		v.Check(urlSegment.MatchString(tenantID), "tenant_id", CodeTenantIDInvalid)
	}
	if host != "" {
		v.Check(urlSegment.MatchString(host), "host", CodeHostInvalid)
	}
	return v.Err()
}

// NormalizeDomains lower-cases and trims domains, dropping blanks and
// duplicates (lago additional_domains=).
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

// Validate checks the connection's settings: issuer, client, a role for
// organization connections, and well-formed domains (the primary domain
// is required for organization connections; additional ones must be host
// names with at least two labels).
func (c *Connection) Validate() error {
	var v core.Validator
	v.Enum("method", c.Method)
	v.Check(c.Method != organization.AuthenticationMethodEmailPassword, "method", core.CodeInvalid)
	v.Required("issuer", c.Issuer)
	v.Required("client_id", c.ClientID)
	v.Required("client_secret", c.ClientSecret)
	v.Required("redirect_url", c.RedirectURL)
	if !c.OrganizationID.IsZero() {
		v.Check(!c.DefaultRoleID.IsZero(), "default_role_id", core.CodeRequired)
		if v.Check(len(c.Domains) > 0, "domain", core.CodeRequired) {
			for _, d := range c.Domains[1:] {
				if !v.Check(domainFormat.MatchString(d), "additional_domains", core.CodeInvalidFormat) {
					break
				}
			}
		}
	}
	return v.Err()
}

// Claims reports whether c claims the email domain (case-insensitive).
func (c *Connection) Claims(domain string) bool {
	return slices.ContainsFunc(c.Domains, func(d string) bool { return strings.EqualFold(d, domain) })
}

// CheckDomainsUnique refuses a connection claiming a domain another
// connection of the same method claims (lago domain_uniqueness).
func CheckDomainsUnique(c *Connection, others []*Connection) error {
	var v core.Validator
	for i, d := range c.Domains {
		taken := slices.ContainsFunc(others, func(o *Connection) bool { return o.ID != c.ID && o.Method == c.Method && o.Claims(d) })
		field := "additional_domains"
		if i == 0 {
			field = "domain"
		}
		if taken {
			v.Add(field, CodeDomainNotUnique)
		}
	}
	return v.Err()
}

// SelectConnection picks the connection of candidates for an email
// domain: an exact match of a primary domain wins, then the oldest
// connection claiming it case-insensitively (lago Entra routing).
func SelectConnection(candidates []*Connection, domain string) *Connection {
	for _, c := range candidates {
		if len(c.Domains) > 0 && c.Domains[0] == domain {
			return c
		}
	}
	var best *Connection
	for _, c := range candidates {
		if c.Claims(domain) && (best == nil || c.CreatedAt.Before(best.CreatedAt)) {
			best = c
		}
	}
	return best
}

// ConnectionResolver finds the organization connections claiming an email
// domain. apps/server implements it over the integrations of libs/core.
type ConnectionResolver interface {
	// ByDomain returns the connections of method claiming domain
	// case-insensitively, in any order.
	ByDomain(ctx context.Context, method organization.AuthenticationMethod, domain string) ([]*Connection, error)
}

// StaticConnections is a [ConnectionResolver] over a fixed list, for
// connections from configuration and for tests.
type StaticConnections struct {
	mu    sync.RWMutex
	conns []*Connection
}

// NewStaticConnections returns a resolver over conns.
func NewStaticConnections(conns ...*Connection) *StaticConnections {
	return &StaticConnections{conns: slices.Clone(conns)}
}

// Add appends a connection after checking its domains are unique.
func (s *StaticConnections) Add(c *Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := CheckDomainsUnique(c, s.conns); err != nil {
		return fmt.Errorf("add connection: %w", err)
	}
	s.conns = append(s.conns, c)
	return nil
}

// ByDomain implements [ConnectionResolver].
func (s *StaticConnections) ByDomain(_ context.Context, method organization.AuthenticationMethod, domain string) ([]*Connection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Connection
	for _, c := range s.conns {
		if c.Method == method && c.Claims(domain) {
			out = append(out, c)
		}
	}
	return out, nil
}
