package clickhouse

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// TestAttributedSQLFinal checks the query reads the current copy of each
// event: the store always deduplicates (parity §1.42).
//
// source: spec/services/events/stores/clickhouse/attributed_usage_query_spec.rb:126
func TestAttributedSQLFinal(t *testing.T) {
	q := metering.AttributedQuery{
		OrganizationID: "org", SubscriptionID: "sub", GroupKey: "team", Limit: 50,
		Charges: []metering.AttributedCharge{{ChargeID: "c", Code: "requests", Count: true}},
	}
	sql, _ := attributedSQL(q, q.Cells(), attributedOrders[metering.AttributedOrderEvents])
	if !strings.Contains(sql, "FROM events_enriched FINAL") {
		t.Errorf("statement got = %s, want = a read of events_enriched FINAL", sql)
	}
}
