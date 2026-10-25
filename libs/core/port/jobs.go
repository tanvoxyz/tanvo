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
	// QueueAlerts runs the subscription activities and usage alerts (lago
	// alerts, app/jobs/usage_monitoring/process_subscription_activity_job.rb:6-10).
	QueueAlerts Queue = "alerts"
	// QueueAlertsHighPriority runs the wallet alerts (lago
	// alerts_high_priority, app/jobs/usage_monitoring/process_wallet_alerts_job.rb:5-10).
	QueueAlertsHighPriority Queue = "alerts_high_priority"
	// QueueWallets runs the refreshes of wallets' ongoing balances (lago
	// wallets, app/jobs/customers/refresh_wallet_job.rb:6-11).
	QueueWallets Queue = "wallets"
	// QueueStreaming runs the deliveries to streaming destinations (lago
	// streaming, app/jobs/deliver_event_job.rb:4).
	QueueStreaming Queue = "streaming"
	// QueueDefault runs everything else.
	QueueDefault Queue = "default"
)

// QueueValues returns every queue, in declaration order.
func QueueValues() []Queue {
	return []Queue{QueueBilling, QueuePayments, QueueWebhooks, QueueDocuments, QueueIntegrations, QueueExports, QueueAlerts, QueueAlertsHighPriority, QueueWallets, QueueStreaming, QueueDefault}
}

// Valid reports whether q is a declared queue.
func (q Queue) Valid() bool {
	switch q {
	case QueueBilling, QueuePayments, QueueWebhooks, QueueDocuments, QueueIntegrations, QueueExports,
		QueueAlerts, QueueAlertsHighPriority, QueueWallets, QueueStreaming, QueueDefault:
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

// QueueClock is implemented by a [JobEnqueuer] whose queue starts the
// jobs by a clock of its own (the database's): QueueNow returns that
// clock's current instant, read in the transaction, so a caller can place
// a RunAt or a unique key relative to when the queue will run the job
// rather than to its own, possibly skewed, clock.
type QueueClock interface {
	// QueueNow returns the queue clock's current instant in UTC.
	QueueNow(ctx context.Context) (time.Time, error)
}

// JobEnqueuer enqueues jobs in the current transaction: a job becomes
// visible to workers only when the transaction commits, and disappears
// when it rolls back.
type JobEnqueuer interface {
	// Enqueue adds jobs to their queues.
	Enqueue(ctx context.Context, jobs ...Job) error
}
