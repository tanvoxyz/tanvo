package billing_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// subsBounds are the expected boundaries of an invoice subscription, in
// lago's notation: from, to, charges from, charges to, fixed charges from,
// fixed charges to; "" is unchecked and "nil" an absent period.
type subsBounds [6]string

// subsBoundaryStep is a perform_billing at an instant.
type subsBoundaryStep struct {
	at     string
	bill   bool
	bounds subsBounds
}

// checkSubsBounds compares the boundaries of is with want.
func checkSubsBounds(t *testing.T, what string, is *invoice.Subscription, want subsBounds) {
	t.Helper()
	got := [6]period.Period{is.Period, is.Period, is.ChargesPeriod, is.ChargesPeriod, is.FixedChargesPeriod, is.FixedChargesPeriod}
	names := [6]string{"from", "to", "charges_from", "charges_to", "fixed_charges_from", "fixed_charges_to"}
	for i, w := range want {
		if w == "" {
			continue
		}
		if w == "nil" {
			if !got[i].IsZero() {
				t.Errorf("%s %s: got = %v, want = nil", what, names[i], got[i])
			}
			continue
		}
		v := got[i].From
		if i%2 == 1 {
			v = got[i].To
		}
		if want := subsTime(w); !v.Equal(want) {
			t.Errorf("%s %s: got = %s, want = %s", what, names[i], v.Format("2006-01-02T15:04:05Z"), want.Format("2006-01-02T15:04:05Z"))
		}
	}
}

func TestScenarioSubscriptionBillingBoundaries(t *testing.T) {
	tests := []struct {
		source                                  string
		interval                                period.Interval
		inAdvance, monthlyCharges, monthlyFixed bool
		created                                 string
		events                                  []string
		initial                                 int
		initialBounds                           subsBounds
		steps                                   []subsBoundaryStep
	}{
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:38", interval: period.Monthly,
			inAdvance: false, monthlyCharges: false, monthlyFixed: false, created: "2024-01-31T01:00:00Z",
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-02-28T23:59:59Z", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z"}},
				{at: "2024-03-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z"}},
				{at: "2024-04-30T02:00:00Z", bill: true, bounds: subsBounds{"2024-03-31T00:00:00Z", "2024-04-29T23:59:59Z", "2024-03-31T00:00:00Z", "2024-04-29T23:59:59Z", "2024-03-31T00:00:00Z", "2024-04-29T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:99", interval: period.Monthly,
			inAdvance: true, monthlyCharges: false, monthlyFixed: false, created: "2024-01-30T00:00:00Z",
			initial: 1, initialBounds: subsBounds{"2024-01-30T00:00:00Z", "2024-02-28T23:59:59Z", "", "", "", ""},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-02-29T00:00:00Z", "2024-03-29T23:59:59Z", "2024-01-30T00:00:00Z", "2024-02-28T23:59:59Z", "2024-01-30T00:00:00Z", "2024-02-28T23:59:59Z"}},
				{at: "2024-03-30T02:00:00Z", bill: true, bounds: subsBounds{"2024-03-30T00:00:00Z", "2024-04-29T23:59:59Z", "2024-02-29T00:00:00Z", "2024-03-29T23:59:59Z", "2024-02-29T00:00:00Z", "2024-03-29T23:59:59Z"}},
				{at: "2024-04-30T02:00:00Z", bill: true, bounds: subsBounds{"2024-04-30T00:00:00Z", "2024-05-29T23:59:59Z", "2024-03-30T00:00:00Z", "2024-04-29T23:59:59Z", "2024-03-30T00:00:00Z", "2024-04-29T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:168", interval: period.Yearly,
			inAdvance: false, monthlyCharges: false, monthlyFixed: false, created: "2024-01-31T01:00:00Z",
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z"},
				{at: "2024-03-31T02:00:00Z"},
				{at: "2024-04-30T02:00:00Z"},
				{at: "2025-01-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2025-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2025-01-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:214", interval: period.Yearly,
			inAdvance: false, monthlyCharges: true, monthlyFixed: false, created: "2024-01-31T01:00:00Z",
			events: []string{"2024-02-01T00:00:00Z"},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z", "nil", "nil"}},
				{at: "2024-03-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-01-31T01:00:00Z", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z", "nil", "nil"}},
				{at: "2024-04-30T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-01-31T01:00:00Z", "2024-03-31T00:00:00Z", "2024-04-29T23:59:59Z", "nil", "nil"}},
				{at: "2025-01-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-12-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2025-01-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:305", interval: period.Yearly,
			inAdvance: false, monthlyCharges: false, monthlyFixed: true, created: "2024-01-31T01:00:00Z",
			events: []string{"2024-02-01T00:00:00Z"},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-01-31T01:00:00Z", "nil", "nil", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z"}},
				{at: "2024-03-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-01-31T01:00:00Z", "nil", "nil", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z"}},
				{at: "2024-04-30T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-01-31T01:00:00Z", "nil", "nil", "2024-03-31T00:00:00Z", "2024-04-29T23:59:59Z"}},
				{at: "2025-01-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2025-01-30T23:59:59Z", "2024-12-31T00:00:00Z", "2025-01-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:396", interval: period.Yearly,
			inAdvance: false, monthlyCharges: true, monthlyFixed: true, created: "2024-01-31T01:00:00Z",
			events: []string{"2024-02-01T00:00:00Z"},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z"}},
				{at: "2024-03-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-01-31T01:00:00Z", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z"}},
				{at: "2025-01-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-12-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-12-31T00:00:00Z", "2025-01-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:472", interval: period.Yearly,
			inAdvance: true, monthlyCharges: true, monthlyFixed: false, created: "2024-01-31T01:00:00Z",
			events:  []string{"2024-02-01T00:00:00Z"},
			initial: 1, initialBounds: subsBounds{"2024-01-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z"},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z", "nil", "nil"}},
				{at: "2025-01-31T02:00:00Z", bill: true, bounds: subsBounds{"2025-01-31T00:00:00Z", "2026-01-30T23:59:59Z", "2024-12-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2025-01-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:545", interval: period.Yearly,
			inAdvance: true, monthlyCharges: false, monthlyFixed: true, created: "2024-01-31T01:00:00Z",
			initial: 1, initialBounds: subsBounds{"2024-01-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z"},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2025-01-30T23:59:59Z", "nil", "nil", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z"}},
				{at: "2025-01-31T02:00:00Z", bill: true, bounds: subsBounds{"2025-01-31T00:00:00Z", "2026-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2025-01-30T23:59:59Z", "2024-12-31T00:00:00Z", "2025-01-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:605", interval: period.Semiannual,
			inAdvance: false, monthlyCharges: false, monthlyFixed: false, created: "2024-01-31T01:00:00Z",
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z"},
				{at: "2024-03-31T02:00:00Z"},
				{at: "2024-04-30T02:00:00Z"},
				{at: "2024-05-31T02:00:00Z"},
				{at: "2024-06-30T02:00:00Z"},
				{at: "2024-07-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-07-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-07-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-07-30T23:59:59Z"}},
				{at: "2024-08-30T02:00:00Z"},
				{at: "2025-01-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-07-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-07-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-07-31T00:00:00Z", "2025-01-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:681", interval: period.Semiannual,
			inAdvance: false, monthlyCharges: true, monthlyFixed: false, created: "2024-01-31T01:00:00Z",
			events: []string{"2024-02-01T00:00:00Z"},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z", "nil", ""}},
				{at: "2024-03-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z", "nil", ""}},
				{at: "2024-07-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-07-30T23:59:59Z", "2024-06-30T00:00:00Z", "2024-07-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-07-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:760", interval: period.Semiannual,
			inAdvance: false, monthlyCharges: false, monthlyFixed: true, created: "2024-01-31T01:00:00Z",
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "", "nil", "nil", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z"}},
				{at: "2024-03-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "", "nil", "nil", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z"}},
				{at: "2024-07-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-07-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-07-30T23:59:59Z", "2024-06-30T00:00:00Z", "2024-07-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:821", interval: period.Semiannual,
			inAdvance: false, monthlyCharges: true, monthlyFixed: true, created: "2024-01-31T01:00:00Z",
			events: []string{"2024-02-01T00:00:00Z"},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z"}},
				{at: "2024-03-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z", "2024-02-29T00:00:00Z", "2024-03-30T23:59:59Z"}},
				{at: "2024-07-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-07-30T23:59:59Z", "2024-06-30T00:00:00Z", "2024-07-30T23:59:59Z", "2024-06-30T00:00:00Z", "2024-07-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:895", interval: period.Semiannual,
			inAdvance: true, monthlyCharges: true, monthlyFixed: false, created: "2024-01-31T01:00:00Z",
			events:  []string{"2024-02-01T00:00:00Z"},
			initial: 1, initialBounds: subsBounds{"2024-01-31T00:00:00Z", "2024-07-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z", "2024-01-31T01:00:00Z"},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z", "nil", "nil"}},
				{at: "2024-07-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-07-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-06-30T00:00:00Z", "2024-07-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-07-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:969", interval: period.Semiannual,
			inAdvance: true, monthlyCharges: false, monthlyFixed: true, created: "2024-01-31T01:00:00Z",
			initial: 1, initialBounds: subsBounds{"2024-01-31T00:00:00Z", "2024-07-30T23:59:59Z", "", "", "", ""},
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "", "nil", "nil", "2024-01-31T01:00:00Z", "2024-02-28T23:59:59Z"}},
				{at: "2024-07-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-07-31T00:00:00Z", "2025-01-30T23:59:59Z", "2024-01-31T01:00:00Z", "2024-07-30T23:59:59Z", "2024-06-30T00:00:00Z", "2024-07-30T23:59:59Z"}},
			},
		},
		{
			source: "spec/scenarios/subscriptions/billing_boundaries_spec.rb:1026", interval: period.Quarterly,
			inAdvance: false, monthlyCharges: false, monthlyFixed: false, created: "2024-01-31T01:00:00Z",
			steps: []subsBoundaryStep{
				{at: "2024-02-29T02:00:00Z"},
				{at: "2024-03-31T02:00:00Z"},
				{at: "2024-04-30T02:00:00Z", bill: true, bounds: subsBounds{"2024-01-31T00:00:00Z", "2024-04-29T23:59:59Z", "2024-01-31T01:00:00Z", "2024-04-29T23:59:59Z", "2024-01-31T01:00:00Z", "2024-04-29T23:59:59Z"}},
				{at: "2024-05-31T02:00:00Z"},
				{at: "2024-06-30T02:00:00Z"},
				{at: "2024-07-31T02:00:00Z", bill: true, bounds: subsBounds{"2024-04-30T00:00:00Z", "2024-07-30T23:59:59Z", "2024-04-30T00:00:00Z", "2024-07-30T23:59:59Z", "2024-04-30T00:00:00Z", "2024-07-30T23:59:59Z"}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			s := newScenario(t, subsAt(tc.created))
			c := s.Customer()
			m := s.Metric("bm", catalog.AggregationTypeCount, "")
			p := s.Plan(tc.interval, 100, func(p *catalog.Plan) {
				p.PayInAdvance = tc.inAdvance
				if tc.monthlyCharges {
					p.BillChargesMonthly = subsPtr(true)
				}
				if tc.monthlyFixed {
					p.BillFixedChargesMonthly = subsPtr(true)
				}
			})
			s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`)
			s.FixedCharge(p, s.AddOn(1000), catalog.FixedChargeModelStandard, `{"amount":"10"}`, "1")
			sub := s.subsCreate(c, p, subsParams{billingTime: period.Anniversary})
			if tc.initial > 0 {
				if got := len(s.subsInvoices(sub)); got != tc.initial {
					t.Fatalf("initial invoices: got = %d, want = %d", got, tc.initial)
				}
				checkSubsBounds(t, "initial", s.subsLastInvoiceSub(sub), tc.initialBounds)
			}
			for _, e := range tc.events {
				s.travelTo(subsAt(e))
				s.ingest(sub, "bm", "field", "10")
			}
			for _, st := range tc.steps {
				before := len(s.subsInvoices(sub))
				s.travelTo(subsAt(st.at))
				s.subsBilling()
				after := len(s.subsInvoices(sub))
				if st.bill != (after == before+1) || after > before+1 {
					t.Fatalf("billing at %s: got = %d new invoices, want = %v", st.at, after-before, st.bill)
				}
				if st.bill {
					checkSubsBounds(t, st.at, s.subsLastInvoiceSub(sub), st.bounds)
				}
			}
		})
	}
}

var _ = billingtest.Date

// subsBoundaryPlan is the plan of billing_boundaries_spec.rb: a standard
// charge on a count metric and a fixed charge.
func (s *scenario) subsBoundaryPlan(opts ...func(*catalog.Plan)) *catalog.Plan {
	s.T.Helper()
	p := s.Plan(period.Monthly, 100, opts...)
	s.Charge(p, s.Metric("bm_"+p.ID.String()[30:], catalog.AggregationTypeCount, ""), catalog.ChargeModelStandard, `{"amount":"10"}`)
	s.FixedCharge(p, s.AddOn(200), catalog.FixedChargeModelStandard, `{"amount":"10"}`, "1")
	return p
}

// source: spec/scenarios/subscriptions/billing_boundaries_spec.rb:1105
func TestScenarioSubscriptionBoundariesWithProgressiveBilling(t *testing.T) {
	s := newScenario(t, subsAt("2024-01-15T10:00:00Z"))
	c := s.Customer()
	p := s.subsBoundaryPlan()
	m := s.Metric("amount_bm", catalog.AggregationTypeSum, "amount")
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"2"}`)
	s.UsageThreshold(p, 20_000, false)
	sub := s.subsCreate(c, p, subsParams{billingTime: period.Anniversary})

	s.travelTo(subsAt("2024-02-15T02:00:00Z"))
	n := len(s.subsInvoices(sub, invoice.TypeSubscription))
	s.subsBilling()
	if got := len(s.subsInvoices(sub, invoice.TypeSubscription)); got != n+1 {
		t.Fatalf("subscription invoices: got = %d, want = %d", got, n+1)
	}
	checkSubsBounds(t, "february", s.subsLastInvoiceSub(sub), subsBounds{
		"2024-01-15T00:00:00Z", "2024-02-14T23:59:59Z", "2024-01-15T10:00:00Z", "2024-02-14T23:59:59Z", "2024-01-15T10:00:00Z", "2024-02-14T23:59:59Z",
	})

	s.travelTo(subsAt("2024-02-20T10:00:00Z"))
	s.ingest(sub, "amount_bm", "amount", "100")
	pbs := s.subsInvoices(sub, invoice.TypeProgressiveBilling)
	if len(pbs) != 1 {
		t.Fatalf("progressive billing invoices: got = %d, want = 1", len(pbs))
	}
	pb := pbs[0]
	if pb.TotalAmount <= 0 {
		t.Errorf("progressive total: got = %d, want > 0", pb.TotalAmount)
	}
	// Tanvo stores the progressive invoice's subscription period from the
	// billing instant (the idempotency key gap), so only the charges
	// boundaries are compared.
	checkSubsBounds(t, "progressive", s.subsLastInvoiceSub(sub), subsBounds{
		"", "2024-03-14T23:59:59Z", "2024-02-15T00:00:00Z", "2024-03-14T23:59:59Z", "2024-02-15T00:00:00Z", "2024-03-14T23:59:59Z",
	})

	s.travelTo(subsAt("2024-03-15T02:00:00Z"))
	n = len(s.subsInvoices(sub, invoice.TypeSubscription))
	s.subsBilling()
	invs := s.subsInvoices(sub, invoice.TypeSubscription)
	if len(invs) != n+1 {
		t.Fatalf("subscription invoices: got = %d, want = %d", len(invs), n+1)
	}
	checkSubsBounds(t, "march", s.subsLastInvoiceSub(sub), subsBounds{
		"2024-02-15T00:00:00Z", "2024-03-14T23:59:59Z", "2024-02-15T00:00:00Z", "2024-03-14T23:59:59Z", "2024-02-15T00:00:00Z", "2024-03-14T23:59:59Z",
	})
	if got, want := invs[len(invs)-1].ProgressiveBillingCreditAmount, pb.TotalAmount; got != want {
		t.Errorf("progressive billing credit: got = %d, want = %d", got, want)
	}
}

// source: spec/scenarios/subscriptions/billing_boundaries_spec.rb:1217
func TestScenarioSubscriptionBoundariesWithChargesPaidInAdvance(t *testing.T) {
	s := newScenario(t, subsAt("2024-01-01T10:00:00Z"))
	c := s.Customer()
	p := s.Plan(period.Monthly, 100)
	bm := s.Metric("bm", catalog.AggregationTypeCount, "")
	s.Charge(p, bm, catalog.ChargeModelStandard, `{"amount":"10"}`)
	s.FixedCharge(p, s.AddOn(200), catalog.FixedChargeModelStandard, `{"amount":"10"}`, "1")
	sum := s.Metric("sum_bm", catalog.AggregationTypeSum, "amount")
	rec := s.Metric("rec_bm", catalog.AggregationTypeSum, "seats", func(m *catalog.BillableMetric) { m.Recurring = true })
	advance := func(ch *catalog.Charge) { ch.PayInAdvance, ch.Invoiceable = true, true }
	s.Charge(p, sum, catalog.ChargeModelStandard, `{"amount":"1"}`)
	s.Charge(p, bm, catalog.ChargeModelStandard, `{"amount":"2"}`, advance)
	s.Charge(p, rec, catalog.ChargeModelStandard, `{"amount":"5"}`)
	s.Charge(p, rec, catalog.ChargeModelStandard, `{"amount":"10"}`, advance)
	sub := s.subsCreate(c, p, subsParams{billingTime: period.Calendar})

	s.travelTo(subsAt("2024-01-10T10:00:00Z"))
	s.ingest(sub, "sum_bm", "amount", "100")
	s.travelTo(subsAt("2024-01-15T10:00:00Z"))
	s.ingest(sub, "bm", "field", "50")
	s.travelTo(subsAt("2024-01-20T10:00:00Z"))
	s.ingest(sub, "rec_bm", "seats", "10")
	if got := len(s.subsInvoices(sub, invoice.TypeSubscription)); got != 2 {
		t.Fatalf("pay in advance invoices: got = %d, want = 2", got)
	}

	s.travelTo(subsAt("2024-02-01T02:00:00Z"))
	s.subsBilling()
	invs := s.subsInvoices(sub, invoice.TypeSubscription)
	if len(invs) != 3 {
		t.Fatalf("subscription invoices: got = %d, want = 3", len(invs))
	}
	checkSubsBounds(t, "february", s.subsLastInvoiceSub(sub), subsBounds{
		"2024-01-01T00:00:00Z", "2024-01-31T23:59:59Z", "2024-01-01T10:00:00Z", "2024-01-31T23:59:59Z", "2024-01-01T10:00:00Z", "2024-01-31T23:59:59Z",
	})
	if got := len(s.subsFeesOf(invs[2].ID, core.FeeTypeCharge)); got < 1 {
		t.Errorf("charge fees: got = %d, want >= 1", got)
	}

	s.travelTo(subsAt("2024-03-01T02:00:00Z"))
	s.subsBilling()
	if got := len(s.subsInvoices(sub, invoice.TypeSubscription)); got != 4 {
		t.Fatalf("subscription invoices: got = %d, want = 4", got)
	}
	checkSubsBounds(t, "march", s.subsLastInvoiceSub(sub), subsBounds{
		"2024-02-01T00:00:00Z", "2024-02-29T23:59:59Z", "2024-02-01T00:00:00Z", "2024-02-29T23:59:59Z", "2024-02-01T00:00:00Z", "2024-02-29T23:59:59Z",
	})
}

// subsCheck compares got with want.
func subsCheck[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got = %v, want = %v", what, got, want)
	}
}

func TestScenarioSubscriptionActivation(t *testing.T) {
	bogota, err := time.LoadLocation("America/Bogota")
	if err != nil {
		t.Fatal(err)
	}
	creation, at := subsAt("2023-08-24T00:07:00Z"), subsAt("2023-08-25T10:00:00Z")
	setup := func(t *testing.T) (*scenario, *customer.Customer, *catalog.Plan, *catalog.FixedCharge) {
		s := newScenario(t, creation)
		c := s.Customer(func(c *customer.Customer) { c.Timezone = bogota })
		p := s.Plan(period.Monthly, 100)
		fc := s.FixedCharge(p, s.AddOn(200), catalog.FixedChargeModelStandard, `{"amount":"100"}`, "1",
			func(f *catalog.FixedCharge) { f.PayInAdvance = true })
		return s, c, p, fc
	}

	t.Run("activates the subscription when it reaches its subscription date", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/activation_spec.rb:29
		s, c, p, _ := setup(t)
		sub := s.subsCreate(c, p, subsParams{billingTime: period.Calendar, subscriptionAt: &at})
		subsCheck(t, "status", sub.Status, subscription.StatusPending)
		s.travelTo(at)
		s.subsActivatePending()
		subsCheck(t, "status", s.subsReload(sub.ID).Status, subscription.StatusActive)
	})

	t.Run("stores rules as inactive, then evaluates and gates on activation date", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/activation_spec.rb:80
		s, c, _, _ := setup(t)
		inAdvance := s.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.PayInAdvance = true })
		sub := s.subsCreate(c, inAdvance, subsParams{externalID: "gated-future-sub", billingTime: period.Calendar, subscriptionAt: &at, paymentRule: subsPtr(48)})
		subsCheck(t, "status", sub.Status, subscription.StatusPending)
		subsCheck(t, "rules", fmt.Sprint(s.subsRules(sub)), fmt.Sprint([]subscription.ActivationRuleStatus{subscription.ActivationRuleStatusInactive}))
		s.travelTo(at)
		s.subsActivatePending()
		subsCheck(t, "status", s.subsReload(sub.ID).Status, subscription.StatusIncomplete)
		subsCheck(t, "rules", fmt.Sprint(s.subsRules(sub)), fmt.Sprint([]subscription.ActivationRuleStatus{subscription.ActivationRuleStatusPending}))
	})

	t.Run("generates a pay in advance invoice for the fixed charge", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/activation_spec.rb:112
		s, c, p, fc := setup(t)
		sub := s.subsCreate(c, p, subsParams{billingTime: period.Calendar, subscriptionAt: &at})
		subsCheck(t, "status", sub.Status, subscription.StatusPending)
		s.travelTo(at)
		s.subsActivatePending()
		subsCheck(t, "status", s.subsReload(sub.ID).Status, subscription.StatusActive)
		invs := s.subsInvoices(sub)
		if len(invs) != 1 {
			t.Fatalf("invoices: got = %d, want = 1", len(invs))
		}
		fees := s.Fees(invs[0].ID)
		if len(fees) != 1 {
			t.Fatalf("fees: got = %d, want = 1", len(fees))
		}
		if fees[0].FixedChargeID != fc.ID {
			t.Errorf("fixed charge: got = %v, want = %s", fees[0].FixedChargeID, fc.ID)
		}
	})
}

