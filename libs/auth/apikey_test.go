package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func (e *env) key(o *organization.Organization) *CreatedAPIKey {
	e.t.Helper()
	k, err := e.keys.Create(e.ctx, o.ID, nil, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return k
}

func (e *env) getKey(k *access.APIKey) *access.APIKey {
	e.t.Helper()
	var out *access.APIKey
	e.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.APIKeys.Get(ctx, k.OrganizationID, k.ID)
		return err
	})
	return out
}

func TestAPIKeysCreate(t *testing.T) {
	// source: spec/services/api_keys/create_service_spec.rb:24
	// source: spec/services/api_keys/create_service_spec.rb:28
	// source: spec/services/api_keys/create_service_spec.rb:34
	e := newEnv(t)
	o := e.org()
	perms := map[permission.APIResource]permission.Level{permission.APIResourceCustomer: permission.LevelRead}
	out, err := e.keys.Create(e.ctx, o.ID, ptr("ci"), perms)
	if err != nil {
		t.Fatal(err)
	}
	if out.Secret == "" || string(out.Key.TokenHash) == out.Secret || !out.Key.Permits(permission.APIResourceCustomer, permission.LevelRead) || out.Key.Permits(permission.APIResourceCustomer, permission.LevelWrite) {
		t.Errorf("key = %+v, want a hashed key with read access to customers", out.Key)
	}
	perms[permission.APIResourceCustomer] = permission.LevelWrite
	if out.Key.Permits(permission.APIResourceCustomer, permission.LevelWrite) {
		t.Error("permissions aliased to the caller's map")
	}
	if got, err := e.keys.Authenticate(e.ctx, out.Secret); err != nil || got.ID != out.Key.ID {
		t.Errorf("Authenticate = %v, %v", got, err)
	}
	if m := e.mailJobs(); len(m) != 1 || m[0].Template != MailAPIKeyCreated || m[0].OrganizationID != o.ID || m[0].Data["name"] != "ci" {
		t.Errorf("mails = %+v, want one api_key.created", m)
	}
	if e.log.count(EventAPIKeyCreated) != 1 {
		t.Errorf("events = %v", e.log.names())
	}
	// source: spec/services/api_keys/create_service_spec.rb:70
	t.Run("full access", func(t *testing.T) {
		k := e.key(o)
		if len(k.Key.Permissions) != 0 || e.authz.Check(APIKeyPrincipal(k.Key), "invoices:void") != nil {
			t.Errorf("permissions = %v, want full access", k.Key.Permissions)
		}
	})
	t.Run("invalid permissions", func(t *testing.T) {
		_, err := e.keys.Create(e.ctx, o.ID, nil, map[permission.APIResource]permission.Level{"nope": permission.LevelRead})
		if fieldOf(err) != "permissions" {
			t.Errorf("got = %v, want a permissions error", err)
		}
	})
}

