package postgres

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

var _ metering.IngestionWatermarks = (*Store)(nil)

// LastIngestedAt implements [metering.IngestionWatermarks]: the latest
// write of the selected events: the later of their ingestion instants and
// of the instants their rows were written, so that a replacement keeping
// the ingestion instant (a re-enrichment) advances it too.
func (s *Store) LastIngestedAt(ctx context.Context, q metering.Query) (time.Time, error) {
	sel, err := newSelection(q)
	if err != nil {
		return time.Time{}, wrap("last ingested at", err)
	}
	var at *time.Time
	if err := s.db.QueryRow(ctx, sel.with("SELECT greatest(max(ingested_at), max(written_at)) FROM sel"), sel.args...).Scan(&at); err != nil || at == nil {
		return time.Time{}, wrap("last ingested at", err)
	}
	return at.UTC(), nil
}
