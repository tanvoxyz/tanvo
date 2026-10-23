package mail

import (
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

const (
	testFrom    = "noreply@tanvo.test"
	testAppURL  = "https://app.tanvo.test"
	testBEEmail = "billing@acme.test"
	testTo      = "customer@example.com"
)

func newComposer(t *testing.T, opts ...func(*Config)) *Composer {
	t.Helper()
	cfg := Config{DefaultFrom: testFrom, AppURL: testAppURL}
	for _, o := range opts {
		o(&cfg)
	}
	c, err := NewComposer(cfg)
	if err != nil {
		t.Fatalf("NewComposer() error = %v", err)
	}
	return c
}

func withoutPDFs(c *Config) { c.DisablePDFs = true }

func blankPDF() []byte { return []byte("%PDF-1.4\n%blank\n") }

func billingEntity(email string) *organization.BillingEntity {
	be := &organization.BillingEntity{
		Name: "ACME Corp",
		EmailSettings: []organization.EmailSetting{
			organization.EmailSettingInvoiceFinalized,
			organization.EmailSettingCreditNoteCreated,
			organization.EmailSettingPaymentReceiptCreated,
		},
	}
	if email != "" {
		be.Email = &email
	}
	return be
}

func documentEmail(locale string) DocumentEmail {
	return DocumentEmail{BillingEntity: billingEntity(testBEEmail), Locale: locale, CustomerEmails: []string{testTo}}
}

func invoiceEmail(locale string) InvoiceEmail {
	return InvoiceEmail{
		DocumentEmail: documentEmail(locale),
		Number:        "ACME-202410-001",
		Currency:      "EUR",
		TotalAmount:   money.MustParse("1234.5"),
		FeesAmount:    money.MustParse("1000"),
		IssuingDate:   core.NewDate(2024, time.October, 3),
		FileURL:       "https://files.tanvo.test/invoice.pdf",
		PDF:           blankPDF(),
	}
}

func creditNoteEmail(locale string) CreditNoteEmail {
	return CreditNoteEmail{
		DocumentEmail: documentEmail(locale),
		Number:        "ACME-CN202410-001",
		InvoiceNumber: "ACME-202410-001",
		Currency:      "USD",
		TotalAmount:   money.MustParse("99.99"),
		IssuingDate:   core.NewDate(2024, time.October, 5),
		Credited:      true,
		FileURL:       "https://files.tanvo.test/credit_note.pdf",
		PDF:           blankPDF(),
	}
}

func receiptEmail(locale string) PaymentReceiptEmail {
	return PaymentReceiptEmail{
		DocumentEmail:  documentEmail(locale),
		Number:         "ACME-RCPT-001",
		Currency:       "EUR",
		PaymentAmount:  money.MustParse("1234.5"),
		PaymentDate:    core.NewDate(2024, time.October, 10),
		TotalDueAmount: money.MustParse("0"),
		Invoices: []ReceiptInvoice{{
			Number: "ACME-202410-001", Currency: "EUR", TotalAmount: money.MustParse("1234.5"),
			FileURL: "https://files.tanvo.test/invoice.pdf", PDF: blankPDF(),
		}},
		FileURL: "https://files.tanvo.test/receipt.pdf",
		PDF:     blankPDF(),
	}
}

func requestEmail(locale string) PaymentRequestEmail {
	return PaymentRequestEmail{
		BillingEntity:      billingEntity(testBEEmail),
		Locale:             locale,
		Email:              testTo,
		CustomerName:       "Jane Doe",
		Currency:           "CHF",
		Amount:             money.MustParse("2970"),
		NetPaymentTermDays: 30,
		PaymentURL:         "https://pay.tanvo.test/pr_1",
		Invoices: []ReceiptInvoice{
			{Number: "ACME-202409-001", Currency: "CHF", TotalDueAmount: money.MustParse("999.99"), FileURL: "https://files.tanvo.test/1.pdf"},
			{Number: "ACME-202409-002", Currency: "CHF", TotalDueAmount: money.MustParse("1970.01"), FileURL: "https://files.tanvo.test/2.pdf"},
		},
	}
}

func authMethodsEmail(locale string) AuthenticationMethodsEmail {
	return AuthenticationMethodsEmail{
		OrganizationName:  "Hooli Inc",
		OrganizationEmail: "contact@hooli.test",
		Locale:            locale,
		ActorEmail:        "admin0@hooli.test",
		AdminEmails:       []string{"admin0@hooli.test", "admin1@hooli.test", "admin2@hooli.test"},
		Additions:         []organization.AuthenticationMethod{organization.AuthenticationMethodOkta},
		Deletions:         []organization.AuthenticationMethod{organization.AuthenticationMethodGoogleOAuth},
	}
}

// builders build one representative message of each kind in a locale
// (system emails ignore it, except authentication_methods_updated).
func builders() map[Kind]func(c *Composer, locale string) (*Message, error) {
	return map[Kind]func(*Composer, string) (*Message, error){
		KindInvoiceCreated: func(c *Composer, l string) (*Message, error) { return c.InvoiceCreated(invoiceEmail(l)) },
		KindCreditNoteCreated: func(c *Composer, l string) (*Message, error) {
			return c.CreditNoteCreated(creditNoteEmail(l))
		},
		KindPaymentReceiptCreated: func(c *Composer, l string) (*Message, error) {
			return c.PaymentReceiptCreated(receiptEmail(l))
		},
		KindPaymentRequestRequested: func(c *Composer, l string) (*Message, error) {
			return c.PaymentRequestRequested(requestEmail(l))
		},
		KindPasswordResetRequested: func(c *Composer, _ string) (*Message, error) {
			return c.PasswordResetRequested(PasswordResetEmail{Email: "user@hooli.test", Token: "reset-token"})
		},
		KindAPIKeyCreated: func(c *Composer, _ string) (*Message, error) {
			return c.APIKeyCreated(APIKeyEmail{OrganizationName: "Hooli & Co", AdminEmails: []string{"admin@hooli.test"}})
		},
		KindAPIKeyRotated: func(c *Composer, _ string) (*Message, error) {
			return c.APIKeyRotated(APIKeyEmail{OrganizationName: "Hooli & Co", AdminEmails: []string{"admin@hooli.test"}})
		},
		KindAPIKeyDestroyed: func(c *Composer, _ string) (*Message, error) {
			return c.APIKeyDestroyed(APIKeyEmail{OrganizationName: "Hooli & Co", AdminEmails: []string{"admin@hooli.test"}})
		},
		KindDataExportCompleted: func(c *Composer, _ string) (*Message, error) {
			return c.DataExportCompleted(DataExportEmail{
				UserEmail: "user@hooli.test", ResourceType: "invoices", Completed: true,
				FileURL: "https://files.tanvo.test/export.csv", Filename: "invoices.csv",
			})
		},
		KindAuthenticationMethodsUpdated: func(c *Composer, l string) (*Message, error) {
			return c.AuthenticationMethodsUpdated(authMethodsEmail(l))
		},
		KindFeatureToggled: func(c *Composer, _ string) (*Message, error) {
			return c.FeatureToggled(FeatureToggledEmail{
				OrganizationName: "Hooli Inc", OwnerEmail: "owner@hooli.test", FeatureKey: "okta", Enabled: true, ActorEmail: "cs@tanvo.test",
			})
		},
		KindInvitation: func(c *Composer, _ string) (*Message, error) {
			return c.Invitation(InvitationEmail{
				Email: "new@hooli.test", OrganizationName: "Hooli Inc", InviterEmail: "admin@hooli.test", RoleName: "Finance",
				Token: "invite-token", ExpiresAt: time.Date(2024, time.October, 17, 9, 0, 0, 0, time.UTC),
			})
		},
	}
}

func addresses(as []Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Address)
	}
	return out
}

func wantSkip(t *testing.T, err error, reason string) {
	t.Helper()
	var se *SkipError
	if !errors.As(err, &se) || !errors.Is(err, ErrSkipped) {
		t.Fatalf("error = %v, want a SkipError", err)
	}
	if se.Reason != reason {
		t.Errorf("skip reason = %q, want = %q", se.Reason, reason)
	}
}
