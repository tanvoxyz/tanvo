package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1/accessv1connect"
	portalv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1/portalv1connect"
)

func ssoClient(s *rpctest.Server, c rpctest.Credential) accessv1connect.SsoConnectionServiceClient {
	return accessv1connect.NewSsoConnectionServiceClient(s.Connect(c))
}

func TestSsoConnections(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	ctx := t.Context()
	c := ssoClient(s, s.Session)
	// source: spec/services/integrations/okta/create_service_spec.rb:55
	created, err := c.CreateSsoConnection(ctx, accessv1.CreateSsoConnectionRequest_builder{
		SsoConnection: accessv1.SsoConnection_builder{
			Provider: accessv1.SsoProvider_SSO_PROVIDER_OKTA.Enum(), Domain: new("foo.bar"), ClientId: new("client_id"),
			ClientSecret: new("client_secret"), OrganizationName: new("Foobar"), DefaultRoleCode: new("finance"),
		}.Build(),
	}.Build())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sc := created.GetSsoConnection()
	if sc.GetDomain() != "foo.bar" || sc.GetDefaultRoleCode() != "finance" || sc.HasClientSecret() {
		t.Errorf("created got = %v, want foo.bar, finance, no secret", sc)
	}
	var stored []byte
	if err := s.Admin.QueryRow(ctx, "SELECT secrets FROM integrations WHERE id = $1", core.MustParseID[core.IntegrationID](sc.GetId()).UUID()).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) == 0 || strings.Contains(string(stored), "client_secret") {
		t.Errorf("stored secrets got = %q, want sealed", stored)
	}

	got, err := c.GetSsoConnection(ctx, accessv1.GetSsoConnectionRequest_builder{SsoConnectionId: new(sc.GetId())}.Build())
	if err != nil || got.GetSsoConnection().HasClientSecret() || got.GetSsoConnection().GetOrganizationName() != "Foobar" {
		t.Errorf("get got = %v, %v", got, err)
	}
	list, err := c.ListSsoConnections(ctx, &accessv1.ListSsoConnectionsRequest{})
	if err != nil || len(list.GetSsoConnections()) != 1 {
		t.Errorf("list got = %v, %v, want one connection", list, err)
	}
	// source: spec/services/integrations/okta/update_service_spec.rb:86
	up, err := c.UpdateSsoConnection(ctx, accessv1.UpdateSsoConnectionRequest_builder{
		SsoConnection: accessv1.SsoConnection_builder{Id: new(sc.GetId()), Domain: new("new.bar"), ClientSecret: new("••••••••…cret")}.Build(),
	}.Build())
	if err != nil || up.GetSsoConnection().GetDomain() != "new.bar" {
		t.Errorf("update got = %v, %v, want domain new.bar", up, err)
	}

	tests := []struct {
		name   string
		cred   rpctest.Credential
		conn   *accessv1.SsoConnection
		code   connect.Code
		reason string
	}{
		{
			name: "undefined provider refused", cred: s.Session, code: connect.CodeInvalidArgument,
			conn: accessv1.SsoConnection_builder{
				Provider: accessv1.SsoProvider(99).Enum(), Domain: new("x.com"), ClientId: new("c"),
				ClientSecret: new("s"), DefaultRoleCode: new("finance"),
			}.Build(),
		},
		{
			// The issuer check goes through the guarded client, which
			// refuses a loopback provider.
			name: "generic oidc with a private issuer", cred: s.Session, code: connect.CodeInvalidArgument, reason: rpc.ReasonValidationErrors,
			conn: accessv1.SsoConnection_builder{
				Provider: accessv1.SsoProvider_SSO_PROVIDER_OIDC.Enum(), Domain: new("g.com"), ClientId: new("c"),
				ClientSecret: new("s"), DefaultRoleCode: new("finance"), IssuerUrl: new("https://127.0.0.1:1"),
			}.Build(),
		},
		{
			name: "generic oidc without issuer", cred: s.Session, code: connect.CodeInvalidArgument, reason: rpc.ReasonValidationErrors,
			conn: accessv1.SsoConnection_builder{
				Provider: accessv1.SsoProvider_SSO_PROVIDER_OIDC.Enum(), Domain: new("g.com"), ClientId: new("c"),
				ClientSecret: new("s"), DefaultRoleCode: new("finance"),
			}.Build(),
		},
		{
			name: "second okta", cred: s.Session, code: connect.CodeInvalidArgument, reason: rpc.ReasonValidationErrors,
			conn: accessv1.SsoConnection_builder{
				Provider: accessv1.SsoProvider_SSO_PROVIDER_OKTA.Enum(), Domain: new("other.com"), ClientId: new("c"),
				ClientSecret: new("s"), OrganizationName: new("o"), DefaultRoleCode: new("finance"),
			}.Build(),
		},
		{
			name: "api key refused", cred: s.APIKey, code: connect.CodePermissionDenied,
			conn: accessv1.SsoConnection_builder{
				Provider: accessv1.SsoProvider_SSO_PROVIDER_ENTRA_ID.Enum(), Domain: new("e.com"), ClientId: new("c"),
				ClientSecret: new("s"), TenantId: new("t"), DefaultRoleCode: new("finance"),
			}.Build(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ssoClient(s, tt.cred).CreateSsoConnection(ctx, accessv1.CreateSsoConnectionRequest_builder{SsoConnection: tt.conn}.Build())
			if got := connect.CodeOf(err); got != tt.code {
				t.Fatalf("got = %v (%v), want = %v", got, err, tt.code)
			}
			if tt.reason != "" && rpc.ErrorReason(err) != tt.reason {
				t.Errorf("got reason = %q, want = %q", rpc.ErrorReason(err), tt.reason)
			}
		})
	}

	// source: spec/services/integrations/okta/destroy_service_spec.rb:20
	if _, err := c.DeleteSsoConnection(ctx, accessv1.DeleteSsoConnectionRequest_builder{SsoConnectionId: new(sc.GetId())}.Build()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// source: spec/services/integrations/okta/destroy_service_spec.rb:37
	_, err = c.GetSsoConnection(ctx, accessv1.GetSsoConnectionRequest_builder{SsoConnectionId: new(sc.GetId())}.Build())
	if connect.CodeOf(err) != connect.CodeNotFound || rpc.ErrorReason(err) != "integration_not_found" {
		t.Errorf("get after delete got = %v (%s), want = NotFound integration_not_found", err, rpc.ErrorReason(err))
	}
}

