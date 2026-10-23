package documents

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Fixtures mirroring lago-api's spec factories (spec/factories/*.rb).

func ptr[T any](v T) *T { return &v }

func d(s string) money.Decimal { return money.MustParse(s) }

func usd() money.Currency { return money.MustCurrency("USD") }

func eur() money.Currency { return money.MustCurrency("EUR") }

// day parses a "2006-01-02" date.
func day(s string) core.Date {
	v, err := core.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

// at parses a "2006-01-02 15:04:05" UTC instant (Time.zone.parse in the
// specs, whose zone is UTC).
func at(s string) time.Time {
	v, err := time.Parse(time.DateTime, s)
	if err != nil {
		panic(err)
	}
	return v
}

// span is the half-open period of lago's inclusive datetimes: to is the
// last second of the period ("2025-08-31 23:59:59").
func span(from, to string) period.Period {
	return period.Period{From: at(from), To: at(to).Add(time.Second)}
}

// ids hands out distinct deterministic identifiers.
type ids struct{ n int }

func (g *ids) uuid() string {
	g.n++
	return fmt.Sprintf("01960000-0000-7000-8000-%012d", g.n)
}

func (g *ids) invoice() core.InvoiceID { return core.MustParseID[core.InvoiceID](g.uuid()) }

func (g *ids) fee() core.FeeID { return core.MustParseID[core.FeeID](g.uuid()) }

func (g *ids) sub() core.SubscriptionID { return core.MustParseID[core.SubscriptionID](g.uuid()) }

func (g *ids) plan() core.PlanID { return core.MustParseID[core.PlanID](g.uuid()) }

func (g *ids) charge() core.ChargeID { return core.MustParseID[core.ChargeID](g.uuid()) }

func (g *ids) metric() core.BillableMetricID {
	return core.MustParseID[core.BillableMetricID](g.uuid())
}

func (g *ids) filter() core.ChargeFilterID { return core.MustParseID[core.ChargeFilterID](g.uuid()) }

func (g *ids) fixedCharge() core.FixedChargeID {
	return core.MustParseID[core.FixedChargeID](g.uuid())
}

func (g *ids) addOn() core.AddOnID { return core.MustParseID[core.AddOnID](g.uuid()) }

func (g *ids) commitment() core.CommitmentID { return core.MustParseID[core.CommitmentID](g.uuid()) }

func (g *ids) tax() core.TaxID { return core.MustParseID[core.TaxID](g.uuid()) }

// staticBillingEntity is billing_entity :with_static_values.
func staticBillingEntity() organization.BillingEntity {
	return organization.BillingEntity{
		Code:  "acme",
		Name:  "ACME Corporation",
		Email: ptr("billing@acme.com"),
		Address: core.Address{
			Line1: "123 Business St", Line2: "Suite 100", City: "San Francisco",
			State: "CA", Zipcode: "94105", Country: "US",
		},
		DocumentLocale:       "en",
		DocumentNumberPrefix: "ACM-8924",
	}
}

// staticCustomer is customer :with_static_values.
func staticCustomer() customer.Customer {
	addr := core.Address{
		Line1: "456 Customer Ave", Line2: "Apt 202", City: "New York",
		State: "NY", Zipcode: "10001", Country: "US",
	}
	return customer.Customer{
		ExternalID:      "customer_123",
		Firstname:       ptr("John"),
		Lastname:        ptr("Doe"),
		Name:            ptr("John Doe"),
		LegalName:       ptr("Doe Corp"),
		LegalNumber:     ptr("1234567890"),
		Email:           ptr("john.doe@example.com"),
		Phone:           ptr("+1-555-123-4567"),
		BillingAddress:  addr,
		ShippingAddress: addr,
	}
}

func staticParty() Party {
	return Party{BillingEntity: staticBillingEntity(), Customer: staticCustomer()}
}

// newPlan is the plan factory: monthly, 1.00 EUR.
func newPlan(g *ids, name string, mod func(p *catalog.Plan)) catalog.Plan {
	p := catalog.Plan{ID: g.plan(), Code: name, Name: name, Interval: period.Monthly, Amount: 100, Currency: eur()}
	if mod != nil {
		mod(&p)
	}
	return p
}

// newSubscription links a subscription of plan to an invoice.
func newSubscription(g *ids, p catalog.Plan, links ...func(is *invoice.Subscription)) Subscription {
	s := subscription.Subscription{ID: g.sub(), ExternalID: "sub_" + p.Code, PlanID: p.ID, Status: subscription.Status("active")}
	is := invoice.Subscription{SubscriptionID: s.ID, Reason: invoice.InvoicingReason("subscription_periodic")}
	for _, l := range links {
		l(&is)
	}
	return Subscription{InvoiceSubscription: is, Subscription: s, Plan: p}
}

// periods sets the three periods of an invoice subscription.
func periods(p period.Period) func(is *invoice.Subscription) {
	return func(is *invoice.Subscription) {
		is.Period, is.ChargesPeriod, is.FixedChargesPeriod = p, p, p
		is.Timestamp = p.To
	}
}

// newInvoice is the invoice factory.
func newInvoice(g *ids, typ invoice.Type, number string, mod func(inv *invoice.Invoice)) invoice.Invoice {
	inv := invoice.Invoice{
		ID: g.invoice(), Type: typ, Status: invoice.Status("finalized"), Number: number, Currency: usd(),
		TaxesRate: money.Zero,
	}
	if mod != nil {
		mod(&inv)
	}
	return inv
}

// newFee is the fee factory: a zero subscription fee in USD.
func newFee(g *ids, typ core.FeeType, mod func(f *invoice.Fee)) invoice.Fee {
	f := invoice.Fee{
		ID: g.fee(), Type: typ, Currency: usd(),
		PreciseAmount: money.Zero, PreciseUnitAmount: money.Zero, Units: money.Zero,
		TotalAggregatedUnits: money.Zero, TaxesPreciseAmount: money.Zero, TaxesRate: money.Zero,
		TaxesBaseRate: money.Zero, PreciseCouponsAmount: money.Zero, PreciseCreditNotesAmount: money.Zero,
		Invoiceable: true,
	}
	if mod != nil {
		mod(&f)
	}
	return f
}

// detailsJSON encodes amount details as pricing does.
func detailsJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode amount details: %v", err)
	}
	return b
}

