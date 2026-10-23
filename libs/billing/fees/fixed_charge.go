package fees

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// FixedChargeFee returns the fee of a fixed charge for the fixed charges
// period of sc (domain §22.8): the units in force at the end of the
// period, priced by the fixed charge's model; a prorated fixed charge
// prices the units weighted by the days each count was in force. A fixed
// charge paid in advance is billed for the period starting at the billing
// instant, less what a replaced subscription already paid for the same
// add-on. The fee's ChargesFrom, ChargesTo and ChargesDurationDays hold the
// fixed charges period. It returns nil when the fixed charge is already
// billed, or when its fee would have no units nor amount and opts.Recurring
// is not set.
func (s *Service) FixedChargeFee(ctx context.Context, r port.Repos, sc Scope, fc *catalog.FixedCharge, opts Options) (*invoice.Fee, error) {
	env := sc.Env
	p, days, ok, err := s.fixedChargePeriod(sc, fc)
	if err != nil || !ok {
		return nil, err
	}
	if !opts.CurrentUsage {
		billed, err := s.fixedChargeBilled(ctx, r, sc, fc, p)
		if err != nil || billed {
			return nil, err
		}
	}
	props, err := pricing.DecodeProperties(pricing.ChargeModel(fc.ChargeModel), fc.Properties.Raw())
	if err != nil {
		return nil, fmt.Errorf("properties of fixed charge %s: %w", fc.ID, err)
	}
	structure := pricing.PricingStructure{Properties: props, Prorated: fc.Prorated, Currency: env.Plan.Currency}

	adj, err := loadAdjustments(ctx, r, sc, opts)
	if err != nil {
		return nil, err
	}
	a := adj.fixedCharge(fc.ID)
	if a != nil && a.Mode != invoice.AdjustmentModeDisplayName {
		return s.adjustedFixedChargeFee(sc, fc, structure, p, days, a)
	}

	full, prorated, err := s.fixedChargeUnits(ctx, r, env, fc, p, days)
	if err != nil {
		return nil, err
	}
	agg := pricing.AggregationResult{Units: full, FullUnits: &full}
	if fc.Prorated {
		agg.Units = prorated
		agg.PerEvent = &pricing.PerEventAggregation{Values: []money.Decimal{full}, Prorated: []money.Decimal{prorated}}
	}
	res, err := pricing.Apply(structure, agg, pricing.ApplyOptions{})
	if err != nil {
		return nil, fmt.Errorf("price fixed charge %s: %w", fc.ID, err)
	}
	deduction, err := s.fixedChargeDeduction(ctx, r, sc, fc, p)
	if err != nil {
		return nil, err
	}
	amounts, err := pricing.ComputeAmounts(pricing.AmountsInput{Currency: env.Plan.Currency, Result: &res, Deduction: deduction})
	if err != nil {
		return nil, fmt.Errorf("amounts of fixed charge %s: %w", fc.ID, err)
	}
	units := deref(res.FullUnits)
	if res.FullUnits == nil {
		units = res.Units
	}
	if res.Units.IsNegative() || res.Amount.IsNegative() {
		units = money.Zero
	}

	f := s.fixedChargeFeeBase(sc, fc, p, days)
	setAmount(f, amounts.Amount, nil)
	f.Units, f.TotalAggregatedUnits = units, units
	if f.AmountDetails, err = details(res.Details); err != nil {
		return nil, err
	}
	if a != nil {
		f.InvoiceDisplayName = a.InvoiceDisplayName
	}
	if opts.CurrentUsage || opts.Recurring || !f.Units.IsZero() || f.Amount != 0 || a != nil {
		return f, nil
	}
	return nil, nil
}

// fixedChargePeriod returns the fixed charges period a fixed charge of sc
// is billed for and its length in days: the period of the run, or for a
// fixed charge paid in advance the period holding the billing instant.
func (s *Service) fixedChargePeriod(sc Scope, fc *catalog.FixedCharge) (period.Period, int, bool, error) {
	b := sc.Boundaries
	if !fc.PayInAdvance {
		return b.FixedCharges, b.FixedChargesDays, b.FixedChargesBilled, nil
	}
	d, err := sc.Env.Dates(s.timestamp(sc), period.CurrentUsage)
	if err != nil {
		return period.Period{}, 0, false, err
	}
	p, ok := d.FixedChargesPeriod()
	return p, d.FixedChargesDays(), ok, nil
}

