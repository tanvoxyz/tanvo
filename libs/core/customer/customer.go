package customer

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Customer is the billed party (domain §6). A customer with documents is
// archived (DeletedAt), never hard-deleted.
type Customer struct {
	// ID identifies the customer.
	ID core.CustomerID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// ExternalID is the customer-provided identifier, unique among
	// non-deleted customers; required, ≤ 255 characters.
	ExternalID string
	// BillingEntityID is the billing entity issuing the customer's
	// documents; defaults to the organization's default entity.
	BillingEntityID core.BillingEntityID
	// CustomerType is the legal kind; "" when unset.
	CustomerType Type
	// AccountType is customer (default) or partner (self-billed).
	AccountType AccountType
	// Name is the display name, ≤ 255 characters.
	Name *string
	// Firstname is the first name of an individual or contact.
	Firstname *string
	// Lastname is the last name of an individual or contact.
	Lastname *string
	// LegalName is the registered name.
	LegalName *string
	// LegalNumber is the registration number.
	LegalNumber *string
	// TaxIdentificationNumber is the VAT or tax ID; it drives the VIES
	// check under EU tax management.
	TaxIdentificationNumber *string
	// Email is a comma-separated list of email addresses.
	Email *string
	// Phone is the contact phone.
	Phone *string
	// URL is the customer's website.
	URL *string
	// LogoURL is the URL of the customer's logo.
	LogoURL *string
	// BillingAddress is the billing address.
	BillingAddress core.Address
	// ShippingAddress is the shipping address; each empty field falls back
	// to the billing address field when printed ([Customer.EffectiveShippingAddress]).
	ShippingAddress core.Address
	// Currency is the default currency of the customer's new billing
	// objects, not a constraint (domain §6): a wallet or one-off invoice
	// created without a currency takes it, and the first subscription,
	// wallet, fixed-amount applied coupon or one-off invoice created while
	// it is zero sets it. Subscriptions, wallets, applied coupons and
	// invoices carry their own currency, so changing it affects none of
	// them.
	Currency money.Currency
	// Timezone overrides the billing entity's time zone; nil inherits.
	Timezone *time.Location
	// DocumentLocale overrides the billing entity's document locale (BCP
	// 47); nil inherits.
	DocumentLocale *string
	// InvoiceGracePeriodDays overrides the billing entity's grace period
	// (≥ 0); nil inherits.
	InvoiceGracePeriodDays *int
	// NetPaymentTermDays overrides the billing entity's payment term
	// (≥ 0); nil inherits.
	NetPaymentTermDays *int
	// FinalizeZeroAmountInvoice is inherit (default), skip or finalize.
	FinalizeZeroAmountInvoice ZeroAmountFinalization
	// IssuingDateAnchor overrides the billing entity's anchor; "" inherits.
	IssuingDateAnchor core.IssuingDateAnchor
	// IssuingDateAdjustment overrides the billing entity's adjustment; ""
	// inherits.
	IssuingDateAdjustment core.IssuingDateAdjustment
	// SkipInvoiceCustomSections disables manual invoice custom sections.
	SkipInvoiceCustomSections bool
	// InvoiceCustomSectionIDs are the manually selected invoice custom
	// sections (domain §11.9); empty falls back to the billing entity's.
	InvoiceCustomSectionIDs []core.InvoiceCustomSectionID
	// SystemInvoiceCustomSectionIDs are the system-generated sections
	// attached by features (for example a self-billing mention); they apply
	// even when SkipInvoiceCustomSections is set.
	SystemInvoiceCustomSectionIDs []core.InvoiceCustomSectionID
	// AppliedDunningCampaignID overrides the billing entity's dunning
	// campaign (domain §16).
	AppliedDunningCampaignID core.DunningCampaignID
	// ExcludeFromDunningCampaign opts the customer out of dunning.
	ExcludeFromDunningCampaign bool
	// DunningCampaignAttempts counts the dunning attempts made per currency
	// code under the applied campaign.
	DunningCampaignAttempts map[string]int
	// LastDunningCampaignAttemptAt is the instant of the last attempt.
	LastDunningCampaignAttemptAt *time.Time
	// SequentialID is assigned on create, per billing entity.
	SequentialID int64
	// Slug is "<prefix>-<sequential_id %03d>", see [Slug].
	Slug string
	// PaymentReceiptCounter numbers payment receipts (domain §22.11).
	PaymentReceiptCounter int64
	// Metadata holds at most 5 pairs (key ≤ 20, value ≤ 100 characters).
	Metadata core.Metadata
	// TaxIDs are the customer's taxes, step 7 of tax resolution (domain
	// §22.10).
	TaxIDs []core.TaxID
	// DeletedAt is set when the customer is archived.
	DeletedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// New returns a customer with the defaults of domain §6.
func New(org core.OrganizationID, id core.CustomerID, externalID string, billingEntity core.BillingEntityID, now time.Time) *Customer {
	return &Customer{
		ID:                        id,
		OrganizationID:            org,
		ExternalID:                strings.TrimSpace(externalID),
		BillingEntityID:           billingEntity,
		AccountType:               AccountTypeCustomer,
		FinalizeZeroAmountInvoice: ZeroAmountFinalizationInherit,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
}

// Validate checks the invariants of domain §6 that need no other
// aggregate.
func (c *Customer) Validate() error {
	var v core.Validator
	v.String("external_id", c.ExternalID, 255)
	v.Check(!c.BillingEntityID.IsZero(), "billing_entity_id", core.CodeRequired)
	v.OptionalEnum("customer_type", c.CustomerType)
	v.Enum("account_type", c.AccountType)
	for _, f := range []struct {
		name string
		s    *string
	}{
		{"name", c.Name},
		{"firstname", c.Firstname},
		{"lastname", c.Lastname},
		{"legal_name", c.LegalName},
		{"legal_number", c.LegalNumber},
		{"tax_identification_number", c.TaxIdentificationNumber},
	} {
		v.OptionalString(f.name, f.s, 255)
	}
	if c.Email != nil && strings.TrimSpace(*c.Email) != "" {
		v.EmailList("email", *c.Email)
	}
	v.Merge("billing_address", c.BillingAddress.Validate())
	v.Merge("shipping_address", c.ShippingAddress.Validate())
	if c.DocumentLocale != nil {
		v.Check(core.ValidLocale(*c.DocumentLocale), "document_locale", core.CodeInvalid)
	}
	if c.InvoiceGracePeriodDays != nil {
		v.Check(*c.InvoiceGracePeriodDays >= 0, "invoice_grace_period_days", core.CodeOutOfRange)
	}
	if c.NetPaymentTermDays != nil {
		v.Check(*c.NetPaymentTermDays >= 0, "net_payment_term_days", core.CodeOutOfRange)
	}
	v.Enum("finalize_zero_amount_invoice", c.FinalizeZeroAmountInvoice)
	v.OptionalEnum("issuing_date_anchor", c.IssuingDateAnchor)
	v.OptionalEnum("issuing_date_adjustment", c.IssuingDateAdjustment)
	for _, n := range c.DunningCampaignAttempts {
		v.Check(n >= 0, "dunning_campaign_attempts", core.CodeOutOfRange)
	}
	v.Merge("metadata", c.Metadata.Validate(core.CustomerMetadataLimits()))
	return v.Err()
}

// Deleted reports whether the customer is archived.
func (c *Customer) Deleted() bool { return c.DeletedAt != nil }

// Archive archives the customer at now (domain §6: deleting a customer
// with documents archives it). Archiving twice fails with
// [core.ErrInvalidTransition].
func (c *Customer) Archive(now time.Time) error {
	if c.Deleted() {
		return &core.TransitionError{Entity: "customer", From: "deleted", To: "deleted"}
	}
	c.DeletedAt = &now
	c.UpdatedAt = now
	return nil
}

// SelfBilled reports whether the customer's invoices are self-billed
// (account type partner, domain §11.1).
func (c *Customer) SelfBilled() bool { return c.AccountType == AccountTypePartner }

// Slug returns the customer slug "<prefix>-<seq %03d>" (domain §6); the
// width is a minimum.
func Slug(prefix string, seq int64) string { return fmt.Sprintf("%s-%03d", prefix, seq) }

// AssignSequence sets the sequential ID and the slug derived from the
// billing entity's document number prefix.
func (c *Customer) AssignSequence(seq int64, prefix string) {
	c.SequentialID = seq
	c.Slug = Slug(prefix, seq)
}

// DisplayName returns the name printed for the customer: the legal name
// (when preferLegalName) or the name, followed by " - " and the first and
// last names when present.
func (c *Customer) DisplayName(preferLegalName bool) string {
	var names []string
	main := present(c.Name)
	if preferLegalName && present(c.LegalName) != "" {
		main = present(c.LegalName)
	}
	if main != "" {
		names = append(names, main)
	}
	first, last := present(c.Firstname), present(c.Lastname)
	if first != "" || last != "" {
		if len(names) > 0 {
			names = append(names, "-")
		}
		if first != "" {
			names = append(names, first)
		}
		if last != "" {
			names = append(names, last)
		}
	}
	return strings.Join(names, " ")
}

// EffectiveShippingAddress returns the shipping address with each empty
// field taken from the billing address (domain §6).
func (c *Customer) EffectiveShippingAddress() core.Address {
	return c.ShippingAddress.WithFallback(c.BillingAddress)
}

// SameBillingAndShippingAddress reports whether the shipping address is
// empty or equal to the billing address.
func (c *Customer) SameBillingAndShippingAddress() bool {
	return c.ShippingAddress.IsZero() || c.ShippingAddress == c.BillingAddress
}

// EmptyBillingAndShippingAddress reports whether both addresses are empty.
func (c *Customer) EmptyBillingAndShippingAddress() bool {
	return c.ShippingAddress.IsZero() && c.BillingAddress.IsZero()
}

// Emails returns the addresses of the Email list, trimmed.
func (c *Customer) Emails() []string {
	if c.Email == nil {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(*c.Email, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Defaults are the billing entity values a customer inherits (domain §6:
// settings resolve customer → billing entity → UTC).
type Defaults struct {
	// Timezone is the billing entity's time zone; nil means UTC.
	Timezone *time.Location
	// DocumentLocale is the billing entity's document locale.
	DocumentLocale string
	// InvoiceGracePeriodDays is the billing entity's grace period.
	InvoiceGracePeriodDays int
	// NetPaymentTermDays is the billing entity's payment term.
	NetPaymentTermDays int
	// FinalizeZeroAmountInvoice is the billing entity's flag.
	FinalizeZeroAmountInvoice bool
	// IssuingDateAnchor is the billing entity's anchor.
	IssuingDateAnchor core.IssuingDateAnchor
	// IssuingDateAdjustment is the billing entity's adjustment.
	IssuingDateAdjustment core.IssuingDateAdjustment
	// InvoiceCustomSectionIDs are the billing entity's manual sections.
	InvoiceCustomSectionIDs []core.InvoiceCustomSectionID
}

// ApplicableTimezone returns the customer's time zone, else the billing
// entity's, else UTC (domain §22.1).
func (c *Customer) ApplicableTimezone(d Defaults) *time.Location {
	switch {
	case c.Timezone != nil:
		return c.Timezone
	case d.Timezone != nil:
		return d.Timezone
	default:
		return time.UTC
	}
}

// ApplicableGracePeriodDays returns the customer's grace period, else the
// billing entity's (domain §22.12).
func (c *Customer) ApplicableGracePeriodDays(d Defaults) int {
	if c.InvoiceGracePeriodDays != nil {
		return *c.InvoiceGracePeriodDays
	}
	return d.InvoiceGracePeriodDays
}

// ApplicableNetPaymentTermDays returns the customer's payment term, else
// the billing entity's (domain §22.12).
func (c *Customer) ApplicableNetPaymentTermDays(d Defaults) int {
	if c.NetPaymentTermDays != nil {
		return *c.NetPaymentTermDays
	}
	return d.NetPaymentTermDays
}

// ApplicableDocumentLocale returns the customer's document locale, else
// the billing entity's (domain §11.8).
func (c *Customer) ApplicableDocumentLocale(d Defaults) string {
	if c.DocumentLocale != nil && *c.DocumentLocale != "" {
		return *c.DocumentLocale
	}
	return d.DocumentLocale
}

// ApplicableFinalizeZeroAmountInvoice resolves zero-amount finalization:
// finalize → true, skip → false, inherit → the billing entity's flag
// (domain §22.12).
func (c *Customer) ApplicableFinalizeZeroAmountInvoice(d Defaults) bool {
	switch c.FinalizeZeroAmountInvoice {
	case ZeroAmountFinalizationFinalize:
		return true
	case ZeroAmountFinalizationSkip:
		return false
	default:
		return d.FinalizeZeroAmountInvoice
	}
}

// ApplicableIssuingDateAnchor returns the customer's anchor, else the
// billing entity's.
func (c *Customer) ApplicableIssuingDateAnchor(d Defaults) core.IssuingDateAnchor {
	if c.IssuingDateAnchor != "" {
		return c.IssuingDateAnchor
	}
	return d.IssuingDateAnchor
}

// ApplicableIssuingDateAdjustment returns the customer's adjustment, else
// the billing entity's.
func (c *Customer) ApplicableIssuingDateAdjustment(d Defaults) core.IssuingDateAdjustment {
	if c.IssuingDateAdjustment != "" {
		return c.IssuingDateAdjustment
	}
	return d.IssuingDateAdjustment
}

// ConfigurableInvoiceCustomSections returns the manually selected sections
// that apply: none when skipped, else the customer's selection, else the
// billing entity's (domain §11.9).
func (c *Customer) ConfigurableInvoiceCustomSections(d Defaults) []core.InvoiceCustomSectionID {
	switch {
	case c.SkipInvoiceCustomSections:
		return nil
	case len(c.InvoiceCustomSectionIDs) > 0:
		return slices.Clone(c.InvoiceCustomSectionIDs)
	default:
		return slices.Clone(d.InvoiceCustomSectionIDs)
	}
}

// ApplicableInvoiceCustomSections returns the configurable sections
// followed by the system-generated ones, without duplicates.
func (c *Customer) ApplicableInvoiceCustomSections(d Defaults) []core.InvoiceCustomSectionID {
	out := c.ConfigurableInvoiceCustomSections(d)
	for _, id := range c.SystemInvoiceCustomSectionIDs {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// Documents are the facts about a customer's other aggregates that its
// editability rules depend on.
type Documents struct {
	// HasSubscriptions: the customer has at least one subscription.
	HasSubscriptions bool
	// HasInvoices: the customer has at least one invoice.
	HasInvoices bool
	// HasFixedAmountAppliedCoupons: the customer has at least one applied
	// coupon with an amount, terminated or not. Applied percentage coupons
	// carry no currency and do not count (customer.rb:281).
	HasFixedAmountAppliedCoupons bool
	// HasWallets: the customer has at least one wallet.
	HasWallets bool
}

// Editable reports whether the customer has no subscriptions, invoices,
// fixed-amount applied coupons and wallets (domain §6,
// customer.rb:277-283); only an editable customer can change external_id
// or account_type.
func Editable(d Documents) bool {
	return !d.HasSubscriptions && !d.HasInvoices && !d.HasFixedAmountAppliedCoupons && !d.HasWallets
}

// CodeNotEditable: external_id or account_type changed on a customer that
// is not editable.
const CodeNotEditable = "customer_not_editable"

// CheckUpdate enforces the update rules of domain §6 between the stored
// customer old and the requested next: external_id and account_type change
// only while editable. The currency is a default and changes freely.
func CheckUpdate(old, next *Customer, d Documents) error {
	var v core.Validator
	if !Editable(d) {
		v.Check(old.ExternalID == next.ExternalID, "external_id", CodeNotEditable)
		v.Check(old.AccountType == next.AccountType, "account_type", CodeNotEditable)
	}
	return v.Err()
}

func present(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
