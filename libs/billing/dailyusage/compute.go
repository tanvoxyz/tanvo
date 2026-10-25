package dailyusage

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// selectionPage is the page size of the subscriptions [Service.ComputeAll]
// reads at once.
const selectionPage = 500

// ComputeAll enqueues the [kernel.JobComputeDailyUsage] of every active
// subscription of org not skipping daily usages, whose customer is
// entering a new day at in its time zone (local hour 0, 1 or 2), whose
// usage of the previous day is not stored yet and whose usage may have
// changed (lago DailyUsages::ComputeAllService,
// app/services/daily_usages/compute_all_service.rb): it received an event
// since the previous UTC day; or it has a prorated charge or a charge on
// a weighted sum metric; or it has a charge on a recurring metric and a
// billing period rolled over the day before at. The jobs run within
// [kernel.Deps.DailyUsageJitter] of now, at random. It returns how many
// were enqueued.
//
// Each organization runs as its own job, so one failing organization does
// not stop the others (lago rescues per organization,
// compute_all_service.rb:16-22).
//
// The stored-usage check reads the day before at in the customer's time
// zone, the day [Service.Compute] stores; lago reads the UTC day
// (compute_all_service.rb:142-148), which for a zone ahead of UTC is two
// days back, so its check never matches there and the job runs to find
// the stored row (compute_service.rb:19-22): the outcome is the same.
func (s *Service) ComputeAll(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	at = at.UTC()
	utcDay := core.DateOf(at, time.UTC)
	yesterday := utcDay.AddDays(-1)
	type dayKey struct {
		sub core.SubscriptionID
		day core.Date
	}
	stored, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) (map[dayKey]bool, error) {
		days, err := r.DailyUsages.ListDaysBetween(ctx, org, utcDay.AddDays(-2), utcDay)
		if err != nil {
			return nil, fmt.Errorf("list daily usages of organization %s: %w", org, err)
		}
		out := make(map[dayKey]bool, len(days))
		for _, d := range days {
			out[dayKey{d.SubscriptionID, d.UsageDate}] = true
		}
		return out, nil
	})
	if err != nil {
		return 0, err
	}

	locs := map[core.CustomerID]*time.Location{}
	plans := map[core.PlanID]planUsage{}
	jitter := s.jitter()
	n := 0
	page := core.PageRequest{Size: selectionPage}
	for {
		next := ""
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			subs, err := r.Subscriptions.List(ctx, org, subscription.Filter{Statuses: []subscription.Status{subscription.StatusActive}}, page)
			if err != nil {
				return fmt.Errorf("list active subscriptions of organization %s: %w", org, err)
			}
			next = subs.NextToken
			cc := newCatalogCache(r, org)
			for _, sub := range subs.Items {
				if sub.SkipDailyUsage {
					continue
				}
				loc, err := location(ctx, r, sub, locs)
				if err != nil {
					return err
				}
				if !inWindow(at, loc) {
					continue
				}
				today := core.DateOf(at, loc)
				if stored[dayKey{sub.ID, today.AddDays(-1)}] {
					continue
				}
				ok, err := s.changed(ctx, r, cc, sub, at, yesterday, plans)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
				args := kernel.DailyUsageArgs{SubscriptionID: sub.ID, Timestamp: at}
				runAt := s.d.Now().Add(rand.N(jitter))
				if err := kernel.Enqueue(ctx, r, org, kernel.JobComputeDailyUsage, args, kernel.DailyUsageKey(sub.ID, today), runAt); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		if err != nil {
			return n, err
		}
		if next == "" {
			return n, nil
		}
		page.Token = next
	}
}

// changed reports whether the usage of sub may have changed by at: the
// event, time-dependent and recurring legs of compute_all_service.rb:57-94.
func (s *Service) changed(ctx context.Context, r port.Repos, cc *catalogCache, sub *subscription.Subscription, at time.Time, yesterday core.Date, plans map[core.PlanID]planUsage) (bool, error) {
	if last := sub.LastReceivedEventOn; last != nil && !last.Before(yesterday) {
		return true, nil
	}
	pu, ok := plans[sub.PlanID]
	if !ok {
		var err error
		if pu, err = planUsageOf(ctx, r, cc, sub.PlanID); err != nil {
			return false, err
		}
		plans[sub.PlanID] = pu
	}
	if pu.timeDependent {
		return true, nil
	}
	if !pu.recurring {
		return false, nil
	}
	// The billing day itself is billed by the periodic invoice, so the
	// carried-over usage is stored on the next day's run
	// (compute_all_service.rb:80-94, Subscriptions::BillingDateQuery at
	// timestamp - 1.day).
	env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
	if err != nil {
		return false, err
	}
	return env.Period().IsBillingDay(at.Add(-24 * time.Hour)), nil
}

