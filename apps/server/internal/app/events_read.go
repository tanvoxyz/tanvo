package app

import (
	"fmt"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/ingest"
)

// newEventReader returns the reader of the event API: the raw event log
// of postgres-mode organizations and the ClickHouse ingestion ledger of
// kafka-mode ones (ch, nil when ClickHouse is not configured).
func newEventReader(cfg config.Config, tx port.Transactor, modeOf func(core.OrganizationID) ingest.Mode, ch *clickhouse.Store) (*ingest.Reader, error) {
	rc := ingest.ReaderConfig{Transactor: tx, Mode: ingest.Mode(cfg.IngestMode), ModeFor: modeOf}
	if ch != nil {
		rc.Raw = ch
	}
	r, err := ingest.NewReader(rc)
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	return r, nil
}
