package wallets_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

func logActivities(h *billingtest.Harness) {
	h.LogActivities(auditlog.Actor{Source: auditlog.ActivitySourceAPI})
}

func TestActivityLogs(t *testing.T) {
	t.Run("wallet created and updated", func(t *testing.T) {
		t.Log("source: spec/services/wallets/create_service_spec.rb:63")
		t.Log("source: spec/services/wallets/update_service_spec.rb:44")
		h, s := setup(t, t0)
		logActivities(h)
		c := eurCustomer(h)
		w, err := s.Create(h.Ctx, h.OrgID(), createInput(c))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{Name: wallets.To(ptr("new name"))}); err != nil {
			t.Fatalf("update: %v", err)
		}
		id := w.ID.UUID()
		h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityWalletCreated, ResourceType: auditlog.ResourceWallet, ResourceID: id, ExternalCustomerID: c.ExternalID},
			billingtest.ActivityWant{Type: auditlog.ActivityWalletUpdated, ResourceType: auditlog.ResourceWallet, ResourceID: id, ExternalCustomerID: c.ExternalID, Changes: []string{"name"}},
		)
	})

	t.Run("transactions from params", func(t *testing.T) {
		t.Log("source: spec/services/wallet_transactions/create_from_params_service_spec.rb:94")
		h, s := setup(t, t0)
		c := h.Customer()
		w := h.Wallet(c, "10")
		logActivities(h)
		if _, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{
			WalletID: w.ID, PaidCredits: dec("10.00"), GrantedCredits: dec("15.00"), VoidedCredits: dec("3.00"),
		}); err != nil {
			t.Fatal(err)
		}
		// A transaction's log is on its wallet.
		want := billingtest.ActivityWant{Type: auditlog.ActivityWalletTransactionCreated, ResourceType: auditlog.ResourceWallet, ResourceID: w.ID.UUID(), ExternalCustomerID: c.ExternalID}
		h.WantActivities(want, want, want)
	})

	t.Run("a rejected transaction logs nothing", func(t *testing.T) {
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10")
		logActivities(h)
		if _, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{WalletID: w.ID, VoidedCredits: dec("30000.00")}); err == nil {
			t.Fatal("create: error = nil, want an error")
		}
		h.WantActivities()
	})

	t.Run("paid credit invoice finalized", func(t *testing.T) {
		t.Log("source: spec/services/invoices/paid_credit_service_spec.rb:143")
		h, s := setup(t, t0)
		logActivities(h)
		w, tr := purchase(t, h, s, false)
		ext := customerOf(h, w).ExternalID
		h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityWalletTransactionCreated, ResourceType: auditlog.ResourceWallet, ResourceID: w.ID.UUID(), ExternalCustomerID: ext},
			billingtest.ActivityWant{Type: auditlog.ActivityInvoicePaidCreditAdded, ResourceType: auditlog.ResourceInvoice, ResourceID: tr.InvoiceID.UUID(), ExternalCustomerID: ext},
		)
	})

	t.Run("open credit invoice paid: settled and finalized", func(t *testing.T) {
		t.Log("source: spec/services/wallet_transactions/settle_service_spec.rb:27")
		t.Log("source: spec/services/invoices/finalize_open_credit_service_spec.rb:33")
		h, s := setup(t, t0)
		w, tr := purchase(t, h, s, true)
		logActivities(h)
		h.Clock.Advance(48 * time.Hour)
		if err := s.HandleCreditInvoicePayment(h.Ctx, h.OrgID(), tr.InvoiceID, true); err != nil {
			t.Fatal(err)
		}
		ext := customerOf(h, w).ExternalID
		h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityWalletTransactionUpdated, ResourceType: auditlog.ResourceWallet, ResourceID: w.ID.UUID(), ExternalCustomerID: ext, Changes: settleChanges},
			billingtest.ActivityWant{Type: auditlog.ActivityInvoicePaidCreditAdded, ResourceType: auditlog.ResourceInvoice, ResourceID: tr.InvoiceID.UUID(), ExternalCustomerID: ext},
		)
	})

	t.Run("open credit invoice payment failed", func(t *testing.T) {
		t.Log("source: spec/services/wallet_transactions/mark_as_failed_service_spec.rb:34")
		h, s := setup(t, t0)
		w, tr := purchase(t, h, s, true)
		logActivities(h)
		if err := s.HandleCreditInvoicePayment(h.Ctx, h.OrgID(), tr.InvoiceID, false); err != nil {
			t.Fatal(err)
		}
		// Marking a failed transaction again still logs, without changes,
		// as the service's activity_loggable does.
		if _, err := s.MarkAsFailed(h.Ctx, h.OrgID(), tr.ID); err != nil {
			t.Fatal(err)
		}
		if got := transaction(h, tr.ID).Status; got != wallet.SettlementStatusFailed {
			t.Fatalf("status = %s, want = failed", got)
		}
		ext := customerOf(h, w).ExternalID
		h.WantActivities(
			billingtest.ActivityWant{Type: auditlog.ActivityWalletTransactionUpdated, ResourceType: auditlog.ResourceWallet, ResourceID: w.ID.UUID(), ExternalCustomerID: ext, Changes: failChanges},
			billingtest.ActivityWant{Type: auditlog.ActivityWalletTransactionUpdated, ResourceType: auditlog.ResourceWallet, ResourceID: w.ID.UUID(), ExternalCustomerID: ext},
		)
	})
}

var (
	settleChanges = []string{"settled_at", "status"}
	failChanges   = []string{"failed_at", "status"}
)

func customerOf(h *billingtest.Harness, w *wallet.Wallet) *customer.Customer {
	return billingtest.Read(h, func(ctx context.Context, r port.Repos) (*customer.Customer, error) {
		return r.Customers.Get(ctx, h.OrgID(), w.CustomerID)
	})
}
