package invoicing_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// finalizeAndVoid finalizes the draft inv and voids it.
func (f *fixture) finalizeAndVoid(inv *invoice.Invoice) *invoice.Invoice {
	f.T.Helper()
	if inv.Status == invoice.StatusDraft {
		if _, err := f.svc.FinalizeDraft(f.Ctx, f.OrgID(), inv.ID); err != nil {
			f.T.Fatalf("finalize: %v", err)
		}
	}
	if _, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{}); err != nil {
		f.T.Fatalf("void: %v", err)
	}
	return f.Invoice(inv.ID)
}

// voidedInvoice bills and voids the periodic invoice of a new
// subscription and returns it with its only fee, a subscription fee of
// 10.00, which opts change before the void.
func (f *fixture) voidedInvoice(opts ...func(*invoice.Fee)) (*invoice.Invoice, *invoice.Fee) {
	f.T.Helper()
	draft := f.billOne(f.subscribe())
	fs := f.Fees(draft.ID)
	if len(fs) != 1 {
		f.T.Fatalf("fees = %d, want = 1", len(fs))
	}
	for _, o := range opts {
		o(fs[0])
	}
	f.SaveFees(fs[0])
	return f.finalizeAndVoid(draft), fs[0]
}

// chargedVoidedInvoice adds a standard charge of 1.00 a unit to the plan,
// bills three units of January and voids the invoice, returning it with
// its subscription, charge and charge fee, which opts change before the
// void.
func (f *fixture) chargedVoidedInvoice(opts ...func(*invoice.Fee)) (*invoice.Invoice, *subscription.Subscription, *catalog.Charge, *invoice.Fee) {
	f.T.Helper()
	m := f.Metric("value_sum", catalog.AggregationTypeSum, "value")
	c := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	sub := f.subscribe()
	f.Event(sub, "value_sum", billingtest.Date(2026, time.January, 10), "value", "3")
	draft := f.billOne(sub)
	fee := f.chargeFeeOf(draft.ID, c.ID)
	for _, o := range opts {
		o(fee)
	}
	f.SaveFees(fee)
	return f.finalizeAndVoid(draft), sub, c, fee
}

// regenerate regenerates the voided invoice with the fees and returns the
// new invoice's fees.
func (f *fixture) regenerate(voided *invoice.Invoice, changes ...invoicing.RegeneratedFee) (*invoice.Invoice, []*invoice.Fee) {
	f.T.Helper()
	inv, err := f.svc.Regenerate(f.Ctx, f.OrgID(), voided.ID, invoicing.RegenerateInput{Fees: changes})
	if err != nil {
		f.T.Fatalf("regenerate: %v", err)
	}
	return f.Invoice(inv.ID), f.Fees(inv.ID)
}

// regeneratedFee regenerates the voided invoice with the changes and
// returns the only fee of the new invoice.
func (f *fixture) regeneratedFee(voided *invoice.Invoice, changes ...invoicing.RegeneratedFee) *invoice.Fee {
	f.T.Helper()
	_, fs := f.regenerate(voided, changes...)
	if len(fs) != 1 {
		f.T.Fatalf("regenerated fees = %d, want = 1", len(fs))
	}
	return fs[0]
}

func dec(s string) *money.Decimal {
	d := money.MustParse(s)
	return &d
}

// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:54
func TestRegenerateAdjustedFee(t *testing.T) {
	f := newFixture(t, billingDay)
	voided, old := f.voidedInvoice()
	got := f.regeneratedFee(voided, invoicing.RegeneratedFee{
		FeeID: old.ID, InvoiceDisplayName: new("new-dis-name"), Units: dec("10"), UnitAmount: dec("50.50"),
	})
	if got.InvoiceDisplayName == nil || *got.InvoiceDisplayName != "new-dis-name" {
		t.Errorf("invoice_display_name got = %v, want = new-dis-name", got.InvoiceDisplayName)
	}
	if !got.Units.Equal(money.NewFromInt(10)) || got.UnitAmount != 5050 || got.Amount != 50500 {
		t.Errorf("units, unit amount, amount = %s, %d, %d, want = 10, 5050, 50500", got.Units, got.UnitAmount, got.Amount)
	}
	if got.ID == old.ID || got.PaymentStatus != invoice.FeePaymentStatusPending {
		t.Errorf("id, payment status = %s, %s, want = a new pending fee", got.ID, got.PaymentStatus)
	}
	// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:172
	if got.OriginalFeeID != old.ID {
		t.Errorf("original_fee_id got = %s, want = %s", got.OriginalFeeID, old.ID)
	}
}

