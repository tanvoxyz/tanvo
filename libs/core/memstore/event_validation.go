package memstore

import (
	"context"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
)

func (r eventLogRepo) ValidationErrors(_ context.Context, org core.OrganizationID, from, to time.Time) (event.ValidationErrors, error) {
	var out event.ValidationErrors
	evs := r.d.events.find(func(x *event.Event) bool {
		return x.OrganizationID == org && !x.CreatedAt.Before(from) && x.CreatedAt.Before(to)
	})
	for _, e := range evs {
		// Every metric with the code, discarded ones included, as the view
		// joins billable_metrics on the code alone.
		ms := r.c.metrics.find(func(m *catalog.BillableMetric) bool { return m.OrganizationID == org && m.Code == e.Code })
		if len(ms) == 0 {
			out.InvalidCode = append(out.InvalidCode, e.TransactionID)
			continue
		}
		for _, m := range ms {
			if m.DeletedAt != nil {
				continue
			}
			if validationMissingProperty(m, e) {
				out.MissingAggregationProperty = append(out.MissingAggregationProperty, e.TransactionID)
			}
			for _, f := range m.Filters {
				v, ok := e.Properties[f.Key]
				if !ok {
					continue
				}
				valid := false
				for _, x := range f.Values {
					valid = valid || x == v
				}
				if !valid {
					out.InvalidFilterValues = append(out.InvalidFilterValues, e.TransactionID)
				}
			}
		}
	}
	out.Normalize()
	return out, nil
}

// validationMissingProperty reports whether e misses the aggregation
// property of m.
func validationMissingProperty(m *catalog.BillableMetric, e *event.Event) bool {
	if m.AggregationType == catalog.AggregationTypeCount {
		return false
	}
	if m.FieldName == nil {
		return true
	}
	v, ok := e.Properties[*m.FieldName]
	if !ok {
		return true
	}
	switch m.AggregationType {
	case catalog.AggregationTypeSum, catalog.AggregationTypeMax, catalog.AggregationTypeWeightedSum, catalog.AggregationTypeLatest:
		return !event.IsValidationNumber(v)
	}
	return false
}
