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
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// CommitmentFee returns the minimum commitment true-up fee of sc (domain
// §22.9): the plan's minimum commitment, prorated on the days the
// subscription was active in the commitment period, less the subscription,
// charge and fixed-charge fees billed for that period. A plan paid in
// arrears reconciles the period of sc; a plan paid in advance reconciles
// the previous period, so its first invoice has no commitment fee. pending
// are the fees of the invoice not stored yet. The fee is returned even
// when zero. It returns nil when the plan has no minimum commitment, when
// the invoice already has the fee, or when there is no period to
// reconcile.
func (s *Service) CommitmentFee(ctx context.Context, r port.Repos, sc Scope, pending []*invoice.Fee) (*invoice.Fee, error) {
	env := sc.Env
	if sc.Invoice == nil || sc.InvoiceSubscription == nil {
		return nil, nil
	}
	c, err := r.Commitments.GetByPlan(ctx, env.OrganizationID(), env.Plan.ID, catalog.CommitmentTypeMinimumCommitment)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load commitment of plan %s: %w", env.Plan.ID, err)
	}
	stored, err := invoiceFees(ctx, r, sc)
	if err != nil {
		return nil, err
	}
	for _, f := range append(stored, pending...) {
		if f.Type == core.FeeTypeCommitment && f.SubscriptionID == env.Subscription.ID {
			return nil, nil
		}
	}

	iss, err := r.InvoiceSubscriptions.ListBySubscription(ctx, env.OrganizationID(), env.Subscription.ID)
	if err != nil {
		return nil, fmt.Errorf("load invoices of subscription %s: %w", env.Subscription.ID, err)
	}
	rec, err := s.reconciled(ctx, r, sc, iss)
	if err != nil || rec == nil {
		return nil, err
	}

	start, end, err := s.commitmentPeriod(sc, rec, iss)
	if err != nil {
		return nil, err
	}
	commitment, err := s.commitmentAmount(sc, c, rec, iss, start, end)
	if err != nil {
		return nil, err
	}
	recurring, err := s.recurringWindow(ctx, r, sc, rec, iss, start, end)
	if err != nil {
		return nil, err
	}
	used, preciseUsed, err := s.commitmentUsage(ctx, r, sc, pending, start, end, recurring)
	if err != nil {
		return nil, err
	}
	amount := max(commitment-used, 0)
	precise := money.Max(kernel.Cents(commitment).Sub(preciseUsed), money.Zero)

	f := s.newFee(sc, core.FeeTypeCommitment)
	f.CommitmentID = c.ID
	f.Amount, f.PreciseAmount = amount, precise
	f.UnitAmount = amount
	f.PreciseUnitAmount = env.Plan.Currency.MinorToUnits(kernel.Cents(amount))
	f.Units, f.TotalAggregatedUnits = money.One, money.One
	name := c.InvoiceName()
	f.InvoiceDisplayName = &name
	f.Properties = invoice.FeeProperties{From: rec.Period.From, To: rec.Period.To, Timestamp: rec.Timestamp}
	return f, nil
}

// reconciled returns the invoice subscription of the period the commitment
// fee of sc reconciles: the one of sc for a plan paid in arrears, the
// previous one with a subscription fee for a plan paid in advance (lago
// InvoiceSubscription#previous_invoice_subscription), or nil.
func (s *Service) reconciled(ctx context.Context, r port.Repos, sc Scope, iss []*invoice.Subscription) (*invoice.Subscription, error) {
	cur := sc.InvoiceSubscription
	if !sc.Env.Plan.PayInAdvance {
		return cur, nil
	}
	return previousInvoiceSubscription(ctx, r, sc, iss)
}

// previousInvoiceSubscription returns the latest invoice subscription of
// sc's subscription before (or starting with) the one of sc that has a
// subscription fee (lago InvoiceSubscription#previous_invoice_subscription),
// or nil.
func previousInvoiceSubscription(ctx context.Context, r port.Repos, sc Scope, iss []*invoice.Subscription) (*invoice.Subscription, error) {
	cur := sc.InvoiceSubscription
	var best *invoice.Subscription
	for _, is := range iss {
		if is.ID == cur.ID || is.Period.From.After(cur.Period.From) {
			continue
		}
		if best != nil && !is.Period.From.After(best.Period.From) {
			continue
		}
		fs, err := r.Fees.ListByInvoice(ctx, is.OrganizationID, is.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load fees of invoice %s: %w", is.InvoiceID, err)
		}
		for _, f := range fs {
			if f.Type == core.FeeTypeSubscription && f.SubscriptionID == sc.Env.Subscription.ID {
				best = is
				break
			}
		}
	}
	return best, nil
}

