package rpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// InboundWebhookPattern is the route of the payment provider webhooks
// (architecture §4): the provider and the receiving connection.
const InboundWebhookPattern = "POST /webhooks/{provider}/{connection_id}"

// InboundWebhookReceiver verifies and stores a provider webhook;
// [webhooks.Inbound] implements it.
type InboundWebhookReceiver interface {
	// Receive verifies, stores and enqueues a delivery; a delivery without
	// an event to handle returns nil and is not stored.
	Receive(ctx context.Context, rcv webhooks.Received) (*payment.InboundWebhook, error)
}

// WebhookAcknowledger gives the body a provider expects in answer to its
// webhooks; [payments.Service] implements it.
type WebhookAcknowledger interface {
	// WebhookAcknowledgement returns the answer body of provider, or false
	// for an empty one.
	WebhookAcknowledgement(provider integration.Provider) (contentType string, body []byte, ok bool)
}

// InboundWebhooks is the HTTP handler of [InboundWebhookPattern] (lago
// WebhooksController, app/controllers/webhooks_controller.rb): the
// connection is looked up in every organization, the delivery verified by
// its provider and stored for processing.
//
//   - an unknown provider or connection answers 404;
//   - a delivery whose signature or payload does not verify answers 400;
//   - a stored delivery, or one without an event to handle, answers 200,
//     with the provider's expected body (Adyen's "[accepted]");
//   - any other failure answers 500, for the provider to deliver again.
type InboundWebhooks struct {
	// Tx looks up the connection's organization.
	Tx port.Transactor
	// Receiver verifies and stores the deliveries.
	Receiver InboundWebhookReceiver
	// Acks gives the providers' expected answers; nil answers empty.
	Acks WebhookAcknowledger
	// Log reports the failures.
	Log *slog.Logger
}

// ServeHTTP implements [http.Handler].
func (h *InboundWebhooks) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	provider := integration.Provider(r.PathValue("provider"))
	if c, ok := integration.CategoryOf(provider); !ok || c != integration.CategoryPayment {
		http.NotFound(w, r)
		return
	}
	connID, err := core.ParseID[core.IntegrationID](r.PathValue("connection_id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var org core.OrganizationID
	err = h.Tx.InTx(ctx, func(ctx context.Context, repos port.Repos) error {
		var err error
		org, err = repos.Integrations.OrganizationOf(ctx, connID)
		return err
	})
	if errors.Is(err, core.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.fail(ctx, w, provider, err)
		return
	}
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	_, err = h.Receiver.Receive(ctx, webhooks.Received{
		OrganizationID: org, Source: string(provider), IntegrationID: connID, Payload: payload, Header: r.Header.Clone(),
	})
	switch {
	case errors.Is(err, payments.ErrInvalidWebhook):
		h.Log.InfoContext(ctx, "webhooks: invalid provider webhook", "organization_id", org.String(),
			"integration_id", connID.String(), "provider", string(provider), "error", err)
		http.Error(w, "invalid webhook", http.StatusBadRequest)
		return
	case errors.Is(err, core.ErrNotFound), errors.Is(err, webhooks.ErrUnknownSource):
		http.NotFound(w, r)
		return
	case err != nil:
		h.fail(ctx, w, provider, err)
		return
	}
	if h.Acks != nil {
		if ct, body, ok := h.Acks.WebhookAcknowledgement(provider); ok {
			w.Header().Set("Content-Type", ct)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *InboundWebhooks) fail(ctx context.Context, w http.ResponseWriter, provider integration.Provider, err error) {
	h.Log.ErrorContext(ctx, "webhooks: provider webhook not stored", "provider", string(provider), "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
