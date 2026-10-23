package rpc

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/clientip"
	"github.com/tanvoxyz/tanvo/apps/server/internal/useragent"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1/accessv1connect"
)

// AuthDeps are the services behind [AuthService].
type AuthDeps struct {
	// Users registers users and signs them in.
	Users *auth.Users
	// Invites looks invites up and accepts them.
	Invites *auth.Invites
	// Resets requests and performs password resets.
	Resets *auth.PasswordResets
	// OIDC runs the OpenID Connect sign-ins.
	OIDC *auth.OIDC
	// Sessions revokes the session of a sign-out.
	Sessions *auth.Sessions
	// IPs resolves the client address recorded with a session.
	IPs *clientip.Resolver
	// RedirectURLs are the callback URLs of the OpenID Connect methods
	// (Google: AppURL + "/auth/google/callback"); a start request's
	// redirect_uri must be the one of a method of its provider.
	RedirectURLs map[organization.AuthenticationMethod]string
}

// AuthService serves tanvo.access.v1.AuthService: sign-up, the sign-ins,
// invites, password resets, the current user and sign-out. The SSO
// connection RPCs answer Unimplemented.
type AuthService struct {
	accessv1connect.UnimplementedAuthServiceHandler

	d AuthDeps
}

var _ accessv1connect.AuthServiceHandler = (*AuthService)(nil)

// NewAuthService returns the authentication service over d.
func NewAuthService(d AuthDeps) *AuthService { return &AuthService{d: d} }

// ssoMethods are the methods of OIDC_PROVIDER_SSO, tried in order: the
// first with a connection for the email's domain is used.
var ssoMethods = []organization.AuthenticationMethod{
	organization.AuthenticationMethodOkta,
	organization.AuthenticationMethodEntraID,
	organization.AuthenticationMethodOIDC,
}

var oidcIntents = NewEnum(map[access.OIDCIntent]accessv1.OidcIntent{
	access.OIDCIntentLogin:        accessv1.OidcIntent_OIDC_INTENT_SIGN_IN,
	access.OIDCIntentSignUp:       accessv1.OidcIntent_OIDC_INTENT_SIGN_UP,
	access.OIDCIntentAcceptInvite: accessv1.OidcIntent_OIDC_INTENT_ACCEPT_INVITE,
})

// SignUp implements AuthService.SignUp (lago RegisterUser).
func (s *AuthService) SignUp(ctx context.Context, req *accessv1.SignUpRequest) (*accessv1.SignUpResponse, error) {
	in, err := s.d.Users.Register(ctx, req.GetEmail(), req.GetPassword(), req.GetOrganizationName(), s.meta(ctx))
	if err != nil {
		return nil, Error(ctx, err)
	}
	org, err := s.joined(ctx, in)
	if err != nil {
		return nil, err
	}
	return accessv1.SignUpResponse_builder{Session: session(in), User: user(in.User), Organization: org}.Build(), nil
}

// SignInWithPassword implements AuthService.SignInWithPassword (lago
// LoginUser).
func (s *AuthService) SignInWithPassword(ctx context.Context, req *accessv1.SignInWithPasswordRequest) (*accessv1.SignInWithPasswordResponse, error) {
	in, err := s.d.Users.Login(ctx, req.GetEmail(), req.GetPassword(), s.meta(ctx))
	if err != nil {
		return nil, Error(ctx, err)
	}
	orgs, err := s.organizations(ctx, in.User.ID, organization.AuthenticationMethodEmailPassword)
	if err != nil {
		return nil, err
	}
	return accessv1.SignInWithPasswordResponse_builder{Session: session(in), User: user(in.User), Organizations: orgs}.Build(), nil
}

