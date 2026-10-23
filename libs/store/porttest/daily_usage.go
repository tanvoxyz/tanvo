package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// testDailyUsage checks the daily usage snapshots, the subscription's
// skip_daily_usage and last_received_event_on, and the daily usage work
// scan.
func testDailyUsage(t *testing.T, h Harness) {
	t.Run("daily usages", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, other := subscriptionFixtures(e)
		org := p.org
		sub := p.newSub(e, "daily")
		otherSub := other.newSub(e, "daily")
		from, to := at(-10*24*time.Hour), at(20*24*time.Hour)
		day := core.NewDate(2025, time.March, 13)
		snapshot := subscription.UsageSnapshot{
			From: from, To: to, IssuingDate: core.NewDate(2025, time.April, 3), Currency: "EUR",
			Amount: 1200, TotalAmount: 1200,
			Charges: []subscription.ChargeUsage{{
				UsageValues: subscription.UsageValues{Units: money.MustParse("12.5"), TotalAggregatedUnits: money.MustParse("12.5"), EventsCount: 3, Amount: 1200},
				Currency:    "EUR",
				Charge:      subscription.ChargeRef{ID: core.NewID[core.ChargeID](e.ids), ChargeModel: "standard"},
				BillableMetric: subscription.MetricRef{
					ID: core.NewID[core.BillableMetricID](e.ids), Name: "Calls", Code: "calls", AggregationType: "sum_agg",
				},
				Grouped: []subscription.GroupedUsage{{
					UsageValues: subscription.UsageValues{Units: money.MustParse("12.5"), TotalAggregatedUnits: money.MustParse("0"), Amount: 1200},
					GroupedBy:   map[string]string{"region": "eu"},
				}},
				PresentationBreakdowns: []subscription.PresentationBreakdownUsage{{PresentationBy: map[string]*string{"team": ptr("a"), "env": nil}, Units: money.MustParse("2")}},
			}},
		}
		newUsage := func(s *subscription.Subscription, d core.Date, diff *subscription.UsageSnapshot) *subscription.DailyUsage {
			return &subscription.DailyUsage{
				ID: core.NewID[core.DailyUsageID](e.ids), OrganizationID: s.OrganizationID, CustomerID: s.CustomerID,
				SubscriptionID: s.ID, ExternalSubscriptionID: s.ExternalID, Usage: snapshot, UsageDiff: diff,
				From: from, To: to, RefreshedAt: at(-time.Hour), UsageDate: d, CreatedAt: now, UpdatedAt: now,
			}
		}
		first := newUsage(sub, day.AddDays(-3), nil)
		second := newUsage(sub, day.AddDays(-1), &snapshot)
		third := newUsage(sub, day, &snapshot)
		third.From = from.Add(time.Hour)
		foreign := newUsage(otherSub, day, &snapshot)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, sub))
			noErr(t, "Subscriptions.Create() other", r.Subscriptions.Create(ctx, otherSub))
			for _, d := range []*subscription.DailyUsage{first, second, third, foreign} {
				noErr(t, "DailyUsages.Create()", r.DailyUsages.Create(ctx, d))
			}
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.DailyUsages.GetByDate(ctx, org, sub.ID, day.AddDays(-1))
			noErr(t, "GetByDate()", err)
			equal(t, "GetByDate()", got, second)
			got, err = r.DailyUsages.GetByDate(ctx, org, sub.ID, day.AddDays(-3))
			noErr(t, "GetByDate() first", err)
			equal(t, "GetByDate() first (empty diff)", got, first)
			_, err = r.DailyUsages.GetByDate(ctx, org, sub.ID, day.AddDays(-2))
			wantErr(t, "GetByDate() missing day", err, core.ErrNotFound)
			_, err = r.DailyUsages.GetByDate(ctx, other.org, sub.ID, day)
			wantErr(t, "GetByDate() other organization", err, core.ErrNotFound)

			got, err = r.DailyUsages.GetByPeriodAndDate(ctx, org, sub.ID, from.Add(time.Hour), to, day)
			noErr(t, "GetByPeriodAndDate()", err)
			equal(t, "GetByPeriodAndDate()", got.ID, third.ID)
			_, err = r.DailyUsages.GetByPeriodAndDate(ctx, org, sub.ID, from, to, day)
			wantErr(t, "GetByPeriodAndDate() other period", err, core.ErrNotFound)

			got, err = r.DailyUsages.Previous(ctx, org, sub.ID, from, to, day.AddDays(1))
			noErr(t, "Previous()", err)
			equal(t, "Previous() (latest day of the period)", got.ID, second.ID)
			got, err = r.DailyUsages.Previous(ctx, org, sub.ID, from, to, day.AddDays(-1))
			noErr(t, "Previous() before second", err)
			equal(t, "Previous() before second", got.ID, first.ID)
			_, err = r.DailyUsages.Previous(ctx, org, sub.ID, from, to, day.AddDays(-3))
			wantErr(t, "Previous() before first", err, core.ErrNotFound)

			days, err := r.DailyUsages.ListDaysBetween(ctx, org, day.AddDays(-2), day)
			noErr(t, "ListDaysBetween()", err)
			equal(t, "ListDaysBetween()", days, []subscription.DailyUsageDay{
				{SubscriptionID: sub.ID, UsageDate: day.AddDays(-1)}, {SubscriptionID: sub.ID, UsageDate: day},
			})

			all := listAll(t, 2, func(page core.PageRequest) (core.Page[*subscription.DailyUsage], error) {
				return r.DailyUsages.ListBySubscription(ctx, org, sub.ID, page)
			})
			equal(t, "ListBySubscription()", ids(all, func(d *subscription.DailyUsage) core.DailyUsageID { return d.ID }),
				[]core.DailyUsageID{first.ID, second.ID, third.ID})
			return nil
		})
	})

	t.Run("event received and skip flag", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		p, _ := subscriptionFixtures(e)
		org := p.org
		active := p.newSub(e, "received")
		active.SkipDailyUsage = true
		pending := p.newSub(e, "received-pending")
		pending.Status, pending.StartedAt = subscription.StatusPending, nil
		day := core.NewDate(2025, time.March, 14)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, active))
			noErr(t, "Subscriptions.Create() pending", r.Subscriptions.Create(ctx, pending))
			noErr(t, "RecordEventReceived()", r.Subscriptions.RecordEventReceived(ctx, org, active.ID, day))
			noErr(t, "RecordEventReceived() pending", r.Subscriptions.RecordEventReceived(ctx, org, pending.ID, day))
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Subscriptions.Get(ctx, org, active.ID)
			noErr(t, "Get()", err)
			equal(t, "LastReceivedEventOn", got.LastReceivedEventOn, &day)
			equal(t, "SkipDailyUsage", got.SkipDailyUsage, true)
			got, err = r.Subscriptions.Get(ctx, org, pending.ID)
			noErr(t, "Get() pending", err)
			equal(t, "LastReceivedEventOn of a pending subscription", got.LastReceivedEventOn, (*core.Date)(nil))

			// Update writes neither a stale nor a missing day.
			stale := *active
			stale.LastReceivedEventOn = nil
			stale.SkipDailyUsage = false
			noErr(t, "Update()", r.Subscriptions.Update(ctx, &stale))
			got, err = r.Subscriptions.Get(ctx, org, active.ID)
			noErr(t, "Get() after update", err)
			equal(t, "LastReceivedEventOn after update", got.LastReceivedEventOn, &day)
			equal(t, "SkipDailyUsage after update", got.SkipDailyUsage, false)
			return nil
		})
	})

	t.Run("organizations with daily usages", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		pa, pb := subscriptionFixtures(e)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, pa.newSub(e, "work-daily")))
			skipped := pb.newSub(e, "work-daily-skipped")
			skipped.SkipDailyUsage = true
			noErr(t, "Subscriptions.Create() skipped", r.Subscriptions.Create(ctx, skipped))
			return nil
		})
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Organizations.WithWork(ctx, organization.WorkDailyUsages, now)
			noErr(t, "WithWork()", err)
			equal(t, "WithWork(daily_usages)", got, []core.OrganizationID{pa.org})
			return nil
		})
	})
}
