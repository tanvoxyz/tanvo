package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/tanvoxyz/tanvo/apps/server/internal/logctx"
	"github.com/tanvoxyz/tanvo/libs/eventstore"
)

// bucketRowsWarning is the largest number of rows of one bucket of one
// subscription (its combinations of dimension values) the bucket report
// accepts without a warning: past it, the definition's dimensions are
// unbounded, and dropping it sends its reads back to the events.
const bucketRowsWarning = 10_000

// bucketStatsReport reports the size of the usage buckets of every
// definition of the event stores: histograms of their rows, by store
// (organizations are on the logs, never on metrics), and a warning per
// definition past [bucketRowsWarning].
type bucketStatsReport struct {
	stores  map[string]eventstore.BucketDefiner
	rows    metric.Int64Histogram
	maxRows metric.Int64Histogram
}

func newBucketStatsReport(s *EventStores, mp metric.MeterProvider) (*bucketStatsReport, error) {
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
	r := &bucketStatsReport{stores: map[string]eventstore.BucketDefiner{"postgres": s.pg}, rows: rows, maxRows: maxRows}
	if s.ch != nil {
		r.stores["clickhouse"] = s.ch
	}
	return r, nil
}

// run reports every definition of every store; a failing definition is
// logged and the report goes on.
func (r *bucketStatsReport) run(ctx context.Context) error {
	var errs []error
	for name, s := range r.stores {
		defs, err := s.AllBucketDefinitions(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		attrs := metric.WithAttributes(attribute.String("tanvo.eventstore", name))
		for _, d := range defs {
			st, err := s.BucketStats(ctx, d)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			r.rows.Record(ctx, st.Rows, attrs)
			r.maxRows.Record(ctx, st.MaxRowsPerBucket, attrs)
			if st.MaxRowsPerBucket > bucketRowsWarning {
				slog.WarnContext(ctx, "eventstore: usage buckets with unbounded dimensions",
					logctx.OrganizationKey, d.OrganizationID, "store", name, "code", d.Code, "definition", d.ID(),
					"rows", st.Rows, "max_rows_per_bucket", st.MaxRowsPerBucket)
			}
		}
		slog.InfoContext(ctx, "eventstore: reported usage buckets", "store", name, "definitions", len(defs))
	}
	return errors.Join(errs...)
}
