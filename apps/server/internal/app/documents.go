package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/textproto"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/billing/docgen"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/documents"
	"github.com/tanvoxyz/tanvo/libs/documents/einvoice"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/mail"
)

// Attempts of the document jobs (lago SendEmailJob and the
// GenerateDocumentsJob classes).
const (
	// renderAttempts bounds a generation failing on the renderer or the
	// blob store (generate_documents_job.rb:13-18: HTTP errors, refused
	// connections, timeouts and EOF, 6 attempts polynomially longer).
	renderAttempts = jobs.HTTPRetryAttempts
	// filesNotReadyAttempts bounds a receipt email waiting for its files
	// (send_email_job.rb:41).
	filesNotReadyAttempts = 8
	// smtpBusyAttempts bounds an email refused with a 4xx reply
	// (send_email_job.rb:40, Net::SMTPServerBusy).
	smtpBusyAttempts = 25
)

// docs wires the document generator: the rendering and email jobs and the
// files of the RPCs.
type docs struct {
	gen      *docgen.Service
	d        *kernel.Deps
	renderer *documents.Renderer
	composer *mail.Composer
	sender   mail.Sender
	log      *slog.Logger
}

var _ rpc.DocumentFiles = (*docs)(nil)

// newDocuments returns the document generator. With a blob store, the
// Typst renderer is started (TANVO_TYPST_PATH, TANVO_DOCUMENTS_FONT_PATHS)
// and the documents are rendered and stored; without one, nothing is
// rendered and the document emails go without files.
func newDocuments(cfg config.Config, tx port.Transactor, stores *EventStores, clock core.Clock, log *slog.Logger,
	emitter port.WebhookEmitter, blobs port.BlobStore, composer *mail.Composer, sender mail.Sender,
) (*docs, error) {
	d := &kernel.Deps{
		Tx:           tx,
		Events:       eventRouter{stores: stores},
		Clock:        clock,
		IDs:          core.UUIDv7Generator{},
		Webhooks:     emitter,
		ActivityLogs: auditlog.NewActivityProducer(clock, core.UUIDv7Generator{}, rpc.ActivityActor),
		Logger:       log,
		Transient:    retriedByBillingRetry,
	}
	w := &docs{d: d, composer: composer, sender: sender, log: log}
	var render docgen.Renderer
	if blobs != nil {
		r, err := documents.New(documents.Options{Typst: cfg.TypstPath, FontPaths: cfg.DocumentsFontPaths, Logger: log})
		if err != nil {
			return nil, fmt.Errorf("documents: %w (set TANVO_TYPST_PATH)", err)
		}
		w.renderer, render = r, typstRenderer{r: r}
	}
	gen, err := docgen.New(d, blobs, render, docgen.Options{DisablePDF: cfg.DisablePDFGeneration})
	if err != nil {
		w.close()
		return nil, fmt.Errorf("documents: %w", err)
	}
	w.gen = gen
	return w, nil
}

// close removes the renderer's directory.
func (w *docs) close() {
	if w == nil || w.renderer == nil {
		return
	}
	if err := w.renderer.Close(); err != nil {
		w.log.Warn("documents: close renderer", "error", err)
	}
}

