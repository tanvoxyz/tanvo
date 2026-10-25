package metering_test

import (
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestAttributedQueryCells(t *testing.T) {
	one := money.NewFromInt(1)
	lookup := &metering.PriceLookup{
		KeySets: []metering.PriceKeySet{
			{Keys: []string{"model", "region"}, Entries: map[string]int{"opus\x1feu": 1}},
			{Keys: []string{"model"}, Entries: map[string]int{"opus": 2}},
		},
		FilterIDs:   []string{"opus_eu", "opus"},
		UnitAmounts: []money.Decimal{money.NewFromInt(20), money.NewFromInt(10)},
	}
	query := func(split bool) metering.AttributedQuery {
		return metering.AttributedQuery{Charges: []metering.AttributedCharge{
			{ChargeID: "tokens", Code: "tokens", UnitAmount: &one, Lookup: lookup, Split: split},
			{ChargeID: "requests", Code: "requests", Count: true},
		}}
	}
	tests := []struct {
		name  string
		split bool
		want  []metering.AttributedCell
	}{
		{
			// source: spec/services/events/stores/clickhouse/attributed_usage_query_spec.rb:83
			name: "one cell per charge",
			want: []metering.AttributedCell{
				{ChargeID: "tokens", Priced: true},
				{ChargeID: "requests", Charge: 1},
			},
		},
		{
			// source: spec/services/events/stores/clickhouse/attributed_usage_query_spec.rb:92
			name: "one cell per filter of a split charge and one for the default price", split: true,
			want: []metering.AttributedCell{
				{ChargeID: "tokens", ChargeFilterID: "opus_eu", Priced: true, Rank: 1},
				{ChargeID: "tokens", ChargeFilterID: "opus", Priced: true, Rank: 2},
				{ChargeID: "tokens", Priced: true, Rank: 3},
				{ChargeID: "requests", Charge: 1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := query(tt.split).Cells(); !slices.Equal(got, tt.want) {
				t.Errorf("cells got = %+v, want = %+v", got, tt.want)
			}
		})
	}
}

func TestAttributedValuesAdd(t *testing.T) {
	a := metering.AttributedValues{Amount: money.NewFromInt(3), Events: 2, Cells: []metering.AttributedCellUsage{{Units: money.NewFromInt(4), Events: 2}}}
	got := a.Add(metering.AttributedValues{})
	if got.Amount.String() != "3" || got.Events != 2 || len(got.Cells) != 1 || got.Cells[0].Units.String() != "4" {
		t.Errorf("sum with zero got = %+v, want = %+v", got, a)
	}
}
