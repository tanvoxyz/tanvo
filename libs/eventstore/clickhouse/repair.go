package clickhouse

import (
	"context"
	"fmt"
	"time"
)

// RepairMargin is the margin [Store.RepairRecentBuckets] repairs before the
// start of the previous run: above the longest write and the clock skew
// between writers.
const RepairMargin = 10 * time.Minute

// RepairRecentBuckets runs [Store.RepairBuckets] since the start of the
// previous completed run minus margin, then records the start of this run
// in bucket_repairs, so that the schedule survives restarts and runs on any
// node. Without a previous run (a new deployment, or none in the 30 days
// bucket_repairs keeps), it repairs every bucket. A failed run records
// nothing: the next one repairs from the same point.
func (s *Store) RepairRecentBuckets(ctx context.Context, margin time.Duration) error {
	ctx = consistent(ctx)
	started := s.sequences(1)
	var prev uint64
	if err := s.each(ctx, `SELECT max(started) FROM bucket_repairs`, nil, []any{&prev}, func() error { return nil }); err != nil {
		return wrap("repair recent buckets", err)
	}
	since := time.Time{}
	if prev > 0 {
		since = time.Unix(0, int64(prev)).Add(-margin)
	}
	if err := s.RepairBuckets(ctx, since); err != nil {
		return err
	}
	if err := s.conn.Exec(ctx, `INSERT INTO bucket_repairs (started, finished) VALUES ($1, $2)`, started, s.now().UTC()); err != nil {
		return wrap("repair recent buckets", fmt.Errorf("record run: %w", err))
	}
	return nil
}