// register adds the generation kinds and the document email kind to reg.
func (w *docs) register(reg *jobs.Registry) error {
	if err := jobs.Register(reg, jobs.Kind{Name: kernel.JobGenerateInvoiceDocument, MaxAttempts: 1, Retry: renderRetry()},
		jobs.HandlerFunc[kernel.InvoiceArgs](func(ctx context.Context, j *jobs.Job[kernel.InvoiceArgs]) error {
			return permanentDomain(w.gen.GenerateInvoice(ctx, j.OrganizationID, j.Args))
		})); err != nil {
		return err
	}
	if err := jobs.Register(reg, jobs.Kind{Name: kernel.JobGenerateCreditNoteDocument, MaxAttempts: 1, Retry: renderRetry()},
		jobs.HandlerFunc[kernel.CreditNoteArgs](func(ctx context.Context, j *jobs.Job[kernel.CreditNoteArgs]) error {
			return permanentDomain(w.gen.GenerateCreditNote(ctx, j.OrganizationID, j.Args))
		})); err != nil {
		return err
	}
	if err := jobs.Register(reg, jobs.Kind{Name: kernel.JobGeneratePaymentReceiptDocument, MaxAttempts: 1, Retry: renderRetry()},
		jobs.HandlerFunc[kernel.PaymentReceiptArgs](func(ctx context.Context, j *jobs.Job[kernel.PaymentReceiptArgs]) error {
			return permanentDomain(w.gen.GeneratePaymentReceipt(ctx, j.OrganizationID, j.Args))
		})); err != nil {
		return err
	}
	return jobs.Register(reg, jobs.Kind{Name: kernel.JobSendDocumentEmail, MaxAttempts: 1, Retry: emailRetry()},
		jobs.HandlerFunc[kernel.DocumentEmailArgs](w.sendEmail))
}

// renderRetry are the retry rules of the generation kinds: the store's
// transient failures as for billing, then any failure of the renderer or
// the blob store [renderAttempts] times. Domain errors fail for good
// ([permanentDomain]).
func renderRetry() []jobs.RetryRule {
	return append(billingRetry(), jobs.RetryRule{Match: func(err error) bool { return !isDomain(err) }, Attempts: renderAttempts})
}

// emailRetry are the retry rules of the document email kind, those of
// lago's SendEmailJob (send_email_job.rb:34-41): a receipt whose files
// are not ready 8 attempts, an SMTP 4xx reply 25, a timeout or a broken
// connection 6; and the store's transient failures as for billing. Other
// failures are not retried.
func emailRetry() []jobs.RetryRule {
	return append([]jobs.RetryRule{
		jobs.RetryOn(filesNotReadyAttempts, jobs.Polynomial(0.15), mail.ErrFilesNotReady),
		{Match: smtpBusy, Attempts: smtpBusyAttempts},
		{Match: mail.IsTemporary, Attempts: mailAttempts},
	}, billingRetry()...)
}

// smtpBusy reports whether err is a 4xx SMTP reply.
func smtpBusy(err error) bool {
	var te *textproto.Error
	return errors.As(err, &te) && te.Code >= 400 && te.Code < 500
}

// attemptsOf returns the attempts the rules give a job failing with err.
func attemptsOf(rules []jobs.RetryRule, err error) int {
	for _, r := range rules {
		if r.Match(err) {
			return r.Attempts
		}
	}
	if errors.Is(err, jobs.ErrRetriable) {
		return jobs.RetriableAttempts
	}
	return 1
}

// isDomain reports whether err is a domain refusal, which a retry cannot
// change.
func isDomain(err error) bool {
	return errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrNotAllowed) || errors.Is(err, core.ErrInvalidValue) ||
		errors.Is(err, core.ErrInvalidTransition)
}

// permanentDomain fails a domain refusal for good.
func permanentDomain(err error) error {
	if err != nil && isDomain(err) {
		return jobs.Permanent(err)
	}
	return err
}

// emailed is a composed document email with what its activity log names.
type emailed struct {
	m        *mail.Message
	doc      mail.DocumentRef
	id       uuid.UUID
	resource auditlog.ResourceType
	customer *customer.Customer
}

// sendEmail runs a [kernel.JobSendDocumentEmail]: it renders the missing
// PDF, composes the email (InvoiceMailer, CreditNoteMailer or
// PaymentReceiptMailer#created), sends it and records email.sent (lago
// SendEmailJob, which logs after delivery and after the last failed
// attempt).
func (w *docs) sendEmail(ctx context.Context, j *jobs.Job[kernel.DocumentEmailArgs]) error {
	org, a := j.OrganizationID, j.Args
	e, err := w.compose(ctx, org, a)
	if errors.Is(err, mail.ErrSkipped) {
		w.log.InfoContext(ctx, "mail: skipped", "document", a.Type, "reason", err.Error())
		return nil
	}
	if err != nil {
		return permanentDomain(err)
	}
	if e == nil {
		// The document is gone.
		return nil
	}
	sendErr := w.sender.Send(ctx, e.m)
	if sendErr == nil || j.Attempt >= attemptsOf(emailRetry(), sendErr) {
		if err := w.logEmail(ctx, org, e, a.Resend, sendErr); err != nil {
			return errors.Join(sendErr, err)
		}
	}
	return sendErr
}

