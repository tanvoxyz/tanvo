package wallets_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// topUp is the expected outcome of a rule run: the credits bought and
// granted, nil when no transaction of that kind is made.
type topUp struct {
	paid, granted *money.Decimal
}

func wantTopUp(t *testing.T, ts []*wallet.Transaction, want *topUp) {
	t.Helper()
	if want == nil {
		if len(ts) != 0 {
			t.Fatalf("transactions: got = %d, want = 0", len(ts))
		}
		return
	}
	var paid, granted *money.Decimal
	for _, tr := range ts {
		c := tr.CreditAmount
		switch tr.TransactionStatus {
		case wallet.TransactionStatusPurchased:
			paid = &c
		case wallet.TransactionStatusGranted:
			granted = &c
		}
	}
	for _, x := range []struct {
		name      string
		got, want *money.Decimal
	}{{"paid", paid, want.paid}, {"granted", granted, want.granted}} {
		switch {
		case x.got == nil && x.want == nil:
		case x.got == nil || x.want == nil || !eq(*x.got, *x.want):
			t.Errorf("%s credits: got = %v, want = %v", x.name, decString(x.got), decString(x.want))
		}
	}
}

func decString(d *money.Decimal) string {
	if d == nil {
		return "none"
	}
	return d.String()
}

// thresholdWallet is the wallet of the lago threshold top-up spec: a
// balance of 10 credits, an ongoing balance of 5.5.
func thresholdWallet(h *billingtest.Harness, opts ...func(*wallet.Wallet)) *wallet.Wallet {
	return h.Wallet(h.Customer(), "10", append([]func(*wallet.Wallet){func(w *wallet.Wallet) {
		w.OngoingBalanceAmount, w.OngoingUsageBalanceAmount = 550, 450
		w.CreditsOngoingBalance, w.CreditsOngoingUsageBalance = money.MustParse("5.5"), money.MustParse("4.5")
	}}, opts...)...)
}

func threshold(credits, paid, granted string) func(*wallet.RecurringTransactionRule) {
	return func(r *wallet.RecurringTransactionRule) {
		r.Trigger, r.Interval, r.ThresholdCredits = wallet.RecurringTriggerThreshold, 0, dec(credits)
		r.PaidCredits, r.GrantedCredits = money.MustParse(paid), money.MustParse(granted)
	}
}

func target(balance string) func(*wallet.RecurringTransactionRule) {
	return func(r *wallet.RecurringTransactionRule) {
		r.Method, r.TargetOngoingBalance = wallet.RecurringMethodTarget, dec(balance)
		r.PaidCredits, r.GrantedCredits = money.Zero, money.Zero
	}
}

func minTopUp(n int64) func(*wallet.Wallet) {
	return func(w *wallet.Wallet) { w.PaidTopUpMinAmount = &n }
}

// wantPO checks the purchase order number of every run transaction; ""
// expects none.
func wantPO(want string) func(t *testing.T, ts []*wallet.Transaction) {
	return func(t *testing.T, ts []*wallet.Transaction) {
		for _, tr := range ts {
			got := ""
			if tr.PurchaseOrderNumber != nil {
				got = *tr.PurchaseOrderNumber
			}
			if got != want {
				t.Errorf("purchase_order_number: got = %q, want = %q", got, want)
			}
		}
	}
}

func walletPO(po string) func(*wallet.Wallet) {
	return func(w *wallet.Wallet) { w.PurchaseOrderNumber = &po }
}

func rulePO(po string) func(*wallet.RecurringTransactionRule) {
	return func(r *wallet.RecurringTransactionRule) { r.PurchaseOrderNumber = &po }
}

