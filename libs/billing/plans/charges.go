package plans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// ChargeInput are the values of a charge to create, update or override.
// Only the set fields apply: a create defaults the others (lago
// Charges::CreateService), an update keeps them. It is encoded in the
// charge cascade jobs, so every field is JSON.
type ChargeInput struct {
	// ID is the charge a plan update changes; zero, or an ID that is not a
	// charge of the plan, creates one (lago Plans::UpdateService
	// #process_charges finds the charge by id).
	ID core.ChargeID
	// Code is the charge's code, unique among the plan's charges; a plan
	// create or update generates a missing one from the metric's code
	// (lago Charges::GenerateCodeService).
	Code core.Optional[string]
	// BillableMetric is the charged metric, required on create and
	// ignored on update: a charge keeps its metric.
	BillableMetric refs.Ref
	// ChargeModel is the pricing model.
	ChargeModel core.Optional[catalog.ChargeModel]
	// Properties are the model's properties; unset or empty, a create (or
	// a model change) takes the model's defaults (lago
	// ChargeModels::BuildDefaultPropertiesService).
	Properties core.Optional[catalog.Properties]
	// Filters replace the charge's filters, matched to the kept ones by
	// values; an empty list removes them.
	Filters core.Optional[[]ChargeFilterInput]
	// PayInAdvance bills the charge on each event.
	PayInAdvance core.Optional[bool]
	// Invoiceable puts in-advance fees on invoices; defaults to true.
	Invoiceable core.Optional[bool]
	// RegroupPaidFees regroups paid in-advance fees on an invoice.
	RegroupPaidFees core.Optional[catalog.RegroupPaidFees]
	// Prorated prorates the charge.
	Prorated core.Optional[bool]
	// AcceptsTargetWallet splits the charge's usage by the wallet each
	// event targets; a create defaults it to false and an update changes
	// it whenever it is set, even on a subscribed plan or from a cascade
	// (lago Charges::UpdateService).
	AcceptsTargetWallet core.Optional[bool]
	// MinAmount is the minimum spending of a period, in units of the
	// plan's currency; defaults to zero.
	MinAmount core.Optional[money.Decimal]
	// InvoiceDisplayName replaces the metric's name on invoices.
	InvoiceDisplayName core.Optional[*string]
	// AppliedPricingUnit prices the charge in a pricing unit; an update
	// changes only the conversion rate of a charge that has one (lago
	// AppliedPricingUnits::UpdateService).
	AppliedPricingUnit core.Optional[*PricingUnitInput]
	// Taxes replace the charge's taxes; an empty list removes them.
	Taxes core.Optional[[]refs.Ref]

	// parent is the parent charge of a charge created on an override
	// plan by a cascade.
	parent core.ChargeID
}

// PricingUnitInput is the pricing unit of a charge.
type PricingUnitInput struct {
	// PricingUnit is the pricing unit; ignored on update.
	PricingUnit refs.Ref
	// ConversionRate is the fiat value of one pricing unit, > 0.
	ConversionRate money.Decimal
}

// codeValueAlreadyExists is the code of a subscription charge filter whose
// values another filter of the charge has (lago
// Subscriptions::ChargeFilters::CreateService).
const codeValueAlreadyExists = "value_already_exists"

// chargeUpdate says how [Service.updateCharge] runs: on the charge itself,
// or on an override charge from its parent's cascade (lago
// Charges::UpdateService cascade_options).
type chargeUpdate struct {
	// cascade updates an override charge from its parent.
	cascade bool
	// equalProperties reports whether the override still prices like the
	// parent before its update.
	equalProperties bool
	// equalRate reports whether the override still has the parent's
	// conversion rate before its update.
	equalRate bool
}

