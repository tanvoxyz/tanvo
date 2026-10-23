package webhooks

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

func newEndpoints(t *testing.T) (*Endpoints, *memstore.Store, core.OrganizationID) {
	t.Helper()
	s := memstore.New()
	c := &clock{now: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	return &Endpoints{Tx: s, Clock: c, IDs: ids, Rand: rand.Reader, AllowPrivate: true}, s, core.NewID[core.OrganizationID](ids)
}

func fieldCodes(err error) map[string]string {
	ve, ok := errors.AsType[*core.ValidationError](err)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, fe := range ve.Errors {
		out[fe.Field] = fe.Code
	}
	return out
}

func TestEndpointsCreate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		in     EndpointInput
		want   func(t *testing.T, e *webhook.Endpoint)
		codes  map[string]string
	}{
		{
			name:   "creates",
			source: "spec/services/webhook_endpoints/create_service_spec.rb:22",
			in:     EndpointInput{URL: "http://foo.bar", Name: new("Test Webhook"), EventTypes: []string{"customer.created"}},
			want: func(t *testing.T, e *webhook.Endpoint) {
				if e.URL != "http://foo.bar" || *e.Name != "Test Webhook" || !slices.Equal(e.EventTypes, []webhook.EventType{"customer.created"}) {
					t.Errorf("got %+v, want the created attributes", e)
				}
				if !webhook.ValidSecret(e.Secret) {
					t.Errorf("got secret = %q, want a whsec_ secret", e.Secret)
				}
			},
		},
		{
			name:   "partial params",
			source: "spec/services/webhook_endpoints/create_service_spec.rb:34",
			in:     EndpointInput{URL: "http://foo.bar"},
			want: func(t *testing.T, e *webhook.Endpoint) {
				if e.Name != nil || e.EventTypes != nil {
					t.Errorf("got name = %v, event_types = %v, want both nil", e.Name, e.EventTypes)
				}
			},
		},
		{
			name:   "star subscribes to every type",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:76",
			in:     EndpointInput{URL: "http://foo.bar", EventTypes: []string{"*"}},
			want: func(t *testing.T, e *webhook.Endpoint) {
				if e.EventTypes != nil {
					t.Errorf("got event_types = %v, want nil", e.EventTypes)
				}
			},
		},
		{
			name:   "invalid url",
			source: "spec/services/webhook_endpoints/create_service_spec.rb:55",
			in:     EndpointInput{URL: "foobar"},
			codes:  map[string]string{"url": webhook.CodeInvalidURL},
		},
		{
			name:   "invalid event types",
			source: "spec/services/webhook_endpoints/create_service_spec.rb:68",
			in:     EndpointInput{URL: "http://foo.bar", EventTypes: []string{"invalid.event"}},
			codes:  map[string]string{"event_types": webhook.CodeInvalidTypes},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			svc, _, org := newEndpoints(t)
			e, err := svc.Create(t.Context(), org, tt.in)
			if tt.codes != nil {
				if got := fieldCodes(err); fmt.Sprint(got) != fmt.Sprint(tt.codes) {
					t.Fatalf("got errors = %v (%v), want = %v", got, err, tt.codes)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() = %v", err)
			}
			tt.want(t, e)
		})
	}
}

