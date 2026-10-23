package core

// IssuingDateAnchor is the period boundary an invoice's issuing date is anchored to (domain §22.12); used by billing entities and customers.
type IssuingDateAnchor string

// IssuingDateAnchor values.
const (
	// IssuingDateAnchorCurrentPeriodEnd is "current_period_end": the end of the billed period.
	IssuingDateAnchorCurrentPeriodEnd IssuingDateAnchor = "current_period_end"
	// IssuingDateAnchorNextPeriodStart is "next_period_start": the start of the next period.
	IssuingDateAnchorNextPeriodStart IssuingDateAnchor = "next_period_start"
)

// IssuingDateAnchorValues returns every IssuingDateAnchor, in declaration order.
func IssuingDateAnchorValues() []IssuingDateAnchor {
	return []IssuingDateAnchor{IssuingDateAnchorCurrentPeriodEnd, IssuingDateAnchorNextPeriodStart}
}

// Valid reports whether v is one of the declared IssuingDateAnchor values.
func (v IssuingDateAnchor) Valid() bool {
	switch v {
	case IssuingDateAnchorCurrentPeriodEnd, IssuingDateAnchorNextPeriodStart:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v IssuingDateAnchor) String() string { return string(v) }

// ParseIssuingDateAnchor parses the stored form s, failing with [ErrInvalidValue]
// for anything but a declared value.
func ParseIssuingDateAnchor(s string) (IssuingDateAnchor, error) {
	if v := IssuingDateAnchor(s); v.Valid() {
		return v, nil
	}
	return "", InvalidEnum("core.IssuingDateAnchor", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v IssuingDateAnchor) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *IssuingDateAnchor) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseIssuingDateAnchor(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// IssuingDateAdjustment tells whether an issuing date keeps its anchor or follows the finalization date (domain §22.12).
type IssuingDateAdjustment string

// IssuingDateAdjustment values.
const (
	// IssuingDateAdjustmentKeepAnchor is "keep_anchor": keep the anchor date.
	IssuingDateAdjustmentKeepAnchor IssuingDateAdjustment = "keep_anchor"
	// IssuingDateAdjustmentAlignWithFinalizationDate is "align_with_finalization_date": move to the finalization date.
	IssuingDateAdjustmentAlignWithFinalizationDate IssuingDateAdjustment = "align_with_finalization_date"
)

// IssuingDateAdjustmentValues returns every IssuingDateAdjustment, in declaration order.
func IssuingDateAdjustmentValues() []IssuingDateAdjustment {
	return []IssuingDateAdjustment{IssuingDateAdjustmentKeepAnchor, IssuingDateAdjustmentAlignWithFinalizationDate}
}

// Valid reports whether v is one of the declared IssuingDateAdjustment values.
func (v IssuingDateAdjustment) Valid() bool {
	switch v {
	case IssuingDateAdjustmentKeepAnchor, IssuingDateAdjustmentAlignWithFinalizationDate:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v IssuingDateAdjustment) String() string { return string(v) }

// ParseIssuingDateAdjustment parses the stored form s, failing with [ErrInvalidValue]
// for anything but a declared value.
func ParseIssuingDateAdjustment(s string) (IssuingDateAdjustment, error) {
	if v := IssuingDateAdjustment(s); v.Valid() {
		return v, nil
	}
	return "", InvalidEnum("core.IssuingDateAdjustment", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v IssuingDateAdjustment) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *IssuingDateAdjustment) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseIssuingDateAdjustment(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// PaymentMethodType tells how a subscription, wallet or invoice is collected (domain §15.5).
type PaymentMethodType string

// PaymentMethodType values.
const (
	// PaymentMethodTypeProvider is "provider": charged automatically through a payment provider.
	PaymentMethodTypeProvider PaymentMethodType = "provider"
	// PaymentMethodTypeManual is "manual": paid out of band; no automatic charge.
	PaymentMethodTypeManual PaymentMethodType = "manual"
)

// PaymentMethodTypeValues returns every PaymentMethodType, in declaration order.
func PaymentMethodTypeValues() []PaymentMethodType {
	return []PaymentMethodType{PaymentMethodTypeProvider, PaymentMethodTypeManual}
}

// Valid reports whether v is one of the declared PaymentMethodType values.
func (v PaymentMethodType) Valid() bool {
	switch v {
	case PaymentMethodTypeProvider, PaymentMethodTypeManual:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v PaymentMethodType) String() string { return string(v) }

// ParsePaymentMethodType parses the stored form s, failing with [ErrInvalidValue]
// for anything but a declared value.
func ParsePaymentMethodType(s string) (PaymentMethodType, error) {
	if v := PaymentMethodType(s); v.Valid() {
		return v, nil
	}
	return "", InvalidEnum("core.PaymentMethodType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v PaymentMethodType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *PaymentMethodType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParsePaymentMethodType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// FeeType is the kind of a fee (domain §12); also used to restrict wallets.
type FeeType string

// FeeType values.
const (
	// FeeTypeSubscription is "subscription": the base fee of a plan.
	FeeTypeSubscription FeeType = "subscription"
	// FeeTypeCharge is "charge": a usage charge.
	FeeTypeCharge FeeType = "charge"
	// FeeTypeFixedCharge is "fixed_charge": a fixed charge.
	FeeTypeFixedCharge FeeType = "fixed_charge"
	// FeeTypeAddOn is "add_on": an add-on line of a one-off invoice.
	FeeTypeAddOn FeeType = "add_on"
	// FeeTypeCredit is "credit": a wallet credit purchase.
	FeeTypeCredit FeeType = "credit"
	// FeeTypeCommitment is "commitment": a minimum commitment true-up.
	FeeTypeCommitment FeeType = "commitment"
	// FeeTypeProduct is "product": a rate-card product fee.
	FeeTypeProduct FeeType = "product"
)

// FeeTypeValues returns every FeeType, in declaration order.
func FeeTypeValues() []FeeType {
	return []FeeType{FeeTypeSubscription, FeeTypeCharge, FeeTypeFixedCharge, FeeTypeAddOn, FeeTypeCredit, FeeTypeCommitment, FeeTypeProduct}
}

// Valid reports whether v is one of the declared FeeType values.
func (v FeeType) Valid() bool {
	switch v {
	case FeeTypeSubscription, FeeTypeCharge, FeeTypeFixedCharge, FeeTypeAddOn, FeeTypeCredit, FeeTypeCommitment, FeeTypeProduct:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v FeeType) String() string { return string(v) }

// ParseFeeType parses the stored form s, failing with [ErrInvalidValue]
// for anything but a declared value.
func ParseFeeType(s string) (FeeType, error) {
	if v := FeeType(s); v.Valid() {
		return v, nil
	}
	return "", InvalidEnum("core.FeeType", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v FeeType) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *FeeType) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseFeeType(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// PaymentStatus is the collection status of an invoice or payment request.
type PaymentStatus string

// PaymentStatus values.
const (
	// PaymentStatusPending is "pending": not paid yet.
	PaymentStatusPending PaymentStatus = "pending"
	// PaymentStatusSucceeded is "succeeded": fully paid.
	PaymentStatusSucceeded PaymentStatus = "succeeded"
	// PaymentStatusFailed is "failed": the last payment attempt failed.
	PaymentStatusFailed PaymentStatus = "failed"
)

// PaymentStatusValues returns every PaymentStatus, in declaration order.
func PaymentStatusValues() []PaymentStatus {
	return []PaymentStatus{PaymentStatusPending, PaymentStatusSucceeded, PaymentStatusFailed}
}

// Valid reports whether v is one of the declared PaymentStatus values.
func (v PaymentStatus) Valid() bool {
	switch v {
	case PaymentStatusPending, PaymentStatusSucceeded, PaymentStatusFailed:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v PaymentStatus) String() string { return string(v) }

// ParsePaymentStatus parses the stored form s, failing with [ErrInvalidValue]
// for anything but a declared value.
func ParsePaymentStatus(s string) (PaymentStatus, error) {
	if v := PaymentStatus(s); v.Valid() {
		return v, nil
	}
	return "", InvalidEnum("core.PaymentStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v PaymentStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *PaymentStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParsePaymentStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}

// RefundStatus is the status of a refund and of a credit note's refund.
type RefundStatus string

// RefundStatus values.
const (
	// RefundStatusPending is "pending": sent to the provider.
	RefundStatusPending RefundStatus = "pending"
	// RefundStatusSucceeded is "succeeded": refunded.
	RefundStatusSucceeded RefundStatus = "succeeded"
	// RefundStatusFailed is "failed": refused by the provider.
	RefundStatusFailed RefundStatus = "failed"
)

// RefundStatusValues returns every RefundStatus, in declaration order.
func RefundStatusValues() []RefundStatus {
	return []RefundStatus{RefundStatusPending, RefundStatusSucceeded, RefundStatusFailed}
}

// Valid reports whether v is one of the declared RefundStatus values.
func (v RefundStatus) Valid() bool {
	switch v {
	case RefundStatusPending, RefundStatusSucceeded, RefundStatusFailed:
		return true
	default:
		return false
	}
}

// String returns the stored form of v.
func (v RefundStatus) String() string { return string(v) }

// ParseRefundStatus parses the stored form s, failing with [ErrInvalidValue]
// for anything but a declared value.
func ParseRefundStatus(s string) (RefundStatus, error) {
	if v := RefundStatus(s); v.Valid() {
		return v, nil
	}
	return "", InvalidEnum("core.RefundStatus", s)
}

// MarshalText implements encoding.TextMarshaler.
func (v RefundStatus) MarshalText() ([]byte, error) { return []byte(v), nil }

// UnmarshalText implements encoding.TextUnmarshaler. An empty text decodes
// to the zero value (an unset optional enumeration).
func (v *RefundStatus) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*v = ""
		return nil
	}
	p, err := ParseRefundStatus(string(b))
	if err != nil {
		return err
	}
	*v = p
	return nil
}