// createCharge creates the charge in on the plan p (Charges::CreateService);
// with cascade, it also enqueues the creation of its overrides on p's
// child plans.
//
// source: app/services/charges/create_service.rb:15-75
func (s *Service) createCharge(ctx context.Context, r port.Repos, p *catalog.Plan, in ChargeInput, cascade bool) (*catalog.Charge, error) {
	org := p.OrganizationID
	m, err := refs.Metrics(r, org).One(ctx, in.BillableMetric)
	if err != nil {
		return nil, err
	}
	now := s.d.Now()
	c := &catalog.Charge{
		ID:                  core.NewID[core.ChargeID](s.d.IDs),
		OrganizationID:      org,
		PlanID:              p.ID,
		ParentID:            in.parent,
		Code:                in.Code.Value,
		BillableMetricID:    m.ID,
		ChargeModel:         in.ChargeModel.Value,
		PayInAdvance:        in.PayInAdvance.Value,
		Prorated:            in.Prorated.Value,
		AcceptsTargetWallet: in.AcceptsTargetWallet.Value,
		Invoiceable:         true,
		InvoiceDisplayName:  in.InvoiceDisplayName.Value,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if c.Code == "" {
		if c.Code, err = chargeGenerateCode(ctx, r, p, m); err != nil {
			return nil, err
		}
	}
	props := in.Properties.Value
	if !chargePropertiesPresent(props) {
		props = chargeDefaultProperties(c.ChargeModel)
	}
	if c.Properties, err = chargeFilterProperties(c.ChargeModel, props, false); err != nil {
		return nil, err
	}
	if len(in.Filters.Value) > 0 {
		if err := s.applyChargeFilters(c, in.Filters.Value); err != nil {
			return nil, err
		}
	}
	in.Invoiceable.Apply(&c.Invoiceable)
	in.RegroupPaidFees.Apply(&c.RegroupPaidFees)
	if c.MinAmount, err = chargeMinAmount(p, in.MinAmount.Value); err != nil {
		return nil, err
	}
	if in.AppliedPricingUnit.Set && in.AppliedPricingUnit.Value != nil {
		if c.AppliedPricingUnit, err = chargePricingUnit(ctx, r, org, *in.AppliedPricingUnit.Value); err != nil {
			return nil, err
		}
	}
	if in.Taxes.Set {
		if c.TaxIDs, err = refs.Taxes(r, org).IDs(ctx, in.Taxes.Value); err != nil {
			return nil, err
		}
	}
	if err := validateChargeRecord(c, m); err != nil {
		return nil, err
	}
	if err := r.Charges.Create(ctx, c); err != nil {
		return nil, chargeCodeConflict(err)
	}
	if cascade {
		children, err := r.Plans.ListChildren(ctx, org, p.ID)
		if err != nil {
			return nil, fmt.Errorf("load override plans of %s: %w", p.ID, err)
		}
		if len(children) > 0 {
			if err := s.enqueueCreateChargeChildren(ctx, r, c, in); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

// updateCharge changes the charge c with in (Charges::UpdateService). On
// the charge itself, the model, metric-bound flags, minimum and code only
// change while the plan has no subscription; from a cascade, the override
// takes the parent's code and grouping keys, and its pricing when it still
// priced like the parent.
//
// source: app/services/charges/update_service.rb:18-95
func (s *Service) updateCharge(ctx context.Context, r port.Repos, c *catalog.Charge, in ChargeInput, u chargeUpdate) (*catalog.Charge, error) {
	if u.cascade && in.ChargeModel.Value != c.ChargeModel {
		return c, nil
	}
	org := c.OrganizationID
	p, err := r.Plans.Get(ctx, org, c.PlanID)
	if err != nil {
		return nil, fmt.Errorf("load plan %s: %w", c.PlanID, err)
	}
	attached, err := planAttached(ctx, r, p)
	if err != nil {
		return nil, err
	}
	m, err := r.Metrics.Get(ctx, org, c.BillableMetricID)
	if err != nil {
		return nil, fmt.Errorf("load billable metric %s: %w", c.BillableMetricID, err)
	}
	c = c.Clone()
	oldModel := c.ChargeModel
	if !attached {
		in.ChargeModel.Apply(&c.ChargeModel)
	}
	if !u.cascade {
		in.InvoiceDisplayName.Apply(&c.InvoiceDisplayName)
	}
	if u.cascade {
		if code := in.Code.Value; code != "" {
			c.Code = code
		}
		if c.Properties, err = chargeCascadeGroupKeys(c.Properties, in.Properties.Value, true); err != nil {
			return nil, err
		}
	}
	if !u.cascade || u.equalProperties {
		switch props := in.Properties.Value; {
		case chargePropertiesPresent(props):
			if c.Properties, err = chargeFilterProperties(c.ChargeModel, props, false); err != nil {
				return nil, err
			}
		case in.Properties.Set || c.ChargeModel != oldModel:
			if c.Properties, err = chargeFilterProperties(c.ChargeModel, chargeDefaultProperties(c.ChargeModel), false); err != nil {
				return nil, err
			}
		}
	}
	in.AcceptsTargetWallet.Apply(&c.AcceptsTargetWallet)
	if a := in.AppliedPricingUnit; a.Set && a.Value != nil && c.AppliedPricingUnit != nil && (!u.cascade || u.equalRate) {
		c.AppliedPricingUnit.ConversionRate = a.Value.ConversionRate
	}
	if !u.cascade {
		if in.Filters.Set {
			if err := s.applyChargeFilters(c, in.Filters.Value); err != nil {
				return nil, err
			}
		}
		if in.Taxes.Set {
			if c.TaxIDs, err = refs.Taxes(r, org).IDs(ctx, in.Taxes.Value); err != nil {
				return nil, err
			}
		}
		// Charges cannot be edited further once the plan is subscribed
		// (update_service.rb:72).
		if !attached {
			in.Invoiceable.Apply(&c.Invoiceable)
			if in.MinAmount.Set {
				if c.MinAmount, err = chargeMinAmount(p, in.MinAmount.Value); err != nil {
					return nil, err
				}
			}
			if code := in.Code.Value; code != "" {
				c.Code = code
			}
			in.PayInAdvance.Apply(&c.PayInAdvance)
			in.Prorated.Apply(&c.Prorated)
			in.RegroupPaidFees.Apply(&c.RegroupPaidFees)
		}
	}
	if err := validateChargeRecord(c, m); err != nil {
		return nil, err
	}
	c.UpdatedAt = s.d.Now()
	if err := r.Charges.Update(ctx, c); err != nil {
		return nil, chargeCodeConflict(err)
	}
	return c, nil
}

// discardCharge discards the charge c with its filters
// (Charges::DestroyService); with cascade, it also enqueues the discard of
// its overrides.
//
// source: app/services/charges/destroy_service.rb:13-33
func (s *Service) discardCharge(ctx context.Context, r port.Repos, c *catalog.Charge, cascade bool) (*catalog.Charge, error) {
	now := s.d.Now()
	if err := r.Charges.Discard(ctx, c.OrganizationID, c.ID, now); err != nil {
		return nil, fmt.Errorf("discard charge %s: %w", c.ID, err)
	}
	out := c.Clone()
	out.DeletedAt, out.UpdatedAt = &now, now
	if !cascade {
		return out, nil
	}
	children, err := r.Charges.ListChildren(ctx, c.OrganizationID, c.ID)
	if err != nil {
		return nil, fmt.Errorf("load overrides of charge %s: %w", c.ID, err)
	}
	if len(children) > 0 {
		args := kernel.ChargeArgs{ChargeID: c.ID}
		if err := kernel.Enqueue(ctx, r, c.OrganizationID, kernel.JobDestroyChargeChildren, args, "", now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// processPlanCharges replaces the charges of the plan p by in
// (Plans::UpdateService#process_charges and #sanitize_charges), cascading
// to the child plans with cascade.
//
// source: app/services/plans/update_service.rb:245-276, app/services/plans/chargeables_validation_service.rb:28-35
func (s *Service) processPlanCharges(ctx context.Context, r port.Repos, p *catalog.Plan, in []ChargeInput, cascade bool) error {
	org := p.OrganizationID
	for _, c := range in {
		if c.BillableMetric.IsZero() {
			continue
		}
		if _, err := refs.Metrics(r, org).One(ctx, c.BillableMetric); err != nil {
			if errors.As(err, new(*core.NotFoundError)) {
				return core.NotFound("billable_metrics")
			}
			return err
		}
	}
	if cascade {
		children, err := r.Plans.ListChildren(ctx, org, p.ID)
		if err != nil {
			return fmt.Errorf("load override plans of %s: %w", p.ID, err)
		}
		cascade = len(children) > 0
	}
	current, err := r.Charges.ListByPlan(ctx, org, p.ID)
	if err != nil {
		return fmt.Errorf("load charges of plan %s: %w", p.ID, err)
	}
	keep := map[core.ChargeID]bool{}
	for _, c := range in {
		if !c.ID.IsZero() {
			keep[c.ID] = true
		}
		i := slices.IndexFunc(current, func(x *catalog.Charge) bool { return x.ID == c.ID })
		if c.ID.IsZero() || i < 0 {
			created, err := s.createCharge(ctx, r, p, c, false)
			if err != nil {
				return err
			}
			keep[created.ID] = true
			if cascade {
				if err := s.enqueueCreateChargeChildren(ctx, r, created, c); err != nil {
					return err
				}
			}
			continue
		}
		old := current[i]
		updated, err := s.updateCharge(ctx, r, old, c, chargeUpdate{})
		if err != nil {
			return err
		}
		if !cascade {
			continue
		}
		var rate *money.Decimal
		if a := c.AppliedPricingUnit; a.Set && a.Value != nil {
			rate = &a.Value.ConversionRate
		}
		if err := s.enqueueUpdateChargeChildren(ctx, r, old, updated, c.Code.Value, rate, c.AcceptsTargetWallet); err != nil {
			return err
		}
		if c.Filters.Set {
			if err := s.dispatchChargeFilterCascade(ctx, r, updated, old.Filters, updated.Filters); err != nil {
				return err
			}
		}
	}
	for _, c := range current {
		if keep[c.ID] {
			continue
		}
		if _, err := s.discardCharge(ctx, r, c, cascade); err != nil {
			return err
		}
	}
	return nil
}

// overrideCharge copies parent onto the override plan plan with the values
// of in, nil for none (Charges::OverrideService). The copy keeps the
// parent's filters with their codes and its pricing unit, but not its
// taxes, which only in sets.
//
// source: app/services/charges/override_service.rb:13-62
func (s *Service) overrideCharge(ctx context.Context, r port.Repos, parent *catalog.Charge, plan *catalog.Plan, in *ChargeInput) (*catalog.Charge, error) {
	if in == nil {
		in = &ChargeInput{}
	}
	o := parent.NewOverride(core.NewID[core.ChargeID](s.d.IDs), plan.ID, func() core.ChargeFilterID {
		return core.NewID[core.ChargeFilterID](s.d.IDs)
	}, s.d.Now())
	o.OrganizationID = plan.OrganizationID
	o.TaxIDs = nil
	in.Properties.Apply(&o.Properties)
	if in.MinAmount.Set {
		var err error
		if o.MinAmount, err = chargeMinAmount(plan, in.MinAmount.Value); err != nil {
			return nil, err
		}
	}
	in.InvoiceDisplayName.Apply(&o.InvoiceDisplayName)
	if in.Filters.Set {
		if err := s.applyChargeFilters(o, in.Filters.Value); err != nil {
			return nil, err
		}
	}
	if a := in.AppliedPricingUnit; a.Set && a.Value != nil && o.AppliedPricingUnit != nil && !a.Value.ConversionRate.IsZero() {
		o.AppliedPricingUnit.ConversionRate = a.Value.ConversionRate
	}
	if in.Taxes.Set {
		var err error
		if o.TaxIDs, err = refs.Taxes(r, plan.OrganizationID).IDs(ctx, in.Taxes.Value); err != nil {
			return nil, err
		}
	}
	m, err := r.Metrics.Get(ctx, o.OrganizationID, o.BillableMetricID)
	if err != nil {
		return nil, fmt.Errorf("load billable metric %s: %w", o.BillableMetricID, err)
	}
	if err := validateChargeRecord(o, m); err != nil {
		return nil, err
	}
	if err := r.Charges.Create(ctx, o); err != nil {
		return nil, chargeCodeConflict(err)
	}
	return o, nil
}

// enqueueCreateChargeChildren enqueues the copy of the new charge c onto
// the override plans of its plan.
func (s *Service) enqueueCreateChargeChildren(ctx context.Context, r port.Repos, c *catalog.Charge, in ChargeInput) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode charge payload: %w", err)
	}
	args := kernel.CreateChargeChildrenArgs{ChargeID: c.ID, Payload: payload}
	return kernel.Enqueue(ctx, r, c.OrganizationID, kernel.JobCreateChargeChildren, args, "", s.d.Now())
}

// enqueueUpdateChargeChildren enqueues the cascade of the update of old
// into updated to its overrides: code is the code they take ("" keeps
// theirs), rate the new conversion rate, nil for none, and targetWallet
// the accepts_target_wallet of a plan update's payload, which the
// overrides take when set (lago Charges::CascadeUpdatable#trigger_cascade
// and Plans::UpdateService#cascade_charge_update, whose payload carries
// accepts_target_wallet).
func (s *Service) enqueueUpdateChargeChildren(ctx context.Context, r port.Repos, old, updated *catalog.Charge, code string, rate *money.Decimal, targetWallet core.Optional[bool]) error {
	props := updated.Properties.Clone()
	args := kernel.UpdateChargeChildrenArgs{
		ChargeID:       updated.ID,
		Code:           code,
		ChargeModel:    updated.ChargeModel,
		Properties:     &props,
		ConversionRate: rate,
		OldChargeModel: old.ChargeModel,
		OldProperties:  old.Properties.Clone(),
	}
	if targetWallet.Set {
		args.AcceptsTargetWallet = &targetWallet.Value
	}
	if old.AppliedPricingUnit != nil {
		r := old.AppliedPricingUnit.ConversionRate
		args.OldConversionRate = &r
	}
	return kernel.Enqueue(ctx, r, updated.OrganizationID, kernel.JobUpdateChargeChildren, args, "", s.d.Now())
}

// planAttached reports whether the plan p has subscriptions in any status
// (lago Plan#attached_to_subscriptions?).
func planAttached(ctx context.Context, r port.Repos, p *catalog.Plan) (bool, error) {
	page, err := r.Subscriptions.List(ctx, p.OrganizationID, subscription.Filter{PlanID: p.ID}, core.PageRequest{Size: 1})
	if err != nil {
		return false, fmt.Errorf("load subscriptions of plan %s: %w", p.ID, err)
	}
	return len(page.Items) > 0, nil
}

// chargeSuffix matches the numeric suffix of a generated charge code.
var chargeSuffix = regexp.MustCompile(`^_(\d+)$`)

// chargeGenerateCode returns the code of a new charge of the metric m on
// the plan p: the metric's code when no parent charge of the plan uses it,
// else the code with the highest numeric suffix in use plus one, at least
// 2.
//
// source: app/services/charges/generate_code_service.rb:20-33
func chargeGenerateCode(ctx context.Context, r port.Repos, p *catalog.Plan, m *catalog.BillableMetric) (string, error) {
	charges, err := r.Charges.ListByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return "", fmt.Errorf("load charges of plan %s: %w", p.ID, err)
	}
	base := m.Code
	if !slices.ContainsFunc(charges, func(c *catalog.Charge) bool { return !c.IsChild() && c.Code == base }) {
		return base, nil
	}
	maxSuffix := 1
	for _, c := range charges {
		rest, ok := strings.CutPrefix(c.Code, base)
		if c.IsChild() || !ok {
			continue
		}
		if g := chargeSuffix.FindStringSubmatch(rest); g != nil {
			if n, err := strconv.Atoi(g[1]); err == nil && n > maxSuffix {
				maxSuffix = n
			}
		}
	}
	return base + "_" + strconv.Itoa(maxSuffix+1), nil
}

// chargeMinAmount converts the minimum amount in units of the plan's
// currency to minor units.
func chargeMinAmount(p *catalog.Plan, d money.Decimal) (int64, error) {
	n, err := p.Currency.ToMinor(d, money.HalfUp)
	if err != nil {
		var v core.Validator
		v.Add("min_amount", core.CodeInvalid)
		return 0, v.Err()
	}
	return n, nil
}

// chargePricingUnit returns the applied pricing unit of in (lago
// AppliedPricingUnits::CreateService).
func chargePricingUnit(ctx context.Context, r port.Repos, org core.OrganizationID, in PricingUnitInput) (*catalog.AppliedPricingUnit, error) {
	pu, err := refs.PricingUnits(r, org).One(ctx, in.PricingUnit)
	if err != nil {
		return nil, err
	}
	return &catalog.AppliedPricingUnit{PricingUnitID: pu.ID, ConversionRate: in.ConversionRate}, nil
}

// chargeCodeConflict maps a code already used to its validation error.
//
// source: app/services/charges/create_service.rb:78-79
func chargeCodeConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		var v core.Validator
		v.Add("code", core.CodeAlreadyExists)
		return v.Err()
	}
	return err
}

// validateChargeRecord checks the charge c on the metric m: the rules of
// the model, then the properties of the charge and of its filters with
// the model's validator, whose errors lago reports on properties
// (ChargePropertiesValidation#validate_charge_model_properties).
//
// source: app/models/concerns/charge_properties_validation.rb:18-28
func validateChargeRecord(c *catalog.Charge, m *catalog.BillableMetric) error {
	var v core.Validator
	v.Merge("", catalog.ValidateCharge(c, m))
	opts := pricing.ValidateOptions{LatestAggregation: m.AggregationType == catalog.AggregationTypeLatest}
	chargePropertiesErrors(&v, "properties", c.ChargeModel, c.Properties, opts)
	for i := range c.Filters {
		chargePropertiesErrors(&v, "filters["+strconv.Itoa(i)+"].properties", c.ChargeModel, c.Filters[i].Properties, opts)
	}
	return v.Err()
}

// chargePropertiesErrors adds the errors of the model's validator on
// props to v under field, once per code.
func chargePropertiesErrors(v *core.Validator, field string, model catalog.ChargeModel, props catalog.Properties, opts pricing.ValidateOptions) {
	if !model.Valid() || !props.IsObject() {
		return
	}
	errs, err := pricing.ValidateJSON(pricing.ChargeModel(model), props.Raw(), opts)
	if err != nil {
		v.Add(field, core.CodeInvalid)
		return
	}
	var seen []string
	for _, e := range errs {
		if !slices.Contains(seen, e.Code) {
			seen = append(seen, e.Code)
			v.Add(field, e.Code)
		}
	}
}

// chargePropertiesPresent reports whether props hold at least one key
// (Rails presence).
func chargePropertiesPresent(props catalog.Properties) bool {
	m, err := chargePropertiesMap(props)
	return err == nil && len(m) > 0
}

// chargePropertiesMap decodes props, keeping numbers as written.
func chargePropertiesMap(props catalog.Properties) (map[string]any, error) {
	raw := map[string]any{}
	dec := json.NewDecoder(strings.NewReader(string(props.Raw())))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode properties: %w", err)
	}
	return raw, nil
}

// chargePropertiesOf encodes raw.
func chargePropertiesOf(raw map[string]any) (catalog.Properties, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return catalog.Properties{}, fmt.Errorf("encode properties: %w", err)
	}
	return catalog.NewProperties(b)
}

