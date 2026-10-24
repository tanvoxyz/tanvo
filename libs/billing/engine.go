package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/contracts"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/customers"
	"github.com/tanvoxyz/tanvo/libs/billing/dailyusage"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/dunning"
	"github.com/tanvoxyz/tanvo/libs/billing/estimates"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/quotes"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Deps are the dependencies of the engine.
type Deps = kernel.Deps

// Engine is the billing engine: the services wired to one another.
type Engine struct {
	// Fees computes fees.
	Fees *fees.Service
	// Usage computes usage and checks thresholds.
	Usage *usage.Service
	// Deductions applies progressive billing credits, coupons, credit
	// notes and wallet credits, and administers coupons.
	Deductions *deduct.Service
	// CreditNotes manages credit notes.
	CreditNotes *creditnotes.Service
	// Wallets manages wallets and their transactions.
	Wallets *wallets.Service
	// Invoices generates invoices.
	Invoices *invoicing.Service
	// Lifecycle creates, changes and terminates subscriptions.
	Lifecycle *lifecycle.Service
	// Plans deletes plans.
	Plans *plans.Service
	// Estimates estimates the fees of events.
	Estimates *estimates.Service
	// DailyUsages stores the daily usage snapshots.
	DailyUsages *dailyusage.Service
	// Dunning manages and runs the dunning campaigns.
	Dunning *dunning.Service
	// Customers manages customers and terminates the relations of the
	// deleted ones ([customers.JobTerminateRelations]).
	Customers *customers.Service
	// Quotes runs the quotes, order forms and orders, and executes the
	// orders ([kernel.JobExecuteOrder]).
	Quotes *quotes.Service
	// Contracts manages the contracts, their rate cards and billing
	// segments (domain §21.4).
	Contracts *contracts.Service

	d                    *kernel.Deps
	terminationAlertDays []int
}

// New returns an engine over d configured by opts.
func New(d Deps, opts ...Option) (*Engine, error) {
	if err := d.Check(); err != nil {
		return nil, fmt.Errorf("billing engine: %w", err)
	}
	deps := &d
	u := usage.New(deps)
	w := wallets.New(deps).WithUsage(u)
	cn := creditnotes.New(deps)
	cn.SetWalletVoider(w)
	inv := invoicing.New(deps).WithCreditNotes(cn).WithCreditInvoices(w)
	lc := lifecycle.New(deps).WithBiller(inv).WithCreditNotes(cn)
	inv.WithStatusResolver(lc)
	e := &Engine{
		Fees:        fees.New(deps),
		Usage:       u,
		Deductions:  deduct.New(deps),
		CreditNotes: cn,
		Wallets:     w,
		Invoices:    inv,
		Lifecycle:   lc,
		Plans:       plans.New(deps, lc, inv),
		Estimates:   estimates.New(deps),
		DailyUsages: dailyusage.New(deps, u),
		Dunning:     dunning.New(deps),
		d:           deps,

		terminationAlertDays: defaultTerminationAlertDays(),
	}
	cs, err := customers.New(deps, customers.Relations{Lifecycle: lc, Coupons: e.Deductions, Wallets: w})
	if err != nil {
		return nil, fmt.Errorf("billing engine: customers: %w", err)
	}
	e.Customers = cs
	qs, err := quotes.New(deps, quotes.Services{
		Subscriptions: subscriptions.New(deps, lc, u, e.Plans), Lifecycle: lc, Plans: e.Plans,
		Invoices: inv, Coupons: e.Deductions, Wallets: w,
	})
	if err != nil {
		return nil, fmt.Errorf("billing engine: quotes: %w", err)
	}
	e.Quotes = qs
	ct, err := contracts.New(deps)
	if err != nil {
		return nil, fmt.Errorf("billing engine: contracts: %w", err)
	}
	e.Contracts = ct
	inv.WithContractSegments(ct)
	for _, o := range opts {
		o(e)
	}
	return e, nil
}

// JobKinds returns every job kind [Engine.HandleJob] runs: those of
// [kernel.JobKinds], [customers.JobTerminateRelations],
// [customers.JobVIESCheck] and [invoicing.JobFinalizePendingVIES].
func (e *Engine) JobKinds() []string {
	return append(kernel.JobKinds(), customers.JobTerminateRelations, customers.JobVIESCheck, invoicing.JobFinalizePendingVIES)
}

// BillSubscriptions runs the periodic billing of organization org at
// billingAt (lago Subscriptions::OrganizationBillingService) with
// [invoicing.Service.BillSubscriptions]: every subscription due that day
// is billed, or, when it has a pending downgrade, gets a
// [kernel.JobSwapDowngrade] (lago Subscriptions::TerminateJob). It returns
// how many billing groups were enqueued.
func (e *Engine) BillSubscriptions(ctx context.Context, org core.OrganizationID, billingAt time.Time) (int, error) {
	return e.Invoices.BillSubscriptions(ctx, org, billingAt)
}

