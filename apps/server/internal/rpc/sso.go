package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1/accessv1connect"
)

// SsoConnections serves tanvo.access.v1.SsoConnectionService over the
// organizations' Okta and Entra ID integrations (lago
// Integrations::Okta and Integrations::EntraId services) and generic
// OpenID Connect ones (a Tanvo extension). The client secret is write
// only: no response carries it.
type SsoConnections struct {
	accessv1connect.UnimplementedSsoConnectionServiceHandler

	svc *auth.SSOConnections
}

var _ accessv1connect.SsoConnectionServiceHandler = (*SsoConnections)(nil)

// NewSsoConnections returns the service over svc.
func NewSsoConnections(svc *auth.SSOConnections) *SsoConnections { return &SsoConnections{svc: svc} }

// ssoProviders maps the providers: the Okta and Entra ID presets and
// generic OIDC.
var ssoProviders = NewEnum(map[integration.Provider]accessv1.SsoProvider{
	integration.ProviderOkta:    accessv1.SsoProvider_SSO_PROVIDER_OKTA,
	integration.ProviderEntraID: accessv1.SsoProvider_SSO_PROVIDER_ENTRA_ID,
	integration.ProviderOIDC:    accessv1.SsoProvider_SSO_PROVIDER_OIDC,
})

// tokenAuthMethods maps the token endpoint authentication methods; ""
// follows discovery.
var tokenAuthMethods = NewEnum(map[integration.TokenAuthMethod]accessv1.TokenEndpointAuthMethod{
	integration.TokenAuthClientSecretBasic: accessv1.TokenEndpointAuthMethod_TOKEN_ENDPOINT_AUTH_METHOD_CLIENT_SECRET_BASIC,
	integration.TokenAuthClientSecretPost:  accessv1.TokenEndpointAuthMethod_TOKEN_ENDPOINT_AUTH_METHOD_CLIENT_SECRET_POST,
})

// ssoUpdateFields are the update_mask paths of UpdateSsoConnection.
var ssoUpdateFields = []string{
	"domain", "additional_domains", "client_id", "client_secret", "organization_name", "host", "tenant_id", "default_role_code",
	"issuer_url", "token_endpoint_auth_method", "scopes", "email_claim", "name_claim", "require_verified_email", "authorization_params",
}

// ssoGeneric reads the generic OIDC settings of c; an unset
// require_verified_email is true.
func ssoGeneric(c *accessv1.SsoConnection) auth.GenericOIDC {
	return auth.GenericOIDC{
		IssuerURL: c.GetIssuerUrl(), TokenAuthMethod: tokenAuthMethods.Core(c.GetTokenEndpointAuthMethod()),
		Scopes: c.GetScopes(), EmailClaim: c.GetEmailClaim(), NameClaim: c.GetNameClaim(),
		AllowUnverifiedEmail: c.HasRequireVerifiedEmail() && !c.GetRequireVerifiedEmail(),
		AuthorizationParams:  c.GetAuthorizationParams(),
	}
}

// CreateSsoConnection implements SsoConnectionService.CreateSsoConnection.
func (s *SsoConnections) CreateSsoConnection(ctx context.Context, req *accessv1.CreateSsoConnectionRequest) (*accessv1.CreateSsoConnectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c := req.GetSsoConnection()
	out, err := s.svc.Create(ctx, p.OrganizationID, auth.SSOConnectionInput{
		Provider: ssoProviders.Core(c.GetProvider()), Domain: c.GetDomain(), AdditionalDomains: c.GetAdditionalDomains(),
		ClientID: c.GetClientId(), ClientSecret: c.GetClientSecret(), OrganizationName: c.GetOrganizationName(),
		Host: c.GetHost(), TenantID: c.GetTenantId(), DefaultRoleCode: c.GetDefaultRoleCode(), Generic: ssoGeneric(c),
	})
	if err != nil {
		return nil, ssoError(ctx, err)
	}
	return accessv1.CreateSsoConnectionResponse_builder{SsoConnection: ssoConnection(out)}.Build(), nil
}

// GetSsoConnection implements SsoConnectionService.GetSsoConnection.
func (s *SsoConnections) GetSsoConnection(ctx context.Context, req *accessv1.GetSsoConnectionRequest) (*accessv1.GetSsoConnectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.IntegrationID]("sso_connection_id", req.GetSsoConnectionId())
	if err != nil {
		return nil, err
	}
	out, err := s.svc.Get(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.GetSsoConnectionResponse_builder{SsoConnection: ssoConnection(out)}.Build(), nil
}

// ListSsoConnections implements SsoConnectionService.ListSsoConnections.
// An organization has at most one connection per provider, so every
// connection is on the first page.
func (s *SsoConnections) ListSsoConnections(ctx context.Context, _ *accessv1.ListSsoConnectionsRequest) (*accessv1.ListSsoConnectionsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.svc.List(ctx, p.OrganizationID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*accessv1.SsoConnection, len(cs))
	for i, c := range cs {
		out[i] = ssoConnection(c)
	}
	return accessv1.ListSsoConnectionsResponse_builder{SsoConnections: out}.Build(), nil
}

