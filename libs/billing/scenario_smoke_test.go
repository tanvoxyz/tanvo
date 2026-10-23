package billing_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestScenarioSmoke bills a monthly in-arrears plan with a sum charge
// through the engine's clock jobs.
func TestScenarioSmoke(t *testing.T) {
	s := newScenario(t, billingtest.Date(2024, 1, 1))
	c := s.Customer()
	m := s.Metric("api", catalog.AggregationTypeSum, "n")
	p := s.Plan(period.Monthly, 1000)
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := s.Subscription(c, p)
	s.travelTo(billingtest.Date(2024, 1, 10))
	s.ingest(sub, "api", "n", "5")
	s.travelTo(billingtest.Date(2024, 2, 1, 1))
	s.clock()
	invs := s.Invoices(c.ID)
	if len(invs) != 1 {
		t.Fatalf("invoices = %d, want = 1", len(invs))
	}
	if got, want := invs[0].TotalAmount, int64(1500); got != want {
		t.Errorf("total = %d, want = %d", got, want)
	}
}
