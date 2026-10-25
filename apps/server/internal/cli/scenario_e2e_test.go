package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/cli"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/dockertest"
)

// scenario is a server for one end-to-end scenario: every role over a
// fresh PostgreSQL database, a billing clock the test moves, and an
// organization signed up through the API with an admin session and a
// full-access API key. Requests go through the public API only.
type scenario struct {
	t     *testing.T
	srv   *app.App
	base  string
	clock *billingtest.Clock
	pool  *pgxpool.Pool
	logs  *lockedBuffer
	stop  func()
	// environ is the server's environment, for a second replica.
	environ []string

	// key is the organization's API key.
	key string
	// session and org are the admin's session and organization, for the
	// RPCs API keys may not call (integrations, dunning campaigns).
	session, org string
}

// scenarioOptions configure [newScenario].
type scenarioOptions struct {
	// environ is added to the server's environment.
	environ []string
	// stripeURL is the Stripe API base URL (a fake Stripe server).
	stripeURL string
	// selfAPIURL sets TANVO_API_URL to the server's own address, which
	// serves the file links.
	selfAPIURL bool
}

// scenarioSecret is the installation secret of the scenario servers.
var scenarioSecret = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))

// newScenario starts a scenario server at the billing instant at. The
// periodic schedule is off: the test triggers the clock jobs it needs
// with [scenario.trigger].
func newScenario(t *testing.T, at time.Time, opts scenarioOptions) *scenario {
	t.Helper()
	dsn := startPostgres(t)
	port, err := dockertest.FreePort()
	if err != nil {
		t.Fatal(err)
	}
	s := &scenario{t: t, clock: billingtest.NewClock(at), logs: &lockedBuffer{}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server logs:\n%s", s.logs.String())
		}
	})
	environ := append([]string{
		"TANVO_DATABASE_URL=" + dsn,
		"TANVO_HTTP_ADDR=127.0.0.1:" + port,
		"TANVO_MIGRATE_ON_START=false",
		"TANVO_SHUTDOWN_TIMEOUT=10s",
		"TANVO_LOG_FORMAT=text",
		"TANVO_SECRET_KEY=" + scenarioSecret,
		"TANVO_APP_URL=https://app.tanvo.test",
		"OTEL_SDK_DISABLED=true",
	}, opts.environ...)
	if opts.selfAPIURL {
		environ = append(environ, "TANVO_API_URL=http://127.0.0.1:"+port)
	}
	migrate(t, dsn, environ, s.logs)
	s.environ = environ
	s.srv, s.base, s.stop = serveEnv(t, environ, s.logs, cli.Env{Clock: s.clock, StripeBaseURL: opts.stripeURL, ManualClockJobs: true})
	t.Cleanup(s.stop)
	if s.pool, err = pgxpool.New(t.Context(), dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.pool.Close)

	// The organization, through the API: a sign-up, then an API key.
	up := s.do(http.MethodPost, "/v1/auth:sign_up", "", "", map[string]any{
		"email": "admin@acme.test", "password": "correct-horse-battery-staple", "organization_name": "Acme",
	})
	s.session, s.org = up.str("session.token"), up.str("organization.organization_id")
	key := s.do(http.MethodPost, "/v1/api_keys", s.session, s.org, map[string]any{"name": "scenario"})
	s.key = key.str("api_key.secret")
	if s.session == "" || s.org == "" || s.key == "" {
		t.Fatalf("sign-up: got session %q, organization %q, key %q, want all three", s.session, s.org, s.key)
	}
	return s
}

// at moves the billing clock to t.
func (s *scenario) at(t time.Time) { s.clock.Set(t) }

// api sends a REST request with the API key and returns its JSON body,
// failing the test unless the status is 200.
func (s *scenario) api(method, path string, body any) jsonValue {
	s.t.Helper()
	return s.do(method, path, s.key, "", body)
}

