package deduct_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// restore runs Restore on inv in a transaction.
func (f *fixture) restore(inv *invoice.Invoice, opts deduct.RestoreOptions) (deduct.Restored, error) {
	f.h.T.Helper()
	var out deduct.Restored
	err := f.h.Deps.Tx.InTx(f.h.Ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = f.s.Restore(ctx, r, inv, opts)
		return err
	})
	return out, err
}

func TestRestoreCoupon(t *testing.T) {
	tests := []struct {
		name       string
		coupon     func(*coupon.Coupon)
		applied    func(*coupon.AppliedCoupon)
		wantStatus coupon.Status
		check      func(t *testing.T, a *coupon.AppliedCoupon)
	}{
		// source: spec/services/applied_coupons/recredit_service_spec.rb:38
		{
			name: "terminated is reactivated", wantStatus: coupon.StatusActive,
			check: func(t *testing.T, a *coupon.AppliedCoupon) {
				eq(t, "terminated at", a.TerminatedAt == nil, true)
				eq(t, "remaining", *a.RemainingAmount, 100)
			},
		},
		// source: spec/services/applied_coupons/recredit_service_spec.rb:57
		{
			name: "forever stays terminated", wantStatus: coupon.StatusTerminated,
			applied: func(a *coupon.AppliedCoupon) { a.Frequency, a.RemainingAmount = coupon.FrequencyForever, nil },
		},
		// source: spec/services/applied_coupons/recredit_service_spec.rb:72
		{
			name: "terminated coupon stays terminated", wantStatus: coupon.StatusTerminated,
			coupon: func(c *coupon.Coupon) {
				now := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
				c.Status, c.TerminatedAt = coupon.StatusTerminated, &now
			},
		},
		// source: spec/services/applied_coupons/recredit_service_spec.rb:92
		{
			name: "recurring duration", wantStatus: coupon.StatusActive,
			applied: func(a *coupon.AppliedCoupon) {
				a.Status, a.TerminatedAt, a.RemainingAmount = coupon.StatusActive, nil, nil
				a.Frequency, a.FrequencyDuration, a.FrequencyDurationRemaining = coupon.FrequencyRecurring, ptr(3), ptr(2)
			},
			check: func(t *testing.T, a *coupon.AppliedCoupon) {
				eq(t, "remaining duration", *a.FrequencyDurationRemaining, 3)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			c := h.FixedAmountCoupon(100, func(c *coupon.Coupon) {
				if tt.coupon != nil {
					tt.coupon(c)
				}
			})
			a := h.ApplyCoupon(c, f.cust, func(a *coupon.AppliedCoupon) {
				now := h.Now()
				a.Status, a.TerminatedAt, a.RemainingAmount = coupon.StatusTerminated, &now, ptr[int64](0)
				if tt.applied != nil {
					tt.applied(a)
				}
			})
			inv := h.StoredInvoice(f.cust, func(inv *invoice.Invoice) { inv.Status = invoice.StatusVoided })
			h.CreateCredit(&invoice.Credit{
				ID: core.NewID[core.InvoiceCreditID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: inv.ID, AppliedCouponID: a.ID,
				Amount: 100, Currency: inv.Currency, BeforeTaxes: true, CreatedAt: h.Now(),
			})
			out, err := f.restore(inv, deduct.RestoreOptions{})
			mustOK(t, err)
			eq(t, "restored", len(out.AppliedCoupons), 1)
			got := h.ReloadAppliedCoupon(a.ID)
			eq(t, "status", got.Status, tt.wantStatus)
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

// source: spec/services/applied_coupons/recredit_service_spec.rb:21
// lago can hold a coupon credit without applied coupon; Tanvo's credits
// always reference it, so only a dangling reference is tested.
func TestRestoreCouponNotFound(t *testing.T) {
	f := newFixture(t)
	h := f.h
	inv := h.StoredInvoice(f.cust)
	h.CreateCredit(&invoice.Credit{
		ID: core.NewID[core.InvoiceCreditID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: inv.ID,
		AppliedCouponID: core.NewID[core.AppliedCouponID](h.IDs), Amount: 100, Currency: inv.Currency, BeforeTaxes: true,
	})
	_, err := f.restore(inv, deduct.RestoreOptions{})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("got error = %v, want = %v", err, core.ErrNotFound)
	}
}

// Coupons, credit notes and wallets consumed by an invoice return on void
// (domain §11.4).
func TestRestoreDeductions(t *testing.T) {
	tests := []struct {
		name        string
		issuesNote  bool
		voidedNote  bool
		wantNote    int64
		wantWallet  int64
		wantInbound int
	}{
		// source: spec/services/credit_notes/recredit_service_spec.rb:1
		// source: spec/services/wallet_transactions/recredit_service_spec.rb:23
		{name: "everything", wantNote: 30, wantWallet: 1000, wantInbound: 1},
		// source: app/services/invoices/void_service.rb:41 (with
		// generate_credit_note the creditable amount counts what wallets
		// and credit notes paid: neither is given back)
		{name: "credit note issued", issuesNote: true, wantNote: 0, wantWallet: 950},
		{name: "voided credit note", voidedNote: true, wantNote: 0, wantWallet: 1000, wantInbound: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			h := f.h
			src := h.StoredInvoice(f.cust, func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized })
			cn := h.StoredCreditNote(src, 30)
			w := h.Wallet(f.cust, "10")
			inv := h.StoredInvoice(f.cust)
			h.StoredFee(inv, 80)
			inv.SubTotalExcludingTaxesAmount = 80
			out, _, err := f.after(inv, deduct.Options{})
			mustOK(t, err)
			eq(t, "credit notes", out.CreditNotes, 30)
			eq(t, "prepaid", out.PrepaidCredit, 50)
			eq(t, "consumed balance", h.ReloadWallet(w.ID).BalanceAmount, 950)
			if tt.voidedNote {
				h.Do(func(ctx context.Context, r port.Repos) error {
					c, err := r.CreditNotes.Get(ctx, h.OrgID(), cn.ID)
					if err != nil {
						return err
					}
					c.CreditStatus, c.BalanceAmount = creditnote.CreditStatusVoided, 0
					return r.CreditNotes.Update(ctx, c)
				})
			}
			inv.Status = invoice.StatusVoided
			before := len(h.Webhooks())
			got, err := f.restore(inv, deduct.RestoreOptions{IssuesCreditNote: tt.issuesNote})
			mustOK(t, err)
			eq(t, "note balance", h.ReloadCreditNote(cn.ID).BalanceAmount, tt.wantNote)
			eq(t, "wallet balance", h.ReloadWallet(w.ID).BalanceAmount, tt.wantWallet)
			eq(t, "inbound transactions", len(got.WalletTransactions), tt.wantInbound)
			if tt.wantInbound == 0 {
				return
			}
			in := got.WalletTransactions[0]
			eq(t, "type", in.TransactionType, wallet.TransactionTypeInbound)
			eq(t, "status", in.TransactionStatus, wallet.TransactionStatusGranted)
			eq(t, "amount", in.Amount, 50)
			eq(t, "webhook", slices.Contains(h.Webhooks()[before:], webhook.EventTypeWalletTransactionCreated), true)
		})
	}
}

// source: spec/services/wallet_transactions/recredit_service_spec.rb:10
// A terminated wallet is not recredited.
func TestRestoreTerminatedWallet(t *testing.T) {
	f := newFixture(t)
	h := f.h
	w := h.Wallet(f.cust, "10")
	inv := h.StoredInvoice(f.cust)
	h.StoredFee(inv, 80)
	inv.SubTotalExcludingTaxesAmount = 80
	_, _, err := f.after(inv, deduct.Options{})
	mustOK(t, err)
	h.Do(func(ctx context.Context, r port.Repos) error {
		got, err := r.Wallets.Get(ctx, h.OrgID(), w.ID)
		if err != nil {
			return err
		}
		if err := got.Terminate(h.Now()); err != nil {
			return err
		}
		return r.Wallets.Update(ctx, got)
	})
	got, err := f.restore(inv, deduct.RestoreOptions{})
	mustOK(t, err)
	eq(t, "inbound transactions", len(got.WalletTransactions), 0)
}

func TestResetDraft(t *testing.T) {
	f := newFixture(t)
	h := f.h
	inv := h.StoredInvoice(f.cust, func(inv *invoice.Invoice) {
		inv.Status = invoice.StatusDraft
		inv.ProgressiveBillingCreditAmount, inv.CouponsAmount = 10, 20
	})
	h.CreateCredit(&invoice.Credit{
		ID: core.NewID[core.InvoiceCreditID](h.IDs), OrganizationID: h.OrgID(), InvoiceID: inv.ID,
		ProgressiveBillingInvoiceID: core.NewID[core.InvoiceID](h.IDs), Amount: 10, Currency: inv.Currency, BeforeTaxes: true,
	})
	h.Do(func(ctx context.Context, r port.Repos) error { return f.s.ResetDraft(ctx, r, inv) })
	eq(t, "credits", len(h.InvoiceCredits(inv.ID)), 0)
	eq(t, "progressive", inv.ProgressiveBillingCreditAmount, 0)
	eq(t, "coupons", inv.CouponsAmount, 0)

	inv.Status = invoice.StatusFinalized
	var te *core.TransitionError
	err := h.Deps.Tx.InTx(h.Ctx, func(ctx context.Context, r port.Repos) error { return f.s.ResetDraft(ctx, r, inv) })
	if !errors.As(err, &te) {
		t.Errorf("finalized: got error = %v, want = transition error", err)
	}
}
