package kernel_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var walletT0 = billingtest.Date(2024, time.March, 5, 10)

// walletTx stores a settled inbound purchase of 1000 on wallet w.
func walletTx(h *billingtest.Harness, w *wallet.Wallet, opts ...func(*wallet.Transaction)) *wallet.Transaction {
	h.T.Helper()
	t := &wallet.Transaction{
		ID: core.NewID[core.WalletTransactionID](h.IDs), OrganizationID: h.OrgID(), WalletID: w.ID,
		TransactionType: wallet.TransactionTypeInbound, TransactionStatus: wallet.TransactionStatusPurchased,
		Status: wallet.SettlementStatusSettled, Source: wallet.TransactionSourceManual, Currency: w.Currency,
		Amount: 1000, CreditAmount: money.MustParse("10"), Priority: w.Priority, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	for _, o := range opts {
		o(t)
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.WalletTransactions.Create(ctx, t) })
	return t
}

func walletOutbound(amount int64) func(*wallet.Transaction) {
	return func(t *wallet.Transaction) {
		t.TransactionType, t.TransactionStatus = wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced
		t.Amount, t.CreditAmount = amount, money.New(amount, -2)
	}
}

func walletInbound(status wallet.TransactionStatus, remaining int64, priority int, created time.Time) func(*wallet.Transaction) {
	return func(t *wallet.Transaction) {
		t.TransactionStatus, t.Priority, t.CreatedAt = status, priority, created
		t.Amount, t.CreditAmount = remaining, money.New(remaining, -2)
		t.RemainingAmount = &remaining
	}
}

func wantWalletDec(t *testing.T, name string, got money.Decimal, want string) {
	t.Helper()
	if got.Cmp(money.MustParse(want)) != 0 {
		t.Errorf("%s: got = %s, want = %s", name, got, want)
	}
}

func TestIncreaseWalletBalance(t *testing.T) {
	cases := []struct {
		name          string
		consumed      string
		credits       string
		resetConsumed bool
		balance       int64
		creditsBal    string
		wantConsumed  string
		wantConsAmt   int64
	}{
		// source: spec/services/wallets/balance/increase_service_spec.rb:31
		{name: "increase", credits: "4.5", consumed: "1", balance: 1450, creditsBal: "14.5", wantConsumed: "1", wantConsAmt: 100},
		// source: spec/services/wallets/balance/increase_service_spec.rb:52
		{name: "rounding", credits: "17.96999", consumed: "1", balance: 2797, creditsBal: "27.96999", wantConsumed: "1", wantConsAmt: 100},
		// source: spec/services/wallets/balance/increase_service_spec.rb:66
		{name: "reset consumed credits", credits: "0.5", consumed: "1", resetConsumed: true, balance: 1050, creditsBal: "10.5", wantConsumed: "0.5", wantConsAmt: 50},
		// source: spec/services/wallets/balance/increase_service_spec.rb:76
		{name: "reset more than consumed", credits: "2", consumed: "1", resetConsumed: true, balance: 1200, creditsBal: "12", wantConsumed: "0", wantConsAmt: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := billingtest.New(t, walletT0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) {
				w.ConsumedCredits = money.MustParse(tc.consumed)
				w.ConsumedAmount = 100
			})
			c, err := wallet.NewCredit(money.MustParse(tc.credits), w.Currency, w.RateAmount, false)
			if err != nil {
				t.Fatal(err)
			}
			tr := walletTx(h, w, func(t *wallet.Transaction) { t.Amount, t.CreditAmount = c.Amount, c.Credits })
			h.Do(func(ctx context.Context, r port.Repos) error {
				return kernel.IncreaseWalletBalance(ctx, h.Deps, r, w, tr, tc.resetConsumed)
			})
			got := h.ReloadWallet(w.ID)
			if got.BalanceAmount != tc.balance {
				t.Errorf("balance: got = %d, want = %d", got.BalanceAmount, tc.balance)
			}
			wantWalletDec(t, "credits balance", got.CreditsBalance, tc.creditsBal)
			wantWalletDec(t, "consumed credits", got.ConsumedCredits, tc.wantConsumed)
			if got.ConsumedAmount != tc.wantConsAmt {
				t.Errorf("consumed amount: got = %d, want = %d", got.ConsumedAmount, tc.wantConsAmt)
			}
			// source: spec/services/wallets/balance/increase_service_spec.rb:37
			if !slices.Contains(h.JobKinds(), kernel.JobRefreshWalletOngoingBalance) {
				t.Errorf("jobs: got = %v, want %s", h.JobKinds(), kernel.JobRefreshWalletOngoingBalance)
			}
			// source: spec/services/wallets/balance/increase_service_spec.rb:42
			if !slices.Contains(h.Webhooks(), webhook.EventTypeWalletUpdated) {
				t.Errorf("webhooks: got = %v, want wallet.updated", h.Webhooks())
			}
		})
	}
}

