package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Failure classes of the integration jobs, matched by [RetryRules].
var (
	// ErrSyncRetryable marks a provider failure the job retries a few
	// times: a server error or a failed dependency (lago re-raises
	// LagoHttpClient::HttpError on 5xx and 424, retried by the job).
	ErrSyncRetryable = errors.New("integrations: provider failure, retried")
	// ErrSyncTimeout marks a call that timed out without an answer (lago
	// Net::ReadTimeout): the record may have been created, so the retry
	// looks it up first where the provider allows it.
	ErrSyncTimeout = errors.New("integrations: provider timed out")
	// ErrSyncPayload marks a document that cannot be built yet: the
	// invoice of a payment is not synced, or the invoice has no file
	// (lago Integrations::Aggregator::BasePayload::Failure, retried).
	ErrSyncPayload = errors.New("integrations: document incomplete")

	// errNetSuiteTimeout marks an [ErrSyncTimeout] of a NetSuite call,
	// retried after [netsuiteTimeoutDelay] instead of a polynomial wait.
	errNetSuiteTimeout = errors.New("integrations: netsuite timed out")
)

// netsuiteTimeoutDelay is the wait before a NetSuite invoice whose
// creation timed out is looked up again; the other providers wait
// polynomially longer (lago aggregator/invoices/create_job.rb:11-13 and
// :28-42).
const netsuiteTimeoutDelay = 6 * time.Minute

// Retry is a retry rule of an integration job kind: an error matching
// Match is retried until the job ran Attempts times, Delay apart, or with
// a polynomially longer wait when Delay is zero (lago
// wait: :polynomially_longer). An error no rule matches is not retried.
type Retry struct {
	// Match reports whether the rule applies to err.
	Match func(err error) bool
	// Attempts is the total number of attempts.
	Attempts int
	// Delay is the constant wait between attempts; zero waits
	// polynomially longer.
	Delay time.Duration
}

// Attempts of the integration jobs (lago app/jobs/integrations/).
const (
	requestLimitAttempts = 100
	throttlingAttempts   = 25
	taxRetryAttempts     = 6
)

func is(target error) func(error) bool { return func(err error) bool { return errors.Is(err, target) } }

func requestLimit(attempts int) Retry {
	return Retry{Match: is(integration.ErrRequestLimit), Attempts: attempts}
}

func retryable(attempts int) Retry { return Retry{Match: is(ErrSyncRetryable), Attempts: attempts} }

// ConcurrencyLimit is how many accounting and contact sync jobs of one
// organization run at once across the deployment (lago
// config/initializers/throttling.rb:10, the concurrency limit of the jobs
// including ConcurrencyThrottlable, which lago shares across every
// organization).
const ConcurrencyLimit = 5

// ConcurrencySlot is the prefix of the slots [ConcurrencyLimited] jobs
// share; [ConcurrencySlotOf] names the slots of one organization.
const ConcurrencySlot = "integrations.sync"

// ConcurrencySlotOf returns the name of the slots the [ConcurrencyLimited]
// jobs of org share, so that one organization's backlog does not hold the
// slots of the others.
func ConcurrencySlotOf(org core.OrganizationID) string {
	return ConcurrencySlot + "/" + org.String()
}

// ConcurrencyLimited reports whether jobs of kind share the
// [ConcurrencyLimit]: the contact, invoice, credit note and payment syncs
// (integration_customers/create_job.rb and update_job.rb,
// aggregator/{invoices,credit_notes,payments}/create_job.rb).
func ConcurrencyLimited(kind string) bool {
	switch kind {
	case kernel.JobSyncContact, kernel.JobSyncInvoice, kernel.JobSyncCreditNote, kernel.JobSyncPayment:
		return true
	default:
		return false
	}
}

// JobKinds returns the kinds [Service.HandleJob] runs.
func JobKinds() []string { return kernel.IntegrationJobKinds() }

