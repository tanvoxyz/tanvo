package metering

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// HourlyMaxWindow is the longest window [HourlyUsage] reads.
const HourlyMaxWindow = 31 * 24 * time.Hour

// HourlyOtherKey is the key of the series folding the smallest ones, which
// no charge filter ID takes.
const HourlyOtherKey = "__other__"

// HourlyMaxSeries is the default number of series [HourlyUsage] keeps
// apart; the others fold into one.
const HourlyMaxSeries = 20

var (
	// ErrHourlyInvalidWindow reports a window that does not end after it
	// starts.
	ErrHourlyInvalidWindow = errors.New("metering: hourly usage window is empty")
	// ErrHourlyWindowTooLong reports a window longer than [HourlyMaxWindow].
	ErrHourlyWindowTooLong = errors.New("metering: hourly usage window is too long")
)

// HourlySeries is one series of an hourly breakdown: the usage of one
// charge filter (or of the charge default, matching no filter).
type HourlySeries struct {
	// Key identifies the series, typically the charge filter ID; "" for
	// the charge default.
	Key string `json:"key"`
	// Filter selects the events of the series.
	Filter Filter `json:"filter"`
}

// HourlyRequest asks for the hourly usage of a charge.
type HourlyRequest struct {
	// Query scopes the read: organization, external subscription and code.
	// Its window, filter and history settings are not used.
	Query Query `json:"query"`
	// Metric is the billable metric of the charge: count or sum.
	Metric Metric `json:"metric"`
	// From and To bound the window. It starts on the hour, in Location, at
	// or before From, and ends at To, or at the end of the bucket holding
	// To when To is not on a bucket boundary.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Location is the customer's time zone; UTC when nil.
	Location *time.Location `json:"-"`
	// Start, when set, is the start of the subscription: the window never
	// starts before it, as the events before it may belong to the
	// subscription it replaced under the same external ID. The partial
	// bucket it opens is read from the events.
	Start time.Time `json:"start,omitzero"`
	// Series are the series of the breakdown; one series matching every
	// event when empty.
	Series []HourlySeries `json:"series,omitempty"`
	// MaxSeries is the number of series kept apart, biggest first; the
	// others fold into one "other" series. [HourlyMaxSeries] when zero.
	MaxSeries int `json:"max_series,omitempty"`
}

// HourlySeriesTotal is the total of one series over the window.
type HourlySeriesTotal struct {
	// Key is the key of the series; [HourlyOtherKey] for the folded one.
	Key   string        `json:"key"`
	Units money.Decimal `json:"units"`
	// EventsCount is the number of events, numeric values or not.
	EventsCount int64 `json:"events_count"`
	// Other marks the series folding the smallest ones.
	Other bool `json:"other,omitempty"`
}

// HourlySeriesFor returns the disjoint series of the filters of a charge:
// the charge default (key "") and one per filter (keyed by its ID), each
// selecting the events billed on it (see [ChargeFilterScope]).
func HourlySeriesFor(filters []ChargeFilter, metric []MetricFilter) ([]HourlySeries, error) {
	ids := make([]string, 0, len(filters)+1)
	ids = append(ids, "")
	for _, f := range filters {
		ids = append(ids, f.ID)
	}
	out := make([]HourlySeries, 0, len(ids))
	for _, id := range ids {
		scope, err := ChargeFilterScope(filters, metric, id)
		if err != nil {
			return nil, err
		}
		out = append(out, HourlySeries{Key: id, Filter: scope})
	}
	return out, nil
}

// Hour is the usage of one hour, per series.
type Hour struct {
	// Time is the start of the hour.
	Time        time.Time     `json:"time"`
	Units       money.Decimal `json:"units"`
	EventsCount int64         `json:"events_count"`
	// Usages holds one entry per series of [HourlyUsageResult.Series], in
	// the same order.
	Usages []HourlySeriesTotal `json:"usages"`
}

// HourlyUsageResult is the hourly breakdown of the usage of a charge.
type HourlyUsageResult struct {
	// From and To are the window the units describe.
	From           time.Time       `json:"from"`
	To             time.Time       `json:"to"`
	Location       string          `json:"timezone"`
	Aggregation    AggregationType `json:"aggregation_type"`
	LastIngestedAt time.Time       `json:"last_ingested_at,omitzero"`
	// Series are the series with usage in the window, biggest first.
	Series []HourlySeriesTotal `json:"series"`
	// Hours are the hours of the window, gaps included.
	Hours []Hour `json:"hours"`
}

// hourWall returns the start of the hour holding t in loc.
func hourWall(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return t.Add(-time.Duration(l.Minute())*time.Minute - time.Duration(l.Second())*time.Second - time.Duration(l.Nanosecond())).UTC()
}

