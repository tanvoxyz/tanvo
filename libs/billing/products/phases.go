package products

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

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

// PhaseOwner is the card a ramp belongs to: a plan rate card, or a rate
// card applied to a contract. The phase operations ending in Of run on it
// inside the caller's transaction.
type PhaseOwner struct {
	// Lock is the owner's lock, taken before the guard (lago
	// applied_rate_card.with_lock).
	Lock port.LockKey
	// Guard fails when the owner's ramp cannot change; nil never fails.
	Guard func(ctx context.Context, r port.Repos) error
	// RateCardID is the applied rate card.
	RateCardID core.RateCardID
	// List returns the owner's kept phases by position.
	List func(ctx context.Context, r port.Repos) ([]*catalog.RatePhase, error)
	// Save makes phases the owner's kept phases, discarding the others at
	// at.
	Save func(ctx context.Context, r port.Repos, phases []*catalog.RatePhase, at time.Time) error
	// Own sets the owner on a new phase.
	Own func(p *catalog.RatePhase)
}

// planPhaseOwner returns the owner of the ramp of the plan rate card ref
// of the plan planRef; its guard is rate_phase plan_locked.
func planPhaseOwner(ctx context.Context, r port.Repos, org core.OrganizationID, planRef, ref refs.Ref) (PhaseOwner, error) {
	p, err := planRateCardOf(ctx, r, org, planRef, ref)
	if err != nil {
		return PhaseOwner{}, err
	}
	return PhaseOwner{
		Lock:       planRateCardLock(p.ID),
		Guard:      func(ctx context.Context, r port.Repos) error { return planGuard(ctx, r, org, p.PlanID, "rate_phase") },
		RateCardID: p.RateCardID,
		List: func(ctx context.Context, r port.Repos) ([]*catalog.RatePhase, error) {
			return r.PlanRateCards.ListPhases(ctx, org, p.ID)
		},
		Save: func(ctx context.Context, r port.Repos, phases []*catalog.RatePhase, at time.Time) error {
			return r.PlanRateCards.SavePhases(ctx, org, p.ID, phases, at)
		},
		Own: func(ph *catalog.RatePhase) { ph.PlanRateCardID = p.ID },
	}, nil
}

// ListPhases returns the phases of the plan rate card ref of the plan by
// position.
//
// source: app/controllers/api/v2/plan_rate_cards/rate_phases_controller.rb:7-11
func (s *Service) ListPhases(ctx context.Context, org core.OrganizationID, planRef, ref refs.Ref) ([]*catalog.RatePhase, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]*catalog.RatePhase, error) {
		o, err := planPhaseOwner(ctx, r, org, planRef, ref)
		if err != nil {
			return nil, err
		}
		return o.List(ctx, r)
	})
}

// CreatePhase inserts a phase into the ramp of the plan rate card ref
// ([Service.CreatePhaseOf]); a plan a contract uses is rate_phase
// plan_locked.
//
// source: app/services/rate_phases/create_service.rb:17-80
func (s *Service) CreatePhase(ctx context.Context, org core.OrganizationID, planRef, ref refs.Ref, in PhaseInput) (*catalog.RatePhase, error) {
	var out *catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := planPhaseOwner(ctx, r, org, planRef, ref)
		if err != nil {
			return err
		}
		out, err = s.CreatePhaseOf(ctx, r, org, o, in)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create rate phase: %w", err)
	}
	return out, nil
}

// CreatePhaseOf inserts a phase into the ramp of o; later phases shift
// down. The owner's guard runs under its lock; a position outside 1..n+1
// is position positions_must_be_contiguous; an indefinite phase before the
// last is billing_interval_cycle_count indefinite_phase_must_be_last, a
// definite last one last_phase_must_be_indefinite. Override errors are
// reported under rate_override.
//
// source: app/services/rate_phases/create_service.rb:17-80
func (s *Service) CreatePhaseOf(ctx context.Context, r port.Repos, org core.OrganizationID, o PhaseOwner, in PhaseInput) (*catalog.RatePhase, error) {
	existing, k, err := s.lockPhases(ctx, r, org, o)
	if err != nil {
		return nil, err
	}
	position := len(existing) + 1
	if in.Position != nil {
		position = *in.Position
	} else if n := len(existing); n > 0 && existing[n-1].BillingIntervalCycleCount == nil && in.BillingIntervalCycleCount != nil {
		position = existing[n-1].Position
	}
	if position < 1 || position > len(existing)+1 {
		return nil, fieldError("position", catalog.CodePositionsMustBeContiguous)
	}
	counts := make([]*int, 0, len(existing)+1)
	for _, e := range existing {
		counts = append(counts, e.BillingIntervalCycleCount)
	}
	counts = slices.Insert(counts, position-1, in.BillingIntervalCycleCount)
	if slices.Contains(counts[:len(counts)-1], nil) {
		return nil, fieldError("billing_interval_cycle_count", catalog.CodeIndefinitePhaseMustBeLast)
	}
	if counts[len(counts)-1] != nil {
		return nil, fieldError("billing_interval_cycle_count", catalog.CodeLastPhaseMustBeIndefinite)
	}
	phase := s.newPhase(org, o.Own, in.Code, position)
	phase.Name = in.Name
	phase.BillingIntervalCycleCount = in.BillingIntervalCycleCount
	if in.Override != nil {
		if phase.Override, err = buildOverride(k, *in.Override); err != nil {
			return nil, prefixed("rate_override", err)
		}
	}
	if err := validatePhase(phase, existing); err != nil {
		return nil, err
	}
	for _, e := range existing {
		if e.Position >= position {
			e.Position++
		}
	}
	phases := append(existing, phase)
	sortPhases(phases)
	if err := o.Save(ctx, r, phases, s.d.Now()); err != nil {
		return nil, conflict(err, "code")
	}
	return phase, nil
}

