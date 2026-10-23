package payments_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// manualFixture is a finalized invoice of 100.00 to pay manually.
type manualFixture struct {
	h    *billingtest.Harness
	svc  *payments.Service
	cust *customer.Customer
	inv  *invoice.Invoice
}

func newManualFixture(t *testing.T, opts ...func(*invoice.Invoice)) *manualFixture {
	t.Helper()
	h, svc := newHarness(t)
	f := &manualFixture{h: h, svc: svc, cust: h.Customer()}
	f.inv = h.StoredInvoice(f.cust, append([]func(*invoice.Invoice){finalized(10000, 0)}, opts...)...)
	return f
}

func (f *manualFixture) pay(amount string) (*payment.Payment, error) {
	paidAt := billingtest.Date(2025, 3, 10, 12)
	return f.svc.CreateManual(f.h.Ctx, payments.ManualInput{
		OrganizationID: f.h.OrgID(), InvoiceID: f.inv.ID, Amount: money.MustParse(amount),
		Reference: "ref1", PaidAt: &paidAt,
	})
}

func TestCreateManualRejects(t *testing.T) {
	t.Run("missing invoice", func(t *testing.T) {
		t.Log("source: spec/services/payments/manual_create_service_spec.rb:30")
		f := newManualFixture(t)
		f.inv.ID = core.NewID[core.InvoiceID](f.h.IDs)
		_, err := f.pay("100")
		wantNotFound(t, err, "invoice")
	})
	t.Run("draft invoice", func(t *testing.T) {
		t.Log("source: spec/services/payments/manual_create_service_spec.rb:41")
		f := newManualFixture(t, func(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft })
		if _, err := f.pay("100"); !errors.Is(err, core.ErrNotAllowed) {
			t.Fatalf("error: got = %v, want = %v", err, core.ErrNotAllowed)
		}
	})
	t.Run("payment request succeeded", func(t *testing.T) {
		t.Log("source: spec/services/payments/manual_create_service_spec.rb:56")
		f := newManualFixture(t)
		f.h.StoredPaymentRequest(f.cust.ID, core.PaymentStatusSucceeded, f.inv)
		_, err := f.pay("100")
		wantRule(t, err, payment.CodePaymentRequestSucceeded)
	})
	t.Run("amount greater than due", func(t *testing.T) {
		t.Log("source: spec/services/payments/manual_create_service_spec.rb:67")
		f := newManualFixture(t)
		_, err := f.pay("100.01")
		wantFields(t, err, "amount:"+payment.CodeGreaterThan)
	})
	t.Run("amount not positive", func(t *testing.T) {
		t.Log("source: spec/services/payments/manual_create_service_spec.rb:78")
		f := newManualFixture(t)
		_, err := f.pay("0")
		wantFields(t, err, "amount:"+core.CodeInvalid)
	})
	t.Run("amount below the currency unit", func(t *testing.T) {
		f := newManualFixture(t)
		_, err := f.pay("1.001")
		wantFields(t, err, "amount:"+core.CodeInvalid)
	})
}

func TestCreateManualPartial(t *testing.T) {
	t.Log("source: spec/services/payments/manual_create_service_spec.rb:138")
	f := newManualFixture(t)
	p, err := f.pay("20")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != payment.KindManual || p.Amount != 2000 || p.Status != payment.StateSucceeded {
		t.Errorf("payment: got = %s %d %s, want = manual 2000 succeeded", p.Kind, p.Amount, p.Status)
	}
	if !p.PaidAt.Equal(billingtest.Date(2025, 3, 10, 12)) {
		t.Errorf("paid at: got = %v, want = 2025-03-10", p.PaidAt)
	}
	if !p.CreatedAt.Equal(*p.PaidAt) {
		t.Errorf("created at: got = %v, want = the paid at %v (manual_create_service.rb:34)", p.CreatedAt, p.PaidAt)
	}
	inv := f.h.Invoice(f.inv.ID)
	if inv.TotalPaidAmount != 2000 || inv.PaymentStatus == core.PaymentStatusSucceeded {
		t.Errorf("invoice: got = %d %s, want = 2000 not succeeded", inv.TotalPaidAmount, inv.PaymentStatus)
	}
	if hasWebhook(f.h, webhook.EventTypeInvoicePaymentStatusUpdated) {
		t.Errorf("webhooks: got = %v, want = no invoice.payment_status_updated", f.h.Webhooks())
	}
	if receiptOf(f.h, p.ID) == nil {
		t.Error("receipt: got = none, want = one")
	}
}

