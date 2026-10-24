package wallets_test

import (
	"context"
	"slices"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func thresholdRule(r *wallet.RecurringTransactionRule) {
	r.Trigger, r.Interval, r.ThresholdCredits = wallet.RecurringTriggerThreshold, 0, dec("1")
}

func TestRefreshAllocation(t *testing.T) {
	type wspec struct {
		balance   int64
		priority  int
		threshold bool
		ruleOpts  []func(*wallet.RecurringTransactionRule)
		currency  money.Currency
		feeTypes  []core.FeeType
		metrics   []int // indexes of the metrics the wallet targets
		code      string
	}
	type fee struct {
		typ    core.FeeType
		metric int // index of the metric, -1 for none
		amount int64
		cur    money.Currency
		target string // target wallet code of the fee
	}
	charge := func(metric int, amount int64) fee { return fee{core.FeeTypeCharge, metric, amount, usd, ""} }
	two := []wspec{{balance: 50, priority: 1}, {balance: 150, priority: 2}}
	cases := []struct {
		name    string
		wallets []wspec
		fees    []fee
		want    []int64
	}{
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:36
		{name: "overflow cascades", wallets: two, fees: []fee{charge(0, 80)}, want: []int64{50, 30}},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:47
		{
			name:    "threshold wallet absorbs",
			wallets: []wspec{{balance: 50, priority: 1, threshold: true}, {balance: 150, priority: 2}},
			fees:    []fee{charge(0, 80)}, want: []int64{80, 0},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:61
		{
			name:    "both threshold wallets",
			wallets: []wspec{{balance: 50, priority: 1, threshold: true}, {balance: 150, priority: 2, threshold: true}},
			fees:    []fee{charge(0, 80)}, want: []int64{80, 0},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:72
		{
			name: "threshold rule not active",
			wallets: []wspec{
				{balance: 50, priority: 1, threshold: true, ruleOpts: []func(*wallet.RecurringTransactionRule){
					func(r *wallet.RecurringTransactionRule) {
						r.Status, r.TerminatedAt = wallet.RuleStatusTerminated, ptr(t0)
					},
				}},
				{balance: 150, priority: 2},
			},
			fees: []fee{charge(0, 80)}, want: []int64{50, 30},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:83
		{
			name: "interval rule",
			wallets: []wspec{
				{balance: 50, priority: 1, threshold: true, ruleOpts: []func(*wallet.RecurringTransactionRule){
					func(r *wallet.RecurringTransactionRule) {
						r.Trigger, r.ThresholdCredits = wallet.RecurringTriggerInterval, nil
					},
				}},
				{balance: 150, priority: 2},
			},
			fees: []fee{charge(0, 80)}, want: []int64{50, 30},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:98
		{
			name:    "middle threshold wallet",
			wallets: []wspec{{balance: 50, priority: 1}, {balance: 50, priority: 2, threshold: true}, {balance: 50, priority: 3}},
			fees:    []fee{charge(0, 200)}, want: []int64{50, 150, 0},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:108
		{name: "single wallet", wallets: []wspec{{balance: 150, priority: 2}}, fees: []fee{charge(0, 80)}, want: []int64{80}},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:117
		{name: "single wallet overflows", wallets: []wspec{{balance: 50, priority: 1}}, fees: []fee{charge(0, 80)}, want: []int64{80}},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:125
		{name: "usage above all balances", wallets: two, fees: []fee{charge(0, 250)}, want: []int64{50, 200}},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:138
		{name: "pay-in-advance offset", wallets: two, fees: []fee{charge(0, 80), charge(0, -30)}, want: []int64{50, 0}},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:153
		{name: "progressive billing offset", wallets: two, fees: []fee{charge(0, 80), charge(0, -30)}, want: []int64{50, 0}},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:164
		{name: "draft invoice fee", wallets: two, fees: []fee{charge(0, 40), charge(0, 40)}, want: []int64{50, 30}},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:179
		{
			name:    "metric targeting",
			wallets: []wspec{{balance: 1000, priority: 1, metrics: []int{0}}, {balance: 2000, priority: 2}},
			fees:    []fee{charge(0, 600), charge(1, 500)}, want: []int64{600, 500},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:190
		{
			name: "allowed fee types",
			wallets: []wspec{
				{balance: 1000, priority: 1, feeTypes: []core.FeeType{core.FeeTypeSubscription}},
				{balance: 2000, priority: 2, feeTypes: []core.FeeType{core.FeeTypeCharge}},
			},
			fees: []fee{charge(0, 80)}, want: []int64{0, 80},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:200
		{
			name:    "currency mismatch",
			wallets: []wspec{{balance: 1000, priority: 1}, {balance: 1000, priority: 2, currency: eur}},
			fees:    []fee{{core.FeeTypeCharge, 0, 80, eur, ""}}, want: []int64{0, 80},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:211
		{
			name:    "zero-balance wallet in the middle",
			wallets: []wspec{{balance: 0, priority: 1}, {balance: 50, priority: 2}, {balance: 50, priority: 3}},
			fees:    []fee{charge(0, 80)}, want: []int64{0, 50, 30},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:222
		{name: "two metrics competing", wallets: two, fees: []fee{charge(0, 40), charge(1, 40)}, want: []int64{50, 30}},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:234
		{
			name:    "subscription fee bypasses a targeting wallet",
			wallets: []wspec{{balance: 50, priority: 1, metrics: []int{0}}, {balance: 150, priority: 2}},
			fees:    []fee{{core.FeeTypeSubscription, -1, 80, usd, ""}}, want: []int64{0, 80},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:248
		{name: "fully covered by progressive billing", wallets: two, fees: []fee{charge(0, 80), charge(0, -80)}, want: []int64{0, 0}},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:263
		{
			name: "progressive billing above one metric", wallets: two,
			fees: []fee{charge(0, 80), charge(1, 100), charge(1, -150)}, want: []int64{30, 0},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:297
		{
			name: "targets a missing wallet", wallets: two,
			fees: []fee{{core.FeeTypeCharge, 0, 80, usd, "missing-wallet"}}, want: []int64{0, 0},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:314
		{
			name:    "targeted wallet absorbs the full amount",
			wallets: []wspec{{balance: 50, priority: 1, code: "wallet-b"}, {balance: 150, priority: 2}},
			fees:    []fee{{core.FeeTypeCharge, 0, 80, usd, "wallet-b"}}, want: []int64{80, 0},
		},
		// source: spec/services/wallets/balance/allocate_ongoing_usage_by_wallets_service_spec.rb:331
		{name: "no usage", wallets: two, want: []int64{0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			c := h.Customer()
			metrics := []core.BillableMetricID{core.NewID[core.BillableMetricID](h.IDs), core.NewID[core.BillableMetricID](h.IDs)}
			ws := make([]*wallet.Wallet, len(tc.wallets))
			for i, spec := range tc.wallets {
				ws[i] = h.Wallet(c, "0", func(w *wallet.Wallet) {
					w.BalanceAmount, w.OngoingBalanceAmount = spec.balance, spec.balance
					w.CreditsBalance = money.New(spec.balance, -2)
					w.Priority, w.AllowedFeeTypes = spec.priority, spec.feeTypes
					if spec.code != "" {
						w.Code = &spec.code
					}
					if spec.currency != (money.Currency{}) {
						w.Currency = spec.currency
					}
					for _, m := range spec.metrics {
						w.BillableMetricIDs = append(w.BillableMetricIDs, metrics[m])
					}
				})
				if spec.threshold {
					addRule(h, ws[i], append([]func(*wallet.RecurringTransactionRule){thresholdRule}, spec.ruleOpts...)...)
				}
			}
			var fees []wallets.UsageFee
			for _, f := range tc.fees {
				uf := wallets.UsageFee{FeeType: f.typ, Currency: f.cur, Amount: f.amount, TargetWalletCode: f.target}
				if f.metric >= 0 {
					uf.BillableMetricID = metrics[f.metric]
				}
				fees = append(fees, uf)
			}
			s = s.WithUsage(fakeUsage{c.ID: fees})
			if _, err := s.RefreshOngoingBalances(h.Ctx, h.OrgID(), c.ID); err != nil {
				t.Fatal(err)
			}
			for i, w := range ws {
				if got := h.ReloadWallet(w.ID).OngoingUsageBalanceAmount; got != tc.want[i] {
					t.Errorf("wallet %d usage: got = %d, want = %d", i, got, tc.want[i])
				}
			}
		})
	}
}

func TestRefreshWallet(t *testing.T) {
	cases := []struct {
		name         string
		rate         string
		depleted     bool
		usage        int64
		usageCredits string
		ongoing      int64
		ongoingCreds string
		wantDepleted bool
		webhook      int
	}{
		// source: spec/services/wallets/balance/refresh_ongoing_usage_service_spec.rb:25
		{name: "usage above the balance", rate: "1", usage: 1100, usageCredits: "11", ongoing: -100, ongoingCreds: "-1", wantDepleted: true, webhook: 1},
		// source: spec/services/wallets/balance/refresh_ongoing_usage_service_spec.rb:35
		{name: "rate of 2", rate: "2", usage: 1100, usageCredits: "5.5", ongoing: -100, ongoingCreds: "-0.5", wantDepleted: true, webhook: 1},
		// source: spec/services/wallets/balance/refresh_ongoing_usage_service_spec.rb:63
		{name: "already depleted", rate: "1", depleted: true, usage: 1100, usageCredits: "11", ongoing: -100, ongoingCreds: "-1", wantDepleted: true},
		// source: spec/services/wallets/balance/refresh_ongoing_usage_service_spec.rb:76
		{name: "positive after depletion", rate: "1", depleted: true, usage: 500, usageCredits: "5", ongoing: 500, ongoingCreds: "5"},
		// source: spec/services/wallets/balance/update_ongoing_service_spec.rb:32
		{name: "not depleted", rate: "1", usage: 550, usageCredits: "5.5", ongoing: 450, ongoingCreds: "4.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			c := h.Customer()
			w := h.Wallet(c, "10", func(w *wallet.Wallet) {
				w.RateAmount = money.MustParse(tc.rate)
				w.CreditsBalance = money.MustParse("10").Div(w.RateAmount)
				w.OngoingBalanceAmount, w.OngoingUsageBalanceAmount = 800, 200
				w.DepletedOngoingBalance = tc.depleted
			})
			s = s.WithUsage(fakeUsage{c.ID: {{FeeType: core.FeeTypeCharge, Currency: usd, Amount: tc.usage}}})
			if _, err := s.RefreshWallet(h.Ctx, h.OrgID(), w.ID); err != nil {
				t.Fatal(err)
			}
			got := h.ReloadWallet(w.ID)
			if got.OngoingUsageBalanceAmount != tc.usage || got.OngoingBalanceAmount != tc.ongoing {
				t.Errorf("ongoing: got = (%d, %d), want = (%d, %d)", got.OngoingUsageBalanceAmount, got.OngoingBalanceAmount, tc.usage, tc.ongoing)
			}
			wantDec(t, "credits ongoing usage", got.CreditsOngoingUsageBalance, tc.usageCredits)
			wantDec(t, "credits ongoing balance", got.CreditsOngoingBalance, tc.ongoingCreds)
			if got.DepletedOngoingBalance != tc.wantDepleted {
				t.Errorf("depleted: got = %v, want = %v", got.DepletedOngoingBalance, tc.wantDepleted)
			}
			// source: spec/services/wallets/balance/update_ongoing_service_spec.rb:62
			if n := countWebhooks(h, webhook.EventTypeWalletDepletedOngoingBalance); n != tc.webhook {
				t.Errorf("wallet.depleted_ongoing_balance: got = %d, want = %d", n, tc.webhook)
			}
		})
	}
}

func TestRefreshThresholdTopUp(t *testing.T) {
	cases := []struct {
		name  string
		usage int64
		rule  bool
		want  bool
	}{
		// source: spec/services/wallets/balance/update_ongoing_service_spec.rb:47
		{name: "changed with a threshold rule", usage: 550, rule: true, want: true},
		// source: spec/services/wallets/balance/update_ongoing_service_spec.rb:96
		{name: "unchanged", usage: 200, rule: true},
		{name: "changed without a threshold rule", usage: 550},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			c := h.Customer()
			w := h.Wallet(c, "10", func(w *wallet.Wallet) {
				w.OngoingBalanceAmount, w.OngoingUsageBalanceAmount = 800, 200
				w.CreditsOngoingBalance, w.CreditsOngoingUsageBalance = money.MustParse("8"), money.MustParse("2")
			})
			if tc.rule {
				addRule(h, w, thresholdRule)
			}
			s = s.WithUsage(fakeUsage{c.ID: {{FeeType: core.FeeTypeCharge, Currency: usd, Amount: tc.usage}}})
			if _, err := s.RefreshOngoingBalances(h.Ctx, h.OrgID(), c.ID); err != nil {
				t.Fatal(err)
			}
			if got := slices.Contains(h.JobKinds(), kernel.JobWalletThresholdTopUp); got != tc.want {
				t.Errorf("threshold top-up job: got = %v, want = %v (%v)", got, tc.want, h.JobKinds())
			}
		})
	}
}

func TestRefreshWithoutUsage(t *testing.T) {
	// Without a usage source the ongoing balance is the balance.
	h, s := setup(t, t0)
	c := h.Customer()
	w := h.Wallet(c, "10", func(w *wallet.Wallet) { w.OngoingBalanceAmount = 0 })
	if _, err := s.RefreshOngoingBalances(h.Ctx, h.OrgID(), c.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.ReloadWallet(w.ID).OngoingBalanceAmount; got != 1000 {
		t.Errorf("ongoing balance: got = %d, want = 1000", got)
	}
}

func TestRefreshTerminatedWallet(t *testing.T) {
	h, s := setup(t, t0)
	c := h.Customer()
	w := h.Wallet(c, "10", terminated(t0))
	s = s.WithUsage(fakeUsage{c.ID: {{FeeType: core.FeeTypeCharge, Currency: usd, Amount: 500}}})
	if _, err := s.RefreshOngoingBalances(h.Ctx, h.OrgID(), c.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.ReloadWallet(w.ID).OngoingUsageBalanceAmount; got != 0 {
		t.Errorf("terminated wallet usage: got = %d, want = 0", got)
	}
}

// heldLocks records the locks taken through the repositories.
type heldLocks struct {
	inner port.Locker
	keys  []port.LockKey
}

func (l *heldLocks) Lock(ctx context.Context, org core.OrganizationID, k port.LockKey) error {
	l.keys = append(l.keys, k)
	return l.inner.Lock(ctx, org, k)
}

type heldLocksTx struct {
	inner port.Transactor
	rec   *heldLocks
}

func (t *heldLocksTx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	return t.inner.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		t.rec.keys = nil // locks end with their transaction
		t.rec.inner, r.Locks = r.Locks, t.rec
		return fn(ctx, r)
	})
}

// usageUnderLock is a [wallets.UsageSource] recording the locks held when
// the usage is computed.
type usageUnderLock struct {
	rec  *heldLocks
	held []port.LockKey
}

func (u *usageUnderLock) OngoingUsage(context.Context, port.Repos, *customer.Customer) ([]wallets.UsageFee, error) {
	u.held = slices.Clone(u.rec.keys)
	return nil, nil
}

// The ongoing usage, which may call the tax provider, is computed without
// the customer's prepaid_credits lock, which every finalization of the
// customer needs: lago computes it holding no customer lock
// (Customers::RefreshWalletJob → RefreshWalletsService). Only the
// allocation and the writes run under the lock.
//
// source: app/services/customers/refresh_wallets_service.rb:14, app/jobs/customers/refresh_wallet_job.rb:47
func TestRefreshOngoingBalancesComputesUsageOutsideLock(t *testing.T) {
	h, _ := setup(t, t0)
	c := h.Customer()
	h.Wallet(c, "0")
	rec := &heldLocks{}
	d := *h.Deps
	d.Tx = &heldLocksTx{inner: h.Deps.Tx, rec: rec}
	u := &usageUnderLock{rec: rec}
	s := wallets.New(&d).WithUsage(u)
	if _, err := s.RefreshOngoingBalances(h.Ctx, h.OrgID(), c.ID); err != nil {
		t.Fatal(err)
	}
	if lock := port.CustomerLock(customer.LockScopePrepaidCredits, c.ID); slices.Contains(u.held, lock) {
		t.Errorf("locks held computing usage = %v, want = without %v", u.held, lock)
	}
	if want := port.CustomerLock(customer.LockScopePrepaidCredits, c.ID); !slices.Contains(rec.keys, want) {
		t.Errorf("locks held writing = %v, want = %v", rec.keys, want)
	}
}

// changingUsage returns amounts[i] on its i-th call and runs between
// after its first call, as a finalization consuming credits would.
type changingUsage struct {
	amounts []int64
	calls   int
	between func()
}

func (u *changingUsage) OngoingUsage(context.Context, port.Repos, *customer.Customer) ([]wallets.UsageFee, error) {
	a := u.amounts[min(u.calls, len(u.amounts)-1)]
	u.calls++
	return []wallets.UsageFee{{FeeType: core.FeeTypeCharge, Currency: usd, Amount: a}}, nil
}

// afterTx runs then after the first transaction that computed the usage.
type afterTx struct {
	inner port.Transactor
	u     *changingUsage
	done  bool
}

func (a *afterTx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	err := a.inner.InTx(ctx, fn)
	if !a.done && a.u.calls == 1 {
		a.done = true
		a.u.between()
	}
	return err
}

// A balance changed between the usage computation and the lock (an
// invoice consumed credits, so the usage it billed left the current
// usage) makes the refresh compute the usage again.
func TestRefreshOngoingBalancesRecomputesWhenBalanceChanged(t *testing.T) {
	tests := []struct {
		name      string
		change    bool
		wantCalls int
		wantUsage int64
		wantBal   int64
	}{
		{"unchanged", false, 1, 500, 1000},
		{"balance changed", true, 2, 300, 800},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := setup(t, t0)
			c := h.Customer()
			w := h.Wallet(c, "10")
			u := &changingUsage{amounts: []int64{500, 300}, between: func() {
				if !tt.change {
					return
				}
				h.Do(func(ctx context.Context, r port.Repos) error {
					got, err := r.Wallets.Get(ctx, h.OrgID(), w.ID)
					if err != nil {
						return err
					}
					got.BalanceAmount -= 200
					return r.Wallets.Update(ctx, got)
				})
			}}
			d := *h.Deps
			d.Tx = &afterTx{inner: h.Deps.Tx, u: u}
			if _, err := wallets.New(&d).WithUsage(u).RefreshOngoingBalances(h.Ctx, h.OrgID(), c.ID); err != nil {
				t.Fatal(err)
			}
			got := h.ReloadWallet(w.ID)
			if u.calls != tt.wantCalls || got.OngoingUsageBalanceAmount != tt.wantUsage || got.OngoingBalanceAmount != tt.wantBal-tt.wantUsage {
				t.Errorf("calls, usage, ongoing = %d, %d, %d, want = %d, %d, %d",
					u.calls, got.OngoingUsageBalanceAmount, got.OngoingBalanceAmount, tt.wantCalls, tt.wantUsage, tt.wantBal-tt.wantUsage)
			}
		})
	}
}
