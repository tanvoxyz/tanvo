package plans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// FixedChargeInput are the values of a fixed charge to create, update or
// override. Only the set fields apply: a create defaults the others (lago
// FixedCharges::CreateService), an update keeps them. It is encoded in the
// fixed-charge cascade jobs, so every field is JSON.
type FixedChargeInput struct {
	// ID is the fixed charge a plan update changes; zero, or an ID that is
	// not a fixed charge of the plan, creates one (lago
	// Plans::UpdateService#process_fixed_charges finds it by id).
	ID core.FixedChargeID
	// AddOn is the priced add-on, required on create and ignored on
	// update: a fixed charge keeps its add-on.
	AddOn refs.Ref
	// Code is the fixed charge's code, unique among the plan's fixed
	// charges; a plan create or update generates a missing one from the
	// add-on's code (lago FixedCharges::GenerateCodeService).
	Code core.Optional[string]
	// ChargeModel is the pricing model; it only changes while the plan has
	// no subscription.
	ChargeModel core.Optional[catalog.FixedChargeModel]
	// Properties are the model's properties; unset or empty, a create (or
	// a model change) takes the model's defaults (lago
	// ChargeModels::BuildDefaultPropertiesService).
	Properties core.Optional[catalog.Properties]
	// Units are the units billed per period; zero by default.
	Units core.Optional[money.Decimal]
	// PayInAdvance bills the fixed charge at the start of the period; set
	// on create only.
	PayInAdvance core.Optional[bool]
	// Prorated prorates units changed during a period; set on create only.
	Prorated core.Optional[bool]
	// InvoiceDisplayName replaces the add-on's name on invoices.
	InvoiceDisplayName core.Optional[*string]
	// ApplyUnitsImmediately makes a change of units take effect at once
	// rather than at the start of the next fixed charges period.
	ApplyUnitsImmediately bool
	// Taxes replace the fixed charge's taxes; an empty list removes them.
	Taxes core.Optional[[]refs.Ref]

	// parent is the parent fixed charge of one created on an override plan
	// by a cascade.
	parent core.FixedChargeID
}

// fixedChargeUpdate says how [Service.updateFixedCharge] runs: on the
// fixed charge itself, or on an override from its parent's cascade (lago
// FixedCharges::UpdateService cascade_options and trigger_billing).
type fixedChargeUpdate struct {
	// cascade updates an override fixed charge from its parent.
	cascade bool
	// equalProperties reports whether the override still prices like the
	// parent before its update.
	equalProperties bool
	// triggerBilling bills the pay-in-advance units applied immediately.
	triggerBilling bool
}

// Codes of the fixed-charge rules.
const (
	// codeCannotOverrideChargeModel refuses an override of another model
	// (lago FixedCharges::OverrideService).
	codeCannotOverrideChargeModel = "cannot_override_charge_model"
	// codeFixedChargeAlreadyDeleted refuses the deletion of a discarded
	// fixed charge (lago FixedCharges::DestroyService).
	codeFixedChargeAlreadyDeleted = "fixed_charge_already_deleted"
	// codeSubscriptionIncomplete refuses the change of a fixed charge of an
	// incomplete subscription (lago
	// Subscriptions::UpdateOrOverrideFixedChargeService).
	codeSubscriptionIncomplete = "subscription_incomplete"
)

// createFixedCharge creates the fixed charge in on the plan p
// (FixedCharges::CreateService); with cascade, it also enqueues the
// creation of its overrides on p's child plans.
func (s *Service) createFixedCharge(ctx context.Context, r port.Repos, p *catalog.Plan, in FixedChargeInput, cascade bool) (*catalog.FixedCharge, error) {
	return s.createFixedChargeAt(ctx, r, p, in, cascade, s.d.Now())
}

