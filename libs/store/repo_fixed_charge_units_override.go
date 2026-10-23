package store

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// unitsOverrideRepo implements subscription.FixedChargeUnitsOverrideRepository.
type unitsOverrideRepo struct{ t *txState }

var _ subscription.FixedChargeUnitsOverrideRepository = unitsOverrideRepo{}

func unitsOverrideParams(o *subscription.FixedChargeUnitsOverride) db.CreateFixedChargeUnitsOverrideParams {
	return db.CreateFixedChargeUnitsOverrideParams{
		OrganizationID: o.OrganizationID.UUID(),
		ID:             o.ID.UUID(),
		SubscriptionID: o.SubscriptionID.UUID(),
		FixedChargeID:  o.FixedChargeID.UUID(),
		Units:          o.Units,
		CreatedAt:      utc(o.CreatedAt),
		UpdatedAt:      utc(o.UpdatedAt),
		DeletedAt:      optUTC(o.DeletedAt),
	}
}

func unitsOverrideOf(r db.FixedChargeUnitsOverride) (*subscription.FixedChargeUnitsOverride, error) {
	return &subscription.FixedChargeUnitsOverride{
		ID:             idOf[core.FixedChargeUnitsOverrideID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		SubscriptionID: idOf[core.SubscriptionID](r.SubscriptionID),
		FixedChargeID:  idOf[core.FixedChargeID](r.FixedChargeID),
		Units:          r.Units,
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
		DeletedAt:      optUTC(r.DeletedAt),
	}, nil
}

func (r unitsOverrideRepo) Create(ctx context.Context, o *subscription.FixedChargeUnitsOverride) error {
	q, err := r.t.queries(ctx, o.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.CreateFixedChargeUnitsOverride(ctx, unitsOverrideParams(o))
		return err
	})
	return insertErr("fixed_charge_units_override", n, err)
}

func (r unitsOverrideRepo) Update(ctx context.Context, o *subscription.FixedChargeUnitsOverride) error {
	q, err := r.t.queries(ctx, o.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateFixedChargeUnitsOverride(ctx, db.UpdateFixedChargeUnitsOverrideParams{
			SubscriptionID: o.SubscriptionID.UUID(),
			FixedChargeID:  o.FixedChargeID.UUID(),
			Units:          o.Units,
			CreatedAt:      utc(o.CreatedAt),
			UpdatedAt:      utc(o.UpdatedAt),
			DeletedAt:      optUTC(o.DeletedAt),
			OrganizationID: o.OrganizationID.UUID(),
			ID:             o.ID.UUID(),
		})
		return err
	})
	return updateErr("fixed_charge_units_override", o.ID, n, err)
}

func (r unitsOverrideRepo) Discard(ctx context.Context, org core.OrganizationID, id core.FixedChargeUnitsOverrideID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	at = utc(at)
	n, err := q.DiscardFixedChargeUnitsOverride(ctx, db.DiscardFixedChargeUnitsOverrideParams{OrganizationID: org.UUID(), ID: id.UUID(), DeletedAt: &at})
	return updateErr("fixed_charge_units_override", id, n, err)
}

func (r unitsOverrideRepo) ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.FixedChargeUnitsOverride, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListFixedChargeUnitsOverridesBySubscription(ctx, db.ListFixedChargeUnitsOverridesBySubscriptionParams{
		OrganizationID: org.UUID(), SubscriptionID: sub.UUID(),
	})
	if err != nil {
		return nil, fmt.Errorf("list fixed charge units overrides of %s: %w", sub, mapErr(err))
	}
	return convAll(rows, unitsOverrideOf)
}

func (r unitsOverrideRepo) ListByFixedCharge(ctx context.Context, org core.OrganizationID, fixedCharge core.FixedChargeID) ([]*subscription.FixedChargeUnitsOverride, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListFixedChargeUnitsOverridesByFixedCharge(ctx, db.ListFixedChargeUnitsOverridesByFixedChargeParams{
		OrganizationID: org.UUID(), FixedChargeID: fixedCharge.UUID(),
	})
	if err != nil {
		return nil, fmt.Errorf("list units overrides of fixed charge %s: %w", fixedCharge, mapErr(err))
	}
	return convAll(rows, unitsOverrideOf)
}
