package documents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/documents/einvoice"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/pdfa"
	"github.com/tanvoxyz/tanvo/libs/documents/internal/typst"
)

// Content types of rendered documents.
const (
	// ContentTypePDF is the media type of PDF documents.
	ContentTypePDF = "application/pdf"
	// ContentTypeXML is the media type of e-invoices.
	ContentTypeXML = "application/xml"
)

// ErrUnsupported reports a document kind or data type the renderer does not
// render.
var ErrUnsupported = errors.New("documents: unsupported document")

// Options configure a [Renderer].
type Options struct {
	// Typst is the typst executable; "typst" in PATH when empty.
	Typst string
	// Dir is the parent of the renderer's private directory (templates,
	// fonts and jobs); the system temporary directory when empty.
	Dir string
	// FontPaths are font directories for scripts the embedded Inter does
	// not cover: zh-CN and zh-TW documents need a Noto CJK font (fonts-noto-cjk), and customers'
	// names and addresses may use other scripts. Nil means the system font
	// directories (typst.SystemFontDirs); an empty non-nil slice adds none.
	FontPaths []string
	// Logger receives warnings; slog.Default() when nil.
	Logger *slog.Logger
}

// Renderer renders invoices, credit notes, payment receipts and payment
// requests as PDF/A-3b with Typst, and e-invoices as XML. It implements
// [port.DocumentRenderer]. A Renderer is safe for concurrent use.
type Renderer struct {
	cat    *i18n.Catalog
	engine *typst.Engine
}

var _ port.DocumentRenderer = (*Renderer)(nil)

// New starts a renderer: it loads the translations and writes the
// templates and fonts to a private directory. Close removes it.
func New(opts Options) (*Renderer, error) {
	cat, err := i18n.Load()
	if err != nil {
		return nil, fmt.Errorf("documents: %w", err)
	}
	e, err := typst.New(typst.Options{Binary: opts.Typst, Dir: opts.Dir, FontPaths: opts.FontPaths, Logger: opts.Logger})
	if err != nil {
		return nil, fmt.Errorf("documents: %w", err)
	}
	return &Renderer{cat: cat, engine: e}, nil
}

// Close removes the renderer's private directory.
func (r *Renderer) Close() error { return r.engine.Close() }

// Locales returns the supported document locales.
func (r *Renderer) Locales() []string { return r.cat.Locales() }

// Render renders a document of kind k from data, which is *[Invoice] for
// invoice and einvoice, *[CreditNote] for credit_note, *[PaymentReceipt]
// for payment_receipt and *[PaymentRequest] for payment_request. An empty
// locale selects the customer's applicable document locale; an unknown one
// falls back to English. The einvoice kind returns the invoice's CII XML.
func (r *Renderer) Render(ctx context.Context, k port.DocumentKind, locale string, data any) (port.Document, error) {
	var body []byte
	var err error
	switch k {
	case port.DocumentInvoice:
		d, ok := data.(*Invoice)
		if !ok {
			return port.Document{}, fmt.Errorf("%w: %s data %T", ErrUnsupported, k, data)
		}
		body, err = r.InvoicePDF(ctx, d, locale)
	case port.DocumentCreditNote:
		d, ok := data.(*CreditNote)
		if !ok {
			return port.Document{}, fmt.Errorf("%w: %s data %T", ErrUnsupported, k, data)
		}
		body, err = r.CreditNotePDF(ctx, d, locale)
	case port.DocumentPaymentReceipt:
		d, ok := data.(*PaymentReceipt)
		if !ok {
			return port.Document{}, fmt.Errorf("%w: %s data %T", ErrUnsupported, k, data)
		}
		body, err = r.PaymentReceiptPDF(ctx, d, locale)
	case port.DocumentPaymentRequest:
		// lago-api has no such document: a payment request is only an
		// email (app/views/payment_request_mailer/requested.slim) to which
		// the requested invoices' own PDFs are attached
		// (app/mailers/payment_request_mailer.rb:4, :36). Tanvo adds the
		// PDF with the email's wording.
		d, ok := data.(*PaymentRequest)
		if !ok {
			return port.Document{}, fmt.Errorf("%w: %s data %T", ErrUnsupported, k, data)
		}
		body, err = r.PaymentRequestPDF(ctx, d, locale)
	case port.DocumentEInvoice:
		d, ok := data.(*Invoice)
		if !ok {
			return port.Document{}, fmt.Errorf("%w: %s data %T", ErrUnsupported, k, data)
		}
		body, err = r.InvoiceXML(d, locale, CII)
		if err != nil {
			return port.Document{}, err
		}
		return port.Document{ContentType: ContentTypeXML, Body: body}, nil
	default:
		return port.Document{}, fmt.Errorf("%w: kind %q", ErrUnsupported, k)
	}
	if err != nil {
		return port.Document{}, err
	}
	return port.Document{ContentType: ContentTypePDF, Body: body}, nil
}

