package docgen

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Sender is who sends a document email and to whom by default.
type Sender struct {
	// BillingEntity issues the document.
	BillingEntity *organization.BillingEntity
	// Customer receives it.
	Customer *customer.Customer
	// FromEmailEnabled sends from the billing entity's email
	// ([organization.Organization.FromEmailEnabled]).
	FromEmailEnabled bool
	// Locale is the customer's applicable document locale.
	Locale string
}

// sender loads the sender of a document issued by be to cust.
func sender(ctx context.Context, r port.Repos, org core.OrganizationID, be core.BillingEntityID, cust core.CustomerID) (Sender, error) {
	env, err := kernel.LoadCustomerEnv(ctx, r, org, cust, be)
	if err != nil {
		return Sender{}, err
	}
	o, err := r.Organizations.Get(ctx, org)
	if err != nil {
		return Sender{}, fmt.Errorf("load organization %s: %w", org, err)
	}
	return Sender{
		BillingEntity: env.BillingEntity, Customer: env.Customer, FromEmailEnabled: o.FromEmailEnabled,
		Locale: env.Customer.ApplicableDocumentLocale(env.Defaults),
	}, nil
}

// Attachment is a document file of an email.
type Attachment struct {
	// PDF is the file, nil when PDFs are disabled or it is not rendered.
	PDF []byte
	// FileURL is its permanent link, empty when it is not rendered.
	FileURL string
}

// attachment reads ref and links it.
func (s *Service) attachment(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (Attachment, error) {
	var a Attachment
	var err error
	if a.FileURL, err = s.FileURL(ctx, org, ref); err != nil {
		return a, err
	}
	if s.PDFsEnabled() {
		if a.PDF, err = s.read(ctx, org, ref); err != nil {
			return a, err
		}
	}
	return a, nil
}

// ensure ignores the failure of a mailer's ensure_pdf, which calls the
// PDF service without raising (lago InvoiceMailer#ensure_pdf and
// CreditNoteMailer#ensure_pdf): a document that cannot be rendered is
// emailed without its file.
func ensure(err error) error {
	if errors.Is(err, core.ErrNotAllowed) || errors.Is(err, core.ErrNotFound) {
		return nil
	}
	return err
}

// InvoiceEmail is the data of the email of an invoice.
type InvoiceEmail struct {
	Sender
	Attachment
	// Invoice is the invoice.
	Invoice *invoice.Invoice
}

// InvoiceEmail renders the PDF of the invoice id when it is missing and
// returns the data of its email; nil when the invoice is gone.
//
// source: app/mailers/invoice_mailer.rb:4-17
func (s *Service) InvoiceEmail(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*InvoiceEmail, error) {
	if _, err := s.generateInvoice(ctx, org, id, genOptions{}, false); ensure(err) != nil {
		return nil, err
	}
	e, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*InvoiceEmail, error) {
		inv, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			return nil, err
		}
		snd, err := sender(ctx, r, org, inv.BillingEntityID, inv.CustomerID)
		if err != nil {
			return nil, err
		}
		return &InvoiceEmail{Sender: snd, Invoice: inv}, nil
	})
	if errors.Is(err, core.ErrNotFound) && e == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if e.Attachment, err = s.attachment(ctx, org, e.Invoice.File); err != nil {
		return nil, err
	}
	return e, nil
}

// CreditNoteEmail is the data of the email of a credit note.
type CreditNoteEmail struct {
	Sender
	Attachment
	// CreditNote is the credit note.
	CreditNote *creditnote.CreditNote
	// Invoice is the credited invoice.
	Invoice *invoice.Invoice
}

// CreditNoteEmail renders the PDF of the credit note id when it is
// missing and returns the data of its email; nil when the credit note is
// gone. The billing entity is the credited invoice's.
//
// source: app/mailers/credit_note_mailer.rb:4-17
func (s *Service) CreditNoteEmail(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) (*CreditNoteEmail, error) {
	if _, err := s.generateCreditNote(ctx, org, id, genOptions{}); ensure(err) != nil {
		return nil, err
	}
	e, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*CreditNoteEmail, error) {
		cn, err := r.CreditNotes.Get(ctx, org, id)
		if err != nil {
			return nil, err
		}
		inv, err := r.Invoices.Get(ctx, org, cn.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", cn.InvoiceID, err)
		}
		snd, err := sender(ctx, r, org, inv.BillingEntityID, cn.CustomerID)
		if err != nil {
			return nil, err
		}
		return &CreditNoteEmail{Sender: snd, CreditNote: cn, Invoice: inv}, nil
	})
	if errors.Is(err, core.ErrNotFound) && e == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if e.Attachment, err = s.attachment(ctx, org, e.CreditNote.File); err != nil {
		return nil, err
	}
	return e, nil
}

// ReceiptInvoice is an invoice settled by a receipted payment.
type ReceiptInvoice struct {
	Attachment
	// Invoice is the invoice.
	Invoice *invoice.Invoice
}

// PaymentReceiptEmail is the data of the email of a payment receipt.
type PaymentReceiptEmail struct {
	Sender
	Attachment
	// Receipt is the receipt.
	Receipt *payment.Receipt
	// Payment is the receipted payment.
	Payment *payment.Payment
	// TotalDueAmount is what remains to pay on the payable, in minor
	// units: the invoice's total due, or the request's amount less the
	// payment.
	TotalDueAmount int64
	// Invoices are the invoices the payment settles.
	Invoices []ReceiptInvoice
}

