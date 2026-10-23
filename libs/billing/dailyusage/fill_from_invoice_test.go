package dailyusage_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

const fillInvoiceSpec = "spec/services/daily_usages/fill_from_invoice_service_spec.rb"

// invoiceFixture is the setup of fill_from_invoice_service_spec.rb: a New
// York customer's invoice billing a subscription for the charges period
// 2025-06-07 04:00 to 2025-07-01 04:00 UTC, at 2025-07-01 04:10:02.
type invoiceFixture struct {
	*fixture
	inv    *invoice.Invoice
	sub    *subscription.Subscription
	metric *catalog.BillableMetric
	ts     time.Time
	cp     period.Period
}

func newInvoiceFixture(t *testing.T) *invoiceFixture {
	t.Helper()
	ts := time.Date(2025, 7, 1, 4, 10, 2, 0, time.UTC)
	ny := paris(t, "America/New_York")
	f := &invoiceFixture{
		fixture: newFixture(t, ts, func(c *customer.Customer) { c.Timezone = ny }),
		ts:      ts,
		cp:      period.Period{From: billingtest.Date(2025, 6, 7, 4), To: billingtest.Date(2025, 7, 1, 4)},
	}
	f.metric = f.Metric("api_calls", catalog.AggregationTypeCount, "")
	f.sub = f.fixture.sub(f.plan, billingtest.Date(2025, 1, 1))
	f.inv = f.StoredInvoice(f.cust, func(inv *invoice.Invoice) { inv.IssuingDate = core.NewDate(2025, 7, 1) })
	return f
}

// link links the invoice to s for the charges period cp.
func (f *invoiceFixture) link(s *subscription.Subscription, cp period.Period) {
	f.T.Helper()
	is := &invoice.Subscription{
		ID: core.NewID[core.InvoiceSubscriptionID](f.IDs), OrganizationID: f.OrgID(), InvoiceID: f.inv.ID,
		SubscriptionID: s.ID, Reason: invoice.InvoicingReasonSubscriptionPeriodic,
		Period: period.Period{From: billingtest.Date(2025, 6, 6, 4), To: billingtest.Date(2025, 7, 1, 4)}, ChargesPeriod: cp,
		Timestamp: f.ts, Recurring: true, CreatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceSubscriptions.Create(ctx, is) })
}

// chargeFee stores a charge fee of s on the invoice for a new charge of
// amount.
func (f *invoiceFixture) chargeFee(s *subscription.Subscription, units int64, amount, taxes int64, opts ...func(*invoice.Fee)) *invoice.Fee {
	f.T.Helper()
	c := f.Charge(f.plan, f.metric, catalog.ChargeModelStandard, `{"amount":"1"}`)
	return f.StoredFee(f.inv, amount, append([]func(*invoice.Fee){billingtest.ChargeFee(s, c.ID), func(fee *invoice.Fee) {
		fee.Units, fee.TaxesAmount = money.NewFromInt(units), taxes
		fee.Properties.ChargesFrom, fee.Properties.ChargesTo = f.cp.From, f.cp.To
	}}, opts...)...)
}

// inAdvanceFee stores an unbilled pay-in-advance charge fee of s from an
// event, for the charges period [from, to).
func (f *invoiceFixture) inAdvanceFee(s *subscription.Subscription, from, to time.Time) *invoice.Fee {
	f.T.Helper()
	return f.chargeFee(s, 1, 200, 2, func(fee *invoice.Fee) {
		fee.InvoiceID = core.InvoiceID{}
		fee.PayInAdvance, fee.PayInAdvanceEventTransactionID = true, "tr_1"
		fee.Properties.ChargesFrom, fee.Properties.ChargesTo = from, to
	})
}

func (f *invoiceFixture) fill(subs ...*subscription.Subscription) []*subscription.DailyUsage {
	f.T.Helper()
	ids := make([]core.SubscriptionID, len(subs))
	for i, s := range subs {
		ids[i] = s.ID
	}
	out, err := f.svc.FillFromInvoice(f.Ctx, f.OrgID(), f.inv.ID, ids)
	if err != nil {
		f.T.Fatalf("FillFromInvoice() error = %v", err)
	}
	return out
}

func TestFillFromInvoiceSkips(t *testing.T) {
	tests := []struct {
		source string
		cp     func(f *invoiceFixture) period.Period
		fee    bool
	}{
		{source: ":44", cp: func(f *invoiceFixture) period.Period { return f.cp }},
		{source: ":53", cp: func(*invoiceFixture) period.Period { return period.Period{} }, fee: true},
		{source: ":80", cp: func(f *invoiceFixture) period.Period { return period.Period{From: f.cp.To, To: f.cp.From} }, fee: true},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			f := newInvoiceFixture(t)
			f.link(f.sub, tt.cp(f))
			if tt.fee {
				f.chargeFee(f.sub, 12, 1200, 0)
			}
			if got := len(f.fill(f.sub)); got != 0 || len(f.usages(f.sub)) != 0 {
				t.Errorf("%s%s: stored: got = %d, want = 0", fillInvoiceSpec, tt.source, got)
			}
		})
	}
}

