package wallets

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// thresholdTopUpDelay delays the threshold top-up job after a refresh, so
// that a burst of refreshes runs it once (lago CreateJob wait).
const thresholdTopUpDelay = 2 * time.Second

// UsageFee is one amount of a customer's usage that is not yet paid for,
// in minor units including taxes. The amounts of one fee type, metric and
// currency are summed into a net amount before allocation.
type UsageFee struct {
	// FeeType is the fee type.
	FeeType core.FeeType
	// BillableMetricID is the metric of charge fees; zero otherwise.
	BillableMetricID core.BillableMetricID
	// TargetWalletCode is the wallet the fee's group targets, for the fees
	// of a charge accepting a target wallet; "" otherwise. Only the wallet
	// with that code pays the fee.
	TargetWalletCode string
	// Currency is the fee currency.
	Currency money.Currency
	// Amount is signed: positive for usage to pay, negative for usage
	// already billed.
	Amount int64
}

// UsageSource reports the usage a customer's ongoing wallet balances
// deduct (lago Customers::RefreshWalletsService inputs). The engine
// returns, for every subscription of the customer:
//   - the current usage fees: amount + taxes;
//   - the fees of draft invoices: amount + taxes − coupons;
//   - the fees of progressive billing invoices of the current period:
//     −(subtotal excluding taxes + taxes);
//   - the pay-in-advance fees of the current period: −(amount + taxes).
type UsageSource interface {
	// OngoingUsage returns the usage fees of customer c.
	OngoingUsage(ctx context.Context, r port.Repos, c *customer.Customer) ([]UsageFee, error)
}

// RefreshWallet refreshes the ongoing balances of the customer of a wallet
// (the [kernel.JobRefreshWalletOngoingBalance] handler).
func (s *Service) RefreshWallet(ctx context.Context, org core.OrganizationID, id core.WalletID) ([]*wallet.Wallet, error) {
	var c core.CustomerID
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		w, err := r.Wallets.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load wallet %s: %w", id, err)
		}
		c = w.CustomerID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.RefreshOngoingBalances(ctx, org, c)
}

// maxRefreshAttempts bounds how many times a refresh computes the usage
// again because the balances changed meanwhile.
const maxRefreshAttempts = 3

// RefreshOngoingBalances recomputes the ongoing balances of every active
// wallet of a customer (lago Customers::RefreshWalletsService): the usage
// is allocated across the wallets, each wallet's ongoing balance becomes its
// balance minus its share. A wallet whose ongoing balance falls to zero or
// below emits wallet.depleted_ongoing_balance; a changed ongoing balance
// schedules the wallet's threshold top-up check.
//
// The usage, which aggregates every active subscription and may call the
// tax provider, is computed in a transaction holding no lock, as lago
// computes it holding no customer lock (Customers::RefreshWalletJob,
// app/jobs/customers/refresh_wallet_job.rb:47). The allocation and the
// writes then run under the customer's prepaid_credits lock, so that they
// never overwrite balances an invoice consumed concurrently. When the
// wallets' balances changed between the two, an invoice consumed credits
// for usage that has left the current usage, so the usage is computed
// again; after [maxRefreshAttempts] the last usage is applied, which is
// as stale as lago's always is.
func (s *Service) RefreshOngoingBalances(ctx context.Context, org core.OrganizationID, c core.CustomerID) ([]*wallet.Wallet, error) {
	for attempt := 1; ; attempt++ {
		var (
			seen []*wallet.Wallet
			fees []UsageFee
			gen  int64
		)
		// The refresh is claimed, in a transaction of its own, before the
		// usage is read: the claim waits for the transactions that flagged
		// the customer, and a flag raised by later events survives this
		// refresh.
		err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			var err error
			if gen, err = r.Customers.ClaimWalletRefresh(ctx, org, c); err != nil {
				return fmt.Errorf("claim wallet refresh of customer %s: %w", c, err)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			var err error
			if seen, err = r.Wallets.ListActiveForApplication(ctx, org, c); err != nil {
				return fmt.Errorf("list wallets of customer %s: %w", c, err)
			}
			if len(seen) == 0 {
				return nil
			}
			fees, err = s.ongoingUsage(ctx, r, org, c)
			return err
		})
		if err != nil {
			return nil, err
		}
		if len(seen) == 0 {
			return nil, nil
		}
		var out []*wallet.Wallet
		stale := false
		err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			if err := lockCustomerCredits(ctx, r, org, c); err != nil {
				return err
			}
			ws, err := r.Wallets.ListActiveForApplication(ctx, org, c)
			if err != nil {
				return fmt.Errorf("list wallets of customer %s: %w", c, err)
			}
			if !sameBalances(seen, ws) && attempt < maxRefreshAttempts {
				stale = true
				return nil
			}
			out = ws
			if err := s.allocate(ctx, r, org, ws, fees); err != nil {
				return err
			}
			// lago app/services/customers/refresh_wallets_service.rb:36 clears
			// awaiting_wallet_refresh once the balances are saved.
			if err := r.Customers.ClearWalletRefresh(ctx, org, c, gen); err != nil {
				return fmt.Errorf("clear wallet refresh of customer %s: %w", c, err)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if !stale {
			return out, nil
		}
	}
}

