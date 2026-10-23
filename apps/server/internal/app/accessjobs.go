package app

import (
	"context"
	"crypto/rand"
	"log/slog"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

// sessionRetention is how long ended sessions are kept before the purge
// deletes them.
const sessionRetention = 24 * time.Hour

// registerAccessPurges registers the session purge clock job and the
// idempotency key purge clock job with its per-organization jobs.
func (h jobHandlers) registerAccessPurges(reg *jobs.Registry) error {
	if err := jobs.Register(reg, jobs.Kind{Name: jobs.KindPurgeSessions},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, j *jobs.Job[struct{}]) error {
			// Sessions have no organization: the clock job deletes them
			// itself, in one indexed delete.
			sessions := auth.NewSessions(auth.Deps{
				Tx: h.tx, Clock: core.FixedClock(j.ScheduledAt), IDs: core.UUIDv7Generator{}, Rand: rand.Reader,
			}, auth.DefaultSessionConfig())
			s, st, err := sessions.Purge(ctx, sessionRetention)
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "auth: purged sessions", "sessions", s, "oidc_states", st)
			return nil
		})); err != nil {
		return err
	}
	if err := jobs.Register(reg, jobs.Kind{Name: jobs.KindPurgeIdempotencyKeys},
		jobs.HandlerFunc[struct{}](func(ctx context.Context, j *jobs.Job[struct{}]) error {
			_, err := rpc.DispatchIdempotencyPurges(ctx, h.tx, j.ScheduledAt)
			return err
		})); err != nil {
		return err
	}
	return jobs.Register(reg, jobs.Kind{Name: rpc.JobKindPurgeIdempotencyKeys},
		jobs.HandlerFunc[rpc.PurgeArgs](func(ctx context.Context, j *jobs.Job[rpc.PurgeArgs]) error {
			n, err := rpc.PurgeIdempotencyKeys(ctx, h.tx, j.OrganizationID, j.Args)
			if err != nil {
				return err
			}
			slog.InfoContext(ctx, "rpc: purged idempotency keys", "count", n)
			return nil
		}))
}