// chargeDefaultProperties returns the properties of a charge of model
// created without any; none for an unknown model.
//
// source: app/services/charge_models/build_default_properties_service.rb:10-21
func chargeDefaultProperties(model catalog.ChargeModel) catalog.Properties {
	p, err := pricing.DefaultProperties(pricing.ChargeModel(model))
	if err != nil {
		return catalog.Properties{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return catalog.Properties{}
	}
	out, err := catalog.NewProperties(b)
	if err != nil {
		return catalog.Properties{}
	}
	return out
}

// chargeFilterProperties keeps the properties model uses
// (ChargeModels::FilterPropertiesService); a filter's properties drop
// their presentation group keys, which only the charge has.
//
// source: app/services/charge_filters/create_or_update_batch_service.rb:57-60
func chargeFilterProperties(model catalog.ChargeModel, props catalog.Properties, filter bool) (catalog.Properties, error) {
	raw, err := chargePropertiesMap(props)
	if err != nil {
		return catalog.Properties{}, err
	}
	if filter {
		delete(raw, "presentation_group_keys")
	}
	return chargePropertiesOf(pricing.FilterProperties(pricing.UsageCharge, pricing.ChargeModel(model), raw))
}

// chargeCascadeGroupKeys returns props with the pricing group keys of
// from, or without any when from has none, and, with presentation, the
// same for the presentation group keys: grouping is structural, so an
// override follows its parent's even when it prices on its own.
//
// source: app/services/charges/update_service.rb:103-123, app/services/charge_filters/cascade_service.rb:198-208
func chargeCascadeGroupKeys(props, from catalog.Properties, presentation bool) (catalog.Properties, error) {
	raw, err := chargePropertiesMap(props)
	if err != nil {
		return catalog.Properties{}, err
	}
	src, err := chargePropertiesMap(from)
	if err != nil {
		return catalog.Properties{}, err
	}
	keys := []string{"pricing_group_keys"}
	if presentation {
		keys = append(keys, "presentation_group_keys")
	}
	for _, k := range keys {
		if v, ok := src[k]; ok && v != nil {
			raw[k] = v
		} else {
			delete(raw, k)
		}
	}
	return chargePropertiesOf(raw)
}

// CreateCharge creates the charge in on the plan (lago
// Api::V1::Plans::ChargesController#create); with cascade, it is copied
// onto the plan's override plans with active or pending subscriptions.
// The plan is an original plan, by ID or code; the charge needs a code.
//
// source: app/controllers/api/v1/plans/charges_controller.rb:40-55, app/models/charge.rb:50
func (s *Service) CreateCharge(ctx context.Context, org core.OrganizationID, plan refs.Ref, in ChargeInput, cascade bool) (*catalog.Charge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Charge, error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return nil, err
		}
		if in.Code.Value == "" {
			var v core.Validator
			v.Add("code", core.CodeRequired)
			return nil, v.Err()
		}
		in.ID, in.parent = core.ChargeID{}, core.ChargeID{}
		return s.createCharge(ctx, r, p, in, cascade)
	})
}

