package cli_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/cli"
	"github.com/tanvoxyz/tanvo/apps/server/internal/dockertest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/metering"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// lockedBuffer collects the server's logs.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// schemaEnv set to "source" makes [migrate] replace the store's migrated
// tables with libs/store/schema, while a change to the schema has no
// migration yet (as libs/store's tests do).
const schemaEnv = "TANVO_STORE_TEST_SCHEMA"

// migrate runs `tanvo migrate`, then, with schemaEnv set to "source",
// reloads the store's tables from libs/store/schema, keeping the applied
// migrations so that the schema check passes.
func migrate(t *testing.T, dsn string, environ []string, logs *lockedBuffer) {
	t.Helper()
	ctx := t.Context()
	if err := cli.Run(ctx, []string{"migrate"}, cli.Env{Environ: environ, Stdout: logs}); err != nil {
		t.Fatalf("migrate error = %v", err)
	}
	if os.Getenv(schemaEnv) != "source" {
		return
	}
	files, err := filepath.Glob("../../../../libs/store/schema/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("store schema files got = %v %v, want some", files, err)
	}
	slices.Sort(files)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	stmts := []string{
		"CREATE SCHEMA e2e_keep",
		"ALTER TABLE public.tanvo_schema_revisions SET SCHEMA e2e_keep",
		"DROP SCHEMA public CASCADE",
		"CREATE SCHEMA public",
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		stmts = append(stmts, string(b))
	}
	stmts = append(stmts, "ALTER TABLE e2e_keep.tanvo_schema_revisions SET SCHEMA public", "DROP SCHEMA e2e_keep")
	for _, stmt := range stmts {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("load the store schema: %v", err)
		}
	}
}

