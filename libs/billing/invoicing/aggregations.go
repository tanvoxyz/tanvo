package invoicing

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// cacheAggregations stores the aggregation states of a fee computation
// (domain §10.3). A pay-in-advance event's state is always a new row
// (lago Fees::CreatePayInAdvanceService#cache_aggregation_result); a
// recurring state is kept once per charge, group and instant (lago
// Fees::ChargeService#persist_recurring_value, find_or_initialize_by).
func (s *Service) cacheAggregations(ctx context.Context, r port.Repos, org core.OrganizationID, res *fees.ChargeResult, inAdvance bool) error {
	if res == nil {
		return nil
	}
	for _, c := range res.Aggregations {
		a := kernel.StoredCachedAggregation(s.d, org, c.ExternalSubscriptionID, c.ChargeID, c.ChargeFilterID, c.Aggregation)
		var err error
		if inAdvance {
			err = r.CachedAggregations.Create(ctx, a)
		} else {
			_, err = r.CachedAggregations.CreateIfAbsent(ctx, a)
		}
		if err != nil {
			return fmt.Errorf("cache aggregation of charge %s: %w", c.ChargeID, err)
		}
	}
	return nil
}

// aggregationCache returns the fee options' cache of the stored cached
// aggregations of env's subscription.
func aggregationCache(r port.Repos, env *kernel.SubscriptionEnv) func(event.CachedAggregationScope) metering.CachedAggregations {
	return kernel.AggregationCache(r, env.OrganizationID())
}
