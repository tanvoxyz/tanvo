package docgen_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/docgen"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func wantRule(t *testing.T, err error, code string) {
	t.Helper()
	var re *core.RuleError
	if !errors.As(err, &re) || re.Code != code {
		t.Errorf("error got = %v, want = rule %s", err, code)
	}
}

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	if !errors.Is(err, core.ErrNotFound) || !strings.Contains(err.Error(), resource+"_not_found") {
		t.Errorf("error got = %v, want = %s_not_found", err, resource)
	}
}

func (e *env) wantWebhooks(want ...webhook.EventType) {
	e.T.Helper()
	if got := e.Webhooks(); !slices.Equal(got, want) {
		e.T.Errorf("webhooks got = %v, want = %v", got, want)
	}
}

func (e *env) wantStored(ref core.BlobRef, contentType string) {
	e.T.Helper()
	if ref.IsZero() || e.blobs.types[ref] != contentType {
		e.T.Errorf("file got = %q (%s), want = a stored %s", ref, e.blobs.types[ref], contentType)
	}
}

// TestGenerateInvoice ports the PDF service and documents job of invoices.
func TestGenerateInvoice(t *testing.T) {
	// source: spec/services/invoices/generate_pdf_service_spec.rb:24
	// source: spec/jobs/invoices/generate_documents_job_spec.rb:26
	t.Run("generates the pdf", func(t *testing.T) {
		e := newEnv(t)
		inv := e.invoice()
		if err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID}); err != nil {
			t.Fatal(err)
		}
		got := e.reloadInvoice(inv.ID)
		e.wantStored(got.File, docgen.ContentTypePDF)
		if !strings.HasSuffix(string(got.File), inv.Number+".pdf") {
			t.Errorf("file name got = %q, want = %s.pdf", got.File, inv.Number)
		}
		if !got.XMLFile.IsZero() {
			t.Errorf("xml got = %q, want = none without e-invoicing", got.XMLFile)
		}
		if e.render.invoice == nil || e.render.invoice.Invoice.ID != inv.ID || e.render.invoice.Customer.ID != e.cust.ID {
			t.Errorf("rendered data got = %+v, want = the invoice and its customer", e.render.invoice)
		}
		// source: spec/services/invoices/generate_pdf_service_spec.rb:30
		// source: spec/services/invoices/generate_pdf_service_spec.rb:138
		e.wantWebhooks(webhook.EventTypeInvoiceGenerated)
		// source: spec/services/invoices/generate_pdf_service_spec.rb:34
		e.WantActivities(billingtest.ActivityWant{
			Type: auditlog.ActivityInvoiceGenerated, ResourceType: auditlog.ResourceInvoice, ResourceID: inv.ID.UUID(),
			ExternalCustomerID: e.cust.ExternalID,
		})
	})

	// source: spec/services/invoices/generate_pdf_service_spec.rb:44
	// source: spec/services/invoices/generate_xml_service_spec.rb:73
	t.Run("not found", func(t *testing.T) {
		e := newEnv(t)
		err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: core.NewID[core.InvoiceID](e.IDs)})
		wantNotFound(t, err, "invoice")
	})

	// source: spec/services/invoices/generate_pdf_service_spec.rb:55
	// source: spec/services/invoices/generate_xml_service_spec.rb:84
	t.Run("draft", func(t *testing.T) {
		e := newEnv(t)
		e.einvoicing("FR")
		inv := e.invoice(func(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft })
		wantRule(t, e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID}), docgen.CodeIsDraft)
		if n := e.render.n("invoice.pdf") + e.render.n("invoice.xml"); n != 0 {
			t.Errorf("renders got = %d, want = 0", n)
		}
	})

	// source: spec/services/invoices/generate_pdf_service_spec.rb:64
	t.Run("already generated", func(t *testing.T) {
		e := newEnv(t)
		old := e.blobs.put(t, "invoice.pdf", docgen.ContentTypePDF)
		inv := e.invoice(func(inv *invoice.Invoice) { inv.File = old })
		if err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID}); err != nil {
			t.Fatal(err)
		}
		if n := e.render.n("invoice.pdf"); n != 0 {
			t.Errorf("pdf renders got = %d, want = 0", n)
		}
		if got := e.reloadInvoice(inv.ID).File; got != old {
			t.Errorf("file got = %q, want = %q", got, old)
		}
		e.wantWebhooks()
	})

	// source: spec/services/invoices/generate_pdf_service_spec.rb:82
	t.Run("deleted billable metric", func(t *testing.T) {
		e := newEnv(t)
		m := e.Metric("api_calls", catalog.AggregationTypeCount, "")
		p := e.Plan(period.Monthly, 100)
		ch := e.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"10"}`)
		s := e.Subscription(e.cust, p)
		inv := e.invoice()
		e.StoredFee(inv, 10, billingtest.ChargeFee(s, ch.ID))
		e.Do(func(ctx context.Context, r port.Repos) error {
			if err := r.Charges.Discard(ctx, e.OrgID(), ch.ID, e.Now()); err != nil {
				return err
			}
			return r.Metrics.Discard(ctx, e.OrgID(), m.ID, e.Now())
		})
		if err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID}); err != nil {
			t.Fatal(err)
		}
		e.wantStored(e.reloadInvoice(inv.ID).File, docgen.ContentTypePDF)
		if got, ok := e.render.invoice.Catalog.Metrics[m.ID]; !ok || got.DeletedAt == nil {
			t.Errorf("metric got = %+v (%v), want = the discarded metric", got, ok)
		}
	})

	// source: spec/services/invoices/generate_pdf_service_spec.rb:143
	t.Run("force", func(t *testing.T) {
		e := newEnv(t)
		e.einvoicing("FR")
		oldPDF := e.blobs.put(t, "invoice.pdf", docgen.ContentTypePDF)
		oldXML := e.blobs.put(t, "invoice.xml", docgen.ContentTypeXML)
		inv := e.invoice(func(inv *invoice.Invoice) { inv.File, inv.XMLFile = oldPDF, oldXML })
		if err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID, Force: true}); err != nil {
			t.Fatal(err)
		}
		got := e.reloadInvoice(inv.ID)
		if got.File == oldPDF || got.XMLFile == oldXML {
			t.Errorf("files got = %q %q, want = new files", got.File, got.XMLFile)
		}
		e.wantStored(got.File, docgen.ContentTypePDF)
		e.wantStored(got.XMLFile, docgen.ContentTypeXML)
		if want := []core.BlobRef{oldXML, oldPDF}; !slices.Equal(e.blobs.deleted, want) {
			t.Errorf("deleted got = %v, want = %v", e.blobs.deleted, want)
		}
		e.wantWebhooks(webhook.EventTypeInvoiceGenerated)
	})

	// source: spec/services/invoices/generate_pdf_service_spec.rb:204
	t.Run("pdf failure", func(t *testing.T) {
		e := newEnv(t)
		e.einvoicing("FR")
		e.render.failPDF = errAttach
		inv := e.invoice()
		err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID})
		if !errors.Is(err, errAttach) {
			t.Fatalf("error got = %v, want = %v", err, errAttach)
		}
		got := e.reloadInvoice(inv.ID)
		if !got.File.IsZero() || !got.XMLFile.IsZero() {
			t.Errorf("files got = %q %q, want = none", got.File, got.XMLFile)
		}
		if len(e.blobs.files) != 0 {
			t.Errorf("stored files got = %d, want = 0 (the XML is discarded)", len(e.blobs.files))
		}
		e.wantWebhooks()
	})

	// source: spec/jobs/invoices/generate_documents_job_spec.rb:37
	// source: spec/jobs/invoices/notify_job_spec.rb:10
	// source: spec/jobs/invoices/generate_documents_job_spec.rb:45
	for _, notify := range []bool{true, false} {
		t.Run("notify "+map[bool]string{true: "true", false: "false"}[notify], func(t *testing.T) {
			e := newEnv(t)
			inv := e.invoice()
			if err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID, Notify: notify}); err != nil {
				t.Fatal(err)
			}
			jobs := e.jobs(kernel.JobSendDocumentEmail)
			if !notify {
				if len(jobs) != 0 {
					t.Errorf("email jobs got = %d, want = 0", len(jobs))
				}
				return
			}
			if len(jobs) != 1 {
				t.Fatalf("email jobs got = %d, want = 1", len(jobs))
			}
			var a kernel.DocumentEmailArgs
			if err := json.Unmarshal(jobs[0].Args, &a); err != nil {
				t.Fatal(err)
			}
			if a.Type != kernel.DocumentInvoice || a.ID != inv.ID.UUID() || a.Resend {
				t.Errorf("email args got = %+v, want = invoice %s", a, inv.ID)
			}
		})
	}
}

// TestGenerateInvoiceXML ports the XML service of invoices.
func TestGenerateInvoiceXML(t *testing.T) {
	// source: spec/services/invoices/generate_xml_service_spec.rb:37
	// source: spec/jobs/invoices/generate_documents_job_spec.rb:31
	t.Run("generates the xml", func(t *testing.T) {
		e := newEnv(t)
		e.einvoicing("FR")
		inv := e.invoice()
		if err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID}); err != nil {
			t.Fatal(err)
		}
		got := e.reloadInvoice(inv.ID)
		e.wantStored(got.XMLFile, docgen.ContentTypeXML)
		e.wantStored(got.File, docgen.ContentTypePDF)
	})

	for _, c := range []struct {
		name    string
		country string
		einv    bool
		hasXML  bool
	}{
		// source: spec/services/invoices/generate_xml_service_spec.rb:23
		// source: spec/services/invoices/generate_xml_service_spec.rb:96
		{name: "already generated", country: "FR", einv: true, hasXML: true},
		// source: spec/services/invoices/generate_xml_service_spec.rb:108
		{name: "country not allowed", country: "BR", einv: true},
		// source: spec/services/invoices/generate_xml_service_spec.rb:117
		{name: "einvoicing disabled", country: "FR"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.einv {
				e.einvoicing(c.country)
			}
			var old core.BlobRef
			if c.hasXML {
				old = e.blobs.put(t, "invoice.xml", docgen.ContentTypeXML)
			}
			inv := e.invoice(func(inv *invoice.Invoice) { inv.XMLFile = old })
			if err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID}); err != nil {
				t.Fatal(err)
			}
			if n := e.render.n("invoice.xml"); n != 0 {
				t.Errorf("xml renders got = %d, want = 0", n)
			}
			if got := e.reloadInvoice(inv.ID).XMLFile; got != old {
				t.Errorf("xml got = %q, want = %q", got, old)
			}
		})
	}
}

// TestGenerateWithoutRenderer covers the configurations that render
// nothing.
func TestGenerateWithoutRenderer(t *testing.T) {
	t.Run("pdf disabled", func(t *testing.T) {
		e := newEnv(t, docgen.Options{DisablePDF: true})
		e.einvoicing("FR")
		inv := e.invoice()
		if err := e.svc.GenerateInvoice(e.Ctx, e.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID}); err != nil {
			t.Fatal(err)
		}
		got := e.reloadInvoice(inv.ID)
		if !got.File.IsZero() {
			t.Errorf("pdf got = %q, want = none", got.File)
		}
		e.wantStored(got.XMLFile, docgen.ContentTypeXML)
		e.wantWebhooks()
	})
	t.Run("no blob store", func(t *testing.T) {
		h := billingtest.New(t, billingtest.Date(2025, 3, 10))
		svc, err := docgen.New(h.Deps, nil, nil, docgen.Options{})
		if err != nil {
			t.Fatal(err)
		}
		c := h.Customer()
		inv := h.StoredInvoice(c, finalized)
		if err := svc.GenerateInvoice(h.Ctx, h.OrgID(), kernel.InvoiceArgs{InvoiceID: inv.ID, Notify: true}); err != nil {
			t.Fatal(err)
		}
		if got := h.Invoice(inv.ID); !got.File.IsZero() || !got.XMLFile.IsZero() {
			t.Errorf("files got = %q %q, want = none", got.File, got.XMLFile)
		}
		if got := h.JobKinds(); !slices.Equal(got, []string{kernel.JobSendDocumentEmail}) {
			t.Errorf("jobs got = %v, want = the email only", got)
		}
		if svc.PDFsEnabled() {
			t.Errorf("PDFsEnabled got = true, want = false")
		}
	})
}

// TestGenerateCreditNote ports the PDF and XML services and the documents
// job of credit notes.
func TestGenerateCreditNote(t *testing.T) {
	// source: spec/services/credit_notes/generate_pdf_service_spec.rb:24
	// source: spec/services/credit_notes/generate_xml_service_spec.rb:39
	// source: spec/jobs/credit_notes/generate_documents_job_spec.rb:25
	t.Run("generates the documents", func(t *testing.T) {
		e := newEnv(t)
		e.einvoicing("FR")
		cn := e.creditNote()
		if err := e.svc.GenerateCreditNote(e.Ctx, e.OrgID(), kernel.CreditNoteArgs{CreditNoteID: cn.ID}); err != nil {
			t.Fatal(err)
		}
		got := e.ReloadCreditNote(cn.ID)
		e.wantStored(got.File, docgen.ContentTypePDF)
		e.wantStored(got.XMLFile, docgen.ContentTypeXML)
		if d := e.render.creditNote; d == nil || d.Invoice.ID != cn.InvoiceID {
			t.Errorf("rendered data got = %+v, want = the credit note with its invoice", d)
		}
		// source: spec/services/credit_notes/generate_pdf_service_spec.rb:172
		e.wantWebhooks(webhook.EventTypeCreditNoteGenerated)
		// source: spec/services/credit_notes/generate_pdf_service_spec.rb:36
		e.WantActivities(billingtest.ActivityWant{
			Type: auditlog.ActivityCreditNoteGenerated, ResourceType: auditlog.ResourceCreditNote, ResourceID: cn.ID.UUID(),
			ExternalCustomerID: e.cust.ExternalID,
		})
	})

	// source: spec/services/credit_notes/generate_pdf_service_spec.rb:53
	t.Run("preferred locale", func(t *testing.T) {
		e := newEnv(t)
		e.UpdateCustomer(e.cust, func(c *customer.Customer) { c.DocumentLocale = new("fr") })
		cn := e.creditNote()
		if err := e.svc.GenerateCreditNote(e.Ctx, e.OrgID(), kernel.CreditNoteArgs{CreditNoteID: cn.ID}); err != nil {
			t.Fatal(err)
		}
		if got := e.render.creditNote.Locale; got != "fr" {
			t.Errorf("locale got = %q, want = fr", got)
		}
	})

	// source: spec/services/credit_notes/generate_pdf_service_spec.rb:131
	// source: spec/services/credit_notes/generate_xml_service_spec.rb:75
	t.Run("not found", func(t *testing.T) {
		e := newEnv(t)
		err := e.svc.GenerateCreditNote(e.Ctx, e.OrgID(), kernel.CreditNoteArgs{CreditNoteID: core.NewID[core.CreditNoteID](e.IDs)})
		wantNotFound(t, err, "credit_note")
	})

	// source: spec/services/credit_notes/generate_pdf_service_spec.rb:143
	// source: spec/services/credit_notes/generate_xml_service_spec.rb:86
	t.Run("draft", func(t *testing.T) {
		e := newEnv(t)
		cn := e.creditNote(func(cn *creditnote.CreditNote) { cn.Status = creditnote.StatusDraft })
		err := e.svc.GenerateCreditNote(e.Ctx, e.OrgID(), kernel.CreditNoteArgs{CreditNoteID: cn.ID})
		wantNotFound(t, err, "credit_note")
	})

	// source: spec/services/credit_notes/generate_pdf_service_spec.rb:154
	// source: spec/services/credit_notes/generate_xml_service_spec.rb:98
	t.Run("already generated", func(t *testing.T) {
		e := newEnv(t)
		e.einvoicing("FR")
		pdf := e.blobs.put(t, "cn.pdf", docgen.ContentTypePDF)
		xml := e.blobs.put(t, "cn.xml", docgen.ContentTypeXML)
		cn := e.creditNote(func(cn *creditnote.CreditNote) { cn.File, cn.XMLFile = pdf, xml })
		if err := e.svc.GenerateCreditNote(e.Ctx, e.OrgID(), kernel.CreditNoteArgs{CreditNoteID: cn.ID}); err != nil {
			t.Fatal(err)
		}
		if n := e.render.n("credit_note.pdf") + e.render.n("credit_note.xml"); n != 0 {
			t.Errorf("renders got = %d, want = 0", n)
		}
		e.wantWebhooks()
	})

	for _, c := range []struct {
		name    string
		country string
		einv    bool
	}{
		// source: spec/services/credit_notes/generate_xml_service_spec.rb:25
		// source: spec/services/credit_notes/generate_xml_service_spec.rb:110
		{name: "country not allowed", country: "BR", einv: true},
		// source: spec/services/credit_notes/generate_xml_service_spec.rb:119
		{name: "einvoicing disabled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.einv {
				e.einvoicing(c.country)
			}
			cn := e.creditNote()
			if err := e.svc.GenerateCreditNote(e.Ctx, e.OrgID(), kernel.CreditNoteArgs{CreditNoteID: cn.ID}); err != nil {
				t.Fatal(err)
			}
			if n := e.render.n("credit_note.xml"); n != 0 {
				t.Errorf("xml renders got = %d, want = 0", n)
			}
			if got := e.ReloadCreditNote(cn.ID).XMLFile; !got.IsZero() {
				t.Errorf("xml got = %q, want = none", got)
			}
		})
	}

	// source: spec/services/credit_notes/generate_pdf_service_spec.rb:182
	t.Run("force", func(t *testing.T) {
		e := newEnv(t)
		old := e.blobs.put(t, "cn.pdf", docgen.ContentTypePDF)
		cn := e.creditNote(func(cn *creditnote.CreditNote) { cn.File = old })
		if err := e.svc.GenerateCreditNote(e.Ctx, e.OrgID(), kernel.CreditNoteArgs{CreditNoteID: cn.ID, Force: true}); err != nil {
			t.Fatal(err)
		}
		if got := e.ReloadCreditNote(cn.ID).File; got == old {
			t.Errorf("file got = %q, want = a new file", got)
		}
		e.wantWebhooks(webhook.EventTypeCreditNoteGenerated)
	})

	// source: spec/services/credit_notes/generate_pdf_service_spec.rb:110
	t.Run("pdf failure", func(t *testing.T) {
		e := newEnv(t)
		e.render.failPDF = errAttach
		cn := e.creditNote()
		err := e.svc.GenerateCreditNote(e.Ctx, e.OrgID(), kernel.CreditNoteArgs{CreditNoteID: cn.ID})
		if !errors.Is(err, errAttach) {
			t.Errorf("error got = %v, want = %v", err, errAttach)
		}
		if got := e.ReloadCreditNote(cn.ID).File; !got.IsZero() {
			t.Errorf("file got = %q, want = none", got)
		}
	})
}

// TestGeneratePaymentReceipt ports the PDF and XML services and the
// documents job of payment receipts.
func TestGeneratePaymentReceipt(t *testing.T) {
	// source: spec/services/payment_receipts/generate_pdf_service_spec.rb:28
	// source: spec/jobs/payment_receipts/generate_documents_job_spec.rb:26
	t.Run("generates the pdf", func(t *testing.T) {
		e := newEnv(t)
		logo := e.blobs.put(t, "logo.png", "image/png")
		e.blobs.files[logo] = []byte("\x89PNG\r\n\x1a\n")
		e.UpdateBillingEntity(func(be *organization.BillingEntity) { be.Logo = logo })
		inv := e.invoice()
		rc := e.receipt(inv)
		if err := e.svc.GeneratePaymentReceipt(e.Ctx, e.OrgID(), kernel.PaymentReceiptArgs{PaymentReceiptID: rc.ID}); err != nil {
			t.Fatal(err)
		}
		got := e.reloadReceipt(rc.ID)
		e.wantStored(got.File, docgen.ContentTypePDF)
		d := e.render.receipt
		if d == nil || d.Invoice == nil || d.Invoice.Invoice.ID != inv.ID {
			t.Fatalf("rendered data got = %+v, want = the receipt of the invoice", d)
		}
		if d.Logo == nil || d.Logo.ContentType != "image/png" {
			t.Errorf("logo got = %+v, want = the billing entity's PNG", d.Logo)
		}
		// source: spec/services/payment_receipts/generate_pdf_service_spec.rb:34
		// source: spec/services/payment_receipts/generate_pdf_service_spec.rb:89
		e.wantWebhooks(webhook.EventTypePaymentReceiptGenerated)
		// source: spec/services/payment_receipts/generate_pdf_service_spec.rb:38
		e.WantActivities(billingtest.ActivityWant{
			Type: auditlog.ActivityPaymentReceiptGenerated, ResourceType: auditlog.ResourcePaymentReceipt, ResourceID: rc.ID.UUID(),
			ExternalCustomerID: e.cust.ExternalID,
		})
	})

	// source: spec/services/payment_receipts/generate_pdf_service_spec.rb:44
	// source: spec/services/payment_receipts/generate_xml_service_spec.rb:84
	t.Run("not found", func(t *testing.T) {
		e := newEnv(t)
		err := e.svc.GeneratePaymentReceipt(e.Ctx, e.OrgID(), kernel.PaymentReceiptArgs{PaymentReceiptID: core.NewID[core.PaymentReceiptID](e.IDs)})
		wantNotFound(t, err, "payment_receipt")
	})

	// source: spec/services/payment_receipts/generate_pdf_service_spec.rb:55
	t.Run("progressive billing invoice", func(t *testing.T) {
		e := newEnv(t)
		rc := e.receipt(e.invoice(func(inv *invoice.Invoice) { inv.Type = invoice.TypeProgressiveBilling }))
		if err := e.svc.GeneratePaymentReceipt(e.Ctx, e.OrgID(), kernel.PaymentReceiptArgs{PaymentReceiptID: rc.ID}); err != nil {
			t.Fatal(err)
		}
		e.wantStored(e.reloadReceipt(rc.ID).File, docgen.ContentTypePDF)
	})

	// source: spec/services/payment_receipts/generate_pdf_service_spec.rb:67
	// source: spec/services/payment_receipts/generate_xml_service_spec.rb:95
	t.Run("already generated", func(t *testing.T) {
		e := newEnv(t)
		e.einvoicing("FR")
		pdf := e.blobs.put(t, "receipt.pdf", docgen.ContentTypePDF)
		xml := e.blobs.put(t, "receipt.xml", docgen.ContentTypeXML)
		rc := e.receipt(e.invoice(), func(rc *payment.Receipt) { rc.File, rc.XMLFile = pdf, xml })
		if err := e.svc.GeneratePaymentReceipt(e.Ctx, e.OrgID(), kernel.PaymentReceiptArgs{PaymentReceiptID: rc.ID}); err != nil {
			t.Fatal(err)
		}
		if n := e.render.n("receipt.pdf") + e.render.n("receipt.xml"); n != 0 {
			t.Errorf("renders got = %d, want = 0", n)
		}
		e.wantWebhooks()
	})

	// source: spec/services/payment_receipts/generate_pdf_service_spec.rb:97
	t.Run("force", func(t *testing.T) {
		e := newEnv(t)
		old := e.blobs.put(t, "receipt.pdf", docgen.ContentTypePDF)
		rc := e.receipt(e.invoice(), func(rc *payment.Receipt) { rc.File = old })
		if err := e.svc.GeneratePaymentReceipt(e.Ctx, e.OrgID(), kernel.PaymentReceiptArgs{PaymentReceiptID: rc.ID, Force: true}); err != nil {
			t.Fatal(err)
		}
		if got := e.reloadReceipt(rc.ID).File; got == old {
			t.Errorf("file got = %q, want = a new file", got)
		}
	})

	// source: spec/services/payment_receipts/generate_pdf_service_spec.rb:158
	t.Run("pdf failure", func(t *testing.T) {
		e := newEnv(t)
		e.render.failPDF = errAttach
		rc := e.receipt(e.invoice())
		err := e.svc.GeneratePaymentReceipt(e.Ctx, e.OrgID(), kernel.PaymentReceiptArgs{PaymentReceiptID: rc.ID})
		if !errors.Is(err, errAttach) {
			t.Errorf("error got = %v, want = %v", err, errAttach)
		}
	})

	// source: spec/services/payment_receipts/generate_xml_service_spec.rb:48
	// source: spec/jobs/payment_receipts/generate_documents_job_spec.rb:31
	t.Run("generates the xml", func(t *testing.T) {
		e := newEnv(t)
		e.einvoicing("FR")
		rc := e.receipt(e.invoice())
		if err := e.svc.GeneratePaymentReceipt(e.Ctx, e.OrgID(), kernel.PaymentReceiptArgs{PaymentReceiptID: rc.ID}); err != nil {
			t.Fatal(err)
		}
		e.wantStored(e.reloadReceipt(rc.ID).XMLFile, docgen.ContentTypeXML)
	})

	for _, c := range []struct {
		name    string
		country string
		einv    bool
	}{
		// source: spec/services/payment_receipts/generate_xml_service_spec.rb:34
		// source: spec/services/payment_receipts/generate_xml_service_spec.rb:107
		{name: "country not allowed", country: "BR", einv: true},
		// source: spec/services/payment_receipts/generate_xml_service_spec.rb:116
		{name: "einvoicing disabled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.einv {
				e.einvoicing(c.country)
			}
			rc := e.receipt(e.invoice())
			if err := e.svc.GeneratePaymentReceipt(e.Ctx, e.OrgID(), kernel.PaymentReceiptArgs{PaymentReceiptID: rc.ID}); err != nil {
				t.Fatal(err)
			}
			if got := e.reloadReceipt(rc.ID).XMLFile; !got.IsZero() || e.render.n("receipt.xml") != 0 {
				t.Errorf("xml got = %q, want = none", got)
			}
		})
	}

	// source: spec/jobs/payment_receipts/generate_documents_job_spec.rb:37
	// source: spec/jobs/payment_receipts/notify_job_spec.rb:8
	// source: spec/jobs/payment_receipts/generate_documents_job_spec.rb:45
	for _, notify := range []bool{true, false} {
		t.Run("notify "+map[bool]string{true: "true", false: "false"}[notify], func(t *testing.T) {
			e := newEnv(t)
			rc := e.receipt(e.invoice())
			if err := e.svc.GeneratePaymentReceipt(e.Ctx, e.OrgID(), kernel.PaymentReceiptArgs{PaymentReceiptID: rc.ID, Notify: notify}); err != nil {
				t.Fatal(err)
			}
			want := 0
			if notify {
				want = 1
			}
			if got := len(e.jobs(kernel.JobSendDocumentEmail)); got != want {
				t.Errorf("email jobs got = %d, want = %d", got, want)
			}
		})
	}

	// source: app/graphql/mutations/payment_receipts/download.rb:18-22
	t.Run("download renders at once", func(t *testing.T) {
		e := newEnv(t)
		rc := e.receipt(e.invoice())
		got, err := e.svc.PaymentReceiptPDF(e.Ctx, e.OrgID(), rc.ID)
		if err != nil {
			t.Fatal(err)
		}
		e.wantStored(got.File, docgen.ContentTypePDF)
		if _, err := e.svc.PaymentReceiptPDF(e.Ctx, e.OrgID(), rc.ID); err != nil {
			t.Fatal(err)
		}
		if n := e.render.n("receipt.pdf"); n != 1 {
			t.Errorf("pdf renders got = %d, want = 1", n)
		}
	})
}
