package products

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// RateOverrideInput is the prices of a phase replacing its card's rates.
// The card's structural settings are never overridden: the input has no
// field for them.
type RateOverrideInput struct {
	// RateModel is the pricing model.
	RateModel catalog.ChargeModel
	// Properties are the prices; tiers name only their upper bound.
	Properties catalog.Properties
	// MinAmount is the minimum per billing interval in minor units; 0 when
	// nil.
	MinAmount *int64
	// BillingIntervalCount overrides the rate's interval count; nil keeps
	// it.
	BillingIntervalCount *int
	// BillingIntervalUnit overrides the rate's interval unit; empty keeps
	// it.
	BillingIntervalUnit catalog.IntervalUnit
	// PricingUnitConversionRate converts the card's pricing unit; required
	// when the card has one.
	PricingUnitConversionRate *money.Decimal
}

// PhaseInput is a phase of a plan rate card's ramp.
type PhaseInput struct {
	// Code is unique among the plan rate card's phases.
	Code string
	// Position is the phase's place from 1; nil appends it (before an
	// indefinite last phase when the new one is definite).
	Position *int
	// Name is the display name.
	Name *string
	// BillingIntervalCycleCount is the number of billing intervals; nil
	// for the indefinite last phase.
	BillingIntervalCycleCount *int
	// Override is the phase's prices; nil bills the card's rates.
	Override *RateOverrideInput
}

// PhaseUpdate is a partial update of a phase: only the set fields change.
type PhaseUpdate struct {
	// Code is the unique code.
	Code core.Optional[string]
	// Position moves the phase; the indefinite last phase stays last.
	Position core.Optional[int]
	// Name is the display name.
	Name core.Optional[*string]
	// BillingIntervalCycleCount is the number of billing intervals; nil
	// only on the last phase.
	BillingIntervalCycleCount core.Optional[*int]
	// Override replaces the phase's prices; nil clears them.
	Override core.Optional[*RateOverrideInput]
}

// ListPhases returns the phases of the plan rate card ref of the plan by
// position.
//
// source: app/controllers/api/v2/plan_rate_cards/rate_phases_controller.rb:7-11
func (s *Service) ListPhases(ctx context.Context, org core.OrganizationID, planRef, ref refs.Ref) ([]*catalog.RatePhase, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]*catalog.RatePhase, error) {
		p, err := planRateCardOf(ctx, r, org, planRef, ref)
		if err != nil {
			return nil, err
		}
		return r.PlanRateCards.ListPhases(ctx, org, p.ID)
	})
}

// CreatePhase inserts a phase into the ramp of the plan rate card ref;
// later phases shift down. A plan a contract uses is rate_phase
// plan_locked; a position outside 1..n+1 is position
// positions_must_be_contiguous; an indefinite phase before the last is
// billing_interval_cycle_count indefinite_phase_must_be_last, a definite
// last one last_phase_must_be_indefinite. Override errors are reported
// under rate_override.
//
// source: app/services/rate_phases/create_service.rb:17-80
func (s *Service) CreatePhase(ctx context.Context, org core.OrganizationID, planRef, ref refs.Ref, in PhaseInput) (*catalog.RatePhase, error) {
	var out *catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, existing, k, err := s.lockPhases(ctx, r, org, planRef, ref)
		if err != nil {
			return err
		}
		position := len(existing) + 1
		if in.Position != nil {
			position = *in.Position
		} else if n := len(existing); n > 0 && existing[n-1].BillingIntervalCycleCount == nil && in.BillingIntervalCycleCount != nil {
			position = existing[n-1].Position
		}
		if position < 1 || position > len(existing)+1 {
			return fieldError("position", catalog.CodePositionsMustBeContiguous)
		}
		counts := make([]*int, 0, len(existing)+1)
		for _, e := range existing {
			counts = append(counts, e.BillingIntervalCycleCount)
		}
		counts = slices.Insert(counts, position-1, in.BillingIntervalCycleCount)
		if slices.Contains(counts[:len(counts)-1], nil) {
			return fieldError("billing_interval_cycle_count", catalog.CodeIndefinitePhaseMustBeLast)
		}
		if counts[len(counts)-1] != nil {
			return fieldError("billing_interval_cycle_count", catalog.CodeLastPhaseMustBeIndefinite)
		}
		phase := s.newPhase(org, p.ID, in.Code, position)
		phase.Name = in.Name
		phase.BillingIntervalCycleCount = in.BillingIntervalCycleCount
		if in.Override != nil {
			if phase.Override, err = buildOverride(k, *in.Override); err != nil {
				return prefixed("rate_override", err)
			}
		}
		if err := validatePhase(phase, existing); err != nil {
			return err
		}
		for _, e := range existing {
			if e.Position >= position {
				e.Position++
			}
		}
		phases := append(existing, phase)
		sortPhases(phases)
		if err := r.PlanRateCards.SavePhases(ctx, org, p.ID, phases, s.d.Now()); err != nil {
			return conflict(err, "code")
		}
		out = phase
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create rate phase: %w", err)
	}
	return out, nil
}

