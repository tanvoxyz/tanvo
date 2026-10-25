package rpc_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1/accessv1connect"
	logv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/log/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/log/v1/logv1connect"
	organizationv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/organization/v1/organizationv1connect"
)

// member adds an active member of s.Org holding roles; it returns the
// membership.
func member(t *testing.T, s *rpctest.Server, email string, roles ...permission.BuiltInRole) *access.Membership {
	t.Helper()
	ids := core.UUIDv7Generator{}
	s.Clock.Advance(time.Second)
	now := s.Clock.Now()
	u := access.NewUser(core.NewID[core.UserID](ids), email, nil, now)
	roleIDs := make([]core.RoleID, len(roles))
	for i, r := range roles {
		roleIDs[i] = access.BuiltInRoleID(r)
	}
	m := access.NewMembership(core.NewID[core.MembershipID](ids), s.Org.ID, u.ID, roleIDs, now)
	err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		if err := r.Users.Create(ctx, u); err != nil {
			return err
		}
		return r.Memberships.Create(ctx, m)
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	return m
}

func wantCode(t *testing.T, err error, code connect.Code, reason string) {
	t.Helper()
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("got = %v (%v), want = %v", got, err, code)
	}
	if reason != "" {
		if got := rpc.ErrorReason(err); got != reason {
			t.Errorf("got reason = %q, want = %q", got, reason)
		}
	}
}

func TestMemberships(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := accessv1connect.NewMemberServiceClient(s.Connect(s.Session))
	ctx := t.Context()
	fin := member(t, s, "finance@tanvo.test", permission.BuiltInRoleFinance)
	mgr := member(t, s, "manager@tanvo.test", permission.BuiltInRoleManager)

	t.Run("list", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/memberships_resolver_spec.rb:23")
		resp, err := c.ListMemberships(ctx, accessv1.ListMembershipsRequest_builder{}.Build())
		if err != nil {
			t.Fatal(err)
		}
		var emails []string
		for _, m := range resp.GetMemberships() {
			emails = append(emails, m.GetUser().GetEmail())
		}
		// Newest first (lago apply_consistent_ordering).
		if want := []string{"manager@tanvo.test", "finance@tanvo.test", "admin@tanvo.test"}; !slices.Equal(emails, want) {
			t.Errorf("emails got = %v, want = %v", emails, want)
		}
		t.Log("source: spec/graphql/resolvers/memberships_resolver_spec.rb:43")
		if resp.GetAdminCount() != 1 {
			t.Errorf("admin_count got = %d, want = 1", resp.GetAdminCount())
		}
	})
	t.Run("filters", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/memberships_resolver_spec.rb:100,111")
		resp, err := c.ListMemberships(ctx, accessv1.ListMembershipsRequest_builder{Search: new("FINANCE")}.Build())
		if err != nil || len(resp.GetMemberships()) != 1 || resp.GetMemberships()[0].GetId() != fin.ID.String() {
			t.Errorf("search got = %v, %v, want = the finance member", resp, err)
		}
		resp, err = c.ListMemberships(ctx, accessv1.ListMembershipsRequest_builder{Filter: new(`role_id = "code:manager"`)}.Build())
		if err != nil || len(resp.GetMemberships()) != 1 || resp.GetMemberships()[0].GetId() != mgr.ID.String() {
			t.Errorf("role filter got = %v, %v, want = the manager", resp, err)
		}
	})
	t.Run("update", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/memberships/update_spec.rb:53")
		resp, err := c.UpdateMembership(ctx, accessv1.UpdateMembershipRequest_builder{
			Membership: accessv1.Membership_builder{Id: new(mgr.ID.String()), RoleIds: []string{"code:finance"}}.Build(),
		}.Build())
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.GetMembership().GetRoleIds(); !slices.Equal(got, []string{access.BuiltInRoleID(permission.BuiltInRoleFinance).String()}) {
			t.Errorf("role_ids got = %v, want = finance", got)
		}
	})
	t.Run("non-admin cannot grant admin", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/memberships/update_spec.rb:135")
		// The finance role lacks organization:members:update.
		finCred := session(t, s, fin)
		_, err := accessv1connect.NewMemberServiceClient(s.Connect(finCred)).UpdateMembership(ctx, accessv1.UpdateMembershipRequest_builder{
			Membership: accessv1.Membership_builder{Id: new(mgr.ID.String()), RoleIds: []string{"code:admin"}}.Build(),
		}.Build())
		wantCode(t, err, connect.CodePermissionDenied, "")
	})
	t.Run("revoke own", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/memberships/revoke_spec.rb:61")
		var own string
		resp, _ := c.ListMemberships(ctx, accessv1.ListMembershipsRequest_builder{Filter: new(`role_id = "code:admin"`)}.Build())
		own = resp.GetMemberships()[0].GetId()
		_, err := c.RevokeMembership(ctx, accessv1.RevokeMembershipRequest_builder{MembershipId: &own}.Build())
		wantCode(t, err, connect.CodeFailedPrecondition, access.CodeCannotRevokeOwnMembership)
	})
	t.Run("revoke", func(t *testing.T) {
		t.Log("source: spec/graphql/mutations/memberships/revoke_spec.rb:49")
		resp, err := c.RevokeMembership(ctx, accessv1.RevokeMembershipRequest_builder{MembershipId: new(fin.ID.String())}.Build())
		if err != nil {
			t.Fatal(err)
		}
		if resp.GetMembership().GetStatus() != accessv1.MembershipStatus_MEMBERSHIP_STATUS_REVOKED || resp.GetMembership().GetRevokedAt() == nil {
			t.Errorf("got %v, want a revoked membership", resp.GetMembership())
		}
	})
	t.Run("api keys cannot call", func(t *testing.T) {
		_, err := accessv1connect.NewMemberServiceClient(s.Connect(s.APIKey)).ListMemberships(ctx, accessv1.ListMembershipsRequest_builder{}.Build())
		wantCode(t, err, connect.CodePermissionDenied, rpc.ReasonForbidden)
	})
}

