package fees

import (
	"maps"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/metering"
)

// UsageFilters narrow the aggregation of a current usage (lago
// UsageFilters, app/models/usage_filters.rb). The zero value narrows
// nothing.
type UsageFilters struct {
	// SkipGrouping ignores the pricing group keys: one fee per charge and
	// filter (lago skip_grouping).
	SkipGrouping bool
	// Group keeps the events whose property is one of the values, for
	// each key; the keys stop grouping the fees, the other pricing group
	// keys still do (lago filter_by_group). A key of the charge filter is
	// replaced.
	Group map[string][]string
	// Presentation keeps the presentation breakdowns of these keys only;
	// nil keeps them all (lago filter_by_presentation).
	Presentation []string
}

// apply narrows the aggregation request req.
//
// source: app/services/fees/charge_service.rb:548-551, :570-573, :597-605
func (u UsageFilters) apply(req *metering.Request) {
	if u.SkipGrouping {
		req.GroupBy = nil
	}
	if u.Presentation != nil {
		req.PresentationBy = slices.DeleteFunc(slices.Clone(req.PresentationBy), func(k string) bool { return !slices.Contains(u.Presentation, k) })
	}
	if len(u.Group) == 0 {
		return
	}
	req.GroupBy = slices.DeleteFunc(slices.Clone(req.GroupBy), func(k string) bool { _, ok := u.Group[k]; return ok })
	if len(req.GroupBy) == 0 {
		req.GroupBy = nil
	}
	// The charge filter's matching values are merged with the group's,
	// which win on a shared key (Hash#merge).
	matching := maps.Clone(req.Filter.Matching)
	if matching == nil {
		matching = make(map[string][]string, len(u.Group))
	}
	for k, vs := range u.Group {
		matching[k] = slices.Clone(vs)
	}
	req.Filter.Matching = matching
}
