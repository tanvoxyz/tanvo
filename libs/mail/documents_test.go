package mail

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestInvoiceCreated(t *testing.T) {
	// source: spec/mailers/invoice_mailer_spec.rb:25
	t.Run("envelope", func(t *testing.T) {
		in := invoiceEmail("en")
		m, err := newComposer(t).InvoiceCreated(in)
		if err != nil {
			t.Fatalf("InvoiceCreated() error = %v", err)
		}
		if got, want := m.Subject, "Your Invoice from ACME Corp #"+in.Number; got != want {
			t.Errorf("Subject = %q, want = %q", got, want)
		}
		if got, want := addresses(m.To), []string{testTo}; !slices.Equal(got, want) {
			t.Errorf("To = %v, want = %v", got, want)
		}
		if got, want := m.From, (Address{Name: "ACME Corp", Address: testFrom}); got != want {
			t.Errorf("From = %v, want = %v", got, want)
		}
		if got, want := addresses(m.ReplyTo), []string{testBEEmail}; !slices.Equal(got, want) {
			t.Errorf("ReplyTo = %v, want = %v", got, want)
		}
		if len(m.Attachments) != 1 || m.Attachments[0].Filename != "invoice-"+in.Number+".pdf" {
			t.Errorf("Attachments = %v, want = [invoice-%s.pdf]", m.Attachments, in.Number)
		}
		if !strings.Contains(m.HTML, in.FileURL) {
			t.Errorf("HTML has no download link %s", in.FileURL)
		}
	})

	// source: spec/mailers/invoice_mailer_spec.rb:36
	t.Run("pdfs disabled", func(t *testing.T) {
		m, err := newComposer(t, withoutPDFs).InvoiceCreated(invoiceEmail("en"))
		if err != nil {
			t.Fatalf("InvoiceCreated() error = %v", err)
		}
		if len(m.Attachments) != 0 {
			t.Errorf("Attachments = %v, want = none", m.Attachments)
		}
		if strings.Contains(m.HTML, "invoice.pdf") || strings.Contains(m.Text, "invoice.pdf") {
			t.Errorf("body links the PDF although PDFs are disabled")
		}
	})

	tests := []struct {
		name   string
		mutate func(*InvoiceEmail)
		reason string
	}{
		// source: spec/mailers/invoice_mailer_spec.rb:65
		{"billing entity email nil", func(in *InvoiceEmail) { in.BillingEntity.Email = nil }, ReasonBillingEntityEmailMissing},
		// source: spec/mailers/invoice_mailer_spec.rb:75
		{"customer email nil", func(in *InvoiceEmail) { in.CustomerEmails = nil }, ReasonNoRecipient},
		// source: spec/mailers/invoice_mailer_spec.rb:87
		{"customer email empty", func(in *InvoiceEmail) { in.CustomerEmails = []string{""} }, ReasonNoRecipient},
		// source: spec/mailers/invoice_mailer_spec.rb:99
		{"fees amount zero", func(in *InvoiceEmail) { in.FeesAmount = money.MustParse("0") }, ReasonZeroFeesAmount},
		{"email setting disabled", func(in *InvoiceEmail) { in.BillingEntity.EmailSettings = nil }, ReasonEmailSettingDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := invoiceEmail("en")
			tt.mutate(&in)
			_, err := newComposer(t).InvoiceCreated(in)
			wantSkip(t, err, tt.reason)
		})
	}

	// source: spec/services/emails/resend_service_spec.rb:88
	t.Run("resend ignores email settings", func(t *testing.T) {
		in := invoiceEmail("en")
		in.BillingEntity.EmailSettings = nil
		in.Resend = true
		if _, err := newComposer(t).InvoiceCreated(in); err != nil {
			t.Errorf("InvoiceCreated() error = %v, want = nil", err)
		}
	})

	t.Run("from email enabled and custom recipients", func(t *testing.T) {
		in := invoiceEmail("en")
		in.FromEmailEnabled = true
		in.To = []string{"custom@example.com, another@example.com"}
		in.Cc = []string{"cc@example.com"}
		in.Bcc = []string{"bcc@example.com"}
		m, err := newComposer(t).InvoiceCreated(in)
		if err != nil {
			t.Fatalf("InvoiceCreated() error = %v", err)
		}
		if got, want := m.From.Address, testBEEmail; got != want {
			t.Errorf("From = %q, want = %q", got, want)
		}
		if got, want := addresses(m.To), []string{"custom@example.com", "another@example.com"}; !slices.Equal(got, want) {
			t.Errorf("To = %v, want = %v", got, want)
		}
		if got, want := m.Recipients(), []string{"custom@example.com", "another@example.com", "cc@example.com", "bcc@example.com"}; !slices.Equal(got, want) {
			t.Errorf("Recipients() = %v, want = %v", got, want)
		}
	})

	t.Run("no sender configured", func(t *testing.T) {
		c := newComposer(t, func(c *Config) { c.DefaultFrom = "" })
		if _, err := c.InvoiceCreated(invoiceEmail("en")); err == nil || errors.Is(err, ErrSkipped) {
			t.Errorf("InvoiceCreated() error = %v, want a configuration error", err)
		}
	})
}