// session returns a session of the user of m acting in s.Org.
func session(t *testing.T, s *rpctest.Server, m *access.Membership) rpctest.Credential {
	t.Helper()
	d := auth.Deps{Tx: s.Store, Clock: s.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader}
	token, _, err := auth.NewSessions(d, auth.DefaultSessionConfig()).Issue(t.Context(), m.UserID, organization.AuthenticationMethodEmailPassword, auth.SessionMeta{})
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	return rpctest.Credential{Token: token, Organization: s.Org.ID.String()}
}

func TestRoles(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := accessv1connect.NewMemberServiceClient(s.Connect(s.Session))
	ctx := t.Context()

	t.Log("source: spec/graphql/mutations/roles/create_spec.rb:50")
	created, err := c.CreateRole(ctx, accessv1.CreateRoleRequest_builder{Role: accessv1.Role_builder{
		Code: new("dev"), Name: new("Developer"), Permissions: []string{"addons:read", "customers:read"},
	}.Build()}.Build())
	if err != nil {
		t.Fatal(err)
	}
	r := created.GetRole()
	if r.GetCode() != "dev" || r.GetBuiltIn() || !slices.Equal(r.GetPermissions(), []string{"addons:read", "customers:read"}) {
		t.Errorf("created got = %v", r)
	}

	t.Log("source: app/graphql/types/roles/create_input.rb:11 (permissions are PermissionEnum values)")
	_, err = c.CreateRole(ctx, accessv1.CreateRoleRequest_builder{Role: accessv1.Role_builder{
		Code: new("dev2"), Name: new("Dev"), Permissions: []string{"nope:read"},
	}.Build()}.Build())
	wantCode(t, err, connect.CodeInvalidArgument, "")

	got, err := c.GetRole(ctx, accessv1.GetRoleRequest_builder{RoleId: new("code:dev")}.Build())
	if err != nil || got.GetRole().GetId() != r.GetId() {
		t.Fatalf("get by code got = %v, %v", got, err)
	}
	admin, err := c.GetRole(ctx, accessv1.GetRoleRequest_builder{RoleId: new("code:admin")}.Build())
	if err != nil || !admin.GetRole().GetBuiltIn() || len(admin.GetRole().GetPermissions()) != len(permission.All()) {
		t.Errorf("admin got = %v, %v, want every permission", admin, err)
	}

	t.Log("source: spec/graphql/resolvers/roles_resolver_spec.rb:47")
	list, err := c.ListRoles(ctx, accessv1.ListRolesRequest_builder{}.Build())
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, r := range list.GetRoles() {
		codes = append(codes, r.GetCode())
	}
	if !slices.Equal(codes, []string{"admin", "finance", "manager", "dev"}) {
		t.Errorf("codes got = %v, want = built-ins then dev", codes)
	}

	upd, err := c.UpdateRole(ctx, accessv1.UpdateRoleRequest_builder{Role: accessv1.Role_builder{Id: new(r.GetId()), Name: new("Engineer")}.Build()}.Build())
	if err != nil || upd.GetRole().GetName() != "Engineer" || len(upd.GetRole().GetPermissions()) != 2 {
		t.Errorf("update got = %v, %v", upd, err)
	}
	_, err = c.UpdateRole(ctx, accessv1.UpdateRoleRequest_builder{Role: accessv1.Role_builder{Id: new("code:admin"), Name: new("Boss")}.Build()}.Build())
	wantCode(t, err, connect.CodeFailedPrecondition, access.CodePredefinedRole)

	if _, err := c.DeleteRole(ctx, accessv1.DeleteRoleRequest_builder{RoleId: new(r.GetId())}.Build()); err != nil {
		t.Fatal(err)
	}
	_, err = c.GetRole(ctx, accessv1.GetRoleRequest_builder{RoleId: new(r.GetId())}.Build())
	wantCode(t, err, connect.CodeNotFound, "")

	// Creating a role logs role.created with the user and device of the
	// request (lago security_log.rb resolve_user_id, graphql_controller.rb:35).
	var logs []*auditlog.SecurityLog
	err = s.Store.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.SecurityLogs.List(ctx, s.Org.ID, auditlog.SecurityLogFilter{Events: []string{auth.EventRoleCreated}}, core.PageRequest{})
		logs = p.Items
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].UserID != s.User.ID || logs[0].DeviceInfo["user_agent"] == nil {
		t.Errorf("security logs got = %+v, want role.created by the admin with a device", logs)
	}
}

