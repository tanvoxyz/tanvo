package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	eventv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/event/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/event/v1/eventv1connect"
)

// keyProbe is a KeyAuthenticator counting the calls of each method.
type keyProbe struct {
	key *access.APIKey

	mu             sync.Mutex
	direct, cached int
}

func (p *keyProbe) Authenticate(context.Context, string) (*access.APIKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.direct++
	return p.key, nil
}

func (p *keyProbe) AuthenticateCached(context.Context, string) (*access.APIKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cached++
	return p.key, nil
}

// TestCachedKeyProcedures checks that the event ingestion and instant fee
// estimate RPCs read their API key through the cache and the other RPCs
// do not (lago ACTIONS_WITH_CACHED_API_KEY,
// app/controllers/api/v1/events_controller.rb:10).
func TestCachedKeyProcedures(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	probe := &keyProbe{key: &access.APIKey{OrganizationID: s.Org.ID}}
	server := connect.NewServer(rpc.Auth(rpc.Authenticators{Keys: probe, Authz: auth.NewAuthorizer(s.Store)}))
	eventv1connect.RegisterEventServiceHandler(server, eventv1connect.UnimplementedEventServiceHandler{})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hc := &http.Client{Transport: bearer{token: "secret"}}
	client := eventv1connect.NewEventServiceClient(connect.NewClient(connecthttp.NewTransport(hc, srv.URL)))

	tests := []struct {
		name   string
		call   func(ctx context.Context) error
		cached bool
	}{
		{"IngestEvent", func(ctx context.Context) error {
			_, err := client.IngestEvent(ctx, &eventv1.IngestEventRequest{})
			return err
		}, true},
		{"IngestEvents", func(ctx context.Context) error {
			_, err := client.IngestEvents(ctx, &eventv1.IngestEventsRequest{})
			return err
		}, true},
		{"EstimateInstantFees", func(ctx context.Context) error {
			_, err := client.EstimateInstantFees(ctx, &eventv1.EstimateInstantFeesRequest{})
			return err
		}, true},
		{"EstimateFees", func(ctx context.Context) error {
			_, err := client.EstimateFees(ctx, &eventv1.EstimateFeesRequest{})
			return err
		}, false},
		{"ListEvents", func(ctx context.Context) error {
			_, err := client.ListEvents(ctx, &eventv1.ListEventsRequest{})
			return err
		}, false},
	}
	for _, tt := range tests {
		probe.mu.Lock()
		probe.direct, probe.cached = 0, 0
		probe.mu.Unlock()
		// The handlers are unimplemented: the call passes authentication
		// and authorization, then fails in the handler.
		if err := tt.call(t.Context()); connect.CodeOf(err) != connect.CodeUnimplemented {
			t.Fatalf("%s got = %v, want = Unimplemented", tt.name, err)
		}
		want := [2]int{1, 0}
		if tt.cached {
			want = [2]int{0, 1}
		}
		if got := [2]int{probe.direct, probe.cached}; got != want {
			t.Errorf("%s (direct, cached) got = %v, want = %v", tt.name, got, want)
		}
	}
}
