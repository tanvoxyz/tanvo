package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var ids = core.UUIDv7Generator{}

const secret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"

type fixture struct {
	t     *testing.T
	store *memstore.Store
	clock *clock
	org   core.OrganizationID
	disp  *Dispatcher
	emit  *Emitter
	// pending are the jobs whose run time has not come.
	pending []port.Job
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s := memstore.New()
	c := &clock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	return &fixture{
		t: t, store: s, clock: c, org: core.NewID[core.OrganizationID](ids),
		disp: &Dispatcher{Tx: s, Sender: &Sender{Client: NewClient(ClientConfig{AllowPrivate: true, Timeout: 5 * time.Second}), AllowPrivate: true}, Clock: c, Jitter: func() float64 { return 0 }},
		emit: &Emitter{Tx: s},
	}
}

func (f *fixture) endpoint(url string, types []webhook.EventType) *webhook.Endpoint {
	f.t.Helper()
	e := &webhook.Endpoint{ID: core.NewID[core.WebhookEndpointID](ids), OrganizationID: f.org, URL: url, EventTypes: types, Secret: secret, CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now()}
	f.inTx(func(ctx context.Context, r port.Repos) error { return r.WebhookEndpoints.Create(ctx, e) })
	return e
}

func (f *fixture) inTx(fn func(ctx context.Context, r port.Repos) error) {
	f.t.Helper()
	if err := f.store.InTx(context.Background(), fn); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) event(t webhook.EventType) webhook.Event {
	return webhook.Event{
		ID: core.NewID[core.AuditEventID](ids), OrganizationID: f.org, Type: t, ObjectType: "invoice",
		ObjectID: uuid.Must(uuid.NewV7()), Payload: json.RawMessage(`{"id":"inv_1","total_amount_cents":1000}`), OccurredAt: f.clock.Now(),
	}
}

// run executes the jobs enqueued so far (and those they enqueue) whose
// run time has come, and returns the kinds run.
func (f *fixture) run() []string {
	f.t.Helper()
	var kinds []string
	var later []port.Job
	for {
		jobs := f.store.TakeJobs()
		if len(jobs) == 0 {
			break
		}
		for _, j := range jobs {
			if !j.RunAt.IsZero() && j.RunAt.After(f.clock.Now()) {
				later = append(later, j)
				continue
			}
			kinds = append(kinds, j.Kind)
			var err error
			switch j.Kind {
			case KindFanOut:
				var a EventArgs
				_ = json.Unmarshal(j.Args, &a)
				err = f.disp.FanOut(context.Background(), j.OrganizationID, a)
			case KindDeliver:
				var a DeliveryArgs
				_ = json.Unmarshal(j.Args, &a)
				err = f.disp.Deliver(context.Background(), j.OrganizationID, a)
			default:
				f.t.Fatalf("unexpected job %s", j.Kind)
			}
			if err != nil {
				f.t.Fatalf("%s: %v", j.Kind, err)
			}
		}
	}
	f.pending = append(f.pending[:0], later...)
	return kinds
}

func (f *fixture) deliveries(e *webhook.Endpoint) []*webhook.Delivery {
	f.t.Helper()
	var out []*webhook.Delivery
	f.inTx(func(ctx context.Context, r port.Repos) error {
		p, err := r.WebhookDeliveries.ListByEndpoint(ctx, f.org, e.ID, webhook.DeliveryFilter{}, core.PageRequest{})
		out = p.Items
		return err
	})
	return out
}

type received struct {
	header http.Header
	body   string
}

func server(t *testing.T, status int, body string) (*httptest.Server, *[]received) {
	t.Helper()
	var mu sync.Mutex
	var got []received
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, received{r.Header.Clone(), string(b)})
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s, &got
}

