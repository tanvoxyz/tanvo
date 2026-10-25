package rpc_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// fakeEnriched serves stored enriched events, in the order of
// EnrichedEvents, and records the filters asked.
type fakeEnriched struct {
	events []metering.EnrichedEvent
	got    []metering.RawFilter
}

func (f *fakeEnriched) EnrichedEvents(_ context.Context, rf metering.RawFilter, after *metering.EnrichedPosition, limit int) ([]metering.EnrichedEvent, error) {
	f.got = append(f.got, rf)
	var out []metering.EnrichedEvent
	for _, e := range f.events {
		if e.OrganizationID == rf.OrganizationID && (rf.SubscriptionID == "" || e.SubscriptionID == rf.SubscriptionID) && (rf.Code == "" || e.Code == rf.Code) {
			out = append(out, e)
		}
	}
	if after != nil {
		i := slices.IndexFunc(out, func(e metering.EnrichedEvent) bool { return e.TransactionID == after.TransactionID })
		out = out[i+1:]
	}
	return out[:min(limit, len(out))], nil
}

// withEnriched serves the event API with the enriched events of en for
// organizations in mode.
func withEnriched(t *testing.T, mode ingest.Mode, en *fakeEnriched) rpctest.Option {
	return func(o *app.APIOptions) {
		svc, err := ingest.NewService(ingest.Config{Catalog: ingest.RepoCatalog{Transactor: o.Tx}})
		if err != nil {
			t.Fatal(err)
		}
		r, err := ingest.NewReader(ingest.ReaderConfig{Transactor: o.Tx, Mode: mode, Enriched: en})
		if err != nil {
			t.Fatal(err)
		}
		o.Ingest, o.EventReader = svc, r
	}
}

type restEnriched struct {
	TransactionID          string            `json:"transaction_id"`
	ExternalSubscriptionID string            `json:"external_subscription_id"`
	Code                   string            `json:"code"`
	Timestamp              string            `json:"timestamp"`
	EnrichedAt             string            `json:"enriched_at"`
	Value                  *string           `json:"value"`
	DecimalValue           *string           `json:"decimal_value"`
	PreciseTotalAmount     *string           `json:"precise_total_amount"`
	Properties             map[string]string `json:"properties"`
	SubscriptionID         *string           `json:"subscription_id"`
}

// getEnriched lists enriched events over REST and returns the status, the
// endpoint status header and the body.
func getEnriched(t *testing.T, s *rpctest.Server, query string) (int, string, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL+"/v1/enriched_events?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.HTTPClient(s.APIKey).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get(rpc.EndpointStatusHeader), body
}

