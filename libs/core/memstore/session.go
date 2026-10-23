package memstore

import (
	"bytes"
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
)

func cloneSession(s access.Session) access.Session {
	s.TokenHash = slices.Clone(s.TokenHash)
	if s.RevokedAt != nil {
		t := *s.RevokedAt
		s.RevokedAt = &t
	}
	return s
}

func cloneOIDCState(s access.OIDCState) access.OIDCState {
	s.KeyHash = slices.Clone(s.KeyHash)
	s.InviteTokenHash = slices.Clone(s.InviteTokenHash)
	return s
}

func cloneUserDevice(d access.UserDevice) access.UserDevice {
	for _, p := range []**string{&d.Browser, &d.OS, &d.DeviceType, &d.LastIPAddress} {
		if *p != nil {
			v := **p
			*p = &v
		}
	}
	return d
}

// sessionRepo implements access.SessionRepository.
type sessionRepo struct{ d *accessData }

func (r sessionRepo) Create(_ context.Context, s *access.Session) error {
	if r.d.sessions.has(s.ID) {
		return conflict("session", "id")
	}
	if r.d.sessions.any(func(x *access.Session) bool { return bytes.Equal(x.TokenHash, s.TokenHash) }) {
		return conflict("session", "token")
	}
	if !r.d.users.has(s.UserID) {
		return notFound("user", s.UserID)
	}
	r.d.sessions.put(s.ID, *s)
	return nil
}

func (r sessionRepo) GetByTokenHash(_ context.Context, tokenHash []byte) (*access.Session, error) {
	s, ok := r.d.sessions.first(func(x *access.Session) bool { return bytes.Equal(x.TokenHash, tokenHash) })
	if !ok {
		return nil, notFound("session", stringer("token"))
	}
	return s, nil
}

func (r sessionRepo) Touch(_ context.Context, id core.SessionID, at time.Time) error {
	s, ok := r.d.sessions.get(id)
	if !ok {
		return notFound("session", id)
	}
	s.LastSeenAt = at
	r.d.sessions.put(id, *s)
	return nil
}

func (r sessionRepo) Revoke(_ context.Context, id core.SessionID, at time.Time) error {
	s, ok := r.d.sessions.get(id)
	if !ok {
		return notFound("session", id)
	}
	if s.RevokedAt == nil {
		s.RevokedAt = &at
		r.d.sessions.put(id, *s)
	}
	return nil
}

func (r sessionRepo) RevokeByUser(_ context.Context, user core.UserID, keep core.SessionID, at time.Time) (int, error) {
	rows := r.d.sessions.find(func(x *access.Session) bool { return x.UserID == user && x.ID != keep && x.RevokedAt == nil })
	for _, s := range rows {
		s.RevokedAt = &at
		r.d.sessions.put(s.ID, *s)
	}
	return len(rows), nil
}

func (r sessionRepo) DeleteExpired(_ context.Context, before time.Time) (int, error) {
	return r.d.sessions.deleteWhere(func(x *access.Session) bool {
		return x.ExpiresAt.Before(before) || (x.RevokedAt != nil && x.RevokedAt.Before(before))
	}), nil
}

// oidcStateRepo implements access.OIDCStateRepository. States are keyed by
// their key hash; the map is copied by snapshots and its values are never
// mutated in place.
type oidcStateRepo struct{ d *accessData }

func (r oidcStateRepo) Create(_ context.Context, s *access.OIDCState) error {
	k := string(s.KeyHash)
	if _, ok := r.d.oidcStates[k]; ok {
		return conflict("oidc_state", "key")
	}
	r.d.oidcStates[k] = cloneOIDCState(*s)
	return nil
}

func (r oidcStateRepo) Take(_ context.Context, keyHash []byte, now time.Time) (*access.OIDCState, error) {
	k := string(keyHash)
	s, ok := r.d.oidcStates[k]
	delete(r.d.oidcStates, k)
	if !ok || s.Expired(now) {
		return nil, notFound("oidc_state", stringer("key"))
	}
	c := cloneOIDCState(s)
	return &c, nil
}

func (r oidcStateRepo) DeleteExpired(_ context.Context, before time.Time) (int, error) {
	n := 0
	for k, s := range r.d.oidcStates {
		if s.ExpiresAt.Before(before) {
			delete(r.d.oidcStates, k)
			n++
		}
	}
	return n, nil
}

// userDeviceRepo implements access.UserDeviceRepository.
type userDeviceRepo struct{ d *accessData }

func (r userDeviceRepo) duplicate(d *access.UserDevice) bool {
	return r.d.userDevices.any(func(x *access.UserDevice) bool {
		return x.UserID == d.UserID && x.Fingerprint == d.Fingerprint
	})
}

func (r userDeviceRepo) Create(_ context.Context, d *access.UserDevice) error {
	if r.d.userDevices.has(d.ID) {
		return conflict("user_device", "id")
	}
	if r.duplicate(d) {
		return conflict("user_device", "fingerprint")
	}
	if !r.d.users.has(d.UserID) {
		return notFound("user", d.UserID)
	}
	r.d.userDevices.put(d.ID, *d)
	return nil
}

// Update writes the client description and the last sign-in; the user,
// fingerprint and creation instant are immutable.
func (r userDeviceRepo) Update(_ context.Context, d *access.UserDevice) error {
	cur, ok := r.d.userDevices.get(d.ID)
	if !ok || cur.UserID != d.UserID {
		return notFound("user_device", d.ID)
	}
	cur.Browser, cur.OS, cur.DeviceType, cur.LastIPAddress = d.Browser, d.OS, d.DeviceType, d.LastIPAddress
	cur.LastLoggedAt, cur.UpdatedAt = d.LastLoggedAt, d.UpdatedAt
	r.d.userDevices.put(d.ID, *cur)
	return nil
}

func (r userDeviceRepo) GetByFingerprint(_ context.Context, user core.UserID, fingerprint string) (*access.UserDevice, error) {
	d, ok := r.d.userDevices.first(func(x *access.UserDevice) bool { return x.UserID == user && x.Fingerprint == fingerprint })
	if !ok {
		return nil, notFound("user_device", stringer(fingerprint))
	}
	return d, nil
}

func (r userDeviceRepo) ListByUser(_ context.Context, user core.UserID) ([]*access.UserDevice, error) {
	return r.d.userDevices.find(func(x *access.UserDevice) bool { return x.UserID == user }), nil
}