// commitmentPeriod returns the subscription period the commitment of rec
// covers (lago Commitments::DatesService): the period holding its billing
// instant for a plan paid in advance or a subscription ending for good,
// else the period just billed.
//
// A subscription ending for good the day after a period ended covers that
// ended period instead (lago Subscriptions::TerminatedDatesService), unless,
// for a plan paid in advance, a periodic invoice of iss already covers it.
func (s *Service) commitmentPeriod(sc Scope, rec *invoice.Subscription, iss []*invoice.Subscription) (time.Time, time.Time, error) {
	env := sc.Env
	mode := period.Billing
	terminated := env.Subscription.TerminatedAt != nil
	downgraded := env.Next != nil && env.Change == period.Downgrade
	if env.Plan.PayInAdvance || (terminated && !downgraded) {
		mode = period.CurrentUsage
	}
	if terminated && env.Next == nil {
		d, adjusted, err := env.Period().TerminationDates(rec.Timestamp)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if adjusted && (!env.Plan.PayInAdvance || !periodicallyInvoiced(env, d, iss)) {
			return d.PreviousPeriodStart(), d.PeriodEnd(), nil
		}
	}
	d, err := env.Dates(rec.Timestamp, mode)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return d.PreviousPeriodStart(), d.PeriodEnd(), nil
}

// periodicallyInvoiced reports whether a periodic invoice subscription of
// iss has the periods of d (lago
// TerminatedDatesService#matching_invoice_subscription?).
func periodicallyInvoiced(env *kernel.SubscriptionEnv, d *period.Dates, iss []*invoice.Subscription) bool {
	sp, _ := d.SubscriptionPeriod()
	cp, _ := d.ChargesPeriod()
	fp, _ := d.FixedChargesPeriod()
	ps := env.Period()
	return slices.ContainsFunc(iss, func(is *invoice.Subscription) bool {
		return is.Recurring && samePeriod(is.Period, sp) &&
			(!ps.Plan.ChargesBilledMonthly() || samePeriod(is.ChargesPeriod, cp)) &&
			(!ps.Plan.FixedChargesBilledMonthly() || samePeriod(is.FixedChargesPeriod, fp))
	})
}

// samePeriod reports whether a and b span the same instants.
func samePeriod(a, b period.Period) bool { return a.From.Equal(b.From) && a.To.Equal(b.To) }

// commitmentAmount returns the commitment prorated on the days the
// subscription was billed in [start, end) (lago
// Commitments::CalculateProratedCoefficientService), in minor units.
func (s *Service) commitmentAmount(sc Scope, c *catalog.Commitment, rec *invoice.Subscription, iss []*invoice.Subscription, start, end time.Time) (int64, error) {
	env := sc.Env
	if c.Amount == 0 {
		return 0, nil
	}
	// A plan paid in advance ending for good is prorated up to its
	// termination, on the current invoice subscription.
	coefficientIS := rec
	if env.Plan.PayInAdvance && env.Subscription.TerminatedAt != nil {
		coefficientIS = sc.InvoiceSubscription
	}
	first := coefficientIS.Period.From
	for _, is := range append(iss, coefficientIS) {
		if !is.Period.From.Before(start) && is.Period.From.Before(first) {
			first = is.Period.From
		}
	}
	loc := env.Location()
	ending := coefficientIS.Period.To
	if t := env.Subscription.TerminatedAt; t != nil {
		// lago counts to min(terminated_at, end_of_period): a termination
		// before the end of the period at a local midnight bills the day
		// it ends on (date_diff_with_timezone, see [billedTo]); one at the
		// end of the period (a downgrade) does not.
		ending = end
		if t.Before(end) {
			ending = billedTo(*t, t, loc)
		}
	}
	days := period.DaysBetween(first, ending, loc)
	total := period.DaysBetween(start, end, loc)
	if total <= 0 {
		return c.Amount, nil
	}
	coefficient := money.NewFromInt(int64(days)).Div(money.NewFromInt(int64(total)))
	return kernel.RoundCents(kernel.Cents(c.Amount).Mul(coefficient)), nil
}

// commitmentUsage sums the subscription, charge and fixed-charge fees of
// the subscription for the commitment period [start, end), stored and
// pending, leaving out voided invoices.
func (s *Service) commitmentUsage(ctx context.Context, r port.Repos, sc Scope, pending []*invoice.Fee, start, end time.Time, recurring *recurringWindow) (int64, money.Decimal, error) {
	stored, err := subscriptionFees(ctx, r, sc.Env, core.FeeTypeSubscription, core.FeeTypeCharge, core.FeeTypeFixedCharge)
	if err != nil {
		return 0, money.Zero, err
	}
	seen := map[core.FeeID]bool{}
	voided := map[core.InvoiceID]bool{}
	used, precise := int64(0), money.Zero
	for _, f := range append(pending, stored...) {
		if seen[f.ID] || f.SubscriptionID != sc.Env.Subscription.ID {
			continue
		}
		seen[f.ID] = true
		kind, err := s.inAdvanceChargeKind(ctx, r, f)
		if err != nil {
			return 0, money.Zero, err
		}
		switch kind {
		case periodicInAdvance:
			continue
		case periodicInAdvanceRecurring:
			if !recurring.holds(f, sc) {
				continue
			}
		default:
			if !inCommitmentPeriod(f, start, end) {
				continue
			}
		}
		if !f.InvoiceID.IsZero() && f.InvoiceID != sc.Invoice.ID {
			v, ok := voided[f.InvoiceID]
			if !ok {
				inv, err := r.Invoices.Get(ctx, f.OrganizationID, f.InvoiceID)
				if err != nil {
					return 0, money.Zero, fmt.Errorf("load invoice %s: %w", f.InvoiceID, err)
				}
				v = inv.Status == invoice.StatusVoided
				voided[f.InvoiceID] = v
			}
			if v {
				continue
			}
		}
		used += f.Amount
		precise = precise.Add(f.PreciseAmount)
	}
	return used, precise, nil
}

