package plans

import (
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// ListSubscriptionCharges lists the charges of the subscription's plan,
// most recent first. The subscription is found by ID, or by external ID
// among the active ones.
//
// source: app/controllers/api/v1/subscriptions/charges_controller.rb:10-26
func (s *Service) ListSubscriptionCharges(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, page core.PageRequest) (core.Page[*catalog.Charge], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.Charge], error) {
		sb, err := refs.Subscription(ctx, r, org, sub)
		if err != nil {
			return core.Page[*catalog.Charge]{}, err
		}
		charges, err := r.Charges.ListByPlan(ctx, org, sb.PlanID)
		if err != nil {
			return core.Page[*catalog.Charge]{}, fmt.Errorf("load charges of plan %s: %w", sb.PlanID, err)
		}
		slices.Reverse(charges)
		return chargePage(charges, func(c *catalog.Charge) string { return c.ID.String() }, page)
	})
}

// GetSubscriptionCharge returns the charge of the subscription's plan, by
// ID or code.
//
// source: app/controllers/api/v1/subscriptions/charges_controller.rb:28-36, 91-94
func (s *Service) GetSubscriptionCharge(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, charge refs.Ref) (*catalog.Charge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Charge, error) {
		_, c, err := subscriptionCharge(ctx, r, org, sub, charge)
		return c, err
	})
}

// UpdateSubscriptionCharge sets the subscription's own pricing of the
// charge (lago Subscriptions::UpdateOrOverrideChargeService): the
// subscription moves to an override plan if it is not on one, and the
// override of the charge there takes the properties, minimum, display
// name, filters, conversion rate and taxes set in in; the other fields of
// in are ignored.
//
// source: app/services/subscriptions/update_or_override_charge_service.rb:15-80
func (s *Service) UpdateSubscriptionCharge(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, charge refs.Ref, in ChargeInput) (*catalog.Charge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Charge, error) {
		sb, c, err := subscriptionCharge(ctx, r, org, sub, charge)
		if err != nil {
			return nil, err
		}
		target, err := s.ensurePlanOverride(ctx, r, sb)
		if err != nil {
			return nil, err
		}
		if c.PlanID == target.ID {
			return s.updateChargeOverride(ctx, r, target, c, in)
		}
		parent, err := chargeParent(ctx, r, c)
		if err != nil {
			return nil, err
		}
		existing, err := chargeOverrideOn(ctx, r, target, parent)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return s.updateChargeOverride(ctx, r, target, existing, in)
		}
		return s.overrideCharge(ctx, r, parent, target, &in)
	})
}

// updateChargeOverride changes the override charge c of the plan p in
// place with the subscription-level fields of in.
//
// source: app/services/subscriptions/update_or_override_charge_service.rb:54-78
func (s *Service) updateChargeOverride(ctx context.Context, r port.Repos, p *catalog.Plan, c *catalog.Charge, in ChargeInput) (*catalog.Charge, error) {
	c = c.Clone()
	in.Properties.Apply(&c.Properties)
	if in.MinAmount.Set {
		var err error
		if c.MinAmount, err = chargeMinAmount(p, in.MinAmount.Value); err != nil {
			return nil, err
		}
	}
	in.InvoiceDisplayName.Apply(&c.InvoiceDisplayName)
	if in.Filters.Set {
		if err := s.applyChargeFilters(c, in.Filters.Value); err != nil {
			return nil, err
		}
	}
	if a := in.AppliedPricingUnit; a.Set && a.Value != nil && c.AppliedPricingUnit != nil {
		c.AppliedPricingUnit.ConversionRate = a.Value.ConversionRate
	}
	if in.Taxes.Set {
		var err error
		if c.TaxIDs, err = refs.Taxes(r, c.OrganizationID).IDs(ctx, in.Taxes.Value); err != nil {
			return nil, err
		}
	}
	if err := s.saveChargeFilters(ctx, r, c); err != nil {
		return nil, err
	}
	return c, nil
}