// createFixedChargeAt is [Service.createFixedCharge] for a change at ts:
// the units of the plan's subscriptions change at ts.
//
// source: app/services/fixed_charges/create_service.rb:15-66
func (s *Service) createFixedChargeAt(ctx context.Context, r port.Repos, p *catalog.Plan, in FixedChargeInput, cascade bool, ts time.Time) (*catalog.FixedCharge, error) {
	org := p.OrganizationID
	a, err := refs.AddOns(r, org).One(ctx, in.AddOn)
	if err != nil {
		return nil, err
	}
	now := s.d.Now()
	f := &catalog.FixedCharge{
		ID:                 core.NewID[core.FixedChargeID](s.d.IDs),
		OrganizationID:     org,
		PlanID:             p.ID,
		ParentID:           in.parent,
		Code:               in.Code.Value,
		AddOnID:            a.ID,
		ChargeModel:        in.ChargeModel.Value,
		Units:              money.Zero,
		PayInAdvance:       in.PayInAdvance.Value,
		Prorated:           in.Prorated.Value,
		InvoiceDisplayName: in.InvoiceDisplayName.Value,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	in.Units.Apply(&f.Units)
	if f.Code == "" {
		if f.Code, err = fixedChargeGenerateCode(ctx, r, p, a); err != nil {
			return nil, err
		}
	}
	props := in.Properties.Value
	if !chargePropertiesPresent(props) {
		props = fixedChargeDefaultProperties(f.ChargeModel)
	}
	if f.Properties, err = fixedChargeFilterProperties(f.ChargeModel, props); err != nil {
		return nil, err
	}
	if in.Taxes.Set {
		if f.TaxIDs, err = refs.Taxes(r, org).IDs(ctx, in.Taxes.Value); err != nil {
			return nil, err
		}
	}
	if err := validateFixedChargeRecord(f); err != nil {
		return nil, err
	}
	if err := r.FixedCharges.Create(ctx, f); err != nil {
		return nil, chargeCodeConflict(err)
	}
	if _, err := s.emitFixedChargeEvents(ctx, r, f, nil, in.ApplyUnitsImmediately, ts); err != nil {
		return nil, err
	}
	if cascade {
		children, err := r.Plans.ListChildren(ctx, org, p.ID)
		if err != nil {
			return nil, fmt.Errorf("load override plans of %s: %w", p.ID, err)
		}
		if len(children) > 0 {
			if err := s.enqueueCreateFixedChargeChildren(ctx, r, f, in); err != nil {
				return nil, err
			}
		}
	}
	return f, nil
}

// updateFixedCharge changes the fixed charge f with in at ts
// (FixedCharges::UpdateService). On the fixed charge itself, the model and
// code only change while the plan has no subscription; pay_in_advance and
// prorated never change. From a cascade, the override takes the parent's
// code, and its units and properties when it still priced like the
// parent. A change of units records the units of the plan's subscriptions
// and, with triggerBilling and applied immediately, bills a pay-in-advance
// fixed charge.
//
// source: app/services/fixed_charges/update_service.rb:22-75
func (s *Service) updateFixedCharge(ctx context.Context, r port.Repos, f *catalog.FixedCharge, in FixedChargeInput, u fixedChargeUpdate, ts time.Time) (*catalog.FixedCharge, error) {
	if u.cascade && in.ChargeModel.Value != f.ChargeModel {
		return f, nil
	}
	org := f.OrganizationID
	p, err := r.Plans.Get(ctx, org, f.PlanID)
	if err != nil {
		return nil, fmt.Errorf("load plan %s: %w", f.PlanID, err)
	}
	attached, err := planAttached(ctx, r, p)
	if err != nil {
		return nil, err
	}
	old := f
	f = f.Clone()
	if !attached {
		in.ChargeModel.Apply(&f.ChargeModel)
	}
	if !u.cascade {
		in.InvoiceDisplayName.Apply(&f.InvoiceDisplayName)
	}
	if code := in.Code.Value; u.cascade && code != "" {
		f.Code = code
	}
	if !u.cascade || u.equalProperties {
		in.Units.Apply(&f.Units)
		switch props := in.Properties.Value; {
		case chargePropertiesPresent(props):
			if f.Properties, err = fixedChargeFilterProperties(f.ChargeModel, props); err != nil {
				return nil, err
			}
		case in.Properties.Set || f.ChargeModel != old.ChargeModel:
			if f.Properties, err = fixedChargeFilterProperties(f.ChargeModel, fixedChargeDefaultProperties(f.ChargeModel)); err != nil {
				return nil, err
			}
		}
	}
	if !u.cascade {
		if in.Taxes.Set {
			if f.TaxIDs, err = refs.Taxes(r, org).IDs(ctx, in.Taxes.Value); err != nil {
				return nil, err
			}
		}
		// Structural fields cannot be edited once the plan is subscribed
		// (update_service.rb:62).
		if code := in.Code.Value; !attached && code != "" {
			f.Code = code
		}
	}
	if err := validateFixedChargeRecord(f); err != nil {
		return nil, err
	}
	f.UpdatedAt = s.d.Now()
	if err := r.FixedCharges.Update(ctx, f); err != nil {
		return nil, chargeCodeConflict(err)
	}
	if !old.Units.Equal(f.Units) {
		if _, err := s.emitFixedChargeEvents(ctx, r, f, nil, in.ApplyUnitsImmediately, ts); err != nil {
			return nil, err
		}
		if u.triggerBilling && in.ApplyUnitsImmediately && f.PayInAdvance {
			if err := s.enqueueAllInAdvanceFixedCharges(ctx, r, p, ts, f.ID); err != nil {
				return nil, err
			}
		}
	}
	return f, nil
}

// discardFixedCharge discards the fixed charge f
// (FixedCharges::DestroyService); with cascade, it also enqueues the
// discard of its overrides.
//
// source: app/services/fixed_charges/destroy_service.rb:14-33
func (s *Service) discardFixedCharge(ctx context.Context, r port.Repos, f *catalog.FixedCharge, cascade bool) (*catalog.FixedCharge, error) {
	now := s.d.Now()
	if err := r.FixedCharges.Discard(ctx, f.OrganizationID, f.ID, now); err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return nil, core.NewRuleError(codeFixedChargeAlreadyDeleted, "the fixed charge is already deleted")
		}
		return nil, fmt.Errorf("discard fixed charge %s: %w", f.ID, err)
	}
	out := f.Clone()
	out.DeletedAt, out.UpdatedAt = &now, now
	if !cascade {
		return out, nil
	}
	children, err := r.FixedCharges.ListChildren(ctx, f.OrganizationID, f.ID)
	if err != nil {
		return nil, fmt.Errorf("load overrides of fixed charge %s: %w", f.ID, err)
	}
	if len(children) > 0 {
		args := kernel.FixedChargeArgs{FixedChargeID: f.ID}
		if err := kernel.Enqueue(ctx, r, f.OrganizationID, kernel.JobDestroyFixedChargeChildren, args, "", now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fixedChargeCascadeAction is the kind of a change cascaded by a plan
// update to its override plans.
type fixedChargeCascadeAction string

// Actions of a plan update cascade (lago
// FixedCharges::CascadeChildPlanUpdateService actions).
const (
	fixedChargeCascadeCreate fixedChargeCascadeAction = "create"
	fixedChargeCascadeUpdate fixedChargeCascadeAction = "update"
)

// fixedChargeCascade is a fixed charge created or updated by a plan
// update, to cascade to the plan's override plans (an entry of lago's
// cascade_fixed_charges_payload).
type fixedChargeCascade struct {
	// Action is create or update.
	Action fixedChargeCascadeAction `json:"action"`
	// FixedChargeID is the parent fixed charge.
	FixedChargeID core.FixedChargeID `json:"fixed_charge_id"`
	// Input are the values the overrides take: on create, the input of
	// the parent with its code; on update, the parent's new code, model,
	// properties and units.
	Input FixedChargeInput `json:"input"`
	// OldChargeModel, OldProperties and OldUnits are the parent's pricing
	// before an update.
	OldChargeModel catalog.FixedChargeModel `json:"old_charge_model,omitempty"`
	// OldProperties are the parent's properties before an update.
	OldProperties catalog.Properties `json:"old_properties"`
	// OldUnits are the parent's units before an update.
	OldUnits money.Decimal `json:"old_units"`
}

// processPlanFixedCharges replaces the fixed charges of the plan p by in
// (Plans::UpdateService#process_fixed_charges and
// #sanitize_fixed_charges), cascading to the child plans with cascade.
// Once done, the active subscriptions of a plan with pay-in-advance fixed
// charges are billed for the units applied immediately.
//
// source: app/services/plans/update_service.rb:278-337, app/services/plans/chargeables_validation_service.rb:37-44
func (s *Service) processPlanFixedCharges(ctx context.Context, r port.Repos, p *catalog.Plan, in []FixedChargeInput, cascade bool) error {
	org := p.OrganizationID
	for _, f := range in {
		if f.AddOn.IsZero() {
			continue
		}
		if _, err := refs.AddOns(r, org).One(ctx, f.AddOn); err != nil {
			if errors.As(err, new(*core.NotFoundError)) {
				return core.NotFound("add_ons")
			}
			return err
		}
	}
	ts := s.d.Now()
	current, err := r.FixedCharges.ListByPlan(ctx, org, p.ID)
	if err != nil {
		return fmt.Errorf("load fixed charges of plan %s: %w", p.ID, err)
	}
	keep := map[core.FixedChargeID]bool{}
	var payload []fixedChargeCascade
	for _, f := range in {
		if !f.ID.IsZero() {
			keep[f.ID] = true
		}
		i := slices.IndexFunc(current, func(x *catalog.FixedCharge) bool { return x.ID == f.ID })
		if f.ID.IsZero() || i < 0 {
			f.ID, f.parent = core.FixedChargeID{}, core.FixedChargeID{}
			created, err := s.createFixedChargeAt(ctx, r, p, f, false, ts)
			if err != nil {
				return err
			}
			keep[created.ID] = true
			f.Code = core.Some(created.Code)
			payload = append(payload, fixedChargeCascade{Action: fixedChargeCascadeCreate, FixedChargeID: created.ID, Input: f})
			continue
		}
		old := current[i]
		updated, err := s.updateFixedCharge(ctx, r, old, f, fixedChargeUpdate{}, ts)
		if err != nil {
			return err
		}
		payload = append(payload, fixedChargeCascade{
			Action:         fixedChargeCascadeUpdate,
			FixedChargeID:  updated.ID,
			Input:          fixedChargeCascadeInput(updated, f.Code.Value, f.ApplyUnitsImmediately),
			OldChargeModel: old.ChargeModel,
			OldProperties:  old.Properties.Clone(),
			OldUnits:       old.Units,
		})
	}
	for _, f := range current {
		if keep[f.ID] {
			continue
		}
		if _, err := s.discardFixedCharge(ctx, r, f, cascade); err != nil {
			return err
		}
	}
	// source: app/services/plans/update_service.rb:310
	if err := s.billInAdvanceFixedCharges(ctx, r, p, ts); err != nil {
		return err
	}
	if !cascade {
		return nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode fixed charges cascade: %w", err)
	}
	args := kernel.CascadeFixedChargesArgs{PlanID: p.ID, Payload: b, Timestamp: ts}
	return kernel.Enqueue(ctx, r, org, kernel.JobCascadeFixedCharges, args, "", s.d.Now())
}

// fixedChargeCascadeInput returns the values the overrides of the updated
// parent f take: its model, properties and units, with code ("" keeps
// theirs) (lago FixedCharges::CascadeUpdatable#build_cascade_params).
//
// source: app/services/fixed_charges/cascade_updatable.rb:19-26
func fixedChargeCascadeInput(f *catalog.FixedCharge, code string, immediate bool) FixedChargeInput {
	in := FixedChargeInput{
		ChargeModel:           core.Some(f.ChargeModel),
		Properties:            core.Some(f.Properties.Clone()),
		Units:                 core.Some(f.Units),
		ApplyUnitsImmediately: immediate,
	}
	if code != "" {
		in.Code = core.Some(code)
	}
	return in
}

// billInAdvanceFixedCharges enqueues the billing at ts of the active
// subscriptions of p when p has a pay-in-advance fixed charge.
//
// source: app/services/plans/update_service.rb:326-328
func (s *Service) billInAdvanceFixedCharges(ctx context.Context, r port.Repos, p *catalog.Plan, ts time.Time) error {
	fcs, err := r.FixedCharges.ListByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return fmt.Errorf("load fixed charges of plan %s: %w", p.ID, err)
	}
	if !slices.ContainsFunc(fcs, func(f *catalog.FixedCharge) bool { return f.PayInAdvance }) {
		return nil
	}
	return s.enqueueAllInAdvanceFixedCharges(ctx, r, p, ts, core.FixedChargeID{})
}

// enqueueAllInAdvanceFixedCharges enqueues the billing at ts of the
// active subscriptions of p, except, with fc set, those with their own
// units of fc (lago Invoices::CreateAllPayInAdvanceFixedChargesJob, unique
// until executed).
func (s *Service) enqueueAllInAdvanceFixedCharges(ctx context.Context, r port.Repos, p *catalog.Plan, ts time.Time, fc core.FixedChargeID) error {
	args := kernel.AllInAdvanceFixedChargesArgs{PlanID: p.ID, Timestamp: ts, FixedChargeID: fc}
	key := fmt.Sprintf("fixed_advance_all:%s:%d:%s", p.ID, ts.UnixNano(), fc)
	return kernel.Enqueue(ctx, r, p.OrganizationID, kernel.JobCreateAllInAdvanceFixedCharges, args, key, s.d.Now())
}

// enqueueCreateFixedChargeChildren enqueues the copy of the new fixed
// charge f onto the override plans of its plan.
func (s *Service) enqueueCreateFixedChargeChildren(ctx context.Context, r port.Repos, f *catalog.FixedCharge, in FixedChargeInput) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode fixed charge payload: %w", err)
	}
	args := kernel.CreateFixedChargeChildrenArgs{FixedChargeID: f.ID, Payload: payload}
	return kernel.Enqueue(ctx, r, f.OrganizationID, kernel.JobCreateFixedChargeChildren, args, "", s.d.Now())
}

