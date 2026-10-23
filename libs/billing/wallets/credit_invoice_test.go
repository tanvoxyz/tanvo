package wallets_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func creditInvoice(h *billingtest.Harness, id core.InvoiceID) (*invoice.Invoice, []*invoice.Fee) {
	inv := billingtest.Read(h, func(ctx context.Context, r port.Repos) (*invoice.Invoice, error) {
		return r.Invoices.Get(ctx, h.OrgID(), id)
	})
	fees := billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*invoice.Fee, error) {
		return r.Fees.ListByInvoice(ctx, h.OrgID(), id)
	})
	return inv, fees
}

// purchase buys 10 credits on a new wallet holding 10.
func purchase(t *testing.T, h *billingtest.Harness, s *wallets.Service, irsp bool, opts ...func(*customer.Customer)) (*wallet.Wallet, *wallet.Transaction) {
	t.Helper()
	w := h.Wallet(h.Customer(opts...), "10")
	ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{
		WalletID: w.ID, PaidCredits: dec("10"), InvoiceRequiresSuccessfulPayment: &irsp,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w, ts[0]
}

func TestBillPaidCredit(t *testing.T) {
	// source: spec/services/invoices/paid_credit_service_spec.rb:32
	h, s := setup(t, t0)
	w, tr := purchase(t, h, s, false)
	// source: spec/services/invoices/paid_credit_service_spec.rb:55
	if tr.InvoiceID.IsZero() {
		t.Fatal("invoice: got = none, want = the credit invoice")
	}
	inv, fees := creditInvoice(h, tr.InvoiceID)
	if inv.Type != invoice.TypeCredit || inv.Status != invoice.StatusFinalized || inv.TotalAmount != 1000 || inv.Currency != w.Currency {
		t.Errorf("invoice: got = (%s, %s, %d, %s), want = (credit, finalized, 1000, USD)", inv.Type, inv.Status, inv.TotalAmount, inv.Currency)
	}
	if len(fees) != 1 || fees[0].Type != core.FeeTypeCredit || fees[0].WalletTransactionID != tr.ID || fees[0].Amount != 1000 {
		t.Errorf("fees: got = %+v, want one credit fee of 1000 for the purchase", fees)
	}
	// The finalized invoice grants no credits: the purchase settles when
	// the payment succeeds (lago Invoices::PrepaidCreditJob).
	if got := transaction(h, tr.ID); got.Status != wallet.SettlementStatusPending {
		t.Errorf("purchase: got = %s, want = pending", got.Status)
	}
	if got := h.ReloadWallet(w.ID).BalanceAmount; got != 1000 {
		t.Errorf("balance: got = %d, want = 1000", got)
	}
	// source: spec/services/fees/paid_credit_service_spec.rb:44
	if !fees[0].PreciseUnitAmount.Equal(money.One) || fees[0].UnitAmount != 100 {
		t.Errorf("unit amount: got = %s/%d, want = 1/100", fees[0].PreciseUnitAmount, fees[0].UnitAmount)
	}
	// source: spec/services/invoices/paid_credit_service_spec.rb:134
	if n := countWebhooks(h, webhook.EventTypeInvoicePaidCreditAdded); n != 1 {
		t.Errorf("invoice.paid_credit_added: got = %d, want = 1", n)
	}
	// source: spec/services/invoices/paid_credit_service_spec.rb:239
	if !slices.Contains(h.JobKinds(), kernel.JobCreateInvoicePayment) {
		t.Errorf("jobs: got = %v, want %s", h.JobKinds(), kernel.JobCreateInvoicePayment)
	}
}

func TestBillPaidCreditTimezone(t *testing.T) {
	// source: spec/services/invoices/paid_credit_service_spec.rb:249
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	h, s := setup(t, billingtest.Date(2024, time.March, 5, 20))
	_, tr := purchase(t, h, s, false, func(c *customer.Customer) { c.Timezone = tokyo })
	inv, _ := creditInvoice(h, tr.InvoiceID)
	if want := core.NewDate(2024, time.March, 6); inv.IssuingDate != want {
		t.Errorf("issuing date: got = %v, want = %v", inv.IssuingDate, want)
	}
}

func TestBillPaidCreditRequiringPayment(t *testing.T) {
	// source: spec/services/invoices/paid_credit_service_spec.rb:322
	h, s := setup(t, t0)
	w, tr := purchase(t, h, s, true)
	inv, _ := creditInvoice(h, tr.InvoiceID)
	if inv.Status != invoice.StatusOpen {
		t.Errorf("invoice: got = %s, want = open", inv.Status)
	}
	if tr.Status != wallet.SettlementStatusPending {
		t.Errorf("purchase: got = %s, want = pending", tr.Status)
	}
	if got := h.ReloadWallet(w.ID).BalanceAmount; got != 1000 {
		t.Errorf("balance: got = %d, want = 1000", got)
	}
	if n := countWebhooks(h, webhook.EventTypeInvoicePaidCreditAdded); n != 0 {
		t.Errorf("invoice.paid_credit_added: got = %d, want = 0", n)
	}
}

func TestHandleCreditInvoicePayment(t *testing.T) {
	cases := []struct {
		name        string
		succeeded   bool
		wantStatus  wallet.SettlementStatus
		wantInvoice invoice.Status
		wantBalance int64
		webhook     int
	}{
		// source: spec/jobs/invoices/prepaid_credit_job_spec.rb:30
		{name: "payment succeeded", succeeded: true, wantStatus: wallet.SettlementStatusSettled, wantInvoice: invoice.StatusFinalized, wantBalance: 2000, webhook: 1},
		// source: spec/jobs/invoices/prepaid_credit_job_spec.rb:67
		{name: "payment failed", wantStatus: wallet.SettlementStatusFailed, wantInvoice: invoice.StatusOpen, wantBalance: 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w, tr := purchase(t, h, s, true)
			h.Clock.Advance(48 * time.Hour)
			if err := s.HandleCreditInvoicePayment(h.Ctx, h.OrgID(), tr.InvoiceID, tc.succeeded); err != nil {
				t.Fatal(err)
			}
			if got := transaction(h, tr.ID).Status; got != tc.wantStatus {
				t.Errorf("purchase: got = %s, want = %s", got, tc.wantStatus)
			}
			inv, _ := creditInvoice(h, tr.InvoiceID)
			if inv.Status != tc.wantInvoice {
				t.Errorf("invoice: got = %s, want = %s", inv.Status, tc.wantInvoice)
			}
			// source: spec/services/invoices/finalize_open_credit_service_spec.rb:18
			if tc.succeeded && inv.IssuingDate != core.NewDate(2024, time.March, 7) {
				t.Errorf("issuing date: got = %v, want = 2024-03-07", inv.IssuingDate)
			}
			if got := h.ReloadWallet(w.ID).BalanceAmount; got != tc.wantBalance {
				t.Errorf("balance: got = %d, want = %d", got, tc.wantBalance)
			}
			if n := countWebhooks(h, webhook.EventTypeInvoicePaidCreditAdded); n != tc.webhook {
				t.Errorf("invoice.paid_credit_added: got = %d, want = %d", n, tc.webhook)
			}
		})
	}
	// source: spec/services/invoices/finalize_open_credit_service_spec.rb:36
	t.Run("already finalized", func(t *testing.T) {
		h, s := setup(t, t0)
		w, tr := purchase(t, h, s, false)
		if err := s.HandleCreditInvoicePayment(h.Ctx, h.OrgID(), tr.InvoiceID, true); err != nil {
			t.Fatal(err)
		}
		if got := h.ReloadWallet(w.ID).BalanceAmount; got != 2000 {
			t.Errorf("balance: got = %d, want = 2000", got)
		}
		if n := countWebhooks(h, webhook.EventTypeInvoicePaidCreditAdded); n != 1 {
			t.Errorf("invoice.paid_credit_added: got = %d, want = 1", n)
		}
	})
}

