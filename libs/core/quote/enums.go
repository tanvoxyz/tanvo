package quote

import (
	"github.com/tanvoxyz/tanvo/libs/core"
)

// OrderType is what accepting a quote does (domain §21.1).
type OrderType string

// OrderType values.
const (
	// OrderTypeSubscriptionCreation is "subscription_creation": creates a subscription.
	OrderTypeSubscriptionCreation OrderType = "subscription_creation"
	// OrderTypeSubscriptionAmendment is "subscription_amendment": amends an existing subscription.
	OrderTypeSubscriptionAmendment OrderType = "subscription_amendment"
	// OrderTypeOneOff is "one_off": issues a one-off invoice.
	OrderTypeOneOff OrderType = "one_off"
)

// OrderTypeValues returns every OrderType, in declaration order.
func OrderTypeValues() []OrderType {
	return []OrderType{OrderTypeSubscriptionCreation, OrderTypeSubscriptionAmendment, OrderTypeOneOff}
}

// Valid reports whether v is one of the declared OrderType values.
func (v OrderType) Valid() bool {
	switch v {
	case OrderTypeSubscriptionCreation, OrderTypeSubscriptionAmendment, OrderTypeOneOff:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v OrderType) String() string { return string(v) }

// ParseOrderType parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseOrderType(s string) (OrderType, error) {
	if v := OrderType(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("quote.OrderType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v OrderType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *OrderType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseOrderType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// VersionStatus is the lifecycle status of a quote version (domain §21.1).
type VersionStatus string

// VersionStatus values.
const (
	// VersionStatusDraft is "draft": being edited.
	VersionStatusDraft VersionStatus = "draft"
	// VersionStatusApproved is "approved": approved; an order form exists.
	VersionStatusApproved VersionStatus = "approved"
	// VersionStatusVoided is "voided": no longer valid.
	VersionStatusVoided VersionStatus = "voided"
)

// VersionStatusValues returns every VersionStatus, in declaration order.
func VersionStatusValues() []VersionStatus {
	return []VersionStatus{VersionStatusDraft, VersionStatusApproved, VersionStatusVoided}
}

// Valid reports whether v is one of the declared VersionStatus values.
func (v VersionStatus) Valid() bool {
	switch v {
	case VersionStatusDraft, VersionStatusApproved, VersionStatusVoided:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v VersionStatus) String() string { return string(v) }

// ParseVersionStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseVersionStatus(s string) (VersionStatus, error) {
	if v := VersionStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("quote.VersionStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v VersionStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *VersionStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseVersionStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// VersionVoidReason tells why a quote version was voided (domain §21.1).
type VersionVoidReason string

// VersionVoidReason values.
const (
	// VersionVoidReasonManual is "manual": voided by a user.
	VersionVoidReasonManual VersionVoidReason = "manual"
	// VersionVoidReasonSuperseded is "superseded": replaced by a newer version.
	VersionVoidReasonSuperseded VersionVoidReason = "superseded"
	// VersionVoidReasonCascadeOfExpired is "cascade_of_expired": its order form expired.
	VersionVoidReasonCascadeOfExpired VersionVoidReason = "cascade_of_expired"
	// VersionVoidReasonCascadeOfVoided is "cascade_of_voided": its order form was voided.
	VersionVoidReasonCascadeOfVoided VersionVoidReason = "cascade_of_voided"
)

// VersionVoidReasonValues returns every VersionVoidReason, in declaration order.
func VersionVoidReasonValues() []VersionVoidReason {
	return []VersionVoidReason{VersionVoidReasonManual, VersionVoidReasonSuperseded, VersionVoidReasonCascadeOfExpired, VersionVoidReasonCascadeOfVoided}
}

// Valid reports whether v is one of the declared VersionVoidReason values.
func (v VersionVoidReason) Valid() bool {
	switch v {
	case VersionVoidReasonManual, VersionVoidReasonSuperseded, VersionVoidReasonCascadeOfExpired, VersionVoidReasonCascadeOfVoided:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v VersionVoidReason) String() string { return string(v) }

// ParseVersionVoidReason parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseVersionVoidReason(s string) (VersionVoidReason, error) {
	if v := VersionVoidReason(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("quote.VersionVoidReason", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v VersionVoidReason) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *VersionVoidReason) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseVersionVoidReason(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// OrderFormStatus is the lifecycle status of an order form (domain §21.2).
type OrderFormStatus string

// OrderFormStatus values.
const (
	// OrderFormStatusGenerated is "generated": awaiting signature.
	OrderFormStatusGenerated OrderFormStatus = "generated"
	// OrderFormStatusSigned is "signed": signed by the customer.
	OrderFormStatusSigned OrderFormStatus = "signed"
	// OrderFormStatusExpired is "expired": past its expiry without signature.
	OrderFormStatusExpired OrderFormStatus = "expired"
	// OrderFormStatusVoided is "voided": no longer valid.
	OrderFormStatusVoided OrderFormStatus = "voided"
)

// OrderFormStatusValues returns every OrderFormStatus, in declaration order.
func OrderFormStatusValues() []OrderFormStatus {
	return []OrderFormStatus{OrderFormStatusGenerated, OrderFormStatusSigned, OrderFormStatusExpired, OrderFormStatusVoided}
}

// Valid reports whether v is one of the declared OrderFormStatus values.
func (v OrderFormStatus) Valid() bool {
	switch v {
	case OrderFormStatusGenerated, OrderFormStatusSigned, OrderFormStatusExpired, OrderFormStatusVoided:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v OrderFormStatus) String() string { return string(v) }

// ParseOrderFormStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseOrderFormStatus(s string) (OrderFormStatus, error) {
	if v := OrderFormStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("quote.OrderFormStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v OrderFormStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *OrderFormStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseOrderFormStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// OrderFormVoidReason tells why an order form was voided (domain §21.2).
type OrderFormVoidReason string

// OrderFormVoidReason values.
const (
	// OrderFormVoidReasonManual is "manual": voided by a user.
	OrderFormVoidReasonManual OrderFormVoidReason = "manual"
	// OrderFormVoidReasonExpired is "expired": voided by its expiry.
	OrderFormVoidReasonExpired OrderFormVoidReason = "expired"
	// OrderFormVoidReasonInvalid is "invalid": its content is no longer valid.
	OrderFormVoidReasonInvalid OrderFormVoidReason = "invalid"
)

// OrderFormVoidReasonValues returns every OrderFormVoidReason, in declaration order.
func OrderFormVoidReasonValues() []OrderFormVoidReason {
	return []OrderFormVoidReason{OrderFormVoidReasonManual, OrderFormVoidReasonExpired, OrderFormVoidReasonInvalid}
}

// Valid reports whether v is one of the declared OrderFormVoidReason values.
func (v OrderFormVoidReason) Valid() bool {
	switch v {
	case OrderFormVoidReasonManual, OrderFormVoidReasonExpired, OrderFormVoidReasonInvalid:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v OrderFormVoidReason) String() string { return string(v) }

// ParseOrderFormVoidReason parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseOrderFormVoidReason(s string) (OrderFormVoidReason, error) {
	if v := OrderFormVoidReason(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("quote.OrderFormVoidReason", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v OrderFormVoidReason) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *OrderFormVoidReason) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseOrderFormVoidReason(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// OrderStatus is the lifecycle status of an order (domain §21.3).
type OrderStatus string

// OrderStatus values.
const (
	// OrderStatusCreated is "created": recorded, not executed.
	OrderStatusCreated OrderStatus = "created"
	// OrderStatusExecuted is "executed": applied.
	OrderStatusExecuted OrderStatus = "executed"
	// OrderStatusFailed is "failed": execution failed.
	OrderStatusFailed OrderStatus = "failed"
)

// OrderStatusValues returns every OrderStatus, in declaration order.
func OrderStatusValues() []OrderStatus {
	return []OrderStatus{OrderStatusCreated, OrderStatusExecuted, OrderStatusFailed}
}

// Valid reports whether v is one of the declared OrderStatus values.
func (v OrderStatus) Valid() bool {
	switch v {
	case OrderStatusCreated, OrderStatusExecuted, OrderStatusFailed:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v OrderStatus) String() string { return string(v) }

// ParseOrderStatus parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseOrderStatus(s string) (OrderStatus, error) {
	if v := OrderStatus(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("quote.OrderStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v OrderStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *OrderStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseOrderStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// ExecutionMode tells whether executing an order applies its content (domain §21.3).
type ExecutionMode string

// ExecutionMode values.
const (
	// ExecutionModeExecute is "execute": creates or amends the subscription, or issues the invoice.
	ExecutionModeExecute ExecutionMode = "execute"
	// ExecutionModeOrderOnly is "order_only": records the order without applying it.
	ExecutionModeOrderOnly ExecutionMode = "order_only"
)

// ExecutionModeValues returns every ExecutionMode, in declaration order.
func ExecutionModeValues() []ExecutionMode {
	return []ExecutionMode{ExecutionModeExecute, ExecutionModeOrderOnly}
}

// Valid reports whether v is one of the declared ExecutionMode values.
func (v ExecutionMode) Valid() bool {
	switch v {
	case ExecutionModeExecute, ExecutionModeOrderOnly:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v ExecutionMode) String() string { return string(v) }

// ParseExecutionMode parses the stored form s, failing with [core.ErrInvalidValue]
// for anything but a declared value.
func ParseExecutionMode(s string) (ExecutionMode, error) {
	if v := ExecutionMode(s); v.Valid() {
		return v, nil
	}
	return "", core.InvalidEnum("quote.ExecutionMode", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v ExecutionMode) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *ExecutionMode) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseExecutionMode(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