// enqueueUpdateFixedChargeChildren enqueues the cascade of the update of
// old into updated to its overrides (lago
// FixedCharges::CascadeUpdatable#trigger_cascade).
//
// source: app/services/fixed_charges/cascade_updatable.rb:9-17
func (s *Service) enqueueUpdateFixedChargeChildren(ctx context.Context, r port.Repos, old, updated *catalog.FixedCharge) error {
	args := kernel.UpdateFixedChargeChildrenArgs{
		FixedChargeID:  updated.ID,
		Code:           updated.Code,
		ChargeModel:    updated.ChargeModel,
		Properties:     updated.Properties.Clone(),
		Units:          updated.Units,
		OldChargeModel: old.ChargeModel,
		OldProperties:  old.Properties.Clone(),
		OldUnits:       old.Units,
	}
	return kernel.Enqueue(ctx, r, updated.OrganizationID, kernel.JobUpdateFixedChargeChildren, args, "", s.d.Now())
}

// fixedChargeGenerateCode returns the code of a new fixed charge of the
// add-on a on the plan p: the add-on's code when no parent fixed charge of
// the plan uses it, else the code with the highest numeric suffix in use
// plus one, at least 2.
//
// source: app/services/fixed_charges/generate_code_service.rb:22-35
func fixedChargeGenerateCode(ctx context.Context, r port.Repos, p *catalog.Plan, a *catalog.AddOn) (string, error) {
	fcs, err := r.FixedCharges.ListByPlan(ctx, p.OrganizationID, p.ID)
	if err != nil {
		return "", fmt.Errorf("load fixed charges of plan %s: %w", p.ID, err)
	}
	base := a.Code
	if !slices.ContainsFunc(fcs, func(f *catalog.FixedCharge) bool { return !f.IsChild() && f.Code == base }) {
		return base, nil
	}
	maxSuffix := 1
	for _, f := range fcs {
		rest, ok := strings.CutPrefix(f.Code, base)
		if f.IsChild() || !ok {
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

// validateFixedChargeRecord checks the fixed charge f: its invariants,
// then its properties with the model's validator, whose errors lago
// reports on properties (FixedCharge#validate_properties).
//
// source: app/models/fixed_charge.rb:36-47,95-99
func validateFixedChargeRecord(f *catalog.FixedCharge) error {
	var v core.Validator
	if err := f.Validate(); err != nil {
		var ve *core.ValidationError
		if !errors.As(err, &ve) {
			return err
		}
		for _, e := range ve.Errors {
			v.Add(e.Field, e.Code)
		}
	}
	chargePropertiesErrors(&v, "properties", catalog.ChargeModel(f.ChargeModel), f.Properties, pricing.ValidateOptions{})
	return v.Err()
}

// fixedChargeDefaultProperties returns the properties of a fixed charge of
// model created without any.
func fixedChargeDefaultProperties(model catalog.FixedChargeModel) catalog.Properties {
	return chargeDefaultProperties(catalog.ChargeModel(model))
}

// fixedChargeFilterProperties keeps the properties model uses
// (ChargeModels::FilterPropertiesService on a fixed charge).
//
// source: app/services/charge_models/filter_properties_service.rb:12-30
func fixedChargeFilterProperties(model catalog.FixedChargeModel, props catalog.Properties) (catalog.Properties, error) {
	raw, err := chargePropertiesMap(props)
	if err != nil {
		return catalog.Properties{}, err
	}
	return chargePropertiesOf(pricing.FilterProperties(pricing.FixedCharge, pricing.ChargeModel(model), raw))
}

// CreateFixedCharge creates the fixed charge in on the plan (lago
// Api::V1::Plans::FixedChargesController#create); with cascade, it is
// copied onto the plan's override plans with active or pending
// subscriptions. The plan is an original plan, by ID or code; the fixed
// charge needs a code.
//
// source: app/controllers/api/v1/plans/fixed_charges_controller.rb:41-56, app/models/fixed_charge.rb:38
func (s *Service) CreateFixedCharge(ctx context.Context, org core.OrganizationID, plan refs.Ref, in FixedChargeInput, cascade bool) (*catalog.FixedCharge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.FixedCharge, error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return nil, err
		}
		if in.Code.Value == "" {
			var v core.Validator
			v.Add("code", core.CodeRequired)
			return nil, v.Err()
		}
		in.ID, in.parent = core.FixedChargeID{}, core.FixedChargeID{}
		return s.createFixedCharge(ctx, r, p, in, cascade)
	})
}

