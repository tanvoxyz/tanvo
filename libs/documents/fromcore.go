package documents

import (
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// FromCore holds the core objects that documents refer to, by ID, and maps
// fees and subscriptions to their view models. The caller loads the
// objects; missing ones are left nil.
type FromCore struct {
	// Plans by ID.
	Plans map[core.PlanID]catalog.Plan
	// Subscriptions by ID.
	Subscriptions map[core.SubscriptionID]subscription.Subscription
	// Charges by ID.
	Charges map[core.ChargeID]catalog.Charge
	// Metrics are the billable metrics by ID.
	Metrics map[core.BillableMetricID]catalog.BillableMetric
	// ChargeFilters by ID.
	ChargeFilters map[core.ChargeFilterID]catalog.ChargeFilter
	// FixedCharges by ID.
	FixedCharges map[core.FixedChargeID]catalog.FixedCharge
	// AddOns by ID.
	AddOns map[core.AddOnID]catalog.AddOn
	// PricingUnits by ID.
	PricingUnits map[core.PricingUnitID]catalog.PricingUnit
	// WalletTransactions by ID.
	WalletTransactions map[core.WalletTransactionID]wallet.Transaction
	// Wallets by ID.
	Wallets map[core.WalletID]wallet.Wallet
}

func ref[K comparable, V any](m map[K]V, k K) *V {
	v, ok := m[k]
	if !ok {
		return nil
	}
	return &v
}

// Fees returns the view models of fees, in order, each with its catalog
// objects and its true-up fee. Breakdowns and proration are left for the
// caller, which computes them from usage.
func (c *FromCore) Fees(fees []invoice.Fee) []Fee {
	trueUps := map[core.FeeID]*invoice.Fee{}
	for i := range fees {
		if p := fees[i].TrueUpParentFeeID; !p.IsZero() {
			trueUps[p] = &fees[i]
		}
	}
	out := make([]Fee, 0, len(fees))
	for i := range fees {
		f := Fee{Fee: fees[i], TrueUpFee: trueUps[fees[i].ID]}
		if id := f.Fee.ChargeID; !id.IsZero() {
			f.Charge = ref(c.Charges, id)
		}
		if f.Charge != nil {
			f.Metric = ref(c.Metrics, f.Charge.BillableMetricID)
			if pu := f.Charge.AppliedPricingUnit; pu != nil {
				f.PricingUnit = ref(c.PricingUnits, pu.PricingUnitID)
			}
		}
		if id := f.Fee.ChargeFilterID; !id.IsZero() {
			f.ChargeFilter = ref(c.ChargeFilters, id)
		}
		if id := f.Fee.FixedChargeID; !id.IsZero() {
			f.FixedCharge = ref(c.FixedCharges, id)
		}
		if id := f.Fee.AddOnID; !id.IsZero() {
			f.AddOn = ref(c.AddOns, id)
		}
		if id := f.Fee.WalletTransactionID; !id.IsZero() {
			f.WalletTransaction = ref(c.WalletTransactions, id)
			if f.WalletTransaction != nil {
				f.Wallet = ref(c.Wallets, f.WalletTransaction.WalletID)
			}
		}
		out = append(out, f)
	}
	return out
}

// InvoiceSubscriptions returns the view models of an invoice's
// subscription links, in order, skipping links whose subscription or plan
// is missing.
func (c *FromCore) InvoiceSubscriptions(links []invoice.Subscription) []Subscription {
	out := make([]Subscription, 0, len(links))
	for _, l := range links {
		s, ok := c.Subscriptions[l.SubscriptionID]
		if !ok {
			continue
		}
		p, ok := c.Plans[s.PlanID]
		if !ok {
			continue
		}
		out = append(out, Subscription{InvoiceSubscription: l, Subscription: s, Plan: p})
	}
	return out
}
