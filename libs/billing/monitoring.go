package billing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/alert"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// sweepPage is the page size of the periodic sweeps.
const sweepPage = 500

// TrackUsage records that the subscriptions subs of customer c received
// events (lago Events::PostProcessService#track_subscription_activity and
// Customer#flag_wallets_for_refresh, and the events processor's refresh
// flag, Subscriptions::FlagRefreshedService): it enqueues each
// subscription's [kernel.JobProcessSubscriptionActivity] for the end of
// the current [kernel.ActivityInterval] and, when the customer has an
// active wallet, the refresh of the customer's ongoing balances. It runs
// in the transaction carried by ctx, or in a new one.
//
// Lago records an activity only when the organization uses lifetime usage
// or progressive billing or the subscription has alerts
// (track_subscription_activity_service.rb:23-32); with no premium gates
// (domain §4.1) every subscription keeps a lifetime usage, so every one is
// tracked.
func (e *Engine) TrackUsage(ctx context.Context, org core.OrganizationID, c core.CustomerID, subs []core.SubscriptionID) error {
	now := e.d.Now()
	return e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		locs := map[core.CustomerID]*time.Location{}
		var queueNow time.Time
		for _, id := range subs {
			if id.IsZero() {
				continue
			}
			if queueNow.IsZero() {
				var err error
				if queueNow, err = kernel.QueueNow(ctx, r, now); err != nil {
					return err
				}
			}
			key, at := kernel.ActivityKey(id, queueNow)
			if err := kernel.Enqueue(ctx, r, org, kernel.JobProcessSubscriptionActivity, kernel.SubscriptionArgs{SubscriptionID: id}, key, at); err != nil {
				return err
			}
			if err := recordEventReceived(ctx, r, org, c, id, now, locs); err != nil {
				return err
			}
		}
		if c.IsZero() {
			return nil
		}
		ws, err := r.Wallets.ListActiveForApplication(ctx, org, c)
		if err != nil {
			return fmt.Errorf("list active wallets of customer %s: %w", c, err)
		}
		if len(ws) == 0 {
			return nil
		}
		return kernel.EnqueueWalletRefresh(ctx, r, ws[0])
	})
}

// recordEventReceived sets the last_received_event_on of the subscription
// id to the day of now in the time zone of its customer, c or, when c is
// zero, the subscription's (lago
// UsageMonitoring::TrackSubscriptionActivityService,
// app/services/usage_monitoring/track_subscription_activity_service.rb:19-20,
// with the date of app/services/events/post_process_service.rb:98); the
// repository writes it only for an active subscription and only when it
// changes. locs caches the customers' time zones.
func recordEventReceived(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, id core.SubscriptionID, now time.Time, locs map[core.CustomerID]*time.Location) error {
	if c.IsZero() {
		sub, err := r.Subscriptions.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", id, err)
		}
		if sub.Status != subscription.StatusActive {
			return nil
		}
		c = sub.CustomerID
	}
	loc, ok := locs[c]
	if !ok {
		env, err := kernel.LoadCustomerEnv(ctx, r, org, c, core.BillingEntityID{})
		if err != nil {
			return err
		}
		loc = env.Location
		locs[c] = loc
	}
	if err := r.Subscriptions.RecordEventReceived(ctx, org, id, core.DateOf(now, loc)); err != nil {
		return fmt.Errorf("record event received by subscription %s: %w", id, err)
	}
	return nil
}

// AlertSaved schedules the evaluation of alert a after it was created or
// updated (lago UsageMonitoring::CreateAlertService and UpdateAlertService
// track_subscription_activity and process_wallet_alerts,
// app/services/usage_monitoring/create_alert_service.rb:136-151 and
// update_alert_service.rb:84-103): the activity of its subscription when
// the subscription is active, the [kernel.JobProcessWalletAlerts] of its
// wallet when the wallet is active. It runs in the transaction carried by
// ctx, or in a new one, so the jobs become visible with the saved alert.
func (e *Engine) AlertSaved(ctx context.Context, org core.OrganizationID, a *alert.Alert) error {
	return e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if !a.WalletID.IsZero() {
			w, err := r.Wallets.Get(ctx, org, a.WalletID)
			if errors.Is(err, core.ErrNotFound) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("load wallet %s: %w", a.WalletID, err)
			}
			if !w.Active() {
				return nil
			}
			return kernel.EnqueueWalletAlerts(ctx, r, w)
		}
		if a.SubscriptionExternalID == "" {
			return nil
		}
		s, err := r.Subscriptions.GetByExternalID(ctx, org, a.SubscriptionExternalID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", a.SubscriptionExternalID, err)
		}
		if s.Status != subscription.StatusActive {
			return nil
		}
		queueNow, err := kernel.QueueNow(ctx, r, e.d.Now())
		if err != nil {
			return err
		}
		key, at := kernel.ActivityKey(s.ID, queueNow)
		return kernel.Enqueue(ctx, r, org, kernel.JobProcessSubscriptionActivity, kernel.SubscriptionArgs{SubscriptionID: s.ID}, key, at)
	})
}

