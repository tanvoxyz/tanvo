package rpc_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// withEvents serves the event API: the intake over the store's catalog
// (the tests seed events directly) and the reader of postgres mode.
func withEvents(t *testing.T) rpctest.Option {
	return func(o *app.APIOptions) {
		svc, err := ingest.NewService(ingest.Config{Catalog: ingest.RepoCatalog{Transactor: o.Tx}})
		if err != nil {
			t.Fatal(err)
		}
		r, err := ingest.NewReader(ingest.ReaderConfig{Transactor: o.Tx})
		if err != nil {
			t.Fatal(err)
		}
		o.Ingest, o.EventReader = svc, r
	}
}

type restEvent struct {
	ID                     string `json:"id"`
	TransactionID          string `json:"transaction_id"`
	ExternalSubscriptionID string `json:"external_subscription_id"`
	Code                   string `json:"code"`
	Timestamp              string `json:"timestamp"`
	CreatedAt              string `json:"created_at"`
}

// seedEvent stores an event of the server's organization in the raw log.
func seedEvent(t *testing.T, s *rpctest.Server, tx, code, sub string, ts time.Time) *event.Event {
	t.Helper()
	e := &event.Event{
		ID: core.NewID[core.EventID](core.UUIDv7Generator{}), OrganizationID: s.Org.ID, TransactionID: tx, Code: code,
		ExternalSubscriptionID: sub, Timestamp: ts.UTC(), Properties: map[string]string{}, IngestedAt: s.Clock.Now(),
	}
	err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		_, _, err := r.Events.Append(ctx, s.Org.ID, []*event.Event{e})
		return err
	})
	if err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return e
}

func eventIDs(events []restEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.TransactionID
	}
	return out
}

func TestListEvents(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t, withEvents(t))
	now := rpctest.Now
	seedEvent(t, s, "five_days", "api_calls", "sub_a", now.AddDate(0, 0, -5))
	seedEvent(t, s, "three_days", "storage", "sub_b", now.AddDate(0, 0, -3))
	seedEvent(t, s, "one_day", "storage", "sub_b", now.AddDate(0, 0, -1))
	started := now.AddDate(0, 0, -2)
	h := s.Fixtures
	sub := h.Subscription(h.Customer(), h.Plan(period.Monthly, 0), billingtest.SubscribedAt(started))

	seedEvent(t, s, "after_start", "api_calls", sub.ExternalID, started.Add(time.Second))
	seedEvent(t, s, "before_start", "api_calls", sub.ExternalID, started.Add(-time.Second))

	tests := []struct {
		name, source, query string
		want                []string
	}{
		{
			"without params", "spec/requests/api/v1/events_controller_spec.rb:309", "",
			[]string{"one_day", "after_start", "before_start", "three_days", "five_days"},
		},
		{
			"with code", "spec/requests/api/v1/events_controller_spec.rb:342", "filter=" + url.QueryEscape(`code = "storage"`),
			[]string{"one_day", "three_days"},
		},
		{
			"with external subscription id", "spec/requests/api/v1/events_controller_spec.rb:356", "filter=" + url.QueryEscape(`external_subscription_id = "sub_a"`),
			[]string{"five_days"},
		},
		{
			"with timestamp", "spec/requests/api/v1/events_controller_spec.rb:370",
			"filter=" + url.QueryEscape(`timestamp >= "`+now.AddDate(0, 0, -4).Format(time.RFC3339)+`" AND timestamp <= "`+now.Add(-60*time.Hour).Format(time.RFC3339)+`"`),
			[]string{"three_days"},
		},
		{
			"with timestamp_from_started_at", "spec/requests/api/v1/events_controller_spec.rb:388",
			"timestamp_from_started_at=true&filter=" + url.QueryEscape(`external_subscription_id = "`+sub.ExternalID+`"`),
			[]string{"after_start"},
		},
		{
			"with timestamp_from_started_at false", "spec/requests/api/v1/events_controller_spec.rb:409",
			"timestamp_from_started_at=false&filter=" + url.QueryEscape(`timestamp >= "`+now.AddDate(0, 0, -10).Format(time.RFC3339)+`"`),
			[]string{"one_day", "after_start", "before_start", "three_days", "five_days"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// source: see tt.source
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/events?"+tt.query, nil)
			if status != http.StatusOK {
				t.Fatalf("status got = %d (%s), want = 200", status, body)
			}
			got := decode[struct{ Events []restEvent }](t, body)
			if !slices.Equal(eventIDs(got.Events), tt.want) {
				t.Errorf("events got = %v, want = %v (%s)", eventIDs(got.Events), tt.want, tt.source)
			}
		})
	}

	// source: spec/requests/api/v1/events_controller_spec.rb:323
	t.Run("with pagination", func(t *testing.T) {
		var all []string
		path := "/v1/events?page_size=2"
		for {
			status, body := s.REST(t, s.APIKey, http.MethodGet, path, nil)
			if status != http.StatusOK {
				t.Fatalf("status got = %d (%s), want = 200", status, body)
			}
			page := decode[struct {
				Events        []restEvent
				NextPageToken string `json:"next_page_token"`
			}](t, body)
			if len(page.Events) > 2 {
				t.Fatalf("page size got = %d, want ≤ 2", len(page.Events))
			}
			all = append(all, eventIDs(page.Events)...)
			if page.NextPageToken == "" {
				break
			}
			path = "/v1/events?page_size=2&page_token=" + url.QueryEscape(page.NextPageToken)
		}
		if len(all) != 5 {
			t.Errorf("events got = %v, want = 5 events", all)
		}
	})

	t.Run("invalid filter", func(t *testing.T) {
		status, _ := s.REST(t, s.APIKey, http.MethodGet, "/v1/events?filter="+url.QueryEscape(`timestamp > "x"`), nil)
		if status != http.StatusBadRequest {
			t.Errorf("status got = %d, want = 400", status)
		}
	})
}

