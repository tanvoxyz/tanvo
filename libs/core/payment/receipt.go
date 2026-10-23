package payment

import (
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Receipt is the document issued for a succeeded payment (domain §15.2);
// one per payment.
type Receipt struct {
	// ID identifies the receipt.
	ID core.PaymentReceiptID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// PaymentID is the receipted payment.
	PaymentID core.PaymentID
	// BillingEntityID is the issuing billing entity.
	BillingEntityID core.BillingEntityID
	// Number is "<customer slug>-RCPT-<counter %06d>" (§22.11).
	Number string
	// File references the rendered PDF.
	File core.BlobRef
	// XMLFile references the e-invoicing XML, when produced.
	XMLFile core.BlobRef
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// FormatReceiptNumber returns the number of a customer's counter-th
// receipt: "<customer slug>-RCPT-<counter %06d>".
func FormatReceiptNumber(customerSlug string, counter int) string {
	return fmt.Sprintf("%s-RCPT-%06d", customerSlug, counter)
}

// Validate checks that the receipt has a payment and a number.
func (r *Receipt) Validate() error {
	var v core.Validator
	v.Check(!r.PaymentID.IsZero(), "payment_id", core.CodeRequired)
	v.String("number", r.Number, 255)
	return v.Err()
}
