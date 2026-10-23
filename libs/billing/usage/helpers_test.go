package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// fixture is a harness with the usage service, a customer, a monthly plan
// with a count metric charged amount per unit and an active subscription.
type fixture struct {
	*billingtest.Harness
	svc    *usage.Service
	cust   *customer.Customer
	plan   *catalog.Plan
	metric *catalog.BillableMetric
	charge *catalog.Charge
	sub    *subscription.Subscription
}

func newFixture(t *testing.T, now time.Time, amount string, subOpts ...func(*subscription.Subscription)) *fixture {
	t.Helper()
	h := billingtest.New(t, now)
	f := &fixture{Harness: h, svc: usage.New(h.Deps)}
	f.cust = f.Customer()
	f.plan = f.Plan(period.Monthly, 0)
	f.metric = f.Metric("calls", catalog.AggregationTypeCount, "")
	f.charge = f.Charge(f.plan, f.metric, catalog.ChargeModelStandard, `{"amount":"`+amount+`"}`)
	opts := append([]func(*subscription.Subscription){billingtest.SubscribedAt(now.AddDate(0, -6, 0))}, subOpts...)
	f.sub = f.Subscription(f.cust, f.plan, opts...)
	return f
}

// events stores n events of the metric at ts.
func (f *fixture) events(s *subscription.Subscription, n int, ts time.Time) {
	f.T.Helper()
	for range n {
		f.Event(s, f.metric.Code, ts)
	}
}

// lifetime stores a lifetime usage of s.
func (f *fixture) lifetime(s *subscription.Subscription, opts ...func(*subscription.LifetimeUsage)) *subscription.LifetimeUsage {
	f.T.Helper()
	lu := &subscription.LifetimeUsage{
		ID: core.NewID[core.LifetimeUsageID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: s.ID,
		ExternalSubscriptionID: s.ExternalID, Currency: billingtest.USD, CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	for _, o := range opts {
		o(lu)
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.LifetimeUsages.Upsert(ctx, lu) })
	return lu
}

// reload returns the stored lifetime usage of the chain of s.
func (f *fixture) reload(s *subscription.Subscription) *subscription.LifetimeUsage {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*subscription.LifetimeUsage, error) {
		return r.LifetimeUsages.GetByExternalID(ctx, f.OrgID(), s.ExternalID)
	})
}

// invoiceOf stores an invoice of type typ and status st linked to s for the
// month holding the clock.
func (f *fixture) invoiceOf(s *subscription.Subscription, typ invoice.Type, st invoice.Status, opts ...func(*invoice.Invoice)) *invoice.Invoice {
	f.T.Helper()
	inv := f.StoredInvoice(f.cust, append([]func(*invoice.Invoice){func(i *invoice.Invoice) { i.Type, i.Status = typ, st }}, opts...)...)
	now := f.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	reason := invoice.InvoicingReasonSubscriptionPeriodic
	if typ == invoice.TypeProgressiveBilling {
		reason = invoice.InvoicingReasonProgressiveBilling
	}
	f.LinkSubscription(inv, s, reason, from, from.AddDate(0, 1, 0))
	return inv
}

// subscriptionNamed is a subscription option setting the external ID.
func subscriptionNamed(id string) func(*subscription.Subscription) {
	return func(s *subscription.Subscription) { s.ExternalID = id }
}

// status is a subscription option setting the status.
func status(st subscription.Status) func(*subscription.Subscription) {
	return func(s *subscription.Subscription) { s.Status = st }
}
