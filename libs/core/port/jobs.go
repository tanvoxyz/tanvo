package port

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
)

// Queue names a job queue (architecture §9).
type Queue string

// Queues.
const (
	// QueueBilling runs billing runs, invoice finalization and refreshes.
	QueueBilling Queue = "billing"
	// QueuePayments runs payment creation and provider callbacks.
	QueuePayments Queue = "payments"
	// QueueWebhooks runs outbound webhook deliveries.
	QueueWebhooks Queue = "webhooks"
	// QueueDocuments runs document rendering and emails.
	QueueDocuments Queue = "documents"
	// QueueIntegrations runs accounting, CRM and tax integration syncs.
	QueueIntegrations Queue = "integrations"
	// QueueExports runs data exports.
	QueueExports Queue = "exports"
	// QueueDefault runs everything else.
	QueueDefault Queue = "default"
)

// QueueValues returns every queue, in declaration order.
func QueueValues() []Queue {
	return []Queue{QueueBilling, QueuePayments, QueueWebhooks, QueueDocuments, QueueIntegrations, QueueExports, QueueDefault}
}

// Valid reports whether q is a declared queue.
func (q Queue) Valid() bool {
	switch q {
	case QueueBilling, QueuePayments, QueueWebhooks, QueueDocuments, QueueIntegrations, QueueExports, QueueDefault:
		return true
	default:
		return false
	}
}

// String returns the queue name.
func (q Queue) String() string { return string(q) }

// Job is one background job to run after the enqueuing transaction
// commits.
type Job struct {
	// Kind names the job handler ("invoice.finalize", …); libs/jobs
	// registers the kinds.
	Kind string
	// Queue is the queue that runs the job.
	Queue Queue
	// OrganizationID is the tenant, used for fairness (architecture §9).
	OrganizationID core.OrganizationID
	// Args is the JSON-encoded argument of the handler.
	Args json.RawMessage
	// RunAt delays the job; zero runs it as soon as possible.
	RunAt time.Time
	// UniqueKey, when set, makes the enqueue a no-op while a job of the
	// same organization, kind and key is pending or running.
	UniqueKey string
	// MaxAttempts bounds the retries; 0 uses the queue's default.
	MaxAttempts int
}

// JobEnqueuer enqueues jobs in the current transaction: a job becomes
// visible to workers only when the transaction commits, and disappears
// when it rolls back.
type JobEnqueuer interface {
	// Enqueue adds jobs to their queues.
	Enqueue(ctx context.Context, jobs ...Job) error
}
