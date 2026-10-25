package wallets_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// billingEntity stores a second billing entity with code.
func billingEntity(h *billingtest.Harness, code string) *organization.BillingEntity {
	h.T.Helper()
	be := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), code, code, h.Now())
	be.DocumentNumberPrefix = "BE" + code
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
	return be
}

func TestTransactionBillingEntity(t *testing.T) {
	tests := []struct {
		name, source string
		wallet       bool
		want         func(h *billingtest.Harness, walletEntity core.BillingEntityID) core.BillingEntityID
	}{
		{
			name: "the wallet's", source: "spec/services/wallet_transactions/create_service_spec.rb:201", wallet: true,
			want: func(_ *billingtest.Harness, we core.BillingEntityID) core.BillingEntityID { return we },
		},
		{
			name: "the customer's without one on the wallet", source: "spec/services/wallet_transactions/create_service_spec.rb:210",
			want: func(h *billingtest.Harness, _ core.BillingEntityID) core.BillingEntityID { return h.BillingEntity.ID },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, s := setup(t, t0)
			be := billingEntity(h, "be2")
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) {
				if tt.wallet {
					w.BillingEntityID = be.ID
				}
			})
			ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{WalletID: w.ID, GrantedCredits: dec("1")})
			if err != nil {
				t.Fatal(err)
			}
			if got, want := transaction(h, ts[0].ID).BillingEntityID, tt.want(h, be.ID); got != want {
				t.Errorf("billing entity: got = %v, want = %v", got, want)
			}
		})
	}
}

func TestVoidBillingEntity(t *testing.T) {
	// source: spec/services/wallet_transactions/void_service_spec.rb:141
	h, s := setup(t, t0)
	original, current := billingEntity(h, "original"), billingEntity(h, "current")
	w := h.Wallet(h.Customer(), "10", traceable, func(w *wallet.Wallet) { w.BillingEntityID = current.ID })
	in := h.InboundTransaction(w, wallet.TransactionStatusGranted, "10", func(t *wallet.Transaction) { t.BillingEntityID = original.ID })
	ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{WalletID: w.ID, VoidedTransactionID: in.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := transaction(h, ts[0].ID).BillingEntityID; got != original.ID {
		t.Errorf("billing entity: got = %v, want = the inbound transaction's %v", got, original.ID)
	}
}

func TestBillPaidCreditBillingEntity(t *testing.T) {
	tests := []struct {
		name, source string
		wallet       bool
		snapshot     bool
		want         string
	}{
		{name: "the customer's", source: "spec/services/invoices/paid_credit_service_spec.rb:112", want: "org"},
		{name: "the wallet's without a snapshot", source: "spec/services/invoices/paid_credit_service_spec.rb:126", wallet: true, want: "wallet"},
		{name: "the snapshot over the wallet's current one", source: "spec/services/invoices/paid_credit_service_spec.rb:354", wallet: true, snapshot: true, want: "snapshot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, s := setup(t, t0)
			entities := map[string]core.BillingEntityID{
				"org": h.BillingEntity.ID, "wallet": billingEntity(h, "wallet").ID, "snapshot": billingEntity(h, "snapshot").ID,
			}
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) {
				if tt.wallet {
					w.BillingEntityID = entities["wallet"]
				}
			})
			tr := addTransaction(h, w, func(t *wallet.Transaction) {
				t.Status, t.Amount, t.CreditAmount = wallet.SettlementStatusPending, 1500, money.MustParse("15")
				if tt.snapshot {
					t.BillingEntityID = entities["snapshot"]
				}
			})
			if err := s.BillPaidCredit(h.Ctx, w, tr); err != nil {
				t.Fatal(err)
			}
			inv, _ := creditInvoice(h, transaction(h, tr.ID).InvoiceID)
			if inv.BillingEntityID != entities[tt.want] {
				t.Errorf("invoice billing entity: got = %v, want = %s %v", inv.BillingEntityID, tt.want, entities[tt.want])
			}
		})
	}
}
