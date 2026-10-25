package docgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Media types of the stored documents.
const (
	ContentTypePDF = "application/pdf"
	ContentTypeXML = "application/xml"
)

// CodeIsDraft is the rule code of a document asked for a draft invoice or
// credit note (lago not_allowed_failure!(code: "is_draft")).
const CodeIsDraft = "is_draft"

// ErrNoBlobStore reports a document that cannot be stored: no blob store
// is configured.
var ErrNoBlobStore = errors.New("docgen: no blob store configured")

// Renderer renders the documents. apps/server implements it with
// libs/documents; the XML methods render UBL, the PDFs embed the CII XML
// when the billing entity issues e-invoices.
type Renderer interface {
	// EInvoicing reports whether be issues e-invoices (lago
	// BillingEntity#eligible_for_einvoicing?).
	EInvoicing(be *organization.BillingEntity) bool
	// InvoicePDF renders the PDF of an invoice.
	InvoicePDF(ctx context.Context, d *Invoice) ([]byte, error)
	// InvoiceXML renders the UBL XML of an invoice.
	InvoiceXML(ctx context.Context, d *Invoice) ([]byte, error)
	// CreditNotePDF renders the PDF of a credit note.
	CreditNotePDF(ctx context.Context, d *CreditNote) ([]byte, error)
	// CreditNoteXML renders the UBL XML of a credit note.
	CreditNoteXML(ctx context.Context, d *CreditNote) ([]byte, error)
	// PaymentReceiptPDF renders the PDF of a payment receipt.
	PaymentReceiptPDF(ctx context.Context, d *PaymentReceipt) ([]byte, error)
	// PaymentReceiptXML renders the UBL XML of a payment receipt.
	PaymentReceiptXML(ctx context.Context, d *PaymentReceipt) ([]byte, error)
}

// Options configure the service.
type Options struct {
	// DisablePDF renders no PDFs (lago LAGO_DISABLE_PDF_GENERATION); the
	// XML files are still rendered.
	DisablePDF bool
}

// Service renders and stores documents.
type Service struct {
	d      *kernel.Deps
	blobs  port.BlobStore
	render Renderer
	fees   *fees.Service
	opts   Options
}

// New returns the service over the checked dependencies d. blobs and
// render may be nil: nothing is then rendered, and the document emails go
// without files.
func New(d *kernel.Deps, blobs port.BlobStore, render Renderer, opts Options) (*Service, error) {
	if err := d.Check(); err != nil {
		return nil, err
	}
	return &Service{d: d, blobs: blobs, render: render, fees: fees.New(d), opts: opts}, nil
}

// PDFsEnabled reports whether PDFs are rendered, stored and attached.
func (s *Service) PDFsEnabled() bool { return !s.opts.DisablePDF && s.blobs != nil && s.render != nil }

// canRender reports whether any document can be rendered.
func (s *Service) canRender() bool { return s.blobs != nil && s.render != nil }

// Options of a generation.
type genOptions struct {
	// force renders again existing files (lago's admin context).
	force bool
	// xml also renders the XML (the documents job; the mailers render the
	// PDF only).
	xml bool
}

// plan is what a generation renders.
type plan struct {
	xml, pdf bool
}

// decide applies lago's should_generate_xml? and should_generate_pdf?:
// the XML when forced or when missing for an e-invoicing billing entity,
// the PDF unless disabled, when forced or missing.
func (s *Service) decide(o genOptions, hasXML, hasPDF bool, be *organization.BillingEntity) plan {
	if !s.canRender() {
		return plan{}
	}
	return plan{
		xml: o.xml && (o.force || (!hasXML && s.render.EInvoicing(be))),
		pdf: !s.opts.DisablePDF && (o.force || !hasPDF),
	}
}

// put stores body as name.
func (s *Service) put(ctx context.Context, org core.OrganizationID, name, contentType string, body []byte) (core.BlobRef, error) {
	ref, err := s.blobs.Put(ctx, org, name, contentType, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("store %s: %w", name, err)
	}
	return ref, nil
}