func TestThresholdTopUp(t *testing.T) {
	ago := func(d time.Duration) *time.Time { return ptr(t0.Add(-d)) }
	failed := func(source wallet.TransactionSource, at *time.Time) func(*wallet.Transaction) {
		return func(t *wallet.Transaction) {
			t.Source, t.Status, t.SettledAt, t.FailedAt = source, wallet.SettlementStatusFailed, nil, at
		}
	}
	cases := []struct {
		name   string
		wallet []func(*wallet.Wallet)
		rule   []func(*wallet.RecurringTransactionRule)
		before func(h *billingtest.Harness, w *wallet.Wallet)
		want   *topUp
		check  func(t *testing.T, ts []*wallet.Transaction)
		err    []string
	}{
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:35
		{
			name:   "fixed top-up ignoring the limits",
			wallet: []func(*wallet.Wallet){minTopUp(20550)},
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3"), func(r *wallet.RecurringTransactionRule) {
				r.IgnorePaidTopUpLimits = true
			}},
			want: &topUp{dec("10"), dec("3")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				for _, tr := range ts {
					if tr.Source != wallet.TransactionSourceThreshold || tr.InvoiceRequiresSuccessfulPayment || tr.Name != nil {
						t.Errorf("got = (%s, %v, %v), want = (threshold, false, nil)", tr.Source, tr.InvoiceRequiresSuccessfulPayment, tr.Name)
					}
				}
			},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:204
		{
			name: "rule purchase order number",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3"), rulePO("PO-RULE-123")},
			want: &topUp{dec("10"), dec("3")}, check: wantPO("PO-RULE-123"),
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:227
		{
			name:   "wallet purchase order number under a blank rule one",
			wallet: []func(*wallet.Wallet){walletPO("PO-WALLET-123")},
			rule:   []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3"), rulePO("   ")},
			want:   &topUp{dec("10"), dec("3")}, check: wantPO("PO-WALLET-123"),
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:263
		{
			name: "no purchase order number",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			want: &topUp{dec("10"), dec("3")}, check: wantPO(""),
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:64
		{
			name: "rule requires a successful payment",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3"), func(r *wallet.RecurringTransactionRule) {
				r.InvoiceRequiresSuccessfulPayment = true
			}},
			want: &topUp{dec("10"), dec("3")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if p := ts[0]; !p.InvoiceRequiresSuccessfulPayment || p.Status != wallet.SettlementStatusPending {
					t.Errorf("purchase: got = (%v, %s), want = (true, pending)", p.InvoiceRequiresSuccessfulPayment, p.Status)
				}
			},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:88
		{
			name:   "rule keeps the wallet limits",
			wallet: []func(*wallet.Wallet){minTopUp(20550)},
			rule:   []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			err:    []string{"paid_credits/amount_below_minimum"},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:110
		{
			name: "transaction metadata",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3"), func(r *wallet.RecurringTransactionRule) {
				r.TransactionMetadata = core.Metadata{{Key: "valid_value", Value: "also_valid"}}
			}},
			want: &topUp{dec("10"), dec("3")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if m := ts[0].Metadata; len(m) != 1 || m[0].Key != "valid_value" {
					t.Errorf("metadata: got = %v, want = [valid_value=also_valid]", m)
				}
			},
		},
		{
			name: "transaction name",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3"), func(r *wallet.RecurringTransactionRule) {
				r.TransactionName = ptr("Recurring Transaction Rule")
			}},
			want: &topUp{dec("10"), dec("3")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if n := ts[0].Name; n == nil || *n != "Recurring Transaction Rule" {
					t.Errorf("name: got = %v, want = Recurring Transaction Rule", n)
				}
			},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:248
		{
			name: "several top-ups short",
			wallet: []func(*wallet.Wallet){func(w *wallet.Wallet) {
				w.BalanceAmount, w.CreditsBalance = 5000, money.MustParse("50")
				w.OngoingBalanceAmount, w.CreditsOngoingBalance = -42980, money.MustParse("-429.8")
			}},
			rule: []func(*wallet.RecurringTransactionRule){threshold("10", "50", "0")},
			want: &topUp{paid: dec("450")},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:262
		{name: "threshold not crossed", rule: []func(*wallet.RecurringTransactionRule){threshold("2", "10", "3")}},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:276
		{
			name: "pending transactions",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				addTransaction(h, w, func(t *wallet.Transaction) {
					t.Status, t.SettledAt, t.Amount, t.CreditAmount = wallet.SettlementStatusPending, nil, 100, money.One
				})
			},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:280
		{
			name:   "pending credits at a rate not one",
			wallet: []func(*wallet.Wallet){func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.1") }},
			rule:   []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				addTransaction(h, w, func(t *wallet.Transaction) {
					t.Status, t.SettledAt, t.Amount, t.CreditAmount = wallet.SettlementStatusPending, nil, 10, money.One
				})
			},
		},
		{
			name: "pending purchase of the rule",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				rule := rules(h, w.ID)[0]
				addTransaction(h, w, func(t *wallet.Transaction) {
					t.Status, t.SettledAt, t.Amount, t.CreditAmount = wallet.SettlementStatusPending, nil, 0, money.Zero
					t.RecurringTransactionRuleID = rule.ID
				})
			},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:300
		{
			name: "expired rule",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "0", "0"), target("200"), func(r *wallet.RecurringTransactionRule) {
				r.ExpirationAt = ago(24 * time.Hour)
			}},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:318
		{
			name:   "target",
			wallet: []func(*wallet.Wallet){minTopUp(20550)},
			rule:   []func(*wallet.RecurringTransactionRule){threshold("6", "0", "0"), target("200")},
			want:   &topUp{paid: dec("205.5")},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:345
		{
			name:   "target above the maximum",
			wallet: []func(*wallet.Wallet){func(w *wallet.Wallet) { w.PaidTopUpMaxAmount = ptr[int64](10000) }},
			rule:   []func(*wallet.RecurringTransactionRule){threshold("6", "0", "0"), target("200")},
			want:   &topUp{paid: dec("194.5")},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:362
		{
			name:   "target granted",
			wallet: []func(*wallet.Wallet){minTopUp(20550)},
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "0", "0"), target("200"), func(r *wallet.RecurringTransactionRule) {
				r.GrantsTargetTopUp = true
			}},
			want: &topUp{granted: dec("194.5")},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:410
		{
			name: "recent declined top-up",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				addTransaction(h, w, failed(wallet.TransactionSourceThreshold, ago(10*time.Minute)))
			},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:425
		{
			name: "declined top-up older than the back-off",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				addTransaction(h, w, failed(wallet.TransactionSourceThreshold, ago(wallet.ThresholdSuppression+time.Minute)))
			},
			want: &topUp{dec("10"), dec("3")},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:431
		{
			name: "top-up settled after the decline",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				addTransaction(h, w, failed(wallet.TransactionSourceThreshold, ago(10*time.Minute)))
				addTransaction(h, w, func(t *wallet.Transaction) {
					t.Source, t.CreatedAt, t.SettledAt = wallet.TransactionSourceThreshold, *ago(time.Minute), ago(time.Minute)
				})
			},
			want: &topUp{dec("10"), dec("3")},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:443
		{
			name: "only granted credits after the decline",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				addTransaction(h, w, failed(wallet.TransactionSourceThreshold, ago(10*time.Minute)))
				addTransaction(h, w, granted, func(t *wallet.Transaction) {
					t.Source, t.SettledAt = wallet.TransactionSourceThreshold, ago(time.Minute)
				})
			},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:457
		{
			name: "earlier top-up settled after the decline",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				addTransaction(h, w, failed(wallet.TransactionSourceThreshold, ago(10*time.Minute)))
				addTransaction(h, w, func(t *wallet.Transaction) { t.CreatedAt, t.SettledAt = *ago(2 * time.Hour), ago(time.Minute) })
			},
			want: &topUp{dec("10"), dec("3")},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:471
		{
			name: "rule only grants credits",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "0", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				addTransaction(h, w, failed(wallet.TransactionSourceThreshold, ago(10*time.Minute)))
			},
			want: &topUp{granted: dec("3")},
		},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:478
		{
			name: "declined manual transaction",
			rule: []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
			before: func(h *billingtest.Harness, w *wallet.Wallet) {
				addTransaction(h, w, failed(wallet.TransactionSourceManual, ago(10*time.Minute)))
			},
			want: &topUp{dec("10"), dec("3")},
		},
		{
			name:   "terminated wallet",
			wallet: []func(*wallet.Wallet){terminated(t0)},
			rule:   []func(*wallet.RecurringTransactionRule){threshold("6", "10", "3")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := thresholdWallet(h, tc.wallet...)
			addRule(h, w, tc.rule...)
			if tc.before != nil {
				tc.before(h, w)
			}
			ts, err := s.ThresholdTopUp(h.Ctx, h.OrgID(), w.ID)
			wantCodes(t, err, tc.err...)
			if err != nil {
				return
			}
			wantTopUp(t, ts, tc.want)
			if tc.check != nil {
				tc.check(t, ts)
			}
		})
	}
}