// ProcessSubscriptionActivity processes the usage activity of subscription
// id (lago UsageMonitoring::ProcessSubscriptionActivityService): unless the
// subscription is no longer active, it recomputes the lifetime usage and
// bills the crossed usage thresholds ([Engine.CheckUsageThresholds]), then
// evaluates each usage alert of the subscription. The
// billable_metric_lifetime_usage_units alerts, whose usage since the start
// of the subscription is costly, are evaluated by a
// [kernel.JobProcessLifetimeUsageAlert] enqueued [kernel.ActivityInterval]
// later (lago process_subscription_activity_service.rb:46-47). The current
// usage, without taxes, is computed once and shared by the lifetime usage
// and the current usage alerts (lago
// process_subscription_activity_service.rb:28-31, 78-86); when it fails,
// both are skipped and the lifetime usage amount and lifetime units alerts
// still run. A failure does not stop the alerts: the first error is
// returned once every alert was processed.
func (e *Engine) ProcessSubscriptionActivity(ctx context.Context, org core.OrganizationID, id core.SubscriptionID) error {
	var sub *subscription.Subscription
	var alerts []*alert.Alert
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		s, err := r.Subscriptions.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", id, err)
		}
		sub = s
		if s.Status != subscription.StatusActive {
			return nil
		}
		alerts, err = r.Alerts.ListBySubscription(ctx, org, s.ExternalID)
		if err != nil {
			return fmt.Errorf("list alerts of subscription %s: %w", s.ExternalID, err)
		}
		return nil
	})
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil || sub.Status != subscription.StatusActive {
		return err
	}
	var errs []error
	var current *usage.Usage
	err = e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		u, err := e.Usage.CurrentUsage(ctx, r, org, id, usage.Options{SkipTaxes: true})
		current = u
		return err
	})
	if err != nil {
		current = nil
		errs = append(errs, fmt.Errorf("current usage of subscription %s: %w", id, err))
	} else if _, err := e.checkUsageThresholds(ctx, org, id, current); err != nil {
		errs = append(errs, err)
	}
	for _, a := range alerts {
		var value func(ctx context.Context, r port.Repos, a *alert.Alert) (money.Decimal, bool, error)
		switch {
		case a.Type == alert.TypeBillableMetricLifetimeUsageUnits:
			err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
				return kernel.EnqueueLifetimeUsageAlert(ctx, r, org, a.ID, id, e.d.Now())
			})
			if err != nil {
				errs = append(errs, err)
			}
			continue
		case a.Type == alert.TypeLifetimeUsageAmount:
			value = func(ctx context.Context, r port.Repos, _ *alert.Alert) (money.Decimal, bool, error) {
				lu, err := r.LifetimeUsages.GetBySubscription(ctx, org, id)
				if errors.Is(err, core.ErrNotFound) {
					return money.Decimal{}, false, nil
				}
				if err != nil {
					return money.Decimal{}, false, fmt.Errorf("load lifetime usage of subscription %s: %w", id, err)
				}
				return money.NewFromInt(lu.Total()), true, nil
			}
		case a.Type.IsCurrentUsage():
			if current == nil {
				continue
			}
			u := current
			value = func(ctx context.Context, r port.Repos, a *alert.Alert) (money.Decimal, bool, error) {
				return currentUsageValue(ctx, r, a, u)
			}
		default:
			continue
		}
		if err := e.evaluateAlert(ctx, org, a.ID, sub.ID, core.BillableMetricID{}, value); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// ProcessLifetimeUsageAlert evaluates the billable_metric_lifetime_usage_units
