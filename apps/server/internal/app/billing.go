package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpc"
	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/ingest"
	"github.com/tanvoxyz/tanvo/libs/ingest/kafka"
	"github.com/tanvoxyz/tanvo/libs/jobs"
	"github.com/tanvoxyz/tanvo/libs/tax"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// newBilling returns the billing engine over the store and the event
// stores, its domain events recorded by the webhook emitter. No tax
// provider or refund gateway is wired before the providers (phase 5):
// customers taxed by a provider wait for it, and refunds stay pending.
func newBilling(cfg config.Config, tx port.Transactor, stores *EventStores, clock core.Clock, log *slog.Logger) (*billing.Engine, error) {
	e, err := billing.New(kernel.Deps{
		Tx:               tx,
		Events:           eventRouter{stores: stores},
		Clock:            clock,
		IDs:              core.UUIDv7Generator{},
		Webhooks:         &webhooks.Emitter{Tx: tx},
		ActivityLogs:     auditlog.NewActivityProducer(clock, core.UUIDv7Generator{}, rpc.ActivityActor),
		Logger:           log,
		DailyUsageJitter: cfg.DailyUsageSchedulingJitter,
		Transient:        retriedByBillingRetry,
	}, billing.WithTerminationAlertDays(cfg.SubscriptionTerminationAlertDays...),
		billing.WithVIES(&tax.VIESClient{}))
	if err != nil {
		return nil, fmt.Errorf("billing: %w", err)
	}
	// A completed VIES check finalizes the invoices held for it.
	customers.WithVIESInvoiceFinalizer(e.Invoices)(e.Customers)
	return e, nil
}

// billingJobs registers the billing job kinds and the billing periodic
// jobs.
type billingJobs struct {
	engine *billing.Engine
	tx     port.Transactor
	// clock, when set, is the instant of the clock jobs instead of their
	// tick (a test hook: [Options.Clock]).
	clock core.Clock
	log   *slog.Logger
}

// clockDispatch is a periodic job of the billing schedule: it lists the
// organizations with work of its kind and enqueues one billing job to
// each (architecture §9).
type clockDispatch struct {
	// clock is the periodic kind (libs/jobs).
	clock string
	// work is the due work the organizations are listed by.
	work organization.Work
	// kind is the per-organization billing job.
	kind string
	// args are the job's args at the tick; nil for jobs without args.
	args func(tick time.Time) any
	// perTick keys the job by the tick as well as by its kind: its work
	// depends on the day of the tick (billing days, interval top-ups), so
	// a later tick does not cover an earlier one. The other jobs do the
	// work due by their instant, so one pending job per organization
	// covers the ticks that arrive while it waits.
	perTick bool
}

func instant(t time.Time) any { return kernel.InstantArgs{At: t} }