func TestAPIKeysUpdate(t *testing.T) {
	tests := []struct {
		name string
		upd  APIKeyUpdate
		want func(t *testing.T, k *access.APIKey)
		code string
	}{
		// source: spec/services/api_keys/update_service_spec.rb:61
		{name: "name", upd: APIKeyUpdate{Name: ptr("renamed")}, want: func(t *testing.T, k *access.APIKey) {
			if k.Name == nil || *k.Name != "renamed" {
				t.Errorf("name = %v, want = renamed", k.Name)
			}
		}},
		// source: spec/services/api_keys/update_service_spec.rb:25
		// source: spec/services/api_keys/update_service_spec.rb:29
		{name: "permissions", upd: APIKeyUpdate{Permissions: ptr(map[permission.APIResource]permission.Level{permission.APIResourceInvoice: permission.LevelWrite})}, want: func(t *testing.T, k *access.APIKey) {
			if !k.Permits(permission.APIResourceInvoice, permission.LevelWrite) || k.Permits(permission.APIResourceCustomer, permission.LevelRead) {
				t.Errorf("permissions = %v", k.Permissions)
			}
		}},
		{name: "invalid permissions", upd: APIKeyUpdate{Permissions: ptr(map[permission.APIResource]permission.Level{permission.APIResourceInvoice: "admin"})}, code: core.CodeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			k := e.key(o)
			got, err := e.keys.Update(e.ctx, o.ID, k.Key.ID, tt.upd)
			if tt.code != "" {
				if err == nil {
					t.Fatal("got = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.want(t, got)
			tt.want(t, e.getKey(k.Key))
			if e.log.count(EventAPIKeyUpdated) != 1 {
				t.Errorf("events = %v", e.log.names())
			}
		})
	}
	// source: spec/services/api_keys/update_service_spec.rb:80
	t.Run("not found", func(t *testing.T) {
		e := newEnv(t)
		o := e.org()
		k := e.key(e.org())
		if _, err := e.keys.Update(e.ctx, o.ID, k.Key.ID, APIKeyUpdate{Name: ptr("x")}); code(err) != "api_key_not_found" || !errors.Is(err, core.ErrNotFound) {
			t.Errorf("got = %v, want = api_key_not_found", err)
		}
	})
}

func TestAPIKeysRotate(t *testing.T) {
	tests := []struct {
		name      string
		expiresIn *time.Duration
		oldWorks  bool
	}{
		// source: spec/services/api_keys/rotate_service_spec.rb:72
		// source: spec/services/api_keys/rotate_service_spec.rb:76
		// source: spec/services/api_keys/rotate_service_spec.rb:83
		{name: "expires the old key now"},
		// source: spec/services/api_keys/rotate_service_spec.rb:22
		// source: spec/services/api_keys/rotate_service_spec.rb:28
		// source: spec/services/api_keys/rotate_service_spec.rb:40
		{name: "overlap window", expiresIn: ptr(time.Hour), oldWorks: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			perms := map[permission.APIResource]permission.Level{permission.APIResourceCustomer: permission.LevelRead}
			old, err := e.keys.Create(e.ctx, o.ID, ptr("old"), perms)
			if err != nil {
				t.Fatal(err)
			}
			e.mailJobs()
			var rot APIKeyRotation
			if tt.expiresIn != nil {
				rot.ExpiresAt = ptr(e.clock.Now().Add(*tt.expiresIn))
			}
			n, err := e.keys.Rotate(e.ctx, o.ID, old.Key.ID, rot)
			if err != nil {
				t.Fatal(err)
			}
			if n.Key.ID == old.Key.ID || n.Secret == old.Secret || !n.Key.Permits(permission.APIResourceCustomer, permission.LevelRead) || n.Key.Permits(permission.APIResourceInvoice, permission.LevelRead) {
				t.Errorf("new key = %+v, want a new key with the old permissions", n.Key)
			}
			if n.Key.Name == nil || *n.Key.Name != "old" {
				t.Errorf("name = %v, want the old name", n.Key.Name)
			}
			if _, err := e.keys.Authenticate(e.ctx, n.Secret); err != nil {
				t.Errorf("new key: %v", err)
			}
			_, err = e.keys.Authenticate(e.ctx, old.Secret)
			if (err == nil) != tt.oldWorks {
				t.Errorf("old key authenticates = %v, want = %v", err == nil, tt.oldWorks)
			}
			if tt.expiresIn != nil {
				e.clock.Advance(*tt.expiresIn)
				if _, err := e.keys.Authenticate(e.ctx, old.Secret); !errors.Is(err, ErrUnauthenticated) {
					t.Errorf("old key after the window = %v, want = ErrUnauthenticated", err)
				}
			}
			if m := e.mailJobs(); len(m) != 1 || m[0].Template != MailAPIKeyRotated {
				t.Errorf("mails = %+v, want one api_key.rotated", m)
			}
			if e.log.count(EventAPIKeyRotated) != 1 {
				t.Errorf("events = %v", e.log.names())
			}
		})
	}
	t.Run("new name", func(t *testing.T) {
		e := newEnv(t)
		o := e.org()
		n, err := e.keys.Rotate(e.ctx, o.ID, e.key(o).Key.ID, APIKeyRotation{Name: ptr("new")})
		if err != nil || *n.Key.Name != "new" {
			t.Errorf("got = %v, %v", n, err)
		}
	})
	// source: spec/services/api_keys/rotate_service_spec.rb:113
	// source: spec/services/api_keys/rotate_service_spec.rb:121
	t.Run("not found", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.keys.Rotate(e.ctx, e.org().ID, core.NewID[core.APIKeyID](e.deps.IDs), APIKeyRotation{}); code(err) != "api_key_not_found" {
			t.Errorf("got = %s, want = api_key_not_found", code(err))
		}
	})
}

