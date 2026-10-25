package usageattribution_test

import (
	"cmp"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/usageattribution"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// lookupFixture is the charge of
// spec/services/usage_attributions/charge_price_lookup_service_spec.rb:8-28:
// filters on opus, opus in eu, sonnet or haiku, and every region, created
// in that order.
func lookupFixture() (*catalog.Charge, *catalog.BillableMetric) {
	ids := core.UUIDv7Generator{}
	m := &catalog.BillableMetric{
		ID: core.NewID[core.BillableMetricID](ids), Code: "tokens", AggregationType: catalog.AggregationTypeSum,
		Filters: []catalog.MetricFilter{
			{ID: core.NewID[core.BillableMetricFilterID](ids), Key: "model", Values: []string{"opus", "sonnet", "haiku"}},
			{ID: core.NewID[core.BillableMetricFilterID](ids), Key: "region", Values: []string{"eu", "us"}},
		},
	}
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	filter := func(values map[string][]string, amount string, age int) catalog.ChargeFilter {
		return catalog.ChargeFilter{
			ID: core.NewID[core.ChargeFilterID](ids), Values: values,
			Properties: catalog.MustProperties(`{"amount":"` + amount + `"}`), CreatedAt: at.AddDate(0, 0, -age),
		}
	}
	c := &catalog.Charge{
		ID: core.NewID[core.ChargeID](ids), BillableMetricID: m.ID, ChargeModel: catalog.ChargeModelStandard,
		Properties: catalog.MustProperties(`{"amount":"1"}`),
		Filters: []catalog.ChargeFilter{
			filter(map[string][]string{"model": {"opus"}}, "10", 4),
			filter(map[string][]string{"model": {"opus"}, "region": {"eu"}}, "20", 3),
			filter(map[string][]string{"model": {"sonnet", "haiku"}}, "3", 2),
			filter(map[string][]string{"region": {catalog.AllFilterValues}}, "0.5", 1),
		},
	}
	return c, m
}

func TestPriceLookup(t *testing.T) {
	const spec = "spec/services/usage_attributions/charge_price_lookup_service_spec.rb:"
	eur := money.MustCurrency("EUR")
	tests := []struct {
		source  string
		name    string
		change  func(*catalog.Charge)
		max     int
		filters []int
		sets    []metering.PriceKeySet
		amounts []string
		none    bool
	}{
		{
			source: spec + "30", name: "ranks the filters by specificity, then by age",
			filters: []int{1, 0, 2, 3},
		},
		{
			source: spec + "34", name: "maps the values of each key set to the rank of their best filter",
			sets: []metering.PriceKeySet{
				{Keys: []string{"model", "region"}, Entries: map[string]int{"opus\x1feu": 1}},
				{Keys: []string{"model"}, Entries: map[string]int{"opus": 2, "sonnet": 3, "haiku": 3}},
				{Keys: []string{"region"}, Entries: map[string]int{"eu": 4, "us": 4}},
			},
		},
		{
			source: spec + "44", name: "returns the unit amount of each filter in cents",
			amounts: []string{"2000", "1000", "300", "50"},
		},
		{
			source: spec + "51", name: "converts the unit amounts of a charge priced in a pricing unit",
			change: func(c *catalog.Charge) {
				c.AppliedPricingUnit = &catalog.AppliedPricingUnit{ConversionRate: money.NewFromInt(2)}
			},
			amounts: []string{"4000", "2000", "600", "100"},
		},
		{
			source: spec + "59", name: "leaves a discarded filter out",
			change:  func(c *catalog.Charge) { c.Filters[2].DeletedAt = new(time.Now()) },
			filters: []int{1, 0, 3},
		},
		{
			source: spec + "67", name: "returns no lookup when the expanded values exceed the limit",
			max: 5, none: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			c, m := lookupFixture()
			if tt.change != nil {
				tt.change(c)
			}
			max := cmp.Or(tt.max, usageattribution.MaxLookupEntries)
			l := usageattribution.PriceLookup(c, m, eur, max)
			if tt.none {
				if l != nil {
					t.Fatalf("lookup got = %+v, want = nil", l)
				}
				return
			}
			if l == nil {
				t.Fatal("lookup got = nil, want = a lookup")
			}
			if tt.filters != nil {
				var want []string
				for _, i := range tt.filters {
					want = append(want, c.Filters[i].ID.String())
				}
				if !slices.Equal(l.FilterIDs, want) {
					t.Errorf("filter ids got = %v, want = %v", l.FilterIDs, want)
				}
			}
			if tt.sets != nil {
				if !slices.EqualFunc(l.KeySets, tt.sets, func(a, b metering.PriceKeySet) bool {
					return slices.Equal(a.Keys, b.Keys) && maps.Equal(a.Entries, b.Entries)
				}) {
					t.Errorf("key sets got = %v, want = %v", l.KeySets, tt.sets)
				}
			}
			if tt.amounts != nil {
				var got []string
				for _, a := range l.UnitAmounts {
					got = append(got, a.Reduce().String())
				}
				if !slices.Equal(got, tt.amounts) {
					t.Errorf("unit amounts got = %v, want = %v", got, tt.amounts)
				}
			}
		})
	}
}
