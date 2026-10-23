package quote

import (
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Signed document limits (lago OrderForm::SIGNED_DOCUMENT_CONTENT_TYPES and
// SIGNED_DOCUMENT_MAX_SIZE, app/models/order_form.rb:19-20).
const (
	// SignedDocumentMaxSize is the largest signed document, in bytes
	// (exclusive).
	SignedDocumentMaxSize = 10 << 20
)

// SignedDocumentContentTypes are the accepted signed document types.
var SignedDocumentContentTypes = []string{"application/pdf", "image/jpeg", "image/png"}

// OrderForm is the document a customer signs for an approved quote version
// (domain §21.2). A version has at most one order form.
type OrderForm struct {
	// ID identifies the order form.
	ID core.OrderFormID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// Number is unique per organization, see [FormatNumber].
	Number string
	// SequentialID is the sequence of the number, per organization.
	SequentialID int
	// CustomerID is the customer.
	CustomerID core.CustomerID
	// QuoteVersionID is the approved version.
	QuoteVersionID core.QuoteVersionID
	// Status is the lifecycle state.
	Status OrderFormStatus
	// VoidReason is set once voided or expired.
	VoidReason OrderFormVoidReason
	// ExpiresAt is when the order form expires unless signed; nil never
	// expires.
	ExpiresAt *time.Time
	// SignedAt is set once signed.
	SignedAt *time.Time
	// SignedDocument references the uploaded signed document.
	SignedDocument core.BlobRef
	// VoidedAt is set once voided or expired.
	VoidedAt *time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewOrderForm generates the order form of an approved version.
func NewOrderForm(id core.OrderFormID, q *Quote, v *Version, expiresAt *time.Time, now time.Time) (*OrderForm, error) {
	if v.Status != VersionStatusApproved {
		return nil, core.NewRuleError(CodeNotApprovable, "an order form needs an approved quote version")
	}
	return &OrderForm{
		ID:             id,
		OrganizationID: v.OrganizationID,
		CustomerID:     q.CustomerID,
		QuoteVersionID: v.ID,
		Status:         OrderFormStatusGenerated,
		ExpiresAt:      expiresAt,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// AssignNumber sets SequentialID and, unless a number is already set,
// Number from the creation year.
func (f *OrderForm) AssignNumber(seq int) {
	f.SequentialID = seq
	if f.Number == "" {
		f.Number = FormatNumber(OrderFormPrefix, f.CreatedAt.UTC().Year(), seq)
	}
}

// Validate checks the invariants of domain §21.2.
func (f *OrderForm) Validate() error {
	var v core.Validator
	v.Required("number", f.Number)
	v.Check(!f.CustomerID.IsZero(), "customer_id", core.CodeRequired)
	v.Check(!f.QuoteVersionID.IsZero(), "quote_version_id", core.CodeRequired)
	v.Enum("status", f.Status)
	v.OptionalEnum("void_reason", f.VoidReason)
	if f.Status == OrderFormStatusVoided || f.Status == OrderFormStatusExpired {
		v.Check(f.VoidReason != "", "void_reason", core.CodeRequired)
	}
	if f.Status == OrderFormStatusSigned {
		v.Check(f.SignedAt != nil, "signed_at", core.CodeRequired)
	}
	return v.Err()
}

var orderFormTransitions = map[OrderFormStatus][]OrderFormStatus{
	OrderFormStatusGenerated: {OrderFormStatusSigned, OrderFormStatusExpired, OrderFormStatusVoided},
}

// CanTransitionOrderForm reports whether an order form may go from one
// status to another: generated → signed | expired | voided.
func CanTransitionOrderForm(from, to OrderFormStatus) bool {
	return slices.Contains(orderFormTransitions[from], to)
}

func (f *OrderForm) to(s OrderFormStatus, now time.Time) error {
	if !CanTransitionOrderForm(f.Status, s) {
		return &core.TransitionError{Entity: "order_form", From: string(f.Status), To: string(s)}
	}
	f.Status = s
	f.UpdatedAt = now
	return nil
}

// Sign marks the order form signed at now with the optional signed
// document. The caller then creates the [Order].
func (f *OrderForm) Sign(document core.BlobRef, now time.Time) error {
	if err := f.to(OrderFormStatusSigned, now); err != nil {
		return err
	}
	f.SignedAt, f.SignedDocument = &now, document
	return nil
}

// Expire expires the order form at now and voids its version v as
// cascade_of_expired (lago OrderForms::ExpireService).
func (f *OrderForm) Expire(v *Version, now time.Time) error {
	return f.close(OrderFormStatusExpired, OrderFormVoidReasonExpired, VersionVoidReasonCascadeOfExpired, v, now)
}

// Void voids the order form at now for reason (manual or invalid) and
// voids its version v as cascade_of_voided.
func (f *OrderForm) Void(reason OrderFormVoidReason, v *Version, now time.Time) error {
	if reason != OrderFormVoidReasonManual && reason != OrderFormVoidReasonInvalid {
		return core.InvalidEnum("order form void reason", string(reason))
	}
	return f.close(OrderFormStatusVoided, reason, VersionVoidReasonCascadeOfVoided, v, now)
}

func (f *OrderForm) close(s OrderFormStatus, reason OrderFormVoidReason, cascade VersionVoidReason, v *Version, now time.Time) error {
	if v == nil || v.ID != f.QuoteVersionID {
		return core.NewRuleError(core.CodeInvalid, "the version of the order form is required")
	}
	if !CanTransitionOrderForm(f.Status, s) {
		return &core.TransitionError{Entity: "order_form", From: string(f.Status), To: string(s)}
	}
	if err := v.Void(cascade, now); err != nil {
		return err
	}
	_ = f.to(s, now)
	f.VoidReason, f.VoidedAt = reason, &now
	return nil
}

// Expirable reports whether a generated order form is due to expire at
// now: its expiry date, in the customer's time zone loc, is today or
// earlier (lago OrderForm.expirable).
func (f *OrderForm) Expirable(now time.Time, loc *time.Location) bool {
	if f.Status != OrderFormStatusGenerated || f.ExpiresAt == nil {
		return false
	}
	if loc == nil {
		loc = time.UTC
	}
	return !core.DateOf(*f.ExpiresAt, loc).After(core.DateOf(now, loc))
}
