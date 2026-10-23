package catalog

import (
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// DefaultCommitmentInvoiceName is the invoice name of a minimum commitment
// without a display name.
const DefaultCommitmentInvoiceName = "Minimum commitment"

// Commitment is a minimum spend per billing period of a plan (domain §7.8).
// A plan has at most one commitment per type; the true-up rule is in domain
// §22.15.
type Commitment struct {
	// ID identifies the commitment.
	ID core.CommitmentID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// PlanID is the plan the commitment belongs to.
	PlanID core.PlanID
	// CommitmentType is the kind of commitment.
	CommitmentType CommitmentType
	// Amount is the committed spend per period, in minor units of the
	// plan's currency; > 0.
	Amount int64
	// InvoiceDisplayName replaces [DefaultCommitmentInvoiceName] on
	// invoices.
	InvoiceDisplayName *string
	// TaxIDs are the commitment's own taxes (domain §22.10, level 5).
	TaxIDs []core.TaxID
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// Validate checks the invariants of domain §7.8.
func (c *Commitment) Validate() error {
	var v core.Validator
	v.Check(!c.PlanID.IsZero(), "plan_id", core.CodeRequired)
	v.Enum("commitment_type", c.CommitmentType)
	v.Positive("amount", c.Amount)
	v.Check(len(c.TaxIDs) == len(part2Unique(c.TaxIDs)), "tax_ids", core.CodeDuplicated)
	return v.Err()
}

// InvoiceName returns the name printed on invoices: the display name, or
// [DefaultCommitmentInvoiceName] when it is unset or blank.
func (c *Commitment) InvoiceName() string {
	if c.InvoiceDisplayName != nil && *c.InvoiceDisplayName != "" {
		return *c.InvoiceDisplayName
	}
	return DefaultCommitmentInvoiceName
}

// part2Unique returns s without repeated elements, keeping the first.
func part2Unique[T comparable](s []T) []T {
	seen := make(map[T]bool, len(s))
	out := make([]T, 0, len(s))
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
