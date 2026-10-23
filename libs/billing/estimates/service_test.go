package estimates_test

import (
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/estimates"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

type fixture struct {
	*billingtest.Harness
	svc  *estimates.Service
	cust *customer.Customer
	plan *catalog.Plan
	sub  *subscription.Subscription
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := billingtest.New(t, now)
	f := &fixture{Harness: h, svc: estimates.New(h.Deps)}
	f.cust = h.Customer()
	f.plan = h.Plan(period.Monthly, 0)
	f.sub = h.Subscription(f.cust, f.plan, billingtest.SubscribedAt(now.AddDate(-1, 0, 0)))
	return f
}

func inAdvance(c *catalog.Charge) { c.PayInAdvance = true }

func expression(src string) func(*catalog.BillableMetric) {
	return func(m *catalog.BillableMetric) { m.Expression = &src }
}

func (f *fixture) event(m *catalog.BillableMetric, props map[string]string) estimates.Event {
	return estimates.Event{Code: m.Code, ExternalSubscriptionID: f.sub.ExternalID, Properties: props}
}

func wantField(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) || !ve.Has(field, code) {
		t.Errorf("error got = %v, want = %s %s", err, field, code)
	}
}

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Errorf("error got = %v, want = %s_not_found", err, resource)
	}
}

// source: spec/services/fees/estimate_pay_in_advance_service_spec.rb:44
func TestPayInAdvance(t *testing.T) {
	f := newFixture(t)
	m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"100"}`, inAdvance)
	fs, err := f.svc.PayInAdvance(f.Ctx, f.OrgID(), f.event(m, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 {
		t.Fatalf("fees got = %d, want = 1", len(fs))
	}
	fee := fs[0]
	if fee.ChargeID != c.ID || fee.SubscriptionID != f.sub.ID || !fee.PayInAdvance || fee.EventsCount != 1 ||
		!fee.PayInAdvanceEventID.IsZero() || fee.PayInAdvanceEventTransactionID == "" || fee.Amount != 100_00 {
		t.Errorf("fee got = %+v, want = 100.00 on the charge, for one unstored event", fee)
	}
	if len(f.Emitted.Events()) != 0 {
		t.Errorf("webhooks got = %v, want = none", f.Emitted.Events())
	}
}

// source: spec/services/fees/estimate_pay_in_advance_service_spec.rb:62
func TestPayInAdvanceTaxes(t *testing.T) {
	f := newFixture(t)
	m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"100"}`, inAdvance)
	be := f.Tax("be_vat", "20")
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.TaxIDs = []core.TaxID{be.ID} })
	fs, err := f.svc.PayInAdvance(f.Ctx, f.OrgID(), f.event(m, nil))
	if err != nil {
		t.Fatal(err)
	}
	if fee := fs[0]; len(fee.AppliedTaxes) != 1 || fee.AppliedTaxes[0].TaxCode != "be_vat" || !fee.TaxesRate.Equal(money.MustParse("20")) || fee.TaxesAmount <= 0 {
		t.Errorf("fee taxes got = %+v rate %v amount %d, want = be_vat at 20", fee.AppliedTaxes, fee.TaxesRate, fee.TaxesAmount)
	}

	// source: spec/services/fees/estimate_pay_in_advance_service_spec.rb:80
	ct := f.Tax("customer_vat", "8")
	f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.TaxIDs = []core.TaxID{ct.ID} })
	fs, err = f.svc.PayInAdvance(f.Ctx, f.OrgID(), f.event(m, nil))
	if err != nil {
		t.Fatal(err)
	}
	if fee := fs[0]; len(fee.AppliedTaxes) != 1 || fee.AppliedTaxes[0].TaxCode != "customer_vat" || !fee.TaxesRate.Equal(money.MustParse("8")) {
		t.Errorf("fee taxes got = %+v rate %v, want = customer_vat at 8", fee.AppliedTaxes, fee.TaxesRate)
	}
}

