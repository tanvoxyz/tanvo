package metering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// BucketWidth is the width of a usage bucket. Every UTC offset in use is a
// multiple of 15 minutes, so the day (and hour) boundaries of every time
// zone fall on bucket boundaries.
const BucketWidth = 15 * time.Minute

// TargetWalletCodeProperty is the event property naming the wallet an event
// targets. Every [BucketDefinition] keeps it apart, so that the buckets
// carry the target wallet code of their events.
const TargetWalletCodeProperty = "target_wallet_code"

// BucketOtherValue is the dimension value of the events whose value of a
// dimension with declared values is not one of them (see
// [BucketDefinition.Values]).
const BucketOtherValue = "__tanvo_other_value__"

// BucketFloor returns the start of the bucket holding t, in UTC.
func BucketFloor(t time.Time) time.Time { return t.UTC().Truncate(BucketWidth) }

// BucketCeil returns the first bucket boundary at or after t, in UTC.
func BucketCeil(t time.Time) time.Time {
	f := BucketFloor(t)
	if f.Equal(t) {
		return f
	}
	return f.Add(BucketWidth)
}

// BucketDefinition declares the usage buckets an event store maintains for
// a billable metric code of an organization: per external subscription and
// 15-minute UTC bucket, one row per combination of the values of the
// dimension properties, holding the number of events, the sum of their
// precise amounts and, for the aggregated property, the number of numeric
// values, their sum, their maximum and the latest one.
//
// Buckets are keyed by property values rather than by charge and charge
// filter: a query is answered from them when every property its charge
// filter, pricing group values and grouping keys read is a dimension, and
// filters are matched on the dimensions the same way they are matched on
// the properties of the events. Editing charge filters thus never stales
// the buckets; only a new filter key needs a new definition.
type BucketDefinition struct {
	OrganizationID string `json:"organization_id"`
	Code           string `json:"code"`
	// Property is the aggregated property; empty for buckets that only
	// count events.
	Property string `json:"property,omitempty"`
	// Dimensions are the properties the rows keep apart.
	Dimensions []string `json:"dimensions,omitempty"`
	// Values, for the dimensions that are charge filter keys, are the
	// values the billable metric filter declares: the rows keep these
	// apart and fold every other non-empty value into [BucketOtherValue],
	// which bounds their number. Charge filters only match declared values
	// (see [AllFilterValues]), so they read the folded rows as the events.
	// A dimension without declared values keeps every value apart.
	Values map[string][]string `json:"values,omitempty"`
}

// Normalize returns the definition with sorted, distinct dimensions that
// include [TargetWalletCodeProperty], and sorted, distinct declared values
// of dimensions only.
func (d BucketDefinition) Normalize() BucketDefinition {
	dims := append(slices.Clone(d.Dimensions), TargetWalletCodeProperty)
	slices.Sort(dims)
	d.Dimensions = slices.Compact(dims)
	var values map[string][]string
	for k, vs := range d.Values {
		if len(vs) == 0 || !slices.Contains(d.Dimensions, k) {
			continue
		}
		vs = slices.Clone(vs)
		slices.Sort(vs)
		if values == nil {
			values = map[string][]string{}
		}
		values[k] = slices.Compact(vs)
	}
	d.Values = values
	return d
}

// Dimension returns the dimension value of a property value of the
// dimension key: the value itself, or [BucketOtherValue] for a non-empty
// value outside the declared values of the key.
func (d BucketDefinition) Dimension(key, value string) string {
	declared, ok := d.Values[key]
	if !ok || value == "" {
		return value
	}
	if _, found := slices.BinarySearch(declared, value); found {
		return value
	}
	return BucketOtherValue
}

// DimensionsOf returns the dimensions of event properties: the dimension
// properties present, folded with [BucketDefinition.Dimension]. d must be
// normalized.
func (d BucketDefinition) DimensionsOf(p Properties) Properties {
	out := Properties{}
	for _, k := range d.Dimensions {
		if v, ok := p[k]; ok {
			out[k] = d.Dimension(k, v)
		}
	}
	return out
}