// GetFixedCharge returns the fixed charge of the original plan, by ID or
// code.
//
// source: app/controllers/api/v1/plans/fixed_charges_controller.rb:30-39
func (s *Service) GetFixedCharge(ctx context.Context, org core.OrganizationID, plan, fixedCharge refs.Ref) (*catalog.FixedCharge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.FixedCharge, error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return nil, err
		}
		return fixedChargeOnPlan(ctx, r, org, p.ID, fixedCharge, true)
	})
}

// ListFixedCharges lists the fixed charges of the original plan, most
// recent first.
//
// source: app/controllers/api/v1/plans/fixed_charges_controller.rb:10-28
func (s *Service) ListFixedCharges(ctx context.Context, org core.OrganizationID, plan refs.Ref, page core.PageRequest) (core.Page[*catalog.FixedCharge], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.FixedCharge], error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return core.Page[*catalog.FixedCharge]{}, err
		}
		fcs, err := r.FixedCharges.ListByPlan(ctx, org, p.ID)
		if err != nil {
			return core.Page[*catalog.FixedCharge]{}, fmt.Errorf("load fixed charges of plan %s: %w", p.ID, err)
		}
		fcs = slices.DeleteFunc(fcs, (*catalog.FixedCharge).IsChild)
		slices.Reverse(fcs)
		return chargePage(fcs, func(f *catalog.FixedCharge) string { return f.ID.String() }, page)
	})
}

