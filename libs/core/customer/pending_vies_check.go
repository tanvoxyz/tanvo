package customer

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// PendingVIESCheck is the VIES check of a customer's VAT number waiting to
// be retried (domain §22.10, lago PendingViesCheck): at most one per
// customer, kept while the customer's EU tax is undecided. While it exists
// the customer's invoices wait for their taxes.
type PendingVIESCheck struct {
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// CustomerID is the customer whose VAT number is checked.
	CustomerID core.CustomerID
	// BillingEntityID is the customer's billing entity when the check was
	// scheduled.
	BillingEntityID core.BillingEntityID
	// TaxIdentificationNumber is the checked VAT number.
	TaxIdentificationNumber *string
	// AttemptsCount counts the failed attempts since the check was
	// scheduled (≥ 0).
	AttemptsCount int
	// LastAttemptAt is the instant of the last failed attempt.
	LastAttemptAt *time.Time
	// LastErrorType classifies the last failure (rate_limit, timeout,
	// blocked, invalid_requester, service_unavailable,
	// member_state_unavailable or unknown).
	LastErrorType *string
	// LastErrorMessage is the message of the last failure.
	LastErrorMessage *string
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// VIESErrorTypes returns the values of [PendingVIESCheck.LastErrorType]
// (pending_vies_check.rb:4-14).
func VIESErrorTypes() []string {
	return []string{"rate_limit", "timeout", "blocked", "invalid_requester", "service_unavailable", "member_state_unavailable", "unknown"}
}

// PendingVIESCheckRepository stores pending VIES checks, one per customer.
// Every method is scoped to an organization.
type PendingVIESCheckRepository interface {
	// Get returns the customer's pending check; core.ErrNotFound when it
	// has none.
	Get(ctx context.Context, org core.OrganizationID, customer core.CustomerID) (*PendingVIESCheck, error)
	// Save inserts p, or replaces the customer's pending check with p
	// keeping its creation instant.
	Save(ctx context.Context, p *PendingVIESCheck) error
	// Delete removes the customer's pending check and reports whether it
	// had one.
	Delete(ctx context.Context, org core.OrganizationID, customer core.CustomerID) (bool, error)
}