// billingSchedule is the billing part of the clock schedule, with the
// lago-api clock jobs it ports (app/jobs/clock/).
func billingSchedule() []clockDispatch {
	return []clockDispatch{
		// activate_subscriptions_job.rb
		{jobs.KindActivateSubscriptions, organization.WorkPendingSubscriptions, kernel.JobActivatePendingSubscriptions, instant, false},
		// terminate_ended_subscriptions_job.rb
		{jobs.KindTerminateEndedSubscriptions, organization.WorkEndingSubscriptions, kernel.JobTerminateEndedSubscriptions, instant, false},
		// subscriptions_biller_job.rb
		{jobs.KindBillSubscriptions, organization.WorkActiveSubscriptions, kernel.JobBillSubscriptions, func(t time.Time) any {
			return kernel.BillSubscriptionsArgs{BillingAt: t}
		}, true},
		// free_trial_subscriptions_biller_job.rb
		{jobs.KindBillEndedTrials, organization.WorkActiveSubscriptions, kernel.JobEndTrials, instant, false},
		// retry_generating_subscription_invoices_job.rb
		{jobs.KindRetryGeneratingInvoices, organization.WorkStuckGeneratingInvoices, kernel.JobRetryGeneratingInvoices, instant, false},
		// finalize_invoices_job.rb
		{jobs.KindFinalizeInvoices, organization.WorkDraftInvoices, kernel.JobFinalizeDueDrafts, instant, false},
		// refresh_draft_invoices_job.rb
		{jobs.KindRefreshDraftInvoices, organization.WorkRefreshRequested, kernel.JobRefreshFlaggedDrafts, nil, false},
		// retry_failed_invoices_job.rb
		{jobs.KindRetryFailedInvoices, organization.WorkTaxRetriedInvoices, kernel.JobRetryTaxFailedInvoices, nil, false},
		// refresh_lifetime_usages_job.rb
		{jobs.KindRefreshLifetimeUsages, organization.WorkLifetimeUsages, kernel.JobRefreshLifetimeUsages, nil, false},
		// refresh_wallets_ongoing_balance_job.rb
		{jobs.KindRefreshWalletsOngoingBalance, organization.WorkAwaitingWalletRefresh, kernel.JobRefreshOngoingBalances, nil, false},
		// terminate_wallets_job.rb and terminate_recurring_transaction_rules_job.rb:
		// one job expires both, so the second dispatch is skipped while the
		// first waits.
		{jobs.KindTerminateWallets, organization.WorkExpiredWallets, kernel.JobExpireWallets, instant, false},
		{jobs.KindTerminateRecurringRules, organization.WorkExpiredWallets, kernel.JobExpireWallets, instant, false},
		// create_interval_wallet_transactions_job.rb
		{jobs.KindCreateIntervalWalletTransactions, organization.WorkIntervalRules, kernel.JobWalletIntervalTopUps, instant, true},
		// terminate_coupons_job.rb
		{jobs.KindTerminateCoupons, organization.WorkExpiredCoupons, kernel.JobTerminateCoupons, instant, false},
		// mark_invoices_as_payment_overdue_job.rb
		{jobs.KindMarkInvoicesPaymentOverdue, organization.WorkOverdueInvoices, kernel.JobMarkOverdueInvoices, instant, false},
		// subscriptions_to_be_terminated_job.rb
		{jobs.KindAlertSubscriptionsToBeTerminated, organization.WorkEndingAtSubscriptions, kernel.JobAlertTerminatingSubscriptions, instant, false},
		// expire_incomplete_subscriptions_job.rb
		{jobs.KindExpireIncompleteSubscriptions, organization.WorkExpirableSubscriptions, kernel.JobExpireIncompleteSubscriptions, instant, false},
		// expire_order_forms_job.rb
		{jobs.KindExpireOrderForms, organization.WorkExpiringOrderForms, kernel.JobExpireOrderForms, instant, false},
		// execute_scheduled_orders_job.rb
		{jobs.KindExecuteScheduledOrders, organization.WorkExecutableOrders, kernel.JobExecuteScheduledOrders, instant, false},
		// compute_all_daily_usages_job.rb: hourly, each run covering the
		// organizations entering a new day in their time zone.
		{jobs.KindComputeDailyUsages, organization.WorkDailyUsages, kernel.JobComputeDailyUsages, instant, true},
		// events_validation_job.rb: the window is the hour before the tick's
		// hour, so a later tick does not cover an earlier one.
		{jobs.KindValidateEvents, organization.WorkEventsToValidate, kernel.JobValidateEvents, instant, true},
		// record_deletions_cleanup_job.rb
		{jobs.KindCleanRecordDeletions, organization.WorkExpiredRecordDeletions, kernel.JobCleanRecordDeletions, instant, false},
		// activate_contracts_job.rb: a pending contract is a pending
		// subscription.
		{jobs.KindActivateContracts, organization.WorkPendingSubscriptions, kernel.JobActivateContracts, instant, false},
		// create_billing_segments_job.rb: a contract with a due rate card is
		// an active subscription.
		{jobs.KindCreateBillingSegments, organization.WorkActiveSubscriptions, kernel.JobCreateBillingSegments, instant, false},
		// process_billing_segments_job.rb
		{jobs.KindProcessBillingSegments, organization.WorkAwaitingBillingSegments, kernel.JobProcessBillingSegmentsAll, instant, false},
		// process_dunning_campaigns_job.rb
		{jobs.KindProcessDunningCampaigns, organization.WorkDunningCustomers, kernel.JobProcessDunningCampaigns, instant, false},
	}
}

