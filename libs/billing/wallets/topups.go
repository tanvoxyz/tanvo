package wallets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Threshold top-up bursts are logged (lago ThresholdTopUpService).
const (
	burstTopUps = 3
	burstWindow = 10 * time.Minute
)

// ThresholdTopUp runs the active threshold rule of a wallet (lago
// Wallets::ThresholdTopUpService, the [kernel.JobWalletThresholdTopUp]
// handler) when its ongoing credits, plus the credits of pending
// purchases, are at or below the threshold. It is skipped while a purchase
// of the rule is pending, and a paid top-up is suppressed for an hour after
// a declined one unless a purchase settled since (domain §14.3).
func (s *Service) ThresholdTopUp(ctx context.Context, org core.OrganizationID, id core.WalletID) ([]*wallet.Transaction, error) {
	var out []*wallet.Transaction
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ts, err := s.thresholdTopUp(ctx, r, org, id)
		out = ts
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) thresholdTopUp(ctx context.Context, r port.Repos, org core.OrganizationID, id core.WalletID) ([]*wallet.Transaction, error) {
	now := s.d.Now()
	// The checks (threshold, pending purchases, back-off) and the top-up
	// run under the lock, so that two runs cannot both top up one
	// crossing (lago: unique CreateJob per wallet and source).
	w, err := lockWallet(ctx, r, org, id)
	if err != nil {
		return nil, err
	}
	if !w.Active() {
		return nil, nil
	}
	rules, err := r.RecurringTransactionRules.ListActiveThreshold(ctx, org, w.ID, now)
	if err != nil {
		return nil, fmt.Errorf("list rules of wallet %s: %w", w.ID, err)
	}
	if len(rules) == 0 {
		return nil, nil
	}
	rule := rules[0]
	if !rule.ThresholdReached(w.CreditsOngoingBalance) {
		return nil, nil
	}
	if _, err := r.WalletTransactions.GetPendingPurchase(ctx, org, rule.ID); err == nil {
		return nil, nil
	} else if !errors.Is(err, core.ErrNotFound) {
		return nil, fmt.Errorf("pending purchase of rule %s: %w", rule.ID, err)
	}
	ts, err := kernel.All(func(p core.PageRequest) (core.Page[*wallet.Transaction], error) {
		return r.WalletTransactions.List(ctx, org, wallet.TransactionFilter{WalletID: w.ID}, p)
	})
	if err != nil {
		return nil, fmt.Errorf("list transactions of wallet %s: %w", w.ID, err)
	}
	pending := money.Zero
	for _, t := range ts {
		if t.Status == wallet.SettlementStatusPending {
			pending = pending.Add(t.CreditAmount)
		}
	}
	if pending.Add(w.CreditsOngoingBalance).Cmp(*rule.ThresholdCredits) > 0 {
		return nil, nil
	}
	paid := rule.ComputePaidCredits(w, w.CreditsOngoingBalance, pending)
	if paid.IsPositive() && backingOffAfterDecline(ts, now) {
		return nil, nil
	}
	if n := automaticTopUpsSince(ts, now.Add(-burstWindow)); n >= burstTopUps {
		s.d.Logger.WarnContext(ctx, "automatic wallet top-up burst", "wallet_id", w.ID, "organization_id", org, "top_ups", n)
	}
	return s.createTransactions(ctx, r, org, ruleTopUp(w, rule, wallet.TransactionSourceThreshold, paid))
}

// backingOffAfterDecline reports whether a threshold purchase failed less
// than an hour ago with no purchase settled since.
func backingOffAfterDecline(ts []*wallet.Transaction, now time.Time) bool {
	var last *time.Time
	for _, t := range ts {
		if automaticPurchase(t) && t.FailedAt != nil && (last == nil || t.FailedAt.After(*last)) {
			last = t.FailedAt
		}
	}
	if !wallet.ThresholdSuppressed(last, now) {
		return false
	}
	for _, t := range ts {
		if t.Inbound() && t.TransactionStatus == wallet.TransactionStatusPurchased &&
			t.Status == wallet.SettlementStatusSettled && t.SettledAt != nil && !t.SettledAt.Before(*last) {
			return false
		}
	}
	return true
}

func automaticPurchase(t *wallet.Transaction) bool {
	return t.Source == wallet.TransactionSourceThreshold && t.Inbound() && t.TransactionStatus == wallet.TransactionStatusPurchased
}

func automaticTopUpsSince(ts []*wallet.Transaction, since time.Time) int {
	n := 0
	for _, t := range ts {
		if automaticPurchase(t) && !t.CreatedAt.Before(since) {
			n++
		}
	}
	return n
}

