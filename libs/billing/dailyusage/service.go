package dailyusage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// Service computes and stores daily usage snapshots.
type Service struct {
	d     *kernel.Deps
	usage *usage.Service
}

// New returns the service over the checked dependencies d, computing the
// usages with u.
func New(d *kernel.Deps, u *usage.Service) *Service {
	return &Service{d: d, usage: u}
}

// jitter returns the window the computations of one run are spread over.
func (s *Service) jitter() time.Duration {
	if s.d.DailyUsageJitter > 0 {
		return s.d.DailyUsageJitter
	}
	return kernel.DefaultDailyUsageJitter
}

// HandleJob runs the daily usage jobs ([kernel.JobComputeDailyUsages],
// [kernel.JobComputeDailyUsage], [kernel.JobFillDailyUsageFromInvoice],
// [kernel.JobFillDailyUsageHistory] and
// [kernel.JobRebuildDailyUsageHistory]) and reports whether j is one.
func (s *Service) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	switch j.Kind {
	case kernel.JobComputeDailyUsages:
		var a kernel.InstantArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		if a.At.IsZero() {
			// The clock job's tick is the instant (lago
			// Clock::ComputeAllDailyUsagesJob passes Time.current).
			return true, fmt.Errorf("%s: %w: no instant", j.Kind, core.ErrInvalidValue)
		}
		_, err := s.ComputeAll(ctx, j.OrganizationID, a.At)
		return true, err
	case kernel.JobComputeDailyUsage:
		var a kernel.DailyUsageArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		_, err := s.Compute(ctx, j.OrganizationID, a.SubscriptionID, a.Timestamp)
		return true, err
	case kernel.JobFillDailyUsageFromInvoice:
		var a kernel.FillDailyUsageFromInvoiceArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		_, err := s.FillFromInvoice(ctx, j.OrganizationID, a.InvoiceID, a.SubscriptionIDs)
		return true, err
	case kernel.JobFillDailyUsageHistory:
		var a kernel.FillDailyUsageHistoryArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.FillHistory(ctx, j.OrganizationID, a)
	case kernel.JobRebuildDailyUsageHistory:
		var a kernel.RebuildDailyUsageHistoryArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		_, _, err := s.RebuildHistory(ctx, j.OrganizationID, a)
		return true, err
	}
	return false, nil
}

func decode(j port.Job, v any) error {
	if err := json.Unmarshal(j.Args, v); err != nil {
		return fmt.Errorf("decode %s args: %w", j.Kind, err)
	}
	return nil
}

// planUsage tells how the usage of a plan's charges changes.
type planUsage struct {
	// timeDependent: a charge is prorated or on a weighted sum metric, so
	// its usage changes without events.
	timeDependent bool
	// recurring: a charge is on a recurring metric, so its usage carries
	// over into each new period.
	recurring bool
}

// planUsageOf classifies the charges of plan (lago
// DailyUsages::ComputeAllService#time_dependent_plan_ids and
// #recurring_plan_ids, app/services/daily_usages/compute_all_service.rb:107-124,
// and DailyUsages::FillHistoryService#produces_daily_usage_without_events?
// and #recurring_charges?, fill_history_service.rb:156-174). A deleted
// metric counts for neither.
func planUsageOf(ctx context.Context, r port.Repos, cc *catalogCache, plan core.PlanID) (planUsage, error) {
	charges, err := r.Charges.ListByPlan(ctx, cc.org, plan)
	if err != nil {
		return planUsage{}, fmt.Errorf("load charges of plan %s: %w", plan, err)
	}
	var pu planUsage
	for _, c := range charges {
		m, err := cc.metric(ctx, c.BillableMetricID)
		if err != nil {
			return planUsage{}, err
		}
		if m == nil {
			continue
		}
		pu.timeDependent = pu.timeDependent || c.Prorated || m.AggregationType == catalog.AggregationTypeWeightedSum
		pu.recurring = pu.recurring || m.Recurring
	}
	return pu, nil
}

// previous returns the usage of the latest snapshot of the subscription
// for the charges period [from, to) before day, or nil (lago
// DailyUsages::ComputeDiffService#previous_daily_usage,
// app/services/daily_usages/compute_diff_service.rb:67-73).
func previous(ctx context.Context, r port.Repos, org core.OrganizationID, sub core.SubscriptionID, from, to time.Time, day core.Date) (*subscription.DailyUsage, error) {
	p, err := r.DailyUsages.Previous(ctx, org, sub, from, to, day)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("load previous daily usage of subscription %s: %w", sub, err)
	}
	return p, nil
}

// store saves snap as the snapshot of sub for day, with diff (nil for an
// empty diff).
func (s *Service) store(ctx context.Context, r port.Repos, sub *subscription.Subscription, snap subscription.UsageSnapshot, day core.Date, refreshedAt time.Time, diff *subscription.UsageSnapshot) (*subscription.DailyUsage, error) {
	now := s.d.Now()
	du := &subscription.DailyUsage{
		ID: core.NewID[core.DailyUsageID](s.d.IDs), OrganizationID: sub.OrganizationID, CustomerID: sub.CustomerID,
		SubscriptionID: sub.ID, ExternalSubscriptionID: sub.ExternalID, Usage: snap, UsageDiff: diff,
		From: snap.From, To: snap.To, RefreshedAt: refreshedAt.UTC(), UsageDate: day, CreatedAt: now, UpdatedAt: now,
	}
	if err := du.Validate(); err != nil {
		return nil, err
	}
	if err := r.DailyUsages.Create(ctx, du); err != nil {
		return nil, fmt.Errorf("store daily usage of subscription %s for %s: %w", sub.ID, day, err)
	}
	return du, nil
}

// diffOf returns snap minus prev (the whole snap when prev is nil).
func diffOf(snap subscription.UsageSnapshot, prev *subscription.DailyUsage) *subscription.UsageSnapshot {
	var p *subscription.UsageSnapshot
	if prev != nil {
		p = &prev.Usage
	}
	d := subscription.DiffUsage(snap, p)
	return &d
}

// read runs fn in a transaction of d and returns its result.
func read[T any](ctx context.Context, d *kernel.Deps, fn func(ctx context.Context, r port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	return out, err
}
