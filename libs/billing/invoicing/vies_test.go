package invoicing_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// viesSetup makes the fixture's billing entity manage EU taxes and, when
// pending, gives its customer a VIES check in progress.
func (f *fixture) viesSetup(eu, pending bool) {
	f.T.Helper()
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.EUTaxManagement = eu })
	if pending {
		f.Do(func(ctx context.Context, r port.Repos) error {
			return r.PendingVIESChecks.Save(ctx, &customer.PendingVIESCheck{
				OrganizationID: f.OrgID(), CustomerID: f.cust.ID, BillingEntityID: f.cust.BillingEntityID,
				CreatedAt: f.Now(), UpdatedAt: f.Now(),
			})
		})
	}
}

// completeVIES deletes the customer's pending VIES check.
func (f *fixture) completeVIES() {
	f.T.Helper()
	f.Do(func(ctx context.Context, r port.Repos) error {
		_, err := r.PendingVIESChecks.Delete(ctx, f.OrgID(), f.cust.ID)
		return err
	})
}

// finalizeVIES runs the pending VIES finalization of id as its job does.
func (f *fixture) finalizeVIES(id core.InvoiceID) *invoice.Invoice {
	f.T.Helper()
	inv, err := f.svc.FinalizePendingVIES(f.Ctx, f.OrgID(), id)
	if err != nil {
		f.T.Fatalf("finalize pending vies: %v", err)
	}
	return f.Invoice(inv.ID)
}

// TestVIESHoldSubscriptionInvoice bills a periodic invoice while the
// customer's VIES check is in progress or not.
func TestVIESHoldSubscriptionInvoice(t *testing.T) {
	tests := []struct {
		name, source string
		eu, pending  bool
		provider     bool
		grace        int
		wantStatus   invoice.Status
		wantTax      invoice.TaxStatus
		wantPull     int
		wantWebhook  webhook.EventType
	}{
		{"check in progress", "spec/services/invoices/compute_taxes_and_totals_service_spec.rb:89", true, true, false, 0, invoice.StatusOpen, invoice.TaxStatusPending, 0, ""},
		{"not finalizing", "spec/services/invoices/compute_taxes_and_totals_service_spec.rb:101", true, true, false, 3, invoice.StatusDraft, invoice.TaxStatusPending, 0, webhook.EventTypeInvoiceDrafted},
		{"no check", "spec/services/invoices/ensure_completed_vies_check_service_spec.rb:46", true, false, false, 0, invoice.StatusFinalized, invoice.TaxStatusSucceeded, 0, webhook.EventTypeInvoiceCreated},
		{"EU taxes not managed", "spec/services/invoices/ensure_completed_vies_check_service_spec.rb:35", false, true, false, 0, invoice.StatusFinalized, invoice.TaxStatusSucceeded, 0, webhook.EventTypeInvoiceCreated},
		{"tax provider wins", "spec/services/invoices/compute_taxes_and_totals_service_spec.rb:114", true, true, true, 0, invoice.StatusOpen, invoice.TaxStatusPending, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newFixture(t, billingDay)
			f.taxCustomer("20")
			f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = tt.grace })
			f.viesSetup(tt.eu, tt.pending)
			if tt.provider {
				f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
			}
			inv := f.billOne(f.subscribe())
			if inv.Status != tt.wantStatus || inv.TaxStatus != tt.wantTax {
				t.Fatalf("status, tax status = %s, %s, want = %s, %s", inv.Status, inv.TaxStatus, tt.wantStatus, tt.wantTax)
			}
			if got := f.enqueued(kernel.JobPullProviderTaxes); got != tt.wantPull {
				t.Errorf("tax pulls = %d, want = %d", got, tt.wantPull)
			}
			if tt.wantWebhook != "" && !f.emitted(tt.wantWebhook) {
				t.Errorf("webhooks = %v, want = %s", f.Webhooks(), tt.wantWebhook)
			}
			if tt.wantStatus == invoice.StatusOpen && (f.emitted(webhook.EventTypeInvoiceCreated) || f.enqueued(kernel.JobCreateInvoicePayment) != 0) {
				t.Errorf("webhooks, jobs = %v, %v, want = no invoice.created and no payment", f.Webhooks(), f.JobKinds())
			}
		})
	}
}

