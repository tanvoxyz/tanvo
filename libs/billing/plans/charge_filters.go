package plans

import (
	"context"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// ChargeFilterInput are the values of a charge filter to create or
// update.
type ChargeFilterInput struct {
	// Values map metric filter keys to the values the filter matches; set
	// on create, ignored on update.
	Values map[string][]string
	// Properties are the charge model's properties for the filter.
	Properties core.Optional[catalog.Properties]
	// InvoiceDisplayName replaces the generated display name on invoices.
	InvoiceDisplayName core.Optional[*string]
}

// applyChargeFilters replaces the filters of the charge c by in
// (ChargeFilters::CreateOrUpdateBatchService): a kept filter with the same
// values is updated in place and keeps its code, a new one gets the code
// of its values, free on the charge, and the others are removed. The
// filters take the order of in.
//
// source: app/services/charge_filters/create_or_update_batch_service.rb:17-187
func (s *Service) applyChargeFilters(c *catalog.Charge, in []ChargeFilterInput) error {
	if len(in) == 0 {
		c.Filters = nil
		return nil
	}
	if slices.ContainsFunc(in, func(f ChargeFilterInput) bool { return len(f.Values) == 0 }) {
		var v core.Validator
		v.Add("values", core.CodeRequired)
		return v.Err()
	}
	taken := map[string]bool{}
	for _, f := range c.Filters {
		taken[f.Code] = true
	}
	now := s.d.Now()
	out := make([]catalog.ChargeFilter, 0, len(in))
	for _, fi := range in {
		props, err := chargeFilterProperties(c.ChargeModel, fi.Properties.Value, true)
		if err != nil {
			return err
		}
		if i := slices.IndexFunc(c.Filters, func(f catalog.ChargeFilter) bool {
			return catalog.ChargeFilterValuesEqual(f.Values, fi.Values)
		}); i >= 0 {
			f := c.Filters[i].Clone()
			f.Values = fi.Values
			f.Properties = props
			f.InvoiceDisplayName = fi.InvoiceDisplayName.Value
			out = append(out, f)
			continue
		}
		code := chargeFilterFreeCode(fi.Values, taken)
		out = append(out, catalog.ChargeFilter{
			ID:                 core.NewID[core.ChargeFilterID](s.d.IDs),
			ChargeID:           c.ID,
			Values:             fi.Values,
			Properties:         props,
			InvoiceDisplayName: fi.InvoiceDisplayName.Value,
			Code:               code,
			CreatedAt:          now,
		})
	}
	c.Filters = out
	return nil
}

// chargeFilterFreeCode returns the code of a new filter with values, free
// among taken, and takes it (ChargeFilter.next_free_code).
//
// source: app/models/charge_filter.rb:28-48
func chargeFilterFreeCode(values map[string][]string, taken map[string]bool) string {
	code := catalog.NextFreeCode(catalog.ChargeFilterCode(values), func(s string) bool { return taken[s] })
	taken[code] = true
	return code
}

// createChargeFilter adds a filter to the charge c
// (ChargeFilters::CreateService) and, with cascade, enqueues its copy onto
// the charge's overrides.
//
// source: app/services/charge_filters/create_service.rb:16-46
func (s *Service) createChargeFilter(ctx context.Context, r port.Repos, c *catalog.Charge, in ChargeFilterInput, cascade bool) (*catalog.ChargeFilter, error) {
	if len(in.Values) == 0 {
		var v core.Validator
		v.Add("values", core.CodeRequired)
		return nil, v.Err()
	}
	props, err := chargeFilterProperties(c.ChargeModel, in.Properties.Value, true)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, f := range c.Filters {
		taken[f.Code] = true
	}
	f := catalog.ChargeFilter{
		ID:                 core.NewID[core.ChargeFilterID](s.d.IDs),
		ChargeID:           c.ID,
		Values:             in.Values,
		Properties:         props,
		InvoiceDisplayName: in.InvoiceDisplayName.Value,
		Code:               chargeFilterFreeCode(in.Values, taken),
		CreatedAt:          s.d.Now(),
	}
	c = c.Clone()
	c.Filters = append(c.Filters, f)
	if err := s.saveChargeFilters(ctx, r, c); err != nil {
		return nil, err
	}
	if cascade {
		err := s.enqueueChargeFilterCascade(ctx, r, c, kernel.CascadeChargeFilterArgs{
			Action:             kernel.ChargeFilterCreated,
			Values:             f.Values,
			NewProperties:      &f.Properties,
			InvoiceDisplayName: f.InvoiceDisplayName,
			Code:               f.Code,
		})
		if err != nil {
			return nil, err
		}
	}
	return &f, nil
}

// updateChargeFilter changes the filter id of the charge c with the set
// fields of in (ChargeFilters::UpdateService) and, with cascade, enqueues
// the change of its copies.
//
// source: app/services/charge_filters/update_service.rb:16-40
func (s *Service) updateChargeFilter(ctx context.Context, r port.Repos, c *catalog.Charge, id core.ChargeFilterID, in ChargeFilterInput, cascade bool) (*catalog.ChargeFilter, error) {
	c = c.Clone()
	f, ok := c.Filter(id)
	if !ok {
		return nil, core.NotFound("charge_filter")
	}
	old := f.Properties.Clone()
	in.InvoiceDisplayName.Apply(&f.InvoiceDisplayName)
	if in.Properties.Set {
		props, err := chargeFilterProperties(c.ChargeModel, in.Properties.Value, true)
		if err != nil {
			return nil, err
		}
		f.Properties = props
	}
	out := f.Clone()
	if err := s.saveChargeFilters(ctx, r, c); err != nil {
		return nil, err
	}
	if cascade {
		err := s.enqueueChargeFilterCascade(ctx, r, c, kernel.CascadeChargeFilterArgs{
			Action:             kernel.ChargeFilterUpdated,
			Values:             out.Values,
			OldProperties:      &old,
			NewProperties:      &out.Properties,
			InvoiceDisplayName: out.InvoiceDisplayName,
			Code:               out.Code,
		})
		if err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// destroyChargeFilter discards the filter id of the charge c
// (ChargeFilters::DestroyService) and, with cascade, enqueues the discard
// of its copies.
//
// source: app/services/charge_filters/destroy_service.rb:14-31
func (s *Service) destroyChargeFilter(ctx context.Context, r port.Repos, c *catalog.Charge, id core.ChargeFilterID, cascade bool) (*catalog.ChargeFilter, error) {
	c = c.Clone()
	f, ok := c.Filter(id)
	if !ok {
		return nil, core.NotFound("charge_filter")
	}
	out := f.Clone()
	c.Filters = slices.DeleteFunc(c.Filters, func(x catalog.ChargeFilter) bool { return x.ID == id })
	if err := s.saveChargeFilters(ctx, r, c); err != nil {
		return nil, err
	}
	now := c.UpdatedAt
	out.DeletedAt = &now
	if cascade {
		err := s.enqueueChargeFilterCascade(ctx, r, c, kernel.CascadeChargeFilterArgs{
			Action: kernel.ChargeFilterDestroyed,
			Values: out.Values,
			Code:   out.Code,
		})
		if err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// saveChargeFilters validates the charge c and stores it with its
// filters.
func (s *Service) saveChargeFilters(ctx context.Context, r port.Repos, c *catalog.Charge) error {
	m, err := r.Metrics.Get(ctx, c.OrganizationID, c.BillableMetricID)
	if err != nil {
		return err
	}
	if err := validateChargeRecord(c, m); err != nil {
		return err
	}
	c.UpdatedAt = s.d.Now()
	return chargeCodeConflict(r.Charges.Update(ctx, c))
}

// enqueueChargeFilterCascade enqueues the cascade a of a filter change of
// the charge c when c has overrides (ChargeFilters::FilterCascadable).
//
// source: app/services/charge_filters/filter_cascadable.rb:9-23
func (s *Service) enqueueChargeFilterCascade(ctx context.Context, r port.Repos, c *catalog.Charge, a kernel.CascadeChargeFilterArgs) error {
	children, err := r.Charges.ListChildren(ctx, c.OrganizationID, c.ID)
	if err != nil {
		return err
	}
	if len(children) == 0 {
		return nil
	}
	a.ChargeID = c.ID
	return kernel.Enqueue(ctx, r, c.OrganizationID, kernel.JobCascadeChargeFilter, a, "", s.d.Now())
}

// dispatchChargeFilterCascade enqueues one cascade per filter of the
// charge c that changed from before to after, matched by code
// (ChargeFilters::CascadeDispatcher; every filter has a code).
//
// source: app/services/charge_filters/cascade_dispatcher.rb:14-58
func (s *Service) dispatchChargeFilterCascade(ctx context.Context, r port.Repos, c *catalog.Charge, before, after []catalog.ChargeFilter) error {
	old := map[string]catalog.ChargeFilter{}
	for _, f := range before {
		old[f.Code] = f
	}
	for _, f := range after {
		prev, ok := old[f.Code]
		delete(old, f.Code)
		if ok && prev.Properties.Equal(f.Properties) && equalDisplayName(prev.InvoiceDisplayName, f.InvoiceDisplayName) {
			continue
		}
		a := kernel.CascadeChargeFilterArgs{
			Action:             kernel.ChargeFilterCreated,
			Values:             f.Values,
			NewProperties:      &f.Properties,
			InvoiceDisplayName: f.InvoiceDisplayName,
			Code:               f.Code,
		}
		if ok {
			a.Action, a.OldProperties = kernel.ChargeFilterUpdated, &prev.Properties
		}
		if err := s.enqueueChargeFilterCascade(ctx, r, c, a); err != nil {
			return err
		}
	}
	for _, f := range before {
		if _, ok := old[f.Code]; !ok {
			continue
		}
		a := kernel.CascadeChargeFilterArgs{
			Action:             kernel.ChargeFilterDestroyed,
			Values:             f.Values,
			OldProperties:      &f.Properties,
			InvoiceDisplayName: f.InvoiceDisplayName,
			Code:               f.Code,
		}
		if err := s.enqueueChargeFilterCascade(ctx, r, c, a); err != nil {
			return err
		}
	}
	return nil
}

// equalDisplayName reports whether two optional display names are equal.
func equalDisplayName(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// CreateChargeFilter adds the filter in to the charge of the original
// plan; with cascade, it is copied onto the charge's overrides.
//
// source: app/controllers/api/v1/plans/charges/filters_controller.rb:33-48
func (s *Service) CreateChargeFilter(ctx context.Context, org core.OrganizationID, plan, charge refs.Ref, in ChargeFilterInput, cascade bool) (*catalog.ChargeFilter, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ChargeFilter, error) {
		c, err := planChargeOf(ctx, r, org, plan, charge)
		if err != nil {
			return nil, err
		}
		return s.createChargeFilter(ctx, r, c, in, cascade)
	})
}

// ListChargeFilters lists the filters of the charge of the original plan
// in their order.
//
// source: app/controllers/api/v1/plans/charges/filters_controller.rb:13-27
func (s *Service) ListChargeFilters(ctx context.Context, org core.OrganizationID, plan, charge refs.Ref, page core.PageRequest) (core.Page[*catalog.ChargeFilter], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.ChargeFilter], error) {
		c, err := planChargeOf(ctx, r, org, plan, charge)
		if err != nil {
			return core.Page[*catalog.ChargeFilter]{}, err
		}
		return chargeFilterPage(c, page)
	})
}

// GetChargeFilter returns the filter id of the charge of the original
// plan; another charge's filter fails with charge_filter_not_found.
//
// source: app/controllers/api/v1/plans/charges/filters_controller.rb:29-31, 106-110
func (s *Service) GetChargeFilter(ctx context.Context, org core.OrganizationID, plan, charge refs.Ref, id core.ChargeFilterID) (*catalog.ChargeFilter, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ChargeFilter, error) {
		c, err := planChargeOf(ctx, r, org, plan, charge)
		if err != nil {
			return nil, err
		}
		return chargeFilterOf(c, id)
	})
}