// compose builds the email of a; nil when the document is gone.
func (w *docs) compose(ctx context.Context, org core.OrganizationID, a kernel.DocumentEmailArgs) (*emailed, error) {
	env := func(s docgen.Sender) mail.DocumentEmail {
		return mail.DocumentEmail{
			BillingEntity: s.BillingEntity, FromEmailEnabled: s.FromEmailEnabled, Locale: s.Locale,
			CustomerEmails: s.Customer.Emails(), To: a.To, Cc: a.Cc, Bcc: a.Bcc,
			// The job is enqueued once the email setting allowed it
			// (or for a resend, which ignores it).
			Resend: a.Resend, SettingChecked: !a.Resend,
		}
	}
	switch a.Type {
	case kernel.DocumentInvoice:
		e, err := w.gen.InvoiceEmail(ctx, org, idOf[core.InvoiceID](a.ID))
		if err != nil || e == nil {
			return nil, err
		}
		inv := e.Invoice
		m, err := w.composer.InvoiceCreated(mail.InvoiceEmail{
			DocumentEmail: env(e.Sender), Number: inv.Number, Currency: inv.Currency.Code(),
			TotalAmount: inv.Currency.FromMinor(inv.TotalAmount), FeesAmount: inv.Currency.FromMinor(inv.FeesAmount),
			IssuingDate: inv.IssuingDate, FileURL: e.FileURL, PDF: e.PDF,
		})
		if err != nil {
			return nil, err
		}
		return &emailed{m: m, doc: mail.DocumentRef{Type: a.Type, ID: inv.ID.String(), Number: inv.Number}, id: a.ID, resource: auditlog.ResourceInvoice, customer: e.Customer}, nil
	case kernel.DocumentCreditNote:
		e, err := w.gen.CreditNoteEmail(ctx, org, idOf[core.CreditNoteID](a.ID))
		if err != nil || e == nil {
			return nil, err
		}
		cn := e.CreditNote
		m, err := w.composer.CreditNoteCreated(creditNoteEmail(env(e.Sender), cn, e.Invoice.Number, e.Attachment))
		if err != nil {
			return nil, err
		}
		return &emailed{m: m, doc: mail.DocumentRef{Type: a.Type, ID: cn.ID.String(), Number: cn.Number}, id: a.ID, resource: auditlog.ResourceCreditNote, customer: e.Customer}, nil
	case kernel.DocumentPaymentReceipt:
		e, err := w.gen.PaymentReceiptEmail(ctx, org, idOf[core.PaymentReceiptID](a.ID))
		if err != nil || e == nil {
			return nil, err
		}
		m, err := w.composer.PaymentReceiptCreated(receiptEmail(env(e.Sender), e))
		if err != nil {
			return nil, err
		}
		return &emailed{m: m, doc: mail.DocumentRef{Type: a.Type, ID: e.Receipt.ID.String(), Number: e.Receipt.Number}, id: a.ID, resource: auditlog.ResourcePaymentReceipt, customer: e.Customer}, nil
	}
	return nil, fmt.Errorf("mail: document type %q: %w", a.Type, core.ErrInvalidValue)
}

// creditNoteEmail is the composer data of credit note cn.
func creditNoteEmail(env mail.DocumentEmail, cn *creditnote.CreditNote, invoiceNumber string, f docgen.Attachment) mail.CreditNoteEmail {
	return mail.CreditNoteEmail{
		DocumentEmail: env, Number: cn.Number, InvoiceNumber: invoiceNumber, Currency: cn.Currency.Code(),
		TotalAmount: cn.Currency.FromMinor(cn.TotalAmount), IssuingDate: cn.IssuingDate,
		// credit_note.rb:107-117 credited?, refunded? and has_offset?.
		Credited: cn.CreditAmount > 0, Refunded: cn.RefundAmount > 0, Offset: cn.OffsetAmount > 0,
		FileURL: f.FileURL, PDF: f.PDF,
	}
}