// swap sets *slot to ref when ref is set and the slot is empty or force;
// it returns the ref to delete once committed: the replaced file, or ref
// when another run stored the file first.
func swap(slot *core.BlobRef, ref core.BlobRef, force bool) (stale core.BlobRef, set bool) {
	if ref.IsZero() {
		return "", false
	}
	if !slot.IsZero() && !force {
		return ref, false
	}
	stale, *slot = *slot, ref
	return stale, true
}

// cleanup deletes refs from the blob store; a failure only leaves an
// orphan file, so it is logged.
func (s *Service) cleanup(ctx context.Context, org core.OrganizationID, refs ...core.BlobRef) {
	for _, ref := range refs {
		if ref.IsZero() {
			continue
		}
		if err := s.blobs.Delete(ctx, org, ref); err != nil && !errors.Is(err, core.ErrNotFound) {
			s.d.Logger.WarnContext(ctx, "docgen: delete replaced file", "organization_id", org.String(), "ref", string(ref), "error", err)
		}
	}
}

// discard deletes the files stored by a generation that failed.
func (s *Service) discard(ctx context.Context, org core.OrganizationID, refs ...core.BlobRef) {
	s.cleanup(ctx, org, refs...)
}

// GenerateInvoice renders and stores the XML and the PDF of the invoice
// a.InvoiceID, announces the PDF with invoice.generated, and emails the
// invoice when a.Notify (the [kernel.JobGenerateInvoiceDocument] job). A
// draft fails with [CodeIsDraft].
//
// source: app/jobs/invoices/generate_documents_job.rb:20-25
// source: app/services/invoices/generate_pdf_service.rb:14-26, 92-96
// source: app/services/invoices/generate_xml_service.rb:14-23, 60-64
func (s *Service) GenerateInvoice(ctx context.Context, org core.OrganizationID, a kernel.InvoiceArgs) error {
	_, err := s.generateInvoice(ctx, org, a.InvoiceID, genOptions{force: a.Force, xml: true}, a.Notify)
	return err
}

// generateInvoice runs an invoice generation and returns the invoice as
// stored.
func (s *Service) generateInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID, o genOptions, notify bool) (*invoice.Invoice, error) {
	var (
		d  *Invoice
		pl plan
	)
	inv, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*invoice.Invoice, error) {
		inv, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", id, asNotFound(err, "invoice"))
		}
		if inv.Status == invoice.StatusDraft {
			return nil, core.NewRuleError(CodeIsDraft, "a draft invoice has no document")
		}
		be, err := r.BillingEntities.Get(ctx, org, inv.BillingEntityID)
		if err != nil {
			return nil, fmt.Errorf("load billing entity %s: %w", inv.BillingEntityID, err)
		}
		if pl = s.decide(o, !inv.XMLFile.IsZero(), !inv.File.IsZero(), be); pl.xml || pl.pdf {
			l := &loader{s: s, r: r, org: org}
			if d, err = l.invoiceData(ctx, inv); err != nil {
				return nil, err
			}
		}
		return inv, nil
	})
	if err != nil {
		return nil, err
	}
	var xmlRef, pdfRef core.BlobRef
	if pl.xml {
		body, err := s.render.InvoiceXML(ctx, d)
		if err != nil {
			return nil, fmt.Errorf("render xml of invoice %s: %w", id, err)
		}
		if xmlRef, err = s.put(ctx, org, inv.Number+".xml", ContentTypeXML, body); err != nil {
			return nil, err
		}
	}
	if pl.pdf {
		body, err := s.render.InvoicePDF(ctx, d)
		if err != nil {
			s.discard(ctx, org, xmlRef)
			return nil, fmt.Errorf("render pdf of invoice %s: %w", id, err)
		}
		if pdfRef, err = s.put(ctx, org, inv.Number+".pdf", ContentTypePDF, body); err != nil {
			s.discard(ctx, org, xmlRef)
			return nil, err
		}
	}
	var stale []core.BlobRef
	out, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*invoice.Invoice, error) {
		stale = stale[:0]
		inv, err := r.Invoices.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", id, err)
		}
		oldXML, setXML := swap(&inv.XMLFile, xmlRef, o.force)
		oldPDF, setPDF := swap(&inv.File, pdfRef, o.force)
		stale = append(stale, oldXML, oldPDF)
		if setXML || setPDF {
			inv.UpdatedAt = s.d.Now()
			if err := r.Invoices.Update(ctx, inv); err != nil {
				return nil, fmt.Errorf("save documents of invoice %s: %w", id, err)
			}
		}
		if setPDF {
			if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeInvoiceGenerated, "invoice", kernel.ObjectID(inv.ID), inv); err != nil {
				return nil, err
			}
			if err := s.d.LogInvoice(ctx, r, auditlog.ActivityInvoiceGenerated, inv); err != nil {
				return nil, err
			}
		}
		if notify {
			// invoices/generate_documents_job.rb:24, notify_job.rb:13-15.
			if err := enqueueEmail(ctx, r, org, kernel.DocumentEmailArgs{Type: kernel.DocumentInvoice, ID: inv.ID.UUID()}, s.d); err != nil {
				return nil, err
			}
		}
		return inv, nil
	})
	if err != nil {
		s.discard(ctx, org, xmlRef, pdfRef)
		return nil, err
	}
	s.cleanup(ctx, org, stale...)
	return out, nil
}

