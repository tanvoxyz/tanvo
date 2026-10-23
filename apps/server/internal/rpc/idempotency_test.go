package rpc_test

import (
	"net/http"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core/idempotency"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// keyTransport sets the Idempotency-Key header of the requests.
type keyTransport struct {
	key  string
	next http.RoundTripper
}

func (k keyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(rpc.IdempotencyHeader, k.key)
	return k.next.RoundTrip(r)
}

func TestIdempotency(t *testing.T) {
	t.Parallel()
	s := rpctest.New(t)
	ctx := t.Context()
	create := func(key, code string) (*catalogv1.CreateBillableMetricResponse, error) {
		c := metricsClient(s, s.APIKey)
		if key != "" {
			hc := s.HTTPClient(s.APIKey)
			hc.Transport = keyTransport{key: key, next: hc.Transport}
			c = catalogv1connect.NewBillableMetricServiceClient(connect.NewClient(connecthttp.NewTransport(hc, s.URL)))
		}
		return c.CreateBillableMetric(ctx, catalogv1.CreateBillableMetricRequest_builder{
			BillableMetric: catalogv1.BillableMetric_builder{
				Code: new(code), Name: new(code), AggregationType: catalogv1.AggregationType_AGGREGATION_TYPE_COUNT.Enum(),
			}.Build(),
		}.Build())
	}

	first, err := create("k1", "bm")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	replay, err := create("k1", "bm")
	if err != nil {
		t.Fatalf("replay: got %v, want the stored response", err)
	}
	if got, want := replay.GetBillableMetric().GetId(), first.GetBillableMetric().GetId(); got != want {
		t.Errorf("got replayed id = %s, want = %s", got, want)
	}
	if _, err := create("k1", "other"); rpc.ErrorReason(err) != rpc.ReasonIdempotencyKeyReused {
		t.Errorf("got %v, want = %s", err, rpc.ReasonIdempotencyKeyReused)
	}
	// Without the key, the same request runs again and fails on the code.
	if _, err := create("", "bm"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("got %v, want = InvalidArgument for the duplicate code", err)
	}
	// A failed request releases its key.
	if _, err := create("k2", "bm"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("got %v, want = InvalidArgument for the duplicate code", err)
	}
	if _, err := create("k2", "fresh"); err != nil {
		t.Errorf("got %v after a failed request, want = nil", err)
	}

	long := make([]byte, idempotency.MaxKeyLen+1)
	for i := range long {
		long[i] = 'k'
	}
	if _, err := create(string(long), "long"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("got %v, want = InvalidArgument for a long key", err)
	}
}