func TestCreditNoteCreated(t *testing.T) {
	// source: spec/mailers/credit_note_mailer_spec.rb:15
	t.Run("envelope", func(t *testing.T) {
		in := creditNoteEmail("en")
		m, err := newComposer(t).CreditNoteCreated(in)
		if err != nil {
			t.Fatalf("CreditNoteCreated() error = %v", err)
		}
		if got, want := addresses(m.To), []string{testTo}; !slices.Equal(got, want) {
			t.Errorf("To = %v, want = %v", got, want)
		}
		if got, want := addresses(m.ReplyTo), []string{testBEEmail}; !slices.Equal(got, want) {
			t.Errorf("ReplyTo = %v, want = %v", got, want)
		}
		if len(m.Attachments) != 1 || m.Attachments[0].Filename != "credit_note-"+in.Number+".pdf" {
			t.Errorf("Attachments = %v, want = [credit_note-%s.pdf]", m.Attachments, in.Number)
		}
		if got, want := m.Subject, "Your credit note from ACME Corp #"+in.Number; got != want {
			t.Errorf("Subject = %q, want = %q", got, want)
		}
	})

	// source: spec/mailers/credit_note_mailer_spec.rb:24
	t.Run("pdfs disabled", func(t *testing.T) {
		m, err := newComposer(t, withoutPDFs).CreditNoteCreated(creditNoteEmail("en"))
		if err != nil {
			t.Fatalf("CreditNoteCreated() error = %v", err)
		}
		if len(m.Attachments) != 0 {
			t.Errorf("Attachments = %v, want = none", m.Attachments)
		}
	})

	skips := []struct {
		name   string
		mutate func(*CreditNoteEmail)
		reason string
	}{
		// source: spec/mailers/credit_note_mailer_spec.rb:49
		{"billing entity email nil", func(in *CreditNoteEmail) { in.BillingEntity.Email = nil }, ReasonBillingEntityEmailMissing},
		// source: spec/mailers/credit_note_mailer_spec.rb:61
		{"customer email nil", func(in *CreditNoteEmail) { in.CustomerEmails = nil }, ReasonNoRecipient},
		// source: spec/mailers/credit_note_mailer_spec.rb:73
		{"customer email empty", func(in *CreditNoteEmail) { in.CustomerEmails = []string{" "} }, ReasonNoRecipient},
		{"email setting disabled", func(in *CreditNoteEmail) {
			in.BillingEntity.EmailSettings = []organization.EmailSetting{organization.EmailSettingInvoiceFinalized}
		}, ReasonEmailSettingDisabled},
	}
	for _, tt := range skips {
		t.Run(tt.name, func(t *testing.T) {
			in := creditNoteEmail("en")
			tt.mutate(&in)
			_, err := newComposer(t).CreditNoteCreated(in)
			wantSkip(t, err, tt.reason)
		})
	}

	// The notice of app/views/credit_note_mailer/created.slim.
	notices := []struct {
		name                       string
		credited, refunded, offset bool
		want                       string
	}{
		{"credited", true, false, false, "Credited on customer balance on Oct 05, 2024"},
		{"refunded", false, true, false, "Refunded on Oct 05, 2024"},
		{"offset", false, false, true, "Offset invoice ACME-202410-001 on Oct 05, 2024"},
		{"several", true, true, false, "Issued on Oct 05, 2024"},
	}
	for _, tt := range notices {
		t.Run("notice "+tt.name, func(t *testing.T) {
			in := creditNoteEmail("en")
			in.Credited, in.Refunded, in.Offset = tt.credited, tt.refunded, tt.offset
			m, err := newComposer(t).CreditNoteCreated(in)
			if err != nil {
				t.Fatalf("CreditNoteCreated() error = %v", err)
			}
			if !strings.Contains(m.Text, tt.want) {
				t.Errorf("Text = %q, want it to contain %q", m.Text, tt.want)
			}
		})
	}
}

