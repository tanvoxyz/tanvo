package metering

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/money"
)

var _ AttributedUsageReader = (*MemoryStore)(nil)

// attributedScale is the number of decimal places attributed units and
// amounts are truncated to, as the ClickHouse store computes them.
const attributedScale = 12

// AttributedUsageAvailable implements [AttributedUsageReader].
func (s *MemoryStore) AttributedUsageAvailable(string) bool { return true }

// AttributedUsage implements [AttributedUsageReader]: the reference of the
// ClickHouse query (see the eventstore's).
func (s *MemoryStore) AttributedUsage(ctx context.Context, q AttributedQuery) (AttributedUsage, error) {
	if err := checkContext(ctx); err != nil {
		return AttributedUsage{}, err
	}
	cells := q.Cells()
	zero := func() AttributedValues { return AttributedValues{Cells: make([]AttributedCellUsage, len(cells))} }
	res := AttributedUsage{Unattributed: zero(), Level: zero()}
	if len(q.Charges) == 0 {
		return res, nil
	}
	if q.Order != AttributedOrderAmount && q.Order != AttributedOrderEvents {
		return AttributedUsage{}, fmt.Errorf("metering: attributed usage: order %q", q.Order)
	}
	codes := map[string]bool{}
	for _, c := range q.Charges {
		codes[c.Code] = true
	}

	byNode := map[string]*AttributedValues{}
	var nodes []string
	for _, e := range s.events {
		if e.OrganizationID != q.OrganizationID || e.SubscriptionID != q.SubscriptionID || !codes[e.Code] ||
			e.Timestamp.Before(q.From) || e.Timestamp.After(q.To) || !labelsMatch(q.LabelFilters, e.AttributionLabels) {
			continue
		}
		node := e.AttributionLabels[q.GroupKey]
		v, ok := byNode[node]
		if !ok {
			z := zero()
			v = &z
			byNode[node] = v
			nodes = append(nodes, node)
		}
		v.Events++
		for i, cell := range cells {
			c := q.Charges[cell.Charge]
			if e.Code != c.Code {
				continue
			}
			rank := 0
			if c.Lookup != nil {
				rank = c.Lookup.rank(e.Properties)
			}
			if cell.Rank > 0 && rank != cell.Rank {
				continue
			}
			units := money.NewFromInt(1)
			if !c.Count {
				units = eventUnits(e.Value)
			}
			u := &v.Cells[i]
			u.Units = u.Units.Add(units)
			u.Events++
			if cell.Priced {
				amount := units.Mul(c.unitPrice(rank)).Truncate(attributedScale)
				u.Amount = u.Amount.Add(amount)
				v.Amount = v.Amount.Add(amount)
			}
		}
	}
	if q.MaxGroups > 0 && len(nodes) > q.MaxGroups {
		return AttributedUsage{}, fmt.Errorf("%w: %d values", ErrAttributedTooManyGroups, len(nodes))
	}

	var ranked []string
	for _, n := range nodes {
		if n == "" {
			res.Unattributed = *byNode[n]
			continue
		}
		ranked = append(ranked, n)
	}
	slices.SortFunc(ranked, func(a, b string) int {
		va, vb := byNode[a], byNode[b]
		c := 0
		if q.Order == AttributedOrderAmount {
			c = vb.Amount.Cmp(va.Amount)
		} else {
			c = cmp.Compare(vb.Events, va.Events)
		}
		return cmp.Or(c, strings.Compare(a, b))
	})
	res.Groups = int64(len(ranked))
	search := strings.ToLower(q.Search)
	matches := 0
	for i, n := range ranked {
		res.Level = res.Level.Add(*byNode[n])
		pos := i + 1
		if search != "" {
			if !strings.Contains(strings.ToLower(n), search) {
				pos = -1
			} else {
				matches++
				pos = matches
			}
		}
		in := pos > q.Offset && pos <= q.Offset+q.Limit
		if in || i == 0 {
			res.Rows = append(res.Rows, AttributedRow{Value: n, Rank: i + 1, InPage: in, AttributedValues: *byNode[n]})
		}
	}
	return res, nil
}

// labelsMatch reports whether the labels pass the filters: each filtered
// label (missing reads as "") is one of the values.
func labelsMatch(filters map[string][]string, labels map[string]string) bool {
	for k, vs := range filters {
		if !slices.Contains(vs, labels[k]) {
			return false
		}
	}
	return true
}

// eventUnits returns the value enrichment gave an event, truncated to the
// attributed scale; zero when missing or not a number.
func eventUnits(v *string) money.Decimal {
	if v == nil {
		return money.Zero
	}
	d, err := money.Parse(*v)
	if err != nil {
		return money.Zero
	}
	return d.Truncate(attributedScale)
}

// rank returns the rank of the filter pricing an event with the
// properties: the lowest rank its values find across the key sets, the
// default rank when none.
func (l *PriceLookup) rank(p Properties) int {
	best := l.DefaultRank()
	for _, ks := range l.KeySets {
		values := make([]string, len(ks.Keys))
		for i, k := range ks.Keys {
			values[i] = p[k]
		}
		if r, ok := ks.Entries[strings.Join(values, PriceLookupSeparator)]; ok && r < best {
			best = r
		}
	}
	return best
}

// unitPrice returns the unit amount of an event of the charge whose
// filter rank is rank (0 without a lookup).
func (c AttributedCharge) unitPrice(rank int) money.Decimal {
	if c.Lookup != nil && rank >= 1 && rank <= len(c.Lookup.UnitAmounts) {
		return c.Lookup.UnitAmounts[rank-1].Truncate(attributedScale)
	}
	return c.UnitAmount.Truncate(attributedScale)
}