// UpdatePhase changes the phase phaseRef of the plan rate card ref
// ([Service.UpdatePhaseOf]); a plan a contract uses is rate_phase
// plan_locked.
//
// source: app/services/rate_phases/update_service.rb:15-80, :92-124
func (s *Service) UpdatePhase(ctx context.Context, org core.OrganizationID, planRef, ref, phaseRef refs.Ref, in PhaseUpdate) (*catalog.RatePhase, error) {
	var out *catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := planPhaseOwner(ctx, r, org, planRef, ref)
		if err != nil {
			return err
		}
		out, err = s.UpdatePhaseOf(ctx, r, org, o, phaseRef, in)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("update rate phase: %w", err)
	}
	return out, nil
}

// UpdatePhaseOf changes the phase phaseRef of the ramp of o. The owner's
// guard runs under its lock; making a phase other than the last
// indefinite is billing_interval_cycle_count
// indefinite_phase_must_be_last, giving the last one a count
// last_phase_must_be_indefinite; a position outside 1..n is position
// positions_must_be_contiguous, moving the last phase position
// indefinite_phase_must_be_last, moving another one last position
// last_phase_must_be_indefinite.
//
// source: app/services/rate_phases/update_service.rb:15-80, :92-124
func (s *Service) UpdatePhaseOf(ctx context.Context, r port.Repos, org core.OrganizationID, o PhaseOwner, phaseRef refs.Ref, in PhaseUpdate) (*catalog.RatePhase, error) {
	siblings, k, err := s.lockPhases(ctx, r, org, o)
	if err != nil {
		return nil, err
	}
	phase, err := phaseOf(siblings, phaseRef)
	if err != nil {
		return nil, err
	}
	n := len(siblings)
	last := siblings[n-1] == phase
	if in.BillingIntervalCycleCount.Set {
		if in.BillingIntervalCycleCount.Value == nil && !last {
			return nil, fieldError("billing_interval_cycle_count", catalog.CodeIndefinitePhaseMustBeLast)
		}
		if in.BillingIntervalCycleCount.Value != nil && last {
			return nil, fieldError("billing_interval_cycle_count", catalog.CodeLastPhaseMustBeIndefinite)
		}
	}
	if in.Position.Set {
		target := in.Position.Value
		switch {
		case target < 1 || target > n:
			return nil, fieldError("position", catalog.CodePositionsMustBeContiguous)
		case last && target != n:
			return nil, fieldError("position", catalog.CodeIndefinitePhaseMustBeLast)
		case !last && target == n:
			return nil, fieldError("position", catalog.CodeLastPhaseMustBeIndefinite)
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
				return nil, prefixed("rate_override", err)
			}
		}
	}
	if err := validatePhase(phase, siblings); err != nil {
		return nil, err
	}
	phase.UpdatedAt = s.d.Now()
	sortPhases(siblings)
	if err := o.Save(ctx, r, siblings, s.d.Now()); err != nil {
		return nil, conflict(err, "code")
	}
	return phase, nil
}

// DeletePhase removes the phase phaseRef of the plan rate card ref
// ([Service.DeletePhaseOf]); a plan a contract uses is rate_phase
// plan_locked.
//
// source: app/services/rate_phases/destroy_service.rb:15-46
func (s *Service) DeletePhase(ctx context.Context, org core.OrganizationID, planRef, ref, phaseRef refs.Ref) (*catalog.RatePhase, error) {
	var out *catalog.RatePhase
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		o, err := planPhaseOwner(ctx, r, org, planRef, ref)
		if err != nil {
			return err
		}
		out, err = s.DeletePhaseOf(ctx, r, org, o, phaseRef)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("delete rate phase: %w", err)
	}
	return out, nil
}

