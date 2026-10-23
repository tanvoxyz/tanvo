package plans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// chargeJobs returns the handlers of the charge cascade jobs.
func (s *Service) chargeJobs() map[string]func(context.Context, port.Job) error {
	return map[string]func(context.Context, port.Job) error{
		kernel.JobCreateChargeChildren:  s.handleCreateChargeChildren,
		kernel.JobUpdateChargeChildren:  s.handleUpdateChargeChildren,
		kernel.JobDestroyChargeChildren: s.handleDestroyChargeChildren,
		kernel.JobCascadeChargeFilter:   s.handleCascadeChargeFilter,
		kernel.JobDestroyMetricFilters:  s.handleDestroyMetricFilters,
	}
}

// decodeArgs decodes the args of the job j into a.
func decodeArgs(j port.Job, a any) error {
	if err := json.Unmarshal(j.Args, a); err != nil {
		return fmt.Errorf("decode %s args: %w", j.Kind, err)
	}
	return nil
}

// handleCreateChargeChildren runs a [kernel.JobCreateChargeChildren].
// Without plans, it lists the override plans with active or pending
// subscriptions and enqueues one job per batch of them; with plans, the
// charge is created, from the payload of its parent, on each override plan
// of the batch that has no copy of it yet, as the job may run more than
// once. A discarded parent is left alone.
//
// source: app/jobs/charges/create_children_job.rb:7-19, app/jobs/charges/create_children_batch_job.rb:7-9, app/services/charges/create_children_service.rb:15-35
func (s *Service) handleCreateChargeChildren(ctx context.Context, j port.Job) error {
	var a kernel.CreateChargeChildrenArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	org := j.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := keptCharge(ctx, r, org, a.ChargeID)
		if c == nil || err != nil {
			return err
		}
		if a.PlanIDs == nil {
			plans, err := s.livePlanChildren(ctx, r, org, c.PlanID)
			if err != nil {
				return err
			}
			for batch := range slices.Chunk(plans, kernel.ChargeChildrenBatch) {
				b := a
				for _, p := range batch {
					b.PlanIDs = append(b.PlanIDs, p.ID)
				}
				if err := kernel.Enqueue(ctx, r, org, j.Kind, b, "", s.d.Now()); err != nil {
					return err
				}
			}
			return nil
		}
		var in ChargeInput
		if err := json.Unmarshal(a.Payload, &in); err != nil {
			return fmt.Errorf("decode %s payload: %w", j.Kind, err)
		}
		children, err := r.Plans.ListChildren(ctx, org, c.PlanID)
		if err != nil {
			return fmt.Errorf("load override plans of %s: %w", c.PlanID, err)
		}
		for _, p := range children {
			if !slices.Contains(a.PlanIDs, p.ID) {
				continue
			}
			charges, err := r.Charges.ListByPlan(ctx, org, p.ID)
			if err != nil {
				return fmt.Errorf("load charges of plan %s: %w", p.ID, err)
			}
			if slices.ContainsFunc(charges, func(x *catalog.Charge) bool { return x.ParentID == c.ID }) {
				continue
			}
			child := in
			child.ID = core.ChargeID{}
			if child.Code.Value == "" {
				child.Code = core.Some(c.Code)
			}
			child.parent = c.ID
			if _, err := s.createCharge(ctx, r, p, child, false); err != nil {
				return fmt.Errorf("create charge %s on plan %s: %w", c.ID, p.ID, err)
			}
		}
		return nil
	})
}