// ongoingUsage returns the usage fees of customer id, none without a
// usage source.
func (s *Service) ongoingUsage(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID) ([]UsageFee, error) {
	if s.usage == nil {
		return nil, nil
	}
	c, err := r.Customers.Get(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("load customer %s: %w", id, err)
	}
	fees, err := s.usage.OngoingUsage(ctx, r, c)
	if err != nil {
		return nil, fmt.Errorf("usage of customer %s: %w", id, err)
	}
	return fees, nil
}

// sameBalances reports whether a and b are the same wallets with the same
// balances.
func sameBalances(a, b []*wallet.Wallet) bool {
	return slices.EqualFunc(a, b, func(x, y *wallet.Wallet) bool {
		return x.ID == y.ID && x.BalanceAmount == y.BalanceAmount
	})
}

// allocate allocates the usage fees across the wallets ws and saves their
// ongoing balances.
func (s *Service) allocate(ctx context.Context, r port.Repos, org core.OrganizationID, ws []*wallet.Wallet, fees []UsageFee) error {
	now := s.d.Now()
	threshold := make([]bool, len(ws))
	for i, w := range ws {
		rules, err := r.RecurringTransactionRules.ListActiveThreshold(ctx, org, w.ID, now)
		if err != nil {
			return fmt.Errorf("list rules of wallet %s: %w", w.ID, err)
		}
		threshold[i] = len(rules) > 0
	}
	alloc := AllocateUsage(ws, threshold, fees)
	for i, w := range ws {
		if err := s.refreshWallet(ctx, r, w, alloc[i], threshold[i]); err != nil {
			return err
		}
	}
	return nil
}

// refreshWallet sets the ongoing balances of w from its share of the usage
// (lago Wallets::Balance::RefreshOngoingUsageService and
// UpdateOngoingService).
func (s *Service) refreshWallet(ctx context.Context, r port.Repos, w *wallet.Wallet, usage int64, threshold bool) error {
	ongoing := w.BalanceAmount - usage
	changed := w.OngoingUsageBalanceAmount != usage || w.OngoingBalanceAmount != ongoing
	w.OngoingUsageBalanceAmount = usage
	w.CreditsOngoingUsageBalance = minorToCredits(usage, w)
	w.OngoingBalanceAmount = ongoing
	w.CreditsOngoingBalance = minorToCredits(ongoing, w)
	depleted := false
	switch {
	case !w.DepletedOngoingBalance && ongoing <= 0:
		w.DepletedOngoingBalance, depleted = true, true
	case w.DepletedOngoingBalance && ongoing > 0:
		w.DepletedOngoingBalance = false
	}
	w.UpdatedAt = s.d.Now()
	if err := r.Wallets.Update(ctx, w); err != nil {
		return fmt.Errorf("update wallet %s: %w", w.ID, err)
	}
	if depleted {
		if err := kernel.Emit(ctx, s.d, w.OrganizationID, webhook.EventTypeWalletDepletedOngoingBalance, "wallet", kernel.ObjectID(w.ID), w); err != nil {
			return err
		}
	}
	if changed && threshold {
		if err := kernel.Enqueue(ctx, r, w.OrganizationID, kernel.JobWalletThresholdTopUp, kernel.WalletArgs{WalletID: w.ID},
			w.ID.String(), s.d.Now().Add(thresholdTopUpDelay)); err != nil {
			return err
		}
	}
	// lago app/services/wallets/balance/update_ongoing_service.rb:27,
	// whether or not the balances changed.
	return kernel.EnqueueWalletAlerts(ctx, r, w)
}

