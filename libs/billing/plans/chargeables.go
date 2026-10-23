package plans

import (
	"context"
	"errors"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// validateChargeables checks, before a plan is written, that every
// billable metric the charges reference and every add-on the fixed
// charges reference is a kept object of the organization: a missing
// metric is core.NotFound("billable_metrics"), a missing add-on
// core.NotFound("add_ons"). Charges and fixed charges without a
// reference are skipped.
//
// source: app/services/plans/chargeables_validation_service.rb:14-68
func validateChargeables(ctx context.Context, r port.Repos, org core.OrganizationID, charges []ChargeInput, fixed []FixedChargeInput) error {
	metrics := refs.Metrics(r, org)
	for _, ref := range distinctRefs(charges, func(c ChargeInput) refs.Ref { return c.BillableMetric }) {
		if _, err := metrics.One(ctx, ref); err != nil {
			return chargeableError(err, "billable_metrics")
		}
	}
	addOns := refs.AddOns(r, org)
	for _, ref := range distinctRefs(fixed, func(f FixedChargeInput) refs.Ref { return f.AddOn }) {
		if _, err := addOns.One(ctx, ref); err != nil {
			return chargeableError(err, "add_ons")
		}
	}
	return nil
}

// distinctRefs returns the distinct non-zero references of items.
func distinctRefs[T any](items []T, ref func(T) refs.Ref) []refs.Ref {
	seen := map[refs.Ref]bool{}
	var out []refs.Ref
	for _, it := range items {
		if r := ref(it); !r.IsZero() && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// chargeableError turns a not-found error into core.NotFound(resource)
// and returns any other error as is.
func chargeableError(err error, resource string) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(resource)
	}
	return err
}