// source: spec/services/webhooks/send_http_service_spec.rb:21
func TestDeliverSuccess(t *testing.T) {
	f := newFixture(t)
	srv, got := server(t, http.StatusOK, "ok")
	ep := f.endpoint(srv.URL, nil)
	e := f.event(webhook.EventTypeInvoiceCreated)
	if err := f.emit.Emit(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if kinds := f.run(); strings.Join(kinds, ",") != KindFanOut+","+KindDeliver {
		t.Fatalf("jobs: got = %v", kinds)
	}
	if len(*got) != 1 {
		t.Fatalf("requests: got = %d, want = 1", len(*got))
	}
	req := (*got)[0]
	if req.header.Get("Content-Type") != "application/json" || req.header.Get(HeaderID) != e.ID.String() {
		t.Errorf("headers: got = %v", req.header)
	}
	if err := Verify(secret, req.header, []byte(req.body), f.clock.Now(), 0); err != nil {
		t.Errorf("signature: got = %v, want = nil", err)
	}
	var env Envelope
	if err := json.Unmarshal([]byte(req.body), &env); err != nil || env.Type != webhook.EventTypeInvoiceCreated || env.ObjectType != "invoice" || env.OrganizationID != f.org || string(env.Data) != string(e.Payload) {
		t.Errorf("envelope: got = %s %v", req.body, err)
	}
	ds := f.deliveries(ep)
	if len(ds) != 1 {
		t.Fatalf("deliveries: got = %d, want = 1", len(ds))
	}
	d := ds[0]
	if d.Status != webhook.StatusSucceeded || *d.HTTPStatus != 200 || *d.Response != "ok" || d.Attempts != 1 || d.Payload == nil {
		t.Errorf("delivery: got = %+v", d)
	}
}

// source: spec/services/webhooks/send_http_service_spec.rb:91,98,111
func TestDeliverRetries(t *testing.T) {
	f := newFixture(t)
	srv, got := server(t, http.StatusForbidden, `{"message":"forbidden"}`)
	ep := f.endpoint(srv.URL, nil)
	if err := f.emit.Emit(context.Background(), f.event(webhook.EventTypeInvoiceCreated)); err != nil {
		t.Fatal(err)
	}
	f.run()
	d := f.deliveries(ep)[0]
	if d.Status != webhook.StatusRetrying || *d.HTTPStatus != 403 || d.Attempts != 1 || len(f.pending) != 1 {
		t.Fatalf("after attempt 1: got = %s status %d attempts %d pending jobs %d", d.Status, *d.HTTPStatus, d.Attempts, len(f.pending))
	}
	// Backoff after one failure: 1⁴ + 2 = 3 seconds.
	if want := f.clock.Now().Add(3 * time.Second); !f.pending[0].RunAt.Equal(want) {
		t.Errorf("retry at: got = %v, want = %v", f.pending[0].RunAt, want)
	}
	f.requeue()
	f.clock.advance(3 * time.Second)
	f.run()
	d = f.deliveries(ep)[0]
	if d.Status != webhook.StatusRetrying || d.Attempts != 2 || d.LastAttemptAt == nil {
		t.Fatalf("after attempt 2: got = %s attempts %d", d.Status, d.Attempts)
	}
	// 2⁴ + 2 = 18 seconds.
	f.requeue()
	f.clock.advance(18 * time.Second)
	f.run()
	d = f.deliveries(ep)[0]
	if d.Status != webhook.StatusFailed || d.Attempts != 3 || len(f.pending) != 0 {
		t.Errorf("after attempt 3: got = %s attempts %d pending %d, want = failed 3 0", d.Status, d.Attempts, len(f.pending))
	}
	if len(*got) != 3 {
		t.Errorf("requests: got = %d, want = 3", len(*got))
	}
	// A manual retry is one more attempt (lago Webhooks::RetryService).
	if _, err := f.disp.Retry(context.Background(), f.org, d.ID); err != nil {
		t.Fatal(err)
	}
	f.run()
	d = f.deliveries(ep)[0]
	if d.Status != webhook.StatusFailed || d.Attempts != 4 {
		t.Errorf("after manual retry: got = %s attempts %d, want = failed 4", d.Status, d.Attempts)
	}
}

// requeue puts the delayed jobs back for run.
func (f *fixture) requeue() {
	f.t.Helper()
	jobs := f.pending
	f.pending = nil
	f.inTx(func(ctx context.Context, r port.Repos) error { return r.Jobs.Enqueue(ctx, jobs...) })
}

// source: spec/services/webhooks/send_http_service_spec.rb:166 (connection
// failure), :178 (private address)
func TestDeliverWithoutResponse(t *testing.T) {
	f := newFixture(t)
	srv, _ := server(t, http.StatusOK, "")
	closed := srv.URL
	srv.Close()
	ep := f.endpoint(closed, nil)
	if err := f.emit.Emit(context.Background(), f.event(webhook.EventTypeInvoiceCreated)); err != nil {
		t.Fatal(err)
	}
	f.run()
	d := f.deliveries(ep)[0]
	if d.Status != webhook.StatusRetrying || d.HTTPStatus != nil || *d.Response != responseConnectionFailed {
		t.Errorf("refused: got = %s %v %q", d.Status, d.HTTPStatus, *d.Response)
	}

	// The guarded client refuses the loopback test server.
	g := newFixture(t)
	g.disp.Sender = &Sender{Client: NewClient(ClientConfig{}), AllowPrivate: true}
	live, got := server(t, http.StatusOK, "ok")
	ep = g.endpoint(live.URL, nil)
	if err := g.emit.Emit(context.Background(), g.event(webhook.EventTypeInvoiceCreated)); err != nil {
		t.Fatal(err)
	}
	g.run()
	d = g.deliveries(ep)[0]
	if d.Status != webhook.StatusRetrying || *d.Response != responseBlocked || len(*got) != 0 {
		t.Errorf("blocked: got = %s %q requests %d", d.Status, *d.Response, len(*got))
	}

	// Plain HTTP needs AllowPrivate.
	h := newFixture(t)
	h.disp.Sender = &Sender{Client: NewClient(ClientConfig{AllowPrivate: true})}
	ep = h.endpoint(live.URL, nil)
	if err := h.emit.Emit(context.Background(), h.event(webhook.EventTypeInvoiceCreated)); err != nil {
		t.Fatal(err)
	}
	h.run()
	if d = h.deliveries(ep)[0]; *d.Response != responseHTTPSRequired || len(*got) != 0 {
		t.Errorf("http: got = %q requests %d", *d.Response, len(*got))
	}
}

// source: spec/services/webhooks/send_http_service_spec.rb:128,139,154
func TestDeliverStoredResponse(t *testing.T) {
	big := strings.Repeat("a", webhook.MaxResponseBytes-1) + "é"
	tests := []struct{ name, body, want string }{
		{"truncated on a character boundary", big, strings.Repeat("a", webhook.MaxResponseBytes-1)},
		{"invalid UTF-8 dropped", "ok\xff", "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			srv, _ := server(t, http.StatusOK, tt.body)
			ep := f.endpoint(srv.URL, nil)
			if err := f.emit.Emit(context.Background(), f.event(webhook.EventTypeInvoiceCreated)); err != nil {
				t.Fatal(err)
			}
			f.run()
			if d := f.deliveries(ep)[0]; d.Status != webhook.StatusSucceeded || *d.Response != tt.want {
				t.Errorf("got = %s %d bytes, want = succeeded %d bytes", d.Status, len(*d.Response), len(tt.want))
			}
		})
	}
}

