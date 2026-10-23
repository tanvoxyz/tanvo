package auth

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CodeEnabledAuthenticationMethodsRequired: deleting the connection would
// leave the organization without a sign-in method (lago
// okta/destroy_service.rb:8).
const CodeEnabledAuthenticationMethodsRequired = "enabled_authentication_methods_required"

// maskedSecretPrefix starts the obfuscated secret the console sends back
// unchanged; such a value keeps the stored secret (lago
// okta/update_service.rb client_secret_update?).
const maskedSecretPrefix = "••••••••…"

// SSOConnection is an organization's single sign-on connection: an Okta
// or Entra ID integration (lago Integrations::OktaIntegration and
// EntraIdIntegration) or a generic OpenID Connect one (a Tanvo
// extension). Its client secret is never returned.
type SSOConnection struct {
	// ID is the integration's ID.
	ID core.IntegrationID
	// OrganizationID owns the connection.
	OrganizationID core.OrganizationID
	// Provider is integration.ProviderOkta, ProviderEntraID or
	// ProviderOIDC.
	Provider integration.Provider
	// Domain is the primary email domain, lower-case.
	Domain string
	// AdditionalDomains are further email domains (Entra ID, generic).
	AdditionalDomains []string
	// ClientID is the OAuth client ID.
	ClientID string
	// OrganizationName is the Okta organization.
	OrganizationName string
	// Host overrides the provider host.
	Host string
	// TenantID is the Entra ID tenant.
	TenantID string
	// DefaultRoleCode is the role of the members a first sign-in creates.
	DefaultRoleCode string
	// Generic holds the generic OIDC settings; zero for the presets.
	Generic GenericOIDC
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// SSOConnectionInput is a connection to create.
type SSOConnectionInput struct {
	// Provider is integration.ProviderOkta, ProviderEntraID or
	// ProviderOIDC.
	Provider integration.Provider
	// Domain is the primary email domain.
	Domain string
	// AdditionalDomains are further email domains (Entra ID and generic).
	AdditionalDomains []string
	// ClientID is the OAuth client ID.
	ClientID string
	// ClientSecret is the OAuth client secret.
	ClientSecret string
	// OrganizationName is the Okta organization (Okta only).
	OrganizationName string
	// Host overrides the provider host.
	Host string
	// TenantID is the Entra ID tenant (Entra ID only).
	TenantID string
	// DefaultRoleCode is the role of the members a first sign-in creates.
	DefaultRoleCode string
	// Generic holds the generic OIDC settings (ProviderOIDC only).
	Generic GenericOIDC
}

// GenericOIDC are the settings of a generic OpenID Connect connection
// ([integration.AuthSettings] has their rules).
type GenericOIDC struct {
	// IssuerURL is the issuer; required.
	IssuerURL string
	// TokenAuthMethod is the token endpoint authentication; "" follows
	// discovery.
	TokenAuthMethod integration.TokenAuthMethod
	// Scopes are the requested scopes; empty is
	// [integration.DefaultScopes].
	Scopes []string
	// EmailClaim is the claim read as the email; "" is "email".
	EmailClaim string
	// NameClaim is the claim read as the name; "" is "name".
	NameClaim string
	// AllowUnverifiedEmail accepts emails not verified by the provider.
	AllowUnverifiedEmail bool
	// AuthorizationParams are extra authorization request parameters.
	AuthorizationParams map[string]string
}

func (g GenericOIDC) apply(s *integration.AuthSettings) {
	s.IssuerURL, s.TokenAuthMethod, s.Scopes = g.IssuerURL, g.TokenAuthMethod, g.Scopes
	s.EmailClaim, s.NameClaim, s.AllowUnverifiedEmail = g.EmailClaim, g.NameClaim, g.AllowUnverifiedEmail
	s.AuthorizationParams = g.AuthorizationParams
}

func genericOf(s integration.AuthSettings) GenericOIDC {
	return GenericOIDC{
		IssuerURL: s.IssuerURL, TokenAuthMethod: s.TokenAuthMethod, Scopes: s.Scopes, EmailClaim: s.EmailClaim,
		NameClaim: s.NameClaim, AllowUnverifiedEmail: s.AllowUnverifiedEmail, AuthorizationParams: s.AuthorizationParams,
	}
}

// SSOConnectionUpdate is a partial update of a connection: only the set
// fields change.
type SSOConnectionUpdate struct {
	// Domain is the primary email domain.
	Domain core.Optional[string]
	// AdditionalDomains are further email domains (Entra ID only).
	AdditionalDomains core.Optional[[]string]
	// ClientID is the OAuth client ID.
	ClientID core.Optional[string]
	// ClientSecret replaces the secret unless blank or the masked value.
	ClientSecret core.Optional[string]
	// OrganizationName is the Okta organization.
	OrganizationName core.Optional[string]
	// Host overrides the provider host.
	Host core.Optional[string]
	// TenantID is the Entra ID tenant.
	TenantID core.Optional[string]
	// DefaultRoleCode is the role of the members a first sign-in creates.
	DefaultRoleCode core.Optional[string]
	// IssuerURL is the generic connection's issuer.
	IssuerURL core.Optional[string]
	// TokenAuthMethod is the generic connection's token endpoint
	// authentication.
	TokenAuthMethod core.Optional[integration.TokenAuthMethod]
	// Scopes are the generic connection's scopes.
	Scopes core.Optional[[]string]
	// EmailClaim is the generic connection's email claim.
	EmailClaim core.Optional[string]
	// NameClaim is the generic connection's name claim.
	NameClaim core.Optional[string]
	// AllowUnverifiedEmail is the generic connection's verification
	// opt-out.
	AllowUnverifiedEmail core.Optional[bool]
	// AuthorizationParams are the generic connection's extra parameters.
	AuthorizationParams core.Optional[map[string]string]
}

// apply sets the fields of u on settings, but the role.
func (u SSOConnectionUpdate) apply(settings *integration.AuthSettings) {
	u.ClientID.Apply(&settings.ClientID)
	u.Domain.Apply(&settings.Domain)
	u.AdditionalDomains.Apply(&settings.AdditionalDomains)
	u.OrganizationName.Apply(&settings.OrganizationName)
	u.Host.Apply(&settings.Host)
	u.TenantID.Apply(&settings.TenantID)
	u.IssuerURL.Apply(&settings.IssuerURL)
	u.TokenAuthMethod.Apply(&settings.TokenAuthMethod)
	u.Scopes.Apply(&settings.Scopes)
	u.EmailClaim.Apply(&settings.EmailClaim)
	u.NameClaim.Apply(&settings.NameClaim)
	u.AllowUnverifiedEmail.Apply(&settings.AllowUnverifiedEmail)
	u.AuthorizationParams.Apply(&settings.AuthorizationParams)
}

// ssoSecrets is the sealed JSON of a connection's secrets (lago
// secrets_accessors :client_secret).
type ssoSecrets struct {
	ClientSecret string `json:"client_secret"`
}

// ssoProviders are the providers of connections, with their lago codes
// and names (okta/create_service.rb:15-16, entra_id/create_service.rb:15-16)
// and the sign-in method they enable.
var ssoProviders = map[integration.Provider]struct {
	code, name string
	method     organization.AuthenticationMethod
}{
	integration.ProviderOkta:    {"okta", "Okta Integration", organization.AuthenticationMethodOkta},
	integration.ProviderEntraID: {"entra_id", "Entra ID Integration", organization.AuthenticationMethodEntraID},
	integration.ProviderOIDC:    {"oidc", "OIDC Integration", organization.AuthenticationMethodOIDC},
}

// IssuerChecker checks a generic connection's issuer against its
// discovery document as the connection is written; [OIDC.CheckIssuer]
// implements it.
type IssuerChecker interface {
	// CheckIssuer reports what would fail at sign-in as field errors.
	CheckIssuer(ctx context.Context, issuer string, m integration.TokenAuthMethod) error
}

// SSOConnections manages the organizations' single sign-on connections,
// stored as integrations whose client secret is sealed by a [SecretBox].
// [IntegrationConnections] reads them back at sign-in.
//
// Lago gates them behind the okta and entra_id premium integrations;
// Tanvo's core offers them to every organization (parity §1.50).
type SSOConnections struct {
	d       Deps
	box     SecretBox
	issuers IssuerChecker
}

// NewSSOConnections returns the service; box seals the client secrets.
func NewSSOConnections(d Deps, box SecretBox) *SSOConnections {
	return &SSOConnections{d: d, box: box}
}

// WithIssuerChecker makes the service fetch a generic connection's
// discovery document when its issuer or token endpoint authentication is
// set, refusing a provider sign-in would fail with; without one the
// issuer is only checked at sign-in.
func (s *SSOConnections) WithIssuerChecker(c IssuerChecker) *SSOConnections {
	s.issuers = c
	return s
}

// checkIssuer checks a generic connection's well-formed issuer, outside
// any transaction since it makes a network request; malformed settings
// are reported by [SSOConnections.write].
func (s *SSOConnections) checkIssuer(ctx context.Context, p integration.Provider, settings integration.AuthSettings) error {
	if p != integration.ProviderOIDC || s.issuers == nil ||
		!integration.ValidIssuerURL(settings.IssuerURL) || !settings.TokenAuthMethod.Valid() {
		return nil
	}
	return s.issuers.CheckIssuer(ctx, settings.IssuerURL, settings.TokenAuthMethod)
}

// Create creates the connection, enables its sign-in method on the
// organization and returns it. An organization has at most one connection
// per provider (code value_already_exist); a domain claimed by a
// connection of the same provider in any organization is
// domain_not_unique.
// A generic connection's issuer is checked against its discovery
// document first (see [SSOConnections.WithIssuerChecker]).
//
// source: app/services/integrations/okta/create_service.rb:7-33
// source: app/services/integrations/entra_id/create_service.rb:7-34
func (s *SSOConnections) Create(ctx context.Context, org core.OrganizationID, in SSOConnectionInput) (*SSOConnection, error) {
	p, ok := ssoProviders[in.Provider]
	if !ok {
		return nil, fieldError("provider", core.CodeInvalid)
	}
	settings := integration.AuthSettings{
		ClientID: in.ClientID, Domain: in.Domain, AdditionalDomains: in.AdditionalDomains,
		OrganizationName: in.OrganizationName, Host: in.Host, TenantID: in.TenantID,
	}
	in.Generic.apply(&settings)
	if err := s.checkIssuer(ctx, in.Provider, settings); err != nil {
		return nil, fmt.Errorf("create sso connection: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	var out *SSOConnection
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		role, err := ssoRole(ctx, r, org, in.DefaultRoleCode)
		if err != nil {
			return err
		}
		i := &integration.Integration{
			ID: core.NewID[core.IntegrationID](s.d.IDs), OrganizationID: org, Code: p.code, Name: p.name,
			Category: integration.CategoryAuth, Provider: in.Provider, CreatedAt: now, UpdatedAt: now,
		}
		settings := settings
		settings.DefaultRoleID = role
		if err := s.write(ctx, r, i, settings, nil, in.ClientSecret); err != nil {
			return err
		}
		if err := r.Integrations.Create(ctx, i); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return fieldError("code", core.CodeAlreadyExists)
			}
			return fmt.Errorf("create integration: %w", err)
		}
		if err := setMethod(ctx, r, org, p.method, true); err != nil {
			return err
		}
		out, err = ssoConnection(ctx, r, i)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create sso connection: %w", err)
	}
	s.d.record(ctx, integrationEvent(org, EventIntegrationCreated, out.Provider, nil))
	return out, nil
}

