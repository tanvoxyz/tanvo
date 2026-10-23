package usage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Thresholds returns the usage thresholds that drive the progressive
// billing of sub (domain §7.9): its own, else its plan's, else the parent
// plan's; fixed ones by ascending amount, then the recurring one. None when
// progressive billing is disabled.
func (s *Service) Thresholds(ctx context.Context, r port.Repos, sub *subscription.Subscription) ([]*catalog.UsageThreshold, error) {
	if sub.ProgressiveBillingDisabled {
		return nil, nil
	}
	org := sub.OrganizationID
	own, err := r.UsageThresholds.ListBySubscription(ctx, org, sub.ID)
	if err != nil {
		return nil, fmt.Errorf("list usage thresholds of subscription %s: %w", sub.ID, err)
	}
	p, err := r.Plans.Get(ctx, org, sub.PlanID)
	if err != nil {
		return nil, fmt.Errorf("load plan %s: %w", sub.PlanID, err)
	}
	plan, err := r.UsageThresholds.ListByPlan(ctx, org, p.ID)
	if err != nil {
		return nil, fmt.Errorf("list usage thresholds of plan %s: %w", p.ID, err)
	}
	var parent []*catalog.UsageThreshold
	if !p.ParentID.IsZero() && len(own) == 0 && len(plan) == 0 {
		if parent, err = r.UsageThresholds.ListByPlan(ctx, org, p.ParentID); err != nil {
			return nil, fmt.Errorf("list usage thresholds of plan %s: %w", p.ParentID, err)
		}
	}
	out := slices.Clone(subscription.ApplicableUsageThresholds(sub, own, plan, parent))
	slices.SortStableFunc(out, func(a, b *catalog.UsageThreshold) int {
		if a.Recurring != b.Recurring {
			if a.Recurring {
				return 1
			}
			return -1
		}
		return cmp.Compare(a.Amount, b.Amount)
	})
	return out, nil
}

// split returns the fixed thresholds of ts (in order) and the recurring
// one, nil when there is none.
func split(ts []*catalog.UsageThreshold) (fixed []*catalog.UsageThreshold, recurring *catalog.UsageThreshold) {
	for _, t := range ts {
		if t.Recurring {
			if recurring == nil {
				recurring = t
			}
			continue
		}
		fixed = append(fixed, t)
	}
	return fixed, recurring
}

// CrossedThresholds returns the thresholds of ts (sorted as [Service.Thresholds]
// returns them) that the lifetime usage lu crosses, given the amount
// progressive billing invoices already billed in the period (lago
// LifetimeUsages::UsageThresholds::CheckService): the fixed thresholds
// between the invoiced and the total usage, then the recurring one when a
// whole step of it is crossed.
func CrossedThresholds(lu *subscription.LifetimeUsage, ts []*catalog.UsageThreshold, progressiveBilled int64) []*catalog.UsageThreshold {
	if len(ts) == 0 {
		return nil
	}
	fixed, recurring := split(ts)
	// The current usage of the period minus what progressive invoices
	// already billed of it: a recurring threshold can be crossed several
	// times per period.
	current := lu.CurrentUsageAmount - progressiveBilled
	if current < 0 {
		return nil
	}
	invoiced := lu.HistoricalUsageAmount + lu.InvoicedUsageAmount + progressiveBilled
	var largest int64
	for _, t := range fixed {
		largest = max(largest, t.Amount)
	}
	total := invoiced + current
	var out []*catalog.UsageThreshold
	switch {
	case invoiced < largest:
		for _, t := range fixed {
			if t.Amount > invoiced && t.Amount <= total {
				out = append(out, t)
			}
		}
		if recurring != nil && total-largest >= recurring.Amount {
			out = append(out, recurring)
		}
	case recurring != nil && recurring.Amount > 0:
		if current+invoiced%recurring.Amount >= recurring.Amount {
			out = append(out, recurring)
		}
	}
	return out
}

// Crossing is the outcome of [Service.CheckThresholds].
type Crossing struct {
	// LifetimeUsage is the lifetime usage checked.
	LifetimeUsage *subscription.LifetimeUsage
	// Thresholds are the crossed thresholds, fixed ones by ascending amount
	// then the recurring one; empty when none is crossed.
	Thresholds []*catalog.UsageThreshold
	// ProgressiveBilled is what progressive billing invoices of the
	// current period already billed, in minor units.
	ProgressiveBilled int64
}

