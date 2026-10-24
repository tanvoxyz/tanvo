package fees

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// SegmentEvents is what an invoice group's metered segments find in the
// events, read once before pricing them (lago
// Events::BillingPeriodFilterService.for_billing_segments!): for each
// contract and non-recurring metric code, the distinct combinations of
// the contract's metric filter properties its events carry over the
// contract's segments. A segment whose filter no combination reaches is
// priced without reading its events (lago filtered_aggregations).
type SegmentEvents struct {
	combos map[segmentEventsKey][]metering.Properties
}

// segmentEventsKey is a contract and a metric code.
type segmentEventsKey struct {
	contract core.SubscriptionID
	code     string
}

// SegmentEvents reads the property combinations of the events of the
// metered segments segs.
//
// source: app/services/billing_segments/process_service.rb:84, :138-144, app/services/events/billing_period_filters/billing_segments_resolver.rb:12-43
func (s *Service) SegmentEvents(ctx context.Context, r port.Repos, segs []ProductSegment) (*SegmentEvents, error) {
	type window struct {
		ps       ProductSegment
		from, to time.Time
		codes    map[string]bool
		keys     map[string]bool
	}
	byContract := map[core.SubscriptionID]*window{}
	var order []core.SubscriptionID
	metrics := map[core.BillableMetricID]*catalog.BillableMetric{}
	for _, ps := range segs {
		if ps.Product.ProductType != catalog.ProductTypeMetered {
			continue
		}
		id := ps.Product.BillableMetricID
		m, ok := metrics[id]
		if !ok {
			var err error
			if m, err = r.Metrics.Get(ctx, ps.Segment.OrganizationID, id); err != nil {
				return nil, fmt.Errorf("load metric %s: %w", id, err)
			}
			metrics[id] = m
		}
		w, ok := byContract[ps.Contract.ID]
		if !ok {
			w = &window{ps: ps, from: ps.Segment.StartedAt, to: ps.Segment.EndedAt, codes: map[string]bool{}, keys: map[string]bool{}}
			byContract[ps.Contract.ID] = w
			order = append(order, ps.Contract.ID)
		}
		w.from, w.to = minTime(w.from, ps.Segment.StartedAt), maxTime(w.to, ps.Segment.EndedAt)
		for _, f := range m.Filters {
			w.keys[f.Key] = true
		}
		if !m.Recurring {
			// Recurring usage carries over, so it is always aggregated.
			w.codes[m.Code] = true
		}
	}
	out := &SegmentEvents{combos: map[segmentEventsKey][]metering.Properties{}}
	for _, id := range order {
		w := byContract[id]
		keys := slices.Sorted(maps.Keys(w.keys))
		for _, code := range slices.Sorted(maps.Keys(w.codes)) {
			q := metering.Query{
				OrganizationID: w.ps.Segment.OrganizationID.String(),
				SubscriptionID: w.ps.Contract.ExternalID,
				Code:           code,
				From:           w.from,
				To:             kernel.MeteringTo(w.to),
				Location:       w.ps.Location,
			}
			key := segmentEventsKey{contract: id, code: code}
			if len(keys) == 0 {
				n, err := s.d.Events.Count(ctx, q)
				if err != nil {
					return nil, fmt.Errorf("count %s events of %s: %w", code, w.ps.Contract.ExternalID, err)
				}
				if n > 0 {
					out.combos[key] = []metering.Properties{{}}
				}
				continue
			}
			groups, err := s.d.Events.GroupedCount(ctx, q, keys)
			if err != nil {
				return nil, fmt.Errorf("read %s event properties of %s: %w", code, w.ps.Contract.ExternalID, err)
			}
			for _, g := range groups {
				if g.Count == 0 {
					continue
				}
				p := metering.Properties{}
				for k, v := range g.Groups {
					if v != nil {
						p[k] = *v
					}
				}
				out.combos[key] = append(out.combos[key], p)
			}
		}
	}
	return out, nil
}

// bypass reports whether no event of the segment's contract reaches its
// filter bucket: the selected product filter when the card prices one,
// else the default bucket, reached by the events matching none of the
// product's filters. A recurring metric is never bypassed.
//
// source: app/services/fees/charge_service.rb:492-505, app/services/events/billing_period_filters/base_resolver.rb:56-77, app/services/events/billing_period_filters/event_matching_service.rb:15-34
func (e *SegmentEvents) bypass(ps ProductSegment, m *catalog.BillableMetric, filters []*catalog.ProductFilter, metric []metering.MetricFilter, selected *catalog.ProductFilter) bool {
	if e == nil || m.Recurring {
		return false
	}
	for _, p := range e.combos[segmentEventsKey{contract: ps.Contract.ID, code: m.Code}] {
		if selected != nil {
			if valuesMatch(filterValues(selected, metric), p) {
				return false
			}
			continue
		}
		if !slices.ContainsFunc(filters, func(f *catalog.ProductFilter) bool { return valuesMatch(filterValues(f, metric), p) }) {
			return false
		}
	}
	return true
}

// valuesMatch reports whether the properties carry, for every key of
// values, one of its values.
func valuesMatch(values map[string][]string, p metering.Properties) bool {
	for k, vs := range values {
		v, ok := p[k]
		if !ok || !slices.Contains(vs, v) {
			return false
		}
	}
	return true
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
