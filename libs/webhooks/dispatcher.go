package webhooks

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Job kinds registered by libs/jobs.
const (
	// KindFanOut creates the deliveries of a recorded domain event; its
	// arguments are [EventArgs].
	KindFanOut = "webhook.deliver_event"
	// KindDeliver makes one attempt of a delivery; its arguments are
	// [DeliveryArgs].
	KindDeliver = "webhook.deliver"
	// KindPurge deletes an organization's deliveries past their retention;
	// its arguments are [PurgeArgs].
	KindPurge = "webhook.purge"
)

// PurgeArgs are the arguments of a [KindPurge] job.
type PurgeArgs struct {
	// Cutoff is the creation instant before which deliveries are deleted:
	// the clock tick minus [webhook.Retention].
	Cutoff time.Time `json:"cutoff"`
}

// PurgeBatch is the number of deliveries a purge deletes per transaction
// (lago-api's Clock::WebhooksCleanupJob batch size).
const PurgeBatch = 1000

// EventArgs are the arguments of a [KindFanOut] job.
type EventArgs struct {
	// EventID is the recorded domain event.
	EventID core.AuditEventID `json:"event_id"`
}

// DeliveryArgs are the arguments of a [KindDeliver] job.
type DeliveryArgs struct {
	// DeliveryID is the delivery to attempt.
	DeliveryID core.WebhookID `json:"delivery_id"`
}

// Emitter implements [port.WebhookEmitter] over a transactor: it records
// the event and enqueues its [KindFanOut] job in the transaction carried
// by ctx, or in a new one.
type Emitter struct {
	// Tx is the transaction boundary.
	Tx port.Transactor
}

var _ port.WebhookEmitter = (*Emitter)(nil)

// Emit implements [port.WebhookEmitter].
func (m *Emitter) Emit(ctx context.Context, e webhook.Event) error {
	if err := e.Validate(); err != nil {
		return fmt.Errorf("emit %s: %w", e.Type, err)
	}
	return m.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := r.DomainEvents.Record(ctx, &e); err != nil {
			return fmt.Errorf("emit %s: %w", e.Type, err)
		}
		job, err := newJob(KindFanOut, QueueFor(e.Type, 0), e.OrganizationID, EventArgs{EventID: e.ID}, time.Time{}, e.ID.String())
		if err != nil {
			return fmt.Errorf("emit %s: %w", e.Type, err)
		}
		return r.Jobs.Enqueue(ctx, job)
	})
}

// highPriority are the event types whose fan-out and first attempt run on
// [port.QueueWebhooksHighPriority].
//
// source: app/jobs/send_webhook_job.rb:91
var highPriority = []webhook.EventType{webhook.EventTypeAlertTriggered}

// QueueFor returns the queue of the job of an event of type t whose
// delivery made attempts attempts: the fan-out (0) and first attempt of a
// high-priority type run on [port.QueueWebhooksHighPriority], everything
// else, retries included, on [port.QueueWebhooks].
//
// source: app/jobs/send_webhook_job.rb:91-99, app/services/webhooks/base_service.rb:29, app/jobs/send_http_webhook_job.rb:4
func QueueFor(t webhook.EventType, attempts int) port.Queue {
	if attempts == 0 && slices.Contains(highPriority, t) {
		return port.QueueWebhooksHighPriority
	}
	return port.QueueWebhooks
}

func newJob(kind string, queue port.Queue, org core.OrganizationID, args any, runAt time.Time, key string) (port.Job, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return port.Job{}, err
	}
	return port.Job{Kind: kind, Queue: queue, OrganizationID: org, Args: b, RunAt: runAt, UniqueKey: key}, nil
}

// deliverJob is the job of the next attempt of d. Its unique key includes
// the attempt count, so that the job of a retry can be enqueued while the
// job of the previous attempt is still running, and a sweep enqueuing a
// job that is already pending is a no-op.
func deliverJob(d *webhook.Delivery) (port.Job, error) {
	var runAt time.Time
	if d.NextAttemptAt != nil {
		runAt = *d.NextAttemptAt
	}
	return newJob(KindDeliver, QueueFor(d.EventType, d.Attempts), d.OrganizationID, DeliveryArgs{DeliveryID: d.ID}, runAt, fmt.Sprintf("%s/%d", d.ID, d.Attempts))
}

