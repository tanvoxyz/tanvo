package rpc

import (
	"context"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	organizationv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1/organizationv1connect"
)

// Organizations serves tanvo.organization.v1.OrganizationService over the
// organization settings (lago Api::V1::OrganizationsController and the
// GraphQL updateOrganization mutation).
type Organizations struct {
	organizationv1connect.UnimplementedOrganizationServiceHandler

	svc *auth.OrganizationSettings
}

var _ organizationv1connect.OrganizationServiceHandler = (*Organizations)(nil)

// NewOrganizations returns the service over svc.
func NewOrganizations(svc *auth.OrganizationSettings) *Organizations {
	return &Organizations{svc: svc}
}

var authenticationMethods = NewEnum(map[organization.AuthenticationMethod]organizationv1.AuthenticationMethod{
	organization.AuthenticationMethodEmailPassword: organizationv1.AuthenticationMethod_AUTHENTICATION_METHOD_EMAIL_PASSWORD,
	organization.AuthenticationMethodGoogleOAuth:   organizationv1.AuthenticationMethod_AUTHENTICATION_METHOD_GOOGLE_OAUTH,
	organization.AuthenticationMethodOkta:          organizationv1.AuthenticationMethod_AUTHENTICATION_METHOD_OKTA,
	organization.AuthenticationMethodEntraID:       organizationv1.AuthenticationMethod_AUTHENTICATION_METHOD_ENTRA_ID,
	organization.AuthenticationMethodOIDC:          organizationv1.AuthenticationMethod_AUTHENTICATION_METHOD_OIDC,
})

// organizationAddressFields are the update_mask paths of the address's
// fields.
var organizationAddressFields = []string{
	"address.line1", "address.line2", "address.city", "address.state", "address.zipcode", "address.country",
}

// organizationUpdateFields are the update_mask paths of UpdateOrganization;
// "address" stands for every address field.
var organizationUpdateFields = append([]string{"slug", "email", "address", "logo", "authentication_methods", "document_number_prefix"}, organizationAddressFields...)

// GetOrganization implements OrganizationService.GetOrganization.
func (s *Organizations) GetOrganization(ctx context.Context, _ *organizationv1.GetOrganizationRequest) (*organizationv1.GetOrganizationResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	o, err := s.svc.Get(ctx, p.OrganizationID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	b, err := s.proto(ctx, o)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return organizationv1.GetOrganizationResponse_builder{Organization: b}.Build(), nil
}

// UpdateOrganization implements OrganizationService.UpdateOrganization
// (lago Organizations::UpdateService). Changing the authentication methods
// needs a user holding authentication_methods:update (lago
// app/graphql/mutations/organizations/update.rb; the REST API does not
// take them).
func (s *Organizations) UpdateOrganization(ctx context.Context, req *organizationv1.UpdateOrganizationRequest) (*organizationv1.UpdateOrganizationResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetOrganization()
	paths, err := UpdatePaths(req.GetUpdateMask(), organizationUpdateFields, func(path string) bool {
		switch path {
		case "slug":
			return b.HasSlug()
		case "email":
			return b.HasEmail()
		case "address":
			return b.HasAddress()
		case "logo":
			return b.HasLogo()
		case "document_number_prefix":
			return b.HasDocumentNumberPrefix()
		case "authentication_methods":
			return len(b.GetAuthenticationMethods()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var u auth.OrganizationUpdate
	var addressPaths []string
	for _, path := range paths {
		switch path {
		case "slug":
			u.Slug = core.Some(b.GetSlug())
		case "email":
			u.Email = core.Some(b.GetEmail())
		case "logo":
			u.Logo = core.Some(b.GetLogo())
		case "document_number_prefix":
			u.DocumentNumberPrefix = core.Some(b.GetDocumentNumberPrefix())
		case "authentication_methods":
			if p.Kind != auth.PrincipalUser || !access.AnyGrants(p.Roles, permission.Permission("authentication_methods:update")) {
				return nil, newError(connect.CodePermissionDenied, ReasonForbidden, "changing the authentication methods needs the authentication_methods:update permission")
			}
			methods := make([]organization.AuthenticationMethod, 0, len(b.GetAuthenticationMethods()))
			for _, m := range b.GetAuthenticationMethods() {
				methods = append(methods, authenticationMethods.Core(m))
			}
			u.AuthenticationMethods = core.Some(methods)
		case "address":
			addressPaths = append(addressPaths, organizationAddressFields...)
		default:
			addressPaths = append(addressPaths, path)
		}
	}
	if len(addressPaths) > 0 {
		cur, err := s.svc.Get(ctx, p.OrganizationID)
		if err != nil {
			return nil, Error(ctx, err)
		}
		a, in := cur.Address, b.GetAddress()
		for _, path := range addressPaths {
			switch path {
			case "address.line1":
				a.Line1 = in.GetLine1()
			case "address.line2":
				a.Line2 = in.GetLine2()
			case "address.city":
				a.City = in.GetCity()
			case "address.state":
				a.State = in.GetState()
			case "address.zipcode":
				a.Zipcode = in.GetZipcode()
			case "address.country":
				a.Country = in.GetCountry()
			}
		}
		u.Address = core.Some(a)
	}
	o, err := s.svc.Update(ctx, p.OrganizationID, actorID(p), u)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.proto(ctx, o)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return organizationv1.UpdateOrganizationResponse_builder{Organization: out}.Build(), nil
}

func (s *Organizations) proto(ctx context.Context, o *organization.Organization) (*organizationv1.Organization, error) {
	logo, err := s.svc.LogoURL(ctx, o)
	if err != nil {
		return nil, err
	}
	methods := make([]organizationv1.AuthenticationMethod, 0, len(o.AuthenticationMethods))
	for _, m := range o.AuthenticationMethods {
		methods = append(methods, authenticationMethods.Proto(m))
	}
	retention := int32(o.AuditLogsRetentionDays) //nolint:gosec // a day count
	wallets := int32(o.MaxWalletsPerCustomer)    //nolint:gosec // a wallet count
	return organizationv1.Organization_builder{
		Id:                     new(o.ID.String()),
		Name:                   &o.Name,
		Slug:                   &o.Slug,
		Email:                  o.Email,
		Address:                beAddress(o.Address),
		LogoUrl:                StringPtr(logo),
		AuthenticationMethods:  methods,
		AuditLogsRetentionDays: &retention,
		MaxWalletsPerCustomer:  &wallets,
		CreatedAt:              Timestamp(o.CreatedAt),
		UpdatedAt:              Timestamp(o.UpdatedAt),
		DocumentNumberPrefix:   &o.DocumentNumberPrefix,
	}.Build(), nil
}

// ServerInfo describes the running server.
type ServerInfo struct {
	// Version is the server's version.
	Version string
	// Revision is the source revision.
	Revision string
}

// Server serves tanvo.organization.v1.ServerService (lago GraphQL
// currentVersion, app/graphql/resolvers/version_resolver.rb).
type Server struct {
	organizationv1connect.UnimplementedServerServiceHandler

	info ServerInfo
}

var _ organizationv1connect.ServerServiceHandler = (*Server)(nil)

// NewServer returns the service reporting info.
func NewServer(info ServerInfo) *Server {
	return &Server{info: info}
}

// GetServerInfo implements ServerService.GetServerInfo.
func (s *Server) GetServerInfo(context.Context, *organizationv1.GetServerInfoRequest) (*organizationv1.GetServerInfoResponse, error) {
	return organizationv1.GetServerInfoResponse_builder{
		Version:  &s.info.Version,
		Revision: StringPtr(s.info.Revision),
	}.Build(), nil
}
