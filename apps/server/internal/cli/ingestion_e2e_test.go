package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/dockertest"
)

// TestScenarioIngestion ingests events through the public API on both
// event stores (PostgreSQL, and Kafka with ClickHouse) and checks what
// billing makes of them: single and batch ingestion, the
// external_contract_id alias, Unix-second timestamps as numbers and
// strings, deduplication, an event before the subscription left out by
// the billing period, metric filters, expressions, grouping keys, the
// remove operation of unique counts, the count, sum, max, latest and
// unique count store queries, and the in-advance fee of an event. The
// first current usage reads the events and defines the usage buckets;
// once the define jobs ran, the same usage is served from the buckets.
//
// source: spec/requests/api/v1/events_controller_spec.rb:32, 56, 77, 144, 216
// source: spec/services/events/create_batch_service_spec.rb:55
// source: spec/services/events/create_service_spec.rb:146
// source: spec/scenarios/pay_in_advance_charges_spec.rb:15
func TestScenarioIngestion(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"postgres", "kafka"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var environ []string
			var clickhouseURL string
			if mode == "kafka" {
				var brokers string
				brokers, clickhouseURL = startKafka(t)
				environ = []string{
					"TANVO_INGEST_MODE=kafka", "TANVO_KAFKA_BROKERS=" + brokers, "TANVO_KAFKA_CREATE_TOPICS=true",
					"TANVO_CLICKHOUSE_URL=" + clickhouseURL,
				}
			}
			// The billing clock stays behind the wall clock, so that the jobs
			// enqueued at the billing instant (in-advance fees) are due.
			start := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
			s := newScenario(t, start, scenarioOptions{environ: environ})
			s.ingestionCatalog()
			s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
				"external_id": "sub", "customer_id": "external_id:cust", "plan_id": "code:plan", "billing_time": "BILLING_TIME_ANNIVERSARY",
			})
			s.idle()

			t1, t2 := start.Add(time.Minute), start.Add(2*time.Minute)
			s.at(start.Add(5 * time.Minute))
			ev := func(tx, code string, ts any, props map[string]any) map[string]any {
				e := map[string]any{"transaction_id": tx, "external_subscription_id": "sub", "code": code, "timestamp": ts}
				if props != nil {
					e["properties"] = props
				}
				return e
			}
			rfc := func(at time.Time) string { return at.Format(time.RFC3339) }

			// A single event; the topics the server created may not have a
			// leader yet, and ingestion is idempotent, so it is retried.
			if err := dockertest.Wait(t.Context(), time.Minute, func(context.Context) error {
				if status, body := s.send(http.MethodPost, "/v1/events", s.key, "", ev("c-1", "calls", rfc(t1), map[string]any{"region": "eu"})); status != http.StatusOK {
					return fmt.Errorf("POST /v1/events: status %d (%s)", status, body)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// The contract alias with a fractional Unix timestamp.
			s.api(http.MethodPost, "/v1/events", map[string]any{
				"transaction_id": "c-2", "external_contract_id": "sub", "code": "calls",
				"timestamp": float64(t1.Unix()) + 0.5, "properties": map[string]any{"region": "us"},
			})
			// The in-advance event.
			s.api(http.MethodPost, "/v1/events", ev("s-1", "seats", t1.Unix(), map[string]any{"seats": 3}))

			// The batch: the contract alias with a Unix-second string
			// timestamp, an alias the explicit subscription wins over, every
			// aggregation, a duplicate of c-1 and an event before the
			// subscription.
			batch := []map[string]any{
				{
					"transaction_id": "c-3", "external_contract_id": "sub", "code": "calls",
					"timestamp": strconv.FormatInt(t1.Unix(), 10), "properties": map[string]any{"region": "eu"},
				},
				ev("c-old", "calls", rfc(start.Add(-time.Hour)), map[string]any{"region": "eu"}),
				ev("c-1", "calls", rfc(t1), map[string]any{"region": "eu"}),
				ev("t-1", "tokens", rfc(t1), map[string]any{"tokens": 5}),
				{
					"transaction_id": "t-2", "external_subscription_id": "sub", "external_contract_id": "ignored", "code": "tokens",
					"timestamp": rfc(t1), "properties": map[string]any{"tokens": "2.5"},
				},
				ev("p-1", "peak", rfc(t1), map[string]any{"value": 3}),
				ev("p-2", "peak", rfc(t1), map[string]any{"value": 9}),
				ev("p-3", "peak", rfc(t2), map[string]any{"value": 4}),
				ev("l-1", "level", rfc(t1), map[string]any{"value": 7}),
				ev("l-2", "level", rfc(t2), map[string]any{"value": 4}),
				ev("u-1", "users", rfc(t1), map[string]any{"user": "a"}),
				ev("u-2", "users", rfc(t1), map[string]any{"user": "b"}),
				ev("u-3", "users", rfc(t1), map[string]any{"user": "c"}),
				ev("u-4", "users", rfc(t2), map[string]any{"user": "b", "operation_type": "remove"}),
				ev("d-1", "doubled", rfc(t1), map[string]any{"a": 3}),
				ev("d-2", "doubled", rfc(t1), map[string]any{"a": "1.5"}),
				ev("g-1", "grouped", rfc(t1), map[string]any{"region": "eu"}),
				ev("g-2", "grouped", rfc(t1), map[string]any{"region": "eu"}),
				ev("g-3", "grouped", rfc(t1), map[string]any{"region": "us"}),
				ev("w-1", "weighted", rfc(t1), map[string]any{"value": 10}),
			}
			res := s.api(http.MethodPost, "/v1/events:batch", map[string]any{"events": batch})
			var dups []string
			for _, r := range res.list("results") {
				if r.str("duplicate") == "true" {
					dups = append(dups, r.str("transaction_id"))
				}
			}
			// The API reports the re-sent c-1 a duplicate on PostgreSQL; on
			// Kafka the ClickHouse writer drops it after the API answered
			// (libs/ingest), which the usage below checks.
			wantDups := []string{"c-1"}
			if mode == "kafka" {
				wantDups = nil
			}
			if !slices.Equal(dups, wantDups) {
				t.Errorf("batch duplicates: got = %v, want = %v", dups, wantDups)
			}
			if err := dockertest.Wait(t.Context(), time.Minute, func(context.Context) error {
				status, body := s.send(http.MethodGet, "/v1/events/c-2", s.key, "", nil)
				if status != http.StatusOK {
					return fmt.Errorf("GET /v1/events/c-2: status %d (%s)", status, body)
				}
				var got struct {
					Event struct {
						ExternalSubscriptionID string `json:"external_subscription_id"`
						Timestamp              string `json:"timestamp"`
					} `json:"event"`
				}
				if err := json.Unmarshal(body, &got); err != nil {
					return err
				}
				// Milliseconds always, as lago's iso8601(3)
				// (app/serializers/v1/event_serializer.rb:11).
				if want := t1.Format("2006-01-02T15:04:05") + ".500Z"; got.Event.ExternalSubscriptionID != "sub" || got.Event.Timestamp != want {
					return fmt.Errorf("aliased event got = %s %s, want = sub %s", got.Event.ExternalSubscriptionID, got.Event.Timestamp, want)
				}
				return nil
			}); err != nil {
				t.Error(err)
			}

			// Units and amounts (cents, at 1 a unit and 2 for calls in
			// Europe) of each charge, and the groups of the grouped one.
			want := map[string]string{
				"calls": "3/500", "tokens": "7.5/750", "peak": "9/900", "level": "4/400",
				"users": "2/200", "doubled": "9/900", "grouped": "3/300", "weighted": "ok",
			}
			wantFilters := map[string]string{"eu": "2/400", "": "1/100"}
			wantGroups := map[string]string{"eu": "2", "us": "1"}
			check := func(stage string) {
				t.Helper()
				var got, filters, groups map[string]string
				err := dockertest.Wait(t.Context(), 2*time.Minute, func(context.Context) error {
					got, filters, groups = s.ingestionUsage(t1)
					if !maps.Equal(got, want) || !maps.Equal(filters, wantFilters) || !maps.Equal(groups, wantGroups) {
						return fmt.Errorf("usage %v, filters %v, groups %v", got, filters, groups)
					}
					return nil
				})
				if err != nil {
					t.Errorf("%s: got = %v, filters %v, groups %v, want = %v, filters %v, groups %v",
						stage, got, filters, groups, want, wantFilters, wantGroups)
				}
			}
			check("from the events")
			s.idle()
			if n := s.completedJobs(kernel.JobDefineUsageBuckets); n == 0 {
				t.Errorf("completed %s jobs: got = 0, want = at least 1", kernel.JobDefineUsageBuckets)
			}
			if defs, rows := s.bucketRows(clickhouseURL); defs == 0 || rows == 0 {
				t.Errorf("ready bucket definitions and bucket rows: got = %d and %d, want = some of both", defs, rows)
			}
			check("from the usage buckets")

			// The in-advance fee of s-1.
			err := dockertest.Wait(t.Context(), time.Minute, func(context.Context) error {
				for _, f := range s.api(http.MethodGet, "/v1/fees", nil).list("fees") {
					if f.str("event_transaction_id") == "s-1" {
						if got := fmt.Sprintf("%s/%s/%d", decimalString(f.str("units")), f.str("pay_in_advance"), f.cents("amount")); got != "3/true/300" {
							return fmt.Errorf("fee of s-1 got = %s, want = 3/true/300", got)
						}
						return nil
					}
				}
				return fmt.Errorf("no fee of s-1")
			})
			if err != nil {
				t.Error(err)
			}
			s.noFailedJobs()
		})
	}
}

