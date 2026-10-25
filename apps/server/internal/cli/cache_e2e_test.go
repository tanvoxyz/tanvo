package cli_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/tanvoxyz/tanvo/apps/server/internal/cli"
	"github.com/tanvoxyz/tanvo/libs/dockertest"
)

// startValkey starts a Valkey server and returns its URL and a client.
func startValkey(t *testing.T) (string, valkey.Client) {
	t.Helper()
	c := start(t, dockertest.Spec{Image: "valkey/valkey:8.1.10", Ports: []string{"6379/tcp"}})
	addr := c.Addr("6379/tcp")
	var client valkey.Client
	if err := dockertest.Wait(t.Context(), time.Minute, func(ctx context.Context) error {
		var err error
		client, err = valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, DisableCache: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return "redis://" + addr, client
}

// keys returns the keys of the server matching pattern.
func keys(t *testing.T, c valkey.Client, pattern string) []string {
	t.Helper()
	out, err := c.Do(t.Context(), c.B().Keys().Pattern(pattern).Build()).AsStrSlice()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// TestServeSharedCache serves two replicas over one database and one
// Valkey server: an API key read by the ingestion of one replica is
// cached for both, and rotating it on one replica stops the other from
// accepting it.
func TestServeSharedCache(t *testing.T) {
	t.Parallel()
	url, client := startValkey(t)
	s := newScenario(t, time.Now().UTC().Truncate(time.Second), scenarioOptions{environ: []string{"TANVO_CACHE_URL=" + url}})

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

	created := s.admin(http.MethodPost, "/v1/api_keys", map[string]any{"name": "ingestion"})
	id, secret := created.str("api_key.id"), created.str("api_key.secret")
	ingest := func(base, tx string) int {
		t.Helper()
		ev := map[string]any{
			"transaction_id": tx, "external_subscription_id": "sub-1", "code": "api_calls",
			"timestamp": s.clock.Now().Format(time.RFC3339),
		}
		body, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		status, _ := post(t, base+"/v1/events", secret, string(body))
		return status
	}

	if got := ingest(s.base, "tx-1"); got != http.StatusOK {
		t.Fatalf("ingest on the first replica got = %d, want = 200", got)
	}
	cached := keys(t, client, "tanvo:api-keys:*")
	if len(cached) != 1 {
		t.Fatalf("cached API keys got = %v, want = one", cached)
	}
	if got := ingest(other, "tx-2"); got != http.StatusOK {
		t.Fatalf("ingest on the second replica got = %d, want = 200", got)
	}
	if got := keys(t, client, "tanvo:api-keys:*"); !slices.Equal(got, cached) {
		t.Errorf("cached API keys got = %v, want = %v", got, cached)
	}

	s.admin(http.MethodPost, "/v1/api_keys/"+id+":rotate", map[string]any{})
	if got := keys(t, client, "tanvo:api-keys:*"); len(got) != 0 {
		t.Errorf("cached API keys after the rotation got = %v, want = none", got)
	}
	if got := ingest(other, "tx-3"); got != http.StatusUnauthorized {
		t.Errorf("ingest with the rotated key on the second replica got = %d, want = 401", got)
	}
}

// TestServeUnreachableCache starts a server whose cache server does not
// answer: it is ready and serves, every cache missing.
func TestServeUnreachableCache(t *testing.T) {
	t.Parallel()
	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	s := newScenario(t, time.Now().UTC().Truncate(time.Second), scenarioOptions{environ: []string{"TANVO_CACHE_URL=redis://127.0.0.1:" + port}})
	for _, tx := range []string{"tx-1", "tx-2"} {
		s.api(http.MethodPost, "/v1/events", map[string]any{
			"transaction_id": tx, "external_subscription_id": "sub-1", "code": "api_calls",
			"timestamp": s.clock.Now().Format(time.RFC3339),
		})
	}
}