// ListSubscriptionChargeFilters lists the filters of the charge of the
// subscription's plan in their order.
//
// source: app/controllers/api/v1/subscriptions/charges/filters_controller.rb:13-27
func (s *Service) ListSubscriptionChargeFilters(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, charge refs.Ref, page core.PageRequest) (core.Page[*catalog.ChargeFilter], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.ChargeFilter], error) {
		_, c, err := subscriptionCharge(ctx, r, org, sub, charge)
		if err != nil {
			return core.Page[*catalog.ChargeFilter]{}, err
		}
		return chargeFilterPage(c, page)
	})
}

// GetSubscriptionChargeFilter returns the filter id of the charge of the
// subscription's plan; another charge's filter fails with
// charge_filter_not_found.
//
// source: app/controllers/api/v1/subscriptions/charges/filters_controller.rb:29-31, 112-115
func (s *Service) GetSubscriptionChargeFilter(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, charge refs.Ref, id core.ChargeFilterID) (*catalog.ChargeFilter, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ChargeFilter, error) {
		_, c, err := subscriptionCharge(ctx, r, org, sub, charge)
		if err != nil {
			return nil, err
		}
		return chargeFilterOf(c, id)
	})
}

// CreateSubscriptionChargeFilter adds the filter in to the subscription's
// override of the charge, moving the subscription to an override plan if
// needed; values another filter of the override has fail with
// value_already_exists on values.
//
// source: app/services/subscriptions/charge_filters/create_service.rb:17-44
func (s *Service) CreateSubscriptionChargeFilter(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, charge refs.Ref, in ChargeFilterInput) (*catalog.ChargeFilter, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ChargeFilter, error) {
		if len(in.Values) == 0 {
			var v core.Validator
			v.Add("values", core.CodeRequired)
			return nil, v.Err()
		}
		target, err := s.subscriptionChargeOverride(ctx, r, org, sub, charge)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(target.Filters, func(f catalog.ChargeFilter) bool { return chargeFilterSameValues(f.Values, in.Values) }) {
			var v core.Validator
			v.Add("values", codeValueAlreadyExists)
			return nil, v.Err()
		}
		return s.createChargeFilter(ctx, r, target, in, false)
	})
}

// UpdateSubscriptionChargeFilter sets the subscription's own pricing of
// the filter id of the charge: on the subscription's override of the
// charge, the filter with the same values takes the set fields of in, or
// is created with them and the filter's other values.
//
// source: app/services/subscriptions/charge_filters/update_or_override_service.rb:18-81
func (s *Service) UpdateSubscriptionChargeFilter(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, charge refs.Ref, id core.ChargeFilterID, in ChargeFilterInput) (*catalog.ChargeFilter, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ChargeFilter, error) {
		_, c, err := subscriptionCharge(ctx, r, org, sub, charge)
		if err != nil {
			return nil, err
		}
		f, ok := c.Filter(id)
		if !ok {
			return nil, core.NotFound("charge_filter")
		}
		src := f.Clone()
		target, err := s.subscriptionChargeOverride(ctx, r, org, sub, charge)
		if err != nil {
			return nil, err
		}
		if i := slices.IndexFunc(target.Filters, func(x catalog.ChargeFilter) bool { return chargeFilterSameValues(x.Values, src.Values) }); i >= 0 {
			return s.updateChargeFilter(ctx, r, target, target.Filters[i].ID, in, false)
		}
		create := ChargeFilterInput{
			Values:             src.Values,
			Properties:         core.Some(src.Properties),
			InvoiceDisplayName: core.Some(src.InvoiceDisplayName),
		}
		if in.Properties.Set {
			create.Properties = in.Properties
		}
		if in.InvoiceDisplayName.Set {
			create.InvoiceDisplayName = in.InvoiceDisplayName
		}
		return s.createChargeFilter(ctx, r, target, create, false)
	})
}

