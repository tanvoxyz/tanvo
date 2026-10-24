package payments_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func logActivities(h *billingtest.Harness) {
	h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
}

func TestActivityLogs(t *testing.T) {
	t.Run("manual payment", func(t *testing.T) {
		t.Log("source: spec/services/payments/manual_create_service_spec.rb:210")
		t.Log("source: spec/services/payment_receipts/create_service_spec.rb:103")
		f := newManualFixture(t)
		logActivities(f.h)
		p, err := f.pay("100")
		if err != nil {
			t.Fatalf("pay: %v", err)
		}
		rc := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*payment.Receipt, error) {
			return r.PaymentReceipts.GetByPayment(ctx, f.h.OrgID(), p.ID)
		})
		ext := f.cust.ExternalID
		f.h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityInvoicePaymentStatusUpdated, ResourceType: auditlog.ResourceInvoice, ResourceID: f.inv.ID.UUID(), ExternalCustomerID: ext},
			// A payment's log is on what it pays.
			billingtest.ActivityWant{Type: auditlog.ActivityPaymentRecorded, ResourceType: auditlog.ResourceInvoice, ResourceID: f.inv.ID.UUID(), ExternalCustomerID: ext},
			billingtest.ActivityWant{Type: auditlog.ActivityPaymentReceiptCreated, ResourceType: auditlog.ResourcePaymentReceipt, ResourceID: rc.ID.UUID(), ExternalCustomerID: ext},
		)
	})

	t.Run("a rejected manual payment logs nothing", func(t *testing.T) {
		f := newManualFixture(t)
		logActivities(f.h)
		if _, err := f.pay("100.01"); err == nil {
			t.Fatal("pay: error = nil, want an error")
		}
		f.h.WantActivities()
	})

	t.Run("invoice payment status update", func(t *testing.T) {
		t.Log("source: spec/services/invoices/update_service_spec.rb:313")
		f := newManualFixture(t)
		logActivities(f.h)
		st := core.PaymentStatusSucceeded
		if _, err := f.svc.UpdateInvoice(f.h.Ctx, f.h.OrgID(), f.inv.ID, payments.InvoiceUpdate{PaymentStatus: &st}); err != nil {
			t.Fatalf("UpdateInvoice: %v", err)
		}
		f.h.WantActivities(billingtest.ActivityWant{
			Type: auditlog.ActivityInvoicePaymentStatusUpdated, ResourceType: auditlog.ResourceInvoice, ResourceID: f.inv.ID.UUID(), ExternalCustomerID: f.cust.ExternalID,
		})
	})

	t.Run("payment request", func(t *testing.T) {
		t.Log("source: app/services/payment_requests/create_service.rb:41")
		f := newRequestFixture(t)
		logActivities(f.h)
		q, err := f.svc.CreateRequest(f.h.Ctx, f.input())
		if err != nil {
			t.Fatalf("CreateRequest: %v", err)
		}
		f.h.WantActivities(billingtest.ActivityWant{
			Type: auditlog.ActivityPaymentRequestCreated, ResourceType: auditlog.ResourcePaymentRequest, ResourceID: q.ID.UUID(), ExternalCustomerID: f.cust.ExternalID,
		})
	})
}