func TestPaymentReceiptCreated(t *testing.T) {
	// source: spec/mailers/payment_receipt_mailer_spec.rb:18
	t.Run("envelope", func(t *testing.T) {
		in := receiptEmail("en")
		m, err := newComposer(t).PaymentReceiptCreated(in)
		if err != nil {
			t.Fatalf("PaymentReceiptCreated() error = %v", err)
		}
		if got, want := addresses(m.To), []string{testTo}; !slices.Equal(got, want) {
			t.Errorf("To = %v, want = %v", got, want)
		}
		if got, want := addresses(m.ReplyTo), []string{testBEEmail}; !slices.Equal(got, want) {
			t.Errorf("ReplyTo = %v, want = %v", got, want)
		}
		names := make([]string, 0, len(m.Attachments))
		for _, a := range m.Attachments {
			names = append(names, a.Filename)
		}
		if want := []string{"receipt-ACME-RCPT-001.pdf", "invoice-ACME-202410-001.pdf"}; !slices.Equal(names, want) {
			t.Errorf("attachments = %v, want = %v", names, want)
		}
	})

	// source: spec/mailers/payment_receipt_mailer_spec.rb:27
	t.Run("pdfs disabled", func(t *testing.T) {
		in := receiptEmail("en")
		in.PDF, in.Invoices[0].PDF = nil, nil
		m, err := newComposer(t, withoutPDFs).PaymentReceiptCreated(in)
		if err != nil {
			t.Fatalf("PaymentReceiptCreated() error = %v", err)
		}
		if len(m.Attachments) != 0 {
			t.Errorf("Attachments = %v, want = none", m.Attachments)
		}
	})

	notReady := []struct {
		name   string
		mutate func(*PaymentReceiptEmail)
	}{
		// source: spec/mailers/payment_receipt_mailer_spec.rb:41
		{"receipt file missing", func(in *PaymentReceiptEmail) { in.PDF = nil }},
		// source: spec/mailers/payment_receipt_mailer_spec.rb:58
		{"invoice file missing", func(in *PaymentReceiptEmail) { in.Invoices[0].PDF = nil }},
		// source: spec/mailers/payment_receipt_mailer_spec.rb:91
		{"one request invoice missing", func(in *PaymentReceiptEmail) {
			in.Invoices = append(in.Invoices, ReceiptInvoice{Number: "ACME-202410-002", Currency: "EUR", TotalAmount: money.MustParse("10")})
		}},
	}
	for _, tt := range notReady {
		t.Run(tt.name, func(t *testing.T) {
			in := receiptEmail("en")
			tt.mutate(&in)
			_, err := newComposer(t).PaymentReceiptCreated(in)
			if !errors.Is(err, ErrFilesNotReady) {
				t.Errorf("PaymentReceiptCreated() error = %v, want = %v", err, ErrFilesNotReady)
			}
		})
	}

	// source: spec/mailers/payment_receipt_mailer_spec.rb:84
	t.Run("request covering several invoices", func(t *testing.T) {
		in := receiptEmail("en")
		in.Invoices = append(in.Invoices, ReceiptInvoice{
			Number: "ACME-202410-002", Currency: "EUR", TotalAmount: money.MustParse("10"),
			FileURL: "https://files.tanvo.test/other.pdf", PDF: blankPDF(),
		})
		m, err := newComposer(t).PaymentReceiptCreated(in)
		if err != nil {
			t.Fatalf("PaymentReceiptCreated() error = %v", err)
		}
		if got, want := addresses(m.To), []string{testTo}; !slices.Equal(got, want) {
			t.Errorf("To = %v, want = %v", got, want)
		}
		if len(m.Attachments) != 3 {
			t.Errorf("len(Attachments) = %d, want = 3", len(m.Attachments))
		}
		if !strings.Contains(m.HTML, "https://files.tanvo.test/other.pdf") {
			t.Errorf("HTML has no link to the second invoice")
		}
	})

	skips := []struct {
		name   string
		mutate func(*PaymentReceiptEmail)
		reason string
	}{
		// source: spec/mailers/payment_receipt_mailer_spec.rb:100
		{"billing entity email nil", func(in *PaymentReceiptEmail) { in.BillingEntity.Email = nil }, ReasonBillingEntityEmailMissing},
		// source: spec/mailers/payment_receipt_mailer_spec.rb:112
		{"customer email nil", func(in *PaymentReceiptEmail) { in.CustomerEmails = nil }, ReasonNoRecipient},
		// source: spec/mailers/payment_receipt_mailer_spec.rb:124
		{"customer email empty", func(in *PaymentReceiptEmail) { in.CustomerEmails = []string{""} }, ReasonNoRecipient},
	}
	for _, tt := range skips {
		t.Run(tt.name, func(t *testing.T) {
			in := receiptEmail("en")
			tt.mutate(&in)
			_, err := newComposer(t).PaymentReceiptCreated(in)
			wantSkip(t, err, tt.reason)
		})
	}
}

