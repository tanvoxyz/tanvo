package usage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var _ wallets.UsageSource = (*Service)(nil)

// OngoingUsage implements [wallets.UsageSource] (lago
// Customers::RefreshWalletsService inputs): for every active subscription
// of customer c, its current usage fees (amount + taxes) and, negated, the
// pay-in-advance ones among them and the fees of the progressive billing
// invoices of the current period (subtotal + taxes); then the fees of the
// customer's draft invoices with a non-zero total (amount + taxes −
// coupons).
func (s *Service) OngoingUsage(ctx context.Context, r port.Repos, c *customer.Customer) ([]wallets.UsageFee, error) {
	org := c.OrganizationID
	charges := chargeCache{r: r, org: org}
	usages, err := s.CustomerUsage(ctx, r, org, c.ID, Options{})
	if err != nil {
		return nil, err
	}
	var out []wallets.UsageFee
	add := func(f *invoice.Fee, amount int64) error {
		uf := wallets.UsageFee{FeeType: f.Type, Currency: f.Currency, Amount: amount}
		if f.Type == core.FeeTypeCharge && !f.ChargeID.IsZero() {
			ch, err := charges.get(ctx, f.ChargeID)
			if err != nil {
				return err
			}
			uf.BillableMetricID = ch.metric
		}
		out = append(out, uf)
		return nil
	}
	now := s.d.Now()
	for _, u := range usages {
		for _, f := range u.Fees {
			if err := add(f, f.TotalAmount()); err != nil {
				return nil, err
			}
		}
		for _, f := range u.Fees {
			ch, err := charges.get(ctx, f.ChargeID)
			if err != nil {
				return nil, err
			}
			if !ch.inAdvance {
				continue
			}
			if err := add(f, -f.TotalAmount()); err != nil {
				return nil, err
			}
		}
		pbs, err := progressiveFees(ctx, r, u, now)
		if err != nil {
			return nil, err
		}
		for _, f := range pbs {
			if err := add(f, -(kernel.RoundCents(f.SubTotalExcludingTaxesPreciseAmount()) + f.TaxesAmount)); err != nil {
				return nil, err
			}
		}
	}
	drafts, err := kernel.All(func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
		return r.Invoices.List(ctx, org, invoice.Filter{CustomerID: c.ID, Statuses: []invoice.Status{invoice.StatusDraft}}, p)
	})
	if err != nil {
		return nil, fmt.Errorf("list draft invoices of customer %s: %w", c.ID, err)
	}
	for _, inv := range drafts {
		if inv.TotalAmount == 0 {
			continue
		}
		fs, err := r.Fees.ListByInvoice(ctx, org, inv.ID)
		if err != nil {
			return nil, fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
		}
		for _, f := range fs {
			if err := add(f, f.TotalAmount()-kernel.RoundCents(f.PreciseCouponsAmount)); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// progressiveFees returns the fees of the finalized or failed progressive
// billing invoices of the subscription of the usage u whose charges period contains now.
func progressiveFees(ctx context.Context, r port.Repos, u *Usage, now time.Time) ([]*invoice.Fee, error) {
	sub := u.Subscription
	iss, err := r.InvoiceSubscriptions.ListBySubscription(ctx, sub.OrganizationID, sub.ID)
	if err != nil {
		return nil, fmt.Errorf("list invoice subscriptions of %s: %w", sub.ID, err)
	}
	var out []*invoice.Fee
	seen := map[core.InvoiceID]bool{}
	for _, is := range iss {
		if seen[is.InvoiceID] || is.ChargesPeriod.From.After(now) || !is.ChargesPeriod.To.After(now) {
			continue
		}
		seen[is.InvoiceID] = true
		inv, err := r.Invoices.Get(ctx, sub.OrganizationID, is.InvoiceID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load invoice %s: %w", is.InvoiceID, err)
		}
		if inv.Type != invoice.TypeProgressiveBilling || (inv.Status != invoice.StatusFinalized && inv.Status != invoice.StatusFailed) {
			continue
		}
		fs, err := r.Fees.ListByInvoice(ctx, sub.OrganizationID, inv.ID)
		if err != nil {
			return nil, fmt.Errorf("list fees of invoice %s: %w", inv.ID, err)
		}
		out = append(out, fs...)
	}
	return out, nil
}

// cachedCharge is what the ongoing usage reads of a charge.
type cachedCharge struct {
	metric    core.BillableMetricID
	inAdvance bool
}

// chargeCache loads charges once.
type chargeCache struct {
	r   port.Repos
	org core.OrganizationID
	m   map[core.ChargeID]cachedCharge
}

// get returns the charge id.
func (c *chargeCache) get(ctx context.Context, id core.ChargeID) (cachedCharge, error) {
	if v, ok := c.m[id]; ok {
		return v, nil
	}
	ch, err := c.r.Charges.Get(ctx, c.org, id)
	if err != nil {
		return cachedCharge{}, fmt.Errorf("load charge %s: %w", id, err)
	}
	if c.m == nil {
		c.m = map[core.ChargeID]cachedCharge{}
	}
	v := cachedCharge{metric: ch.BillableMetricID, inAdvance: ch.PayInAdvance}
	c.m[id] = v
	return v, nil
}