// ingestionCatalog creates the customer, the metrics and the plan of
// [TestScenarioIngestion].
func (s *scenario) ingestionCatalog() {
	s.t.Helper()
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
	for _, m := range []map[string]any{
		{"code": "calls", "aggregation_type": "AGGREGATION_TYPE_COUNT", "filters": []map[string]any{{"key": "region", "values": []string{"eu", "us"}}}},
		{"code": "tokens", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "tokens"},
		{"code": "peak", "aggregation_type": "AGGREGATION_TYPE_MAX", "field_name": "value"},
		{"code": "level", "aggregation_type": "AGGREGATION_TYPE_LATEST", "field_name": "value"},
		{"code": "users", "aggregation_type": "AGGREGATION_TYPE_UNIQUE_COUNT", "field_name": "user"},
		{"code": "doubled", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "total", "expression": "event.properties.a * 2"},
		{"code": "grouped", "aggregation_type": "AGGREGATION_TYPE_COUNT"},
		{"code": "seats", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "seats"},
		{"code": "weighted", "aggregation_type": "AGGREGATION_TYPE_WEIGHTED_SUM", "field_name": "value", "weighted_interval": "WEIGHTED_INTERVAL_SECONDS"},
	} {
		m["name"] = m["code"]
		s.api(http.MethodPost, "/v1/billable_metrics", m)
	}
	unit := map[string]any{"standard": map[string]any{"amount": "1"}}
	charge := func(code string) map[string]any {
		return map[string]any{"code": code, "billable_metric_id": "code:" + code, "charge_model": "CHARGE_MODEL_STANDARD", "properties": unit}
	}
	calls := charge("calls")
	calls["filters"] = []map[string]any{{
		"values":     map[string]any{"region": map[string]any{"values": []string{"eu"}}},
		"properties": map[string]any{"standard": map[string]any{"amount": "2"}},
	}}
	grouped := charge("grouped")
	grouped["properties"] = map[string]any{"standard": map[string]any{"amount": "1"}, "grouping": map[string]any{"pricing_group_keys": []string{"region"}}}
	seats := charge("seats")
	seats["pay_in_advance"], seats["invoiceable"] = true, false
	s.api(http.MethodPost, "/v1/plans", map[string]any{
		"name": "Plan", "code": "plan", "interval": "INTERVAL_MONTHLY", "amount": money("0"),
		"charges": []map[string]any{calls, charge("tokens"), charge("peak"), charge("level"), charge("users"), charge("doubled"), grouped, seats, charge("weighted")},
	})
}