// minorToCredits converts an amount of the wallet's currency to credits,
// half up to [wallet.CreditPlaces] places.
func minorToCredits(amount int64, w *wallet.Wallet) money.Decimal {
	return w.Currency.FromMinor(amount).Div(w.RateAmount).Round(wallet.CreditPlaces, money.HalfUp)
}

// usageKey is the netting bucket of usage fees.
type usageKey struct {
	feeType      core.FeeType
	metric       core.BillableMetricID
	targetWallet string
	currency     string
}

func (k usageKey) compare(o usageKey) int {
	return cmp.Or(cmp.Compare(k.feeType, o.feeType), k.metric.Compare(o.metric),
		cmp.Compare(k.targetWallet, o.targetWallet), cmp.Compare(k.currency, o.currency))
}

// AllocateUsage distributes the usage fees across the wallets ws, ordered
// for application, and returns each wallet's share in minor units (lago
// Wallets::Balance::AllocateOngoingUsageByWalletsService). The fees are
// netted per fee type, metric, target wallet code and currency; positive
// buckets, largest first, are allocated within a per-currency budget (the
// sum of the nets, never negative) to the wallets that pay them, in order,
// each up to its balance. A wallet with a threshold rule (threshold[i]) or the last
// applicable wallet absorbs the rest and may go negative.
func AllocateUsage(ws []*wallet.Wallet, threshold []bool, fees []UsageFee) []int64 {
	nets := map[usageKey]int64{}
	budgets := map[string]int64{}
	for _, f := range fees {
		k := usageKey{f.FeeType, f.BillableMetricID, f.TargetWalletCode, f.Currency.Code()}
		nets[k] += f.Amount
		budgets[k.currency] += f.Amount
	}
	for c, b := range budgets {
		budgets[c] = max(0, b)
	}
	keys := make([]usageKey, 0, len(nets))
	for k, n := range nets {
		if n > 0 {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b usageKey) int {
		return cmp.Or(cmp.Compare(nets[b], nets[a]), a.compare(b))
	})
	alloc := make([]int64, len(ws))
	for _, k := range keys {
		remaining := min(nets[k], budgets[k.currency])
		var applicable []int
		for i, w := range ws {
			if paysUsage(w, k) {
				applicable = append(applicable, i)
			}
		}
		for n, i := range applicable {
			if remaining <= 0 {
				break
			}
			var take int64
			if threshold[i] || n == len(applicable)-1 {
				take = remaining
			} else {
				room := ws[i].BalanceAmount - alloc[i]
				if room <= 0 {
					continue
				}
				take = min(remaining, room)
			}
			alloc[i] += take
			remaining -= take
			budgets[k.currency] -= take
		}
	}
	return alloc
}

// paysUsage reports whether wallet w pays the usage bucket k: same
// currency, and the wallet the bucket targets, else unrestricted, of an
// allowed fee type or a charge of a targeted metric.
//
// source: app/services/wallets/balance/allocate_ongoing_usage_by_wallets_service.rb:126-136
func paysUsage(w *wallet.Wallet, k usageKey) bool {
	if w.Currency.Code() != k.currency {
		return false
	}
	if k.targetWallet != "" {
		return w.Code != nil && *w.Code == k.targetWallet
	}
	if w.Unrestricted() || slices.Contains(w.AllowedFeeTypes, k.feeType) {
		return true
	}
	return k.feeType == core.FeeTypeCharge && !k.metric.IsZero() && slices.Contains(w.BillableMetricIDs, k.metric)
}
