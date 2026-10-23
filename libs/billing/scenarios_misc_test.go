package billing_test

import (
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestMiscPayInAdvanceSameTimestamp prices a batch of in-advance events
// sharing one timestamp, each at its own position in the graduated
// ranges. Lago runs it on the ClickHouse store; the scenario harness runs
// it on the memory store, the backends sharing the metering conformance
// suite.
func TestMiscPayInAdvanceSameTimestamp(t *testing.T) {
	// source: spec/scenarios/clickhouse/pay_in_advance_charges_spec.rb:15
	s := newScenario(t, billingtest.Date(2023, 1, 24))
	c := s.Customer()
	p := s.Plan(period.Monthly, 1000)
	sub := s.walSubscribe(c, p)
	m := s.Metric("count_metric", catalog.AggregationTypeCount, "")
	ch := s.Charge(p, m, catalog.ChargeModelGraduated,
		`{"graduated_ranges":[{"from_value":0,"to_value":2,"per_unit_amount":"0","flat_amount":"0"},{"from_value":3,"to_value":null,"per_unit_amount":"2","flat_amount":"0"}]}`,
		func(ch *catalog.Charge) { ch.PayInAdvance, ch.Invoiceable = true, false })
	ts := billingtest.Date(2023, 2, 15).Add(10 * time.Hour)
	// The whole batch is stored before any fee is computed.
	var ids []string
	for range 3 {
		ids = append(ids, s.Event(sub, m.Code, ts).TransactionID)
	}
	s.travelTo(ts.Add(time.Second))
	for _, id := range ids {
		_, err := s.E.Invoices.InAdvanceCharge(s.Ctx, s.OrgID(), kernel.InAdvanceChargeArgs{SubscriptionID: sub.ID, TransactionID: id, Timestamp: ts, Code: m.Code})
		s.must(err)
	}
	s.drain()
	var amounts []int64
	var txs []string
	for _, f := range s.walChargeFees(ch.ID) {
		amounts = append(amounts, f.Amount)
		txs = append(txs, f.PayInAdvanceEventTransactionID)
		if got := f.Units.String(); got != "1" {
			t.Errorf("fee units: got = %s, want = 1", got)
		}
	}
	slices.Sort(amounts)
	slices.Sort(txs)
	slices.Sort(ids)
	if !slices.Equal(amounts, []int64{0, 0, 200}) {
		t.Errorf("amounts: got = %v, want = [0 0 200]", amounts)
	}
	if !slices.Equal(txs, ids) {
		t.Errorf("fee transactions: got = %v, want = %v", txs, ids)
	}
}