// UpdatePhase changes the phase phaseRef of the plan rate card ref. A
// plan a contract uses is rate_phase plan_locked; making a phase
// other than the last indefinite is billing_interval_cycle_count
// indefinite_phase_must_be_last, giving the last one a count
// last_phase_must_be_indefinite; a position outside 1..n is position
// positions_must_be_contiguous, moving the last phase position
// indefinite_phase_must_be_last, moving another one last position
// last_phase_must_be_indefinite.
//
// source: app/services/rate_phases/update_service.rb:15-80, :92-124
func (s *Service) UpdatePhase(ctx context.Context, org core.OrganizationID, planRef, ref, phaseRef refs.Ref, in PhaseUpdate) (*catalog.RatePhase, error) {
	var out *catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, siblings, k, err := s.lockPhases(ctx, r, org, planRef, ref)
		if err != nil {
			return err
		}
		phase, err := phaseOf(siblings, phaseRef)
		if err != nil {
			return err
		}
		n := len(siblings)
		last := siblings[n-1] == phase
		if in.BillingIntervalCycleCount.Set {
			if in.BillingIntervalCycleCount.Value == nil && !last {
				return fieldError("billing_interval_cycle_count", catalog.CodeIndefinitePhaseMustBeLast)
			}
			if in.BillingIntervalCycleCount.Value != nil && last {
				return fieldError("billing_interval_cycle_count", catalog.CodeLastPhaseMustBeIndefinite)
			}
		}
		if in.Position.Set {
			target := in.Position.Value
			switch {
			case target < 1 || target > n:
				return fieldError("position", catalog.CodePositionsMustBeContiguous)
			case last && target != n:
				return fieldError("position", catalog.CodeIndefinitePhaseMustBeLast)
			case !last && target == n:
				return fieldError("position", catalog.CodeLastPhaseMustBeIndefinite)
			}
			movePhase(siblings, phase, target)
		}
		in.Name.Apply(&phase.Name)
		in.Code.Apply(&phase.Code)
		in.BillingIntervalCycleCount.Apply(&phase.BillingIntervalCycleCount)
		if in.Override.Set {
			phase.Override = nil
			if in.Override.Value != nil {
				if phase.Override, err = buildOverride(k, *in.Override.Value); err != nil {
					return prefixed("rate_override", err)
				}
			}
		}
		if err := validatePhase(phase, siblings); err != nil {
			return err
		}
		phase.UpdatedAt = s.d.Now()
		sortPhases(siblings)
		if err := r.PlanRateCards.SavePhases(ctx, org, p.ID, siblings, s.d.Now()); err != nil {
			return conflict(err, "code")
		}
		out = phase
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update rate phase: %w", err)
	}
	return out, nil
}

