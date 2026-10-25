package wallets

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// ComputedUsage is the current usage a [ComputedUsageSource] computed
// for a refresh. The wallets service does not read it: it hands it to the
// [Streamer] unchanged, so that the streamed current usage is the one the
// balances were refreshed from.
type ComputedUsage any

// ComputedUsageSource is a [UsageSource] that also returns the current
// usage it computed the fees from.
type ComputedUsageSource interface {
	UsageSource
	// OngoingUsageComputed returns the usage fees of customer c and the
	// current usage they come from.
	OngoingUsageComputed(ctx context.Context, r port.Repos, c *customer.Customer) ([]UsageFee, ComputedUsage, error)
}

// Streamer delivers the streaming events of a refresh of ongoing balances
// (lago Customers::RefreshWalletsService#deliver_streaming_events,
// app/services/customers/refresh_wallets_service.rb:46-54).
type Streamer interface {
	// WalletsRefreshed runs in the transaction that saved the refreshed
	// balances of customer c, computed from usage (nil when the usage
	// source does not return it). It enqueues, in that transaction, the
	// deliveries that run as jobs, and returns what runs once the
	// transaction committed, or nil: a rolled back refresh reaches no
	// stream.
	WalletsRefreshed(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, usage ComputedUsage) (func(context.Context), error)
}

// RefreshWallet refreshes the ongoing balances of the customer of a wallet
// (the [kernel.JobRefreshWalletOngoingBalance] handler, lago
// Customers::RefreshWalletJob, app/jobs/customers/refresh_wallet_job.rb:43-64).
// A customer with a tax_error detail is skipped. When the tax provider
// cannot resolve the customer's address, the failure is recorded as a
// tax_error detail on the customer instead of failing the job, and the
// customer is no longer refreshed until its address changes.
func (s *Service) RefreshWallet(ctx context.Context, org core.OrganizationID, id core.WalletID) ([]*wallet.Wallet, error) {
	var (
		c       core.CustomerID
		blocked bool
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		w, err := r.Wallets.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load wallet %s: %w", id, err)
		}
		c = w.CustomerID
		blocked, err = hasTaxError(ctx, r, org, c)
		return err
	})
	if err != nil || blocked {
		return nil, err
	}
	ws, err := s.RefreshOngoingBalances(ctx, org, c)
	if pe, ok := addressUnresolved(err); ok {
		return nil, s.recordTaxError(ctx, org, c, pe, err)
	}
	return ws, err
}

// customerAddressInvalid is the tax provider's error code for an address
// it cannot resolve (lago Integrations::Aggregator::Taxes::BaseService::
// CUSTOMER_ADDRESS_INVALID, app/services/integrations/aggregator/taxes/base_service.rb:10).
const customerAddressInvalid = "customerAddressCouldNotResolve"

// hasTaxError reports whether customer c has a tax_error detail (lago
// customer.error_details.tax_error.exists?).
func hasTaxError(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID) (bool, error) {
	ds, err := r.CustomerErrorDetails.ListByCustomers(ctx, org, []core.CustomerID{c})
	if err != nil {
		return false, fmt.Errorf("list error details of customer %s: %w", c, err)
	}
	return slices.ContainsFunc(ds, func(d *customer.ErrorDetail) bool { return d.Code == customer.ErrorCodeTaxError }), nil
}

// addressUnresolved returns the tax provider failure of err when it is
// about the customer's address: lago matches the code within the failure
// message ("code: message", refresh_wallet_job.rb:50-52).
func addressUnresolved(err error) (*kernel.TaxProviderError, bool) {
	pe, ok := errors.AsType[*kernel.TaxProviderError](err)
	if !ok || !strings.Contains(pe.Code+": "+pe.Message, customerAddressInvalid) {
		return nil, false
	}
	return pe, true
}

// recordTaxError stores the tax_error detail of customer c for the
// provider failure pe of the refresh error err (refresh_wallet_job.rb:53-63).
// Its details hold the provider's failure under "tax_error" and the error
// under "error"; lago's Ruby backtrace has no counterpart.
func (s *Service) recordTaxError(ctx context.Context, org core.OrganizationID, c core.CustomerID, pe *kernel.TaxProviderError, err error) error {
	msg := pe.Code
	if pe.Message != "" {
		msg += ": " + pe.Message
	}
	details, jerr := json.Marshal(map[string]string{string(customer.ErrorCodeTaxError): msg, "error": err.Error()})
	if jerr != nil {
		return fmt.Errorf("encode tax error of customer %s: %w", c, jerr)
	}
	if err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		return r.CustomerErrorDetails.Create(ctx, &customer.ErrorDetail{
			ID: core.NewID[core.ErrorDetailID](s.d.IDs), OrganizationID: org, CustomerID: c,
			Code: customer.ErrorCodeTaxError, Details: details, CreatedAt: s.d.Now(),
		})
	}); err != nil {
		return fmt.Errorf("store tax error of customer %s: %w", c, err)
	}
	s.d.Logger.WarnContext(ctx, "wallet refresh stopped: tax provider cannot resolve the customer address",
		"organization_id", org, "customer_id", c, "tax_error", msg)
	return nil
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
			seen     []*wallet.Wallet
			fees     []UsageFee
			computed ComputedUsage
			gen      int64
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
			fees, computed, err = s.ongoingUsage(ctx, r, org, c)
			return err
		})
		if err != nil {
			return nil, err
		}
		if len(seen) == 0 {
			return nil, nil
		}
		var (
			out   []*wallet.Wallet
			after func(context.Context)
		)
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
			if s.streamer != nil {
				if after, err = s.streamer.WalletsRefreshed(ctx, r, org, c, computed); err != nil {
					return fmt.Errorf("stream refresh of customer %s: %w", c, err)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if !stale {
			if after != nil {
				after(ctx)
			}
			return out, nil
		}
	}
}

// ongoingUsage returns the usage fees of customer id and, when the usage
// source returns it, the current usage they come from; none without a
// usage source.
func (s *Service) ongoingUsage(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID) ([]UsageFee, ComputedUsage, error) {
	if s.usage == nil {
		return nil, nil, nil
	}
	c, err := r.Customers.Get(ctx, org, id)
	if err != nil {
		return nil, nil, fmt.Errorf("load customer %s: %w", id, err)
	}
	var (
		fees     []UsageFee
		computed ComputedUsage
	)
	if cs, ok := s.usage.(ComputedUsageSource); ok {
		fees, computed, err = cs.OngoingUsageComputed(ctx, r, c)
	} else {
		fees, err = s.usage.OngoingUsage(ctx, r, c)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("usage of customer %s: %w", id, err)
	}
	return fees, computed, nil
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