// receiptEmail is the composer data of a receipt email; the payment date
// is the payment's creation day in UTC (payment_receipt_mailer/created.slim:25).
func receiptEmail(env mail.DocumentEmail, e *docgen.PaymentReceiptEmail) mail.PaymentReceiptEmail {
	p := e.Payment
	out := mail.PaymentReceiptEmail{
		DocumentEmail: env, Number: e.Receipt.Number, Currency: p.Currency.Code(),
		PaymentAmount: p.Currency.FromMinor(p.Amount), PaymentDate: core.DateOf(p.CreatedAt, time.UTC),
		TotalDueAmount: p.Currency.FromMinor(e.TotalDueAmount), FileURL: e.FileURL, PDF: e.PDF,
	}
	for _, ri := range e.Invoices {
		inv := ri.Invoice
		out.Invoices = append(out.Invoices, mail.ReceiptInvoice{
			Number: inv.Number, Currency: inv.Currency.Code(), TotalAmount: inv.Currency.FromMinor(inv.TotalAmount),
			TotalDueAmount: inv.Currency.FromMinor(inv.TotalDueAmount()), FileURL: ri.FileURL, PDF: ri.PDF,
		})
	}
	return out
}

// logEmail records the email.sent activity log of e (lago
// Utils::EmailActivityLog): sent, resent or, with sendErr, failed with the
// error. The log is anchored on the document and names its customer.
func (w *docs) logEmail(ctx context.Context, org core.OrganizationID, e *emailed, resend bool, sendErr error) error {
	act, ok := mail.NewActivity(e.m, mail.ActivityOptions{Document: &e.doc, Resend: resend, Err: sendErr})
	if !ok {
		return nil
	}
	obj := map[string]any{
		"status": act.Status,
		"email": map[string]any{
			"subject": act.Subject, "to": act.To, "cc": act.Cc, "bcc": act.Bcc, "body_preview": act.BodyPreview,
		},
		"document": map[string]any{"type": act.Document.Type, "number": act.Document.Number, "lago_id": act.Document.ID},
	}
	if sendErr != nil {
		obj["error"] = map[string]any{"class": errorClass(sendErr), "message": act.Error}
	}
	ext := ""
	if e.customer != nil {
		ext = e.customer.ExternalID
	}
	return w.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return w.d.LogActivity(ctx, r, auditlog.Activity{
			Type: auditlog.ActivityEmailSent, OrganizationID: org, ResourceType: e.resource, ResourceID: e.id,
			Object: obj, ExternalCustomerID: ext,
		})
	})
}

// errorClass names the innermost error of err's chain, as lago logs the
// exception's class.
func errorClass(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
		}
		err = next
	}
}

// FileURL implements [rpc.DocumentFiles].
func (w *docs) FileURL(ctx context.Context, org core.OrganizationID, ref core.BlobRef) (string, error) {
	return w.gen.FileURL(ctx, org, ref)
}

// PaymentReceipt implements [rpc.DocumentFiles].
func (w *docs) PaymentReceipt(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*payment.Receipt, error) {
	return w.gen.PaymentReceiptPDF(ctx, org, id)
}

// ResendInvoice implements [rpc.DocumentFiles].
func (w *docs) ResendInvoice(ctx context.Context, org core.OrganizationID, id core.InvoiceID, to rpc.Recipients) error {
	t, err := w.gen.ResendInvoice(ctx, org, id)
	if err != nil {
		return err
	}
	return w.resend(ctx, org, mail.DocumentInvoice, kernel.DocumentInvoice, id.UUID(), t, to)
}

// ResendCreditNote implements [rpc.DocumentFiles].
func (w *docs) ResendCreditNote(ctx context.Context, org core.OrganizationID, id core.CreditNoteID, to rpc.Recipients) error {
	t, err := w.gen.ResendCreditNote(ctx, org, id)
	if err != nil {
		return err
	}
	return w.resend(ctx, org, mail.DocumentCreditNote, kernel.DocumentCreditNote, id.UUID(), t, to)
}

