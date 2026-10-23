package rpc_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	webhookv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/webhook/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/webhook/v1/webhookv1connect"
)

// restEndpoint is the JSON of a webhook endpoint in a REST response.
type restEndpoint struct {
	ID         string   `json:"id"`
	URL        string   `json:"url"`
	Name       string   `json:"name"`
	EventTypes []string `json:"event_types"`
	Secret     string   `json:"secret"`
}

// allowPrivate lets the tests use plain-HTTP endpoints as lago's specs do.
func allowPrivate(o *app.APIOptions) { o.WebhooksAllowPrivate = true }

// endpointBody decodes the webhook endpoint of a REST response.
func endpointBody(t *testing.T, body []byte) restEndpoint {
	t.Helper()
	return decode[struct {
		WebhookEndpoint restEndpoint `json:"webhook_endpoint"`
	}](t, body).WebhookEndpoint
}

func webhooksClient(s *rpctest.Server, c rpctest.Credential) webhookv1connect.WebhookServiceClient {
	return webhookv1connect.NewWebhookServiceClient(s.Connect(c))
}

func createEndpoint(t *testing.T, s *rpctest.Server, body string) restEndpoint {
	t.Helper()
	status, out := s.REST(t, s.APIKey, http.MethodPost, "/v1/webhook_endpoints", body)
	if status != http.StatusOK {
		t.Fatalf("create: got status = %d (%s), want = 200", status, out)
	}
	return endpointBody(t, out)
}

func TestCreateWebhookEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		body   string
		status int
		check  func(t *testing.T, e restEndpoint)
	}{
		{
			name:   "valid",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:28",
			body:   `{"url":"http://foo.bar/hook","name":"Test Webhook","event_types":["customer.created","customer.updated"]}`,
			status: http.StatusOK,
			check: func(t *testing.T, e restEndpoint) {
				if e.URL != "http://foo.bar/hook" || e.Name != "Test Webhook" || !slices.Equal(e.EventTypes, []string{"customer.created", "customer.updated"}) {
					t.Errorf("got %+v, want the created attributes", e)
				}
				if !strings.HasPrefix(e.Secret, "whsec_") {
					t.Errorf("got secret = %q, want a whsec_ secret", e.Secret)
				}
			},
		},
		{
			name:   "no event types",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:78",
			body:   `{"url":"http://foo.bar/hook"}`,
			status: http.StatusOK,
			check: func(t *testing.T, e restEndpoint) {
				if len(e.EventTypes) != 0 {
					t.Errorf("got event_types = %v, want none", e.EventTypes)
				}
			},
		},
		{
			name:   "event types not an array",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:42",
			body:   `{"url":"http://foo.bar/hook","event_types":"wrong"}`,
			status: http.StatusBadRequest,
		},
		{
			name:   "invalid event types",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:60",
			body:   `{"url":"http://foo.bar/hook","event_types":["wrong.type"]}`,
			status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t, allowPrivate)
			status, body := s.REST(t, s.APIKey, http.MethodPost, "/v1/webhook_endpoints", tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, endpointBody(t, body))
			}
		})
	}
}

func TestCreateWebhookEndpointRules(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	t.Run("private url", func(t *testing.T) {
		t.Log("source: app/models/webhook_endpoint.rb:26")
		_, err := webhooksClient(s, s.APIKey).CreateWebhookEndpoint(t.Context(), webhookv1.CreateWebhookEndpointRequest_builder{
			WebhookEndpoint: webhookv1.WebhookEndpoint_builder{Url: new("https://127.0.0.1/hook")}.Build(),
		}.Build())
		v := rpc.ErrorViolations(err)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || len(v) != 1 || v[0].Field != "webhook_endpoint.url" || v[0].Reason != webhook.CodeInvalidURL {
			t.Errorf("got %v (%+v), want = InvalidArgument on webhook_endpoint.url", err, v)
		}
	})
	t.Run("duplicate url", func(t *testing.T) {
		t.Log("source: app/models/webhook_endpoint.rb:27")
		c := webhooksClient(s, s.APIKey)
		req := webhookv1.CreateWebhookEndpointRequest_builder{
			WebhookEndpoint: webhookv1.WebhookEndpoint_builder{Url: new("https://hooks.example.com/a")}.Build(),
		}.Build()
		if _, err := c.CreateWebhookEndpoint(t.Context(), req); err != nil {
			t.Fatal(err)
		}
		_, err := c.CreateWebhookEndpoint(t.Context(), req)
		v := rpc.ErrorViolations(err)
		if connect.CodeOf(err) != connect.CodeInvalidArgument || len(v) != 1 || v[0].Reason != core.CodeAlreadyExists {
			t.Errorf("got %v (%+v), want = value_already_exist on webhook_endpoint.url", err, v)
		}
	})
}