// InvoicePDF renders an invoice. When the billing entity issues
// e-invoices, its CII XML is attached as a Factur-X/ZUGFeRD invoice.
func (r *Renderer) InvoicePDF(ctx context.Context, d *Invoice, locale string) ([]byte, error) {
	w := newWriter(r.cat, &d.Party, locale, d.Invoice.Currency)
	l := w.invoiceLayout(d)
	var xml []byte
	if einvoice.Eligible(&d.BillingEntity) {
		var err error
		if xml, err = einvoice.InvoiceCII(w.einvoiceInvoice(d)); err != nil {
			return nil, fmt.Errorf("documents: invoice %s: %w", d.Invoice.ID, err)
		}
	}
	return r.compile(ctx, l, &d.Party, xml, "INVOICE")
}

// CreditNotePDF renders a credit note, with its Factur-X XML attached when
// the billing entity issues e-invoices.
func (r *Renderer) CreditNotePDF(ctx context.Context, d *CreditNote, locale string) ([]byte, error) {
	w := newWriter(r.cat, &d.Party, locale, d.CreditNote.Currency)
	l := w.creditNoteLayout(d)
	var xml []byte
	if einvoice.Eligible(&d.BillingEntity) {
		var err error
		if xml, err = einvoice.CreditNoteCII(w.einvoiceCreditNote(d)); err != nil {
			return nil, fmt.Errorf("documents: credit note %s: %w", d.CreditNote.ID, err)
		}
	}
	return r.compile(ctx, l, &d.Party, xml, "INVOICE")
}

// PaymentReceiptPDF renders a payment receipt.
func (r *Renderer) PaymentReceiptPDF(ctx context.Context, d *PaymentReceipt, locale string) ([]byte, error) {
	w := newWriter(r.cat, &d.Party, locale, d.Payment.Currency)
	return r.compile(ctx, w.receiptLayout(d), &d.Party, nil, "")
}

// PaymentRequestPDF renders a payment request.
func (r *Renderer) PaymentRequestPDF(ctx context.Context, d *PaymentRequest, locale string) ([]byte, error) {
	w := newWriter(r.cat, &d.Party, locale, d.Request.Currency)
	return r.compile(ctx, w.paymentRequestLayout(d), &d.Party, nil, "")
}