// Update changes the connection id. A generic connection whose issuer or
// token endpoint authentication changes is checked against its discovery
// document first.
//
// source: app/services/integrations/okta/update_service.rb:14-35
// source: app/services/integrations/entra_id/update_service.rb:14-36
func (s *SSOConnections) Update(ctx context.Context, org core.OrganizationID, id core.IntegrationID, in SSOConnectionUpdate) (*SSOConnection, error) {
	var (
		out  *SSOConnection
		diff map[string]any
	)
	if in.IssuerURL.Set || in.TokenAuthMethod.Set {
		var (
			p        integration.Provider
			settings integration.AuthSettings
		)
		if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			i, err := ssoIntegration(ctx, r, org, id)
			if err != nil {
				return err
			}
			p = i.Provider
			settings, err = i.AuthSettings()
			return err
		}); err != nil {
			return nil, fmt.Errorf("update sso connection %s: %w", id, err)
		}
		in.apply(&settings)
		if err := s.checkIssuer(ctx, p, settings); err != nil {
			return nil, fmt.Errorf("update sso connection %s: %w", id, err)
		}
	}
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		i, err := ssoIntegration(ctx, r, org, id)
		if err != nil {
			return err
		}
		before, err := ssoConnection(ctx, r, i)
		if err != nil {
			return err
		}
		old, err := i.AuthSettings()
		if err != nil {
			return err
		}
		settings := old
		in.apply(&settings)
		if in.DefaultRoleCode.Set {
			if settings.DefaultRoleID, err = ssoRole(ctx, r, org, in.DefaultRoleCode.Value); err != nil {
				return err
			}
		}
		secret := ""
		if v := in.ClientSecret.Value; in.ClientSecret.Set && strings.TrimSpace(v) != "" && !strings.HasPrefix(v, maskedSecretPrefix) {
			secret = v
		}
		if err := s.write(ctx, r, i, settings, &old, secret); err != nil {
			return err
		}
		i.UpdatedAt = s.d.Clock.Now().UTC()
		if err := r.Integrations.Update(ctx, i); err != nil {
			return fmt.Errorf("update integration: %w", err)
		}
		if out, err = ssoConnection(ctx, r, i); err != nil {
			return err
		}
		diff = connectionDiff(before, out)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update sso connection %s: %w", id, err)
	}
	s.d.record(ctx, integrationEvent(org, EventIntegrationUpdated, out.Provider, diff))
	return out, nil
}

