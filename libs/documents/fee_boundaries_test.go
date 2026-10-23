package documents

import (
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Ports of spec/views/helpers/fee_boundaries_helper_spec.rb.

const boundariesSpec = "spec/views/helpers/fee_boundaries_helper_spec.rb:"

// boundaries is the spec's December invoice subscription and fee builders.
type boundaries struct {
	g    *ids
	plan catalog.Plan
	sub  Subscription
	v    *invoiceView
}

func newBoundaries(t *testing.T) *boundaries {
	t.Helper()
	g := &ids{}
	plan := newPlan(g, "plan", nil)
	dec := span("2025-12-01 00:00:00", "2025-12-31 23:59:59")
	sub := newSubscription(g, plan, periods(dec), func(is *invoice.Subscription) { is.Timestamp = at("2026-01-01 00:00:00") })
	party := staticParty()
	w := newWriter(testCatalog(t), &party, "", usd())
	inv := newInvoice(g, invoice.TypeSubscription, "INV", nil)
	return &boundaries{g: g, plan: plan, sub: sub, v: &invoiceView{writer: w, d: &Invoice{Party: party, Invoice: inv}, inv: &inv}}
}

func (b *boundaries) fee(typ core.FeeType, p period.Period, mod func(f *Fee)) *Fee {
	f := &Fee{Fee: newFee(b.g, typ, func(f *invoice.Fee) { f.SubscriptionID = b.sub.Subscription.ID })}
	switch typ {
	case core.FeeTypeSubscription, core.FeeTypeCommitment:
		f.Fee.Properties.From, f.Fee.Properties.To = p.From, p.To
	case core.FeeTypeCharge:
		c, m := metricCharge(b.g, b.plan, "metric", nil)
		f.Charge, f.Metric, f.Fee.ChargeID = &c, &m, c.ID
		f.Fee.Properties.ChargesFrom, f.Fee.Properties.ChargesTo = p.From, p.To
	case core.FeeTypeFixedCharge:
		f.Period = p
	}
	if mod != nil {
		mod(f)
	}
	return f
}

func (b *boundaries) groups(fees ...*Fee) []*feeGroup { return b.v.groupByPeriod(fees, &b.sub) }

func dates(p billingPeriod) [2]string {
	return [2]string{p.from.UTC().Format(time.DateOnly), p.to.UTC().Add(-time.Nanosecond).Format(time.DateOnly)}
}

func TestGroupFeesByBillingPeriod(t *testing.T) {
	b := newBoundaries(t)
	dec := span("2025-12-01 00:00:00", "2025-12-31 23:59:59")
	jan := span("2026-01-01 00:00:00", "2026-01-31 23:59:59")
	subFee := b.fee(core.FeeTypeSubscription, dec, nil)
	arrears := b.fee(core.FeeTypeCharge, dec, nil)
	advance := b.fee(core.FeeTypeCharge, jan, nil)

	t.Run("groups by billing period, chronologically", func(t *testing.T) {
		t.Logf("source: %s72, %s78, %s85, %s92", boundariesSpec, boundariesSpec, boundariesSpec, boundariesSpec)
		gs := b.groups(advance, subFee, arrears)
		if len(gs) != 2 {
			t.Fatalf("groups: got = %d, want = 2", len(gs))
		}
		if got := [2][2]string{dates(gs[0].period), dates(gs[1].period)}; got != [2][2]string{{"2025-12-01", "2025-12-31"}, {"2026-01-01", "2026-01-31"}} {
			t.Errorf("periods: got = %v, want = December then January", got)
		}
		if gs[0].subscription != subFee || !slices.Equal(gs[0].charges, []*Fee{arrears}) || !slices.Equal(gs[1].charges, []*Fee{advance}) {
			t.Errorf("fees: got = misplaced, want = subscription and arrears charge in December, advance charge in January")
		}
	})

	t.Run("fixed charge fees", func(t *testing.T) {
		t.Logf("source: %s131, %s143", boundariesSpec, boundariesSpec)
		f1 := b.fee(core.FeeTypeFixedCharge, dec, nil)
		f2 := b.fee(core.FeeTypeFixedCharge, dec, nil)
		f3 := b.fee(core.FeeTypeFixedCharge, jan, nil)
		gs := b.groups(f1, f2, f3)
		if len(gs) != 2 || len(gs[0].fixedCharges) != 2 || !slices.Equal(gs[1].fixedCharges, []*Fee{f3}) {
			t.Errorf("groups: got = %d, want = December (2 fixed charges) and January (1)", len(gs))
		}
	})

	commitment := func(p period.Period, mod func(f *Fee)) *Fee { return b.fee(core.FeeTypeCommitment, p, mod) }
	for _, c := range []struct {
		name, line string
		fee        *Fee
		want       [2]string
	}{
		{"commitment with properties", "181", commitment(dec, nil), [2]string{"2025-12-01", "2025-12-31"}},
		{"commitment without properties, plan paid in arrears", "213", commitment(period.Period{}, nil), [2]string{"2025-12-01", "2025-12-31"}},
		{
			"commitment without properties, plan paid in advance", "262",
			commitment(period.Period{}, func(f *Fee) { f.Period = span("2025-11-01 00:00:00", "2025-11-30 23:59:59") }),
			[2]string{"2025-11-01", "2025-11-30"},
		},
		{"subscription fee without properties", "346", b.fee(core.FeeTypeSubscription, period.Period{}, nil), [2]string{"2025-12-01", "2025-12-31"}},
		{"charge fee without properties", "367", b.fee(core.FeeTypeCharge, period.Period{}, nil), [2]string{"2025-12-01", "2025-12-31"}},
		{"fixed charge fee without properties", "390", b.fee(core.FeeTypeFixedCharge, period.Period{}, nil), [2]string{"2025-12-01", "2025-12-31"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s%s", boundariesSpec, c.line)
			gs := b.groups(c.fee)
			if len(gs) != 1 {
				t.Fatalf("groups: got = %d, want = 1", len(gs))
			}
			if got := dates(gs[0].period); got != c.want {
				t.Errorf("period: got = %v, want = %v", got, c.want)
			}
			if !gs[0].any() {
				t.Errorf("group fees: got = none, want = the fee")
			}
		})
	}

	t.Run("charges sorted alphabetically", func(t *testing.T) {
		t.Logf("source: %s311, %s320", boundariesSpec, boundariesSpec)
		bFee := b.fee(core.FeeTypeCharge, dec, func(f *Fee) { f.Fee.InvoiceDisplayName = ptr("charge b") })
		aFee := b.fee(core.FeeTypeCharge, dec, func(f *Fee) { f.Fee.InvoiceDisplayName = ptr("charge a") })
		gs := b.groups(bFee, aFee)
		if len(gs) != 1 || !slices.Equal(gs[0].charges, []*Fee{aFee, bFee}) {
			t.Errorf("charges: got = unsorted, want = charge a, charge b")
		}
	})

	t.Run("add-on and credit fees are ignored", func(t *testing.T) {
		t.Logf("source: %s418, %s426", boundariesSpec, boundariesSpec)
		if gs := b.groups(b.fee(core.FeeTypeAddOn, period.Period{}, nil), b.fee(core.FeeTypeCredit, period.Period{}, nil)); len(gs) != 0 {
			t.Errorf("groups: got = %d, want = 0", len(gs))
		}
	})

	t.Run("same dates, different times", func(t *testing.T) {
		t.Logf("source: %s435", boundariesSpec)
		other := b.fee(core.FeeTypeCharge, period.Period{From: at("2025-12-01 12:00:00"), To: at("2025-12-31 12:00:00")}, nil)
		if gs := b.groups(arrears, other); len(gs) != 1 || len(gs[0].charges) != 2 {
			t.Errorf("groups: got = %d, want = 1 with both charges", len(gs))
		}
	})

	t.Run("mixed fee types in one period", func(t *testing.T) {
		t.Logf("source: %s483", boundariesSpec)
		fixed := b.fee(core.FeeTypeFixedCharge, dec, nil)
		com := commitment(dec, nil)
		gs := b.groups(subFee, arrears, fixed, com)
		if len(gs) != 1 || gs[0].subscription != subFee || gs[0].commitment != com || len(gs[0].charges) != 1 || len(gs[0].fixedCharges) != 1 {
			t.Errorf("groups: got = %d, want = 1 with every fee", len(gs))
		}
	})
}

func TestBillingPeriodFor(t *testing.T) {
	b := newBoundaries(t)
	jan := span("2026-01-01 00:00:00", "2026-01-31 23:59:59")
	dec := span("2025-12-01 00:00:00", "2025-12-31 23:59:59")
	for _, c := range []struct {
		name, line string
		fee        *Fee
		want       [2]string
	}{
		{"subscription fee", "557", b.fee(core.FeeTypeSubscription, jan, nil), [2]string{"2026-01-01", "2026-01-31"}},
		{"charge fee", "581", b.fee(core.FeeTypeCharge, jan, nil), [2]string{"2026-01-01", "2026-01-31"}},
		{"fixed charge fee", "605", b.fee(core.FeeTypeFixedCharge, jan, nil), [2]string{"2026-01-01", "2026-01-31"}},
		{"commitment fee", "627", b.fee(core.FeeTypeCommitment, jan, nil), [2]string{"2026-01-01", "2026-01-31"}},
		{"missing properties", "646", b.fee(core.FeeTypeSubscription, period.Period{}, nil), [2]string{"2025-12-01", "2025-12-31"}},
		{"other fee type", "664", b.fee(core.FeeTypeAddOn, period.Period{}, nil), [2]string{"2025-12-01", "2025-12-31"}},
		{
			// The reconciliation fee of a recurring pay-in-advance charge
			// shows the period paid in advance, not its properties.
			"reconciliation fee", "722",
			b.fee(core.FeeTypeCharge, dec, func(f *Fee) {
				f.Charge.PayInAdvance, f.Metric.Recurring, f.Metric.AggregationType = true, true, catalog.AggregationTypeSum
				f.AdvanceInterval = jan
			}),
			[2]string{"2026-01-01", "2026-01-31"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s%s", boundariesSpec, c.line)
			if got := dates(billingPeriodFor(c.fee, &b.sub)); got != c.want {
				t.Errorf("billing period: got = %v, want = %v", got, c.want)
			}
		})
	}
}

// source: spec/views/helpers/fee_boundaries_helper_spec.rb:742
func TestFormatBillingPeriod(t *testing.T) {
	b := newBoundaries(t)
	r := &feeRows{w: b.v.writer}
	p := span("2025-12-01 00:00:00", "2025-12-31 23:59:59")
	b.v.periodHeader(r, billingPeriod{p.From, p.To})
	if got, want := r.rows[0].Cells[0].Text[0], "Fees from Dec 01, 2025 to Dec 31, 2025"; got != want {
		t.Errorf("billing period: got = %q, want = %q", got, want)
	}
}

func TestFeeGroupAny(t *testing.T) {
	// source: spec/views/helpers/fee_boundaries_helper_spec.rb:810
	f := &Fee{}
	for _, c := range []struct {
		name string
		g    feeGroup
		want bool
	}{
		{"subscription fee", feeGroup{subscription: f}, true},
		{"charge fees", feeGroup{charges: []*Fee{f}}, true},
		{"fixed charge fees", feeGroup{fixedCharges: []*Fee{f}}, true},
		{"commitment fee", feeGroup{commitment: f}, true},
		{"no fees", feeGroup{}, false},
	} {
		if got := c.g.any(); got != c.want {
			t.Errorf("%s: got = %t, want = %t", c.name, got, c.want)
		}
	}
}

// Ports of spec/views/helpers/invoice_subscriptions_helper_spec.rb
// (filtered_fees; load_subscriptions_with_fees is the caller's FromCore).
func TestFilteredFees(t *testing.T) {
	const src = "spec/views/helpers/invoice_subscriptions_helper_spec.rb:"
	for _, c := range []struct {
		name, line string
		fees       func(b *boundaries) (all, want []*Fee)
	}{
		{"subscription fee", "49", func(b *boundaries) ([]*Fee, []*Fee) {
			f := b.fee(core.FeeTypeSubscription, period.Period{}, nil)
			return []*Fee{f}, []*Fee{f}
		}},
		{"commitment fee", "55", func(b *boundaries) ([]*Fee, []*Fee) {
			f := b.fee(core.FeeTypeCommitment, period.Period{}, nil)
			return []*Fee{f}, []*Fee{f}
		}},
		{"fixed charge fee with units", "62", func(b *boundaries) ([]*Fee, []*Fee) {
			f := b.fee(core.FeeTypeFixedCharge, period.Period{}, func(f *Fee) { f.Fee.Units = d("1") })
			return []*Fee{f}, []*Fee{f}
		}},
		{"fixed charge fee with an amount only", "68", func(b *boundaries) ([]*Fee, []*Fee) {
			f := b.fee(core.FeeTypeFixedCharge, period.Period{}, func(f *Fee) { f.Fee.Amount = 200 })
			return []*Fee{f}, []*Fee{f}
		}},
		{"zero fixed charge fee", "76", func(b *boundaries) ([]*Fee, []*Fee) {
			return []*Fee{b.fee(core.FeeTypeFixedCharge, period.Period{}, nil)}, nil
		}},
		{"charge fee with units", "84", func(b *boundaries) ([]*Fee, []*Fee) {
			f := b.fee(core.FeeTypeCharge, period.Period{}, func(f *Fee) { f.Fee.Units, f.Fee.TotalAggregatedUnits = d("5"), d("5") })
			return []*Fee{f}, []*Fee{f}
		}},
		{"charge fee with an amount only", "90", func(b *boundaries) ([]*Fee, []*Fee) {
			f := b.fee(core.FeeTypeCharge, period.Period{}, func(f *Fee) { f.Fee.Amount = 200 })
			return []*Fee{f}, []*Fee{f}
		}},
		{"zero charge fee", "106", func(b *boundaries) ([]*Fee, []*Fee) {
			return []*Fee{b.fee(core.FeeTypeCharge, period.Period{}, nil)}, nil
		}},
		{"zero parent of a true-up fee", "126", func(b *boundaries) ([]*Fee, []*Fee) {
			parent := b.fee(core.FeeTypeCharge, period.Period{}, nil)
			trueUp := b.fee(core.FeeTypeCharge, period.Period{}, func(f *Fee) {
				f.Fee.Units, f.Fee.TotalAggregatedUnits, f.Fee.TrueUpParentFeeID = d("5"), d("5"), parent.Fee.ID
			})
			return []*Fee{parent, trueUp}, []*Fee{parent}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s%s", src, c.line)
			b := newBoundaries(t)
			all, want := c.fees(b)
			b.v.fees = all
			if got := b.v.filteredFees(&b.sub); !slices.Equal(got, want) {
				t.Errorf("filtered fees: got = %d fees, want = %d", len(got), len(want))
			}
		})
	}
}
