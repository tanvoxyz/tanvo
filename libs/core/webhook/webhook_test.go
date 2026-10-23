package webhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

var testNow = time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func testSecret(t *testing.T) string {
	t.Helper()
	s, err := NewSecret(bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEndpoint(t *testing.T) {
	type tc struct {
		Name       string   `yaml:"name"`
		Source     string   `yaml:"source"`
		URL        string   `yaml:"url"`
		EventTypes []string `yaml:"event_types"`
		Normalized any      `yaml:"normalized"`
		Errors     []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "endpoint.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			e := &Endpoint{URL: c.URL, EventTypes: NormalizeEventTypes(c.EventTypes), Secret: testSecret(t)}
			if c.Normalized == "all" {
				if e.EventTypes != nil {
					t.Errorf("EventTypes = %v, want = nil", e.EventTypes)
				}
			} else {
				want := []string{}
				for _, s := range c.Normalized.([]any) {
					want = append(want, s.(string))
				}
				got := []string{}
				for _, et := range e.EventTypes {
					got = append(got, string(et))
				}
				if e.EventTypes == nil || !slices.Equal(got, want) {
					t.Errorf("EventTypes = %#v, want = %v", e.EventTypes, want)
				}
			}
			if got := fieldErrors(e.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() = %v, want = %v", got, c.Errors)
			}
		})
	}
}

func TestSubscribes(t *testing.T) {
	all := &Endpoint{}
	none := &Endpoint{EventTypes: []EventType{}}
	some := &Endpoint{EventTypes: []EventType{EventTypeInvoiceCreated}}
	for _, c := range []struct {
		e    *Endpoint
		t    EventType
		want bool
	}{
		{all, EventTypeAlertTriggered, true},
		{none, EventTypeAlertTriggered, false},
		{some, EventTypeInvoiceCreated, true},
		{some, EventTypeAlertTriggered, false},
	} {
		if got := c.e.Subscribes(c.t); got != c.want {
			t.Errorf("Subscribes(%s) with %v = %v, want = %v", c.t, c.e.EventTypes, got, c.want)
		}
	}
}

func TestSecret(t *testing.T) {
	s := testSecret(t)
	if !strings.HasPrefix(s, SecretPrefix) || !ValidSecret(s) {
		t.Errorf("NewSecret() = %q, want = a valid whsec_ secret", s)
	}
	for _, bad := range []string{"", "whsec_", "whsec_!!!", "sk_" + s[6:], "whsec_" + "AAAA"} {
		if ValidSecret(bad) {
			t.Errorf("ValidSecret(%q) = true, want = false", bad)
		}
	}
}

func TestCheckEndpointLimit(t *testing.T) {
	if err := CheckEndpointLimit(MaxEndpoints - 1); err != nil {
		t.Errorf("CheckEndpointLimit(9) = %v, want = nil", err)
	}
	err := CheckEndpointLimit(MaxEndpoints)
	if re, ok := errors.AsType[*core.RuleError](err); !ok || re.Code != CodeExceededLimit {
		t.Errorf("CheckEndpointLimit(10) = %v, want = %s", err, CodeExceededLimit)
	}
}

func TestDeliveryTransitionMatrix(t *testing.T) {
	allowed := map[[2]Status]bool{
		{StatusPending, StatusRetrying}: true, {StatusPending, StatusSucceeded}: true, {StatusPending, StatusFailed}: true,
		{StatusRetrying, StatusRetrying}: true, {StatusRetrying, StatusSucceeded}: true, {StatusRetrying, StatusFailed}: true,
		{StatusRetrying, StatusPending}: true, {StatusFailed, StatusPending}: true,
	}
	for _, from := range StatusValues() {
		for _, to := range StatusValues() {
			if got := CanTransition(from, to); got != allowed[[2]Status{from, to}] {
				t.Errorf("CanTransition(%s, %s) = %v, want = %v", from, to, got, !got)
			}
		}
	}
}

func newTestDelivery() *Delivery {
	e := &Event{
		ID:         core.MustParseID[core.AuditEventID]("0190a0a0-0000-7000-8000-000000000001"),
		Type:       EventTypeInvoiceCreated,
		ObjectType: "invoice",
		ObjectID:   uuid.MustParse("0190a0a0-0000-7000-8000-000000000002"),
		Payload:    json.RawMessage(`{"invoice":{}}`),
		OccurredAt: testNow,
	}
	return NewDelivery(core.MustParseID[core.WebhookID]("0190a0a0-0000-7000-8000-000000000003"),
		core.MustParseID[core.WebhookEndpointID]("0190a0a0-0000-7000-8000-000000000004"), e, testNow)
}

