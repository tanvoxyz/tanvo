package wallets_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// section stores an invoice custom section with code.
func section(h *billingtest.Harness, code string) *invoice.CustomSection {
	h.T.Helper()
	s := &invoice.CustomSection{
		ID: core.NewID[core.InvoiceCustomSectionID](h.IDs), OrganizationID: h.OrgID(), Code: code, Name: code, SectionType: invoice.CustomSectionTypeManual,
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceCustomSections.Create(ctx, s) })
	return s
}

// deleteSection soft-deletes s.
func deleteSection(h *billingtest.Harness, s *invoice.CustomSection) {
	h.T.Helper()
	s.DeletedAt = new(h.Now())
	h.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceCustomSections.Update(ctx, s) })
}

// appliedCodes returns the codes of the sections printed on invoice id.
func appliedCodes(h *billingtest.Harness, id core.InvoiceID) []string {
	h.T.Helper()
	as := billingtest.Read(h, func(ctx context.Context, r port.Repos) ([]*invoice.AppliedCustomSection, error) {
		return r.AppliedInvoiceCustomSections.ListByInvoice(ctx, h.OrgID(), id)
	})
	out := []string{}
	for _, a := range as {
		out = append(out, a.Code)
	}
	return out
}

func sectionsOf(skip bool, ss ...*invoice.CustomSection) wallet.CustomSections {
	cs := wallet.CustomSections{Skip: skip}
	for _, s := range ss {
		cs.IDs = append(cs.IDs, s.ID)
	}
	return cs
}

func wantSections(t *testing.T, what string, got, want wallet.CustomSections) {
	t.Helper()
	if got.Skip != want.Skip || !slices.Equal(got.IDs, want.IDs) {
		t.Errorf("%s sections: got = %+v, want = %+v", what, got, want)
	}
}