// start starts a container, skipping the test without a Docker engine.
func start(t *testing.T, spec dockertest.Spec) *dockertest.Container {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	ctx := t.Context()
	engine, err := dockertest.Connect(ctx)
	if errors.Is(err, dockertest.ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	c, err := engine.Run(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// startPostgres starts a postgres:18 container and returns its URL.
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	c := start(t, dockertest.Spec{
		Image: "postgres:18",
		Env:   []string{"POSTGRES_USER=tanvo", "POSTGRES_PASSWORD=tanvo", "POSTGRES_DB=tanvo"},
		Cmd:   []string{"postgres", "-c", "fsync=off", "-c", "synchronous_commit=off"},
		Ports: []string{"5432/tcp"},
	})
	dsn := fmt.Sprintf("postgres://tanvo:tanvo@127.0.0.1:%s/tanvo?sslmode=disable", c.Ports["5432/tcp"])
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := dockertest.Wait(ctx, time.Minute, pool.Ping); err != nil {
		t.Fatal(err)
	}
	return dsn
}

// TestServeSmoke migrates a fresh database, serves every role, ingests an
// event over the REST API, reads it back from the event store and shuts
// down cleanly.
func TestServeSmoke(t *testing.T) {
	dsn := startPostgres(t)
	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server logs:\n%s", logs.String())
		}
	})
	environ := []string{
		"TANVO_DATABASE_URL=" + dsn,
		"TANVO_HTTP_ADDR=127.0.0.1:" + port,
		"TANVO_MIGRATE_ON_START=false",
		"TANVO_SHUTDOWN_TIMEOUT=10s",
		"TANVO_LOG_FORMAT=text",
		"OTEL_SDK_DISABLED=true",
	}
	ctx := t.Context()

	migrate(t, dsn, environ, logs)
	// Migrating again is a no-op.
	if err := cli.Run(ctx, []string{"migrate"}, cli.Env{Environ: environ, Stdout: logs}); err != nil {
		t.Fatalf("second migrate error = %v", err)
	}

	org, secret := seed(t, dsn)

	srv, base, stop := serve(t, environ, logs)
	if err := expect(ctx, base+"/healthz", http.StatusOK); err != nil {
		t.Fatalf("liveness: %v", err)
	}

	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	body := fmt.Sprintf(`{"transaction_id":"tx-1","external_subscription_id":"sub-1","code":"api_calls","timestamp":%q,"properties":{"region":"eu","tokens":12,"bytes":12345678901234567890}}`, at.Format(time.RFC3339))
	status, resp := post(t, base+"/v1/events", secret, body)
	if status != http.StatusOK {
		t.Fatalf("POST /v1/events status got = %d, want = 200; body %s", status, resp)
	}
	var out struct {
		Event struct {
			TransactionID string            `json:"transaction_id"`
			Properties    map[string]string `json:"properties"`
		} `json:"event"`
		Duplicate bool `json:"duplicate"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("response %s: %v", resp, err)
	}
	if out.Event.TransactionID != "tx-1" || out.Duplicate || out.Event.Properties["tokens"] != "12" || out.Event.Properties["bytes"] != "12345678901234567890" {
		t.Errorf("response got = %s, want = tx-1 accepted with tokens 12 and bytes 12345678901234567890", resp)
	}
	if status, resp := post(t, base+"/v1/events", secret, body); status != http.StatusOK || !strings.Contains(string(resp), `"duplicate":true`) {
		t.Errorf("repeated POST got = %d %s, want = 200 duplicate", status, resp)
	}
	if status, _ := post(t, base+"/v1/events", "wrong", body); status != http.StatusUnauthorized {
		t.Errorf("POST with a wrong key status got = %d, want = 401", status)
	}
	if status, resp := post(t, base+"/v1/events", secret, `{"transaction_id":"tx-2","code":"api_calls"}`); status != http.StatusBadRequest {
		t.Errorf("POST without a subscription status got = %d %s, want = 400", status, resp)
	}
	// Connect JSON keeps property numbers exact too.
	batch := fmt.Sprintf(`{"events":[{"transaction_id":"tx-3","external_subscription_id":"sub-1","code":"api_calls","timestamp":%q,"properties":{"rate":0.12345678901234567890123}}]}`, at.Format(time.RFC3339))
	if status, resp := post(t, base+"/tanvo.event.v1.EventService/IngestEvents", secret, batch); status != http.StatusOK || !strings.Contains(string(resp), `"rate":"0.12345678901234567890123"`) {
		t.Errorf("Connect IngestEvents got = %d %s, want = 200 with rate 0.12345678901234567890123", status, resp)
	}

	// The REST batch validates and deduplicates each event. It comes
	// through a private proxy, which the loopback peer (trusted) forwards.
	batch = fmt.Sprintf(`{"events":[
		{"transaction_id":"tx-4","external_subscription_id":"sub-1","code":"api_calls","timestamp":%[1]q},
		{"transaction_id":"tx-5","external_subscription_id":"sub-2","code":"api_calls","timestamp":%[1]q},
		{"transaction_id":"tx-1","external_subscription_id":"sub-1","code":"api_calls","timestamp":%[1]q}]}`, at.Format(time.RFC3339))
	status, resp = postWith(t, base+"/v1/events:batch", secret, batch, http.Header{"X-Forwarded-For": {forwardedClient + ", 10.0.0.3"}})
	if status != http.StatusOK {
		t.Fatalf("POST /v1/events:batch status got = %d, want = 200; body %s", status, resp)
	}
	var results struct {
		Results []struct {
			TransactionID string `json:"transaction_id"`
			Duplicate     bool   `json:"duplicate"`
			Event         struct {
				UserAgent string `json:"user_agent"`
				IPAddress string `json:"ip_address"`
			} `json:"event"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp, &results); err != nil {
		t.Fatalf("batch response %s: %v", resp, err)
	}
	var got []string
	for _, r := range results.Results {
		got = append(got, fmt.Sprintf("%s:%t:%s:%s", r.TransactionID, r.Duplicate, r.Event.UserAgent, r.Event.IPAddress))
	}
	want := []string{"tx-4:false:" + userAgent + ":" + forwardedClient, "tx-5:false:" + userAgent + ":" + forwardedClient, "tx-1:true:" + userAgent + ":" + forwardedClient}
	if !slices.Equal(got, want) {
		t.Errorf("batch results got = %v, want = %v", got, want)
	}

	n, err := srv.EventStores().For(org).Count(ctx, metering.Query{
		OrganizationID: org.String(), Code: "api_calls", From: at.Add(-time.Hour), To: at.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if n != 4 {
		t.Errorf("Count() got = %d, want = 4", n)
	}
	// The PostgreSQL event store is reachable in its own schema as well.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var stored int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM eventstore.events WHERE organization_id = $1", org.String()).Scan(&stored); err != nil {
		t.Fatalf("eventstore.events: %v", err)
	}
	if stored != 4 {
		t.Errorf("eventstore.events rows got = %d, want = 4", stored)
	}
	// The raw log keeps the request metadata.
	var ua, ip string
	if err := pool.QueryRow(ctx, "SELECT metadata->>'user_agent', metadata->>'ip_address' FROM events WHERE organization_id = $1 AND transaction_id = 'tx-4'", org.String()).Scan(&ua, &ip); err != nil {
		t.Fatalf("events metadata: %v", err)
	}
	if ua != userAgent || ip != forwardedClient {
		t.Errorf("events metadata got = %q %q, want = %q %s", ua, ip, userAgent, forwardedClient)
	}

	stop()
}

