package metering

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// IngestionWatermarks is implemented by the stores that report the latest
// ingestion of the events a query selects. The current-usage cache of the
// billing engine validates its entries against it: an entry is kept while
// no event was ingested after the one it aggregated (lago
// Subscriptions::ChargeCacheService lazy validation).
type IngestionWatermarks interface {
	// LastIngestedAt returns the latest instant an event selected by q
	// was stored at, the zero time when q selects none. A store routing
	// to another that does not report it fails with [ErrNotImplemented].
	LastIngestedAt(ctx context.Context, q Query) (time.Time, error)
}

// Watermark returns the ingestion watermark of the events the aggregation
// req reads: the latest instant one of them was stored at; for a recurring
// metric, whose usage carries over, the events of every earlier period
// count too and the watermark is at least the start of the period (lago
// Events::BillingPeriodFilters::BaseResolver, base_resolver.rb:32-52). ok
// is false when the store reports no watermark, when req is answered from
// the usage buckets, which are fresh already (lago charge_service.rb:140),
// or when req is capped by [Request.MaxTimestamp] or bound to an event; the
// result must not be cached then. A zero watermark with ok means that no
// event is read.
func Watermark(ctx context.Context, store Store, req Request) (time.Time, bool, error) {
	w, isW := store.(IngestionWatermarks)
	if !isW || !req.MaxTimestamp.IsZero() || req.Event != nil {
		return time.Time{}, false, nil
	}
	if _, ok := store.(BucketReader); ok && bucketRequest(req) {
		return time.Time{}, false, nil
	}
	q := Query{
		OrganizationID: req.OrganizationID,
		SubscriptionID: req.SubscriptionID,
		Code:           req.Metric.Code,
		From:           req.From,
		To:             req.To,
		IncludeHistory: req.Metric.Recurring,
		Filter:         req.Filter,
		GroupValues:    req.GroupValues,
		Location:       req.Location,
	}
	at, err := w.LastIngestedAt(ctx, q)
	switch {
	case errors.Is(err, ErrNotImplemented):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("metering: watermark of %s: %w", req.Metric.Code, err)
	}
	at = at.UTC()
	if req.Metric.Recurring && at.Before(req.From) {
		at = req.From.UTC()
	}
	return at, true, nil
}

var _ IngestionWatermarks = (*MemoryStore)(nil)

// LastIngestedAt implements [IngestionWatermarks].
func (s *MemoryStore) LastIngestedAt(ctx context.Context, q Query) (time.Time, error) {
	if err := checkContext(ctx); err != nil {
		return time.Time{}, err
	}
	var at time.Time
	for _, e := range s.selectEvents(q) {
		if e.IngestedAt.After(at) {
			at = e.IngestedAt
		}
	}
	return at.UTC(), nil
}
