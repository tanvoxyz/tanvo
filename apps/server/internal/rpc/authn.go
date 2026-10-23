package rpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/logctx"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
)

// OrganizationHeader names the organization a user session acts in
// (lago's x-lago-organization header). API keys and portal tokens belong
// to one organization and ignore it.
const OrganizationHeader = "X-Tanvo-Organization"

// Reasons of the authentication errors.
const (
	// ReasonOrganizationRequired: a user session called an organization
	// RPC without [OrganizationHeader].
	ReasonOrganizationRequired = "organization_required"
)

// KeyAuthenticator resolves an API key secret (libs/auth APIKeys).
type KeyAuthenticator interface {
	// Authenticate returns the active key whose secret is secret, or
	// auth.ErrUnauthenticated.
	Authenticate(ctx context.Context, secret string) (*access.APIKey, error)
}

// SessionAuthenticator resolves a session token (libs/auth Sessions).
type SessionAuthenticator interface {
	// Authenticate returns the live session of token, or
	// auth.ErrUnauthenticated.
	Authenticate(ctx context.Context, token string) (*auth.Session, error)
}

// PortalVerifier verifies a customer portal token (libs/auth
// PortalAuthenticator).
type PortalVerifier interface {
	// Verify returns the claims of a valid token whose customer is live,
	// or auth.ErrUnauthenticated.
	Verify(ctx context.Context, token string) (auth.PortalClaims, error)
}

// Authenticators are the credentials [Auth] accepts. A nil field refuses
// that credential.
type Authenticators struct {
	// Keys authenticates API keys.
	Keys KeyAuthenticator
	// Sessions authenticates user sessions.
	Sessions SessionAuthenticator
	// Portal verifies customer portal tokens.
	Portal PortalVerifier
	// Authz checks permissions and resolves user principals.
	Authz *auth.Authorizer
}

type principalKey struct{}

// Principal returns the principal the [Auth] interceptor authenticated.
func Principal(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	return p, ok
}

// WithPrincipal returns ctx carrying p, as [Auth] does.
func WithPrincipal(ctx context.Context, p auth.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// Auth enforces the tanvo.common.v1.access option of each RPC
// (architecture §4.4). Every credential is sent as "Authorization: Bearer
// <token>":
//
//   - anonymous RPCs pass without one;
//   - portal RPCs take a customer portal token;
//   - the other RPCs take an API key or a user session token. A session
//     acts in the organization named by [OrganizationHeader], where the
//     user needs an active membership; with the header the token is only
//     tried as a session, without it as an API key first.
//
// An API key may call only RPCs with an api_resource, granted at the read
// level for NO_SIDE_EFFECTS RPCs and the write level otherwise. A user
// needs the RPC's permission in the organization; an authenticated RPC
// accepts any user, and a session without the header (the principal then
// has no organization). An RPC without the option is refused. The
// organization of the principal is put on the span and on the logs of
// the request.
func Auth(a Authenticators) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			md, ok := spec.Schema.(protoreflect.MethodDescriptor)
			if !ok {
				return newError(connect.CodePermissionDenied, ReasonForbidden, "procedure has no access rule")
			}
			rule, _ := proto.GetExtension(md.Options(), commonv1.E_Access).(*commonv1.Access)
			if rule == nil {
				return newError(connect.CodePermissionDenied, ReasonForbidden, "procedure has no access rule")
			}
			if rule.GetAnonymous() {
				return next(ctx, spec, stream)
			}
			info, _ := connect.CallInfoForServerContext(ctx)
			token, ok := bearer(info)
			if !ok {
				return newError(connect.CodeUnauthenticated, ReasonUnauthenticated, "missing credential: send Authorization: Bearer <token>")
			}
			p, err := a.authenticate(ctx, rule, info, token)
			if err != nil {
				return Error(ctx, err)
			}
			if !p.OrganizationID.IsZero() {
				trace.SpanFromContext(ctx).SetAttributes(attribute.String(logctx.OrganizationKey, p.OrganizationID.String()))
				ctx = logctx.With(ctx, slog.String(logctx.OrganizationKey, p.OrganizationID.String()))
			}
			ctx = WithPrincipal(ctx, p)
			if err := a.authorize(p, rule, md); err != nil {
				return Error(ctx, err)
			}
			return next(ctx, spec, stream)
		}
	}
}

// authenticate resolves the principal of token for an RPC with rule.
func (a Authenticators) authenticate(ctx context.Context, rule *commonv1.Access, info *connect.CallInfo, token string) (auth.Principal, error) {
	if rule.GetPortal() {
		if a.Portal == nil {
			return auth.Principal{}, auth.ErrUnauthenticated
		}
		c, err := a.Portal.Verify(ctx, token)
		if err != nil {
			return auth.Principal{}, err
		}
		return auth.PortalPrincipal(c), nil
	}
	orgHeader := strings.TrimSpace(info.RequestHeader().Get(OrganizationHeader))
	if orgHeader == "" && a.Keys != nil {
		key, err := a.Keys.Authenticate(ctx, token)
		if err == nil {
			return auth.APIKeyPrincipal(key), nil
		}
		if !errors.Is(err, auth.ErrUnauthenticated) {
			return auth.Principal{}, err
		}
	}
	if a.Sessions == nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	sess, err := a.Sessions.Authenticate(ctx, token)
	if err != nil {
		return auth.Principal{}, err
	}
	if orgHeader == "" {
		return auth.Principal{Kind: auth.PrincipalUser, Session: sess}, nil
	}
	org, err := core.ParseID[core.OrganizationID](orgHeader)
	if err != nil {
		return auth.Principal{}, InvalidArgument("invalid organization", []FieldViolation{{
			Field: OrganizationHeader, Reason: core.CodeInvalid, Description: "must be an organization ID",
		}})
	}
	return a.Authz.UserPrincipal(ctx, sess, org)
}

// authorize checks that p may call the RPC md with rule.
func (a Authenticators) authorize(p auth.Principal, rule *commonv1.Access, md protoreflect.MethodDescriptor) error {
	switch p.Kind {
	case auth.PrincipalPortal:
		return nil
	case auth.PrincipalAPIKey:
		if rule.GetApiResource() == "" {
			return newError(connect.CodePermissionDenied, ReasonForbidden, "API keys cannot call this procedure")
		}
		level := permission.LevelWrite
		if idempotencyLevel(md) == descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
			level = permission.LevelRead
		}
		resource := permission.APIResource(rule.GetApiResource())
		if err := a.Authz.CheckAPI(p, resource, level); err != nil {
			if errors.Is(err, auth.ErrPermissionDenied) {
				// The reason names the refused mode and resource, as
				// lago's ApiKeys forbidden code does
				// (app/controllers/api/base_controller.rb:75).
				return newError(connect.CodePermissionDenied,
					fmt.Sprintf("%s_action_not_allowed_for_%s", level, resource), err.Error())
			}
			return err
		}
		return nil
	case auth.PrincipalUser:
		if rule.GetAuthenticated() {
			return nil
		}
		if p.OrganizationID.IsZero() {
			return newError(connect.CodePermissionDenied, ReasonOrganizationRequired,
				"send the organization to act in as "+OrganizationHeader)
		}
		if rule.GetPermission() == "" {
			return newError(connect.CodePermissionDenied, ReasonForbidden, "users cannot call this procedure")
		}
		return a.Authz.Check(p, permission.Permission(rule.GetPermission()))
	}
	return auth.ErrUnauthenticated
}

func bearer(info *connect.CallInfo) (string, bool) {
	if info == nil {
		return "", false
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(info.RequestHeader().Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