// ruleTopUp returns the top-up a rule run makes.
func ruleTopUp(w *wallet.Wallet, rule *wallet.RecurringTransactionRule, source wallet.TransactionSource, paid money.Decimal) *TransactionInput {
	granted := rule.ComputeGrantedCredits(w)
	irsp := rule.InvoiceRequiresSuccessfulPayment
	in := &TransactionInput{
		WalletID:                         w.ID,
		PaidCredits:                      &paid,
		GrantedCredits:                   &granted,
		Source:                           source,
		Name:                             rule.TransactionName,
		Metadata:                         rule.TransactionMetadata,
		InvoiceRequiresSuccessfulPayment: &irsp,
		IgnorePaidTopUpLimits:            rule.Method == wallet.RecurringMethodTarget || rule.IgnorePaidTopUpLimits,
		RecurringTransactionRuleID:       rule.ID,
	}
	if rule.PaymentMethodType != "" || !rule.PaymentMethodID.IsZero() {
		in.PaymentMethod = &PaymentMethod{Type: rule.PaymentMethodType, ID: rule.PaymentMethodID}
	}
	return in
}

// RunIntervalTopUps runs the interval rules due at the instant at (lago
// Wallets::CreateIntervalWalletTransactionsService, the
// [kernel.JobWalletIntervalTopUps] handler): a rule is due on the
// anniversaries of its start (or of the wallet's creation) in the
// customer's time zone, at most once a day, and not on the day the wallet
// was created. Each rule runs in its own transaction; the errors are
// joined.
func (s *Service) RunIntervalTopUps(ctx context.Context, org core.OrganizationID, at time.Time) ([]*wallet.Transaction, error) {
	var rules []*wallet.RecurringTransactionRule
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		rules, err = r.RecurringTransactionRules.ListActiveInterval(ctx, org, at)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list interval rules: %w", err)
	}
	var out []*wallet.Transaction
	var errs []error
	for _, rule := range rules {
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			ts, err := s.intervalTopUp(ctx, r, rule, at)
			out = append(out, ts...)
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("interval rule %s: %w", rule.ID, err))
		}
	}
	return out, errors.Join(errs...)
}

func (s *Service) intervalTopUp(ctx context.Context, r port.Repos, rule *wallet.RecurringTransactionRule, at time.Time) ([]*wallet.Transaction, error) {
	org := rule.OrganizationID
	w, err := lockWallet(ctx, r, org, rule.WalletID)
	if err != nil {
		return nil, err
	}
	if !w.Active() {
		return nil, nil
	}
	// The rule listed before the lock may have been changed or terminated
	// since, so the top-up follows the rule as read under the lock. Lago
	// computes the top-up from the listed rule and creates it in a later
	// job (app/services/wallets/create_interval_wallet_transactions_service.rb:8);
	// reading it again here only narrows that window.
	rule, err = r.RecurringTransactionRules.Get(ctx, org, rule.ID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("load recurring rule: %w", err)
	case rule.Trigger != wallet.RecurringTriggerInterval || !rule.CurrentlyActive(at):
		return nil, nil
	}
	env, err := kernel.LoadCustomerEnv(ctx, r, org, w.CustomerID, core.BillingEntityID{})
	if err != nil {
		return nil, err
	}
	anchor := w.CreatedAt
	if rule.StartedAt != nil {
		anchor = *rule.StartedAt
	}
	today := env.Today(at)
	if anchor.After(at) || env.Today(w.CreatedAt) == today || !IntervalDue(rule.Interval, env.Today(anchor), today) {
		return nil, nil
	}
	ts, err := kernel.All(func(p core.PageRequest) (core.Page[*wallet.Transaction], error) {
		return r.WalletTransactions.List(ctx, org, wallet.TransactionFilter{WalletID: w.ID, TransactionType: wallet.TransactionTypeInbound}, p)
	})
	if err != nil {
		return nil, fmt.Errorf("list transactions of wallet %s: %w", w.ID, err)
	}
	if intervalApplied(ts, env, today) {
		return nil, nil
	}
	paid := rule.ComputePaidCredits(w, w.CreditsOngoingBalance, money.Zero)
	in := ruleTopUp(w, rule, wallet.TransactionSourceInterval, paid)
	if rule.Method == wallet.RecurringMethodTarget && paid.IsZero() && in.GrantedCredits.IsZero() {
		return nil, nil
	}
	return s.createTransactions(ctx, r, org, in)
}

// intervalApplied reports whether the interval top-up due on the local
// date today already ran: an interval purchase or grant of the wallet was
// created on a local date in [today, today + 7 days). Lago checks the
// creation date equals today, which holds when the clock job runs on its
// own day; a dispatched job may run later than its tick (libs/jobs
// Periodic), so a retry of a late run must still find the transaction it
// made. Seven days is the shortest interval, so the window never reaches
// the next due date.
func intervalApplied(ts []*wallet.Transaction, env *kernel.CustomerEnv, today core.Date) bool {
	end := today.AddDays(7)
	for _, t := range ts {
		if t.Source != wallet.TransactionSourceInterval || !t.Inbound() {
			continue
		}
		if d := env.Today(t.CreatedAt); !d.Before(today) && d.Before(end) {
			return true
		}
	}
	return false
}