// DeletePhase removes the phase phaseRef of the plan rate card ref; later
// phases shift up. A plan a contract uses is rate_phase plan_locked;
// the indefinite last phase is rate_phase indefinite_phase_not_deletable,
// a single phase rate_phase cannot_delete_last_phase.
//
// source: app/services/rate_phases/destroy_service.rb:15-46
func (s *Service) DeletePhase(ctx context.Context, org core.OrganizationID, planRef, ref, phaseRef refs.Ref) (*catalog.RatePhase, error) {
	var out *catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, siblings, _, err := s.lockPhases(ctx, r, org, planRef, ref)
		if err != nil {
			return err
		}
		phase, err := phaseOf(siblings, phaseRef)
		if err != nil {
			return err
		}
		if siblings[len(siblings)-1] == phase && phase.BillingIntervalCycleCount == nil {
			return fieldError("rate_phase", catalog.CodeIndefinitePhaseNotDeletable)
		}
		if len(siblings) == 1 {
			return fieldError("rate_phase", catalog.CodeCannotDeleteLastPhase)
		}
		rest := slices.DeleteFunc(slices.Clone(siblings), func(e *catalog.RatePhase) bool { return e == phase })
		for _, e := range rest {
			if e.Position > phase.Position {
				e.Position--
			}
		}
		now := s.d.Now()
		if err := r.PlanRateCards.SavePhases(ctx, org, p.ID, rest, now); err != nil {
			return fmt.Errorf("save phases: %w", err)
		}
		phase.DeletedAt = &now
		out = phase
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("delete rate phase: %w", err)
	}
	return out, nil
}

// lockPhases resolves the plan rate card ref, takes its lock, checks the
// edit guard (rate_phase plan_locked) and returns its phases by position
// with its card.
func (s *Service) lockPhases(ctx context.Context, r port.Repos, org core.OrganizationID, planRef, ref refs.Ref) (*catalog.PlanRateCard, []*catalog.RatePhase, card, error) {
	p, err := planRateCardOf(ctx, r, org, planRef, ref)
	if err != nil {
		return nil, nil, card{}, err
	}
	if err := lockKey(ctx, r, org, planRateCardLock(p.ID)); err != nil {
		return nil, nil, card{}, err
	}
	if err := planGuard(ctx, r, org, p.PlanID, "rate_phase"); err != nil {
		return nil, nil, card{}, err
	}
	phases, err := r.PlanRateCards.ListPhases(ctx, org, p.ID)
	if err != nil {
		return nil, nil, card{}, fmt.Errorf("list phases: %w", err)
	}
	c, err := getRateCard(ctx, r, org, p.RateCardID)
	if err != nil {
		return nil, nil, card{}, fmt.Errorf("load rate card: %w", err)
	}
	k, err := cardOf(ctx, r, c)
	if err != nil {
		return nil, nil, card{}, err
	}
	return p, phases, k, nil
}

// buildPhases builds a whole ramp for a new plan rate card: one phase per
// input, positions 1..n, the last one only indefinite. Errors of the
// sequence are reported on rate_phases, those of a phase under
// rate_phases. (as a card's nested rates are under rates.).
//
// source: app/services/rate_phases/replace_service.rb:17-48, :78-101
func (s *Service) buildPhases(k card, id core.PlanRateCardID, in []PhaseInput) ([]*catalog.RatePhase, error) {
	if len(in) == 0 {
		return nil, fieldError("rate_phases", core.CodeRequired)
	}
	order := make([]int, len(in))
	for i := range order {
		order[i] = i
	}
	position := func(i int) int {
		if in[i].Position == nil {
			return 0
		}
		return *in[i].Position
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(position(a), position(b)) })
	for n, i := range order {
		if position(i) != n+1 {
			return nil, fieldError("rate_phases", catalog.CodePositionsMustBeContiguous)
		}
	}
	for _, i := range order[:len(order)-1] {
		if in[i].BillingIntervalCycleCount == nil {
			return nil, fieldError("rate_phases", catalog.CodeIndefinitePhaseMustBeLast)
		}
	}
	if in[order[len(order)-1]].BillingIntervalCycleCount != nil {
		return nil, fieldError("rate_phases", catalog.CodeLastPhaseMustBeIndefinite)
	}
	phases := make([]*catalog.RatePhase, 0, len(in))
	for _, i := range order {
		phase := s.newPhase(k.c.OrganizationID, id, in[i].Code, position(i))
		phase.Name = in[i].Name
		phase.BillingIntervalCycleCount = in[i].BillingIntervalCycleCount
		if in[i].Override != nil {
			o, err := buildOverride(k, *in[i].Override)
			if err != nil {
				return nil, prefixed("rate_phases.rate_override", err)
			}
			phase.Override = o
		}
		if err := validatePhase(phase, phases); err != nil {
			return nil, prefixed("rate_phases", err)
		}
		phases = append(phases, phase)
	}
	return phases, nil
}

