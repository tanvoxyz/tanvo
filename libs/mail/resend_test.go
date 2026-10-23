package mail

import (
	"errors"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestValidateResend(t *testing.T) {
	base := func(typ DocumentType) ResendRequest {
		return ResendRequest{
			Type: typ, Finalized: true, FeesAmount: money.MustParse("10"),
			BillingEntity: billingEntity(testBEEmail), CustomerEmails: []string{testTo},
		}
	}
	tests := []struct {
		name      string
		req       func() ResendRequest
		noFrom    bool
		rule      string
		fields    []core.FieldError
		wantTo    []string
		notFound  bool
		wantValid bool
	}{
		// source: spec/services/emails/resend_service_spec.rb:23
		{name: "resource nil", req: func() ResendRequest { return ResendRequest{Type: DocumentInvoice} }, notFound: true},
		// source: spec/services/emails/resend_service_spec.rb:41
		{name: "finalized invoice", req: func() ResendRequest { return base(DocumentInvoice) }, wantValid: true},
		// source: spec/services/emails/resend_service_spec.rb:53
		{name: "custom recipients", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.To = []string{"custom@example.com", "another@example.com"}
			r.Cc, r.Bcc = []string{"cc@example.com"}, []string{"bcc@example.com"}
			return r
		}, wantValid: true, wantTo: []string{"custom@example.com", "another@example.com"}},
		// source: spec/services/emails/resend_service_spec.rb:65
		{name: "draft invoice", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.Finalized = false
			return r
		}, rule: "invoice_not_finalized"},
		// source: spec/services/emails/resend_service_spec.rb:96
		{name: "email settings disabled", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.BillingEntity.EmailSettings = nil
			return r
		}, wantValid: true},
		// source: spec/services/emails/resend_service_spec.rb:115
		{
			name: "no sender configured", req: func() ResendRequest { return base(DocumentInvoice) }, noFrom: true,
			fields: []core.FieldError{{Field: "from", Code: core.CodeRequired}},
		},
		// source: spec/services/emails/resend_service_spec.rb:126
		{name: "own sender email", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.FromEmailEnabled = true
			return r
		}, noFrom: true, wantValid: true},
		// source: spec/services/emails/resend_service_spec.rb:139
		{name: "invoice without fees", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.FeesAmount = money.MustParse("0")
			return r
		}, fields: []core.FieldError{{Field: "fees_amount", Code: core.CodeNotPositive}}},
		// source: spec/services/emails/resend_service_spec.rb:153
		{name: "billing entity without email", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.BillingEntity.Email = nil
			return r
		}, fields: []core.FieldError{{Field: "billing_entity", Code: core.CodeRequired}}},
		// source: spec/services/emails/resend_service_spec.rb:166
		{name: "invalid recipient", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.To = []string{"invalid-email"}
			return r
		}, fields: []core.FieldError{{Field: "to", Code: core.CodeInvalidEmail}}},
		{name: "invalid cc and bcc", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.Cc, r.Bcc = []string{"nope"}, []string{"also nope"}
			return r
		}, fields: []core.FieldError{{Field: "cc", Code: core.CodeInvalidEmail}, {Field: "bcc", Code: core.CodeInvalidEmail}}},
		// source: spec/services/emails/resend_service_spec.rb:179
		{name: "unicode local part", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.To = []string{"joão.silva@example.com"}
			return r
		}, wantValid: true, wantTo: []string{"joão.silva@example.com"}},
		// source: spec/services/emails/resend_service_spec.rb:192
		{name: "no customer email and no recipient", req: func() ResendRequest {
			r := base(DocumentInvoice)
			r.CustomerEmails = nil
			return r
		}, fields: []core.FieldError{{Field: "to", Code: core.CodeRequired}}},
		// source: spec/services/emails/resend_service_spec.rb:209
		{name: "finalized credit note", req: func() ResendRequest { return base(DocumentCreditNote) }, wantValid: true},
		// source: spec/services/emails/resend_service_spec.rb:220
		{name: "draft credit note", req: func() ResendRequest {
			r := base(DocumentCreditNote)
			r.Finalized = false
			return r
		}, rule: "credit_note_not_finalized"},
		// source: spec/services/emails/resend_service_spec.rb:235
		{name: "payment receipt without status check", req: func() ResendRequest {
			r := base(DocumentPaymentReceipt)
			r.Finalized = false
			r.FeesAmount = money.MustParse("0")
			return r
		}, wantValid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newComposer(t)
			if tt.noFrom {
				c = newComposer(t, func(c *Config) { c.DefaultFrom = "" })
			}
			d, err := c.ValidateResend(tt.req(), "en")
			switch {
			case tt.notFound:
				if !errors.Is(err, core.ErrNotFound) {
					t.Fatalf("error = %v, want = %v", err, core.ErrNotFound)
				}
			case tt.rule != "":
				var re *core.RuleError
				if !errors.As(err, &re) || re.Code != tt.rule {
					t.Fatalf("error = %v, want a RuleError %q", err, tt.rule)
				}
			case tt.fields != nil:
				if got := core.FieldErrors(err); !slices.Equal(got, tt.fields) {
					t.Fatalf("FieldErrors() = %v, want = %v", got, tt.fields)
				}
			case tt.wantValid:
				if err != nil {
					t.Fatalf("error = %v, want = nil", err)
				}
				if !d.Resend {
					t.Errorf("Resend = false, want = true")
				}
				if tt.wantTo != nil && !slices.Equal(d.To, tt.wantTo) {
					t.Errorf("To = %v, want = %v", d.To, tt.wantTo)
				}
			}
		})
	}

	// The validated envelope builds the email even with settings off.
	t.Run("builds the invoice email", func(t *testing.T) {
		r := base(DocumentInvoice)
		r.BillingEntity.EmailSettings = nil
		r.To = []string{"joão.silva@example.com"}
		c := newComposer(t)
		d, err := c.ValidateResend(r, "en")
		if err != nil {
			t.Fatalf("ValidateResend() error = %v", err)
		}
		in := invoiceEmail("en")
		in.DocumentEmail = d
		m, err := c.InvoiceCreated(in)
		if err != nil {
			t.Fatalf("InvoiceCreated() error = %v", err)
		}
		if got, want := addresses(m.To), r.To; !slices.Equal(got, want) {
			t.Errorf("To = %v, want = %v", got, want)
		}
	})
}
