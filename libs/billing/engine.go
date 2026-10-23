package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
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

	d *kernel.Deps
}

// New returns an engine over d.
func New(d Deps) (*Engine, error) {
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
	return &Engine{
		Fees:        fees.New(deps),
		Usage:       u,
		Deductions:  deduct.New(deps),
		CreditNotes: cn,
		Wallets:     w,
		Invoices:    inv,
		Lifecycle:   lc,
		d:           deps,
	}, nil
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
	var out *invoice.Invoice
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := kernel.LockSubscription(ctx, r, org, id); err != nil {
			return err
		}
		c, err := e.Usage.RefreshAndCheck(ctx, r, org, id)
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
	for _, h := range []func(context.Context, port.Job) (bool, error){e.Invoices.HandleJob, e.Lifecycle.HandleJob, e.Wallets.HandleJob} {
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