// fixedChargeFeeBase returns a fixed-charge fee of sc for the period p.
func (s *Service) fixedChargeFeeBase(sc Scope, fc *catalog.FixedCharge, p period.Period, days int) *invoice.Fee {
	f := s.newFee(sc, core.FeeTypeFixedCharge)
	f.FixedChargeID = fc.ID
	f.PayInAdvance = fc.PayInAdvance
	f.Properties = invoice.FeeProperties{
		From: sc.Boundaries.Subscription.From, To: sc.Boundaries.Subscription.To,
		ChargesFrom: p.From, ChargesTo: p.To,
		Timestamp: s.timestamp(sc), ChargesDurationDays: days,
	}
	return f
}

// fixedChargeBilled reports whether the fixed charge already has a fee on
// the invoice of sc or, when paid in advance, a fee with units or amount
// for the period p on an invoice that is not voided.
func (s *Service) fixedChargeBilled(ctx context.Context, r port.Repos, sc Scope, fc *catalog.FixedCharge, p period.Period) (bool, error) {
	fs, err := invoiceFees(ctx, r, sc)
	if err != nil {
		return false, err
	}
	for _, f := range fs {
		if f.Type == core.FeeTypeFixedCharge && f.FixedChargeID == fc.ID && f.SubscriptionID == sc.Env.Subscription.ID {
			return true, nil
		}
	}
	if !fc.PayInAdvance {
		return false, nil
	}
	all, err := subscriptionFees(ctx, r, sc.Env, core.FeeTypeFixedCharge)
	if err != nil {
		return false, err
	}
	for _, f := range all {
		if f.FixedChargeID != fc.ID || (f.Units.IsZero() && f.Amount == 0) || f.InvoiceID.IsZero() ||
			!sameSecond(f.Properties.ChargesFrom, p.From) || !sameSecond(f.Properties.ChargesTo, p.To) {
			continue
		}
		inv, err := r.Invoices.Get(ctx, f.OrganizationID, f.InvoiceID)
		if err != nil {
			return false, fmt.Errorf("load invoice %s: %w", f.InvoiceID, err)
		}
		if inv.Status != invoice.StatusVoided {
			return true, nil
		}
	}
	return false, nil
}

// sameSecond reports whether two instants fall in the same second.
func sameSecond(a, b time.Time) bool { return a.Truncate(time.Second).Equal(b.Truncate(time.Second)) }

