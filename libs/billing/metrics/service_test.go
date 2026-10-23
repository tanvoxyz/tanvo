package metrics_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/metrics"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*billingtest.Harness, *metrics.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	s, err := metrics.New(h.Deps, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h, s
}

func ptr[T any](v T) *T { return &v }

func reload(h *billingtest.Harness, id core.BillableMetricID) (*catalog.BillableMetric, error) {
	var out *catalog.BillableMetric
	err := h.Deps.Tx.InTx(context.Background(), func(ctx context.Context, r port.Repos) error {
		m, err := r.Metrics.Get(ctx, h.OrgID(), id)
		out = m
		return err
	})
	return out, err
}

func reloadCharge(h *billingtest.Harness, id core.ChargeID) *catalog.Charge {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*catalog.Charge, error) {
		return r.Charges.Get(ctx, h.OrgID(), id)
	})
}

// wantFieldError fails unless err carries the field error.
func wantFieldError(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if !ve.Has(field, code) {
		t.Fatalf("errors = %v, want %s %s", ve.Errors, field, code)
	}
}

func webhooks(h *billingtest.Harness, t webhook.EventType) int {
	return len(slices.DeleteFunc(h.Webhooks(), func(e webhook.EventType) bool { return e != t }))
}

// draftOf returns a draft invoice billing a subscription of p.
func draftOf(h *billingtest.Harness, p *catalog.Plan) *invoice.Invoice {
	c := h.Customer()
	s := h.Subscription(c, p)
	inv := h.StoredInvoice(c, func(i *invoice.Invoice) { i.Status = invoice.StatusDraft })
	h.LinkSubscription(inv, s, invoice.InvoicingReasonSubscriptionPeriodic, now.AddDate(0, -1, 0), now)
	return inv
}