// newPhase returns a phase of the plan rate card id of org at position.
func (s *Service) newPhase(org core.OrganizationID, id core.PlanRateCardID, code string, position int) *catalog.RatePhase {
	now := s.d.Now()
	return &catalog.RatePhase{
		ID:             core.NewID[core.RatePhaseID](s.d.IDs),
		OrganizationID: org,
		PlanRateCardID: id,
		Code:           code,
		Position:       position,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

// buildOverride checks and normalizes the override in of a phase of the
// card k: a missing conversion rate, an incompatible model and a minimum
// on an advance card first, then the tiers, then the record's rules and
// the properties with the model's validator.
//
// source: app/services/rate_overrides/create_service.rb:20-60, app/models/rate_override.rb:24-60
func buildOverride(k card, in RateOverrideInput) (*catalog.RateOverride, error) {
	o := &catalog.RateOverride{
		RateModel:                 in.RateModel,
		Properties:                in.Properties,
		BillingIntervalCount:      in.BillingIntervalCount,
		BillingIntervalUnit:       in.BillingIntervalUnit,
		PricingUnitConversionRate: in.PricingUnitConversionRate,
	}
	if in.MinAmount != nil {
		o.MinAmount = *in.MinAmount
	}
	if !k.c.PricingUnitID.IsZero() && o.PricingUnitConversionRate == nil {
		return nil, fieldError("pricing_unit_conversion_rate", core.CodeRequired)
	}
	if o.RateModel.Valid() {
		if code := catalog.RateModelError(o.RateModel, k.c, k.p, k.m); code != "" {
			return nil, fieldError("rate_model", code)
		}
	}
	if o.MinAmount > 0 && k.c.BillingTiming == catalog.BillingTimingAdvance {
		return nil, fieldError("min_amount", catalog.CodeNotAllowedForBillingTiming)
	}
	props, err := NormalizeRanges(o.Properties)
	if err != nil {
		return nil, err
	}
	o.Properties = props
	var v core.Validator
	verr := catalog.ValidateRateOverride(o, k.c, k.p, k.m)
	v.Merge("", verr)
	if !hasField(verr, "rate_model") {
		rateProperties(&v, "properties", o.RateModel, o.Properties, k.m)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return o, nil
}

// validatePhase checks the phase's own fields and its code's uniqueness
// among others.
//
// source: app/models/rate_phase.rb:19-29
func validatePhase(phase *catalog.RatePhase, others []*catalog.RatePhase) error {
	var v core.Validator
	v.Merge("", phase.Validate())
	if phase.Code != "" && slices.ContainsFunc(others, func(o *catalog.RatePhase) bool { return o.ID != phase.ID && o.Code == phase.Code }) {
		v.Add("code", core.CodeAlreadyExists)
	}
	return v.Err()
}

// movePhase moves phase to target among siblings, shifting the phases in
// between.
//
// source: app/services/rate_phases/update_service.rb:111-124
func movePhase(siblings []*catalog.RatePhase, phase *catalog.RatePhase, target int) {
	current := phase.Position
	for _, e := range siblings {
		switch {
		case e == phase:
		case target < current && e.Position >= target && e.Position < current:
			e.Position++
		case target > current && e.Position > current && e.Position <= target:
			e.Position--
		}
	}
	phase.Position = target
}

// sortPhases orders phases by position.
func sortPhases(phases []*catalog.RatePhase) {
	slices.SortStableFunc(phases, func(a, b *catalog.RatePhase) int { return cmp.Compare(a.Position, b.Position) })
}

// phaseOf returns the phase ref references among phases: by ID or code.
// A missing phase is core.NotFound("rate_phase").
func phaseOf(phases []*catalog.RatePhase, ref refs.Ref) (*catalog.RatePhase, error) {
	i := slices.IndexFunc(phases, func(p *catalog.RatePhase) bool {
		if ref.Code != "" {
			return p.Code == ref.Code
		}
		return p.ID.UUID() == ref.ID
	})
	if i < 0 || ref.IsZero() {
		return nil, core.NotFound(ResourceRatePhase)
	}
	return phases[i], nil
}