// CheckThresholds returns the thresholds the stored lifetime usage of the
// active subscription sub crosses (lago LifetimeUsages::CheckThresholdsService
// without the invoice). The caller bills them with a progressive billing
// invoice and emits subscription.usage_threshold_reached for each. A
// subscription that is not active or has no lifetime usage crosses none.
func (s *Service) CheckThresholds(ctx context.Context, r port.Repos, sub *subscription.Subscription) (Crossing, error) {
	var out Crossing
	if sub.Status != subscription.StatusActive {
		return out, nil
	}
	ts, err := s.Thresholds(ctx, r, sub)
	if err != nil || len(ts) == 0 {
		return out, err
	}
	lu, err := r.LifetimeUsages.GetBySubscription(ctx, sub.OrganizationID, sub.ID)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return out, nil
		}
		return out, fmt.Errorf("load lifetime usage of %s: %w", sub.ID, err)
	}
	pb, err := deduct.New(s.d).ProgressiveBilledAmount(ctx, r, sub.OrganizationID, sub.ID, s.d.Now(), false)
	if err != nil {
		return out, err
	}
	out.LifetimeUsage, out.ProgressiveBilled = lu, pb.BilledAmount
	out.Thresholds = CrossedThresholds(lu, ts, pb.BilledAmount)
	return out, nil
}

// RefreshAndCheck recomputes the lifetime usage of subscription id, then
// checks its thresholds (lago LifetimeUsages::RecalculateAndCheckJob,
// without the invoice). current is the current usage already computed
// without taxes, or nil to compute it (lago recalculate_and_check_job.rb:27,
// current_usage:).
func (s *Service) RefreshAndCheck(ctx context.Context, r port.Repos, org core.OrganizationID, id core.SubscriptionID, current *Usage) (Crossing, error) {
	lu, err := s.RefreshLifetimeUsage(ctx, r, org, id, current)
	if err != nil {
		return Crossing{}, err
	}
	sub, err := r.Subscriptions.Get(ctx, org, id)
	if err != nil {
		return Crossing{}, fmt.Errorf("load subscription %s: %w", id, err)
	}
	c, err := s.CheckThresholds(ctx, r, sub)
	if err != nil {
		return Crossing{}, err
	}
	if c.LifetimeUsage == nil {
		c.LifetimeUsage = lu
	}
	return c, nil
}

// ThresholdProgress is a threshold with how much of it the lifetime usage
// completes.
type ThresholdProgress struct {
	// Threshold is the usage threshold.
	Threshold *catalog.UsageThreshold
	// Amount is the lifetime amount of the threshold, in minor units: its
	// amount for a fixed one, the amount of that occurrence for a recurring
	// one.
	Amount int64
	// Ratio is the completion, 1 for a reached threshold.
	Ratio money.Decimal
	// Reached reports whether the lifetime usage reached it.
	Reached bool
	// ReachedAt is when it was reached: the creation of its applied
	// threshold, else now; nil when not reached.
	ReachedAt *time.Time
}

// ThresholdsCompletion returns the progress of the lifetime usage lu of sub
// toward its thresholds (lago LifetimeUsages::UsageThresholdsCompletionService):
// every reached fixed threshold, then either the next fixed one with its
// ratio, the farther fixed ones at 0 and the recurring one after the
// largest, or, past the fixed thresholds, every reached occurrence of the
// recurring one and the next one.
func (s *Service) ThresholdsCompletion(ctx context.Context, r port.Repos, sub *subscription.Subscription, lu *subscription.LifetimeUsage) ([]ThresholdProgress, error) {
	ts, err := s.Thresholds(ctx, r, sub)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	subs, err := chain(ctx, r, sub)
	if err != nil {
		return nil, err
	}
	var applied []*subscription.AppliedUsageThreshold
	for _, x := range subs {
		as, err := r.AppliedUsageThresholds.ListBySubscription(ctx, x.OrganizationID, x.ID)
		if err != nil {
			return nil, fmt.Errorf("list applied usage thresholds of %s: %w", x.ID, err)
		}
		applied = append(applied, as...)
	}
	return Completion(lu.Total(), ts, applied, s.d.Now()), nil
}