// ResendPaymentReceipt implements [rpc.DocumentFiles].
func (w *docs) ResendPaymentReceipt(ctx context.Context, org core.OrganizationID, id core.PaymentReceiptID, to rpc.Recipients) error {
	t, err := w.gen.ResendPaymentReceipt(ctx, org, id)
	if err != nil {
		return err
	}
	return w.resend(ctx, org, mail.DocumentPaymentReceipt, kernel.DocumentPaymentReceipt, id.UUID(), t, to)
}

// resend applies lago's Emails::ResendService checks to t and enqueues
// its email with the recipients (resend_service.rb:15-21, 52-55, 81-92).
// No premium license is required in Tanvo.
func (w *docs) resend(ctx context.Context, org core.OrganizationID, mt mail.DocumentType, kind string, id uuid.UUID, t *docgen.ResendTarget, to rpc.Recipients) error {
	env, err := w.composer.ValidateResend(mail.ResendRequest{
		Type: mt, Finalized: t.Finalized, FeesAmount: t.FeesAmount, BillingEntity: t.BillingEntity,
		FromEmailEnabled: t.FromEmailEnabled, CustomerEmails: t.Customer.Emails(), To: to.To, Cc: to.Cc, Bcc: to.Bcc,
	}, t.Locale)
	if err != nil {
		return err
	}
	recipients := env.To
	if len(nonBlankStrings(recipients)) == 0 {
		recipients = env.CustomerEmails
	}
	return w.gen.EnqueueEmail(ctx, org, kernel.DocumentEmailArgs{
		Type: kind, ID: id, To: recipients, Cc: env.Cc, Bcc: env.Bcc, Resend: true,
	})
}

// InvoiceLink returns the permanent link of the PDF of invoice id,
// rendered when missing; empty without one (the payment request email).
func (w *docs) InvoiceLink(ctx context.Context, org core.OrganizationID, id core.InvoiceID) (string, error) {
	return w.gen.InvoiceLink(ctx, org, id)
}

