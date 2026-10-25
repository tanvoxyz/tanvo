package docgen_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/docgen"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// TestInvoiceEmail covers the data of the invoice email.
func TestInvoiceEmail(t *testing.T) {
	// source: spec/mailers/invoice_mailer_spec.rb:46
	t.Run("renders a missing pdf", func(t *testing.T) {
		e := newEnv(t)
		inv := e.invoice()
		m, err := e.svc.InvoiceEmail(e.Ctx, e.OrgID(), inv.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n := e.render.n("invoice.pdf"); n != 1 {
			t.Errorf("pdf renders got = %d, want = 1", n)
		}
		if string(m.PDF) != "%PDF invoice.pdf" || m.FileURL != "https://files.test/"+string(m.Invoice.File) {
			t.Errorf("attachment got = %q %q, want = the rendered PDF and its link", m.PDF, m.FileURL)
		}
		if m.Customer.ID != e.cust.ID || m.BillingEntity.ID != e.BillingEntity.ID || m.Locale != "en" {
			t.Errorf("sender got = %s %s %q, want = the customer, its billing entity and en", m.Customer.ID, m.BillingEntity.ID, m.Locale)
		}
		if got := e.jobs(kernel.JobSendDocumentEmail); len(got) != 0 {
			t.Errorf("email jobs got = %d, want = 0 (the mailer does not notify)", len(got))
		}
	})

	// source: spec/mailers/invoice_mailer_spec.rb:36
	t.Run("pdfs disabled", func(t *testing.T) {
		e := newEnv(t, docgen.Options{DisablePDF: true})
		m, err := e.svc.InvoiceEmail(e.Ctx, e.OrgID(), e.invoice().ID)
		if err != nil {
			t.Fatal(err)
		}
		if m.PDF != nil || m.FileURL != "" {
			t.Errorf("attachment got = %q %q, want = none", m.PDF, m.FileURL)
		}
	})

	t.Run("draft is sent without file", func(t *testing.T) {
		e := newEnv(t)
		inv := e.invoice(func(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft })
		m, err := e.svc.InvoiceEmail(e.Ctx, e.OrgID(), inv.ID)
		if err != nil {
			t.Fatal(err)
		}
		if m.PDF != nil || m.FileURL != "" {
			t.Errorf("attachment got = %q %q, want = none", m.PDF, m.FileURL)
		}
	})

	t.Run("gone", func(t *testing.T) {
		e := newEnv(t)
		m, err := e.svc.InvoiceEmail(e.Ctx, e.OrgID(), core.NewID[core.InvoiceID](e.IDs))
		if err != nil || m != nil {
			t.Errorf("got = %v %v, want = nil nil", m, err)
		}
	})
}

// TestCreditNoteEmail covers the data of the credit note email.
func TestCreditNoteEmail(t *testing.T) {
	// source: spec/mailers/credit_note_mailer_spec.rb:34
	e := newEnv(t)
	cn := e.creditNote()
	m, err := e.svc.CreditNoteEmail(e.Ctx, e.OrgID(), cn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.render.n("credit_note.pdf"); n != 1 || string(m.PDF) != "%PDF credit_note.pdf" {
		t.Errorf("pdf got = %d renders %q, want = 1 render attached", n, m.PDF)
	}
	if m.Invoice.ID != cn.InvoiceID {
		t.Errorf("invoice got = %s, want = %s", m.Invoice.ID, cn.InvoiceID)
	}
}

// TestPaymentReceiptEmail covers the data of the payment receipt email.
func TestPaymentReceiptEmail(t *testing.T) {
	t.Run("invoice payment", func(t *testing.T) {
		e := newEnv(t)
		inv := e.invoice(func(inv *invoice.Invoice) { inv.TotalAmount = 3000 })
		rc := e.receipt(inv)
		m, err := e.svc.PaymentReceiptEmail(e.Ctx, e.OrgID(), rc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(m.PDF) != "%PDF receipt.pdf" {
			t.Errorf("receipt pdf got = %q, want = the rendered receipt", m.PDF)
		}
		if len(m.Invoices) != 1 || m.Invoices[0].Invoice.ID != inv.ID || m.Invoices[0].PDF != nil {
			t.Errorf("invoices got = %+v, want = the invoice without a file (not rendered here)", m.Invoices)
		}
		if n := e.render.n("invoice.pdf"); n != 0 {
			t.Errorf("invoice renders got = %d, want = 0", n)
		}
		if m.TotalDueAmount != inv.TotalDueAmount() {
			t.Errorf("total due got = %d, want = %d", m.TotalDueAmount, inv.TotalDueAmount())
		}
	})

	t.Run("payment request", func(t *testing.T) {
		e := newEnv(t)
		a := e.invoice(func(inv *invoice.Invoice) { inv.File = e.blobs.put(t, "a.pdf", docgen.ContentTypePDF) })
		b := e.invoice(func(inv *invoice.Invoice) { inv.File = e.blobs.put(t, "b.pdf", docgen.ContentTypePDF) })
		q := e.StoredPaymentRequest(e.cust.ID, core.PaymentStatusSucceeded, a, b)
		p := e.StoredPayment(payment.RequestPayable(q.ID), e.cust.ID, q.Amount, payment.StateSucceeded, core.IntegrationID{})
		rc := &payment.Receipt{
			ID: core.NewID[core.PaymentReceiptID](e.IDs), OrganizationID: e.OrgID(), PaymentID: p.ID,
			BillingEntityID: e.BillingEntity.ID, Number: "ORG-RCPT-000001", CreatedAt: e.Now(), UpdatedAt: e.Now(),
		}
		e.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentReceipts.Create(ctx, rc) })
		m, err := e.svc.PaymentReceiptEmail(e.Ctx, e.OrgID(), rc.ID)
		if err != nil {
			t.Fatal(err)
		}
		var got []core.InvoiceID
		for _, i := range m.Invoices {
			got = append(got, i.Invoice.ID)
			if string(i.PDF) != "old" || i.FileURL == "" {
				t.Errorf("invoice %s attachment got = %q %q, want = its stored file", i.Invoice.ID, i.PDF, i.FileURL)
			}
		}
		if want := []core.InvoiceID{a.ID, b.ID}; !slices.Equal(got, want) {
			t.Errorf("invoices got = %v, want = %v", got, want)
		}
		if m.TotalDueAmount != 0 {
			t.Errorf("total due got = %d, want = 0", m.TotalDueAmount)
		}
	})
}

// TestInvoiceLink covers the invoice links of the payment request email.
func TestInvoiceLink(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice()
	got, err := e.svc.InvoiceLink(e.Ctx, e.OrgID(), inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://files.test/" + string(e.reloadInvoice(inv.ID).File); got != want {
		t.Errorf("link got = %q, want = %q", got, want)
	}
	draft := e.invoice(func(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft })
	if got, err := e.svc.InvoiceLink(e.Ctx, e.OrgID(), draft.ID); err != nil || got != "" {
		t.Errorf("draft link got = %q %v, want = none", got, err)
	}
}

// TestResend covers the documents the resend RPCs look up.
func TestResend(t *testing.T) {
	e := newEnv(t)
	inv := e.invoice(func(inv *invoice.Invoice) { inv.FeesAmount = 1250 })
	got, err := e.svc.ResendInvoice(e.Ctx, e.OrgID(), inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number != inv.Number || !got.Finalized || got.FeesAmount.String() != "12.50" || got.Customer.ID != e.cust.ID {
		t.Errorf("invoice target got = %+v, want = finalized %s with 12.50 of fees", got, inv.Number)
	}
	// source: app/controllers/api/v1/invoices_controller.rb:180-182
	hidden := e.invoice(func(inv *invoice.Invoice) { inv.Status = invoice.StatusGenerating })
	if _, err := e.svc.ResendInvoice(e.Ctx, e.OrgID(), hidden.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("invisible invoice got = %v, want = not found", err)
	}
	cn := e.creditNote()
	if got, err := e.svc.ResendCreditNote(e.Ctx, e.OrgID(), cn.ID); err != nil || got.Number != cn.Number || !got.Finalized {
		t.Errorf("credit note target got = %+v %v, want = finalized %s", got, err, cn.Number)
	}
	rc := e.receipt(inv)
	if got, err := e.svc.ResendPaymentReceipt(e.Ctx, e.OrgID(), rc.ID); err != nil || got.Number != rc.Number || got.Customer.ID != e.cust.ID {
		t.Errorf("receipt target got = %+v %v, want = %s of the payment's customer", got, err, rc.Number)
	}
	a := kernel.DocumentEmailArgs{Type: kernel.DocumentCreditNote, ID: cn.ID.UUID(), To: []string{"a@x.test"}, Bcc: []string{"b@x.test"}, Resend: true}
	if err := e.svc.EnqueueEmail(e.Ctx, e.OrgID(), a); err != nil {
		t.Fatal(err)
	}
	jobs := e.jobs(kernel.JobSendDocumentEmail)
	if len(jobs) != 1 {
		t.Fatalf("email jobs got = %d, want = 1", len(jobs))
	}
	var args kernel.DocumentEmailArgs
	if err := json.Unmarshal(jobs[0].Args, &args); err != nil {
		t.Fatal(err)
	}
	if args.Type != a.Type || args.ID != a.ID || !slices.Equal(args.To, a.To) || !slices.Equal(args.Bcc, a.Bcc) || !args.Resend {
		t.Errorf("email args got = %+v, want = %+v", args, a)
	}
}