// Delete deletes the connection id and disables its sign-in method on the
// organization; it is refused (enabled_authentication_methods_required)
// when that method is the organization's only one.
//
// source: app/services/integrations/okta/destroy_service.rb:6-27
// source: app/services/integrations/entra_id/destroy_service.rb:6-27
func (s *SSOConnections) Delete(ctx context.Context, org core.OrganizationID, id core.IntegrationID) error {
	var provider integration.Provider
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		i, err := ssoIntegration(ctx, r, org, id)
		if err != nil {
			return err
		}
		provider = i.Provider
		method := ssoProviders[i.Provider].method
		o, err := r.Organizations.Get(ctx, org)
		if err != nil {
			return fmt.Errorf("load organization: %w", err)
		}
		if !slices.ContainsFunc(o.AuthenticationMethods, func(m organization.AuthenticationMethod) bool { return m != method }) {
			return rule(CodeEnabledAuthenticationMethodsRequired)
		}
		if err := r.Integrations.Delete(ctx, org, id); err != nil {
			return fmt.Errorf("delete integration: %w", err)
		}
		return setMethod(ctx, r, org, method, false)
	})
	if err != nil {
		return fmt.Errorf("delete sso connection %s: %w", id, err)
	}
	s.d.record(ctx, integrationEvent(org, EventIntegrationDeleted, provider, nil))
	return nil
}

