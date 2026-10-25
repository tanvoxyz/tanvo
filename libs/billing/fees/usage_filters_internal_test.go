package fees

import (
	"reflect"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// TestUsageFiltersApply narrows the aggregation request of a charge.
//
// source: app/services/fees/charge_service.rb:548-551, :570-573, :597-605
func TestUsageFiltersApply(t *testing.T) {
	t.Parallel()
	base := func() metering.Request {
		return metering.Request{
			GroupBy:        []string{"workspace", "user"},
			PresentationBy: []string{"region", "team"},
			Filter:         metering.Filter{Matching: map[string][]string{"cloud": {"aws"}, "workspace": {"old"}}},
		}
	}
	cases := []struct {
		name    string
		filters UsageFilters
		want    func(r *metering.Request)
	}{
		{"skip grouping", UsageFilters{SkipGrouping: true}, func(r *metering.Request) { r.GroupBy = nil }},
		// source: spec/services/fees/charge_service_spec.rb:4465
		{"every presentation key", UsageFilters{}, func(*metering.Request) {}},
		// source: spec/services/fees/charge_service_spec.rb:4528
		{"presentation keys overlapping", UsageFilters{Presentation: []string{"team", "other"}}, func(r *metering.Request) {
			r.PresentationBy = []string{"team"}
		}},
		// source: spec/services/fees/charge_service_spec.rb:4496
		{"empty presentation filter", UsageFilters{Presentation: []string{}}, func(r *metering.Request) { r.PresentationBy = []string{} }},
		// source: spec/services/fees/charge_service_spec.rb:4560
		{"presentation keys not overlapping", UsageFilters{Presentation: []string{"other"}}, func(r *metering.Request) { r.PresentationBy = []string{} }},
		{"group key removed from grouping, merged into matching", UsageFilters{Group: map[string][]string{"workspace": {"a", "b"}}}, func(r *metering.Request) {
			r.GroupBy = []string{"user"}
			r.Filter.Matching = map[string][]string{"cloud": {"aws"}, "workspace": {"a", "b"}}
		}},
		{"every group key filtered", UsageFilters{Group: map[string][]string{"workspace": {"a"}, "user": {"1"}}}, func(r *metering.Request) {
			r.GroupBy = nil
			r.Filter.Matching = map[string][]string{"cloud": {"aws"}, "workspace": {"a"}, "user": {"1"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, want := base(), base()
			matching, groupBy := got.Filter.Matching, got.GroupBy
			tc.filters.apply(&got)
			tc.want(&want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got = %+v, want = %+v", got, want)
			}
			// The charge's request is shared: apply copies before narrowing.
			if b := base(); !reflect.DeepEqual(matching, b.Filter.Matching) || !reflect.DeepEqual(groupBy, b.GroupBy) {
				t.Errorf("original: got = %v %v, want = unchanged", matching, groupBy)
			}
		})
	}

	// source: spec/services/fees/charge_service_spec.rb:4480, :4511, :4543
	t.Run("charge without presentation keys", func(t *testing.T) {
		t.Parallel()
		for _, p := range [][]string{nil, {}, {"region"}} {
			r := metering.Request{}
			UsageFilters{Presentation: p}.apply(&r)
			if r.PresentationBy != nil {
				t.Errorf("filter %v: got = %v, want = no presentation", p, r.PresentationBy)
			}
		}
	})

	t.Run("filter without matching", func(t *testing.T) {
		t.Parallel()
		r := metering.Request{}
		UsageFilters{Group: map[string][]string{"user": {"1"}}}.apply(&r)
		if want := map[string][]string{"user": {"1"}}; !reflect.DeepEqual(r.Filter.Matching, want) {
			t.Errorf("got = %v, want = %v", r.Filter.Matching, want)
		}
	})
}
