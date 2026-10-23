package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func testFixedChargeUnitsOverride(t *testing.T, h Harness) {
	e := newEnv(t, h)
	p, other := subscriptionFixtures(e)
	org := p.org
	e.tx(func(ctx context.Context, r port.Repos) error {
		addOn := &catalog.AddOn{
			ID: core.NewID[core.AddOnID](e.ids), OrganizationID: org, Code: "seat", Name: "Seat",
			Amount: 500, Currency: money.MustCurrency("EUR"), CreatedAt: now, UpdatedAt: now,
		}
		noErr(t, "AddOns.Create()", r.AddOns.Create(ctx, addOn))
		fixed := func(code string) *catalog.FixedCharge {
			props, err := catalog.NewProperties([]byte(`{"amount":"5"}`))
			noErr(t, "NewProperties()", err)
			f := &catalog.FixedCharge{
				ID: core.NewID[core.FixedChargeID](e.ids), OrganizationID: org, PlanID: p.plan.ID, Code: code,
				AddOnID: addOn.ID, ChargeModel: catalog.FixedChargeModelStandard, Properties: props,
				Units: money.MustParse("1"), CreatedAt: now, UpdatedAt: now,
			}
			noErr(t, "FixedCharges.Create()", r.FixedCharges.Create(ctx, f))
			return f
		}
		fc1, fc2 := fixed("seats"), fixed("rooms")
		s1, s2 := p.newSub(e, "uo_1"), p.newSub(e, "uo_2")
		noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, s1))
		noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, s2))

		mk := func(s *subscription.Subscription, fc *catalog.FixedCharge, units string) *subscription.FixedChargeUnitsOverride {
			return &subscription.FixedChargeUnitsOverride{
				ID: core.NewID[core.FixedChargeUnitsOverrideID](e.ids), OrganizationID: org, SubscriptionID: s.ID,
				FixedChargeID: fc.ID, Units: money.MustParse(units), CreatedAt: now, UpdatedAt: now,
			}
		}
		a, b, c := mk(s1, fc1, "3"), mk(s1, fc2, "0"), mk(s2, fc1, "7.5")
		for _, x := range []*subscription.FixedChargeUnitsOverride{a, b, c} {
			noErr(t, "Create()", r.FixedChargeUnitsOverrides.Create(ctx, x))
		}
		wantErr(t, "Create() of a second kept override", r.FixedChargeUnitsOverrides.Create(ctx, mk(s1, fc1, "4")), core.ErrConflict)

		got, err := r.FixedChargeUnitsOverrides.ListBySubscription(ctx, org, s1.ID)
		noErr(t, "ListBySubscription()", err)
		equal(t, "ListBySubscription()", got, []*subscription.FixedChargeUnitsOverride{a, b})
		got, err = r.FixedChargeUnitsOverrides.ListByFixedCharge(ctx, org, fc1.ID)
		noErr(t, "ListByFixedCharge()", err)
		equal(t, "ListByFixedCharge()", got, []*subscription.FixedChargeUnitsOverride{a, c})

		a.Units = money.MustParse("9")
		a.UpdatedAt = at(time.Hour)
		noErr(t, "Update()", r.FixedChargeUnitsOverrides.Update(ctx, a))
		noErr(t, "Discard()", r.FixedChargeUnitsOverrides.Discard(ctx, org, b.ID, at(2*time.Hour)))
		wantErr(t, "Discard() twice", r.FixedChargeUnitsOverrides.Discard(ctx, org, b.ID, at(2*time.Hour)), core.ErrNotFound)
		wantErr(t, "Update() of a discarded override", r.FixedChargeUnitsOverrides.Update(ctx, b), core.ErrNotFound)
		got, err = r.FixedChargeUnitsOverrides.ListBySubscription(ctx, org, s1.ID)
		noErr(t, "ListBySubscription() after discard", err)
		equal(t, "ListBySubscription() after discard", got, []*subscription.FixedChargeUnitsOverride{a})

		// A discarded override frees its subscription and fixed charge.
		d := mk(s1, fc2, "2")
		noErr(t, "Create() after discard", r.FixedChargeUnitsOverrides.Create(ctx, d))

		got, err = r.FixedChargeUnitsOverrides.ListBySubscription(ctx, other.org, s1.ID)
		noErr(t, "ListBySubscription() from another organization", err)
		equal(t, "ListBySubscription() from another organization", len(got), 0)
		wantErr(t, "Discard() from another organization", r.FixedChargeUnitsOverrides.Discard(ctx, other.org, a.ID, now), core.ErrNotFound)
		return nil
	})
}
