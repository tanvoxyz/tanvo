package payment

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// IntentID identifies a hosted checkout link.
type IntentID = core.PaymentIntentID

// IntentValidity is how long a hosted checkout link is valid (domain
// §15.1).
const IntentValidity = 24 * time.Hour

// Intent is a hosted checkout link for an invoice (domain §15.1); at most
// one active link per invoice.
type Intent struct {
	// ID identifies the link.
	ID IntentID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// InvoiceID is the invoice paid through the link.
	InvoiceID core.InvoiceID
	// Status is the link status.
	Status IntentStatus
	// PaymentURL is the hosted checkout URL.
	PaymentURL *string
	// ProviderSessionID is the provider's checkout session identifier.
	ProviderSessionID *string
	// ExpiresAt is when the link stops being valid.
	ExpiresAt time.Time
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// NewIntent returns an active link for invoice, valid [IntentValidity]
// from now.
func NewIntent(org core.OrganizationID, id IntentID, invoice core.InvoiceID, now time.Time) *Intent {
	return &Intent{
		ID:             id,
		OrganizationID: org,
		InvoiceID:      invoice,
		Status:         IntentStatusActive,
		ExpiresAt:      now.Add(IntentValidity),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// Validate checks the link.
func (i *Intent) Validate() error {
	var v core.Validator
	v.Check(!i.InvoiceID.IsZero(), "invoice_id", core.CodeRequired)
	v.Enum("status", i.Status)
	v.Check(!i.ExpiresAt.IsZero(), "expires_at", core.CodeRequired)
	return v.Err()
}

// NonExpired reports whether the link is still within its validity at now.
func (i *Intent) NonExpired(now time.Time) bool { return i.ExpiresAt.After(now) }

// AwaitingExpiration reports whether the link is active but past its
// validity at now.
func (i *Intent) AwaitingExpiration(now time.Time) bool {
	return i.Status == IntentStatusActive && !i.ExpiresAt.After(now)
}

// Expire marks an active link expired at now.
func (i *Intent) Expire(now time.Time) error {
	if i.Status != IntentStatusActive {
		return &core.TransitionError{Entity: "payment_intent", From: string(i.Status), To: string(IntentStatusExpired)}
	}
	i.Status = IntentStatusExpired
	i.UpdatedAt = now
	return nil
}
