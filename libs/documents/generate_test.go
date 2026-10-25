package documents

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

// TestInvoiceTemplateSelfBilled checks that a self-billed invoice is
// printed with the self-billed template.
//
// source: spec/services/invoices/generate_pdf_service_spec.rb:128
func TestInvoiceTemplateSelfBilled(t *testing.T) {
	inv := prepaidCreditsInvoice(300, 200)(t, &ids{})
	if got := inv.Template(); got != TemplateInvoice {
		t.Errorf("template got = %s, want = %s", got, TemplateInvoice)
	}
	inv.Invoice.SelfBilled = true
	if got := inv.Template(); got != TemplateSelfBilled {
		t.Errorf("self-billed template got = %s, want = %s", got, TemplateSelfBilled)
	}
}

// TestRenderCreditNoteTemplates checks that a credit note is printed with
// the credit note template, and with the self-billed one for a
// self-billed invoice.
func TestRenderCreditNoteTemplates(t *testing.T) {
	r, _ := newTestRenderer(t)
	for _, tt := range []struct {
		source     string
		selfBilled bool
	}{
		{"spec/services/credit_notes/generate_pdf_service_spec.rb:30", false},
		{"spec/services/credit_notes/generate_pdf_service_spec.rb:46", true},
	} {
		t.Log("source:", tt.source)
		d := creditNoteData("CN-202510-001", 3000, nil)(t, &ids{})
		d.Invoice.SelfBilled = tt.selfBilled
		b, err := r.CreditNotePDF(context.Background(), d, "en")
		if err != nil {
			t.Fatalf("CreditNotePDF: %v", err)
		}
		text := pdfText(t, b)
		if !strings.Contains(text, "CN-202510-001") {
			t.Errorf("text got = %q, want = the credit note number", text)
		}
		if got := strings.Contains(text, "self-billing"); got != tt.selfBilled {
			t.Errorf("self-billed footer got = %v, want = %v", got, tt.selfBilled)
		}
	}
}

// TestRenderCreditNoteFacturX checks that the credit note of an
// e-invoicing billing entity embeds its CII XML.
//
// source: spec/services/credit_notes/generate_pdf_service_spec.rb:98
func TestRenderCreditNoteFacturX(t *testing.T) {
	r, _ := newTestRenderer(t)
	d := creditNoteData("CN-202510-001", 3000, nil)(t, &ids{})
	d.Invoice.Type = invoice.TypeSubscription
	d.BillingEntity.EInvoicing = true
	d.BillingEntity.Address.Country = "FR"
	b, err := r.CreditNotePDF(context.Background(), d, "fr")
	if err != nil {
		t.Fatalf("CreditNotePDF: %v", err)
	}
	for _, want := range []string{"/AFRelationship/Alternative", "factur-x.xml", "/EmbeddedFiles"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("PDF: got = no %q, want = %q", want, want)
		}
	}
}
