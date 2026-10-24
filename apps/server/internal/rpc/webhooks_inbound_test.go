package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/memstore"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// fakeReceiver records the deliveries and answers with err.
type fakeReceiver struct {
	err  error
	got  []webhooks.Received
	stub *payment.InboundWebhook
}

func (f *fakeReceiver) Receive(_ context.Context, rcv webhooks.Received) (*payment.InboundWebhook, error) {
	f.got = append(f.got, rcv)
	return f.stub, f.err
}

// fakeAcks answers Adyen deliveries with "[accepted]".
type fakeAcks struct{}

func (fakeAcks) WebhookAcknowledgement(p integration.Provider) (string, []byte, bool) {
	if p != integration.ProviderAdyen {
		return "", nil, false
	}
	return "text/plain", []byte("[accepted]"), true
}

func TestInboundWebhooks(t *testing.T) {
	ids := core.UUIDv7Generator{}
	now := time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)
	st := memstore.New()
	org := core.NewID[core.OrganizationID](ids)
	connect := func(p integration.Provider) core.IntegrationID {
		i := &integration.Integration{
			ID: core.NewID[core.IntegrationID](ids), OrganizationID: org, Code: string(p), Name: string(p),
			Category: integration.CategoryPayment, Provider: p, Settings: []byte("{}"), CreatedAt: now, UpdatedAt: now,
		}
		if err := st.InTx(context.Background(), func(ctx context.Context, r port.Repos) error {
			return r.Integrations.Create(ctx, i)
		}); err != nil {
			t.Fatal(err)
		}
		return i.ID
	}
	stripeConn, adyenConn := connect(integration.ProviderStripe), connect(integration.ProviderAdyen)
	tests := []struct {
		name     string
		path     string
		err      error
		want     int
		wantBody string
		stored   bool
	}{
		{name: "stored", path: fmt.Sprintf("/webhooks/stripe/%s", stripeConn), want: http.StatusOK, stored: true},
		{name: "adyen acknowledgement", path: fmt.Sprintf("/webhooks/adyen/%s", adyenConn), want: http.StatusOK, wantBody: "[accepted]", stored: true},
		{name: "not a payment provider", path: fmt.Sprintf("/webhooks/hubspot/%s", stripeConn), want: http.StatusNotFound},
		{name: "bad connection id", path: "/webhooks/stripe/nope", want: http.StatusNotFound},
		{name: "unknown connection", path: fmt.Sprintf("/webhooks/stripe/%s", core.NewID[core.IntegrationID](ids)), want: http.StatusNotFound},
		{
			name: "invalid signature", path: fmt.Sprintf("/webhooks/stripe/%s", stripeConn),
			err: fmt.Errorf("verify: %w", payments.ErrInvalidWebhook), want: http.StatusBadRequest, stored: true,
		},
		{
			name: "storage failure", path: fmt.Sprintf("/webhooks/stripe/%s", stripeConn),
			err: errors.New("database down"), want: http.StatusInternalServerError, stored: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rcv := &fakeReceiver{err: tt.err}
			h := &InboundWebhooks{Tx: st, Receiver: rcv, Acks: fakeAcks{}, Log: slog.New(slog.DiscardHandler)}
			mux := http.NewServeMux()
			mux.Handle(InboundWebhookPattern, h)
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{"id":"evt_1"}`))
			req.Header.Set("Stripe-Signature", "t=1,v1=abc")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Fatalf("status: got = %d, want = %d", w.Code, tt.want)
			}
			if body, _ := io.ReadAll(w.Body); tt.wantBody != "" && string(body) != tt.wantBody {
				t.Errorf("body: got = %q, want = %q", body, tt.wantBody)
			}
			if got := len(rcv.got) == 1; got != tt.stored {
				t.Fatalf("received: got = %t, want = %t", got, tt.stored)
			}
			if tt.stored {
				if got := rcv.got[0]; got.OrganizationID != org || string(got.Payload) != `{"id":"evt_1"}` ||
					got.Header.Get("Stripe-Signature") != "t=1,v1=abc" {
					t.Errorf("delivery: got = %+v, want = the organization, payload and headers", got)
				}
			}
		})
	}
}
