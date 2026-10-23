package billing_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// source: spec/services/events/post_validation_service_spec.rb:95
func TestValidateEvents(t *testing.T) {
	now := time.Date(2026, 6, 1, 10, 3, 0, 0, time.UTC)
	s := newScenario(t, now)
	// The events of the spec are created 25 minutes before the hour.
	created := now.Truncate(time.Hour).Add(-25 * time.Minute)
	other := organization.New(core.NewID[core.OrganizationID](s.IDs), "Other", now)
	s.Do(func(ctx context.Context, r port.Repos) error {
		if err := r.Organizations.Create(ctx, other); err != nil {
			return err
		}
		sum := &catalog.BillableMetric{
			ID: core.NewID[core.BillableMetricID](s.IDs), OrganizationID: s.OrgID(), Code: "sum_bm", Name: "Sum",
			AggregationType: catalog.AggregationTypeSum, FieldName: new("item_id"), CreatedAt: now, UpdatedAt: now,
		}
		filtered := &catalog.BillableMetric{
			ID: core.NewID[core.BillableMetricID](s.IDs), OrganizationID: s.OrgID(), Code: "filtered_bm", Name: "Filtered",
			AggregationType: catalog.AggregationTypeCount, CreatedAt: now, UpdatedAt: now,
			Filters: []catalog.MetricFilter{{
				ID: core.NewID[core.BillableMetricFilterID](s.IDs), Key: "region", Values: []string{"eu-west-1", "us-east-1"},
			}},
		}
		for _, m := range []*catalog.BillableMetric{sum, filtered} {
			if err := r.Metrics.Create(ctx, m); err != nil {
				return err
			}
		}
		ev := func(txn, code string, props map[string]string) *event.Event {
			return &event.Event{
				ID: core.NewID[core.EventID](s.IDs), OrganizationID: s.OrgID(), TransactionID: txn, Code: code,
				ExternalSubscriptionID: "sub", Timestamp: created, Properties: props,
				IngestedAt: created, CreatedAt: created, UpdatedAt: created,
			}
		}
		_, _, err := r.Events.Append(ctx, s.OrgID(), []*event.Event{
			ev("invalid_code", "unknown", nil),
			ev("missing", "sum_bm", map[string]string{}),
			ev("negative", "sum_bm", map[string]string{"item_id": "-12"}),
			ev("bad_filter", "filtered_bm", map[string]string{"region": "us-west-4"}),
		})
		return err
	})

	// source: spec/services/events/post_validation_service_spec.rb:86
	got, err := s.E.ValidateEvents(s.Ctx, other.ID, now)
	if err != nil {
		t.Fatalf("ValidateEvents() of another organization error got = %v, want = nil", err)
	}
	if !got.Empty() || len(s.Webhooks()) != 0 {
		t.Errorf("ValidateEvents() of another organization got = %+v, webhooks %v, want = none", got, s.Webhooks())
	}

	want := event.ValidationErrors{
		InvalidCode: []string{"invalid_code"}, MissingAggregationProperty: []string{"missing"}, InvalidFilterValues: []string{"bad_filter"},
	}
	s.run(kernel.JobValidateEvents, kernel.InstantArgs{At: now})
	evs := s.Emitted.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventTypeEventsErrors || evs[0].ObjectType != "events_errors" || evs[0].OrganizationID != s.OrgID() {
		t.Fatalf("webhooks got = %+v, want = one events.errors of the organization", evs)
	}
	var payload event.ValidationErrors
	if err := json.Unmarshal(evs[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !reflect.DeepEqual(payload, want) {
		t.Errorf("events.errors payload got = %+v, want = %+v", payload, want)
	}

	// The next hour's window holds none of them.
	s.Emitted.Reset()
	got, err = s.E.ValidateEvents(s.Ctx, s.OrgID(), now.Add(time.Hour))
	if err != nil || !got.Empty() || len(s.Webhooks()) != 0 {
		t.Errorf("ValidateEvents() an hour later got = %+v, %v, webhooks %v, want = none", got, err, s.Webhooks())
	}
}

// source: spec/jobs/clock/record_deletions_cleanup_job_spec.rb
func TestCleanRecordDeletions(t *testing.T) {
	now := time.Now().UTC()
	s := newScenario(t, now)
	c := s.Customer()
	// More than one batch of tombstones (spec: "when there are more
	// records than the batch size").
	n := export.RecordDeletionBatch + 1
	inv := s.StoredInvoice(c, func(inv *invoice.Invoice) { inv.Status = invoice.StatusDraft })
	s.Do(func(ctx context.Context, r port.Repos) error {
		for range n {
			f := &invoice.Fee{
				ID: core.NewID[core.FeeID](s.IDs), OrganizationID: s.OrgID(), InvoiceID: inv.ID, Type: core.FeeTypeCharge,
				Currency: inv.Currency, CreatedAt: now, UpdatedAt: now,
			}
			if err := r.Fees.Create(ctx, f); err != nil {
				return err
			}
		}
		return nil
	})
	s.Do(func(ctx context.Context, r port.Repos) error { return r.Fees.DeleteByInvoice(ctx, s.OrgID(), inv.ID) })
	count := func() int {
		return len(billingtest.Read(s.Harness, func(ctx context.Context, r port.Repos) ([]*export.RecordDeletion, error) {
			return kernel.All(func(p core.PageRequest) (core.Page[*export.RecordDeletion], error) {
				return r.RecordDeletions.List(ctx, s.OrgID(), p)
			})
		}))
	}
	if got := count(); got != n {
		t.Fatalf("record deletions got = %d, want = %d", got, n)
	}

	for _, tc := range []struct {
		name    string
		at      time.Time
		removed int
		left    int
	}{
		// "does not delete record deletions within the retention period"
		{"three weeks old kept", now.Add(21 * 24 * time.Hour), 0, n},
		// "deletes record deletions older than the retention period"
		{"two months old removed", now.AddDate(0, 2, 0), n, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.E.CleanRecordDeletions(s.Ctx, s.OrgID(), tc.at)
			if err != nil {
				t.Fatalf("CleanRecordDeletions() error got = %v, want = nil", err)
			}
			if got != tc.removed {
				t.Errorf("CleanRecordDeletions() got = %d, want = %d", got, tc.removed)
			}
			if left := count(); left != tc.left {
				t.Errorf("record deletions left got = %d, want = %d", left, tc.left)
			}
		})
	}
}
