package billing_test

import (
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// subsParams are the create_subscription parameters of the lago scenarios.
type subsParams struct {
	externalID     string
	billingTime    period.BillingTime
	subscriptionAt *time.Time
	endingAt       *time.Time
	opts           []func(*subscription.Subscription)
	// paymentRule stores a payment activation rule with that timeout
	// (hours) before the creation; nil stores none.
	paymentRule *int
}

// subsCreate subscribes c to p at the clock's instant through the
// lifecycle (lago create_subscription), then drains the jobs. It returns
// the resulting subscription.
func (s *scenario) subsCreate(c *customer.Customer, p *catalog.Plan, in subsParams) *subscription.Subscription {
	s.T.Helper()
	ext := in.externalID
	if ext == "" {
		ext = c.ExternalID
	}
	at := s.Now()
	if in.subscriptionAt != nil {
		at = *in.subscriptionAt
	}
	sub := subscription.New(s.OrgID(), core.NewID[core.SubscriptionID](s.IDs), c.ID, p.ID, ext, at,
		subscription.PlanFacts{PayInAdvance: p.PayInAdvance}, s.Now())
	if in.billingTime != 0 {
		sub.BillingTime = in.billingTime
	}
	sub.EndingAt = in.endingAt
	for _, o := range in.opts {
		o(sub)
	}
	if in.paymentRule != nil {
		rule := subscription.NewActivationRule(s.OrgID(), core.NewID[core.ActivationRuleID](s.IDs), sub.ID,
			subscription.ActivationRuleTypePayment, *in.paymentRule, s.Now())
		s.tx(func(ctx context.Context, r port.Repos) error { return r.ActivationRules.Create(ctx, rule) })
	}
	out, err := s.E.Lifecycle.Create(s.Ctx, sub)
	s.must(err)
	s.drain()
	return s.subsReload(out.ID)
}

// subsReload reloads a subscription.
func (s *scenario) subsReload(id core.SubscriptionID) *subscription.Subscription {
	s.T.Helper()
	var out *subscription.Subscription
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Subscriptions.Get(ctx, s.OrgID(), id)
		return err
	})
	return out
}

// subsOfCustomer returns the subscriptions of c in creation order.
func (s *scenario) subsOfCustomer(c *customer.Customer) []*subscription.Subscription {
	s.T.Helper()
	var out []*subscription.Subscription
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Subscriptions.ListByCustomer(ctx, s.OrgID(), c.ID)
		return err
	})
	slices.SortStableFunc(out, func(a, b *subscription.Subscription) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// subsInvoiceSubs returns the invoice subscriptions of sub, oldest first.
func (s *scenario) subsInvoiceSubs(sub *subscription.Subscription) []*invoice.Subscription {
	s.T.Helper()
	var out []*invoice.Subscription
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.InvoiceSubscriptions.ListBySubscription(ctx, s.OrgID(), sub.ID)
		return err
	})
	slices.SortStableFunc(out, func(a, b *invoice.Subscription) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// subsInvoices returns the visible invoices billing sub (lago
// subscription.invoices), oldest first, optionally of the types.
func (s *scenario) subsInvoices(sub *subscription.Subscription, types ...invoice.Type) []*invoice.Invoice {
	s.T.Helper()
	var out []*invoice.Invoice
	seen := map[core.InvoiceID]bool{}
	for _, is := range s.subsInvoiceSubs(sub) {
		if seen[is.InvoiceID] {
			continue
		}
		seen[is.InvoiceID] = true
		inv := s.Invoice(is.InvoiceID)
		if inv.Status == invoice.StatusGenerating {
			continue
		}
		if len(types) > 0 && !slices.Contains(types, inv.Type) {
			continue
		}
		out = append(out, inv)
	}
	return out
}

// subsLastInvoiceSub returns the invoice subscription of sub on its last
// invoice.
func (s *scenario) subsLastInvoiceSub(sub *subscription.Subscription) *invoice.Subscription {
	s.T.Helper()
	all := s.subsInvoiceSubs(sub)
	if len(all) == 0 {
		s.T.Fatalf("subscription %s has no invoice", sub.ID)
	}
	return all[len(all)-1]
}

// subsChargeFees returns the charge fees of an invoice.
func (s *scenario) subsFeesOf(id core.InvoiceID, t core.FeeType) []*invoice.Fee {
	var out []*invoice.Fee
	for _, f := range s.Fees(id) {
		if f.Type == t {
			out = append(out, f)
		}
	}
	return out
}

// subsSum sums the amounts of fees.
func subsSum(fs []*invoice.Fee) int64 {
	var n int64
	for _, f := range fs {
		n += f.Amount
	}
	return n
}

// subsTime parses an RFC 3339 instant; lago's inclusive "…:59:59" ends
// become Tanvo's exclusive ends one second later.
func subsTime(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		panic(err)
	}
	if t.Second() == 59 && t.Minute() == 59 {
		t = t.Add(time.Second)
	}
	return t.UTC()
}