func (r *Renderer) compile(ctx context.Context, l *layout, p *Party, xml []byte, docType string) ([]byte, error) {
	if err := r.checkFonts(l.locale); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	if l.Logo != "" {
		ext, ok := imageExtensions[p.Logo.ContentType]
		if !ok {
			return nil, fmt.Errorf("%w: logo content type %q", ErrUnsupported, p.Logo.ContentType)
		}
		l.Logo = "logo" + ext
		files[l.Logo] = p.Logo.Data
	}
	data, err := json.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("documents: encode layout: %w", err)
	}
	inputs := map[string]string{}
	if xml != nil {
		files[facturXName] = xml
		inputs["facturx"] = facturXName
	}
	res, err := r.engine.Compile(ctx, typst.Job{Template: "document.typ", Data: data, Files: files, Inputs: inputs, PDFA: true})
	if err != nil {
		return nil, fmt.Errorf("documents: %w", err)
	}
	if xml == nil || !res.PDFA {
		return res.PDF, nil
	}
	out, err := pdfa.AddFacturX(res.PDF, pdfa.FacturX{DocumentType: docType, FileName: facturXName, Version: "1.0", ConformanceLevel: "EN 16931"})
	if err != nil {
		return nil, fmt.Errorf("documents: %w", err)
	}
	return out, nil
}

const facturXName = "factur-x.xml"

var imageExtensions = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
	"image/svg+xml": ".svg",
	"image/webp":    ".webp",
}

// Format is an e-invoice syntax.
type Format string

// E-invoice formats.
const (
	// CII is UN/CEFACT Cross Industry Invoice (Factur-X, ZUGFeRD).
	CII Format = "cii"
	// UBL is OASIS UBL 2.1 (Peppol, XRechnung).
	UBL Format = "ubl"
)

// InvoiceXML returns the invoice's e-invoice in format; locale selects the
// language of its texts as for the PDF.
func (r *Renderer) InvoiceXML(d *Invoice, locale string, f Format) ([]byte, error) {
	w := newWriter(r.cat, &d.Party, locale, d.Invoice.Currency)
	in := w.einvoiceInvoice(d)
	switch f {
	case CII:
		return einvoice.InvoiceCII(in)
	case UBL:
		return einvoice.InvoiceUBL(in)
	}
	return nil, fmt.Errorf("%w: format %q", ErrUnsupported, f)
}

// CreditNoteXML returns the credit note's e-invoice in format.
func (r *Renderer) CreditNoteXML(d *CreditNote, locale string, f Format) ([]byte, error) {
	w := newWriter(r.cat, &d.Party, locale, d.CreditNote.Currency)
	in := w.einvoiceCreditNote(d)
	switch f {
	case CII:
		return einvoice.CreditNoteCII(in)
	case UBL:
		return einvoice.CreditNoteUBL(in)
	}
	return nil, fmt.Errorf("%w: format %q", ErrUnsupported, f)
}

// PaymentReceiptXML returns the receipt's e-invoice in format; the payment
// means are the provider of the payment's connection ("stripe", …; empty for
// manual payments).
func (r *Renderer) PaymentReceiptXML(d *PaymentReceipt, locale string, f Format) ([]byte, error) {
	w := newWriter(r.cat, &d.Party, locale, d.Payment.Currency)
	in := &einvoice.Payment{
		Payment: &d.Payment, Receipt: &d.Receipt, BillingEntity: &d.BillingEntity, Customer: &d.Customer,
		ProviderType: d.providerType(), Labels: w.einvoiceLabels(),
	}
	switch {
	case d.Invoice != nil:
		in.Invoices = append(in.Invoices, &d.Invoice.Invoice)
	case d.Request != nil:
		for i := range d.Request.Invoices {
			in.Invoices = append(in.Invoices, &d.Request.Invoices[i])
		}
	}
	switch f {
	case CII:
		return einvoice.PaymentCII(in)
	case UBL:
		return einvoice.PaymentUBL(in)
	}
	return nil, fmt.Errorf("%w: format %q", ErrUnsupported, f)
}

func (w *writer) einvoiceLabels() einvoice.Labels {
	t := w.l.T
	return einvoice.Labels{
		StandardPayment:       t("invoice.e_invoicing.standard_payment"),
		PrepaidCredits:        t("invoice.prepaid_credits"),
		CreditNotes:           t("invoice.credit_notes"),
		CreditCard:            t("invoice.e_invoicing.credit_card"),
		PaymentInformation:    t("invoice.e_invoicing.payment_information"),
		CreditCardInformation: t("invoice.e_invoicing.credit_card_information"),
		DiscountReason:        t("invoice.e_invoicing.discount_reason"),
		PaymentTerm:           t("invoice.payment_term"),
		PaymentTermDays:       t("invoice.payment_term_days"),
		WordsConnector:        t("support.array.words_connector"),
		TwoWordsConnector:     t("support.array.two_words_connector"),
		LastWordConnector:     t("support.array.last_word_connector"),
	}
}

