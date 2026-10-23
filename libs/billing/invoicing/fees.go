package invoicing

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// subscriptionFees computes and stores the fees of the subscription
// invoice inv over rows (lago Invoices::CalculateFeesService): the
// subscription fee, the charge fees, the fixed charge fees and the
// minimum commitment true-up of each subscription. It also returns the
// recurring fees of non-invoiceable pay-in-advance charges, stored
// without an invoice and taxed.
func (s *Service) subscriptionFees(ctx context.Context, r port.Repos, inv *invoice.Invoice, rows []*billed, skipCharges bool) (all, standalone []*invoice.Fee, err error) {
	metrics := map[core.BillableMetricID]*catalog.BillableMetric{}
	metric := func(id core.BillableMetricID) (*catalog.BillableMetric, error) {
		if m, ok := metrics[id]; ok {
			return m, nil
		}
		m, err := r.Metrics.Get(ctx, inv.OrganizationID, id)
		if err != nil {
			return nil, fmt.Errorf("load billable metric %s: %w", id, err)
		}
		metrics[id] = m
		return m, nil
	}
	store := func(f *invoice.Fee) error {
		if err := r.Fees.Create(ctx, f); err != nil {
			return fmt.Errorf("create fee: %w", err)
		}
		return nil
	}
	for _, b := range rows {
		env := b.env
		sc := fees.Scope{Env: env, Invoice: inv, InvoiceSubscription: b.is, Boundaries: b.bounds, Timestamp: b.is.Timestamp}
		opts := fees.Options{Recurring: b.is.Recurring}
		var own []*invoice.Fee
		history, err := r.InvoiceSubscriptions.ListBySubscription(ctx, inv.OrganizationID, env.Subscription.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("load invoices of subscription %s: %w", env.Subscription.ID, err)
		}
		fixed, err := env.FixedCharges(ctx, r)
		if err != nil {
			return nil, nil, err
		}

		bill, err := s.billsSubscriptionFee(ctx, r, inv, b, history, fixed)
		if err != nil {
			return nil, nil, err
		}
		if bill {
			f, err := s.fees.SubscriptionFee(ctx, r, sc, opts)
			if err != nil {
				return nil, nil, err
			}
			if f != nil {
				if err := store(f); err != nil {
					return nil, nil, err
				}
				own = append(own, f)
			}
		}

		var charges, nextCharges []*catalog.Charge
		if !skipCharges {
			if charges, err = env.Charges(ctx, r); err != nil {
				return nil, nil, err
			}
			if env.NextPlan != nil {
				if nextCharges, err = r.Charges.ListByPlan(ctx, env.OrganizationID(), env.NextPlan.ID); err != nil {
					return nil, nil, fmt.Errorf("load charges of plan %s: %w", env.NextPlan.ID, err)
				}
			}
		}
		chargesValid := b.bounds.ChargesBilled && b.bounds.Charges.From.Before(b.bounds.Charges.To)
		for _, c := range charges {
			m, err := metric(c.BillableMetricID)
			if err != nil {
				return nil, nil, err
			}
			if !c.Invoiceable || (c.PayInAdvance && !m.Recurring) || !chargesValid || skipsCharge(env, c, m, nextCharges) {
				continue
			}
			res, err := s.fees.ChargeFees(ctx, r, sc, c, opts)
			if err != nil {
				return nil, nil, err
			}
			if res == nil {
				continue
			}
			for _, f := range res.Fees {
				if err := store(f); err != nil {
					return nil, nil, err
				}
			}
			own = append(own, res.Fees...)
		}

		if billsFixedCharges(env, inv, fixed) && b.bounds.FixedChargesBilled && !b.bounds.FixedCharges.To.Before(b.bounds.FixedCharges.From) {
			for _, fc := range fixed {
				if !billsFixedCharge(env, fc, history) {
					continue
				}
				f, err := s.fees.FixedChargeFee(ctx, r, sc, fc, opts)
				if err != nil {
					return nil, nil, err
				}
				if f == nil {
					continue
				}
				if err := store(f); err != nil {
					return nil, nil, err
				}
				own = append(own, f)
			}
		}

		if !skipCharges && chargesValid && !justReplaced(env, b.is.Timestamp) {
			for _, c := range charges {
				m, err := metric(c.BillableMetricID)
				if err != nil {
					return nil, nil, err
				}
				if c.Invoiceable || !c.PayInAdvance || !m.Recurring || skipsCharge(env, c, m, nextCharges) {
					continue
				}
				nsc := sc
				nsc.Invoice, nsc.InvoiceSubscription = nil, nil
				res, err := s.fees.ChargeFees(ctx, r, nsc, c, fees.Options{Recurring: true})
				if err != nil {
					return nil, nil, err
				}
				if res == nil {
					continue
				}
				if err := s.taxStandalone(ctx, r, env.Customer, env, res.Fees); err != nil {
					return nil, nil, err
				}
				for _, f := range res.Fees {
					if err := store(f); err != nil {
						return nil, nil, err
					}
				}
				standalone = append(standalone, res.Fees...)
			}
		}

		if billsCommitment(env, b, history) {
			f, err := s.fees.CommitmentFee(ctx, r, sc, own)
			if err != nil {
				return nil, nil, err
			}
			if f != nil && f.Amount != 0 {
				if err := store(f); err != nil {
					return nil, nil, err
				}
				own = append(own, f)
			}
		}
		all = append(all, own...)
	}
	return all, standalone, nil
}