func TestEndpointsCreateRules(t *testing.T) {
	t.Parallel()
	t.Run("duplicate url", func(t *testing.T) {
		t.Parallel()
		t.Log("source: app/models/webhook_endpoint.rb:27")
		svc, _, org := newEndpoints(t)
		if _, err := svc.Create(t.Context(), org, EndpointInput{URL: "https://a.test"}); err != nil {
			t.Fatal(err)
		}
		_, err := svc.Create(t.Context(), org, EndpointInput{URL: "https://a.test"})
		if got := fieldCodes(err)["url"]; got != core.CodeAlreadyExists {
			t.Errorf("got url = %q (%v), want = %q", got, err, core.CodeAlreadyExists)
		}
	})
	t.Run("limit", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/models/webhook_endpoint_spec.rb (max_webhook_endpoints)")
		svc, _, org := newEndpoints(t)
		for i := range webhook.MaxEndpoints {
			if _, err := svc.Create(t.Context(), org, EndpointInput{URL: fmt.Sprintf("https://%d.test", i)}); err != nil {
				t.Fatal(err)
			}
		}
		_, err := svc.Create(t.Context(), org, EndpointInput{URL: "https://more.test"})
		if re, ok := errors.AsType[*core.RuleError](err); !ok || re.Code != webhook.CodeExceededLimit {
			t.Errorf("got %v, want rule %s", err, webhook.CodeExceededLimit)
		}
	})
	t.Run("private address", func(t *testing.T) {
		t.Parallel()
		t.Log("source: app/models/webhook_endpoint.rb:26 (block_private_addresses)")
		svc, _, org := newEndpoints(t)
		svc.AllowPrivate = false
		for _, u := range []string{"https://127.0.0.1/hook", "http://example.test"} {
			_, err := svc.Create(t.Context(), org, EndpointInput{URL: u})
			if got := fieldCodes(err)["url"]; got != webhook.CodeInvalidURL {
				t.Errorf("Create(%s): got url = %q (%v), want = %q", u, got, err, webhook.CodeInvalidURL)
			}
		}
	})
}

func TestEndpointsUpdate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		in     EndpointUpdate
		want   func(t *testing.T, e *webhook.Endpoint)
		codes  map[string]string
	}{
		{
			name:   "updates",
			source: "spec/services/webhook_endpoints/update_service_spec.rb:22",
			in: EndpointUpdate{
				URL: core.Some("http://foo.bar"), Name: core.Some(new("Updated Webhook")),
				EventTypes: core.Some([]string{"customer.updated"}),
			},
			want: func(t *testing.T, e *webhook.Endpoint) {
				if e.URL != "http://foo.bar" || *e.Name != "Updated Webhook" || !slices.Equal(e.EventTypes, []webhook.EventType{"customer.updated"}) {
					t.Errorf("got %+v, want the updated attributes", e)
				}
			},
		},
		{
			name:   "partial params",
			source: "spec/services/webhook_endpoints/update_service_spec.rb:40",
			in:     EndpointUpdate{URL: core.Some("http://foo.bar")},
			want: func(t *testing.T, e *webhook.Endpoint) {
				if *e.Name != "Original Webhook" || !slices.Equal(e.EventTypes, []webhook.EventType{"customer.created"}) {
					t.Errorf("got %+v, want the original name and event types", e)
				}
			},
		},
		{
			name:   "event types to null",
			source: "spec/services/webhook_endpoints/update_service_spec.rb:109",
			in:     EndpointUpdate{EventTypes: core.Some[[]string](nil)},
			want: func(t *testing.T, e *webhook.Endpoint) {
				if e.EventTypes != nil {
					t.Errorf("got event_types = %v, want nil", e.EventTypes)
				}
			},
		},
		{
			name:   "event types to empty",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:240",
			in:     EndpointUpdate{EventTypes: core.Some([]string{})},
			want: func(t *testing.T, e *webhook.Endpoint) {
				if e.EventTypes == nil || len(e.EventTypes) != 0 {
					t.Errorf("got event_types = %#v, want []", e.EventTypes)
				}
			},
		},
		{
			name:   "invalid url",
			source: "spec/services/webhook_endpoints/update_service_spec.rb:79",
			in:     EndpointUpdate{URL: core.Some("foobar")},
			codes:  map[string]string{"url": webhook.CodeInvalidURL},
		},
		{
			name:   "invalid event types",
			source: "spec/services/webhook_endpoints/update_service_spec.rb:98",
			in:     EndpointUpdate{EventTypes: core.Some([]string{"invalid.event"})},
			codes:  map[string]string{"event_types": webhook.CodeInvalidTypes},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			svc, _, org := newEndpoints(t)
			e, err := svc.Create(t.Context(), org, EndpointInput{URL: "https://original.test", Name: new("Original Webhook"), EventTypes: []string{"customer.created"}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := svc.Update(t.Context(), org, e.ID, tt.in)
			if tt.codes != nil {
				if c := fieldCodes(err); fmt.Sprint(c) != fmt.Sprint(tt.codes) {
					t.Fatalf("got errors = %v (%v), want = %v", c, err, tt.codes)
				}
				return
			}
			if err != nil {
				t.Fatalf("Update() = %v", err)
			}
			tt.want(t, got)
			if got.Secret != e.Secret {
				t.Errorf("got secret changed, want it kept")
			}
		})
	}
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/webhook_endpoints/update_service_spec.rb:60")
		svc, _, org := newEndpoints(t)
		_, err := svc.Update(t.Context(), org, core.NewID[core.WebhookEndpointID](ids), EndpointUpdate{})
		if nf, ok := errors.AsType[*core.NotFoundError](err); !ok || nf.Resource != ResourceEndpoint {
			t.Errorf("got %v, want webhook_endpoint_not_found", err)
		}
	})
}