func TestCreateTransactionsCustomSections(t *testing.T) {
	tests := []struct {
		name, source string
		cs           func(sec *invoice.CustomSection) *wallets.CustomSections
		want         func(sec *invoice.CustomSection) wallet.CustomSections
	}{
		{
			name: "codes", source: "spec/services/wallet_transactions/create_from_params_service_spec.rb:483",
			cs: func(*invoice.CustomSection) *wallets.CustomSections {
				return &wallets.CustomSections{Sections: []refs.Ref{refs.ByCode("section_code_1"), refs.ByCode("unknown")}}
			},
			want: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
		},
		{
			name: "IDs without skipping", source: "spec/services/wallet_transactions/create_from_params_service_spec.rb:506",
			cs: func(sec *invoice.CustomSection) *wallets.CustomSections {
				return &wallets.CustomSections{Skip: new(false), Sections: []refs.Ref{refs.ByID(sec.ID), refs.ByID(sec.ID)}}
			},
			want: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
		},
		{
			name: "skip", source: "spec/services/wallet_transactions/create_from_params_service_spec.rb:523",
			cs: func(sec *invoice.CustomSection) *wallets.CustomSections {
				return &wallets.CustomSections{Skip: new(true), Sections: []refs.Ref{refs.ByID(sec.ID)}}
			},
			want: func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
		},
		{
			name: "none", source: "tanvo (no selection keeps the transaction without sections)",
			cs:   func(*invoice.CustomSection) *wallets.CustomSections { return nil },
			want: func(*invoice.CustomSection) wallet.CustomSections { return wallet.CustomSections{} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, s := setup(t, t0)
			sec := section(h, "section_code_1")
			w := h.Wallet(h.Customer(), "10")
			ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{
				WalletID: w.ID, PaidCredits: dec("10"), GrantedCredits: dec("10"), InvoiceCustomSections: tt.cs(sec),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(ts) != 2 {
				t.Fatalf("transactions: got = %d, want = 2", len(ts))
			}
			for _, tr := range ts {
				wantSections(t, string(tr.TransactionStatus), transaction(h, tr.ID).InvoiceCustomSections, tt.want(sec))
			}
		})
	}
}

func TestCreateTransactionsVoidedInvoice(t *testing.T) {
	// source: spec/services/wallet_transactions/create_from_params_service_spec.rb:199
	h, s := setup(t, t0)
	c := h.Customer()
	voided := h.StoredInvoice(c, func(inv *invoice.Invoice) { inv.Status = invoice.StatusVoided })
	w := h.Wallet(c, "10")
	ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), wallets.TransactionInput{
		WalletID: w.ID, GrantedCredits: dec("10"), VoidedInvoiceID: voided.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := transaction(h, ts[0].ID).VoidedInvoiceID; got != voided.ID {
		t.Errorf("voided_invoice_id: got = %v, want = %v", got, voided.ID)
	}
}

func TestRecreditVoidedInvoice(t *testing.T) {
	// source: spec/services/wallet_transactions/recredit_service_spec.rb:49
	h, s := setup(t, t0)
	c := h.Customer()
	voided := h.StoredInvoice(c, func(inv *invoice.Invoice) { inv.Status = invoice.StatusVoided })
	w := h.Wallet(c, "10")
	tr := addTransaction(h, w, func(t *wallet.Transaction) {
		t.TransactionType, t.TransactionStatus = wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced
		t.InvoiceID, t.PurchaseOrderNumber = voided.ID, new("PO-123")
	})
	got, err := s.Recredit(h.Ctx, h.OrgID(), tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	got = transaction(h, got.ID)
	if got.VoidedInvoiceID != voided.ID || got.TransactionType != wallet.TransactionTypeInbound ||
		got.TransactionStatus != wallet.TransactionStatusGranted || got.PurchaseOrderNumber == nil || *got.PurchaseOrderNumber != "PO-123" {
		t.Errorf("recredit: got = (%v, %s, %s, %v), want = (%v, inbound, granted, PO-123)",
			got.VoidedInvoiceID, got.TransactionType, got.TransactionStatus, got.PurchaseOrderNumber, voided.ID)
	}
}

func TestBillPaidCreditCustomSections(t *testing.T) {
	tests := []struct {
		name, source string
		customer     func(c *customer.Customer, def *invoice.CustomSection)
		wallet       func(sec *invoice.CustomSection) wallet.CustomSections
		tx           func(sec *invoice.CustomSection) *wallets.CustomSections
		want         []string
	}{
		{
			name: "the transaction's over the customer's", source: "spec/services/invoices/paid_credit_service_spec.rb:154",
			tx: func(*invoice.CustomSection) *wallets.CustomSections {
				return &wallets.CustomSections{Sections: []refs.Ref{refs.ByCode("resource")}}
			},
			want: []string{"resource"},
		},
		{
			name: "the transaction's when the customer skips", source: "spec/support/shared_examples/applied_invoice_custom_sections.rb:16",
			customer: func(c *customer.Customer, _ *invoice.CustomSection) { c.SkipInvoiceCustomSections = true },
			tx: func(*invoice.CustomSection) *wallets.CustomSections {
				return &wallets.CustomSections{Sections: []refs.Ref{refs.ByCode("resource")}}
			},
			want: []string{"resource"},
		},
		{
			name: "the wallet's over the customer's", source: "spec/services/invoices/paid_credit_service_spec.rb:161",
			wallet: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
			want:   []string{"resource"},
		},
		{
			name: "the transaction skips", source: "spec/services/invoices/paid_credit_service_spec.rb:168",
			wallet: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
			tx:     func(*invoice.CustomSection) *wallets.CustomSections { return &wallets.CustomSections{Skip: new(true)} },
			want:   []string{},
		},
		{
			name: "the wallet skips", source: "spec/services/invoices/paid_credit_service_spec.rb:187",
			wallet: func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
			want:   []string{},
		},
		{
			name: "the customer's", source: "spec/services/invoices/paid_credit_service_spec.rb:150",
			want: []string{"default"},
		},
		{
			name: "a deleted wallet section falls back", source: "tanvo (deleted sections stay linked and are ignored, as kept ones are in lago)",
			wallet: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
			want:   []string{"default"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, s := setup(t, t0)
			def := section(h, "default")
			res := section(h, "resource")
			c := h.Customer(func(c *customer.Customer) {
				c.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{def.ID}
				if tt.customer != nil {
					tt.customer(c, def)
				}
			})
			w := h.Wallet(c, "10", func(w *wallet.Wallet) {
				if tt.wallet != nil {
					w.InvoiceCustomSections = tt.wallet(res)
				}
			})
			if tt.name == "a deleted wallet section falls back" {
				deleteSection(h, res)
			}
			in := wallets.TransactionInput{WalletID: w.ID, PaidCredits: dec("10")}
			if tt.tx != nil {
				in.InvoiceCustomSections = tt.tx(res)
			}
			ts, err := s.CreateTransactions(h.Ctx, h.OrgID(), in)
			if err != nil {
				t.Fatal(err)
			}
			if ts[0].InvoiceID.IsZero() {
				t.Fatal("invoice: got = none, want = the credit invoice")
			}
			if got := appliedCodes(h, ts[0].InvoiceID); !slices.Equal(got, tt.want) {
				t.Errorf("sections: got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestWalletCustomSections(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		// source: app/services/wallets/create_service.rb:88-90
		h, s := setup(t, t0)
		sec := section(h, "section_code_1")
		in := createInput(eurCustomer(h))
		in.InvoiceCustomSections = &wallets.CustomSections{Sections: []refs.Ref{refs.ByCode("section_code_1")}}
		w, err := s.Create(h.Ctx, h.OrgID(), in)
		if err != nil {
			t.Fatal(err)
		}
		wantSections(t, "wallet", h.ReloadWallet(w.ID).InvoiceCustomSections, sectionsOf(false, sec))
		// The initial top-up selects none: its invoice takes the wallet's
		// (create_service.rb:122-134).
		ts := transactions(h, w.ID)
		if len(ts) != 1 {
			t.Fatalf("transactions: got = %d, want = 1", len(ts))
		}
		wantSections(t, "top-up", ts[0].InvoiceCustomSections, wallet.CustomSections{})
		if got := appliedCodes(h, ts[0].InvoiceID); !slices.Equal(got, []string{"section_code_1"}) {
			t.Errorf("invoice sections: got = %v, want = [section_code_1]", got)
		}
	})
	tests := []struct {
		name, source string
		cur          func(sec *invoice.CustomSection) wallet.CustomSections
		in           *wallets.CustomSections
		want         func(sec *invoice.CustomSection) wallet.CustomSections
	}{
		{
			name: "skip", source: "spec/services/wallets/update_service_spec.rb:134",
			cur: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
			in:  &wallets.CustomSections{Skip: new(true)}, want: func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
		},
		{
			name: "select while skipping keeps skipping", source: "app/services/invoice_custom_sections/attach_to_resource_service.rb:58-59",
			cur:  func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
			in:   &wallets.CustomSections{Sections: []refs.Ref{refs.ByCode("section_code_1")}},
			want: func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
		},
		{
			name: "stop skipping and select", source: "app/services/invoice_custom_sections/attach_to_resource_service.rb:52-54",
			cur:  func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
			in:   &wallets.CustomSections{Skip: new(false), Sections: []refs.Ref{refs.ByCode("section_code_1")}},
			want: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
		},
		{
			name: "an empty list clears", source: "app/services/invoice_custom_sections/attach_to_resource_service.rb:80-84",
			cur:  func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
			in:   &wallets.CustomSections{Sections: []refs.Ref{}},
			want: func(*invoice.CustomSection) wallet.CustomSections { return wallet.CustomSections{} },
		},
		{
			name: "omitted keeps", source: "app/services/invoice_custom_sections/attach_to_resource_service.rb:15",
			cur:  func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
			want: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
		},
	}
	for _, tt := range tests {
		t.Run("update "+tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, s := setup(t, t0)
			sec := section(h, "section_code_1")
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.InvoiceCustomSections = tt.cur(sec) })
			if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{InvoiceCustomSections: tt.in}); err != nil {
				t.Fatal(err)
			}
			wantSections(t, "wallet", h.ReloadWallet(w.ID).InvoiceCustomSections, tt.want(sec))
		})
	}
}

func TestRuleCustomSections(t *testing.T) {
	t.Run("create skipping", func(t *testing.T) {
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:418
		h, s := setup(t, t0)
		in := ruleWallet(h, wallets.RuleInput{
			Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, Method: wallet.RecurringMethodTarget,
			TargetOngoingBalance: dec("100.0"), InvoiceCustomSections: &wallets.CustomSections{Skip: new(true)},
		})
		w, err := s.Create(h.Ctx, h.OrgID(), in)
		if err != nil {
			t.Fatal(err)
		}
		wantSections(t, "rule", rules(h, w.ID)[0].InvoiceCustomSections, sectionsOf(true))
	})
	t.Run("create with sections", func(t *testing.T) {
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:432
		h, s := setup(t, t0)
		s1, s2 := section(h, "section_code_1"), section(h, "section_code_2")
		in := ruleWallet(h, wallets.RuleInput{
			Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, Method: wallet.RecurringMethodTarget,
			TargetOngoingBalance:  dec("100.0"),
			InvoiceCustomSections: &wallets.CustomSections{Sections: []refs.Ref{refs.ByCode("section_code_1"), refs.ByCode("section_code_2")}},
		})
		w, err := s.Create(h.Ctx, h.OrgID(), in)
		if err != nil {
			t.Fatal(err)
		}
		wantSections(t, "rule", rules(h, w.ID)[0].InvoiceCustomSections, sectionsOf(false, s1, s2))
	})
	update := func(t *testing.T, terminate bool) {
		h, s := setup(t, t0)
		w := h.Wallet(h.Customer(), "10")
		old := addRule(h, w, func(r *wallet.RecurringTransactionRule) {
			if terminate {
				r.Status, r.TerminatedAt = wallet.RuleStatusTerminated, new(t0)
			}
		})
		in := wallets.RuleInput{
			ID: old.ID, Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly, PaidCredits: dec("105"), GrantedCredits: dec("105"),
			InvoiceCustomSections: &wallets.CustomSections{Skip: new(true)},
		}
		if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{RecurringTransactionRules: wallets.To([]wallets.RuleInput{in})}); err != nil {
			t.Fatal(err)
		}
		active := activeRules(h, w.ID)
		if len(active) != 1 {
			t.Fatalf("active rules: got = %d, want = 1", len(active))
		}
		wantSections(t, "rule", active[0].InvoiceCustomSections, sectionsOf(true))
	}
	// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:14
	t.Run("update", func(t *testing.T) { update(t, false) })
	// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:85
	t.Run("update an inactive rule", func(t *testing.T) { update(t, true) })
}

