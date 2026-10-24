package fees

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// A segment's aggregation is bypassed when no event combination of its
// contract reaches its filter bucket: the selected product filter, or the
// default bucket of the events matching no product filter.
//
// source: app/services/fees/charge_service.rb:492-505, app/services/events/billing_period_filters/event_matching_service.rb:15-34, spec/services/events/billing_period_filters/event_matching_service_spec.rb
func TestSegmentEventsBypass(t *testing.T) {
	t.Parallel()
	contract := &subscription.Subscription{ID: core.SubscriptionID{}}
	ps := ProductSegment{Contract: contract}
	m := &catalog.BillableMetric{Code: "calls", Filters: []catalog.MetricFilter{{Key: "region", Values: []string{"eu", "us"}}}}
	metric := []metering.MetricFilter{{Key: "region", Values: []string{"eu", "us"}}}
	eu := "eu"
	euFilter := &catalog.ProductFilter{Values: []catalog.ProductFilterValue{{Key: "region", Value: &eu}}}
	all := &catalog.ProductFilter{Values: []catalog.ProductFilterValue{{Key: "region"}}}
	events := func(combos ...metering.Properties) *SegmentEvents {
		return &SegmentEvents{combos: map[segmentEventsKey][]metering.Properties{{contract: contract.ID, code: "calls"}: combos}}
	}
	for _, tc := range []struct {
		name      string
		events    *SegmentEvents
		recurring bool
		filters   []*catalog.ProductFilter
		selected  *catalog.ProductFilter
		want      bool
	}{
		{name: "unknown events always aggregate", events: nil, want: false},
		{name: "no events bypass", events: events(), want: true},
		{name: "recurring metrics always aggregate", events: events(), recurring: true, want: false},
		{name: "matching selected filter aggregates", events: events(metering.Properties{"region": "eu"}), filters: []*catalog.ProductFilter{euFilter}, selected: euFilter, want: false},
		{name: "unmatched selected filter bypasses", events: events(metering.Properties{"region": "us"}), filters: []*catalog.ProductFilter{euFilter}, selected: euFilter, want: true},
		{name: "filter on every value matches any value", events: events(metering.Properties{"region": "us"}), filters: []*catalog.ProductFilter{all}, selected: all, want: false},
		{name: "filter key missing bypasses", events: events(metering.Properties{}), filters: []*catalog.ProductFilter{euFilter}, selected: euFilter, want: true},
		{name: "default bucket of matched events bypasses", events: events(metering.Properties{"region": "eu"}), filters: []*catalog.ProductFilter{euFilter}, want: true},
		{name: "default bucket of unmatched events aggregates", events: events(metering.Properties{"region": "eu"}, metering.Properties{"region": "us"}), filters: []*catalog.ProductFilter{euFilter}, want: false},
		{name: "default bucket without filters aggregates", events: events(metering.Properties{}), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mm := *m
			mm.Recurring = tc.recurring
			if got := tc.events.bypass(ps, &mm, tc.filters, metric, tc.selected); got != tc.want {
				t.Errorf("bypass() = %v, want = %v", got, tc.want)
			}
		})
	}
}
