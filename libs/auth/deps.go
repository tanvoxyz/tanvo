package auth

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Deps are the dependencies shared by the services.
type Deps struct {
	// Tx opens transactions over the access repositories.
	Tx port.Transactor
	// Clock is the time source.
	Clock core.Clock
	// IDs generates identifiers.
	IDs core.IDGenerator
	// Rand is the source of randomness (crypto/rand.Reader in production).
	Rand io.Reader
	// SecurityLog receives security events; nil discards them.
	SecurityLog SecurityLogger
	// Logger logs failures that do not fail the operation; nil uses
	// slog.Default.
	Logger *slog.Logger
}

func (d Deps) now() time.Time { return d.Clock.Now().UTC() }

func (d Deps) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// record sends events to the security log after the operation committed;
// a failure is logged, not returned, as in lago.
func (d Deps) record(ctx context.Context, events ...SecurityEvent) {
	if d.SecurityLog == nil {
		return
	}
	for _, e := range events {
		if e.At.IsZero() {
			e.At = d.now()
		}
		if err := d.SecurityLog.Record(ctx, e); err != nil {
			d.logger().WarnContext(ctx, "security log", "event", e.Event, "organization_id", e.OrganizationID.String(), "error", err)
		}
	}
}
