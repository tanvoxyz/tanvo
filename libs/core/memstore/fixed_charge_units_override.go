package memstore

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

func cloneUnitsOverride(o subscription.FixedChargeUnitsOverride) subscription.FixedChargeUnitsOverride {
	o.DeletedAt = clonePtr(o.DeletedAt)
	return o
}

// unitsOverrideRepo implements subscription.FixedChargeUnitsOverrideRepository.
type unitsOverrideRepo struct{ d *subscriptionData }

var _ subscription.FixedChargeUnitsOverrideRepository = unitsOverrideRepo{}

func (r unitsOverrideRepo) check(o *subscription.FixedChargeUnitsOverride) error {
	if o.DeletedAt != nil {
		return nil
	}
	if r.d.unitsOverrides.any(func(x *subscription.FixedChargeUnitsOverride) bool {
		return x.ID != o.ID && x.OrganizationID == o.OrganizationID && x.DeletedAt == nil &&
			x.SubscriptionID == o.SubscriptionID && x.FixedChargeID == o.FixedChargeID
	}) {
		return conflict("fixed_charge_units_override", "fixed_charge_id")
	}
	return nil
}

func (r unitsOverrideRepo) Create(_ context.Context, o *subscription.FixedChargeUnitsOverride) error {
	if r.d.unitsOverrides.has(o.ID) {
		return conflict("fixed_charge_units_override", "id")
	}
	if err := r.check(o); err != nil {
		return err
	}
	r.d.unitsOverrides.put(o.ID, *o)
	return nil
}

func (r unitsOverrideRepo) Update(_ context.Context, o *subscription.FixedChargeUnitsOverride) error {
	cur, ok := r.d.unitsOverrides.get(o.ID)
	if !ok || cur.OrganizationID != o.OrganizationID || cur.DeletedAt != nil {
		return notFound("fixed_charge_units_override", o.ID)
	}
	if err := r.check(o); err != nil {
		return err
	}
	r.d.unitsOverrides.put(o.ID, *o)
	return nil
}

func (r unitsOverrideRepo) Discard(_ context.Context, org core.OrganizationID, id core.FixedChargeUnitsOverrideID, at time.Time) error {
	cur, ok := r.d.unitsOverrides.get(id)
	if !ok || cur.OrganizationID != org || cur.DeletedAt != nil {
		return notFound("fixed_charge_units_override", id)
	}
	cur.DeletedAt = &at
	cur.UpdatedAt = at
	r.d.unitsOverrides.put(id, *cur)
	return nil
}

func (r unitsOverrideRepo) ListBySubscription(_ context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.FixedChargeUnitsOverride, error) {
	return r.d.unitsOverrides.find(func(x *subscription.FixedChargeUnitsOverride) bool {
		return x.OrganizationID == org && x.SubscriptionID == sub && x.DeletedAt == nil
	}), nil
}

func (r unitsOverrideRepo) ListByFixedCharge(_ context.Context, org core.OrganizationID, fixedCharge core.FixedChargeID) ([]*subscription.FixedChargeUnitsOverride, error) {
	return r.d.unitsOverrides.find(func(x *subscription.FixedChargeUnitsOverride) bool {
		return x.OrganizationID == org && x.FixedChargeID == fixedCharge && x.DeletedAt == nil
	}), nil
}