// StartOidcSignIn implements AuthService.StartOidcSignIn (lago
// GoogleService#authorize_url, Okta and EntraId AuthorizeService).
func (s *AuthService) StartOidcSignIn(ctx context.Context, req *accessv1.StartOidcSignInRequest) (*accessv1.StartOidcSignInResponse, error) {
	methods := ssoMethods
	if req.GetProvider() == accessv1.OidcProvider_OIDC_PROVIDER_GOOGLE {
		methods = []organization.AuthenticationMethod{organization.AuthenticationMethodGoogleOAuth}
	}
	allowed := false
	for _, m := range methods {
		if u := s.d.RedirectURLs[m]; u != "" && u == req.GetRedirectUri() {
			allowed = true
		}
	}
	if !allowed {
		return nil, InvalidArgument("invalid redirect_uri", []FieldViolation{{
			Field: "redirect_uri", Reason: core.CodeInvalid, Description: "must be a callback URL of the provider",
		}})
	}
	in := auth.BeginRequest{
		Intent:           oidcIntents.Core(req.GetIntent()),
		Email:            req.GetEmail(),
		InviteToken:      req.GetInviteToken(),
		OrganizationName: req.GetOrganizationName(),
	}
	var err error
	for _, m := range methods {
		in.Method = m
		var u string
		u, err = s.d.OIDC.Begin(ctx, in)
		if err == nil {
			return accessv1.StartOidcSignInResponse_builder{AuthorizationUrl: &u}.Build(), nil
		}
		if auth.ErrorCode(err) != auth.CodeDomainNotConfigured {
			break
		}
	}
	return nil, Error(ctx, err)
}

// CompleteOidcSignIn implements AuthService.CompleteOidcSignIn (lago
// GoogleService#login/register_user/accept_invite, Okta and EntraId
// LoginService and AcceptInviteService).
func (s *AuthService) CompleteOidcSignIn(ctx context.Context, req *accessv1.CompleteOidcSignInRequest) (*accessv1.CompleteOidcSignInResponse, error) {
	in, err := s.d.OIDC.Complete(ctx, auth.CallbackRequest{State: req.GetState(), Code: req.GetCode(), Meta: s.meta(ctx)})
	if err != nil {
		return nil, Error(ctx, err)
	}
	orgs, err := s.organizations(ctx, in.User.ID, in.Session.Method)
	if err != nil {
		return nil, err
	}
	return accessv1.CompleteOidcSignInResponse_builder{Session: session(in), User: user(in.User), Organizations: orgs}.Build(), nil
}

// LookupInvite implements AuthService.LookupInvite (lago invite query).
func (s *AuthService) LookupInvite(ctx context.Context, req *accessv1.LookupInviteRequest) (*accessv1.LookupInviteResponse, error) {
	inv, err := s.d.Invites.Lookup(ctx, req.GetToken())
	if err != nil {
		return nil, Error(ctx, err)
	}
	org, err := s.d.Invites.InviteOrganization(ctx, inv)
	if err != nil {
		return nil, Error(ctx, err)
	}
	methods := make([]string, len(org.AuthenticationMethods))
	for i, m := range org.AuthenticationMethods {
		methods[i] = string(m)
	}
	return accessv1.LookupInviteResponse_builder{
		Email: &inv.Email, OrganizationName: &org.Name, AuthenticationMethods: methods,
	}.Build(), nil
}

// AcceptInvite implements AuthService.AcceptInvite (lago AcceptInvite).
func (s *AuthService) AcceptInvite(ctx context.Context, req *accessv1.AcceptInviteRequest) (*accessv1.AcceptInviteResponse, error) {
	in, err := s.d.Invites.Accept(ctx, req.GetToken(), req.GetPassword(), s.meta(ctx))
	if err != nil {
		return nil, Error(ctx, err)
	}
	org, err := s.joined(ctx, in)
	if err != nil {
		return nil, err
	}
	return accessv1.AcceptInviteResponse_builder{Session: session(in), User: user(in.User), Organization: org}.Build(), nil
}

// RequestPasswordReset implements AuthService.RequestPasswordReset (lago
// CreatePasswordReset). An unknown email succeeds too, so the RPC does not
// tell which addresses have accounts; lago fails with user not_found, which
// lets anyone enumerate the accounts.
func (s *AuthService) RequestPasswordReset(ctx context.Context, req *accessv1.RequestPasswordResetRequest) (*accessv1.RequestPasswordResetResponse, error) {
	if _, err := s.d.Resets.Request(ctx, req.GetEmail()); err != nil && !errors.Is(err, core.ErrNotFound) {
		return nil, Error(ctx, err)
	}
	return &accessv1.RequestPasswordResetResponse{}, nil
}

// ResetPassword implements AuthService.ResetPassword (lago ResetPassword).
func (s *AuthService) ResetPassword(ctx context.Context, req *accessv1.ResetPasswordRequest) (*accessv1.ResetPasswordResponse, error) {
	in, err := s.d.Resets.Reset(ctx, req.GetToken(), req.GetPassword(), s.meta(ctx))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return accessv1.ResetPasswordResponse_builder{Session: session(in), User: user(in.User)}.Build(), nil
}

