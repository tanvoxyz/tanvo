package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// registerDevice records the sign-in of user from the client of meta
// (lago UserDevices::RegisterService). A client without a user agent is
// not tracked. The first sign-in from a device is logged as
// [EventNewDeviceLoggedIn] in every active organization of the user,
// unless skipLog: sign-ups and new users joining by invite are covered by
// their own events. A failure is logged, not returned: the session is
// already open.
func (u *Users) registerDevice(ctx context.Context, user *access.User, meta SessionMeta, skipLog bool) {
	if meta.UserAgent == "" {
		return
	}
	var (
		created bool
		ms      []*access.Membership
	)
	err := u.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		now := u.d.now()
		dev, err := r.UserDevices.GetByFingerprint(ctx, user.ID, access.DeviceFingerprint(meta.UserAgent))
		switch {
		case errors.Is(err, core.ErrNotFound):
			created = true
			dev = access.NewUserDevice(core.NewID[core.UserDeviceID](u.d.IDs), user.ID, meta.UserAgent, now)
		case err != nil:
			return fmt.Errorf("get device: %w", err)
		}
		dev.SignedIn(meta.Browser, meta.OS, meta.DeviceType, meta.IP, now)
		if created {
			if err := r.UserDevices.Create(ctx, dev); err != nil {
				return fmt.Errorf("create device: %w", err)
			}
		} else if err := r.UserDevices.Update(ctx, dev); err != nil {
			return fmt.Errorf("update device: %w", err)
		}
		if created && !skipLog {
			if ms, err = r.Memberships.ListActiveByUser(ctx, user.ID); err != nil {
				return fmt.Errorf("list memberships: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		u.d.logger().WarnContext(ctx, "register device", "error", err)
		return
	}
	for _, m := range ms {
		u.d.record(ctx, SecurityEvent{OrganizationID: m.OrganizationID, UserID: user.ID, Type: SecurityLogUser, Event: EventNewDeviceLoggedIn, Resources: map[string]any{
			"user_agent": meta.UserAgent, "ip_address": meta.IP, "browser": meta.Browser, "os": meta.OS, "device_type": meta.DeviceType,
		}})
	}
}