// alert id of subscription sub against the usage of the subscription since
// it started (lago UsageMonitoring::ProcessLifetimeUsageAlertService): it
// returns at once when the alert is gone or of another type, the
// subscription is no longer active or its plan has no charge of the
// alert's metric. The usage of those charges, without taxes, is computed
// holding no lock; the alert is then evaluated only if it still exists and
// still watches the same metric, as the usage measured that metric (lago
// process_lifetime_usage_alert_service.rb:33-40, and the stale_metric?
// check of process_alert_service.rb:21).
func (e *Engine) ProcessLifetimeUsageAlert(ctx context.Context, org core.OrganizationID, id core.AlertID, sub core.SubscriptionID) error {
	var (
		metric  core.BillableMetricID
		charges []core.ChargeID
	)
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		a, err := r.Alerts.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load alert %s: %w", id, err)
		}
		if a.Type != alert.TypeBillableMetricLifetimeUsageUnits {
			return nil
		}
		s, err := r.Subscriptions.Get(ctx, org, sub)
		if err != nil {
			return fmt.Errorf("load subscription %s: %w", sub, err)
		}
		if s.Status != subscription.StatusActive {
			return nil
		}
		cs, err := r.Charges.ListByPlan(ctx, org, s.PlanID)
		if err != nil {
			return fmt.Errorf("list charges of plan %s: %w", s.PlanID, err)
		}
		for _, c := range cs {
			if c.BillableMetricID == a.BillableMetricID {
				charges = append(charges, c.ID)
			}
		}
		metric = a.BillableMetricID
		return nil
	})
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil || len(charges) == 0 {
		return err
	}
	var u *usage.Usage
	err = e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		u, err = e.Usage.CurrentUsage(ctx, r, org, sub, usage.Options{SkipTaxes: true, FullUsage: true, ChargeIDs: charges})
		return err
	})
	if err != nil {
		return fmt.Errorf("full usage of subscription %s: %w", sub, err)
	}
	return e.evaluateAlert(ctx, org, id, sub, metric, func(ctx context.Context, r port.Repos, a *alert.Alert) (money.Decimal, bool, error) {
		return currentUsageValue(ctx, r, a, u)
	})
}

// ProcessWalletAlerts evaluates every wallet alert of wallet id against
// the wallet's balances (lago UsageMonitoring::ProcessWalletAlertsService).
// It stops at the first failure.
func (e *Engine) ProcessWalletAlerts(ctx context.Context, org core.OrganizationID, id core.WalletID) error {
	var (
		w      *wallet.Wallet
		alerts []*alert.Alert
	)
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if w, err = r.Wallets.Get(ctx, org, id); err != nil {
			return fmt.Errorf("load wallet %s: %w", id, err)
		}
		if alerts, err = r.Alerts.ListByWallet(ctx, org, id); err != nil {
			return fmt.Errorf("list alerts of wallet %s: %w", id, err)
		}
		return nil
	})
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, a := range alerts {
		if !a.Type.IsWalletType() {
			continue
		}
		err := e.evaluateAlert(ctx, org, a.ID, core.SubscriptionID{}, core.BillableMetricID{}, func(_ context.Context, _ port.Repos, a *alert.Alert) (money.Decimal, bool, error) {
			v, ok := walletValue(a, w)
			return v, ok, nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// walletValue is the value of a wallet alert a for wallet w (lago find_value
// of the wallet alerts, app/models/usage_monitoring/wallet_*_alert.rb): its
// balance or ongoing balance, in minor units or credits.
func walletValue(a *alert.Alert, w *wallet.Wallet) (money.Decimal, bool) {
	switch a.Type {
	case alert.TypeWalletBalanceAmount:
		return money.NewFromInt(w.BalanceAmount), true
	case alert.TypeWalletCreditsBalance:
		return w.CreditsBalance, true
	case alert.TypeWalletOngoingBalanceAmount:
		return money.NewFromInt(w.OngoingBalanceAmount), true
	case alert.TypeWalletCreditsOngoingBalance:
		return w.CreditsOngoingBalance, true
	default:
		return money.Decimal{}, false
	}
}

// currentUsageValue is the value of a current-usage alert a in u (lago
// find_value of CurrentUsageAmountAlert and the billable metric alerts):
// the usage amount, or the largest amount or units among the fees of the
// charges of the alert's metric; false when the plan has no such charge.
func currentUsageValue(ctx context.Context, r port.Repos, a *alert.Alert, u *usage.Usage) (money.Decimal, bool, error) {
	if a.Type == alert.TypeCurrentUsageAmount {
		return money.NewFromInt(u.Amount), true, nil
	}
	charges, err := r.Charges.ListByMetric(ctx, a.OrganizationID, a.BillableMetricID)
	if err != nil {
		return money.Decimal{}, false, fmt.Errorf("list charges of billable metric %s: %w", a.BillableMetricID, err)
	}
	ids := make([]core.ChargeID, len(charges))
	for i, c := range charges {
		ids[i] = c.ID
	}
	var out money.Decimal
	found := false
	for _, f := range u.Fees {
		if f.ChargeID.IsZero() || !slices.Contains(ids, f.ChargeID) {
			continue
		}
		v := f.Units
		if a.Type == alert.TypeBillableMetricCurrentUsageAmount {
			v = money.NewFromInt(f.Amount)
		}
		if !found || v.Cmp(out) > 0 {
			out = v
		}
		found = true
	}
	return out, found, nil
}

// evaluateAlert evaluates the alert id against the value value reads, in
// one transaction holding the alert's lock (lago
// UsageMonitoring::ProcessAlertService): the value and the thresholds come
// from the alert as stored under the lock. Crossed thresholds are recorded
// as a triggered alert of subscription sub (zero for a wallet alert) and
// alert.triggered is emitted; the value becomes the alert's previous value.
// A deleted alert is skipped, and so is one whose metric is no longer
// metric when metric is set (lago stale_metric?,
// app/services/usage_monitoring/process_alert_service.rb:21,41-43).
func (e *Engine) evaluateAlert(ctx context.Context, org core.OrganizationID, id core.AlertID, sub core.SubscriptionID, metric core.BillableMetricID,
	value func(ctx context.Context, r port.Repos, a *alert.Alert) (money.Decimal, bool, error),
) error {
	return e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if err := kernel.LockAlert(ctx, r, org, id); err != nil {
			return err
		}
		a, err := r.Alerts.Get(ctx, org, id)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load alert %s: %w", id, err)
		}
		if !metric.IsZero() && a.BillableMetricID != metric {
			return nil
		}
		now := e.d.Now()
		current, ok, err := value(ctx, r, a)
		if err != nil {
			return err
		}
		if ok {
			if t := a.Evaluate(core.NewID[core.TriggeredAlertID](e.d.IDs), sub, current, now); t != nil {
				if err := r.TriggeredAlerts.Create(ctx, t); err != nil {
					return fmt.Errorf("record triggered alert of %s: %w", id, err)
				}
				if err := kernel.Emit(ctx, e.d, org, webhook.EventTypeAlertTriggered, "triggered_alert", t.ID.UUID(), t); err != nil {
					return err
				}
			}
		} else {
			a.LastProcessedAt = &now
			a.UpdatedAt = now
		}
		if err := r.Alerts.Update(ctx, a); err != nil {
			return fmt.Errorf("update alert %s: %w", id, err)
		}
		return nil
	})
}

