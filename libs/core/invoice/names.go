package invoice

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

// FeeRefs are the objects a fee's names are read from (domain §12.1 fee
// names). Only the ones of the fee's type need to be set: Charge, Metric
// and ChargeFilter for charge fees, AddOn for add-on fees, FixedCharge and
// its AddOn for fixed-charge fees, WalletTransaction for credit fees, and
// Subscription and Plan for subscription fees and as the fallback of every
// type. A commitment fee carries its name in InvoiceDisplayName, set when it
// is built (lago Fees::Commitments::Minimum::BuildFeeBaseService#build_fee).
type FeeRefs struct {
	// Charge is the charge of a charge fee.
	Charge *catalog.Charge
	// Metric is the charge's billable metric.
	Metric *catalog.BillableMetric
	// ChargeFilter is the fee's charge filter, if any.
	ChargeFilter *catalog.ChargeFilter
	// FixedCharge is the fixed charge of a fixed-charge fee.
	FixedCharge *catalog.FixedCharge
	// AddOn is the add-on of an add-on fee or of a fixed charge.
	AddOn *catalog.AddOn
	// WalletTransaction is the transaction of a credit fee.
	WalletTransaction *wallet.Transaction
	// Subscription is the fee's subscription.
	Subscription *subscription.Subscription
	// Plan is the subscription's plan.
	Plan *catalog.Plan
}

func present(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

// SubscriptionInvoiceName is the name of the fee's subscription on
// invoices, or "" without a subscription and plan.
func (r FeeRefs) SubscriptionInvoiceName() string {
	if r.Subscription == nil || r.Plan == nil {
		return ""
	}
	return r.Subscription.InvoiceName(r.Plan.Name, r.Plan.InvoiceDisplayName)
}

func (r FeeRefs) metricName() string {
	if r.Metric == nil {
		return ""
	}
	return r.Metric.Name
}

func (r FeeRefs) creditName() string {
	if r.WalletTransaction != nil {
		if s := present(r.WalletTransaction.Name); s != "" {
			return s
		}
	}
	return string(core.FeeTypeCredit)
}

// InvoiceName is the name of the fee on invoices: its display name, else
// the name of what it bills.
func (r FeeRefs) InvoiceName(f *Fee) string {
	if s := present(f.InvoiceDisplayName); s != "" {
		return s
	}
	switch f.Type {
	case core.FeeTypeCharge:
		if r.Charge != nil {
			return r.Charge.InvoiceName(r.metricName())
		}
		return r.metricName()
	case core.FeeTypeAddOn:
		if r.AddOn != nil {
			return r.AddOn.InvoiceName()
		}
	case core.FeeTypeCredit:
		return r.creditName()
	case core.FeeTypeFixedCharge:
		if r.FixedCharge != nil {
			if s := present(r.FixedCharge.InvoiceDisplayName); s != "" {
				return s
			}
		}
		if r.AddOn != nil {
			return r.AddOn.InvoiceName()
		}
	}
	return r.SubscriptionInvoiceName()
}

// ItemName is the name of what the fee bills.
func (r FeeRefs) ItemName(f *Fee) string {
	switch f.Type {
	case core.FeeTypeCharge:
		return r.metricName()
	case core.FeeTypeAddOn, core.FeeTypeFixedCharge:
		if r.AddOn != nil {
			return r.AddOn.Name
		}
	case core.FeeTypeCredit:
		return r.creditName()
	}
	if r.Plan != nil {
		return r.Plan.Name
	}
	return ""
}

// ItemCode is the code of what the fee bills.
func (r FeeRefs) ItemCode(f *Fee) string {
	switch f.Type {
	case core.FeeTypeCharge:
		if r.Metric != nil {
			return r.Metric.Code
		}
	case core.FeeTypeAddOn, core.FeeTypeFixedCharge:
		if r.AddOn != nil {
			return r.AddOn.Code
		}
	case core.FeeTypeCredit:
		return string(core.FeeTypeCredit)
	}
	if r.Plan != nil {
		return r.Plan.Code
	}
	return ""
}

// ItemDescription is the description of what the fee bills.
func (r FeeRefs) ItemDescription(f *Fee) string {
	switch f.Type {
	case core.FeeTypeCharge:
		if r.Metric != nil {
			return present(r.Metric.Description)
		}
	case core.FeeTypeAddOn, core.FeeTypeFixedCharge:
		if r.AddOn != nil {
			return present(r.AddOn.Description)
		}
	case core.FeeTypeCredit:
		return string(core.FeeTypeCredit)
	}
	if r.Plan != nil {
		return present(r.Plan.Description)
	}
	return ""
}

// FilterDisplayName is the display name of the fee's charge filter, its
// values joined by sep, or "".
func (r FeeRefs) FilterDisplayName(sep string) string {
	if r.ChargeFilter == nil {
		return ""
	}
	return r.ChargeFilter.DisplayName(sep)
}

// GroupedValues are the non-empty pricing group values of the fee in
// stored key order: shorter keys first, then bytewise (the order of jsonb
// object keys).
func GroupedValues(f *Fee) []string {
	var out []string
	keys := slices.SortedFunc(maps.Keys(f.GroupedBy), func(a, b string) int {
		return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b))
	})
	for _, k := range keys {
		if v := f.GroupedBy[k]; v != "" {
			out = append(out, v)
		}
	}
	return out
}

// GroupedByDisplay is the pricing group suffix of a charge fee's name
// (" • v1 • v2"), or "".
func GroupedByDisplay(f *Fee) string {
	if f.Type != core.FeeTypeCharge {
		return ""
	}
	vs := GroupedValues(f)
	if len(vs) == 0 {
		return ""
	}
	return " • " + strings.Join(vs, " • ")
}

// Title is the full name of the fee on invoices: its invoice name, pricing
// group values and charge filter.
func (r FeeRefs) Title(f *Fee) string {
	s := r.InvoiceName(f) + GroupedByDisplay(f)
	if !f.ChargeFilterID.IsZero() {
		s += " • " + r.FilterDisplayName(" • ")
	}
	return s
}

// SortingKey is the key invoices sort fees by alphabetically.
func (r FeeRefs) SortingKey(f *Fee) string {
	base := strings.ToLower(r.InvoiceName(f) + " " + r.FilterDisplayName(", "))
	if f.Type != core.FeeTypeCharge || len(f.GroupedBy) == 0 {
		return base
	}
	var vals []string
	for _, k := range slices.Sorted(maps.Keys(f.GroupedBy)) {
		vals = append(vals, f.GroupedBy[k])
	}
	return strings.ToLower(r.InvoiceName(f) + " " + strings.Join(vals, "") + " " + r.FilterDisplayName(", "))
}