// ID returns the identifier of the definition within its organization and
// code: a hash of the property and the normalized dimensions.
func (d BucketDefinition) ID() string {
	n := d.Normalize()
	h := sha256.New()
	h.Write([]byte(n.Property))
	for _, k := range n.Dimensions {
		h.Write([]byte{0})
		h.Write([]byte(k))
		if vs, ok := n.Values[k]; ok {
			h.Write([]byte{1})
			for _, v := range vs {
				h.Write([]byte{2})
				h.Write([]byte(v))
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Covers reports whether the buckets of the definition answer the read:
// same code, the read's property when the read takes numeric values, and
// every key the read looks at among the dimensions.
func (d BucketDefinition) Covers(r BucketRead) bool {
	q := r.Query
	if d.OrganizationID != q.OrganizationID || d.Code != q.Code {
		return false
	}
	if q.Numeric && d.Property != q.Property {
		return false
	}
	n := d.Normalize()
	for _, k := range r.Keys() {
		if _, ok := slices.BinarySearch(n.Dimensions, k); !ok {
			return false
		}
	}
	// A folded dimension answers the charge filter values it declares
	// (and the empty value, never folded), not grouping keys.
	for k := range n.Values {
		if slices.Contains(r.By, k) || slices.Contains(r.Columns, k) {
			return false
		}
		if _, ok := q.GroupValues[k]; ok {
			return false
		}
		values := slices.Clone(q.Filter.Matching[k])
		for _, clause := range q.Filter.Ignored {
			values = append(values, clause[k]...)
		}
		for _, v := range values {
			if n.Dimension(k, v) != v || v == BucketOtherValue {
				return false
			}
		}
	}
	return true
}

// CoveringDefinition returns the definition, among defs, whose buckets
// answer the read with the fewest dimensions (ties broken by ID).
func CoveringDefinition(defs []BucketDefinition, r BucketRead) (BucketDefinition, bool) {
	var (
		best  BucketDefinition
		found bool
	)
	for _, d := range defs {
		if !d.Covers(r) {
			continue
		}
		d = d.Normalize()
		if !found || len(d.Dimensions) < len(best.Dimensions) ||
			(len(d.Dimensions) == len(best.Dimensions) && d.ID() < best.ID()) {
			best, found = d, true
		}
	}
	return best, found
}

// BucketDefinitionFor returns the definition serving the charges of a
// billable metric with the metric filters, whose pricing groups and
// breakdowns read the given keys. Filter keys fold the values their metric
// filter does not declare, unless a pricing group or breakdown reads them.
func BucketDefinitionFor(organizationID string, m Metric, filters []MetricFilter, keys ...[]string) BucketDefinition {
	d := BucketDefinition{OrganizationID: organizationID, Code: m.Code}
	if m.Aggregation != AggregationCount {
		d.Property = m.FieldName
	}
	grouped := slices.Concat(keys...)
	for _, f := range filters {
		d.Dimensions = append(d.Dimensions, f.Key)
		if len(f.Values) > 0 && !slices.Contains(grouped, f.Key) {
			if d.Values == nil {
				d.Values = map[string][]string{}
			}
			d.Values[f.Key] = append(d.Values[f.Key], f.Values...)
		}
	}
	d.Dimensions = append(d.Dimensions, grouped...)
	return d.Normalize()
}

// BucketRead reads the usage buckets of a query over whole buckets.
type BucketRead struct {
	// Query scopes the read (organization, subscription, code), narrows it
	// (charge filter, pricing group values) and, with Numeric, names the
	// aggregated property. Its time window and history settings are not
	// used: From and To bound the read.
	Query Query
	// From and To are bucket boundaries: the read covers [From, To).
	From, To time.Time
	// By are the keys the totals are grouped by.
	By []string
	// Columns are keys read on the row holding the latest value of each
	// group (see [BucketTotal.LatestColumns]).
	Columns []string
	// PerBucket returns the totals per bucket as well.
	PerBucket bool
}

// Keys returns the properties the read looks at: the keys of the charge
// filter, of the pricing group values, the grouping keys and the columns,
// sorted and distinct.
func (r BucketRead) Keys() []string {
	q := r.Query
	keys := slices.Concat(slices.Collect(maps.Keys(q.Filter.Matching)), slices.Collect(maps.Keys(q.GroupValues)), r.By, r.Columns)
	for _, clause := range q.Filter.Ignored {
		for k, values := range clause {
			if len(values) > 0 {
				keys = append(keys, k)
			}
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// BucketTotal is the total of the buckets of one group (and one bucket,
// with [BucketRead.PerBucket]).
type BucketTotal struct {
	// Groups are the values of [BucketRead.By], nil when missing or empty.
	Groups Groups `json:"groups"`
	// Bucket is the start of the bucket, with [BucketRead.PerBucket].
	Bucket time.Time `json:"bucket,omitzero"`
	// Count is the number of events: of events carrying a numeric value of
	// the property when the query is Numeric (possibly 0).
	Count int64 `json:"count"`
	// EventsCount is the number of events, numeric values or not.
	EventsCount int64 `json:"events_count"`
	// Sum, Max and Latest are the sum, the largest and the most recent (by
	// timestamp, then ingestion) of the numeric values; Max and Latest are
	// nil without any. They are only read for Numeric queries.
	Sum    money.Decimal  `json:"sum"`
	Max    *money.Decimal `json:"max,omitempty"`
	Latest *money.Decimal `json:"latest,omitempty"`
	// LatestColumns are the values of [BucketRead.Columns] on the event
	// holding Latest.
	LatestColumns Groups `json:"latest_columns,omitempty"`
	// PreciseTotalAmountCents is the sum of the precise amounts of the
	// events counted.
	PreciseTotalAmountCents money.Decimal `json:"precise_total_amount_cents"`
	// LastEventAt and LastIngestedAt are the latest timestamp and ingestion
	// time of the events counted.
	LastEventAt    time.Time `json:"last_event_at,omitzero"`
	LastIngestedAt time.Time `json:"last_ingested_at,omitzero"`
}

// BucketReader reads pre-aggregated usage buckets. The PostgreSQL and
// ClickHouse event stores implement it next to [Store].
type BucketReader interface {
	// ReadBuckets returns the totals of the buckets in [r.From, r.To) of
	// the definition covering the read (see [CoveringDefinition]), one per
	// group with events, and ok false when no ready definition covers it.
	// The definition is checked in the same snapshot as the rows, so that
	// a concurrent drop never reads as an empty usage.
	ReadBuckets(ctx context.Context, r BucketRead) (totals []BucketTotal, ok bool, err error)
}

// ErrBucketsUnavailable reports a read the usage buckets cannot answer.
var ErrBucketsUnavailable = errors.New("metering: usage buckets unavailable")

// BucketAggregations are the aggregations the usage buckets serve: their
// totals recompose across buckets.
var BucketAggregations = []AggregationType{AggregationCount, AggregationSum, AggregationMax, AggregationLatest}

// BucketChargeModels are the charge models priced from totals only, which
// the usage buckets serve. Percentage charges read their events one by one
// for free units, and custom charges evaluate each event.
var BucketChargeModels = []string{"standard", "graduated", "package", "volume", "graduated_percentage", "dynamic"}

// BucketCharge is the part of a charge deciding whether its usage is served
// from the buckets.
type BucketCharge struct {
	ChargeModel         string `json:"charge_model"`
	PayInAdvance        bool   `json:"pay_in_advance,omitempty"`
	Prorated            bool   `json:"prorated,omitempty"`
	AcceptsTargetWallet bool   `json:"accepts_target_wallet,omitempty"`
}

// BucketUnsupportedReason returns why the usage of a charge of the metric
// is not served from the buckets, or "" when it is.
func BucketUnsupportedReason(m Metric, c BucketCharge) string {
	switch {
	case !slices.Contains(BucketChargeModels, c.ChargeModel):
		return "unsupported_charge_model"
	case !slices.Contains(BucketAggregations, m.Aggregation):
		return "unsupported_aggregation_type"
	case c.PayInAdvance:
		return "pay_in_advance"
	case c.Prorated:
		return "prorated"
	case m.Recurring:
		return "recurring_metric"
	case c.AcceptsTargetWallet:
		return "target_wallet"
	}
	return ""
}

// BucketServable reports whether the usage of a charge of the metric is
// served from the buckets; billing then sets [Request.UsageBuckets].
func BucketServable(m Metric, c BucketCharge) bool { return BucketUnsupportedReason(m, c) == "" }

// bucketRequest reports whether the aggregation of the request may read
// the usage buckets.
func bucketRequest(r Request) bool {
	return r.UsageBuckets && slices.Contains(BucketAggregations, r.Metric.Aggregation) &&
		!r.Metric.Recurring && !r.Prorated && !r.PayInAdvance && r.MaxTimestamp.IsZero() && r.Event == nil
}

// projectGroups returns the values of keys among groups.
func projectGroups(g Groups, keys []string) Groups {
	out := make(Groups, len(keys))
	for _, k := range keys {
		out[k] = g[k]
	}
	return out
}

// unionKeys returns the keys of a followed by those of b not in a.
func unionKeys(a, b []string) []string {
	out := slices.Clone(a)
	for _, k := range b {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}
