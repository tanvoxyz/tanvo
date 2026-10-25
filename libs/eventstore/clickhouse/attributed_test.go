package clickhouse_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/eventstore/clickhouse"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/metering/storetest"
)

// TestAttributedUsage runs the attributed usage cases on the store, the
// events written with their attribution labels.
//
// The split cases check the cells of each charge, one per filter of a
// split charge and its default (the cells of metering.AttributedQuery).
//
// source: spec/services/events/stores/clickhouse/attributed_usage_query_spec.rb:83, 92, 106, 146, 156, 160, 115, 168, 176, 185, 193, 203, 216, 224, 241
func TestAttributedUsage(t *testing.T) {
	store := clickhouse.New(database(t))
	storetest.RunAttributed(t, func(t *testing.T, events []metering.Event) metering.AttributedUsageReader {
		truncate(t)
		if err := store.Replace(t.Context(), events); err != nil {
			t.Fatal(err)
		}
		return store
	})
}