// fixedChargeUnits returns the units of a fixed charge for p: those of the
// last created change in force in p, and the units weighted by the days
// each change was in force over the days of the whole period (lago
// FixedChargeEvents::Aggregations::SimpleAggregationService and
// ProratedAggregationService).
//
// As in lago, the changes are those taking effect in p plus the last
// created one taking effect before p, ordered by creation: a change
// created later but taking effect earlier than another supersedes it. Each
// remaining change is in force from its effective date to that of the next
// created one. Without any change, the fixed charge's own units are in
// force over the whole period.
func (s *Service) fixedChargeUnits(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, fc *catalog.FixedCharge, p period.Period, days int) (full, prorated money.Decimal, err error) {
	events, err := s.fixedChargeEvents(ctx, r, env, fc)
	if err != nil {
		return money.Zero, money.Zero, err
	}
	type change struct {
		ev *subscription.FixedChargeEvent
		at time.Time
	}
	byCreation := func(a, b change) int {
		if c := a.ev.CreatedAt.Compare(b.ev.CreatedAt); c != 0 {
			return c
		}
		return a.ev.ID.Compare(b.ev.ID)
	}
	var changes []change
	var before *change
	for _, e := range events {
		c := change{ev: e, at: e.Timestamp}
		if !e.ApplyUnitsImmediately {
			c.at = fixedChargeEffectiveAt(env, e.Timestamp)
		}
		switch {
		case c.at.Before(p.From):
			if before == nil || byCreation(c, *before) > 0 {
				before = &c
			}
		case c.at.Before(p.To):
			changes = append(changes, c)
		}
	}
	if before != nil {
		changes = append(changes, *before)
	}
	slices.SortFunc(changes, byCreation)
	if len(changes) == 0 {
		// Without any change, the fixed charge's own units are in force
		// over the whole period.
		changes = []change{{ev: &subscription.FixedChargeEvent{Units: fc.Units}, at: p.From}}
	}
	full = changes[len(changes)-1].ev.Units
	if !fc.Prorated || days <= 0 {
		return full, full, nil
	}

	// A change superseded by one created later and taking effect earlier
	// is dropped (lago later_event_earlier_timestamp_sql).
	kept := changes[:0:0]
	for _, c := range changes {
		superseded := slices.ContainsFunc(changes, func(o change) bool {
			return o.at.Before(c.at) && o.ev.CreatedAt.After(c.ev.CreatedAt)
		})
		if !superseded {
			kept = append(kept, c)
		}
	}
	loc := env.Location()
	// The last change runs to the start of the day after the last instant
	// of p, so a period cut mid-day counts that day (lago
	// to_datetime_excluded); the next day's start resolves a midnight
	// skipped by DST forward.
	last := period.EndOfDay(p.To.Add(-time.Nanosecond), loc)
	total := money.NewFromInt(int64(days))
	prorated = money.Zero
	for i, c := range kept {
		start, end := c.at, last
		if i+1 < len(kept) {
			end = kept[i+1].at
		}
		if start.Before(p.From) {
			start = p.From
		}
		if end.Before(p.From) {
			end = p.From
		}
		segment := money.NewFromInt(int64(calendarDays(start, end, loc)))
		w := c.ev.Units.Mul(segment).Div(total)
		if w.IsNegative() {
			continue
		}
		prorated = prorated.Add(w.Round(6, money.HalfUp))
	}
	return full, prorated, nil
}

// fixedChargeEffectiveAt returns the instant a change of fixed charge units
// made at t and not applied immediately takes effect: the start of the next
// fixed charges period.
func fixedChargeEffectiveAt(env *kernel.SubscriptionEnv, t time.Time) time.Time {
	d, err := env.Dates(t, period.CurrentUsage)
	if err != nil {
		return t
	}
	if end, ok := d.FixedChargesPeriodEnd(); ok {
		return end
	}
	return t
}

// calendarDays returns the number of local dates from the date of a to the
// date of b.
func calendarDays(a, b time.Time, loc *time.Location) int {
	da, db := startOfDay(a, loc).In(loc), startOfDay(b, loc).In(loc)
	ua := time.Date(da.Year(), da.Month(), da.Day(), 0, 0, 0, 0, time.UTC)
	ub := time.Date(db.Year(), db.Month(), db.Day(), 0, 0, 0, 0, time.UTC)
	return int(ub.Sub(ua).Hours() / 24)
}

// fixedChargeEvents returns the unit changes of a fixed charge and of the
// fixed charge it overrides.
func (s *Service) fixedChargeEvents(ctx context.Context, r port.Repos, env *kernel.SubscriptionEnv, fc *catalog.FixedCharge) ([]*subscription.FixedChargeEvent, error) {
	ids := []core.FixedChargeID{fc.ID}
	if !fc.ParentID.IsZero() {
		ids = append(ids, fc.ParentID)
	}
	var out []*subscription.FixedChargeEvent
	for _, id := range ids {
		es, err := r.FixedChargeEvents.ListBySubscription(ctx, env.OrganizationID(), env.Subscription.ID, id)
		if err != nil {
			return nil, fmt.Errorf("load units of fixed charge %s: %w", id, err)
		}
		out = append(out, es...)
	}
	return out, nil
}

