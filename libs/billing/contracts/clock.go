package contracts

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period/schedule"
)

// cardSchedule is a contract rate card's billing schedule with the rows
// its segments name.
type cardSchedule struct {
	*schedule.Schedule
	// rateCard is the applied rate card.
	rateCard *catalog.RateCard
	// rates are the card's rates by ID.
	rates map[string]*catalog.Rate
	// phases are the card's phases with an override, by ID.
	phases map[string]*catalog.RatePhase
}

// defaultLifecycle sets the lifecycle a card of contract c starts with:
// the contract's start day in loc as its effective date, anchor (else
// the contract's effective anchor) as its billing anchor, and its clock
// at the contract's start (lago Contract#default_rate_card_lifecycle).
//
// source: app/models/contract.rb:126-132
func defaultLifecycle(card *subscription.RateCard, c *subscription.Subscription, loc *time.Location, anchor *core.Date) {
	card.EffectiveDate = core.DateOf(c.SubscriptionAt, loc)
	if anchor != nil {
		card.BillingAnchorDate = *anchor
	} else {
		card.BillingAnchorDate = c.EffectiveBillingAnchorDate(loc)
	}
	next := c.SubscriptionAt
	card.NextBillingAt = &next
}

// buildSchedule builds the billing schedule of card on contract c in loc,
// resuming at its latest stored cycle (lago
// Billing::RateCards::BuildScheduleService). A card without rates fails
// with core.NotFound("rate"), a schedule that cannot be built with the
// rule invalid_billing_schedule.
//
// source: app/services/billing/rate_cards/build_schedule_service.rb:15-41
func buildSchedule(ctx context.Context, r port.Repos, c *subscription.Subscription, loc *time.Location, card *subscription.RateCard) (*cardSchedule, error) {
	return buildScheduleFrom(ctx, r, c, loc, card, true)
}

// buildScheduleFrom is [buildSchedule], resuming at the card's latest
// stored cycle only when resume is set (lago
// resume_from_billing_segments).
//
// source: app/services/billing/rate_cards/build_schedule_service.rb:8-13, :32
func buildScheduleFrom(ctx context.Context, r port.Repos, c *subscription.Subscription, loc *time.Location, card *subscription.RateCard, resume bool) (*cardSchedule, error) {
	org := card.OrganizationID
	rc, err := r.RateCards.Get(ctx, org, card.RateCardID)
	if err != nil {
		return nil, fmt.Errorf("load rate card %s: %w", card.RateCardID, err)
	}
	rates, err := r.Rates.ListByRateCard(ctx, org, card.RateCardID)
	if err != nil {
		return nil, fmt.Errorf("list rates of %s: %w", card.RateCardID, err)
	}
	if len(rates) == 0 {
		return nil, core.NotFound(ResourceRate)
	}
	phases, err := r.ContractRateCards.ListPhases(ctx, org, card.ID)
	if err != nil {
		return nil, fmt.Errorf("list phases of %s: %w", card.ID, err)
	}
	var resumeAt *time.Time
	if resume {
		if resumeAt, err = r.BillingSegments.MaxCycleStartedAt(ctx, org, card.ID); err != nil {
			return nil, fmt.Errorf("latest cycle of %s: %w", card.ID, err)
		}
	}
	cs := &cardSchedule{rateCard: rc, rates: map[string]*catalog.Rate{}, phases: map[string]*catalog.RatePhase{}}
	p := schedule.Params{
		StartsAt: card.EffectiveDate.Midnight(loc),
		EndsAt:   c.EndingAt,
		Anchor:   schedule.Date{Year: card.BillingAnchorDate.Year, Month: card.BillingAnchorDate.Month, Day: card.BillingAnchorDate.Day},
		Location: loc,
		ResumeAt: resumeAt,
	}
	for _, rt := range slices.Backward(rates) {
		cs.rates[rt.ID.String()] = rt
		p.Rates = append(p.Rates, &schedule.Rate{
			ID: rt.ID.String(), EffectiveFrom: rt.EffectiveFrom,
			IntervalCount: rt.BillingIntervalCount, IntervalUnit: schedule.Unit(rt.BillingIntervalUnit),
		})
	}
	if p.Terms, err = schedule.NewTerms(schedule.Timing(rc.BillingTiming), rc.Proration); err != nil {
		return nil, invalidSchedule(err)
	}
	for _, ph := range phases {
		var o *schedule.Override
		if ph.Override != nil {
			cs.phases[ph.ID.String()] = ph
			o = &schedule.Override{ID: ph.ID.String(), IntervalCount: ph.Override.BillingIntervalCount, IntervalUnit: schedule.Unit(ph.Override.BillingIntervalUnit)}
		}
		sp, err := schedule.NewPhase(ph.Code, ph.BillingIntervalCycleCount, o)
		if err != nil {
			return nil, invalidSchedule(err)
		}
		p.Phases = append(p.Phases, sp)
	}
	if len(p.Phases) == 0 || !p.Phases[len(p.Phases)-1].Unbounded() {
		p.Phases = append(p.Phases, schedule.DefaultPhase())
	}
	if cs.Schedule, err = schedule.New(p); err != nil {
		return nil, invalidSchedule(err)
	}
	return cs, nil
}

// invalidSchedule is the failure of a schedule that cannot be built (lago
// service_failure invalid_billing_schedule).
func invalidSchedule(err error) error {
	if !errors.Is(err, schedule.ErrInvalid) {
		return err
	}
	return core.NewRuleError(subscription.CodeInvalidBillingSchedule, err.Error())
}

// buildFailed reports whether err is a schedule that could not be built
// for a domain reason, as opposed to a failed read.
func buildFailed(err error) bool {
	var nf *core.NotFoundError
	var re *core.RuleError
	return errors.As(err, &nf) || (errors.As(err, &re) && re.Code == subscription.CodeInvalidBillingSchedule)
}