func TestInvites(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := accessv1connect.NewMemberServiceClient(s.Connect(s.Session))
	ctx := t.Context()

	t.Log("source: spec/graphql/mutations/invites/create_spec.rb:56")
	created, err := c.CreateInvite(ctx, accessv1.CreateInviteRequest_builder{Invite: accessv1.Invite_builder{
		Email: new("new@tanvo.test"), RoleIds: []string{"code:finance"},
	}.Build()}.Build())
	if err != nil {
		t.Fatal(err)
	}
	inv := created.GetInvite()
	if inv.GetAcceptUrl() == "" || inv.GetStatus() != accessv1.InviteStatus_INVITE_STATUS_PENDING {
		t.Errorf("created got = %v, want a pending invite with its URL", inv)
	}
	list, err := c.ListInvites(ctx, accessv1.ListInvitesRequest_builder{Search: new("new@")}.Build())
	if err != nil || len(list.GetInvites()) != 1 || list.GetInvites()[0].GetAcceptUrl() != "" {
		t.Errorf("list got = %v, %v, want the invite without its URL", list, err)
	}
	upd, err := c.UpdateInvite(ctx, accessv1.UpdateInviteRequest_builder{Invite: accessv1.Invite_builder{
		Id: new(inv.GetId()), RoleIds: []string{"code:manager"},
	}.Build()}.Build())
	if err != nil || !slices.Equal(upd.GetInvite().GetRoleIds(), []string{access.BuiltInRoleID(permission.BuiltInRoleManager).String()}) {
		t.Errorf("update got = %v, %v", upd, err)
	}

	t.Log("source: spec/graphql/mutations/invites/revoke_spec.rb:31")
	rev, err := c.RevokeInvite(ctx, accessv1.RevokeInviteRequest_builder{InviteId: new(inv.GetId())}.Build())
	if err != nil || rev.GetInvite().GetStatus() != accessv1.InviteStatus_INVITE_STATUS_REVOKED {
		t.Errorf("revoke got = %v, %v", rev, err)
	}
	// Only a pending invite can be revoked (app/services/invites/revoke_service.rb:14).
	t.Log("source: spec/graphql/mutations/invites/revoke_spec.rb:53")
	_, err = c.RevokeInvite(ctx, accessv1.RevokeInviteRequest_builder{InviteId: new(inv.GetId())}.Build())
	if err == nil {
		t.Error("revoking twice got = nil, want an error")
	}
}