func TestThresholdTopUpBurst(t *testing.T) {
	purchases := func(n int, opts ...func(*wallet.Transaction)) func(h *billingtest.Harness, w *wallet.Wallet) {
		return func(h *billingtest.Harness, w *wallet.Wallet) {
			for range n {
				addTransaction(h, w, append([]func(*wallet.Transaction){func(t *wallet.Transaction) {
					t.Source = wallet.TransactionSourceThreshold
				}}, opts...)...)
			}
		}
	}
	cases := []struct {
		name   string
		before func(h *billingtest.Harness, w *wallet.Wallet)
		logged bool
	}{
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:513
		{name: "fewer top-ups than the burst", before: purchases(2)},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:518
		{name: "top-ups older than the window", before: purchases(3, func(t *wallet.Transaction) {
			t.CreatedAt = t0.Add(-11 * time.Minute)
		})},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:530
		{name: "manual top-ups", before: purchases(3, func(t *wallet.Transaction) { t.Source = wallet.TransactionSourceManual })},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:535
		{name: "outbound transactions", before: purchases(3, func(t *wallet.Transaction) {
			t.TransactionType, t.TransactionStatus = wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced
		})},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:549
		{name: "granted top-ups", before: func(h *billingtest.Harness, w *wallet.Wallet) {
			purchases(2)(h, w)
			purchases(3, granted)(h, w)
		}},
		// source: spec/services/wallets/threshold_top_up_service_spec.rb:564
		{name: "burst", before: purchases(3), logged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := billingtest.New(t, t0)
			var logs bytes.Buffer
			h.Deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			s := wallets.New(h.Deps)
			w := thresholdWallet(h)
			addRule(h, w, threshold("6", "10", "3"))
			tc.before(h, w)
			ts, err := s.ThresholdTopUp(h.Ctx, h.OrgID(), w.ID)
			if err != nil {
				t.Fatal(err)
			}
			// source: spec/services/wallets/threshold_top_up_service_spec.rb:503
			wantTopUp(t, ts, &topUp{dec("10"), dec("3")})
			if got := strings.Contains(logs.String(), "automatic wallet top-up burst"); got != tc.logged {
				t.Errorf("burst logged: got = %v, want = %v", got, tc.logged)
			}
		})
	}
}

