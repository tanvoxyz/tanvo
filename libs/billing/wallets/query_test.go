package wallets_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
)

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error = %v, want %s not found", err, resource)
	}
}

func walletIDs(ws []*wallet.Wallet) []core.WalletID {
	out := make([]core.WalletID, len(ws))
	for i, w := range ws {
		out[i] = w.ID
	}
	return out
}

func TestList(t *testing.T) {
	t.Parallel()
	// source: spec/queries/wallets_query_spec.rb:55
	t.Run("pages through the wallets", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		c := h.Customer()
		var want []core.WalletID
		for range 3 {
			want = append(want, h.Wallet(c, "1").ID)
		}
		p1, err := s.List(h.Ctx, h.OrgID(), wallets.ListFilter{}, core.PageRequest{Size: 2})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		p2, err := s.List(h.Ctx, h.OrgID(), wallets.ListFilter{}, core.PageRequest{Size: 2, Token: p1.NextToken})
		if err != nil {
			t.Fatalf("List(page 2) error = %v", err)
		}
		got := append(walletIDs(p1.Items), walletIDs(p2.Items)...)
		if len(got) != 3 || got[0] != want[0] || got[2] != want[2] || p2.NextToken != "" {
			t.Fatalf("wallets = %v (next %q), want %v", got, p2.NextToken, want)
		}
	})
	// source: spec/queries/wallets_query_spec.rb:69
	// source: spec/queries/wallets_query_spec.rb:80
	t.Run("filters by currency", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		c := h.Customer()
		h.Wallet(c, "1")
		e := h.Wallet(h.Customer(), "1", func(w *wallet.Wallet) { w.Currency = eur })
		p, err := s.List(h.Ctx, h.OrgID(), wallets.ListFilter{Currency: eur}, core.PageRequest{Size: 1})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(p.Items) != 1 || p.Items[0].ID != e.ID || p.NextToken != "" {
			t.Fatalf("wallets = %v, want the EUR wallet", walletIDs(p.Items))
		}
		p, err = s.List(h.Ctx, h.OrgID(), wallets.ListFilter{Currency: eur, CustomerID: c.ID}, core.PageRequest{})
		if err != nil || len(p.Items) != 0 {
			t.Fatalf("List() = %v, %v, want no wallets", walletIDs(p.Items), err)
		}
	})
	// source: spec/queries/wallets_query_spec.rb:90
	// source: spec/queries/wallets_query_spec.rb:101
	t.Run("filters by customer, which must exist", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		c := h.Customer()
		w := h.Wallet(c, "1")
		h.Wallet(h.Customer(), "1")
		p, err := s.List(h.Ctx, h.OrgID(), wallets.ListFilter{CustomerID: c.ID}, core.PageRequest{})
		if err != nil || len(p.Items) != 1 || p.Items[0].ID != w.ID {
			t.Fatalf("List() = %v, %v, want the customer's wallet", walletIDs(p.Items), err)
		}
		_, err = s.List(h.Ctx, h.OrgID(), wallets.ListFilter{CustomerID: core.NewID[core.CustomerID](h.IDs)}, core.PageRequest{})
		wantNotFound(t, err, "customer")
	})
	// source: spec/queries/wallets_query_spec.rb:112
	t.Run("filters by the wallet's billing entity or else its customer's", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		other := organization.NewBillingEntity(h.OrgID(), core.NewID[core.BillingEntityID](h.IDs), "other", "Other", t0)
		h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, other) })
		def, oth := h.Customer(), h.Customer(func(c *customer.Customer) { c.BillingEntityID = other.ID })
		stamped := h.Wallet(oth, "1", func(w *wallet.Wallet) { w.BillingEntityID = h.BillingEntity.ID })
		inherited := h.Wallet(def, "1")
		h.Wallet(def, "1", func(w *wallet.Wallet) { w.BillingEntityID = other.ID })
		h.Wallet(oth, "1")
		p, err := s.List(h.Ctx, h.OrgID(), wallets.ListFilter{BillingEntityIDs: []core.BillingEntityID{h.BillingEntity.ID}}, core.PageRequest{})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if got := walletIDs(p.Items); len(got) != 2 || got[0] != stamped.ID || got[1] != inherited.ID {
			t.Fatalf("wallets = %v, want %v and %v", got, stamped.ID, inherited.ID)
		}
		// source: spec/queries/wallets_query_spec.rb:150
		p, err = s.List(h.Ctx, h.OrgID(), wallets.ListFilter{BillingEntityIDs: []core.BillingEntityID{h.BillingEntity.ID, other.ID}}, core.PageRequest{})
		if err != nil || len(p.Items) != 4 {
			t.Fatalf("List(both) = %d, %v, want 4 wallets", len(p.Items), err)
		}
	})
}

