package access

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// UserDevice is a client a user has signed in from (domain §5.8),
// identified per user by the fingerprint of its user agent. A sign-in
// from a new device is recorded in the security log.
type UserDevice struct {
	// ID identifies the device.
	ID core.UserDeviceID
	// UserID is the user.
	UserID core.UserID
	// Fingerprint is [DeviceFingerprint] of the user agent; unique per
	// user.
	Fingerprint string
	// Browser is the browser name and version, if known.
	Browser *string
	// OS is the operating system, if known.
	OS *string
	// DeviceType is the kind of device (desktop, smartphone, …), if
	// known.
	DeviceType *string
	// LastIPAddress is the client address of the last sign-in.
	LastIPAddress *string
	// LastLoggedAt is the instant of the last sign-in from the device.
	LastLoggedAt time.Time
	// CreatedAt is the first sign-in from the device.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// DeviceFingerprint returns the fingerprint of a user agent: its SHA-256
// in lower-case hex.
func DeviceFingerprint(userAgent string) string {
	h := sha256.Sum256([]byte(userAgent))
	return hex.EncodeToString(h[:])
}

// NewUserDevice returns the device of user with the user agent's
// fingerprint, signed in at now.
func NewUserDevice(id core.UserDeviceID, user core.UserID, userAgent string, now time.Time) *UserDevice {
	return &UserDevice{ID: id, UserID: user, Fingerprint: DeviceFingerprint(userAgent), LastLoggedAt: now, CreatedAt: now, UpdatedAt: now}
}

// Validate checks the invariants of domain §5.8.
func (d *UserDevice) Validate() error {
	var v core.Validator
	v.Check(!d.UserID.IsZero(), "user_id", core.CodeRequired)
	v.Required("fingerprint", d.Fingerprint)
	v.Check(!d.LastLoggedAt.IsZero(), "last_logged_at", core.CodeRequired)
	return v.Err()
}

// SignedIn records a sign-in from the device at now, with the client's
// description (blank values are kept as nil).
func (d *UserDevice) SignedIn(browser, os, deviceType, ip string, now time.Time) {
	d.Browser, d.OS, d.DeviceType, d.LastIPAddress = optional(browser), optional(os), optional(deviceType), optional(ip)
	d.LastLoggedAt = now
	d.UpdatedAt = now
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
