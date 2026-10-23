package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// testEventValidation checks the post-validation of events
// (event.Log.ValidationErrors) and the organizations it runs for
// (organization.WorkEventsToValidate), ported from
// spec/services/events/post_validation_service_spec.rb and
// spec/jobs/clock/events_validation_job_spec.rb.
func testEventValidation(t *testing.T, h Harness) {
	from, to := event.ValidationWindow(now)
	// The events of the specs are created 25 minutes before the hour.
	inWindow := to.Add(-25 * time.Minute)
	stored := func(e *env, org core.OrganizationID, txn, code string, props map[string]string, created time.Time) *event.Event {
		ev := eventFixture(e, org, txn, "")
		ev.Code, ev.Properties, ev.CreatedAt, ev.UpdatedAt, ev.IngestedAt = code, props, created, created, created
		return ev
	}

	t.Run("validation errors", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		e.tx(func(ctx context.Context, r port.Repos) error {
			// source: spec/services/events/post_validation_service_spec.rb:19-24
			sum := catalogFixtureMetric(e, org, "sum_bm")
			sum.AggregationType, sum.FieldName = catalog.AggregationTypeSum, ptr("item_id")
			// source: spec/services/events/post_validation_service_spec.rb:48-62
			filtered := catalogFixtureMetric(e, org, "filtered_bm")
			filtered.Filters = []catalog.MetricFilter{{
				ID: core.NewID[core.BillableMetricFilterID](e.ids), Key: "region", Values: []string{"eu-west-1", "us-east-1"},
			}}
			unique := catalogFixtureMetric(e, org, "unique_bm")
			unique.AggregationType, unique.FieldName = catalog.AggregationTypeUniqueCount, ptr("user")
			gone := catalogFixtureMetric(e, org, "gone_bm")
			for _, m := range []*catalog.BillableMetric{sum, filtered, unique, gone} {
				noErr(t, "Metrics.Create()", r.Metrics.Create(ctx, m))
			}
			noErr(t, "Metrics.Discard()", r.Metrics.Discard(ctx, org, gone.ID, now))

			evs := []*event.Event{
				// source: spec/services/events/post_validation_service_spec.rb:10-17
				stored(e, org, "tr_invalid_code", "unknown_code", nil, inWindow),
				// source: spec/services/events/post_validation_service_spec.rb:26-34
				stored(e, org, "tr_missing", "sum_bm", map[string]string{}, inWindow),
				// source: spec/services/events/post_validation_service_spec.rb:36-44
				stored(e, org, "tr_negative", "sum_bm", map[string]string{"item_id": "-12"}, inWindow),
				stored(e, org, "tr_not_numeric", "sum_bm", map[string]string{"item_id": "1e3"}, inWindow),
				stored(e, org, "tr_decimal", "sum_bm", map[string]string{"item_id": "12.50"}, inWindow),
				// source: spec/services/events/post_validation_service_spec.rb:64-72
				stored(e, org, "tr_bad_filter", "filtered_bm", map[string]string{"region": "us-west-4"}, inWindow),
				stored(e, org, "tr_good_filter", "filtered_bm", map[string]string{"region": "eu-west-1"}, inWindow),
				stored(e, org, "tr_no_filter_key", "filtered_bm", map[string]string{"other": "x"}, inWindow),
				// A non-numeric aggregation needs the field, not a number.
				stored(e, org, "tr_unique_text", "unique_bm", map[string]string{"user": "ada"}, inWindow),
				stored(e, org, "tr_unique_missing", "unique_bm", nil, inWindow),
				// A code naming only a discarded metric is not checked
				// (last_hour_events_mv_v04.sql: billable_metrics.deleted_at IS NULL).
				stored(e, org, "tr_gone", "gone_bm", nil, inWindow),
				// The window is [from, to).
				stored(e, org, "tr_at_from", "unknown_code", nil, from),
				stored(e, org, "tr_before", "unknown_code", nil, from.Add(-time.Microsecond)),
				stored(e, org, "tr_at_to", "unknown_code", nil, to),
			}
			for _, ev := range evs {
				ev.ExternalSubscriptionID = ev.TransactionID
			}
			_, _, err := r.Events.Append(ctx, org, evs)
			noErr(t, "Events.Append()", err)

			got, err := r.Events.ValidationErrors(ctx, org, from, to)
			noErr(t, "ValidationErrors()", err)
			equal(t, "ValidationErrors()", got, event.ValidationErrors{
				InvalidCode:                []string{"tr_at_from", "tr_invalid_code"},
				MissingAggregationProperty: []string{"tr_missing", "tr_not_numeric", "tr_unique_missing"},
				InvalidFilterValues:        []string{"tr_bad_filter"},
			})

			// source: spec/services/events/post_validation_service_spec.rb:86-92
			got, err = r.Events.ValidationErrors(ctx, other, from, to)
			noErr(t, "ValidationErrors() of another organization", err)
			equal(t, "ValidationErrors() of another organization empty", got.Empty(), true)
			equal(t, "ValidationErrors() of another organization", got, event.ValidationErrors{
				InvalidCode: []string{}, MissingAggregationProperty: []string{}, InvalidFilterValues: []string{},
			})
			return nil
		})
	})

	// source: spec/jobs/clock/events_validation_job_spec.rb:25-42
	t.Run("organizations with events to validate", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		_, c := e.org("third")
		e.tx(func(ctx context.Context, r port.Repos) error {
			// a has an event in the window and an endpoint; b an endpoint
			// and only an event before the window; c an event but no
			// endpoint.
			for _, org := range []core.OrganizationID{a.OrganizationID, b.OrganizationID} {
				noErr(t, "WebhookEndpoints.Create()", r.WebhookEndpoints.Create(ctx, webhookFixtureEndpoint(e, org, "https://hooks.test/"+org.String(), nil)))
			}
			for org, created := range map[core.OrganizationID]time.Time{
				a.OrganizationID: inWindow, b.OrganizationID: from.Add(-time.Minute), c.OrganizationID: inWindow,
			} {
				_, _, err := r.Events.Append(ctx, org, []*event.Event{stored(e, org, "tr_1", "api_calls", nil, created)})
				noErr(t, "Events.Append()", err)
			}
			got, err := r.Organizations.WithWork(ctx, organization.WorkEventsToValidate, now)
			noErr(t, "WithWork()", err)
			equal(t, "WithWork()", got, []core.OrganizationID{a.OrganizationID})
			return nil
		})
	})
}