func testCatalog(t *testing.T) *i18n.Catalog {
	t.Helper()
	cat, err := i18n.Load()
	if err != nil {
		t.Fatalf("load translations: %v", err)
	}
	return cat
}

// fixedSpec describes a fixed charge fee of the fixed_charge_fee factory.
type fixedSpec struct {
	name      string
	model     catalog.FixedChargeModel
	prorated  bool
	advance   bool
	amount    int64
	units     string
	unitCents int64
	precise   string
	details   any
	period    period.Period
	cur       money.Currency // USD when zero
}

// fixedFee builds a fixed charge fee with its fixed charge and add-on.
func fixedFee(t *testing.T, g *ids, sub *Subscription, s fixedSpec) Fee {
	t.Helper()
	addOn := catalog.AddOn{ID: g.addOn(), Code: "add_on", Name: "Add-on", Amount: 200, Currency: usd()}
	model := s.model
	if model == "" {
		model = catalog.FixedChargeModelStandard
	}
	fc := catalog.FixedCharge{
		ID: g.fixedCharge(), PlanID: sub.Plan.ID, AddOnID: addOn.ID, ChargeModel: model,
		Units: d("1"), PayInAdvance: s.advance, Prorated: s.prorated,
	}
	precise := s.precise
	if precise == "" {
		precise = "0"
	}
	f := newFee(g, core.FeeTypeFixedCharge, func(f *invoice.Fee) {
		f.SubscriptionID, f.FixedChargeID, f.AddOnID = sub.Subscription.ID, fc.ID, addOn.ID
		f.Amount, f.Units, f.UnitAmount, f.PreciseUnitAmount = s.amount, d(s.units), s.unitCents, d(precise)
		f.PayInAdvance, f.InvoiceDisplayName = s.advance, ptr(s.name)
		f.Currency = currencyOr(s.cur, usd())
		if s.details != nil {
			f.AmountDetails = detailsJSON(t, s.details)
		}
	})
	return Fee{Fee: f, FixedCharge: &fc, AddOn: &addOn, Period: s.period}
}

// graduatedFixedDetails are the graduated amount details of the v4 spec's
// fixed charge fees: 10 units at 5.00 + 200.00 flat, 5 at 1.00 + 300.00.
func graduatedFixedDetails() any {
	return map[string]any{"graduated_ranges": []map[string]any{
		{"from_value": 0, "to_value": 10, "units": "10.0", "per_unit_amount": "5.0", "per_unit_total_amount": "50.0", "flat_unit_amount": "200.0"},
		{"from_value": 11, "to_value": nil, "units": "5.0", "per_unit_amount": "1.0", "per_unit_total_amount": "5.0", "flat_unit_amount": "300.0"},
	}}
}

// metricCharge is a standard charge of a count metric (billable_metric and
// standard_charge factories).
func metricCharge(g *ids, plan catalog.Plan, metricName string, mod func(c *catalog.Charge)) (catalog.Charge, catalog.BillableMetric) {
	m := catalog.BillableMetric{ID: g.metric(), Code: metricName, Name: metricName, Description: ptr("some description"), AggregationType: catalog.AggregationTypeCount}
	c := catalog.Charge{
		ID: g.charge(), PlanID: plan.ID, BillableMetricID: m.ID, ChargeModel: catalog.ChargeModelStandard,
		Properties: catalog.MustProperties(`{"amount":"100"}`), Invoiceable: true,
	}
	if mod != nil {
		mod(&c)
	}
	return c, m
}

// chargeFee is the charge_fee factory for charge c of sub.
func chargeFee(g *ids, sub *Subscription, c *catalog.Charge, m *catalog.BillableMetric, mod func(f *invoice.Fee)) Fee {
	f := newFee(g, core.FeeTypeCharge, func(f *invoice.Fee) {
		f.SubscriptionID, f.ChargeID = sub.Subscription.ID, c.ID
		p := sub.InvoiceSubscription.ChargesPeriod
		f.Properties.ChargesFrom, f.Properties.ChargesTo = p.From, p.To
		if mod != nil {
			mod(f)
		}
	})
	return Fee{Fee: f, Charge: c, Metric: m}
}

func d0() money.Decimal { return money.Zero }
