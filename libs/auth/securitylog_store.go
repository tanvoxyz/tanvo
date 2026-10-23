package auth

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// SecurityContext is the request context a security event takes its
// missing actor and device from (lago CurrentContext membership and
// device_info).
type SecurityContext struct {
	// UserID is the user of the request; zero for API keys and jobs.
	UserID core.UserID
	// DeviceInfo describes the client of the request; nil when unknown.
	DeviceInfo map[string]any
}

// RepoSecurityLogger is the [SecurityLogger] storing events in
// port.Repos.SecurityLogs, each in its own transaction (lago
// Utils::SecurityLog.produce).
type RepoSecurityLogger struct {
	tx      port.Transactor
	clock   core.Clock
	ids     core.IDGenerator
	current func(context.Context) SecurityContext
}

var _ SecurityLogger = (*RepoSecurityLogger)(nil)

// NewRepoSecurityLogger returns a logger writing through tx. current, when
// not nil, gives the request context of events without a user or device
// (lago security_log.rb resolve_user_id and CurrentContext.device_info).
func NewRepoSecurityLogger(tx port.Transactor, clock core.Clock, ids core.IDGenerator, current func(context.Context) SecurityContext) *RepoSecurityLogger {
	return &RepoSecurityLogger{tx: tx, clock: clock, ids: ids, current: current}
}

// Record stores e.
func (l *RepoSecurityLogger) Record(ctx context.Context, e SecurityEvent) error {
	now := l.clock.Now().UTC()
	at := e.At.UTC()
	if e.At.IsZero() {
		at = now
	}
	entry := &auditlog.SecurityLog{
		ID: l.ids.NewUUID(), OrganizationID: e.OrganizationID, UserID: e.UserID,
		Type: e.Type, Event: e.Event, Resources: e.Resources, DeviceInfo: e.DeviceInfo,
		LoggedAt: at, CreatedAt: now,
	}
	if l.current != nil {
		c := l.current(ctx)
		if entry.UserID.IsZero() {
			entry.UserID = c.UserID
		}
		if entry.DeviceInfo == nil {
			entry.DeviceInfo = c.DeviceInfo
		}
	}
	err := l.tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return r.SecurityLogs.Create(ctx, entry)
	})
	if err != nil {
		return fmt.Errorf("record security log: %w", err)
	}
	return nil
}
