package mail

import (
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// DocumentEmail is the envelope of a document email (invoice, credit note,
// payment receipt), shared by lago's DocumentMailer subclasses.
type DocumentEmail struct {
	// BillingEntity issues the document; its email is the reply-to (and
	// the sender when FromEmailEnabled) and its email settings gate the
	// email.
	BillingEntity *organization.BillingEntity
	// FromEmailEnabled sends from the billing entity's email instead of
	// the instance sender (lago's "from_email" premium integration).
	FromEmailEnabled bool
	// Locale is the customer's applicable document locale.
	Locale string
	// CustomerEmails are the customer's addresses (customer.Emails()).
	CustomerEmails []string
	// To replaces CustomerEmails when set (resend with custom recipients).
	To []string
	// Cc are copied recipients.
	Cc []string
	// Bcc are hidden recipients.
	Bcc []string
	// Resend marks a manual resend, which ignores the email settings
	// (lago's Emails::ResendService).
	Resend bool
}

// envelope applies the rules of lago's document mailers: the email
// setting (unless resending), a billing entity email, at least one
// recipient; from is the billing entity's from address with its name,
// reply-to its email with its name.
func (c *Composer) envelope(k Kind, setting organization.EmailSetting, d DocumentEmail) (*Message, error) {
	be := d.BillingEntity
	if be == nil {
		return nil, fmt.Errorf("%s email: missing billing entity", k)
	}
	if setting != "" && !d.Resend && !be.SendsEmail(setting) {
		return nil, skip(k, ReasonEmailSettingDisabled)
	}
	beEmail := ""
	if be.Email != nil {
		beEmail = strings.TrimSpace(*be.Email)
	}
	if beEmail == "" {
		return nil, skip(k, ReasonBillingEntityEmailMissing)
	}
	toList := d.CustomerEmails
	if len(nonBlank(d.To)) > 0 {
		toList = d.To
	}
	to, err := parseAddresses(toList)
	if err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	if len(to) == 0 {
		return nil, skip(k, ReasonNoRecipient)
	}
	m := &Message{Kind: k, To: to}
	if m.Cc, err = parseAddresses(d.Cc); err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	if m.Bcc, err = parseAddresses(d.Bcc); err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	if m.From, err = c.fromAddress(beEmail, d.FromEmailEnabled, be.Name); err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	m.ReplyTo = []Address{{Name: be.Name, Address: beEmail}}
	return m, nil
}

// fromAddress is lago's from_email_address: own email when the "from
// email" feature is enabled, else the instance sender; named name.
func (c *Composer) fromAddress(own string, ownEnabled bool, name string) (Address, error) {
	if ownEnabled && own != "" {
		return Address{Name: name, Address: own}, nil
	}
	a, err := c.systemFrom()
	if err != nil {
		return Address{}, err
	}
	a.Name = name
	return a, nil
}

// FromAddress returns the sender a document email of the billing entity
// would use, or an error when none is configured (for resend validation).
func (c *Composer) FromAddress(be *organization.BillingEntity, fromEmailEnabled bool) (Address, error) {
	own := ""
	if be.Email != nil {
		own = strings.TrimSpace(*be.Email)
	}
	return c.fromAddress(own, fromEmailEnabled, be.Name)
}

func nonBlank(list []string) []string {
	var out []string
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// InvoiceEmail is the data of the finalized invoice email.
type InvoiceEmail struct {
	DocumentEmail
	// Number is the invoice number.
	Number string
	// Currency is the invoice currency code.
	Currency string
	// TotalAmount is the invoice total in currency units.
	TotalAmount money.Decimal
	// FeesAmount is the fees amount; a zero amount is never emailed.
	FeesAmount money.Decimal
	// IssuingDate is the issuing date.
	IssuingDate core.Date
	// FileURL is the PDF download link.
	FileURL string
	// PDF is the invoice PDF, attached as invoice-<number>.pdf.
	PDF []byte
}

type invoiceView struct {
	BillingEntityName, Total, IssuingDate, Number, FileURL string
}

// InvoiceCreated builds InvoiceMailer#created.
func (c *Composer) InvoiceCreated(in InvoiceEmail) (*Message, error) {
	m, err := c.envelope(KindInvoiceCreated, organization.EmailSettingInvoiceFinalized, in.DocumentEmail)
	if err != nil {
		return nil, err
	}
	if in.FeesAmount.IsZero() {
		return nil, skip(KindInvoiceCreated, ReasonZeroFeesAmount)
	}
	l := c.Localizer(in.Locale)
	v := invoiceView{BillingEntityName: in.BillingEntity.Name, Number: in.Number, FileURL: in.FileURL}
	if v.Total, err = l.Money(in.TotalAmount, in.Currency); err != nil {
		return nil, err
	}
	if v.IssuingDate, err = l.Date(in.IssuingDate); err != nil {
		return nil, err
	}
	if m.Subject, err = l.T("email.invoice.finalized.subject", "billing_entity_name", v.BillingEntityName, "invoice_number", in.Number); err != nil {
		return nil, err
	}
	c.attach(m, "invoice-"+in.Number+".pdf", in.PDF)
	return m, c.render(m, l, layoutData{}, v)
}

func (c *Composer) attach(m *Message, name string, pdf []byte) {
	if !c.cfg.DisablePDFs && len(pdf) > 0 {
		m.Attachments = append(m.Attachments, Attachment{Filename: name, ContentType: "application/pdf", Data: pdf})
	}
}

// CreditNoteEmail is the data of the credit note email.
type CreditNoteEmail struct {
	DocumentEmail
	// Number is the credit note number.
	Number string
	// InvoiceNumber is the credited invoice's number.
	InvoiceNumber string
	// Currency is the credit note currency code.
	Currency string
	// TotalAmount is the credit note total in currency units.
	TotalAmount money.Decimal
	// IssuingDate is the issuing date.
	IssuingDate core.Date
	// Credited, Refunded and Offset tell how the credit note is settled.
	Credited, Refunded, Offset bool
	// FileURL is the PDF download link.
	FileURL string
	// PDF is the credit note PDF, attached as credit_note-<number>.pdf.
	PDF []byte
}

type creditNoteView struct {
	BillingEntityName, Total, IssuingDate, Number, InvoiceNumber, FileURL, Notice string
}

// CreditNoteCreated builds CreditNoteMailer#created.
func (c *Composer) CreditNoteCreated(in CreditNoteEmail) (*Message, error) {
	m, err := c.envelope(KindCreditNoteCreated, organization.EmailSettingCreditNoteCreated, in.DocumentEmail)
	if err != nil {
		return nil, err
	}
	l := c.Localizer(in.Locale)
	v := creditNoteView{BillingEntityName: in.BillingEntity.Name, Number: in.Number, InvoiceNumber: in.InvoiceNumber, FileURL: in.FileURL}
	if v.Total, err = l.Money(in.TotalAmount, in.Currency); err != nil {
		return nil, err
	}
	if v.IssuingDate, err = l.Date(in.IssuingDate); err != nil {
		return nil, err
	}
	n := 0
	for _, b := range []bool{in.Credited, in.Refunded, in.Offset} {
		if b {
			n++
		}
	}
	switch {
	case n > 1:
		v.Notice, err = l.T("email.credit_note.created.issued_notice", "date", v.IssuingDate)
	case in.Credited:
		v.Notice, err = l.T("email.credit_note.created.credited_notice", "date", v.IssuingDate)
	case in.Refunded:
		v.Notice, err = l.T("email.credit_note.created.refunded_notice", "date", v.IssuingDate)
	case in.Offset:
		v.Notice, err = l.T("email.credit_note.created.offset_invoice_notice", "invoice_number", in.InvoiceNumber, "date", v.IssuingDate)
	}
	if err != nil {
		return nil, err
	}
	if m.Subject, err = l.T("email.credit_note.created.subject", "billing_entity_name", v.BillingEntityName, "credit_note_number", in.Number); err != nil {
		return nil, err
	}
	c.attach(m, "credit_note-"+in.Number+".pdf", in.PDF)
	return m, c.render(m, l, layoutData{}, v)
}

// ReceiptInvoice is an invoice settled by a payment.
type ReceiptInvoice struct {
	// Number is the invoice number.
	Number string
	// Currency is the invoice currency code.
	Currency string
	// TotalAmount is the invoice total (receipts) in currency units.
	TotalAmount money.Decimal
	// TotalDueAmount is the amount still due (payment requests).
	TotalDueAmount money.Decimal
	// FileURL is the invoice PDF link.
	FileURL string
	// PDF is the invoice PDF (receipts attach every settled invoice).
	PDF []byte
}

// PaymentReceiptEmail is the data of the payment receipt email.
type PaymentReceiptEmail struct {
	DocumentEmail
	// Number is the receipt number.
	Number string
	// Currency is the payment currency code.
	Currency string
	// PaymentAmount is the amount paid.
	PaymentAmount money.Decimal
	// PaymentDate is the payment date.
	PaymentDate core.Date
	// TotalDueAmount is what remains to pay on the payable.
	TotalDueAmount money.Decimal
	// Invoices are the invoices the payment settles.
	Invoices []ReceiptInvoice
	// FileURL is the receipt PDF link.
	FileURL string
	// PDF is the receipt PDF, attached as receipt-<number>.pdf.
	PDF []byte
}

type receiptInvoiceView struct{ Number, Total, FileURL string }

type receiptView struct {
	BillingEntityName, Amount, PaymentDate, TotalDue, Number, FileURL string
	Invoices                                                          []receiptInvoiceView
}

// PaymentReceiptCreated builds PaymentReceiptMailer#created. With PDFs
// enabled, a missing receipt or invoice PDF fails with [ErrFilesNotReady].
func (c *Composer) PaymentReceiptCreated(in PaymentReceiptEmail) (*Message, error) {
	if !c.cfg.DisablePDFs {
		if len(in.PDF) == 0 {
			return nil, fmt.Errorf("payment receipt %s: %w", in.Number, ErrFilesNotReady)
		}
		for _, inv := range in.Invoices {
			if len(inv.PDF) == 0 {
				return nil, fmt.Errorf("payment receipt %s: invoice %s: %w", in.Number, inv.Number, ErrFilesNotReady)
			}
		}
	}
	m, err := c.envelope(KindPaymentReceiptCreated, organization.EmailSettingPaymentReceiptCreated, in.DocumentEmail)
	if err != nil {
		return nil, err
	}
	l := c.Localizer(in.Locale)
	v := receiptView{BillingEntityName: in.BillingEntity.Name, Number: in.Number, FileURL: in.FileURL}
	if v.Amount, err = l.Money(in.PaymentAmount, in.Currency); err != nil {
		return nil, err
	}
	if v.TotalDue, err = l.Money(in.TotalDueAmount, in.Currency); err != nil {
		return nil, err
	}
	if v.PaymentDate, err = l.Date(in.PaymentDate); err != nil {
		return nil, err
	}
	for _, inv := range in.Invoices {
		t, err := l.Money(inv.TotalAmount, inv.Currency)
		if err != nil {
			return nil, err
		}
		v.Invoices = append(v.Invoices, receiptInvoiceView{Number: inv.Number, Total: t, FileURL: inv.FileURL})
	}
	if m.Subject, err = l.T("email.payment_receipt.created.subject", "billing_entity_name", v.BillingEntityName, "payment_receipt_number", in.Number); err != nil {
		return nil, err
	}
	c.attach(m, "receipt-"+in.Number+".pdf", in.PDF)
	for _, inv := range in.Invoices {
		c.attach(m, "invoice-"+inv.Number+".pdf", inv.PDF)
	}
	return m, c.render(m, l, layoutData{}, v)
}

// PaymentRequestEmail is the data of the payment request (overdue
// balance) email.
type PaymentRequestEmail struct {
	// BillingEntity issues the request.
	BillingEntity *organization.BillingEntity
	// FromEmailEnabled sends from the billing entity's email.
	FromEmailEnabled bool
	// Locale is the customer's applicable document locale.
	Locale string
	// Email is the payment request's recipient list.
	Email string
	// Bcc are the dunning campaign's BCC addresses.
	Bcc []string
	// CustomerName is the customer's display name.
	CustomerName string
	// Currency is the request currency code.
	Currency string
	// Amount is the requested amount.
	Amount money.Decimal
	// NetPaymentTermDays is the customer's applicable payment term.
	NetPaymentTermDays int
	// PaymentURL is the hosted payment link; empty hides the button.
	PaymentURL string
	// Invoices are the overdue invoices (TotalDueAmount is shown).
	Invoices []ReceiptInvoice
}

type requestInvoiceView struct{ Number, TotalDue, FileURL string }

type requestView struct {
	BillingEntityName, CustomerName, Amount, PaymentURL string
	NetPaymentTermDays                                  int
	Invoices                                            []requestInvoiceView
}

// PaymentRequestRequested builds PaymentRequestMailer#requested.
func (c *Composer) PaymentRequestRequested(in PaymentRequestEmail) (*Message, error) {
	const k = KindPaymentRequestRequested
	be := in.BillingEntity
	if be == nil {
		return nil, fmt.Errorf("%s email: missing billing entity", k)
	}
	to, err := parseAddresses([]string{in.Email})
	if err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	if len(to) == 0 {
		return nil, skip(k, ReasonNoRecipient)
	}
	beEmail := ""
	if be.Email != nil {
		beEmail = strings.TrimSpace(*be.Email)
	}
	if beEmail == "" {
		return nil, skip(k, ReasonBillingEntityEmailMissing)
	}
	m := &Message{Kind: k, To: to, ReplyTo: []Address{{Name: be.Name, Address: beEmail}}}
	if m.Bcc, err = parseAddresses(in.Bcc); err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	if m.From, err = c.fromAddress(beEmail, in.FromEmailEnabled, be.Name); err != nil {
		return nil, fmt.Errorf("%s email: %w", k, err)
	}
	l := c.Localizer(in.Locale)
	v := requestView{BillingEntityName: be.Name, CustomerName: in.CustomerName, PaymentURL: in.PaymentURL, NetPaymentTermDays: in.NetPaymentTermDays}
	if v.Amount, err = l.Money(in.Amount, in.Currency); err != nil {
		return nil, err
	}
	for _, inv := range in.Invoices {
		t, err := l.Money(inv.TotalDueAmount, inv.Currency)
		if err != nil {
			return nil, err
		}
		v.Invoices = append(v.Invoices, requestInvoiceView{Number: inv.Number, TotalDue: t, FileURL: inv.FileURL})
	}
	if m.Subject, err = l.T("email.payment_request.requested.subject", "billing_entity_name", be.Name); err != nil {
		return nil, err
	}
	return m, c.render(m, l, layoutData{}, v)
}
