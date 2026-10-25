package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/cli"
	"github.com/tanvoxyz/tanvo/apps/server/internal/dockertest"
	"github.com/tanvoxyz/tanvo/libs/ingest"
)

// TestEventsReprocess runs `tanvo events reprocess` on both event stores:
// an event ingested before its metric existed, on a subscription whose
// plan gains a charge on the metric afterwards, is found by the dry run,
// and re-enriched by the job the reprocess run enqueues. In kafka mode
// the enriched event then carries the metric's value.
//
// source: spec/jobs/events/stores/clickhouse/pre_enrichment_check_job_spec.rb:22
func TestEventsReprocess(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"postgres", "kafka"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var environ []string
			if mode == "kafka" {
				brokers, clickhouseURL := startKafka(t)
				environ = []string{
					"TANVO_INGEST_MODE=kafka", "TANVO_KAFKA_BROKERS=" + brokers, "TANVO_KAFKA_CREATE_TOPICS=true",
					"TANVO_CLICKHOUSE_URL=" + clickhouseURL,
				}
			}
			start := time.Now().UTC().Truncate(time.Second)
			s := newScenario(t, start, scenarioOptions{environ: environ})
			s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
			s.api(http.MethodPost, "/v1/plans", map[string]any{
				"name": "Plan", "code": "plan", "interval": "INTERVAL_MONTHLY", "amount": map[string]any{"currency": "EUR", "amount": "0"},
			})
			sub := s.api(http.MethodPost, "/v1/subscriptions", map[string]any{
				"external_id": "sub", "customer_id": "external_id:cust", "plan_id": "code:plan",
			}).str("subscription.id")

			// The event comes before its metric.
			s.at(start.Add(time.Minute))
			ev := map[string]any{
				"transaction_id": "tx-1", "external_subscription_id": "sub", "code": "tokens",
				"timestamp": s.clock.Now().Format(time.RFC3339), "properties": map[string]any{"count": 7},
			}
			if err := dockertest.Wait(t.Context(), time.Minute, func(context.Context) error {
				// The topics the server created may not have a leader yet.
				if status, body := s.send(http.MethodPost, "/v1/events", s.key, "", ev); status != http.StatusOK {
					return fmt.Errorf("POST /v1/events: status %d (%s)", status, body)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if mode == "kafka" {
				if v := s.enrichedValue(t, "tx-1", func(string) bool { return true }); v != "" {
					t.Fatalf("enriched value before the metric got = %q, want = none", v)
				}
			}

			// The metric, then a charge on the plan after the subscription
			// started.
			s.at(start.Add(time.Hour))
			s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{
				"name": "Tokens", "code": "tokens", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "count",
			})
			s.api(http.MethodPost, "/v1/plans/code:plan/charges", map[string]any{
				"code": "tokens", "billable_metric_id": "code:tokens", "charge_model": "CHARGE_MODEL_STANDARD",
				"properties": map[string]any{"standard": map[string]any{"amount": "1"}},
			})

			run := func(args ...string) string {
				t.Helper()
				var out bytes.Buffer
				env := cli.Env{Environ: append([]string{
					"TANVO_DATABASE_URL=" + s.pool.Config().ConnString(), "TANVO_LOG_FORMAT=text", "OTEL_SDK_DISABLED=true",
				}, environ...), Stdout: &out}
				if err := cli.Run(t.Context(), append([]string{"events", "reprocess", "--organization", s.org}, args...), env); err != nil {
					t.Fatalf("events reprocess %v error = %v; output %s", args, err, out.String())
				}
				return out.String()
			}
			want := "subscription " + sub + ": tokens\n"
			if out := run(); !strings.Contains(out, "[DRY RUN] "+want) || !strings.Contains(out, "[DRY RUN] 1 subscriptions detected") {
				t.Errorf("dry run output got = %q, want = the subscription with tokens", out)
			}
			if n := s.jobCount(t); n != 0 {
				t.Errorf("re-enrichment jobs after the dry run got = %d, want = 0", n)
			}
			if out := run("--reprocess", "--sleep", "0s", "--batch-size", "10"); !strings.Contains(out, "[REPROCESS] "+want) {
				t.Errorf("reprocess output got = %q, want = the subscription with tokens", out)
			}
			s.idle()
			s.noFailedJobs()
			if n := s.jobCount(t); n != 1 {
				t.Errorf("re-enrichment jobs got = %d, want = 1", n)
			}
			if !strings.Contains(s.logs.String(), "subscription_id="+sub+" events=1 batches=1") {
				t.Errorf("server logs got no re-enrichment of 1 event of %s", sub)
			}
			if mode == "kafka" {
				if v := s.enrichedValue(t, "tx-1", func(v string) bool { return v == "7" }); v != "7" {
					t.Errorf("enriched value after re-enrichment got = %q, want = 7", v)
				}
			}
		})
	}
}

// jobCount returns the number of re-enrichment jobs.
func (s *scenario) jobCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM river.river_job WHERE kind = $1`, ingest.JobReEnrichSubscription).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// enrichedValue waits for the enriched event with the transaction ID
// whose value satisfies ok and returns its value.
func (s *scenario) enrichedValue(t *testing.T, tx string, ok func(string) bool) string {
	t.Helper()
	var value string
	if err := dockertest.Wait(t.Context(), time.Minute, func(context.Context) error {
		for _, e := range s.api(http.MethodGet, "/v1/enriched_events", nil).list("enriched_events") {
			if e.str("transaction_id") == tx {
				if value = e.str("value"); ok(value) {
					return nil
				}
				return fmt.Errorf("enriched event %s value %q", tx, value)
			}
		}
		return fmt.Errorf("no enriched event %s", tx)
	}); err != nil {
		t.Fatal(err)
	}
	return value
}