// DeleteSubscriptionChargeFilter discards, on the subscription's override
// of the charge, the filter with the values of the filter id of the
// charge. The discarded filter is returned.
//
// source: app/services/subscriptions/charge_filters/destroy_service.rb:17-38
func (s *Service) DeleteSubscriptionChargeFilter(ctx context.Context, org core.OrganizationID, sub refs.SubscriptionRef, charge refs.Ref, id core.ChargeFilterID) (*catalog.ChargeFilter, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ChargeFilter, error) {
		_, c, err := subscriptionCharge(ctx, r, org, sub, charge)
		if err != nil {
			return nil, err
		}
		f, ok := c.Filter(id)
		if !ok {
			return nil, core.NotFound("charge_filter")
		}
		values := f.Values
		target, err := s.subscriptionChargeOverride(ctx, r, org, sub, charge)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(target.Filters, func(x catalog.ChargeFilter) bool { return chargeFilterSameValues(x.Values, values) })
		if i < 0 {
			return nil, core.NotFound("charge_filter")
		}
		return s.destroyChargeFilter(ctx, r, target, target.Filters[i].ID, false)
	})
}

// subscriptionCharge resolves the subscription and the kept charge of its
// plan, by ID or code.
//
// source: app/controllers/api/v1/subscriptions/base_controller.rb:11-22, app/controllers/api/v1/subscriptions/charges_controller.rb:91-94
func subscriptionCharge(ctx context.Context, r port.Repos, org core.OrganizationID, sub refs.SubscriptionRef, charge refs.Ref) (*subscription.Subscription, *catalog.Charge, error) {
	sb, err := refs.Subscription(ctx, r, org, sub)
	if err != nil {
		return nil, nil, err
	}
	c, err := chargeOnPlan(ctx, r, org, sb.PlanID, charge, false)
	if err != nil {
		return nil, nil, err
	}
	return sb, c, nil
}

// subscriptionChargeOverride returns the subscription's override of the
// charge, moving the subscription to an override plan and copying the
// charge there when needed (Subscriptions::Concerns::ChargeOverrideConcern
// #find_or_create_charge_override).
//
// source: app/services/subscriptions/concerns/charge_override_concern.rb:9-30
func (s *Service) subscriptionChargeOverride(ctx context.Context, r port.Repos, org core.OrganizationID, sub refs.SubscriptionRef, charge refs.Ref) (*catalog.Charge, error) {
	sb, c, err := subscriptionCharge(ctx, r, org, sub, charge)
	if err != nil {
		return nil, err
	}
	target, err := s.ensurePlanOverride(ctx, r, sb)
	if err != nil {
		return nil, err
	}
	parent, err := chargeParent(ctx, r, c)
	if err != nil {
		return nil, err
	}
	existing, err := chargeOverrideOn(ctx, r, target, parent)
	if err != nil || existing != nil {
		return existing, err
	}
	return s.overrideCharge(ctx, r, parent, target, nil)
}

// chargeParent returns the parent of the override charge c, or c itself.
func chargeParent(ctx context.Context, r port.Repos, c *catalog.Charge) (*catalog.Charge, error) {
	if !c.IsChild() {
		return c, nil
	}
	p, err := r.Charges.Get(ctx, c.OrganizationID, c.ParentID)
	if err != nil {
		return nil, fmt.Errorf("load parent charge %s: %w", c.ParentID, err)
	}
	return p, nil
}

// chargeOverrideOn returns the kept override of parent on the plan p, or
// nil.
func chargeOverrideOn(ctx context.Context, r port.Repos, p *catalog.Plan, parent *catalog.Charge) (*catalog.Charge, error) {
	charges, err := r.Charges.ListByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return nil, fmt.Errorf("load charges of plan %s: %w", p.ID, err)
	}
	if i := slices.IndexFunc(charges, func(c *catalog.Charge) bool { return c.ParentID == parent.ID }); i >= 0 {
		return charges[i], nil
	}
	return nil, nil
}