// handleUpdateChargeChildren runs a [kernel.JobUpdateChargeChildren].
// Without children, it lists the overrides of the charge on plans with
// active or pending subscriptions and enqueues one job per batch of them;
// with children, each override of the batch takes the new pricing in
// cascade mode. A discarded parent is left alone.
//
// source: app/jobs/charges/update_children_job.rb:7-21, app/jobs/charges/update_children_batch_job.rb:7-21, app/services/charges/update_children_service.rb:20-45
func (s *Service) handleUpdateChargeChildren(ctx context.Context, j port.Job) error {
	var a kernel.UpdateChargeChildrenArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	org := j.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		parent, err := keptCharge(ctx, r, org, a.ChargeID)
		if parent == nil || err != nil {
			return err
		}
		if a.ChildIDs == nil {
			children, err := s.liveChargeChildren(ctx, r, org, a.ChargeID)
			if err != nil {
				return err
			}
			for batch := range slices.Chunk(children, kernel.ChargeChildrenBatch) {
				b := a
				for _, c := range batch {
					b.ChildIDs = append(b.ChildIDs, c.ID)
				}
				if err := kernel.Enqueue(ctx, r, org, j.Kind, b, "", s.d.Now()); err != nil {
					return err
				}
			}
			return nil
		}
		old := &catalog.Charge{ChargeModel: a.OldChargeModel, Properties: a.OldProperties}
		in := ChargeInput{ChargeModel: core.Some(a.ChargeModel)}
		if a.Code != "" {
			in.Code = core.Some(a.Code)
		}
		if a.Properties != nil {
			in.Properties = core.Some(*a.Properties)
		}
		if a.ConversionRate != nil {
			in.AppliedPricingUnit = core.Some(&PricingUnitInput{ConversionRate: *a.ConversionRate})
		}
		children, err := r.Charges.ListChildren(ctx, org, a.ChargeID)
		if err != nil {
			return fmt.Errorf("load overrides of charge %s: %w", a.ChargeID, err)
		}
		for _, child := range children {
			if !slices.Contains(a.ChildIDs, child.ID) {
				continue
			}
			u := chargeUpdate{
				cascade:         true,
				equalProperties: old.EqualProperties(child),
				equalRate:       chargeEqualRate(a.OldConversionRate, child),
			}
			if _, err := s.updateCharge(ctx, r, child, in, u); err != nil {
				return fmt.Errorf("update override charge %s: %w", child.ID, err)
			}
		}
		return nil
	})
}

// keptCharge returns the charge id, or nil when it is missing or discarded
// (Charge default_scope kept).
func keptCharge(ctx context.Context, r port.Repos, org core.OrganizationID, id core.ChargeID) (*catalog.Charge, error) {
	c, err := r.Charges.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load charge %s: %w", id, err)
	}
	if c.DeletedAt != nil {
		return nil, nil
	}
	return c, nil
}

// chargeEqualRate reports whether the override child has the parent's
// conversion rate old, nil when the parent had no pricing unit
// (Charge#equal_applied_pricing_unit_rate?).
//
// source: app/models/charge.rb:93-97
func chargeEqualRate(old *money.Decimal, child *catalog.Charge) bool {
	return old != nil && child.AppliedPricingUnit != nil && old.Equal(child.AppliedPricingUnit.ConversionRate)
}

// handleDestroyChargeChildren runs a [kernel.JobDestroyChargeChildren]:
// once the charge is discarded, its overrides on plans with active or
// pending subscriptions are discarded too.
//
// source: app/jobs/charges/destroy_children_job.rb:7-10, app/services/charges/destroy_children_service.rb:12-28
func (s *Service) handleDestroyChargeChildren(ctx context.Context, j port.Job) error {
	var a kernel.ChargeArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	org := j.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Charges.Get(ctx, org, a.ChargeID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load charge %s: %w", a.ChargeID, err)
		}
		if c.DeletedAt == nil {
			return nil
		}
		children, err := s.liveChargeChildren(ctx, r, org, c.ID)
		if err != nil {
			return err
		}
		for _, child := range children {
			if _, err := s.discardCharge(ctx, r, child, false); err != nil {
				return err
			}
		}
		return nil
	})
}