// providerKinds are the billing kinds that call a tax provider or a
// payment provider: a retry could repeat the call, so only errors marked
// [jobs.ErrRetriable] retry them.
var providerKinds = []string{kernel.JobPullProviderTaxes, kernel.JobVoidProviderTaxes, kernel.JobRefundCreditNote, kernel.JobReportCreditNoteTaxes}

// register registers every billing kind, the kinds billing enqueues for
// the payments and documents libraries, and the billing periodic jobs.
func (b billingJobs) register(reg *jobs.Registry) error {
	for _, k := range b.engine.JobKinds() {
		kind := jobs.Kind{Name: k}
		if !slices.Contains(providerKinds, k) {
			kind.Retry = billingRetry()
		} else {
			kind.Retry = providerRetry()
		}
		if k == kernel.JobProcessSubscriptionActivity {
			// lago UsageMonitoring::ProcessSubscriptionActivityJob
			// (app/jobs/usage_monitoring/process_subscription_activity_job.rb)
			// re-enqueues itself at once on any error, up to 4 attempts.
			kind.MaxAttempts, kind.Backoff = activityAttempts, func(int) time.Duration { return 0 }
		}
		if err := jobs.Register(reg, kind, jobs.HandlerFunc[json.RawMessage](b.run)); err != nil {
			return err
		}
	}
	if err := b.registerPending(reg); err != nil {
		return err
	}
	for _, d := range billingSchedule() {
		if err := jobs.Register(reg, jobs.Kind{Name: d.clock}, jobs.HandlerFunc[struct{}](func(ctx context.Context, j *jobs.Job[struct{}]) error {
			n, err := b.dispatch(ctx, d, b.tick(j.ScheduledAt))
			if err != nil {
				return err
			}
			b.log.InfoContext(ctx, "billing: dispatched", "kind", d.kind, "organizations", n)
			return nil
		})); err != nil {
			return err
		}
	}
	return nil
}

// registerPending registers the kinds billing enqueues for libraries not
// wired yet, so that the transactions enqueueing them commit:
//
//   - payments.create_invoice_payment completes at once: without payment
//     providers (phase 5) an invoice's payment stays pending, as lago-api
//     skips an invoice without a provider
//     (app/services/invoices/payments/create_service.rb:152).
//   - documents.generate_invoice and documents.generate_credit_note are
//     cancelled, kept in River for a manual retry once libs/documents is
//     wired to the store and a blob store.
func (b billingJobs) registerPending(reg *jobs.Registry) error {
	// Phase 5 hook: the payments orchestration replaces this handler. Its
	// payment status update (lago Invoices::UpdateService#
	// handle_prepaid_credits, app/services/invoices/update_service.rb:113-117)
	// must call wallets.Service.HandleCreditInvoicePayment
	// (b.engine.Wallets) for a credit invoice whose payment succeeded or
	// failed, so that its wallet transaction settles.
	if err := jobs.Register(reg, jobs.Kind{Name: kernel.JobCreateInvoicePayment},
		jobs.HandlerFunc[kernel.InvoiceArgs](func(ctx context.Context, j *jobs.Job[kernel.InvoiceArgs]) error {
			b.log.DebugContext(ctx, "payments: no payment provider", "invoice", j.Args.InvoiceID.String())
			return nil
		})); err != nil {
		return err
	}
	for _, k := range []string{kernel.JobGenerateInvoiceDocument, kernel.JobGenerateCreditNoteDocument} {
		if err := jobs.Register(reg, jobs.Kind{Name: k}, jobs.HandlerFunc[json.RawMessage](func(context.Context, *jobs.Job[json.RawMessage]) error {
			return jobs.Permanent(fmt.Errorf("%s: documents are not wired: %w", k, core.ErrNotAllowed))
		})); err != nil {
			return err
		}
	}
	return nil
}

// run runs a billing job with the engine.
func (b billingJobs) run(ctx context.Context, j *jobs.Job[json.RawMessage]) error {
	args := j.Args
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	ok, err := b.engine.HandleJob(ctx, port.Job{
		Kind: j.Kind, Queue: kernel.QueueOf(j.Kind), OrganizationID: j.OrganizationID, Args: args, UniqueKey: j.UniqueKey,
	})
	if !ok {
		return jobs.Permanent(fmt.Errorf("billing: kind %s: %w", j.Kind, core.ErrNotFound))
	}
	return err
}

