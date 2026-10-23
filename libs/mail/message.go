package mail

import (
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Address is an email address with an optional display name; its String
// method encodes the name per RFC 2047.
type Address = netmail.Address

// Kind names the email a [Message] is, for logging and activity records.
type Kind string

// Kinds of messages, one per lago-api mailer action plus the invitation.
const (
	// KindInvoiceCreated is InvoiceMailer#created.
	KindInvoiceCreated Kind = "invoice_created"
	// KindCreditNoteCreated is CreditNoteMailer#created.
	KindCreditNoteCreated Kind = "credit_note_created"
	// KindPaymentReceiptCreated is PaymentReceiptMailer#created.
	KindPaymentReceiptCreated Kind = "payment_receipt_created"
	// KindPaymentRequestRequested is PaymentRequestMailer#requested.
	KindPaymentRequestRequested Kind = "payment_request_requested"
	// KindPasswordResetRequested is PasswordResetMailer#requested.
	KindPasswordResetRequested Kind = "password_reset_requested"
	// KindAPIKeyCreated is ApiKeyMailer#created.
	KindAPIKeyCreated Kind = "api_key_created"
	// KindAPIKeyRotated is ApiKeyMailer#rotated.
	KindAPIKeyRotated Kind = "api_key_rotated"
	// KindAPIKeyDestroyed is ApiKeyMailer#destroyed.
	KindAPIKeyDestroyed Kind = "api_key_destroyed"
	// KindDataExportCompleted is DataExportMailer#completed.
	KindDataExportCompleted Kind = "data_export_completed"
	// KindAuthenticationMethodsUpdated is
	// OrganizationMailer#authentication_methods_updated.
	KindAuthenticationMethodsUpdated Kind = "authentication_methods_updated"
	// KindFeatureToggled is AdminMailer#feature_toggled.
	KindFeatureToggled Kind = "feature_toggled"
	// KindInvitation is the invitation to join an organization (new in
	// Tanvo; lago shares invite links by hand).
	KindInvitation Kind = "invitation"
)

// Kinds returns every message kind.
func Kinds() []Kind {
	return []Kind{
		KindInvoiceCreated, KindCreditNoteCreated, KindPaymentReceiptCreated, KindPaymentRequestRequested,
		KindPasswordResetRequested, KindAPIKeyCreated, KindAPIKeyRotated, KindAPIKeyDestroyed,
		KindDataExportCompleted, KindAuthenticationMethodsUpdated, KindFeatureToggled, KindInvitation,
	}
}

// Attachment is a file attached to a message.
type Attachment struct {
	// Filename is the file name shown to the recipient.
	Filename string
	// ContentType is the media type ("application/pdf").
	ContentType string
	// Data is the file content.
	Data []byte
}

// Message is one email ready to send.
type Message struct {
	// Kind names the email; empty for messages built elsewhere.
	Kind Kind
	// Locale is the locale the body was rendered in.
	Locale string
	// From is the sender, also used as the SMTP envelope sender.
	From Address
	// ReplyTo are the reply addresses.
	ReplyTo []Address
	// To are the primary recipients.
	To []Address
	// Cc are the copied recipients.
	Cc []Address
	// Bcc are the hidden recipients; they are never written in headers.
	Bcc []Address
	// Subject is the subject line.
	Subject string
	// Text is the plain-text body.
	Text string
	// HTML is the HTML body.
	HTML string
	// Attachments are the attached files.
	Attachments []Attachment
}

// Recipients returns the envelope recipients: To, Cc and Bcc addresses,
// without duplicates (compared case-insensitively), in that order.
func (m *Message) Recipients() []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]Address{m.To, m.Cc, m.Bcc} {
		for _, a := range list {
			k := strings.ToLower(a.Address)
			if !seen[k] {
				seen[k] = true
				out = append(out, a.Address)
			}
		}
	}
	return out
}

// Validate checks that the message can be sent: a sender, at least one
// recipient, a body, and no line breaks in the subject or file names
// (header injection).
func (m *Message) Validate() error {
	var errs []error
	if m.From.Address == "" {
		errs = append(errs, errors.New("missing sender"))
	}
	if len(m.Recipients()) == 0 {
		errs = append(errs, errors.New("missing recipients"))
	}
	if m.Text == "" && m.HTML == "" {
		errs = append(errs, errors.New("missing body"))
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		errs = append(errs, errors.New("subject contains a line break"))
	}
	for _, list := range [][]Address{{m.From}, m.ReplyTo, m.To, m.Cc, m.Bcc} {
		for _, a := range list {
			if strings.ContainsAny(a.Address+a.Name, "\r\n") {
				errs = append(errs, fmt.Errorf("address %q contains a line break", a.Address))
			}
		}
	}
	for _, at := range m.Attachments {
		if at.Filename == "" || strings.ContainsAny(at.Filename, "\r\n\"") {
			errs = append(errs, fmt.Errorf("invalid attachment file name %q", at.Filename))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("invalid message: %w", err)
	}
	return nil
}

// FromPortEmail converts the core port.Email into a Message, parsing its
// addresses (with or without display names).
func FromPortEmail(e port.Email) (*Message, error) {
	from, err := netmail.ParseAddress(e.From)
	if err != nil {
		return nil, fmt.Errorf("parse from %q: %w", e.From, err)
	}
	m := &Message{From: *from, Subject: e.Subject, Text: e.Text, HTML: e.HTML}
	if e.ReplyTo != "" {
		if m.ReplyTo, err = parseAddresses([]string{e.ReplyTo}); err != nil {
			return nil, err
		}
	}
	if m.To, err = parseAddresses(e.To); err != nil {
		return nil, err
	}
	if m.Cc, err = parseAddresses(e.Cc); err != nil {
		return nil, err
	}
	if m.Bcc, err = parseAddresses(e.Bcc); err != nil {
		return nil, err
	}
	for _, a := range e.Attachments {
		m.Attachments = append(m.Attachments, Attachment{Filename: a.Name, ContentType: a.ContentType, Data: a.Body})
	}
	return m, nil
}

// parseAddresses parses each entry, which may itself be a comma-separated
// list; blank entries are dropped.
func parseAddresses(list []string) ([]Address, error) {
	var out []Address
	for _, s := range list {
		if strings.TrimSpace(s) == "" {
			continue
		}
		as, err := netmail.ParseAddressList(s)
		if err != nil {
			return nil, fmt.Errorf("parse address %q: %w", s, err)
		}
		for _, a := range as {
			out = append(out, *a)
		}
	}
	return out, nil
}