func TestDecreaseWalletBalance(t *testing.T) {
	cases := []struct {
		name         string
		rate         string
		balance      string
		amount       int64
		credits      string
		wantBalance  int64
		wantCredits  string
		wantConsumed string
	}{
		// source: spec/services/wallets/balance/decrease_service_spec.rb:25
		{name: "decrease", rate: "1", balance: "10", amount: 450, credits: "4.5", wantBalance: 550, wantCredits: "5.5", wantConsumed: "4.5"},
		// source: spec/services/wallets/balance/decrease_service_spec.rb:78
		{
			name: "rate with rounding issues", rate: "1.375", balance: "100", amount: 100, credits: "0.72727",
			wantBalance: 13650, wantCredits: "99.27273", wantConsumed: "0.72727",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := billingtest.New(t, walletT0)
			w := h.Wallet(h.Customer(), tc.balance, func(w *wallet.Wallet) {
				w.RateAmount = money.MustParse(tc.rate)
				w.BalanceAmount = mustMinor(t, w.Currency, w.CreditsBalance.Mul(w.RateAmount))
			})
			tr := walletTx(h, w, walletOutbound(tc.amount), func(t *wallet.Transaction) { t.CreditAmount = money.MustParse(tc.credits) })
			h.Do(func(ctx context.Context, r port.Repos) error {
				return kernel.DecreaseWalletBalance(ctx, h.Deps, r, w, tr)
			})
			got := h.ReloadWallet(w.ID)
			if got.BalanceAmount != tc.wantBalance {
				t.Errorf("balance: got = %d, want = %d", got.BalanceAmount, tc.wantBalance)
			}
			wantWalletDec(t, "credits balance", got.CreditsBalance, tc.wantCredits)
			// source: spec/services/wallets/balance/decrease_service_spec.rb:29
			wantWalletDec(t, "consumed credits", got.ConsumedCredits, tc.wantConsumed)
			if got.ConsumedAmount != tc.amount {
				t.Errorf("consumed amount: got = %d, want = %d", got.ConsumedAmount, tc.amount)
			}
			// source: spec/services/wallets/balance/decrease_service_spec.rb:37
			if !slices.Contains(h.JobKinds(), kernel.JobRefreshWalletOngoingBalance) {
				t.Errorf("jobs: got = %v, want %s", h.JobKinds(), kernel.JobRefreshWalletOngoingBalance)
			}
			// source: spec/services/wallets/balance/decrease_service_spec.rb:41
			if !slices.Contains(h.Webhooks(), webhook.EventTypeWalletUpdated) {
				t.Errorf("webhooks: got = %v, want wallet.updated", h.Webhooks())
			}
		})
	}
}

