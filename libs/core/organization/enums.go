package organization

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// AuthenticationMethod is a way members of an organization may sign in (domain §4.1).
type AuthenticationMethod string

// AuthenticationMethod values.
const (
	// AuthenticationMethodEmailPassword is "email_password": email and password.
	AuthenticationMethodEmailPassword AuthenticationMethod = "email_password"
	// AuthenticationMethodGoogleOAuth is "google_oauth": Google OpenID Connect.
	AuthenticationMethodGoogleOAuth AuthenticationMethod = "google_oauth"
	// AuthenticationMethodOkta is "okta": Okta single sign-on.
	AuthenticationMethodOkta AuthenticationMethod = "okta"
	// AuthenticationMethodEntraID is "entra_id": Microsoft Entra ID single sign-on.
	AuthenticationMethodEntraID AuthenticationMethod = "entra_id"
)

// AuthenticationMethodValues returns every AuthenticationMethod, in declaration order.
func AuthenticationMethodValues() []AuthenticationMethod {
	return []AuthenticationMethod{AuthenticationMethodEmailPassword, AuthenticationMethodGoogleOAuth, AuthenticationMethodOkta, AuthenticationMethodEntraID}
}

// Valid reports whether v is one of the declared AuthenticationMethod values.
func (v AuthenticationMethod) Valid() bool {
	switch v {
	case AuthenticationMethodEmailPassword, AuthenticationMethodGoogleOAuth, AuthenticationMethodOkta, AuthenticationMethodEntraID:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v AuthenticationMethod) String() string { return string(v) }

// ParseAuthenticationMethod parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseAuthenticationMethod(s string) (AuthenticationMethod, error) {
	if v := AuthenticationMethod(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("organization.AuthenticationMethod", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v AuthenticationMethod) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *AuthenticationMethod) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseAuthenticationMethod(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// DocumentNumbering is the numbering scheme of a billing entity's invoices (domain §22.11).
type DocumentNumbering string

// DocumentNumbering values.
const (
	// DocumentNumberingPerCustomer is "per_customer": one sequence per customer.
	DocumentNumberingPerCustomer DocumentNumbering = "per_customer"
	// DocumentNumberingPerBillingEntity is "per_billing_entity": one sequence per billing entity.
	DocumentNumberingPerBillingEntity DocumentNumbering = "per_billing_entity"
)

// DocumentNumberingValues returns every DocumentNumbering, in declaration order.
func DocumentNumberingValues() []DocumentNumbering {
	return []DocumentNumbering{DocumentNumberingPerCustomer, DocumentNumberingPerBillingEntity}
}

// Valid reports whether v is one of the declared DocumentNumbering values.
func (v DocumentNumbering) Valid() bool {
	switch v {
	case DocumentNumberingPerCustomer, DocumentNumberingPerBillingEntity:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v DocumentNumbering) String() string { return string(v) }

// ParseDocumentNumbering parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseDocumentNumbering(s string) (DocumentNumbering, error) {
	if v := DocumentNumbering(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("organization.DocumentNumbering", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v DocumentNumbering) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *DocumentNumbering) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseDocumentNumbering(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// EmailSetting is a document email a billing entity sends (domain §4.2).
type EmailSetting string

// EmailSetting values.
const (
	// EmailSettingInvoiceFinalized is "invoice.finalized": email finalized invoices.
	EmailSettingInvoiceFinalized EmailSetting = "invoice.finalized"
	// EmailSettingCreditNoteCreated is "credit_note.created": email created credit notes.
	EmailSettingCreditNoteCreated EmailSetting = "credit_note.created"
	// EmailSettingPaymentReceiptCreated is "payment_receipt.created": email payment receipts.
	EmailSettingPaymentReceiptCreated EmailSetting = "payment_receipt.created"
)

// EmailSettingValues returns every EmailSetting, in declaration order.
func EmailSettingValues() []EmailSetting {
	return []EmailSetting{EmailSettingInvoiceFinalized, EmailSettingCreditNoteCreated, EmailSettingPaymentReceiptCreated}
}

// Valid reports whether v is one of the declared EmailSetting values.
func (v EmailSetting) Valid() bool {
	switch v {
	case EmailSettingInvoiceFinalized, EmailSettingCreditNoteCreated, EmailSettingPaymentReceiptCreated:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v EmailSetting) String() string { return string(v) }

// ParseEmailSetting parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseEmailSetting(s string) (EmailSetting, error) {
	if v := EmailSetting(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("organization.EmailSetting", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v EmailSetting) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *EmailSetting) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseEmailSetting(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// EInvoicingCountry is a country whose e-invoicing format Tanvo produces (domain §11.8).
type EInvoicingCountry string

// EInvoicingCountry values.
const (
	// EInvoicingCountryFR is "FR": France (Factur-X).
	EInvoicingCountryFR EInvoicingCountry = "FR"
	// EInvoicingCountryDE is "DE": Germany (ZUGFeRD).
	EInvoicingCountryDE EInvoicingCountry = "DE"
)

// EInvoicingCountryValues returns every EInvoicingCountry, in declaration order.
func EInvoicingCountryValues() []EInvoicingCountry {
	return []EInvoicingCountry{EInvoicingCountryFR, EInvoicingCountryDE}
}

// Valid reports whether v is one of the declared EInvoicingCountry values.
func (v EInvoicingCountry) Valid() bool {
	switch v {
	case EInvoicingCountryFR, EInvoicingCountryDE:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v EInvoicingCountry) String() string { return string(v) }

// ParseEInvoicingCountry parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseEInvoicingCountry(s string) (EInvoicingCountry, error) {
	if v := EInvoicingCountry(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("organization.EInvoicingCountry", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v EInvoicingCountry) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *EInvoicingCountry) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseEInvoicingCountry(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