func TestFillFromInvoice(t *testing.T) {
	// source: spec/services/daily_usages/fill_from_invoice_service_spec.rb:112
	// and :422 (usage_date is the local day of the period's end)
	f := newInvoiceFixture(t)
	f.link(f.sub, f.cp)
	f.chargeFee(f.sub, 12, 1200, 0)
	out := f.fill(f.sub)
	if len(out) != 1 || len(f.usages(f.sub)) != 1 {
		t.Fatalf("%s:112: stored: got = %d, want = 1", fillInvoiceSpec, len(out))
	}
	du := out[0]
	checkRow(t, fillInvoiceSpec+":112", du, f.sub, core.NewDate(2025, 6, 30), f.ts, f.cp.From, f.cp.To)
	if du.UsageDiff == nil {
		t.Errorf("%s:112: usage_diff: got = nil, want = the whole usage", fillInvoiceSpec)
	}
	if du.Usage.IssuingDate != core.NewDate(2025, 7, 1) || du.Usage.Currency != "USD" || du.Usage.Amount != 1200 {
		t.Errorf("%s:112: usage head: got = %s %s %d, want = 2025-07-01 USD 1200", fillInvoiceSpec, du.Usage.IssuingDate, du.Usage.Currency, du.Usage.Amount)
	}
}

func TestFillFromInvoiceFees(t *testing.T) {
	tests := []struct {
		source      string
		fees        func(f *invoiceFixture)
		wantCharges int
		wantAmount  int64
		wantTotal   int64
	}{
		{source: ":132", fees: func(f *invoiceFixture) {
			f.chargeFee(f.sub, 12, 1200, 0)
			f.chargeFee(f.sub, 0, 0, 0)
		}, wantCharges: 1, wantAmount: 1200, wantTotal: 1200},
		{source: ":145", fees: func(f *invoiceFixture) {
			f.chargeFee(f.sub, 5, 0, 0)
		}, wantCharges: 1, wantAmount: 0, wantTotal: 0},
		{source: ":291", fees: func(f *invoiceFixture) {
			f.chargeFee(f.sub, 10, 1000, 100)
			f.inAdvanceFee(f.sub, f.cp.From, f.cp.To)
			f.StoredFee(f.inv, 300, billingtest.SubscriptionFee(f.sub))
		}, wantCharges: 2, wantAmount: 1200, wantTotal: 1302},
		{source: ":342 (seconds)", fees: func(f *invoiceFixture) {
			f.inAdvanceFee(f.sub, f.cp.From, f.cp.To)
		}, wantCharges: 1, wantAmount: 200, wantTotal: 202},
		{source: ":381 (milliseconds)", fees: func(f *invoiceFixture) {
			f.inAdvanceFee(f.sub, f.cp.From.Add(400*time.Microsecond), f.cp.To.Add(999*time.Microsecond))
		}, wantCharges: 1, wantAmount: 200, wantTotal: 202},
		{source: "tanvo (another period's in-advance fee)", fees: func(f *invoiceFixture) {
			f.chargeFee(f.sub, 1, 100, 0)
			f.inAdvanceFee(f.sub, f.cp.From.Add(time.Second), f.cp.To)
		}, wantCharges: 1, wantAmount: 100, wantTotal: 100},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			src := fillInvoiceSpec + tt.source
			f := newInvoiceFixture(t)
			f.link(f.sub, f.cp)
			tt.fees(f)
			out := f.fill(f.sub)
			if len(out) != 1 {
				t.Fatalf("%s: stored: got = %d, want = 1", src, len(out))
			}
			u := out[0].Usage
			if len(u.Charges) != tt.wantCharges || u.Amount != tt.wantAmount || u.TotalAmount != tt.wantTotal {
				t.Errorf("%s: charges, amount, total: got = %d, %d, %d, want = %d, %d, %d", src,
					len(u.Charges), u.Amount, u.TotalAmount, tt.wantCharges, tt.wantAmount, tt.wantTotal)
			}
		})
	}
}

func TestFillFromInvoiceExisting(t *testing.T) {
	tests := []struct {
		source string
		// to is the end of the stored row's period.
		to       func(f *invoiceFixture) time.Time
		wantRows int
	}{
		{source: ":163", to: func(f *invoiceFixture) time.Time { return f.cp.To }, wantRows: 1},
		{source: ":250", to: func(f *invoiceFixture) time.Time { return f.cp.To.Add(-time.Microsecond) }, wantRows: 2},
	}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			f := newInvoiceFixture(t)
			f.link(f.sub, f.cp)
			f.chargeFee(f.sub, 12, 1200, 0)
			f.storeUsage(f.sub, core.NewDate(2025, 6, 30), func(du *subscription.DailyUsage) {
				du.From, du.To, du.RefreshedAt = f.cp.From, tt.to(f), f.ts
			})
			f.fill(f.sub)
			if got := len(f.usages(f.sub)); got != tt.wantRows {
				t.Errorf("%s%s: rows: got = %d, want = %d", fillInvoiceSpec, tt.source, got, tt.wantRows)
			}
		})
	}
}