func TestScenarioSubscriptionTerminateRecreate(t *testing.T) {
	subscriptionAt := subsAt("2025-03-15T00:00:00Z")
	rotation, anniversary := subsAt("2026-03-13T00:00:00Z"), subsAt("2026-03-15T00:00:00Z")
	type world struct {
		s *scenario
		c *customer.Customer
		p *catalog.Plan
	}
	start := func(t *testing.T) *world {
		s := newScenario(t, subscriptionAt)
		c := s.Customer()
		m := s.Metric("items", catalog.AggregationTypeSum, "item_count")
		p := s.Plan(period.Yearly, 365_00)
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		s.subsCreate(c, p, subsParams{billingTime: period.Anniversary, subscriptionAt: &subscriptionAt})
		s.travelTo(subsAt("2025-06-01T00:00:00Z"))
		sub := s.subsOfCustomer(c)[0]
		for range 5 {
			s.ingest(sub, "items", "item_count", "10")
		}
		return &world{s, c, p}
	}
	rotate := func(w *world) {
		w.s.travelTo(rotation)
		w.s.subsTerminate(w.s.subsOfCustomer(w.c)[0])
		w.s.subsCreate(w.c, w.p, subsParams{billingTime: period.Anniversary, subscriptionAt: &subscriptionAt})
	}
	firstLast := func(w *world) (*subscription.Subscription, *subscription.Subscription) {
		all := w.s.subsOfCustomer(w.c)
		return all[0], all[len(all)-1]
	}

	t.Run("starts the recreated subscription at the termination, keeping the anniversary anchor", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_recreate_spec.rb:69
		w := start(t)
		rotate(w)
		old, cur := firstLast(w)
		if old.ID == cur.ID {
			t.Fatalf("recreated subscription is the terminated one")
		}
		subsCheck(t, "status", cur.Status, subscription.StatusActive)
		subsCheck(t, "subscription_at", cur.SubscriptionAt, subscriptionAt)
		if cur.StartedAt == nil || old.TerminatedAt == nil || !cur.StartedAt.Equal(*old.TerminatedAt) {
			t.Errorf("started_at: got = %v, want = %v", cur.StartedAt, old.TerminatedAt)
		}
	})

	t.Run("does not bill the terminated period a second time", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_recreate_spec.rb:80
		w := start(t)
		rotate(w)
		old, cur := firstLast(w)
		oldInvs := w.s.subsInvoices(old)
		subsCheck(t, "terminating charges", subsSum(w.s.subsFeesOf(oldInvs[len(oldInvs)-1].ID, core.FeeTypeCharge)), int64(50_00))
		w.s.travelTo(anniversary)
		w.s.subsBilling()
		is := w.s.subsLastInvoiceSub(cur)
		subsCheck(t, "from", is.Period.From, rotation)
		subsCheck(t, "charges_from", is.ChargesPeriod.From, rotation)
		curInvs := w.s.subsInvoices(cur)
		subsCheck(t, "anniversary charges", subsSum(w.s.subsFeesOf(curInvs[len(curInvs)-1].ID, core.FeeTypeCharge)), int64(0))
		var all int64
		for _, inv := range w.s.Invoices(w.c.ID) {
			all += subsSum(w.s.subsFeesOf(inv.ID, core.FeeTypeCharge))
		}
		subsCheck(t, "all charges", all, int64(50_00))
	})

	t.Run("carries the invoiced lifetime usage across the rotation without double counting it", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_recreate_spec.rb:102
		w := start(t)
		w.s.travelTo(rotation)
		w.s.subsUsageUpdate(w.c)
		old, _ := firstLast(w)
		subsCheck(t, "current usage before rotation", w.s.subsLifetimeUsage(old).CurrentUsageAmount, int64(50_00))
		rotate(w)
		w.s.subsUsageUpdate(w.c)
		_, cur := firstLast(w)
		// Deviation: lago creates a fresh record for the recreated
		// subscription; Tanvo keeps one lifetime usage per external ID and
		// moves it to the active subscription of the chain, so the record
		// is the same and only its amounts are compared.
		lu := w.s.subsLifetimeUsage(cur)
		subsCheck(t, "invoiced", lu.InvoicedUsageAmount, int64(50_00))
		subsCheck(t, "current", lu.CurrentUsageAmount, int64(0))
		subsCheck(t, "total", lu.Total(), int64(50_00))
	})
}