// IntervalDue reports whether an interval rule anchored on the local date
// anchor runs on the local date today: weekly on the anchor's weekday;
// monthly, quarterly and semiannually on the anchor's day of the month
// (the last day of shorter months), every 1, 3 or 6 months from the
// anchor's month; yearly on the anchor's day and month (February 28 of
// common years for February 29).
func IntervalDue(i period.Interval, anchor, today core.Date) bool {
	switch i {
	case period.Weekly:
		return anchor.Midnight(time.UTC).Weekday() == today.Midnight(time.UTC).Weekday()
	case period.Monthly:
		return dayDue(anchor, today)
	case period.Quarterly:
		return monthsApart(anchor, today)%3 == 0 && dayDue(anchor, today)
	case period.Semiannual:
		return monthsApart(anchor, today)%6 == 0 && dayDue(anchor, today)
	case period.Yearly:
		if anchor.Month != today.Month {
			return false
		}
		if today.Month == time.February && today.Day == 28 && lastDay(today) == 28 {
			return anchor.Day == 28 || anchor.Day == 29
		}
		return anchor.Day == today.Day
	default:
		return false
	}
}

// dayDue reports whether today is the anchor's day of the month, or the
// last day of a month shorter than it.
func dayDue(anchor, today core.Date) bool {
	if today.Day == lastDay(today) {
		return anchor.Day >= today.Day
	}
	return anchor.Day == today.Day
}

func lastDay(d core.Date) int { return core.NewDate(d.Year, d.Month+1, 0).Day }

func monthsApart(a, b core.Date) int {
	n := (int(b.Month) - int(a.Month)) % 12
	if n < 0 {
		n += 12
	}
	return n
}

// ExpireWallets terminates the wallets whose expiration passed at the
// instant at, and the rules whose expiration passed (the
// [kernel.JobExpireWallets] handler).
//
// Each wallet and each rule is terminated in its own transaction, as lago
// terminates each in its own service call, so that one failure does not
// keep the others alive; the errors are joined. Each is reread under its
// lock and skipped when no longer due.
func (s *Service) ExpireWallets(ctx context.Context, org core.OrganizationID, at time.Time) error {
	var ws []*wallet.Wallet
	var rules []*wallet.RecurringTransactionRule
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if ws, err = r.Wallets.ListExpired(ctx, org, at); err != nil {
			return fmt.Errorf("list expired wallets: %w", err)
		}
		if rules, err = r.RecurringTransactionRules.ListEligibleForTermination(ctx, org, at); err != nil {
			return fmt.Errorf("list expired rules: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, w := range ws {
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			w, err := lockWallet(ctx, r, org, w.ID)
			if err != nil {
				return err
			}
			if w.ExpirationAt == nil || w.ExpirationAt.After(at) {
				return nil
			}
			return s.terminate(ctx, r, w)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("expire wallet %s: %w", w.ID, err))
		}
	}
	for _, rule := range rules {
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			rule, err := r.RecurringTransactionRules.Get(ctx, org, rule.ID)
			if err != nil {
				return fmt.Errorf("load rule: %w", err)
			}
			if rule.Status != wallet.RuleStatusActive || rule.ExpirationAt == nil || rule.ExpirationAt.After(at) {
				return nil
			}
			if err := rule.Terminate(s.d.Now()); err != nil {
				return fmt.Errorf("terminate rule: %w", err)
			}
			if err := r.RecurringTransactionRules.Update(ctx, rule); err != nil {
				return fmt.Errorf("update rule: %w", err)
			}
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("expire rule %s: %w", rule.ID, err))
		}
	}
	return errors.Join(errs...)
}

// HandleJob runs a wallet job (refresh, threshold top-up, interval
// top-ups, expiration) and reports whether j is one.
func (s *Service) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	switch j.Kind {
	case kernel.JobRefreshWalletOngoingBalance, kernel.JobWalletThresholdTopUp:
		var a kernel.WalletArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
		}
		var err error
		if j.Kind == kernel.JobWalletThresholdTopUp {
			_, err = s.ThresholdTopUp(ctx, j.OrganizationID, a.WalletID)
		} else {
			_, err = s.RefreshWallet(ctx, j.OrganizationID, a.WalletID)
		}
		return true, err
	case kernel.JobWalletIntervalTopUps, kernel.JobExpireWallets:
		var a kernel.InstantArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
		}
		if j.Kind == kernel.JobExpireWallets {
			return true, s.ExpireWallets(ctx, j.OrganizationID, a.At)
		}
		_, err := s.RunIntervalTopUps(ctx, j.OrganizationID, a.At)
		return true, err
	default:
		return false, nil
	}
}
