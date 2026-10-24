package invoicing_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/contracts"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// contractAdvance is a segment fixture whose invoicing service bills the
// events of contracts in advance, with a count metric "calls" filtered
// by region and an active contract started on 1 July.
type contractAdvance struct {
	*segmentFixture
	metric *catalog.BillableMetric
	ct     *subscription.Subscription
}

// sep2 is the day the fixture's rates come in force (its clock).
var sep2 = core.NewDate(2026, time.September, 2)

func newContractAdvance(t *testing.T, opts ...func(*subscription.Subscription)) *contractAdvance {
	t.Helper()
	f := newSegmentFixture(t, 0)
	ct, err := contracts.New(f.Deps)
	if err != nil {
		t.Fatalf("contracts.New() error = %v", err)
	}
	f.svc.WithContractSegments(ct)
	m := f.Metric("calls", catalog.AggregationTypeCount, "", f.filtered("region", "eu", "us"))
	return &contractAdvance{segmentFixture: f, metric: m, ct: f.contract(opts...)}
}

// advanceCard creates a monthly rate card in advance of a new metered
// product of the metric, at a standard 10.00 per event, shown on invoices
// when display is set, on the product filter selecting the region when
// one is given, and applies it to the contract from 2 September.
func (f *contractAdvance) advanceCard(code string, m *catalog.BillableMetric, display bool, region string) (*catalog.RateCard, *subscription.RateCard) {
	f.T.Helper()
	return f.pricedCard(code, m, display, region, catalog.ChargeModelStandard, `{"amount":"10"}`)
}

// pricedCard is [contractAdvance.advanceCard] with the rate's model and
// properties.
func (f *contractAdvance) pricedCard(code string, m *catalog.BillableMetric, display bool, region string, model catalog.ChargeModel, props string) (*catalog.RateCard, *subscription.RateCard) {
	f.T.Helper()
	ctx, org := f.Ctx, f.OrgID()
	p, err := f.cards.CreateProduct(ctx, org, products.ProductInput{Code: code, Name: code, ProductType: catalog.ProductTypeMetered, Metric: refs.ByID(m.ID)})
	if err != nil {
		f.T.Fatalf("CreateProduct() error = %v", err)
	}
	in := products.RateCardInput{
		Product: refs.ByID(p.ID), Code: code, Name: code, Currency: billingtest.USD,
		BillingTiming: catalog.BillingTimingAdvance, DisplayOnInvoice: &display,
		Rates: []products.RateInput{{
			Code: "base", EffectiveFrom: f.Now(), RateModel: model,
			Properties: catalog.MustProperties(props), BillingIntervalUnit: catalog.IntervalUnitMonth,
		}},
	}
	if region != "" {
		pf, err := f.cards.CreateFilter(ctx, org, p.ID, products.FilterInput{
			Code: code + "-" + region, Name: region, Values: []products.FilterValueInput{{Key: "region", Value: &region}},
		})
		if err != nil {
			f.T.Fatalf("CreateFilter() error = %v", err)
		}
		in.Filter = refs.ByID(pf.ID)
	}
	c, err := f.cards.CreateRateCard(ctx, org, in)
	if err != nil {
		f.T.Fatalf("CreateRateCard() error = %v", err)
	}
	return c, f.ContractRateCard(f.ct, c, func(rc *subscription.RateCard) { rc.EffectiveDate, rc.BillingAnchorDate = sep2, sep2 })
}

// bill stores an event of the metric at ts with the properties and bills
// it in advance, returning its fees.
func (f *contractAdvance) bill(ts time.Time, props ...string) []*invoice.Fee {
	f.T.Helper()
	e := f.Event(f.ct, f.metric.Code, ts, props...)
	return f.billEvent(kernel.InAdvanceChargeArgs{
		SubscriptionID: f.ct.ID, TransactionID: e.TransactionID, Timestamp: ts, Code: f.metric.Code, Properties: e.Properties,
	})
}