// GetCurrentUser implements AuthService.GetCurrentUser (lago CurrentUser
// query): the session's user and every organization of its active
// memberships.
func (s *AuthService) GetCurrentUser(ctx context.Context, _ *accessv1.GetCurrentUserRequest) (*accessv1.GetCurrentUserResponse, error) {
	sess, err := userSession(ctx)
	if err != nil {
		return nil, err
	}
	u, err := s.d.Users.Get(ctx, sess.UserID)
	if err != nil {
		return nil, Error(ctx, err)
	}
	orgs, err := s.organizations(ctx, u.ID, "")
	if err != nil {
		return nil, err
	}
	return accessv1.GetCurrentUserResponse_builder{User: user(u), Organizations: orgs}.Build(), nil
}

// SignOut implements AuthService.SignOut: it revokes the request's
// session.
func (s *AuthService) SignOut(ctx context.Context, _ *accessv1.SignOutRequest) (*accessv1.SignOutResponse, error) {
	if _, err := userSession(ctx); err != nil {
		return nil, err
	}
	info, _ := connect.CallInfoForServerContext(ctx)
	token, _ := bearer(info)
	if err := s.d.Sessions.Revoke(ctx, token); err != nil {
		return nil, Error(ctx, err)
	}
	return &accessv1.SignOutResponse{}, nil
}

// userSession returns the session of a user principal.
func userSession(ctx context.Context) (*auth.Session, error) {
	p, ok := Principal(ctx)
	if !ok || p.Kind != auth.PrincipalUser || p.Session == nil {
		return nil, newError(connect.CodePermissionDenied, ReasonForbidden, "a user session is required")
	}
	return p.Session, nil
}

// meta describes the client of the request for the session it opens.
func (s *AuthService) meta(ctx context.Context) auth.SessionMeta {
	info, ok := connect.CallInfoForServerContext(ctx)
	if !ok {
		return auth.SessionMeta{}
	}
	h := info.RequestHeader()
	ua := h.Get("User-Agent")
	d := useragent.Parse(ua)
	return auth.SessionMeta{UserAgent: ua, IP: s.d.IPs.Resolve(info.PeerAddr, h), Browser: d.Browser, OS: d.OS, DeviceType: d.DeviceType}
}

// organizations lists the organizations of user allowing method ("" for
// all).
func (s *AuthService) organizations(ctx context.Context, id core.UserID, method organization.AuthenticationMethod) ([]*accessv1.OrganizationAccess, error) {
	orgs, err := s.d.Users.Organizations(ctx, id, method)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*accessv1.OrganizationAccess, len(orgs))
	for i, o := range orgs {
		out[i] = organizationAccess(o)
	}
	return out, nil
}

// joined returns the organization a sign-up created or an invite joined.
func (s *AuthService) joined(ctx context.Context, in *auth.SignedIn) (*accessv1.OrganizationAccess, error) {
	if in.Organization == nil {
		return nil, nil
	}
	orgs, err := s.d.Users.Organizations(ctx, in.User.ID, "")
	if err != nil {
		return nil, Error(ctx, err)
	}
	for _, o := range orgs {
		if o.Organization.ID == in.Organization.ID {
			return organizationAccess(o), nil
		}
	}
	return organizationAccess(auth.OrganizationAccess{Organization: in.Organization, Membership: in.Membership}), nil
}

func organizationAccess(o auth.OrganizationAccess) *accessv1.OrganizationAccess {
	b := accessv1.OrganizationAccess_builder{
		OrganizationId:   new(o.Organization.ID.String()),
		OrganizationName: new(o.Organization.Name),
	}
	if o.Membership != nil {
		b.MembershipId = new(o.Membership.ID.String())
	}
	if len(o.Roles) > 0 {
		b.RoleCode = new(o.Roles[0].Code)
	}
	for _, p := range access.UnionPermissions(o.Roles) {
		b.Permissions = append(b.Permissions, string(p))
	}
	return b.Build()
}

func session(in *auth.SignedIn) *accessv1.Session {
	return accessv1.Session_builder{Token: new(in.Token), ExpiresAt: Timestamp(in.Session.ExpiresAt)}.Build()
}

func user(u *access.User) *accessv1.User {
	return accessv1.User_builder{
		Id: new(u.ID.String()), Email: new(u.Email), Name: u.Name,
		LastSignInAt: TimestampPtr(u.LastSignInAt), CreatedAt: Timestamp(u.CreatedAt),
	}.Build()
}