// admin is [scenario.api] with the admin's session.
func (s *scenario) admin(method, path string, body any) jsonValue {
	s.t.Helper()
	return s.do(method, path, s.session, s.org, body)
}

// do sends a REST request authenticated with token (and the organization
// header when org is set) and returns its JSON body, failing the test
// unless the status is 200.
func (s *scenario) do(method, path, token, org string, body any) jsonValue {
	s.t.Helper()
	status, out := s.send(method, path, token, org, body)
	if status != http.StatusOK {
		s.t.Fatalf("%s %s: got status = %d (%s), want = 200", method, path, status, out)
	}
	var v any
	if len(out) > 0 {
		if err := json.Unmarshal(out, &v); err != nil {
			s.t.Fatalf("%s %s: decode %s: %v", method, path, out, err)
		}
	}
	return jsonValue{v: v}
}

// send sends a REST request and returns its status and body.
func (s *scenario) send(method, path, token, org string, body any) (int, []byte) {
	s.t.Helper()
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			s.t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(s.t.Context(), method, s.base+path, r)
	if err != nil {
		s.t.Fatal(err)
	}
	if r != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if org != "" {
		req.Header.Set(rpc.OrganizationHeader, org)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return resp.StatusCode, out
}

// event ingests one event through the REST API at the billing clock.
func (s *scenario) event(sub, code, transactionID string, properties map[string]any) {
	s.t.Helper()
	ev := map[string]any{
		"transaction_id": transactionID, "external_subscription_id": sub, "code": code,
		"timestamp": s.clock.Now().Format(time.RFC3339),
	}
	if properties != nil {
		ev["properties"] = properties
	}
	s.api(http.MethodPost, "/v1/events", ev)
}

// trigger runs the clock job kind once at the billing clock's instant.
func (s *scenario) trigger(kind string) {
	s.t.Helper()
	if err := s.srv.Jobs().Trigger(s.t.Context(), kind); err != nil {
		s.t.Fatalf("Trigger(%s) error = %v", kind, err)
	}
}

// idleHorizon is how many seconds ahead [scenario.idle] runs delayed jobs: up to
// the latest run of a subscription activity signalled now, the end of
// the next activity interval plus its commit grace (kernel.ActivityKey).
var idleHorizon = (kernel.ActivityInterval + kernel.ActivityCommitGrace).Seconds()

// idle waits until no job is left to run: every job enqueued so far,
// and every job they enqueued, has completed, been discarded or been
// cancelled. Jobs scheduled within [idleHorizon] run at once; later ones
// are left out.
func (s *scenario) idle() {
	s.t.Helper()
	err := dockertest.Wait(s.t.Context(), 2*time.Minute, func(ctx context.Context) error {
		// The delayed jobs due within the horizon (a subscription's
		// activity of the current interval) run now, as lago's
		// perform_all_enqueued_jobs runs them.
		if _, err := s.pool.Exec(ctx, `UPDATE river.river_job SET state = 'available', scheduled_at = now()
			WHERE state = 'scheduled' AND scheduled_at < now() + $1 * interval '1 second'`, idleHorizon); err != nil {
			return err
		}
		var left []string
		if err := s.pool.QueryRow(ctx, `SELECT coalesce(array_agg(kind || ' ' || state::text || ' ' || scheduled_at::text), '{}') FROM river.river_job
			WHERE state IN ('available', 'running', 'retryable', 'pending') OR (state = 'scheduled' AND scheduled_at < now() + $1 * interval '1 second')`, idleHorizon).Scan(&left); err != nil {
			return err
		}
		if len(left) > 0 {
			return fmt.Errorf("jobs left: %v", left)
		}
		return nil
	})
	if err != nil {
		s.t.Fatalf("jobs: %v", err)
	}
}

// failedJobs returns the kinds and errors of the discarded jobs and of
// the jobs that failed an attempt.
func (s *scenario) failedJobs() []string {
	s.t.Helper()
	rows, err := s.pool.Query(s.t.Context(), `SELECT kind, state, errors::text FROM river.river_job
		WHERE state = 'discarded' OR jsonb_array_length(coalesce(to_jsonb(errors), '[]'::jsonb)) > 0 ORDER BY id`)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, state string
		var errs *string
		if err := rows.Scan(&kind, &state, &errs); err != nil {
			s.t.Fatal(err)
		}
		e := ""
		if errs != nil {
			e = *errs
		}
		out = append(out, kind+" ("+state+"): "+e)
	}
	return out
}