// source: spec/services/fees/estimate_pay_in_advance_service_spec.rb:97
func TestPayInAdvanceDynamic(t *testing.T) {
	f := newFixture(t)
	m := f.Metric("sum", catalog.AggregationTypeSum, "value")
	f.Charge(f.plan, m, catalog.ChargeModelDynamic, `{}`, inAdvance)
	ev := f.event(m, map[string]string{"value": "10"})
	amount := money.MustParse("12000")
	ev.PreciseTotalAmountCents = &amount
	fs, err := f.svc.PayInAdvance(f.Ctx, f.OrgID(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Amount != 120_00 {
		t.Errorf("fees got = %+v, want = one fee of 120.00", fs)
	}
}

// source: spec/services/fees/estimate_pay_in_advance_service_spec.rb:131
func TestPayInAdvanceExpression(t *testing.T) {
	f := newFixture(t)
	m := f.Metric("sum", catalog.AggregationTypeSum, "result", expression("event.properties.left + event.properties.right"))
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"100"}`, inAdvance)
	fs, err := f.svc.PayInAdvance(f.Ctx, f.OrgID(), f.event(m, map[string]string{"left": "1", "right": "2"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || !fs[0].Units.Equal(money.NewFromInt(3)) || fs[0].Amount != 300_00 {
		t.Errorf("fees got = %+v, want = 3 units for 300.00", fs)
	}

	// source: spec/services/fees/estimate_pay_in_advance_service_spec.rb:165
	_, err = f.svc.PayInAdvance(f.Ctx, f.OrgID(), f.event(m, nil))
	wantField(t, err, "properties", estimates.CodeExpressionFailed)
}

func TestPayInAdvanceErrors(t *testing.T) {
	f := newFixture(t)
	m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
	sum := f.Metric("sum", catalog.AggregationTypeSum, "value")
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"100"}`)
	f.Charge(f.plan, sum, catalog.ChargeModelStandard, `{"amount":"1"}`, inAdvance)

	// source: spec/services/fees/estimate_pay_in_advance_service_spec.rb:182
	_, err := f.svc.PayInAdvance(f.Ctx, f.OrgID(), f.event(m, nil))
	wantField(t, err, "code", estimates.CodeNoInstantCharge)

	// source: spec/services/fees/estimate_pay_in_advance_service_spec.rb:217
	_, err = f.svc.PayInAdvance(f.Ctx, f.OrgID(), estimates.Event{Code: m.Code})
	wantNotFound(t, err, "subscription")
	_, err = f.svc.PayInAdvance(f.Ctx, f.OrgID(), estimates.Event{Code: m.Code, ExternalSubscriptionID: "unknown"})
	wantNotFound(t, err, "subscription")

	// source: app/services/events/validate_creation_service.rb:52,56
	_, err = f.svc.PayInAdvance(f.Ctx, f.OrgID(), f.event(&catalog.BillableMetric{Code: "unknown"}, nil))
	wantNotFound(t, err, "billable_metric")
	_, err = f.svc.PayInAdvance(f.Ctx, f.OrgID(), f.event(sum, map[string]string{"value": "abc"}))
	wantField(t, err, "properties", estimates.CodeNotANumber)
}

// source: spec/services/fees/estimate_pay_in_advance_service_spec.rb:194
func TestPayInAdvanceMultipleCharges(t *testing.T) {
	f := newFixture(t)
	m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"100"}`, inAdvance)
	f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`, inAdvance)
	fs, err := f.svc.PayInAdvance(f.Ctx, f.OrgID(), f.event(m, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Errorf("fees got = %d, want = 2", len(fs))
	}
}

// source: spec/services/fees/estimate_instant/batch_pay_in_advance_service_spec.rb
func TestInstant(t *testing.T) {
	tests := []struct {
		name       string
		agg        catalog.AggregationType
		field      string
		expression string
		model      catalog.ChargeModel
		props      string
		event      map[string]string
		wantAmount int64
		wantUnits  string
	}{
		// :56
		{name: "percentage", agg: catalog.AggregationTypeSum, field: "value", model: catalog.ChargeModelPercentage, props: `{"rate":"0.1","fixed_amount":"0"}`, wantAmount: 0, wantUnits: "0"},
		// :70
		{name: "percentage with properties", agg: catalog.AggregationTypeSum, field: "value", model: catalog.ChargeModelPercentage, props: `{"rate":"0.1","fixed_amount":"0"}`, event: map[string]string{"value": "500"}, wantAmount: 50, wantUnits: "500"},
		// :100
		{name: "standard with properties", agg: catalog.AggregationTypeSum, field: "value", model: catalog.ChargeModelStandard, props: `{"amount":"10"}`, event: map[string]string{"value": "500"}, wantAmount: 500000, wantUnits: "500"},
		// :113
		{name: "standard with an expression", agg: catalog.AggregationTypeSum, field: "value", expression: "event.properties.test * 2", model: catalog.ChargeModelStandard, props: `{"amount":"10"}`, event: map[string]string{"test": "200"}, wantAmount: 400000, wantUnits: "400"},
		// :129
		{name: "percentage with an expression", agg: catalog.AggregationTypeSum, field: "value", expression: "event.properties.test * 2", model: catalog.ChargeModelPercentage, props: `{"rate":"0.1","fixed_amount":"0"}`, event: map[string]string{"test": "200"}, wantAmount: 40, wantUnits: "400"},
		// :184
		{name: "metric without field name", agg: catalog.AggregationTypeCount, model: catalog.ChargeModelPercentage, props: `{"rate":"10","fixed_amount":"0"}`, wantAmount: 10, wantUnits: "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			var opts []func(*catalog.BillableMetric)
			if tt.expression != "" {
				opts = append(opts, expression(tt.expression))
			}
			m := f.Metric("bm", tt.agg, tt.field, opts...)
			c := f.Charge(f.plan, m, tt.model, tt.props, inAdvance)
			ev := f.event(m, tt.event)
			ev.TransactionID = "tr_1"
			fs, err := f.svc.Instant(f.Ctx, f.OrgID(), []estimates.Event{ev})
			if err != nil {
				t.Fatal(err)
			}
			if len(fs) != 1 {
				t.Fatalf("fees got = %d, want = 1", len(fs))
			}
			got := fs[0]
			if got.Charge.ID != c.ID || got.TransactionID != "tr_1" || got.Amount != tt.wantAmount || !got.Units.Equal(money.MustParse(tt.wantUnits)) {
				t.Errorf("fee got = amount %d units %v charge %v tx %q, want = amount %d units %s", got.Amount, got.Units, got.Charge.ID, got.TransactionID, tt.wantAmount, tt.wantUnits)
			}
		})
	}
}