func TestCreate(t *testing.T) {
	t.Parallel()
	// source: spec/services/billable_metrics/create_service_spec.rb:24
	t.Run("creates a metric and emits billable_metric.created", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		m, err := s.Create(t.Context(), h.OrgID(), metrics.CreateInput{
			Code: "new_metric", Name: "New Metric", Description: ptr("New metric description"),
			AggregationType: catalog.AggregationTypeCount, FieldName: ptr("ignored"),
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		got, err := reload(h, m.ID)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if got.Code != "new_metric" || got.Recurring || got.FieldName != nil {
			t.Errorf("metric = %+v, want code new_metric, not recurring, no field name", got)
		}
		if n := webhooks(h, webhook.EventTypeBillableMetricCreated); n != 1 {
			t.Errorf("billable_metric.created events = %d, want = 1", n)
		}
	})
	// source: spec/services/billable_metrics/create_service_spec.rb:106
	t.Run("creates the filters sorted by key, values deduplicated", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		m, err := s.Create(t.Context(), h.OrgID(), metrics.CreateInput{
			Code: "m", Name: "M", AggregationType: catalog.AggregationTypeCount,
			Filters: []catalog.MetricFilter{{Key: "region", Values: []string{"eu", "eu", "us"}}, {Key: "cloud", Values: []string{"aws", "google"}}},
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		got, _ := reload(h, m.ID)
		keys := []string{}
		for _, f := range got.Filters {
			keys = append(keys, f.Key)
		}
		if !slices.Equal(keys, []string{"cloud", "region"}) || !slices.Equal(got.Filters[1].Values, []string{"eu", "us"}) {
			t.Errorf("filters = %+v, want cloud then region [eu us]", got.Filters)
		}
	})
	cases := []struct {
		name        string
		source      string
		in          metrics.CreateInput
		existing    string
		field, code string
	}{
		{
			// source: spec/services/billable_metrics/create_service_spec.rb:114
			name: "filter without values", field: "values", code: core.CodeRequired,
			in: metrics.CreateInput{Code: "m", Name: "M", AggregationType: catalog.AggregationTypeCount, Filters: []catalog.MetricFilter{{Key: "foo"}}},
		},
		{
			// source: spec/services/billable_metric_filters/create_or_update_batch_service_spec.rb (duplicated keys)
			name: "duplicated filter keys", field: "key", code: core.CodeAlreadyExists,
			in: metrics.CreateInput{Code: "m", Name: "M", AggregationType: catalog.AggregationTypeCount, Filters: []catalog.MetricFilter{{Key: "a", Values: []string{"x"}}, {Key: "a", Values: []string{"y"}}}},
		},
		{
			// source: spec/services/billable_metrics/create_service_spec.rb:156
			name: "code already used", existing: "new_metric", field: "code", code: core.CodeAlreadyExists,
			in: metrics.CreateInput{Code: "new_metric", Name: "M", AggregationType: catalog.AggregationTypeCount},
		},
		{
			// source: app/models/billable_metric.rb:126 (validate_expression)
			name: "invalid expression", field: "expression", code: metrics.CodeInvalidExpression,
			in: metrics.CreateInput{Code: "m", Name: "M", AggregationType: catalog.AggregationTypeSum, FieldName: ptr("f"), Expression: ptr("1 +")},
		},
		{
			// source: app/models/billable_metric.rb:117 (validate_recurring)
			name: "recurring count", field: "recurring", code: catalog.CodeNotCompatibleWithAggregationType,
			in: metrics.CreateInput{Code: "m", Name: "M", AggregationType: catalog.AggregationTypeCount, Recurring: true},
		},
		{
			// source: app/models/billable_metric.rb:55 (field_name presence)
			name: "sum without field name", field: "field_name", code: core.CodeRequired,
			in: metrics.CreateInput{Code: "m", Name: "M", AggregationType: catalog.AggregationTypeSum},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, s := setup(t)
			if tc.existing != "" {
				h.Metric(tc.existing, catalog.AggregationTypeCount, "")
			}
			_, err := s.Create(t.Context(), h.OrgID(), tc.in)
			wantFieldError(t, err, tc.field, tc.code)
			if n := webhooks(h, webhook.EventTypeBillableMetricCreated); n != 0 {
				t.Errorf("billable_metric.created events = %d, want = 0", n)
			}
		})
	}
	// source: spec/services/billable_metrics/create_service_spec.rb:72
	t.Run("code of a deleted metric", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		old := h.Metric("reused", catalog.AggregationTypeCount, "")
		if _, err := s.Delete(t.Context(), h.OrgID(), old.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if _, err := s.Create(t.Context(), h.OrgID(), metrics.CreateInput{Code: "reused", Name: "R", AggregationType: catalog.AggregationTypeCount}); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	})
}

func updateParams() metrics.UpdateInput {
	return metrics.UpdateInput{
		Name:              core.Some("New Metric"),
		Code:              core.Some("new_metric"),
		Description:       core.Some(ptr("New metric description")),
		AggregationType:   core.Some(catalog.AggregationTypeSum),
		FieldName:         core.Some(ptr("field_value")),
		Expression:        core.Some(ptr("1 + 3")),
		RoundingFunction:  core.Some(catalog.RoundingFunctionCeil),
		RoundingPrecision: core.Some(ptr(2)),
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()
	// source: spec/services/billable_metrics/update_service_spec.rb:27
	t.Run("updates every field and emits billable_metric.updated", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		m := h.Metric("old", catalog.AggregationTypeCount, "")
		got, err := s.Update(t.Context(), h.OrgID(), m.ID, updateParams())
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if got.Code != "new_metric" || got.AggregationType != catalog.AggregationTypeSum || *got.FieldName != "field_value" ||
			got.RoundingFunction != catalog.RoundingFunctionCeil || *got.RoundingPrecision != 2 || *got.Expression != "1 + 3" {
			t.Errorf("metric = %+v, want every field updated", got)
		}
		if n := webhooks(h, webhook.EventTypeBillableMetricUpdated); n != 1 {
			t.Errorf("billable_metric.updated events = %d, want = 1", n)
		}
	})
	// source: spec/services/billable_metrics/update_service_spec.rb:144
	t.Run("only name, description and filters once a plan uses it", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		m := h.Metric("old", catalog.AggregationTypeCount, "")
		h.Charge(h.Plan(period.Monthly, 0), m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		in := updateParams()
		in.Filters = core.Some([]catalog.MetricFilter{{Key: "cloud", Values: []string{"aws"}}})
		got, err := s.Update(t.Context(), h.OrgID(), m.ID, in)
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if got.Name != "New Metric" || *got.Description != "New metric description" || len(got.Filters) != 1 {
			t.Errorf("metric = %+v, want name, description and filters updated", got)
		}
		if got.Code != "old" || got.AggregationType != catalog.AggregationTypeCount || got.Expression != nil || got.RoundingFunction != "" {
			t.Errorf("metric = %+v, want the locked fields kept", got)
		}
	})
	t.Run("every field when only a deleted plan uses it", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		m := h.Metric("old", catalog.AggregationTypeCount, "")
		h.Charge(h.Plan(period.Monthly, 0, func(p *catalog.Plan) { p.Status = catalog.PlanStatusDeleted }), m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		got, err := s.Update(t.Context(), h.OrgID(), m.ID, updateParams())
		if err != nil {
			t.Fatalf("Update() error = %v", err)
		}
		if got.Code != "new_metric" {
			t.Errorf("code = %q, want = new_metric", got.Code)
		}
	})
	// source: spec/services/billable_metrics/update_service_spec.rb:86
	t.Run("blank name", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		m := h.Metric("old", catalog.AggregationTypeCount, "")
		_, err := s.Update(t.Context(), h.OrgID(), m.ID, metrics.UpdateInput{Name: core.Some("")})
		wantFieldError(t, err, "name", core.CodeRequired)
	})
	// source: spec/services/billable_metrics/update_service_spec.rb:101
	t.Run("rolls back the filters when the metric is invalid", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		m := h.Metric("old", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
			m.Filters = []catalog.MetricFilter{{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "region", Values: []string{"US", "EU"}}}
		})
		draft := draftOf(h, chargedPlan(h, m))
		_, err := s.Update(t.Context(), h.OrgID(), m.ID, metrics.UpdateInput{
			Name: core.Some(""), Filters: core.Some([]catalog.MetricFilter{{Key: "region", Values: []string{"US"}}}),
		})
		wantFieldError(t, err, "name", core.CodeRequired)
		got, _ := reload(h, m.ID)
		if !slices.Equal(got.Filters[0].Values, []string{"US", "EU"}) {
			t.Errorf("filter values = %v, want = [US EU]", got.Filters[0].Values)
		}
		if h.Invoice(draft.ID).RefreshRequested {
			t.Errorf("draft refresh requested = true, want = false")
		}
	})
	// source: spec/services/billable_metrics/update_service_spec.rb:118
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t)
		_, err := s.Update(t.Context(), h.OrgID(), core.NewID[core.BillableMetricID](h.IDs), updateParams())
		var nf *core.NotFoundError
		if !errors.As(err, &nf) || nf.Error() != "billable_metric_not_found" {
			t.Fatalf("error = %v, want billable_metric_not_found", err)
		}
	})
}