// portalProbe is a customer portal service whose GetPortalCustomer
// records the principal the interceptor authenticated.
type portalProbe struct {
	portalv1connect.UnimplementedCustomerPortalServiceHandler

	got chan auth.Principal
}

func (p *portalProbe) GetPortalCustomer(ctx context.Context, _ *portalv1.GetPortalCustomerRequest) (*portalv1.GetPortalCustomerResponse, error) {
	pr, _ := rpc.Principal(ctx)
	p.got <- pr
	return &portalv1.GetPortalCustomerResponse{}, nil
}

// TestPortalAuthentication checks the portal token flow (lago
// CustomerPortalUser): a token issued for a live customer authenticates
// the portal RPCs as that customer; an expired token, a deleted customer
// and other credentials are refused.
func TestPortalAuthentication(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	tokens, err := app.NewPortalTokens(rpctest.SecretKey, "https://app.tanvo.test", s.Clock)
	if err != nil {
		t.Fatal(err)
	}
	probe := &portalProbe{got: make(chan auth.Principal, 1)}
	server := connect.NewServer(rpc.Auth(rpc.Authenticators{
		Portal: auth.PortalAuthenticator{Tokens: tokens, Tx: s.Store}, Authz: auth.NewAuthorizer(s.Store),
	}))
	portalv1connect.RegisterCustomerPortalServiceHandler(server, probe)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := func(c rpctest.Credential) portalv1connect.CustomerPortalServiceClient {
		hc := &http.Client{Transport: bearer{token: c.Token}}
		return portalv1connect.NewCustomerPortalServiceClient(connect.NewClient(connecthttp.NewTransport(hc, srv.URL)))
	}

	live := s.Fixtures.Customer()
	gone := s.Fixtures.Customer(func(c *customer.Customer) { at := s.Clock.Now(); c.DeletedAt = &at })
	expired := s.Portal(t, live.ID)
	s.Clock.Set(s.Clock.Now().Add(auth.DefaultPortalTokenTTL + time.Second))
	tests := []struct {
		name string
		cred rpctest.Credential
		ok   bool
	}{
		{name: "live customer", cred: s.Portal(t, live.ID), ok: true},
		{name: "expired token", cred: expired},
		{name: "deleted customer", cred: s.Portal(t, gone.ID)},
		{name: "api key", cred: s.APIKey},
		{name: "session", cred: rpctest.Credential{Token: s.Session.Token}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client(tt.cred).GetPortalCustomer(t.Context(), &portalv1.GetPortalCustomerRequest{})
			if !tt.ok {
				if connect.CodeOf(err) != connect.CodeUnauthenticated {
					t.Errorf("got = %v, want = Unauthenticated", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("got = %v, want = nil", err)
			}
			p := <-probe.got
			if p.Kind != auth.PrincipalPortal || p.CustomerID != live.ID || p.OrganizationID != s.Org.ID {
				t.Errorf("principal got = %+v, want = portal %s", p, live.ID)
			}
		})
	}
}

// bearer sends token as the bearer credential.
type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}
