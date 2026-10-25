package app_test

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// TestBootstrap checks that `tanvo admin bootstrap` finds before it
// creates each object of lib/tasks/signup.rake, so that it may run at
// every start.
func TestBootstrap(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	ctx := t.Context()
	hasher := auth.NewPasswordHasher(auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32}, rand.Reader)
	run := func(t *testing.T, in app.BootstrapInput) *app.BootstrapResult {
		t.Helper()
		res, err := app.BootstrapTx(ctx, s.Store, s.Clock, hasher, in)
		if err != nil {
			t.Fatalf("bootstrap(%+v) error got = %v, want = nil", in, err)
		}
		return res
	}
	entity := func(t *testing.T, org core.OrganizationID) string {
		t.Helper()
		var got string
		if err := s.Admin.QueryRow(ctx, `SELECT string_agg(code || ':' || name || ':' || is_default, ',') FROM billing_entities WHERE organization_id = $1`,
			org.String()).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	in := app.BootstrapInput{OrganizationName: "Acme Corp", Email: "Admin@Acme.test", Password: "s3cret!"}

	first := run(t, in)
	if want := []string{"user", "organization", "billing_entity", "membership", "api_key"}; !slices.Equal(first.Created, want) {
		t.Errorf("first run created got = %v, want = %v", first.Created, want)
	}
	if first.APIKey == "" {
		t.Error("first run api key got = empty, want = a generated secret")
	}
	if got := entity(t, first.Organization.ID); got != "acme-corp:Acme Corp:true" {
		t.Errorf("billing entity got = %q, want = acme-corp:Acme Corp:true", got)
	}
	if ok, _, err := hasher.Verify(first.User.PasswordHash, "s3cret!"); !ok || err != nil || first.User.Email != "admin@acme.test" {
		t.Errorf("user got = %s with password ok %t (%v), want = admin@acme.test with the password", first.User.Email, ok, err)
	}
	k, err := keyBySecret(ctx, s.Store, first.APIKey)
	if err != nil || k.OrganizationID != first.Organization.ID || len(k.Permissions) != 0 {
		t.Errorf("generated key got = %+v (%v), want = a full-access key of %s", k, err, first.Organization.ID)
	}

	// Running again creates nothing and keeps the password.
	again := run(t, app.BootstrapInput{OrganizationName: "Acme Corp", Email: "admin@acme.test", Password: "other"})
	if len(again.Created) != 0 || again.APIKey != "" || again.Organization.ID != first.Organization.ID || again.User.ID != first.User.ID {
		t.Errorf("second run got = created %v, key %q, org %s, user %s, want = nothing created in %s for %s",
			again.Created, again.APIKey, again.Organization.ID, again.User.ID, first.Organization.ID, first.User.ID)
	}
	if ok, _, _ := hasher.Verify(again.User.PasswordHash, "s3cret!"); !ok {
		t.Error("second run password got = changed, want = kept")
	}

	t.Run("given api key", func(t *testing.T) {
		in := app.BootstrapInput{OrganizationName: "Acme Corp", Email: "admin@acme.test", Password: "s3cret!", APIKey: "given-key-0123456789"}
		if res := run(t, in); !slices.Equal(res.Created, []string{"api_key"}) || res.APIKey != "" {
			t.Errorf("created got = %v (key %q), want = [api_key] with no secret printed", res.Created, res.APIKey)
		}
		if res := run(t, in); len(res.Created) != 0 {
			t.Errorf("rerun created got = %v, want = none", res.Created)
		}
		if k, err := keyBySecret(ctx, s.Store, in.APIKey); err != nil || k.OrganizationID != first.Organization.ID || k.Prefix != "given-ke" {
			t.Errorf("given key got = %+v (%v), want = a key of %s prefixed given-ke", k, err, first.Organization.ID)
		}
		// The key of another organization is refused.
		in.OrganizationName = "Other Org"
		if _, err := app.BootstrapTx(ctx, s.Store, s.Clock, hasher, in); !errors.Is(err, core.ErrConflict) {
			t.Errorf("another organization's key error got = %v, want = %v", err, core.ErrConflict)
		}
		err := s.Store.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			_, err := r.Organizations.GetBySlug(ctx, "other-org")
			return err
		})
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("refused run organization got = %v, want = rolled back", err)
		}
	})

	t.Run("existing organization and member", func(t *testing.T) {
		// An organization without a billing entity, and a member who is
		// not an admin.
		now := s.Clock.Now()
		var org *organization.Organization
		user := access.NewUser(core.NewID[core.UserID](core.UUIDv7Generator{}), "viewer@acme.test", nil, now)
		err := s.Store.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			var err error
			if org, err = (auth.BasicOrganizationCreator{Clock: s.Clock, IDs: core.UUIDv7Generator{}}).CreateOrganization(ctx, r, "Bare Org"); err != nil {
				return err
			}
			if err := r.Users.Create(ctx, user); err != nil {
				return err
			}
			return r.Memberships.Create(ctx, access.NewMembership(core.NewID[core.MembershipID](core.UUIDv7Generator{}), org.ID, user.ID,
				[]core.RoleID{access.BuiltInRoleID(permission.BuiltInRoleFinance)}, now))
		})
		if err != nil {
			t.Fatal(err)
		}
		res := run(t, app.BootstrapInput{OrganizationName: "Bare Org", Email: "viewer@acme.test", Password: "x"})
		if want := []string{"billing_entity", "admin_role", "api_key"}; !slices.Equal(res.Created, want) || res.Organization.ID != org.ID {
			t.Errorf("created got = %v in %s, want = %v in %s", res.Created, res.Organization.ID, want, org.ID)
		}
		if got := entity(t, org.ID); got != "bare-org:Bare Org:true" {
			t.Errorf("billing entity got = %q, want = bare-org:Bare Org:true", got)
		}
		var m *access.Membership
		_ = s.Store.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			m, err = r.Memberships.GetActiveByUser(ctx, org.ID, user.ID)
			return err
		})
		want := access.NormalizeRoleIDs([]core.RoleID{access.BuiltInRoleID(permission.BuiltInRoleAdmin), access.BuiltInRoleID(permission.BuiltInRoleFinance)})
		if err != nil || !slices.Equal(m.RoleIDs, want) {
			t.Errorf("membership roles got = %v (%v), want = %v", m, err, want)
		}
	})

	t.Run("invalid input", func(t *testing.T) {
		for _, in := range []app.BootstrapInput{
			{Email: "a@b.test", Password: "x"},
			{OrganizationName: "Acme Corp", Password: "x"},
			{OrganizationName: "Acme Corp", Email: "a@b.test"},
			{OrganizationName: "Acme Corp", Email: "a@b.test", Password: "x", APIKey: "short"},
		} {
			var ve *core.ValidationError
			if _, err := app.BootstrapTx(ctx, s.Store, s.Clock, hasher, in); !errors.As(err, &ve) {
				t.Errorf("bootstrap(%+v) error got = %v, want = a validation error", in, err)
			}
		}
	})
}

// keyBySecret returns the API key of secret.
func keyBySecret(ctx context.Context, tx port.Transactor, secret string) (*access.APIKey, error) {
	var k *access.APIKey
	err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		k, err = r.APIKeys.GetByTokenHash(ctx, access.HashToken(secret))
		return err
	})
	return k, err
}