func TestDeliveryAttempts(t *testing.T) {
	d := newTestDelivery()
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want = nil", err)
	}
	if !d.Due(testNow) {
		t.Error("Due(now) = false, want = true for a new delivery")
	}
	code := 500
	for i, want := range []Status{StatusRetrying, StatusRetrying, StatusFailed} {
		if err := d.Fail(&code, "boom", 0, testNow); err != nil {
			t.Fatalf("attempt %d: Fail() = %v", i+1, err)
		}
		if d.Status != want || d.Attempts != i+1 {
			t.Errorf("attempt %d: status = %s, attempts = %d, want = %s, %d", i+1, d.Status, d.Attempts, want, i+1)
		}
	}
	if d.NextAttemptAt != nil || d.Due(testNow.Add(time.Hour)) {
		t.Errorf("NextAttemptAt = %v, want = nil once failed", d.NextAttemptAt)
	}
	if err := d.Retry(testNow); err != nil || d.Status != StatusPending {
		t.Fatalf("Retry() = %v, status = %s, want = nil, pending", err, d.Status)
	}
	if err := d.Fail(nil, "Connection failed", 0, testNow); err != nil || d.Status != StatusFailed {
		t.Errorf("Fail() after manual retry: %v, status = %s, want = failed", err, d.Status)
	}
	_ = d.Retry(testNow)
	if err := d.Succeed(200, []byte("ok"), testNow); err != nil || d.Status != StatusSucceeded || *d.HTTPStatus != 200 || *d.Response != "ok" {
		t.Errorf("Succeed() = %v, status = %s", err, d.Status)
	}
	if err := d.Retry(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Retry() of a succeeded delivery = %v, want = ErrInvalidTransition", err)
	}
}

func TestDeliveryRetryDue(t *testing.T) {
	d := newTestDelivery()
	_ = d.Fail(nil, "x", 0, testNow)
	if want := testNow.Add(3 * time.Second); !d.NextAttemptAt.Equal(want) {
		t.Errorf("NextAttemptAt = %v, want = %v", d.NextAttemptAt, want)
	}
	if d.Due(testNow.Add(2*time.Second)) || !d.Due(testNow.Add(3*time.Second)) {
		t.Error("Due() does not follow NextAttemptAt")
	}
}

func TestBackoff(t *testing.T) {
	for _, c := range []struct {
		attempts int
		jitter   float64
		want     time.Duration
	}{
		{0, 0, 2 * time.Second},
		{1, 0, 3 * time.Second},
		{2, 0, 18 * time.Second},
		{2, 1, 20400 * time.Millisecond},
		{3, 0, 83 * time.Second},
		{2, 5, 20400 * time.Millisecond},
		{-1, 0, 2 * time.Second},
	} {
		if got := Backoff(c.attempts, c.jitter); got != c.want {
			t.Errorf("Backoff(%d, %v) = %v, want = %v", c.attempts, c.jitter, got, c.want)
		}
	}
}

func TestTruncateResponse(t *testing.T) {
	if got := TruncateResponse([]byte("ok\xffok")); got != "okok" {
		t.Errorf("TruncateResponse(invalid) = %q, want = %q", got, "okok")
	}
	long := strings.Repeat("é", MaxResponseBytes)
	got := TruncateResponse([]byte(long))
	if len(got) > MaxResponseBytes || !utf8.ValidString(got) || len(got) < MaxResponseBytes-1 {
		t.Errorf("len(TruncateResponse(long)) = %d, want = ≤ %d valid UTF-8", len(got), MaxResponseBytes)
	}
}

func TestEventValidate(t *testing.T) {
	e := newTestDelivery()
	ev := &Event{Type: e.EventType, ObjectType: "invoice", ObjectID: e.ObjectID, Payload: e.Payload, OccurredAt: testNow}
	if err := ev.Validate(); err != nil {
		t.Errorf("Validate() = %v, want = nil", err)
	}
	ev = &Event{Type: "nope", Payload: json.RawMessage(`{`)}
	want := []string{"type: invalid_value", "object_type: value_is_mandatory", "object_id: value_is_mandatory", "payload: invalid_format", "occurred_at: value_is_mandatory"}
	if got := fieldErrors(ev.Validate()); !slices.Equal(got, want) {
		t.Errorf("Validate() = %v, want = %v", got, want)
	}
}