// SwapDowngrade runs a [kernel.JobSwapDowngrade]: in one transaction it
// terminates subscription id at billingAt and starts its pending
// downgrade; a subscription whose downgrade is no longer pending (already
// swapped, or changed since the scan) is left alone.
func (e *Engine) SwapDowngrade(ctx context.Context, org core.OrganizationID, id core.SubscriptionID, billingAt time.Time) (bool, error) {
	var swapped bool
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		swapped, err = e.Lifecycle.SwapDowngrade(ctx, r, org, id, billingAt)
		return err
	})
	return swapped, err
}

// CheckUsageThresholds recomputes the lifetime usage of subscription id
// and, when it crosses usage thresholds, bills them with a progressive
// billing invoice and emits subscription.usage_threshold_reached for each,
// in one transaction (lago LifetimeUsages::RecalculateAndCheckService). It
// returns the progressive billing invoice, or nil.
func (e *Engine) CheckUsageThresholds(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) (*invoice.Invoice, error) {
	return e.checkUsageThresholds(ctx, org, id, nil)
}

// checkUsageThresholds is [Engine.CheckUsageThresholds] with the current
// usage of the subscription already computed without taxes, or nil.
func (e *Engine) checkUsageThresholds(ctx context.Context, org core.OrganizationID, id core.SubscriptionID, current *usage.Usage) (*invoice.Invoice, error) {
	var out *invoice.Invoice
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := kernel.LockSubscription(ctx, r, org, id); err != nil {
			return err
		}
		c, err := e.Usage.RefreshAndCheck(ctx, r, org, id, current)
		if err != nil {
			return err
		}
		if len(c.Thresholds) == 0 {
			return nil
		}
		sub, err := r.Subscriptions.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", id, err)
		}
		out, err = e.Invoices.ProgressiveBilling(ctx, r, sub, c.Thresholds, c.LifetimeUsage)
		if err != nil {
			return err
		}
		for _, t := range c.Thresholds {
			if err := kernel.Emit(ctx, e.d, org, webhook.EventTypeSubscriptionUsageThresholdReached, "subscription", sub.ID.UUID(), thresholdReached{Subscription: sub, UsageThreshold: t}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// thresholdReached is the payload of subscription.usage_threshold_reached.
type thresholdReached struct {
	Subscription   *subscription.Subscription `json:"subscription"`
	UsageThreshold *catalog.UsageThreshold    `json:"usage_threshold"`
}

// HandleJob runs a billing job and reports whether j is one. The periodic
// run goes through [Engine.BillSubscriptions] so pending downgrades swap
// first; the other kinds go to the service that owns them.
//
// The per-organization jobs a clock job dispatches (libs/jobs Periodic)
// carry the clock tick as their instant (BillSubscriptionsArgs.BillingAt,
// InstantArgs.At); a job without it is refused rather than run at the
// worker's "now", since a backlog may run it on a later day.
func (e *Engine) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	switch j.Kind {
	case kernel.JobActivatePendingSubscriptions, kernel.JobTerminateEndedSubscriptions, kernel.JobEndTrials,
		kernel.JobFinalizeDueDrafts, kernel.JobWalletIntervalTopUps, kernel.JobExpireWallets, kernel.JobTerminateCoupons:
		var a kernel.InstantArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		if a.At.IsZero() {
			return true, fmt.Errorf("%s: %w: no instant", j.Kind, core.ErrInvalidValue)
		}
		if j.Kind == kernel.JobTerminateCoupons {
			_, err := e.Deductions.TerminateExpiredCoupons(ctx, j.OrganizationID, a.At)
			return true, err
		}
	case kernel.JobMarkOverdueInvoices, kernel.JobAlertTerminatingSubscriptions, kernel.JobExpireIncompleteSubscriptions,
		kernel.JobExpireOrderForms, kernel.JobValidateEvents, kernel.JobCleanRecordDeletions:
		var a kernel.InstantArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		if a.At.IsZero() {
			return true, fmt.Errorf("%s: %w: no instant", j.Kind, core.ErrInvalidValue)
		}
		var err error
		switch j.Kind {
		case kernel.JobMarkOverdueInvoices:
			_, err = e.MarkOverdueInvoices(ctx, j.OrganizationID, a.At)
		case kernel.JobAlertTerminatingSubscriptions:
			_, err = e.AlertTerminatingSubscriptions(ctx, j.OrganizationID, a.At)
		case kernel.JobExpireIncompleteSubscriptions:
			_, err = e.ExpireIncompleteSubscriptions(ctx, j.OrganizationID, a.At)
		case kernel.JobValidateEvents:
			_, err = e.ValidateEvents(ctx, j.OrganizationID, a.At)
		case kernel.JobCleanRecordDeletions:
			_, err = e.CleanRecordDeletions(ctx, j.OrganizationID, a.At)
		default:
			_, err = e.ExpireOrderForms(ctx, j.OrganizationID, a.At)
		}
		return true, err
	case kernel.JobProcessDunningCampaigns:
		var a kernel.InstantArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, e.Dunning.ProcessCampaigns(ctx, j.OrganizationID, a.At)
	case kernel.JobProcessDunningCustomer:
		var a kernel.CustomerArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, e.Dunning.ProcessCustomer(ctx, j.OrganizationID, a.CustomerID)
	case kernel.JobProcessDunningAttempt:
		var a kernel.DunningAttemptArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, e.Dunning.ProcessAttempt(ctx, j.OrganizationID, a)
	case kernel.JobMarkInvoiceOverdue:
		var a kernel.InvoiceArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		// A refused mark (paid, not due, dispute lost, …) completes the
		// job: lago calls the service without raising
		// (app/jobs/invoices/payments/mark_overdue_job.rb:11-13).
		_, err := e.MarkInvoiceOverdue(ctx, j.OrganizationID, a.InvoiceID)
		return true, ignoreRuleError(err)
	case kernel.JobExpireIncompleteSubscription:
		var a kernel.SubscriptionArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		_, err := e.ExpireIncompleteSubscription(ctx, j.OrganizationID, a.SubscriptionID)
		return true, err
	case kernel.JobResolvePaymentGate:
		var a kernel.PaymentGateArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, e.Lifecycle.ResolvePaymentGate(ctx, j.OrganizationID, a.SubscriptionID, a.InvoiceID, a.PaymentStatus)
	case kernel.JobExpireOrderForm:
		var a kernel.OrderFormArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		// A voided or signed order form completes the job. Lago raises
		// the forbidden failure (app/jobs/order_forms/expire_order_form_job.rb:12)
		// into a job that is never retried (app/jobs/application_job.rb:4),
		// so the refusal ends it either way; here it does not fill the
		// retry queue with a job that cannot succeed.
		_, err := e.ExpireOrderForm(ctx, j.OrganizationID, a.OrderFormID)
		return true, ignoreRuleError(err)
	case kernel.JobSwapDowngrade:
		var a kernel.SwapDowngradeArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		if a.BillingAt.IsZero() {
			return true, fmt.Errorf("%s: %w: no billing instant", j.Kind, core.ErrInvalidValue)
		}
		_, err := e.SwapDowngrade(ctx, j.OrganizationID, a.SubscriptionID, a.BillingAt)
		return true, err
	case kernel.JobBillSubscriptions:
		var a kernel.BillSubscriptionsArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		if a.BillingAt.IsZero() {
			return true, fmt.Errorf("%s: %w: no billing instant", j.Kind, core.ErrInvalidValue)
		}
		_, err := e.BillSubscriptions(ctx, j.OrganizationID, a.BillingAt)
		return true, err
	case kernel.JobCheckUsageThresholds, kernel.JobRefreshLifetimeUsage:
		var a kernel.SubscriptionArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		_, err := e.CheckUsageThresholds(ctx, j.OrganizationID, a.SubscriptionID)
		return true, err
	case kernel.JobProcessSubscriptionActivity:
		var a kernel.SubscriptionArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, e.ProcessSubscriptionActivity(ctx, j.OrganizationID, a.SubscriptionID)
	case kernel.JobProcessLifetimeUsageAlert:
		var a kernel.LifetimeUsageAlertArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, e.ProcessLifetimeUsageAlert(ctx, j.OrganizationID, a.AlertID, a.SubscriptionID)
	case kernel.JobProcessWalletAlerts:
		var a kernel.WalletArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		return true, e.ProcessWalletAlerts(ctx, j.OrganizationID, a.WalletID)
	case kernel.JobRefreshLifetimeUsages:
		_, err := e.RefreshLifetimeUsages(ctx, j.OrganizationID)
		return true, err
	case kernel.JobRefreshOngoingBalances:
		_, err := e.RefreshOngoingBalances(ctx, j.OrganizationID)
		return true, err
	case kernel.JobRefundCreditNote, kernel.JobReportCreditNoteTaxes:
		var a kernel.CreditNoteArgs
		if err := decode(j, &a); err != nil {
			return true, err
		}
		if j.Kind == kernel.JobRefundCreditNote {
			_, err := e.CreditNotes.Refund(ctx, j.OrganizationID, a.CreditNoteID)
			return true, err
		}
		return true, e.CreditNotes.ReportTaxes(ctx, j.OrganizationID, a.CreditNoteID)
	}
	for _, h := range []func(context.Context, port.Job) (bool, error){e.Invoices.HandleJob, e.Lifecycle.HandleJob, e.Wallets.HandleJob, e.Plans.HandleJob, e.DailyUsages.HandleJob, e.Customers.HandleJob, e.Quotes.HandleJob, e.Contracts.HandleJob, e.Invoices.HandleSegmentJob} {
		if ok, err := h(ctx, j); ok {
			return true, err
		}
	}
	return false, nil
}

// decode decodes the args of j into v.
func decode(j port.Job, v any) error {
	if err := json.Unmarshal(j.Args, v); err != nil {
		return fmt.Errorf("decode %s args: %w", j.Kind, err)
	}
	return nil
}

// ignoreRuleError returns nil for a *core.RuleError, the refusal of an
// operation whose state forbids it, and err otherwise.
func ignoreRuleError(err error) error {
	if _, ok := errors.AsType[*core.RuleError](err); ok {
		return nil
	}
	return err
}