// fixedChargeDeduction returns what the replaced subscription already paid
// for the same add-on over p, when sc is the first bill of a subscription
// replacing another and its prorated fixed charge is paid in advance; nil
// otherwise.
func (s *Service) fixedChargeDeduction(ctx context.Context, r port.Repos, sc Scope, fc *catalog.FixedCharge, p period.Period) (*pricing.Deduction, error) {
	env := sc.Env
	if !fc.PayInAdvance || !fc.Prorated || env.Previous == nil {
		return nil, nil
	}
	n, err := invoiceCount(ctx, r, sc)
	if err != nil || n > 1 {
		return nil, err
	}
	prevCharges, err := r.FixedCharges.ListByPlan(ctx, env.OrganizationID(), env.Previous.PlanID)
	if err != nil {
		return nil, fmt.Errorf("load fixed charges of plan %s: %w", env.Previous.PlanID, err)
	}
	i := slices.IndexFunc(prevCharges, func(o *catalog.FixedCharge) bool { return o.AddOnID == fc.AddOnID })
	if i < 0 {
		return nil, nil
	}
	prevFees, err := kernel.All(func(pg core.PageRequest) (core.Page[*invoice.Fee], error) {
		return r.Fees.List(ctx, env.OrganizationID(), invoice.FeeFilter{SubscriptionID: env.Previous.ID, Types: []core.FeeType{core.FeeTypeFixedCharge}}, pg)
	})
	if err != nil {
		return nil, fmt.Errorf("load fees of subscription %s: %w", env.Previous.ID, err)
	}
	var paid *invoice.Fee
	for _, f := range prevFees {
		if f.FixedChargeID != prevCharges[i].ID || f.Properties.ChargesFrom.After(p.From) || f.Properties.ChargesTo.Before(p.To.Truncate(time.Millisecond)) {
			continue
		}
		if paid == nil || f.CreatedAt.After(paid.CreatedAt) {
			paid = f
		}
	}
	if paid == nil {
		return nil, nil
	}
	return &pricing.Deduction{
		AmountCents: paid.Amount,
		BilledDays:  ceilDays(p.To.Sub(p.From)),
		PeriodDays:  ceilDays(paid.Properties.ChargesTo.Sub(paid.Properties.Timestamp)),
	}, nil
}

// ceilDays returns a duration in days, rounded up.
func ceilDays(d time.Duration) int64 {
	const day = 24 * time.Hour
	n := int64(d / day)
	if d%day > 0 {
		n++
	}
	return n
}

// adjustedFixedChargeFee builds the fee of a fixed charge from its
// adjustment (lago Fees::InitFromAdjustedFixedChargeFeeService).
func (s *Service) adjustedFixedChargeFee(sc Scope, fc *catalog.FixedCharge, structure pricing.PricingStructure, p period.Period, days int, a *invoice.AdjustedFee) (*invoice.Fee, error) {
	f := s.fixedChargeFeeBase(sc, fc, p, days)
	cur := sc.Env.Plan.Currency
	sub := cur.SubunitToUnit()
	f.Units, f.TotalAggregatedUnits = a.Units, a.Units
	f.InvoiceDisplayName = a.InvoiceDisplayName
	if a.Mode == invoice.AdjustmentModeUnits {
		units := a.Units
		res, err := pricing.Apply(structure, pricing.AggregationResult{Units: units, CurrentUsageUnits: &units, FullUnits: &units}, pricing.ApplyOptions{})
		if err != nil {
			return nil, fmt.Errorf("price adjusted fee of fixed charge %s: %w", fc.ID, err)
		}
		f.Amount = int64Of(cur.Round(res.Amount, money.HalfUp).Mul(sub))
		f.PreciseAmount = res.Amount.Mul(sub)
		f.UnitAmount = int64Of(res.UnitAmount.Mul(sub))
		f.PreciseUnitAmount = res.UnitAmount
		if f.AmountDetails, err = details(res.Details); err != nil {
			return nil, err
		}
		return f, nil
	}
	unitCents := a.UnitPreciseAmount.Mul(sub)
	f.UnitAmount = kernel.RoundCents(unitCents)
	f.PreciseAmount = a.Units.Mul(unitCents)
	f.Amount = kernel.RoundCents(f.PreciseAmount)
	f.PreciseUnitAmount = a.UnitPreciseAmount
	f.AmountDetails = []byte("{}")
	return f, nil
}