// Envelope is the JSON body of every delivery.
type Envelope struct {
	// Type is the event type.
	Type webhook.EventType `json:"type"`
	// Timestamp is when the event occurred.
	Timestamp time.Time `json:"timestamp"`
	// OrganizationID is the organization of the event.
	OrganizationID core.OrganizationID `json:"organization_id"`
	// ObjectType names the kind of object in Data.
	ObjectType string `json:"object_type"`
	// Data is the API representation of the object.
	Data json.RawMessage `json:"data"`
}

// Dispatcher runs the webhook jobs.
type Dispatcher struct {
	// Tx is the transaction boundary.
	Tx port.Transactor
	// Sender makes the attempts.
	Sender *Sender
	// Clock is the time source.
	Clock core.Clock
	// Jitter returns a number in [0, 1) that randomizes backoffs; nil uses
	// math/rand/v2.
	Jitter func() float64
}

// deliveryID is the id of the delivery of event to endpoint: a UUIDv7
// with the event's timestamp and bits derived from both ids, so that a
// fan-out job that runs twice finds the deliveries it already created.
func deliveryID(event core.AuditEventID, endpoint core.WebhookEndpointID) core.WebhookID {
	ev, ep := event.UUID(), endpoint.UUID()
	h := sha256.Sum256(append(ev[:], ep[:]...))
	var u uuid.UUID
	copy(u[:6], ev[:6])
	copy(u[6:], h[:10])
	u[6] = u[6]&0x0f | 0x70
	u[8] = u[8]&0x3f | 0x80
	return core.MustParseID[core.WebhookID](u.String())
}

// FanOut runs a [KindFanOut] job: it creates a pending delivery of the
// event to every endpoint subscribed to its type and enqueues their
// attempts. Running it again creates nothing new; an event that no longer
// exists is ignored.
func (p *Dispatcher) FanOut(ctx context.Context, org core.OrganizationID, args EventArgs) error {
	return p.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		e, err := r.DomainEvents.Get(ctx, org, args.EventID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("fan out %s: %w", args.EventID, err)
		}
		endpoints, err := r.WebhookEndpoints.ListSubscribed(ctx, org, e.Type)
		if err != nil {
			return fmt.Errorf("fan out %s: %w", e.ID, err)
		}
		if len(endpoints) == 0 {
			return nil
		}
		body, err := json.Marshal(Envelope{Type: e.Type, Timestamp: e.OccurredAt, OrganizationID: org, ObjectType: e.ObjectType, Data: e.Payload})
		if err != nil {
			return fmt.Errorf("fan out %s: %w", e.ID, err)
		}
		now := p.Clock.Now()
		for _, ep := range endpoints {
			id := deliveryID(e.ID, ep.ID)
			if _, err := r.WebhookDeliveries.Get(ctx, org, id); err == nil {
				continue
			} else if !errors.Is(err, core.ErrNotFound) {
				return fmt.Errorf("fan out %s: %w", e.ID, err)
			}
			d := webhook.NewDelivery(id, ep.ID, e, now)
			d.Payload = body
			if err := r.WebhookDeliveries.Create(ctx, d); err != nil {
				return fmt.Errorf("fan out %s to %s: %w", e.ID, ep.ID, err)
			}
			job, err := deliverJob(d)
			if err != nil {
				return fmt.Errorf("fan out %s: %w", e.ID, err)
			}
			if err := r.Jobs.Enqueue(ctx, job); err != nil {
				return fmt.Errorf("fan out %s: %w", e.ID, err)
			}
		}
		return nil
	})
}

// Deliver runs a [KindDeliver] job: one attempt of a due delivery, sent
// outside any transaction, then recorded with the next attempt enqueued
// when the delivery is retrying. A delivery that is not due (already
// attempted, succeeded, or deleted with its endpoint) is ignored.
func (p *Dispatcher) Deliver(ctx context.Context, org core.OrganizationID, args DeliveryArgs) error {
	var (
		d  *webhook.Delivery
		ep *webhook.Endpoint
	)
	err := p.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if d, err = r.WebhookDeliveries.Get(ctx, org, args.DeliveryID); err != nil {
			return err
		}
		ep, err = r.WebhookEndpoints.Get(ctx, org, d.EndpointID)
		return err
	})
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("deliver %s: %w", args.DeliveryID, err)
	}
	if !d.Due(p.Clock.Now()) {
		return nil
	}
	attempts := d.Attempts
	res, err := p.Sender.Send(ctx, ep, d, p.Clock.Now())
	if err != nil {
		return err
	}
	return p.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		d, err := r.WebhookDeliveries.Get(ctx, org, args.DeliveryID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("deliver %s: %w", args.DeliveryID, err)
		}
		if d.Attempts != attempts || !d.Due(p.Clock.Now()) {
			// Another worker recorded an attempt meanwhile.
			return nil
		}
		now := p.Clock.Now()
		if res.Succeeded {
			err = d.Succeed(*res.HTTPStatus, res.Response, now)
		} else {
			err = d.Fail(res.HTTPStatus, string(res.Response), p.jitter(), now)
		}
		if err != nil {
			return fmt.Errorf("deliver %s: %w", d.ID, err)
		}
		if err := r.WebhookDeliveries.Update(ctx, d); err != nil {
			return fmt.Errorf("deliver %s: %w", d.ID, err)
		}
		if d.Status != webhook.StatusRetrying {
			return nil
		}
		job, err := deliverJob(d)
		if err != nil {
			return fmt.Errorf("deliver %s: %w", d.ID, err)
		}
		return r.Jobs.Enqueue(ctx, job)
	})
}