func nonBlankStrings(list []string) []string {
	var out []string
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// typstRenderer renders the documents of the generator with libs/documents:
// PDF/A-3 with Typst, and the e-invoices in UBL.
type typstRenderer struct{ r *documents.Renderer }

var _ docgen.Renderer = typstRenderer{}

// EInvoicing implements [docgen.Renderer].
func (t typstRenderer) EInvoicing(be *organization.BillingEntity) bool { return einvoice.Eligible(be) }

// InvoicePDF implements [docgen.Renderer].
func (t typstRenderer) InvoicePDF(ctx context.Context, d *docgen.Invoice) ([]byte, error) {
	return t.r.InvoicePDF(ctx, invoiceModel(d), d.Locale)
}

// InvoiceXML implements [docgen.Renderer].
func (t typstRenderer) InvoiceXML(_ context.Context, d *docgen.Invoice) ([]byte, error) {
	return t.r.InvoiceXML(invoiceModel(d), d.Locale, documents.UBL)
}

// CreditNotePDF implements [docgen.Renderer].
func (t typstRenderer) CreditNotePDF(ctx context.Context, d *docgen.CreditNote) ([]byte, error) {
	return t.r.CreditNotePDF(ctx, creditNoteModel(d), d.Locale)
}

// CreditNoteXML implements [docgen.Renderer].
func (t typstRenderer) CreditNoteXML(_ context.Context, d *docgen.CreditNote) ([]byte, error) {
	return t.r.CreditNoteXML(creditNoteModel(d), d.Locale, documents.UBL)
}

// PaymentReceiptPDF implements [docgen.Renderer].
func (t typstRenderer) PaymentReceiptPDF(ctx context.Context, d *docgen.PaymentReceipt) ([]byte, error) {
	return t.r.PaymentReceiptPDF(ctx, receiptModel(d), d.Locale)
}

// PaymentReceiptXML implements [docgen.Renderer].
func (t typstRenderer) PaymentReceiptXML(_ context.Context, d *docgen.PaymentReceipt) ([]byte, error) {
	return t.r.PaymentReceiptXML(receiptModel(d), d.Locale, documents.UBL)
}

func party(p docgen.Party) documents.Party {
	out := documents.Party{BillingEntity: p.BillingEntity, Customer: p.Customer}
	if p.Logo != nil {
		out.Logo = &documents.Image{ContentType: p.Logo.ContentType, Data: p.Logo.Data}
	}
	return out
}

func fromCore(c docgen.Catalog) *documents.FromCore {
	return &documents.FromCore{
		Plans: c.Plans, Subscriptions: c.Subscriptions, Charges: c.Charges, Metrics: c.Metrics,
		ChargeFilters: c.ChargeFilters, FixedCharges: c.FixedCharges, AddOns: c.AddOns, PricingUnits: c.PricingUnits,
		WalletTransactions: c.WalletTransactions, Wallets: c.Wallets,
	}
}

func invoiceModel(d *docgen.Invoice) *documents.Invoice {
	fc := fromCore(d.Catalog)
	fees := fc.Fees(d.Fees)
	for i := range fees {
		x, ok := d.Extras[fees[i].Fee.ID]
		if !ok {
			continue
		}
		for _, b := range x.RecurringBreakdown {
			fees[i].RecurringBreakdown = append(fees[i].RecurringBreakdown, documents.BreakdownItem{
				Date: core.NewDate(b.Date.Year, b.Date.Month, b.Date.Day), Action: documents.BreakdownAction(b.Action),
				Amount: b.Amount, Duration: b.Duration, TotalDuration: b.TotalDuration,
			})
		}
		if x.HasProration {
			fees[i].ProrationDays = &documents.DayRange{Days: x.ProrationDays, Total: x.ProrationTotal}
		}
		fees[i].PayInAdvanceInterval = x.PayInAdvanceInterval
	}
	out := &documents.Invoice{
		Party: party(d.Party), Invoice: d.Invoice, Subscriptions: fc.InvoiceSubscriptions(d.Links), Fees: fees,
		AppliedTaxes: d.AppliedTaxes, ProgressiveBillingCredits: d.ProgressiveBillingCredits,
		CustomSections: d.CustomSections, HasWalletTransactions: d.HasWalletTransactions,
	}
	for _, c := range d.Coupons {
		out.Coupons = append(out.Coupons, documents.CouponCredit{
			CouponName: c.CouponName, PercentageRate: c.PercentageRate, FixedAmount: c.FixedAmount, Currency: c.Currency, Amount: c.Amount,
		})
	}
	if u := d.UsageThreshold; u != nil {
		out.UsageThreshold = &documents.AppliedUsageThreshold{LifetimeUsageAmount: u.LifetimeUsageAmount, PassedThresholdAmount: u.PassedThresholdAmount}
	}
	return out
}

func creditNoteModel(d *docgen.CreditNote) *documents.CreditNote {
	fc := fromCore(d.Catalog)
	return &documents.CreditNote{
		Party: party(d.Party), CreditNote: d.CreditNote, Invoice: d.Invoice, InvoiceAppliedTaxes: d.InvoiceAppliedTaxes,
		Fees: fc.Fees(d.Fees), Subscriptions: fc.InvoiceSubscriptions(d.Links), WalletName: d.WalletName,
	}
}

func receiptModel(d *docgen.PaymentReceipt) *documents.PaymentReceipt {
	out := &documents.PaymentReceipt{Party: party(d.Party), Receipt: d.Receipt, Payment: d.Payment, Provider: d.Provider}
	if d.Invoice != nil {
		out.Invoice = invoiceModel(d.Invoice)
	}
	if q := d.Request; q != nil {
		out.Request = &documents.PaymentRequest{Party: party(q.Party), Request: q.Request, Invoices: q.Invoices}
	}
	return out
}

// idOf returns u as an identifier of type I.
func idOf[I core.Identifier[I]](u uuid.UUID) I { return core.MustParseID[I](u.String()) }