func TestListTransactions(t *testing.T) {
	t.Parallel()
	// source: spec/queries/wallet_transactions_query_spec.rb:78
	// source: spec/queries/wallet_transactions_query_spec.rb:92
	t.Run("filters by status and type", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10")
		pending := h.InboundTransaction(w, wallet.TransactionStatusPurchased, "1", func(t *wallet.Transaction) { t.Status, t.SettledAt = wallet.SettlementStatusPending, nil })
		h.InboundTransaction(w, wallet.TransactionStatusGranted, "1")
		p, err := s.ListTransactions(h.Ctx, h.OrgID(), w.ID, wallets.TransactionListFilter{Status: wallet.SettlementStatusPending, TransactionType: wallet.TransactionTypeInbound}, core.PageRequest{})
		if err != nil || len(p.Items) != 1 || p.Items[0].ID != pending.ID {
			t.Fatalf("ListTransactions() = %v, %v, want the pending one", p.Items, err)
		}
	})
	// source: spec/queries/wallet_transactions_query_spec.rb:106
	// source: spec/queries/wallet_transactions_query_spec.rb:117
	// source: spec/queries/wallet_transactions_query_spec.rb:136
	t.Run("filters by metadata", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10")
		md := func(kv ...string) func(*wallet.Transaction) {
			return func(t *wallet.Transaction) {
				for i := 0; i < len(kv); i += 2 {
					t.Metadata = append(t.Metadata, core.MetadataItem{Key: kv[i], Value: kv[i+1]})
				}
			}
		}
		tagged := h.InboundTransaction(w, wallet.TransactionStatusGranted, "1", md("team", "a", "env", "prod"))
		h.InboundTransaction(w, wallet.TransactionStatusGranted, "1", md("team", "a"))
		h.InboundTransaction(w, wallet.TransactionStatusGranted, "1", md("team", "b", "env", "prod"))
		p, err := s.ListTransactions(h.Ctx, h.OrgID(), w.ID, wallets.TransactionListFilter{Metadata: map[string]string{"team": "a", "env": "prod", "blank": ""}}, core.PageRequest{Size: 1})
		if err != nil || len(p.Items) != 1 || p.Items[0].ID != tagged.ID {
			t.Fatalf("ListTransactions() = %v, %v, want the tagged one", p.Items, err)
		}
	})
	t.Run("requires the wallet", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		_, err := s.ListTransactions(h.Ctx, h.OrgID(), core.NewID[core.WalletID](h.IDs), wallets.TransactionListFilter{}, core.PageRequest{})
		wantNotFound(t, err, "wallet")
	})
}