func (p *Dispatcher) jitter() float64 {
	if p.Jitter != nil {
		return p.Jitter()
	}
	return rand.Float64() //nolint:gosec // backoff jitter needs no cryptographic randomness
}

// Retry schedules a manual attempt of a failed or retrying delivery now
// and returns it; a succeeded delivery fails with a transition error.
func (p *Dispatcher) Retry(ctx context.Context, org core.OrganizationID, id core.WebhookID) (*webhook.Delivery, error) {
	var d *webhook.Delivery
	err := p.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if d, err = r.WebhookDeliveries.Get(ctx, org, id); err != nil {
			return err
		}
		if err := d.Retry(p.Clock.Now()); err != nil {
			return err
		}
		if err := r.WebhookDeliveries.Update(ctx, d); err != nil {
			return err
		}
		job, err := deliverJob(d)
		if err != nil {
			return err
		}
		return r.Jobs.Enqueue(ctx, job)
	})
	if err != nil {
		return nil, fmt.Errorf("retry webhook %s: %w", id, err)
	}
	return d, nil
}

// Sweep enqueues the attempts of up to limit due deliveries, recovering
// jobs lost before they ran; jobs already pending are not duplicated. It
// returns how many deliveries were due.
func (p *Dispatcher) Sweep(ctx context.Context, limit int) (int, error) {
	var n int
	err := p.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		due, err := r.WebhookDeliveries.ListDue(ctx, p.Clock.Now(), limit)
		if err != nil {
			return err
		}
		jobs := make([]port.Job, 0, len(due))
		for _, d := range due {
			job, err := deliverJob(d)
			if err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		n = len(due)
		if n == 0 {
			return nil
		}
		return r.Jobs.Enqueue(ctx, jobs...)
	})
	if err != nil {
		return 0, fmt.Errorf("sweep webhooks: %w", err)
	}
	return n, nil
}

// DispatchPurges runs the webhook retention clock job of the tick at now:
// it enqueues a [KindPurge] job for every organization with webhook
// endpoints (deliveries are deleted with their endpoint), and returns how
// many. Each organization's deletes run in its own job, under its
// row-level security scope.
func (p *Dispatcher) DispatchPurges(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := p.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		orgs, err := r.WebhookEndpoints.Organizations(ctx)
		if err != nil {
			return err
		}
		args := PurgeArgs{Cutoff: now.UTC().Add(-webhook.Retention)}
		jobs := make([]port.Job, 0, len(orgs))
		for _, org := range orgs {
			job, err := newJob(KindPurge, port.QueueWebhooks, org, args, time.Time{}, KindPurge)
			if err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		n = len(jobs)
		return r.Jobs.Enqueue(ctx, jobs...)
	})
	if err != nil {
		return 0, fmt.Errorf("dispatch webhook purges: %w", err)
	}
	return n, nil
}

// Purge runs a [KindPurge] job: it deletes org's deliveries last updated
// before the cutoff, [PurgeBatch] per transaction, and returns how many
// (Clock::WebhooksCleanupJob).
func (p *Dispatcher) Purge(ctx context.Context, org core.OrganizationID, args PurgeArgs) (int, error) {
	total := 0
	for {
		var n int
		err := p.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			var err error
			n, err = r.WebhookDeliveries.PurgeBefore(ctx, org, args.Cutoff, PurgeBatch)
			return err
		})
		if err != nil {
			return total, fmt.Errorf("purge webhooks: %w", err)
		}
		total += n
		if n < PurgeBatch {
			return total, nil
		}
	}
}
