package fees

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// An event is billed on the product filter it matches with the most
// keys, ties keeping the first loaded filter; a value left open matches
// every value of the metric filter.
//
// source: spec/services/events/billing_period_filters/event_matching_service_spec.rb:85-160
func TestMatchProductFilter(t *testing.T) {
	t.Parallel()
	metric := []metering.MetricFilter{
		{Key: "payment_method", Values: []string{"card", "transfer"}},
		{Key: "card_type", Values: []string{"credit", "debit"}},
	}
	str := func(s string) *string { return &s }
	filter := func(id string, values ...catalog.ProductFilterValue) *catalog.ProductFilter {
		return &catalog.ProductFilter{ID: core.MustParseID[core.ProductFilterID](id), Values: values}
	}
	open := filter("00000000-0000-0000-0000-000000000001", catalog.ProductFilterValue{Key: "payment_method"})
	card := filter("00000000-0000-0000-0000-000000000002", catalog.ProductFilterValue{Key: "payment_method", Value: str("card")})
	credit := filter("00000000-0000-0000-0000-000000000003",
		catalog.ProductFilterValue{Key: "payment_method", Value: str("card")}, catalog.ProductFilterValue{Key: "card_type", Value: str("credit")})
	for _, tc := range []struct {
		name    string
		source  string
		filters []*catalog.ProductFilter
		props   metering.Properties
		want    *catalog.ProductFilter
	}{
		{"an open value matches a configured value", "event_matching_service_spec.rb:116", []*catalog.ProductFilter{open}, metering.Properties{"payment_method": "card"}, open},
		{"ties keep the first loaded filter", "event_matching_service_spec.rb:128", []*catalog.ProductFilter{open, card}, metering.Properties{"payment_method": "card"}, open},
		{"an unknown value matches nothing", "event_matching_service_spec.rb:138", []*catalog.ProductFilter{open}, metering.Properties{"payment_method": "virtual_card"}, nil},
		{"an empty value matches nothing", "event_matching_service_spec.rb:138", []*catalog.ProductFilter{open}, metering.Properties{"payment_method": ""}, nil},
		{"a missing key matches nothing", "event_matching_service_spec.rb:155", []*catalog.ProductFilter{open}, metering.Properties{"card_type": "credit"}, nil},
		{"the filter with more keys wins", "event_matching_service_spec.rb:32", []*catalog.ProductFilter{card, credit}, metering.Properties{"payment_method": "card", "card_type": "credit"}, credit},
		{"no filters match nothing", "event_matching_service_spec.rb:32", nil, metering.Properties{"payment_method": "card"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: spec/services/events/billing_period_filters/%s", tc.source)
			if got := MatchProductFilter(tc.filters, metric, tc.props); got != tc.want {
				t.Errorf("MatchProductFilter() = %v, want = %v", got, tc.want)
			}
		})
	}
}