// Completion computes [Service.ThresholdsCompletion] for a lifetime total,
// the thresholds ts (sorted as [Service.Thresholds] returns them) and the
// applied thresholds of the chain.
func Completion(total int64, ts []*catalog.UsageThreshold, applied []*subscription.AppliedUsageThreshold, now time.Time) []ThresholdProgress {
	fixed, recurring := split(ts)
	var largest int64
	for _, t := range fixed {
		largest = max(largest, t.Amount)
	}
	var out []ThresholdProgress
	var lastPassed int64
	i := 0
	for ; i < len(fixed) && fixed[i].Amount <= total; i++ {
		t := fixed[i]
		var at time.Time
		for _, a := range applied {
			if a.UsageThresholdID == t.ID && a.CreatedAt.After(at) {
				at = a.CreatedAt
			}
		}
		if at.IsZero() {
			at = now
		}
		out = append(out, reached(t, t.Amount, at))
		lastPassed = t.Amount
	}
	if i < len(fixed) {
		next := fixed[i]
		ratio := money.NewFromInt(total - lastPassed).DivOrZero(money.NewFromInt(next.Amount - lastPassed))
		out = append(out, ThresholdProgress{Threshold: next, Amount: next.Amount, Ratio: ratio})
		for _, t := range fixed[i+1:] {
			out = append(out, ThresholdProgress{Threshold: t, Amount: t.Amount, Ratio: money.Zero})
		}
		if recurring != nil {
			out = append(out, ThresholdProgress{Threshold: recurring, Amount: largest + recurring.Amount, Ratio: money.Zero})
		}
		return out
	}
	if recurring == nil || recurring.Amount <= 0 {
		return out
	}
	var occurrences []*subscription.AppliedUsageThreshold
	for _, a := range applied {
		if a.UsageThresholdID == recurring.ID {
			occurrences = append(occurrences, a)
		}
	}
	slices.SortStableFunc(occurrences, func(a, b *subscription.AppliedUsageThreshold) int {
		return cmp.Compare(a.LifetimeUsageAmount, b.LifetimeUsageAmount)
	})
	step := recurring.Amount
	n := (total - lastPassed) / step
	for k := int64(1); k <= n; k++ {
		amount := lastPassed + k*step
		at := now
		if j := slices.IndexFunc(occurrences, func(a *subscription.AppliedUsageThreshold) bool { return a.LifetimeUsageAmount >= amount }); j >= 0 {
			at = occurrences[j].CreatedAt
		}
		out = append(out, reached(recurring, amount, at))
	}
	remainder := (total - lastPassed) % step
	out = append(out, ThresholdProgress{
		Threshold: recurring, Amount: total - remainder + step,
		Ratio: money.NewFromInt(remainder).Div(money.NewFromInt(step)),
	})
	return out
}

// reached returns the progress of a reached threshold.
func reached(t *catalog.UsageThreshold, amount int64, at time.Time) ThresholdProgress {
	return ThresholdProgress{Threshold: t, Amount: amount, Ratio: money.NewFromInt(1), Reached: true, ReachedAt: &at}
}

// LastAndNext is the last reached and the next threshold of a lifetime
// usage (lago LifetimeUsages::FindLastAndNextThresholdsService).
type LastAndNext struct {
	// LastAmount is the lifetime amount of the last reached threshold; nil
	// when none is reached.
	LastAmount *int64
	// NextAmount is the lifetime amount of the next threshold; nil when
	// there is none.
	NextAmount *int64
	// NextRatio is the completion of the next threshold; nil when there is
	// none.
	NextRatio *money.Decimal
}

// LastAndNextThresholds returns the last reached and the next threshold of
// the lifetime usage lu of sub.
func (s *Service) LastAndNextThresholds(ctx context.Context, r port.Repos, sub *subscription.Subscription, lu *subscription.LifetimeUsage) (LastAndNext, error) {
	ps, err := s.ThresholdsCompletion(ctx, r, sub, lu)
	if err != nil {
		return LastAndNext{}, err
	}
	return LastAndNextOf(ps), nil
}

// LastAndNextOf picks the last reached and the next threshold of the
// progress ps returned by [Completion].
func LastAndNextOf(ps []ThresholdProgress) LastAndNext {
	var out LastAndNext
	next := 0
	for i, p := range ps {
		if p.Reached {
			out.LastAmount = &ps[i].Amount
			next = i + 1
		}
	}
	if next < len(ps) {
		out.NextAmount, out.NextRatio = &ps[next].Amount, &ps[next].Ratio
	}
	return out
}