// terminated reports whether the subscription is terminated.
func terminated(s *subscription.Subscription) bool {
	return s.Status == subscription.StatusTerminated || s.TerminatedAt != nil
}

// upgraded reports whether the subscription was replaced by an upgrade.
func upgraded(env *kernel.SubscriptionEnv) bool {
	return env.Next != nil && env.Change == period.Upgrade
}

// billsSubscriptionFee ports lago CalculateFeesService
// #should_create_subscription_fee?.
func (s *Service) billsSubscriptionFee(ctx context.Context, r port.Repos, inv *invoice.Invoice, b *billed, history []*invoice.Subscription, fixed []*catalog.FixedCharge) (bool, error) {
	env := b.env
	sub, plan := env.Subscription, env.Plan
	at := b.is.Timestamp
	loc := env.Location()
	subFees, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Fee], error) {
		return r.Fees.List(ctx, inv.OrganizationID, invoice.FeeFilter{SubscriptionID: sub.ID, Types: []core.FeeType{core.FeeTypeSubscription}}, p)
	})
	if err != nil {
		return false, fmt.Errorf("load subscription fees of %s: %w", sub.ID, err)
	}
	if plan.PayInAdvance {
		today := core.DateOf(at, loc)
		for _, f := range subFees {
			if f.InvoiceID != inv.ID && (inv.VoidedInvoiceID.IsZero() || f.InvoiceID != inv.VoidedInvoiceID) && core.DateOf(f.CreatedAt, loc) == today {
				return false, nil
			}
		}
	}
	if plan.Interval == period.Yearly || plan.Interval == period.Semiannual {
		startedInPast := sub.StartedAt != nil && core.DateOf(*sub.StartedAt, time.UTC).Before(core.DateOf(sub.CreatedAt, time.UTC))
		d := b.dates
		var ok bool
		switch {
		case plan.PayInAdvance && !startedInPast:
			ok = d.FirstMonthOfPeriod() || len(subFees) == 0
		case plan.PayInAdvance:
			ok = !d.FirstMonthOfFirstPeriod() && d.FirstMonthOfPeriod()
		default:
			ok = terminated(sub) || d.FirstMonthOfPeriod()
		}
		if !ok {
			return false, nil
		}
	}
	ps := env.Period()
	if sub.TrialEndedAt == nil && ps.InTrial(at) {
		if end, ok := ps.TrialEnd(); ok && core.DateOf(end, loc) != core.DateOf(at, loc) {
			return false, nil
		}
	}
	if !plan.PayInAdvance && startingOnly(history) {
		for _, fc := range fixed {
			if fc.PayInAdvance {
				return false, nil
			}
		}
	}
	switch {
	case sub.Status == subscription.StatusActive || sub.Status == subscription.StatusIncomplete:
		return true, nil
	case terminated(sub):
		return !plan.PayInAdvance || (sub.TerminatedAt != nil && sub.TerminatedAt.After(inv.CreatedAt)), nil
	}
	return false, nil
}