func TestEndpointsDelete(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/services/webhook_endpoints/destroy_service_spec.rb:14")
	svc, _, org := newEndpoints(t)
	e, err := svc.Create(t.Context(), org, EndpointInput{URL: "https://a.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Delete(t.Context(), org, e.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if _, err := svc.Get(t.Context(), org, e.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("got Get() = %v, want not found", err)
	}
	if _, err := svc.Delete(t.Context(), org, e.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("got Delete() again = %v, want not found", err)
	}
}

func TestEndpointsList(t *testing.T) {
	t.Parallel()
	t.Log("source: app/queries/webhook_endpoints_query.rb:9-12")
	svc, _, org := newEndpoints(t)
	for _, u := range []string{"https://c.test", "https://a.test", "https://b.test"} {
		if _, err := svc.Create(t.Context(), org, EndpointInput{URL: u}); err != nil {
			t.Fatal(err)
		}
	}
	var urls []string
	page := core.PageRequest{Size: 2}
	for {
		p, err := svc.List(t.Context(), org, page)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Items {
			urls = append(urls, e.URL)
		}
		if p.NextToken == "" {
			break
		}
		page.Token = p.NextToken
	}
	if want := []string{"https://a.test", "https://b.test", "https://c.test"}; !slices.Equal(urls, want) {
		t.Errorf("got %v, want = %v", urls, want)
	}
}

func TestEndpointsRetry(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, status webhook.Status) (*Endpoints, *memstore.Store, core.OrganizationID, *webhook.Endpoint, *webhook.Delivery) {
		t.Helper()
		svc, s, org := newEndpoints(t)
		e, err := svc.Create(t.Context(), org, EndpointInput{URL: "https://a.test"})
		if err != nil {
			t.Fatal(err)
		}
		now := svc.Clock.Now()
		d := &webhook.Delivery{
			ID: core.NewID[core.WebhookID](ids), OrganizationID: org, EndpointID: e.ID, EventID: core.NewID[core.AuditEventID](ids),
			EventType: webhook.EventTypeCustomerCreated, ObjectType: "customer", ObjectID: uuid.New(), Payload: []byte(`{}`),
			Status: status, Attempts: webhook.MaxAttempts, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.InTx(t.Context(), func(ctx context.Context, r port.Repos) error { return r.WebhookDeliveries.Create(ctx, d) }); err != nil {
			t.Fatal(err)
		}
		return svc, s, org, e, d
	}
	t.Run("failed", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/webhooks/retry_service_spec.rb:10")
		svc, s, org, e, d := setup(t, webhook.StatusFailed)
		got, err := svc.Retry(t.Context(), org, e.ID, d.ID)
		if err != nil {
			t.Fatalf("Retry() = %v", err)
		}
		if got.ID != d.ID || got.Status != webhook.StatusPending {
			t.Errorf("got %s %s, want %s pending", got.ID, got.Status, d.ID)
		}
		jobs := s.Jobs()
		if !slices.ContainsFunc(jobs, func(j port.Job) bool { return j.Kind == KindDeliver }) {
			t.Errorf("got jobs = %v, want a %s job", jobs, KindDeliver)
		}
	})
	t.Run("succeeded", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/webhooks/retry_service_spec.rb:31")
		svc, _, org, e, d := setup(t, webhook.StatusSucceeded)
		_, err := svc.Retry(t.Context(), org, e.ID, d.ID)
		if re, ok := errors.AsType[*core.RuleError](err); !ok || re.Code != CodeIsSucceeded {
			t.Errorf("got %v, want rule %s", err, CodeIsSucceeded)
		}
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/webhooks/retry_service_spec.rb:22")
		svc, _, org, _, d := setup(t, webhook.StatusFailed)
		_, err := svc.Retry(t.Context(), org, core.NewID[core.WebhookEndpointID](ids), d.ID)
		if nf, ok := errors.AsType[*core.NotFoundError](err); !ok || nf.Resource != ResourceDelivery {
			t.Errorf("got %v, want webhook_not_found", err)
		}
	})
}