func TestAPIKeys(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	c := accessv1connect.NewApiKeyServiceClient(s.Connect(s.Session))
	ctx := t.Context()

	list, err := c.ListApiKeys(ctx, accessv1.ListApiKeysRequest_builder{}.Build())
	if err != nil || len(list.GetApiKeys()) != 1 {
		t.Fatalf("list got = %v, %v, want the seeded key", list, err)
	}
	seeded := list.GetApiKeys()[0]

	t.Log("source: spec/services/api_keys/destroy_service_spec.rb:57")
	_, err = c.DeleteApiKey(ctx, accessv1.DeleteApiKeyRequest_builder{ApiKeyId: new(seeded.GetId())}.Build())
	wantCode(t, err, connect.CodeFailedPrecondition, access.CodeLastNonExpiringKey)

	t.Log("source: spec/graphql/mutations/api_keys/create_spec.rb:39")
	created, err := c.CreateApiKey(ctx, accessv1.CreateApiKeyRequest_builder{ApiKey: accessv1.ApiKey_builder{
		Name: new("ci"),
		Grants: []*accessv1.ApiKeyGrant{accessv1.ApiKeyGrant_builder{
			Resource: new(accessv1.ApiResource_API_RESOURCE_CUSTOMER), Level: new(accessv1.ApiAccessLevel_API_ACCESS_LEVEL_READ),
		}.Build()},
	}.Build()}.Build())
	if err != nil {
		t.Fatal(err)
	}
	k := created.GetApiKey()
	if k.GetSecret() == "" || k.GetName() != "ci" || len(k.GetGrants()) != 1 || k.GetGrants()[0].GetResource() != accessv1.ApiResource_API_RESOURCE_CUSTOMER {
		t.Errorf("created got = %v", k)
	}
	got, err := c.GetApiKey(ctx, accessv1.GetApiKeyRequest_builder{ApiKeyId: new(k.GetId())}.Build())
	if err != nil || got.GetApiKey().GetSecret() != "" {
		t.Errorf("get got = %v, %v, want the key without its secret", got, err)
	}

	t.Log("source: spec/graphql/mutations/api_keys/rotate_spec.rb:44")
	rot, err := c.RotateApiKey(ctx, accessv1.RotateApiKeyRequest_builder{ApiKeyId: new(k.GetId()), Overlap: durationpb.New(time.Hour)}.Build())
	if err != nil {
		t.Fatal(err)
	}
	if rot.GetApiKey().GetSecret() == "" || rot.GetApiKey().GetName() != "ci" {
		t.Errorf("rotate got = %v", rot)
	}
	// The old key stays valid until the end of the overlap.
	old, err := c.GetApiKey(ctx, accessv1.GetApiKeyRequest_builder{ApiKeyId: new(k.GetId())}.Build())
	if err != nil || !old.GetApiKey().GetExpiresAt().AsTime().Equal(s.Clock.Now().Add(time.Hour)) {
		t.Errorf("old key got = %v, %v, want it to expire in an hour", old, err)
	}

	t.Log("source: spec/services/api_keys/destroy_service_spec.rb:40")
	if _, err := c.DeleteApiKey(ctx, accessv1.DeleteApiKeyRequest_builder{ApiKeyId: new(rot.GetApiKey().GetId())}.Build()); err != nil {
		t.Fatal(err)
	}
	_, err = c.GetApiKey(ctx, accessv1.GetApiKeyRequest_builder{ApiKeyId: new(rot.GetApiKey().GetId())}.Build())
	wantCode(t, err, connect.CodeNotFound, "")
}