// TestVIESHoldOneOff creates a one-off invoice while the customer's VIES
// check is in progress: it waits, keeping the explicit taxes of its fees,
// and is not announced.
//
// source: app/services/invoices/create_one_off_service.rb:64-71, app/services/fees/one_off_service.rb:55-62
func TestVIESHoldOneOff(t *testing.T) {
	f := newFixture(t, billingDay)
	f.viesSetup(true, true)
	tx := f.Tax("explicit", "10")
	addOn := f.AddOn(1000)
	inv, err := f.svc.CreateOneOff(f.Ctx, invoicing.OneOffInput{
		OrganizationID: f.OrgID(), CustomerID: f.cust.ID,
		Fees: []fees.OneOff{{AddOnID: addOn.ID, TaxCodes: []string{tx.Code}}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	inv = f.Invoice(inv.ID)
	if inv.Status != invoice.StatusOpen || inv.TaxStatus != invoice.TaxStatusPending {
		t.Fatalf("status, tax status = %s, %s, want = open, pending", inv.Status, inv.TaxStatus)
	}
	if f.emitted(webhook.EventTypeInvoiceOneOffCreated) || f.enqueued(kernel.JobCreateInvoicePayment) != 0 {
		t.Errorf("webhooks, jobs = %v, %v, want = no invoice.one_off_created and no payment", f.Webhooks(), f.JobKinds())
	}
	if fs := f.Fees(inv.ID); len(fs) != 1 || fs[0].TaxesAmount != 100 {
		t.Fatalf("fee taxes = %v, want = 100", fs)
	}

	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:318
	f.completeVIES()
	inv = f.finalizeVIES(inv.ID)
	if inv.Status != invoice.StatusFinalized || inv.TaxStatus != invoice.TaxStatusSucceeded {
		t.Fatalf("status, tax status = %s, %s, want = finalized, succeeded", inv.Status, inv.TaxStatus)
	}
	checkAmounts(t, inv, 1000, 100, 1100)
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:323
	if !f.emitted(webhook.EventTypeInvoiceOneOffCreated) || f.emitted(webhook.EventTypeInvoiceCreated) {
		t.Errorf("webhooks = %v, want = invoice.one_off_created", f.Webhooks())
	}
}

// TestFinalizePendingVIES finalizes a periodic invoice held for the VIES
// check once the check decided the customer's tax.
func TestFinalizePendingVIES(t *testing.T) {
	f := newFixture(t, billingDay)
	f.viesSetup(true, true)
	cs := f.customSection("vies")
	f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{cs.ID} })
	inv := f.billOne(f.subscribe())
	if inv.TaxStatus != invoice.TaxStatusPending {
		t.Fatalf("tax status = %s, want = pending", inv.TaxStatus)
	}
	held := len(f.appliedSections(inv.ID))

	// The check applied the customer's EU tax and was deleted.
	f.taxCustomer("20")
	f.completeVIES()
	inv = f.finalizeVIES(inv.ID)
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:133
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:138
	if inv.Status != invoice.StatusFinalized || inv.TaxStatus != invoice.TaxStatusSucceeded {
		t.Fatalf("status, tax status = %s, %s, want = finalized, succeeded", inv.Status, inv.TaxStatus)
	}
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:143
	checkAmounts(t, inv, 1000, 200, 1200)
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:159
	if inv.PaymentStatus != core.PaymentStatusPending {
		t.Errorf("payment status = %s, want = pending", inv.PaymentStatus)
	}
	if inv.Number == "" {
		t.Errorf("number = %q, want = a number", inv.Number)
	}
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:151
	// ApplyInvoiceCustomSectionsService.call(invoice:) adds the customer's
	// sections again (finalize_pending_vies_invoice_service.rb:25).
	if got := f.appliedSections(inv.ID); len(got) != held+1 || !slices.Contains(got, "vies") {
		t.Errorf("applied sections = %v, want = %d with vies", got, held+1)
	}
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:165
	if !f.emitted(webhook.EventTypeInvoiceCreated) {
		t.Errorf("webhooks = %v, want = invoice.created", f.Webhooks())
	}
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:176
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:181
	if f.enqueued(kernel.JobGenerateInvoiceDocument) != 1 || f.enqueued(kernel.JobCreateInvoicePayment) != 1 {
		t.Errorf("jobs = %v, want = a document and a payment", f.JobKinds())
	}
	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:274
	// (no net payment term: due on the issuing date).
	if inv.PaymentDueDate != inv.IssuingDate {
		t.Errorf("payment due date = %s, want = %s", inv.PaymentDueDate, inv.IssuingDate)
	}
}

// TestFinalizePendingVIESSkips leaves alone the invoices the finalization
// does not apply to.
func TestFinalizePendingVIESSkips(t *testing.T) {
	tests := []struct {
		name, source string
		setup        func(f *fixture, inv *invoice.Invoice)
	}{
		{"check in progress", "spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:117", func(*fixture, *invoice.Invoice) {}},
		{"tax provider", "spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:102", func(f *fixture, _ *invoice.Invoice) {
			f.completeVIES()
			f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
		}},
		{"taxes not pending", "spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:90", func(f *fixture, inv *invoice.Invoice) {
			f.completeVIES()
			inv.TaxStatus = invoice.TaxStatusSucceeded
			f.SaveInvoice(inv)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newFixture(t, billingDay)
			f.viesSetup(true, true)
			inv := f.billOne(f.subscribe())
			tt.setup(f, inv)
			before := f.Invoice(inv.ID)
			got := f.finalizeVIES(inv.ID)
			if got.Status != before.Status || got.TaxStatus != before.TaxStatus || got.TotalAmount != before.TotalAmount {
				t.Errorf("status, tax status, total = %s, %s, %d, want = %s, %s, %d",
					got.Status, got.TaxStatus, got.TotalAmount, before.Status, before.TaxStatus, before.TotalAmount)
			}
			if f.emitted(webhook.EventTypeInvoiceCreated) {
				t.Errorf("webhooks = %v, want = no invoice.created", f.Webhooks())
			}
		})
	}
}

// TestFinalizePendingVIESJob runs the job the VIES check enqueues.
//
// source: spec/jobs/invoices/finalize_pending_vies_invoice_job_spec.rb:16
func TestFinalizePendingVIESJob(t *testing.T) {
	f := newFixture(t, billingDay)
	f.viesSetup(true, true)
	inv := f.billOne(f.subscribe())
	f.completeVIES()
	f.Do(func(ctx context.Context, r port.Repos) error { return f.svc.EnqueueFinalizePendingVIES(ctx, r, inv) })
	if f.enqueued(invoicing.JobFinalizePendingVIES) != 1 {
		t.Fatalf("jobs = %v, want = %s", f.JobKinds(), invoicing.JobFinalizePendingVIES)
	}
	args := []byte(`{"invoice_id":"` + inv.ID.String() + `"}`)
	ok, err := f.svc.HandleJob(f.Ctx, port.Job{Kind: invoicing.JobFinalizePendingVIES, OrganizationID: f.OrgID(), Args: args})
	if !ok || err != nil {
		t.Fatalf("handle = %t, %v, want = true, nil", ok, err)
	}
	if got := f.Invoice(inv.ID); got.Status != invoice.StatusFinalized {
		t.Errorf("status = %s, want = finalized", got.Status)
	}
}

// TestVIESInAdvanceFixedCharges bills pay-in-advance fixed charges while
// the customer's VIES check is in progress, then finalizes the invoice.
func TestVIESInAdvanceFixedCharges(t *testing.T) {
	t.Run("held", func(t *testing.T) {
		f := newFCFixture(t)
		f.viesSetup(true, true)
		s := f.subscribe()
		f.event(s, f.fc, "10", fcAt)
		inv := f.Invoice(f.run(s).ID)
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:420
		if inv.Status != invoice.StatusOpen || inv.TaxStatus != invoice.TaxStatusPending {
			t.Fatalf("status, tax status = %s, %s, want = open, pending", inv.Status, inv.TaxStatus)
		}
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:428
		if f.emitted(webhook.EventTypeInvoiceCreated) || f.enqueued(kernel.JobCreateInvoicePayment) != 0 {
			t.Errorf("webhooks, jobs = %v, %v, want = no invoice.created and no payment", f.Webhooks(), f.JobKinds())
		}
		// source: spec/services/invoices/create_pay_in_advance_fixed_charges_service_spec.rb:437
		if !f.emitted(webhook.EventTypeFeeCreated) {
			t.Errorf("webhooks = %v, want = fee.created", f.Webhooks())
		}
		// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:450
		f.completeVIES()
		inv = f.finalizeVIES(inv.ID)
		if inv.Status != invoice.StatusFinalized || inv.TaxStatus != invoice.TaxStatusSucceeded || inv.FeesAmount != 10000 {
			t.Errorf("status, tax status, fees = %s, %s, %d, want = finalized, succeeded, 10000", inv.Status, inv.TaxStatus, inv.FeesAmount)
		}
		// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:468
		// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:473
		if !f.emitted(webhook.EventTypeInvoiceCreated) || f.enqueued(kernel.JobCreateInvoicePayment) != 1 {
			t.Errorf("webhooks, jobs = %v, %v, want = invoice.created and a payment", f.Webhooks(), f.JobKinds())
		}
	})

	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:547
	t.Run("gated", func(t *testing.T) {
		f := newFCFixture(t)
		f.viesSetup(true, true)
		s := f.subscribe()
		rule := f.gated(s)
		f.event(s, f.fc, "10", fcAt)
		inv := f.Invoice(f.run(s).ID)
		if inv.Status != invoice.StatusOpen || inv.TaxStatus != invoice.TaxStatusPending || f.enqueued(kernel.JobCreateInvoicePayment) != 0 {
			t.Fatalf("status, tax status, jobs = %s, %s, %v, want = open, pending, no payment", inv.Status, inv.TaxStatus, f.JobKinds())
		}
		f.completeVIES()
		inv = f.finalizeVIES(inv.ID)
		if inv.Status != invoice.StatusOpen || inv.TaxStatus != invoice.TaxStatusSucceeded {
			t.Errorf("status, tax status = %s, %s, want = open, succeeded", inv.Status, inv.TaxStatus)
		}
		if f.emitted(webhook.EventTypeInvoiceCreated) || f.enqueued(kernel.JobCreateInvoicePayment) != 1 {
			t.Errorf("webhooks, jobs = %v, %v, want = a payment only", f.Webhooks(), f.JobKinds())
		}
		if got := f.ruleStatus(rule.ID, s); got != subscription.ActivationRuleStatusPending {
			t.Errorf("rule = %s, want = pending", got)
		}
	})

	// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:556
	t.Run("gated with zero total", func(t *testing.T) {
		f := newFCFixture(t)
		f.viesSetup(true, true)
		s := f.subscribe()
		rule := f.gated(s)
		f.event(s, f.fc, "0", fcAt)
		inv := f.Invoice(f.run(s).ID)
		// The gate waits for the taxes (create_pay_in_advance_fixed_charges_service.rb:56).
		if got := f.ruleStatus(rule.ID, s); got != subscription.ActivationRuleStatusPending {
			t.Fatalf("rule = %s, want = pending", got)
		}
		f.completeVIES()
		f.finalizeVIES(inv.ID)
		// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:565
		if got := f.ruleStatus(rule.ID, s); got != subscription.ActivationRuleStatusSatisfied {
			t.Errorf("rule = %s, want = satisfied", got)
		}
		// source: spec/services/invoices/finalize_pending_vies_invoice_service_spec.rb:571
		if got := f.subscriptionStatus(s); got != subscription.StatusActive {
			t.Errorf("subscription = %s, want = active", got)
		}
	})
}

// TestFinalizePendingVIESKeepsDeductions changes the customer's coupons
// between holding and finalizing an invoice: the finalization keeps the
// coupon credit applied when the invoice was held and computes only its
// taxes and totals.
//
// source: app/services/invoices/finalize_pending_vies_invoice_service.rb:24, app/services/invoices/compute_amounts_from_fees.rb:28-31
func TestFinalizePendingVIESKeepsDeductions(t *testing.T) {
	f := newFixture(t, billingDay)
	f.viesSetup(true, true)
	first := f.ApplyCoupon(f.FixedAmountCoupon(300), f.cust)
	inv := f.billOne(f.subscribe())
	if inv.TaxStatus != invoice.TaxStatusPending || inv.CouponsAmount != 300 {
		t.Fatalf("tax status, coupons = %s, %d, want = pending, 300", inv.TaxStatus, inv.CouponsAmount)
	}

	second := f.ApplyCoupon(f.FixedAmountCoupon(500), f.cust)
	f.taxCustomer("20")
	f.completeVIES()
	inv = f.finalizeVIES(inv.ID)
	if inv.Status != invoice.StatusFinalized || inv.CouponsAmount != 300 {
		t.Fatalf("status, coupons = %s, %d, want = finalized, 300", inv.Status, inv.CouponsAmount)
	}
	checkAmounts(t, inv, 1000, 140, 840)
	credits := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.Credit, error) {
		return r.Credits.ListByInvoice(ctx, f.OrgID(), inv.ID)
	})
	if len(credits) != 1 || credits[0].AppliedCouponID != first.ID || credits[0].Amount != 300 {
		t.Errorf("credits = %+v, want = the 300 credit of the first coupon", credits)
	}
	if a := f.appliedCoupon(first.ID); a.Status != coupon.StatusTerminated {
		t.Errorf("first coupon status = %s, want = terminated", a.Status)
	}
	if a := f.appliedCoupon(second.ID); a.Status != coupon.StatusActive {
		t.Errorf("second coupon status = %s, want = active", a.Status)
	}
}
