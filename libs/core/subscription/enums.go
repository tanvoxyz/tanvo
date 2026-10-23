package subscription

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Status is the lifecycle status of a subscription (domain §9.2).
type Status string

// Status values.
const (
	// StatusPending is "pending": created with a future subscription_at, or a downgrade waiting for the period end.
	StatusPending Status = "pending"
	// StatusIncomplete is "incomplete": activated but waiting for an activation rule.
	StatusIncomplete Status = "incomplete"
	// StatusActive is "active": billed and receiving events.
	StatusActive Status = "active"
	// StatusTerminated is "terminated": ended after being active; terminal.
	StatusTerminated Status = "terminated"
	// StatusCanceled is "canceled": ended before becoming active; terminal.
	StatusCanceled Status = "canceled"
)

// StatusValues returns every Status, in declaration order.
func StatusValues() []Status {
	return []Status{StatusPending, StatusIncomplete, StatusActive, StatusTerminated, StatusCanceled}
}

// Valid reports whether v is one of the declared Status values.
func (v Status) Valid() bool {
	switch v {
	case StatusPending, StatusIncomplete, StatusActive, StatusTerminated, StatusCanceled:
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
	return "", core.InvalidEnum("subscription.Status", s)
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

// CancellationReason tells why a subscription was canceled (domain §9.3).
type CancellationReason string

// CancellationReason values.
const (
	// CancellationReasonPaymentFailed is "payment_failed": the activation payment failed.
	CancellationReasonPaymentFailed CancellationReason = "payment_failed"
	// CancellationReasonTimeout is "timeout": the activation rule expired.
	CancellationReasonTimeout CancellationReason = "timeout"
	// CancellationReasonManual is "manual": terminated by a request.
	CancellationReasonManual CancellationReason = "manual"
)

// CancellationReasonValues returns every CancellationReason, in declaration order.
func CancellationReasonValues() []CancellationReason {
	return []CancellationReason{CancellationReasonPaymentFailed, CancellationReasonTimeout, CancellationReasonManual}
}

// Valid reports whether v is one of the declared CancellationReason values.
func (v CancellationReason) Valid() bool {
	switch v {
	case CancellationReasonPaymentFailed, CancellationReasonTimeout, CancellationReasonManual:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v CancellationReason) String() string { return string(v) }

// ParseCancellationReason parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseCancellationReason(s string) (CancellationReason, error) {
	if v := CancellationReason(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("subscription.CancellationReason", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v CancellationReason) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *CancellationReason) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseCancellationReason(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// TerminationCreditNote tells how the unused part of an in-advance subscription is returned on termination (domain §13.3).
type TerminationCreditNote string

// TerminationCreditNote values.
const (
	// TerminationCreditNoteCredit is "credit": a credit note balance.
	TerminationCreditNoteCredit TerminationCreditNote = "credit"
	// TerminationCreditNoteRefund is "refund": a refund up to what was paid, the rest as credit.
	TerminationCreditNoteRefund TerminationCreditNote = "refund"
	// TerminationCreditNoteOffset is "offset": an offset against the invoice's due amount, the rest as credit.
	TerminationCreditNoteOffset TerminationCreditNote = "offset"
	// TerminationCreditNoteSkip is "skip": no credit note.
	TerminationCreditNoteSkip TerminationCreditNote = "skip"
)

// TerminationCreditNoteValues returns every TerminationCreditNote, in declaration order.
func TerminationCreditNoteValues() []TerminationCreditNote {
	return []TerminationCreditNote{TerminationCreditNoteCredit, TerminationCreditNoteRefund, TerminationCreditNoteOffset, TerminationCreditNoteSkip}
}

// Valid reports whether v is one of the declared TerminationCreditNote values.
func (v TerminationCreditNote) Valid() bool {
	switch v {
	case TerminationCreditNoteCredit, TerminationCreditNoteRefund, TerminationCreditNoteOffset, TerminationCreditNoteSkip:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v TerminationCreditNote) String() string { return string(v) }

// ParseTerminationCreditNote parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseTerminationCreditNote(s string) (TerminationCreditNote, error) {
	if v := TerminationCreditNote(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("subscription.TerminationCreditNote", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v TerminationCreditNote) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *TerminationCreditNote) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseTerminationCreditNote(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// TerminationInvoice tells whether termination issues an invoice (domain §22.14).
type TerminationInvoice string

// TerminationInvoice values.
const (
	// TerminationInvoiceGenerate is "generate": issue the termination invoice.
	TerminationInvoiceGenerate TerminationInvoice = "generate"
	// TerminationInvoiceSkip is "skip": issue none.
	TerminationInvoiceSkip TerminationInvoice = "skip"
)

// TerminationInvoiceValues returns every TerminationInvoice, in declaration order.
func TerminationInvoiceValues() []TerminationInvoice {
	return []TerminationInvoice{TerminationInvoiceGenerate, TerminationInvoiceSkip}
}

// Valid reports whether v is one of the declared TerminationInvoice values.
func (v TerminationInvoice) Valid() bool {
	switch v {
	case TerminationInvoiceGenerate, TerminationInvoiceSkip:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v TerminationInvoice) String() string { return string(v) }

// ParseTerminationInvoice parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseTerminationInvoice(s string) (TerminationInvoice, error) {
	if v := TerminationInvoice(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("subscription.TerminationInvoice", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v TerminationInvoice) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *TerminationInvoice) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseTerminationInvoice(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ActivationRuleType is the kind of condition an activation rule waits for (domain §9.3).
type ActivationRuleType string

// ActivationRuleType values.
const (
	// ActivationRuleTypePayment is "payment": a successful payment of the starting invoice.
	ActivationRuleTypePayment ActivationRuleType = "payment"
)

// ActivationRuleTypeValues returns every ActivationRuleType, in declaration order.
func ActivationRuleTypeValues() []ActivationRuleType {
	return []ActivationRuleType{ActivationRuleTypePayment}
}

// Valid reports whether v is one of the declared ActivationRuleType values.
func (v ActivationRuleType) Valid() bool {
	switch v {
	case ActivationRuleTypePayment:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ActivationRuleType) String() string { return string(v) }

// ParseActivationRuleType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseActivationRuleType(s string) (ActivationRuleType, error) {
	if v := ActivationRuleType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("subscription.ActivationRuleType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ActivationRuleType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ActivationRuleType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseActivationRuleType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ActivationRuleStatus is the lifecycle status of an activation rule (domain §9.3).
type ActivationRuleStatus string

// ActivationRuleStatus values.
const (
	// ActivationRuleStatusInactive is "inactive": not evaluated yet.
	ActivationRuleStatusInactive ActivationRuleStatus = "inactive"
	// ActivationRuleStatusPending is "pending": waiting for its condition.
	ActivationRuleStatusPending ActivationRuleStatus = "pending"
	// ActivationRuleStatusSatisfied is "satisfied": the condition was met.
	ActivationRuleStatusSatisfied ActivationRuleStatus = "satisfied"
	// ActivationRuleStatusNotApplicable is "not_applicable": the activation produced nothing to wait for.
	ActivationRuleStatusNotApplicable ActivationRuleStatus = "not_applicable"
	// ActivationRuleStatusDeclined is "declined": the subscription was terminated while waiting.
	ActivationRuleStatusDeclined ActivationRuleStatus = "declined"
	// ActivationRuleStatusFailed is "failed": the condition failed (payment failed).
	ActivationRuleStatusFailed ActivationRuleStatus = "failed"
	// ActivationRuleStatusExpired is "expired": the timeout elapsed.
	ActivationRuleStatusExpired ActivationRuleStatus = "expired"
)

// ActivationRuleStatusValues returns every ActivationRuleStatus, in declaration order.
func ActivationRuleStatusValues() []ActivationRuleStatus {
	return []ActivationRuleStatus{ActivationRuleStatusInactive, ActivationRuleStatusPending, ActivationRuleStatusSatisfied, ActivationRuleStatusNotApplicable, ActivationRuleStatusDeclined, ActivationRuleStatusFailed, ActivationRuleStatusExpired}
}

// Valid reports whether v is one of the declared ActivationRuleStatus values.
func (v ActivationRuleStatus) Valid() bool {
	switch v {
	case ActivationRuleStatusInactive, ActivationRuleStatusPending, ActivationRuleStatusSatisfied, ActivationRuleStatusNotApplicable, ActivationRuleStatusDeclined, ActivationRuleStatusFailed, ActivationRuleStatusExpired:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ActivationRuleStatus) String() string { return string(v) }

// ParseActivationRuleStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseActivationRuleStatus(s string) (ActivationRuleStatus, error) {
	if v := ActivationRuleStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("subscription.ActivationRuleStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ActivationRuleStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ActivationRuleStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseActivationRuleStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// EditableField is a field of a subscription that an update may change (domain §9.5).
type EditableField string

// EditableField values.
const (
	// EditableFieldName is the EditableField "name".
	EditableFieldName EditableField = "name"
	// EditableFieldEndingAt is the EditableField "ending_at".
	EditableFieldEndingAt EditableField = "ending_at"
	// EditableFieldSubscriptionAt is the EditableField "subscription_at".
	EditableFieldSubscriptionAt EditableField = "subscription_at"
	// EditableFieldPurchaseOrderNumber is the EditableField "purchase_order_number".
	EditableFieldPurchaseOrderNumber EditableField = "purchase_order_number"
	// EditableFieldPaymentMethod is the EditableField "payment_method".
	EditableFieldPaymentMethod EditableField = "payment_method"
	// EditableFieldProgressiveBillingDisabled is the EditableField "progressive_billing_disabled".
	EditableFieldProgressiveBillingDisabled EditableField = "progressive_billing_disabled"
	// EditableFieldConsolidateInvoice is the EditableField "consolidate_invoice".
	EditableFieldConsolidateInvoice EditableField = "consolidate_invoice"
	// EditableFieldTerminationOptions is the EditableField "termination_options".
	EditableFieldTerminationOptions EditableField = "termination_options"
	// EditableFieldInvoiceCustomSections is the EditableField "invoice_custom_sections".
	EditableFieldInvoiceCustomSections EditableField = "invoice_custom_sections"
	// EditableFieldPlanOverrides is the EditableField "plan_overrides".
	EditableFieldPlanOverrides EditableField = "plan_overrides"
)

// EditableFieldValues returns every EditableField, in declaration order.
func EditableFieldValues() []EditableField {
	return []EditableField{EditableFieldName, EditableFieldEndingAt, EditableFieldSubscriptionAt, EditableFieldPurchaseOrderNumber, EditableFieldPaymentMethod, EditableFieldProgressiveBillingDisabled, EditableFieldConsolidateInvoice, EditableFieldTerminationOptions, EditableFieldInvoiceCustomSections, EditableFieldPlanOverrides}
}

// Valid reports whether v is one of the declared EditableField values.
func (v EditableField) Valid() bool {
	switch v {
	case EditableFieldName, EditableFieldEndingAt, EditableFieldSubscriptionAt, EditableFieldPurchaseOrderNumber, EditableFieldPaymentMethod, EditableFieldProgressiveBillingDisabled, EditableFieldConsolidateInvoice, EditableFieldTerminationOptions, EditableFieldInvoiceCustomSections, EditableFieldPlanOverrides:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v EditableField) String() string { return string(v) }

// ParseEditableField parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseEditableField(s string) (EditableField, error) {
	if v := EditableField(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("subscription.EditableField", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v EditableField) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *EditableField) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseEditableField(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