// DeletePhaseOf removes the phase phaseRef of the ramp of o; later phases
// shift up. The owner's guard runs under its lock; the indefinite last
// phase is rate_phase indefinite_phase_not_deletable, a single phase
// rate_phase cannot_delete_last_phase.
//
// source: app/services/rate_phases/destroy_service.rb:15-46
func (s *Service) DeletePhaseOf(ctx context.Context, r port.Repos, org core.OrganizationID, o PhaseOwner, phaseRef refs.Ref) (*catalog.RatePhase, error) {
	siblings, _, err := s.lockPhases(ctx, r, org, o)
	if err != nil {
		return nil, err
	}
	phase, err := phaseOf(siblings, phaseRef)
	if err != nil {
		return nil, err
	}
	if siblings[len(siblings)-1] == phase && phase.BillingIntervalCycleCount == nil {
		return nil, fieldError("rate_phase", catalog.CodeIndefinitePhaseNotDeletable)
	}
	if len(siblings) == 1 {
		return nil, fieldError("rate_phase", catalog.CodeCannotDeleteLastPhase)
	}
	rest := slices.DeleteFunc(slices.Clone(siblings), func(e *catalog.RatePhase) bool { return e == phase })
	for _, e := range rest {
		if e.Position > phase.Position {
			e.Position--
		}
	}
	now := s.d.Now()
	if err := o.Save(ctx, r, rest, now); err != nil {
		return nil, fmt.Errorf("save phases: %w", err)
	}
	phase.DeletedAt = &now
	return phase, nil
}

// lockPhases takes the lock of o, checks its guard and returns its phases
// by position with its card.
func (s *Service) lockPhases(ctx context.Context, r port.Repos, org core.OrganizationID, o PhaseOwner) ([]*catalog.RatePhase, card, error) {
	if err := lockKey(ctx, r, org, o.Lock); err != nil {
		return nil, card{}, err
	}
	if o.Guard != nil {
		if err := o.Guard(ctx, r); err != nil {
			return nil, card{}, err
		}
	}
	phases, err := o.List(ctx, r)
	if err != nil {
		return nil, card{}, fmt.Errorf("list phases: %w", err)
	}
	k, err := s.cardByID(ctx, r, org, o.RateCardID)
	if err != nil {
		return nil, card{}, err
	}
	return phases, k, nil
}

// cardByID loads the kept rate card id with its product, filter and
// metric.
func (s *Service) cardByID(ctx context.Context, r port.Repos, org core.OrganizationID, id core.RateCardID) (card, error) {
	c, err := getRateCard(ctx, r, org, id)
	if err != nil {
		return card{}, fmt.Errorf("load rate card: %w", err)
	}
	return cardOf(ctx, r, c)
}

// BuildPhasesOf builds a whole ramp for a new card applying the rate card
// rateCard, owned through own ([Service.buildPhases]); nil in builds the
// single indefinite phase coded "default".
//
// source: app/services/rate_phases/replace_service.rb:17-48, app/services/contract_rate_cards/create_service.rb:44-48
func (s *Service) BuildPhasesOf(ctx context.Context, r port.Repos, org core.OrganizationID, rateCard core.RateCardID, own func(*catalog.RatePhase), in *[]PhaseInput) ([]*catalog.RatePhase, error) {
	if in == nil {
		return []*catalog.RatePhase{s.newPhase(org, own, "default", 1)}, nil
	}
	k, err := s.cardByID(ctx, r, org, rateCard)
	if err != nil {
		return nil, err
	}
	return s.buildPhases(k, own, *in)
}

// buildPhases builds a whole ramp for a new card: one phase per input,
// positions 1..n, the last one only indefinite. Errors of the sequence are
// reported on rate_phases, those of a phase under rate_phases. (as a
// card's nested rates are under rates.).
//
// source: app/services/rate_phases/replace_service.rb:17-48, :78-101
func (s *Service) buildPhases(k card, own func(*catalog.RatePhase), in []PhaseInput) ([]*catalog.RatePhase, error) {
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
		phase := s.newPhase(k.c.OrganizationID, own, in[i].Code, position(i))
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

// newPhase returns a phase of org at position, owned through own.
func (s *Service) newPhase(org core.OrganizationID, own func(*catalog.RatePhase), code string, position int) *catalog.RatePhase {
	now := s.d.Now()
	p := &catalog.RatePhase{
		ID:             core.NewID[core.RatePhaseID](s.d.IDs),
		OrganizationID: org,
		Code:           code,
		Position:       position,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	own(p)
	return p
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
