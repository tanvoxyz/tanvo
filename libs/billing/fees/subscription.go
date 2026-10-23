package fees

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// SubscriptionFee returns the subscription fee of sc for the subscription
// period of its boundaries (domain §22.5): the plan amount, prorated for a
// partial first period, an upgrade, a termination or a trial ending in the
// period. It returns nil when the invoice already has the subscription's
// fee.
func (s *Service) SubscriptionFee(ctx context.Context, r port.Repos, sc Scope, opts Options) (*invoice.Fee, error) {
	env := sc.Env
	existing, err := invoiceFees(ctx, r, sc)
	if err != nil {
		return nil, err
	}
	for _, f := range existing {
		if f.Type == core.FeeTypeSubscription && f.SubscriptionID == env.Subscription.ID {
			return nil, nil
		}
	}

	precise, err := s.subscriptionAmount(ctx, r, sc)
	if err != nil {
		return nil, err
	}
	rounded := kernel.RoundCents(precise)
	cur := env.Plan.Currency
	sub := cur.SubunitToUnit()

	f := s.newFee(sc, core.FeeTypeSubscription)
	b := sc.Boundaries
	f.Properties = s.chargeProperties(sc)
	f.Properties.From, f.Properties.To = b.Subscription.From, b.Subscription.To
	f.Amount, f.PreciseAmount = rounded, precise
	f.UnitAmount, f.PreciseUnitAmount = rounded, kernel.Cents(rounded).Div(sub)
	f.Units, f.TotalAggregatedUnits = money.One, money.One
	f.AmountDetails, err = json.Marshal(map[string]int64{"plan_amount_cents": env.Plan.Amount})
	if err != nil {
		return nil, fmt.Errorf("encode subscription fee details: %w", err)
	}

	adj, err := loadAdjustments(ctx, r, sc, opts)
	if err != nil {
		return nil, err
	}
	a := adj.subscription()
	if a == nil {
		return f, nil
	}
	f.InvoiceDisplayName = a.InvoiceDisplayName
	switch a.Mode {
	case invoice.AdjustmentModeUnits:
		f.Units = a.Units
		f.Amount = kernel.RoundCents(a.Units.Mul(kernel.Cents(rounded)))
		f.PreciseAmount = a.Units.Mul(precise)
	case invoice.AdjustmentModeUnitsAndUnitPrice:
		unitCents := a.UnitPreciseAmount.Mul(sub)
		f.Units = a.Units
		f.PreciseAmount = a.Units.Mul(unitCents)
		f.Amount = kernel.RoundCents(f.PreciseAmount)
		f.UnitAmount = kernel.RoundCents(unitCents)
		f.PreciseUnitAmount = a.UnitPreciseAmount
	}
	return f, nil
}

// subscriptionAmount returns the unrounded subscription fee of sc in minor
// units (lago Fees::SubscriptionService#compute_amount).
func (s *Service) subscriptionAmount(ctx context.Context, r port.Repos, sc Scope) (money.Decimal, error) {
	env := sc.Env
	sub, plan := env.Subscription, env.Plan
	d, err := env.Dates(s.timestamp(sc), period.Billing)
	if err != nil {
		return money.Zero, err
	}
	p := sc.Boundaries.Subscription
	ps := env.Period()
	loc := env.Location()
	amount := kernel.Cents(plan.Amount)
	trialEnd, hasTrial := ps.TrialEnd()

	// from cuts the period at the end of a trial ending in it; covered
	// reports a trial covering the whole period.
	from, covered := p.From, false
	if hasTrial {
		if !trialEnd.Before(p.To) {
			covered = true
		} else if trialEnd.After(p.From) {
			from = trialEnd
		}
	}
	dayPrice := func(days int) money.Decimal {
		return amount.DivOrZero(money.NewFromInt(int64(days)))
	}
	prorated := func(days int, periodDays int) money.Decimal {
		if covered {
			return money.Zero
		}
		return money.NewFromInt(int64(days)).Mul(dayPrice(periodDays))
	}

	// The last bill of a subscription paid in arrears that ends or is
	// upgraded: the days it was used.
	if sub.TerminatedAt != nil && !plan.PayInAdvance && (env.Next == nil || env.Change == period.Upgrade) {
		to := billedTo(p.To, sub.TerminatedAt, loc)
		return prorated(ps.BilledDays(from, to), d.PeriodDaysAt(from)), nil
	}

	// The first bill of a subscription created by an upgrade: the days
	// from the upgrade.
	upgraded, err := s.createdByUpgrade(ctx, r, sc)
	if err != nil {
		return money.Zero, err
	}
	if upgraded {
		return prorated(period.DaysBetween(from, p.To, loc), d.PeriodDays()), nil
	}

	full, err := s.fullPeriod(ctx, r, sc, d)
	if err != nil {
		return money.Zero, err
	}
	if full {
		switch {
		case covered:
			return money.Zero, nil
		case from.After(p.From):
			return prorated(period.DaysBetween(from, p.To, loc), d.PeriodDaysAt(p.From)), nil
		}
		return amount, nil
	}

	// The first bill of a subscription (or of one replacing a downgraded
	// one): the days since it started.
	return prorated(period.DaysBetween(from, p.To, loc), d.PeriodDays()), nil
}

// createdByUpgrade reports whether the subscription of sc replaces an
// upgraded one and is billed for the first time.
func (s *Service) createdByUpgrade(ctx context.Context, r port.Repos, sc Scope) (bool, error) {
	env := sc.Env
	if env.Previous == nil {
		return false, nil
	}
	n, err := invoiceCount(ctx, r, sc)
	if err != nil {
		return false, err
	}
	if n > 1 {
		return false, nil
	}
	prev, err := r.Plans.Get(ctx, env.OrganizationID(), env.Previous.PlanID)
	if err != nil {
		return false, fmt.Errorf("load plan of subscription %s: %w", env.Previous.ID, err)
	}
	return kernel.PlanChange(prev, env.Plan) == period.Upgrade, nil
}

// fullPeriod reports whether the subscription of sc is billed a full
// period: it was billed before, or it is paid in advance on its
// anniversary from the start, or it started in the past.
func (s *Service) fullPeriod(ctx context.Context, r port.Repos, sc Scope, d *period.Dates) (bool, error) {
	env := sc.Env
	sub, plan := env.Subscription, env.Plan
	if plan.PayInAdvance && sub.BillingTime == period.Anniversary && env.Previous == nil {
		return true, nil
	}
	createdAt := s.d.Now()
	if sc.Invoice != nil {
		createdAt = sc.Invoice.CreatedAt
	}
	fs, err := subscriptionFees(ctx, r, env, core.FeeTypeSubscription)
	if err != nil {
		return false, err
	}
	for _, f := range fs {
		if f.CreatedAt.Before(createdAt) && (sc.Invoice == nil || f.InvoiceID != sc.Invoice.ID) {
			return true, nil
		}
	}
	if sub.StartedAt == nil {
		return false, nil
	}
	startedInPast := dateOf(*sub.StartedAt).Before(dateOf(sub.CreatedAt))
	if startedInPast && plan.PayInAdvance {
		return true, nil
	}
	return startedInPast && sub.StartedAt.Before(d.PreviousPeriodStart()), nil
}

// dateOf returns the UTC midnight of the day of t (lago compares dates of
// UTC instants).
func dateOf(t time.Time) time.Time { return startOfDay(t, time.UTC) }