func TestPaymentRequestRequested(t *testing.T) {
	// source: spec/mailers/payment_request_mailer_spec.rb:42
	t.Run("envelope and invoices", func(t *testing.T) {
		in := requestEmail("en")
		m, err := newComposer(t).PaymentRequestRequested(in)
		if err != nil {
			t.Fatalf("PaymentRequestRequested() error = %v", err)
		}
		if got, want := addresses(m.To), []string{testTo}; !slices.Equal(got, want) {
			t.Errorf("To = %v, want = %v", got, want)
		}
		if got, want := addresses(m.ReplyTo), []string{testBEEmail}; !slices.Equal(got, want) {
			t.Errorf("ReplyTo = %v, want = %v", got, want)
		}
		if len(m.Bcc) != 0 {
			t.Errorf("Bcc = %v, want = none", m.Bcc)
		}
		for _, s := range []string{"ACME-202409-001", "ACME-202409-002", "CHF 999.99", "CHF 1,970.01"} {
			if !strings.Contains(m.HTML, s) {
				t.Errorf("HTML does not contain %q", s)
			}
		}
		if got, want := m.Subject, "Your overdue balance from ACME Corp"; got != want {
			t.Errorf("Subject = %q, want = %q", got, want)
		}
	})

	// source: spec/mailers/payment_request_mailer_spec.rb:52
	t.Run("payment link", func(t *testing.T) {
		m, err := newComposer(t).PaymentRequestRequested(requestEmail("en"))
		if err != nil {
			t.Fatalf("PaymentRequestRequested() error = %v", err)
		}
		if !strings.Contains(m.HTML, `id="payment_link" href="https://pay.tanvo.test/pr_1"`) {
			t.Errorf("HTML has no a#payment_link to the payment URL")
		}
		if !strings.Contains(m.HTML, "Pay balance") {
			t.Errorf("HTML does not contain %q", "Pay balance")
		}
	})

	// source: spec/mailers/payment_request_mailer_spec.rb:62
	t.Run("dunning campaign bcc", func(t *testing.T) {
		in := requestEmail("en")
		in.Bcc = []string{"bcc1@example.com", "bcc2@example.com"}
		m, err := newComposer(t).PaymentRequestRequested(in)
		if err != nil {
			t.Fatalf("PaymentRequestRequested() error = %v", err)
		}
		if got := addresses(m.Bcc); !slices.Equal(got, in.Bcc) {
			t.Errorf("Bcc = %v, want = %v", got, in.Bcc)
		}
	})

	skips := []struct {
		name   string
		mutate func(*PaymentRequestEmail)
		reason string
	}{
		// source: spec/mailers/payment_request_mailer_spec.rb:75
		{"payment request email nil", func(in *PaymentRequestEmail) { in.Email = "" }, ReasonNoRecipient},
		// source: spec/mailers/payment_request_mailer_spec.rb:83
		{"billing entity email nil", func(in *PaymentRequestEmail) { in.BillingEntity.Email = nil }, ReasonBillingEntityEmailMissing},
	}
	for _, tt := range skips {
		t.Run(tt.name, func(t *testing.T) {
			in := requestEmail("en")
			tt.mutate(&in)
			_, err := newComposer(t).PaymentRequestRequested(in)
			wantSkip(t, err, tt.reason)
		})
	}

	// source: spec/mailers/payment_request_mailer_spec.rb:91
	t.Run("no payment url", func(t *testing.T) {
		in := requestEmail("en")
		in.PaymentURL = ""
		m, err := newComposer(t).PaymentRequestRequested(in)
		if err != nil {
			t.Fatalf("PaymentRequestRequested() error = %v", err)
		}
		if strings.Contains(m.HTML, "payment_link") || strings.Contains(m.HTML, "Pay balance") {
			t.Errorf("HTML shows the payment link without a payment URL")
		}
	})

	// config/locales/en/email.yml payment_terms (zero, one, other).
	terms := []struct {
		days int
		want string
	}{
		{0, "require payment upon invoice generation"},
		{1, "payment terms is 1 day."},
		{45, "payment terms are 45 days."},
	}
	for _, tt := range terms {
		in := requestEmail("en")
		in.NetPaymentTermDays = tt.days
		m, err := newComposer(t).PaymentRequestRequested(in)
		if err != nil {
			t.Fatalf("PaymentRequestRequested(%d days) error = %v", tt.days, err)
		}
		if !strings.Contains(m.Text, tt.want) {
			t.Errorf("Text(%d days) = %q, want it to contain %q", tt.days, m.Text, tt.want)
		}
	}
}