// startKafka starts a Redpanda broker and a ClickHouse server and returns
// the broker address and the ClickHouse URL.
func startKafka(t *testing.T) (brokers, clickhouseURL string) {
	t.Helper()
	ctx := t.Context()
	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	start(t, dockertest.Spec{
		Image: "docker.redpanda.com/redpandadata/redpanda:v25.2.10",
		Cmd: []string{
			"redpanda", "start", "--mode", "dev-container", "--smp", "1", "--overprovisioned",
			"--kafka-addr", "PLAINTEXT://0.0.0.0:" + port,
			"--advertise-kafka-addr", "PLAINTEXT://127.0.0.1:" + port,
		},
		Ports:     []string{port + "/tcp"},
		HostPorts: map[string]string{port + "/tcp": port},
	})
	c := start(t, dockertest.Spec{
		Image: "clickhouse/clickhouse-server:25.8",
		Env:   []string{"CLICKHOUSE_USER=tanvo", "CLICKHOUSE_PASSWORD=tanvo", "CLICKHOUSE_DB=tanvo"},
		Ports: []string{"9000/tcp"},
	})
	brokers = "127.0.0.1:" + port
	clickhouseURL = fmt.Sprintf("clickhouse://tanvo:tanvo@127.0.0.1:%s/tanvo", c.Ports["9000/tcp"])
	if err := dockertest.Wait(ctx, 2*time.Minute, func(ctx context.Context) error {
		cl, err := kgo.NewClient(kgo.SeedBrokers(brokers))
		if err != nil {
			return err
		}
		defer cl.Close()
		return cl.Ping(ctx)
	}); err != nil {
		t.Fatalf("redpanda: %v", err)
	}
	if err := dockertest.Wait(ctx, 2*time.Minute, func(ctx context.Context) error {
		conn, err := clickhouseConn(clickhouseURL)
		if err != nil {
			return err
		}
		defer conn.Close()
		return conn.Ping(ctx)
	}); err != nil {
		t.Fatalf("clickhouse: %v", err)
	}
	return brokers, clickhouseURL
}

func clickhouseConn(url string) (driver.Conn, error) {
	opts, err := ch.ParseDSN(url)
	if err != nil {
		return nil, err
	}
	return ch.Open(opts)
}