// Get returns the connection id.
func (s *SSOConnections) Get(ctx context.Context, org core.OrganizationID, id core.IntegrationID) (*SSOConnection, error) {
	var out *SSOConnection
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		i, err := ssoIntegration(ctx, r, org, id)
		if err != nil {
			return err
		}
		out, err = ssoConnection(ctx, r, i)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("get sso connection %s: %w", id, err)
	}
	return out, nil
}

// List returns the organization's connections (at most one per provider)
// in creation order.
func (s *SSOConnections) List(ctx context.Context, org core.OrganizationID) ([]*SSOConnection, error) {
	var out []*SSOConnection
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var all []*integration.Integration
		for _, p := range []integration.Provider{integration.ProviderOkta, integration.ProviderEntraID, integration.ProviderOIDC} {
			is, err := r.Integrations.ListByProvider(ctx, org, p)
			if err != nil {
				return fmt.Errorf("list integrations: %w", err)
			}
			all = append(all, is...)
		}
		slices.SortStableFunc(all, func(a, b *integration.Integration) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), a.ID.Compare(b.ID))
		})
		for _, i := range all {
			c, err := ssoConnection(ctx, r, i)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list sso connections: %w", err)
	}
	return out, nil
}

// Secret returns the decrypted client secret of i, for
// [IntegrationConnections.Secret].
func (s *SSOConnections) Secret(ctx context.Context, i *integration.Integration) (string, error) {
	plain, err := s.box.Open(ctx, i.OrganizationID, i.Secrets)
	if err != nil {
		return "", err
	}
	var sec ssoSecrets
	if err := json.Unmarshal(plain, &sec); err != nil {
		return "", fmt.Errorf("integration %s secrets: %w", i.ID, ErrSecretUndecryptable)
	}
	return sec.ClientSecret, nil
}

