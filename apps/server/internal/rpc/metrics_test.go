package rpc_test

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

func TestMain(m *testing.M) { os.Exit(rpctest.Main(m)) }

// restMetric is the JSON of a billable metric in a REST response.
type restMetric struct {
	ID                string `json:"id"`
	Code              string `json:"code"`
	Name              string `json:"name"`
	AggregationType   string `json:"aggregation_type"`
	WeightedInterval  string `json:"weighted_interval"`
	Recurring         bool   `json:"recurring"`
	Expression        string `json:"expression"`
	RoundingFunction  string `json:"rounding_function"`
	RoundingPrecision int    `json:"rounding_precision"`
	CreatedAt         string `json:"created_at"`
	Filters           []struct {
		Key    string   `json:"key"`
		Values []string `json:"values"`
	} `json:"filters"`
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

func metricsClient(s *rpctest.Server, c rpctest.Credential) catalogv1connect.BillableMetricServiceClient {
	return catalogv1connect.NewBillableMetricServiceClient(s.Connect(c))
}

func TestCreateBillableMetric(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		status int
		check  func(t *testing.T, m restMetric)
	}{
		{
			name:   "basic",
			source: "spec/requests/api/v1/billable_metrics_controller_spec.rb:33",
			body: `{"name":"BM1","code":"BM1_code","description":"description","aggregation_type":"AGGREGATION_TYPE_SUM",
				"field_name":"amount_sum","expression":"1 + 2","recurring":true,"rounding_function":"ROUNDING_FUNCTION_ROUND","rounding_precision":2}`,
			status: http.StatusOK,
			check: func(t *testing.T, m restMetric) {
				if m.ID == "" || m.CreatedAt == "" {
					t.Errorf("got id = %q, created_at = %q, want both set", m.ID, m.CreatedAt)
				}
				if m.Code != "BM1_code" || m.Name != "BM1" || !m.Recurring || m.Expression != "1 + 2" {
					t.Errorf("got %+v, want the created attributes", m)
				}
				if m.RoundingFunction != "ROUNDING_FUNCTION_ROUND" || m.RoundingPrecision != 2 {
					t.Errorf("got rounding = %s/%d, want ROUNDING_FUNCTION_ROUND/2", m.RoundingFunction, m.RoundingPrecision)
				}
				if len(m.Filters) != 0 {
					t.Errorf("got filters = %v, want none", m.Filters)
				}
			},
		},
		{
			name:   "weighted sum",
			source: "spec/requests/api/v1/billable_metrics_controller_spec.rb:61",
			body: `{"name":"BM1","code":"BM1_code","description":"description","aggregation_type":"AGGREGATION_TYPE_WEIGHTED_SUM",
				"field_name":"amount_sum","recurring":true,"weighted_interval":"WEIGHTED_INTERVAL_SECONDS"}`,
			status: http.StatusOK,
			check: func(t *testing.T, m restMetric) {
				if !m.Recurring || m.AggregationType != "AGGREGATION_TYPE_WEIGHTED_SUM" || m.WeightedInterval != "WEIGHTED_INTERVAL_SECONDS" {
					t.Errorf("got %+v, want a recurring weighted sum in seconds", m)
				}
			},
		},
		{
			name:   "filters",
			source: "spec/requests/api/v1/billable_metrics_controller_spec.rb:87",
			body: `{"name":"BM1","code":"BM1_code","aggregation_type":"AGGREGATION_TYPE_COUNT",
				"filters":[{"key":"key","values":["value1","value2"]}]}`,
			status: http.StatusOK,
			check: func(t *testing.T, m restMetric) {
				if len(m.Filters) != 1 || m.Filters[0].Key != "key" || len(m.Filters[0].Values) != 2 ||
					m.Filters[0].Values[0] != "value1" || m.Filters[0].Values[1] != "value2" {
					t.Errorf("got filters = %+v, want [key: value1, value2]", m.Filters)
				}
			},
		},
		{
			name:   "invalid input",
			source: "spec/requests/api/v1/billable_metrics_controller_spec.rb:100",
			body:   `"BL"`,
			status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/billable_metrics", tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, decode[struct {
					BillableMetric restMetric `json:"billable_metric"`
				}](t, body).BillableMetric)
			}
		})
	}
}