// PaymentReceiptEmail renders the PDF of the receipt id when it is
// missing and returns the data of its email; nil when the receipt is
// gone. The invoices' PDFs are not rendered here: the composer reports a
// missing one as not ready, and the email is retried.
//
// source: app/mailers/payment_receipt_mailer.rb:4-36
func (s *Service) PaymentReceiptEmail(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*PaymentReceiptEmail, error) {
	if _, err := s.generateReceipt(ctx, org, id, genOptions{}, false); ensure(err) != nil {
		return nil, err
	}
	e, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*PaymentReceiptEmail, error) {
		rc, err := r.PaymentReceipts.Get(ctx, org, id)
		if err != nil {
			return nil, err
		}
		p, err := r.Payments.Get(ctx, org, rc.PaymentID)
		if err != nil {
			return nil, fmt.Errorf("load payment %s: %w", rc.PaymentID, err)
		}
		snd, err := sender(ctx, r, org, rc.BillingEntityID, p.CustomerID)
		if err != nil {
			return nil, err
		}
		e := &PaymentReceiptEmail{Sender: snd, Receipt: rc, Payment: p}
		if p.Payable.Type == payment.PayableTypePaymentRequest {
			q, err := r.PaymentRequests.Get(ctx, org, p.Payable.PaymentRequestID)
			if err != nil {
				return nil, fmt.Errorf("load payment request %s: %w", p.Payable.PaymentRequestID, err)
			}
			e.TotalDueAmount = q.Amount - p.Amount
			for _, iid := range q.InvoiceIDs {
				inv, err := r.Invoices.Get(ctx, org, iid)
				if err != nil {
					return nil, fmt.Errorf("load invoice %s: %w", iid, err)
				}
				e.Invoices = append(e.Invoices, ReceiptInvoice{Invoice: inv})
			}
			return e, nil
		}
		inv, err := r.Invoices.Get(ctx, org, p.Payable.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", p.Payable.InvoiceID, err)
		}
		e.TotalDueAmount = inv.TotalDueAmount()
		e.Invoices = []ReceiptInvoice{{Invoice: inv}}
		return e, nil
	})
	if errors.Is(err, core.ErrNotFound) && e == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if e.Attachment, err = s.attachment(ctx, org, e.Receipt.File); err != nil {
		return nil, err
	}
	for i := range e.Invoices {
		if e.Invoices[i].Attachment, err = s.attachment(ctx, org, e.Invoices[i].Invoice.File); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// InvoiceLink renders the PDF of the invoice id when it is missing and
// returns its permanent link, empty when it has none (lago
// PaymentRequestMailer#ensure_invoices_pdf and Invoice#file_url).
//
// source: app/mailers/payment_request_mailer.rb:36-40
func (s *Service) InvoiceLink(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (string, error) {
	inv, err := s.generateInvoice(ctx, org, id, genOptions{}, false)
	if ensure(err) != nil {
		return "", err
	}
	if inv == nil {
		return "", nil
	}
	return s.FileURL(ctx, org, inv.File)
}

// ResendTarget is a document to email again, with what lago's
// Emails::ResendService checks.
type ResendTarget struct {
	Sender
	// Number is the document number.
	Number string
	// Finalized tells whether an invoice or credit note is finalized.
	Finalized bool
	// FeesAmount is an invoice's fees amount in currency units.
	FeesAmount money.Decimal
}

// ResendInvoice returns the visible invoice id to email again; any other
// is not found.
//
// source: app/controllers/api/v1/invoices_controller.rb:180-182
func (s *Service) ResendInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (*ResendTarget, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*ResendTarget, error) {
		inv, err := r.Invoices.Get(ctx, org, id)
		if err == nil && !inv.Visible() {
			err = core.ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", id, asNotFound(err, "invoice"))
		}
		snd, err := sender(ctx, r, org, inv.BillingEntityID, inv.CustomerID)
		if err != nil {
			return nil, err
		}
		return &ResendTarget{
			Sender: snd, Number: inv.Number, Finalized: inv.Status == invoice.StatusFinalized,
			FeesAmount: inv.Currency.FromMinor(inv.FeesAmount),
		}, nil
	})
}

// ResendCreditNote returns the credit note id to email again.
//
// source: app/services/emails/resend_service.rb:50-58
func (s *Service) ResendCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID) (*ResendTarget, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*ResendTarget, error) {
		cn, err := r.CreditNotes.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load credit note %s: %w", id, asNotFound(err, "credit_note"))
		}
		inv, err := r.Invoices.Get(ctx, org, cn.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", cn.InvoiceID, err)
		}
		snd, err := sender(ctx, r, org, inv.BillingEntityID, cn.CustomerID)
		if err != nil {
			return nil, err
		}
		return &ResendTarget{Sender: snd, Number: cn.Number, Finalized: cn.Status == creditnote.StatusFinalized}, nil
	})
}

// ResendPaymentReceipt returns the payment receipt id to email again; its
// customer is the payment's.
//
// source: app/services/emails/resend_service.rb:60-64
func (s *Service) ResendPaymentReceipt(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*ResendTarget, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*ResendTarget, error) {
		rc, err := r.PaymentReceipts.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load payment receipt %s: %w", id, asNotFound(err, "payment_receipt"))
		}
		p, err := r.Payments.Get(ctx, org, rc.PaymentID)
		if err != nil {
			return nil, fmt.Errorf("load payment %s: %w", rc.PaymentID, err)
		}
		snd, err := sender(ctx, r, org, rc.BillingEntityID, p.CustomerID)
		if err != nil {
			return nil, err
		}
		return &ResendTarget{Sender: snd, Number: rc.Number, Finalized: true}, nil
	})
}

// EnqueueEmail enqueues the email a ([kernel.JobSendDocumentEmail]).
func (s *Service) EnqueueEmail(ctx context.Context, org core.OrganizationID, a kernel.DocumentEmailArgs) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return enqueueEmail(ctx, r, org, a, s.d)
	})
}