// tick returns the instant of a clock job due at scheduled.
func (b billingJobs) tick(scheduled time.Time) time.Time {
	if b.clock != nil {
		return b.clock.Now().UTC()
	}
	return scheduled.UTC()
}

// dispatch enqueues the billing job of d for every organization with
// work of d's kind at tick, in one transaction, and returns their number.
func (b billingJobs) dispatch(ctx context.Context, d clockDispatch, tick time.Time) (int, error) {
	var raw json.RawMessage = []byte(`{}`)
	if d.args != nil {
		var err error
		if raw, err = json.Marshal(d.args(tick)); err != nil {
			return 0, fmt.Errorf("billing: encode %s args: %w", d.kind, err)
		}
	}
	key := d.kind
	if d.perTick {
		key += ":" + tick.Format(time.RFC3339)
	}
	n := 0
	err := b.tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		orgs, err := r.Organizations.WithWork(ctx, d.work, tick)
		if err != nil {
			return err
		}
		js := make([]port.Job, len(orgs))
		for i, org := range orgs {
			js[i] = port.Job{Kind: d.kind, Queue: kernel.QueueOf(d.kind), OrganizationID: org, Args: raw, UniqueKey: key}
		}
		n = len(js)
		if n == 0 {
			return nil
		}
		return r.Jobs.Enqueue(ctx, js...)
	})
	if err != nil {
		return 0, fmt.Errorf("billing: dispatch %s: %w", d.kind, err)
	}
	return n, nil
}

// billingRetry are the retry rules of the billing kinds that run in the
// store alone: lago-api retries its billing jobs on lock failures
// (BaseLockService::FailedToAcquireLock, ActiveRecord::StaleObjectError,
// PG::LockNotAvailable: MAX_LOCK_RETRY_ATTEMPTS with a random delay,
// e.g. app/jobs/bill_subscription_job.rb:14) and on numbering conflicts
// (Sequenced::SequenceError: 15 attempts, app/jobs/bill_subscription_job.rb:15).
// Their counterparts here are PostgreSQL's transient failures (deadlock,
// serialization failure, lock not available) and unique violations
// (core.ErrConflict). A billing job runs in one transaction, so a retry
// repeats nothing that committed.
func billingRetry() []jobs.RetryRule {
	return []jobs.RetryRule{
		{Match: transientDB, Attempts: jobs.LockRetryAttempts, Backoff: jobs.RandomDelay(jobs.LockRetryMaxDelay)},
		jobs.SequenceRetry(core.ErrConflict),
	}
}

// providerRetry are the retry rules of [providerKinds]: a tax provider
// at its API limit or under contention is asked again (lago
// Integrations::Aggregator::BaseService.retryable_errors, 6 attempts
// polynomially longer, app/jobs/invoices/provider_taxes/pull_taxes_and_apply_job.rb:15).
func providerRetry() []jobs.RetryRule {
	return []jobs.RetryRule{jobs.RetryOn(jobs.HTTPRetryAttempts, jobs.Polynomial(0.15), kernel.ErrTaxContention)}
}

// retriedByBillingRetry reports whether a rule of [billingRetry], or the
// [jobs.ErrRetriable] rule every kind has, runs a job failing with err
// again.
func retriedByBillingRetry(err error) bool {
	return transientDB(err) || errors.Is(err, core.ErrConflict) || errors.Is(err, jobs.ErrRetriable)
}

// activityAttempts is the attempt bound of the subscription activity job.
const activityAttempts = 4

// PostgreSQL error codes of transient failures.
const (
	pgSerializationFailure = "40001"
	pgDeadlockDetected     = "40P01"
	pgLockNotAvailable     = "55P03"
)

// transientDB reports whether err is a PostgreSQL failure that a new
// transaction may not hit.
func transientDB(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case pgSerializationFailure, pgDeadlockDetected, pgLockNotAvailable:
		return true
	}
	return false
}