func TestTopUpCustomSections(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2 Jan 2006", s)
		if err != nil {
			panic(err)
		}
		return d
	}
	run := map[string]func(t *testing.T, h *billingtest.Harness, s *wallets.Service, rule func(*wallet.RecurringTransactionRule)) []*wallet.Transaction{
		"threshold": func(t *testing.T, h *billingtest.Harness, s *wallets.Service, rule func(*wallet.RecurringTransactionRule)) []*wallet.Transaction {
			w := thresholdWallet(h)
			addRule(h, w, threshold("6", "10", "3"), rule)
			ts, err := s.ThresholdTopUp(h.Ctx, h.OrgID(), w.ID)
			if err != nil {
				t.Fatal(err)
			}
			return ts
		},
		"interval": func(t *testing.T, h *billingtest.Harness, s *wallets.Service, rule func(*wallet.RecurringTransactionRule)) []*wallet.Transaction {
			w := h.Wallet(h.Customer(), "0", func(w *wallet.Wallet) { w.CreatedAt = day("20 Feb 2021") })
			addRule(h, w, func(r *wallet.RecurringTransactionRule) { r.Interval = period.Weekly }, rule)
			ts, err := s.RunIntervalTopUps(h.Ctx, h.OrgID(), h.Now())
			if err != nil {
				t.Fatal(err)
			}
			return ts
		},
	}
	tests := []struct {
		name, trigger, source string
		rule                  func(sec *invoice.CustomSection) wallet.CustomSections
		deleted               bool
		want                  func(sec *invoice.CustomSection) wallet.CustomSections
	}{
		{
			name: "the rule's sections", trigger: "threshold", source: "spec/services/wallets/threshold_top_up_service_spec.rb:138",
			rule: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
			want: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
		},
		{
			name: "the rule skips", trigger: "threshold", source: "spec/services/wallets/threshold_top_up_service_spec.rb:153",
			rule: func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
			want: func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
		},
		{
			name: "the rule's sections", trigger: "interval", source: "spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:549",
			rule: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
			want: func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
		},
		{
			name: "the rule skips", trigger: "interval", source: "spec/services/wallets/create_interval_wallet_transactions_service_spec.rb:567",
			rule: func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
			want: func(*invoice.CustomSection) wallet.CustomSections { return sectionsOf(true) },
		},
		{
			name: "no opinion", trigger: "interval", source: "app/models/recurring_transaction_rule.rb:88-93",
			rule: func(*invoice.CustomSection) wallet.CustomSections { return wallet.CustomSections{} },
			want: func(*invoice.CustomSection) wallet.CustomSections { return wallet.CustomSections{} },
		},
		{
			name: "a deleted section is not copied", trigger: "threshold", source: "app/models/recurring_transaction_rule.rb:91 (kept sections only)",
			rule:    func(sec *invoice.CustomSection) wallet.CustomSections { return sectionsOf(false, sec) },
			deleted: true,
			want:    func(*invoice.CustomSection) wallet.CustomSections { return wallet.CustomSections{} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.trigger+" "+tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			now := t0
			if tt.trigger == "interval" {
				now = day("18 Jun 2022")
			}
			h, s := setup(t, now)
			sec := section(h, "section_code_1")
			if tt.deleted {
				deleteSection(h, sec)
			}
			ts := run[tt.trigger](t, h, s, func(r *wallet.RecurringTransactionRule) { r.InvoiceCustomSections = tt.rule(sec) })
			if len(ts) == 0 {
				t.Fatal("top-up: got = none, want = transactions")
			}
			for _, tr := range ts {
				wantSections(t, string(tr.TransactionStatus), transaction(h, tr.ID).InvoiceCustomSections, tt.want(sec))
			}
		})
	}
}