func TestOrganization(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	ctx := t.Context()

	t.Log("source: spec/requests/api/v1/organizations_controller_spec.rb:45")
	status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/organization", map[string]any{
		"email": "billing@tanvo.test", "slug": " New-Slug ", "address": map[string]any{"city": "Paris", "country": "FR"},
	})
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	got := decode[struct {
		Organization struct {
			Email   string `json:"email"`
			Slug    string `json:"slug"`
			Address struct {
				City string `json:"city"`
			} `json:"address"`
		} `json:"organization"`
	}](t, body).Organization
	if got.Email != "billing@tanvo.test" || got.Slug != "new-slug" || got.Address.City != "Paris" {
		t.Errorf("got %+v, want the new email, slug and city", got)
	}

	// The from_email premium integration is the from_email_enabled setting
	// (BillingEntity#from_email_address, billing_entity.rb:141-145).
	for _, on := range []bool{true, false} {
		status, body = s.REST(t, s.APIKey, http.MethodPatch, "/v1/organization", map[string]any{"from_email_enabled": on})
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := decode[struct {
			Organization struct {
				FromEmailEnabled bool   `json:"from_email_enabled"`
				Slug             string `json:"slug"`
			} `json:"organization"`
		}](t, body).Organization
		if got.FromEmailEnabled != on || got.Slug != "new-slug" {
			t.Errorf("got %+v, want from_email_enabled = %v and the slug kept", got, on)
		}
	}

	t.Log("source: spec/services/organizations/update_service_spec.rb:177")
	status, _ = s.REST(t, s.APIKey, http.MethodPatch, "/v1/organization", map[string]any{"slug": "admin"})
	if status != http.StatusBadRequest {
		t.Errorf("reserved slug got status = %d, want = 400", status)
	}

	methods := &organizationv1.UpdateOrganizationRequest{}
	methods.SetOrganization(organizationv1.Organization_builder{AuthenticationMethods: []organizationv1.AuthenticationMethod{
		organizationv1.AuthenticationMethod_AUTHENTICATION_METHOD_EMAIL_PASSWORD,
	}}.Build())
	// Deviation: v1.55.0 defines authentication_methods:update (config/permissions.yml:16)
	// but never checks it on the server (update_organization_input.rb:8); the REST
	// API does not take the field (organizations_controller.rb).
	_, err := organizationv1connect.NewOrganizationServiceClient(s.Connect(s.APIKey)).UpdateOrganization(ctx, methods)
	wantCode(t, err, connect.CodePermissionDenied, rpc.ReasonForbidden)
	resp, err := organizationv1connect.NewOrganizationServiceClient(s.Connect(s.Session)).UpdateOrganization(ctx, methods)
	if err != nil || !slices.Equal(resp.GetOrganization().GetAuthenticationMethods(), methods.GetOrganization().GetAuthenticationMethods()) {
		t.Errorf("session update got = %v, %v", resp, err)
	}

	info, err := organizationv1connect.NewServerServiceClient(s.Connect(s.Session)).GetServerInfo(ctx, organizationv1.GetServerInfoRequest_builder{}.Build())
	if err != nil || info.GetVersion() == "" {
		t.Errorf("server info got = %v, %v", info, err)
	}
}