func TestAPIKeysDestroy(t *testing.T) {
	tests := []struct {
		name   string
		others int
		force  bool
		code   string
		mails  int
	}{
		// source: spec/services/api_keys/destroy_service_spec.rb:40
		// source: spec/services/api_keys/destroy_service_spec.rb:44
		// source: spec/services/api_keys/destroy_service_spec.rb:49
		{name: "another non-expiring key exists", others: 1, mails: 1},
		// source: spec/services/api_keys/destroy_service_spec.rb:57
		// source: spec/services/api_keys/destroy_service_spec.rb:63
		// source: spec/services/api_keys/destroy_service_spec.rb:67
		{name: "last non-expiring key", code: access.CodeLastNonExpiringKey},
		// source: spec/services/api_keys/destroy_service_spec.rb:80
		// source: spec/services/api_keys/destroy_service_spec.rb:84
		{name: "last non-expiring key with force", force: true},
		// source: spec/services/api_keys/destroy_service_spec.rb:102
		// source: spec/services/api_keys/destroy_service_spec.rb:106
		{name: "force with another non-expiring key", others: 1, force: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			o := e.org()
			k := e.key(o)
			for range tt.others {
				e.key(o)
			}
			e.mailJobs()
			got, err := e.keys.Destroy(e.ctx, o.ID, k.Key.ID, tt.force)
			if code(err) != tt.code {
				t.Fatalf("got = %s, want = %s", code(err), tt.code)
			}
			if m := e.mailJobs(); len(m) != tt.mails {
				t.Errorf("mails = %d, want = %d", len(m), tt.mails)
			}
			if err != nil {
				if e.getKey(k.Key).ExpiresAt != nil {
					t.Error("refused key expired")
				}
				return
			}
			if got.ExpiresAt == nil || !got.ExpiresAt.Equal(e.clock.Now()) {
				t.Errorf("expires_at = %v, want now", got.ExpiresAt)
			}
			if _, err := e.keys.Authenticate(e.ctx, k.Secret); !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("destroyed key authenticates: %v", err)
			}
			if e.log.count(EventAPIKeyDeleted) != 1 {
				t.Errorf("events = %v", e.log.names())
			}
		})
	}
	t.Run("expiring key", func(t *testing.T) {
		e := newEnv(t)
		o := e.org()
		k := e.key(o)
		n, err := e.keys.Rotate(e.ctx, o.ID, k.Key.ID, APIKeyRotation{ExpiresAt: ptr(e.clock.Now().Add(time.Hour))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.keys.Destroy(e.ctx, o.ID, k.Key.ID, false); err != nil {
			t.Errorf("destroying an expiring key: %v", err)
		}
		if _, err := e.keys.Destroy(e.ctx, o.ID, n.Key.ID, false); code(err) != access.CodeLastNonExpiringKey {
			t.Errorf("got = %s, want = %s", code(err), access.CodeLastNonExpiringKey)
		}
	})
	// source: spec/services/api_keys/destroy_service_spec.rb:16
	// source: spec/services/api_keys/destroy_service_spec.rb:22
	t.Run("not found", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.keys.Destroy(e.ctx, e.org().ID, core.NewID[core.APIKeyID](e.deps.IDs), false); code(err) != "api_key_not_found" {
			t.Errorf("got = %s, want = api_key_not_found", code(err))
		}
	})
}