func TestUpdateBillableMetric(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		// ref is the path reference; "" is the metric's code:<code>.
		ref    string
		body   string
		status int
		check  func(t *testing.T, m restMetric)
	}{
		{
			name:   "updates",
			source: "spec/requests/api/v1/billable_metrics_controller_spec.rb:132",
			body:   `{"name":"BM1","code":"BM1_code","description":"description","aggregation_type":"AGGREGATION_TYPE_SUM","field_name":"amount_sum"}`,
			status: http.StatusOK,
			check: func(t *testing.T, m restMetric) {
				if m.Code != "BM1_code" || len(m.Filters) != 0 {
					t.Errorf("got %+v, want code BM1_code without filters", m)
				}
			},
		},
		{
			name:   "not found",
			source: "spec/requests/api/v1/billable_metrics_controller_spec.rb:141",
			ref:    "code%3Aunknown",
			body:   `{"name":"BM1"}`,
			status: http.StatusNotFound,
		},
		{
			name:   "duplicate code",
			source: "spec/requests/api/v1/billable_metrics_controller_spec.rb:150",
			body:   `{"code":"other"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "weighted sum",
			source: "spec/requests/api/v1/billable_metrics_controller_spec.rb:173",
			body: `{"name":"BM1","code":"BM1_code","description":"description","aggregation_type":"AGGREGATION_TYPE_WEIGHTED_SUM",
				"field_name":"amount_sum","recurring":true,"weighted_interval":"WEIGHTED_INTERVAL_SECONDS"}`,
			status: http.StatusOK,
			check: func(t *testing.T, m restMetric) {
				if !m.Recurring || m.AggregationType != "AGGREGATION_TYPE_WEIGHTED_SUM" || m.WeightedInterval != "WEIGHTED_INTERVAL_SECONDS" {
					t.Errorf("got %+v, want a recurring weighted sum in seconds", m)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t)
			m := s.Fixtures.Metric("bm", catalog.AggregationTypeCount, "")
			s.Fixtures.Metric("other", catalog.AggregationTypeCount, "")
			ref := tt.ref
			if ref == "" {
				ref = "code%3A" + m.Code
			}
			status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/billable_metrics/"+ref, tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check == nil {
				return
			}
			got := decode[struct {
				BillableMetric restMetric `json:"billable_metric"`
			}](t, body).BillableMetric
			if got.ID != m.ID.String() {
				t.Errorf("got id = %s, want = %s", got.ID, m.ID)
			}
			tt.check(t, got)
		})
	}
}

func TestGetBillableMetric(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	m := s.Fixtures.Metric("bm", catalog.AggregationTypeCount, "")
	gone := s.Fixtures.Metric("gone", catalog.AggregationTypeCount, "")
	if _, err := metricsClient(s, s.APIKey).DeleteBillableMetric(t.Context(),
		catalogv1.DeleteBillableMetricRequest_builder{BillableMetricId: new(gone.ID.String())}.Build()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	tests := []struct {
		name   string
		source string
		ref    string
		status int
	}{
		{"by code", "spec/requests/api/v1/billable_metrics_controller_spec.rb:195", "code%3Abm", http.StatusOK},
		{"by id", "spec/requests/api/v1/billable_metrics_controller_spec.rb:195", m.ID.String(), http.StatusOK},
		{"not found", "spec/requests/api/v1/billable_metrics_controller_spec.rb:206", core.NewID[core.BillableMetricID](core.UUIDv7Generator{}).String(), http.StatusNotFound},
		{"deleted", "spec/requests/api/v1/billable_metrics_controller_spec.rb:215", gone.ID.String(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/billable_metrics/"+tt.ref, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status != http.StatusOK {
				return
			}
			got := decode[struct {
				BillableMetric restMetric `json:"billable_metric"`
			}](t, body).BillableMetric
			if got.ID != m.ID.String() || got.Code != "bm" {
				t.Errorf("got %s/%s, want = %s/bm", got.ID, got.Code, m.ID)
			}
		})
	}
}

func TestDeleteBillableMetric(t *testing.T) {
	t.Parallel()
	t.Run("discards it and deletes its alerts", func(t *testing.T) {
		t.Parallel()
		// source: spec/requests/api/v1/billable_metrics_controller_spec.rb:232
		// source: spec/requests/api/v1/billable_metrics_controller_spec.rb:236
		// source: spec/requests/api/v1/billable_metrics_controller_spec.rb:241
		s := rpctest.New(t)
		m := s.Fixtures.Metric("bm", catalog.AggregationTypeCount, "")
		_, err := s.Admin.Exec(t.Context(), `INSERT INTO alerts (organization_id, id, code, alert_type, subscription_external_id,
			billable_metric_id, direction, previous_value, created_at, updated_at)
			VALUES ($1, $2, 'alert', 'billable_metric_current_usage_amount', 'sub', $3, 'increasing', 0, now(), now())`,
			s.Org.ID.UUID(), core.NewID[core.AlertID](core.UUIDv7Generator{}).UUID(), m.ID.UUID())
		if err != nil {
			t.Fatalf("insert alert: %v", err)
		}
		status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/billable_metrics/code%3Abm", nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := decode[struct {
			BillableMetric restMetric `json:"billable_metric"`
		}](t, body).BillableMetric
		if got.ID != m.ID.String() || got.Code != "bm" {
			t.Errorf("got %s/%s, want = %s/bm", got.ID, got.Code, m.ID)
		}
		// lago discards the metric (destroy_service.rb:33): the row stays
		// with deleted_at set, and reads no longer find it.
		for query, want := range map[string]int{
			"SELECT count(*) FROM billable_metrics WHERE organization_id = $1 AND deleted_at IS NOT NULL": 1,
			"SELECT count(*) FROM billable_metrics WHERE organization_id = $1 AND deleted_at IS NULL":     0,
			"SELECT count(*) FROM alerts WHERE organization_id = $1 AND deleted_at IS NULL":               0,
		} {
			var n int
			if err := s.Admin.QueryRow(t.Context(), query, s.Org.ID.UUID()).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != want {
				t.Errorf("got %q = %d, want = %d", query, n, want)
			}
		}
		if status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/billable_metrics/"+m.ID.String(), nil); status != http.StatusNotFound {
			t.Errorf("get after delete got status = %d (%s), want = 404", status, body)
		}
		// The code is free again among the live metrics.
		status, body = s.REST(t, s.APIKey, http.MethodPost, "/v1/billable_metrics",
			`{"name":"BM","code":"bm","aggregation_type":"AGGREGATION_TYPE_COUNT"}`)
		if status != http.StatusOK {
			t.Errorf("create with the freed code got status = %d (%s), want = 200", status, body)
		}
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		// source: spec/requests/api/v1/billable_metrics_controller_spec.rb:252
		s := rpctest.New(t)
		status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/billable_metrics/code%3Aunknown", nil)
		if status != http.StatusNotFound {
			t.Fatalf("got status = %d (%s), want = 404", status, body)
		}
	})
}

func TestListBillableMetrics(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	first := s.Fixtures.Metric("first", catalog.AggregationTypeCount, "")
	s.Clock.Advance(time.Second)
	second := s.Fixtures.Metric("second", catalog.AggregationTypeSum, "amount", func(m *catalog.BillableMetric) { m.Recurring = true })

	type page struct {
		BillableMetrics []restMetric `json:"billable_metrics"`
		NextPageToken   string       `json:"next_page_token"`
	}
	ids := func(p page) []string {
		out := make([]string, len(p.BillableMetrics))
		for i, m := range p.BillableMetrics {
			out[i] = m.ID
		}
		return out
	}

	t.Run("all", func(t *testing.T) {
		// source: spec/requests/api/v1/billable_metrics_controller_spec.rb:267
		status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/billable_metrics", nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		got := ids(decode[page](t, body))
		if len(got) != 2 || got[0] != second.ID.String() || got[1] != first.ID.String() {
			t.Errorf("got = %v, want = [%s %s]", got, second.ID, first.ID)
		}
	})
	t.Run("pagination", func(t *testing.T) {
		// source: spec/requests/api/v1/billable_metrics_controller_spec.rb:281
		status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/billable_metrics?page_size=1", nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		p := decode[page](t, body)
		if got := ids(p); len(got) != 1 || got[0] != second.ID.String() || p.NextPageToken == "" {
			t.Fatalf("got = %v (next %q), want = [%s] and a next page", got, p.NextPageToken, second.ID)
		}
		status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/billable_metrics?page_size=1&page_token="+p.NextPageToken, nil)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		p = decode[page](t, body)
		if got := ids(p); len(got) != 1 || got[0] != first.ID.String() || p.NextPageToken != "" {
			t.Errorf("got = %v (next %q), want = [%s] and no next page", got, p.NextPageToken, first.ID)
		}
	})
	t.Run("filter", func(t *testing.T) {
		// Tanvo exposes the filters of lago's BillableMetricsQuery.
		c := metricsClient(s, s.APIKey)
		for filter, want := range map[string]string{
			`aggregation_type = "count"`:                first.ID.String(),
			`aggregation_type = "AGGREGATION_TYPE_SUM"`: second.ID.String(),
			`recurring = true`:                          second.ID.String(),
		} {
			resp, err := c.ListBillableMetrics(t.Context(), catalogv1.ListBillableMetricsRequest_builder{Filter: new(filter)}.Build())
			if err != nil {
				t.Fatalf("%s: %v", filter, err)
			}
			if got := resp.GetBillableMetrics(); len(got) != 1 || got[0].GetId() != want {
				t.Errorf("%s: got %d metrics, want = [%s]", filter, len(got), want)
			}
		}
		_, err := c.ListBillableMetrics(t.Context(), catalogv1.ListBillableMetricsRequest_builder{Filter: new(`name = "x"`)}.Build())
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("got %v, want = InvalidArgument for an unknown field", err)
		}
	})
}

func TestEvaluateExpression(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	t.Run("valid", func(t *testing.T) {
		// source: spec/requests/api/v1/billable_metrics_controller_spec.rb:310
		status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/billable_metrics:evaluate_expression",
			`{"expression":"round(event.properties.value)","code":"bm_code","properties":{"value":"2.4"}}`)
		if status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		if got := decode[struct {
			Value string `json:"value"`
		}](t, body).Value; got != "2.0" {
			t.Errorf("got = %q, want = %q", got, "2.0")
		}
	})
	t.Run("connect", func(t *testing.T) {
		props, err := structpb.NewStruct(map[string]any{"value": "2.4"})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := metricsClient(s, s.Session).EvaluateExpression(t.Context(), catalogv1.EvaluateExpressionRequest_builder{
			Expression: new("round(event.properties.value)"), Properties: props,
		}.Build())
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.GetValue(); got != "2.0" {
			t.Errorf("got = %q, want = %q", got, "2.0")
		}
	})
	t.Run("missing expression", func(t *testing.T) {
		// source: spec/requests/api/v1/billable_metrics_controller_spec.rb:322
		_, err := metricsClient(s, s.APIKey).EvaluateExpression(t.Context(), catalogv1.EvaluateExpressionRequest_builder{Expression: new("")}.Build())
		v := rpc.ErrorViolations(err)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || len(v) != 1 || v[0].Field != "expression" || v[0].Reason != core.CodeRequired {
			t.Errorf("got %v (%+v), want = InvalidArgument on expression", err, v)
		}
	})
}

// TestBillableMetricPermissions ports the "requires API permission"
// examples (spec/support/shared_examples/api_requirements.rb:17) of each
// action and checks the user permissions.
func TestBillableMetricPermissions(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	m := s.Fixtures.Metric("bm", catalog.AggregationTypeCount, "")
	d := auth.Deps{Tx: s.Store, Clock: s.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader}
	keyWith := func(level permission.Level) rpctest.Credential {
		k, err := auth.NewAPIKeys(d, nil).Create(t.Context(), s.Org.ID, nil, map[permission.APIResource]permission.Level{
			permission.APIResourceBillableMetric: level,
		})
		if err != nil {
			t.Fatal(err)
		}
		return rpctest.Credential{Token: k.Secret}
	}
	read, write := keyWith(permission.LevelRead), keyWith(permission.LevelWrite)
	other := func() rpctest.Credential {
		k, err := auth.NewAPIKeys(d, nil).Create(t.Context(), s.Org.ID, nil, map[permission.APIResource]permission.Level{
			permission.APIResourcePlan: permission.LevelWrite,
		})
		if err != nil {
			t.Fatal(err)
		}
		return rpctest.Credential{Token: k.Secret}
	}()

	create := func(c rpctest.Credential) error {
		_, err := metricsClient(s, c).CreateBillableMetric(t.Context(), catalogv1.CreateBillableMetricRequest_builder{
			BillableMetric: catalogv1.BillableMetric_builder{
				Code: new("new_" + c.Token[len(c.Token)-6:]), Name: new("n"),
				AggregationType: catalogv1.AggregationType_AGGREGATION_TYPE_COUNT.Enum(),
			}.Build(),
		}.Build())
		return err
	}
	evaluate := func(c rpctest.Credential) error {
		_, err := metricsClient(s, c).EvaluateExpression(t.Context(), catalogv1.EvaluateExpressionRequest_builder{
			Expression: new("1 + 1"),
		}.Build())
		return err
	}
	get := func(c rpctest.Credential) error {
		_, err := metricsClient(s, c).GetBillableMetric(t.Context(),
			catalogv1.GetBillableMetricRequest_builder{BillableMetricId: new(m.ID.String())}.Build())
		return err
	}
	tests := []struct {
		name   string
		source string
		call   func(rpctest.Credential) error
		cred   rpctest.Credential
		code   connect.Code
		reason string
	}{
		{"write key creates", "spec/requests/api/v1/billable_metrics_controller_spec.rb:31", create, write, 0, ""},
		{"read key cannot create", "spec/requests/api/v1/billable_metrics_controller_spec.rb:31", create, read, connect.CodePermissionDenied, "write_action_not_allowed_for_billable_metric"},
		{"read key reads", "spec/requests/api/v1/billable_metrics_controller_spec.rb:193", get, read, 0, ""},
		{"write key evaluates", "spec/requests/api/v1/billable_metrics_controller_spec.rb:307", evaluate, write, 0, ""},
		{"read key cannot evaluate", "spec/requests/api/v1/billable_metrics_controller_spec.rb:307", evaluate, read, connect.CodePermissionDenied, "write_action_not_allowed_for_billable_metric"},
		{"other resource key cannot read", "spec/requests/api/v1/billable_metrics_controller_spec.rb:193", get, other, connect.CodePermissionDenied, "read_action_not_allowed_for_billable_metric"},
		{"anonymous", "", get, rpctest.Anonymous, connect.CodeUnauthenticated, rpc.ReasonUnauthenticated},
		{"admin session", "", get, s.Session, 0, ""},
		{"session without organization", "", get, rpctest.Credential{Token: s.Session.Token}, connect.CodePermissionDenied, rpc.ReasonOrganizationRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			err := tt.call(tt.cred)
			if tt.code == 0 {
				if err != nil {
					t.Fatalf("got %v, want = nil", err)
				}
				return
			}
			if got := connect.CodeOf(err); got != tt.code {
				t.Fatalf("got = %v (%v), want = %v", got, err, tt.code)
			}
			if got := rpc.ErrorReason(err); got != tt.reason {
				t.Errorf("got reason = %q, want = %q", got, tt.reason)
			}
		})
	}
}
