package billing_test

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// rootSubscribe creates, through the lifecycle service, a subscription of
// c to p subscribed now (lago create_subscription), then drains the jobs.
func (s *scenario) rootSubscribe(c *customer.Customer, p *catalog.Plan, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	s.T.Helper()
	id := core.NewID[core.SubscriptionID](s.IDs)
	sub := subscription.New(s.OrgID(), id, c.ID, p.ID, "sub_"+id.String()[30:], s.Now(), subscription.PlanFacts{PayInAdvance: p.PayInAdvance}, s.Now())
	for _, o := range opts {
		o(sub)
	}
	out, err := s.E.Lifecycle.Create(s.Ctx, sub)
	s.must(err)
	s.drain()
	return s.rootReload(out.ID)
}

// rootTerminate terminates sub now (lago terminate_subscription), then
// drains the jobs.
func (s *scenario) rootTerminate(sub *subscription.Subscription, opts ...func(*lifecycle.TerminateInput)) *subscription.Subscription {
	s.T.Helper()
	in := lifecycle.TerminateInput{OrganizationID: s.OrgID(), SubscriptionID: sub.ID}
	for _, o := range opts {
		o(&in)
	}
	_, err := s.E.Lifecycle.Terminate(s.Ctx, in)
	s.must(err)
	s.drain()
	return s.rootReload(sub.ID)
}

// rootReload reloads a subscription.
func (s *scenario) rootReload(id core.SubscriptionID) *subscription.Subscription {
	s.T.Helper()
	var out *subscription.Subscription
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Subscriptions.Get(ctx, s.OrgID(), id)
		return err
	})
	return out
}

// rootInvoices returns the invoices of c by ascending creation, those of
// sub only when sub is set.
func (s *scenario) rootInvoices(c *customer.Customer, sub *subscription.Subscription) []*invoice.Invoice {
	s.T.Helper()
	all := s.Invoices(c.ID)
	if sub == nil {
		return all
	}
	var out []*invoice.Invoice
	for _, inv := range all {
		var linked bool
		s.tx(func(ctx context.Context, r port.Repos) error {
			iss, err := r.InvoiceSubscriptions.ListByInvoice(ctx, s.OrgID(), inv.ID)
			for _, is := range iss {
				linked = linked || is.SubscriptionID == sub.ID
			}
			return err
		})
		if linked {
			out = append(out, inv)
		}
	}
	return out
}

// rootTimezone is a customer option setting its IANA time zone.
func rootTimezone(name string) func(*customer.Customer) {
	return func(c *customer.Customer) {
		loc, err := time.LoadLocation(name)
		if err != nil {
			panic(err)
		}
		c.Timezone = loc
	}
}

// rootInAdvance is a plan option making it paid in advance.
func rootInAdvance(p *catalog.Plan) { p.PayInAdvance = true }

// rootFeesSplit returns the fees of an invoice split into the charge fees
// without true-up parent and the true-up fees.
func (s *scenario) rootFeesSplit(id core.InvoiceID) (usage, trueUps []*invoice.Fee) {
	s.T.Helper()
	for _, f := range s.Fees(id) {
		if f.TrueUpParentFeeID.IsZero() {
			usage = append(usage, f)
		} else {
			trueUps = append(trueUps, f)
		}
	}
	return usage, trueUps
}

// rootRefresh refreshes a draft invoice (lago refresh_invoice).
func (s *scenario) rootRefresh(inv *invoice.Invoice) *invoice.Invoice {
	s.T.Helper()
	_, err := s.E.Invoices.RefreshDraft(s.Ctx, s.OrgID(), inv.ID)
	s.must(err)
	s.drain()
	return s.Invoice(inv.ID)
}

// rootFinalize finalizes a draft invoice (lago finalize_invoice).
func (s *scenario) rootFinalize(inv *invoice.Invoice) *invoice.Invoice {
	s.T.Helper()
	_, err := s.E.Invoices.FinalizeDraft(s.Ctx, s.OrgID(), inv.ID)
	s.must(err)
	s.drain()
	return s.Invoice(inv.ID)
}

// rootGrace is a customer option setting its invoice grace period.
func rootGrace(days int) func(*customer.Customer) {
	return func(c *customer.Customer) { c.InvoiceGracePeriodDays = &days }
}

// rootMinAmount is a charge option setting its spending minimum.
func rootMinAmount(amount int64) func(*catalog.Charge) {
	return func(c *catalog.Charge) { c.MinAmount = amount }
}

// rootBill bills sub now with the periodic reason (lago
// BillSubscriptionJob.perform_now), then drains the jobs.
func (s *scenario) rootBill(subs ...*subscription.Subscription) {
	s.T.Helper()
	a := kernel.BillSubscriptionArgs{BillingAt: s.Now(), Reason: string(invoice.InvoicingReasonSubscriptionPeriodic)}
	for _, sub := range subs {
		a.SubscriptionIDs = append(a.SubscriptionIDs, sub.ID)
	}
	s.run(kernel.JobBillSubscription, a)
}

// rootUsage returns the current usage of sub now (lago
// fetch_current_usage).
func (s *scenario) rootUsage(sub *subscription.Subscription, opts usage.Options) *usage.Usage {
	s.T.Helper()
	var out *usage.Usage
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = s.E.Usage.CurrentUsage(ctx, r, s.OrgID(), sub.ID, opts)
		return err
	})
	return out
}