func TestGetWebhookEndpoint(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t, allowPrivate)
	e := createEndpoint(t, s, `{"url":"http://foo.bar/hook"}`)
	tests := []struct {
		name   string
		source string
		id     string
		status int
	}{
		{"existing", "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:129", e.ID, http.StatusOK},
		{"not existing", "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:140", uuid.NewString(), http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/webhook_endpoints/"+tt.id, nil)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if status == http.StatusOK {
				if got := endpointBody(t, body); got.ID != e.ID || got.Secret != "" {
					t.Errorf("got id = %q, secret = %q, want = %q without a secret", got.ID, got.Secret, e.ID)
				}
			}
		})
	}
}

func TestListWebhookEndpoints(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/requests/api/v1/webhook_endpoints_controller_spec.rb:110")
	s := rpctest.New(t, allowPrivate)
	for _, u := range []string{"http://c.test/h", "http://a.test/h", "http://b.test/h"} {
		createEndpoint(t, s, `{"url":"`+u+`"}`)
	}
	status, body := s.REST(t, s.APIKey, http.MethodGet, "/v1/webhook_endpoints", nil)
	if status != http.StatusOK {
		t.Fatalf("got status = %d (%s), want = 200", status, body)
	}
	got := decode[struct {
		WebhookEndpoints []restEndpoint `json:"webhook_endpoints"`
	}](t, body)
	var urls []string
	for _, e := range got.WebhookEndpoints {
		urls = append(urls, e.URL)
	}
	if want := []string{"http://a.test/h", "http://b.test/h", "http://c.test/h"}; !slices.Equal(urls, want) {
		t.Errorf("got %v, want = %v", urls, want)
	}
}

func TestUpdateWebhookEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		query  string
		body   string
		status int
		check  func(t *testing.T, e restEndpoint)
	}{
		{
			name:   "all parameters",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:220",
			body:   `{"url":"http://foo.bar/new","name":"Updated","event_types":["invoice.created"]}`,
			status: http.StatusOK,
			check: func(t *testing.T, e restEndpoint) {
				if e.URL != "http://foo.bar/new" || e.Name != "Updated" || !slices.Equal(e.EventTypes, []string{"invoice.created"}) {
					t.Errorf("got %+v, want the updated attributes", e)
				}
			},
		},
		{
			name:   "event types cleared",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:236",
			query:  "?update_mask=eventTypes",
			body:   `{"event_types":[]}`,
			status: http.StatusOK,
			check: func(t *testing.T, e restEndpoint) {
				if len(e.EventTypes) != 0 {
					t.Errorf("got event_types = %v, want none", e.EventTypes)
				}
			},
		},
		{
			name:   "only the url",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:300",
			body:   `{"url":"http://foo.bar"}`,
			status: http.StatusOK,
			check: func(t *testing.T, e restEndpoint) {
				if e.URL != "http://foo.bar" || e.Name != "Original" || !slices.Equal(e.EventTypes, []string{"customer.created"}) {
					t.Errorf("got %+v, want only the url changed", e)
				}
			},
		},
		{
			name:   "invalid event types",
			source: "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:272",
			body:   `{"event_types":["wrong.type"]}`,
			status: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			s := rpctest.New(t, allowPrivate)
			e := createEndpoint(t, s, `{"url":"http://foo.bar/hook","name":"Original","event_types":["customer.created"]}`)
			status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/webhook_endpoints/"+e.ID+tt.query, tt.body)
			if status != tt.status {
				t.Fatalf("got status = %d (%s), want = %d", status, body, tt.status)
			}
			if tt.check != nil {
				tt.check(t, endpointBody(t, body))
			}
		})
	}
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/requests/api/v1/webhook_endpoints_controller_spec.rb:314")
		s := rpctest.New(t, allowPrivate)
		status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/webhook_endpoints/"+uuid.NewString(), `{"url":"http://foo.bar"}`)
		if status != http.StatusNotFound {
			t.Errorf("got status = %d (%s), want = 404", status, body)
		}
	})
}

