package rpc

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
)

// Recipients are the custom recipients of a resent document email.
type Recipients struct {
	// To replaces the customer's addresses when set.
	To []string
	// Cc are copied.
	Cc []string
	// Bcc are hidden.
	Bcc []string
}

// DocumentFiles links the stored documents, renders a receipt on demand and
// emails documents again; apps/server implements it over the document
// generator, the blob store and the mailer.
type DocumentFiles interface {
	// FileURL returns the permanent link of a stored file, empty when it
	// has none.
	FileURL(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (string, error)
	// PaymentReceipt renders the PDF of the receipt id when it is missing
	// and returns the receipt.
	PaymentReceipt(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*payment.Receipt, error)
	// ResendInvoice emails the invoice id again.
	ResendInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID, to Recipients) error
	// ResendCreditNote emails the credit note id again.
	ResendCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID, to Recipients) error
	// ResendPaymentReceipt emails the payment receipt id again.
	ResendPaymentReceipt(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID, to Recipients) error
}

// documentPair returns the PDF and XML documents of the files pdf and xml:
// a set file is returned with its media type and, when docs is set, its
// permanent link (no expiry).
func documentPair(ctx context.Context, docs DocumentFiles, org core.OrganizationID, pdf, xml core.BlobRef) (*commonv1.Document, *commonv1.Document, error) {
	one := func(ref core.BlobRef, contentType string) (*commonv1.Document, error) {
		if ref.IsZero() {
			return nil, nil
		}
		b := commonv1.Document_builder{ContentType: new(contentType)}
		if docs != nil {
			u, err := docs.FileURL(ctx, org, ref)
			if err != nil {
				return nil, err
			}
			if u != "" {
				b.Url = new(u)
			}
		}
		return b.Build(), nil
	}
	p, err := one(pdf, "application/pdf")
	if err != nil {
		return nil, nil, err
	}
	x, err := one(xml, "application/xml")
	if err != nil {
		return nil, nil, err
	}
	return p, x, nil
}