// GenerateCreditNote renders and stores the XML and the PDF of the
// finalized credit note a.CreditNoteID and announces the PDF with
// credit_note.generated (the [kernel.JobGenerateCreditNoteDocument] job).
// A credit note that is not finalized is not found.
//
// source: app/jobs/credit_notes/generate_documents_job.rb:20-23
// source: app/services/credit_notes/generate_pdf_service.rb:14-24, 86-90
// source: app/services/credit_notes/generate_xml_service.rb:14-24, 60-64
func (s *Service) GenerateCreditNote(ctx context.Context, org core.OrganizationID, a kernel.CreditNoteArgs) error {
	_, err := s.generateCreditNote(ctx, org, a.CreditNoteID, genOptions{force: a.Force, xml: true})
	return err
}

func (s *Service) generateCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID, o genOptions) (*creditnote.CreditNote, error) {
	var (
		d  *CreditNote
		pl plan
	)
	cn, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*creditnote.CreditNote, error) {
		cn, err := r.CreditNotes.Get(ctx, org, id)
		if err == nil && cn.Status != creditnote.StatusFinalized {
			err = core.ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("load credit note %s: %w", id, asNotFound(err, "credit_note"))
		}
		inv, err := r.Invoices.Get(ctx, org, cn.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", cn.InvoiceID, err)
		}
		be, err := r.BillingEntities.Get(ctx, org, inv.BillingEntityID)
		if err != nil {
			return nil, fmt.Errorf("load billing entity %s: %w", inv.BillingEntityID, err)
		}
		if pl = s.decide(o, !cn.XMLFile.IsZero(), !cn.File.IsZero(), be); pl.xml || pl.pdf {
			l := &loader{s: s, r: r, org: org}
			if d, err = l.creditNoteData(ctx, cn); err != nil {
				return nil, err
			}
		}
		return cn, nil
	})
	if err != nil {
		return nil, err
	}
	var xmlRef, pdfRef core.BlobRef
	if pl.xml {
		body, err := s.render.CreditNoteXML(ctx, d)
		if err != nil {
			return nil, fmt.Errorf("render xml of credit note %s: %w", id, err)
		}
		if xmlRef, err = s.put(ctx, org, cn.Number+".xml", ContentTypeXML, body); err != nil {
			return nil, err
		}
	}
	if pl.pdf {
		body, err := s.render.CreditNotePDF(ctx, d)
		if err != nil {
			s.discard(ctx, org, xmlRef)
			return nil, fmt.Errorf("render pdf of credit note %s: %w", id, err)
		}
		if pdfRef, err = s.put(ctx, org, cn.Number+".pdf", ContentTypePDF, body); err != nil {
			s.discard(ctx, org, xmlRef)
			return nil, err
		}
	}
	var stale []core.BlobRef
	out, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*creditnote.CreditNote, error) {
		stale = stale[:0]
		cn, err := r.CreditNotes.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load credit note %s: %w", id, err)
		}
		oldXML, setXML := swap(&cn.XMLFile, xmlRef, o.force)
		oldPDF, setPDF := swap(&cn.File, pdfRef, o.force)
		stale = append(stale, oldXML, oldPDF)
		if setXML || setPDF {
			cn.UpdatedAt = s.d.Now()
			if err := r.CreditNotes.Update(ctx, cn); err != nil {
				return nil, fmt.Errorf("save documents of credit note %s: %w", id, err)
			}
		}
		if setPDF {
			if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeCreditNoteGenerated, "credit_note", cn.ID.UUID(), cn); err != nil {
				return nil, err
			}
			if err := s.d.LogChange(ctx, r, kernel.Change{
				Type: auditlog.ActivityCreditNoteGenerated, OrganizationID: org, ResourceType: auditlog.ResourceCreditNote,
				ResourceID: cn.ID.UUID(), Object: cn, CustomerID: cn.CustomerID,
			}); err != nil {
				return nil, err
			}
		}
		return cn, nil
	})
	if err != nil {
		s.discard(ctx, org, xmlRef, pdfRef)
		return nil, err
	}
	s.cleanup(ctx, org, stale...)
	return out, nil
}