// RetryRules returns the retry rules of kind, first match first (lago
// retry_on of each job).
func RetryRules(kind string) []Retry {
	switch kind {
	case kernel.JobFetchTaxCompany:
		// avalara/fetch_company_id_job.rb: the service rescues every HTTP
		// error, so nothing retries.
		return nil
	case kernel.JobReportInvoiceTaxes:
		// aggregator/taxes/invoices/create_job.rb
		return []Retry{requestLimit(throttlingAttempts), {Match: is(kernel.ErrTaxContention), Attempts: taxRetryAttempts}}
	case kernel.JobSyncContact:
		// integration_customers/create_job.rb and update_job.rb
		return []Retry{requestLimit(throttlingAttempts), retryable(3)}
	case kernel.JobSyncInvoice:
		// aggregator/invoices/create_job.rb: a timeout is retried 6
		// times; NetSuite waits 6 minutes before the lookup.
		return []Retry{
			{Match: is(errNetSuiteTimeout), Attempts: 6, Delay: netsuiteTimeoutDelay},
			{Match: is(ErrSyncTimeout), Attempts: 6},
			requestLimit(requestLimitAttempts), retryable(3),
		}
	case kernel.JobSyncCreditNote, kernel.JobFetchIntegrationItems, kernel.JobDeployCRM, kernel.JobSaveCRMAccount:
		// aggregator/credit_notes/create_job.rb, fetch_items_job.rb,
		// hubspot/*/deploy_*_job.rb, hubspot/save_portal_id_job.rb
		return []Retry{requestLimit(requestLimitAttempts), retryable(3)}
	case kernel.JobSyncPayment:
		// aggregator/payments/create_job.rb
		return []Retry{
			{Match: is(ErrSyncTimeout), Attempts: 25},
			{Match: is(ErrSyncPayload), Attempts: 10},
			requestLimit(requestLimitAttempts), retryable(5),
		}
	case kernel.JobCreateCRMInvoice, kernel.JobUpdateCRMInvoice, kernel.JobCreateCRMSubscription, kernel.JobUpdateCRMSubscription:
		// aggregator/{invoices,subscriptions}/hubspot/{create,update}_job.rb
		return []Retry{{Match: is(ErrSyncPayload), Attempts: 10}, requestLimit(requestLimitAttempts), retryable(10)}
	case kernel.JobAssociateCRMInvoice, kernel.JobAssociateCRMSubscription:
		// aggregator/{invoices,subscriptions}/hubspot/create_customer_association_job.rb
		return []Retry{requestLimit(requestLimitAttempts), retryable(10)}
	default:
		return nil
	}
}

// HandleJob runs attempt (1 for the first run) of an integration job;
// false when the kind is not one of [JobKinds].
func (s *Service) HandleJob(ctx context.Context, j port.Job, attempt int) (bool, error) {
	org := j.OrganizationID
	switch j.Kind {
	case kernel.JobFetchTaxCompany:
		var a kernel.IntegrationArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.fetchTaxCompany(ctx, org, a.IntegrationID)
	case kernel.JobReportInvoiceTaxes:
		var a kernel.InvoiceArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.reportInvoiceTaxes(ctx, org, a.InvoiceID)
	case kernel.JobSyncContact:
		var a kernel.IntegrationCustomerArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.syncContact(ctx, org, a)
	case kernel.JobSyncInvoice:
		var a kernel.SyncInvoiceArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.syncInvoice(ctx, org, a, attempt > 1)
	case kernel.JobSyncCreditNote:
		var a kernel.CreditNoteArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.syncCreditNote(ctx, org, a.CreditNoteID)
	case kernel.JobSyncPayment:
		var a kernel.PaymentArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.syncPayment(ctx, org, a.PaymentID)
	case kernel.JobCreateCRMInvoice, kernel.JobUpdateCRMInvoice, kernel.JobAssociateCRMInvoice:
		var a kernel.InvoiceArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.crmInvoiceJob(ctx, org, j.Kind, a.InvoiceID)
	case kernel.JobCreateCRMSubscription, kernel.JobUpdateCRMSubscription, kernel.JobAssociateCRMSubscription:
		var a kernel.SubscriptionArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.crmSubscriptionJob(ctx, org, j.Kind, a.SubscriptionID)
	case kernel.JobFetchIntegrationItems:
		var a kernel.IntegrationArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		_, err := s.FetchItems(ctx, org, a.IntegrationID)
		return true, err
	case kernel.JobDeployCRM:
		var a kernel.IntegrationArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.deployCRM(ctx, org, a.IntegrationID)
	case kernel.JobSaveCRMAccount:
		var a kernel.IntegrationArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, s.saveCRMAccount(ctx, org, a.IntegrationID)
	default:
		return false, nil
	}
}

func decode(j port.Job, v any) error {
	if err := json.Unmarshal(j.Args, v); err != nil {
		return fmt.Errorf("decode %s args: %w", j.Kind, err)
	}
	return nil
}