func TestDeleteWebhookEndpoint(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t, allowPrivate)
	e := createEndpoint(t, s, `{"url":"http://foo.bar/hook"}`)
	t.Run("existing", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/webhook_endpoints_controller_spec.rb:160")
		if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/webhook_endpoints/"+e.ID, nil); status != http.StatusOK {
			t.Fatalf("got status = %d (%s), want = 200", status, body)
		}
		var n int
		if err := s.Admin.QueryRow(t.Context(), "SELECT count(*) FROM webhook_endpoints WHERE id = $1", e.ID).Scan(&n); err != nil || n != 0 {
			t.Errorf("got count = %d (%v), want = 0", n, err)
		}
		t.Log("source: spec/services/webhook_endpoints/destroy_service_spec.rb:19")
		var logs []*auditlog.SecurityLog
		if err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
			p, err := r.SecurityLogs.List(ctx, s.Org.ID, auditlog.SecurityLogFilter{Types: []string{"webhook_endpoint"}}, core.PageRequest{})
			logs = p.Items
			return err
		}); err != nil {
			t.Fatal(err)
		}
		events := map[string]any{}
		for _, l := range logs {
			events[l.Event] = l.Resources["webhook_url"]
		}
		if len(logs) != 2 || events["webhook_endpoint.created"] != "http://foo.bar/hook" || events["webhook_endpoint.deleted"] != "http://foo.bar/hook" {
			t.Errorf("security logs got = %+v, want = webhook_endpoint.created and .deleted of http://foo.bar/hook", logs)
		}
	})
	t.Run("not existing", func(t *testing.T) {
		t.Log("source: spec/requests/api/v1/webhook_endpoints_controller_spec.rb:183")
		if status, body := s.REST(t, s.APIKey, http.MethodDelete, "/v1/webhook_endpoints/"+e.ID, nil); status != http.StatusNotFound {
			t.Errorf("got status = %d (%s), want = 404", status, body)
		}
	})
}

func TestListWebhookEventTypes(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/graphql/resolvers/event_types_resolver_spec.rb")
	s := rpctest.New(t)
	res, err := webhooksClient(s, s.Session).ListWebhookEventTypes(t.Context(), webhookv1.ListWebhookEventTypesRequest_builder{}.Build())
	if err != nil {
		t.Fatal(err)
	}
	types := res.GetEventTypes()
	if len(types) == 0 || types[0].GetName() == "" || types[0].GetCategory() == "" || types[0].GetDescription() == "" {
		t.Fatalf("got %v, want described event types", types)
	}
	if slices.ContainsFunc(types, func(e *webhookv1.WebhookEventType) bool { return e.GetName() == "event.error" }) {
		t.Errorf("got the deprecated event.error, want it left out")
	}
}

