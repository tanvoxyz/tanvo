package documents

import (
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Ports of spec/views/helpers: the view helpers' Go counterparts.

func testWriter(t *testing.T) *writer {
	t.Helper()
	p := staticParty()
	return newWriter(testCatalog(t), &p, "", usd())
}

func computeFee(groupedBy map[string]string, filterName *string) *Fee {
	g := &ids{}
	plan := newPlan(g, "plan", nil)
	sub := newSubscription(g, plan)
	c, m := metricCharge(g, plan, "metric", func(c *catalog.Charge) { c.InvoiceDisplayName = ptr("Compute") })
	f := chargeFee(g, &sub, &c, &m, func(f *invoice.Fee) { f.GroupedBy = groupedBy })
	if filterName != nil {
		cf := catalog.ChargeFilter{ID: g.filter(), ChargeID: c.ID, InvoiceDisplayName: filterName}
		f.ChargeFilter, f.Fee.ChargeFilterID = &cf, cf.ID
	}
	return &f
}

func TestFeeTitle(t *testing.T) {
	const src = "spec/views/helpers/fee_display_helper_spec.rb:"
	premium := ptr("EU Premium")
	eu := map[string]string{"region": "eu"}
	w := testWriter(t)
	for _, c := range []struct {
		line      string
		groupedBy map[string]string
		filter    *string
		want      string
	}{
		{"16", nil, nil, "Compute"},
		{"24", eu, nil, "Compute • eu"},
		{"33", nil, premium, "Compute • EU Premium"},
		{"42", eu, premium, "Compute • eu • EU Premium"},
	} {
		t.Run(c.want, func(t *testing.T) {
			t.Logf("source: %s%s", src, c.line)
			if got := w.feeTitle(computeFee(c.groupedBy, c.filter)); got != c.want {
				t.Errorf("fee title: got = %q, want = %q", got, c.want)
			}
		})
	}
}

func TestGroupedByDisplay(t *testing.T) {
	const src = "spec/views/helpers/fee_display_helper_spec.rb:"
	for _, c := range []struct {
		name, line string
		groupedBy  map[string]string
		want       string
	}{
		{"grouped by property", "153", map[string]string{"key_1": "mercredi", "key_2": "week_01", "key_3": "2024"}, " • mercredi • week_01 • 2024"},
		// The charge's grouped_by property does not matter: the fee's
		// values are printed (fee_display_helper_spec.rb:165).
		{"nil values skipped", "179", map[string]string{"key_1": "", "key_2": "week_01", "key_3": "2024"}, " • week_01 • 2024"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s%s", src, c.line)
			if got := groupedByDisplay(computeFee(c.groupedBy, nil)); got != c.want {
				t.Errorf("grouped by display: got = %q, want = %q", got, c.want)
			}
		})
	}
}

func TestShouldDisplaySubscriptionFee(t *testing.T) {
	const src = "spec/views/helpers/fee_display_helper_spec.rb:"
	for _, c := range []struct {
		name, line  string
		progressive bool
		fees        map[core.FeeType]int64
		want        bool
	}{
		{"progressive billing", "88", true, map[core.FeeType]int64{core.FeeTypeSubscription: 0}, false},
		{"no charge fees", "96", false, map[core.FeeType]int64{core.FeeTypeSubscription: 0}, true},
		{"positive charge fee, zero subscription fee", "107", false, map[core.FeeType]int64{core.FeeTypeCharge: 500, core.FeeTypeSubscription: 0}, false},
		{"positive fixed charge fee, zero subscription fee", "118", false, map[core.FeeType]int64{core.FeeTypeFixedCharge: 500, core.FeeTypeSubscription: 0}, false},
		{"positive fixed charge and subscription fees", "129", false, map[core.FeeType]int64{core.FeeTypeFixedCharge: 500, core.FeeTypeSubscription: 300}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s%s", src, c.line)
			g := &ids{}
			sub := newSubscription(g, newPlan(g, "plan", nil))
			typ := invoice.TypeSubscription
			if c.progressive {
				typ = invoice.TypeProgressiveBilling
			}
			d := &Invoice{Party: staticParty(), Invoice: newInvoice(g, typ, "INV", nil), Subscriptions: []Subscription{sub}}
			for _, ft := range slices.Sorted(func(yield func(core.FeeType) bool) {
				for k := range c.fees {
					if !yield(k) {
						return
					}
				}
			}) {
				d.Fees = append(d.Fees, Fee{Fee: newFee(g, ft, func(f *invoice.Fee) { f.SubscriptionID, f.Amount = sub.Subscription.ID, c.fees[ft] })})
			}
			w := testWriter(t)
			v := &invoiceView{writer: w, d: d, inv: &d.Invoice}
			for i := range d.Fees {
				v.fees = append(v.fees, &d.Fees[i])
			}
			if got := v.shouldDisplaySubscriptionFee(&d.Subscriptions[0]); got != c.want {
				t.Errorf("should display subscription fee: got = %t, want = %t", got, c.want)
			}
		})
	}
}

