package wallets_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func TestHandleCreditInvoicePaidByCreditNote(t *testing.T) {
	// source: app/jobs/invoices/prepaid_credit_job.rb (should_grant_prepaid_credits?, paid_by_credit_note?)
	h, s := setup(t, t0)
	w, tr := purchase(t, h, s, true)
	h.Do(func(ctx context.Context, r port.Repos) error {
		return r.InvoiceSettlements.Create(ctx, &invoice.Settlement{
			ID: core.NewID[core.InvoiceSettlementID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: tr.InvoiceID,
			Type: invoice.SettlementTypeCreditNote, Amount: 1000, Currency: w.Currency, CreatedAt: h.Now(),
		})
	})
	if err := s.HandleCreditInvoicePayment(h.Ctx, h.OrgID(), tr.InvoiceID, true); err != nil {
		t.Fatal(err)
	}
	if got := transaction(h, tr.ID).Status; got != wallet.SettlementStatusFailed {
		t.Errorf("purchase: got = %s, want = failed", got)
	}
	if got := h.ReloadWallet(w.ID).BalanceAmount; got != 1000 {
		t.Errorf("balance: got = %d, want = 1000", got)
	}
}

func TestHandleCreditInvoicePaidAfterFailure(t *testing.T) {
	// source: app/services/wallet_transactions/settle_service.rb (no status guard)
	// source: app/services/wallets/apply_paid_credits_service.rb:14
	h, s := setup(t, t0)
	w, tr := purchase(t, h, s, true)
	if err := s.HandleCreditInvoicePayment(h.Ctx, h.OrgID(), tr.InvoiceID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.HandleCreditInvoicePayment(h.Ctx, h.OrgID(), tr.InvoiceID, true); err != nil {
		t.Fatal(err)
	}
	if got := transaction(h, tr.ID).Status; got != wallet.SettlementStatusSettled {
		t.Errorf("purchase: got = %s, want = settled", got)
	}
	if got := h.ReloadWallet(w.ID).BalanceAmount; got != 2000 {
		t.Errorf("balance: got = %d, want = 2000", got)
	}
}

func TestApplyPaidCreditsTwice(t *testing.T) {
	// source: app/services/wallets/apply_paid_credits_service.rb:14
	h, s := setup(t, t0)
	w, tr := purchase(t, h, s, true)
	for range 2 {
		if _, err := s.ApplyPaidCredits(h.Ctx, h.OrgID(), tr.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.ReloadWallet(w.ID).BalanceAmount; got != 2000 {
		t.Errorf("balance: got = %d, want = 2000", got)
	}
}

func TestRunIntervalTopUpsLate(t *testing.T) {
	// source: app/services/wallets/create_interval_wallet_transactions_service.rb:254
	// A clock job for the due date that runs after midnight, or is retried,
	// must not top up twice.
	due := billingtest.Date(2022, time.June, 18, 0)
	h, s := setup(t, due.Add(26*time.Hour))
	w := h.Wallet(h.Customer(), "0", func(w *wallet.Wallet) { w.CreatedAt = billingtest.Date(2021, time.February, 20, 0) })
	addRule(h, w, func(r *wallet.RecurringTransactionRule) { r.Interval = period.Weekly })
	for i, want := range []int{2, 0} {
		ts, err := s.RunIntervalTopUps(h.Ctx, h.OrgID(), due)
		if err != nil {
			t.Fatal(err)
		}
		if len(ts) != want {
			t.Errorf("run %d: got = %d transactions, want = %d", i+1, len(ts), want)
		}
	}
}

// afterFirstTx runs then once the first transaction committed, as a
// concurrent writer would between two transactions.
type afterFirstTx struct {
	inner port.Transactor
	then  func()
	done  bool
}

func (a *afterFirstTx) InTx(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	err := a.inner.InTx(ctx, fn)
	if !a.done {
		a.done = true
		a.then()
	}
	return err
}

// An interval top-up follows its rule as read under the wallet lock, not
// as listed before it: a rule changed or terminated in between tops up
// as changed, or not at all.
func TestRunIntervalTopUpsReloadsRule(t *testing.T) {
	due := billingtest.Date(2022, time.June, 18, 0)
	tests := []struct {
		name   string
		change func(*wallet.RecurringTransactionRule)
		want   int
	}{
		{"unchanged", func(*wallet.RecurringTransactionRule) {}, 2},
		{"granted credits removed", func(r *wallet.RecurringTransactionRule) { r.GrantedCredits = money.Zero }, 1},
		{"terminated", func(r *wallet.RecurringTransactionRule) {
			if err := r.Terminate(due); err != nil {
				t.Fatal(err)
			}
		}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := setup(t, due.Add(time.Hour))
			w := h.Wallet(h.Customer(), "0", func(w *wallet.Wallet) { w.CreatedAt = billingtest.Date(2021, time.February, 20, 0) })
			rule := addRule(h, w, func(r *wallet.RecurringTransactionRule) { r.Interval = period.Weekly })
			d := *h.Deps
			d.Tx = &afterFirstTx{inner: h.Deps.Tx, then: func() {
				h.Do(func(ctx context.Context, r port.Repos) error {
					got, err := r.RecurringTransactionRules.Get(ctx, h.OrgID(), rule.ID)
					if err != nil {
						return err
					}
					tt.change(got)
					return r.RecurringTransactionRules.Update(ctx, got)
				})
			}}
			ts, err := wallets.New(&d).RunIntervalTopUps(h.Ctx, h.OrgID(), due)
			if err != nil {
				t.Fatal(err)
			}
			if len(ts) != tt.want {
				t.Errorf("transactions = %d, want = %d", len(ts), tt.want)
			}
		})
	}
}
