package wallets_test

import (
	"context"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

func TestVoidCreditNoteCredits(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		status     wallet.SettlementStatus
		refund     int64
		offset     int64
		wantStatus wallet.SettlementStatus
		wantCents  int64
	}{
		{
			name: "an offset fails the pending purchase", source: "app/jobs/invoices/prepaid_credit_job.rb:24",
			status: wallet.SettlementStatusPending, offset: 10000,
			wantStatus: wallet.SettlementStatusFailed, wantCents: 10000,
		},
		{
			name: "an offset leaves a settled purchase", source: "app/services/wallet_transactions/mark_as_failed_service.rb:21",
			status: wallet.SettlementStatusSettled, offset: 10000,
			wantStatus: wallet.SettlementStatusSettled, wantCents: 10000,
		},
		{
			name: "a refund voids its credits", source: "app/services/credit_notes/create_service.rb:283",
			status: wallet.SettlementStatusSettled, refund: 4000,
			wantStatus: wallet.SettlementStatusSettled, wantCents: 6000,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, s := setup(t, t0)
			c := h.Customer()
			w := h.Wallet(c, "100")
			purchase := h.InboundTransaction(w, wallet.TransactionStatusPurchased, "100", func(tr *wallet.Transaction) {
				tr.Status = tt.status
				if tt.status == wallet.SettlementStatusPending {
					tr.SettledAt = nil
				}
			})
			cn := &creditnote.CreditNote{
				ID: core.NewID[core.CreditNoteID](h.IDs), OrganizationID: h.OrgID(),
				RefundAmount: tt.refund, OffsetAmount: tt.offset, Currency: w.Currency,
			}
			h.Do(func(ctx context.Context, r port.Repos) error {
				return s.VoidCreditNoteCredits(ctx, r, w, purchase, cn)
			})
			if got := transaction(h, purchase.ID).Status; got != tt.wantStatus {
				t.Errorf("purchase status = %s, want = %s", got, tt.wantStatus)
			}
			got := billingtest.Read(h, func(ctx context.Context, r port.Repos) (*wallet.Wallet, error) {
				return r.Wallets.Get(ctx, h.OrgID(), w.ID)
			}).BalanceAmount
			if got != tt.wantCents {
				t.Errorf("balance = %d, want = %d", got, tt.wantCents)
			}
		})
	}
}
