package mail

import (
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// DocumentType is a document that can be emailed again.
type DocumentType string

// Document types.
const (
	// DocumentInvoice is an invoice.
	DocumentInvoice DocumentType = "invoice"
	// DocumentCreditNote is a credit note.
	DocumentCreditNote DocumentType = "credit_note"
	// DocumentPaymentReceipt is a payment receipt.
	DocumentPaymentReceipt DocumentType = "payment_receipt"
)

// ResendRequest asks to email a document again (lago's
// Emails::ResendService).
type ResendRequest struct {
	// Type is the document type.
	Type DocumentType
	// Finalized tells whether the invoice or credit note is finalized;
	// payment receipts are always resendable.
	Finalized bool
	// FeesAmount is the invoice's fees amount; zero-amount invoices are
	// never emailed.
	FeesAmount money.Decimal
	// BillingEntity issues the document.
	BillingEntity *organization.BillingEntity
	// FromEmailEnabled sends from the billing entity's email.
	FromEmailEnabled bool
	// CustomerEmails are the customer's addresses, used without To.
	CustomerEmails []string
	// To, Cc and Bcc are custom recipients.
	To, Cc, Bcc []string
}

// Field error codes of [ValidateResend], on fields billing_entity, from,
// fees_amount, to, cc and bcc.
const (
	// CodeNotFinalized is the rule code "<type>_not_finalized".
	CodeNotFinalized = "_not_finalized"
)

// ValidateResend applies lago's resend rules and returns the DocumentEmail
// to pass to the document builder (Resend set, so email settings are
// ignored): the document must be finalized (a [core.RuleError]
// "<type>_not_finalized"), the billing entity needs an email, a sender
// must be configured, an invoice needs a non-zero fees amount, and every
// recipient must be a valid address ([core.ValidationError]).
func (c *Composer) ValidateResend(r ResendRequest, locale string) (DocumentEmail, error) {
	if r.BillingEntity == nil {
		return DocumentEmail{}, fmt.Errorf("resend %s: %w", r.Type, core.ErrNotFound)
	}
	if r.Type != DocumentPaymentReceipt && !r.Finalized {
		return DocumentEmail{}, core.NewRuleError(string(r.Type)+CodeNotFinalized, "the document is not finalized")
	}
	to := nonBlank(r.To)
	if len(to) == 0 {
		to = nonBlank(r.CustomerEmails)
	}
	var v core.Validator
	v.Check(r.BillingEntity.Email != nil && strings.TrimSpace(*r.BillingEntity.Email) != "", "billing_entity", core.CodeRequired)
	_, err := c.FromAddress(r.BillingEntity, r.FromEmailEnabled)
	v.Check(err == nil, "from", core.CodeRequired)
	if r.Type == DocumentInvoice {
		v.Check(!r.FeesAmount.IsZero(), "fees_amount", core.CodeNotPositive)
	}
	if v.Check(len(to) > 0, "to", core.CodeRequired) {
		checkEmails(&v, "to", to)
	}
	checkEmails(&v, "cc", r.Cc)
	checkEmails(&v, "bcc", r.Bcc)
	if err := v.Err(); err != nil {
		return DocumentEmail{}, err
	}
	return DocumentEmail{
		BillingEntity: r.BillingEntity, FromEmailEnabled: r.FromEmailEnabled, Locale: locale,
		CustomerEmails: r.CustomerEmails, To: r.To, Cc: r.Cc, Bcc: r.Bcc, Resend: true,
	}, nil
}

func checkEmails(v *core.Validator, field string, list []string) {
	for _, e := range list {
		if !v.Check(core.ValidEmail(strings.TrimSpace(e)), field, core.CodeInvalidEmail) {
			return
		}
	}
}
