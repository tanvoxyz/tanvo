package rpc_test

import (
	"context"
	"slices"
	"testing"

	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	walletv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/wallet/v1"
)

// section stores an invoice custom section with code.
func (f *walletFixture) section(code string) *invoice.CustomSection {
	h := f.s.Fixtures
	s := &invoice.CustomSection{
		ID: core.NewID[core.InvoiceCustomSectionID](h.IDs), OrganizationID: h.OrgID(), Code: code, Name: code, SectionType: invoice.CustomSectionTypeManual,
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceCustomSections.Create(ctx, s) })
	return s
}

func TestCreateWalletFields(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	sec := f.section("section_code_1")
	in := f.walletInput()
	in.SetPaidCredits("10")
	in.SetGrantedCredits("5")
	in.SetPurchaseOrderNumber("PO-WALLET")
	in.SetTransactionName("Initial")
	in.SetTransactionPriority(3)
	in.SetTransactionMetadata([]*commonv1.MetadataItem{commonv1.MetadataItem_builder{Key: new("k"), Value: new("v")}.Build()})
	in.SetPaidTopUpMaxAmount("5")
	in.SetIgnorePaidTopUpLimitsOnCreation(true)
	in.SetInvoiceCustomSectionIds([]string{"code:section_code_1"})
	in.SetRecurringTransactionRules([]*walletv1.RecurringTransactionRule{walletv1.RecurringTransactionRule_builder{
		Trigger:  new(walletv1.RecurringTrigger_RECURRING_TRIGGER_INTERVAL),
		Interval: new(commonv1.Interval_INTERVAL_MONTHLY), GrantedCredits: new("5"),
		PurchaseOrderNumber: new("PO-RULE"), SkipInvoiceCustomSections: new(true),
	}.Build()})
	w, err := f.create(t, in)
	if err != nil {
		t.Fatalf("CreateWallet: %v", err)
	}
	// source: spec/requests/api/v1/wallets_controller_spec.rb:15
	if w.GetPurchaseOrderNumber() != "PO-WALLET" || w.GetSkipInvoiceCustomSections() || !slices.Equal(w.GetInvoiceCustomSectionIds(), []string{sec.ID.String()}) {
		t.Errorf("wallet: got = (%q, %t, %v), want = (PO-WALLET, false, [%s])",
			w.GetPurchaseOrderNumber(), w.GetSkipInvoiceCustomSections(), w.GetInvoiceCustomSectionIds(), sec.ID)
	}
	if r := w.GetRecurringTransactionRules()[0]; r.GetPurchaseOrderNumber() != "PO-RULE" || !r.GetSkipInvoiceCustomSections() || len(r.GetInvoiceCustomSectionIds()) != 0 {
		t.Errorf("rule: got = %v, want = PO-RULE skipping the sections", r)
	}
	list, err := f.c.ListWalletTransactions(t.Context(), walletv1.ListWalletTransactionsRequest_builder{WalletId: new(w.GetId())}.Build())
	if err != nil {
		t.Fatalf("ListWalletTransactions: %v", err)
	}
	// The initial top-up takes the transaction_* fields and ignores the
	// maximum (create_service.rb:122-134).
	ts := list.GetWalletTransactions()
	if len(ts) != 2 {
		t.Fatalf("transactions: got = %d, want = 2", len(ts))
	}
	for _, tr := range ts {
		if tr.GetName() != "Initial" || tr.GetPriority() != 3 || len(tr.GetMetadata()) != 1 || tr.GetPurchaseOrderNumber() != "PO-RULE" {
			t.Errorf("top-up: got = (%q, %d, %v, %q), want = (Initial, 3, 1 item, PO-RULE)", tr.GetName(), tr.GetPriority(), tr.GetMetadata(), tr.GetPurchaseOrderNumber())
		}
	}
}

func TestUpdateWalletFields(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	sec := f.section("section_code_1")
	w := f.s.Fixtures.Wallet(f.cust, "10")
	update := func(in *walletv1.Wallet, mask ...string) *walletv1.Wallet {
		t.Helper()
		req := walletv1.UpdateWalletRequest_builder{Wallet: in}.Build()
		if mask != nil {
			req.SetUpdateMask(&fieldmaskpb.FieldMask{Paths: mask})
		}
		resp, err := f.c.UpdateWallet(t.Context(), req)
		if err != nil {
			t.Fatalf("UpdateWallet: %v", err)
		}
		return resp.GetWallet()
	}
	id := new(w.ID.String())
	// source: spec/services/wallets/update_service_spec.rb:47
	got := update(walletv1.Wallet_builder{Id: id, PurchaseOrderNumber: new("PO-1"), InvoiceCustomSectionIds: []string{"code:section_code_1"}}.Build())
	if got.GetPurchaseOrderNumber() != "PO-1" || !slices.Equal(got.GetInvoiceCustomSectionIds(), []string{sec.ID.String()}) {
		t.Errorf("got = (%q, %v), want = (PO-1, [%s])", got.GetPurchaseOrderNumber(), got.GetInvoiceCustomSectionIds(), sec.ID)
	}
	// A masked empty list clears the sections
	// (attach_to_resource_service.rb:80-84).
	got = update(walletv1.Wallet_builder{Id: id}.Build(), "invoice_custom_section_ids", "purchase_order_number")
	if got.HasPurchaseOrderNumber() || len(got.GetInvoiceCustomSectionIds()) != 0 {
		t.Errorf("got = (%q, %v), want = both cleared", got.GetPurchaseOrderNumber(), got.GetInvoiceCustomSectionIds())
	}
	// source: spec/services/wallets/update_service_spec.rb:134
	got = update(walletv1.Wallet_builder{Id: id, SkipInvoiceCustomSections: new(true)}.Build())
	if !got.GetSkipInvoiceCustomSections() {
		t.Errorf("skip_invoice_custom_sections: got = false, want = true")
	}
}