// source: spec/services/webhooks/base_service_spec.rb:16,47,60,72,90,106
func TestFanOut(t *testing.T) {
	f := newFixture(t)
	srv, got := server(t, http.StatusNoContent, "")
	all := f.endpoint(srv.URL+"/all", nil)
	none := f.endpoint(srv.URL+"/none", []webhook.EventType{})
	match := f.endpoint(srv.URL+"/match", []webhook.EventType{webhook.EventTypeInvoiceCreated})
	other := f.endpoint(srv.URL+"/other", []webhook.EventType{webhook.EventTypeCustomerCreated})
	e := f.event(webhook.EventTypeInvoiceCreated)
	if err := f.emit.Emit(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	// The fan-out job running twice creates each delivery once.
	f.inTx(func(ctx context.Context, r port.Repos) error {
		job, _ := newJob(KindFanOut, f.org, EventArgs{EventID: e.ID}, time.Time{}, "again")
		return r.Jobs.Enqueue(ctx, job)
	})
	f.run()
	for _, c := range []struct {
		ep   *webhook.Endpoint
		want int
	}{{all, 1}, {none, 0}, {match, 1}, {other, 0}} {
		ds := f.deliveries(c.ep)
		if len(ds) != c.want {
			t.Errorf("%s: got = %d deliveries, want = %d", c.ep.URL, len(ds), c.want)
			continue
		}
		for _, d := range ds {
			if d.Status != webhook.StatusSucceeded || d.EventType != e.Type || d.ObjectID != e.ObjectID || d.ObjectType != "invoice" {
				t.Errorf("%s: got = %+v", c.ep.URL, d)
			}
		}
	}
	if len(*got) != 2 {
		t.Errorf("requests: got = %d, want = 2", len(*got))
	}
}

// source: spec/services/webhooks/base_service_spec.rb:72 (deleted endpoint)
func TestDeliverDeletedEndpoint(t *testing.T) {
	f := newFixture(t)
	srv, got := server(t, http.StatusOK, "")
	ep := f.endpoint(srv.URL, nil)
	if err := f.emit.Emit(context.Background(), f.event(webhook.EventTypeInvoiceCreated)); err != nil {
		t.Fatal(err)
	}
	// Run the fan-out only, then delete the endpoint before the attempt.
	jobs := f.store.TakeJobs()
	var a EventArgs
	_ = json.Unmarshal(jobs[0].Args, &a)
	if err := f.disp.FanOut(context.Background(), f.org, a); err != nil {
		t.Fatal(err)
	}
	f.inTx(func(ctx context.Context, r port.Repos) error { return r.WebhookEndpoints.Delete(ctx, f.org, ep.ID) })
	f.run()
	if len(*got) != 0 {
		t.Errorf("requests: got = %d, want = 0", len(*got))
	}
}

func TestSweepAndPurge(t *testing.T) {
	f := newFixture(t)
	srv, _ := server(t, http.StatusOK, "")
	ep := f.endpoint(srv.URL, nil)
	if err := f.emit.Emit(context.Background(), f.event(webhook.EventTypeInvoiceCreated)); err != nil {
		t.Fatal(err)
	}
	// Run the fan-out, then lose the delivery job.
	jobs := f.store.TakeJobs()
	var a EventArgs
	_ = json.Unmarshal(jobs[0].Args, &a)
	if err := f.disp.FanOut(context.Background(), f.org, a); err != nil {
		t.Fatal(err)
	}
	f.store.TakeJobs()
	n, err := f.disp.Sweep(context.Background(), 100)
	if err != nil || n != 1 {
		t.Fatalf("Sweep: got = %d %v, want = 1", n, err)
	}
	f.run()
	if d := f.deliveries(ep)[0]; d.Status != webhook.StatusSucceeded {
		t.Errorf("after sweep: got = %s, want = succeeded", d.Status)
	}
	if n, _ := f.disp.Sweep(context.Background(), 100); n != 0 {
		t.Errorf("second sweep: got = %d, want = 0", n)
	}
	f.clock.advance(webhook.Retention + time.Second)
	n, err = f.disp.DispatchPurges(context.Background(), f.clock.Now())
	if err != nil || n != 1 {
		t.Fatalf("DispatchPurges: got = %d %v, want = 1", n, err)
	}
	purges := f.store.TakeJobs()
	if len(purges) != 1 || purges[0].Kind != KindPurge || purges[0].OrganizationID != f.org || purges[0].Queue != port.QueueWebhooks {
		t.Fatalf("purge jobs got = %+v, want = one %s job of %s", purges, KindPurge, f.org)
	}
	var pa PurgeArgs
	if err := json.Unmarshal(purges[0].Args, &pa); err != nil {
		t.Fatal(err)
	}
	if want := f.clock.Now().Add(-webhook.Retention); !pa.Cutoff.Equal(want) {
		t.Errorf("purge cutoff got = %v, want = %v", pa.Cutoff, want)
	}
	if n, err := f.disp.Purge(context.Background(), f.org, pa); err != nil || n != 1 {
		t.Errorf("Purge: got = %d %v, want = 1", n, err)
	}
	if got := f.deliveries(ep); len(got) != 0 {
		t.Errorf("deliveries after purge got = %d, want = 0", len(got))
	}
}

func TestKindMatchesMemstore(t *testing.T) {
	if KindFanOut != memstore.JobDeliverWebhookEvent {
		t.Errorf("got = %s, want = %s", KindFanOut, memstore.JobDeliverWebhookEvent)
	}
}

func TestDeliveryIDDeterministic(t *testing.T) {
	ev, ep := core.NewID[core.AuditEventID](ids), core.NewID[core.WebhookEndpointID](ids)
	a, b := deliveryID(ev, ep), deliveryID(ev, ep)
	if a != b || a.UUID().Version() != 7 || a == deliveryID(ev, core.NewID[core.WebhookEndpointID](ids)) {
		t.Errorf("got = %s %s", a, b)
	}
	if a.UUID()[0] != ev.UUID()[0] || a.UUID()[5] != ev.UUID()[5] {
		t.Errorf("timestamp: got = %s, want prefix of %s", a, ev)
	}
}
