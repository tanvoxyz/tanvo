package invoice

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Type is the kind of an invoice (domain §11.6).
type Type string

// Type values.
const (
	// TypeSubscription is "subscription": periodic, starting and terminating invoices of subscriptions.
	TypeSubscription Type = "subscription"
	// TypeCredit is "credit": a purchase of wallet credits.
	TypeCredit Type = "credit"
	// TypeOneOff is "one_off": add-on lines created on demand.
	TypeOneOff Type = "one_off"
	// TypeAdvanceCharges is "advance_charges": regrouped in-advance fees paid out of band.
	TypeAdvanceCharges Type = "advance_charges"
	// TypeProgressiveBilling is "progressive_billing": an invoice triggered by a usage threshold.
	TypeProgressiveBilling Type = "progressive_billing"
)

// TypeValues returns every Type, in declaration order.
func TypeValues() []Type {
	return []Type{TypeSubscription, TypeCredit, TypeOneOff, TypeAdvanceCharges, TypeProgressiveBilling}
}

// Valid reports whether v is one of the declared Type values.
func (v Type) Valid() bool {
	switch v {
	case TypeSubscription, TypeCredit, TypeOneOff, TypeAdvanceCharges, TypeProgressiveBilling:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Type) String() string { return string(v) }

// ParseType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseType(s string) (Type, error) {
	if v := Type(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("invoice.Type", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Type) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Type) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Status is the lifecycle status of an invoice (domain §11.2).
type Status string

// Status values.
const (
	// StatusGenerating is "generating": being built; invisible to the API.
	StatusGenerating Status = "generating"
	// StatusDraft is "draft": recomputed on refresh until the grace period ends.
	StatusDraft Status = "draft"
	// StatusOpen is "open": built, awaiting a gate (activation payment, provider taxes, credit payment).
	StatusOpen Status = "open"
	// StatusFinalized is "finalized": numbered, immutable and payable.
	StatusFinalized Status = "finalized"
	// StatusClosed is "closed": a zero-amount invoice that was not finalized; terminal.
	StatusClosed Status = "closed"
	// StatusVoided is "voided": voided; terminal.
	StatusVoided Status = "voided"
	// StatusFailed is "failed": the tax computation failed; can be retried.
	StatusFailed Status = "failed"
	// StatusDeleted is "deleted": a deleted draft, kept with its fees and
	// subscription periods; invisible to the API; terminal.
	StatusDeleted Status = "deleted"
)

// StatusValues returns every Status, in declaration order.
func StatusValues() []Status {
	return []Status{StatusGenerating, StatusDraft, StatusOpen, StatusFinalized, StatusClosed, StatusVoided, StatusFailed, StatusDeleted}
}

// Valid reports whether v is one of the declared Status values.
func (v Status) Valid() bool {
	switch v {
	case StatusGenerating, StatusDraft, StatusOpen, StatusFinalized, StatusClosed, StatusVoided, StatusFailed, StatusDeleted:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Status) String() string { return string(v) }

// ParseStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseStatus(s string) (Status, error) {
	if v := Status(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("invoice.Status", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Status) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Status) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// TaxStatus is the status of a provider tax computation (domain §17.4).
type TaxStatus string

// TaxStatus values.
const (
	// TaxStatusPending is "pending": the provider has not answered yet.
	TaxStatusPending TaxStatus = "pending"
	// TaxStatusSucceeded is "succeeded": taxes were computed.
	TaxStatusSucceeded TaxStatus = "succeeded"
	// TaxStatusFailed is "failed": the provider failed.
	TaxStatusFailed TaxStatus = "failed"
)

// TaxStatusValues returns every TaxStatus, in declaration order.
func TaxStatusValues() []TaxStatus {
	return []TaxStatus{TaxStatusPending, TaxStatusSucceeded, TaxStatusFailed}
}

// Valid reports whether v is one of the declared TaxStatus values.
func (v TaxStatus) Valid() bool {
	switch v {
	case TaxStatusPending, TaxStatusSucceeded, TaxStatusFailed:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v TaxStatus) String() string { return string(v) }

// ParseTaxStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseTaxStatus(s string) (TaxStatus, error) {
	if v := TaxStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("invoice.TaxStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v TaxStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *TaxStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseTaxStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// InvoicingReason is why an invoice bills a subscription (domain §11.1).
type InvoicingReason string

// InvoicingReason values.
const (
	// InvoicingReasonSubscriptionStarting is "subscription_starting": the starting invoice at activation.
	InvoicingReasonSubscriptionStarting InvoicingReason = "subscription_starting"
	// InvoicingReasonSubscriptionPeriodic is "subscription_periodic": a periodic billing day.
	InvoicingReasonSubscriptionPeriodic InvoicingReason = "subscription_periodic"
	// InvoicingReasonSubscriptionTerminating is "subscription_terminating": the termination invoice.
	InvoicingReasonSubscriptionTerminating InvoicingReason = "subscription_terminating"
	// InvoicingReasonInAdvanceCharge is "in_advance_charge": a fee of an in-advance charge event.
	InvoicingReasonInAdvanceCharge InvoicingReason = "in_advance_charge"
	// InvoicingReasonInAdvanceChargePeriodic is "in_advance_charge_periodic": the carried-over value of a recurring in-advance charge.
	InvoicingReasonInAdvanceChargePeriodic InvoicingReason = "in_advance_charge_periodic"
	// InvoicingReasonProgressiveBilling is "progressive_billing": a reached usage threshold.
	InvoicingReasonProgressiveBilling InvoicingReason = "progressive_billing"
)

// InvoicingReasonValues returns every InvoicingReason, in declaration order.
func InvoicingReasonValues() []InvoicingReason {
	return []InvoicingReason{InvoicingReasonSubscriptionStarting, InvoicingReasonSubscriptionPeriodic, InvoicingReasonSubscriptionTerminating, InvoicingReasonInAdvanceCharge, InvoicingReasonInAdvanceChargePeriodic, InvoicingReasonProgressiveBilling}
}

// Valid reports whether v is one of the declared InvoicingReason values.
func (v InvoicingReason) Valid() bool {
	switch v {
	case InvoicingReasonSubscriptionStarting, InvoicingReasonSubscriptionPeriodic, InvoicingReasonSubscriptionTerminating, InvoicingReasonInAdvanceCharge, InvoicingReasonInAdvanceChargePeriodic, InvoicingReasonProgressiveBilling:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v InvoicingReason) String() string { return string(v) }

// ParseInvoicingReason parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseInvoicingReason(s string) (InvoicingReason, error) {
	if v := InvoicingReason(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("invoice.InvoicingReason", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v InvoicingReason) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *InvoicingReason) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseInvoicingReason(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// FeePaymentStatus is the payment status of a fee, managed for unbilled in-advance fees (domain §12.1).
type FeePaymentStatus string

// FeePaymentStatus values.
const (
	// FeePaymentStatusPending is "pending": not paid yet.
	FeePaymentStatusPending FeePaymentStatus = "pending"
	// FeePaymentStatusSucceeded is "succeeded": paid.
	FeePaymentStatusSucceeded FeePaymentStatus = "succeeded"
	// FeePaymentStatusFailed is "failed": the payment failed.
	FeePaymentStatusFailed FeePaymentStatus = "failed"
	// FeePaymentStatusRefunded is "refunded": refunded.
	FeePaymentStatusRefunded FeePaymentStatus = "refunded"
)

// FeePaymentStatusValues returns every FeePaymentStatus, in declaration order.
func FeePaymentStatusValues() []FeePaymentStatus {
	return []FeePaymentStatus{FeePaymentStatusPending, FeePaymentStatusSucceeded, FeePaymentStatusFailed, FeePaymentStatusRefunded}
}

// Valid reports whether v is one of the declared FeePaymentStatus values.
func (v FeePaymentStatus) Valid() bool {
	switch v {
	case FeePaymentStatusPending, FeePaymentStatusSucceeded, FeePaymentStatusFailed, FeePaymentStatusRefunded:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v FeePaymentStatus) String() string { return string(v) }

// ParseFeePaymentStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseFeePaymentStatus(s string) (FeePaymentStatus, error) {
	if v := FeePaymentStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("invoice.FeePaymentStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v FeePaymentStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *FeePaymentStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseFeePaymentStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// SettlementType is how an invoice is settled (domain §11.5).
type SettlementType string

// SettlementType values.
const (
	// SettlementTypePayment is "payment": by a payment.
	SettlementTypePayment SettlementType = "payment"
	// SettlementTypeCreditNote is "credit_note": by an offset credit note.
	SettlementTypeCreditNote SettlementType = "credit_note"
)

// SettlementTypeValues returns every SettlementType, in declaration order.
func SettlementTypeValues() []SettlementType {
	return []SettlementType{SettlementTypePayment, SettlementTypeCreditNote}
}

// Valid reports whether v is one of the declared SettlementType values.
func (v SettlementType) Valid() bool {
	switch v {
	case SettlementTypePayment, SettlementTypeCreditNote:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v SettlementType) String() string { return string(v) }

// ParseSettlementType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseSettlementType(s string) (SettlementType, error) {
	if v := SettlementType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("invoice.SettlementType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v SettlementType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *SettlementType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseSettlementType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// CustomSectionType is the origin of an invoice custom section (domain §11.9).
type CustomSectionType string

// CustomSectionType values.
const (
	// CustomSectionTypeManual is "manual": created by a user.
	CustomSectionTypeManual CustomSectionType = "manual"
	// CustomSectionTypeSystemGenerated is "system_generated": attached by a feature (for example a self-billing mention).
	CustomSectionTypeSystemGenerated CustomSectionType = "system_generated"
)

// CustomSectionTypeValues returns every CustomSectionType, in declaration order.
func CustomSectionTypeValues() []CustomSectionType {
	return []CustomSectionType{CustomSectionTypeManual, CustomSectionTypeSystemGenerated}
}

// Valid reports whether v is one of the declared CustomSectionType values.
func (v CustomSectionType) Valid() bool {
	switch v {
	case CustomSectionTypeManual, CustomSectionTypeSystemGenerated:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v CustomSectionType) String() string { return string(v) }

// ParseCustomSectionType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseCustomSectionType(s string) (CustomSectionType, error) {
	if v := CustomSectionType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("invoice.CustomSectionType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v CustomSectionType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *CustomSectionType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseCustomSectionType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ErrorCode is the kind of an error recorded on an invoice.
type ErrorCode string

// ErrorCode values.
const (
	// ErrorCodeNotProvided is "not_provided": no detail available.
	ErrorCodeNotProvided ErrorCode = "not_provided"
	// ErrorCodeTaxError is "tax_error": the tax provider failed to compute taxes.
	ErrorCodeTaxError ErrorCode = "tax_error"
	// ErrorCodeTaxVoidingError is "tax_voiding_error": the tax provider failed to void taxes.
	ErrorCodeTaxVoidingError ErrorCode = "tax_voiding_error"
	// ErrorCodeInvoiceGenerationError is "invoice_generation_error": the invoice could not be generated.
	ErrorCodeInvoiceGenerationError ErrorCode = "invoice_generation_error"
)

// ErrorCodeValues returns every ErrorCode, in declaration order.
func ErrorCodeValues() []ErrorCode {
	return []ErrorCode{ErrorCodeNotProvided, ErrorCodeTaxError, ErrorCodeTaxVoidingError, ErrorCodeInvoiceGenerationError}
}

// Valid reports whether v is one of the declared ErrorCode values.
func (v ErrorCode) Valid() bool {
	switch v {
	case ErrorCodeNotProvided, ErrorCodeTaxError, ErrorCodeTaxVoidingError, ErrorCodeInvoiceGenerationError:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ErrorCode) String() string { return string(v) }

// ParseErrorCode parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseErrorCode(s string) (ErrorCode, error) {
	if v := ErrorCode(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("invoice.ErrorCode", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ErrorCode) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ErrorCode) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseErrorCode(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// AdjustmentMode is what a fee adjustment on a draft invoice changes (domain §12.3).
type AdjustmentMode string

// AdjustmentMode values.
const (
	// AdjustmentModeDisplayName is "display_name": the display name only.
	AdjustmentModeDisplayName AdjustmentMode = "display_name"
	// AdjustmentModeUnits is "units": the units; the price is recomputed from the charge model.
	AdjustmentModeUnits AdjustmentMode = "units"
	// AdjustmentModeUnitsAndUnitPrice is "units_and_unit_price": the units and the unit price; amount = units × unit price.
	AdjustmentModeUnitsAndUnitPrice AdjustmentMode = "units_and_unit_price"
)

// AdjustmentModeValues returns every AdjustmentMode, in declaration order.
func AdjustmentModeValues() []AdjustmentMode {
	return []AdjustmentMode{AdjustmentModeDisplayName, AdjustmentModeUnits, AdjustmentModeUnitsAndUnitPrice}
}

// Valid reports whether v is one of the declared AdjustmentMode values.
func (v AdjustmentMode) Valid() bool {
	switch v {
	case AdjustmentModeDisplayName, AdjustmentModeUnits, AdjustmentModeUnitsAndUnitPrice:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v AdjustmentMode) String() string { return string(v) }

// ParseAdjustmentMode parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseAdjustmentMode(s string) (AdjustmentMode, error) {
	if v := AdjustmentMode(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("invoice.AdjustmentMode", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v AdjustmentMode) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *AdjustmentMode) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseAdjustmentMode(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
