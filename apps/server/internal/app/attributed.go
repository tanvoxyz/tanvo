package app

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

var _ metering.AttributedUsageReader = (*EventStores)(nil)

// errAttributedUnavailable is an attributed usage query of an
// organization whose events are not in ClickHouse.
var errAttributedUnavailable = errors.New("app: attributed usage needs the ClickHouse event store")

// clickHouseOf returns the ClickHouse store when it holds org's events.
func (s *EventStores) clickHouseOf(org string) (metering.AttributedUsageReader, bool) {
	id, err := core.ParseID[core.OrganizationID](org)
	if err != nil || s.ch == nil || s.modeOf(id) != ingest.ModeKafka {
		return nil, false
	}
	return s.ch, true
}

// AttributedUsageAvailable implements [metering.AttributedUsageReader]:
// enrichment labels the events of the organizations ingesting through
// Kafka, stored in ClickHouse (parity §1.42).
func (s *EventStores) AttributedUsageAvailable(org string) bool {
	_, ok := s.clickHouseOf(org)
	return ok
}

// AttributedUsage implements [metering.AttributedUsageReader].
func (s *EventStores) AttributedUsage(ctx context.Context, q metering.AttributedQuery) (metering.AttributedUsage, error) {
	ch, ok := s.clickHouseOf(q.OrganizationID)
	if !ok {
		return metering.AttributedUsage{}, errAttributedUnavailable
	}
	return ch.AttributedUsage(ctx, q)
}

// noAttributedUsage is the reader of an API without event stores: the
// query is available to no organization.
type noAttributedUsage struct{}

func (noAttributedUsage) AttributedUsageAvailable(string) bool { return false }

func (noAttributedUsage) AttributedUsage(context.Context, metering.AttributedQuery) (metering.AttributedUsage, error) {
	return metering.AttributedUsage{}, errAttributedUnavailable
}