// inAdvanceBiller enqueues the billing of in-advance events
// ([ingest.InAdvanceBiller]): one in-advance job per event, unique on the
// event's subscription and transaction ID (lago Events::PayInAdvanceJob,
// app/jobs/events/pay_in_advance_job.rb:13-19). In postgres mode the jobs
// commit with the ingestion transaction.
type inAdvanceBiller struct{ engine *billing.Engine }

var _ ingest.InAdvanceBiller = inAdvanceBiller{}

// BillInAdvance implements [ingest.InAdvanceBiller].
func (b inAdvanceBiller) BillInAdvance(ctx context.Context, events []*ingest.Enriched) error {
	byOrg := map[core.OrganizationID][]kernel.InAdvanceChargeArgs{}
	var orgs []core.OrganizationID
	for _, e := range events {
		if _, ok := byOrg[e.OrganizationID]; !ok {
			orgs = append(orgs, e.OrganizationID)
		}
		byOrg[e.OrganizationID] = append(byOrg[e.OrganizationID], kernel.InAdvanceChargeArgs{
			SubscriptionID:          e.InAdvanceTarget(),
			EventID:                 e.ID,
			TransactionID:           e.TransactionID,
			Timestamp:               e.Timestamp,
			Code:                    e.Code,
			Properties:              e.Properties,
			PreciseTotalAmountCents: e.PreciseTotalAmountCents,
		})
	}
	for _, org := range orgs {
		if err := b.engine.EnqueueInAdvanceCharges(ctx, org, byOrg[org]); err != nil {
			return fmt.Errorf("billing: in-advance events of %s: %w", org, err)
		}
	}
	return nil
}

// usageTracker passes usage refresh signals to billing
// ([ingest.UsageSignaler]): each signal schedules the activity of its
// subscriptions and refreshes its customer's wallets
// ([billing.Engine.TrackUsage]).
type usageTracker struct{ engine *billing.Engine }

var _ ingest.UsageSignaler = usageTracker{}

// SignalUsage implements [ingest.UsageSignaler].
func (t usageTracker) SignalUsage(ctx context.Context, signals []ingest.UsageSignal) error {
	for _, s := range signals {
		if err := t.engine.TrackUsage(ctx, s.OrganizationID, s.CustomerID, s.SubscriptionIDs); err != nil {
			return fmt.Errorf("billing: usage of customer %s: %w", s.CustomerID, err)
		}
	}
	return nil
}

// writeThenBill is the handler of the write consumer: it stores the
// batch with write, then bills the in-advance events of the records
// stored with bill, so that billing aggregates over stored events. A
// failed billing retries its records; the write drops them as duplicates
// and billing is idempotent per event.
func writeThenBill(write, bill kafka.Handler) kafka.Handler {
	return func(ctx context.Context, recs []*kgo.Record) []kafka.Result {
		results := write(ctx, recs)
		var stored []*kgo.Record
		var at []int
		for i, r := range results {
			if r.Disposition == kafka.Done && r.Err == nil {
				stored = append(stored, recs[i])
				at = append(at, i)
			}
		}
		if len(stored) == 0 {
			return results
		}
		for j, r := range bill(ctx, stored) {
			if r.Disposition != kafka.Done {
				i := at[j]
				results[i].Disposition, results[i].Code, results[i].Err = r.Disposition, r.Code, r.Err
			}
		}
		return results
	}
}

// usageSignals returns the handler of the usage refresh topic: it decodes
// the signals and passes them to signals. Signals are hints (the clock
// sweeps reconcile them), so a failed one is logged and dropped, and an
// undecodable one goes to the dead letters.
func usageSignals(signals ingest.UsageSignaler, log *slog.Logger) kafka.Handler {
	return func(ctx context.Context, recs []*kgo.Record) []kafka.Result {
		results := make([]kafka.Result, len(recs))
		for i, rec := range recs {
			var s ingest.UsageSignal
			if err := json.Unmarshal(rec.Value, &s); err != nil {
				results[i] = kafka.Result{Disposition: kafka.Dead, Code: ingest.ErrorDecode, Err: fmt.Errorf("decode usage signal: %w", err)}
				continue
			}
			if err := signals.SignalUsage(ctx, []ingest.UsageSignal{s}); err != nil {
				log.WarnContext(ctx, "billing: usage signal", "organization", s.OrganizationID.String(), "error", err)
			}
		}
		return results
	}
}