// seedLifecycle resets card's lifecycle to the contract's defaults with
// anchor as its own billing anchor (nil inherits the contract's), then
// resets its clock at now (lago ContractRateCards::SeedLifecycleService).
//
// source: app/services/contract_rate_cards/seed_lifecycle_service.rb:13-22
func (s *Service) seedLifecycle(ctx context.Context, r port.Repos, c *subscription.Subscription, loc *time.Location, card *subscription.RateCard, anchor *core.Date, now time.Time) error {
	defaultLifecycle(card, c, loc, anchor)
	if err := s.saveCard(ctx, r, card, now); err != nil {
		return err
	}
	return s.resetClock(ctx, r, c, loc, card, now)
}

// resetClock moves card's clock to the billing instant of the segment
// serving ts, or the next one; a card whose schedule cannot be built
// keeps its clock (lago ContractRateCards::ResetBillingClockService).
//
// source: app/services/contract_rate_cards/reset_billing_clock_service.rb:13-22
func (s *Service) resetClock(ctx context.Context, r port.Repos, c *subscription.Subscription, loc *time.Location, card *subscription.RateCard, ts time.Time) error {
	sch, err := buildSchedule(ctx, r, c, loc, card)
	if buildFailed(err) {
		return nil
	}
	if err != nil {
		return err
	}
	next, ok := sch.BillingAtCovering(ts)
	if !ok {
		return nil
	}
	card.NextBillingAt = &next
	return s.saveCard(ctx, r, card, s.d.Now())
}

// advanceClock moves card's clock to the next billing instant after ts
// when that is later than the clock, or clears it once billing ended
// (lago ContractRateCards::AdvanceBillingClockService).
//
// source: app/services/contract_rate_cards/advance_billing_clock_service.rb:13-21
func (s *Service) advanceClock(ctx context.Context, r port.Repos, card *subscription.RateCard, sch *cardSchedule, ts time.Time) error {
	next, ok := sch.NextBillingAt(ts)
	switch {
	case !ok:
		card.NextBillingAt = nil
	case card.NextBillingAt == nil || next.After(*card.NextBillingAt):
		card.NextBillingAt = &next
	default:
		return nil
	}
	return s.saveCard(ctx, r, card, s.d.Now())
}

// saveCard validates and stores card.
func (s *Service) saveCard(ctx context.Context, r port.Repos, card *subscription.RateCard, now time.Time) error {
	if err := card.Validate(); err != nil {
		return err
	}
	card.UpdatedAt = now
	if err := r.ContractRateCards.Update(ctx, card); err != nil {
		return fmt.Errorf("update contract rate card %s: %w", card.ID, err)
	}
	return nil
}

// materialize applies the rate cards of contract c's plan to it: one card
// per plan rate card with its units, the contract's default lifecycle and
// a copy of its phases (overrides included), each seeded (lago
// Contracts::MaterializeRateCardsService).
//
// source: app/services/contracts/materialize_rate_cards_service.rb:15-53
func (s *Service) materialize(ctx context.Context, r port.Repos, c *subscription.Subscription, loc *time.Location, now time.Time) error {
	if c.PlanID.IsZero() {
		return nil
	}
	org := c.OrganizationID
	plan, err := kernel.All(func(page core.PageRequest) (core.Page[*catalog.PlanRateCard], error) {
		return r.PlanRateCards.List(ctx, org, catalog.PlanRateCardQuery{PlanID: c.PlanID}, page)
	})
	if err != nil {
		return fmt.Errorf("list rate cards of plan %s: %w", c.PlanID, err)
	}
	for _, prc := range plan {
		// Locked so that a concurrent phase edit of the plan waits for the
		// contract and then finds the plan locked.
		if err := r.Locks.Lock(ctx, org, port.LockKey{Scope: "plan_rate_card", ID: prc.ID.UUID()}); err != nil {
			return fmt.Errorf("lock plan rate card %s: %w", prc.ID, err)
		}
		phases, err := r.PlanRateCards.ListPhases(ctx, org, prc.ID)
		if err != nil {
			return fmt.Errorf("list phases of %s: %w", prc.ID, err)
		}
		card := &subscription.RateCard{
			ID: core.NewID[core.SubscriptionRateCardID](s.d.IDs), OrganizationID: org, SubscriptionID: c.ID,
			RateCardID: prc.RateCardID, Units: prc.Units, CreatedAt: now, UpdatedAt: now,
		}
		defaultLifecycle(card, c, loc, nil)
		copies := make([]*catalog.RatePhase, len(phases))
		for i, ph := range phases {
			cp := *ph
			cp.ID = core.NewID[core.RatePhaseID](s.d.IDs)
			cp.PlanRateCardID = core.PlanRateCardID{}
			cp.SubscriptionRateCardID = card.ID
			cp.CreatedAt, cp.UpdatedAt, cp.DeletedAt = now, now, nil
			if ph.Override != nil {
				o := *ph.Override
				cp.Override = &o
			}
			copies[i] = &cp
		}
		if err := s.createCard(ctx, r, card, copies); err != nil {
			return err
		}
		if err := s.seedLifecycle(ctx, r, c, loc, card, nil, now); err != nil {
			return err
		}
	}
	return nil
}

// createCard validates and stores card with its phases.
func (s *Service) createCard(ctx context.Context, r port.Repos, card *subscription.RateCard, phases []*catalog.RatePhase) error {
	if err := card.Validate(); err != nil {
		return err
	}
	if err := r.ContractRateCards.Create(ctx, card, phases); err != nil {
		return fmt.Errorf("create contract rate card: %w", err)
	}
	return nil
}
