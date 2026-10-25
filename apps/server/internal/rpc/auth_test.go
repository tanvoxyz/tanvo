package rpc_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	accessv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/access/v1/accessv1connect"
)

func authClient(s *rpctest.Server, c rpctest.Credential) accessv1connect.AuthServiceClient {
	return accessv1connect.NewAuthServiceClient(s.Connect(c))
}

func TestAuthSessionLifecycle(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	ctx := t.Context()
	anon := authClient(s, rpctest.Anonymous)

	// source: spec/graphql/mutations/register_user_spec.rb:45
	up, err := anon.SignUp(ctx, accessv1.SignUpRequest_builder{
		Email: new("foo@bar.com"), Password: new("ILoveLago!"), OrganizationName: new("FooBar"),
	}.Build())
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if got := up.GetUser().GetEmail(); got != "foo@bar.com" {
		t.Errorf("got email = %q, want = foo@bar.com", got)
	}
	if got := up.GetOrganization().GetOrganizationName(); got != "FooBar" {
		t.Errorf("got organization = %q, want = FooBar", got)
	}
	if up.GetSession().GetToken() == "" || up.GetOrganization().GetRoleCode() == "" {
		t.Errorf("got session %q, role %q, want both set", up.GetSession().GetToken(), up.GetOrganization().GetRoleCode())
	}
	// The organization gets its default billing entity, coded after its
	// name.
	// source: spec/services/organizations/create_service_spec.rb:100
	var entity string
	if err := s.Admin.QueryRow(ctx, `SELECT code || ':' || name || ':' || is_default FROM billing_entities WHERE organization_id = $1`,
		up.GetOrganization().GetOrganizationId()).Scan(&entity); err != nil || entity != "foobar:FooBar:true" {
		t.Errorf("got billing entity = %q (%v), want = foobar:FooBar:true", entity, err)
	}

	// source: spec/graphql/mutations/register_user_spec.rb:73
	_, err = anon.SignUp(ctx, accessv1.SignUpRequest_builder{
		Email: new("foo@bar.com"), Password: new("ILoveLago!"), OrganizationName: new("Other"),
	}.Build())
	if v := rpc.ErrorViolations(err); connect.CodeOf(err) != connect.CodeInvalidArgument || len(v) != 1 || v[0].Field != "email" || v[0].Reason != "user_already_exists" {
		t.Errorf("got %v (%+v), want = InvalidArgument email user_already_exists", err, v)
	}

	// source: spec/graphql/mutations/login_user_spec.rb:22
	in, err := anon.SignInWithPassword(ctx, accessv1.SignInWithPasswordRequest_builder{
		Email: new("foo@bar.com"), Password: new("ILoveLago!"),
	}.Build())
	if err != nil {
		t.Fatalf("SignInWithPassword: %v", err)
	}
	if in.GetUser().GetId() != up.GetUser().GetId() || len(in.GetOrganizations()) != 1 {
		t.Errorf("got user %s with %d organizations, want = %s with 1", in.GetUser().GetId(), len(in.GetOrganizations()), up.GetUser().GetId())
	}

	// source: spec/graphql/mutations/login_user_spec.rb:40
	_, err = anon.SignInWithPassword(ctx, accessv1.SignInWithPasswordRequest_builder{
		Email: new("foo@bar.com"), Password: new("wrong-password"),
	}.Build())
	if connect.CodeOf(err) == 0 || rpc.ErrorReason(err) != "incorrect_login_or_password" {
		t.Errorf("got %v (reason %q), want = incorrect_login_or_password", err, rpc.ErrorReason(err))
	}

	// The session acts without an organization on the authenticated RPCs.
	sess := rpctest.Credential{Token: in.GetSession().GetToken()}
	me, err := authClient(s, sess).GetCurrentUser(ctx, accessv1.GetCurrentUserRequest_builder{}.Build())
	if err != nil {
		t.Fatalf("GetCurrentUser: %v", err)
	}
	if me.GetUser().GetEmail() != "foo@bar.com" || len(me.GetOrganizations()) != 1 {
		t.Errorf("got %s with %d organizations, want = foo@bar.com with 1", me.GetUser().GetEmail(), len(me.GetOrganizations()))
	}

	if _, err := authClient(s, sess).SignOut(ctx, accessv1.SignOutRequest_builder{}.Build()); err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	_, err = authClient(s, sess).GetCurrentUser(ctx, accessv1.GetCurrentUserRequest_builder{}.Build())
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("got %v after sign-out, want = Unauthenticated", err)
	}
}

func TestSignUpDisabled(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t, func(o *app.APIOptions) { o.DisableSignup = true })
	_, err := authClient(s, rpctest.Anonymous).SignUp(t.Context(), accessv1.SignUpRequest_builder{
		Email: new("foo@bar.com"), Password: new("ILoveLago!"), OrganizationName: new("FooBar"),
	}.Build())
	if connect.CodeOf(err) == 0 {
		t.Errorf("got %v, want an error", err)
	}
}

func TestRequestPasswordResetHidesUnknownEmails(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	for _, email := range []string{s.User.Email, "nobody@tanvo.test"} {
		if _, err := authClient(s, rpctest.Anonymous).RequestPasswordReset(t.Context(),
			accessv1.RequestPasswordResetRequest_builder{Email: new(email)}.Build()); err != nil {
			t.Errorf("RequestPasswordReset(%s): got %v, want = nil", email, err)
		}
	}
}
