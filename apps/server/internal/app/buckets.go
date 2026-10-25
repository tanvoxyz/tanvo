package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/tanvoxyz/tanvo/apps/server/internal/logctx"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/eventstore"
)

// bucketRowsWarning is the number of rows of one bucket of one
// subscription past which the bucket report warns when no limit blocks
// the definitions (TANVO_USAGE_BUCKETS_MAX_ROWS_PER_BUCKET=0).
const bucketRowsWarning = 10_000

// namedBucketStore is an event store keeping usage buckets, with its name
// for the logs and metrics.
type namedBucketStore struct {
	name  string
	store eventstore.BucketDefiner
}

// bucketStores returns the event stores keeping usage buckets, PostgreSQL
// first.
func bucketStores(s *EventStores) []namedBucketStore {
	out := []namedBucketStore{{"postgres", s.pg}}
	if s.ch != nil {
		out = append(out, namedBucketStore{"clickhouse", s.ch})
	}
	return out
}

// bucketStatsReport reports the size of the usage buckets of every
// definition of the event stores: histograms of their rows, by store
// (organizations are on the logs, never on metrics). A definition past
// the limit of rows per bucket is blocked
// ([metering.BucketLifecycle.BlockBuckets]), as its dimensions are
// unbounded; without a limit, it is reported by a warning.
type bucketStatsReport struct {
	stores  []namedBucketStore
	limit   int64
	rows    metric.Int64Histogram
	maxRows metric.Int64Histogram
}

func newBucketStatsReport(s *EventStores, limit int64, mp metric.MeterProvider) (*bucketStatsReport, error) {
	m := mp.Meter(scopeName)
	rows, err := m.Int64Histogram("tanvo.eventstore.bucket_definition.rows",
		metric.WithDescription("Rows of the usage buckets of a definition"), metric.WithUnit("{row}"))
	if err != nil {
		return nil, fmt.Errorf("bucket stats: %w", err)
	}
	maxRows, err := m.Int64Histogram("tanvo.eventstore.bucket_definition.max_bucket_rows",
		metric.WithDescription("Largest number of rows of one bucket of one subscription of a definition"), metric.WithUnit("{row}"))
	if err != nil {
		return nil, fmt.Errorf("bucket stats: %w", err)
	}
	return &bucketStatsReport{stores: bucketStores(s), limit: limit, rows: rows, maxRows: maxRows}, nil
}

// run reports every definition of every store, blocking those past the
// limit; a failing definition is logged and the report goes on.
func (r *bucketStatsReport) run(ctx context.Context) error {
	var errs []error
	for _, ns := range r.stores {
		name, s := ns.name, ns.store
		defs, err := s.AllBucketDefinitions(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		attrs := metric.WithAttributes(attribute.String("tanvo.eventstore", name))
		blocked := 0
		for _, d := range defs {
			st, err := s.BucketStats(ctx, d)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			r.rows.Record(ctx, st.Rows, attrs)
			r.maxRows.Record(ctx, st.MaxRowsPerBucket, attrs)
			log := []any{
				logctx.OrganizationKey, d.OrganizationID, "store", name, "code", d.Code, "definition", d.ID(),
				"rows", st.Rows, "max_rows_per_bucket", st.MaxRowsPerBucket,
			}
			switch {
			case r.limit > 0 && st.MaxRowsPerBucket > r.limit:
				if err := s.BlockBuckets(ctx, d); err != nil {
					errs = append(errs, err)
					continue
				}
				blocked++
				slog.WarnContext(ctx, "eventstore: blocked usage buckets with unbounded dimensions", append(log, "limit", r.limit)...)
			case r.limit == 0 && st.MaxRowsPerBucket > bucketRowsWarning:
				slog.WarnContext(ctx, "eventstore: usage buckets with unbounded dimensions", log...)
			}
		}
		slog.InfoContext(ctx, "eventstore: reported usage buckets", "store", name, "definitions", len(defs), "blocked", blocked)
	}
	return errors.Join(errs...)
}

// bucketSweep is the daily dispatch of the usage bucket sweeps
// ([kernel.JobSweepUsageBuckets]): one per organization with definitions
// in the store it ingests into. The definitions an organization left in
// the other store (its ingestion mode changed) are read by nothing, so
// that the dispatch drops them itself.
type bucketSweep struct {
	stores *EventStores
	tx     port.Transactor
}

// run dispatches the sweeps at tick, and returns the number of
// organizations swept.
func (b bucketSweep) run(ctx context.Context, tick time.Time) (int, error) {
	var (
		orgs []core.OrganizationID
		errs []error
	)
	for _, ns := range bucketStores(b.stores) {
		states, err := ns.store.BucketDefinitionStates(ctx, "")
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, st := range states {
			org, err := core.ParseID[core.OrganizationID](st.Definition.OrganizationID)
			if err != nil {
				errs = append(errs, fmt.Errorf("usage buckets of organization %q: %w", st.Definition.OrganizationID, err))
				continue
			}
			routed := b.stores.For(org)
			if routed == nil {
				continue
			}
			if any(routed) != any(ns.store) {
				if err := ns.store.DropBuckets(ctx, st.Definition); err != nil {
					errs = append(errs, err)
					continue
				}
				slog.InfoContext(ctx, "eventstore: dropped usage buckets of a store the organization left",
					logctx.OrganizationKey, org.String(), "store", ns.name, "code", st.Definition.Code, "definition", st.Definition.ID())
				continue
			}
			if !slices.Contains(orgs, org) {
				orgs = append(orgs, org)
			}
		}
	}
	err := b.tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		for _, org := range orgs {
			if err := kernel.EnqueueUsageBucketsSweep(ctx, r, org, tick); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("usage buckets: dispatch sweeps: %w", err))
	}
	return len(orgs), errors.Join(errs...)
}
