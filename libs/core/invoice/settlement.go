package invoice

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Settlement records an amount that settles an invoice, by a payment or by
// an offset credit note (domain §11.5).
type Settlement struct {
	// ID identifies the settlement.
	ID core.InvoiceSettlementID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the settled invoice.
	InvoiceID core.InvoiceID
	// Type is how the invoice is settled.
	Type SettlementType
	// PaymentID is the source payment (type payment).
	PaymentID core.PaymentID
	// CreditNoteID is the source credit note (type credit_note).
	CreditNoteID core.CreditNoteID
	// Amount is the settled amount in minor units of Currency.
	Amount int64
	// Currency is the invoice currency.
	Currency money.Currency
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// Validate requires exactly the source matching the type and a positive
// amount.
func (s *Settlement) Validate() error {
	var v core.Validator
	v.Check(!s.InvoiceID.IsZero(), "invoice_id", core.CodeRequired)
	v.Enum("settlement_type", s.Type)
	switch s.Type {
	case SettlementTypePayment:
		v.Check(!s.PaymentID.IsZero(), "source_payment_id", core.CodeRequired)
		v.Check(s.CreditNoteID.IsZero(), "source_credit_note_id", core.CodeForbidden)
	case SettlementTypeCreditNote:
		v.Check(!s.CreditNoteID.IsZero(), "source_credit_note_id", core.CodeRequired)
		v.Check(s.PaymentID.IsZero(), "source_payment_id", core.CodeForbidden)
	}
	v.Positive("amount", s.Amount)
	v.Currency("amount_currency", s.Currency)
	return v.Err()
}

// CustomSection is a block of text printed on invoices (domain §11.9).
type CustomSection struct {
	// ID identifies the section.
	ID core.InvoiceCustomSectionID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Code is unique per organization.
	Code string
	// Name is the section's name.
	Name string
	// DisplayName is the printed title.
	DisplayName *string
	// Details is the printed text.
	Details *string
	// Description is free text, not printed.
	Description *string
	// SectionType is the origin of the section.
	SectionType CustomSectionType
	// DeletedAt is set once deleted; a deleted section is no longer
	// found, and its code is free again.
	DeletedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the invariants of a custom section.
func (s *CustomSection) Validate() error {
	var v core.Validator
	v.Code("code", s.Code, 255)
	v.String("name", s.Name, 255)
	v.Enum("section_type", s.SectionType)
	return v.Err()
}

// SectionSelection is one level of the custom section selection (§11.9).
type SectionSelection struct {
	// Skip disables custom sections at this level and below.
	Skip bool
	// SectionIDs are the sections selected at this level; empty inherits.
	SectionIDs []core.InvoiceCustomSectionID
}

// ResolveCustomSections returns the manual sections printed on an invoice:
// the subscription's selection, else the customer's, else the billing
// entity's defaults; a skip flag on the subscription or customer disables
// them (domain §11.9). The billing entity level has no skip flag.
func ResolveCustomSections(subscription, customer SectionSelection, billingEntityDefaults []core.InvoiceCustomSectionID) []core.InvoiceCustomSectionID {
	switch {
	case subscription.Skip || customer.Skip:
		return nil
	case len(subscription.SectionIDs) > 0:
		return slices.Clone(subscription.SectionIDs)
	case len(customer.SectionIDs) > 0:
		return slices.Clone(customer.SectionIDs)
	default:
		return slices.Clone(billingEntityDefaults)
	}
}

// ErrorDetail records an error of an invoice's processing.
type ErrorDetail struct {
	// ID identifies the error detail.
	ID core.ErrorDetailID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the invoice the error is about.
	InvoiceID core.InvoiceID
	// Code is the kind of error.
	Code ErrorCode
	// Details is the provider or engine payload.
	Details json.RawMessage
	// CreatedAt is the creation instant.
	CreatedAt time.Time
}

// RetriedTaxErrorMessage is the text that a tax_error message contains
// when the tax provider hit its API limit; the failed invoices with such a
// message are retried periodically (lago
// app/jobs/clock/retry_failed_invoices_job.rb:11).
const RetriedTaxErrorMessage = "API limit"

// TaxRetried reports whether e is a tax_error whose message contains
// [RetriedTaxErrorMessage], compared case-insensitively.
func (e *ErrorDetail) TaxRetried() bool {
	if e.Code != ErrorCodeTaxError {
		return false
	}
	var d struct {
		Message *string `json:"tax_error_message"`
	}
	if json.Unmarshal(e.Details, &d) != nil || d.Message == nil {
		return false
	}
	return strings.Contains(strings.ToLower(*d.Message), strings.ToLower(RetriedTaxErrorMessage))
}

// Validate checks the invariants of an error detail.
func (e *ErrorDetail) Validate() error {
	var v core.Validator
	v.Check(!e.InvoiceID.IsZero(), "owner_id", core.CodeRequired)
	v.Enum("error_code", e.Code)
	return v.Err()
}