// GetCharge returns the charge of the original plan, by ID or code.
//
// source: app/controllers/api/v1/plans/charges_controller.rb:30-38
func (s *Service) GetCharge(ctx context.Context, org core.OrganizationID, plan, charge refs.Ref) (*catalog.Charge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Charge, error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return nil, err
		}
		return chargeOnPlan(ctx, r, org, p.ID, charge, true)
	})
}

// ListCharges lists the charges of the original plan, most recent first.
//
// source: app/controllers/api/v1/plans/charges_controller.rb:10-28
func (s *Service) ListCharges(ctx context.Context, org core.OrganizationID, plan refs.Ref, page core.PageRequest) (core.Page[*catalog.Charge], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.Charge], error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return core.Page[*catalog.Charge]{}, err
		}
		charges, err := r.Charges.ListByPlan(ctx, org, p.ID)
		if err != nil {
			return core.Page[*catalog.Charge]{}, fmt.Errorf("load charges of plan %s: %w", p.ID, err)
		}
		charges = slices.DeleteFunc(charges, (*catalog.Charge).IsChild)
		slices.Reverse(charges)
		return chargePage(charges, func(c *catalog.Charge) string { return c.ID.String() }, page)
	})
}

// UpdateCharge changes the charge of the original plan with in (lago
// Charges::UpdateService); with cascade, the change goes on to the
// charge's overrides on plans with active or pending subscriptions, with
// its filter changes.
//
// source: app/controllers/api/v1/plans/charges_controller.rb:57-73, app/services/charges/cascade_updatable.rb:9-23
func (s *Service) UpdateCharge(ctx context.Context, org core.OrganizationID, plan, charge refs.Ref, in ChargeInput, cascade bool) (*catalog.Charge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Charge, error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return nil, err
		}
		old, err := chargeOnPlan(ctx, r, org, p.ID, charge, true)
		if err != nil {
			return nil, err
		}
		updated, err := s.updateCharge(ctx, r, old, in, chargeUpdate{})
		if err != nil || !cascade {
			return updated, err
		}
		children, err := r.Charges.ListChildren(ctx, org, updated.ID)
		if err != nil {
			return nil, fmt.Errorf("load overrides of charge %s: %w", updated.ID, err)
		}
		if len(children) == 0 {
			return updated, nil
		}
		if err := s.enqueueUpdateChargeChildren(ctx, r, old, updated, updated.Code, nil, core.Optional[bool]{}); err != nil {
			return nil, err
		}
		if err := s.dispatchChargeFilterCascade(ctx, r, updated, old.Filters, updated.Filters); err != nil {
			return nil, err
		}
		return updated, nil
	})
}

