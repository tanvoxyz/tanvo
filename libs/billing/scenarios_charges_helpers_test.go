package billing_test

import (
	"context"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// chgSubscribe creates a subscription of c to p at the current instant
// through the lifecycle service (lago create_subscription), then drains.
func chgSubscribe(s *scenario, c *customer.Customer, p *catalog.Plan, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	s.T.Helper()
	id := core.NewID[core.SubscriptionID](s.IDs)
	sub := subscription.New(s.OrgID(), id, c.ID, p.ID, c.ExternalID, s.Now(), subscription.PlanFacts{PayInAdvance: p.PayInAdvance}, s.Now())
	for _, o := range opts {
		o(sub)
	}
	out, err := s.E.Lifecycle.Create(s.Ctx, sub)
	s.must(err)
	s.drain()
	return out
}

// chgReloadSub reloads a subscription.
func chgReloadSub(s *scenario, id core.SubscriptionID) *subscription.Subscription {
	s.T.Helper()
	var out *subscription.Subscription
	s.tx(func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = r.Subscriptions.Get(ctx, s.OrgID(), id)
		return err
	})
	return out
}

// chgSubInvoices returns the invoices of a subscription by ascending
// sequential ID (lago subscription.invoices.order(:sequential_id)).
func chgSubInvoices(s *scenario, id core.SubscriptionID) []*invoice.Invoice {
	s.T.Helper()
	var out []*invoice.Invoice
	s.tx(func(ctx context.Context, r port.Repos) error {
		iss, err := r.InvoiceSubscriptions.ListBySubscription(ctx, s.OrgID(), id)
		if err != nil {
			return err
		}
		var ids []core.InvoiceID
		for _, is := range iss {
			if !slices.Contains(ids, is.InvoiceID) {
				ids = append(ids, is.InvoiceID)
			}
		}
		out, err = r.Invoices.ListByIDs(ctx, s.OrgID(), ids)
		return err
	})
	slices.SortStableFunc(out, func(a, b *invoice.Invoice) int {
		return int(chgSeq(a) - chgSeq(b))
	})
	return out
}

// chgSeq is the sequential ID of inv, 0 for a draft without one; ties keep
// creation order.
func chgSeq(inv *invoice.Invoice) int64 {
	if inv.SequentialID == nil {
		return 1 << 40
	}
	return *inv.SequentialID
}

// chgLastInvoice is the subscription's invoice with the highest sequential
// ID, or nil.
func chgLastInvoice(s *scenario, id core.SubscriptionID) *invoice.Invoice {
	s.T.Helper()
	invs := chgSubInvoices(s, id)
	if len(invs) == 0 {
		return nil
	}
	return invs[len(invs)-1]
}

// chgFeesOf returns the fees of inv of type t.
func chgFeesOf(s *scenario, inv *invoice.Invoice, t core.FeeType) []*invoice.Fee {
	s.T.Helper()
	if inv == nil {
		return nil
	}
	var out []*invoice.Fee
	for _, f := range s.Fees(inv.ID) {
		if f.Type == t {
			out = append(out, f)
		}
	}
	return out
}

// chgSum is the total amount of fees.
func chgSum(fs []*invoice.Fee) int64 {
	var n int64
	for _, f := range fs {
		n += f.Amount
	}
	return n
}

// chgAllFees returns every fee of the organization's subscription sub,
// with or without invoice.
func chgAllFees(s *scenario, sub core.SubscriptionID) []*invoice.Fee {
	s.T.Helper()
	var out []*invoice.Fee
	s.tx(func(ctx context.Context, r port.Repos) error {
		fs, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Fee], error) {
			return r.Fees.List(ctx, s.OrgID(), invoice.FeeFilter{SubscriptionID: sub}, p)
		})
		out = fs
		return err
	})
	return out
}