func TestRunIntervalTopUps(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2 Jan 2006", s)
		if err != nil {
			panic(err)
		}
		return d
	}
	noumea, err := time.LoadLocation("Pacific/Noumea")
	if err != nil {
		t.Fatal(err)
	}
	interval := func(i period.Interval) func(*wallet.RecurringTransactionRule) {
		return func(r *wallet.RecurringTransactionRule) { r.Interval = i }
	}
	started := func(s string) func(*wallet.RecurringTransactionRule) {
		return func(r *wallet.RecurringTransactionRule) { r.StartedAt = ptr(day(s)) }
	}
	credits := func(paid, granted string) func(*wallet.RecurringTransactionRule) {
		return func(r *wallet.RecurringTransactionRule) {
			r.PaidCredits, r.GrantedCredits = money.MustParse(paid), money.MustParse(granted)
		}
	}
	ongoing := func(credits string) func(*wallet.Wallet) {
		return func(w *wallet.Wallet) { w.CreditsOngoingBalance = money.MustParse(credits) }
	}
	fixed := &topUp{dec("10"), dec("10")}
	cases := []struct {
		name     string
		created  string
		at       time.Time
		timezone *time.Location
		wallet   []func(*wallet.Wallet)
		rule     []func(*wallet.RecurringTransactionRule)
		before   func(h *billingtest.Harness, w *wallet.Wallet, at time.Time)
		want     *topUp
		check    func(t *testing.T, ts []*wallet.Transaction)
	}{
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:61
		{name: "weekly on the day", created: "20 Feb 2021", at: day("18 Jun 2022"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly)}, want: fixed},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:455
		{name: "rule purchase order number", created: "20 Feb 2021", at: day("18 Jun 2022"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly), rulePO("PO-RULE-123")}, want: fixed, check: wantPO("PO-RULE-123")},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:482
		{name: "wallet purchase order number", created: "20 Feb 2021", at: day("18 Jun 2022"), wallet: []func(*wallet.Wallet){walletPO("PO-WALLET-123")}, rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly)}, want: fixed, check: wantPO("PO-WALLET-123")},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:519
		{name: "no purchase order number", created: "20 Feb 2021", at: day("18 Jun 2022"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly)}, want: fixed, check: wantPO("")},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:69
		{name: "weekly on another day", created: "20 Feb 2021", at: day("19 Jun 2022"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly)}},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:88
		{
			name: "weekly with a transaction name", created: "20 Feb 2021", at: day("18 Jun 2022"),
			rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly), func(r *wallet.RecurringTransactionRule) {
				r.TransactionName = ptr("Recurring Transaction Rule")
			}},
			want: fixed,
			check: func(t *testing.T, ts []*wallet.Transaction) {
				for _, tr := range ts {
					if tr.Name == nil || *tr.Name != "Recurring Transaction Rule" || tr.Source != wallet.TransactionSourceInterval {
						t.Errorf("got = (%v, %s), want = (Recurring Transaction Rule, interval)", tr.Name, tr.Source)
					}
				}
			},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:100
		{
			name: "weekly before the start", created: "20 Feb 2021", at: day("18 Jun 2022"),
			rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly), started("20 Jun 2022")},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:106
		{
			name: "weekly a week after the start", created: "20 Feb 2021", at: day("27 Jun 2022"),
			rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly), started("20 Jun 2022")},
			want: fixed,
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:120
		{
			name: "monthly with a start in the future", created: "20 Feb 2021", at: day("18 Jun 2022"),
			rule: []func(*wallet.RecurringTransactionRule){interval(period.Monthly), started("18 Jul 2022")},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:141
		{
			name: "weekly target", created: "20 Feb 2021", at: day("18 Jun 2022"),
			wallet: []func(*wallet.Wallet){ongoing("50"), minTopUp(20000)},
			rule:   []func(*wallet.RecurringTransactionRule){interval(period.Weekly), target("200")},
			want:   &topUp{paid: dec("200")},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:166
		{
			name: "weekly target granted", created: "20 Feb 2021", at: day("18 Jun 2022"),
			wallet: []func(*wallet.Wallet){ongoing("50"), minTopUp(20000)},
			rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly), target("200"), func(r *wallet.RecurringTransactionRule) {
				r.GrantsTargetTopUp = true
			}},
			want: &topUp{granted: dec("150")},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:184
		{name: "monthly on the day", created: "20 Feb 2021", at: day("20 Mar 2021"), want: fixed},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:192
		{name: "monthly on another day", created: "20 Feb 2021", at: day("21 Mar 2021")},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:202
		{name: "monthly from a 31st", created: "31 Mar 2021", at: day("30 Apr 2021"), want: fixed},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:215
		{name: "monthly a month after the creation", created: "31 Mar 2025", at: day("30 Apr 2025"), rule: []func(*wallet.RecurringTransactionRule){started("15 Apr 2025")}},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:223
		{name: "monthly a month after the start", created: "31 Mar 2025", at: day("15 May 2025"), rule: []func(*wallet.RecurringTransactionRule){started("15 Apr 2025")}, want: fixed},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:239
		{name: "quarterly on the day", created: "20 Feb 2021", at: day("20 May 2021"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Quarterly)}, want: fixed},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:247
		{name: "quarterly on another day", created: "20 Feb 2021", at: day("21 May 2021"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Quarterly)}},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:257
		{name: "quarterly from March", created: "15 Mar 2021", at: day("15 Sep 2022"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Quarterly)}, want: fixed},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:270
		{name: "quarterly from a 31st", created: "31 Mar 2021", at: day("30 Jun 2022"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Quarterly)}, want: fixed},
		{name: "quarterly off the quarter", created: "20 Feb 2021", at: day("20 Apr 2021"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Quarterly)}},
		{name: "semiannual", created: "20 Feb 2021", at: day("20 Aug 2021"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Semiannual)}, want: fixed},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:284
		{name: "yearly on the day", created: "20 Feb 2021", at: day("20 Feb 2022"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Yearly)}, want: fixed},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:292
		{name: "yearly on another day", created: "20 Feb 2021", at: day("21 Feb 2022"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Yearly)}},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:302
		{name: "yearly from February 29", created: "29 Feb 2020", at: day("28 Feb 2022"), rule: []func(*wallet.RecurringTransactionRule){interval(period.Yearly)}, want: fixed},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:317
		{name: "wallet creation day", created: "20 Feb 2021", at: day("20 Feb 2021")},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:326
		{name: "wallet creation day in the customer time zone", created: "20 Feb 2021", at: day("20 Feb 2021").Add(10 * time.Hour), timezone: noumea},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:350
		{
			name: "top-up already made that day", created: "20 Feb 2021", at: day("20 Mar 2021").Add(12 * time.Hour),
			before: func(h *billingtest.Harness, w *wallet.Wallet, at time.Time) {
				addTransaction(h, w, func(t *wallet.Transaction) {
					t.Source, t.CreatedAt = wallet.TransactionSourceInterval, at.Add(-time.Hour)
				})
			},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:360
		{
			name: "top-up already made that day in the customer time zone", created: "20 Feb 2021",
			at: day("20 Mar 2021").Add(22 * time.Hour), timezone: noumea,
			before: func(h *billingtest.Harness, w *wallet.Wallet, at time.Time) {
				addTransaction(h, w, func(t *wallet.Transaction) {
					t.Source, t.CreatedAt = wallet.TransactionSourceInterval, at.Add(-time.Hour)
				})
			},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:386
		{
			name: "rule requires a successful payment", created: "20 Feb 2021", at: day("18 Jun 2022"),
			rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly), func(r *wallet.RecurringTransactionRule) {
				r.InvoiceRequiresSuccessfulPayment = true
			}},
			want: fixed,
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if !ts[0].InvoiceRequiresSuccessfulPayment {
					t.Errorf("invoice requires successful payment: got = false, want = true")
				}
			},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:415
		{
			name: "rule metadata", created: "20 Feb 2021", at: day("18 Jun 2022"),
			rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly), func(r *wallet.RecurringTransactionRule) {
				r.TransactionMetadata = core.Metadata{{Key: "valid_value", Value: "also_valid"}}
			}},
			want: fixed,
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if len(ts[0].Metadata) != 1 {
					t.Errorf("metadata: got = %v, want = 1 item", ts[0].Metadata)
				}
			},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:622
		{
			name: "expired rule", created: "20 Feb 2021", at: day("6 Mar 2021"),
			rule: []func(*wallet.RecurringTransactionRule){interval(period.Weekly), func(r *wallet.RecurringTransactionRule) {
				r.ExpirationAt = ptr(day("20 Feb 2021").Add(2 * time.Hour))
			}},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:650
		{
			name: "target reached", created: "20 Feb 2021", at: day("6 Mar 2021"),
			wallet: []func(*wallet.Wallet){ongoing("500")},
			rule:   []func(*wallet.RecurringTransactionRule){interval(period.Weekly), target("500")},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:673
		{
			name: "fixed without paid credits", created: "20 Feb 2021", at: day("6 Mar 2021"),
			wallet: []func(*wallet.Wallet){ongoing("1000")},
			rule:   []func(*wallet.RecurringTransactionRule){interval(period.Weekly), credits("0", "100")},
			want:   &topUp{granted: dec("100")},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:696
		{
			name: "target without granted credits", created: "20 Feb 2021", at: day("6 Mar 2021"),
			wallet: []func(*wallet.Wallet){ongoing("0")},
			rule:   []func(*wallet.RecurringTransactionRule){interval(period.Weekly), target("500"), credits("100", "0")},
			want:   &topUp{paid: dec("500")},
		},
		// source: spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:720
		{
			name: "target with granted credits", created: "20 Feb 2021", at: day("6 Mar 2021"),
			wallet: []func(*wallet.Wallet){ongoing("0")},
			rule:   []func(*wallet.RecurringTransactionRule){interval(period.Weekly), target("500"), credits("100", "50")},
			want:   &topUp{paid: dec("500")},
		},
		{
			name: "terminated wallet", created: "20 Feb 2021", at: day("20 Mar 2021"),
			wallet: []func(*wallet.Wallet){terminated(day("1 Mar 2021"))},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, tc.at)
			c := h.Customer(func(c *customer.Customer) { c.Timezone = tc.timezone })
			w := h.Wallet(c, "0", append([]func(*wallet.Wallet){func(w *wallet.Wallet) {
				w.CreatedAt = day(tc.created)
			}}, tc.wallet...)...)
			addRule(h, w, tc.rule...)
			if tc.before != nil {
				tc.before(h, w, tc.at)
			}
			ts, err := s.RunIntervalTopUps(h.Ctx, h.OrgID(), tc.at)
			if err != nil {
				t.Fatal(err)
			}
			wantTopUp(t, ts, tc.want)
			if tc.check != nil {
				tc.check(t, ts)
			}
		})
	}
}