// A fee regenerated twice keeps the first voided fee as its original
// (dup_fee.original_fee = voided_fee.original_fee || voided_fee).
//
// source: app/services/invoices/regenerate_from_voided_service.rb:240
func TestRegenerateOriginalFeeChain(t *testing.T) {
	f := newFixture(t, billingDay)
	voided, old := f.voidedInvoice()
	first, fs := f.regenerate(voided, invoicing.RegeneratedFee{FeeID: old.ID, Units: dec("1")})
	second := f.finalizeAndVoid(first)
	got := f.regeneratedFee(second, invoicing.RegeneratedFee{FeeID: fs[0].ID, Units: dec("1")})
	if got.OriginalFeeID != old.ID {
		t.Errorf("original_fee_id got = %s, want = %s", got.OriginalFeeID, old.ID)
	}
}

// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:187
func TestRegenerateInvoiceSubscriptions(t *testing.T) {
	f := newFixture(t, billingDay)
	voided, old := f.voidedInvoice()
	before := f.invoiceSubscriptions(voided.ID)
	inv, _ := f.regenerate(voided, invoicing.RegeneratedFee{FeeID: old.ID, Units: dec("1")})
	got := f.invoiceSubscriptions(inv.ID)
	if len(got) != len(before) || len(got) != 1 {
		t.Fatalf("invoice subscriptions got = %d, want = %d", len(got), len(before))
	}
	if got[0].ID == before[0].ID || got[0].SubscriptionID != before[0].SubscriptionID ||
		got[0].ChargesPeriod != before[0].ChargesPeriod || !got[0].RegeneratedInvoiceID.IsZero() {
		t.Errorf("copy got = %+v, want = a copy of %+v", got[0], before[0])
	}
	// source: app/services/invoices/regenerate_from_voided_service.rb:263
	if marked := f.invoiceSubscriptions(voided.ID)[0]; marked.RegeneratedInvoiceID != inv.ID {
		t.Errorf("voided regenerated_invoice_id got = %s, want = %s", marked.RegeneratedInvoiceID, inv.ID)
	}
}

// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:196
func TestRegenerateProgressiveBilling(t *testing.T) {
	f, sub, th := progressive(t)
	pb := f.progressiveBill(sub, 100, th)
	fee := f.Fees(pb.ID)[0]
	voided := f.finalizeAndVoid(pb)
	inv, _ := f.regenerate(voided, invoicing.RegeneratedFee{FeeID: fee.ID, Units: &fee.Units})
	applied := func(id core.InvoiceID) []*subscription.AppliedUsageThreshold {
		return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.AppliedUsageThreshold, error) {
			return r.AppliedUsageThresholds.ListByInvoice(ctx, f.OrgID(), id)
		})
	}
	was, got := applied(voided.ID), applied(inv.ID)
	if len(got) != 1 || len(was) != 1 || got[0].ID == was[0].ID ||
		got[0].UsageThresholdID != th.ID || got[0].LifetimeUsageAmount != was[0].LifetimeUsageAmount {
		t.Errorf("applied thresholds got = %+v, want = a copy of %+v", got, was)
	}
	if got := len(f.invoiceSubscriptions(inv.ID)); got != 1 {
		t.Errorf("invoice subscriptions = %d, want = 1", got)
	}
}

// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:245
func TestRegenerateOneOff(t *testing.T) {
	f := newFixture(t, billingDay)
	inv, err := f.svc.CreateOneOff(f.Ctx, invoicing.OneOffInput{
		OrganizationID: f.OrgID(), CustomerID: f.cust.ID, Fees: []fees.OneOff{{AddOnID: f.AddOn(1000).ID}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	fee := f.Fees(inv.ID)[0]
	regenerated, fs := f.regenerate(f.finalizeAndVoid(inv), invoicing.RegeneratedFee{FeeID: fee.ID, Units: dec("2"), UnitAmount: dec("10")})
	if got := len(f.invoiceSubscriptions(regenerated.ID)); got != 0 {
		t.Errorf("invoice subscriptions = %d, want = 0", got)
	}
	if len(fs) != 1 || fs[0].Amount != 2000 {
		t.Errorf("fees got = %v, want = one of 2000", fs)
	}
}

// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:519
func TestRegenerateUpdatedUnits(t *testing.T) {
	f := newFixture(t, billingDay)
	voided, old := f.voidedInvoice()
	got := f.regeneratedFee(voided, invoicing.RegeneratedFee{FeeID: old.ID, Units: dec("3")})
	if got.InvoiceDisplayName != nil {
		t.Errorf("invoice_display_name got = %v, want = nil", *got.InvoiceDisplayName)
	}
	if !got.Units.Equal(money.NewFromInt(3)) || got.UnitAmount != old.UnitAmount || got.Amount != 3*old.UnitAmount {
		t.Errorf("units, unit amount, amount = %s, %d, %d, want = 3, %d, %d", got.Units, got.UnitAmount, got.Amount, old.UnitAmount, 3*old.UnitAmount)
	}
}

func TestRegeneratePresentationBreakdowns(t *testing.T) {
	breakdowns := func(fee *invoice.Fee) {
		fee.PresentationBreakdowns = []invoice.PresentationBreakdown{
			{PresentationBy: map[string]*string{"department": new("eng")}, Units: money.MustParse("0.6")},
			{PresentationBy: map[string]*string{"department": new("sales")}, Units: money.MustParse("0.4")},
		}
	}
	for _, tt := range []struct {
		name, source string
		change       func(old *invoice.Fee) invoicing.RegeneratedFee
		kept         bool
	}{
		{
			"units", "spec/services/invoices/regenerate_from_voided_service_spec.rb:389",
			func(old *invoice.Fee) invoicing.RegeneratedFee {
				return invoicing.RegeneratedFee{FeeID: old.ID, Units: dec("3")}
			}, false,
		},
		{
			"unit amount", "spec/services/invoices/regenerate_from_voided_service_spec.rb:407",
			func(old *invoice.Fee) invoicing.RegeneratedFee {
				return invoicing.RegeneratedFee{FeeID: old.ID, Units: &old.Units, UnitAmount: dec("99.99")}
			}, true,
		},
		{
			"display name with the units", "spec/services/invoices/regenerate_from_voided_service_spec.rb:427",
			func(old *invoice.Fee) invoicing.RegeneratedFee {
				return invoicing.RegeneratedFee{FeeID: old.ID, Units: &old.Units, InvoiceDisplayName: new("renamed")}
			}, true,
		},
		{
			"display name only", "spec/services/invoices/regenerate_from_voided_service_spec.rb:499",
			func(old *invoice.Fee) invoicing.RegeneratedFee {
				return invoicing.RegeneratedFee{FeeID: old.ID, InvoiceDisplayName: new("renamed")}
			}, false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newFixture(t, billingDay)
			voided, old := f.voidedInvoice(breakdowns)
			got := f.regeneratedFee(voided, tt.change(old))
			if kept := len(got.PresentationBreakdowns) == 2; kept != tt.kept {
				t.Errorf("breakdowns got = %v, want kept = %v", got.PresentationBreakdowns, tt.kept)
			}
			if !tt.kept && tt.name == "display name only" && !got.Units.IsZero() {
				t.Errorf("units got = %s, want = 0", got.Units)
			}
		})
	}
}

// Only the listed fees are regenerated, each listing copying its fee.
//
// source: app/services/invoices/regenerate_from_voided_service.rb:208-230
func TestRegenerateFeeSelection(t *testing.T) {
	t.Run("unlisted fees dropped", func(t *testing.T) {
		f := newFixture(t, billingDay)
		voided, _ := f.voidedInvoice()
		inv, fs := f.regenerate(voided)
		if len(fs) != 0 || inv.FeesAmount != 0 {
			t.Errorf("fees, amount got = %d, %d, want = 0, 0", len(fs), inv.FeesAmount)
		}
	})
	t.Run("listed twice", func(t *testing.T) {
		f := newFixture(t, billingDay)
		voided, old := f.voidedInvoice()
		inv, fs := f.regenerate(voided, invoicing.RegeneratedFee{FeeID: old.ID, Units: dec("1")}, invoicing.RegeneratedFee{FeeID: old.ID, Units: dec("2")})
		if len(fs) != 2 || inv.FeesAmount != 3*old.UnitAmount {
			t.Errorf("fees, amount got = %d, %d, want = 2, %d", len(fs), inv.FeesAmount, 3*old.UnitAmount)
		}
	})
	// source: app/services/adjusted_fees/create_service.rb:98
	t.Run("unknown fee without a subscription", func(t *testing.T) {
		f := newFixture(t, billingDay)
		voided, _ := f.voidedInvoice()
		_, err := f.svc.Regenerate(f.Ctx, f.OrgID(), voided.ID, invoicing.RegenerateInput{
			Fees: []invoicing.RegeneratedFee{{FeeID: core.NewID[core.FeeID](f.IDs), Units: dec("1")}},
		})
		wantNotFound(t, err, "subscription")
	})
	t.Run("negative units", func(t *testing.T) {
		f := newFixture(t, billingDay)
		voided, old := f.voidedInvoice()
		_, err := f.svc.Regenerate(f.Ctx, f.OrgID(), voided.ID, invoicing.RegenerateInput{
			Fees: []invoicing.RegeneratedFee{{FeeID: old.ID, Units: dec("-1")}},
		})
		wantField(t, err, "fees[0].units", core.CodeNegative)
	})
}

func TestRegenerateChargeFees(t *testing.T) {
	// The display name of a charge fee always comes from the adjustment.
	//
	// source: app/services/invoices/regenerate_from_voided_service.rb:131-150
	t.Run("display name cleared", func(t *testing.T) {
		f := newFixture(t, billingDay)
		voided, _, _, old := f.chargedVoidedInvoice(func(fee *invoice.Fee) { fee.InvoiceDisplayName = new("usage") })
		got := f.regeneratedFee(voided, invoicing.RegeneratedFee{FeeID: old.ID, Units: dec("4")})
		if got.InvoiceDisplayName != nil {
			t.Errorf("invoice_display_name got = %v, want = nil", *got.InvoiceDisplayName)
		}
		if !got.Units.Equal(money.NewFromInt(4)) || got.Amount != 400 {
			t.Errorf("units, amount got = %s, %d, want = 4, 400", got.Units, got.Amount)
		}
	})
	// A charge without a voided fee selects the charge's fee.
	t.Run("by charge", func(t *testing.T) {
		f := newFixture(t, billingDay)
		voided, sub, c, _ := f.chargedVoidedInvoice()
		got := f.regeneratedFee(voided, invoicing.RegeneratedFee{SubscriptionID: sub.ID, ChargeID: c.ID, Units: dec("2")})
		if got.ChargeID != c.ID || !got.Units.Equal(money.NewFromInt(2)) || got.Amount != 200 {
			t.Errorf("charge, units, amount got = %s, %s, %d, want = %s, 2, 200", got.ChargeID, got.Units, got.Amount, c.ID)
		}
	})
	// source: app/services/adjusted_fees/create_service.rb:23
	t.Run("already adjusted", func(t *testing.T) {
		f := newFixture(t, billingDay)
		voided, sub, c, old := f.chargedVoidedInvoice()
		_, err := f.svc.Regenerate(f.Ctx, f.OrgID(), voided.ID, invoicing.RegenerateInput{Fees: []invoicing.RegeneratedFee{
			{FeeID: old.ID, Units: dec("1")}, {SubscriptionID: sub.ID, ChargeID: c.ID, Units: dec("2")},
		}})
		wantField(t, err, "adjusted_fee", core.CodeAlreadyExists)
	})
	// source: spec/services/invoices/regenerate_from_voided_service_spec.rb:540
	t.Run("soft deleted charge", func(t *testing.T) {
		f := newFixture(t, billingDay)
		voided, sub, c, _ := f.chargedVoidedInvoice()
		f.Do(func(ctx context.Context, r port.Repos) error { return r.Charges.Discard(ctx, f.OrgID(), c.ID, f.Now()) })
		_, err := f.svc.Regenerate(f.Ctx, f.OrgID(), voided.ID, invoicing.RegenerateInput{
			Fees: []invoicing.RegeneratedFee{{SubscriptionID: sub.ID, ChargeID: c.ID, Units: dec("2")}},
		})
		wantNotFound(t, err, "charge")
	})
}

// source: spec/services/invoices/build_regeneration_preview_service_spec.rb:38
func TestRegenerationPreview(t *testing.T) {
	f := newFixture(t, billingDay)
	f.taxCustomer("20")
	voided, old := f.voidedInvoice()
	ten := f.Tax("vat_10", "10")
	f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.TaxIDs = []core.TaxID{ten.ID} })
	v, err := f.svc.RegenerationPreview(f.Ctx, f.OrgID(), voided.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if v.Invoice.ID != voided.ID || len(v.Fees) != 1 || v.Fees[0].Fee.ID != old.ID {
		t.Fatalf("preview got = %s with %d fees, want = %s with its fee", v.Invoice.ID, len(v.Fees), voided.ID)
	}
	// The fees are taxed again by the current taxes.
	checkAmounts(t, v.Invoice, 1000, 100, 1100)
	// Nothing is stored.
	checkAmounts(t, f.Invoice(voided.ID), 1000, 200, 1200)
	if got := f.Fees(voided.ID)[0].TaxesAmount; got != 200 {
		t.Errorf("stored fee taxes got = %d, want = 200", got)
	}
}

func TestRegenerationPreviewNotFound(t *testing.T) {
	f := newFixture(t, billingDay)
	_, err := f.svc.RegenerationPreview(f.Ctx, f.OrgID(), core.NewID[core.InvoiceID](f.IDs))
	wantNotFound(t, err, "invoice")
}

// customSection stores a custom section of the code.
func (f *fixture) customSection(code string) *invoice.CustomSection {
	f.T.Helper()
	s := &invoice.CustomSection{
		ID: core.NewID[core.InvoiceCustomSectionID](f.IDs), OrganizationID: f.OrgID(), Code: code, Name: code,
		DisplayName: new(code + " title"), Details: new(code + " details"), SectionType: invoice.CustomSectionTypeManual,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceCustomSections.Create(ctx, s) })
	return s
}

