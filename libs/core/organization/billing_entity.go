package organization

import (
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Field error codes specific to billing entities.
const (
	// CodeEUTaxManagementRequiresEUCountry: eu_tax_management needs an EU
	// address country.
	CodeEUTaxManagementRequiresEUCountry = "billing_entity_must_be_in_eu"
	// CodeCountryMustBePresent: einvoicing needs an address country.
	CodeCountryMustBePresent = "country_must_be_present"
	// CodeCountryNotSupported: einvoicing needs a country of
	// [EInvoicingCountry].
	CodeCountryNotSupported = "country_not_supported"
)

// BillingEntity is the legal issuer of documents (domain §4.2). It is never
// deleted, only archived.
type BillingEntity struct {
	// ID identifies the billing entity.
	ID core.BillingEntityID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique among non-archived entities and immutable.
	Code string
	// Name is required, ≤ 255 characters.
	Name string
	// LegalName is printed on documents.
	LegalName *string
	// LegalNumber is printed on documents.
	LegalNumber *string
	// TaxID is printed on documents.
	TaxID *string
	// Email is the contact address printed on documents.
	Email *string
	// Phone is the contact phone printed on documents.
	Phone *string
	// Address is the postal address; its country drives EU tax management
	// and e-invoicing.
	Address core.Address
	// Logo references the logo file.
	Logo core.BlobRef
	// Timezone is the default time zone of its customers; UTC by default.
	Timezone *time.Location
	// DefaultCurrency is the default currency; USD by default.
	DefaultCurrency money.Currency
	// DocumentLocale is the BCP 47 language of documents; "en" by default.
	DocumentLocale string
	// DocumentNumbering is the invoice numbering scheme.
	DocumentNumbering DocumentNumbering
	// DocumentNumberPrefix is 1–10 upper-case characters, see
	// [DefaultDocumentNumberPrefix].
	DocumentNumberPrefix string
	// InvoiceFooter is printed on invoices, ≤ 600 characters.
	InvoiceFooter *string
	// InvoiceGracePeriodDays is the default grace period (≥ 0).
	InvoiceGracePeriodDays int
	// NetPaymentTermDays is the default payment term (≥ 0).
	NetPaymentTermDays int
	// FinalizeZeroAmountInvoice finalizes zero-amount invoices by default.
	FinalizeZeroAmountInvoice bool
	// SkipCreditInvoicePaymentDelay charges wallet top-up invoices at once,
	// without waiting for a checkout link payment.
	SkipCreditInvoicePaymentDelay bool
	// IssuingDateAnchor is the default issuing date anchor.
	IssuingDateAnchor core.IssuingDateAnchor
	// IssuingDateAdjustment is the default issuing date adjustment.
	IssuingDateAdjustment core.IssuingDateAdjustment
	// EmailSettings are the documents emailed to customers.
	EmailSettings []EmailSetting
	// EUTaxManagement enables EU VAT handling; requires an EU country.
	EUTaxManagement bool
	// EInvoicing embeds e-invoice XML; requires a country of
	// [EInvoicingCountry].
	EInvoicing bool
	// IsDefault marks the organization's default entity (at most one).
	IsDefault bool
	// AppliedDunningCampaignID is the default dunning campaign of its
	// customers (domain §16).
	AppliedDunningCampaignID core.DunningCampaignID
	// TaxIDs are the default taxes, the last fallback of tax resolution
	// (domain §22.10).
	TaxIDs []core.TaxID
	// InvoiceCustomSectionIDs are the default invoice custom sections
	// (domain §11.9).
	InvoiceCustomSectionIDs []core.InvoiceCustomSectionID
	// ArchivedAt is set once archived.
	ArchivedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewBillingEntity returns a billing entity with the defaults of domain
// §4.2 and the document number prefix derived from its name and ID.
func NewBillingEntity(org core.OrganizationID, id core.BillingEntityID, code, name string, now time.Time) *BillingEntity {
	return &BillingEntity{
		ID:                        id,
		OrganizationID:            org,
		Code:                      code,
		Name:                      name,
		Timezone:                  time.UTC,
		DefaultCurrency:           money.MustCurrency("USD"),
		DocumentLocale:            "en",
		DocumentNumbering:         DocumentNumberingPerCustomer,
		DocumentNumberPrefix:      DefaultDocumentNumberPrefix(name, id),
		FinalizeZeroAmountInvoice: true,
		IssuingDateAnchor:         core.IssuingDateAnchorNextPeriodStart,
		IssuingDateAdjustment:     core.IssuingDateAdjustmentAlignWithFinalizationDate,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
}

// DefaultDocumentNumberPrefix returns the first 3 letters (A–Z) of the
// upper-cased name, "-" and the last 4 characters of the ID, upper-cased
// (domain §4.2).
func DefaultDocumentNumberPrefix(name string, id core.BillingEntityID) string {
	var letters []rune
	for _, r := range strings.ToUpper(name) {
		if r >= 'A' && r <= 'Z' {
			letters = append(letters, r)
			if len(letters) == 3 {
				break
			}
		}
	}
	s := id.UUID().String()
	return string(letters) + "-" + strings.ToUpper(s[len(s)-4:])
}

// NormalizeDocumentNumberPrefix upper-cases a prefix as it is stored.
func NormalizeDocumentNumberPrefix(p string) string { return strings.ToUpper(strings.TrimSpace(p)) }

// Validate checks the invariants of domain §4.2.
func (b *BillingEntity) Validate() error {
	var v core.Validator
	v.Code("code", b.Code, 255)
	v.String("name", b.Name, 255)
	v.OptionalString("legal_name", b.LegalName, 255)
	v.OptionalString("legal_number", b.LegalNumber, 255)
	v.OptionalString("tax_id", b.TaxID, 255)
	if b.Email != nil {
		v.Email("email", *b.Email)
	}
	v.Merge("address", b.Address.Validate())
	v.Check(b.Timezone != nil, "timezone", core.CodeInvalidTimezone)
	v.Currency("default_currency", b.DefaultCurrency)
	v.Check(core.ValidLocale(b.DocumentLocale), "document_locale", core.CodeInvalid)
	v.Enum("document_numbering", b.DocumentNumbering)
	if v.Required("document_number_prefix", b.DocumentNumberPrefix) && v.MaxLen("document_number_prefix", b.DocumentNumberPrefix, 10) {
		v.Check(b.DocumentNumberPrefix == strings.ToUpper(b.DocumentNumberPrefix), "document_number_prefix", core.CodeInvalidFormat)
	}
	v.OptionalString("invoice_footer", b.InvoiceFooter, 600)
	v.Check(b.InvoiceGracePeriodDays >= 0, "invoice_grace_period_days", core.CodeOutOfRange)
	v.Check(b.NetPaymentTermDays >= 0, "net_payment_term_days", core.CodeOutOfRange)
	v.Enum("issuing_date_anchor", b.IssuingDateAnchor)
	v.Enum("issuing_date_adjustment", b.IssuingDateAdjustment)
	for _, s := range b.EmailSettings {
		if !v.Check(s.Valid(), "email_settings", core.CodeInvalid) {
			break
		}
	}
	if b.EUTaxManagement {
		v.Check(core.IsEUCountry(b.Address.Country), "eu_tax_management", CodeEUTaxManagementRequiresEUCountry)
	}
	if b.EInvoicing {
		switch {
		case b.Address.Country == "":
			v.Add("einvoicing", CodeCountryMustBePresent)
		case !EInvoicingCountry(b.Address.Country).Valid():
			v.Add("einvoicing", CodeCountryNotSupported)
		}
	}
	return v.Err()
}

// Archived reports whether the entity is archived; an archived entity
// cannot receive new customers or subscriptions.
func (b *BillingEntity) Archived() bool { return b.ArchivedAt != nil }

// Archive archives the entity at now; archiving twice fails with
// [core.ErrInvalidTransition].
func (b *BillingEntity) Archive(now time.Time) error {
	if b.Archived() {
		return &core.TransitionError{Entity: "billing_entity", From: "archived", To: "archived"}
	}
	b.ArchivedAt = &now
	b.IsDefault = false
	b.UpdatedAt = now
	return nil
}

// EUVATEligible reports whether the entity's country is in the EU.
func (b *BillingEntity) EUVATEligible() bool { return core.IsEUCountry(b.Address.Country) }

// SendsEmail reports whether the entity emails documents of setting s.
func (b *BillingEntity) SendsEmail(s EmailSetting) bool { return slices.Contains(b.EmailSettings, s) }