func TestExpireWallets(t *testing.T) {
	h, s := setup(t, t0)
	c := h.Customer()
	expired := h.Wallet(c, "10", func(w *wallet.Wallet) { w.ExpirationAt = ptr(t0.Add(-time.Hour)) })
	later := h.Wallet(c, "10", func(w *wallet.Wallet) { w.ExpirationAt = ptr(t0.Add(time.Hour)) })
	rule := addRule(h, later, func(r *wallet.RecurringTransactionRule) { r.ExpirationAt = ptr(t0.Add(-time.Minute)) })
	if err := s.ExpireWallets(h.Ctx, h.OrgID(), t0); err != nil {
		t.Fatal(err)
	}
	if got := h.ReloadWallet(expired.ID); got.Active() {
		t.Errorf("expired wallet: got = %s, want = terminated", got.Status)
	}
	if got := h.ReloadWallet(later.ID); !got.Active() {
		t.Errorf("wallet expiring later: got = %s, want = active", got.Status)
	}
	if got := rules(h, later.ID)[0]; got.ID != rule.ID || got.Status != wallet.RuleStatusTerminated {
		t.Errorf("expired rule: got = %s, want = terminated", got.Status)
	}
}

func TestIntervalDue(t *testing.T) {
	d := core.NewDate
	cases := []struct {
		interval      period.Interval
		anchor, today core.Date
		want          bool
	}{
		{period.Weekly, d(2021, time.February, 20), d(2022, time.June, 18), true},
		{period.Weekly, d(2021, time.February, 20), d(2022, time.June, 19), false},
		{period.Monthly, d(2021, time.January, 31), d(2021, time.February, 28), true},
		{period.Monthly, d(2020, time.January, 31), d(2020, time.February, 28), false},
		{period.Monthly, d(2020, time.January, 31), d(2020, time.February, 29), true},
		{period.Monthly, d(2021, time.January, 30), d(2021, time.March, 30), true},
		{period.Monthly, d(2021, time.January, 30), d(2021, time.March, 31), false},
		{period.Quarterly, d(2021, time.November, 30), d(2022, time.February, 28), true},
		{period.Quarterly, d(2021, time.November, 30), d(2022, time.March, 30), false},
		{period.Semiannual, d(2021, time.March, 10), d(2021, time.September, 10), true},
		{period.Semiannual, d(2021, time.March, 10), d(2021, time.June, 10), false},
		{period.Yearly, d(2020, time.February, 29), d(2021, time.February, 28), true},
		{period.Yearly, d(2020, time.February, 29), d(2024, time.February, 28), false},
		{period.Yearly, d(2020, time.February, 29), d(2024, time.February, 29), true},
		{period.Yearly, d(2020, time.March, 1), d(2021, time.March, 1), true},
	}
	for _, tc := range cases {
		if got := wallets.IntervalDue(tc.interval, tc.anchor, tc.today); got != tc.want {
			t.Errorf("IntervalDue(%v, %v, %v): got = %v, want = %v", tc.interval, tc.anchor, tc.today, got, tc.want)
		}
	}
}

func TestHandleJob(t *testing.T) {
	h, s := setup(t, t0)
	w := thresholdWallet(h)
	addRule(h, w, threshold("6", "10", "3"))
	s = s.WithUsage(fakeUsage{w.CustomerID: {{FeeType: core.FeeTypeCharge, Currency: usd, Amount: 600}}})
	if _, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{WalletID: w.ID, GrantedCredits: dec("1")}); err != nil {
		t.Fatal(err)
	}
	// The grant refreshes the ongoing balance (11 − 6 = 5), which crosses the
	// threshold and tops the wallet up.
	runJobs(t, h, s)
	var topUps int
	for _, tr := range transactions(h, w.ID) {
		if tr.Source == wallet.TransactionSourceThreshold {
			topUps++
		}
	}
	if topUps != 2 {
		t.Errorf("threshold transactions: got = %d, want = 2", topUps)
	}
}
