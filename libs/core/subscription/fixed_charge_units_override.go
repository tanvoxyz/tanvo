package subscription

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// FixedChargeUnitsOverride is the units of a fixed charge of the original
// plan overridden for one subscription, without overriding the plan
// (domain §7.5). At most one kept override exists per subscription and
// fixed charge; overrides are soft-deleted when they move onto an override
// plan.
type FixedChargeUnitsOverride struct {
	// ID identifies the override.
	ID core.FixedChargeUnitsOverrideID
	// OrganizationID is the owning organization.
	OrganizationID core.OrganizationID
	// SubscriptionID is the subscription whose units are overridden.
	SubscriptionID core.SubscriptionID
	// FixedChargeID is the overridden fixed charge, of the original plan.
	FixedChargeID core.FixedChargeID
	// Units are the subscription's units of the fixed charge (≥ 0).
	Units money.Decimal
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
	// DeletedAt is the instant the override was discarded (nil while kept).
	DeletedAt *time.Time
}

// Validate checks the override's invariants.
func (o *FixedChargeUnitsOverride) Validate() error {
	var v core.Validator
	v.Check(!o.SubscriptionID.IsZero(), "subscription_id", core.CodeRequired)
	v.Check(!o.FixedChargeID.IsZero(), "fixed_charge_id", core.CodeRequired)
	v.DecimalNonNegative("units", o.Units)
	return v.Err()
}

// FixedChargeUnitsOverrideRepository stores fixed charge units overrides.
// Every method is scoped to an organization, and every read sees kept
// overrides only.
type FixedChargeUnitsOverrideRepository interface {
	// Create inserts o; a kept override of the same subscription and fixed
	// charge fails with core.ErrConflict.
	Create(ctx context.Context, o *FixedChargeUnitsOverride) error
	// Update replaces the stored kept override o.ID with o.
	Update(ctx context.Context, o *FixedChargeUnitsOverride) error
	// Discard soft-deletes the kept override id at at; a missing or
	// discarded one fails with core.ErrNotFound.
	Discard(ctx context.Context, org core.OrganizationID, id core.FixedChargeUnitsOverrideID, at time.Time) error
	// ListBySubscription returns the subscription's kept overrides in
	// creation order.
	ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*FixedChargeUnitsOverride, error)
	// ListByFixedCharge returns the kept overrides of the fixed charge in
	// creation order.
	ListByFixedCharge(ctx context.Context, org core.OrganizationID, fixedCharge core.FixedChargeID) ([]*FixedChargeUnitsOverride, error)
}