func TestLogs(t *testing.T) {
	t.Parallel()
	var writer *rpc.APILogWriter
	s := rpctest.New(t, func(o *app.APIOptions) {
		var err error
		if writer, err = rpc.NewAPILogWriter(o.Tx, o.Log, metricnoop.NewMeterProvider(), rpc.APILogWriterConfig{}); err != nil {
			t.Fatal(err)
		}
		o.APILogs = writer
	})
	ctx := t.Context()
	c := logv1connect.NewLogServiceClient(s.Connect(s.APIKey))
	now := s.Clock.Now()
	err := s.Store.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := r.SecurityLogs.Create(ctx, &auditlog.SecurityLog{
			ID: core.UUIDv7Generator{}.NewUUID(), OrganizationID: s.Org.ID, UserID: s.User.ID, Type: "user", Event: "user.signed_up",
			LoggedAt: now.Add(-time.Hour), CreatedAt: now,
		}); err != nil {
			return err
		}
		return r.ActivityLogs.Create(ctx, &auditlog.ActivityLog{
			ID: core.UUIDv7Generator{}.NewUUID(), OrganizationID: s.Org.ID, UserID: s.User.ID, Type: "customer.created",
			Source: auditlog.ActivitySourceFront, ResourceID: core.UUIDv7Generator{}.NewUUID(), ResourceType: "customer", LoggedAt: now.Add(-time.Hour), CreatedAt: now,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Log("source: spec/requests/api/v1/security_logs_controller_spec.rb:62")
	_, err = c.ListSecurityLogs(ctx, logv1.ListSecurityLogsRequest_builder{}.Build())
	wantCode(t, err, connect.CodeInvalidArgument, "")

	t.Log("source: spec/requests/api/v1/security_logs_controller_spec.rb:51")
	sec, err := c.ListSecurityLogs(ctx, logv1.ListSecurityLogsRequest_builder{Filter: new(`logged_at <= "` + now.Format(time.RFC3339) + `" AND log_type = user`)}.Build())
	if err != nil || len(sec.GetSecurityLogs()) != 1 || sec.GetSecurityLogs()[0].GetUserEmail() != "admin@tanvo.test" {
		t.Errorf("security logs got = %v, %v", sec, err)
	}

	t.Log("source: spec/requests/api/v1/activity_logs_controller_spec.rb:108")
	act, err := c.ListActivityLogs(ctx, logv1.ListActivityLogsRequest_builder{Filter: new("user_email = admin@tanvo.test AND activity_source = dashboard")}.Build())
	if err != nil || len(act.GetActivityLogs()) != 1 || act.GetActivityLogs()[0].GetActivitySource() != logv1.ActivitySource_ACTIVITY_SOURCE_DASHBOARD {
		t.Errorf("activity logs got = %v, %v", act, err)
	}

	// A POST made with an API key is logged (lago ApiLoggable); a GET is not.
	s.Fixtures.Tax("vat", "10")
	if status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/taxes", `{"name":"tax","code":"tax_code","rate":"20"}`); status != http.StatusOK {
		t.Fatalf("create tax got status = %d (%s)", status, body)
	}
	if status, _ := s.REST(t, s.APIKey, http.MethodPost, "/v1/taxes", `{"name":"tax","code":"vat","rate":"20"}`); status != http.StatusBadRequest {
		t.Fatalf("duplicate tax got status = %d, want = 400", status)
	}
	s.REST(t, s.APIKey, http.MethodGet, "/v1/taxes", nil)
	writer.Flush(ctx)

	t.Log("source: spec/requests/api/v1/api_logs_controller_spec.rb:33")
	apiLogs, err := c.ListApiLogs(ctx, logv1.ListApiLogsRequest_builder{}.Build())
	if err != nil {
		t.Fatal(err)
	}
	if len(apiLogs.GetApiLogs()) != 2 {
		t.Fatalf("api logs got = %d, want = 2", len(apiLogs.GetApiLogs()))
	}
	var l *logv1.ApiLog
	for _, x := range apiLogs.GetApiLogs() {
		if x.GetHttpStatus() == http.StatusOK {
			l = x
		}
	}
	// The request body is the RPC request (the REST body maps to its tax field).
	if l.GetHttpMethod() != "post" || l.GetRequestPath() != "/v1/taxes" || l.GetApiVersion() != "v1" ||
		l.GetRequestBody().GetFields()["tax"].GetStructValue().GetFields()["code"].GetStringValue() != "tax_code" ||
		l.GetRequestResponse().GetFields()["tax"] == nil {
		t.Errorf("api log got = %v", l)
	}

	t.Log("source: spec/requests/api/v1/api_logs_controller_spec.rb:93")
	failed, err := c.ListApiLogs(ctx, logv1.ListApiLogsRequest_builder{Filter: new("http_status = failed AND request_path = \"/v1/tax*\"")}.Build())
	if err != nil || len(failed.GetApiLogs()) != 1 || failed.GetApiLogs()[0].GetHttpStatus() != 400 {
		t.Errorf("failed api logs got = %v, %v", failed, err)
	}
	one, err := c.GetApiLog(ctx, logv1.GetApiLogRequest_builder{RequestId: new(l.GetRequestId())}.Build())
	if err != nil || one.GetApiLog().GetRequestId() != l.GetRequestId() {
		t.Errorf("get api log got = %v, %v", one, err)
	}

	// Reading logs needs audit_logs:read, which finance lacks (lago audit_logs:view,
	// app/graphql/resolvers/api_logs_resolver.rb:8).
	fin := member(t, s, "finance@tanvo.test", permission.BuiltInRoleFinance)
	_, err = logv1connect.NewLogServiceClient(s.Connect(session(t, s, fin))).ListApiLogs(ctx, logv1.ListApiLogsRequest_builder{}.Build())
	wantCode(t, err, connect.CodePermissionDenied, "")
}