func TestGetEvent(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t, withEvents(t))
	e := seedEvent(t, s, "tr_1", "api_calls", "sub_a", rpctest.Now)
	seedEvent(t, s, "tr_1", "api_calls", "sub_b", rpctest.Now)

	// source: spec/requests/api/v1/events_controller_spec.rb:561
	status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/events/tr_1", nil)
	if status != http.StatusOK {
		t.Fatalf("status got = %d (%s), want = 200", status, body)
	}
	got := decode[struct{ Event restEvent }](t, body).Event
	if got.ID != e.ID.String() || got.Code != "api_calls" || got.TransactionID != "tr_1" || got.ExternalSubscriptionID != "sub_a" {
		t.Errorf("event got = %+v, want = tr_1 of sub_a", got)
	}

	// The subscription picks among events sharing the transaction ID.
	status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/events/tr_1?external_subscription_id=sub_b", nil)
	if got := decode[struct{ Event restEvent }](t, body).Event; status != http.StatusOK || got.ExternalSubscriptionID != "sub_b" {
		t.Errorf("event got = %d %+v, want = tr_1 of sub_b", status, got)
	}

	// source: spec/requests/api/v1/events_controller_spec.rb:585
	status, body = s.REST(t, s.APIKey, http.MethodGet, "/v1/events/unknown", nil)
	if status != http.StatusNotFound {
		t.Errorf("status got = %d (%s), want = 404", status, body)
	}
}

type restEstimatedFee struct {
	ChargeID           string `json:"charge_id"`
	BillableMetricCode string `json:"billable_metric_code"`
	TransactionID      string `json:"transaction_id"`
	Amount             struct {
		Currency string `json:"currency"`
		Amount   string `json:"amount"`
	} `json:"amount"`
	TaxesAmount struct {
		Amount string `json:"amount"`
	} `json:"taxes_amount"`
	Units       string `json:"units"`
	EventsCount string `json:"events_count"`
}

func estimateFixture(t *testing.T, model catalog.ChargeModel, props string, agg catalog.AggregationType, field string, inAdvance bool) (*rpctest.Server, *catalog.BillableMetric, *subscription.Subscription) {
	t.Helper()
	s := rpctest.New(t, withEvents(t))
	h := s.Fixtures
	m := h.Metric("bm", agg, field)
	plan := h.Plan(period.Monthly, 0)
	h.Charge(plan, m, model, props, func(c *catalog.Charge) { c.PayInAdvance = inAdvance })
	h.Clock.Set(rpctest.Now.AddDate(0, -1, 0))
	sub := h.Subscription(h.Customer(), plan)
	h.Clock.Set(rpctest.Now)
	return s, m, sub
}

