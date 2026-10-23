package storetest_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// Concurrent runs of one credit note's refund job record one refund: the
// provider sees one idempotency key (the note's ID), and the runs record
// its outcome under the note's lock.
//
// source: app/services/credit_notes/refunds/stripe_service.rb:105
func TestConcurrentRefund(t *testing.T) {
	h, _ := newHarness(t, billingtest.Date(2026, time.February, 1))
	c := h.Customer()
	inv, _ := h.CreditableInvoice(c, []int64{10, 10}, nil, nil)
	inv.PaymentStatus, inv.TotalPaidAmount = core.PaymentStatusSucceeded, inv.TotalAmount
	h.SaveInvoice(inv)
	h.StoredPayment(payment.InvoicePayable(inv.ID), c.ID, inv.TotalAmount, payment.StateSucceeded, core.IntegrationID{})
	cn := h.StoredCreditNote(inv, 0, func(cn *creditnote.CreditNote) {
		cn.CreditStatus, cn.BalanceAmount = "", 0
		cn.RefundAmount, cn.TotalAmount, cn.RefundStatus = 12, 12, core.RefundStatusPending
	})
	svc := creditnotes.New(h.Deps)
	got := make([]*payment.Refund, 4)
	errs := concurrently(len(got), func(i int) error {
		var err error
		got[i], err = svc.Refund(context.Background(), h.OrgID(), cn.ID)
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("run %d: got = %v, want = nil", i, err)
		}
	}
	stored := h.CreditNoteRefunds(cn.ID)
	if len(stored) != 1 {
		t.Fatalf("refunds: got = %d, want = 1", len(stored))
	}
	for i, r := range got {
		if r != nil && r.ID != stored[0].ID {
			t.Errorf("run %d refund: got = %s, want = %s", i, r.ID, stored[0].ID)
		}
	}
	for _, req := range h.Refunds.Requests {
		if req.IdempotencyKey != cn.ID.String() {
			t.Errorf("idempotency key: got = %q, want = %q", req.IdempotencyKey, cn.ID)
		}
	}
}
