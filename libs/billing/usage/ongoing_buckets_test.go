package usage_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// bucketReads is the harness event store recording the codes of the
// usage bucket reads, which no definition answers.
type bucketReads struct {
	*billingtest.Events
	codes []string
}

func (b *bucketReads) ReadBuckets(_ context.Context, r metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	if !slices.Contains(b.codes, r.Query.Code) {
		b.codes = append(b.codes, r.Query.Code)
	}
	return nil, false, nil
}

// TestOngoingUsageFromBuckets checks that the usage of the wallet refresh
// reads the usage buckets, asking for no presentation breakdown, so a
// charge breaking its usage down by presentation keys is read from them
// too.
//
// source: spec/services/customers/refresh_wallets_service_spec.rb:260, 272
func TestOngoingUsageFromBuckets(t *testing.T) {
	t.Parallel()
	june := billingtest.Date(2025, time.June, 15)
	f := newFixture(t, june, "1")
	regions := f.Metric("regions", catalog.AggregationTypeCount, "")
	f.Charge(f.plan, regions, catalog.ChargeModelStandard, `{"amount":"1","presentation_group_keys":[{"value":"region"}]}`)
	f.events(f.sub, 2, june.Add(-time.Hour))
	f.Event(f.sub, regions.Code, june.Add(-time.Hour), "region", "eu")
	probe := &bucketReads{Events: f.Events}
	d := *f.Deps
	d.Events = probe
	svc := usage.New(&d)

	f.Do(func(ctx context.Context, r port.Repos) error {
		_, _, err := svc.OngoingUsageComputed(ctx, r, f.cust)
		return err
	})
	slices.Sort(probe.codes)
	if want := []string{f.metric.Code, regions.Code}; !slices.Equal(probe.codes, want) {
		t.Errorf("bucket reads: got = %v, want = %v", probe.codes, want)
	}
	if opts := usage.WalletRefreshOptions(); !opts.UsageBuckets || opts.Filters.Presentation == nil || len(opts.Filters.Presentation) != 0 {
		t.Errorf("WalletRefreshOptions: got = %+v, want = the buckets and an empty presentation filter", opts)
	}
}
