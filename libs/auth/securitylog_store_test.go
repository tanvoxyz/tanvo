package auth

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestRepoSecurityLogger(t *testing.T) {
	// source: app/services/utils/security_log.rb:42-62 (user and device
	// from the request context unless given).
	e := newEnv(t)
	o := e.org()
	actor := core.NewID[core.UserID](e.deps.IDs)
	device := map[string]any{"browser": "Firefox"}
	e.deps.SecurityLog = NewRepoSecurityLogger(e.store, e.clock, e.deps.IDs, func(context.Context) SecurityContext {
		return SecurityContext{UserID: actor, DeviceInfo: device}
	})
	if _, err := NewRoles(e.deps).Create(e.ctx, o.ID, RoleInput{Code: "dev", Name: "Dev", Permissions: []permission.Permission{"addons:read"}}); err != nil {
		t.Fatal(err)
	}
	explicit := core.NewID[core.UserID](e.deps.IDs)
	e.deps.record(e.ctx, SecurityEvent{OrganizationID: o.ID, UserID: explicit, Type: SecurityLogUser, Event: EventNewDeviceLoggedIn, DeviceInfo: map[string]any{"browser": "Safari"}})
	e.tx(func(ctx context.Context, r port.Repos) error {
		p, err := r.SecurityLogs.List(ctx, o.ID, auditlog.SecurityLogFilter{}, core.PageRequest{})
		if err != nil {
			return err
		}
		if len(p.Items) != 2 {
			t.Fatalf("logs got = %d, want = 2", len(p.Items))
		}
		byEvent := map[string]*auditlog.SecurityLog{}
		for _, l := range p.Items {
			byEvent[l.Event] = l
		}
		role := byEvent[EventRoleCreated]
		if role.UserID != actor || role.DeviceInfo["browser"] != "Firefox" || role.Type != SecurityLogRole || !role.LoggedAt.Equal(e.clock.Now()) {
			t.Errorf("role.created got = %+v, want the request user and device", role)
		}
		dev := byEvent[EventNewDeviceLoggedIn]
		if dev.UserID != explicit || dev.DeviceInfo["browser"] != "Safari" {
			t.Errorf("new device got = %+v, want the given user and device", dev)
		}
		return nil
	})
}