func TestFillFromInvoiceSubscriptions(t *testing.T) {
	// source: spec/services/daily_usages/fill_from_invoice_service_spec.rb:183-232
	for _, both := range []bool{true, false} {
		f := newInvoiceFixture(t)
		sub2 := f.fixture.sub(f.plan, billingtest.Date(2025, 1, 1))
		f.link(f.sub, f.cp)
		f.link(sub2, f.cp)
		f.chargeFee(f.sub, 12, 1200, 0)
		f.chargeFee(sub2, 12, 1200, 0)
		subs, want := []*subscription.Subscription{f.sub}, 1
		if both {
			subs, want = append(subs, sub2), 2
		}
		out := f.fill(subs...)
		if len(out) != want {
			t.Errorf("%s:183 (both = %v): stored: got = %d, want = %d", fillInvoiceSpec, both, len(out), want)
		}
		for _, du := range out {
			// Each snapshot holds its own subscription's fees only.
			if du.Usage.Amount != 1200 || len(du.Usage.Charges) != 1 {
				t.Errorf("%s:183: usage of %s: got = %d over %d charges, want = 1200 over 1", fillInvoiceSpec, du.SubscriptionID, du.Usage.Amount, len(du.Usage.Charges))
			}
		}
	}
}

func TestFillFromInvoicePresentationBreakdowns(t *testing.T) {
	// source: spec/services/daily_usages/compute_service_spec.rb:112 (the
	// serialized breakdowns of a fee; billing does not compute them yet, so
	// the fee is stored with them)
	f := newInvoiceFixture(t)
	f.link(f.sub, f.cp)
	region := "eu-central"
	f.chargeFee(f.sub, 1, 100, 0, func(fee *invoice.Fee) {
		fee.PresentationBreakdowns = []invoice.PresentationBreakdown{{PresentationBy: map[string]*string{"region": &region}, Units: money.One}}
	})
	out := f.fill(f.sub)
	if len(out) != 1 {
		t.Fatalf("stored: got = %d, want = 1", len(out))
	}
	got, err := json.Marshal(out[0].Usage.Charges[0].PresentationBreakdowns)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"presentation_by":{"region":"eu-central"},"units":"1"}]`; string(got) != want {
		t.Errorf("%s:112: presentation_breakdowns: got = %s, want = %s", computeSpec, got, want)
	}
}

// The snapshot names the metric and the charge filter of a fee even when
// they were discarded after the fee was computed, as lago reads them with
// their discarded rows (app/models/fee.rb:18,33,
// app/serializers/v1/customers/charge_usage_serializer.rb:72-100).
func TestFillFromInvoiceDiscardedCatalog(t *testing.T) {
	f := newInvoiceFixture(t)
	m := f.Metric("regions", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](f.IDs), Key: "region", Values: []string{"eu", "us"}}}
	})
	eu := "Europe"
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
		c.Filters = []catalog.ChargeFilter{
			{Values: map[string][]string{"region": {"eu"}}, Properties: catalog.MustProperties(`{"amount":"1"}`), InvoiceDisplayName: &eu},
			{Values: map[string][]string{"region": {"us"}}, Properties: catalog.MustProperties(`{"amount":"2"}`)},
		}
	})
	f.link(f.sub, f.cp)
	f.StoredFee(f.inv, 300, billingtest.ChargeFee(f.sub, c.ID), func(fee *invoice.Fee) {
		fee.Units, fee.ChargeFilterID = money.NewFromInt(3), c.Filters[0].ID
		fee.Properties.ChargesFrom, fee.Properties.ChargesTo = f.cp.From, f.cp.To
	})
	f.Do(func(ctx context.Context, r port.Repos) error {
		kept := *c
		kept.Filters = c.Filters[1:]
		kept.UpdatedAt = f.Now()
		if err := r.Charges.Update(ctx, &kept); err != nil {
			return err
		}
		return r.Metrics.Discard(ctx, f.OrgID(), m.ID, f.Now())
	})

	out := f.fill(f.sub)
	if len(out) != 1 || len(out[0].Usage.Charges) != 1 {
		t.Fatalf("snapshot got = %+v, want = one charge", out)
	}
	cu := out[0].Usage.Charges[0]
	if cu.BillableMetric.Code != "regions" || cu.BillableMetric.Name != "regions" || cu.BillableMetric.AggregationType != string(catalog.AggregationTypeCount) {
		t.Errorf("billable metric got = %+v, want = the discarded regions metric", cu.BillableMetric)
	}
	if len(cu.Filters) != 1 {
		t.Fatalf("filters got = %+v, want = one", cu.Filters)
	}
	if got := cu.Filters[0]; got.InvoiceDisplayName == nil || *got.InvoiceDisplayName != eu || len(got.Values["region"]) != 1 || got.Values["region"][0] != "eu" {
		t.Errorf("filter got = %+v, want = the discarded eu filter", got)
	}
}