// chargedPlan returns a plan with a charge on m.
func chargedPlan(h *billingtest.Harness, m *catalog.BillableMetric) *catalog.Plan {
	p := h.Plan(period.Monthly, 0)
	h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	return p
}

// source: spec/services/billable_metric_filters/create_or_update_batch_service_spec.rb:322
func TestUpdateFiltersTrimChargeFilters(t *testing.T) {
	t.Parallel()
	h, s := setup(t)
	m := h.Metric("m", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) {
		m.Filters = []catalog.MetricFilter{
			{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "cloud", Values: []string{"aws", "gcp", "azure"}},
			{ID: core.NewID[core.BillableMetricFilterID](h.IDs), Key: "region", Values: []string{"eu", "us"}},
		}
	})
	p := h.Plan(period.Monthly, 0)
	c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
		c.Filters = []catalog.ChargeFilter{
			billingtest.ChargeFilter(map[string][]string{"cloud": {"aws", "gcp"}}, `{"amount":"2"}`),
			billingtest.ChargeFilter(map[string][]string{"cloud": {"gcp"}}, `{"amount":"3"}`),
			billingtest.ChargeFilter(map[string][]string{"cloud": {catalog.AllFilterValues}, "region": {"eu"}}, `{"amount":"4"}`),
			billingtest.ChargeFilter(map[string][]string{"region": {"us"}}, `{"amount":"5"}`),
		}
	})
	draft := draftOf(h, p)
	_, err := s.Update(t.Context(), h.OrgID(), m.ID, metrics.UpdateInput{
		Filters: core.Some([]catalog.MetricFilter{{Key: "cloud", Values: []string{"aws", "azure"}}}),
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	got := reloadCharge(h, c.ID)
	var values []map[string][]string
	for _, f := range got.Filters {
		values = append(values, f.Values)
	}
	want := []map[string][]string{{"cloud": {"aws"}}, {"cloud": {catalog.AllFilterValues}}}
	if len(values) != len(want) {
		t.Fatalf("charge filters = %v, want = %v", values, want)
	}
	for i := range want {
		if !catalog.ChargeFilterValuesEqual(values[i], want[i]) {
			t.Errorf("charge filter %d = %v, want = %v", i, values[i], want[i])
		}
	}
	if !h.Invoice(draft.ID).RefreshRequested {
		t.Errorf("draft refresh requested = false, want = true")
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	// source: spec/services/billable_metrics/destroy_service_spec.rb:21
	h, s := setup(t)
	m := h.Metric("m", catalog.AggregationTypeCount, "")
	p := h.Plan(period.Monthly, 0)
	c := h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	draft := draftOf(h, p)
	al := &alert.Alert{
		ID: core.NewID[core.AlertID](h.IDs), OrganizationID: h.OrgID(), Code: "a", Type: alert.TypeBillableMetricCurrentUsageUnits,
		SubscriptionExternalID: "sub", BillableMetricID: m.ID, Direction: alert.DirectionIncreasing,
		Thresholds: []alert.Threshold{{Value: money.MustParse("1")}}, CreatedAt: now, UpdatedAt: now,
	}
	if err := h.Deps.Tx.InTx(t.Context(), func(ctx context.Context, r port.Repos) error { return r.Alerts.Create(ctx, al) }); err != nil {
		t.Fatal(err)
	}
	got, err := s.Delete(t.Context(), h.OrgID(), m.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got.ID != m.ID {
		t.Errorf("deleted = %s, want = %s", got.ID, m.ID)
	}
	if _, err := reload(h, m.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("reload metric error = %v, want not found", err)
	}
	// source: spec/services/billable_metrics/destroy_service_spec.rb:28
	err = h.Deps.Tx.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		got, err := r.Charges.Get(ctx, h.OrgID(), c.ID)
		if err == nil && got.DeletedAt == nil {
			t.Errorf("charge deleted_at = nil, want = set")
		}
		return err
	})
	if err != nil {
		t.Errorf("reload charge error = %v", err)
	}
	// source: spec/services/billable_metrics/destroy_service_spec.rb:34
	err = h.Deps.Tx.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		_, err := r.Alerts.Get(ctx, h.OrgID(), al.ID)
		return err
	})
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("reload alert error got = %v, want = not found", err)
	}
	// source: spec/services/billable_metrics/destroy_service_spec.rb:52
	if !h.Invoice(draft.ID).RefreshRequested {
		t.Errorf("draft refresh requested = false, want = true")
	}
	// source: spec/services/billable_metrics/destroy_service_spec.rb:41
	if !slices.Contains(h.JobKinds(), kernel.JobDestroyMetricFilters) {
		t.Errorf("jobs got = %v, want = %s", h.JobKinds(), kernel.JobDestroyMetricFilters)
	}
	// source: spec/services/billable_metrics/destroy_service_spec.rb:46
	if n := webhooks(h, webhook.EventTypeBillableMetricDeleted); n != 1 {
		t.Errorf("billable_metric.deleted events = %d, want = 1", n)
	}
	// source: spec/services/billable_metrics/destroy_service_spec.rb:60
	_, err = s.Delete(t.Context(), h.OrgID(), m.ID)
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != metrics.Resource {
		t.Errorf("second Delete() error = %v, want billable_metric_not_found", err)
	}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	_, s := setup(t)
	props := map[string]any{"value": 10.4, "units": 2.0}
	cases := []struct {
		name        string
		source      string
		expr        string
		ev          metrics.SampleEvent
		want        string
		field, code string
	}{
		{
			name: "result", source: "spec/services/billable_metrics/evaluate_expression_service_spec.rb:21",
			expr: "round(event.properties.value * event.properties.units)", ev: metrics.SampleEvent{Code: "test_code", Timestamp: now, Properties: props}, want: "21.0",
		},
		{
			name: "missing expression", source: "spec/services/billable_metrics/evaluate_expression_service_spec.rb:31",
			ev: metrics.SampleEvent{Properties: props}, field: "expression", code: core.CodeRequired,
		},
		{
			name: "invalid expression", source: "spec/services/billable_metrics/evaluate_expression_service_spec.rb:43",
			expr: "invalid_expression", ev: metrics.SampleEvent{Properties: props}, field: "expression", code: metrics.CodeInvalidExpression,
		},
		{
			name: "timestamp defaults to now", source: "spec/services/billable_metrics/evaluate_expression_service_spec.rb:61",
			expr: "event.timestamp", ev: metrics.SampleEvent{Code: "test_code"}, want: "1710496800.0",
		},
		{
			name: "event fails to evaluate", source: "spec/services/billable_metrics/evaluate_expression_service_spec.rb:83",
			expr: "round(event.properties.value * event.properties.units)", ev: metrics.SampleEvent{Properties: map[string]any{"value": "invalid_value", "units": 2.0}}, field: "event", code: metrics.CodeInvalidEvent,
		},
		{
			name: "missing event", source: "spec/services/billable_metrics/evaluate_expression_service_spec.rb:94",
			expr: "round(event.properties.value * event.properties.units)", field: "event", code: metrics.CodeInvalidEvent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.Evaluate(t.Context(), tc.expr, tc.ev)
			if tc.field != "" {
				wantFieldError(t, err, tc.field, tc.code)
				return
			}
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("Evaluate() = %q, want = %q", got, tc.want)
			}
		})
	}
}

func TestList(t *testing.T) {
	t.Parallel()
	h, s := setup(t)
	h.Metric("alpha", catalog.AggregationTypeCount, "")
	h.Clock.Advance(time.Second)
	h.Metric("beta", catalog.AggregationTypeSum, "f")
	page, err := s.List(t.Context(), h.OrgID(), catalog.MetricListFilter{}, core.PageRequest{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var codes []string
	for _, m := range page.Items {
		codes = append(codes, m.Code)
	}
	if !slices.Equal(codes, []string{"beta", "alpha"}) {
		t.Errorf("codes = %v, want = [beta alpha]", codes)
	}
}