// UpdateFixedCharge changes the fixed charge of the original plan with in
// (lago FixedCharges::UpdateService): a change of units applied
// immediately bills the plan's active subscriptions for a pay-in-advance
// fixed charge. With cascade, the change goes on to the fixed charge's
// overrides on plans with active or pending subscriptions.
//
// source: app/controllers/api/v1/plans/fixed_charges_controller.rb:58-76, app/services/fixed_charges/cascade_updatable.rb:9-17
func (s *Service) UpdateFixedCharge(ctx context.Context, org core.OrganizationID, plan, fixedCharge refs.Ref, in FixedChargeInput, cascade bool) (*catalog.FixedCharge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.FixedCharge, error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return nil, err
		}
		old, err := fixedChargeOnPlan(ctx, r, org, p.ID, fixedCharge, true)
		if err != nil {
			return nil, err
		}
		updated, err := s.updateFixedCharge(ctx, r, old, in, fixedChargeUpdate{triggerBilling: true}, s.d.Now())
		if err != nil || !cascade {
			return updated, err
		}
		children, err := r.FixedCharges.ListChildren(ctx, org, updated.ID)
		if err != nil {
			return nil, fmt.Errorf("load overrides of fixed charge %s: %w", updated.ID, err)
		}
		if len(children) == 0 {
			return updated, nil
		}
		if err := s.enqueueUpdateFixedChargeChildren(ctx, r, old, updated); err != nil {
			return nil, err
		}
		return updated, nil
	})
}