// lineDescription is the e-invoice line description (BaseSerializer#fee_description).
func (w *writer) lineDescription(f *Fee) string {
	if s := w.invoiceName(f); s != "" {
		return s
	}
	if s := w.subscription(f); s != nil {
		return w.l.T("invoice.subscription_interval",
			i18n.A("plan_interval", w.l.T("invoice."+s.Plan.Interval.String())), i18n.A("plan_name", s.Plan.InvoiceName()))
	}
	return ""
}

func (w *writer) einvoiceInvoice(d *Invoice) *einvoice.Invoice {
	for i := range d.Subscriptions {
		w.subs[d.Subscriptions[i].Subscription.ID] = &d.Subscriptions[i]
	}
	in := &einvoice.Invoice{
		Invoice: &d.Invoice, BillingEntity: &d.BillingEntity, Customer: &d.Customer,
		PurchaseOrderNumber: deref(d.Invoice.PurchaseOrderNumber), Labels: w.einvoiceLabels(),
		ProviderTaxes: providerTaxes(d.AppliedTaxes), DeliveryDate: deliveryDate(d),
	}
	for i := range d.Fees {
		f := &d.Fees[i]
		in.Lines = append(in.Lines, einvoice.Line{Fee: f.Fee, Name: w.itemName(f), Description: w.lineDescription(f)})
	}
	return in
}

// deref returns *p, or "" when p is nil.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// providerTaxes reports whether a tax provider computed the taxes: an
// applied tax without a tax and with a positive taxable base
// (Invoice::AppliedTax#provider_tax?, app/models/invoice/applied_tax.rb:31-33).
func providerTaxes(taxes []invoice.AppliedTax) bool {
	for _, t := range taxes {
		if t.IsProviderTax() {
			return true
		}
	}
	return false
}

// deliveryDate is the earliest start of the invoice's charges periods (UTC
// date), zero for invoices without subscriptions.
func deliveryDate(d *Invoice) core.Date {
	var first time.Time
	for _, s := range d.Subscriptions {
		from := s.InvoiceSubscription.ChargesPeriod.From
		if from.IsZero() {
			continue
		}
		if first.IsZero() || from.Before(first) {
			first = from
		}
	}
	if first.IsZero() {
		return core.Date{}
	}
	return core.DateOf(first, time.UTC)
}

func (w *writer) einvoiceCreditNote(d *CreditNote) *einvoice.CreditNote {
	for i := range d.Subscriptions {
		w.subs[d.Subscriptions[i].Subscription.ID] = &d.Subscriptions[i]
	}
	fees := map[core.FeeID]*Fee{}
	for i := range d.Fees {
		fees[d.Fees[i].Fee.ID] = &d.Fees[i]
	}
	in := &einvoice.CreditNote{
		CreditNote: &d.CreditNote, Invoice: &d.Invoice, BillingEntity: &d.BillingEntity, Customer: &d.Customer,
		PurchaseOrderNumber: deref(d.Invoice.PurchaseOrderNumber), Labels: w.einvoiceLabels(),
		ProviderTaxes: providerTaxes(d.InvoiceAppliedTaxes),
	}
	for _, it := range d.CreditNote.Items {
		f := fees[it.FeeID]
		if f == nil {
			continue
		}
		in.Lines = append(in.Lines, einvoice.CreditNoteLine{Item: it, Fee: f.Fee, Name: w.itemName(f), Description: w.lineDescription(f)})
	}
	return in
}
