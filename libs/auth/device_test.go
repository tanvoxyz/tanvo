package auth

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
)

func TestRegisterDevice(t *testing.T) {
	mac := SessionMeta{UserAgent: "Mozilla/5.0 (Macintosh)", IP: "192.0.2.1", Browser: "Chrome 130", OS: "Mac", DeviceType: "desktop"}
	phone := SessionMeta{UserAgent: "Mozilla/5.0 (iPhone)", IP: "198.51.100.7", Browser: "Safari 18", OS: "iOS", DeviceType: "smartphone"}
	tests := []struct {
		name string
		// metas are the clients of successive logins of a member of two
		// organizations; the Mac signs in last.
		metas       []SessionMeta
		wantDevices int
		wantLogs    int
	}{
		// source: spec/services/user_devices/register_service_spec.rb:31
		{name: "no user agent", metas: []SessionMeta{{IP: "192.0.2.1"}}},
		// source: spec/services/user_devices/register_service_spec.rb:40
		// source: spec/services/user_devices/register_service_spec.rb:51
		{name: "new device", metas: []SessionMeta{mac}, wantDevices: 1, wantLogs: 2},
		// source: spec/services/user_devices/register_service_spec.rb:85
		// source: spec/services/user_devices/register_service_spec.rb:95
		{name: "known device", metas: []SessionMeta{mac, mac}, wantDevices: 1, wantLogs: 2},
		{name: "two devices", metas: []SessionMeta{mac, phone, mac}, wantDevices: 2, wantLogs: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			u := e.user("member@hooli.com", "pw")
			e.member(u, e.org(), permission.BuiltInRoleAdmin)
			e.member(u, e.org(), permission.BuiltInRoleFinance)
			for _, m := range tt.metas {
				e.clock.Advance(time.Minute)
				if _, err := e.users.Login(e.ctx, u.Email, "pw", m); err != nil {
					t.Fatal(err)
				}
			}
			ds := e.devices(u.ID)
			if len(ds) != tt.wantDevices {
				t.Fatalf("devices got = %d, want = %d", len(ds), tt.wantDevices)
			}
			if got := e.log.count(EventNewDeviceLoggedIn); got != tt.wantLogs {
				t.Errorf("new device logs got = %d, want = %d", got, tt.wantLogs)
			}
			if len(ds) == 0 {
				return
			}
			// source: spec/services/user_devices/register_service_spec.rb:89
			d := ds[0]
			if d.Fingerprint != access.DeviceFingerprint(mac.UserAgent) || *d.Browser != "Chrome 130" || *d.OS != "Mac" ||
				*d.DeviceType != "desktop" || *d.LastIPAddress != "192.0.2.1" || !d.LastLoggedAt.Equal(e.clock.Now()) {
				t.Errorf("device got = %+v, want the last sign-in from the Mac", d)
			}
		})
	}

	// source: spec/services/user_devices/register_service_spec.rb:62
	t.Run("sign-up does not log the first device", func(t *testing.T) {
		e := newEnv(t)
		out, err := e.users.Register(e.ctx, "new@hooli.com", "pw", "Hooli", mac)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(e.devices(out.User.ID)); got != 1 {
			t.Errorf("devices got = %d, want = 1", got)
		}
		if got := e.log.count(EventNewDeviceLoggedIn); got != 0 {
			t.Errorf("new device logs got = %d, want = 0", got)
		}
	})
	t.Run("invite of a new user does not log the first device", func(t *testing.T) {
		e := newEnv(t)
		ci := newInvite(t, e, e.org(), "new@hooli.com", permission.BuiltInRoleManager)
		out, err := e.invites.Accept(e.ctx, ci.Token, "pw", mac)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(e.devices(out.User.ID)); got != 1 {
			t.Errorf("devices got = %d, want = 1", got)
		}
		if got := e.log.count(EventNewDeviceLoggedIn); got != 0 {
			t.Errorf("new device logs got = %d, want = 0", got)
		}
	})
	t.Run("invite of an existing user logs a new device", func(t *testing.T) {
		e := newEnv(t)
		u := e.user("member@hooli.com", "pw")
		e.member(u, e.org(), permission.BuiltInRoleAdmin)
		ci := newInvite(t, e, e.org(), u.Email, permission.BuiltInRoleManager)
		if _, err := e.invites.Accept(e.ctx, ci.Token, "pw", mac); err != nil {
			t.Fatal(err)
		}
		if got := e.log.count(EventNewDeviceLoggedIn); got != 2 {
			t.Errorf("new device logs got = %d, want = 2 (one per active organization)", got)
		}
	})
}
