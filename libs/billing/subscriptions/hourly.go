package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// HourlyWindow is the default length of an hourly usage window, ending now.
const HourlyWindow = 24 * time.Hour

// ErrHourlyUnavailable is an hourly usage the usage buckets cannot answer:
// a charge they do not serve, a metric whose hours do not add up (neither
// a count nor a sum), or an event store without buckets (lago
// forbidden_failure feature_unavailable).
var ErrHourlyUnavailable = errors.New("subscriptions: hourly usage unavailable")

// The field error codes of [Service.HourlyUsage] (lago validation_failure).
const (
	// CodeInvalidWindow: the window does not end after it starts.
	CodeInvalidWindow = "invalid_window"
	// CodeWindowTooLong: the window spans more than
	// [metering.HourlyMaxWindow].
	CodeWindowTooLong = "window_too_long"
)

// BucketDefiner adds usage bucket definitions to an event store (see
// [kernel.BucketDefiner]). An event store implementing it next to
// [metering.BucketReader] gets the definition an hourly usage reads
// defined on its first read.
type BucketDefiner = kernel.BucketDefiner

// HourlyUsageInput is the request of [Service.HourlyUsage].
type HourlyUsageInput struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// Customer is the customer.
	Customer CustomerRef
	// Subscription is one of the customer's subscriptions, of any status.
	Subscription SubscriptionRef
	// ChargeID is a charge of the subscription's plan.
	ChargeID core.ChargeID
	// From and To bound the window; To defaults to now and From to
	// [HourlyWindow] before To.
	From, To time.Time
}

// HourlyUsage is the usage of a charge per hour of the customer's time
// zone, broken down by charge filter.
type HourlyUsage struct {
	// Usage is the breakdown; its series are keyed by charge filter ID, ""
	// for the charge default and [metering.HourlyOtherKey] for the folded
	// series.
	Usage metering.HourlyUsageResult
	// Metric is the charge's billable metric.
	Metric *catalog.BillableMetric
	// Filters are the charge's filters by ID.
	Filters map[string]*catalog.ChargeFilter
}

// HourlyUsage returns the usage of a charge of a customer's subscription
// per hour of the customer's time zone, by charge filter, read from the
// usage buckets (lago RealtimeUsage::HourlyBreakdownService, through the
// subscriptionHourlyUsage query). Only count and sum charges the buckets
// serve are answered, else [ErrHourlyUnavailable]. The window starts on
// the hour at or before From (never before the subscription started) and
// ends at the end of the bucket holding To.
//
// When no bucket definition covers the charge and the event store is a
// [BucketDefiner], the definition is defined (its buckets backfilled from
// the stored events) and the read retried: lago's buckets are filled by a
// stream for every supported charge, Tanvo's are declared per metric.
//
// source: app/graphql/resolvers/subscriptions/hourly_usage_resolver.rb:20-35, app/services/realtime_usage/hourly_breakdown_service.rb:27-55
func (s *Service) HourlyUsage(ctx context.Context, in HourlyUsageInput) (*HourlyUsage, error) {
	org := in.OrganizationID
	var (
		out *HourlyUsage
		req metering.HourlyRequest
		def metering.BucketDefinition
	)
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := findCustomer(ctx, r, org, in.Customer)
		if err != nil {
			return err
		}
		sub, err := findSubscription(ctx, r, org, in.Subscription)
		if err != nil {
			return err
		}
		if sub.CustomerID != c.ID {
			return core.NotFound(Resource)
		}
		env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
		if err != nil {
			return err
		}
		charges, err := env.Charges(ctx, r)
		if err != nil {
			return err
		}
		var charge *catalog.Charge
		for _, ch := range charges {
			if ch.ID == in.ChargeID {
				charge = ch
			}
		}
		if charge == nil {
			return core.NotFound(ResourceCharge)
		}
		m, err := r.Metrics.Get(ctx, org, charge.BillableMetricID)
		if err != nil {
			return fmt.Errorf("load metric of charge %s: %w", charge.ID, err)
		}
		metric := kernel.MeteringMetric(m)
		if metering.BucketUnsupportedReason(metric, metering.BucketCharge{
			ChargeModel: string(charge.ChargeModel), PayInAdvance: charge.PayInAdvance,
			Prorated: charge.Prorated, AcceptsTargetWallet: charge.AcceptsTargetWallet,
		}) != "" || metric.Aggregation != metering.AggregationCount && metric.Aggregation != metering.AggregationSum {
			return fmt.Errorf("charge %s (%s, %s): %w", charge.ID, charge.ChargeModel, m.AggregationType, ErrHourlyUnavailable)
		}
		metricFilters := kernel.MeteringMetricFilters(m)
		series, err := metering.HourlySeriesFor(kernel.MeteringChargeFilters(charge), metricFilters)
		if err != nil {
			return fmt.Errorf("series of charge %s: %w", charge.ID, err)
		}
		to := in.To
		if to.IsZero() {
			to = s.d.Clock.Now()
		}
		from := in.From
		if from.IsZero() {
			from = to.Add(-HourlyWindow)
		}
		req = metering.HourlyRequest{
			Query:    metering.Query{OrganizationID: org.String(), SubscriptionID: sub.ExternalID, Code: m.Code},
			Metric:   metric,
			From:     from,
			To:       to,
			Location: env.Location(),
			Series:   series,
		}
		if sub.StartedAt != nil {
			req.Start = *sub.StartedAt
		}
		def = metering.BucketDefinitionFor(org.String(), metric, metricFilters)
		out = &HourlyUsage{Metric: m, Filters: make(map[string]*catalog.ChargeFilter, len(charge.Filters))}
		for i := range charge.Filters {
			out.Filters[charge.Filters[i].ID.String()] = &charge.Filters[i]
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hourly usage: %w", err)
	}
	if out.Usage, err = s.hourly(ctx, req, def); err != nil {
		return nil, fmt.Errorf("hourly usage: %w", err)
	}
	return out, nil
}

// hourly reads the hourly usage of req from the buckets, defining def
// first when no definition covers it.
func (s *Service) hourly(ctx context.Context, req metering.HourlyRequest, def metering.BucketDefinition) (metering.HourlyUsageResult, error) {
	buckets, ok := s.d.Events.(metering.BucketReader)
	if !ok {
		return metering.HourlyUsageResult{}, fmt.Errorf("event store without usage buckets: %w", ErrHourlyUnavailable)
	}
	res, err := metering.HourlyUsage(ctx, s.d.Events, buckets, req)
	if definer, ok := s.d.Events.(BucketDefiner); ok && errors.Is(err, metering.ErrBucketsUnavailable) {
		if err = definer.DefineBuckets(ctx, def); err == nil {
			res, err = metering.HourlyUsage(ctx, s.d.Events, buckets, req)
		}
	}
	var v core.Validator
	switch {
	case errors.Is(err, metering.ErrHourlyInvalidWindow):
		v.Add("from", CodeInvalidWindow)
		return metering.HourlyUsageResult{}, v.Err()
	case errors.Is(err, metering.ErrHourlyWindowTooLong):
		v.Add("to", CodeWindowTooLong)
		return metering.HourlyUsageResult{}, v.Err()
	case errors.Is(err, metering.ErrBucketsUnavailable):
		return metering.HourlyUsageResult{}, fmt.Errorf("%w: %w", ErrHourlyUnavailable, err)
	case err != nil:
		return metering.HourlyUsageResult{}, err
	}
	return res, nil
}