// subsAt parses an RFC 3339 instant as is.
func subsAt(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

// subsTerminate terminates sub now through the lifecycle (lago
// terminate_subscription), then drains.
func (s *scenario) subsTerminate(sub *subscription.Subscription, opts ...func(*subsTerminateInput)) *subscription.Subscription {
	s.T.Helper()
	in := subsTerminateInput{}
	for _, o := range opts {
		o(&in)
	}
	out, err := s.E.Lifecycle.Terminate(s.Ctx, lifecycle.TerminateInput{OrganizationID: s.OrgID(), SubscriptionID: sub.ID, At: in.at, OnTerminationInvoice: in.onInvoice, OnTerminationCreditNote: in.onCreditNote})
	s.must(err)
	s.drain()
	return s.subsReload(out.ID)
}

// subsTerminateInput are the terminate_subscription parameters.
type subsTerminateInput struct {
	at           time.Time
	onInvoice    subscription.TerminationInvoice
	onCreditNote subscription.TerminationCreditNote
}

// subsActivatePending runs the pending activation at the clock's instant.
func (s *scenario) subsActivatePending() {
	s.T.Helper()
	s.run(kernel.JobActivatePendingSubscriptions, kernel.InstantArgs{At: s.Now()})
}

// subsTerminateEnded runs the termination of ended subscriptions now.
func (s *scenario) subsTerminateEnded() {
	s.T.Helper()
	s.run(kernel.JobTerminateEndedSubscriptions, kernel.InstantArgs{At: s.Now()})
}

// subsEndTrials runs the end of trials at the clock's instant (lago
// FreeTrialSubscriptionsBillerJob).
func (s *scenario) subsEndTrials() {
	s.T.Helper()
	s.run(kernel.JobEndTrials, kernel.InstantArgs{At: s.Now()})
}

// subsBilling is lago perform_billing: the periodic billing and the
// free-trial biller.
func (s *scenario) subsBilling() {
	s.T.Helper()
	s.performBilling()
	s.subsEndTrials()
}

func subsPtr[T any](v T) *T { return &v }

// subsRules returns the activation rule statuses of sub.
func (s *scenario) subsRules(sub *subscription.Subscription) []subscription.ActivationRuleStatus {
	s.T.Helper()
	var out []subscription.ActivationRuleStatus
	s.tx(func(ctx context.Context, r port.Repos) error {
		rules, err := r.ActivationRules.ListBySubscription(ctx, s.OrgID(), sub.ID)
		for _, x := range rules {
			out = append(out, x.Status)
		}
		return err
	})
	return out
}

// subsLifetimeUsage returns the lifetime usage of sub.
func (s *scenario) subsLifetimeUsage(sub *subscription.Subscription) *subscription.LifetimeUsage {
	s.T.Helper()
	var out *subscription.LifetimeUsage
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.LifetimeUsages.GetBySubscription(ctx, s.OrgID(), sub.ID)
		return err
	})
	return out
}

// subsUsageUpdate is lago perform_usage_update: the lifetime usage refresh
// of every active subscription of c.
func (s *scenario) subsUsageUpdate(c *customer.Customer) {
	s.T.Helper()
	for _, sub := range s.subsOfCustomer(c) {
		if sub.Status != subscription.StatusActive {
			continue
		}
		_, err := s.E.CheckUsageThresholds(s.Ctx, s.OrgID(), sub.ID)
		s.must(err)
	}
	s.drain()
}