// write validates settings (old are the stored ones on update) and stores
// them, with secret sealed when not empty, on i. A domain newly claimed
// that a connection of the same provider claims is domain_not_unique
// (lago domain_uniqueness).
func (s *SSOConnections) write(ctx context.Context, r port.Repos, i *integration.Integration, settings integration.AuthSettings, old *integration.AuthSettings, secret string) error {
	settings.Domain = strings.ToLower(strings.TrimSpace(settings.Domain))
	settings.AdditionalDomains = integration.NormalizeDomains(settings.AdditionalDomains)
	var v core.Validator
	v.Merge("", settings.Validate(i.Provider))
	if old == nil {
		v.Required("client_secret", secret)
	}
	var before []string
	if old != nil {
		before = old.Domains()
	}
	for n, d := range settings.Domains() {
		if slices.Contains(before, d) {
			continue
		}
		others, err := r.Integrations.ListByDomain(ctx, i.Provider, d)
		if err != nil {
			return fmt.Errorf("check domain %s: %w", d, err)
		}
		if slices.ContainsFunc(others, func(o *integration.Integration) bool { return o.ID != i.ID }) {
			field := "additional_domains"
			if n == 0 && d == settings.Domain {
				field = "domain"
			}
			v.Add(field, CodeDomainNotUnique)
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	if err := i.SetAuthSettings(settings); err != nil {
		return err
	}
	if secret != "" {
		plain, err := json.Marshal(ssoSecrets{ClientSecret: secret})
		if err != nil {
			return fmt.Errorf("encode secrets: %w", err)
		}
		if i.Secrets, err = s.box.Seal(ctx, i.OrganizationID, plain); err != nil {
			return err
		}
	}
	return nil
}

// ssoIntegration loads the connection id: an Okta or Entra ID integration
// of org.
func ssoIntegration(ctx context.Context, r port.Repos, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, error) {
	i, err := r.Integrations.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, notFound("integration")
	}
	if err != nil {
		return nil, fmt.Errorf("load integration: %w", err)
	}
	if _, ok := ssoProviders[i.Provider]; !ok {
		return nil, notFound("integration")
	}
	return i, nil
}

// ssoRole resolves the default role code.
func ssoRole(ctx context.Context, r port.Repos, org core.OrganizationID, code string) (core.RoleID, error) {
	if strings.TrimSpace(code) == "" {
		return core.RoleID{}, fieldError("default_role_code", core.CodeRequired)
	}
	role, err := r.Roles.GetByCode(ctx, org, code)
	if errors.Is(err, core.ErrNotFound) {
		return core.RoleID{}, fieldError("default_role_code", CodeInvalidRole)
	}
	if err != nil {
		return core.RoleID{}, fmt.Errorf("load role: %w", err)
	}
	return role.ID, nil
}

// ssoConnection returns the connection view of i.
func ssoConnection(ctx context.Context, r port.Repos, i *integration.Integration) (*SSOConnection, error) {
	st, err := i.AuthSettings()
	if err != nil {
		return nil, err
	}
	c := &SSOConnection{
		ID: i.ID, OrganizationID: i.OrganizationID, Provider: i.Provider, Domain: st.Domain,
		AdditionalDomains: st.AdditionalDomains, ClientID: st.ClientID, OrganizationName: st.OrganizationName,
		Host: st.Host, TenantID: st.TenantID, Generic: genericOf(st), CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
	}
	if !st.DefaultRoleID.IsZero() {
		role, err := r.Roles.Get(ctx, i.OrganizationID, st.DefaultRoleID)
		switch {
		case err == nil:
			c.DefaultRoleCode = role.Code
		case !errors.Is(err, core.ErrNotFound):
			return nil, fmt.Errorf("load role: %w", err)
		}
	}
	return c, nil
}

// setMethod enables or disables the sign-in method m on org (lago
// enable_okta_authentication!, disable_okta_authentication!).
func setMethod(ctx context.Context, r port.Repos, org core.OrganizationID, m organization.AuthenticationMethod, on bool) error {
	o, err := r.Organizations.Get(ctx, org)
	if err != nil {
		return fmt.Errorf("load organization: %w", err)
	}
	has := slices.Contains(o.AuthenticationMethods, m)
	switch {
	case on && !has:
		o.AuthenticationMethods = append(o.AuthenticationMethods, m)
	case !on && has:
		o.AuthenticationMethods = slices.DeleteFunc(o.AuthenticationMethods, func(x organization.AuthenticationMethod) bool { return x == m })
	default:
		return nil
	}
	if err := r.Organizations.Update(ctx, o); err != nil {
		return fmt.Errorf("update organization: %w", err)
	}
	return nil
}

// integrationEvent is the security log entry of a connection change; diff
// holds the changed settings of an update.
func integrationEvent(org core.OrganizationID, event string, p integration.Provider, diff map[string]any) SecurityEvent {
	res := map[string]any{"integration_name": ssoProviders[p].name, "integration_type": ssoProviders[p].code}
	maps.Copy(res, diff)
	return SecurityEvent{OrganizationID: org, Type: SecurityLogIntegration, Event: event, Resources: res}
}

// connectionDiff returns the settings that differ between a and b, each
// as {deleted, added} without the empty side (lago
// integrations/update_service.rb register_security_log).
func connectionDiff(a, b *SSOConnection) map[string]any {
	out := map[string]any{}
	change := func(k string, x, y any, xEmpty, yEmpty bool) {
		d := map[string]any{}
		if !xEmpty {
			d["deleted"] = x
		}
		if !yEmpty {
			d["added"] = y
		}
		out[k] = d
	}
	for _, f := range []struct{ k, x, y string }{
		{"client_id", a.ClientID, b.ClientID},
		{"domain", a.Domain, b.Domain},
		{"organization_name", a.OrganizationName, b.OrganizationName},
		{"host", a.Host, b.Host},
		{"tenant_id", a.TenantID, b.TenantID},
		{"default_role_code", a.DefaultRoleCode, b.DefaultRoleCode},
		{"issuer_url", a.Generic.IssuerURL, b.Generic.IssuerURL},
		{"token_endpoint_auth_method", string(a.Generic.TokenAuthMethod), string(b.Generic.TokenAuthMethod)},
		{"email_claim", a.Generic.EmailClaim, b.Generic.EmailClaim},
		{"name_claim", a.Generic.NameClaim, b.Generic.NameClaim},
	} {
		if f.x != f.y {
			change(f.k, f.x, f.y, f.x == "", f.y == "")
		}
	}
	if !slices.Equal(a.AdditionalDomains, b.AdditionalDomains) {
		change("additional_domains", a.AdditionalDomains, b.AdditionalDomains, len(a.AdditionalDomains) == 0, len(b.AdditionalDomains) == 0)
	}
	if !slices.Equal(a.Generic.Scopes, b.Generic.Scopes) {
		change("scopes", a.Generic.Scopes, b.Generic.Scopes, len(a.Generic.Scopes) == 0, len(b.Generic.Scopes) == 0)
	}
	if a.Generic.AllowUnverifiedEmail != b.Generic.AllowUnverifiedEmail {
		change("allow_unverified_email", a.Generic.AllowUnverifiedEmail, b.Generic.AllowUnverifiedEmail, false, false)
	}
	if !maps.Equal(a.Generic.AuthorizationParams, b.Generic.AuthorizationParams) {
		change("authorization_params", a.Generic.AuthorizationParams, b.Generic.AuthorizationParams,
			len(a.Generic.AuthorizationParams) == 0, len(b.Generic.AuthorizationParams) == 0)
	}
	return out
}