// appliedSections returns the codes of the sections applied to id.
func (f *fixture) appliedSections(id core.InvoiceID) []string {
	f.T.Helper()
	as := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.AppliedCustomSection, error) {
		return r.AppliedInvoiceCustomSections.ListByInvoice(ctx, f.OrgID(), id)
	})
	var out []string
	for _, a := range as {
		out = append(out, a.Code)
	}
	return out
}

func TestApplyInvoiceCustomSections(t *testing.T) {
	// source: spec/services/invoices/create_one_off_service_spec.rb (invoice_custom_section)
	t.Run("one-off by code", func(t *testing.T) {
		f := newFixture(t, billingDay)
		f.customSection("eu")
		f.customSection("us")
		add := f.AddOn(1000)
		v, err := f.svc.CreateOneOffRequest(f.Ctx, invoicing.OneOffRequest{
			OrganizationID: f.OrgID(), CustomerID: f.cust.ID,
			Fees:                  []invoicing.OneOffRequestFee{{AddOn: refs.ByID(add.ID)}},
			InvoiceCustomSections: []refs.Ref{refs.ByCode("eu"), refs.ByCode("unknown")},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := f.appliedSections(v.Invoice.ID); len(got) != 1 || got[0] != "eu" {
			t.Errorf("applied sections got = %v, want = [eu]", got)
		}
		if len(v.CustomSections) != 1 || v.CustomSections[0].Code != "eu" {
			t.Errorf("view sections got = %v, want = [eu]", v.CustomSections)
		}
	})
	t.Run("one-off skipped", func(t *testing.T) {
		f := newFixture(t, billingDay)
		eu := f.customSection("eu")
		f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{eu.ID} })
		inv, err := f.svc.CreateOneOff(f.Ctx, invoicing.OneOffInput{
			OrganizationID: f.OrgID(), CustomerID: f.cust.ID, Fees: []fees.OneOff{{AddOnID: f.AddOn(1000).ID}},
			SkipInvoiceCustomSections: true,
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := f.appliedSections(inv.ID); len(got) != 0 {
			t.Errorf("applied sections got = %v, want = none", got)
		}
	})
	// The regenerated invoice prints the customer's sections, by name.
	//
	// source: app/services/invoices/regenerate_from_voided_service.rb:29
	t.Run("regenerated with the customer's sections", func(t *testing.T) {
		f := newFixture(t, billingDay)
		us, eu := f.customSection("us"), f.customSection("eu")
		f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{us.ID, eu.ID} })
		voided, old := f.voidedInvoice()
		inv, _ := f.regenerate(voided, invoicing.RegeneratedFee{FeeID: old.ID, Units: dec("1")})
		if got := f.appliedSections(inv.ID); len(got) != 2 || got[0] != "eu" || got[1] != "us" {
			t.Errorf("applied sections got = %v, want = [eu us]", got)
		}
	})
	t.Run("regenerated, customer skips", func(t *testing.T) {
		f := newFixture(t, billingDay)
		eu := f.customSection("eu")
		f.UpdateCustomer(f.cust, func(c *customer.Customer) {
			c.InvoiceCustomSectionIDs, c.SkipInvoiceCustomSections = []core.InvoiceCustomSectionID{eu.ID}, true
		})
		voided, old := f.voidedInvoice()
		inv, _ := f.regenerate(voided, invoicing.RegeneratedFee{FeeID: old.ID, Units: dec("1")})
		if got := f.appliedSections(inv.ID); len(got) != 0 {
			t.Errorf("applied sections got = %v, want = none", got)
		}
	})
}