func TestInstantBatch(t *testing.T) {
	f := newFixture(t)
	m := f.Metric("bm", catalog.AggregationTypeSum, "value")
	m2 := f.Metric("bm2", catalog.AggregationTypeSum, "value")
	f.Charge(f.plan, m, catalog.ChargeModelPercentage, `{"rate":"0.1","fixed_amount":"0"}`, inAdvance)
	f.Charge(f.plan, m2, catalog.ChargeModelStandard, `{"amount":"1"}`, inAdvance)

	// source: spec/services/fees/estimate_instant/batch_pay_in_advance_service_spec.rb:248
	fs, err := f.svc.Instant(f.Ctx, f.OrgID(), []estimates.Event{
		f.event(m, map[string]string{"value": "100"}),
		f.event(m2, map[string]string{"value": "500"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[0].Amount != 10 || fs[1].Amount != 50000 {
		t.Errorf("fees got = %+v, want = 0.10 and 500.00", fs)
	}
	if fs[0].TransactionID == "" || fs[0].TransactionID == fs[1].TransactionID {
		t.Errorf("transaction IDs got = %q and %q, want = two generated IDs", fs[0].TransactionID, fs[1].TransactionID)
	}

	// source: spec/services/fees/estimate_instant/batch_pay_in_advance_service_spec.rb:172
	_, err = f.svc.Instant(f.Ctx, f.OrgID(), []estimates.Event{{Code: m.Code}})
	wantNotFound(t, err, "subscription")

	// source: spec/services/fees/estimate_instant/batch_pay_in_advance_service_spec.rb:159
	f2 := newFixture(t)
	m3 := f2.Metric("bm", catalog.AggregationTypeSum, "value")
	f2.Charge(f2.plan, m3, catalog.ChargeModelStandard, `{"amount":"1"}`, inAdvance)
	f2.Charge(f2.plan, m3, catalog.ChargeModelStandard, `{"amount":"2"}`, inAdvance)
	fs, err = f2.svc.Instant(f2.Ctx, f2.OrgID(), []estimates.Event{f2.event(m3, nil)})
	if err != nil || len(fs) != 2 {
		t.Errorf("fees got = %d, %v, want = one per charge", len(fs), err)
	}
	f3 := newFixture(t)
	m4 := f3.Metric("bm", catalog.AggregationTypeSum, "value")
	f3.Charge(f3.plan, m4, catalog.ChargeModelPercentage, `{"rate":"1"}`)
	_, err = f3.svc.Instant(f3.Ctx, f3.OrgID(), []estimates.Event{f3.event(m4, nil)})
	wantField(t, err, "code", estimates.CodeNoInstantCharge)
}

// source: app/services/fees/estimate_instant/base_service.rb:15 (the
// charge filter's properties price the event)
func TestInstantChargeFilter(t *testing.T) {
	f := newFixture(t)
	m := f.Metric("bm", catalog.AggregationTypeSum, "value", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{{Key: "region", Values: []string{"eu", "us"}}}
	})
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`, inAdvance, func(c *catalog.Charge) {
		c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"3"}`)}
	})
	fs, err := f.svc.Instant(f.Ctx, f.OrgID(), []estimates.Event{
		f.event(m, map[string]string{"value": "2", "region": "eu"}),
		f.event(m, map[string]string{"value": "2", "region": "us"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 || fs[0].ChargeFilterID != c.Filters[0].ID || fs[0].Amount != 600 || !fs[1].ChargeFilterID.IsZero() || fs[1].Amount != 200 {
		t.Errorf("fees got = %+v, want = 6.00 in the filter and 2.00 without", fs)
	}
}