// GeneratePaymentReceipt renders and stores the XML and the PDF of the
// receipt a.PaymentReceiptID, announces the PDF with
// payment_receipt.generated, and emails the receipt when a.Notify (the
// [kernel.JobGeneratePaymentReceiptDocument] job).
//
// source: app/jobs/payment_receipts/generate_documents_job.rb:20-25
// source: app/services/payment_receipts/generate_pdf_service.rb:14-26, 79-83
// source: app/services/payment_receipts/generate_xml_service.rb:14-23, 59-63
func (s *Service) GeneratePaymentReceipt(ctx context.Context, org core.OrganizationID, a kernel.PaymentReceiptArgs) error {
	_, err := s.generateReceipt(ctx, org, a.PaymentReceiptID, genOptions{force: a.Force, xml: true}, a.Notify)
	return err
}

// PaymentReceiptPDF renders the PDF of the receipt id when it is missing
// and returns the receipt as stored (the receipt download, which renders
// at once).
//
// source: app/graphql/mutations/payment_receipts/download.rb:18-22
func (s *Service) PaymentReceiptPDF(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*payment.Receipt, error) {
	return s.generateReceipt(ctx, org, id, genOptions{}, false)
}

func (s *Service) generateReceipt(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID, o genOptions, notify bool) (*payment.Receipt, error) {
	var (
		d  *PaymentReceipt
		pl plan
	)
	rc, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Receipt, error) {
		rc, err := r.PaymentReceipts.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load payment receipt %s: %w", id, asNotFound(err, "payment_receipt"))
		}
		be, err := r.BillingEntities.Get(ctx, org, rc.BillingEntityID)
		if err != nil {
			return nil, fmt.Errorf("load billing entity %s: %w", rc.BillingEntityID, err)
		}
		if pl = s.decide(o, !rc.XMLFile.IsZero(), !rc.File.IsZero(), be); pl.xml || pl.pdf {
			l := &loader{s: s, r: r, org: org}
			if d, err = l.receiptData(ctx, rc); err != nil {
				return nil, err
			}
		}
		return rc, nil
	})
	if err != nil {
		return nil, err
	}
	var xmlRef, pdfRef core.BlobRef
	if pl.xml {
		body, err := s.render.PaymentReceiptXML(ctx, d)
		if err != nil {
			return nil, fmt.Errorf("render xml of payment receipt %s: %w", id, err)
		}
		if xmlRef, err = s.put(ctx, org, rc.Number+".xml", ContentTypeXML, body); err != nil {
			return nil, err
		}
	}
	if pl.pdf {
		body, err := s.render.PaymentReceiptPDF(ctx, d)
		if err != nil {
			s.discard(ctx, org, xmlRef)
			return nil, fmt.Errorf("render pdf of payment receipt %s: %w", id, err)
		}
		if pdfRef, err = s.put(ctx, org, rc.Number+".pdf", ContentTypePDF, body); err != nil {
			s.discard(ctx, org, xmlRef)
			return nil, err
		}
	}
	var stale []core.BlobRef
	out, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Receipt, error) {
		stale = stale[:0]
		rc, err := r.PaymentReceipts.Get(ctx, org, id)
		if err != nil {
			return nil, fmt.Errorf("load payment receipt %s: %w", id, err)
		}
		oldXML, setXML := swap(&rc.XMLFile, xmlRef, o.force)
		oldPDF, setPDF := swap(&rc.File, pdfRef, o.force)
		stale = append(stale, oldXML, oldPDF)
		if setXML || setPDF {
			rc.UpdatedAt = s.d.Now()
			if err := r.PaymentReceipts.Update(ctx, rc); err != nil {
				return nil, fmt.Errorf("save documents of payment receipt %s: %w", id, err)
			}
		}
		if setPDF {
			p, err := r.Payments.Get(ctx, org, rc.PaymentID)
			if err != nil {
				return nil, fmt.Errorf("load payment %s: %w", rc.PaymentID, err)
			}
			if err := kernel.Emit(ctx, s.d, org, webhook.EventTypePaymentReceiptGenerated, "payment_receipt", rc.ID.UUID(), rc); err != nil {
				return nil, err
			}
			if err := s.d.LogChange(ctx, r, kernel.Change{
				Type: auditlog.ActivityPaymentReceiptGenerated, OrganizationID: org, ResourceType: auditlog.ResourcePaymentReceipt,
				ResourceID: rc.ID.UUID(), Object: rc, CustomerID: p.CustomerID,
			}); err != nil {
				return nil, err
			}
		}
		if notify {
			// payment_receipts/generate_documents_job.rb:24,
			// notify_job.rb:13-15.
			if err := enqueueEmail(ctx, r, org, kernel.DocumentEmailArgs{Type: kernel.DocumentPaymentReceipt, ID: rc.ID.UUID()}, s.d); err != nil {
				return nil, err
			}
		}
		return rc, nil
	})
	if err != nil {
		s.discard(ctx, org, xmlRef, pdfRef)
		return nil, err
	}
	s.cleanup(ctx, org, stale...)
	return out, nil
}