func TestTrackWalletConsumption(t *testing.T) {
	type in struct {
		status    wallet.TransactionStatus
		remaining int64
		priority  int
		daysAgo   int
	}
	cases := []struct {
		name     string
		inbounds []in
		outbound int64
		target   int // index of the inbound consumed, -1 for none
		consumed []int64
		err      string
	}{
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:51
		{
			name:     "granted first at the same priority",
			inbounds: []in{{wallet.TransactionStatusGranted, 3000, 10, 0}, {wallet.TransactionStatusPurchased, 7000, 10, 0}},
			outbound: 5000, target: -1, consumed: []int64{3000, 2000},
		},
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:106
		{
			name:     "outbound exceeds the available amount",
			inbounds: []in{{wallet.TransactionStatusPurchased, 1000, 50, 0}},
			outbound: 5000, target: -1, consumed: []int64{0}, err: kernel.CodeExceedsAvailableAmount,
		},
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:156
		{
			name:     "lower priority number first",
			inbounds: []in{{wallet.TransactionStatusGranted, 3000, 20, 0}, {wallet.TransactionStatusPurchased, 3000, 5, 0}},
			outbound: 4000, target: -1, consumed: []int64{1000, 3000},
		},
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:204
		{
			name:     "older first",
			inbounds: []in{{wallet.TransactionStatusPurchased, 3000, 50, 1}, {wallet.TransactionStatusPurchased, 3000, 50, 3}},
			outbound: 4000, target: -1, consumed: []int64{1000, 3000},
		},
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:224
		{name: "no inbound with remaining amount", outbound: 5000, target: -1, err: kernel.CodeExceedsAvailableAmount},
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:319
		{
			name: "priority, then granted before purchased, then oldest",
			inbounds: []in{
				{wallet.TransactionStatusPurchased, 1000, 2, 3},
				{wallet.TransactionStatusGranted, 1000, 2, 1},
				{wallet.TransactionStatusGranted, 1000, 2, 2},
				{wallet.TransactionStatusPurchased, 1000, 1, 0},
			},
			outbound: 3500, target: -1, consumed: []int64{500, 1000, 1000, 1000},
		},
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:374
		{
			name:     "zero outbound",
			inbounds: []in{{wallet.TransactionStatusPurchased, 1000, 50, 0}},
			outbound: 0, target: -1, consumed: []int64{0},
		},
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:434
		{
			name:     "specific inbound ignores priority",
			inbounds: []in{{wallet.TransactionStatusPurchased, 5000, 50, 0}, {wallet.TransactionStatusGranted, 5000, 1, 0}},
			outbound: 3000, target: 0, consumed: []int64{3000, 0},
		},
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:466
		{
			name:     "outbound exceeds the specific inbound",
			inbounds: []in{{wallet.TransactionStatusPurchased, 5000, 50, 0}},
			outbound: 6000, target: 0, consumed: []int64{0}, err: kernel.CodeExceedsRemainingTransactionAmount,
		},
		// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:500
		{
			name:     "specific inbound with nothing left",
			inbounds: []in{{wallet.TransactionStatusPurchased, 0, 50, 0}},
			outbound: 1000, target: 0, consumed: []int64{0}, err: kernel.CodeExceedsRemainingTransactionAmount,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := billingtest.New(t, walletT0)
			w := h.Wallet(h.Customer(), "1000", func(w *wallet.Wallet) { w.Traceable = true })
			ins := make([]*wallet.Transaction, len(tc.inbounds))
			for i, x := range tc.inbounds {
				ins[i] = walletTx(h, w, walletInbound(x.status, x.remaining, x.priority, walletT0.AddDate(0, 0, -x.daysAgo)))
			}
			out := walletTx(h, w, walletOutbound(tc.outbound))
			var target core.WalletTransactionID
			if tc.target >= 0 {
				target = ins[tc.target].ID
			}
			err := h.Store.InTx(h.Ctx, func(ctx context.Context, r port.Repos) error {
				return kernel.TrackWalletConsumption(ctx, h.Deps, r, out, target)
			})
			var ve *core.ValidationError
			switch {
			case tc.err == "" && err != nil:
				t.Fatalf("err: got = %v, want = nil", err)
			case tc.err != "" && (!errors.As(err, &ve) || !ve.Has("amount", tc.err)):
				t.Fatalf("err: got = %v, want = amount/%s", err, tc.err)
			}
			cons := billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*wallet.Consumption, error) {
				return r.WalletTransactions.ListConsumptionsByOutbound(ctx, h.OrgID(), out.ID)
			})
			byInbound := map[core.WalletTransactionID]int64{}
			for _, c := range cons {
				byInbound[c.InboundTransactionID] += c.ConsumedAmount
			}
			for i, x := range ins {
				if got := byInbound[x.ID]; got != tc.consumed[i] {
					t.Errorf("inbound %d consumed: got = %d, want = %d", i, got, tc.consumed[i])
				}
				// source: spec/services/wallet_transactions/track_consumption_service_spec.rb:66
				left := billingtest.Read(h, func(ctx context.Context, r port.Repos) (*wallet.Transaction, error) {
					return r.WalletTransactions.Get(ctx, h.OrgID(), x.ID)
				}).RemainingAmount
				if want := tc.inbounds[i].remaining - tc.consumed[i]; *left != want {
					t.Errorf("inbound %d remaining: got = %d, want = %d", i, *left, want)
				}
			}
		})
	}
}

func mustMinor(t *testing.T, c money.Currency, d money.Decimal) int64 {
	t.Helper()
	n, err := c.ToMinor(d, money.HalfUp)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