// TestScenarioSubscriptionBilling ports the two shared examples of
// billing_spec.rb ("a subscription billing without duplicated invoices"
// and "a subscription billing on every billing day") at each
// it_behaves_like.
func TestScenarioSubscriptionBilling(t *testing.T) {
	gbp := money.MustCurrency("GBP")
	tests := []struct {
		source, name   string
		interval       period.Interval
		billingTime    period.BillingTime
		timezone       string
		monthlyCharges bool
		// everyDay is "a subscription billing on every billing day": one
		// invoice per billing time; otherwise a single one across them.
		everyDay               bool
		subscribed             time.Time
		before, billing, after []time.Time
	}{
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:103", name: "Billing Subscriptions Scenario / with weekly plan / with calendar billing",
			interval: period.Weekly, billingTime: period.Calendar, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-01T00:00:00Z"),
			before:     []time.Time{subsAt("2023-02-05T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-06T01:00:00Z"), subsAt("2023-02-06T02:00:00Z")},
			after:      []time.Time{subsAt("2023-02-07T01:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:125", name: "Billing Subscriptions Scenario / with weekly plan / with calendar billing / with UTC+ timezone",
			interval: period.Weekly, billingTime: period.Calendar, timezone: "Asia/Kolkata", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-02T00:00:00Z"),
			before:     []time.Time{subsAt("2023-02-12T18:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-12T19:00:00Z"), subsAt("2023-02-13T00:00:00Z"), subsAt("2023-02-13T18:00:00Z")},
			after:      []time.Time{subsAt("2023-02-13T19:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:149", name: "Billing Subscriptions Scenario / with weekly plan / with calendar billing / with UTC- timezone",
			interval: period.Weekly, billingTime: period.Calendar, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-01T06:10:00Z"),
			before:     []time.Time{subsAt("2023-02-13T04:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-13T05:00:00Z"), subsAt("2023-02-13T06:00:00Z"), subsAt("2023-02-13T03:00:00Z"), subsAt("2023-02-14T04:00:00Z")},
			after:      []time.Time{subsAt("2023-02-14T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:161", name: "Billing Subscriptions Scenario / with weekly plan / with anniversary billing",
			interval: period.Weekly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-01T00:00:00Z"),
			before:     []time.Time{subsAt("2023-02-14T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-15T01:00:00Z"), subsAt("2023-02-15T02:00:00Z")},
			after:      []time.Time{subsAt("2023-02-16T01:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:184", name: "Billing Subscriptions Scenario / with weekly plan / with anniversary billing / with UTC+ timezone",
			interval: period.Weekly, billingTime: period.Anniversary, timezone: "Europe/Paris", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-05-02T00:00:00Z"),
			before:     []time.Time{subsAt("2023-05-22T21:00:00Z")},
			billing:    []time.Time{subsAt("2023-05-22T22:00:00Z"), subsAt("2023-05-23T20:00:00Z"), subsAt("2023-05-23T21:00:00Z"), subsAt("2023-05-23T22:10:00Z")},
			after:      []time.Time{subsAt("2023-05-24T00:10:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:208", name: "Billing Subscriptions Scenario / with weekly plan / with anniversary billing / with UTC- timezone",
			interval: period.Weekly, billingTime: period.Anniversary, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-01T06:10:00Z"),
			before:     []time.Time{subsAt("2023-02-15T04:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-15T05:00:00Z"), subsAt("2023-02-15T06:00:00Z"), subsAt("2023-02-15T03:00:00Z"), subsAt("2023-02-16T04:00:00Z")},
			after:      []time.Time{subsAt("2023-02-16T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:224", name: "Billing Subscriptions Scenario / with monthly plan / with calendar billing",
			interval: period.Monthly, billingTime: period.Calendar, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2023-02-28T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-01T01:00:00Z"), subsAt("2023-03-01T02:00:00Z")},
			after:      []time.Time{subsAt("2023-03-02T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:247", name: "Billing Subscriptions Scenario / with monthly plan / with calendar billing / with UTC+ timezone",
			interval: period.Monthly, billingTime: period.Calendar, timezone: "Asia/Kolkata", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-01T00:00:00Z"),
			before:     []time.Time{subsAt("2023-02-28T18:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-28T19:00:00Z"), subsAt("2023-03-01T00:00:00Z"), subsAt("2023-03-01T18:00:00Z")},
			after:      []time.Time{subsAt("2023-03-01T19:00:00Z"), subsAt("2023-03-02T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:272", name: "Billing Subscriptions Scenario / with monthly plan / with calendar billing / with UTC- timezone",
			interval: period.Monthly, billingTime: period.Calendar, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-02T19:00:00Z"),
			before:     []time.Time{subsAt("2023-03-01T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-01T05:00:00Z"), subsAt("2023-03-01T06:00:00Z"), subsAt("2023-03-01T00:00:00Z"), subsAt("2023-03-02T04:00:00Z")},
			after:      []time.Time{subsAt("2023-03-02T05:00:00Z"), subsAt("2023-03-03T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:284", name: "Billing Subscriptions Scenario / with monthly plan / with anniversary billing",
			interval: period.Monthly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-03T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-04T01:00:00Z"), subsAt("2023-03-04T02:00:00Z")},
			after:      []time.Time{subsAt("2023-03-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:293", name: "Billing Subscriptions Scenario / with monthly plan / with anniversary billing / when subscription started on a 31st",
			interval: period.Monthly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-03-31T00:00:00Z"),
			before:     []time.Time{subsAt("2023-04-29T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-04-30T00:00:00Z")},
			after:      []time.Time{subsAt("2023-05-01T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:319", name: "Billing Subscriptions Scenario / with monthly plan / with anniversary billing / with anniversary on a 31st",
			interval: period.Monthly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: true,
			subscribed: subsAt("2022-12-31T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-03T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-01-31T01:00:00Z"), subsAt("2023-02-28T01:00:00Z"), subsAt("2023-03-31T01:00:00Z"), subsAt("2023-04-30T01:00:00Z"), subsAt("2023-05-31T01:00:00Z"), subsAt("2023-06-30T01:00:00Z"), subsAt("2023-07-31T02:00:00Z"), subsAt("2023-08-31T02:00:00Z"), subsAt("2023-09-30T02:00:00Z"), subsAt("2023-10-31T02:00:00Z"), subsAt("2023-11-30T02:00:00Z"), subsAt("2023-12-31T02:00:00Z"), subsAt("2024-01-31T02:00:00Z"), subsAt("2024-02-29T02:00:00Z"), subsAt("2024-03-31T02:00:00Z")},
			after:      []time.Time{subsAt("2023-03-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:344", name: "Billing Subscriptions Scenario / with monthly plan / with anniversary billing / with anniversary on a 30",
			interval: period.Monthly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: true,
			subscribed: subsAt("2022-04-30T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-03T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-01-30T01:00:00Z"), subsAt("2023-02-28T01:00:00Z"), subsAt("2023-03-30T01:00:00Z"), subsAt("2023-04-30T01:00:00Z"), subsAt("2023-05-30T01:00:00Z"), subsAt("2023-06-30T01:00:00Z"), subsAt("2023-07-30T02:00:00Z"), subsAt("2023-08-30T02:00:00Z"), subsAt("2023-09-30T02:00:00Z"), subsAt("2023-10-30T02:00:00Z"), subsAt("2023-11-30T02:00:00Z"), subsAt("2023-12-30T02:00:00Z"), subsAt("2024-02-29T02:00:00Z"), subsAt("2024-03-30T02:00:00Z")},
			after:      []time.Time{subsAt("2023-03-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:367", name: "Billing Subscriptions Scenario / with monthly plan / with anniversary billing / with anniversary on a 28 of february",
			interval: period.Monthly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: true,
			subscribed: subsAt("2022-02-28T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-03T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-01-28T01:00:00Z"), subsAt("2023-02-28T01:00:00Z"), subsAt("2023-03-28T01:00:00Z"), subsAt("2023-04-28T01:00:00Z"), subsAt("2023-05-28T01:00:00Z"), subsAt("2023-06-28T01:00:00Z"), subsAt("2023-07-28T02:00:00Z"), subsAt("2023-08-28T02:00:00Z"), subsAt("2023-09-28T02:00:00Z"), subsAt("2023-10-28T02:00:00Z"), subsAt("2023-11-28T02:00:00Z"), subsAt("2023-12-28T02:00:00Z")},
			after:      []time.Time{subsAt("2023-03-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:390", name: "Billing Subscriptions Scenario / with monthly plan / with anniversary billing / with UTC+ timezone",
			interval: period.Monthly, billingTime: period.Anniversary, timezone: "Asia/Kolkata", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-02T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-01T18:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-01T19:00:00Z"), subsAt("2023-03-02T00:00:00Z"), subsAt("2023-03-02T18:00:00Z")},
			after:      []time.Time{subsAt("2023-03-02T19:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:414", name: "Billing Subscriptions Scenario / with monthly plan / with anniversary billing / with UTC- timezone",
			interval: period.Monthly, billingTime: period.Anniversary, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-02T05:00:00Z"),
			before:     []time.Time{subsAt("2023-03-01T23:00:00Z"), subsAt("2023-03-02T04:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-02T06:00:00Z"), subsAt("2023-03-01T07:00:00Z"), subsAt("2023-03-03T00:00:00Z")},
			after:      []time.Time{subsAt("2023-03-03T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:430", name: "Billing Subscriptions Scenario / with quarterly plan / with calendar billing",
			interval: period.Quarterly, billingTime: period.Calendar, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-01T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-04-01T01:00:00Z"), subsAt("2023-04-01T02:00:00Z")},
			after:      []time.Time{subsAt("2023-05-01T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:453", name: "Billing Subscriptions Scenario / with quarterly plan / with calendar billing / with UTC+ timezone",
			interval: period.Quarterly, billingTime: period.Calendar, timezone: "Asia/Kolkata", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-01T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-31T18:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-31T19:00:00Z"), subsAt("2023-04-01T00:00:00Z"), subsAt("2023-04-01T18:00:00Z")},
			after:      []time.Time{subsAt("2023-04-01T19:00:00Z"), subsAt("2023-04-02T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:478", name: "Billing Subscriptions Scenario / with quarterly plan / with calendar billing / with UTC- timezone",
			interval: period.Quarterly, billingTime: period.Calendar, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-02T19:00:00Z"),
			before:     []time.Time{subsAt("2023-04-01T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-04-01T05:00:00Z"), subsAt("2023-04-01T06:00:00Z"), subsAt("2023-04-02T00:00:00Z"), subsAt("2023-04-02T04:00:00Z")},
			after:      []time.Time{subsAt("2023-04-02T05:00:00Z"), subsAt("2023-04-03T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:490", name: "Billing Subscriptions Scenario / with quarterly plan / with anniversary billing",
			interval: period.Quarterly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-04T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-05-04T01:00:00Z"), subsAt("2023-05-04T02:00:00Z")},
			after:      []time.Time{subsAt("2023-05-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:499", name: "Billing Subscriptions Scenario / with quarterly plan / with anniversary billing / when subscription started on a 31st",
			interval: period.Quarterly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-03-31T00:00:00Z"),
			before:     []time.Time{subsAt("2023-06-29T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-06-30T00:00:00Z")},
			after:      []time.Time{subsAt("2023-07-01T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:514", name: "Billing Subscriptions Scenario / with quarterly plan / with anniversary billing / with anniversary on a 31st",
			interval: period.Quarterly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: true,
			subscribed: subsAt("2022-12-31T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-04T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-31T01:00:00Z"), subsAt("2023-06-30T01:00:00Z"), subsAt("2023-09-30T02:00:00Z"), subsAt("2023-12-31T02:00:00Z")},
			after:      []time.Time{subsAt("2023-05-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:529", name: "Billing Subscriptions Scenario / with quarterly plan / with anniversary billing / with anniversary on a 30",
			interval: period.Quarterly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: true,
			subscribed: subsAt("2022-04-30T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-04T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-01-30T01:00:00Z"), subsAt("2023-04-30T01:00:00Z"), subsAt("2023-07-30T02:00:00Z"), subsAt("2023-10-30T02:00:00Z")},
			after:      []time.Time{subsAt("2023-05-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:544", name: "Billing Subscriptions Scenario / with quarterly plan / with anniversary billing / with anniversary on a 28 of february",
			interval: period.Quarterly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: true,
			subscribed: subsAt("2022-02-28T00:00:00Z"),
			before:     []time.Time{subsAt("2023-03-04T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-28T01:00:00Z"), subsAt("2023-05-28T01:00:00Z"), subsAt("2023-08-28T02:00:00Z"), subsAt("2023-11-28T02:00:00Z")},
			after:      []time.Time{subsAt("2023-05-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:567", name: "Billing Subscriptions Scenario / with quarterly plan / with anniversary billing / with UTC+ timezone",
			interval: period.Quarterly, billingTime: period.Anniversary, timezone: "Asia/Kolkata", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-02T00:00:00Z"),
			before:     []time.Time{subsAt("2023-05-01T18:00:00Z")},
			billing:    []time.Time{subsAt("2023-05-01T19:00:00Z"), subsAt("2023-05-02T00:00:00Z"), subsAt("2023-05-02T18:00:00Z")},
			after:      []time.Time{subsAt("2023-05-02T19:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:591", name: "Billing Subscriptions Scenario / with quarterly plan / with anniversary billing / with UTC- timezone",
			interval: period.Quarterly, billingTime: period.Anniversary, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2023-02-02T05:00:00Z"),
			before:     []time.Time{subsAt("2023-05-01T23:00:00Z"), subsAt("2023-05-02T04:00:00Z")},
			billing:    []time.Time{subsAt("2023-05-02T06:00:00Z"), subsAt("2023-05-01T07:00:00Z"), subsAt("2023-05-03T00:00:00Z")},
			after:      []time.Time{subsAt("2023-05-03T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:607", name: "Billing Subscriptions Scenario / with yearly plan / with calendar billing",
			interval: period.Yearly, billingTime: period.Calendar, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-02-01T00:00:00Z"),
			before:     []time.Time{subsAt("2022-12-31T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-01-01T01:00:00Z"), subsAt("2023-01-01T02:00:00Z")},
			after:      []time.Time{subsAt("2023-01-02T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:632", name: "Billing Subscriptions Scenario / with yearly plan / with calendar billing / with UTC+ timezone",
			interval: period.Yearly, billingTime: period.Calendar, timezone: "Europe/Paris", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-04-02T00:00:00Z"),
			before:     []time.Time{subsAt("2022-12-31T21:00:00Z")},
			billing:    []time.Time{subsAt("2022-12-31T23:00:00Z"), subsAt("2023-01-01T20:00:00Z"), subsAt("2023-01-01T21:00:00Z")},
			after:      []time.Time{subsAt("2023-01-01T22:59:00Z"), subsAt("2023-01-02T00:10:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:653", name: "Billing Subscriptions Scenario / with yearly plan / with calendar billing / with UTC- timezone",
			interval: period.Yearly, billingTime: period.Calendar, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-02-04T19:00:00Z"),
			before:     []time.Time{subsAt("2023-01-01T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-01-01T05:00:00Z"), subsAt("2023-01-01T06:00:00Z"), subsAt("2023-01-02T00:00:00Z")},
			after:      []time.Time{subsAt("2023-01-02T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:665", name: "Billing Subscriptions Scenario / with yearly plan / with anniversary billing",
			interval: period.Yearly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2023-01-01T00:00:00Z"), subsAt("2023-02-03T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-04T01:00:00Z"), subsAt("2023-02-04T02:00:00Z")},
			after:      []time.Time{subsAt("2023-02-05T00:00:00Z"), subsAt("2023-03-04T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:674", name: "Billing Subscriptions Scenario / with yearly plan / with anniversary billing / when subscription started on a 29th of February",
			interval: period.Yearly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2020-02-29T00:00:00Z"),
			before:     []time.Time{subsAt("2023-01-28T00:00:00Z"), subsAt("2023-02-27T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-28T01:00:00Z"), subsAt("2023-02-28T02:00:00Z")},
			after:      []time.Time{subsAt("2023-03-01T00:00:00Z"), subsAt("2023-04-29T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:700", name: "Billing Subscriptions Scenario / with yearly plan / with anniversary billing / with UTC+ timezone",
			interval: period.Yearly, billingTime: period.Anniversary, timezone: "Europe/Paris", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-04-02T00:00:00Z"),
			before:     []time.Time{subsAt("2023-04-01T21:00:00Z")},
			billing:    []time.Time{subsAt("2023-04-02T23:00:00Z"), subsAt("2023-04-02T20:00:00Z"), subsAt("2023-04-02T21:00:00Z"), subsAt("2023-04-02T22:10:00Z")},
			after:      []time.Time{subsAt("2023-04-03T00:10:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:721", name: "Billing Subscriptions Scenario / with yearly plan / with anniversary billing / with UTC- timezone",
			interval: period.Yearly, billingTime: period.Anniversary, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-02-04T19:00:00Z"),
			before:     []time.Time{subsAt("2023-02-04T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-04T05:00:00Z"), subsAt("2023-02-04T06:00:00Z"), subsAt("2023-02-05T00:00:00Z")},
			after:      []time.Time{subsAt("2023-02-05T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:737", name: "Billing Subscriptions Scenario / with semiannual plan / with calendar billing",
			interval: period.Semiannual, billingTime: period.Calendar, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-02-01T00:00:00Z"),
			before:     []time.Time{subsAt("2022-06-30T00:00:00Z")},
			billing:    []time.Time{subsAt("2022-07-01T01:00:00Z"), subsAt("2022-07-01T02:00:00Z")},
			after:      []time.Time{subsAt("2022-07-02T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:762", name: "Billing Subscriptions Scenario / with semiannual plan / with calendar billing / with UTC+ timezone",
			interval: period.Semiannual, billingTime: period.Calendar, timezone: "Europe/Paris", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-04-02T00:00:00Z"),
			before:     []time.Time{subsAt("2022-06-30T21:00:00Z")},
			billing:    []time.Time{subsAt("2022-06-30T23:00:00Z"), subsAt("2022-07-01T20:00:00Z"), subsAt("2022-07-01T21:00:00Z")},
			after:      []time.Time{subsAt("2022-07-01T22:59:00Z"), subsAt("2022-07-02T00:10:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:783", name: "Billing Subscriptions Scenario / with semiannual plan / with calendar billing / with UTC- timezone",
			interval: period.Semiannual, billingTime: period.Calendar, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-02-04T19:00:00Z"),
			before:     []time.Time{subsAt("2022-07-01T00:00:00Z")},
			billing:    []time.Time{subsAt("2022-07-01T05:00:00Z"), subsAt("2022-07-01T06:00:00Z"), subsAt("2022-07-02T00:00:00Z")},
			after:      []time.Time{subsAt("2022-07-02T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:795", name: "Billing Subscriptions Scenario / with semiannual plan / with anniversary billing",
			interval: period.Semiannual, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2022-07-01T00:00:00Z"), subsAt("2022-08-03T00:00:00Z")},
			billing:    []time.Time{subsAt("2022-08-04T01:00:00Z"), subsAt("2022-08-04T02:00:00Z")},
			after:      []time.Time{subsAt("2022-08-05T00:00:00Z"), subsAt("2022-09-04T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:804", name: "Billing Subscriptions Scenario / with semiannual plan / with anniversary billing / when subscription started on a 29th of February",
			interval: period.Semiannual, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2020-02-29T00:00:00Z"),
			before:     []time.Time{subsAt("2023-06-28T00:00:00Z"), subsAt("2023-07-27T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-08-29T01:00:00Z"), subsAt("2023-08-29T02:00:00Z")},
			after:      []time.Time{subsAt("2023-10-01T00:00:00Z"), subsAt("2023-11-29T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:830", name: "Billing Subscriptions Scenario / with semiannual plan / with anniversary billing / with UTC+ timezone",
			interval: period.Semiannual, billingTime: period.Anniversary, timezone: "Europe/Paris", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-04-02T00:00:00Z"),
			before:     []time.Time{subsAt("2022-09-01T21:00:00Z")},
			billing:    []time.Time{subsAt("2022-10-01T23:00:00Z"), subsAt("2022-10-01T20:00:00Z"), subsAt("2022-10-02T21:00:00Z"), subsAt("2022-10-02T22:10:00Z")},
			after:      []time.Time{subsAt("2022-10-03T00:10:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:851", name: "Billing Subscriptions Scenario / with semiannual plan / with anniversary billing / with UTC- timezone",
			interval: period.Semiannual, billingTime: period.Anniversary, timezone: "America/Bogota", monthlyCharges: false, everyDay: false,
			subscribed: subsAt("2022-02-04T19:00:00Z"),
			before:     []time.Time{subsAt("2022-02-04T00:00:00Z")},
			billing:    []time.Time{subsAt("2022-08-04T05:00:00Z"), subsAt("2022-08-04T06:00:00Z"), subsAt("2022-08-05T00:00:00Z")},
			after:      []time.Time{subsAt("2022-08-05T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:869", name: "Billing Subscriptions Scenario / with semiannual plan and monthly charge / with calendar billing",
			interval: period.Semiannual, billingTime: period.Calendar, timezone: "UTC", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2021-12-31T00:00:00Z")},
			billing:    []time.Time{subsAt("2022-07-01T01:00:00Z"), subsAt("2022-07-01T02:00:00Z")},
			after:      []time.Time{subsAt("2022-07-02T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:892", name: "Billing Subscriptions Scenario / with semiannual plan and monthly charge / with calendar billing / with UTC+ timezone",
			interval: period.Semiannual, billingTime: period.Calendar, timezone: "Asia/Kolkata", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2023-02-02T00:00:00Z"),
			before:     []time.Time{subsAt("2023-02-28T18:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-28T19:00:00Z"), subsAt("2023-03-01T00:00:00Z"), subsAt("2023-03-01T18:00:00Z")},
			after:      []time.Time{subsAt("2023-03-01T19:00:00Z"), subsAt("2023-03-02T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:917", name: "Billing Subscriptions Scenario / with semiannual plan and monthly charge / with calendar billing / with UTC- timezone",
			interval: period.Semiannual, billingTime: period.Calendar, timezone: "America/Bogota", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-02-02T19:00:00Z"),
			before:     []time.Time{subsAt("2023-03-01T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-01T05:00:00Z"), subsAt("2023-03-01T06:00:00Z"), subsAt("2023-03-01T00:00:00Z"), subsAt("2023-03-02T04:00:00Z")},
			after:      []time.Time{subsAt("2023-03-02T05:00:00Z"), subsAt("2023-03-03T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:930", name: "Billing Subscriptions Scenario / with semiannual plan and monthly charge / with anniversary billing",
			interval: period.Semiannual, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2023-01-03T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-01-04T01:00:00Z"), subsAt("2023-01-04T02:00:00Z")},
			after:      []time.Time{subsAt("2023-01-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:939", name: "Billing Subscriptions Scenario / with semiannual plan and monthly charge / with anniversary billing / when subscription started on a 31st",
			interval: period.Semiannual, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2023-03-31T00:00:00Z"),
			before:     []time.Time{subsAt("2023-04-29T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-04-30T00:00:00Z")},
			after:      []time.Time{subsAt("2023-05-01T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:965", name: "Billing Subscriptions Scenario / with semiannual plan and monthly charge / with anniversary billing / with UTC+ timezone",
			interval: period.Semiannual, billingTime: period.Anniversary, timezone: "Europe/Paris", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-04-02T00:00:00Z"),
			before:     []time.Time{subsAt("2023-04-01T21:00:00Z")},
			billing:    []time.Time{subsAt("2023-04-02T22:00:00Z"), subsAt("2023-04-02T20:00:00Z"), subsAt("2023-04-02T21:00:00Z"), subsAt("2023-04-02T22:10:00Z")},
			after:      []time.Time{subsAt("2023-04-03T00:10:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:986", name: "Billing Subscriptions Scenario / with semiannual plan and monthly charge / with anniversary billing / with UTC- timezone",
			interval: period.Semiannual, billingTime: period.Anniversary, timezone: "America/Bogota", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-02-04T19:00:00Z"),
			before:     []time.Time{subsAt("2023-03-04T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-04T05:00:00Z"), subsAt("2023-03-04T06:00:00Z"), subsAt("2023-03-05T00:00:00Z")},
			after:      []time.Time{subsAt("2023-03-05T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:1004", name: "Billing Subscriptions Scenario / with yearly plan and monthly charge / with calendar billing",
			interval: period.Yearly, billingTime: period.Calendar, timezone: "UTC", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2021-12-31T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-01-01T01:00:00Z"), subsAt("2023-01-01T02:00:00Z")},
			after:      []time.Time{subsAt("2023-01-02T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:1027", name: "Billing Subscriptions Scenario / with yearly plan and monthly charge / with calendar billing / with UTC+ timezone",
			interval: period.Yearly, billingTime: period.Calendar, timezone: "Asia/Kolkata", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2023-02-02T00:00:00Z"),
			before:     []time.Time{subsAt("2023-02-28T18:00:00Z")},
			billing:    []time.Time{subsAt("2023-02-28T19:00:00Z"), subsAt("2023-03-01T00:00:00Z"), subsAt("2023-03-01T18:00:00Z")},
			after:      []time.Time{subsAt("2023-03-01T19:00:00Z"), subsAt("2023-03-02T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:1052", name: "Billing Subscriptions Scenario / with yearly plan and monthly charge / with calendar billing / with UTC- timezone",
			interval: period.Yearly, billingTime: period.Calendar, timezone: "America/Bogota", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-02-02T19:00:00Z"),
			before:     []time.Time{subsAt("2023-03-01T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-01T05:00:00Z"), subsAt("2023-03-01T06:00:00Z"), subsAt("2023-03-01T00:00:00Z"), subsAt("2023-03-02T04:00:00Z")},
			after:      []time.Time{subsAt("2023-03-02T05:00:00Z"), subsAt("2023-03-03T05:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:1065", name: "Billing Subscriptions Scenario / with yearly plan and monthly charge / with anniversary billing",
			interval: period.Yearly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-02-04T00:00:00Z"),
			before:     []time.Time{subsAt("2023-01-03T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-01-04T01:00:00Z"), subsAt("2023-01-04T02:00:00Z")},
			after:      []time.Time{subsAt("2023-01-05T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:1074", name: "Billing Subscriptions Scenario / with yearly plan and monthly charge / with anniversary billing / when subscription started on a 31st",
			interval: period.Yearly, billingTime: period.Anniversary, timezone: "UTC", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2023-03-31T00:00:00Z"),
			before:     []time.Time{subsAt("2023-04-29T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-04-30T00:00:00Z")},
			after:      []time.Time{subsAt("2023-05-01T00:00:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:1100", name: "Billing Subscriptions Scenario / with yearly plan and monthly charge / with anniversary billing / with UTC+ timezone",
			interval: period.Yearly, billingTime: period.Anniversary, timezone: "Europe/Paris", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-04-02T00:00:00Z"),
			before:     []time.Time{subsAt("2023-04-01T21:00:00Z")},
			billing:    []time.Time{subsAt("2023-04-02T22:00:00Z"), subsAt("2023-04-02T20:00:00Z"), subsAt("2023-04-02T21:00:00Z"), subsAt("2023-04-02T22:10:00Z")},
			after:      []time.Time{subsAt("2023-04-03T00:10:00Z")},
		},
		{
			source: "spec/scenarios/subscriptions/billing_spec.rb:1121", name: "Billing Subscriptions Scenario / with yearly plan and monthly charge / with anniversary billing / with UTC- timezone",
			interval: period.Yearly, billingTime: period.Anniversary, timezone: "America/Bogota", monthlyCharges: true, everyDay: false,
			subscribed: subsAt("2022-02-04T19:00:00Z"),
			before:     []time.Time{subsAt("2023-03-04T00:00:00Z")},
			billing:    []time.Time{subsAt("2023-03-04T05:00:00Z"), subsAt("2023-03-04T06:00:00Z"), subsAt("2023-03-05T00:00:00Z")},
			after:      []time.Time{subsAt("2023-03-05T05:00:00Z")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.timezone)
			if err != nil {
				t.Fatal(err)
			}
			s := newScenario(t, tc.subscribed)
			c := s.Customer(func(c *customer.Customer) {
				c.Timezone, c.Currency = loc, gbp
			})
			p := s.Plan(tc.interval, 5_000_000, func(p *catalog.Plan) {
				p.Currency = gbp
				if tc.monthlyCharges {
					p.BillChargesMonthly = subsPtr(true)
				}
			})
			sub := s.subsCreate(c, p, subsParams{billingTime: tc.billingTime})
			count := func() int { return len(s.subsInvoices(sub)) }
			for _, at := range tc.before {
				s.travelTo(at)
				s.subsBilling()
				if got := count(); got != 0 {
					t.Fatalf("before billing at %s: got = %d invoices, want = 0", at.Format(time.RFC3339), got)
				}
			}
			for _, at := range tc.billing {
				s.travelTo(at)
				s.subsBilling()
			}
			want := 1
			if tc.everyDay {
				want = len(tc.billing)
			}
			if got := count(); got != want {
				t.Fatalf("billing days: got = %d invoices, want = %d", got, want)
			}
			for _, at := range tc.after {
				s.travelTo(at)
				s.subsBilling()
				if got := count(); got != want {
					t.Fatalf("after billing at %s: got = %d invoices, want = %d", at.Format(time.RFC3339), got, want)
				}
			}
		})
	}
}

// subsAmounts returns the sorted amounts of fees.
func subsAmounts(fs []*invoice.Fee) []int64 {
	out := make([]int64, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Amount)
	}
	slices.Sort(out)
	return out
}

// subsCheckAmounts compares the sorted fee amounts with want.
func subsCheckAmounts(t *testing.T, what string, fs []*invoice.Fee, want ...int64) {
	t.Helper()
	slices.Sort(want)
	if got := subsAmounts(fs); !slices.Equal(got, want) {
		t.Errorf("%s: got = %v, want = %v", what, got, want)
	}
}

// subsCheckPeriod compares the charges period of fee properties with
// lago's inclusive bounds.
func subsCheckPeriod(t *testing.T, what string, f *invoice.Fee, from, to string) {
	t.Helper()
	gotFrom, gotTo := f.Properties.ChargesFrom.UTC(), f.Properties.ChargesTo.UTC()
	wantFrom, wantTo := subsAt(from), subsAt(to)
	// lago's inclusive "…23:59:59.999" end is an exclusive end at the
	// next second, or that instant minus a sub-second precision.
	end := wantTo.Add(time.Second)
	okTo := gotTo.Equal(end) || (gotTo.Before(end) && !gotTo.Before(wantTo))
	if !gotFrom.Equal(wantFrom) || !okTo {
		t.Errorf("%s period: got = [%s, %s], want = [%s, %s]", what, gotFrom.Format(time.RFC3339Nano), gotTo.Format(time.RFC3339Nano), from, to)
	}
}

// source: spec/scenarios/subscriptions/downgrade_spec.rb:33
func TestScenarioSubscriptionDowngrade(t *testing.T) {
	at := subsAt("2023-07-19T12:12:00Z")
	s := newScenario(t, at)
	c := s.Customer()
	monthly := s.Plan(period.Monthly, 12_900, func(p *catalog.Plan) { p.PayInAdvance = true })
	yearly := s.Plan(period.Yearly, 118_800, func(p *catalog.Plan) { p.PayInAdvance = true })
	sub := s.subsCreate(c, monthly, subsParams{billingTime: period.Anniversary, subscriptionAt: &at})
	subsCheck(t, "status", sub.Status, subscription.StatusActive)
	invs := s.subsInvoices(sub)
	if len(invs) != 1 {
		t.Fatalf("invoices: got = %d, want = 1", len(invs))
	}
	subsCheck(t, "fees amount", invs[0].FeesAmount, int64(12_900))
	checkSubsBounds(t, "start", s.subsLastInvoiceSub(sub), subsBounds{"2023-07-19T00:00:00Z", "2023-08-18T23:59:59Z", "", "", "", ""})

	months := []struct{ at, from, to, chargesFrom, chargesTo string }{
		{"2023-08-19T12:12:00Z", "2023-08-19T00:00:00Z", "2023-09-18T23:59:59Z", "2023-07-19T12:12:00Z", "2023-08-18T23:59:59Z"},
		{"2023-09-19T12:12:00Z", "2023-09-19T00:00:00Z", "2023-10-18T23:59:59Z", "2023-08-19T00:00:00Z", "2023-09-18T23:59:59Z"},
		{"2023-10-19T12:12:00Z", "2023-10-19T00:00:00Z", "2023-11-18T23:59:59Z", "2023-09-19T00:00:00Z", "2023-10-18T23:59:59Z"},
	}
	for i, m := range months {
		s.travelTo(subsAt(m.at))
		s.subsBilling()
		invs = s.subsInvoices(sub)
		if len(invs) != i+2 {
			t.Fatalf("%s invoices: got = %d, want = %d", m.at, len(invs), i+2)
		}
		subsCheck(t, m.at+" fees amount", invs[len(invs)-1].FeesAmount, int64(12_900))
		checkSubsBounds(t, m.at, s.subsLastInvoiceSub(sub), subsBounds{m.from, m.to, m.chargesFrom, m.chargesTo, "", ""})
	}

	s.travelTo(subsAt("2023-11-09T00:00:00Z"))
	s.subsCreate(c, yearly, subsParams{billingTime: period.Anniversary})
	subsCheck(t, "status after downgrade", s.subsReload(sub.ID).Status, subscription.StatusActive)
	subsCheck(t, "invoices after downgrade", len(s.subsInvoices(sub)), 4)

	s.travelTo(subsAt("2023-11-19T12:12:00Z"))
	s.subsBilling()
	subsCheck(t, "status", s.subsReload(sub.ID).Status, subscription.StatusTerminated)
	subsCheck(t, "invoices", len(s.subsInvoices(sub)), 5)
	subsCheck(t, "customer invoices", len(s.Invoices(c.ID)), 5)
	all := s.subsOfCustomer(c)
	next := all[len(all)-1]
	subsCheck(t, "next status", next.Status, subscription.StatusActive)
	nextInvs := s.subsInvoices(next)
	if len(nextInvs) != 1 {
		t.Fatalf("next invoices: got = %d, want = 1", len(nextInvs))
	}
	// 243 days from the new subscription's start to the old anniversary,
	// over the 366 days of the leap year.
	subsCheck(t, "next fees amount", nextInvs[0].FeesAmount, int64(math.Round(118_800.0/366*243)))
	checkSubsBounds(t, "next", s.subsLastInvoiceSub(next), subsBounds{"2023-11-19T00:00:00Z", "2024-07-18T23:59:59Z", "", "", "", ""})
}

func TestScenarioSubscriptionDowngradeWithFixedCharges(t *testing.T) {
	tests := []struct {
		source              string
		inAdvance, prorated bool
	}{
		{"spec/scenarios/subscriptions/downgrade_spec.rb:155", true, true},
		{"spec/scenarios/subscriptions/downgrade_spec.rb:224", true, false},
		{"spec/scenarios/subscriptions/downgrade_spec.rb:297", false, true},
		{"spec/scenarios/subscriptions/downgrade_spec.rb:375", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			s := newScenario(t, subsAt("2023-07-19T12:12:00Z"))
			c := s.Customer()
			p := s.Plan(period.Monthly, 10_000)
			down := s.Plan(period.Monthly, 1000)
			addOns := []*catalog.AddOn{s.AddOn(200), s.AddOn(200), s.AddOn(200)}
			fc := func(f *catalog.FixedCharge) { f.PayInAdvance, f.Prorated = tc.inAdvance, tc.prorated }
			s.FixedCharge(p, addOns[0], catalog.FixedChargeModelStandard, `{"amount":"1"}`, "10", fc)
			s.FixedCharge(p, addOns[1], catalog.FixedChargeModelStandard, `{"amount":"3"}`, "5", fc)
			s.FixedCharge(down, addOns[1], catalog.FixedChargeModelStandard, `{"amount":"10"}`, "10", fc)
			s.FixedCharge(down, addOns[2], catalog.FixedChargeModelStandard, `{"amount":"20"}`, "1", fc)

			sub := s.subsCreate(c, p, subsParams{billingTime: period.Calendar})
			subsCheck(t, "status", sub.Status, subscription.StatusActive)
			first := []int64{1000, 1500}
			if tc.prorated {
				first = []int64{1000 * 13 / 31, 1500 * 13 / 31}
			}
			if tc.inAdvance {
				invs := s.subsInvoices(sub)
				if len(invs) != 1 {
					t.Fatalf("starting invoices: got = %d, want = 1", len(invs))
				}
				subsCheckAmounts(t, "starting fixed charges", s.subsFeesOf(invs[0].ID, core.FeeTypeFixedCharge), first...)
			} else {
				subsCheck(t, "starting invoices", len(s.subsInvoices(sub)), 0)
			}

			s.travelTo(subsAt("2023-08-01T00:00:00Z"))
			n := len(s.subsInvoices(sub))
			s.subsBilling()
			invs := s.subsInvoices(sub)
			if len(invs) != n+1 {
				t.Fatalf("august invoices: got = %d, want = %d", len(invs), n+1)
			}
			fixed := s.subsFeesOf(invs[len(invs)-1].ID, core.FeeTypeFixedCharge)
			if tc.inAdvance {
				subsCheckAmounts(t, "august fixed charges", fixed, 1000, 1500)
				subsCheckPeriod(t, "august fixed charges", fixed[0], "2023-08-01T00:00:00Z", "2023-08-31T23:59:59Z")
			} else {
				subsCheckAmounts(t, "august fixed charges", fixed, first...)
				subsCheckPeriod(t, "august fixed charges", fixed[0], "2023-07-19T12:12:00Z", "2023-07-31T23:59:59Z")
			}

			s.travelTo(time.Date(2023, 8, 21, 23, 59, 59, 0, time.UTC))
			s.subsCreate(c, down, subsParams{billingTime: period.Calendar})
			subsCheck(t, "status after downgrade", s.subsReload(sub.ID).Status, subscription.StatusActive)
			all := s.subsOfCustomer(c)
			next := all[len(all)-1]

			s.travelTo(subsAt("2023-09-01T00:00:00Z"))
			n = len(s.subsInvoices(sub))
			s.subsBilling()
			invs = s.subsInvoices(sub)
			if len(invs) != n+1 {
				t.Fatalf("september invoices: got = %d, want = %d", len(invs), n+1)
			}
			inv := invs[len(invs)-1]
			var billed []core.SubscriptionID
			s.tx(func(ctx context.Context, r port.Repos) error {
				rows, err := r.InvoiceSubscriptions.ListByInvoice(ctx, s.OrgID(), inv.ID)
				for _, x := range rows {
					billed = append(billed, x.SubscriptionID)
				}
				return err
			})
			want := []core.SubscriptionID{sub.ID}
			if tc.inAdvance {
				want = append(want, next.ID)
			}
			if !subsSameIDs(billed, want) {
				t.Errorf("september subscriptions: got = %v, want = %v", billed, want)
			}
			subsCheck(t, "september subscription fees", len(s.subsFeesOf(inv.ID, core.FeeTypeSubscription)), 1)
			subsCheck(t, "old status", s.subsReload(sub.ID).Status, subscription.StatusTerminated)
			subsCheck(t, "next status", s.subsReload(next.ID).Status, subscription.StatusActive)
			fixed = s.subsFeesOf(inv.ID, core.FeeTypeFixedCharge)
			if tc.inAdvance {
				subsCheckAmounts(t, "september fixed charges", fixed, 10_000, 2000)
				subsCheckPeriod(t, "september fixed charges", fixed[0], "2023-09-01T00:00:00Z", "2023-09-30T23:59:59Z")
				return
			}
			subsCheckAmounts(t, "september fixed charges", fixed, 1000, 1500)
			subsCheckPeriod(t, "september fixed charges", fixed[0], "2023-08-01T00:00:00Z", "2023-08-31T23:59:59Z")
			subsCheck(t, "next invoices", len(s.subsInvoices(next)), 0)

			s.travelTo(subsAt("2023-10-01T00:00:00Z"))
			s.subsBilling()
			nextInvs := s.subsInvoices(next)
			if len(nextInvs) != 1 {
				t.Fatalf("next invoices: got = %d, want = 1", len(nextInvs))
			}
			fixed = s.subsFeesOf(nextInvs[0].ID, core.FeeTypeFixedCharge)
			subsCheckAmounts(t, "october fixed charges", fixed, 10_000, 2000)
			subsCheckPeriod(t, "october fixed charges", fixed[0], "2023-09-01T00:00:00Z", "2023-09-30T23:59:59Z")
		})
	}
}

// subsSameIDs reports whether a and b hold the same IDs.
func subsSameIDs(a, b []core.SubscriptionID) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

// subsTrial is the world of free_trial_billing_spec.rb: a count metric and
// an in-advance monthly plan of 5,000,000 with the trial.
type subsTrial struct {
	*scenario
	c *customer.Customer
	p *catalog.Plan
	m *catalog.BillableMetric
}

func newSubsTrial(t *testing.T, start time.Time, trialDays int, tz string, custOpts ...func(*customer.Customer)) *subsTrial {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	s := newScenario(t, start)
	c := s.Customer(append([]func(*customer.Customer){func(c *customer.Customer) { c.Timezone = loc }}, custOpts...)...)
	m := s.Metric("bm", catalog.AggregationTypeCount, "")
	p := s.Plan(period.Monthly, 5_000_000, func(p *catalog.Plan) { p.PayInAdvance, p.TrialPeriodDays = true, trialDays })
	return &subsTrial{scenario: s, c: c, p: p, m: m}
}

// subscribe is create_customer_subscription!: a standard charge of 10 on
// the metric, then the subscription.
func (w *subsTrial) subscribe() *subscription.Subscription {
	w.T.Helper()
	w.Charge(w.p, w.m, catalog.ChargeModelStandard, `{"amount":"10"}`)
	return w.subsCreate(w.c, w.p, subsParams{})
}

// upgrade creates the upgrade plan (13-day trial, 10,000,000, charge 12)
// and subscribes to it.
func (w *subsTrial) upgrade() {
	w.T.Helper()
	up := w.Plan(period.Monthly, 10_000_000, func(p *catalog.Plan) { p.PayInAdvance, p.TrialPeriodDays = true, 13 })
	w.Charge(up, w.m, catalog.ChargeModelStandard, `{"amount":"12"}`)
	w.subsCreate(w.c, up, subsParams{})
}

// event is create_usage_event! at at.
func (w *subsTrial) event(at time.Time) {
	w.T.Helper()
	w.travelTo(at)
	cur := w.subsOfCustomer(w.c)
	w.ingest(cur[len(cur)-1], "bm")
}

// invoices returns the customer's invoices, oldest first.
func (w *subsTrial) invoices() []*invoice.Invoice { return w.Invoices(w.c.ID) }

// last returns the customer's last invoice.
func (w *subsTrial) last() *invoice.Invoice {
	w.T.Helper()
	invs := w.invoices()
	if len(invs) == 0 {
		w.T.Fatalf("customer has no invoice")
	}
	return invs[len(invs)-1]
}

// checkCount compares the customer's invoice count.
func (w *subsTrial) checkCount(t *testing.T, what string, want int) {
	t.Helper()
	if got := len(w.invoices()); got != want {
		t.Fatalf("%s invoices: got = %d, want = %d", what, got, want)
	}
}

// checkFees compares the fees of inv: how many, then the first
// subscription, charge and commitment amounts (-1 unchecked).
func (w *subsTrial) checkFees(t *testing.T, what string, inv *invoice.Invoice, count int, sub, charge, commitment int64) {
	t.Helper()
	fees := w.Fees(inv.ID)
	if count >= 0 && len(fees) != count {
		t.Errorf("%s fees: got = %d, want = %d", what, len(fees), count)
	}
	for _, c := range []struct {
		typ  core.FeeType
		want int64
	}{{core.FeeTypeSubscription, sub}, {core.FeeTypeCharge, charge}, {core.FeeTypeCommitment, commitment}} {
		if c.want < 0 {
			continue
		}
		fs := w.subsFeesOf(inv.ID, c.typ)
		if len(fs) == 0 {
			t.Errorf("%s %s fee: got none, want = %d", what, c.typ, c.want)
			continue
		}
		if fs[0].Amount != c.want {
			t.Errorf("%s %s fee: got = %d, want = %d", what, c.typ, fs[0].Amount, c.want)
		}
	}
}

func TestScenarioSubscriptionFreeTrialBilling(t *testing.T) {
	at := func(v string) time.Time { return subsAt(v + "Z") }
	grace := func(days int) func(*customer.Customer) {
		return func(c *customer.Customer) { c.InvoiceGracePeriodDays = &days }
	}

	t.Run("bills the customer at the beginning of the subscription", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:44
		w := newSubsTrial(t, at("2024-03-05T12:12:00"), 0, "UTC")
		w.subscribe()
		w.checkCount(t, "start", 1)
		if len(w.subsFeesOf(w.last().ID, core.FeeTypeSubscription)) == 0 {
			t.Errorf("subscription fee: got none, want one")
		}
	})

	t.Run("bills the customer at the end of the free trial", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:57
		w := newSubsTrial(t, at("2024-03-05T12:12:00"), 10, "UTC")
		w.subscribe()
		w.checkCount(t, "start", 0)
		w.travelTo(at("2024-03-10T12:12:00"))
		w.subsBilling()
		w.checkCount(t, "march 10", 0)
		w.travelTo(at("2024-03-15T12:02:00"))
		w.subsBilling()
		w.checkCount(t, "in trial", 0)
		w.travelTo(at("2024-03-15T13:02:00"))
		w.subsBilling()
		w.checkCount(t, "trial end", 1)
		// (31 - 4 - 10) / 31 * 5,000,000
		w.checkFees(t, "trial end", w.last(), 1, 2_741_935, -1, -1)
	})

	t.Run("does not bill the customer if it was already billed at the beginning of the trial", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:90
		w := newSubsTrial(t, at("2024-03-05T12:12:00"), 10, "UTC")
		sub := w.subscribe()
		w.checkCount(t, "start", 0)
		setTrial := func(days int) {
			w.p.TrialPeriodDays = days
			w.tx(func(ctx context.Context, r port.Repos) error { return r.Plans.Update(ctx, w.p) })
		}
		setTrial(0)
		w.run(kernel.JobBillSubscription, kernel.BillSubscriptionArgs{
			SubscriptionIDs: []core.SubscriptionID{sub.ID}, BillingAt: w.Now(), Reason: string(invoice.InvoicingReasonSubscriptionStarting),
		})
		w.checkCount(t, "forced start", 1)
		setTrial(10)
		for _, v := range []string{"2024-03-10T12:12:00", "2024-03-15T15:00:00", "2024-03-20T12:12:00"} {
			w.travelTo(at(v))
			w.subsBilling()
			w.checkCount(t, v, 1)
		}
	})

	upgrade := func(t *testing.T, w *subsTrial, upgradeAt string, events []string) {
		t.Helper()
		w.subscribe()
		w.checkCount(t, "start", 0)
		w.subsBilling()
		w.checkCount(t, "start billing", 0)
		w.event(at(events[0]))
		w.travelTo(at(upgradeAt))
		w.upgrade()
		w.subsBilling()
		w.checkCount(t, "upgrade", 1)
		w.checkFees(t, "upgrade", w.last(), 1, -1, 1000, -1)
		for _, e := range events[1:] {
			w.event(at(e))
		}
	}
	finish := func(t *testing.T, w *subsTrial, wantCharge int64) {
		t.Helper()
		w.travelTo(at("2024-03-18T13:00:00"))
		w.subsBilling()
		w.checkCount(t, "upgraded trial end", 2)
		// (31 - 4 - 13) / 31 * 10,000,000
		w.checkFees(t, "upgraded trial end", w.last(), 1, 4_516_129, -1, -1)
		w.travelTo(at("2024-04-01T12:12:00"))
		w.subsBilling()
		w.checkCount(t, "april", 3)
		w.checkFees(t, "april", w.last(), 2, 10_000_000, wantCharge, -1)
	}

	t.Run("bills the subscription of the upgraded plan at the end of the trial", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:124
		w := newSubsTrial(t, at("2024-03-05T12:12:00"), 10, "UTC")
		upgrade(t, w, "2024-03-10T12:12:00", []string{"2024-03-08T00:00:00", "2024-03-11T00:00:00"})
		w.travelTo(at("2024-03-15T13:00:00"))
		w.subsBilling()
		w.checkCount(t, "old trial end", 1)
		finish(t, w, 1200)
	})

	t.Run("upgrade on day one bills the usage instantly and the upgraded plan at the end of trial", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:181
		w := newSubsTrial(t, at("2024-03-05T12:12:00"), 10, "UTC")
		upgrade(t, w, "2024-03-05T13:15:00", []string{"2024-03-05T13:00:00", "2024-03-05T15:00:00", "2024-03-05T15:01:00"})
		finish(t, w, 2400)
	})

	t.Run("with a grace period bills the usage instantly and the upgraded plan at the end of trial", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:231
		w := newSubsTrial(t, at("2024-03-05T12:12:00"), 10, "UTC", grace(2))
		w.subscribe()
		w.checkCount(t, "start", 0)
		w.subsBilling()
		w.checkCount(t, "start billing", 0)
		w.event(at("2024-03-05T13:00:00"))
		w.travelTo(at("2024-03-05T13:15:00"))
		w.upgrade()
		w.subsBilling()
		w.checkCount(t, "upgrade", 1)
		w.checkFees(t, "upgrade", w.last(), 1, -1, 1000, -1)
		subsCheck(t, "upgrade status", w.last().Status, invoice.StatusDraft)
		w.travelTo(at("2024-03-07T18:00:00"))
		w.performFinalize()
		w.checkFees(t, "finalized", w.last(), 1, -1, 1000, -1)
		subsCheck(t, "finalized status", w.last().Status, invoice.StatusFinalized)
		w.event(at("2024-03-05T15:00:00"))
		w.event(at("2024-03-05T15:01:00"))
		finish(t, w, 2400)
	})

	trialBeyondPeriod := func(t *testing.T, w *subsTrial) {
		t.Helper()
		w.subscribe()
		w.checkCount(t, "start", 0)
		w.event(at("2024-03-10T00:00:00"))
		w.travelTo(at("2024-04-01T00:00:00"))
		w.subsBilling()
		w.checkCount(t, "april", 1)
		w.checkFees(t, "april", w.last(), 1, -1, 1000, -1)
	}

	t.Run("bills subscription at the end of a free trial longer than the period", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:300
		w := newSubsTrial(t, at("2024-03-05T12:12:00"), 45, "UTC")
		trialBeyondPeriod(t, w)
		w.travelTo(at("2024-04-19T13:01:00"))
		w.subsBilling()
		w.checkCount(t, "trial end", 2)
		w.checkFees(t, "trial end", w.last(), 1, 2_000_000, -1, -1) // 5,000,000 * 12 / 30
	})

	t.Run("bills at the end of a long free trial but finalizes after the grace period", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:325
		w := newSubsTrial(t, at("2024-03-05T12:12:00"), 45, "UTC", grace(2))
		trialBeyondPeriod(t, w)
		w.travelTo(at("2024-04-19T13:01:00"))
		w.subsBilling()
		w.checkCount(t, "trial end", 2)
		w.checkFees(t, "trial end", w.last(), 1, 2_000_000, -1, -1)
		subsCheck(t, "trial end status", w.last().Status, invoice.StatusDraft)
		w.travelTo(at("2024-04-21T13:22:00"))
		w.run(kernel.JobRefreshDraft, kernel.InvoiceArgs{InvoiceID: w.last().ID})
		w.checkCount(t, "refreshed", 2)
		w.checkFees(t, "refreshed", w.last(), 1, 2_000_000, -1, -1)
		subsCheck(t, "refreshed status", w.last().Status, invoice.StatusDraft)
		w.performFinalize()
		w.checkCount(t, "finalized", 2)
		w.checkFees(t, "finalized", w.last(), 1, 2_000_000, -1, -1)
		subsCheck(t, "finalized status", w.last().Status, invoice.StatusFinalized)
	})

	t.Run("bills minimum commitment on billing day, despite being in trial", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:368
		w := newSubsTrial(t, at("2024-03-05T12:12:00"), 45, "UTC")
		w.Commitment(w.p, 10_000_000)
		trialBeyondPeriod(t, w)
		w.event(at("2024-04-10T00:00:00"))
		w.travelTo(at("2024-04-19T13:01:00"))
		w.subsBilling()
		w.checkCount(t, "trial end", 2)
		w.checkFees(t, "trial end", w.last(), 1, 2_000_000, -1, -1)
		w.travelTo(at("2024-05-01T00:00:00"))
		w.subsBilling()
		w.checkCount(t, "may", 3)
		// The true-up looks at the previous month, when the trial ended and
		// only 2,000,000 of subscription fee was paid.
		w.checkFees(t, "may", w.last(), 3, 5_000_000, 1000, 10_000_000-2_000_000-1000)
	})

	t.Run("bills subscription and usage-based charges when the trial ends on billing day", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:413
		start := at("2024-03-22T01:12:00")
		w := newSubsTrial(t, start, 10, "Europe/Paris")
		w.subscribe()
		w.checkCount(t, "start", 0)
		w.event(at("2024-03-23T00:00:00"))
		w.checkCount(t, "event", 0)
		w.travelTo(at("2024-03-31T22:01:00"))
		w.subsBilling()
		w.checkCount(t, "billing day", 1)
		w.checkFees(t, "billing day", w.last(), -1, 5_000_000, 1000, -1)
		w.travelTo(at("2024-04-01T13:11:00"))
		w.subsBilling()
		w.checkCount(t, "after trial", 1)
		sub := w.subsOfCustomer(w.c)[0]
		if sub.TrialEndedAt == nil || !sub.TrialEndedAt.Equal(start.AddDate(0, 0, 10)) {
			t.Errorf("trial_ended_at: got = %v, want = %s", sub.TrialEndedAt, start.AddDate(0, 0, 10))
		}
	})

	t.Run("follows customer timezone for billing", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:456
		w := newSubsTrial(t, at("2024-03-22T18:12:00"), 10, "Asia/Tokyo")
		w.subscribe()
		w.checkCount(t, "start", 0)
		w.event(at("2024-03-28T00:00:00"))
		w.checkCount(t, "event", 0)
		w.travelTo(at("2024-03-31T15:10:00"))
		w.subsBilling()
		w.checkCount(t, "billing day", 1)
		w.checkFees(t, "billing day", w.last(), 1, -1, 1000, -1)
		w.travelTo(at("2024-04-01T13:11:00"))
		w.subsBilling()
		w.checkCount(t, "april 1st", 1)
		w.travelTo(at("2024-04-01T19:11:00"))
		w.subsBilling()
		w.checkCount(t, "trial end", 2)
		w.checkFees(t, "trial end", w.last(), 1, 4_833_333, -1, -1)
	})

	t.Run("bills subscription and usage-based charges with the periodic biller after the trial biller", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/free_trial_billing_spec.rb:495
		start := at("2024-03-22T12:12:00")
		w := newSubsTrial(t, start, 10, "Europe/Paris")
		w.subscribe()
		w.checkCount(t, "start", 0)
		w.event(at("2024-03-23T00:00:00"))
		w.checkCount(t, "event", 0)
		w.travelTo(at("2024-04-01T13:01:00"))
		w.subsEndTrials()
		w.checkCount(t, "trial end", 1)
		sub := w.subsOfCustomer(w.c)[0]
		if sub.TrialEndedAt == nil || !sub.TrialEndedAt.Equal(start.AddDate(0, 0, 10)) {
			t.Errorf("trial_ended_at: got = %v, want = %s", sub.TrialEndedAt, start.AddDate(0, 0, 10))
		}
		w.checkFees(t, "trial end", w.last(), 1, 5_000_000, -1, -1)
		w.travelTo(at("2024-04-01T15:11:00"))
		w.performBilling()
		w.checkCount(t, "periodic", 2)
		fees := w.Fees(w.last().ID)
		if len(fees) != 1 || fees[0].Type != core.FeeTypeCharge || fees[0].Amount != 1000 {
			t.Errorf("periodic fees: got = %v, want = one charge fee of 1000", subsAmounts(fees))
		}
	})
}

func TestScenarioSubscriptionUpgrade(t *testing.T) {
	// source: spec/scenarios/subscriptions/upgrade_spec.rb:32
	s := newScenario(t, subsAt("2023-06-29T12:12:00Z"))
	c := s.Customer()
	monthly := s.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.PayInAdvance = true })
	yearly := s.Plan(period.Yearly, 12_000, func(p *catalog.Plan) { p.PayInAdvance = true })

	sub := s.subsCreate(c, monthly, subsParams{billingTime: period.Anniversary})
	subsCheck(t, "status", sub.Status, subscription.StatusActive)
	invs := s.subsInvoices(sub)
	if len(invs) != 1 {
		t.Fatalf("starting invoices: got = %d, want = 1", len(invs))
	}
	subsCheck(t, "starting fees", subsSum(s.Fees(invs[0].ID)), 1000)
	checkSubsBounds(t, "starting", s.subsLastInvoiceSub(sub), subsBounds{"2023-06-29T00:00:00Z", "2023-07-28T23:59:59Z"})

	for _, step := range []struct {
		at     string
		bounds subsBounds
	}{
		{"2023-07-29T12:12:00Z", subsBounds{"2023-07-29T00:00:00Z", "2023-08-28T23:59:59Z", "2023-06-29T12:12:00Z", "2023-07-28T23:59:59Z"}},
		{"2023-08-29T12:12:00Z", subsBounds{"2023-08-29T00:00:00Z", "2023-09-28T23:59:59Z", "2023-07-29T00:00:00Z", "2023-08-28T23:59:59Z"}},
	} {
		s.travelTo(subsAt(step.at))
		n := len(s.subsInvoices(sub))
		s.subsBilling()
		invs = s.subsInvoices(sub)
		if len(invs) != n+1 {
			t.Fatalf("%s invoices: got = %d, want = %d", step.at, len(invs), n+1)
		}
		subsCheck(t, step.at+" fees", subsSum(s.Fees(invs[len(invs)-1].ID)), 1000)
		checkSubsBounds(t, step.at, s.subsLastInvoiceSub(sub), step.bounds)
	}

	s.travelTo(subsAt("2023-09-28T05:00:00Z"))
	s.subsCreate(c, yearly, subsParams{billingTime: period.Anniversary})
	sub = s.subsReload(sub.ID)
	subsCheck(t, "old status", sub.Status, subscription.StatusTerminated)
	subsCheck(t, "old invoices", len(s.subsInvoices(sub)), 4)
	subsCheck(t, "customer invoices", len(s.Invoices(c.ID)), 4)
	checkSubsBounds(t, "termination", s.subsLastInvoiceSub(sub), subsBounds{"", "", "2023-08-29T00:00:00Z", "2023-09-28T05:00:00Z"})
	all := s.subsOfCustomer(c)
	next := all[len(all)-1]
	subsCheck(t, "next plan", next.PlanID, yearly.ID)
	subsCheck(t, "next status", next.Status, subscription.StatusActive)
	nextInvs := s.subsInvoices(next)
	if len(nextInvs) != 1 {
		t.Fatalf("next invoices: got = %d, want = 1", len(nextInvs))
	}
	var credited int64
	for _, inv := range s.Invoices(c.ID) {
		for _, cn := range s.CreditNotes(inv.ID) {
			credited = cn.CreditAmount
		}
	}
	subsCheck(t, "credit note", credited, 32) // 1000 / 31
	days := int(subsAt("2024-06-29T00:00:00Z").Sub(subsAt("2023-09-28T00:00:00Z")).Hours() / 24)
	subsCheck(t, "yearly fees", subsSum(s.Fees(nextInvs[0].ID)), int64(math.Round(float64(days)*12_000/366)))
}

func TestScenarioSubscriptionUpgradeWithFixedCharges(t *testing.T) {
	type world struct {
		s        *scenario
		c        *customer.Customer
		p, up    *catalog.Plan
		sub      *subscription.Subscription
		upgraded func() *subscription.Subscription
	}
	setup := func(t *testing.T, start string, inAdvance, prorated bool) *world {
		s := newScenario(t, subsAt(start))
		c := s.Customer()
		p := s.Plan(period.Monthly, 100)
		up := s.Plan(period.Monthly, 10_000)
		addOns := []*catalog.AddOn{s.AddOn(200), s.AddOn(200), s.AddOn(200)}
		fc := func(f *catalog.FixedCharge) { f.PayInAdvance, f.Prorated = inAdvance, prorated }
		s.FixedCharge(p, addOns[0], catalog.FixedChargeModelStandard, `{"amount":"1"}`, "10", fc)
		s.FixedCharge(p, addOns[1], catalog.FixedChargeModelStandard, `{"amount":"3"}`, "5", fc)
		s.FixedCharge(up, addOns[1], catalog.FixedChargeModelStandard, `{"amount":"10"}`, "10", fc)
		s.FixedCharge(up, addOns[2], catalog.FixedChargeModelStandard, `{"amount":"20"}`, "1", fc)
		w := &world{s: s, c: c, p: p, up: up}
		w.sub = s.subsCreate(c, p, subsParams{billingTime: period.Calendar})
		subsCheck(t, "status", w.sub.Status, subscription.StatusActive)
		w.upgraded = func() *subscription.Subscription {
			s.subsCreate(c, up, subsParams{billingTime: period.Calendar})
			subsCheck(t, "old status", s.subsReload(w.sub.ID).Status, subscription.StatusTerminated)
			all := s.subsOfCustomer(c)
			next := all[len(all)-1]
			subsCheck(t, "next status", next.Status, subscription.StatusActive)
			return next
		}
		return w
	}
	// lastFixed returns the fixed charge fees of the last invoice of sub,
	// checking sub has want invoices.
	lastFixed := func(t *testing.T, s *scenario, sub *subscription.Subscription, what string, want int) (*invoice.Invoice, []*invoice.Fee) {
		t.Helper()
		invs := s.subsInvoices(sub)
		if len(invs) != want {
			t.Fatalf("%s invoices: got = %d, want = %d", what, len(invs), want)
		}
		inv := invs[len(invs)-1]
		return inv, s.subsFeesOf(inv.ID, core.FeeTypeFixedCharge)
	}
	billedSubs := func(s *scenario, inv *invoice.Invoice) []core.SubscriptionID {
		var out []core.SubscriptionID
		s.tx(func(ctx context.Context, r port.Repos) error {
			rows, err := r.InvoiceSubscriptions.ListByInvoice(ctx, s.OrgID(), inv.ID)
			for _, x := range rows {
				out = append(out, x.SubscriptionID)
			}
			return err
		})
		return out
	}
	rnd := func(v float64) int64 { return int64(math.Round(v)) }

	t.Run("in advance prorated", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/upgrade_spec.rb:155
		w := setup(t, "2023-07-19T12:12:00Z", true, true)
		s := w.s
		_, fixed := lastFixed(t, s, w.sub, "starting", 1)
		subsCheckAmounts(t, "starting fixed charges", fixed, 1000*13/31, 1500*13/31)
		subsCheckPeriod(t, "starting fixed charges", fixed[0], "2023-07-19T12:12:00Z", "2023-07-31T23:59:59Z")
		s.travelTo(subsAt("2023-08-01T00:00:00Z"))
		s.subsBilling()
		_, fixed = lastFixed(t, s, w.sub, "august", 2)
		subsCheckAmounts(t, "august fixed charges", fixed, 1000, 1500)
		subsCheckPeriod(t, "august fixed charges", fixed[0], "2023-08-01T00:00:00Z", "2023-08-31T23:59:59Z")

		s.travelTo(subsAt("2023-08-21T12:00:00Z"))
		next := w.upgraded()
		inv, _ := lastFixed(t, s, w.sub, "upgrade", 3)
		if got := billedSubs(s, inv); !subsSameIDs(got, []core.SubscriptionID{w.sub.ID, next.ID}) {
			t.Errorf("upgrade subscriptions: got = %v, want = [%s %s]", got, w.sub.ID, next.ID)
		}
		subsCheckAmounts(t, "upgrade subscription fees", s.subsFeesOf(inv.ID, core.FeeTypeSubscription), rnd(100*20/31.0))
		_, fixed = lastFixed(t, s, next, "next", 1)
		subsCheckAmounts(t, "next fixed charges", fixed, rnd(10_000*11/31.0)-rnd(1500*11/31.0), rnd(2000*11/31.0))
		subsCheckPeriod(t, "next fixed charges", fixed[0], "2023-08-21T12:00:00Z", "2023-08-31T23:59:59Z")

		s.travelTo(subsAt("2023-09-01T00:00:00Z"))
		s.subsBilling()
		_, fixed = lastFixed(t, s, next, "september", 2)
		subsCheckAmounts(t, "september fixed charges", fixed, 10_000, 2000)
		subsCheckPeriod(t, "september fixed charges", fixed[0], "2023-09-01T00:00:00Z", "2023-09-30T23:59:59Z")
	})

	t.Run("in advance prorated, upgraded on the day it starts", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/upgrade_spec.rb:247
		w := setup(t, "2025-12-22T12:12:00Z", true, true)
		s := w.s
		_, fixed := lastFixed(t, s, w.sub, "starting", 1)
		subsCheckAmounts(t, "starting fixed charges", fixed, rnd(1000.0*10/31), rnd(1500.0*10/31))
		subsCheckPeriod(t, "starting fixed charges", fixed[0], "2025-12-22T12:12:00Z", "2025-12-31T23:59:59Z")
		s.travelTo(subsAt("2025-12-22T13:12:00Z"))
		next := w.upgraded()
		lastFixed(t, s, w.sub, "old", 2)
		_, fixed = lastFixed(t, s, next, "next", 1)
		subsCheckAmounts(t, "next fixed charges", fixed, rnd(10_000.0*10/31)-rnd(1500.0*10/31), 2000*10/31)
		subsCheckPeriod(t, "next fixed charges", fixed[0], "2025-12-22T13:12:00Z", "2025-12-31T23:59:59Z")
	})

	t.Run("in advance prorated, first fee prorated for less than a month", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/upgrade_spec.rb:302
		w := setup(t, "2025-12-10T12:12:00Z", true, true)
		s := w.s
		_, fixed := lastFixed(t, s, w.sub, "starting", 1)
		subsCheckAmounts(t, "starting fixed charges", fixed, rnd(1000.0*22/31), rnd(1500.0*22/31))
		subsCheckPeriod(t, "starting fixed charges", fixed[0], "2025-12-10T12:12:00Z", "2025-12-31T23:59:59Z")
		s.travelTo(subsAt("2025-12-22T12:12:00Z"))
		next := w.upgraded()
		lastFixed(t, s, w.sub, "old", 2)
		_, fixed = lastFixed(t, s, next, "next", 1)
		covered := rnd(float64(rnd(1500.0*22/31)) * 10.0 / 22)
		subsCheckAmounts(t, "next fixed charges", fixed, rnd(10_000.0*10/31)-covered, rnd(2000*10/31.0))
		subsCheckPeriod(t, "next fixed charges", fixed[0], "2025-12-22T12:12:00Z", "2025-12-31T23:59:59Z")
	})

	t.Run("in advance not prorated", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/upgrade_spec.rb:353
		w := setup(t, "2023-07-19T12:12:00Z", true, false)
		s := w.s
		_, fixed := lastFixed(t, s, w.sub, "starting", 1)
		subsCheckAmounts(t, "starting fixed charges", fixed, 1000, 1500)
		subsCheckPeriod(t, "starting fixed charges", fixed[0], "2023-07-19T12:12:00Z", "2023-07-31T23:59:59Z")
		s.travelTo(subsAt("2023-08-01T00:00:00Z"))
		s.subsBilling()
		_, fixed = lastFixed(t, s, w.sub, "august", 2)
		subsCheckAmounts(t, "august fixed charges", fixed, 1000, 1500)
		subsCheckPeriod(t, "august fixed charges", fixed[0], "2023-08-01T00:00:00Z", "2023-08-31T23:59:59Z")

		s.travelTo(subsAt("2023-08-21T12:00:00Z"))
		next := w.upgraded()
		inv, fixed := lastFixed(t, s, w.sub, "upgrade", 3)
		if got := billedSubs(s, inv); !subsSameIDs(got, []core.SubscriptionID{w.sub.ID, next.ID}) {
			t.Errorf("upgrade subscriptions: got = %v, want = [%s %s]", got, w.sub.ID, next.ID)
		}
		subsCheckAmounts(t, "upgrade subscription fees", s.subsFeesOf(inv.ID, core.FeeTypeSubscription), rnd(100*20/31.0))
		subsCheckAmounts(t, "upgrade fixed charges", fixed, 10_000, 2000)
		subsCheckPeriod(t, "upgrade fixed charges", fixed[0], "2023-08-21T12:00:00Z", "2023-08-31T23:59:59Z")
		lastFixed(t, s, next, "next", 1)

		s.travelTo(subsAt("2023-09-01T00:00:00Z"))
		s.subsBilling()
		_, fixed = lastFixed(t, s, next, "september", 2)
		subsCheckAmounts(t, "september fixed charges", fixed, 10_000, 2000)
		subsCheckPeriod(t, "september fixed charges", fixed[0], "2023-09-01T00:00:00Z", "2023-09-30T23:59:59Z")
	})

	t.Run("in arrears prorated", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/upgrade_spec.rb:438
		w := setup(t, "2023-07-19T12:12:00Z", false, true)
		s := w.s
		subsCheck(t, "starting invoices", len(s.subsInvoices(w.sub)), 0)
		s.travelTo(subsAt("2023-08-01T00:00:00Z"))
		s.subsBilling()
		_, fixed := lastFixed(t, s, w.sub, "august", 1)
		subsCheckAmounts(t, "august fixed charges", fixed, 1000*13/31, 1500*13/31)
		subsCheckPeriod(t, "august fixed charges", fixed[0], "2023-07-19T12:12:00Z", "2023-07-31T23:59:59Z")

		s.travelTo(subsAt("2023-08-21T12:00:00Z"))
		next := w.upgraded()
		_, fixed = lastFixed(t, s, w.sub, "termination", 2)
		subsCheckAmounts(t, "termination fixed charges", fixed, 1000*21/31, 1500*21/31)
		subsCheckPeriod(t, "termination fixed charges", fixed[0], "2023-08-01T00:00:00Z", "2023-08-21T12:00:00Z")
		subsCheck(t, "next invoices", len(s.subsInvoices(next)), 0)

		s.travelTo(subsAt("2023-09-01T00:00:00Z"))
		s.subsBilling()
		_, fixed = lastFixed(t, s, next, "september", 1)
		subsCheckAmounts(t, "september fixed charges", fixed, 10_000*11/31, rnd(2000*11/31.0))
		subsCheckPeriod(t, "september fixed charges", fixed[0], "2023-08-21T12:00:00Z", "2023-08-31T23:59:59Z")

		s.travelTo(subsAt("2023-10-01T00:00:00Z"))
		s.subsBilling()
		_, fixed = lastFixed(t, s, next, "october", 2)
		subsCheckAmounts(t, "october fixed charges", fixed, 10_000, 2000)
		subsCheckPeriod(t, "october fixed charges", fixed[0], "2023-09-01T00:00:00Z", "2023-09-30T23:59:59Z")
	})

	t.Run("in arrears not prorated", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/upgrade_spec.rb:517
		w := setup(t, "2023-07-19T12:12:00Z", false, false)
		s := w.s
		subsCheck(t, "starting invoices", len(s.subsInvoices(w.sub)), 0)
		s.travelTo(subsAt("2023-08-01T00:00:00Z"))
		s.subsBilling()
		_, fixed := lastFixed(t, s, w.sub, "august", 1)
		subsCheckAmounts(t, "august fixed charges", fixed, 1000, 1500)
		subsCheckPeriod(t, "august fixed charges", fixed[0], "2023-07-19T12:12:00Z", "2023-07-31T23:59:59Z")

		s.travelTo(subsAt("2023-08-21T12:00:00Z"))
		next := w.upgraded()
		_, fixed = lastFixed(t, s, w.sub, "termination", 2)
		subsCheckAmounts(t, "termination fixed charges", fixed, 1000, 1500)
		subsCheckPeriod(t, "termination fixed charges", fixed[0], "2023-08-01T00:00:00Z", "2023-08-21T12:00:00Z")
		subsCheck(t, "next invoices", len(s.subsInvoices(next)), 0)

		s.travelTo(subsAt("2023-09-01T00:00:00Z"))
		s.subsBilling()
		_, fixed = lastFixed(t, s, next, "september", 1)
		subsCheckAmounts(t, "september fixed charges", fixed, 10_000, 2000)
		subsCheckPeriod(t, "september fixed charges", fixed[0], "2023-08-21T12:00:00Z", "2023-08-31T23:59:59Z")
	})
}

// subsCustomerCreditNotes returns the credit notes of c, oldest first.
func (s *scenario) subsCustomerCreditNotes(c *customer.Customer) []*creditnote.CreditNote {
	s.T.Helper()
	var out []*creditnote.CreditNote
	for _, inv := range s.Invoices(c.ID) {
		out = append(out, s.CreditNotes(inv.ID)...)
	}
	slices.SortStableFunc(out, func(a, b *creditnote.CreditNote) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

func TestScenarioSubscriptionMultipleUpgrade(t *testing.T) {
	tests := []struct {
		source      string
		billingTime period.BillingTime
		// fees are the fees of the three starting invoices, credits the
		// credit amounts of the two upgrades.
		fees    [3]int64
		credits [2]int64
	}{
		{"spec/scenarios/subscriptions/multiple_upgrade_spec.rb:46", period.Calendar, [3]int64{871, 968, 1226}, [2]int64{806, 1210}},
		{"spec/scenarios/subscriptions/multiple_upgrade_spec.rb:123", period.Anniversary, [3]int64{1000, 1161, 1471}, [2]int64{968, 1451}},
	}
	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			s := newScenario(t, subsAt("2024-03-05T12:12:00Z"))
			tax := s.Tax("vat_25", "25")
			s.UpdateBillingEntity(func(b *organization.BillingEntity) { b.TaxIDs = append(b.TaxIDs, tax.ID) })
			c := s.Customer()
			var plans []*catalog.Plan
			for _, amount := range []int64{1000, 1500, 1900} {
				plans = append(plans, s.Plan(period.Monthly, amount, func(p *catalog.Plan) { p.PayInAdvance = true }))
			}
			for i, step := range []string{"2024-03-05T12:12:00Z", "2024-03-12T12:12:00Z", "2024-03-12T13:12:00Z"} {
				s.travelTo(subsAt(step))
				var prev *subscription.Subscription
				if i > 0 {
					all := s.subsOfCustomer(c)
					prev = all[len(all)-1]
				}
				sub := s.subsCreate(c, plans[i], subsParams{billingTime: tc.billingTime})
				subsCheck(t, step+" invoices", len(s.Invoices(c.ID)), i+1)
				if prev != nil {
					subsCheck(t, step+" previous status", s.subsReload(prev.ID).Status, subscription.StatusTerminated)
				}
				subsCheck(t, step+" status", sub.Status, subscription.StatusActive)
				invs := s.subsInvoices(sub)
				if len(invs) != 1 {
					t.Fatalf("%s subscription invoices: got = %d, want = 1", step, len(invs))
				}
				subsCheck(t, step+" fees", invs[0].FeesAmount, tc.fees[i])
				if i == 0 {
					continue
				}
				cns := s.subsCustomerCreditNotes(c)
				if len(cns) != i {
					t.Fatalf("%s credit notes: got = %d, want = %d", step, len(cns), i)
				}
				subsCheck(t, step+" credit", cns[i-1].CreditAmount, tc.credits[i-1])
			}
		})
	}
}

func TestScenarioSubscriptionUpgradeProration(t *testing.T) {
	setup := func(t *testing.T, start string) (*scenario, *customer.Customer, *catalog.Plan, *catalog.Plan) {
		s := newScenario(t, subsAt(start))
		c := s.Customer(func(c *customer.Customer) { c.Timezone = time.UTC })
		// One day of the quarter costs 1000 when prorated on the whole
		// period.
		p := s.Plan(period.Quarterly, 91_000)
		cheaper := s.Plan(period.Quarterly, 45_500)
		return s, c, p, cheaper
	}
	terminatedFee := func(t *testing.T, s *scenario, sub *subscription.Subscription) {
		t.Helper()
		s.travelTo(subsAt("2024-06-20T10:00:00Z"))
		sub = s.subsTerminate(sub)
		invs := s.subsInvoices(sub)
		if len(invs) == 0 {
			t.Fatal("no termination invoice")
		}
		subsCheckAmounts(t, "terminated fee", s.subsFeesOf(invs[len(invs)-1].ID, core.FeeTypeSubscription), 32_000)
	}

	t.Run("an upgraded subscription prorates on the whole period", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/upgrade_proration_spec.rb:20
		s, c, p, cheaper := setup(t, "2024-01-15T10:00:00Z")
		s.subsCreate(c, cheaper, subsParams{billingTime: period.Anniversary})
		s.travelTo(subsAt("2024-05-20T10:00:00Z"))
		sub := s.subsCreate(c, p, subsParams{billingTime: period.Anniversary})
		subsCheck(t, "plan", sub.PlanID, p.ID)
		subsCheck(t, "subscription_at", sub.SubscriptionAt, subsAt("2024-01-15T10:00:00Z"))
		subsCheck(t, "started_at", *sub.StartedAt, subsAt("2024-05-20T10:00:00Z"))
		terminatedFee(t, s, sub)
	})

	t.Run("a subscription started mid calendar period prorates on the whole period", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/upgrade_proration_spec.rb:60
		s, c, p, _ := setup(t, "2024-05-20T10:00:00Z")
		sub := s.subsCreate(c, p, subsParams{billingTime: period.Calendar})
		subsCheck(t, "started_at", *sub.StartedAt, subsAt("2024-05-20T10:00:00Z"))
		terminatedFee(t, s, sub)
	})
}

// subsManual is the world of terminate_manually_spec.rb: a 20% billing
// entity tax, a sum metric on item_count with a standard charge of 1 and a
// monthly plan of 10,000 subscribed on 2025-02-08.
type subsManual struct {
	*scenario
	c   *customer.Customer
	sub *subscription.Subscription
}

func newSubsManual(t *testing.T, inAdvance bool, subscriptionAt *time.Time) *subsManual {
	t.Helper()
	s := newScenario(t, subsAt("2025-02-08T00:00:00Z"))
	tax := s.Tax("vat_20", "20")
	s.UpdateBillingEntity(func(b *organization.BillingEntity) { b.TaxIDs = append(b.TaxIDs, tax.ID) })
	c := s.Customer(func(c *customer.Customer) { c.Timezone = time.UTC })
	m := s.Metric("bm", catalog.AggregationTypeSum, "item_count")
	p := s.Plan(period.Monthly, 10_000, func(p *catalog.Plan) { p.PayInAdvance = inAdvance })
	s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := s.subsCreate(c, p, subsParams{subscriptionAt: subscriptionAt})
	return &subsManual{scenario: s, c: c, sub: sub}
}

// events adds 5 events of 10 items at at: 50 euros of charges.
func (w *subsManual) events(at time.Time) {
	w.T.Helper()
	w.travelTo(at)
	for range 5 {
		w.ingest(w.sub, "bm", "item_count", "10")
	}
}

// terminate terminates the subscription at at without running the jobs it
// enqueues (lago terminate_subscription with perform_jobs: false).
func (w *subsManual) terminate(at time.Time, in lifecycle.TerminateInput) error {
	w.T.Helper()
	w.travelTo(at)
	in.OrganizationID, in.SubscriptionID = w.OrgID(), w.sub.ID
	_, err := w.E.Lifecycle.Terminate(w.Ctx, in)
	w.sub = w.subsReload(w.sub.ID)
	return err
}

// invoices returns the invoices of the subscription, oldest first.
func (w *subsManual) invoices() []*invoice.Invoice {
	return w.subsInvoices(w.sub)
}

// checkInAdvanceBilled is expect_pay_in_advance_to_be_billed: 21 of 28
// days, 75 euros plus 20% tax.
func (w *subsManual) checkInAdvanceBilled(t *testing.T) {
	t.Helper()
	subsCheck(t, "status", w.sub.Status, subscription.StatusActive)
	invs := w.invoices()
	if len(invs) != 1 {
		t.Fatalf("subscription invoices: got = %d, want = 1", len(invs))
	}
	subsCheck(t, "starting credit notes", len(w.CreditNotes(invs[0].ID)), 0)
	subsCheck(t, "starting taxes", invs[0].TaxesAmount, 1500)
	subsCheck(t, "starting total", invs[0].TotalAmount, 9000)
}

// creditNote returns the sole credit note of the subscription invoice.
func (w *subsManual) creditNote(t *testing.T) *creditnote.CreditNote {
	t.Helper()
	cns := w.CreditNotes(w.invoices()[0].ID)
	if len(cns) != 1 {
		t.Fatalf("credit notes: got = %d, want = 1", len(cns))
	}
	return cns[0]
}

// checkCreditNote is expect_credit_note_to_be_created: 7 unused days, 25
// euros plus 20% tax. Its balance is not checked: the termination invoice
// is billed, and consumes it, in the termination's transaction, where lago
// bills it in a later job.
func (w *subsManual) checkCreditNote(t *testing.T, refund int64) {
	t.Helper()
	cn := w.creditNote(t)
	subsCheck(t, "credit note total", cn.TotalAmount, 3000)
	subsCheck(t, "credit note credit", cn.CreditAmount, 3000-refund)
	subsCheck(t, "credit note refund", cn.RefundAmount, refund)
}

// checkBilled checks the termination invoice, the last of the
// subscription, and its sole charge fee of 50 euros plus tax.
func (w *subsManual) checkBilled(t *testing.T, invoices int, subTotal, taxes, creditNotes, total int64) {
	t.Helper()
	invs := w.invoices()
	if len(invs) != invoices {
		t.Fatalf("subscription invoices: got = %d, want = %d", len(invs), invoices)
	}
	inv := invs[len(invs)-1]
	subsCheck(t, "billed status", inv.Status, invoice.StatusFinalized)
	subsCheck(t, "billed sub total", inv.SubTotalExcludingTaxesAmount, subTotal)
	subsCheck(t, "billed taxes", inv.TaxesAmount, taxes)
	subsCheck(t, "billed credit notes", inv.CreditNotesAmount, creditNotes)
	subsCheck(t, "billed total", inv.TotalAmount, total)
	charges := w.subsFeesOf(inv.ID, core.FeeTypeCharge)
	if len(charges) != 1 {
		t.Fatalf("charge fees: got = %d, want = 1", len(charges))
	}
	subsCheck(t, "charge fee", charges[0].Amount, 5000)
	subsCheck(t, "charge fee taxes", charges[0].TaxesAmount, 1000)
}

func TestScenarioSubscriptionTerminateManually(t *testing.T) {
	termination := subsAt("2025-02-21T00:00:00Z")
	eventsAt := subsAt("2025-02-10T00:00:00Z")

	t.Run("in advance: credits the unused fee and generates the invoice", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_manually_spec.rb:118
		w := newSubsManual(t, true, nil)
		w.checkInAdvanceBilled(t)
		w.events(eventsAt)
		w.must(w.terminate(termination, lifecycle.TerminateInput{}))
		subsCheck(t, "status", w.sub.Status, subscription.StatusTerminated)
		subsCheck(t, "on termination credit note", w.sub.OnTerminationCreditNote, subscription.TerminationCreditNoteCredit)
		subsCheck(t, "on termination invoice", w.sub.OnTerminationInvoice, subscription.TerminationInvoiceGenerate)
		w.checkCreditNote(t, 0)
		w.drain()
		w.checkBilled(t, 2, 5000, 1000, 3000, 3000)
		subsCheck(t, "credit note balance after", w.creditNote(t).BalanceAmount, 0)
	})

	t.Run("in advance: refunds what was paid of the unused fee", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_manually_spec.rb:145
		w := newSubsManual(t, true, nil)
		w.checkInAdvanceBilled(t)
		w.events(eventsAt)
		// create_payment: a manual payment of 75 euros of the starting
		// invoice.
		inv := w.invoices()[0]
		w.StoredPayment(payment.InvoicePayable(inv.ID), w.c.ID, 7500, payment.StateSucceeded, core.IntegrationID{})
		inv.TotalPaidAmount = 7500
		w.SaveInvoice(inv)
		w.must(w.terminate(termination, lifecycle.TerminateInput{OnTerminationCreditNote: subscription.TerminationCreditNoteRefund}))
		subsCheck(t, "status", w.sub.Status, subscription.StatusTerminated)
		subsCheck(t, "on termination credit note", w.sub.OnTerminationCreditNote, subscription.TerminationCreditNoteRefund)
		// 14 used days (60 euros), 7 unused (30 euros): 75 paid refunds 15
		// and credits 15.
		w.checkCreditNote(t, 1500)
		w.drain()
		w.checkBilled(t, 2, 5000, 1000, 1500, 4500)
		subsCheck(t, "credit note balance after", w.creditNote(t).BalanceAmount, 0)
	})

	t.Run("in advance: skips the credit note", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_manually_spec.rb:179
		w := newSubsManual(t, true, nil)
		w.checkInAdvanceBilled(t)
		w.events(eventsAt)
		w.must(w.terminate(termination, lifecycle.TerminateInput{OnTerminationCreditNote: subscription.TerminationCreditNoteSkip}))
		subsCheck(t, "on termination credit note", w.sub.OnTerminationCreditNote, subscription.TerminationCreditNoteSkip)
		subsCheck(t, "credit notes", len(w.CreditNotes(w.invoices()[0].ID)), 0)
		w.drain()
		invs := w.invoices()
		if len(invs) != 2 {
			t.Fatalf("subscription invoices: got = %d, want = 2", len(invs))
		}
		inv := invs[1]
		subsCheck(t, "billed status", inv.Status, invoice.StatusFinalized)
		subsCheck(t, "billed sub total", inv.SubTotalExcludingTaxesAmount, 5000)
		subsCheck(t, "billed taxes", inv.TaxesAmount, 1000)
		subsCheck(t, "billed credit notes", inv.CreditNotesAmount, 0)
		subsCheck(t, "billed total", inv.TotalAmount, 6000)
	})

	t.Run("in advance: skips the termination invoice", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_manually_spec.rb:203
		w := newSubsManual(t, true, nil)
		w.checkInAdvanceBilled(t)
		w.events(eventsAt)
		w.must(w.terminate(termination, lifecycle.TerminateInput{OnTerminationInvoice: subscription.TerminationInvoiceSkip}))
		subsCheck(t, "status", w.sub.Status, subscription.StatusTerminated)
		subsCheck(t, "on termination invoice", w.sub.OnTerminationInvoice, subscription.TerminationInvoiceSkip)
		w.checkCreditNote(t, 0)
		w.drain()
		subsCheck(t, "subscription invoices", len(w.invoices()), 1)
	})

	t.Run("in advance: terminated on the day it was created", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_manually_spec.rb:224
		w := newSubsManual(t, true, nil)
		w.checkInAdvanceBilled(t)
		w.events(subsAt("2025-02-08T05:00:00Z"))
		w.must(w.terminate(subsAt("2025-02-08T12:00:00Z"), lifecycle.TerminateInput{}))
		subsCheck(t, "status", w.sub.Status, subscription.StatusTerminated)
		subsCheck(t, "on termination credit note", w.sub.OnTerminationCreditNote, subscription.TerminationCreditNoteCredit)
		// 20 unused days of 28: 71.43 euros plus 20% tax.
		cn := w.creditNote(t)
		var items int64
		w.tx(func(ctx context.Context, r port.Repos) error {
			list, err := r.CreditNotes.ListItemsByCreditNote(ctx, w.OrgID(), cn.ID)
			for _, it := range list {
				items += it.Amount
			}
			return err
		})
		subsCheck(t, "credit note items", items, 7143)
		subsCheck(t, "credit note sub total", cn.SubTotalExcludingTaxesAmount, 7143)
		subsCheck(t, "credit note taxes", cn.TaxesAmount, 1429)
		subsCheck(t, "credit note credit", cn.CreditAmount, 8572)
		subsCheck(t, "credit note total", cn.TotalAmount, 8572)
		w.drain()
		w.checkBilled(t, 2, 5000, 1000, 6000, 0)
		subsCheck(t, "credit note balance after", w.creditNote(t).BalanceAmount, 2572)
	})

	t.Run("in advance: cancels a pending subscription", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_manually_spec.rb:266
		w := newSubsManual(t, true, subsPtr(subsAt("2025-02-13T00:00:00Z")))
		subsCheck(t, "status", w.sub.Status, subscription.StatusPending)
		subsCheck(t, "subscription invoices", len(w.invoices()), 0)
		w.must(w.terminate(termination, lifecycle.TerminateInput{OnTerminationCreditNote: subscription.TerminationCreditNoteCredit}))
		subsCheck(t, "status", w.sub.Status, subscription.StatusCanceled)
		// The parameter is ignored; the subscription keeps the credit
		// default its in-advance plan gave it at creation, where lago
		// leaves it nil.
		subsCheck(t, "on termination credit note", w.sub.OnTerminationCreditNote, subscription.TerminationCreditNoteCredit)
		if w.sub.CanceledAt == nil {
			t.Error("canceled_at: got = nil, want = set")
		}
	})

	t.Run("in arrears: ignores the credit note parameter", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_manually_spec.rb:287
		w := newSubsManual(t, false, nil)
		subsCheck(t, "status", w.sub.Status, subscription.StatusActive)
		subsCheck(t, "subscription invoices", len(w.invoices()), 0)
		w.events(eventsAt)
		w.must(w.terminate(termination, lifecycle.TerminateInput{OnTerminationCreditNote: subscription.TerminationCreditNoteCredit}))
		subsCheck(t, "status", w.sub.Status, subscription.StatusTerminated)
		subsCheck(t, "on termination credit note", w.sub.OnTerminationCreditNote, "")
		w.drain()
		w.checkBilled(t, 1, 10_000, 2000, 0, 12_000)
		fees := w.subsFeesOf(w.invoices()[0].ID, core.FeeTypeSubscription)
		if len(fees) != 1 {
			t.Fatalf("subscription fees: got = %d, want = 1", len(fees))
		}
		subsCheck(t, "subscription fee", fees[0].Amount, 5000)
		subsCheck(t, "subscription fee taxes", fees[0].TaxesAmount, 1000)
	})

	t.Run("in arrears: skips the termination invoice", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_manually_spec.rb:321
		w := newSubsManual(t, false, nil)
		w.events(eventsAt)
		w.must(w.terminate(termination, lifecycle.TerminateInput{OnTerminationInvoice: subscription.TerminationInvoiceSkip}))
		subsCheck(t, "status", w.sub.Status, subscription.StatusTerminated)
		subsCheck(t, "on termination credit note", w.sub.OnTerminationCreditNote, "")
		subsCheck(t, "on termination invoice", w.sub.OnTerminationInvoice, subscription.TerminationInvoiceSkip)
		w.drain()
		subsCheck(t, "subscription invoices", len(w.invoices()), 0)
	})

	t.Run("rejects invalid parameters", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/terminate_manually_spec.rb:338
		w := newSubsManual(t, true, nil)
		for _, in := range []lifecycle.TerminateInput{
			{OnTerminationCreditNote: "invalid"},
			{OnTerminationInvoice: "invalid"},
		} {
			err := w.terminate(termination, in)
			if !errors.Is(err, core.ErrInvalidValue) {
				t.Errorf("terminate(%+v) error = %v, want %v", in, err, core.ErrInvalidValue)
			}
		}
		_, err := w.E.Lifecycle.Terminate(w.Ctx, lifecycle.TerminateInput{OrganizationID: w.OrgID(), SubscriptionID: core.NewID[core.SubscriptionID](w.IDs)})
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("terminate(unknown) error = %v, want %v", err, core.ErrNotFound)
		}
	})
}

func TestScenarioSubscriptionTerminateEnded(t *testing.T) {
	type billingRun struct {
		at          string
		wantActive  bool
		wantCount   int
		wantTotal   int64
		wantIssuing string
	}
	tests := []struct {
		source      string
		name        string
		timezone    string
		billingTime period.BillingTime
		inAdvance   bool
		created     string
		ending      string // "" is no ending date
		billing     []billingRun
		// at is when the ended subscriptions are terminated, or when the
		// subscription is terminated manually.
		at     string
		manual bool
		// want* check the last invoice of the subscription.
		wantCount   int
		wantTotal   int64
		wantIssuing string
	}{
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:26", name: "Europe/Paris",
			timezone: "Europe/Paris", billingTime: period.Anniversary, created: "2023-09-05T00:00:00Z", ending: "2023-09-06T00:00:00Z",
			at: "2023-09-06T00:15:00Z", wantCount: 1, wantTotal: 67, wantIssuing: "2023-09-06",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:66", name: "Asia/Bangkok",
			timezone: "Asia/Bangkok", billingTime: period.Anniversary, created: "2023-09-05T00:00:00Z", ending: "2023-09-06T00:00:00Z",
			at: "2023-09-06T00:15:00Z", wantCount: 1, wantTotal: 67, wantIssuing: "2023-09-06",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:103", name: "America/Bogota",
			timezone: "America/Bogota", billingTime: period.Anniversary, created: "2023-09-05T00:00:00Z", ending: "2023-09-06T00:00:00Z",
			at: "2023-09-06T00:15:00Z", wantCount: 1, wantTotal: 67, wantIssuing: "2023-09-05",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:140", name: "ending on the billing day bills the previous period",
			timezone: "Europe/Paris", billingTime: period.Anniversary, created: "2023-09-05T00:00:00Z", ending: "2023-10-05T00:00:00Z",
			at: "2023-10-05T00:15:00Z", wantCount: 1, wantTotal: 1000, wantIssuing: "2023-10-05",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:184", name: "ending on the billing day in advance",
			timezone: "Europe/Paris", billingTime: period.Anniversary, inAdvance: true, created: "2023-09-05T00:00:00Z", ending: "2023-10-05T00:00:00Z",
			at: "2023-10-05T00:15:00Z", wantCount: 2, wantTotal: 0, wantIssuing: "2023-10-05",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:220", name: "terminated on the day of creation bills one day",
			timezone: "Europe/Paris", billingTime: period.Anniversary, created: "2023-09-05T00:00:00Z",
			at: "2023-09-05T05:00:00Z", manual: true, wantCount: 1, wantTotal: 33, wantIssuing: "2023-09-05",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:260", name: "ending on the billing day in America/Bogota",
			timezone: "America/Bogota", billingTime: period.Anniversary, created: "2023-09-05T00:00:00Z", ending: "2023-10-05T00:00:00Z",
			at: "2023-10-04T19:00:00Z", wantCount: 1, wantTotal: 1000, wantIssuing: "2023-10-04",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:297", name: "ending on the billing day in Asia/Bangkok",
			timezone: "Asia/Bangkok", billingTime: period.Anniversary, created: "2023-09-05T00:00:00Z", ending: "2023-10-05T00:00:00Z",
			at: "2023-10-04T19:00:00Z", wantCount: 1, wantTotal: 1000, wantIssuing: "2023-10-05",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:336", name: "calendar ending on the billing day",
			timezone: "Europe/Paris", billingTime: period.Calendar, created: "2023-08-01T00:00:00Z", ending: "2023-10-01T00:00:00Z",
			at: "2023-10-01T00:15:00Z", wantCount: 1, wantTotal: 1000, wantIssuing: "2023-10-01",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:380", name: "calendar ending on the billing day in advance",
			timezone: "Europe/Paris", billingTime: period.Calendar, inAdvance: true, created: "2023-08-01T00:00:00Z", ending: "2023-10-01T00:00:00Z",
			billing: []billingRun{{at: "2023-09-01T00:00:00Z"}},
			at:      "2023-10-01T00:15:00Z", wantCount: 3, wantTotal: 0, wantIssuing: "2023-10-01",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:420", name: "calendar billing run before the termination",
			timezone: "Europe/Paris", billingTime: period.Calendar, created: "2023-08-01T00:00:00Z", ending: "2023-10-01T00:00:00Z",
			billing: []billingRun{{at: "2023-10-01T00:05:00Z", wantActive: true, wantCount: 0}},
			at:      "2023-10-01T00:15:00Z", wantCount: 1, wantTotal: 1000, wantIssuing: "2023-10-01",
		},
		{
			source: "spec/scenarios/subscriptions/terminate_ended_spec.rb:465", name: "calendar billing run before a manual termination",
			timezone: "Europe/Paris", billingTime: period.Calendar, created: "2023-08-01T00:00:00Z",
			billing: []billingRun{{at: "2023-10-01T00:05:00Z", wantActive: true, wantCount: 1, wantTotal: 1000, wantIssuing: "2023-10-01"}},
			at:      "2023-10-01T00:15:00Z", manual: true, wantCount: 2, wantTotal: 32, wantIssuing: "2023-10-01",
		},
	}
	for _, tc := range tests {
		t.Run(tc.source+" "+tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.timezone)
			if err != nil {
				t.Fatal(err)
			}
			s := newScenario(t, subsAt(tc.created))
			c := s.Customer(func(c *customer.Customer) { c.Timezone = loc })
			p := s.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.PayInAdvance = tc.inAdvance })
			params := subsParams{billingTime: tc.billingTime, subscriptionAt: subsPtr(subsAt(tc.created))}
			if tc.ending != "" {
				params.endingAt = subsPtr(subsAt(tc.ending))
			}
			sub := s.subsCreate(c, p, params)
			subsCheck(t, "status", sub.Status, subscription.StatusActive)
			last := func(what string, count int, total int64, issuing string) {
				t.Helper()
				invs := s.subsInvoices(sub)
				if len(invs) != count {
					t.Fatalf("%s invoices: got = %d, want = %d", what, len(invs), count)
				}
				if count == 0 {
					return
				}
				inv := invs[len(invs)-1]
				subsCheck(t, what+" total", inv.TotalAmount, total)
				subsCheck(t, what+" issuing date", inv.IssuingDate.String(), issuing)
			}
			for _, b := range tc.billing {
				s.travelTo(subsAt(b.at))
				s.subsBilling()
				if b.wantActive {
					subsCheck(t, b.at+" status", s.subsReload(sub.ID).Status, subscription.StatusActive)
					last(b.at, b.wantCount, b.wantTotal, b.wantIssuing)
				}
			}
			s.travelTo(subsAt(tc.at))
			if tc.manual {
				s.subsTerminate(sub)
			} else {
				s.subsTerminateEnded()
			}
			sub = s.subsReload(sub.ID)
			subsCheck(t, "terminated status", sub.Status, subscription.StatusTerminated)
			last("termination", tc.wantCount, tc.wantTotal, tc.wantIssuing)
			if tc.inAdvance {
				subsCheck(t, "credit notes", len(s.subsCustomerCreditNotes(c)), 0)
			}
		})
	}
}

func TestScenarioSubscriptionProgressiveBillingEnablement(t *testing.T) {
	setup := func(t *testing.T) (*scenario, *catalog.Plan, *subscription.Subscription) {
		s := newScenario(t, subsAt("2022-12-01T00:00:00Z"))
		c := s.Customer(func(c *customer.Customer) { c.Timezone = time.UTC })
		m := s.Metric("bm", catalog.AggregationTypeSum, "value")
		p := s.Plan(period.Monthly, 5_000_000)
		s.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`)
		sub := s.subsCreate(c, p, subsParams{})
		s.travelTo(subsAt("2022-12-06T00:00:00Z"))
		s.ingest(sub, "bm", "value", "1000000")
		subsCheck(t, "invoices after event", len(s.Invoices(c.ID)), 0)
		return s, p, sub
	}
	// enable adds the usage threshold to the plan (lago update_plan) and
	// refreshes the lifetime usage.
	enable := func(s *scenario, p *catalog.Plan, sub *subscription.Subscription) {
		s.UsageThreshold(p, 20_000, false)
		s.drain()
		s.subsUsageUpdate(s.ReloadCustomer(sub.CustomerID))
	}
	const usage = 1_000_000 * 10 * 100

	t.Run("enabled while usage accumulates", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/progressive_billing_enablement_spec.rb:27
		s, p, sub := setup(t)
		s.travelTo(subsAt("2022-12-07T00:00:00Z"))
		enable(s, p, sub)
		invs := s.Invoices(sub.CustomerID)
		if len(invs) != 1 {
			t.Fatalf("invoices: got = %d, want = 1", len(invs))
		}
		subsCheck(t, "invoice type", invs[0].Type, invoice.TypeProgressiveBilling)
		subsCheck(t, "progressive total", invs[0].TotalAmount, usage)

		s.travelTo(subsAt("2023-01-01T00:00:00Z"))
		s.subsBilling()
		subsCheck(t, "invoices after billing", len(s.Invoices(sub.CustomerID)), 2)
		all := s.subsInvoices(sub)
		inv := all[len(all)-1]
		subsCheck(t, "recurring total", inv.TotalAmount, 5_000_000)
		subsCheck(t, "recurring fees", inv.FeesAmount, 5_000_000+usage)
		subsCheck(t, "recurring progressive credit", inv.ProgressiveBillingCreditAmount, usage)
	})

	t.Run("enabled after the usage was invoiced", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/progressive_billing_enablement_spec.rb:78
		s, p, sub := setup(t)
		s.travelTo(subsAt("2023-01-01T00:00:00Z"))
		s.subsBilling()
		invs := s.Invoices(sub.CustomerID)
		if len(invs) != 1 {
			t.Fatalf("invoices: got = %d, want = 1", len(invs))
		}
		subsCheck(t, "invoice type", invs[0].Type, invoice.TypeSubscription)
		subsCheck(t, "invoice total", invs[0].TotalAmount, 5_000_000+usage)
		s.travelTo(subsAt("2023-01-06T00:00:00Z"))
		enable(s, p, sub)
		subsCheck(t, "invoices after enablement", len(s.Invoices(sub.CustomerID)), 1)
	})
}

func TestScenarioSubscriptionPaymentGatedActivation(t *testing.T) {
	// setup is the world of payment_gated_activation_spec.rb: a monthly
	// plan and a calendar subscription with a payment rule of 48 hours.
	setup := func(t *testing.T, opts ...func(*catalog.Plan)) (*scenario, *customer.Customer, *catalog.Plan) {
		s := newScenario(t, subsAt("2026-03-01T10:00:00Z"))
		c := s.Customer()
		p := s.Plan(period.Monthly, 1000, append([]func(*catalog.Plan){func(p *catalog.Plan) { p.PayInAdvance = true }}, opts...)...)
		return s, c, p
	}
	params := subsParams{externalID: "gated-sub", billingTime: period.Calendar, paymentRule: subsPtr(48)}
	rules := func(t *testing.T, s *scenario, sub *subscription.Subscription, want subscription.ActivationRuleStatus) {
		t.Helper()
		subsCheck(t, "rules", fmt.Sprint(s.subsRules(sub)), fmt.Sprint([]subscription.ActivationRuleStatus{want}))
	}
	soleInvoice := func(t *testing.T, s *scenario, sub *subscription.Subscription) *invoice.Invoice {
		t.Helper()
		invs := s.subsInvoices(sub)
		if len(invs) != 1 {
			t.Fatalf("invoices: got = %d, want = 1", len(invs))
		}
		return invs[0]
	}

	t.Run("gates the subscription, then activates it when the payment succeeds", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:123
		s, c, p := setup(t)
		sub := s.subsCreate(c, p, params)
		subsCheck(t, "status", sub.Status, subscription.StatusIncomplete)
		if sub.StartedAt == nil {
			t.Error("started_at: got = nil, want = set")
		}
		rules(t, s, sub, subscription.ActivationRuleStatusPending)
		inv := soleInvoice(t, s, sub)
		subsCheck(t, "invoice status", inv.Status, invoice.StatusOpen)
		subsCheck(t, "subscription fees", len(s.subsFeesOf(inv.ID, core.FeeTypeSubscription)), 1)

		// The payment success (libs/payments) satisfies the rule and
		// activates the subscription.
		s.tx(func(ctx context.Context, r port.Repos) error {
			list, err := r.ActivationRules.ListBySubscription(ctx, s.OrgID(), sub.ID)
			if err != nil {
				return err
			}
			if err := list[0].Satisfy(s.Now()); err != nil {
				return err
			}
			return r.ActivationRules.Update(ctx, list[0])
		})
		_, err := s.E.Lifecycle.Activate(s.Ctx, s.OrgID(), sub.ID, s.Now())
		s.must(err)
		s.drain()
		subsCheck(t, "status after payment", s.subsReload(sub.ID).Status, subscription.StatusActive)
		rules(t, s, sub, subscription.ActivationRuleStatusSatisfied)
		subsCheck(t, "invoices after payment", len(s.subsInvoices(sub)), 1)
	})

	t.Run("a backdated subscription activates at once", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:411
		s, c, p := setup(t)
		in := params
		in.subscriptionAt = subsPtr(s.Now().AddDate(0, 0, -5))
		sub := s.subsCreate(c, p, in)
		subsCheck(t, "status", sub.Status, subscription.StatusActive)
		subsCheck(t, "invoices", len(s.subsInvoices(sub)), 0)
	})

	t.Run("in trial without in-advance fixed charges, activates at once", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:429
		s, c, p := setup(t, func(p *catalog.Plan) { p.TrialPeriodDays = 30 })
		sub := s.subsCreate(c, p, params)
		subsCheck(t, "status", sub.Status, subscription.StatusActive)
		rules(t, s, sub, subscription.ActivationRuleStatusNotApplicable)
		subsCheck(t, "invoices", len(s.subsInvoices(sub)), 0)
	})

	t.Run("in trial with in-advance fixed charges, gates on the fixed charges", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:445
		s, c, p := setup(t, func(p *catalog.Plan) { p.TrialPeriodDays = 30 })
		s.FixedCharge(p, s.AddOn(200), catalog.FixedChargeModelStandard, `{"amount":"10"}`, "1",
			func(f *catalog.FixedCharge) { f.PayInAdvance = true })
		sub := s.subsCreate(c, p, params)
		subsCheck(t, "status", sub.Status, subscription.StatusIncomplete)
		rules(t, s, sub, subscription.ActivationRuleStatusPending)
	})

	t.Run("an in-arrears plan gates on its in-advance fixed charges", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:484
		s, c, p := setup(t, func(p *catalog.Plan) { p.PayInAdvance = false })
		s.FixedCharge(p, s.AddOn(200), catalog.FixedChargeModelStandard, `{"amount":"10"}`, "1",
			func(f *catalog.FixedCharge) { f.PayInAdvance = true })
		sub := s.subsCreate(c, p, params)
		subsCheck(t, "status", sub.Status, subscription.StatusIncomplete)
		rules(t, s, sub, subscription.ActivationRuleStatusPending)
		inv := soleInvoice(t, s, sub)
		subsCheck(t, "invoice status", inv.Status, invoice.StatusOpen)
		if n := len(s.subsFeesOf(inv.ID, core.FeeTypeFixedCharge)); n == 0 {
			t.Error("fixed charge fees: got = 0, want > 0")
		}
		subsCheck(t, "subscription fees", len(s.subsFeesOf(inv.ID, core.FeeTypeSubscription)), 0)
	})

	t.Run("the merchant cancels the gated subscription", func(t *testing.T) {
		// source: spec/scenarios/subscriptions/payment_gated_activation_spec.rb:1622
		s, c, p := setup(t)
		sub := s.subsCreate(c, p, params)
		subsCheck(t, "status", sub.Status, subscription.StatusIncomplete)
		rules(t, s, sub, subscription.ActivationRuleStatusPending)
		subsCheck(t, "invoice status", soleInvoice(t, s, sub).Status, invoice.StatusOpen)
		sub = s.subsTerminate(sub)
		subsCheck(t, "status", sub.Status, subscription.StatusCanceled)
		subsCheck(t, "cancellation reason", sub.CancellationReason, subscription.CancellationReasonManual)
		rules(t, s, sub, subscription.ActivationRuleStatusDeclined)
	})
}
