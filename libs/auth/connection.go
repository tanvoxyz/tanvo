package auth

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Field error codes of connections (lago Integrations::OktaIntegration and
// EntraIdIntegration), shared with the integration settings.
const (
	// CodeDomainNotUnique: another connection of the method claims the
	// domain.
	CodeDomainNotUnique = integration.CodeDomainNotUnique
	// CodeTenantIDInvalid: the Entra tenant is not a URL segment.
	CodeTenantIDInvalid = integration.CodeTenantIDInvalid
	// CodeHostInvalid: the host is not a URL segment.
	CodeHostInvalid = integration.CodeHostInvalid
)

// DefaultEntraHost is the Entra ID sign-in host of the commercial cloud.
const DefaultEntraHost = "login.microsoftonline.com"

// GoogleIssuer is Google's OpenID Connect issuer.
const GoogleIssuer = "https://accounts.google.com"

// EmailVerification is how a connection treats the email_verified claim.
type EmailVerification int

// EmailVerification values.
const (
	// EmailVerifiedUnlessFalse refuses an identity whose email_verified
	// is false and accepts one without the claim: the behaviour of the
	// Google, Okta and Entra ID presets (Entra ID sends no such claim).
	EmailVerifiedUnlessFalse EmailVerification = iota
	// EmailVerifiedRequired requires email_verified to be true: the
	// default of generic connections.
	EmailVerifiedRequired
	// EmailVerifiedIgnored ignores the claim (allow_unverified_email).
	EmailVerifiedIgnored
)

// Connection is one OpenID Connect identity provider configuration. Google
// is one connection for the installation (no organization, no domains);
// Okta, Entra ID and generic connections belong to an organization and
// claim email domains, which route sign-ins to them. The presets differ
// only in how their issuer is built and which claims they read; the
// protocol is the same for all.
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
	// EmailClaim is the claim read as the email; "" is "email".
	EmailClaim string
	// EmailFallbackClaim is read when the identity has no email claim
	// ("preferred_username" for Entra ID).
	EmailFallbackClaim string
	// NameClaim is the claim read as the display name; "" is "name".
	NameClaim string
	// EmailVerification is how the email_verified claim is checked.
	EmailVerification EmailVerification
	// TokenAuthMethod is how the client authenticates at the token
	// endpoint; "" follows the provider's discovery document.
	TokenAuthMethod integration.TokenAuthMethod
	// AuthParams are extra authorization request parameters.
	AuthParams map[string]string
	// CreatedAt orders connections claiming the same domain.
	CreatedAt time.Time
}

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
		v.Check(integration.ValidURLSegment(tenantID), "tenant_id", CodeTenantIDInvalid)
	}
	if host != "" {
		v.Check(integration.ValidURLSegment(host), "host", CodeHostInvalid)
	}
	return v.Err()
}

// NormalizeDomains lower-cases and trims domains, dropping blanks and
// duplicates ([integration.NormalizeDomains]).
func NormalizeDomains(domains []string) []string { return integration.NormalizeDomains(domains) }

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
				if !v.Check(integration.ValidDomain(d), "additional_domains", core.CodeInvalidFormat) {
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

// providerOf maps an organization sign-in method to its integration
// provider; Google and passwords have none.
func providerOf(m organization.AuthenticationMethod) (integration.Provider, bool) {
	switch m {
	case organization.AuthenticationMethodOkta:
		return integration.ProviderOkta, true
	case organization.AuthenticationMethodEntraID:
		return integration.ProviderEntraID, true
	case organization.AuthenticationMethodOIDC:
		return integration.ProviderOIDC, true
	default:
		return "", false
	}
}

// ConnectionFromIntegration returns the connection of a single sign-on
// integration (Okta, Entra ID or generic OIDC) with its decrypted client
// secret and the callback URL registered at the provider.
func ConnectionFromIntegration(i *integration.Integration, clientSecret, redirectURL string) (*Connection, error) {
	s, err := i.AuthSettings()
	if err != nil {
		return nil, err
	}
	if err := s.Validate(i.Provider); err != nil {
		return nil, fmt.Errorf("integration %s: %w", i.ID, err)
	}
	c := &Connection{
		ID: i.ID.String(), OrganizationID: i.OrganizationID, ClientID: s.ClientID, ClientSecret: clientSecret,
		RedirectURL: redirectURL, Domains: s.Domains(), DefaultRoleID: s.DefaultRoleID, CreatedAt: i.CreatedAt,
	}
	switch i.Provider {
	case integration.ProviderOkta:
		c.Method, c.Issuer = organization.AuthenticationMethodOkta, OktaIssuer(s.Host, s.OrganizationName)
	case integration.ProviderEntraID:
		c.Method, c.Issuer = organization.AuthenticationMethodEntraID, EntraIssuer(s.Host, s.TenantID)
		c.EmailFallbackClaim = "preferred_username"
	case integration.ProviderOIDC:
		c.Method, c.Issuer = organization.AuthenticationMethodOIDC, s.IssuerURL
		c.Scopes = slices.Clone(s.Scopes)
		if len(c.Scopes) == 0 {
			c.Scopes = slices.Clone(integration.DefaultScopes)
		}
		c.EmailClaim, c.NameClaim, c.TokenAuthMethod = s.EmailClaim, s.NameClaim, s.TokenAuthMethod
		c.AuthParams = maps.Clone(s.AuthorizationParams)
		c.EmailVerification = EmailVerifiedRequired
		if s.AllowUnverifiedEmail {
			c.EmailVerification = EmailVerifiedIgnored
		}
	default:
		return nil, fmt.Errorf("integration %s: provider %s: %w", i.ID, i.Provider, core.ErrInvalidValue)
	}
	return c, nil
}

// IntegrationConnections is the [ConnectionResolver] over the single
// sign-on integrations of every organization.
type IntegrationConnections struct {
	// Tx reads the integrations.
	Tx port.Transactor
	// Secret decrypts the integration's client secret.
	Secret func(ctx context.Context, i *integration.Integration) (string, error)
	// RedirectURL is the callback URL of a method.
	RedirectURL func(method organization.AuthenticationMethod) string
}

var _ ConnectionResolver = IntegrationConnections{}

// ByDomain implements [ConnectionResolver]. Integrations whose settings
// are invalid or whose secret cannot be decrypted are skipped; when none
// is usable, their errors are returned.
func (c IntegrationConnections) ByDomain(ctx context.Context, method organization.AuthenticationMethod, domain string) ([]*Connection, error) {
	p, ok := providerOf(method)
	if !ok {
		return nil, nil
	}
	var is []*integration.Integration
	if err := c.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		is, err = r.Integrations.ListByDomain(ctx, p, domain)
		return err
	}); err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	out := make([]*Connection, 0, len(is))
	var errs []error
	for _, i := range is {
		secret, err := c.Secret(ctx, i)
		if err != nil {
			errs = append(errs, fmt.Errorf("integration %s secret: %w", i.ID, err))
			continue
		}
		conn, err := ConnectionFromIntegration(i, secret, c.RedirectURL(method))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, conn)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}
