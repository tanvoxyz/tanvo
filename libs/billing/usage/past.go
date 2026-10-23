package usage

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// PastUsageFilter selects the past usage periods of a subscription.
type PastUsageFilter struct {
	// CustomerID is the customer; mandatory.
	CustomerID core.CustomerID
	// ExternalSubscriptionID is the subscription chain; mandatory.
	ExternalSubscriptionID string
	// PeriodsCount keeps the latest periods only; 0 keeps them all.
	PeriodsCount int
	// BillableMetricID keeps the fees of one metric; zero keeps them all.
	BillableMetricID core.BillableMetricID
}

// PastPeriod is one invoiced usage period.
type PastPeriod struct {
	// InvoiceSubscription is the billed period.
	InvoiceSubscription *invoice.Subscription
	// Invoice is the invoice that billed it.
	Invoice *invoice.Invoice
	// Fees are the charge fees of the chain on that invoice.
	Fees []*invoice.Fee
}

// PastUsage returns the invoiced usage periods of a subscription chain,
// latest charges period first (lago PastUsageQuery).
func (s *Service) PastUsage(ctx context.Context, r port.Repos, org core.OrganizationID, f PastUsageFilter) ([]PastPeriod, error) {
	if f.CustomerID.IsZero() || f.ExternalSubscriptionID == "" {
		return nil, fmt.Errorf("past usage: customer and external subscription ID are mandatory: %w", core.ErrInvalidValue)
	}
	subs, err := kernel.All(func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
		return r.Subscriptions.List(ctx, org, subscription.Filter{CustomerID: f.CustomerID, ExternalID: f.ExternalSubscriptionID}, p)
	})
	if err != nil {
		return nil, fmt.Errorf("list subscriptions of %s: %w", f.ExternalSubscriptionID, err)
	}
	ids := make([]core.SubscriptionID, len(subs))
	var rows []*invoice.Subscription
	for i, sub := range subs {
		ids[i] = sub.ID
		iss, err := r.InvoiceSubscriptions.ListBySubscription(ctx, org, sub.ID)
		if err != nil {
			return nil, fmt.Errorf("list invoice subscriptions of %s: %w", sub.ID, err)
		}
		for _, is := range iss {
			if !is.ChargesPeriod.IsZero() {
				rows = append(rows, is)
			}
		}
	}
	// Latest charges period first, then latest created, then by ID (lago
	// PastUsageQuery#query and BaseQuery#apply_consistent_ordering), so
	// pages and PeriodsCount cut the same rows on every call.
	slices.SortFunc(rows, func(a, b *invoice.Subscription) int {
		if c := b.ChargesPeriod.From.Compare(a.ChargesPeriod.From); c != 0 {
			return c
		}
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	if f.PeriodsCount > 0 && len(rows) > f.PeriodsCount {
		rows = rows[:f.PeriodsCount]
	}
	charges := map[core.ChargeID]core.BillableMetricID{}
	out := make([]PastPeriod, 0, len(rows))
	for _, is := range rows {
		inv, err := r.Invoices.Get(ctx, org, is.InvoiceID)
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", is.InvoiceID, err)
		}
		fs, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return nil, fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
		}
		var kept []*invoice.Fee
		for _, fee := range fs {
			if fee.Type != core.FeeTypeCharge || !slices.Contains(ids, fee.SubscriptionID) {
				continue
			}
			if !f.BillableMetricID.IsZero() {
				m, ok := charges[fee.ChargeID]
				if !ok {
					c, err := r.Charges.Get(ctx, org, fee.ChargeID)
					if err != nil {
						return nil, fmt.Errorf("load charge %s: %w", fee.ChargeID, err)
					}
					m, charges[fee.ChargeID] = c.BillableMetricID, c.BillableMetricID
				}
				if m != f.BillableMetricID {
					continue
				}
			}
			kept = append(kept, fee)
		}
		out = append(out, PastPeriod{InvoiceSubscription: is, Invoice: inv, Fees: kept})
	}
	return out, nil
}
