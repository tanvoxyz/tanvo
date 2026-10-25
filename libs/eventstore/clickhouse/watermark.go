package clickhouse

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

var _ metering.IngestionWatermarks = (*Store)(nil)

// LastIngestedAt implements [metering.IngestionWatermarks]: the latest
// instant a selected event was ingested or its enriched copy written, so
// that a copy written again (a replacement, a re-enrichment) advances it
// even when it keeps the event's ingestion instant (lago reads
// MAX(enriched_at), app/services/events/stores/clickhouse_store.rb:160).
func (s *Store) LastIngestedAt(ctx context.Context, q metering.Query) (time.Time, error) {
	sel, err := newSelection(q)
	if err != nil {
		return time.Time{}, wrap("last ingested at", err)
	}
	var at *time.Time
	err = s.each(ctx, sel.with("SELECT greatest(maxOrNull(ingested_at), maxOrNull(enriched_at)) FROM sel"), sel.args, []any{&at}, func() error { return nil })
	if err != nil || at == nil {
		return time.Time{}, wrap("last ingested at", err)
	}
	return at.UTC(), nil
}
