package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// The audit logs (parity §1.45) are stored in PostgreSQL without
// ClickHouse. With ClickHouse (TANVO_CLICKHOUSE_URL), the log tables of
// PostgreSQL hold each log from the transaction of its change until the
// workers move it to ClickHouse's log tables, and the repositories read
// both (architecture §7.4).

const (
	// logArchiveBatch is how many logs of each kind one move takes.
	logArchiveBatch = 1000
	// logArchiveInterval is the pause between moves once the tables are
	// drained, and after a failure.
	logArchiveInterval = time.Second
)

// logArchive returns the ClickHouse log archive over conn, nil without
// ClickHouse.
func logArchive(conn driver.Conn) auditlog.Archive {
	if conn == nil {
		return nil
	}
	return clickhouse.NewLogStore(conn)
}

// logArchiveOptions returns the store options of the log archive a.
func logArchiveOptions(a auditlog.Archive) []store.Option {
	if a == nil {
		return nil
	}
	return []store.Option{store.WithLogArchive(a)}
}

// runLogArchive moves the logs of s to a until ctx ends: batch after
// batch while full ones come, then every logArchiveInterval. Concurrent
// movers (one per worker process) move different logs. A failure is
// logged and retried; the logs stay readable meanwhile.
func runLogArchive(ctx context.Context, s *store.Store, a auditlog.Archive, log *slog.Logger) error {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		n, err := s.ArchiveLogs(ctx, a, logArchiveBatch)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil
		case err != nil:
			log.WarnContext(ctx, "audit logs: move to clickhouse", "error", err)
			t.Reset(logArchiveInterval)
		case n > 0:
			log.DebugContext(ctx, "audit logs: moved to clickhouse", "logs", n)
			t.Reset(0)
		default:
			t.Reset(logArchiveInterval)
		}
	}
}