// location returns the time zone of the customer of sub (its own billing
// entity's when it has none, then UTC), caching it in locs (lago
// Utils::Timezone.at_time_zone_sql on customer and billing entity).
func location(ctx context.Context, r port.Repos, sub *subscription.Subscription, locs map[core.CustomerID]*time.Location) (*time.Location, error) {
	if loc, ok := locs[sub.CustomerID]; ok {
		return loc, nil
	}
	env, err := kernel.LoadCustomerEnv(ctx, r, sub.OrganizationID, sub.CustomerID, core.BillingEntityID{})
	if err != nil {
		return nil, err
	}
	locs[sub.CustomerID] = env.Location
	return env.Location, nil
}

// inWindow reports whether a customer in loc is entering a new day at
// at: its local hour is 0, 1 or 2, so the hourly clock reaches every time
// zone once (compute_all_service.rb:158-162).
func inWindow(at time.Time, loc *time.Location) bool {
	return at.In(loc).Hour() <= 2
}

// Compute stores the usage of the subscription id for the day before
// timestamp in its customer's time zone, when its fees are not all zero,
// and returns it (lago DailyUsages::ComputeService,
// app/services/daily_usages/compute_service.rb). It stores nothing on a
// billing day, as the periodic invoice fills that day
// ([Service.FillFromInvoice]); a snapshot already stored for the day is
// returned as is. The usage is the current one, or the one at timestamp
// for a subscription terminated after it; its diff is against the
// previous snapshot of the same charges period. It returns nil when
// nothing is stored.
func (s *Service) Compute(ctx context.Context, org core.OrganizationID, id core.SubscriptionID, timestamp time.Time) (*subscription.DailyUsage, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*subscription.DailyUsage, error) {
		env, err := kernel.LoadSubscriptionEnv(ctx, r, org, id)
		if err != nil {
			return nil, err
		}
		sub, loc := env.Subscription, env.Location()
		today := core.DateOf(timestamp, loc)
		day := today.AddDays(-1)

		dates, err := env.Dates(timestamp, period.CurrentUsage)
		if err != nil {
			return nil, err
		}
		if core.DateOf(dates.PreviousPeriodStart(), loc) == today {
			return nil, nil
		}
		existing, err := r.DailyUsages.GetByDate(ctx, org, id, day)
		switch {
		case err == nil:
			return existing, nil
		case !errors.Is(err, core.ErrNotFound):
			return nil, fmt.Errorf("load daily usage of subscription %s for %s: %w", id, day, err)
		}

		// A subscription terminated after the job was enqueued is
		// computed at the run's instant, within its last period
		// (compute_service.rb:58-68).
		// The cache is skipped then, as its entries are not relevant to
		// the last period.
		at, skipCache := s.d.Now(), false
		if sub.Status == subscription.StatusTerminated && sub.TerminatedAt != nil && sub.TerminatedAt.After(timestamp) {
			at, skipCache = timestamp, true
		}
		u, err := s.usage.EnvUsage(ctx, r, env, usage.Options{SkipTaxes: true, AnyStatus: true, Timestamp: at, SkipCache: skipCache})
		if err != nil {
			return nil, err
		}
		fees := nonZero(u.Fees)
		if len(fees) == 0 {
			return nil, nil
		}
		snap, err := snapshot(ctx, newCatalogCache(r, org), headOf(u), fees)
		if err != nil {
			return nil, err
		}
		prev, err := previous(ctx, r, org, id, snap.From, snap.To, day)
		if err != nil {
			return nil, err
		}
		return s.store(ctx, r, sub, snap, day, timestamp, diffOf(snap, prev))
	})
}

// headOf returns the period and totals of a current usage.
func headOf(u *usage.Usage) head {
	return head{
		from: u.From, to: u.To, issuingDate: u.IssuingDate, currency: u.Currency,
		amount: u.Amount, taxes: u.TaxesAmount, total: u.TotalAmount,
	}
}
