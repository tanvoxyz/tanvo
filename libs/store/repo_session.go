package store

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// sessionRepo implements access.SessionRepository. Sessions are global:
// every method runs with queriesAll (the table has no row-level security).
type sessionRepo struct{ t *txState }

var _ access.SessionRepository = sessionRepo{}

func sessionOf(r db.Session) (*access.Session, error) {
	return &access.Session{
		ID:         idOf[core.SessionID](r.ID),
		UserID:     idOf[core.UserID](r.UserID),
		TokenHash:  r.TokenHash,
		Method:     organization.AuthenticationMethod(r.Method),
		UserAgent:  r.UserAgent,
		IP:         r.Ip,
		CreatedAt:  utc(r.CreatedAt),
		LastSeenAt: utc(r.LastSeenAt),
		ExpiresAt:  utc(r.ExpiresAt),
		RevokedAt:  optUTC(r.RevokedAt),
	}, nil
}

func (r sessionRepo) Create(ctx context.Context, s *access.Session) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	n, err := q.CreateSession(ctx, db.CreateSessionParams{
		ID:         s.ID.UUID(),
		UserID:     s.UserID.UUID(),
		TokenHash:  s.TokenHash,
		Method:     string(s.Method),
		UserAgent:  s.UserAgent,
		Ip:         s.IP,
		CreatedAt:  utc(s.CreatedAt),
		LastSeenAt: utc(s.LastSeenAt),
		ExpiresAt:  utc(s.ExpiresAt),
		RevokedAt:  optUTC(s.RevokedAt),
	})
	return insertErr("session", n, err)
}

func (r sessionRepo) GetByTokenHash(ctx context.Context, tokenHash []byte) (*access.Session, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	row, err := q.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, getErr("session", "token", err)
	}
	return sessionOf(row)
}

func (r sessionRepo) Touch(ctx context.Context, id core.SessionID, at time.Time) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	n, err := q.TouchSession(ctx, db.TouchSessionParams{LastSeenAt: utc(at), ID: id.UUID()})
	return updateErr("session", id, n, err)
}

func (r sessionRepo) Revoke(ctx context.Context, id core.SessionID, at time.Time) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	n, err := q.RevokeSession(ctx, db.RevokeSessionParams{RevokedAt: utc(at), ID: id.UUID()})
	return updateErr("session", id, n, err)
}

func (r sessionRepo) RevokeByUser(ctx context.Context, user core.UserID, keep core.SessionID, at time.Time) (int, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return 0, err
	}
	at = utc(at)
	n, err := q.RevokeUserSessions(ctx, db.RevokeUserSessionsParams{RevokedAt: &at, UserID: user.UUID(), Keep: keep.UUID()})
	if err != nil {
		return 0, fmt.Errorf("revoke sessions of user %s: %w", user, mapErr(err))
	}
	return int(n), nil
}

func (r sessionRepo) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return 0, err
	}
	n, err := q.DeleteExpiredSessions(ctx, utc(before))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", mapErr(err))
	}
	return int(n), nil
}

// oidcStateRepo implements access.OIDCStateRepository; states are global.
type oidcStateRepo struct{ t *txState }

var _ access.OIDCStateRepository = oidcStateRepo{}

func (r oidcStateRepo) Create(ctx context.Context, s *access.OIDCState) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	n, err := q.CreateOIDCState(ctx, db.CreateOIDCStateParams{
		KeyHash:          s.KeyHash,
		Method:           string(s.Method),
		Intent:           string(s.Intent),
		ConnectionID:     s.ConnectionID,
		Email:            s.Email,
		InviteTokenHash:  s.InviteTokenHash,
		OrganizationName: s.OrganizationName,
		Nonce:            s.Nonce,
		Verifier:         s.Verifier,
		ExpiresAt:        utc(s.ExpiresAt),
		CreatedAt:        utc(s.CreatedAt),
	})
	return insertErr("oidc_state", n, err)
}