func TestCreateManualFull(t *testing.T) {
	t.Log("source: spec/services/payments/manual_create_service_spec.rb:197")
	f := newManualFixture(t)
	fee := f.h.StoredFee(f.inv, 10000)
	p, err := f.pay("100")
	if err != nil {
		t.Fatal(err)
	}
	inv := f.h.Invoice(f.inv.ID)
	if inv.TotalPaidAmount != 10000 || inv.PaymentStatus != core.PaymentStatusSucceeded {
		t.Errorf("invoice: got = %d %s, want = 10000 succeeded", inv.TotalPaidAmount, inv.PaymentStatus)
	}
	if !hasWebhook(f.h, webhook.EventTypeInvoicePaymentStatusUpdated) {
		t.Errorf("webhooks: got = %v, want = invoice.payment_status_updated", f.h.Webhooks())
	}
	for _, got := range f.h.Fees(f.inv.ID) {
		if got.ID == fee.ID && got.PaymentStatus != invoice.FeePaymentStatusSucceeded {
			t.Errorf("fee payment status: got = %s, want = succeeded", got.PaymentStatus)
		}
	}
	if receiptOf(f.h, p.ID) == nil {
		t.Error("receipt: got = none, want = one")
	}
}

func TestCreateManualOverdueResetsDunning(t *testing.T) {
	f := newManualFixture(t, func(inv *invoice.Invoice) { inv.PaymentOverdue = true })
	q := f.h.StoredPaymentRequest(f.cust.ID, core.PaymentStatusFailed, f.inv)
	q.DunningCampaignID = core.NewID[core.DunningCampaignID](f.h.IDs)
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentRequests.Update(ctx, q) })
	f.h.UpdateCustomer(f.cust, func(c *customer.Customer) {
		c.DunningCampaignAttempts = map[string]int{"USD": 2}
		at := now
		c.LastDunningCampaignAttemptAt = &at
	})
	if _, err := f.pay("100"); err != nil {
		t.Fatal(err)
	}
	if inv := f.h.Invoice(f.inv.ID); inv.PaymentOverdue {
		t.Error("payment overdue: got = true, want = false")
	}
	c := f.h.ReloadCustomer(f.cust.ID)
	if c.DunningCampaignAttempts["USD"] != 0 || c.LastDunningCampaignAttemptAt != nil {
		t.Errorf("dunning: got = %v %v, want = 0 nil", c.DunningCampaignAttempts, c.LastDunningCampaignAttemptAt)
	}
}

func TestCreateManualOffsets(t *testing.T) {
	cases := []struct {
		name, source string
		paid         int64
		offset       int64
		amount       string
		wantPaid     int64
		wantStatus   bool
	}{
		{"covers the rest after the offset", "spec/services/payments/manual_create_service_spec.rb:252", 0, 3000, "70", 7000, true},
		{"less than the rest after the offset", "spec/services/payments/manual_create_service_spec.rb:278", 0, 3000, "50", 5000, false},
		{"final payment after a partial payment and an offset", "spec/services/payments/manual_create_service_spec.rb:338", 3000, 2000, "50", 8000, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Log("source: " + c.source)
			f := newManualFixture(t, finalized(10000, c.paid))
			if c.paid > 0 {
				f.h.StoredPayment(payment.InvoicePayable(f.inv.ID), f.cust.ID, c.paid, payment.StateSucceeded, core.IntegrationID{})
			}
			offset(f.h, f.inv, c.offset)
			if _, err := f.pay(c.amount); err != nil {
				t.Fatal(err)
			}
			inv := f.h.Invoice(f.inv.ID)
			if inv.TotalPaidAmount != c.wantPaid {
				t.Errorf("total paid: got = %d, want = %d", inv.TotalPaidAmount, c.wantPaid)
			}
			if got := inv.PaymentStatus == core.PaymentStatusSucceeded; got != c.wantStatus {
				t.Errorf("paid: got = %t, want = %t", got, c.wantStatus)
			}
			if got := hasWebhook(f.h, webhook.EventTypeInvoicePaymentStatusUpdated); got != c.wantStatus {
				t.Errorf("payment status webhook: got = %t, want = %t", got, c.wantStatus)
			}
		})
	}
}