// handleCascadeChargeFilter runs a [kernel.JobCascadeChargeFilter] on the
// overrides of the charge on plans with active or pending subscriptions.
// The copy of the filter is the override's filter with its code; a create
// also takes a filter with the same values, which then adopts the code.
// An update keeps the pricing of a copy priced on its own, cascading only
// its grouping keys.
//
// source: app/jobs/charge_filters/cascade_job.rb:7-21, app/services/charge_filters/cascade_service.rb:30-226
func (s *Service) handleCascadeChargeFilter(ctx context.Context, j port.Job) error {
	var a kernel.CascadeChargeFilterArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	org := j.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		parent, err := r.Charges.Get(ctx, org, a.ChargeID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load charge %s: %w", a.ChargeID, err)
		}
		children, err := s.liveChargeChildren(ctx, r, org, parent.ID)
		if err != nil || len(children) == 0 {
			return err
		}
		if a.Action == kernel.ChargeFilterUpdated && a.Code == "" {
			return fmt.Errorf("charge %s filter %v has no code", parent.ID, a.Values)
		}
		m, err := r.Metrics.Get(ctx, org, parent.BillableMetricID)
		if err != nil {
			return fmt.Errorf("load billable metric %s: %w", parent.BillableMetricID, err)
		}
		for _, child := range children {
			if err := s.cascadeChargeFilter(ctx, r, child, m, a); err != nil {
				return fmt.Errorf("cascade filter to override charge %s: %w", child.ID, err)
			}
		}
		return nil
	})
}

// cascadeChargeFilter applies the filter change a to the override child
// of a charge on the metric m.
func (s *Service) cascadeChargeFilter(ctx context.Context, r port.Repos, child *catalog.Charge, m *catalog.BillableMetric, a kernel.CascadeChargeFilterArgs) error {
	child = child.Clone()
	i := -1
	if a.Code != "" {
		i = slices.IndexFunc(child.Filters, func(f catalog.ChargeFilter) bool { return f.Code == a.Code })
	}
	if i < 0 && a.Action == kernel.ChargeFilterCreated {
		var matched []int
		for k, f := range child.Filters {
			if chargeFilterSameValues(f.Values, a.Values) {
				matched = append(matched, k)
			}
		}
		if len(matched) > 1 {
			return fmt.Errorf("charge %s holds %v more than once", child.ID, a.Values)
		}
		if len(matched) == 1 {
			i = matched[0]
		}
	}
	var newProps catalog.Properties
	if a.NewProperties != nil {
		newProps = *a.NewProperties
	}
	switch a.Action {
	case kernel.ChargeFilterUpdated:
		if i < 0 {
			return nil
		}
		f := &child.Filters[i]
		if a.OldProperties != nil && !chargePropertiesSameValues(*a.OldProperties, f.Properties) {
			props, err := chargeCascadeGroupKeys(f.Properties, newProps, false)
			if err != nil {
				return err
			}
			if props.Equal(f.Properties) {
				return nil
			}
			f.Properties = props
		} else {
			props, err := chargeFilterProperties(child.ChargeModel, newProps, false)
			if err != nil {
				return err
			}
			f.Properties, f.InvoiceDisplayName = props, a.InvoiceDisplayName
		}
	case kernel.ChargeFilterCreated:
		if i >= 0 {
			if f := &child.Filters[i]; a.Code == "" || f.Code != "" {
				return nil
			}
			child.Filters[i].Code = a.Code
			break
		}
		values := chargeFilterResolvedValues(a.Values, m)
		if len(values) == 0 {
			return nil
		}
		props, err := chargeFilterProperties(child.ChargeModel, newProps, false)
		if err != nil {
			return err
		}
		child.Filters = append(child.Filters, catalog.ChargeFilter{
			ID:                 core.NewID[core.ChargeFilterID](s.d.IDs),
			ChargeID:           child.ID,
			Values:             values,
			Properties:         props,
			InvoiceDisplayName: a.InvoiceDisplayName,
			Code:               a.Code,
			CreatedAt:          s.d.Now(),
		})
	case kernel.ChargeFilterDestroyed:
		if i < 0 {
			return nil
		}
		child.Filters = slices.Delete(child.Filters, i, i+1)
	default:
		return fmt.Errorf("unknown charge filter cascade action %q", a.Action)
	}
	return s.saveChargeFilters(ctx, r, child)
}