// startingOnly reports whether the subscription's only invoice is its
// starting one.
func startingOnly(history []*invoice.Subscription) bool {
	return len(history) == 1 && history[0].Reason == invoice.InvoicingReasonSubscriptionStarting
}

// skipsCharge ports lago CalculateFeesService#should_not_create_charge_fee?:
// recurring charges paid in advance are not billed again when the
// subscription ends; recurring charges carried over by an upgrade are
// billed by the next subscription.
func skipsCharge(env *kernel.SubscriptionEnv, c *catalog.Charge, m *catalog.BillableMetric, next []*catalog.Charge) bool {
	sub := env.Subscription
	if c.PayInAdvance {
		return m.Recurring && terminated(sub) && (upgraded(env) || env.Next == nil)
	}
	if c.Prorated || !m.Recurring || !terminated(sub) || !upgraded(env) {
		return false
	}
	for _, n := range next {
		if n.BillableMetricID == c.BillableMetricID {
			return true
		}
	}
	return false
}

// billsFixedCharges ports lago #should_create_fixed_charge_fees?.
func billsFixedCharges(env *kernel.SubscriptionEnv, inv *invoice.Invoice, fixed []*catalog.FixedCharge) bool {
	sub := env.Subscription
	if sub.Status == subscription.StatusActive || sub.Status == subscription.StatusIncomplete {
		return true
	}
	if !terminated(sub) {
		return false
	}
	if sub.TerminatedAt != nil && sub.TerminatedAt.After(inv.CreatedAt) {
		return true
	}
	for _, fc := range fixed {
		if !fc.PayInAdvance {
			return true
		}
	}
	return false
}

// billsFixedCharge ports lago #should_create_fixed_charge_fee?: a starting
// invoice bills only fixed charges paid in advance; a terminated
// subscription bills none paid in advance.
func billsFixedCharge(env *kernel.SubscriptionEnv, fc *catalog.FixedCharge, history []*invoice.Subscription) bool {
	if !fc.PayInAdvance && startingOnly(history) {
		return false
	}
	return !fc.PayInAdvance || !terminated(env.Subscription)
}

// justReplaced reports whether the subscription started on the day its
// previous one ended, at the billing instant: the recurring fees paid in
// advance under the previous plan still hold.
func justReplaced(env *kernel.SubscriptionEnv, at time.Time) bool {
	prev, sub := env.Previous, env.Subscription
	if prev == nil || prev.TerminatedAt == nil || sub.StartedAt == nil {
		return false
	}
	day := core.DateOf(at, time.UTC)
	return core.DateOf(*prev.TerminatedAt, time.UTC) == day && core.DateOf(*sub.StartedAt, time.UTC) == day
}

// billsCommitment ports the period checks of lago
// #should_create_minimum_commitment_true_up_fee?; the amount check is the
// caller's (a zero true-up is not stored).
func billsCommitment(env *kernel.SubscriptionEnv, b *billed, history []*invoice.Subscription) bool {
	plan := env.Plan
	if plan.PayInAdvance && len(history) <= 1 {
		return false
	}
	if plan.Interval == period.Yearly || plan.Interval == period.Semiannual {
		if !terminated(env.Subscription) && !b.dates.FirstMonthOfPeriod() {
			return false
		}
	}
	return env.Subscription.Status == subscription.StatusActive || terminated(env.Subscription)
}