// TestListEnrichedEvents ports the requests of GET /api/v1/events_enriched.
func TestListEnrichedEvents(t *testing.T) {
	t.Parallel()

	t.Run("without kafka mode", func(t *testing.T) {
		// source: spec/requests/api/v1/events_controller_spec.rb:485
		t.Parallel()
		s := rpctest.New(t, withEnriched(t, ingest.ModePostgres, &fakeEnriched{}))
		status, _, body := getEnriched(t, s, "")
		if status != http.StatusForbidden {
			t.Fatalf("status got = %d (%s), want = 403", status, body)
		}
		if got := couponReason(t, body); got != rpc.ReasonEndpointNotAvailable {
			t.Errorf("reason got = %q, want = %q", got, rpc.ReasonEndpointNotAvailable)
		}
	})

	t.Run("with kafka mode", func(t *testing.T) {
		t.Parallel()
		en := &fakeEnriched{}
		s := rpctest.New(t, withEnriched(t, ingest.ModeKafka, en))
		org := s.Org.ID.String()
		ts := rpctest.Now.AddDate(0, 0, -5).UTC().Truncate(24 * time.Hour)
		enrichedAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		value, number, amount := "12.5", money.MustParse("12.5"), money.MustParse("3")
		en.events = []metering.EnrichedEvent{
			{
				Event: metering.Event{
					OrganizationID: org, SubscriptionID: "sub", TransactionID: "tx_1", Code: "api_calls", Timestamp: ts,
					Properties: metering.Properties{"amount": "12.5"}, PreciseTotalAmountCents: &amount, Value: &value,
				},
				DecimalValue: &number, EnrichedAt: enrichedAt,
			},
			{Event: metering.Event{OrganizationID: org, SubscriptionID: "sub", TransactionID: "tx_2", Code: "storage", Timestamp: ts}, EnrichedAt: enrichedAt.Add(-time.Hour)},
			{Event: metering.Event{OrganizationID: org, SubscriptionID: "other", TransactionID: "tx_3", Code: "api_calls", Timestamp: ts}, EnrichedAt: enrichedAt.Add(-2 * time.Hour)},
			{Event: metering.Event{OrganizationID: "another org", SubscriptionID: "sub", TransactionID: "tx_4", Code: "api_calls", Timestamp: ts}, EnrichedAt: enrichedAt},
		}

		// source: spec/requests/api/v1/events_controller_spec.rb:520
		status, beta, body := getEnriched(t, s, "filter="+url.QueryEscape(`external_subscription_id = "sub"`))
		if status != http.StatusOK {
			t.Fatalf("status got = %d (%s), want = 200", status, body)
		}
		if beta != "beta" {
			t.Errorf("%s got = %q, want = beta", rpc.EndpointStatusHeader, beta)
		}
		got := decode[struct {
			EnrichedEvents []restEnriched `json:"enriched_events"`
		}](t, body).EnrichedEvents
		if len(got) != 2 {
			t.Fatalf("events got = %+v, want = tx_1 and tx_2", got)
		}
		e := got[0]
		if e.TransactionID != "tx_1" || e.Code != "api_calls" || e.ExternalSubscriptionID != "sub" || e.EnrichedAt != "2025-01-01T00:00:00Z" ||
			e.Timestamp != ts.Format(time.RFC3339) || e.SubscriptionID != nil {
			t.Errorf("event got = %+v, want = tx_1 of sub enriched on 2025-01-01, without subscription_id", e)
		}
		if e.Value == nil || *e.Value != "12.5" || e.DecimalValue == nil || *e.DecimalValue != "12.5" || e.PreciseTotalAmount == nil || *e.PreciseTotalAmount != "3" || e.Properties["amount"] != "12.5" {
			t.Errorf("values got = %+v, want = value 12.5, decimal 12.5, amount 3 and the properties", e)
		}
		if got[1].TransactionID != "tx_2" || got[1].Value != nil || got[1].DecimalValue != nil {
			t.Errorf("second event got = %+v, want = tx_2 without a value", got[1])
		}

		// The filter and the pages are those of ListEvents.
		var all []string
		query := "page_size=1&filter=" + url.QueryEscape(`code = "api_calls"`)
		for {
			status, _, body := getEnriched(t, s, query)
			if status != http.StatusOK {
				t.Fatalf("status got = %d (%s), want = 200", status, body)
			}
			page := decode[struct {
				EnrichedEvents []restEnriched `json:"enriched_events"`
				NextPageToken  string         `json:"next_page_token"`
			}](t, body)
			for _, e := range page.EnrichedEvents {
				all = append(all, e.TransactionID)
			}
			if page.NextPageToken == "" {
				break
			}
			query = "page_size=1&page_token=" + url.QueryEscape(page.NextPageToken) + "&filter=" + url.QueryEscape(`code = "api_calls"`)
		}
		if want := []string{"tx_1", "tx_3"}; !slices.Equal(all, want) {
			t.Errorf("pages got = %v, want = %v", all, want)
		}
		if status, _, _ := getEnriched(t, s, "filter="+url.QueryEscape(`subscription_id = "sub"`)); status != http.StatusBadRequest {
			t.Errorf("unknown filter field status got = %d, want = 400", status)
		}
	})
}