// DeleteCharge discards the charge of the original plan with its filters;
// with cascade, its overrides on plans with active or pending
// subscriptions are discarded too. The discarded charge is returned.
//
// source: app/controllers/api/v1/plans/charges_controller.rb:75-87
func (s *Service) DeleteCharge(ctx context.Context, org core.OrganizationID, plan, charge refs.Ref, cascade bool) (*catalog.Charge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.Charge, error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return nil, err
		}
		c, err := chargeOnPlan(ctx, r, org, p.ID, charge, true)
		if err != nil {
			return nil, err
		}
		return s.discardCharge(ctx, r, c, cascade)
	})
}

// chargePlan resolves the original plan of the plan-scoped charge API:
// an override or deleted plan is not found.
//
// source: app/controllers/api/v1/plans/base_controller.rb:11-17
func chargePlan(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (*catalog.Plan, error) {
	p, err := refs.Plans(r, org).One(ctx, ref)
	if err != nil {
		return nil, err
	}
	if p.IsChild() || p.Deleted() {
		return nil, core.NotFound("plan")
	}
	return p, nil
}

// chargeOnPlan resolves the kept charge of the plan, by ID or code;
// parents keeps original charges only.
func chargeOnPlan(ctx context.Context, r port.Repos, org core.OrganizationID, plan core.PlanID, ref refs.Ref, parents bool) (*catalog.Charge, error) {
	var (
		c   *catalog.Charge
		err error
	)
	switch {
	case ref.Code != "":
		c, err = r.Charges.GetByCode(ctx, org, plan, ref.Code)
	case ref.ID != uuid.Nil:
		var id core.ChargeID
		if id, err = core.ParseID[core.ChargeID](ref.ID.String()); err == nil {
			c, err = r.Charges.Get(ctx, org, id)
		}
	default:
		err = core.ErrNotFound
	}
	if err == nil && (c.PlanID != plan || c.DeletedAt != nil || (parents && c.IsChild())) {
		err = core.ErrNotFound
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound("charge")
	}
	if err != nil {
		return nil, fmt.Errorf("find charge %s: %w", ref, err)
	}
	return c, nil
}

// chargePage cuts the page asked by req out of items, whose page tokens are
// the key of the last item of the previous page.
func chargePage[T any](items []T, key func(T) string, req core.PageRequest) (core.Page[T], error) {
	start := 0
	if req.Token != "" {
		i := slices.IndexFunc(items, func(x T) bool { return key(x) == req.Token })
		if i < 0 {
			return core.Page[T]{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
		}
		start = i + 1
	}
	end := min(start+req.Limit(), len(items))
	out := core.Page[T]{Items: items[start:end]}
	if end < len(items) {
		out.NextToken = key(items[end-1])
	}
	return out, nil
}