func TestHandleCreditInvoiceVoided(t *testing.T) {
	h, s := setup(t, t0)
	_, tr := purchase(t, h, s, true)
	if err := s.HandleCreditInvoiceVoided(h.Ctx, h.OrgID(), tr.InvoiceID); err != nil {
		t.Fatal(err)
	}
	if got := transaction(h, tr.ID).Status; got != wallet.SettlementStatusFailed {
		t.Errorf("purchase: got = %s, want = failed", got)
	}
}

// TestBillPaidCreditTruncatesUnitAmount checks that the unit amount of a
// credit fee drops the fraction of a cent of the wallet's rate, as lago's
// bigint unit_amount_cents does, and that the precise unit amount is that
// unit amount in currency units.
//
// source: app/models/wallet_transaction.rb:99 (unit_amount_cents),
// app/services/fees/paid_credit_service.rb:40 (precise_unit_amount)
func TestBillPaidCreditTruncatesUnitAmount(t *testing.T) {
	h, s := setup(t, t0)
	w := h.Wallet(h.Customer(), "0", func(w *wallet.Wallet) { w.RateAmount = money.MustParse("0.12999") })
	irsp := false
	ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{
		WalletID: w.ID, PaidCredits: dec("10"), InvoiceRequiresSuccessfulPayment: &irsp,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, fees := creditInvoice(h, ts[0].InvoiceID)
	if len(fees) != 1 {
		t.Fatalf("fees: got = %d, want = 1", len(fees))
	}
	if fees[0].UnitAmount != 12 || !fees[0].PreciseUnitAmount.Equal(money.MustParse("0.12")) {
		t.Errorf("unit amount: got = %d/%s, want = 12/0.12", fees[0].UnitAmount, fees[0].PreciseUnitAmount)
	}
}

func TestBillPaidCreditPurchaseOrderNumber(t *testing.T) {
	cases := []struct {
		name, source string
		walletPO     string
		txPO         string
		want         string
	}{
		{"transaction", "spec/services/invoices/paid_credit_service_spec.rb:60", "", "PO-123", "PO-123"},
		{"wallet", "spec/services/invoices/paid_credit_service_spec.rb:80", "PO-WALLET-123", "", "PO-WALLET-123"},
		{"transaction over wallet", "spec/services/invoices/paid_credit_service_spec.rb:90", "PO-WALLET-123", "PO-TRANSACTION-123", "PO-TRANSACTION-123"},
		{"neither", "tanvo", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s", c.source)
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) {
				if c.walletPO != "" {
					w.PurchaseOrderNumber = &c.walletPO
				}
			})
			in := wallets.TransactionInput{WalletID: w.ID, PaidCredits: dec("10")}
			if c.txPO != "" {
				in.PurchaseOrderNumber = &c.txPO
			}
			ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), in)
			if err != nil {
				t.Fatal(err)
			}
			inv, _ := creditInvoice(h, ts[0].InvoiceID)
			got := ""
			if inv.PurchaseOrderNumber != nil {
				got = *inv.PurchaseOrderNumber
			}
			if got != c.want {
				t.Errorf("invoice purchase_order_number: got = %q, want = %q", got, c.want)
			}
		})
	}
}
