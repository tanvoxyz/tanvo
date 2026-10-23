package payments_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

func TestCreateReceipt(t *testing.T) {
	cases := []struct {
		name, source string
		partner      bool
		state        payment.State
		want         bool
	}{
		{"partner customer", "spec/services/payment_receipts/create_service_spec.rb:28", true, payment.StateSucceeded, false},
		{"payment not succeeded", "spec/services/payment_receipts/create_service_spec.rb:66", false, payment.StatePending, false},
		{"payment succeeded", "spec/services/payment_receipts/create_service_spec.rb:82", false, payment.StateSucceeded, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Log("source: " + c.source)
			h, svc := newHarness(t)
			cust := h.Customer(func(cu *customer.Customer) {
				if c.partner {
					cu.AccountType = customer.AccountTypePartner
				}
			})
			inv := h.StoredInvoice(cust, finalized(10000, 0))
			p := h.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 10000, c.state, core.IntegrationID{})
			rc, err := svc.CreateReceipt(h.Ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			if got := rc != nil; got != c.want {
				t.Fatalf("receipt: got = %t, want = %t", got, c.want)
			}
			if got := hasWebhook(h, webhook.EventTypePaymentReceiptCreated); got != c.want {
				t.Errorf("payment_receipt.created webhook: got = %t, want = %t", got, c.want)
			}
			if !c.want {
				return
			}
			if want := payment.FormatReceiptNumber(cust.Slug, 1); rc.Number != want {
				t.Errorf("number: got = %s, want = %s", rc.Number, want)
			}
			if rc.BillingEntityID != inv.BillingEntityID || rc.PaymentID != p.ID {
				t.Errorf("receipt: got = %s %s, want = %s %s", rc.BillingEntityID, rc.PaymentID, inv.BillingEntityID, p.ID)
			}
			again, err := svc.CreateReceipt(h.Ctx, p)
			if err != nil || again.ID != rc.ID {
				t.Errorf("existing receipt: got = %v %v, want = %s", again, err, rc.ID)
			}
		})
	}
}

func TestReceiptNumbersPerCustomer(t *testing.T) {
	h, svc := newHarness(t)
	cust := h.Customer()
	var numbers []string
	for range 2 {
		inv := h.StoredInvoice(cust, finalized(10000, 0))
		p := h.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 10000, payment.StateSucceeded, core.IntegrationID{})
		rc, err := svc.CreateReceipt(h.Ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		numbers = append(numbers, rc.Number)
	}
	if want := payment.FormatReceiptNumber(cust.Slug, 2); numbers[1] != want {
		t.Errorf("second number: got = %s, want = %s", numbers[1], want)
	}
}

func TestGetAndListReceipts(t *testing.T) {
	h, svc := newHarness(t)
	cust := h.Customer()
	inv := h.StoredInvoice(cust, finalized(10000, 0))
	p := h.StoredPayment(payment.InvoicePayable(inv.ID), cust.ID, 10000, payment.StateSucceeded, core.IntegrationID{})
	rc, err := svc.CreateReceipt(h.Ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetReceipt(h.Ctx, h.OrgID(), rc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Payment.ID != p.ID || got.Payment.ReceiptID != rc.ID || len(got.Payment.InvoiceIDs) != 1 {
		t.Errorf("receipt payment: got = %+v, want = %s with the receipt", got.Payment, p.ID)
	}
	_, err = svc.GetReceipt(h.Ctx, h.OrgID(), core.NewID[core.PaymentReceiptID](h.IDs))
	wantNotFound(t, err, "payment_receipt")

	for _, f := range []payment.ReceiptFilter{{}, {InvoiceID: inv.ID}} {
		page, err := svc.ListReceipts(h.Ctx, h.OrgID(), payments.ReceiptListInput{Filter: f}, core.PageRequest{})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != rc.ID {
			t.Errorf("list %+v: got = %v %v, want = the receipt", f, page.Items, err)
		}
	}
	page, err := svc.ListReceipts(h.Ctx, h.OrgID(), payments.ReceiptListInput{
		Filter: payment.ReceiptFilter{InvoiceID: core.NewID[core.InvoiceID](h.IDs)},
	}, core.PageRequest{})
	if err != nil || len(page.Items) != 0 {
		t.Errorf("list of another invoice: got = %v %v, want = none", page.Items, err)
	}
	if _, ready, err := svc.DownloadReceipt(h.Ctx, h.OrgID(), rc.ID); err != nil || ready {
		t.Errorf("download: got = %t %v, want = not ready", ready, err)
	}
}