// DeleteFixedCharge discards the fixed charge of the original plan; with
// cascade, its overrides on plans with active or pending subscriptions are
// discarded too. The discarded fixed charge is returned.
//
// source: app/controllers/api/v1/plans/fixed_charges_controller.rb:78-91
func (s *Service) DeleteFixedCharge(ctx context.Context, org core.OrganizationID, plan, fixedCharge refs.Ref, cascade bool) (*catalog.FixedCharge, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.FixedCharge, error) {
		p, err := chargePlan(ctx, r, org, plan)
		if err != nil {
			return nil, err
		}
		f, err := fixedChargeOnPlan(ctx, r, org, p.ID, fixedCharge, true)
		if err != nil {
			return nil, err
		}
		return s.discardFixedCharge(ctx, r, f, cascade)
	})
}

// fixedChargeOnPlan resolves the kept fixed charge of the plan, by ID or
// code; parents keeps original fixed charges only.
func fixedChargeOnPlan(ctx context.Context, r port.Repos, org core.OrganizationID, plan core.PlanID, ref refs.Ref, parents bool) (*catalog.FixedCharge, error) {
	var (
		f   *catalog.FixedCharge
		err error
	)
	switch {
	case ref.Code != "":
		f, err = r.FixedCharges.GetByCode(ctx, org, plan, ref.Code)
	case ref.ID != uuid.Nil:
		var id core.FixedChargeID
		if id, err = core.ParseID[core.FixedChargeID](ref.ID.String()); err == nil {
			f, err = r.FixedCharges.Get(ctx, org, id)
		}
	default:
		err = core.ErrNotFound
	}
	if err == nil && (f.PlanID != plan || f.DeletedAt != nil || (parents && f.IsChild())) {
		err = core.ErrNotFound
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound("fixed_charge")
	}
	if err != nil {
		return nil, fmt.Errorf("find fixed charge %s: %w", ref, err)
	}
	return f, nil
}
