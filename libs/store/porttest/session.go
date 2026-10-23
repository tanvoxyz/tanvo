package porttest

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// hashOf returns the SHA-256 of s.
func hashOf(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// sessionFixture returns a session of user, not stored.
func sessionFixture(e *env, user core.UserID, token string) *access.Session {
	return &access.Session{
		ID: core.NewID[core.SessionID](e.ids), UserID: user, TokenHash: hashOf(token),
		Method: organization.AuthenticationMethodEmailPassword, UserAgent: "Mozilla/5.0", IP: "192.0.2.1",
		CreatedAt: now, LastSeenAt: now, ExpiresAt: at(24 * time.Hour),
	}
}

func testSession(t *testing.T, h Harness) {
	t.Run("sessions", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		alice := accessFixtureUser(t, e, "alice@session.test")
		bob := accessFixtureUser(t, e, "bob@session.test")
		s1 := sessionFixture(e, alice.ID, "s1")
		s2 := sessionFixture(e, alice.ID, "s2")
		s2.Method = organization.AuthenticationMethodOkta
		s3 := sessionFixture(e, alice.ID, "s3")
		b1 := sessionFixture(e, bob.ID, "b1")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, s := range []*access.Session{s1, s2, s3, b1} {
				noErr(t, "Create()", r.Sessions.Create(ctx, s))
			}
			dup := sessionFixture(e, bob.ID, "s1")
			wantErr(t, "Create() with a taken token", r.Sessions.Create(ctx, dup), core.ErrConflict)
			return nil
		})
		// A missing user aborts the transaction in PostgreSQL.
		orphan := sessionFixture(e, core.NewID[core.UserID](e.ids), "orphan")
		wantErr(t, "Create() of an unknown user", e.txErr(func(ctx context.Context, r port.Repos) error {
			return r.Sessions.Create(ctx, orphan)
		}), core.ErrNotFound)
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Sessions.GetByTokenHash(ctx, hashOf("s2"))
			noErr(t, "GetByTokenHash()", err)
			equal(t, "GetByTokenHash()", got, s2)
			_, err = r.Sessions.GetByTokenHash(ctx, hashOf("nope"))
			wantErr(t, "GetByTokenHash() unknown", err, core.ErrNotFound)

			noErr(t, "Touch()", r.Sessions.Touch(ctx, s1.ID, at(time.Minute)))
			s1.LastSeenAt = at(time.Minute)
			got, err = r.Sessions.GetByTokenHash(ctx, hashOf("s1"))
			noErr(t, "GetByTokenHash() after Touch()", err)
			equal(t, "GetByTokenHash() after Touch()", got, s1)
			wantErr(t, "Touch() unknown", r.Sessions.Touch(ctx, core.NewID[core.SessionID](e.ids), now), core.ErrNotFound)

			noErr(t, "Revoke()", r.Sessions.Revoke(ctx, s3.ID, at(time.Hour)))
			noErr(t, "Revoke() again", r.Sessions.Revoke(ctx, s3.ID, at(2*time.Hour)))
			s3.RevokedAt = ptr(at(time.Hour))
			got, err = r.Sessions.GetByTokenHash(ctx, hashOf("s3"))
			noErr(t, "GetByTokenHash() after Revoke()", err)
			equal(t, "GetByTokenHash() after Revoke() keeps the first instant", got, s3)
			wantErr(t, "Revoke() unknown", r.Sessions.Revoke(ctx, core.NewID[core.SessionID](e.ids), now), core.ErrNotFound)

			n, err := r.Sessions.RevokeByUser(ctx, alice.ID, s1.ID, at(3*time.Hour))
			noErr(t, "RevokeByUser()", err)
			equal(t, "RevokeByUser() count", n, 1)
			for _, c := range []struct {
				token string
				want  *time.Time
			}{{"s1", nil}, {"s2", ptr(at(3 * time.Hour))}, {"s3", ptr(at(time.Hour))}, {"b1", nil}} {
				got, err := r.Sessions.GetByTokenHash(ctx, hashOf(c.token))
				noErr(t, "GetByTokenHash() after RevokeByUser()", err)
				equal(t, "RevokedAt of "+c.token+" after RevokeByUser()", got.RevokedAt, c.want)
			}

			// s2 and s3 are revoked before the cut-off; b1 expires before it.
			b1.ExpiresAt = at(time.Hour)
			n, err = r.Sessions.DeleteExpired(ctx, at(4*time.Hour))
			noErr(t, "DeleteExpired()", err)
			equal(t, "DeleteExpired() count", n, 2)
			for _, c := range []struct {
				token string
				kept  bool
			}{{"s1", true}, {"s2", false}, {"s3", false}, {"b1", true}} {
				_, err := r.Sessions.GetByTokenHash(ctx, hashOf(c.token))
				if c.kept {
					noErr(t, c.token+" after DeleteExpired()", err)
				} else {
					wantErr(t, c.token+" after DeleteExpired()", err, core.ErrNotFound)
				}
			}
			n, err = r.Sessions.DeleteExpired(ctx, at(48*time.Hour))
			noErr(t, "DeleteExpired() past every expiry", err)
			equal(t, "DeleteExpired() past every expiry count", n, 2)
			return nil
		})
	})

	t.Run("oidc states", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		login := &access.OIDCState{
			KeyHash: hashOf("state-login"), Method: organization.AuthenticationMethodOkta, Intent: access.OIDCIntentLogin,
			ConnectionID: "okta-acme", Email: "alice@acme.test", Nonce: "n1", Verifier: "v1",
			ExpiresAt: at(10 * time.Minute), CreatedAt: now,
		}
		invite := &access.OIDCState{
			KeyHash: hashOf("state-invite"), Method: organization.AuthenticationMethodGoogleOAuth, Intent: access.OIDCIntentAcceptInvite,
			ConnectionID: "google", InviteTokenHash: hashOf("invite"), Nonce: "n2", Verifier: "v2",
			ExpiresAt: at(10 * time.Minute), CreatedAt: now,
		}
		stale := &access.OIDCState{
			KeyHash: hashOf("state-stale"), Method: organization.AuthenticationMethodEntraID, Intent: access.OIDCIntentSignUp,
			ConnectionID: "entra", OrganizationName: "Acme", Nonce: "n3", Verifier: "v3",
			ExpiresAt: at(-time.Minute), CreatedAt: at(-11 * time.Minute),
		}
		old := &access.OIDCState{
			KeyHash: hashOf("state-old"), Method: organization.AuthenticationMethodOIDC, Intent: access.OIDCIntentLogin,
			ConnectionID: "oidc", Nonce: "n4", Verifier: "v4", ExpiresAt: at(-time.Hour), CreatedAt: at(-2 * time.Hour),
		}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, s := range []*access.OIDCState{login, invite, stale, old} {
				noErr(t, "Create()", r.OIDCStates.Create(ctx, s))
			}
			wantErr(t, "Create() with a taken key", r.OIDCStates.Create(ctx, login), core.ErrConflict)

			got, err := r.OIDCStates.Take(ctx, hashOf("state-login"), now)
			noErr(t, "Take()", err)
			equal(t, "Take()", got, login)
			_, err = r.OIDCStates.Take(ctx, hashOf("state-login"), now)
			wantErr(t, "Take() again", err, core.ErrNotFound)
			got, err = r.OIDCStates.Take(ctx, hashOf("state-invite"), now)
			noErr(t, "Take() of an invite", err)
			equal(t, "Take() of an invite", got, invite)
			_, err = r.OIDCStates.Take(ctx, hashOf("state-stale"), now)
			wantErr(t, "Take() expired", err, core.ErrNotFound)
			_, err = r.OIDCStates.Take(ctx, hashOf("nope"), now)
			wantErr(t, "Take() unknown", err, core.ErrNotFound)

			n, err := r.OIDCStates.DeleteExpired(ctx, now)
			noErr(t, "DeleteExpired()", err)
			equal(t, "DeleteExpired() count (the expired state was taken)", n, 1)
			return nil
		})
	})

	t.Run("user devices", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		alice := accessFixtureUser(t, e, "alice@device.test")
		bob := accessFixtureUser(t, e, "bob@device.test")
		laptop := access.NewUserDevice(core.NewID[core.UserDeviceID](e.ids), alice.ID, "Mozilla/5.0 (Macintosh)", now)
		laptop.SignedIn("Chrome 130", "Mac", "desktop", "192.0.2.1", now)
		phone := access.NewUserDevice(core.NewID[core.UserDeviceID](e.ids), alice.ID, "Mozilla/5.0 (iPhone)", at(time.Minute))
		bobs := access.NewUserDevice(core.NewID[core.UserDeviceID](e.ids), bob.ID, "Mozilla/5.0 (Macintosh)", now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, d := range []*access.UserDevice{laptop, phone, bobs} {
				noErr(t, "Create()", r.UserDevices.Create(ctx, d))
			}
			dup := access.NewUserDevice(core.NewID[core.UserDeviceID](e.ids), alice.ID, "Mozilla/5.0 (Macintosh)", now)
			wantErr(t, "Create() with a taken fingerprint", r.UserDevices.Create(ctx, dup), core.ErrConflict)
			return nil
		})
		orphan := access.NewUserDevice(core.NewID[core.UserDeviceID](e.ids), core.NewID[core.UserID](e.ids), "x", now)
		wantErr(t, "Create() of an unknown user", e.txErr(func(ctx context.Context, r port.Repos) error {
			return r.UserDevices.Create(ctx, orphan)
		}), core.ErrNotFound)
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.UserDevices.GetByFingerprint(ctx, alice.ID, access.DeviceFingerprint("Mozilla/5.0 (Macintosh)"))
			noErr(t, "GetByFingerprint()", err)
			equal(t, "GetByFingerprint()", got, laptop)
			_, err = r.UserDevices.GetByFingerprint(ctx, alice.ID, access.DeviceFingerprint("unknown"))
			wantErr(t, "GetByFingerprint() unknown", err, core.ErrNotFound)

			phone.SignedIn("Safari 18", "iOS", "smartphone", "198.51.100.7", at(time.Hour))
			noErr(t, "Update()", r.UserDevices.Update(ctx, phone))
			got, err = r.UserDevices.GetByFingerprint(ctx, alice.ID, phone.Fingerprint)
			noErr(t, "GetByFingerprint() after Update()", err)
			equal(t, "GetByFingerprint() after Update()", got, phone)
			laptop.SignedIn("", "", "", "", at(2*time.Hour))
			noErr(t, "Update() clearing the description", r.UserDevices.Update(ctx, laptop))

			wrongUser := *phone
			wrongUser.UserID = bob.ID
			wantErr(t, "Update() of another user's device", r.UserDevices.Update(ctx, &wrongUser), core.ErrNotFound)
			missing := access.NewUserDevice(core.NewID[core.UserDeviceID](e.ids), alice.ID, "missing", now)
			wantErr(t, "Update() unknown", r.UserDevices.Update(ctx, missing), core.ErrNotFound)

			list, err := r.UserDevices.ListByUser(ctx, alice.ID)
			noErr(t, "ListByUser()", err)
			equal(t, "ListByUser()", list, []*access.UserDevice{laptop, phone})
			list, err = r.UserDevices.ListByUser(ctx, core.NewID[core.UserID](e.ids))
			noErr(t, "ListByUser() of an unknown user", err)
			equal(t, "ListByUser() of an unknown user", list, []*access.UserDevice{})
			return nil
		})
	})

	t.Run("auth integrations by domain", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		role := access.BuiltInRoleID(permission.BuiltInRoleAdmin)
		sso := func(org core.OrganizationID, code string, p integration.Provider, s integration.AuthSettings) *integration.Integration {
			i := integrationFixture(e, org, code, p)
			s.ClientID, s.DefaultRoleID = "client", role
			noErr(t, "SetAuthSettings()", i.SetAuthSettings(s))
			return i
		}
		okta := sso(a.OrganizationID, "okta", integration.ProviderOkta, integration.AuthSettings{Domain: "Acme.test", OrganizationName: "acme"})
		entra := sso(a.OrganizationID, "entra", integration.ProviderEntraID, integration.AuthSettings{
			Domain: "acme.test", AdditionalDomains: []string{"Acme.example", "acme-labs.test"}, TenantID: "tenant",
		})
		entraB := sso(b.OrganizationID, "entra", integration.ProviderEntraID, integration.AuthSettings{
			Domain: "globex.test", AdditionalDomains: []string{"acme-labs.test"}, TenantID: "tenant",
		})
		// A non-auth integration with a domain setting is never routed.
		ns := integrationFixture(e, a.OrganizationID, "netsuite", integration.ProviderNetsuite)
		noErr(t, "SetSetting()", ns.SetSetting("domain", "acme.test"))
		integrationFixtureCreate(e, okta, entra, entraB, ns)

		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, c := range []struct {
				p      integration.Provider
				domain string
				want   []core.IntegrationID
			}{
				{integration.ProviderOkta, "ACME.test", []core.IntegrationID{okta.ID}},
				{integration.ProviderEntraID, "acme.test", []core.IntegrationID{entra.ID}},
				{integration.ProviderEntraID, "acme.EXAMPLE", []core.IntegrationID{entra.ID}},
				{integration.ProviderEntraID, "acme-labs.test", []core.IntegrationID{entra.ID, entraB.ID}},
				{integration.ProviderEntraID, "globex.test", []core.IntegrationID{entraB.ID}},
				{integration.ProviderOkta, "acme.example", nil},
				{integration.ProviderNetsuite, "acme.test", nil},
				{integration.ProviderOIDC, "acme.test", nil},
			} {
				got, err := r.Integrations.ListByDomain(ctx, c.p, c.domain)
				noErr(t, "ListByDomain()", err)
				ids := make([]core.IntegrationID, 0, len(got))
				for _, i := range got {
					ids = append(ids, i.ID)
				}
				if c.want == nil {
					c.want = []core.IntegrationID{}
				}
				equal(t, "ListByDomain("+string(c.p)+", "+c.domain+")", ids, c.want)
			}
			return nil
		})
	})
}
