package invoice

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Credit is an amount credited on an invoice before or after taxes
// (domain §11.11): a coupon application, a credit-note consumption or a
// progressive-billing credit. Exactly one of AppliedCouponID, CreditNoteID
// and ProgressiveBillingInvoiceID is set.
type Credit struct {
	// ID identifies the credit.
	ID core.InvoiceCreditID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the credited invoice.
	InvoiceID core.InvoiceID
	// AppliedCouponID is set for a coupon credit.
	AppliedCouponID core.AppliedCouponID
	// CreditNoteID is set for a credit-note credit.
	CreditNoteID core.CreditNoteID
	// ProgressiveBillingInvoiceID is set for a progressive-billing credit:
	// the progressive invoice whose fees are deducted.
	ProgressiveBillingInvoiceID core.InvoiceID
	// Amount is the credited amount, in minor units of Currency.
	Amount int64
	// Currency is the invoice currency.
	Currency money.Currency
	// BeforeTaxes is true for coupons and progressive billing.
	BeforeTaxes bool
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// CreditKind tells which deduction a [Credit] records.
type CreditKind int

// Credit kinds.
const (
	// CreditKindCoupon is a coupon application.
	CreditKindCoupon CreditKind = iota + 1
	// CreditKindCreditNote is a credit-note consumption.
	CreditKindCreditNote
	// CreditKindProgressiveBilling is a progressive-billing credit.
	CreditKindProgressiveBilling
)

// Kind returns the kind of the credit.
func (c *Credit) Kind() CreditKind {
	switch {
	case !c.AppliedCouponID.IsZero():
		return CreditKindCoupon
	case !c.CreditNoteID.IsZero():
		return CreditKindCreditNote
	default:
		return CreditKindProgressiveBilling
	}
}

// Validate checks the credit.
func (c *Credit) Validate() error {
	var v core.Validator
	v.Check(!c.InvoiceID.IsZero(), "invoice_id", core.CodeRequired)
	n := 0
	for _, set := range []bool{!c.AppliedCouponID.IsZero(), !c.CreditNoteID.IsZero(), !c.ProgressiveBillingInvoiceID.IsZero()} {
		if set {
			n++
		}
	}
	v.Check(n == 1, "source", core.CodeInvalid)
	v.NonNegative("amount", c.Amount)
	v.Currency("currency", c.Currency)
	return v.Err()
}

// CreditRepository stores invoice credits.
type CreditRepository interface {
	// Create inserts c.
	Create(ctx context.Context, c *Credit) error
	// ListByInvoice returns the credits of an invoice in creation order.
	ListByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*Credit, error)
	// ListByAppliedCoupon returns the credits of an applied coupon in
	// creation order.
	ListByAppliedCoupon(ctx context.Context, org core.OrganizationID, applied core.AppliedCouponID) ([]*Credit, error)
	// ListByCreditNote returns the credits consuming a credit note in
	// creation order.
	ListByCreditNote(ctx context.Context, org core.OrganizationID, cn core.CreditNoteID) ([]*Credit, error)
	// ListByProgressiveBillingInvoice returns the credits deducting a
	// progressive-billing invoice in creation order.
	ListByProgressiveBillingInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*Credit, error)
	// DeleteByInvoice deletes the credits of an invoice (draft refresh).
	DeleteByInvoice(ctx context.Context, org core.OrganizationID, invoice core.InvoiceID) error
}