// chargeFilterOf returns the filter id of c, or charge_filter_not_found.
func chargeFilterOf(c *catalog.Charge, id core.ChargeFilterID) (*catalog.ChargeFilter, error) {
	f, ok := c.Filter(id)
	if !ok {
		return nil, core.NotFound("charge_filter")
	}
	return f, nil
}

// UpdateChargeFilter changes the filter id of the charge of the original
// plan with the set fields of in; with cascade, the change goes on to its
// copies.
//
// source: app/controllers/api/v1/plans/charges/filters_controller.rb:50-67
func (s *Service) UpdateChargeFilter(ctx context.Context, org core.OrganizationID, plan, charge refs.Ref, id core.ChargeFilterID, in ChargeFilterInput, cascade bool) (*catalog.ChargeFilter, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ChargeFilter, error) {
		c, err := planChargeOf(ctx, r, org, plan, charge)
		if err != nil {
			return nil, err
		}
		return s.updateChargeFilter(ctx, r, c, id, in, cascade)
	})
}

// DeleteChargeFilter discards the filter id of the charge of the original
// plan; with cascade, its copies are discarded too. The discarded filter
// is returned.
//
// source: app/controllers/api/v1/plans/charges/filters_controller.rb:69-82
func (s *Service) DeleteChargeFilter(ctx context.Context, org core.OrganizationID, plan, charge refs.Ref, id core.ChargeFilterID, cascade bool) (*catalog.ChargeFilter, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.ChargeFilter, error) {
		c, err := planChargeOf(ctx, r, org, plan, charge)
		if err != nil {
			return nil, err
		}
		return s.destroyChargeFilter(ctx, r, c, id, cascade)
	})
}

// planChargeOf resolves the charge of the original plan of the
// plan-scoped filter API.
//
// source: app/controllers/api/v1/plans/charges/filters_controller.rb:100-109
func planChargeOf(ctx context.Context, r port.Repos, org core.OrganizationID, plan, charge refs.Ref) (*catalog.Charge, error) {
	p, err := chargePlan(ctx, r, org, plan)
	if err != nil {
		return nil, err
	}
	return chargeOnPlan(ctx, r, org, p.ID, charge, true)
}

// chargeFilterPage cuts the page asked by req out of the filters of c.
func chargeFilterPage(c *catalog.Charge, req core.PageRequest) (core.Page[*catalog.ChargeFilter], error) {
	filters := make([]*catalog.ChargeFilter, len(c.Filters))
	for i := range c.Filters {
		filters[i] = &c.Filters[i]
	}
	return chargePage(filters, func(f *catalog.ChargeFilter) string { return f.ID.String() }, req)
}
