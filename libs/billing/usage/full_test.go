package usage_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestFullUsage(t *testing.T) {
	t.Parallel()
	june := billingtest.Date(2025, time.June, 15)
	prorated := func(c *catalog.Charge) { c.Prorated = true }
	cases := []struct {
		name string
		// filter returns the charge filter of the request.
		filter func(f *fixture, other *catalog.Charge) []core.ChargeID
		// otherProrated makes the second charge of the plan prorated.
		otherProrated bool
		wantErr       bool
		wantUnits     int64
	}{
		// source: spec/services/invoices/customer_usage_service_spec.rb:650
		{
			name:      "filter_by_charge_id and no prorated charges",
			filter:    func(f *fixture, _ *catalog.Charge) []core.ChargeID { return []core.ChargeID{f.charge.ID} },
			wantUnits: 5,
		},
		// source: spec/services/invoices/customer_usage_service_spec.rb:696
		{
			name:    "no filter",
			filter:  func(*fixture, *catalog.Charge) []core.ChargeID { return nil },
			wantErr: true,
		},
		// source: spec/services/invoices/customer_usage_service_spec.rb:720
		{
			name:          "a different charge is prorated but the filtered one is not",
			filter:        func(f *fixture, _ *catalog.Charge) []core.ChargeID { return []core.ChargeID{f.charge.ID} },
			otherProrated: true,
			wantUnits:     5,
		},
		// source: spec/services/invoices/customer_usage_service_spec.rb:749
		{
			name:          "the filtered charge itself is prorated",
			filter:        func(_ *fixture, other *catalog.Charge) []core.ChargeID { return []core.ChargeID{other.ID} },
			otherProrated: true,
			wantErr:       true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, june, "10")
			m := f.Metric("seats", catalog.AggregationTypeSum, "value", func(m *catalog.BillableMetric) { m.Recurring = true })
			var opts []func(*catalog.Charge)
			if tc.otherProrated {
				opts = append(opts, prorated)
			}
			other := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"5"}`, opts...)
			f.events(f.sub, 2, june.Add(-time.Hour))
			f.events(f.sub, 3, june.AddDate(0, -3, 0))

			u, err := f.usageOf(f.sub, usage.Options{SkipTaxes: true, FullUsage: true, ChargeIDs: tc.filter(f, other)})
			if tc.wantErr {
				if !errors.Is(err, usage.ErrFullUsageNotAllowed) || !errors.Is(err, core.ErrNotAllowed) {
					t.Fatalf("error: got = %v, want = %v", err, usage.ErrFullUsageNotAllowed)
				}
				return
			}
			if err != nil {
				t.Fatalf("error: got = %v, want = nil", err)
			}
			if len(u.Fees) != 1 {
				t.Fatalf("fees: got = %d, want = 1", len(u.Fees))
			}
			if got, want := u.Fees[0].Units, money.NewFromInt(tc.wantUnits); !got.Equal(want) {
				t.Errorf("units: got = %s, want = %s", got, want)
			}
			if got, want := u.From, *f.sub.StartedAt; !got.Equal(want) {
				t.Errorf("from: got = %s, want = %s", got, want)
			}
			if got, want := u.To, billingtest.Date(2025, time.July, 1); !got.Equal(want) {
				t.Errorf("to: got = %s, want = %s", got, want)
			}
		})
	}

	// The current period alone counts the events of June only.
	t.Run("without full usage", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t, june, "10")
		f.events(f.sub, 2, june.Add(-time.Hour))
		f.events(f.sub, 3, june.AddDate(0, -3, 0))
		u, err := f.usageOf(f.sub, usage.Options{SkipTaxes: true, ChargeIDs: []core.ChargeID{f.charge.ID}})
		if err != nil {
			t.Fatalf("error: got = %v, want = nil", err)
		}
		if got, want := u.Fees[0].Units, money.NewFromInt(2); !got.Equal(want) {
			t.Errorf("units: got = %s, want = %s", got, want)
		}
	})
}
