package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

var errBadSignature = errors.New("bad signature")

type fakeSource struct {
	handled int
	fail    error
}

func (s *fakeSource) Verify(_ context.Context, _ core.OrganizationID, _ core.IntegrationID, payload []byte, sig string) (string, error) {
	if sig != "good" {
		return "", errBadSignature
	}
	var p struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(payload, &p)
	return p.Type, nil
}

func (s *fakeSource) Handle(context.Context, *payment.InboundWebhook) error {
	s.handled++
	return s.fail
}

func newInbound(t *testing.T) (*Inbound, *memstore.Store, *clock, *fakeSource) {
	t.Helper()
	s := memstore.New()
	c := &clock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	src := &fakeSource{}
	return &Inbound{Tx: s, Sources: map[string]Source{"stripe": src}, Clock: c, IDs: ids}, s, c, src
}

func newReceived(org core.OrganizationID, source, sig string) Received {
	return Received{OrganizationID: org, Source: source, IntegrationID: core.NewID[core.IntegrationID](ids), Payload: []byte(`{"type":"payment_intent.succeeded"}`), Signature: sig}
}

func processJobs(t *testing.T, in *Inbound, s *memstore.Store) int {
	t.Helper()
	n := 0
	for _, j := range s.TakeJobs() {
		if j.Kind != KindProcessInbound || j.Queue != port.QueuePayments {
			t.Fatalf("job: got = %s on %s", j.Kind, j.Queue)
		}
		if !j.RunAt.IsZero() && j.RunAt.After(in.Clock.Now()) {
			continue
		}
		var a InboundArgs
		_ = json.Unmarshal(j.Args, &a)
		if err := in.Process(context.Background(), j.OrganizationID, a); err != nil {
			t.Fatal(err)
		}
		n++
	}
	return n
}

func (in *Inbound) get(t *testing.T, s *memstore.Store, org core.OrganizationID, id core.InboundWebhookID) *payment.InboundWebhook {
	t.Helper()
	var w *payment.InboundWebhook
	if err := s.InTx(context.Background(), func(ctx context.Context, r port.Repos) error {
		var err error
		w, err = r.InboundWebhooks.Get(ctx, org, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

// source: spec/services/inbound_webhooks/create_service_spec.rb,
// spec/services/inbound_webhooks/validate_payload_service_spec.rb
func TestReceive(t *testing.T) {
	in, s, _, _ := newInbound(t)
	org := core.NewID[core.OrganizationID](ids)
	if _, err := in.Receive(context.Background(), newReceived(org, "stripe", "bad")); !errors.Is(err, errBadSignature) {
		t.Errorf("bad signature: got = %v", err)
	}
	if _, err := in.Receive(context.Background(), newReceived(org, "paypal", "good")); !errors.Is(err, ErrUnknownSource) {
		t.Errorf("unknown source: got = %v", err)
	}
	if got := len(s.Jobs()); got != 0 {
		t.Errorf("jobs after refusals: got = %d, want = 0", got)
	}
	w, err := in.Receive(context.Background(), newReceived(org, "stripe", "good"))
	if err != nil {
		t.Fatal(err)
	}
	stored := in.get(t, s, org, w.ID)
	if stored.Status != payment.InboundWebhookStatusPending || stored.EventType != "payment_intent.succeeded" || *stored.Signature != "good" || stored.IntegrationID != w.IntegrationID {
		t.Errorf("stored: got = %+v", stored)
	}
	if got := len(s.Jobs()); got != 1 {
		t.Errorf("jobs: got = %d, want = 1", got)
	}
}

// source: spec/services/inbound_webhooks/process_service_spec.rb
func TestProcess(t *testing.T) {
	in, s, c, src := newInbound(t)
	org := core.NewID[core.OrganizationID](ids)
	w, err := in.Receive(context.Background(), newReceived(org, "stripe", "good"))
	if err != nil {
		t.Fatal(err)
	}
	src.fail = errors.New("provider down")
	processJobs(t, in, s)
	got := in.get(t, s, org, w.ID)
	if got.Status != payment.InboundWebhookStatusFailed || got.Attempts != 1 || src.handled != 1 {
		t.Fatalf("after failure: got = %s attempts %d handled %d", got.Status, got.Attempts, src.handled)
	}
	// The retry is due 15 minutes after the attempt.
	if n := processJobs(t, in, s); n != 0 {
		t.Errorf("early retry: got = %d runs, want = 0", n)
	}
	c.advance(payment.WebhookRetryInterval)
	src.fail = nil
	if n, err := in.RetryDue(context.Background(), org); err != nil || n != 1 {
		t.Fatalf("RetryDue: got = %d %v, want = 1", n, err)
	}
	processJobs(t, in, s)
	got = in.get(t, s, org, w.ID)
	if got.Status != payment.InboundWebhookStatusSucceeded || got.Attempts != 2 || src.handled != 2 {
		t.Errorf("after retry: got = %s attempts %d handled %d", got.Status, got.Attempts, src.handled)
	}
	// A succeeded webhook is not processed again.
	if err := in.Process(context.Background(), org, InboundArgs{WebhookID: w.ID}); err != nil || src.handled != 2 {
		t.Errorf("succeeded: got = %v handled %d, want = 2", err, src.handled)
	}
	// An unknown webhook is ignored.
	if err := in.Process(context.Background(), org, InboundArgs{WebhookID: core.NewID[core.InboundWebhookID](ids)}); err != nil {
		t.Errorf("unknown: got = %v", err)
	}
}

func TestProcessWindow(t *testing.T) {
	in, s, c, src := newInbound(t)
	org := core.NewID[core.OrganizationID](ids)
	src.fail = errors.New("down")
	w, err := in.Receive(context.Background(), newReceived(org, "stripe", "good"))
	if err != nil {
		t.Fatal(err)
	}
	processJobs(t, in, s)
	// Failures stop being retried after the retry window.
	for range 10 {
		c.advance(payment.WebhookRetryInterval)
		if _, err := in.RetryDue(context.Background(), org); err != nil {
			t.Fatal(err)
		}
		processJobs(t, in, s)
	}
	got := in.get(t, s, org, w.ID)
	if want := int(payment.WebhookRetryWindow/payment.WebhookRetryInterval) + 1; got.Attempts != want {
		t.Errorf("attempts: got = %d, want = %d", got.Attempts, want)
	}
}