func TestListConsumptions(t *testing.T) {
	t.Parallel()
	traced := func(t *testing.T) (*billingtest.Harness, *wallets.Service, *wallet.Wallet, *wallet.Transaction, []*wallet.Transaction) {
		t.Helper()
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.Traceable = true })
		in := h.InboundTransaction(w, wallet.TransactionStatusGranted, "10")
		var outs []*wallet.Transaction
		for i := range 3 {
			out := h.InboundTransaction(w, wallet.TransactionStatusInvoiced, "1", func(t *wallet.Transaction) {
				t.TransactionType, t.RemainingAmount = wallet.TransactionTypeOutbound, nil
			})
			outs = append(outs, out)
			at := t0.Add(time.Duration(i) * time.Hour)
			h.Do(func(ctx context.Context, r port.Repos) error {
				return r.WalletTransactions.CreateConsumption(ctx, &wallet.Consumption{
					ID: core.NewID[core.WalletTransactionConsumptionID](h.IDs), OrganizationID: h.OrgID(),
					InboundTransactionID: in.ID, OutboundTransactionID: out.ID, ConsumedAmount: 100, CreatedAt: at, UpdatedAt: at,
				})
			})
		}
		return h, s, w, in, outs
	}
	// source: spec/queries/wallet_transaction_consumptions_query_spec.rb:62
	// source: spec/queries/wallet_transaction_consumptions_query_spec.rb:68
	t.Run("lists the consumptions of an inbound transaction, newest first", func(t *testing.T) {
		t.Parallel()
		h, s, _, in, outs := traced(t)
		p1, err := s.ListConsumptions(h.Ctx, h.OrgID(), in.ID, core.PageRequest{Size: 2})
		if err != nil {
			t.Fatalf("ListConsumptions() error = %v", err)
		}
		p2, err := s.ListConsumptions(h.Ctx, h.OrgID(), in.ID, core.PageRequest{Size: 2, Token: p1.NextToken})
		if err != nil {
			t.Fatalf("ListConsumptions(page 2) error = %v", err)
		}
		if len(p1.Items) != 2 || p1.Items[0].OutboundTransactionID != outs[2].ID || len(p2.Items) != 1 || p2.Items[0].OutboundTransactionID != outs[0].ID || p2.NextToken != "" {
			t.Fatalf("pages = %v, %v, want the outbound transactions newest first", p1.Items, p2.Items)
		}
	})
	// source: spec/queries/wallet_transaction_consumptions_query_spec.rb:140
	t.Run("lists the fundings of an outbound transaction", func(t *testing.T) {
		t.Parallel()
		h, s, _, in, outs := traced(t)
		p, err := s.ListConsumptions(h.Ctx, h.OrgID(), outs[1].ID, core.PageRequest{})
		if err != nil || len(p.Items) != 1 || p.Items[0].InboundTransactionID != in.ID {
			t.Fatalf("ListConsumptions() = %v, %v, want the inbound transaction", p.Items, err)
		}
	})
	// source: spec/queries/wallet_transaction_consumptions_query_spec.rb:81
	t.Run("requires the transaction", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		_, err := s.ListConsumptions(h.Ctx, h.OrgID(), core.NewID[core.WalletTransactionID](h.IDs), core.PageRequest{})
		wantNotFound(t, err, "wallet_transaction")
	})
	// source: spec/queries/wallet_transaction_consumptions_query_spec.rb:91
	t.Run("requires a traceable wallet", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		in := h.InboundTransaction(h.Wallet(h.Customer(), "1"), wallet.TransactionStatusGranted, "1")
		_, err := s.ListConsumptions(h.Ctx, h.OrgID(), in.ID, core.PageRequest{})
		wantCodes(t, err, "wallet/"+wallets.CodeNotTraceable)
	})
}

func TestPaymentURLInvoice(t *testing.T) {
	t.Parallel()
	inv := core.NewID[core.InvoiceID]
	cases := []struct {
		name, source string
		opt          func(*wallet.Transaction)
		status       wallet.TransactionStatus
		want         string
	}{
		{"settled purchase", "spec/services/wallet_transactions/payments/generate_payment_url_service_spec.rb:23", nil, wallet.TransactionStatusPurchased, "base/" + wallets.CodeTransactionAlreadySettled},
		{"pending purchase without invoice", "spec/services/wallet_transactions/payments/generate_payment_url_service_spec.rb:35", func(t *wallet.Transaction) { t.Status = wallet.SettlementStatusPending }, wallet.TransactionStatusPurchased, "base/" + wallets.CodeTransactionHasNoInvoice},
		{"grant", "spec/services/wallet_transactions/payments/generate_payment_url_service_spec.rb:71", nil, wallet.TransactionStatusGranted, "base/" + wallets.CodeTransactionNotPurchased},
	}
	for _, tc := range cases {
		t.Run("rejects a "+tc.name, func(t *testing.T) {
			t.Parallel()
			t.Log("source:", tc.source)
			h, s := setup(t, t0)
			var opts []func(*wallet.Transaction)
			if tc.opt != nil {
				opts = append(opts, tc.opt)
			}
			tx := h.InboundTransaction(h.Wallet(h.Customer(), "1"), tc.status, "1", opts...)
			_, err := s.PaymentURLInvoice(h.Ctx, h.OrgID(), tx.ID)
			wantCodes(t, err, tc.want)
		})
	}
	// source: spec/services/wallet_transactions/payments/generate_payment_url_service_spec.rb:42
	t.Run("returns the invoice of a pending purchase", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		id := inv(h.IDs)
		tx := h.InboundTransaction(h.Wallet(h.Customer(), "1"), wallet.TransactionStatusPurchased, "1", func(t *wallet.Transaction) {
			t.Status, t.InvoiceID = wallet.SettlementStatusPending, id
		})
		got, err := s.PaymentURLInvoice(h.Ctx, h.OrgID(), tx.ID)
		if err != nil || got != id {
			t.Fatalf("PaymentURLInvoice() = %v, %v, want %v", got, err, id)
		}
	})
	// source: spec/services/wallet_transactions/payments/generate_payment_url_service_spec.rb:7
	t.Run("requires the transaction", func(t *testing.T) {
		t.Parallel()
		h, s := setup(t, t0)
		_, err := s.PaymentURLInvoice(h.Ctx, h.OrgID(), core.NewID[core.WalletTransactionID](h.IDs))
		wantNotFound(t, err, "wallet_transaction")
	})
}