// ingestionUsage reads the subscription's current usage: units/cents by
// metric code (the in-advance seats left out), the calls filters by
// region ("" for the default) and the grouped units by region. The
// weighted sum is "ok" when its units are 10 weighted by the seconds from
// t1 to the period's end rounded up to the second (postgres_store.rb:349)
// over the charges duration in days (postgres/weighted_sum_query.rb:116).
func (s *scenario) ingestionUsage(t1 time.Time) (charges, filters, groups map[string]string) {
	s.t.Helper()
	u := s.api(http.MethodGet, "/v1/customers/external_id%3Acust/subscriptions/external_id%3Asub/usage:current", nil).at("usage")
	charges, filters, groups = map[string]string{}, map[string]string{}, map[string]string{}
	for _, c := range u.list("charges") {
		code := c.str("billable_metric_code")
		if code == "seats" {
			continue
		}
		if code == "weighted" {
			from, _ := time.Parse(time.RFC3339Nano, u.str("from"))
			to, _ := time.Parse(time.RFC3339Nano, u.str("to"))
			if to.Truncate(time.Second) != to {
				to = to.Truncate(time.Second).Add(time.Second)
			}
			// The charges duration is in whole days (UTC customer).
			days := math.Round(to.Sub(from.Truncate(24*time.Hour)).Hours() / 24)
			want := 10 * to.Sub(t1).Seconds() / (days * 86400)
			got, err := strconv.ParseFloat(c.str("units"), 64)
			charges[code] = "ok"
			if err != nil || math.Abs(got-want) > 1e-9*want {
				charges[code] = fmt.Sprintf("units %s, want %v (period %s to %s)", c.str("units"), want, u.str("from"), u.str("to"))
			}
			continue
		}
		charges[code] = fmt.Sprintf("%s/%d", decimalString(c.str("units")), max(c.cents("amount"), 0))
		for _, f := range c.list("filters") {
			filters[f.str("values.region")] = fmt.Sprintf("%s/%d", decimalString(f.str("units")), max(f.cents("amount"), 0))
		}
		for _, g := range c.list("grouped_usage") {
			groups[g.str("grouped_by.region")] = decimalString(g.str("units"))
		}
	}
	return charges, filters, groups
}