// UpdateSsoConnection implements SsoConnectionService.UpdateSsoConnection:
// the fields updated are the update_mask paths or, without one, the
// fields set. A blank or masked client_secret keeps the stored one (lago
// client_secret_update?).
func (s *SsoConnections) UpdateSsoConnection(ctx context.Context, req *accessv1.UpdateSsoConnectionRequest) (*accessv1.UpdateSsoConnectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c := req.GetSsoConnection()
	id, err := ParseID[core.IntegrationID]("sso_connection.id", c.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), ssoUpdateFields, func(path string) bool {
		switch path {
		case "domain":
			return c.HasDomain()
		case "additional_domains":
			return len(c.GetAdditionalDomains()) > 0
		case "client_id":
			return c.HasClientId()
		case "client_secret":
			return c.HasClientSecret()
		case "organization_name":
			return c.HasOrganizationName()
		case "host":
			return c.HasHost()
		case "tenant_id":
			return c.HasTenantId()
		case "default_role_code":
			return c.HasDefaultRoleCode()
		case "issuer_url":
			return c.HasIssuerUrl()
		case "token_endpoint_auth_method":
			return c.HasTokenEndpointAuthMethod()
		case "scopes":
			return len(c.GetScopes()) > 0
		case "email_claim":
			return c.HasEmailClaim()
		case "name_claim":
			return c.HasNameClaim()
		case "require_verified_email":
			return c.HasRequireVerifiedEmail()
		case "authorization_params":
			return len(c.GetAuthorizationParams()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var in auth.SSOConnectionUpdate
	for _, path := range paths {
		switch path {
		case "domain":
			in.Domain = core.Some(c.GetDomain())
		case "additional_domains":
			in.AdditionalDomains = core.Some(c.GetAdditionalDomains())
		case "client_id":
			in.ClientID = core.Some(c.GetClientId())
		case "client_secret":
			in.ClientSecret = core.Some(c.GetClientSecret())
		case "organization_name":
			in.OrganizationName = core.Some(c.GetOrganizationName())
		case "host":
			in.Host = core.Some(c.GetHost())
		case "tenant_id":
			in.TenantID = core.Some(c.GetTenantId())
		case "default_role_code":
			in.DefaultRoleCode = core.Some(c.GetDefaultRoleCode())
		case "issuer_url":
			in.IssuerURL = core.Some(c.GetIssuerUrl())
		case "token_endpoint_auth_method":
			in.TokenAuthMethod = core.Some(tokenAuthMethods.Core(c.GetTokenEndpointAuthMethod()))
		case "scopes":
			in.Scopes = core.Some(c.GetScopes())
		case "email_claim":
			in.EmailClaim = core.Some(c.GetEmailClaim())
		case "name_claim":
			in.NameClaim = core.Some(c.GetNameClaim())
		case "require_verified_email":
			in.AllowUnverifiedEmail = core.Some(!c.GetRequireVerifiedEmail())
		case "authorization_params":
			in.AuthorizationParams = core.Some(c.GetAuthorizationParams())
		}
	}
	out, err := s.svc.Update(ctx, p.OrganizationID, id, in)
	if err != nil {
		return nil, ssoError(ctx, err)
	}
	return accessv1.UpdateSsoConnectionResponse_builder{SsoConnection: ssoConnection(out)}.Build(), nil
}

// DeleteSsoConnection implements SsoConnectionService.DeleteSsoConnection.
func (s *SsoConnections) DeleteSsoConnection(ctx context.Context, req *accessv1.DeleteSsoConnectionRequest) (*accessv1.DeleteSsoConnectionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.IntegrationID]("sso_connection_id", req.GetSsoConnectionId())
	if err != nil {
		return nil, err
	}
	if err := s.svc.Delete(ctx, p.OrganizationID, id); err != nil {
		return nil, Error(ctx, err)
	}
	return &accessv1.DeleteSsoConnectionResponse{}, nil
}

// ssoError maps the service errors, whose field paths are the
// connection's, onto the request's sso_connection message.
func ssoError(ctx context.Context, err error) error {
	if v, ok := validationViolations("sso_connection.", err); ok {
		return withReason(InvalidArgument("invalid sso connection", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}

func ssoConnection(c *auth.SSOConnection) *accessv1.SsoConnection {
	b := accessv1.SsoConnection_builder{
		Id:                new(c.ID.String()),
		Provider:          new(ssoProviders.Proto(c.Provider)),
		Domain:            new(c.Domain),
		AdditionalDomains: c.AdditionalDomains,
		ClientId:          new(c.ClientID),
		TenantId:          StringPtr(c.TenantID),
		OrganizationName:  StringPtr(c.OrganizationName),
		Host:              StringPtr(c.Host),
		DefaultRoleCode:   StringPtr(c.DefaultRoleCode),
		CreatedAt:         Timestamp(c.CreatedAt),
		UpdatedAt:         Timestamp(c.UpdatedAt),
	}
	if c.Provider == integration.ProviderOIDC {
		g := c.Generic
		b.IssuerUrl = new(g.IssuerURL)
		if g.TokenAuthMethod != "" {
			b.TokenEndpointAuthMethod = new(tokenAuthMethods.Proto(g.TokenAuthMethod))
		}
		b.Scopes, b.AuthorizationParams = g.Scopes, g.AuthorizationParams
		b.EmailClaim, b.NameClaim = StringPtr(g.EmailClaim), StringPtr(g.NameClaim)
		b.RequireVerifiedEmail = new(!g.AllowUnverifiedEmail)
	}
	return b.Build()
}
