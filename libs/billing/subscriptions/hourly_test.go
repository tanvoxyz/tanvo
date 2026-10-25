package subscriptions_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// bucketEvents is the harness event store with the usage buckets of the
// definitions defined on it.
type bucketEvents struct {
	*billingtest.Events
	defs    []metering.BucketDefinition
	defined int
}

func (b *bucketEvents) ReadBuckets(ctx context.Context, r metering.BucketRead) ([]metering.BucketTotal, bool, error) {
	return b.MemoryStore.Buckets(b.defs...).ReadBuckets(ctx, r)
}

func (b *bucketEvents) DefineBuckets(_ context.Context, d metering.BucketDefinition) error {
	b.defs = append(b.defs, d)
	b.defined++
	return nil
}

// hourlyFixture is a customer subscribed to a plan with a charge of the
// aggregation, whose filter on region eu is named Europe, and 12 eu events
// between 09:30 and 09:41.
type hourlyFixture struct {
	*fixture
	events *bucketEvents
	svc    *subscriptions.Service
	in     subscriptions.HourlyUsageInput
	filter core.ChargeFilterID
}

func newHourlyFixture(t *testing.T, agg catalog.AggregationType, chargeOpts ...func(*catalog.Charge)) *hourlyFixture {
	t.Helper()
	f := newFixture(t)
	be := &bucketEvents{Events: f.Events}
	d := *f.Deps
	d.Events = be
	life := lifecycle.New(&d)
	h := &hourlyFixture{fixture: f, events: be, svc: subscriptions.New(&d, life, usage.New(&d), plans.New(&d, life, nil))}
	c := f.Customer()
	p := f.Plan(period.Monthly, 0)
	field := ""
	if agg != catalog.AggregationTypeCount {
		field = "value"
	}
	m := f.Metric("calls", agg, field, func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{{Key: "region", Values: []string{"eu", "us"}}}
	})
	europe := "Europe"
	ch := f.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, append([]func(*catalog.Charge){func(ch *catalog.Charge) {
		cf := billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"2"}`)
		cf.InvoiceDisplayName = &europe
		ch.Filters = []catalog.ChargeFilter{cf}
	}}, chargeOpts...)...)
	h.filter = ch.Filters[0].ID
	sub := f.Subscription(c, p, billingtest.SubscribedAt(now.AddDate(0, 0, -5)))
	for i := range 12 {
		f.Event(sub, "calls", at(9, 30+i), "region", "eu", "value", "1")
	}
	h.in = subscriptions.HourlyUsageInput{
		OrganizationID: f.OrgID(), Customer: subscriptions.CustomerRef{ExternalID: c.ExternalID},
		Subscription: subscriptions.SubscriptionRef{ExternalID: sub.ExternalID}, ChargeID: ch.ID,
		From: at(9, 20), To: at(11, 5),
	}
	return h
}

// at is an instant of the day of now.
func at(h, m int) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.UTC)
}

// source: spec/graphql/resolvers/subscriptions/hourly_usage_resolver_spec.rb:72
func TestHourlyUsage(t *testing.T) {
	h := newHourlyFixture(t, catalog.AggregationTypeCount)
	got, err := h.svc.HourlyUsage(h.Ctx, h.in)
	if err != nil {
		t.Fatalf("HourlyUsage() error = %v", err)
	}
	u := got.Usage
	if !u.From.Equal(at(9, 0)) || !u.To.Equal(at(11, 15)) || u.Location != "UTC" || u.Aggregation != metering.AggregationCount {
		t.Errorf("window: got = %s %s %s %s, want = 09:00 11:15 UTC count", u.From, u.To, u.Location, u.Aggregation)
	}
	if !u.LastIngestedAt.Equal(now) {
		t.Errorf("last ingested at: got = %s, want = %s", u.LastIngestedAt, now)
	}
	if len(u.Series) != 1 || u.Series[0].Key != h.filter.String() || u.Series[0].Units.String() != "12" || u.Series[0].EventsCount != 12 || u.Series[0].Other {
		t.Fatalf("series: got = %+v, want = the Europe filter with 12 units", u.Series)
	}
	if f := got.Filters[u.Series[0].Key]; f == nil || f.InvoiceDisplayName == nil || *f.InvoiceDisplayName != "Europe" {
		t.Errorf("filter: got = %+v, want = Europe", f)
	}
	if got.Metric.Code != "calls" {
		t.Errorf("metric: got = %s, want = calls", got.Metric.Code)
	}
	var hours []string
	for _, hr := range u.Hours {
		hours = append(hours, hr.Time.Format("15:04")+"="+hr.Units.String())
	}
	if got, want := fmt.Sprint(hours), "[09:00=12 10:00=0 11:00=0]"; got != want {
		t.Errorf("hours: got = %s, want = %s", got, want)
	}
	if b := u.Hours[0].Usages; len(b) != 1 || b[0].Key != h.filter.String() || b[0].Units.String() != "12" || b[0].EventsCount != 12 {
		t.Errorf("breakdown: got = %+v, want = 12 units of the Europe filter", b)
	}
	// The buckets are defined on the first read only.
	if _, err := h.svc.HourlyUsage(h.Ctx, h.in); err != nil {
		t.Fatalf("HourlyUsage() again: error = %v", err)
	}
	if h.events.defined != 1 {
		t.Errorf("definitions: got = %d, want = 1", h.events.defined)
	}
}

func TestHourlyUsageSum(t *testing.T) {
	h := newHourlyFixture(t, catalog.AggregationTypeSum)
	got, err := h.svc.HourlyUsage(h.Ctx, h.in)
	if err != nil {
		t.Fatalf("HourlyUsage() error = %v", err)
	}
	if u := got.Usage; u.Aggregation != metering.AggregationSum || len(u.Series) != 1 || u.Series[0].Units.String() != "12" {
		t.Errorf("got = %s %+v, want = sum of 12", u.Aggregation, u.Series)
	}
}

// The window defaults to the 24 hours ending now.
//
// source: app/services/realtime_usage/hourly_breakdown_service.rb:29-30
func TestHourlyUsageDefaultWindow(t *testing.T) {
	h := newHourlyFixture(t, catalog.AggregationTypeCount)
	in := h.in
	in.From, in.To = time.Time{}, time.Time{}
	got, err := h.svc.HourlyUsage(h.Ctx, in)
	if err != nil {
		t.Fatalf("HourlyUsage() error = %v", err)
	}
	if u := got.Usage; !u.From.Equal(now.Add(-24*time.Hour)) || !u.To.Equal(now) || len(u.Hours) != 24 {
		t.Errorf("window: got = %s to %s, %d hours, want = %s to %s, 24 hours", u.From, u.To, len(u.Hours), now.Add(-24*time.Hour), now)
	}
}

func TestHourlyUsageRefused(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		agg          catalog.AggregationType
		charge       func(*catalog.Charge)
		in           func(h *hourlyFixture, in *subscriptions.HourlyUsageInput)
		// Exactly one of the expectations is set.
		notFound, field, code string
		unavailable           bool
	}{
		{
			name: "charge of another plan", source: "spec/graphql/resolvers/subscriptions/hourly_usage_resolver_spec.rb:141",
			in: func(h *hourlyFixture, in *subscriptions.HourlyUsageInput) {
				other := h.Charge(h.Plan(period.Monthly, 0), h.Metric("other", catalog.AggregationTypeCount, ""), catalog.ChargeModelStandard, `{"amount":"1"}`)
				in.ChargeID = other.ID
			},
			notFound: subscriptions.ResourceCharge,
		},
		{
			name: "subscription of another customer",
			in: func(h *hourlyFixture, in *subscriptions.HourlyUsageInput) {
				in.Customer = subscriptions.CustomerRef{ExternalID: h.Customer().ExternalID}
			},
			notFound: subscriptions.Resource,
		},
		{
			name: "unique count", source: "spec/services/realtime_usage/hourly_breakdown_service_spec.rb:228",
			agg: catalog.AggregationTypeUniqueCount, unavailable: true,
		},
		{
			name: "max", source: "spec/services/realtime_usage/hourly_breakdown_service_spec.rb:237",
			agg: catalog.AggregationTypeMax, unavailable: true,
		},
		{
			name: "charge billed in advance", source: "app/services/realtime_usage/hourly_breakdown_service.rb:62-66",
			charge: func(c *catalog.Charge) { c.PayInAdvance = true }, unavailable: true,
		},
		{
			name: "window too long", source: "spec/services/realtime_usage/hourly_breakdown_service_spec.rb:210",
			in: func(_ *hourlyFixture, in *subscriptions.HourlyUsageInput) {
				in.To = in.From.AddDate(0, 0, 40)
			},
			field: "to", code: subscriptions.CodeWindowTooLong,
		},
		{
			name: "empty window", source: "spec/services/realtime_usage/hourly_breakdown_service_spec.rb:246",
			in: func(_ *hourlyFixture, in *subscriptions.HourlyUsageInput) {
				in.From = in.To
			},
			field: "from", code: subscriptions.CodeInvalidWindow,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts []func(*catalog.Charge)
			if tc.charge != nil {
				opts = append(opts, tc.charge)
			}
			h := newHourlyFixture(t, cmp.Or(tc.agg, catalog.AggregationTypeCount), opts...)
			in := h.in
			if tc.in != nil {
				tc.in(h, &in)
			}
			_, err := h.svc.HourlyUsage(h.Ctx, in)
			var fields []core.FieldError
			if tc.field != "" {
				fields = core.FieldErrors(err)
			}
			switch {
			case tc.notFound != "" && notFound(err) != tc.notFound,
				tc.unavailable && !errors.Is(err, subscriptions.ErrHourlyUnavailable),
				tc.field != "" && (len(fields) != 1 || fields[0].Field != tc.field || fields[0].Code != tc.code):
				t.Errorf("got err = %v, want = not found %q, unavailable %t, field %s %s", err, tc.notFound, tc.unavailable, tc.field, tc.code)
			}
		})
	}

	t.Run("event store without buckets", func(t *testing.T) {
		h := newHourlyFixture(t, catalog.AggregationTypeCount)
		_, err := h.fixture.svc.HourlyUsage(h.Ctx, h.in)
		if !errors.Is(err, subscriptions.ErrHourlyUnavailable) {
			t.Errorf("got err = %v, want = %v", err, subscriptions.ErrHourlyUnavailable)
		}
	})
}