// completedJobs counts the completed jobs of a kind.
func (s *scenario) completedJobs(kind string) int {
	s.t.Helper()
	var n int
	if err := s.pool.QueryRow(s.t.Context(), `SELECT count(*) FROM river.river_job WHERE kind = $1 AND state = 'completed'`, kind).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

// bucketRows counts the organization's ready usage bucket definitions and
// its bucket rows, in ClickHouse when clickhouseURL is set and in the
// PostgreSQL event store otherwise.
func (s *scenario) bucketRows(clickhouseURL string) (defs, rows int) {
	s.t.Helper()
	ctx := s.t.Context()
	if clickhouseURL != "" {
		conn, err := clickhouseConn(clickhouseURL)
		if err != nil {
			s.t.Fatal(err)
		}
		defer conn.Close()
		var d, r uint64
		if err := conn.QueryRow(ctx, `SELECT count() FROM usage_bucket_definitions FINAL WHERE organization_id = $1 AND ready = 1`, s.org).Scan(&d); err != nil {
			s.t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, `SELECT count() FROM usage_buckets_15m FINAL WHERE organization_id = $1`, s.org).Scan(&r); err != nil {
			s.t.Fatal(err)
		}
		return int(d), int(r)
	}
	var schema string
	if err := s.pool.QueryRow(ctx, `SELECT table_schema FROM information_schema.tables WHERE table_name = 'usage_buckets'`).Scan(&schema); err != nil {
		s.t.Fatal(err)
	}
	q := `SELECT (SELECT count(*) FROM ` + schema + `.usage_bucket_definitions WHERE organization_id = $1 AND ready),
		(SELECT count(*) FROM ` + schema + `.usage_buckets WHERE organization_id = $1)`
	if err := s.pool.QueryRow(ctx, q, s.org).Scan(&defs, &rows); err != nil {
		s.t.Fatal(err)
	}
	return defs, rows
}