func TestAPIKeysAuthenticate(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	k := e.key(o)
	for _, s := range []string{"", "nope", k.Secret + "x"} {
		if _, err := e.keys.Authenticate(e.ctx, s); !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("Authenticate(%q) = %v, want = ErrUnauthenticated", s, err)
		}
	}
}

func TestUsageTracker(t *testing.T) {
	e := newEnv(t)
	o := e.org()
	k := e.key(o)
	unused := e.key(o)
	if _, err := e.keys.Authenticate(e.ctx, k.Secret); err != nil {
		t.Fatal(err)
	}
	first := e.clock.Now()
	e.clock.Advance(time.Minute)
	if _, err := e.keys.Authenticate(e.ctx, k.Secret); err != nil {
		t.Fatal(err)
	}
	if got := e.getKey(k.Key).LastUsedAt; got != nil {
		t.Errorf("last_used_at = %v before flush, want nil", got)
	}
	if e.usage.Pending() != 1 {
		t.Errorf("pending = %d, want = 1", e.usage.Pending())
	}
	e.usage.Record(o.ID, k.Key.ID, first) // an older use never moves it back
	if err := e.usage.Flush(e.ctx, e.store); err != nil {
		t.Fatal(err)
	}
	// The flush records the uses; the clock job applies them.
	if got := e.getKey(k.Key).LastUsedAt; got != nil {
		t.Errorf("last_used_at = %v before the clock job, want nil", got)
	}
	e.store.TakeJobs()
	if n, err := DispatchUsage(e.ctx, e.store); err != nil || n != 1 {
		t.Fatalf("DispatchUsage() got = %d %v, want = 1 <nil>", n, err)
	}
	var applies []port.Job
	for _, j := range e.store.TakeJobs() {
		if j.Kind == JobKindApplyAPIKeyUsage {
			applies = append(applies, j)
		}
	}
	if len(applies) != 1 || applies[0].OrganizationID != o.ID {
		t.Fatalf("apply jobs got = %+v, want = one of %s", applies, o.ID)
	}
	if n, err := ApplyUsage(e.ctx, e.store, o.ID); err != nil || n != 1 {
		t.Fatalf("ApplyUsage() got = %d %v, want = 1 <nil>", n, err)
	}
	// source: spec/services/api_keys/track_usage_service_spec.rb:16
	if got := e.getKey(k.Key).LastUsedAt; got == nil || !got.Equal(e.clock.Now()) {
		t.Errorf("last_used_at = %v, want = %s", got, e.clock.Now())
	}
	// source: spec/services/api_keys/track_usage_service_spec.rb:24
	if got := e.getKey(unused.Key).LastUsedAt; got != nil {
		t.Errorf("unused key last_used_at = %v, want nil", got)
	}
	// source: spec/services/api_keys/track_usage_service_spec.rb:20
	if e.usage.Pending() != 0 {
		t.Errorf("pending = %d after flush, want = 0", e.usage.Pending())
	}
	if n, err := DispatchUsage(e.ctx, e.store); err != nil || n != 0 {
		t.Errorf("DispatchUsage() after apply got = %d %v, want = 0 <nil>", n, err)
	}
	// A deleted key is skipped, not an error.
	e.usage.Record(o.ID, core.NewID[core.APIKeyID](e.deps.IDs), e.clock.Now())
	if err := e.usage.Flush(e.ctx, e.store); err != nil {
		t.Errorf("flush of an unknown key: %v", err)
	}
}
