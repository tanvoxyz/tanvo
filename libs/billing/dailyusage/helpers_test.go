package dailyusage_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/dailyusage"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// fixture is a harness with the daily usage service, a customer and a
// monthly plan.
type fixture struct {
	*billingtest.Harness
	svc  *dailyusage.Service
	cust *customer.Customer
	plan *catalog.Plan
}

func newFixture(t *testing.T, now time.Time, custOpts ...func(*customer.Customer)) *fixture {
	t.Helper()
	h := billingtest.New(t, now)
	f := &fixture{Harness: h}
	f.svc = dailyusage.New(h.Deps, usage.New(h.Deps))
	f.cust = f.Customer(custOpts...)
	f.plan = f.Plan(period.Monthly, 0)
	return f
}

// sub creates an active subscription of the fixture's customer to p,
// subscribed at start.
func (f *fixture) sub(p *catalog.Plan, start time.Time, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	f.T.Helper()
	return f.Subscription(f.cust, p, append([]func(*subscription.Subscription){billingtest.SubscribedAt(start)}, opts...)...)
}

// receivedOn records an event of s received on day d (none when nil).
func (f *fixture) receivedOn(s *subscription.Subscription, d *core.Date) {
	f.T.Helper()
	if d == nil {
		return
	}
	f.Do(func(ctx context.Context, r port.Repos) error {
		return r.Subscriptions.RecordEventReceived(ctx, f.OrgID(), s.ID, *d)
	})
}

func datePtr(d core.Date) *core.Date { return &d }

func paris(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// jobs returns the pending jobs of kind.
func (f *fixture) jobs(kind string) []port.Job {
	var out []port.Job
	for _, j := range f.Store.Jobs() {
		if j.Kind == kind {
			out = append(out, j)
		}
	}
	return out
}

// computeArgs returns the args of the pending compute jobs by subscription.
func (f *fixture) computeArgs() map[core.SubscriptionID]port.Job {
	f.T.Helper()
	out := map[core.SubscriptionID]port.Job{}
	for _, j := range f.jobs(kernel.JobComputeDailyUsage) {
		var a kernel.DailyUsageArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			f.T.Fatal(err)
		}
		out[a.SubscriptionID] = j
	}
	return out
}

// usages returns the stored snapshots of s, by day.
func (f *fixture) usages(s *subscription.Subscription) []*subscription.DailyUsage {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.DailyUsage, error) {
		p, err := r.DailyUsages.ListBySubscription(ctx, f.OrgID(), s.ID, core.PageRequest{Size: 1000})
		return p.Items, err
	})
}

// storeUsage stores a snapshot of s for day.
func (f *fixture) storeUsage(s *subscription.Subscription, day core.Date, opts ...func(*subscription.DailyUsage)) *subscription.DailyUsage {
	f.T.Helper()
	du := &subscription.DailyUsage{
		ID: core.NewID[core.DailyUsageID](f.IDs), OrganizationID: f.OrgID(), CustomerID: s.CustomerID,
		SubscriptionID: s.ID, ExternalSubscriptionID: s.ExternalID,
		Usage: subscription.UsageSnapshot{Charges: []subscription.ChargeUsage{}},
		From:  day.Midnight(time.UTC), To: day.AddDays(1).Midnight(time.UTC), RefreshedAt: f.Now(), UsageDate: day,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	for _, o := range opts {
		o(du)
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.DailyUsages.Create(ctx, du) })
	return du
}

// terminate is an update terminating a subscription at at.
func terminate(at time.Time) func(*subscription.Subscription) {
	return func(s *subscription.Subscription) { s.Status, s.TerminatedAt = subscription.StatusTerminated, &at }
}
