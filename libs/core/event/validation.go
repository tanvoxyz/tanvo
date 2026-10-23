package event

import (
	"regexp"
	"slices"
	"time"
)

// ValidationErrors are the transaction IDs, each listed once in ascending
// order, of the events that fail post-validation (domain §10.2), the
// payload of the events.errors webhook (lago Events::PostValidationService
// and db/views/last_hour_events_mv_v04.sql):
//
//   - an event whose code names no billable metric, kept or discarded, has
//     an invalid code; an event whose code names only discarded metrics is
//     not checked;
//   - an event of a metric whose aggregation reads a field misses its
//     aggregation property when the field is absent, or, for the numeric
//     aggregations (sum, max, weighted_sum, latest), when its value is not a
//     decimal number ([IsValidationNumber]);
//   - an event that carries the key of one of its metric's filters with a
//     value outside that filter's values has invalid filter values.
type ValidationErrors struct {
	// InvalidCode are the events with an invalid code.
	InvalidCode []string `json:"invalid_code"`
	// MissingAggregationProperty are the events missing their aggregation
	// property.
	MissingAggregationProperty []string `json:"missing_aggregation_property"`
	// InvalidFilterValues are the events with invalid filter values.
	InvalidFilterValues []string `json:"invalid_filter_values"`
}

// Empty reports whether no event failed validation.
func (v ValidationErrors) Empty() bool {
	return len(v.InvalidCode) == 0 && len(v.MissingAggregationProperty) == 0 && len(v.InvalidFilterValues) == 0
}

// Normalize sorts each list and removes its duplicates, and makes the
// empty lists non-nil so that they encode as [].
func (v *ValidationErrors) Normalize() {
	for _, l := range []*[]string{&v.InvalidCode, &v.MissingAggregationProperty, &v.InvalidFilterValues} {
		slices.Sort(*l)
		*l = slices.Compact(*l)
		if *l == nil {
			*l = []string{}
		}
	}
}

// validationNumber is the pattern of a numeric aggregation property
// (last_hour_events_mv_v04.sql:25).
var validationNumber = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// IsValidationNumber reports whether s is a decimal number as
// post-validation reads it: an optional minus sign, digits, and an
// optional fraction.
func IsValidationNumber(s string) bool { return validationNumber.MatchString(s) }

// ValidationWindow returns the window of events the post-validation run at
// instant at checks: the hour before at's hour, in UTC (lago
// date_trunc('hour', NOW()) - INTERVAL '1 hour', last_hour_events_mv_v04.sql:32-33).
func ValidationWindow(at time.Time) (from, to time.Time) {
	to = at.UTC().Truncate(time.Hour)
	return to.Add(-time.Hour), to
}