func TestWebhookDeliveries(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t, allowPrivate)
	e := createEndpoint(t, s, `{"url":"http://foo.bar/hook"}`)
	endpoint, _ := core.ParseID[core.WebhookEndpointID](e.ID)
	now := s.Clock.Now()
	mk := func(status webhook.Status, t webhook.EventType, ago int) *webhook.Delivery {
		at := now.Add(-time.Duration(ago) * time.Minute)
		return &webhook.Delivery{
			ID: core.NewID[core.WebhookID](core.UUIDv7Generator{}), OrganizationID: s.Org.ID, EndpointID: endpoint,
			EventID: core.NewID[core.AuditEventID](core.UUIDv7Generator{}), EventType: t, ObjectType: "customer",
			ObjectID: uuid.New(), Payload: []byte(`{}`), Status: status, Attempts: 1, CreatedAt: at, UpdatedAt: at,
		}
	}
	failed := mk(webhook.StatusFailed, webhook.EventTypeCustomerCreated, 3)
	succeeded := mk(webhook.StatusSucceeded, "invoice.created", 2)
	pending := mk(webhook.StatusPending, webhook.EventTypeCustomerCreated, 1)
	timedOut := mk(webhook.StatusFailed, webhook.EventTypeCustomerCreated, 4)
	failed.HTTPStatus, succeeded.HTTPStatus = new(500), new(200)
	err := s.Store.InTx(t.Context(), func(ctx context.Context, r port.Repos) error {
		for _, d := range []*webhook.Delivery{failed, succeeded, pending, timedOut} {
			if err := r.WebhookDeliveries.Create(ctx, d); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := webhooksClient(s, s.APIKey)
	list := func(t *testing.T, filter, search string) []string {
		t.Helper()
		res, err := c.ListWebhookDeliveries(t.Context(), webhookv1.ListWebhookDeliveriesRequest_builder{
			WebhookEndpointId: new(e.ID), Filter: new(filter), Search: new(search),
		}.Build())
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, d := range res.GetDeliveries() {
			ids = append(ids, d.GetId())
		}
		return ids
	}
	t.Run("list", func(t *testing.T) {
		tests := []struct {
			name, source, filter, search string
			want                         []*webhook.Delivery
		}{
			{"recent first", "app/queries/webhooks_query.rb:10", "", "", []*webhook.Delivery{pending, succeeded, failed, timedOut}},
			{"status", "spec/queries/webhooks_query_spec.rb (statuses)", `status = DELIVERY_STATUS_FAILED`, "", []*webhook.Delivery{failed, timedOut}},
			{"http status code", "spec/queries/webhooks_query_spec.rb:136", `http_status = "200"`, "", []*webhook.Delivery{succeeded}},
			{"http status class", "spec/queries/webhooks_query_spec.rb:146", `http_status = "5xx"`, "", []*webhook.Delivery{failed}},
			{"http status range", "spec/queries/webhooks_query_spec.rb:156", `http_status = "200-205"`, "", []*webhook.Delivery{succeeded}},
			{"http status timeout", "spec/queries/webhooks_query_spec.rb:167", `http_status = "TIMEOUT"`, "", []*webhook.Delivery{timedOut}},
			{"http statuses", "app/queries/webhooks_query.rb:62-76", `http_status = "5xx" OR http_status = "timeout"`, "", []*webhook.Delivery{failed, timedOut}},
			{
				"updated range", "app/queries/webhooks_query.rb:54-59",
				`updated_at >= "` + now.Add(-150*time.Second).Format(time.RFC3339) + `" AND updated_at <= "` + now.Add(-90*time.Second).Format(time.RFC3339) + `"`,
				"",
				[]*webhook.Delivery{succeeded},
			},
			{
				"empty updated range", "spec/queries/webhooks_query_spec.rb:188",
				`updated_at >= "` + now.Add(-2*time.Hour).Format(time.RFC3339) + `" AND updated_at <= "` + now.Add(-time.Hour).Format(time.RFC3339) + `"`,
				"", nil,
			},
			{"event type", "spec/queries/webhooks_query_spec.rb (event_types)", `event_type = "invoice.created"`, "", []*webhook.Delivery{succeeded}},
			{"search", "spec/queries/webhooks_query_spec.rb (search_term)", "", pending.ObjectID.String()[:8], []*webhook.Delivery{pending}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Logf("source: %s", tt.source)
				var want []string
				for _, d := range tt.want {
					want = append(want, d.ID.String())
				}
				if got := list(t, tt.filter, tt.search); !slices.Equal(got, want) {
					t.Errorf("got %v, want = %v", got, want)
				}
			})
		}
	})
	t.Run("invalid filter", func(t *testing.T) {
		for _, tt := range []struct{ source, filter string }{
			{"spec/queries/webhooks_query_spec.rb:178", `http_status = "invalid_status"`},
			{"spec/queries/webhooks_query_spec.rb:201", `updated_at >= "invalid_date"`},
		} {
			t.Logf("source: %s", tt.source)
			_, err := c.ListWebhookDeliveries(t.Context(), webhookv1.ListWebhookDeliveriesRequest_builder{
				WebhookEndpointId: new(e.ID), Filter: new(tt.filter),
			}.Build())
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Errorf("%s: got %v, want = InvalidArgument", tt.filter, err)
			}
		}
	})
	t.Run("get", func(t *testing.T) {
		t.Log("source: spec/graphql/resolvers/webhook_resolver_spec.rb")
		res, err := c.GetWebhookDelivery(t.Context(), webhookv1.GetWebhookDeliveryRequest_builder{
			WebhookEndpointId: new(e.ID), DeliveryId: new(failed.ID.String()),
		}.Build())
		if err != nil {
			t.Fatal(err)
		}
		if d := res.GetDelivery(); d.GetStatus() != webhookv1.DeliveryStatus_DELIVERY_STATUS_FAILED || d.GetObjectId() != failed.ObjectID.String() {
			t.Errorf("got %v, want the failed delivery", d)
		}
		_, err = c.GetWebhookDelivery(t.Context(), webhookv1.GetWebhookDeliveryRequest_builder{
			WebhookEndpointId: new(uuid.NewString()), DeliveryId: new(failed.ID.String()),
		}.Build())
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("got %v from another endpoint, want = NotFound", err)
		}
	})
	t.Run("retry", func(t *testing.T) {
		retry := func(d *webhook.Delivery) (*webhookv1.RetryWebhookDeliveryResponse, error) {
			return c.RetryWebhookDelivery(t.Context(), webhookv1.RetryWebhookDeliveryRequest_builder{
				WebhookEndpointId: new(e.ID), DeliveryId: new(d.ID.String()),
			}.Build())
		}
		t.Log("source: spec/graphql/mutations/webhooks/retry_spec.rb")
		res, err := retry(failed)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.GetDelivery().GetStatus(); got != webhookv1.DeliveryStatus_DELIVERY_STATUS_PENDING {
			t.Errorf("got status = %v, want = DELIVERY_STATUS_PENDING", got)
		}
		t.Log("source: spec/services/webhooks/retry_service_spec.rb:31")
		_, err = retry(succeeded)
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || rpc.ErrorReason(err) != "is_succeeded" {
			t.Errorf("got %v (%s), want = FailedPrecondition is_succeeded", err, rpc.ErrorReason(err))
		}
	})
}

