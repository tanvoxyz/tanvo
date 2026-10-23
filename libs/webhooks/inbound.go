package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// KindProcessInbound processes a stored provider webhook; its arguments
// are [InboundArgs].
const KindProcessInbound = "webhook.process_inbound"

// InboundArgs are the arguments of a [KindProcessInbound] job.
type InboundArgs struct {
	// WebhookID is the stored webhook.
	WebhookID core.InboundWebhookID `json:"webhook_id"`
}

// ErrUnknownSource is returned for a webhook of a source without a
// registered [Source].
var ErrUnknownSource = errors.New("webhooks: unknown webhook source")

// Source verifies and handles the webhooks of one provider; providers/*
// implement it.
type Source interface {
	// Verify checks the signature of a webhook received by a provider
	// connection and returns its event type. A failure is reported to the
	// sender and nothing is stored.
	Verify(ctx context.Context, org core.OrganizationID, conn core.IntegrationID, payload []byte, signature string) (eventType string, err error)
	// Handle applies a stored webhook. An error marks it failed; it is
	// retried per [payment.InboundWebhook.NextRetry].
	Handle(ctx context.Context, w *payment.InboundWebhook) error
}

// Received is a provider webhook as it reached the HTTP handler.
type Received struct {
	// OrganizationID is the organization of the connection.
	OrganizationID core.OrganizationID
	// Source is the provider ("stripe", …).
	Source string
	// IntegrationID is the receiving connection.
	IntegrationID core.IntegrationID
	// Code is the connection's code, if any.
	Code *string
	// Payload is the raw body.
	Payload []byte
	// Signature is the provider signature header.
	Signature string
}

// Inbound receives and processes provider webhooks.
type Inbound struct {
	// Tx is the transaction boundary.
	Tx port.Transactor
	// Sources maps a provider name to its handler.
	Sources map[string]Source
	// Clock is the time source.
	Clock core.Clock
	// IDs generates webhook ids.
	IDs core.IDGenerator
}

// Receive verifies a webhook, stores it pending and enqueues its
// processing (lago InboundWebhooks::CreateService).
func (in *Inbound) Receive(ctx context.Context, rcv Received) (*payment.InboundWebhook, error) {
	src, ok := in.Sources[rcv.Source]
	if !ok {
		return nil, fmt.Errorf("receive %q webhook: %w", rcv.Source, ErrUnknownSource)
	}
	eventType, err := src.Verify(ctx, rcv.OrganizationID, rcv.IntegrationID, rcv.Payload, rcv.Signature)
	if err != nil {
		return nil, fmt.Errorf("receive %s webhook: %w", rcv.Source, err)
	}
	w := payment.NewInboundWebhook(rcv.OrganizationID, core.NewID[core.InboundWebhookID](in.IDs), rcv.Source, eventType, json.RawMessage(rcv.Payload), in.Clock.Now())
	w.IntegrationID = rcv.IntegrationID
	w.Code = rcv.Code
	if rcv.Signature != "" {
		w.Signature = &rcv.Signature
	}
	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("receive %s webhook: %w", rcv.Source, err)
	}
	err = in.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := r.InboundWebhooks.Create(ctx, w); err != nil {
			return err
		}
		return enqueueInbound(ctx, r, w)
	})
	if err != nil {
		return nil, fmt.Errorf("receive %s webhook: %w", rcv.Source, err)
	}
	return w, nil
}

func enqueueInbound(ctx context.Context, r port.Repos, w *payment.InboundWebhook) error {
	job, err := newJob(KindProcessInbound, w.OrganizationID, InboundArgs{WebhookID: w.ID}, nextRun(w), fmt.Sprintf("%s/%d", w.ID, w.Attempts))
	if err != nil {
		return err
	}
	job.Queue = port.QueuePayments
	return r.Jobs.Enqueue(ctx, job)
}

func nextRun(w *payment.InboundWebhook) (t time.Time) {
	t, _ = w.NextRetry()
	return t
}

// Process runs a [KindProcessInbound] job (lago
// InboundWebhooks::ProcessService): a due webhook is marked processing,
// handed to its source outside any transaction, then marked succeeded or
// failed. A failed webhook is retried while [payment.InboundWebhook.NextRetry]
// allows; a webhook being processed by another worker, or succeeded, is
// left alone.
func (in *Inbound) Process(ctx context.Context, org core.OrganizationID, args InboundArgs) error {
	var w *payment.InboundWebhook
	err := in.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if w, err = r.InboundWebhooks.Get(ctx, org, args.WebhookID); err != nil {
			return err
		}
		if !w.Retriable(in.Clock.Now()) {
			w = nil
			return nil
		}
		if err := w.Transition(payment.InboundWebhookStatusProcessing, in.Clock.Now()); err != nil {
			return err
		}
		return r.InboundWebhooks.Update(ctx, w)
	})
	if errors.Is(err, core.ErrNotFound) || (err == nil && w == nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("process webhook %s: %w", args.WebhookID, err)
	}
	src, ok := in.Sources[w.Source]
	var herr error
	if !ok {
		herr = fmt.Errorf("%q: %w", w.Source, ErrUnknownSource)
	} else {
		herr = src.Handle(ctx, w)
	}
	if herr != nil && ctx.Err() != nil {
		// The worker stops; the webhook stays processing and is retried.
		return fmt.Errorf("process webhook %s: %w", w.ID, herr)
	}
	to := payment.InboundWebhookStatusSucceeded
	if herr != nil {
		to = payment.InboundWebhookStatusFailed
		slog.WarnContext(ctx, "inbound webhook failed", "webhook_id", w.ID, "source", w.Source, "attempts", w.Attempts, "error", herr)
	}
	err = in.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := w.Transition(to, in.Clock.Now()); err != nil {
			return err
		}
		if err := r.InboundWebhooks.Update(ctx, w); err != nil {
			return err
		}
		if _, retry := w.NextRetry(); to == payment.InboundWebhookStatusFailed && retry {
			return enqueueInbound(ctx, r, w)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("process webhook %s: %w", w.ID, err)
	}
	return nil
}

// RetryDue enqueues the processing of an organization's webhooks that are
// due ([payment.InboundWebhook.Retriable]), recovering webhooks stuck in
// processing, and returns how many.
func (in *Inbound) RetryDue(ctx context.Context, org core.OrganizationID) (int, error) {
	var n int
	err := in.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		due, err := r.InboundWebhooks.ListRetriable(ctx, org, in.Clock.Now())
		if err != nil {
			return err
		}
		for _, w := range due {
			if err := enqueueInbound(ctx, r, w); err != nil {
				return err
			}
		}
		n = len(due)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("retry inbound webhooks: %w", err)
	}
	return n, nil
}