func TestCreateWalletTransactionFields(t *testing.T) {
	t.Parallel()
	f := newWalletFixture(t)
	sec := f.section("section_code_1")
	w := f.s.Fixtures.Wallet(f.cust, "0", func(w *wallet.Wallet) { w.PurchaseOrderNumber = new("PO-WALLET") })
	resp, err := f.c.CreateWalletTransaction(t.Context(), walletv1.CreateWalletTransactionRequest_builder{
		WalletId: new(w.ID.String()), PaidCredits: new("10"), GrantedCredits: new("10"), Name: new("Custom Top-up Name"),
		Priority: new(int32(1)), PurchaseOrderNumber: new("PO-789"), InvoiceCustomSectionIds: []string{"code:section_code_1"},
	}.Build())
	if err != nil {
		t.Fatalf("CreateWalletTransaction: %v", err)
	}
	ts := resp.GetWalletTransactions()
	if len(ts) != 2 {
		t.Fatalf("transactions: got = %d, want = 2", len(ts))
	}
	for _, tr := range ts {
		// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:38
		// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:247
		// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:283
		if tr.GetPriority() != 1 || tr.GetPurchaseOrderNumber() != "PO-789" || !slices.Equal(tr.GetInvoiceCustomSectionIds(), []string{sec.ID.String()}) ||
			tr.GetPaymentMethodType() != commonv1.PaymentMethodType_PAYMENT_METHOD_TYPE_PROVIDER || tr.HasPaymentMethodId() ||
			tr.GetBillingEntityId() != f.s.Fixtures.BillingEntity.ID.String() {
			t.Errorf("transaction: got = %v, want = priority 1, PO-789, [%s], provider without method, the customer's billing entity", tr, sec.ID)
		}
	}
	// A transaction without its own purchase order number shows the
	// wallet's (WalletTransaction#resolved_purchase_order_number).
	resp, err = f.c.CreateWalletTransaction(t.Context(), walletv1.CreateWalletTransactionRequest_builder{
		WalletId: new(w.ID.String()), GrantedCredits: new("1"),
	}.Build())
	if err != nil || resp.GetWalletTransactions()[0].GetPurchaseOrderNumber() != "PO-WALLET" {
		t.Errorf("purchase_order_number: got = %v (%v), want = PO-WALLET", resp.GetWalletTransactions(), err)
	}
}

func TestCreateWalletTransactionTargetedVoid(t *testing.T) {
	t.Parallel()
	// source: spec/requests/api/v1/wallet_transactions_controller_spec.rb:119
	f := newWalletFixture(t)
	h := f.s.Fixtures
	w := h.Wallet(f.cust, "20", func(w *wallet.Wallet) { w.Traceable = true })
	a := h.InboundTransaction(w, wallet.TransactionStatusGranted, "5")
	b := h.InboundTransaction(w, wallet.TransactionStatusGranted, "15")
	resp, err := f.c.CreateWalletTransaction(t.Context(), walletv1.CreateWalletTransactionRequest_builder{
		WalletId: new(w.ID.String()), VoidedTransactionId: new(b.ID.String()),
	}.Build())
	if err != nil {
		t.Fatalf("CreateWalletTransaction: %v", err)
	}
	if ts := resp.GetWalletTransactions(); len(ts) != 1 || ts[0].GetTransactionStatus() != walletv1.TransactionStatus_TRANSACTION_STATUS_VOIDED || ts[0].GetCreditAmount() != "15.00000" {
		t.Errorf("void: got = %v, want = one void of 15 credits", ts)
	}
	got, err := f.c.GetWalletTransaction(t.Context(), walletv1.GetWalletTransactionRequest_builder{WalletTransactionId: new(a.ID.String())}.Build())
	if err != nil || got.GetWalletTransaction().GetRemainingAmount().GetAmount() != "5.00" {
		t.Errorf("earlier grant: got = %v (%v), want = 5.00 remaining", got.GetWalletTransaction().GetRemainingAmount(), err)
	}
}