// securityLog collects the recorded endpoint changes.
type securityLog struct{ entries []securityEntry }

type securityEntry struct {
	event     string
	resources map[string]any
}

func (l *securityLog) RecordEndpoint(_ context.Context, _ core.OrganizationID, event string, resources map[string]any) error {
	l.entries = append(l.entries, securityEntry{event, resources})
	return nil
}

func TestEndpointsSecurityLog(t *testing.T) {
	t.Parallel()
	svc, _, org := newEndpoints(t)
	log := &securityLog{}
	svc.SecurityLog = log
	ctx := context.Background()

	// source: app/services/webhook_endpoints/create_service.rb:37-42
	e, err := svc.Create(ctx, org, EndpointInput{URL: "http://foo.bar"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// source: spec/services/webhook_endpoints/update_service_spec.rb:53
	if _, err := svc.Update(ctx, org, e.ID, EndpointUpdate{URL: core.Some("http://foo.baz")}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if _, err := svc.Update(ctx, org, e.ID, EndpointUpdate{Name: core.Some(new("n"))}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	// source: spec/services/webhook_endpoints/update_service_spec.rb:67
	if _, err := svc.Update(ctx, org, core.NewID[core.WebhookEndpointID](ids), EndpointUpdate{}); err == nil {
		t.Fatal("Update(unknown) error = nil, want not found")
	}
	// source: spec/services/webhook_endpoints/update_service_spec.rb:86
	if _, err := svc.Update(ctx, org, e.ID, EndpointUpdate{URL: core.Some("foobar")}); err == nil {
		t.Fatal("Update(foobar) error = nil, want a validation error")
	}
	// source: spec/services/webhook_endpoints/destroy_service_spec.rb:19
	if _, err := svc.Delete(ctx, org, e.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	// source: spec/services/webhook_endpoints/destroy_service_spec.rb:34
	if _, err := svc.Delete(ctx, org, e.ID); err == nil {
		t.Fatal("Delete(deleted) error = nil, want not found")
	}

	want := []securityEntry{
		{EventEndpointCreated, map[string]any{"webhook_url": "http://foo.bar"}},
		{EventEndpointUpdated, map[string]any{"webhook_url": map[string]any{"deleted": "http://foo.bar", "added": "http://foo.baz"}}},
		{EventEndpointUpdated, map[string]any{"webhook_url": "http://foo.baz"}},
		{EventEndpointDeleted, map[string]any{"webhook_url": "http://foo.baz"}},
	}
	if got := fmt.Sprint(log.entries); got != fmt.Sprint(want) {
		t.Errorf("got entries = %s, want = %s", got, fmt.Sprint(want))
	}
}