// TestServeKafka ingests through the Kafka path: the API produces the
// raw events, the consumers enrich them and write them to ClickHouse with
// their request metadata.
func TestServeKafka(t *testing.T) {
	dsn := startPostgres(t)
	brokers, clickhouseURL := startKafka(t)
	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server logs:\n%s", logs.String())
		}
	})
	environ := []string{
		"TANVO_DATABASE_URL=" + dsn,
		"TANVO_HTTP_ADDR=127.0.0.1:" + port,
		"TANVO_MIGRATE_ON_START=false",
		"TANVO_SHUTDOWN_TIMEOUT=10s",
		"TANVO_LOG_FORMAT=text",
		"TANVO_INGEST_MODE=kafka",
		"TANVO_KAFKA_BROKERS=" + brokers,
		"TANVO_KAFKA_CREATE_TOPICS=true",
		"TANVO_CLICKHOUSE_URL=" + clickhouseURL,
		"OTEL_SDK_DISABLED=true",
	}
	ctx := t.Context()
	migrate(t, dsn, environ, logs)
	org, secret := seed(t, dsn)
	srv, base, stop := serve(t, environ, logs)
	defer stop()

	if status, body := post(t, base+"/v1/billable_metrics", secret,
		`{"name":"API calls","code":"api_calls","aggregation_type":"AGGREGATION_TYPE_SUM","field_name":"tokens"}`); status != http.StatusOK {
		t.Fatalf("POST /v1/billable_metrics status got = %d (%s), want = 200", status, body)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	batch := fmt.Sprintf(`{"events":[
		{"transaction_id":"k-1","external_subscription_id":"sub-1","code":"api_calls","timestamp":%[1]q,"properties":{"tokens":3}},
		{"transaction_id":"k-2","external_subscription_id":"sub-1","code":"api_calls","timestamp":%[1]q,"properties":{"tokens":4}},
		{"transaction_id":"k-1","external_subscription_id":"sub-1","code":"api_calls","timestamp":%[1]q}]}`, at.Format(time.RFC3339))
	// The topics the server created may not have a leader yet: the
	// ingestion is idempotent, so the batch is retried until produced.
	var (
		status int
		resp   []byte
	)
	if err := dockertest.Wait(ctx, time.Minute, func(context.Context) error {
		if status, resp = post(t, base+"/v1/events:batch", secret, batch); status != http.StatusOK {
			return fmt.Errorf("POST /v1/events:batch status got = %d, want = 200", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp), `"duplicate":true`) {
		t.Fatalf("POST /v1/events:batch got = %s, want = the repeat of k-1 a duplicate", resp)
	}
	es := srv.EventStores().For(org)
	if es == nil || es != metering.Store(srv.EventStores().ClickHouse()) {
		t.Fatalf("event store of a Kafka organization got = %T, want = ClickHouse", es)
	}
	q := metering.Query{OrganizationID: org.String(), SubscriptionID: "sub-1", Code: "api_calls", From: at.Add(-time.Hour), To: at.Add(time.Hour)}
	if err := dockertest.Wait(ctx, time.Minute, func(ctx context.Context) error {
		n, err := es.Count(ctx, q)
		if err != nil {
			return err
		}
		if n != 2 {
			return fmt.Errorf("count got = %d, want = 2", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("events in ClickHouse: %v", err)
	}
	conn, err := clickhouseConn(clickhouseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ua, ip string
	if err := conn.QueryRow(ctx, `SELECT user_agent, ip_address FROM events_raw FINAL WHERE organization_id = $1 AND transaction_id = 'k-2'`, org.String()).Scan(&ua, &ip); err != nil {
		t.Fatalf("events_raw: %v", err)
	}
	if ua != userAgent || ip != "127.0.0.1" {
		t.Errorf("events_raw metadata got = %q %q, want = %q 127.0.0.1", ua, ip, userAgent)
	}

	// The enriched events keep the value enrichment gave them.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/enriched_events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var enriched struct {
		EnrichedEvents []struct {
			TransactionID string `json:"transaction_id"`
			Value         string `json:"value"`
			DecimalValue  string `json:"decimal_value"`
			EnrichedAt    string `json:"enriched_at"`
		} `json:"enriched_events"`
	}
	if err := json.NewDecoder(res.Body).Decode(&enriched); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/enriched_events status got = %d (%v), want = 200", res.StatusCode, err)
	}
	values := map[string]string{}
	for _, e := range enriched.EnrichedEvents {
		if e.EnrichedAt == "" {
			t.Errorf("enriched event %s got = no enriched_at, want = one", e.TransactionID)
		}
		values[e.TransactionID] = e.Value + "/" + e.DecimalValue
	}
	if want := map[string]string{"k-1": "3/3", "k-2": "4/4"}; !maps.Equal(values, want) {
		t.Errorf("enriched values got = %v, want = %v", values, want)
	}

	// A connector's message (libs/ingest/connectors/http.yml) is stored as
	// the API's are, and a message that is not an event lands in
	// events_dead_letter.
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	connector := fmt.Sprintf(`{"organization_id":%q,"external_subscription_id":"sub-1","transaction_id":"c-1","code":"api_calls",
		"timestamp":%d,"properties":{"tokens":5},"ingested_at":%d,"source":"http_connector","precise_total_amount_cents":"0"}`,
		org.String(), at.Unix(), time.Now().Unix())
	if err := cl.ProduceSync(ctx,
		&kgo.Record{Topic: "events.raw", Key: []byte(org.String() + "/sub-1/c-1"), Value: []byte(connector)},
		&kgo.Record{Topic: "events.raw", Key: []byte("garbage"), Value: []byte("not an event")},
	).FirstErr(); err != nil {
		t.Fatalf("produce: %v", err)
	}
	if err := dockertest.Wait(ctx, time.Minute, func(ctx context.Context) error {
		var value string
		if err := conn.QueryRow(ctx, `SELECT value FROM events_enriched FINAL WHERE organization_id = $1 AND transaction_id = 'c-1'`, org.String()).Scan(&value); err != nil {
			return err
		}
		if value != "5" {
			return fmt.Errorf("connector event value got = %q, want = 5", value)
		}
		var code string
		if err := conn.QueryRow(ctx, `SELECT error_code FROM events_dead_letter WHERE event = '"not an event"'`).Scan(&code); err != nil {
			return err
		}
		if code != ingest.ErrorDecode {
			return fmt.Errorf("dead letter code got = %q, want = %s", code, ingest.ErrorDecode)
		}
		return nil
	}); err != nil {
		t.Fatalf("connector event and dead letter: %v", err)
	}
}

// serve runs `tanvo serve --role all` until the test ends or stop is
// called, waits for readiness and returns the app and its base URL. stop
// checks the server exits cleanly.
func serve(t *testing.T, environ []string, logs *lockedBuffer) (srv *app.App, base string, stop func()) {
	t.Helper()
	return serveWith(t, environ, logs, nil)
}

// serveWith is [serve] with the billing clock clock (nil: the system
// clock).
func serveWith(t *testing.T, environ []string, logs *lockedBuffer, clock core.Clock) (srv *app.App, base string, stop func()) {
	t.Helper()
	ctx := t.Context()
	serveCtx, cancel := context.WithCancel(ctx)
	started := make(chan *app.App, 1)
	done := make(chan error, 1)
	go func() {
		done <- cli.Run(serveCtx, []string{"serve", "--role", "all"}, cli.Env{
			Environ: environ, Stdout: logs, Started: func(a *app.App) { started <- a }, Clock: clock,
		})
	}()
	select {
	case srv = <-started:
	case err := <-done:
		cancel()
		t.Fatalf("serve error = %v", err)
	case <-time.After(2 * time.Minute):
		cancel()
		t.Fatal("serve did not start")
	}
	base = "http://" + srv.Addr().String()
	var once sync.Once
	stop = func() {
		t.Helper()
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("serve exit error = %v, want = nil", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("serve did not stop")
			}
			if !strings.Contains(logs.String(), "tanvo: stopped") {
				t.Error("logs got = no clean stop, want = \"tanvo: stopped\"")
			}
		})
	}
	t.Cleanup(cancel)
	if err := dockertest.Wait(ctx, 30*time.Second, func(ctx context.Context) error {
		return expect(ctx, base+"/readyz", http.StatusOK)
	}); err != nil {
		t.Fatalf("readiness: %v", err)
	}
	return srv, base, stop
}

// seed creates an organization and an API key with every permission.
func seed(t *testing.T, dsn string) (core.OrganizationID, string) {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Pool().Close()
	ids := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](ids)
	if err := st.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return r.Organizations.Create(ctx, organization.New(org, "Acme", time.Now().UTC()))
	}); err != nil {
		t.Fatalf("create organization: %v", err)
	}
	keys := auth.NewAPIKeys(auth.Deps{Tx: st, Clock: core.SystemClock{}, IDs: ids, Rand: rand.Reader}, auth.NewUsageTracker())
	k, err := keys.Create(ctx, org, nil, nil)
	if err != nil {
		t.Fatalf("create API key: %v", err)
	}
	return org, k.Secret
}

func expect(ctx context.Context, url string, want int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		return fmt.Errorf("GET %s status got = %d, want = %d", url, resp.StatusCode, want)
	}
	return nil
}

// userAgent is the User-Agent of the test's requests.
const userAgent = "tanvo-e2e/1"

// forwardedClient is the client address the test's proxied requests
// carry in X-Forwarded-For.
const forwardedClient = "198.51.100.23"

func post(t *testing.T, url, key, body string) (int, []byte) {
	t.Helper()
	return postWith(t, url, key, body, nil)
}

// postWith posts body with the extra header h.
func postWith(t *testing.T, url, key, body string, h http.Header) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", userAgent)
	for k, vs := range h {
		req.Header[k] = vs
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}
