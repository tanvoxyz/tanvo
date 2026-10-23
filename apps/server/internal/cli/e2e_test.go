package cli_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/cli"
	"github.com/tanvoxyz/tanvo/apps/server/internal/dockertest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
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

// startPostgres starts a postgres:18 container and returns its URL.
func startPostgres(t *testing.T) string {
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
	c, err := engine.Run(ctx, dockertest.Spec{
		Image: "postgres:18",
		Env:   []string{"POSTGRES_USER=tanvo", "POSTGRES_PASSWORD=tanvo", "POSTGRES_DB=tanvo"},
		Cmd:   []string{"postgres", "-c", "fsync=off", "-c", "synchronous_commit=off"},
		Ports: []string{"5432/tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
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

	if err := cli.Run(ctx, []string{"migrate"}, cli.Env{Environ: environ, Stdout: logs}); err != nil {
		t.Fatalf("migrate error = %v", err)
	}
	// Migrating again is a no-op.
	if err := cli.Run(ctx, []string{"migrate"}, cli.Env{Environ: environ, Stdout: logs}); err != nil {
		t.Fatalf("second migrate error = %v", err)
	}

	org, secret := seed(t, dsn)

	serveCtx, stop := context.WithCancel(ctx)
	defer stop()
	started := make(chan *app.App, 1)
	done := make(chan error, 1)
	go func() {
		done <- cli.Run(serveCtx, []string{"serve", "--role", "all"}, cli.Env{
			Environ: environ, Stdout: logs, Started: func(a *app.App) { started <- a },
		})
	}()
	var srv *app.App
	select {
	case srv = <-started:
	case err := <-done:
		t.Fatalf("serve error = %v", err)
	case <-time.After(time.Minute):
		t.Fatal("serve did not start")
	}
	base := "http://" + srv.Addr().String()

	if err := dockertest.Wait(ctx, 30*time.Second, func(ctx context.Context) error {
		return expect(ctx, base+"/readyz", http.StatusOK)
	}); err != nil {
		t.Fatalf("readiness: %v", err)
	}
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

	n, err := srv.EventStores().For(org).Count(ctx, metering.Query{
		OrganizationID: org.String(), Code: "api_calls", From: at.Add(-time.Hour), To: at.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if n != 2 {
		t.Errorf("Count() got = %d, want = 2", n)
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
	if stored != 2 {
		t.Errorf("eventstore.events rows got = %d, want = 2", stored)
	}

	stop()
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

func post(t *testing.T, url, key, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}