// chargeFilterSameValues reports whether a and b hold the same keys with
// the same values in the same order (Ruby hash equality of
// ChargeFilter#to_h).
func chargeFilterSameValues(a, b map[string][]string) bool {
	return maps.EqualFunc(a, b, slices.Equal)
}

// chargeFilterResolvedValues returns values restricted to the current
// filters of the metric m: unknown keys and values are dropped, and so are
// keys left with no value.
//
// source: app/services/charge_filters/cascade_service.rb:177-187
func chargeFilterResolvedValues(values map[string][]string, m *catalog.BillableMetric) map[string][]string {
	out := map[string][]string{}
	for k, vs := range values {
		mf, ok := m.Filter(k)
		if !ok {
			continue
		}
		valid := slices.DeleteFunc(slices.Clone(vs), func(v string) bool { return !slices.Contains(mf.Values, v) })
		if len(valid) > 0 {
			out[k] = valid
		}
	}
	return out
}

// chargeNumeric matches a property written as a decimal number.
var chargeNumeric = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// chargePropertiesSameValues reports whether a and b hold the same
// properties, comparing top-level numbers by value whether written as
// numbers or strings (CascadeService#normalize_properties).
//
// source: app/services/charge_filters/cascade_service.rb:215-221
func chargePropertiesSameValues(a, b catalog.Properties) bool {
	ma, err := chargePropertiesMap(a)
	if err != nil {
		return false
	}
	mb, err := chargePropertiesMap(b)
	if err != nil {
		return false
	}
	return maps.EqualFunc(ma, mb, func(x, y any) bool {
		dx, okx := chargeNumber(x)
		dy, oky := chargeNumber(y)
		if okx || oky {
			return okx && oky && dx.Equal(dy)
		}
		jx, _ := json.Marshal(x)
		jy, _ := json.Marshal(y)
		return catalog.MustProperties(`{"v":` + string(jx) + `}`).Equal(catalog.MustProperties(`{"v":` + string(jy) + `}`))
	})
}

// chargeNumber returns v as a decimal when it is a JSON number or a
// numeric string.
func chargeNumber(v any) (money.Decimal, bool) {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = x.String()
	case string:
		if !chargeNumeric.MatchString(x) {
			return money.Decimal{}, false
		}
		s = x
	default:
		return money.Decimal{}, false
	}
	d, err := money.Parse(s)
	return d, err == nil
}

// livePlanChildren returns the override plans of the plan id that have
// active or pending subscriptions.
func (s *Service) livePlanChildren(ctx context.Context, r port.Repos, org core.OrganizationID, id core.PlanID) ([]*catalog.Plan, error) {
	children, err := r.Plans.ListChildren(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load override plans of %s: %w", id, err)
	}
	out := children[:0]
	for _, p := range children {
		live, err := planLive(ctx, r, org, p.ID)
		if err != nil {
			return nil, err
		}
		if live {
			out = append(out, p)
		}
	}
	return out, nil
}

// liveChargeChildren returns the overrides of the charge id on plans that
// have active or pending subscriptions.
func (s *Service) liveChargeChildren(ctx context.Context, r port.Repos, org core.OrganizationID, id core.ChargeID) ([]*catalog.Charge, error) {
	children, err := r.Charges.ListChildren(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load overrides of charge %s: %w", id, err)
	}
	live := map[core.PlanID]bool{}
	out := children[:0]
	for _, c := range children {
		ok, seen := live[c.PlanID]
		if !seen {
			if ok, err = planLive(ctx, r, org, c.PlanID); err != nil {
				return nil, err
			}
			live[c.PlanID] = ok
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// planLive reports whether the plan id has active or pending
// subscriptions.
func planLive(ctx context.Context, r port.Repos, org core.OrganizationID, id core.PlanID) (bool, error) {
	subs, err := r.Subscriptions.ListActiveOrPendingByPlan(ctx, org, id)
	if err != nil {
		return false, fmt.Errorf("load subscriptions of plan %s: %w", id, err)
	}
	return len(subs) > 0, nil
}