func (r oidcStateRepo) Take(ctx context.Context, keyHash []byte, now time.Time) (*access.OIDCState, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	row, err := q.TakeOIDCState(ctx, keyHash)
	if err != nil {
		return nil, getErr("oidc_state", "key", err)
	}
	s := &access.OIDCState{
		KeyHash:          row.KeyHash,
		Method:           organization.AuthenticationMethod(row.Method),
		Intent:           access.OIDCIntent(row.Intent),
		ConnectionID:     row.ConnectionID,
		Email:            row.Email,
		InviteTokenHash:  row.InviteTokenHash,
		OrganizationName: row.OrganizationName,
		Nonce:            row.Nonce,
		Verifier:         row.Verifier,
		ExpiresAt:        utc(row.ExpiresAt),
		CreatedAt:        utc(row.CreatedAt),
	}
	if s.Expired(now) {
		return nil, notFound("oidc_state", "key")
	}
	return s, nil
}

func (r oidcStateRepo) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return 0, err
	}
	n, err := q.DeleteExpiredOIDCStates(ctx, utc(before))
	if err != nil {
		return 0, fmt.Errorf("delete expired oidc states: %w", mapErr(err))
	}
	return int(n), nil
}

// userDeviceRepo implements access.UserDeviceRepository; devices are
// global, like users.
type userDeviceRepo struct{ t *txState }

var _ access.UserDeviceRepository = userDeviceRepo{}

func userDeviceParams(d *access.UserDevice) db.CreateUserDeviceParams {
	return db.CreateUserDeviceParams{
		ID:            d.ID.UUID(),
		UserID:        d.UserID.UUID(),
		Fingerprint:   d.Fingerprint,
		Browser:       d.Browser,
		Os:            d.OS,
		DeviceType:    d.DeviceType,
		LastIpAddress: d.LastIPAddress,
		LastLoggedAt:  utc(d.LastLoggedAt),
		CreatedAt:     utc(d.CreatedAt),
		UpdatedAt:     utc(d.UpdatedAt),
	}
}

func userDeviceOf(r db.UserDevice) (*access.UserDevice, error) {
	return &access.UserDevice{
		ID:            idOf[core.UserDeviceID](r.ID),
		UserID:        idOf[core.UserID](r.UserID),
		Fingerprint:   r.Fingerprint,
		Browser:       r.Browser,
		OS:            r.Os,
		DeviceType:    r.DeviceType,
		LastIPAddress: r.LastIpAddress,
		LastLoggedAt:  utc(r.LastLoggedAt),
		CreatedAt:     utc(r.CreatedAt),
		UpdatedAt:     utc(r.UpdatedAt),
	}, nil
}

func (r userDeviceRepo) Create(ctx context.Context, d *access.UserDevice) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	n, err := q.CreateUserDevice(ctx, userDeviceParams(d))
	return insertErr("user_device", n, err)
}

func (r userDeviceRepo) Update(ctx context.Context, d *access.UserDevice) error {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return err
	}
	n, err := q.UpdateUserDevice(ctx, db.UpdateUserDeviceParams{
		Browser:       d.Browser,
		Os:            d.OS,
		DeviceType:    d.DeviceType,
		LastIpAddress: d.LastIPAddress,
		LastLoggedAt:  utc(d.LastLoggedAt),
		UpdatedAt:     utc(d.UpdatedAt),
		ID:            d.ID.UUID(),
		UserID:        d.UserID.UUID(),
	})
	return updateErr("user_device", d.ID, n, err)
}

func (r userDeviceRepo) GetByFingerprint(ctx context.Context, user core.UserID, fingerprint string) (*access.UserDevice, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	row, err := q.GetUserDeviceByFingerprint(ctx, db.GetUserDeviceByFingerprintParams{UserID: user.UUID(), Fingerprint: fingerprint})
	if err != nil {
		return nil, getErr("user_device", fingerprint, err)
	}
	return userDeviceOf(row)
}

func (r userDeviceRepo) ListByUser(ctx context.Context, user core.UserID) ([]*access.UserDevice, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListUserDevices(ctx, user.UUID())
	if err != nil {
		return nil, fmt.Errorf("list devices of user %s: %w", user, mapErr(err))
	}
	return convAll(rows, userDeviceOf)
}
