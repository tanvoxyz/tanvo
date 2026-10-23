package wallets_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func consumptions(h *billingtest.Harness, out core.WalletTransactionID) []*wallet.Consumption {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*wallet.Consumption, error) {
		return r.WalletTransactions.ListConsumptionsByOutbound(ctx, h.OrgID(), out)
	})
}

func transaction(h *billingtest.Harness, id core.WalletTransactionID) *wallet.Transaction {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*wallet.Transaction, error) {
		return r.WalletTransactions.Get(ctx, h.OrgID(), id)
	})
}

func remaining(n int64) func(*wallet.Transaction) {
	return func(t *wallet.Transaction) { t.RemainingAmount = &n }
}

func traceable(w *wallet.Wallet) { w.Traceable = true }

func metadata(n int) core.Metadata {
	m := make(core.Metadata, n)
	for i := range m {
		m[i] = core.MetadataItem{Key: "key" + strconv.Itoa(i), Value: "value"}
	}
	return m
}

func TestValidateTransactions(t *testing.T) {
	type env struct {
		h *billingtest.Harness
		c *customer.Customer
		w *wallet.Wallet
	}
	cases := []struct {
		name     string
		currency money.Currency
		wallet   func(*wallet.Wallet)
		balance  string
		in       func(e env, in *wallets.TransactionInput)
		want     []string
	}{
		// source: spec/services/wallet_transactions/validate_service_spec.rb:34
		{name: "valid", in: func(_ env, in *wallets.TransactionInput) { in.PaidCredits = dec("1") }},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:38
		{
			name: "wallet not found",
			in: func(e env, in *wallets.TransactionInput) {
				in.WalletID = core.NewID[core.WalletID](e.h.IDs)
			},
			want: []string{"wallet_id/wallet_not_found"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:107
		{
			name: "paid credits < 0",
			in:   func(_ env, in *wallets.TransactionInput) { in.PaidCredits = dec("-1") },
			want: []string{"paid_credits/invalid_paid_credits", "paid_credits/invalid_amount"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:107
		{
			name: "granted credits < 0",
			in:   func(_ env, in *wallets.TransactionInput) { in.GrantedCredits = dec("-1") },
			want: []string{"granted_credits/invalid_granted_credits", "granted_credits/invalid_amount"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:107
		{
			name: "voided credits < 0",
			in:   func(_ env, in *wallets.TransactionInput) { in.VoidedCredits = dec("-1") },
			want: []string{"voided_credits/invalid_voided_credits", "voided_credits/invalid_amount"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:98
		{
			name: "paid credits >= 10^25",
			in:   func(_ env, in *wallets.TransactionInput) { in.PaidCredits = dec("1e25") },
			want: []string{"paid_credits/invalid_paid_credits", "paid_credits/invalid_amount"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:98
		{
			name: "granted credits >= 10^25",
			in:   func(_ env, in *wallets.TransactionInput) { in.GrantedCredits = dec("1e25") },
			want: []string{"granted_credits/invalid_granted_credits", "granted_credits/invalid_amount"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:98
		{
			name: "voided credits >= 10^25",
			in:   func(_ env, in *wallets.TransactionInput) { in.VoidedCredits = dec("1e25") },
			want: []string{"voided_credits/invalid_voided_credits", "voided_credits/invalid_amount"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:126
		{
			name: "all credits = 0",
			in: func(_ env, in *wallets.TransactionInput) {
				in.PaidCredits, in.GrantedCredits, in.VoidedCredits = dec("0"), dec("0"), dec("0")
			},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:139
		{
			name:    "paid credits round to zero",
			wallet:  func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.01") },
			balance: "10",
			in:      func(_ env, in *wallets.TransactionInput) { in.PaidCredits = dec("0.4") },
			want:    []string{"paid_credits/amount_rounds_to_zero"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:148
		{
			name:    "granted credits round to zero",
			wallet:  func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.01") },
			balance: "10",
			in: func(_ env, in *wallets.TransactionInput) {
				in.PaidCredits, in.GrantedCredits = dec("0"), dec("0.4")
			},
			want: []string{"granted_credits/amount_rounds_to_zero"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:158
		{
			name:    "credits with a non-zero value",
			wallet:  func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.01") },
			balance: "10",
			in:      func(_ env, in *wallets.TransactionInput) { in.PaidCredits = dec("1") },
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:167
		{
			name:    "strictly zero credits",
			wallet:  func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.01") },
			balance: "10",
			in: func(_ env, in *wallets.TransactionInput) {
				in.PaidCredits, in.GrantedCredits = dec("0"), dec("0")
			},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:177
		{
			name:    "voided credits round to zero",
			wallet:  func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.01") },
			balance: "10",
			in:      func(_ env, in *wallets.TransactionInput) { in.VoidedCredits = dec("0.4") },
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:189
		{
			name:     "three-decimal currency",
			currency: money.MustCurrency("KWD"),
			wallet: func(w *wallet.Wallet) {
				w.Currency, w.RateAmount = money.MustCurrency("KWD"), money.MustParse("0.001")
			},
			balance: "10",
			in:      func(_ env, in *wallets.TransactionInput) { in.PaidCredits = dec("1") },
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:218
		{
			name:   "voided transaction",
			wallet: traceable,
			in: func(e env, in *wallets.TransactionInput) {
				in.VoidedTransactionID = addTransaction(e.h, e.w, granted, remaining(500)).ID
			},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:223
		{
			name: "voided transaction of a wallet not traceable",
			in: func(e env, in *wallets.TransactionInput) {
				in.VoidedTransactionID = addTransaction(e.h, e.w, granted, remaining(500)).ID
			},
			want: []string{"voided_transaction_id/wallet_not_traceable"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:232
		{
			name:   "voided transaction of another wallet",
			wallet: traceable,
			in: func(e env, in *wallets.TransactionInput) {
				other := e.h.Wallet(e.c, "0", traceable)
				in.VoidedTransactionID = addTransaction(e.h, other, granted, remaining(500)).ID
			},
			want: []string{"voided_transaction_id/wallet_transaction_not_found"},
		},
		{
			name:   "voided transaction outbound",
			wallet: traceable,
			in: func(e env, in *wallets.TransactionInput) {
				in.VoidedTransactionID = addTransaction(e.h, e.w, func(t *wallet.Transaction) {
					t.TransactionType, t.TransactionStatus = wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced
				}).ID
			},
			want: []string{"voided_transaction_id/wallet_transaction_not_found"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:241
		{
			name:   "voided transaction fully consumed",
			wallet: traceable,
			in: func(e env, in *wallets.TransactionInput) {
				in.VoidedTransactionID = addTransaction(e.h, e.w, granted, remaining(0)).ID
			},
			want: []string{"voided_transaction_id/no_remaining_amount"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:253
		{
			name:   "voided transaction unsettled",
			wallet: traceable,
			in: func(e env, in *wallets.TransactionInput) {
				in.VoidedTransactionID = addTransaction(e.h, e.w, func(t *wallet.Transaction) {
					t.Status, t.SettledAt = wallet.SettlementStatusPending, nil
				}).ID
			},
			want: []string{"voided_transaction_id/no_remaining_amount"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:266
		{
			name: "insufficient credits",
			in:   func(_ env, in *wallets.TransactionInput) { in.VoidedCredits = dec("1") },
			want: []string{"voided_credits/insufficient_credits"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:294
		{
			name: "maximum number of metadata pairs",
			in: func(_ env, in *wallets.TransactionInput) {
				in.PaidCredits, in.Metadata = dec("1"), metadata(15)
			},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:313
		{
			name: "too many metadata pairs",
			in: func(_ env, in *wallets.TransactionInput) {
				in.PaidCredits, in.Metadata = dec("1"), metadata(16)
			},
			want: []string{"metadata.items/too_many_items"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:332
		{name: "name", in: func(_ env, in *wallets.TransactionInput) { in.PaidCredits, in.Name = dec("1"), ptr("Top-up") }},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:338
		{name: "blank name", in: func(_ env, in *wallets.TransactionInput) { in.PaidCredits, in.Name = dec("1"), ptr("") }},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:350
		{
			name: "name at maximum length",
			in: func(_ env, in *wallets.TransactionInput) {
				in.PaidCredits, in.Name = dec("1"), ptr(strings.Repeat("a", 255))
			},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:356
		{
			name: "name too long",
			in: func(_ env, in *wallets.TransactionInput) {
				in.PaidCredits, in.Name = dec("1"), ptr(strings.Repeat("a", 256))
			},
			want: []string{"name/value_is_too_long"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:395
		{
			name: "provider payment method",
			in: func(e env, in *wallets.TransactionInput) {
				in.PaidCredits = dec("1")
				in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider, ID: paymentMethod(e.h, e.c).ID}
			},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:406
		{
			name: "manual payment method",
			in: func(_ env, in *wallets.TransactionInput) {
				in.PaidCredits = dec("1")
				in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeManual}
			},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:419
		{
			name: "invalid payment method type",
			in: func(e env, in *wallets.TransactionInput) {
				in.PaidCredits = dec("1")
				in.PaymentMethod = &wallets.PaymentMethod{Type: "invalid", ID: paymentMethod(e.h, e.c).ID}
			},
			want: []string{"payment_method/invalid_payment_method"},
		},
		// source: spec/services/wallet_transactions/validate_service_spec.rb:433
		{
			name: "invalid payment method reference",
			in: func(e env, in *wallets.TransactionInput) {
				in.PaidCredits = dec("1")
				in.PaymentMethod = &wallets.PaymentMethod{
					Type: core.PaymentMethodTypeProvider, ID: core.NewID[core.PaymentMethodID](e.h.IDs),
				}
			},
			want: []string{"payment_method/invalid_payment_method"},
		},
		{
			name:   "terminated wallet",
			wallet: terminated(t0),
			in:     func(_ env, in *wallets.TransactionInput) { in.GrantedCredits = dec("1") },
			want:   []string{"wallet_id/wallet_is_terminated"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			c := h.Customer(func(c *customer.Customer) {
				if tc.currency != (money.Currency{}) {
					c.Currency = tc.currency
				}
			})
			balance := cmpOr(tc.balance, "0")
			var opts []func(*wallet.Wallet)
			if tc.wallet != nil {
				opts = append(opts, tc.wallet)
			}
			w := h.Wallet(c, balance, opts...)
			in := wallets.TransactionInput{WalletID: w.ID}
			tc.in(env{h, c, w}, &in)
			_, err := s.CreateTransactions(h.Ctx, h.OrgID(), in)
			wantCodes(t, err, tc.want...)
		})
	}
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func granted(t *wallet.Transaction) { t.TransactionStatus = wallet.TransactionStatusGranted }

func TestCreateTransactionsFromParams(t *testing.T) {
	// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:47
	h, s := setup(t, t0)
	c := h.Customer()
	w := h.Wallet(c, "10")
	ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{
		WalletID: w.ID, PaidCredits: dec("10.00"), GrantedCredits: dec("15.00"), VoidedCredits: dec("3.00"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(ts); got != 3 {
		t.Fatalf("transactions: got = %d, want = 3", got)
	}
	// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:51
	for _, tr := range ts {
		if tr.Priority != 50 || tr.Name != nil || tr.Source != wallet.TransactionSourceManual {
			t.Errorf("%s: got = (%d, %v, %s), want = (50, nil, manual)", tr.TransactionStatus, tr.Priority, tr.Name, tr.Source)
		}
	}
	// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:56
	want := map[wallet.TransactionStatus]string{
		wallet.TransactionStatusPurchased: "10", wallet.TransactionStatusGranted: "15", wallet.TransactionStatusVoided: "3",
	}
	for _, tr := range ts {
		wantDec(t, string(tr.TransactionStatus), tr.CreditAmount, want[tr.TransactionStatus])
	}
	// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:69
	// The purchase settles only once its credit invoice is paid:
	// 10 + 15 − 3.
	w = h.ReloadWallet(w.ID)
	if w.BalanceAmount != 2200 {
		t.Errorf("balance: got = %d, want = 2200", w.BalanceAmount)
	}
	wantDec(t, "credits balance", w.CreditsBalance, "22")
	// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:80
	if !slices.Contains(h.JobKinds(), kernel.JobRefreshWalletOngoingBalance) {
		t.Errorf("jobs: got = %v, want %s", h.JobKinds(), kernel.JobRefreshWalletOngoingBalance)
	}
	// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:85
	if got := countWebhooks(h, webhook.EventTypeWalletTransactionCreated); got != 3 {
		t.Errorf("wallet_transaction.created: got = %d, want = 3", got)
	}
}

func TestCreateTransactionsParams(t *testing.T) {
	cases := []struct {
		name   string
		wallet func(*wallet.Wallet)
		in     wallets.TransactionInput
		check  func(t *testing.T, ts []*wallet.Transaction)
	}{
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:97
		{
			name: "rounding",
			in:   wallets.TransactionInput{PaidCredits: dec("0"), GrantedCredits: dec("10.000000"), VoidedCredits: dec("4.28444999")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				v := ts[len(ts)-1]
				wantDec(t, "voided credits", v.CreditAmount, "4.28444")
				if v.Amount != 428 {
					t.Errorf("voided amount: got = %d, want = 428", v.Amount)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:130
		{
			name: "metadata",
			in:   wallets.TransactionInput{GrantedCredits: dec("1"), Metadata: core.Metadata{{Key: "k", Value: "v"}}},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if got := ts[0].Metadata; len(got) != 1 || got[0].Key != "k" {
					t.Errorf("metadata: got = %v, want = [k=v]", got)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:183
		{
			name: "purchase order number",
			in:   wallets.TransactionInput{PaidCredits: dec("1"), GrantedCredits: dec("1"), PurchaseOrderNumber: ptr("PO-123")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				for _, tr := range ts {
					if tr.PurchaseOrderNumber == nil || *tr.PurchaseOrderNumber != "PO-123" {
						t.Errorf("purchase_order_number: got = %v, want = PO-123", tr.PurchaseOrderNumber)
					}
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:149
		{
			name: "priority",
			in:   wallets.TransactionInput{PaidCredits: dec("1"), GrantedCredits: dec("1"), Priority: ptr(25)},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				for _, tr := range ts {
					if tr.Priority != 25 {
						t.Errorf("priority: got = %d, want = 25", tr.Priority)
					}
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:167
		{
			name: "source",
			in:   wallets.TransactionInput{PaidCredits: dec("1"), GrantedCredits: dec("1"), Source: wallet.TransactionSourceInterval},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				for _, tr := range ts {
					if tr.Source != wallet.TransactionSourceInterval {
						t.Errorf("source: got = %s, want = interval", tr.Source)
					}
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:214
		{
			name: "name",
			in:   wallets.TransactionInput{GrantedCredits: dec("1"), Name: ptr("Top-up")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if ts[0].Name == nil || *ts[0].Name != "Top-up" {
					t.Errorf("name: got = %v, want = Top-up", ts[0].Name)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:221
		{
			name: "blank name",
			in:   wallets.TransactionInput{GrantedCredits: dec("1"), Name: ptr("")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if ts[0].Name != nil {
					t.Errorf("name: got = %v, want = nil", *ts[0].Name)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:238
		{
			name: "invoice requires successful payment",
			in:   wallets.TransactionInput{PaidCredits: dec("1"), InvoiceRequiresSuccessfulPayment: ptr(true)},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if !ts[0].InvoiceRequiresSuccessfulPayment {
					t.Errorf("invoice requires successful payment: got = false, want = true")
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:256
		{
			name:   "invoice does not require successful payment over the wallet",
			wallet: func(w *wallet.Wallet) { w.InvoiceRequiresSuccessfulPayment = true },
			in:     wallets.TransactionInput{PaidCredits: dec("1"), InvoiceRequiresSuccessfulPayment: ptr(false)},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if ts[0].InvoiceRequiresSuccessfulPayment {
					t.Errorf("invoice requires successful payment: got = true, want = false")
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:268
		{
			name:   "invoice requires successful payment from the wallet",
			wallet: func(w *wallet.Wallet) { w.InvoiceRequiresSuccessfulPayment = true },
			in:     wallets.TransactionInput{PaidCredits: dec("1")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if !ts[0].InvoiceRequiresSuccessfulPayment || ts[0].Status != wallet.SettlementStatusPending {
					t.Errorf("got = (%v, %s), want = (true, pending)", ts[0].InvoiceRequiresSuccessfulPayment, ts[0].Status)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:285
		{
			name: "default payment method",
			in:   wallets.TransactionInput{PaidCredits: dec("1")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if ts[0].PaymentMethodType != core.PaymentMethodTypeProvider || !ts[0].PaymentMethodID.IsZero() {
					t.Errorf("payment method: got = (%s, %s), want = (provider, none)", ts[0].PaymentMethodType, ts[0].PaymentMethodID)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:293
		{
			name: "manual payment method",
			in:   wallets.TransactionInput{PaidCredits: dec("1"), PaymentMethod: &wallets.PaymentMethod{Type: core.PaymentMethodTypeManual}},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				if ts[0].PaymentMethodType != core.PaymentMethodTypeManual {
					t.Errorf("payment method type: got = %s, want = manual", ts[0].PaymentMethodType)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:338
		{
			name:   "paid credits above the maximum ignoring limits",
			wallet: func(w *wallet.Wallet) { w.PaidTopUpMaxAmount = ptr[int64](1000) },
			in:     wallets.TransactionInput{PaidCredits: dec("20"), IgnorePaidTopUpLimits: true},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				wantDec(t, "credits", ts[0].CreditAmount, "20")
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:364
		{
			name:   "paid credits below the minimum ignoring limits",
			wallet: func(w *wallet.Wallet) { w.PaidTopUpMinAmount = ptr[int64](1000) },
			in:     wallets.TransactionInput{PaidCredits: dec("5"), IgnorePaidTopUpLimits: true},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				wantDec(t, "credits", ts[0].CreditAmount, "5")
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:444
		{
			name: "decimal value",
			in:   wallets.TransactionInput{PaidCredits: dec("4.399999")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				wantDec(t, "credits", ts[0].CreditAmount, "4.40")
				if ts[0].Amount != 440 {
					t.Errorf("amount: got = %d, want = 440", ts[0].Amount)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:453
		{
			name:   "decimal value and small rate",
			wallet: func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.01") },
			in:     wallets.TransactionInput{PaidCredits: dec("4.399999")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				wantDec(t, "credits", ts[0].CreditAmount, "4")
				if ts[0].Amount != 4 {
					t.Errorf("amount: got = %d, want = 4", ts[0].Amount)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:463
		{
			name:   "decimal value and large rate",
			wallet: func(w *wallet.Wallet) { w.RateAmount = money.MustParse("100") },
			in:     wallets.TransactionInput{PaidCredits: dec("4.3789")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				wantDec(t, "credits", ts[0].CreditAmount, "4.3789")
				if ts[0].Amount != 43789 {
					t.Errorf("amount: got = %d, want = 43789", ts[0].Amount)
				}
			},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:539
		{
			name:   "granted credits from the amount and rate",
			wallet: func(w *wallet.Wallet) { w.RateAmount = money.MustParse("1.375") },
			in:     wallets.TransactionInput{GrantedCredits: dec("10.34567")},
			check: func(t *testing.T, ts []*wallet.Transaction) {
				wantDec(t, "credits", ts[0].CreditAmount, "10.34909")
				if ts[0].Amount != 1423 {
					t.Errorf("amount: got = %d, want = 1423", ts[0].Amount)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			c := h.Customer()
			var opts []func(*wallet.Wallet)
			if tc.wallet != nil {
				opts = append(opts, tc.wallet)
			}
			w := h.Wallet(c, "10", opts...)
			in := tc.in
			in.WalletID = w.ID
			ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), in)
			if err != nil {
				t.Fatal(err)
			}
			if len(ts) == 0 {
				t.Fatal("transactions: got = 0, want > 0")
			}
			tc.check(t, ts)
		})
	}
}

func TestCreateTransactionsJPY(t *testing.T) {
	// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:473
	h, s := setup(t, t0)
	jpy := money.MustCurrency("JPY")
	c := h.Customer(func(c *customer.Customer) { c.Currency = jpy })
	w := h.Wallet(c, "0", func(w *wallet.Wallet) { w.Currency, w.BalanceAmount, w.OngoingBalanceAmount = jpy, 0, 0 })
	ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{WalletID: w.ID, PaidCredits: dec("4.39999")})
	if err != nil {
		t.Fatal(err)
	}
	wantDec(t, "credits", ts[0].CreditAmount, "4")
	if ts[0].Amount != 4 {
		t.Errorf("amount: got = %d, want = 4", ts[0].Amount)
	}
}

func TestCreateTransactionsErrors(t *testing.T) {
	cases := []struct {
		name   string
		wallet func(*wallet.Wallet)
		in     wallets.TransactionInput
		want   []string
	}{
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:115
		{
			name:   "voiding a traceable wallet without inbound balance",
			wallet: traceable,
			in:     wallets.TransactionInput{VoidedCredits: dec("3.00")},
			want:   []string{"amount/exceeds_available_amount"},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:319
		{
			name: "validation error",
			in:   wallets.TransactionInput{PaidCredits: dec("-15")},
			want: []string{"paid_credits/invalid_paid_credits", "paid_credits/invalid_amount"},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:327
		{
			name:   "paid credits above the maximum",
			wallet: func(w *wallet.Wallet) { w.PaidTopUpMaxAmount = ptr[int64](1000) },
			in:     wallets.TransactionInput{PaidCredits: dec("20")},
			want:   []string{"paid_credits/amount_above_maximum"},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:354
		{
			name:   "paid credits below the minimum",
			wallet: func(w *wallet.Wallet) { w.PaidTopUpMinAmount = ptr[int64](1000) },
			in:     wallets.TransactionInput{PaidCredits: dec("5")},
			want:   []string{"paid_credits/amount_below_minimum"},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:380
		{
			name:   "granted credits round to zero",
			wallet: func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.01") },
			in:     wallets.TransactionInput{GrantedCredits: dec("0.4")},
			want:   []string{"granted_credits/amount_rounds_to_zero"},
		},
		// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:412
		{
			name: "invalid payment method type",
			in:   wallets.TransactionInput{PaidCredits: dec("1"), PaymentMethod: &wallets.PaymentMethod{Type: "invalid"}},
			want: []string{"payment_method/invalid_payment_method"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			var opts []func(*wallet.Wallet)
			if tc.wallet != nil {
				opts = append(opts, tc.wallet)
			}
			w := h.Wallet(h.Customer(), "10", opts...)
			in := tc.in
			in.WalletID = w.ID
			_, err := s.CreateTransactions(h.Ctx, h.OrgID(), in)
			wantCodes(t, err, tc.want...)
			if got := len(transactions(h, w.ID)); got != 0 {
				t.Errorf("transactions: got = %d, want = 0", got)
			}
		})
	}
}

func TestCreateTransactionRemainingAmount(t *testing.T) {
	cases := []struct {
		name      string
		traceable bool
		in        wallets.TransactionInput
		want      *int64
	}{
		// source: spec/services/wallet_transactions/create_service_spec.rb:118
		{name: "granted on a traceable wallet", traceable: true, in: wallets.TransactionInput{GrantedCredits: dec("10")}, want: ptr[int64](1000)},
		// source: spec/services/wallet_transactions/create_service_spec.rb:134
		{
			name: "pending purchase on a traceable wallet", traceable: true,
			in: wallets.TransactionInput{PaidCredits: dec("10"), InvoiceRequiresSuccessfulPayment: ptr(true)},
		},
		// source: spec/services/wallet_transactions/create_service_spec.rb:168
		{name: "granted on a wallet not traceable", in: wallets.TransactionInput{GrantedCredits: dec("10")}},
		// source: spec/services/wallet_transactions/create_service_spec.rb:151
		{name: "outbound", traceable: true, in: wallets.TransactionInput{VoidedCredits: dec("1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.Traceable = tc.traceable })
			if tc.traceable {
				addTransaction(h, w, remaining(1000))
			}
			in := tc.in
			in.WalletID = w.ID
			ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), in)
			if err != nil {
				t.Fatal(err)
			}
			got := ts[0].RemainingAmount
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Errorf("remaining amount: got = %v, want = %v", deref(got), deref(tc.want))
			}
		})
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestVoid(t *testing.T) {
	// source: spec/services/wallet_transactions/void_service_spec.rb:32
	t.Run("zero credits", func(t *testing.T) {
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10")
		ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{WalletID: w.ID, VoidedCredits: dec("0")})
		if err != nil || len(ts) != 0 {
			t.Errorf("got = (%d, %v), want = (0, nil)", len(ts), err)
		}
	})
	// source: spec/services/wallet_transactions/void_service_spec.rb:40
	t.Run("minimum arguments", func(t *testing.T) {
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10")
		ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{WalletID: w.ID, VoidedCredits: dec("10")})
		if err != nil {
			t.Fatal(err)
		}
		v := ts[0]
		if v.TransactionType != wallet.TransactionTypeOutbound || v.TransactionStatus != wallet.TransactionStatusVoided ||
			v.Status != wallet.SettlementStatusSettled || v.SettledAt == nil || v.Amount != 1000 || v.Priority != 50 {
			t.Errorf("got = %+v, want a settled voided outbound of 1000 at priority 50", v)
		}
		w = h.ReloadWallet(w.ID)
		if w.BalanceAmount != 0 || !w.CreditsBalance.IsZero() {
			t.Errorf("balance: got = (%d, %s), want = (0, 0)", w.BalanceAmount, w.CreditsBalance)
		}
	})
	// source: spec/services/wallet_transactions/void_service_spec.rb:74
	t.Run("all arguments", func(t *testing.T) {
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10")
		ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{
			WalletID: w.ID, VoidedCredits: dec("10"), Source: wallet.TransactionSourceThreshold, Priority: ptr(25),
			Name: ptr("Void"), Metadata: core.Metadata{{Key: "k", Value: "v"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		v := ts[0]
		if v.Source != wallet.TransactionSourceThreshold || v.Priority != 25 || v.Name == nil || *v.Name != "Void" || len(v.Metadata) != 1 {
			t.Errorf("got = %+v, want threshold, 25, Void, 1 metadata item", v)
		}
	})
}

func TestVoidTraceable(t *testing.T) {
	type inbound struct {
		status    wallet.TransactionStatus
		amount    int64
		remaining int64
		priority  int
		daysAgo   int
	}
	cases := []struct {
		name      string
		balance   string
		inbounds  []inbound
		void      string
		target    int // index of the inbound voided from, -1 for none
		remaining bool
		want      []int64 // remaining amount of each inbound
		consumed  []int64 // consumption amounts in order of the inbounds
		err       []string
	}{
		// source: spec/services/wallet_transactions/void_service_spec.rb:175
		{
			name: "single inbound", balance: "10", target: -1, void: "10",
			inbounds: []inbound{{wallet.TransactionStatusPurchased, 1000, 1000, 50, 0}},
			want:     []int64{0}, consumed: []int64{1000},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:192
		{
			name: "void remaining", balance: "10", target: 0, remaining: true,
			inbounds: []inbound{{wallet.TransactionStatusPurchased, 1000, 1000, 50, 0}},
			want:     []int64{0}, consumed: []int64{1000},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:277
		{
			name: "multiple inbounds", balance: "30", target: -1, void: "25",
			inbounds: []inbound{
				{wallet.TransactionStatusPurchased, 1000, 1000, 50, 3},
				{wallet.TransactionStatusPurchased, 1000, 1000, 50, 2},
				{wallet.TransactionStatusPurchased, 1000, 1000, 50, 1},
			},
			want: []int64{0, 0, 500}, consumed: []int64{1000, 1000, 500},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:331
		{
			name: "partially consumed inbound", balance: "30", target: -1, void: "5",
			inbounds: []inbound{{wallet.TransactionStatusPurchased, 2000, 800, 50, 0}},
			want:     []int64{300}, consumed: []int64{500},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:386
		{
			name: "fully consumed inbound", balance: "30", target: -1, void: "5",
			inbounds: []inbound{
				{wallet.TransactionStatusPurchased, 1000, 0, 50, 2},
				{wallet.TransactionStatusPurchased, 1000, 1000, 50, 1},
			},
			want: []int64{0, 500}, consumed: []int64{0, 500},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:444
		{
			name: "granted before purchased at the same priority", balance: "60", target: -1, void: "15",
			inbounds: []inbound{
				{wallet.TransactionStatusPurchased, 1000, 1000, 50, 2},
				{wallet.TransactionStatusGranted, 1000, 1000, 50, 1},
			},
			want: []int64{500, 0}, consumed: []int64{500, 1000},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:489
		{
			name: "lower priority number first", balance: "60", target: -1, void: "15",
			inbounds: []inbound{
				{wallet.TransactionStatusGranted, 1000, 1000, 10, 2},
				{wallet.TransactionStatusPurchased, 1000, 1000, 1, 1},
			},
			want: []int64{500, 0}, consumed: []int64{500, 1000},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:548
		{
			name: "older first within the same priority and status", balance: "60", target: -1, void: "15",
			inbounds: []inbound{
				{wallet.TransactionStatusGranted, 1000, 1000, 50, 1},
				{wallet.TransactionStatusGranted, 1000, 1000, 50, 3},
			},
			want: []int64{500, 0}, consumed: []int64{500, 1000},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:629
		{
			name: "complex priority scenario", balance: "100", target: -1, void: "80",
			inbounds: []inbound{
				{wallet.TransactionStatusGranted, 2000, 2000, 1, 4},
				{wallet.TransactionStatusGranted, 2500, 2500, 2, 3},
				{wallet.TransactionStatusGranted, 2500, 2500, 2, 1},
				{wallet.TransactionStatusPurchased, 3000, 3000, 2, 2},
			},
			want: []int64{0, 0, 0, 2000}, consumed: []int64{2000, 2500, 2500, 1000},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:691
		{
			name: "void exceeds the available balance", balance: "30", target: -1, void: "15",
			inbounds: []inbound{{wallet.TransactionStatusPurchased, 1000, 1000, 50, 0}},
			want:     []int64{1000}, err: []string{"amount/exceeds_available_amount"},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:786
		{
			name: "specific inbound", balance: "30", target: 0, void: "10",
			inbounds: []inbound{
				{wallet.TransactionStatusPurchased, 2000, 2000, 50, 0},
				{wallet.TransactionStatusGranted, 1000, 1000, 1, 0},
			},
			want: []int64{1000, 1000}, consumed: []int64{1000, 0},
		},
		// source: spec/services/wallet_transactions/void_service_spec.rb:797
		{
			name: "void exceeds the specific inbound", balance: "30", target: 0, void: "25",
			inbounds: []inbound{
				{wallet.TransactionStatusPurchased, 2000, 2000, 50, 0},
				{wallet.TransactionStatusGranted, 1000, 1000, 1, 0},
			},
			want: []int64{2000, 1000}, err: []string{"amount/exceeds_remaining_transaction_amount"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), tc.balance, traceable)
			ins := make([]*wallet.Transaction, len(tc.inbounds))
			for i, in := range tc.inbounds {
				ins[i] = addTransaction(h, w, func(t *wallet.Transaction) {
					t.TransactionStatus, t.Amount, t.Priority = in.status, in.amount, in.priority
					t.CreditAmount = wallet.CreditsFromMinor(in.amount, w.Currency, w.RateAmount)
					t.RemainingAmount = &in.remaining
					t.CreatedAt = t0.Add(-time.Duration(in.daysAgo) * 24 * time.Hour)
				})
			}
			in := wallets.TransactionInput{WalletID: w.ID}
			if tc.void != "" {
				in.VoidedCredits = dec(tc.void)
			}
			if tc.target >= 0 {
				in.VoidedTransactionID = ins[tc.target].ID
			}
			ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), in)
			wantCodes(t, err, tc.err...)
			for i, in := range ins {
				if got := *transaction(h, in.ID).RemainingAmount; got != tc.want[i] {
					t.Errorf("inbound %d remaining: got = %d, want = %d", i, got, tc.want[i])
				}
			}
			if err != nil {
				if got := len(transactions(h, w.ID)); got != len(ins) {
					t.Errorf("transactions: got = %d, want = %d", got, len(ins))
				}
				return
			}
			byInbound := map[core.WalletTransactionID]int64{}
			for _, c := range consumptions(h, ts[0].ID) {
				byInbound[c.InboundTransactionID] += c.ConsumedAmount
			}
			for i, in := range ins {
				if got := byInbound[in.ID]; got != tc.consumed[i] {
					t.Errorf("inbound %d consumed: got = %d, want = %d", i, got, tc.consumed[i])
				}
			}
		})
	}
}

func TestVoidNotTraceable(t *testing.T) {
	// source: spec/services/wallet_transactions/void_service_spec.rb:710
	h, s := setup(t, t0)
	w := h.Wallet(h.Customer(), "10")
	addTransaction(h, w)
	ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{WalletID: w.ID, VoidedCredits: dec("5")})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(consumptions(h, ts[0].ID)); got != 0 {
		t.Errorf("consumptions: got = %d, want = 0", got)
	}
}

func TestSettle(t *testing.T) {
	cases := []struct {
		name      string
		traceable bool
		outbound  bool
		want      *int64
	}{
		// source: spec/services/wallet_transactions/settle_service_spec.rb:30
		{name: "inbound on a traceable wallet", traceable: true, want: ptr[int64](1000)},
		// source: spec/services/wallet_transactions/settle_service_spec.rb:49
		{name: "outbound", traceable: true, outbound: true},
		// source: spec/services/wallet_transactions/settle_service_spec.rb:65
		{name: "inbound on a wallet not traceable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.Traceable = tc.traceable })
			tr := addTransaction(h, w, func(t *wallet.Transaction) {
				t.Status, t.SettledAt = wallet.SettlementStatusPending, nil
				if tc.outbound {
					t.TransactionType, t.TransactionStatus = wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced
				}
			})
			h.Clock.Advance(time.Hour)
			got, err := s.Settle(h.Ctx, h.OrgID(), tr.ID)
			if err != nil {
				t.Fatal(err)
			}
			// source: spec/services/wallet_transactions/settle_service_spec.rb:11
			if got.Status != wallet.SettlementStatusSettled || got.SettledAt == nil || !got.SettledAt.Equal(h.Now()) {
				t.Errorf("status: got = (%s, %v), want = (settled, %v)", got.Status, got.SettledAt, h.Now())
			}
			if r := got.RemainingAmount; (r == nil) != (tc.want == nil) || r != nil && *r != *tc.want {
				t.Errorf("remaining amount: got = %v, want = %v", deref(r), deref(tc.want))
			}
			// source: spec/services/wallet_transactions/settle_service_spec.rb:18
			if n := countWebhooks(h, webhook.EventTypeWalletTransactionUpdated); n != 1 {
				t.Errorf("wallet_transaction.updated: got = %d, want = 1", n)
			}
		})
	}
}

func TestMarkAsFailed(t *testing.T) {
	cases := []struct {
		name    string
		status  wallet.SettlementStatus
		want    wallet.SettlementStatus
		webhook int
	}{
		// source: spec/services/wallet_transactions/mark_as_failed_service_spec.rb:20
		{name: "already failed", status: wallet.SettlementStatusFailed, want: wallet.SettlementStatusFailed},
		// source: spec/services/wallet_transactions/mark_as_failed_service_spec.rb:38
		{name: "pending", status: wallet.SettlementStatusPending, want: wallet.SettlementStatusFailed, webhook: 1},
		// source: spec/services/wallet_transactions/mark_as_failed_service_spec.rb:51
		{name: "settled", status: wallet.SettlementStatusSettled, want: wallet.SettlementStatusSettled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10")
			tr := addTransaction(h, w, func(t *wallet.Transaction) {
				t.Status = tc.status
				switch tc.status {
				case wallet.SettlementStatusFailed:
					t.SettledAt, t.FailedAt = nil, ptr(t0)
				case wallet.SettlementStatusPending:
					t.SettledAt = nil
				default:
					t.SettledAt = ptr(t0)
				}
			})
			if _, err := s.MarkAsFailed(h.Ctx, h.OrgID(), tr.ID); err != nil {
				t.Fatal(err)
			}
			got := transaction(h, tr.ID)
			if got.Status != tc.want {
				t.Errorf("status: got = %s, want = %s", got.Status, tc.want)
			}
			if tc.want == wallet.SettlementStatusFailed && got.FailedAt == nil {
				t.Errorf("failed at: got = nil, want = set")
			}
			if n := countWebhooks(h, webhook.EventTypeWalletTransactionUpdated); n != tc.webhook {
				t.Errorf("wallet_transaction.updated: got = %d, want = %d", n, tc.webhook)
			}
		})
	}
}

func TestRecredit(t *testing.T) {
	// source: spec/services/wallet_transactions/recredit_service_spec.rb:10
	t.Run("terminated wallet", func(t *testing.T) {
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10", terminated(t0))
		tr := addTransaction(h, w, func(t *wallet.Transaction) {
			t.TransactionType, t.TransactionStatus = wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced
		})
		_, err := s.Recredit(h.Ctx, h.OrgID(), tr.ID)
		wantCodes(t, err, "wallet_not_active")
	})
	// source: spec/services/wallet_transactions/recredit_service_spec.rb:25
	t.Run("active wallet", func(t *testing.T) {
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) {
			w.ConsumedCredits, w.ConsumedAmount = money.MustParse("10"), 1000
		})
		tr := addTransaction(h, w, func(t *wallet.Transaction) {
			t.TransactionType, t.TransactionStatus = wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced
			t.PurchaseOrderNumber = ptr("PO-123")
		})
		got, err := s.Recredit(h.Ctx, h.OrgID(), tr.ID)
		if err != nil {
			t.Fatal(err)
		}
		// source: spec/services/wallet_transactions/recredit_service_spec.rb:55
		if got.PurchaseOrderNumber == nil || *got.PurchaseOrderNumber != "PO-123" {
			t.Errorf("purchase_order_number: got = %v, want = PO-123", got.PurchaseOrderNumber)
		}
		if got.TransactionStatus != wallet.TransactionStatusGranted || !eq(got.CreditAmount, money.MustParse("10")) {
			t.Errorf("transaction: got = (%s, %s), want = (granted, 10)", got.TransactionStatus, got.CreditAmount)
		}
		w = h.ReloadWallet(w.ID)
		wantDec(t, "credits balance", w.CreditsBalance, "20")
		// source: spec/services/wallet_transactions/recredit_service_spec.rb:31
		wantDec(t, "consumed credits", w.ConsumedCredits, "0")
		if w.ConsumedAmount != 0 {
			t.Errorf("consumed amount: got = %d, want = 0", w.ConsumedAmount)
		}
	})
	// source: spec/services/wallet_transactions/recredit_service_spec.rb:37
	t.Run("rounds to zero", func(t *testing.T) {
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.01") })
		tr := addTransaction(h, w, func(t *wallet.Transaction) {
			t.TransactionType, t.TransactionStatus = wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced
			t.Amount, t.CreditAmount = 0, money.MustParse("0.4")
		})
		got, err := s.Recredit(h.Ctx, h.OrgID(), tr.ID)
		if err != nil || got != nil {
			t.Errorf("got = (%v, %v), want = (nil, nil)", got, err)
		}
		if n := len(transactions(h, w.ID)); n != 1 {
			t.Errorf("transactions: got = %d, want = 1", n)
		}
	})
}

func TestApplyPaidCredits(t *testing.T) {
	// source: spec/services/wallets/apply_paid_credits_service_spec.rb:1
	h, s := setup(t, t0)
	w := h.Wallet(h.Customer(), "10")
	tr := addTransaction(h, w, func(t *wallet.Transaction) {
		t.Status, t.SettledAt, t.Amount, t.CreditAmount = wallet.SettlementStatusPending, nil, 1500, money.MustParse("15")
	})
	got, err := s.ApplyPaidCredits(h.Ctx, h.OrgID(), tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != wallet.SettlementStatusSettled {
		t.Errorf("status: got = %s, want = settled", got.Status)
	}
	w = h.ReloadWallet(w.ID)
	if w.BalanceAmount != 2500 {
		t.Errorf("balance: got = %d, want = 2500", w.BalanceAmount)
	}
	wantDec(t, "credits balance", w.CreditsBalance, "25")
	// A settled purchase is left as is.
	if _, err := s.ApplyPaidCredits(h.Ctx, h.OrgID(), tr.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.ReloadWallet(w.ID).BalanceAmount; got != 2500 {
		t.Errorf("balance after a second call: got = %d, want = 2500", got)
	}
}
