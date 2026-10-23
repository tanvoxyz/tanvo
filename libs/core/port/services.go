package port

import (
	"context"
	"io"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Clock is the time source; see [core.Clock].
type Clock = core.Clock

// IDGenerator generates row identifiers; see [core.IDGenerator].
type IDGenerator = core.IDGenerator

// DocumentKind names a rendered document (architecture §10).
type DocumentKind string

// Document kinds.
const (
	// DocumentInvoice is an invoice PDF.
	DocumentInvoice DocumentKind = "invoice"
	// DocumentCreditNote is a credit note PDF.
	DocumentCreditNote DocumentKind = "credit_note"
	// DocumentPaymentReceipt is a payment receipt PDF.
	DocumentPaymentReceipt DocumentKind = "payment_receipt"
	// DocumentOrderForm is an order form PDF.
	DocumentOrderForm DocumentKind = "order_form"
	// DocumentEInvoice is an e-invoice XML (Factur-X / ZUGFeRD).
	DocumentEInvoice DocumentKind = "einvoice"
)

// Document is a rendered file.
type Document struct {
	// ContentType is the media type ("application/pdf", "application/xml").
	ContentType string
	// Body is the file content.
	Body []byte
}

// DocumentRenderer renders documents from their template data. libs/documents
// implements it with Typst.
type DocumentRenderer interface {
	// Render renders a document of kind k in locale from data, the
	// JSON-serializable view model of the document.
	Render(ctx context.Context, k DocumentKind, locale string, data any) (Document, error)
}

// BlobStore keeps files (logos, documents, exports).
type BlobStore interface {
	// Put stores the content under a new reference.
	Put(ctx context.Context, org core.OrganizationID, name, contentType string, body io.Reader) (core.BlobRef, error)
	// Get opens a stored file; a missing file is [core.ErrNotFound].
	Get(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (io.ReadCloser, error)
	// Delete removes a stored file; deleting a missing file succeeds.
	Delete(ctx context.Context, org core.OrganizationID, ref core.BlobRef) error
	// URL returns a time-limited download URL of a stored file.
	URL(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (string, error)
}

// Attachment is a file attached to an email.
type Attachment struct {
	// Name is the file name.
	Name string
	// ContentType is the media type.
	ContentType string
	// Body is the file content.
	Body []byte
}

// Email is one message to send.
type Email struct {
	// From is the sender address.
	From string
	// ReplyTo is the optional reply address.
	ReplyTo string
	// To are the recipients.
	To []string
	// Bcc are the hidden recipients (dunning campaigns' bcc_emails).
	Bcc []string
	// Subject is the subject line.
	Subject string
	// Text is the plain-text body.
	Text string
	// HTML is the HTML body.
	HTML string
	// Attachments are the attached files.
	Attachments []Attachment
}

// Mailer sends emails.
type Mailer interface {
	// Send sends one email.
	Send(ctx context.Context, org core.OrganizationID, e Email) error
}

// WebhookEmitter records a domain event in the current transaction and
// schedules its delivery to every subscribed endpoint after commit
// (domain §2.3, §18).
type WebhookEmitter interface {
	// Emit records e and enqueues its deliveries.
	Emit(ctx context.Context, e webhook.Event) error
}