func TestFeeFormats(t *testing.T) {
	const src = "spec/views/helpers/fee_display_helper_spec.rb:"
	w := testWriter(t)
	plain := newFee(&ids{}, core.FeeTypeCharge, func(f *invoice.Fee) { f.Amount = 1000 })
	unit := plain
	unit.PricingUnitUsage = &invoice.PricingUnitUsage{ShortName: "CR", Amount: d("5.05")}
	for _, c := range []struct {
		name, line, got, want string
	}{
		{"with precision", "192", w.withPrecision(&plain, d("0.12345678")), "$0.123457"},
		{"with precision, pricing unit", "200", w.withPrecision(&unit, d("0.12345678")), "0.123457 CR"},
		{"as currency", "213", w.asCurrency(&plain, d("10.53")), "$10.53"},
		{"as currency, pricing unit", "221", w.asCurrency(&unit, d("10.53")), "10.53 CR"},
		{"amount", "251", w.feeAmount(&plain), "$10.00"},
		{"amount, pricing unit", "259", w.feeAmount(&unit), "5.05 CR"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s%s", src, c.line)
			if c.got != c.want {
				t.Errorf("got = %q, want = %q", c.got, c.want)
			}
		})
	}
}

// source: spec/views/helpers/fee_display_helper_spec.rb:228
func TestPercentRate(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"0.8999999999999999", "0.9%"},
		{"0.7000000000000001", "0.7%"},
		{"5.55", "5.55%"},
		{"5", "5%"},
		{"100", "100%"},
		{"0.001", "0.001%"},
		{"2.125", "2.125%"},
		{"1.0", "1%"},
		{"0.0000004", "0.0000004%"},
	} {
		if got := percentRate(d(c.in)); got != c.want {
			t.Errorf("percentRate(%s): got = %q, want = %q", c.in, got, c.want)
		}
	}
}

func TestMinAmount(t *testing.T) {
	const src = "spec/views/helpers/charge_display_helper_spec.rb:"
	w := testWriter(t)
	g := &ids{}
	plan := newPlan(g, "plan", func(p *catalog.Plan) { p.Currency = usd() })
	sub := newSubscription(g, plan)
	w.subs[sub.Subscription.ID] = &sub
	c, m := metricCharge(g, plan, "metric", func(c *catalog.Charge) { c.MinAmount = 500 })
	f := chargeFee(g, &sub, &c, &m, nil)
	t.Logf("source: %s15", src)
	if got, want := w.minAmount(&f), "$5.00"; got != want {
		t.Errorf("min amount: got = %q, want = %q", got, want)
	}
	pu := catalog.PricingUnit{ShortName: "CR"}
	c.AppliedPricingUnit = &catalog.AppliedPricingUnit{ConversionRate: d("1")}
	f.PricingUnit = &pu
	t.Logf("source: %s23", src)
	if got, want := w.minAmount(&f), "5.00 CR"; got != want {
		t.Errorf("min amount: got = %q, want = %q", got, want)
	}
}

// source: spec/views/helpers/line_break_helper_spec.rb:9
func TestBreakLines(t *testing.T) {
	for _, in := range []string{"t\nt", "t\nt\n\n\n", "\n\n\nt\n\n\nt\n\n\n"} {
		if got := breakLines(in); !slices.Equal(got, []string{"t", "t"}) {
			t.Errorf("breakLines(%q): got = %q, want = [t t]", in, got)
		}
	}
}

// source: spec/views/helpers/interval_helper_spec.rb:7
func TestIntervalName(t *testing.T) {
	w := testWriter(t)
	for _, c := range []struct {
		interval period.Interval
		want     string
	}{
		{period.Weekly, "week"},
		{period.Monthly, "month"},
		{period.Yearly, "year"},
		{period.Quarterly, "quarter"},
		{period.Semiannual, "half-year"},
		{period.Interval(0), ""},
	} {
		s := Subscription{Plan: catalog.Plan{Interval: c.interval}}
		if got := w.intervalName(&s); got != c.want {
			t.Errorf("interval name %v: got = %q, want = %q", c.interval, got, c.want)
		}
	}
}

// source: spec/views/helpers/date_helper_spec.rb:9
func TestDateFormat(t *testing.T) {
	w := testWriter(t)
	if got, want := w.date(day("2024-01-02")), "Jan 02, 2024"; got != want {
		t.Errorf("date: got = %q, want = %q", got, want)
	}
	if got := w.date(core.Date{}); got != "" {
		t.Errorf("zero date: got = %q, want = \"\"", got)
	}
}

func TestBreakdownAmount(t *testing.T) {
	sum := &Fee{Metric: &catalog.BillableMetric{AggregationType: catalog.AggregationTypeSum}}
	unique := &Fee{Metric: &catalog.BillableMetric{AggregationType: catalog.AggregationTypeUniqueCount}}
	for _, c := range []struct {
		f      *Fee
		amount string
		want   string
	}{
		{sum, "10", "10.0"},
		{sum, "2.50", "2.5"},
		{sum, "-5", "-5.0"},
		{unique, "2.25", "3"},
		{unique, "4", "4"},
	} {
		if got := breakdownAmount(c.f, d(c.amount)); got != c.want {
			t.Errorf("breakdownAmount(%s, %s): got = %q, want = %q", c.f.Metric.AggregationType, c.amount, got, c.want)
		}
	}
}

// grouped_by is a jsonb column: PostgreSQL orders its keys by length, then
// bytewise, and lago-api prints the values in that order.
func TestGroupedByDisplayOrder(t *testing.T) {
	f := &Fee{Fee: invoice.Fee{Type: core.FeeTypeCharge, GroupedBy: map[string]string{"region": "eu", "az": "a", "cloud": "aws", "env": "prod"}}}
	if got, want := groupedByDisplay(f), " • a • prod • aws • eu"; got != want {
		t.Errorf("groupedByDisplay: got = %q, want = %q", got, want)
	}
}