// inCommitmentPeriod reports whether a fee bills a period within
// [start, end): its subscription period for a subscription fee, its
// charges (or fixed charges) period otherwise.
func inCommitmentPeriod(f *invoice.Fee, start, end time.Time) bool {
	var from, to time.Time
	switch f.Type {
	case core.FeeTypeSubscription:
		from, to = f.Properties.From, f.Properties.To
	case core.FeeTypeCharge:
		from, to = f.Properties.ChargesPeriod()
	case core.FeeTypeFixedCharge:
		from, to = f.Properties.FixedChargesPeriod()
	default:
		return false
	}
	return !from.Before(start) && !to.After(end)
}

// Kinds of charge fees for the commitment usage (lago
// Commitments::Minimum::*::CalculateTrueUpFeeService).
const (
	// ordinaryFee is any fee other than the periodic fee of an in-advance
	// charge.
	ordinaryFee = iota
	// periodicInAdvance is the periodic fee of an in-advance charge of a
	// non-recurring metric, never counted.
	periodicInAdvance
	// periodicInAdvanceRecurring is the periodic fee of an in-advance
	// charge of a recurring metric, counted in the window of
	// [recurringWindow].
	periodicInAdvanceRecurring
)

// inAdvanceChargeKind classifies f for the commitment usage.
func (s *Service) inAdvanceChargeKind(ctx context.Context, r port.Repos, f *invoice.Fee) (int, error) {
	if f.Type != core.FeeTypeCharge || f.PayInAdvance || f.ChargeID.IsZero() {
		return ordinaryFee, nil
	}
	c, err := r.Charges.Get(ctx, f.OrganizationID, f.ChargeID)
	if err != nil {
		return 0, fmt.Errorf("load charge %s: %w", f.ChargeID, err)
	}
	if !c.PayInAdvance {
		return ordinaryFee, nil
	}
	m, err := r.Metrics.Get(ctx, f.OrganizationID, c.BillableMetricID)
	if err != nil {
		return 0, fmt.Errorf("load billable metric %s: %w", c.BillableMetricID, err)
	}
	if !m.Recurring {
		return periodicInAdvance, nil
	}
	return periodicInAdvanceRecurring, nil
}

// recurringWindow is where the periodic fees of in-advance recurring
// charges count; nil counts none.
type recurringWindow struct {
	// start and end bound the fee's period.
	start, end time.Time
	// subscriptionPeriod tests the fee's subscription period rather than
	// its charges period.
	subscriptionPeriod bool
	// excludeCurrent leaves out the fees of the invoice being billed.
	excludeCurrent bool
}

// holds reports whether the periodic fee f of an in-advance recurring
// charge counts.
func (w *recurringWindow) holds(f *invoice.Fee, sc Scope) bool {
	if w == nil {
		return false
	}
	if w.excludeCurrent && f.InvoiceID == sc.Invoice.ID {
		return false
	}
	from, to := f.Properties.ChargesPeriod()
	if f.Type == core.FeeTypeFixedCharge {
		from, to = f.Properties.FixedChargesPeriod()
	}
	if w.subscriptionPeriod {
		from, to = f.Properties.From, f.Properties.To
	}
	return !from.Before(w.start) && !to.After(w.end)
}

// recurringWindow returns the window of the periodic fees of in-advance
// recurring charges: the reconciled period, on the fees' subscription
// period, for a plan paid in advance; for a plan paid in arrears, the
// period of the previous invoice subscription, or, when charges are billed
// monthly, the reconciled period widened by a month without the current
// invoice's fees. The first invoice of a plan paid in arrears billed whole
// counts none.
func (s *Service) recurringWindow(ctx context.Context, r port.Repos, sc Scope, rec *invoice.Subscription, iss []*invoice.Subscription, start, end time.Time) (*recurringWindow, error) {
	plan := sc.Env.Plan
	if plan.PayInAdvance {
		return &recurringWindow{start: start, end: end, subscriptionPeriod: true}, nil
	}
	if plan.ChargesBilledMonthly() {
		return &recurringWindow{start: start.AddDate(0, -1, 0), end: end, excludeCurrent: true}, nil
	}
	prev, err := previousInvoiceSubscription(ctx, r, sc, iss)
	if err != nil || prev == nil {
		return nil, err
	}
	pstart, pend, err := s.commitmentPeriod(sc, prev, iss)
	if err != nil {
		return nil, err
	}
	return &recurringWindow{start: pstart, end: pend}, nil
}