// billEvent bills the event of the args in advance.
func (f *contractAdvance) billEvent(a kernel.InAdvanceChargeArgs) []*invoice.Fee {
	f.T.Helper()
	fs, err := f.svc.InAdvanceCharge(f.Ctx, f.OrgID(), a)
	if err != nil {
		f.T.Fatalf("InAdvanceCharge() error = %v", err)
	}
	return fs
}

func TestInAdvanceContract(t *testing.T) {
	t.Parallel()
	ts := billingtest.Date(2026, time.September, 10, 12)
	sep2At, oct2 := sep2.Midnight(time.UTC), billingtest.Date(2026, time.October, 2)

	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:133
	// source: spec/services/events/pay_in_advance_service_spec.rb:167
	t.Run("stores a standalone product fee of a card not shown on invoices", func(t *testing.T) {
		t.Parallel()
		f := newContractAdvance(t)
		f.taxCustomer("20")
		c, card := f.advanceCard("calls", f.metric, false, "")
		fs := f.bill(ts)
		if len(fs) != 1 {
			t.Fatalf("fees = %d, want = 1", len(fs))
		}
		fee := fs[0]
		if fee.Type != core.FeeTypeProduct || !fee.InvoiceID.IsZero() || !fee.ChargeID.IsZero() || fee.Invoiceable {
			t.Errorf("type, invoice, charge, invoiceable = %s, %s, %s, %t, want = product, none, none, false", fee.Type, fee.InvoiceID, fee.ChargeID, fee.Invoiceable)
		}
		if fee.SubscriptionID != f.ct.ID || fee.SubscriptionRateCardID != card.ID || fee.ProductID != c.ProductID {
			t.Errorf("contract, card, product = %s, %s, %s, want = %s, %s, %s", fee.SubscriptionID, fee.SubscriptionRateCardID, fee.ProductID, f.ct.ID, card.ID, c.ProductID)
		}
		if !fee.PayInAdvance || fee.PaymentStatus != invoice.FeePaymentStatusPending || fee.Amount != 1000 || fee.TaxesAmount != 200 || fee.EventsCount != 1 {
			t.Errorf("in advance, payment, amount, taxes, events = %t, %s, %d, %d, %d, want = true, pending, 1000, 200, 1", fee.PayInAdvance, fee.PaymentStatus, fee.Amount, fee.TaxesAmount, fee.EventsCount)
		}
		p := fee.Properties
		if !p.From.Equal(sep2At) || !p.To.Equal(oct2) || !p.ChargesFrom.Equal(sep2At) || !p.ChargesTo.Equal(oct2) {
			t.Errorf("properties = %+v, want = the segment from 2026-09-02 to 2026-10-02", p)
		}
		if got := f.Invoices(f.cust.ID); len(got) != 0 {
			t.Errorf("invoices = %d, want = 0", len(got))
		}
		if !slices.Contains(f.Webhooks(), webhook.EventTypeFeeCreated) {
			t.Errorf("webhooks = %v, want = fee.created", f.Webhooks())
		}
	})

	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:180
	t.Run("stores no other fee on retry", func(t *testing.T) {
		t.Parallel()
		f := newContractAdvance(t)
		f.advanceCard("calls", f.metric, false, "")
		e := f.Event(f.ct, f.metric.Code, ts)
		a := kernel.InAdvanceChargeArgs{SubscriptionID: f.ct.ID, TransactionID: e.TransactionID, Timestamp: ts, Code: f.metric.Code}
		for i, want := range []int{1, 0} {
			if got := len(f.billEvent(a)); got != want {
				t.Fatalf("run %d: fees = %d, want = %d", i, got, want)
			}
		}
	})

	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:199
	t.Run("bills a filtered card the events of its filter", func(t *testing.T) {
		t.Parallel()
		f := newContractAdvance(t)
		c, _ := f.advanceCard("calls", f.metric, false, "eu")
		fs := f.bill(ts, "region", "eu")
		if len(fs) != 1 || fs[0].ProductFilterID != c.ProductFilterID {
			t.Fatalf("fees = %+v, want = one fee of the card's filter", fs)
		}
		// source: app/services/fees/charge_service/sources/billing_segment.rb:58
		if got := f.bill(ts, "region", "us"); len(got) != 0 {
			t.Errorf("fees of another region = %d, want = 0", len(got))
		}
	})

	// source: app/services/fees/charge_service/sources/billing_segment.rb:50-61
	t.Run("bills a whole-product card only the events of no filter", func(t *testing.T) {
		t.Parallel()
		f := newContractAdvance(t)
		filtered, _ := f.advanceCard("calls", f.metric, false, "eu")
		whole := f.wholeCard(filtered.ProductID)
		eu, us := f.bill(ts, "region", "eu"), f.bill(ts, "region", "us")
		if len(eu) != 1 || eu[0].ProductFilterID != filtered.ProductFilterID {
			t.Errorf("fees of eu = %+v, want = one fee of the filtered card", eu)
		}
		if len(us) != 1 || us[0].SubscriptionRateCardID != whole.ID || !us[0].ProductFilterID.IsZero() {
			t.Errorf("fees of us = %+v, want = one fee of the whole-product card", us)
		}
	})

	// source: app/services/fees/create_pay_in_advance_service.rb:59-71, app/services/charges/pay_in_advance_aggregation_service.rb:14-28
	t.Run("prices each event by the difference it makes to the segment", func(t *testing.T) {
		t.Parallel()
		f := newContractAdvance(t)
		f.pricedCard("calls", f.metric, false, "", catalog.ChargeModelGraduated,
			`{"graduated_ranges":[{"to_value":1,"per_unit_amount":"10","flat_amount":"0"},{"to_value":null,"per_unit_amount":"5","flat_amount":"0"}]}`)
		var got []int64
		for i := range 3 {
			fs := f.bill(ts.Add(time.Duration(i) * time.Hour))
			if len(fs) != 1 {
				t.Fatalf("event %d: fees = %d, want = 1", i, len(fs))
			}
			got = append(got, fs[0].Amount)
		}
		if want := []int64{1000, 500, 500}; !slices.Equal(got, want) {
			t.Errorf("amounts = %v, want = %v", got, want)
		}
	})

	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:222
	// source: spec/services/events/pay_in_advance_service_spec.rb:184
	t.Run("stores a fee for each product of the metric", func(t *testing.T) {
		t.Parallel()
		f := newContractAdvance(t)
		_, first := f.advanceCard("calls", f.metric, false, "")
		_, second := f.advanceCard("more-calls", f.metric, false, "")
		fs := f.bill(ts)
		got := []core.SubscriptionRateCardID{}
		for _, fee := range fs {
			got = append(got, fee.SubscriptionRateCardID)
		}
		want := []core.SubscriptionRateCardID{first.ID, second.ID}
		slices.SortFunc(got, func(a, b core.SubscriptionRateCardID) int { return a.Compare(b) })
		slices.SortFunc(want, func(a, b core.SubscriptionRateCardID) int { return a.Compare(b) })
		if !slices.Equal(got, want) {
			t.Errorf("cards = %v, want = %v", got, want)
		}
	})

	// source: spec/services/fees/create_pay_in_advance_service_spec.rb:246
	t.Run("stores a fee for each transaction", func(t *testing.T) {
		t.Parallel()
		f := newContractAdvance(t)
		f.advanceCard("calls", f.metric, false, "")
		first, second := f.bill(ts), f.bill(ts.Add(time.Hour))
		if len(first) != 1 || len(second) != 1 || first[0].PayInAdvanceEventTransactionID == second[0].PayInAdvanceEventTransactionID {
			t.Fatalf("fees = %d, %d, want = one per transaction", len(first), len(second))
		}
	})

	// source: spec/services/events/pay_in_advance_service_spec.rb:138
	t.Run("bills nothing for an event without the metric's field", func(t *testing.T) {
		t.Parallel()
		f := newContractAdvance(t)
		f.metric = f.Metric("storage", catalog.AggregationTypeSum, "quantity")
		f.advanceCard("storage", f.metric, false, "")
		if got := f.bill(ts, "wrong_field_name", "5"); len(got) != 0 {
			t.Errorf("fees = %d, want = 0", len(got))
		}
		// source: spec/services/events/pay_in_advance_service_spec.rb:129
		if got := f.bill(ts, "quantity", "5"); len(got) != 1 || got[0].Amount != 5000 {
			t.Errorf("fees = %+v, want = one fee of 50.00", got)
		}
	})

	// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:188
	// source: spec/services/events/pay_in_advance_service_spec.rb:229
	t.Run("invoices the fee of a card shown on invoices", func(t *testing.T) {
		t.Parallel()
		f := newContractAdvance(t, func(s *subscription.Subscription) {
			po := "PO-CONTRACT-123"
			s.PurchaseOrderNumber = &po
		})
		f.taxCustomer("20")
		f.advanceCard("calls", f.metric, true, "")
		fs := f.bill(ts)
		if len(fs) != 1 || fs[0].InvoiceID.IsZero() || fs[0].Type != core.FeeTypeProduct {
			t.Fatalf("fees = %+v, want = one product fee on an invoice", fs)
		}
		inv := f.Invoice(fs[0].InvoiceID)
		if inv.Type != invoice.TypeSubscription || inv.Status != invoice.StatusFinalized || inv.Currency != billingtest.USD {
			t.Errorf("type, status, currency = %s, %s, %s, want = subscription, finalized, USD", inv.Type, inv.Status, inv.Currency)
		}
		if inv.PurchaseOrderNumber == nil || *inv.PurchaseOrderNumber != "PO-CONTRACT-123" {
			t.Errorf("purchase order number = %v, want = PO-CONTRACT-123", inv.PurchaseOrderNumber)
		}
		checkAmounts(t, inv, 1000, 200, 1200)
		if !f.emitted(webhook.EventTypeInvoiceCreated) || !f.emitted(webhook.EventTypeFeeCreated) {
			t.Errorf("webhooks = %v, want = invoice.created and fee.created", f.Webhooks())
		}
	})

	sections := []struct {
		name   string
		skip   bool
		entity bool
		want   []string
	}{
		// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:209
		{name: "copies the contract's custom section onto the invoice", want: []string{"contract"}},
		// source: spec/services/invoices/create_pay_in_advance_charge_service_spec.rb:216
		{name: "applies no section of a contract that skips them", skip: true, entity: true},
	}
	for _, c := range sections {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newContractAdvance(t)
			if c.entity {
				cs := f.customSection("entity")
				f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{cs.ID} })
			}
			f.ct.SkipInvoiceCustomSections = c.skip
			if !c.skip {
				f.ct.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{f.customSection("contract").ID}
			}
			f.Do(func(ctx context.Context, r port.Repos) error { return r.Subscriptions.Update(ctx, f.ct) })
			f.advanceCard("calls", f.metric, true, "")
			fs := f.bill(ts)
			if len(fs) != 1 {
				t.Fatalf("fees = %d, want = 1", len(fs))
			}
			if got := f.appliedSections(fs[0].InvoiceID); !slices.Equal(got, c.want) {
				t.Errorf("applied sections = %v, want = %v", got, c.want)
			}
		})
	}
}

// wholeCard creates a rate card in advance pricing the whole product,
// not shown on invoices, and applies it to the contract from 2 September.
func (f *contractAdvance) wholeCard(p core.ProductID) *subscription.RateCard {
	f.T.Helper()
	display := false
	c, err := f.cards.CreateRateCard(f.Ctx, f.OrgID(), products.RateCardInput{
		Product: refs.ByID(p), Code: "whole", Name: "whole", Currency: billingtest.USD,
		BillingTiming: catalog.BillingTimingAdvance, DisplayOnInvoice: &display,
		Rates: []products.RateInput{{
			Code: "base", EffectiveFrom: f.Now(), RateModel: catalog.ChargeModelStandard,
			Properties: catalog.MustProperties(`{"amount":"10"}`), BillingIntervalUnit: catalog.IntervalUnitMonth,
		}},
	})
	if err != nil {
		f.T.Fatalf("CreateRateCard() error = %v", err)
	}
	return f.ContractRateCard(f.ct, c, func(rc *subscription.RateCard) { rc.EffectiveDate, rc.BillingAnchorDate = sep2, sep2 })
}
