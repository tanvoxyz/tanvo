package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/apps/server/internal/cli"
	"github.com/tanvoxyz/tanvo/libs/dockertest"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// pgbouncerImage is the PgBouncer of [startPgBouncer]: 1.21 or later,
// which keeps the protocol-level prepared statements of pgx's statement
// cache across transaction pooling (max_prepared_statements).
const pgbouncerImage = "edoburu/pgbouncer:v1.25.2-p0"

// startPgBouncer starts PgBouncer in transaction pooling mode in front of
// the postgres container pg and returns its URL and its address on the
// container network.
func startPgBouncer(t *testing.T, pg *dockertest.Container) (dsn, ip string) {
	t.Helper()
	if pg.IP == "" {
		t.Fatal("postgres container: no address on the container network")
	}
	c := start(t, dockertest.Spec{
		Image: pgbouncerImage,
		Name:  "pgbouncer",
		Env: []string{
			"DATABASE_URL=postgres://tanvo:tanvo@" + pg.IP + ":5432/tanvo",
			"POOL_MODE=transaction",
			"AUTH_TYPE=scram-sha-256",
			"MAX_PREPARED_STATEMENTS=200",
			"MAX_CLIENT_CONN=500",
			"DEFAULT_POOL_SIZE=20",
		},
		Ports: []string{"5432/tcp"},
	})
	dsn = fmt.Sprintf("postgres://tanvo:tanvo@%s/tanvo?sslmode=disable", c.Addr("5432/tcp"))
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := dockertest.Wait(t.Context(), time.Minute, pool.Ping); err != nil {
		t.Fatal(err)
	}
	return dsn, c.IP
}

// TestServePgBouncer serves two replicas whose TANVO_DATABASE_URL goes
// through PgBouncer in transaction pooling mode, with
// TANVO_DATABASE_DIRECT_URL direct: the migrations, the API, ingestion
// and the jobs work through the pooler, the catalog change of one
// replica reaches the other's ingestion cache through LISTEN on a direct
// connection, and River's notifications run the jobs.
func TestServePgBouncer(t *testing.T) {
	t.Parallel()
	start := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	s := newScenario(t, start, scenarioOptions{pgbouncer: true})

	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	environ := slices.Clone(s.environ)
	for i, e := range environ {
		if strings.HasPrefix(e, "TANVO_HTTP_ADDR=") {
			environ[i] = "TANVO_HTTP_ADDR=127.0.0.1:" + port
		}
	}
	_, other, stop := serveEnv(t, environ, s.logs, cli.Env{Clock: s.clock, ManualClockJobs: true})
	t.Cleanup(stop)

	// The catalog: a metric with an expression no plan uses (so that it
	// may change), and a subscription on a plan of another metric.
	s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{
		"name": "Sum", "code": "sum", "aggregation_type": "AGGREGATION_TYPE_SUM", "field_name": "value",
		"expression": "event.properties.a + event.properties.b",
	})
	s.api(http.MethodPost, "/v1/billable_metrics", map[string]any{"name": "Calls", "code": "calls", "aggregation_type": "AGGREGATION_TYPE_COUNT"})
	s.api(http.MethodPost, "/v1/plans", map[string]any{
		"name": "P", "code": "p", "interval": "INTERVAL_MONTHLY", "amount": money("0"),
		"charges": []map[string]any{{
			"code": "calls", "billable_metric_id": "code:calls", "charge_model": "CHARGE_MODEL_STANDARD",
			"properties": map[string]any{"standard": map[string]any{"amount": "1"}},
		}},
	})
	s.api(http.MethodPost, "/v1/customers", map[string]any{"external_id": "cust", "name": "Customer"})
	s.api(http.MethodPost, "/v1/subscriptions", map[string]any{"external_id": "sub", "customer_id": "external_id:cust", "plan_id": "code:p"})

	// A clock job and the jobs the writes enqueued run through the pooler.
	s.trigger(jobs.KindActivateSubscriptions)
	s.idle()
	s.noFailedJobs()
	var completed int
	if err := s.pool.QueryRow(t.Context(), "SELECT count(*) FROM river.river_job WHERE kind = $1 AND state = 'completed'", jobs.KindActivateSubscriptions).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Errorf("completed %s jobs got = %d, want = 1", jobs.KindActivateSubscriptions, completed)
	}

	// The second replica caches the metric on its first event.
	n := 0
	ingest := func(base string) (int, string) {
		t.Helper()
		n++
		body, err := json.Marshal(map[string]any{
			"transaction_id": fmt.Sprintf("tx-%d", n), "external_subscription_id": "sub", "code": "sum",
			"timestamp": s.clock.Now().Format(time.RFC3339), "properties": map[string]any{"a": "1", "b": "2"},
		})
		if err != nil {
			t.Fatal(err)
		}
		status, resp := post(t, base+"/v1/events", s.key, string(body))
		var out struct {
			Event struct {
				Properties map[string]any `json:"properties"`
			} `json:"event"`
		}
		_ = json.Unmarshal(resp, &out)
		return status, fmt.Sprint(out.Event.Properties["value"])
	}
	if status, value := ingest(other); status != http.StatusOK || value != "3.0" {
		t.Fatalf("event on the second replica got = %d %s, want = 200 3.0", status, value)
	}

	// The first replica changes the expression; the notification reaches
	// the second replica's listener long before the cache expires.
	s.api(http.MethodPatch, "/v1/billable_metrics/code%3Asum?update_mask=expression", map[string]any{
		"expression": "event.properties.a * event.properties.b",
	})
	if err := dockertest.Wait(t.Context(), 30*time.Second, func(context.Context) error {
		if status, value := ingest(other); status != http.StatusOK || value != "2.0" {
			return fmt.Errorf("event on the second replica: got %d %s, want 200 2.0", status, value)
		}
		return nil
	}); err != nil {
		t.Error(err)
	}

	// The business connections are PgBouncer's; the catalog listeners of
	// both replicas are direct.
	var pooled, listeners int
	if err := s.pool.QueryRow(t.Context(), `SELECT
			count(*) FILTER (WHERE client_addr = $1::inet),
			count(*) FILTER (WHERE client_addr <> $1::inet AND query = 'LISTEN ' || $2)
		FROM pg_stat_activity WHERE datname = 'tanvo'`, s.bouncer, store.CatalogChannel).Scan(&pooled, &listeners); err != nil {
		t.Fatal(err)
	}
	if pooled == 0 {
		t.Errorf("connections from PgBouncer got = 0, want = some")
	}
	if listeners != 2 {
		t.Errorf("direct catalog listeners got = %d, want = 2", listeners)
	}
}