// HourlyUsage returns the usage of a count or sum charge per hour of the
// customer's time zone, read from the usage buckets, and from events for
// the partial bucket a subscription start opens (see [HourlyRequest.Start]).
// It returns [ErrBucketsUnavailable] when the metric is not summed per hour
// or no bucket definition covers a series.
func HourlyUsage(ctx context.Context, events Store, buckets BucketReader, req HourlyRequest) (HourlyUsageResult, error) {
	agg := req.Metric.Aggregation
	if agg != AggregationCount && agg != AggregationSum || req.Metric.Recurring {
		return HourlyUsageResult{}, fmt.Errorf("hourly usage of %s: %w", agg, ErrBucketsUnavailable)
	}
	if !req.From.Before(req.To) {
		return HourlyUsageResult{}, ErrHourlyInvalidWindow
	}
	loc := cmp.Or(req.Location, time.UTC)
	start := hourWall(req.From, loc)
	if start.Before(req.Start) {
		start = req.Start.UTC()
	}
	end := BucketCeil(req.To)
	if !start.Before(end) {
		return HourlyUsageResult{}, ErrHourlyInvalidWindow
	}
	// The partial bucket opening the window, after a subscription start.
	whole := BucketCeil(start)
	if end.Sub(start) > HourlyMaxWindow {
		return HourlyUsageResult{}, ErrHourlyWindowTooLong
	}
	series := req.Series
	if len(series) == 0 {
		series = []HourlySeries{{}}
	}
	maxSeries := req.MaxSeries
	if maxSeries <= 0 {
		maxSeries = HourlyMaxSeries
	}
	q := req.Query
	q.GroupValues, q.IncludeHistory, q.Until, q.UntilTransactionID = nil, false, time.Time{}, ""
	q.Numeric = agg == AggregationSum
	q.Property = ""
	if q.Numeric {
		q.Property = req.Metric.FieldName
	}

	out := HourlyUsageResult{From: start, To: end, Location: loc.String(), Aggregation: agg}
	type perHour = map[time.Time]HourlySeriesTotal
	var (
		totals []HourlySeriesTotal
		hours  []perHour
	)
	for _, s := range series {
		sq := q
		sq.Filter = s.Filter
		var rows []BucketTotal
		if whole.Before(end) {
			var ok bool
			var err error
			rows, ok, err = buckets.ReadBuckets(ctx, BucketRead{Query: sq, From: whole, To: end, PerBucket: true})
			if err != nil {
				return HourlyUsageResult{}, fmt.Errorf("hourly usage of series %q: %w", s.Key, err)
			}
			if !ok {
				return HourlyUsageResult{}, fmt.Errorf("hourly usage of series %q: %w", s.Key, ErrBucketsUnavailable)
			}
		}
		if start.Before(whole) {
			edge, err := edgeTotal(ctx, events, sq, start, whole)
			if err != nil {
				return HourlyUsageResult{}, fmt.Errorf("hourly usage of series %q: %w", s.Key, err)
			}
			rows = append(rows, edge)
		}
		total := HourlySeriesTotal{Key: s.Key, Units: money.Zero}
		byHour := perHour{}
		for _, r := range rows {
			units := r.Sum
			if !q.Numeric {
				units = money.NewFromInt(r.Count)
			}
			total.Units = total.Units.Add(units)
			total.EventsCount += r.EventsCount
			wall := hourWall(r.Bucket, loc)
			h := byHour[wall]
			h.Units = h.Units.Add(units)
			h.EventsCount += r.EventsCount
			byHour[wall] = h
			if r.LastIngestedAt.After(out.LastIngestedAt) {
				out.LastIngestedAt = r.LastIngestedAt
			}
		}
		if total.EventsCount == 0 {
			continue
		}
		totals = append(totals, total)
		hours = append(hours, byHour)
	}

	order := make([]int, len(totals))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmp.Or(totals[b].Units.Cmp(totals[a].Units), cmp.Compare(totals[a].Key, totals[b].Key))
	})
	kept := min(len(order), maxSeries)
	for _, i := range order[:kept] {
		out.Series = append(out.Series, totals[i])
	}
	if len(order) > maxSeries {
		other := HourlySeriesTotal{Key: HourlyOtherKey, Units: money.Zero, Other: true}
		for _, i := range order[maxSeries:] {
			other.Units = other.Units.Add(totals[i].Units)
			other.EventsCount += totals[i].EventsCount
		}
		out.Series = append(out.Series, other)
	}

	for wall := hourWall(start, loc); wall.Before(end); wall = wall.Add(time.Hour) {
		h := Hour{Time: wall, Units: money.Zero, Usages: make([]HourlySeriesTotal, 0, len(out.Series))}
		usage := func(key string, other bool, idx []int) {
			u := HourlySeriesTotal{Key: key, Units: money.Zero, Other: other}
			for _, i := range idx {
				v := hours[i][wall]
				u.Units = u.Units.Add(v.Units)
				u.EventsCount += v.EventsCount
			}
			h.Usages = append(h.Usages, u)
			h.Units = h.Units.Add(u.Units)
			h.EventsCount += u.EventsCount
		}
		for _, i := range order[:kept] {
			usage(totals[i].Key, false, []int{i})
		}
		if len(order) > maxSeries {
			usage(HourlyOtherKey, true, order[maxSeries:])
		}
		out.Hours = append(out.Hours, h)
	}
	return out, nil
}

// edgeTotal returns the total of the events of the series in [from, to),
// as a bucket total of the bucket holding from.
func edgeTotal(ctx context.Context, events Store, q Query, from, to time.Time) (BucketTotal, error) {
	if events == nil {
		return BucketTotal{}, fmt.Errorf("partial bucket without an event store: %w", ErrBucketsUnavailable)
	}
	q.From, q.To = from, to.Add(-time.Microsecond)
	all := q
	all.Numeric, all.Property = false, ""
	n, err := events.Count(ctx, all)
	if err != nil {
		return BucketTotal{}, err
	}
	t := BucketTotal{Bucket: BucketFloor(from), Count: n, EventsCount: n, Sum: money.Zero}
	if q.Numeric {
		sum, err := events.Sum(ctx, q)
		if err != nil {
			return BucketTotal{}, err
		}
		t.Sum, t.Count = sum.Value, sum.Count
	}
	return t, nil
}