// enqueueEmail enqueues a [kernel.JobSendDocumentEmail].
func enqueueEmail(ctx context.Context, r port.Repos, org core.OrganizationID, a kernel.DocumentEmailArgs, d *kernel.Deps) error {
	return kernel.Enqueue(ctx, r, org, kernel.JobSendDocumentEmail, a, "", d.Now())
}

// read returns the content of ref, nil when it is unset or gone.
func (s *Service) read(ctx context.Context, org core.OrganizationID, ref core.BlobRef) ([]byte, error) {
	if ref.IsZero() || s.blobs == nil {
		return nil, nil
	}
	rc, err := s.blobs.Get(ctx, org, ref)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	return b, nil
}

// FileURL returns the permanent link of ref, empty when it is unset or no
// blob store is configured.
func (s *Service) FileURL(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (string, error) {
	if ref.IsZero() || s.blobs == nil {
		return "", nil
	}
	u, err := s.blobs.URL(ctx, org, ref)
	if errors.Is(err, core.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("link file: %w", err)
	}
	return u, nil
}

// inTx runs f in a transaction and returns its value.
func inTx[T any](ctx context.Context, tx port.Transactor, f func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := f(ctx, r)
		out = v
		return err
	})
	return out, err
}

// asNotFound turns core.ErrNotFound into the not found error of resource.
func asNotFound(err error, resource string) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(resource)
	}
	return err
}