// RefreshLifetimeUsages enqueues the refresh of every lifetime usage of
// org flagged for recalculation (lago Clock::RefreshLifetimeUsagesJob,
// app/jobs/clock/refresh_lifetime_usages_job.rb:9-19) and returns their
// number. Like lago it sweeps only the records whose invoiced usage is
// flagged; the current usage is refreshed by the usage signals.
func (e *Engine) RefreshLifetimeUsages(ctx context.Context, org core.OrganizationID) (int, error) {
	n := 0
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		page := core.PageRequest{Size: sweepPage}
		for {
			p, err := r.LifetimeUsages.ListToRecalculate(ctx, org, page)
			if err != nil {
				return fmt.Errorf("list lifetime usages to recalculate: %w", err)
			}
			for _, l := range p.Items {
				args := kernel.SubscriptionArgs{SubscriptionID: l.SubscriptionID}
				if err := kernel.Enqueue(ctx, r, org, kernel.JobRefreshLifetimeUsage, args, "subscription:"+l.SubscriptionID.String(), e.d.Now()); err != nil {
					return err
				}
				n++
			}
			if p.NextToken == "" {
				return nil
			}
			page.Token = p.NextToken
		}
	})
	return n, err
}

// RefreshOngoingBalances enqueues the refresh of the ongoing balances of
// every customer of org flagged as awaiting it that has an active wallet
// (lago Clock::RefreshWalletsOngoingBalanceJob,
// app/jobs/clock/refresh_wallets_ongoing_balance_job.rb:10,
// Customer.with_active_wallets.awaiting_wallet_refresh) and returns the
// number of customers. Lago also leaves out the customers with a tax error
// detail (without_tax_errors); Tanvo records no error details yet.
func (e *Engine) RefreshOngoingBalances(ctx context.Context, org core.OrganizationID) (int, error) {
	n := 0
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		page := core.PageRequest{Size: sweepPage}
		for {
			p, err := r.Customers.ListAwaitingWalletRefresh(ctx, org, page)
			if err != nil {
				return fmt.Errorf("list customers awaiting a wallet refresh: %w", err)
			}
			for _, c := range p.Items {
				ws, err := r.Wallets.ListActiveForApplication(ctx, org, c)
				if err != nil {
					return fmt.Errorf("list active wallets of customer %s: %w", c, err)
				}
				if len(ws) == 0 {
					continue
				}
				if err := kernel.EnqueueWalletRefreshJob(ctx, r, ws[0]); err != nil {
					return err
				}
				n++
			}
			if p.NextToken == "" {
				return nil
			}
			page.Token = p.NextToken
		}
	})
	return n, err
}
