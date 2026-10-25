package usageattribution

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// MaxLookupEntries is the most entries the expanded filter values of a
// price lookup may hold; a charge past it has no lookup.
const MaxLookupEntries = 20_000

// PriceLookup returns the lookup resolving which kept filter of charge c,
// on billable metric m, prices an event, with the unit amounts in minor
// units of currency: the filters ranked by billing precedence
// ([metering.ComparePrecedence]: the most keys, the fewest values, the
// oldest), grouped by their set of keys, each set mapping the joined
// values to the best rank holding them. Keys whose metric filter is gone
// are left out, and so are filters without any key left. It returns nil
// when expanding the values exceeds maxEntries.
//
// The lookup ranks the filters as billing does where lago ranks them by
// keys then age only (parity §4).
//
// source: app/services/usage_attributions/charge_price_lookup_service.rb:38-91
func PriceLookup(c *catalog.Charge, m *catalog.BillableMetric, currency money.Currency, maxEntries int) *metering.PriceLookup {
	metric := kernel.MeteringMetricFilters(m)
	type ranked struct {
		f      *catalog.ChargeFilter
		mf     metering.ChargeFilter
		values map[string][]string
	}
	var filters []ranked
	for i := range c.Filters {
		f := &c.Filters[i]
		if f.DeletedAt != nil {
			continue
		}
		values := map[string][]string{}
		for k, vs := range f.Values {
			if !slices.ContainsFunc(metric, func(mf metering.MetricFilter) bool { return mf.Key == k }) {
				continue
			}
			values[k] = vs
		}
		if len(values) == 0 {
			continue
		}
		mf := metering.ChargeFilter{ID: f.ID.String(), Values: values, CreatedAt: f.CreatedAt}
		filters = append(filters, ranked{f: f, mf: mf, values: mf.Expand(metric)})
	}
	slices.SortStableFunc(filters, func(a, b ranked) int { return metering.ComparePrecedence(a.mf, b.mf, metric) })

	entries := 0
	for _, f := range filters {
		n := 1
		for _, vs := range f.values {
			n *= len(vs)
		}
		entries += n
	}
	if entries > maxEntries {
		return nil
	}

	rate := money.NewFromInt(1)
	if c.AppliedPricingUnit != nil {
		rate = c.AppliedPricingUnit.ConversionRate
	}
	out := &metering.PriceLookup{}
	sets := map[string]int{}
	for rank, f := range filters {
		keys := slices.Sorted(func(yield func(string) bool) {
			for k := range f.values {
				if !yield(k) {
					return
				}
			}
		})
		id := strings.Join(keys, "\x00")
		i, ok := sets[id]
		if !ok {
			i = len(out.KeySets)
			sets[id] = i
			out.KeySets = append(out.KeySets, metering.PriceKeySet{Keys: keys, Entries: map[string]int{}})
		}
		for _, combo := range product(keys, f.values) {
			if _, ok := out.KeySets[i].Entries[combo]; !ok {
				out.KeySets[i].Entries[combo] = rank + 1
			}
		}
		out.FilterIDs = append(out.FilterIDs, f.f.ID.String())
		out.UnitAmounts = append(out.UnitAmounts, unitAmount(f.f.Properties, rate, currency))
	}
	// The largest key sets first, then in order of their best filter.
	slices.SortStableFunc(out.KeySets, func(a, b metering.PriceKeySet) int { return len(b.Keys) - len(a.Keys) })
	return out
}

// product returns the values of keys of every combination, joined by
// [metering.PriceLookupSeparator].
func product(keys []string, values map[string][]string) []string {
	out := []string{""}
	for i, k := range keys {
		var next []string
		for _, prefix := range out {
			for _, v := range values[k] {
				if i > 0 {
					v = prefix + metering.PriceLookupSeparator + v
				}
				next = append(next, v)
			}
		}
		out = next
	}
	return out
}

// unitAmount returns the amount property of a standard price in minor
// units of currency, converted from a pricing unit at rate: zero when
// missing or blank.
//
// source: app/services/usage_attributions/charge_price_lookup_service.rb:85-91
func unitAmount(p catalog.Properties, rate money.Decimal, currency money.Currency) money.Decimal {
	var props struct {
		Amount json.RawMessage `json:"amount"`
	}
	amount := money.Zero
	if err := json.Unmarshal(p.Raw(), &props); err == nil && len(props.Amount) > 0 {
		s := strings.Trim(string(props.Amount), `"`)
		if d, err := money.Parse(s); err == nil {
			amount = d
		}
	}
	return amount.Mul(rate).Mul(currency.SubunitToUnit())
}
