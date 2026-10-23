package payment

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// PayableType is the kind of document a payment settles (domain §15.1).
type PayableType string

// PayableType values.
const (
	// PayableTypeInvoice is "invoice": an invoice.
	PayableTypeInvoice PayableType = "invoice"
	// PayableTypePaymentRequest is "payment_request": a payment request grouping invoices.
	PayableTypePaymentRequest PayableType = "payment_request"
)

// PayableTypeValues returns every PayableType, in declaration order.
func PayableTypeValues() []PayableType {
	return []PayableType{PayableTypeInvoice, PayableTypePaymentRequest}
}

// Valid reports whether v is one of the declared PayableType values.
func (v PayableType) Valid() bool {
	switch v {
	case PayableTypeInvoice, PayableTypePaymentRequest:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v PayableType) String() string { return string(v) }

// ParsePayableType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParsePayableType(s string) (PayableType, error) {
	if v := PayableType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("payment.PayableType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v PayableType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *PayableType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParsePayableType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// Kind is how a payment is collected (domain §15.1, PaymentKind).
type Kind string

// Kind values.
const (
	// KindProvider is "provider": through a payment provider connection.
	KindProvider Kind = "provider"
	// KindManual is "manual": recorded by hand.
	KindManual Kind = "manual"
)

// KindValues returns every Kind, in declaration order.
func KindValues() []Kind {
	return []Kind{KindProvider, KindManual}
}

// Valid reports whether v is one of the declared Kind values.
func (v Kind) Valid() bool {
	switch v {
	case KindProvider, KindManual:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v Kind) String() string { return string(v) }

// ParseKind parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseKind(s string) (Kind, error) {
	if v := Kind(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("payment.Kind", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v Kind) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *Kind) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseKind(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// State is the lifecycle status of a payment (domain §15.1, PaymentState).
type State string

// State values.
const (
	// StatePending is "pending": created, not yet processed.
	StatePending State = "pending"
	// StateProcessing is "processing": being processed by the provider.
	StateProcessing State = "processing"
	// StateSucceeded is "succeeded": collected.
	StateSucceeded State = "succeeded"
	// StateFailed is "failed": refused or errored.
	StateFailed State = "failed"
)

// StateValues returns every State, in declaration order.
func StateValues() []State {
	return []State{StatePending, StateProcessing, StateSucceeded, StateFailed}
}

// Valid reports whether v is one of the declared State values.
func (v State) Valid() bool {
	switch v {
	case StatePending, StateProcessing, StateSucceeded, StateFailed:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v State) String() string { return string(v) }

// ParseState parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseState(s string) (State, error) {
	if v := State(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("payment.State", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v State) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *State) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseState(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ConnectionMode is how an object selects its payment or integration connection (domain §15.5).
type ConnectionMode string

// ConnectionMode values.
const (
	// ConnectionModeSpecific is "specific": a named connection.
	ConnectionModeSpecific ConnectionMode = "specific"
	// ConnectionModeSkip is "skip": no automatic action.
	ConnectionModeSkip ConnectionMode = "skip"
	// ConnectionModeInherit is "inherit": the next level decides.
	ConnectionModeInherit ConnectionMode = "inherit"
)

// ConnectionModeValues returns every ConnectionMode, in declaration order.
func ConnectionModeValues() []ConnectionMode {
	return []ConnectionMode{ConnectionModeSpecific, ConnectionModeSkip, ConnectionModeInherit}
}

// Valid reports whether v is one of the declared ConnectionMode values.
func (v ConnectionMode) Valid() bool {
	switch v {
	case ConnectionModeSpecific, ConnectionModeSkip, ConnectionModeInherit:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ConnectionMode) String() string { return string(v) }

// ParseConnectionMode parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseConnectionMode(s string) (ConnectionMode, error) {
	if v := ConnectionMode(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("payment.ConnectionMode", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ConnectionMode) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ConnectionMode) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseConnectionMode(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// InboundWebhookStatus is the processing status of an inbound provider webhook (domain §15.6).
type InboundWebhookStatus string

// InboundWebhookStatus values.
const (
	// InboundWebhookStatusPending is "pending": stored, not processed.
	InboundWebhookStatusPending InboundWebhookStatus = "pending"
	// InboundWebhookStatusProcessing is "processing": being processed.
	InboundWebhookStatusProcessing InboundWebhookStatus = "processing"
	// InboundWebhookStatusSucceeded is "succeeded": processed.
	InboundWebhookStatusSucceeded InboundWebhookStatus = "succeeded"
	// InboundWebhookStatusFailed is "failed": processing failed.
	InboundWebhookStatusFailed InboundWebhookStatus = "failed"
)

// InboundWebhookStatusValues returns every InboundWebhookStatus, in declaration order.
func InboundWebhookStatusValues() []InboundWebhookStatus {
	return []InboundWebhookStatus{InboundWebhookStatusPending, InboundWebhookStatusProcessing, InboundWebhookStatusSucceeded, InboundWebhookStatusFailed}
}

// Valid reports whether v is one of the declared InboundWebhookStatus values.
func (v InboundWebhookStatus) Valid() bool {
	switch v {
	case InboundWebhookStatusPending, InboundWebhookStatusProcessing, InboundWebhookStatusSucceeded, InboundWebhookStatusFailed:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v InboundWebhookStatus) String() string { return string(v) }

// ParseInboundWebhookStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseInboundWebhookStatus(s string) (InboundWebhookStatus, error) {
	if v := InboundWebhookStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("payment.InboundWebhookStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v InboundWebhookStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *InboundWebhookStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseInboundWebhookStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// RefundReason is why a refund was issued (domain §15.3).
type RefundReason string

// RefundReason values.
const (
	// RefundReasonCreditNote is "credit_note": a credit note's refund amount.
	RefundReasonCreditNote RefundReason = "credit_note"
	// RefundReasonSubscriptionActivationExpired is "subscription_activation_expired": a payment-gated subscription expired before activation.
	RefundReasonSubscriptionActivationExpired RefundReason = "subscription_activation_expired"
)

// RefundReasonValues returns every RefundReason, in declaration order.
func RefundReasonValues() []RefundReason {
	return []RefundReason{RefundReasonCreditNote, RefundReasonSubscriptionActivationExpired}
}

// Valid reports whether v is one of the declared RefundReason values.
func (v RefundReason) Valid() bool {
	switch v {
	case RefundReasonCreditNote, RefundReasonSubscriptionActivationExpired:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v RefundReason) String() string { return string(v) }

// ParseRefundReason parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseRefundReason(s string) (RefundReason, error) {
	if v := RefundReason(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("payment.RefundReason", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v RefundReason) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *RefundReason) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseRefundReason(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// IntentStatus is the status of a hosted checkout link (domain §15.1).
type IntentStatus string

// IntentStatus values.
const (
	// IntentStatusActive is "active": the link can be used.
	IntentStatusActive IntentStatus = "active"
	// IntentStatusExpired is "expired": the link is no longer valid.
	IntentStatusExpired IntentStatus = "expired"
)

// IntentStatusValues returns every IntentStatus, in declaration order.
func IntentStatusValues() []IntentStatus {
	return []IntentStatus{IntentStatusActive, IntentStatusExpired}
}

// Valid reports whether v is one of the declared IntentStatus values.
func (v IntentStatus) Valid() bool {
	switch v {
	case IntentStatusActive, IntentStatusExpired:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v IntentStatus) String() string { return string(v) }

// ParseIntentStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseIntentStatus(s string) (IntentStatus, error) {
	if v := IntentStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("payment.IntentStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v IntentStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *IntentStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseIntentStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
