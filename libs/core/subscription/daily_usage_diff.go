package subscription

import (
	"encoding/json"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// DiffUsage returns current minus previous (lago
// DailyUsages::ComputeDiffService, app/services/daily_usages/compute_diff_service.rb):
// the units, events count and amount of each charge usage, of its groups
// and of their filters, matched by charge, group values and filter
// values, and the units of their presentation breakdowns, matched by
// presentation values; an entry previous does not have is kept whole.
// Amount is then the sum of the charges' amounts, TaxesAmount loses the
// previous taxes in the share of the previous amount that came from the
// charges still present, and TotalAmount is their sum. With a nil
// previous, the diff is current. current is not modified.
func DiffUsage(current UsageSnapshot, previous *UsageSnapshot) UsageSnapshot {
	diff := cloneSnapshot(current)
	if previous == nil {
		return diff
	}
	prevCharges := make(map[core.ChargeID]*ChargeUsage, len(previous.Charges))
	for i := range previous.Charges {
		prevCharges[previous.Charges[i].Charge.ID] = &previous.Charges[i]
	}
	for i := range diff.Charges {
		cur := &diff.Charges[i]
		prev, ok := prevCharges[cur.Charge.ID]
		if !ok {
			continue
		}
		cur.subtract(prev.UsageValues)
		diffFilters(cur.Filters, prev.Filters)
		diffBreakdowns(cur.PresentationBreakdowns, prev.PresentationBreakdowns)
		prevGroups := indexBy(prev.Grouped, func(g GroupedUsage) string { return key(g.GroupedBy) })
		for j := range cur.Grouped {
			g := &cur.Grouped[j]
			pg, ok := prevGroups[key(g.GroupedBy)]
			if !ok {
				continue
			}
			g.subtract(pg.UsageValues)
			diffFilters(g.Filters, pg.Filters)
			diffBreakdowns(g.PresentationBreakdowns, pg.PresentationBreakdowns)
		}
	}

	diff.Amount = 0
	for _, c := range diff.Charges {
		diff.Amount += c.Amount
	}
	diff.TaxesAmount -= commonTaxes(diff, previous, prevCharges)
	diff.TotalAmount = diff.Amount + diff.TaxesAmount
	return diff
}

// commonTaxes returns the previous taxes in the share of the previous
// amount that came from the charges of diff, all of them when the
// previous amount is not positive (compute_diff_service.rb:81-90).
func commonTaxes(diff UsageSnapshot, previous *UsageSnapshot, prevCharges map[core.ChargeID]*ChargeUsage) int64 {
	if previous.Amount <= 0 {
		return previous.TaxesAmount
	}
	var common int64
	for _, c := range diff.Charges {
		if p, ok := prevCharges[c.Charge.ID]; ok {
			common += p.Amount
		}
	}
	share := money.NewFromInt(previous.TaxesAmount).Mul(money.NewFromInt(common)).Div(money.NewFromInt(previous.Amount))
	n, _ := share.Round(0, money.HalfUp).Int64()
	return n
}

// subtract removes the units, events count and amount of p
// (compute_diff_service.rb:116-120); the aggregated units and the
// pricing unit amounts are kept.
func (v *UsageValues) subtract(p UsageValues) {
	v.Units = v.Units.Sub(p.Units)
	v.EventsCount -= p.EventsCount
	v.Amount -= p.Amount
}

func diffFilters(cur, prev []FilterUsage) {
	index := indexBy(prev, func(f FilterUsage) string { return key(f.Values) })
	for i := range cur {
		p, ok := index[key(cur[i].Values)]
		if !ok {
			continue
		}
		cur[i].subtract(p.UsageValues)
		diffBreakdowns(cur[i].PresentationBreakdowns, p.PresentationBreakdowns)
	}
}

func diffBreakdowns(cur, prev []PresentationBreakdownUsage) {
	index := indexBy(prev, func(b PresentationBreakdownUsage) string { return key(b.PresentationBy) })
	for i := range cur {
		if p, ok := index[key(cur[i].PresentationBy)]; ok {
			cur[i].Units = cur[i].Units.Sub(p.Units)
		}
	}
}

// indexBy indexes s by k; as Ruby's index_by, a later element replaces an
// earlier one with the same key.
func indexBy[T any](s []T, k func(T) string) map[string]*T {
	out := make(map[string]*T, len(s))
	for i := range s {
		out[k(s[i])] = &s[i]
	}
	return out
}

// key returns the canonical form of a map of values (encoding/json sorts
// the keys).
func key(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // maps of strings always encode
	}
	return string(b)
}

// cloneSnapshot deep-copies u.
func cloneSnapshot(u UsageSnapshot) UsageSnapshot {
	b, err := json.Marshal(u)
	if err != nil {
		panic(err) // a snapshot always encodes
	}
	var c UsageSnapshot
	if err := json.Unmarshal(b, &c); err != nil {
		panic(err)
	}
	return c
}