// noFailedJobs fails the test when a job failed.
func (s *scenario) noFailedJobs() {
	s.t.Helper()
	if f := s.failedJobs(); len(f) > 0 {
		s.t.Errorf("failed jobs: got = %v, want = none", f)
	}
}

// invoices lists the customer's invoices, oldest first.
func (s *scenario) invoices(customer string) []jsonValue {
	s.t.Helper()
	out := s.api(http.MethodGet, "/v1/customers/external_id:"+customer+"/invoices?page_size=100", nil).list("invoices")
	// IDs are UUIDv7: their order is the creation order.
	slices.SortFunc(out, func(a, b jsonValue) int { return strings.Compare(a.str("id"), b.str("id")) })
	return out
}

// waitInvoices waits until the customer has n invoices, all past
// generating, and returns them oldest first.
func (s *scenario) waitInvoices(customer string, n int) []jsonValue {
	s.t.Helper()
	var got []jsonValue
	err := dockertest.Wait(s.t.Context(), time.Minute, func(context.Context) error {
		got = s.invoices(customer)
		if len(got) < n {
			return fmt.Errorf("%d invoices, want %d", len(got), n)
		}
		for _, inv := range got {
			if st := inv.str("status"); st == "INVOICE_STATUS_GENERATING" {
				return fmt.Errorf("invoice %s generating", inv.str("id"))
			}
		}
		return nil
	})
	if err != nil {
		s.t.Fatalf("invoices of %s: %v", customer, err)
	}
	return got
}

// webhookTypes returns the types of the organization's domain events
// (the webhooks), in order.
func (s *scenario) webhookTypes() []string {
	s.t.Helper()
	rows, err := s.pool.Query(s.t.Context(), "SELECT event_type FROM domain_events WHERE organization_id = $1 ORDER BY occurred_at, id", s.org)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

// jsonValue is a decoded JSON value with path accessors: "a.b.0.c" walks
// object keys and array indexes.
type jsonValue struct{ v any }

func (j jsonValue) get(path string) any {
	cur := j.v
	if path == "" {
		return cur
	}
	for part := range strings.SplitSeq(path, ".") {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(c) {
				return nil
			}
			cur = c[i]
		default:
			return nil
		}
	}
	return cur
}

// str returns the value at path as a string ("" when absent; numbers and
// booleans formatted).
func (j jsonValue) str(path string) string {
	switch v := j.get(path).(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// at returns the value at path.
func (j jsonValue) at(path string) jsonValue { return jsonValue{v: j.get(path)} }

// list returns the array at path.
func (j jsonValue) list(path string) []jsonValue {
	a, _ := j.get(path).([]any)
	out := make([]jsonValue, len(a))
	for i, v := range a {
		out[i] = jsonValue{v: v}
	}
	return out
}

// String implements fmt.Stringer.
func (j jsonValue) String() string {
	b, _ := json.Marshal(j.v)
	return string(b)
}

// cents returns the Money at path in minor units of a two-decimal
// currency ("12.34" is 1234); -1 when absent.
func (j jsonValue) cents(path string) int64 {
	a := j.str(path + ".amount")
	if a == "" {
		return -1
	}
	neg := strings.HasPrefix(a, "-")
	a = strings.TrimPrefix(a, "-")
	whole, frac, _ := strings.Cut(a, ".")
	frac = (frac + "00")[:2]
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return -1
	}
	if neg {
		n = -n
	}
	return n
}
