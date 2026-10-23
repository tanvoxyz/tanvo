package creditnote

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// Status is the lifecycle status of a credit note (domain §13.1).
type Status string

// Status values.
const (
	// StatusDraft is "draft": an automatic credit note of a draft invoice, finalized with it.
	StatusDraft Status = "draft"
	// StatusFinalized is "finalized": issued.
	StatusFinalized Status = "finalized"
	// StatusDeleted is "deleted": a draft deleted with its draft invoice,
	// kept and hidden (lago CreditNotes::DeleteService); terminal.
	StatusDeleted Status = "deleted"
)

// StatusValues returns every Status, in declaration order.
func StatusValues() []Status {
	return []Status{StatusDraft, StatusFinalized, StatusDeleted}
}

// Valid reports whether v is one of the declared Status values.
func (v Status) Valid() bool {
	switch v {
	case StatusDraft, StatusFinalized, StatusDeleted:
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
	return "", core.InvalidEnum("creditnote.Status", s)
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

// CreditStatus is the status of a credit note's credit balance (domain §13.2).
type CreditStatus string

// CreditStatus values.
const (
	// CreditStatusAvailable is "available": the balance can be applied to later invoices.
	CreditStatusAvailable CreditStatus = "available"
	// CreditStatusConsumed is "consumed": the balance reached 0.
	CreditStatusConsumed CreditStatus = "consumed"
	// CreditStatusVoided is "voided": the balance was voided.
	CreditStatusVoided CreditStatus = "voided"
)

// CreditStatusValues returns every CreditStatus, in declaration order.
func CreditStatusValues() []CreditStatus {
	return []CreditStatus{CreditStatusAvailable, CreditStatusConsumed, CreditStatusVoided}
}

// Valid reports whether v is one of the declared CreditStatus values.
func (v CreditStatus) Valid() bool {
	switch v {
	case CreditStatusAvailable, CreditStatusConsumed, CreditStatusVoided:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v CreditStatus) String() string { return string(v) }

// ParseCreditStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseCreditStatus(s string) (CreditStatus, error) {
	if v := CreditStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("creditnote.CreditStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v CreditStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *CreditStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseCreditStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Reason is why a credit note is issued (domain §13.1).
type Reason string

// Reason values.
const (
	// ReasonDuplicatedCharge is "duplicated_charge": a charge was billed twice.
	ReasonDuplicatedCharge Reason = "duplicated_charge"
	// ReasonProductUnsatisfactory is "product_unsatisfactory": the product did not satisfy the customer.
	ReasonProductUnsatisfactory Reason = "product_unsatisfactory"
	// ReasonOrderChange is "order_change": the order changed.
	ReasonOrderChange Reason = "order_change"
	// ReasonOrderCancellation is "order_cancellation": the order was canceled (also used by terminations).
	ReasonOrderCancellation Reason = "order_cancellation"
	// ReasonFraudulentCharge is "fraudulent_charge": the charge was fraudulent.
	ReasonFraudulentCharge Reason = "fraudulent_charge"
	// ReasonOther is "other": any other reason.
	ReasonOther Reason = "other"
)

// ReasonValues returns every Reason, in declaration order.
func ReasonValues() []Reason {
	return []Reason{ReasonDuplicatedCharge, ReasonProductUnsatisfactory, ReasonOrderChange, ReasonOrderCancellation, ReasonFraudulentCharge, ReasonOther}
}

// Valid reports whether v is one of the declared Reason values.
func (v Reason) Valid() bool {
	switch v {
	case ReasonDuplicatedCharge, ReasonProductUnsatisfactory, ReasonOrderChange, ReasonOrderCancellation, ReasonFraudulentCharge, ReasonOther:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Reason) String() string { return string(v) }

// ParseReason parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseReason(s string) (Reason, error) {
	if v := Reason(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("creditnote.Reason", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Reason) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Reason) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseReason(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