func TestEstimateFees(t *testing.T) {
	t.Parallel()
	// source: spec/requests/api/v1/events_controller_spec.rb:659
	t.Run("success", func(t *testing.T) {
		s, m, sub := estimateFixture(t, catalog.ChargeModelStandard, `{"amount":"10"}`, catalog.AggregationTypeCount, "", true)
		body := map[string]any{"event": map[string]any{"code": m.Code, "external_subscription_id": sub.ExternalID, "transaction_id": "tr_1", "properties": map[string]any{"foo": "bar"}}}
		status, raw := s.REST(t, s.APIKey, http.MethodPost, "/v1/events:estimate_fees", body)
		if status != http.StatusOK {
			t.Fatalf("status got = %d (%s), want = 200", status, raw)
		}
		fees := decode[struct{ Fees []restEstimatedFee }](t, raw).Fees
		if len(fees) != 1 || fees[0].BillableMetricCode != "bm" || fees[0].Units != "1" || fees[0].EventsCount != "1" ||
			fees[0].Amount.Amount != "10.00" || fees[0].Amount.Currency != "USD" || fees[0].TransactionID != "tr_1" {
			t.Errorf("fees got = %+v, want = one fee of 1 unit for 10.00 USD", fees)
		}
		if n := len(s.Webhooks(t)); n != 0 {
			t.Errorf("webhooks got = %d, want = none", n)
		}
	})

	// source: spec/requests/api/v1/events_controller_spec.rb:679
	t.Run("with taxes applied to the billing entity", func(t *testing.T) {
		s, m, sub := estimateFixture(t, catalog.ChargeModelStandard, `{"amount":"10"}`, catalog.AggregationTypeCount, "", true)
		tax := s.Fixtures.Tax("vat", "20")
		s.Fixtures.UpdateBillingEntity(func(b *organization.BillingEntity) { b.TaxIDs = []core.TaxID{tax.ID} })
		body := map[string]any{"event": map[string]any{"code": m.Code, "external_subscription_id": sub.ExternalID}}
		status, raw := s.REST(t, s.APIKey, http.MethodPost, "/v1/events:estimate_fees", body)
		fees := decode[struct{ Fees []restEstimatedFee }](t, raw).Fees
		if status != http.StatusOK || len(fees) != 1 || fees[0].TaxesAmount.Amount != "2.00" {
			t.Errorf("fees got = %d %+v, want = taxes of 2.00", status, fees)
		}
	})

	// source: spec/requests/api/v1/events_controller_spec.rb:712
	t.Run("not an instant charge", func(t *testing.T) {
		s, m, sub := estimateFixture(t, catalog.ChargeModelStandard, `{"amount":"10"}`, catalog.AggregationTypeCount, "", false)
		body := map[string]any{"event": map[string]any{"code": m.Code, "external_subscription_id": sub.ExternalID}}
		status, raw := s.REST(t, s.APIKey, http.MethodPost, "/v1/events:estimate_fees", body)
		if status != http.StatusBadRequest {
			t.Errorf("status got = %d (%s), want = 400", status, raw)
		}
	})

	// source: spec/requests/api/v1/events_controller_spec.rb:695
	t.Run("unknown subscription", func(t *testing.T) {
		s, m, _ := estimateFixture(t, catalog.ChargeModelStandard, `{"amount":"10"}`, catalog.AggregationTypeCount, "", true)
		body := map[string]any{"event": map[string]any{"code": m.Code, "external_subscription_id": "unknown"}}
		status, raw := s.REST(t, s.APIKey, http.MethodPost, "/v1/events:estimate_fees", body)
		if status != http.StatusNotFound {
			t.Errorf("status got = %d (%s), want = 404", status, raw)
		}
	})
}

func TestEstimateInstantFees(t *testing.T) {
	t.Parallel()
	// source: spec/requests/api/v1/events_controller_spec.rb:760,779
	t.Run("success", func(t *testing.T) {
		s, m, sub := estimateFixture(t, catalog.ChargeModelPercentage, `{"rate":"0.1","fixed_amount":"0"}`, catalog.AggregationTypeSum, "value", true)
		body := map[string]any{"events": []map[string]any{
			{"code": m.Code, "external_subscription_id": sub.ExternalID, "transaction_id": "tr_1", "properties": map[string]any{"value": 400}},
			{"code": m.Code, "external_subscription_id": sub.ExternalID, "transaction_id": "tr_2", "properties": map[string]any{"value": 300}},
		}}
		status, raw := s.REST(t, s.APIKey, http.MethodPost, "/v1/events:estimate_instant_fees", body)
		if status != http.StatusOK {
			t.Fatalf("status got = %d (%s), want = 200", status, raw)
		}
		fees := decode[struct{ Fees []restEstimatedFee }](t, raw).Fees
		if len(fees) != 2 || fees[0].TransactionID != "tr_1" || fees[0].Amount.Amount != "0.40" || fees[0].Units != "400" ||
			fees[1].TransactionID != "tr_2" || fees[1].Amount.Amount != "0.30" || fees[1].Units != "300" || fees[0].EventsCount != "1" {
			t.Errorf("fees got = %+v, want = 0.40 for 400 units and 0.30 for 300", fees)
		}
	})

	// source: spec/requests/api/v1/events_controller_spec.rb:887
	t.Run("not an instant charge", func(t *testing.T) {
		s, m, sub := estimateFixture(t, catalog.ChargeModelStandard, `{"amount":"10"}`, catalog.AggregationTypeSum, "value", false)
		body := map[string]any{"events": []map[string]any{{"code": m.Code, "external_subscription_id": sub.ExternalID}}}
		status, raw := s.REST(t, s.APIKey, http.MethodPost, "/v1/events:estimate_instant_fees", body)
		if status != http.StatusBadRequest {
			t.Errorf("status got = %d (%s), want = 400", status, raw)
		}
	})

	// source: spec/requests/api/v1/events_controller_spec.rb:870
	t.Run("unknown subscription", func(t *testing.T) {
		s, m, _ := estimateFixture(t, catalog.ChargeModelPercentage, `{"rate":"0.1"}`, catalog.AggregationTypeSum, "value", true)
		body := map[string]any{"events": []map[string]any{{"code": m.Code, "external_subscription_id": "unknown"}}}
		status, raw := s.REST(t, s.APIKey, http.MethodPost, "/v1/events:estimate_instant_fees", body)
		if status != http.StatusNotFound {
			t.Errorf("status got = %d (%s), want = 404", status, raw)
		}
	})
}