// TestWebhookEndpointPermissions ports the "requires API permission"
// examples (spec/support/shared_examples/api_requirements.rb:17) of
// webhook_endpoints_controller_spec.rb.
func TestWebhookEndpointPermissions(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t, allowPrivate)
	e := createEndpoint(t, s, `{"url":"http://foo.bar/hook"}`)
	d := auth.Deps{Tx: s.Store, Clock: s.Clock, IDs: core.UUIDv7Generator{}, Rand: rand.Reader}
	keyWith := func(r permission.APIResource, level permission.Level) rpctest.Credential {
		k, err := auth.NewAPIKeys(d, nil).Create(t.Context(), s.Org.ID, nil, map[permission.APIResource]permission.Level{r: level})
		if err != nil {
			t.Fatal(err)
		}
		return rpctest.Credential{Token: k.Secret}
	}
	read := keyWith(permission.APIResourceWebhookEndpoint, permission.LevelRead)
	write := keyWith(permission.APIResourceWebhookEndpoint, permission.LevelWrite)
	other := keyWith(permission.APIResourcePlan, permission.LevelWrite)
	n := 0
	create := func(c rpctest.Credential) error {
		n++
		_, err := webhooksClient(s, c).CreateWebhookEndpoint(t.Context(), webhookv1.CreateWebhookEndpointRequest_builder{
			WebhookEndpoint: webhookv1.WebhookEndpoint_builder{Url: new("http://foo.bar/" + string(rune('a'+n)))}.Build(),
		}.Build())
		return err
	}
	get := func(c rpctest.Credential) error {
		_, err := webhooksClient(s, c).GetWebhookEndpoint(t.Context(),
			webhookv1.GetWebhookEndpointRequest_builder{WebhookEndpointId: new(e.ID)}.Build())
		return err
	}
	tests := []struct {
		name   string
		source string
		call   func(rpctest.Credential) error
		cred   rpctest.Credential
		code   connect.Code
		reason string
	}{
		{"write key creates", "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:26", create, write, 0, ""},
		{"read key cannot create", "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:26", create, read, connect.CodePermissionDenied, "write_action_not_allowed_for_webhook_endpoint"},
		{"read key reads", "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:127", get, read, 0, ""},
		{"other resource key cannot read", "spec/requests/api/v1/webhook_endpoints_controller_spec.rb:127", get, other, connect.CodePermissionDenied, "read_action_not_allowed_for_webhook_endpoint"},
		{"admin session", "", get, s.Session, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.source != "" {
				t.Logf("source: %s", tt.source)
			}
			err := tt.call(tt.cred)
			if tt.code == 0 {
				if err != nil {
					t.Fatalf("got %v, want = nil", err)
				}
				return
			}
			if got := connect.CodeOf(err); got != tt.code {
				t.Fatalf("got = %v (%v), want = %v", got, err, tt.code)
			}
			if got := rpc.ErrorReason(err); got != tt.reason {
				t.Errorf("got reason = %q, want = %q", got, tt.reason)
			}
		})
	}
}
