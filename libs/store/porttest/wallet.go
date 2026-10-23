package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var walletEUR = money.MustCurrency("EUR")

// walletFixtureCustomer creates a customer of the billing entity's
// organization.
func walletFixtureCustomer(e *env, be *organization.BillingEntity, externalID string) core.CustomerID {
	e.t.Helper()
	c := customer.New(be.OrganizationID, core.NewID[core.CustomerID](e.ids), externalID, be.ID, now)
	c.Currency = walletEUR
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Customers.Create(ctx, c) })
	return c.ID
}

// walletFixtureMetric creates a count billable metric.
func walletFixtureMetric(e *env, org core.OrganizationID, code string) core.BillableMetricID {
	e.t.Helper()
	m := &catalog.BillableMetric{
		ID: core.NewID[core.BillableMetricID](e.ids), OrganizationID: org, Code: code, Name: "Metric " + code,
		AggregationType: catalog.AggregationTypeCount, CreatedAt: now, UpdatedAt: now,
	}
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Metrics.Create(ctx, m) })
	return m.ID
}

// walletFixtureInvoice creates a finalized credit invoice of the customer.
func walletFixtureInvoice(e *env, be *organization.BillingEntity, cust core.CustomerID, number string) core.InvoiceID {
	e.t.Helper()
	d := core.NewDate(2025, time.March, 14)
	inv := &invoice.Invoice{
		ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: be.OrganizationID, CustomerID: cust, BillingEntityID: be.ID,
		Type: invoice.TypeCredit, Status: invoice.StatusFinalized, PaymentStatus: core.PaymentStatusPending,
		TaxStatus: invoice.TaxStatusSucceeded, Number: number, Currency: walletEUR,
		IssuingDate: d, PaymentDueDate: d, ExpectedFinalizationDate: d, FinalizedAt: ptr(now),
		CreatedAt: now, UpdatedAt: now,
	}
	e.tx(func(ctx context.Context, r port.Repos) error { return r.Invoices.Create(ctx, inv) })
	return inv.ID
}

// walletFixtureCreditNote creates a finalized credit note of the invoice.
func walletFixtureCreditNote(e *env, org core.OrganizationID, inv core.InvoiceID, cust core.CustomerID) core.CreditNoteID {
	e.t.Helper()
	cn := &creditnote.CreditNote{
		ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: org, InvoiceID: inv, CustomerID: cust,
		Number: "CN-0001", SequentialID: 1, Status: creditnote.StatusFinalized, CreditStatus: creditnote.CreditStatusAvailable,
		RefundStatus: core.RefundStatusPending, Reason: creditnote.ReasonOrderChange, Currency: walletEUR,
		IssuingDate: core.NewDate(2025, time.March, 14), CreatedAt: now, UpdatedAt: now,
	}
	e.tx(func(ctx context.Context, r port.Repos) error { return r.CreditNotes.Create(ctx, cn) })
	return cn.ID
}

// walletNew returns an active EUR wallet of the customer at one euro per
// credit.
func walletNew(e *env, org core.OrganizationID, cust core.CustomerID) *wallet.Wallet {
	return wallet.New(org, core.NewID[core.WalletID](e.ids), cust, walletEUR, money.MustParse("1"), now)
}

// walletCreate creates the wallets in one transaction.
func walletCreate(e *env, ws ...*wallet.Wallet) {
	e.t.Helper()
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, w := range ws {
			if err := r.Wallets.Create(ctx, w); err != nil {
				return err
			}
		}
		return nil
	})
}

func walletID(w *wallet.Wallet) core.WalletID { return w.ID }

func walletTransactionID(t *wallet.Transaction) core.WalletTransactionID { return t.ID }

func walletRuleID(x *wallet.RecurringTransactionRule) core.RecurringTransactionRuleID { return x.ID }

func walletConsumptionID(c *wallet.Consumption) core.WalletTransactionConsumptionID { return c.ID }

func testWallet(t *testing.T, h Harness) {
	t.Run("wallets", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		c1 := walletFixtureCustomer(e, a, "c1")
		c2 := walletFixtureCustomer(e, a, "c2")
		m1 := walletFixtureMetric(e, org, "m1")
		m2 := walletFixtureMetric(e, org, "m2")

		w1 := walletNew(e, org, c1)
		w1.Code = ptr("main")
		w1.Name = ptr("Main wallet")
		w1.RateAmount = money.MustParse("0.001")
		w1.Priority = 7
		w1.ExpirationAt = ptr(at(72 * time.Hour))
		w1.BalanceAmount, w1.CreditsBalance = 1030, money.MustParse("1030.12345")
		w1.OngoingBalanceAmount, w1.CreditsOngoingBalance = 900, money.MustParse("900.5")
		w1.OngoingUsageBalanceAmount, w1.CreditsOngoingUsageBalance = 130, money.MustParse("129.62345")
		w1.ConsumedAmount, w1.ConsumedCredits = 50, money.MustParse("50")
		w1.PaidTopUpMinAmount, w1.PaidTopUpMaxAmount = ptr(int64(100)), ptr(int64(100000))
		w1.InvoiceRequiresSuccessfulPayment = true
		w1.AllowedFeeTypes = []core.FeeType{core.FeeTypeCharge, core.FeeTypeSubscription}
		w1.BillableMetricIDs = []core.BillableMetricID{m2, m1}
		w1.Traceable = true
		w1.BillingEntityID = a.ID
		w1.PaymentMethodType = core.PaymentMethodTypeManual
		w1.LastBalanceSyncAt = ptr(at(time.Minute))
		w1.LastConsumedCreditAt = ptr(at(2 * time.Minute))
		w1.DepletedOngoingBalance = true
		w2 := walletNew(e, org, c2)
		w2.Code = ptr("main")
		w2.PaymentMethodType = ""
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.Wallets.Create(ctx, w1))
			noErr(t, "Create() with the code of another customer's wallet", r.Wallets.Create(ctx, w2))
			wantErr(t, "Create() with the same ID", r.Wallets.Create(ctx, w1), core.ErrConflict)
			dup := walletNew(e, org, c1)
			dup.Code = ptr("main")
			wantErr(t, "Create() with a taken code", r.Wallets.Create(ctx, dup), core.ErrConflict)

			got, err := r.Wallets.Get(ctx, org, w1.ID)
			noErr(t, "Get() after conflicts", err)
			equal(t, "Get()", got, w1)
			got, err = r.Wallets.Get(ctx, org, w2.ID)
			noErr(t, "Get() minimal", err)
			equal(t, "Get() minimal", got, w2)
			got, err = r.Wallets.GetByCode(ctx, org, c1, "main")
			noErr(t, "GetByCode()", err)
			equal(t, "GetByCode()", got, w1)
			_, err = r.Wallets.GetByCode(ctx, org, c1, "nope")
			wantErr(t, "GetByCode() unknown", err, core.ErrNotFound)
			_, err = r.Wallets.Get(ctx, org, core.NewID[core.WalletID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Update clears optional fields and reorders the metrics.
			w2.Name = ptr("Second")
			w2.BillableMetricIDs = []core.BillableMetricID{m1}
			w2.UpdatedAt = at(time.Hour)
			noErr(t, "Update()", r.Wallets.Update(ctx, w2))
			w1.ExpirationAt, w1.PaidTopUpMinAmount, w1.BillingEntityID = nil, nil, core.BillingEntityID{}
			w1.BillableMetricIDs = []core.BillableMetricID{m1, m2}
			w1.AllowedFeeTypes = nil
			noErr(t, "Update() clearing fields", r.Wallets.Update(ctx, w1))
			got, err = r.Wallets.Get(ctx, org, w1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, w1)
			got, err = r.Wallets.Get(ctx, org, w2.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, w2)

			// A terminated wallet frees its code.
			dup.Code = ptr("other")
			noErr(t, "Create() with another code", r.Wallets.Create(ctx, dup))
			dup.Code = ptr("main")
			wantErr(t, "Update() to a taken code", r.Wallets.Update(ctx, dup), core.ErrConflict)
			noErr(t, "Terminate()", w1.Terminate(at(2*time.Hour)))
			noErr(t, "Update() terminated", r.Wallets.Update(ctx, w1))
			_, err = r.Wallets.GetByCode(ctx, org, c1, "main")
			wantErr(t, "GetByCode() terminated", err, core.ErrNotFound)
			noErr(t, "Update() to a freed code", r.Wallets.Update(ctx, dup))
			got, err = r.Wallets.GetByCode(ctx, org, c1, "main")
			noErr(t, "GetByCode() new holder", err)
			equal(t, "GetByCode() new holder", got.ID, dup.ID)
			// A terminated wallet may keep a code an active wallet uses.
			noErr(t, "Update() terminated with a taken code", r.Wallets.Update(ctx, w1))

			// Isolation.
			_, err = r.Wallets.Get(ctx, other, w1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Wallets.GetByCode(ctx, other, c2, "main")
			wantErr(t, "GetByCode() from another organization", err, core.ErrNotFound)
			moved := *w2
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.Wallets.Update(ctx, &moved), core.ErrNotFound)
			p, err := r.Wallets.List(ctx, other, wallet.Filter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(p.Items), 0)
			n, err := r.Wallets.CountActive(ctx, other, c1)
			noErr(t, "CountActive() of another organization", err)
			equal(t, "CountActive() of another organization", n, 0)
			wantErr(t, "Update() unknown", r.Wallets.Update(ctx, walletNew(e, org, c1)), core.ErrNotFound)

			// Listing.
			all := listAll(t, 1, func(p core.PageRequest) (core.Page[*wallet.Wallet], error) {
				return r.Wallets.List(ctx, org, wallet.Filter{}, p)
			})
			equal(t, "List()", all, []*wallet.Wallet{w1, w2, dup})
			list := listAll(t, 2, func(p core.PageRequest) (core.Page[*wallet.Wallet], error) {
				return r.Wallets.List(ctx, org, wallet.Filter{CustomerID: c1}, p)
			})
			equal(t, "List() of a customer", ids(list, walletID), []core.WalletID{w1.ID, dup.ID})
			list = listAll(t, 2, func(p core.PageRequest) (core.Page[*wallet.Wallet], error) {
				return r.Wallets.List(ctx, org, wallet.Filter{Status: wallet.StatusActive}, p)
			})
			equal(t, "List() active", ids(list, walletID), []core.WalletID{w2.ID, dup.ID})
			list = listAll(t, 2, func(p core.PageRequest) (core.Page[*wallet.Wallet], error) {
				return r.Wallets.List(ctx, org, wallet.Filter{CustomerID: c1, Status: wallet.StatusTerminated}, p)
			})
			equal(t, "List() terminated of a customer", ids(list, walletID), []core.WalletID{w1.ID})
			n, err = r.Wallets.CountActive(ctx, org, c1)
			noErr(t, "CountActive()", err)
			equal(t, "CountActive()", n, 1)
			return nil
		})
	})

	t.Run("wallets referencing missing rows", func(t *testing.T) {
		t.Parallel()
		if h.Name == "memstore" {
			t.Skip("memstore does not check references")
		}
		e := newEnv(t, h)
		_, a := e.org("Acme")
		w := walletNew(e, a.OrganizationID, core.NewID[core.CustomerID](e.ids))
		err := e.txErr(func(ctx context.Context, r port.Repos) error { return r.Wallets.Create(ctx, w) })
		wantErr(t, "Create() with an unknown customer", err, core.ErrNotFound)
		w = walletNew(e, a.OrganizationID, walletFixtureCustomer(e, a, "c"))
		w.BillableMetricIDs = []core.BillableMetricID{core.NewID[core.BillableMetricID](e.ids)}
		err = e.txErr(func(ctx context.Context, r port.Repos) error { return r.Wallets.Create(ctx, w) })
		wantErr(t, "Create() with an unknown billable metric", err, core.ErrNotFound)
	})

	t.Run("wallet application order and expiration", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org := a.OrganizationID
		c := walletFixtureCustomer(e, a, "c")
		cb := walletFixtureCustomer(e, b, "cb")
		mk := func(priority int, exp *time.Time, created time.Time) *wallet.Wallet {
			w := walletNew(e, org, c)
			w.Priority, w.ExpirationAt, w.CreatedAt, w.UpdatedAt = priority, exp, created, created
			return w
		}
		// Created in ID order; creation instants and expirations vary.
		noExpLate := mk(10, nil, at(time.Second))
		late := mk(10, ptr(at(48*time.Hour)), now)
		first := mk(5, nil, at(time.Hour))
		soon := mk(10, ptr(at(24*time.Hour)), now)
		noExpEarly := mk(10, nil, now)
		terminated := mk(1, ptr(at(time.Hour)), now)
		noErr(t, "Terminate()", terminated.Terminate(now))
		expiredOther := walletNew(e, b.OrganizationID, cb)
		expiredOther.ExpirationAt = ptr(now)
		walletCreate(e, noExpLate, late, first, soon, noExpEarly, terminated, expiredOther)
		e.tx(func(ctx context.Context, r port.Repos) error {
			got, err := r.Wallets.ListActiveForApplication(ctx, org, c)
			noErr(t, "ListActiveForApplication()", err)
			equal(t, "ListActiveForApplication()", ids(got, walletID),
				[]core.WalletID{first.ID, soon.ID, late.ID, noExpEarly.ID, noExpLate.ID})
			got, err = r.Wallets.ListActiveForApplication(ctx, b.OrganizationID, c)
			noErr(t, "ListActiveForApplication() of another organization", err)
			equal(t, "ListActiveForApplication() of another organization", len(got), 0)
			n, err := r.Wallets.CountActive(ctx, org, c)
			noErr(t, "CountActive()", err)
			equal(t, "CountActive()", n, 5)

			got, err = r.Wallets.ListExpired(ctx, org, at(24*time.Hour))
			noErr(t, "ListExpired() at the expiration", err)
			equal(t, "ListExpired() at the expiration", got, []*wallet.Wallet{soon})
			got, err = r.Wallets.ListExpired(ctx, org, at(24*time.Hour-time.Microsecond))
			noErr(t, "ListExpired() before", err)
			equal(t, "ListExpired() before", len(got), 0)
			got, err = r.Wallets.ListExpired(ctx, org, at(100*time.Hour))
			noErr(t, "ListExpired() later", err)
			equal(t, "ListExpired() later", ids(got, walletID), []core.WalletID{late.ID, soon.ID})
			got, err = r.Wallets.ListExpired(ctx, b.OrganizationID, now)
			noErr(t, "ListExpired() of another organization", err)
			equal(t, "ListExpired() of another organization", ids(got, walletID), []core.WalletID{expiredOther.ID})
			return nil
		})
	})

	t.Run("wallet transactions", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		c := walletFixtureCustomer(e, a, "c")
		inv := walletFixtureInvoice(e, a, c, "INV-0001")
		inv2 := walletFixtureInvoice(e, a, c, "INV-0002")
		cn := walletFixtureCreditNote(e, org, inv, c)
		w := walletNew(e, org, c)
		w.Traceable = true
		w2 := walletNew(e, org, c)
		walletCreate(e, w, w2)
		rule := &wallet.RecurringTransactionRule{
			ID: core.NewID[core.RecurringTransactionRuleID](e.ids), OrganizationID: org, WalletID: w.ID,
			Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: ptr(money.MustParse("10")),
			Method: wallet.RecurringMethodFixed, PaidCredits: money.MustParse("20"), Status: wallet.RuleStatusActive,
			CreatedAt: now, UpdatedAt: now,
		}
		e.tx(func(ctx context.Context, r port.Repos) error { return r.RecurringTransactionRules.Create(ctx, rule) })

		tx := func(wid core.WalletID, typ wallet.TransactionType, st wallet.TransactionStatus, priority int, created time.Time) *wallet.Transaction {
			return &wallet.Transaction{
				ID: core.NewID[core.WalletTransactionID](e.ids), OrganizationID: org, WalletID: wid,
				TransactionType: typ, TransactionStatus: st, Status: wallet.InitialStatus(st), Source: wallet.TransactionSourceManual,
				Currency: walletEUR, Amount: 1000, CreditAmount: money.MustParse("1000"), Priority: priority,
				CreatedAt: created, UpdatedAt: created,
			}
		}
		// A purchase with every field set.
		full := tx(w.ID, wallet.TransactionTypeInbound, wallet.TransactionStatusPurchased, 50, now)
		full.Source = wallet.TransactionSourceThreshold
		full.Amount, full.CreditAmount, full.RemainingAmount = 1235, money.MustParse("1234.56789"), ptr(int64(1235))
		full.Name = ptr("Top-up")
		full.InvoiceID = inv
		full.CreditNoteID = cn
		full.RecurringTransactionRuleID = rule.ID
		full.Metadata = core.Metadata{{Key: "po", Value: "42", DisplayInInvoice: true}, {Key: "k", Value: "v"}}
		full.PaymentMethodType = core.PaymentMethodTypeProvider
		full.InvoiceRequiresSuccessfulPayment = true
		// Inbound transactions with value left, in their consumption order.
		g1 := tx(w.ID, wallet.TransactionTypeInbound, wallet.TransactionStatusGranted, 50, at(time.Hour))
		g1.RemainingAmount = ptr(int64(10))
		p1 := tx(w.ID, wallet.TransactionTypeInbound, wallet.TransactionStatusPurchased, 1, at(2*time.Hour))
		p1.Status, p1.SettledAt, p1.RemainingAmount = wallet.SettlementStatusSettled, ptr(at(2*time.Hour)), ptr(int64(5))
		g0 := tx(w.ID, wallet.TransactionTypeInbound, wallet.TransactionStatusGranted, 50, now)
		g0.RemainingAmount = ptr(int64(1))
		g2 := tx(w.ID, wallet.TransactionTypeInbound, wallet.TransactionStatusGranted, 50, at(time.Hour))
		g2.RemainingAmount = ptr(int64(3))
		p2 := tx(w.ID, wallet.TransactionTypeInbound, wallet.TransactionStatusPurchased, 50, now)
		p2.Status, p2.RemainingAmount = wallet.SettlementStatusSettled, ptr(int64(100))
		used := tx(w.ID, wallet.TransactionTypeInbound, wallet.TransactionStatusGranted, 1, now)
		used.RemainingAmount = ptr(int64(0))
		untraced := tx(w2.ID, wallet.TransactionTypeInbound, wallet.TransactionStatusGranted, 1, now)
		out := tx(w.ID, wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced, 50, at(3*time.Hour))
		out.InvoiceID = inv2
		void := tx(w2.ID, wallet.TransactionTypeOutbound, wallet.TransactionStatusVoided, 50, at(3*time.Hour))
		void.InvoiceID = inv
		void.CreditNoteID = cn
		all := []*wallet.Transaction{full, g1, p1, g0, g2, p2, used, untraced, out, void}
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range all {
				noErr(t, "Create()", r.WalletTransactions.Create(ctx, x))
			}
			wantErr(t, "Create() with the same ID", r.WalletTransactions.Create(ctx, full), core.ErrConflict)
			got, err := r.WalletTransactions.Get(ctx, org, full.ID)
			noErr(t, "Get() after a conflict", err)
			equal(t, "Get()", got, full)
			got, err = r.WalletTransactions.Get(ctx, org, out.ID)
			noErr(t, "Get() minimal", err)
			equal(t, "Get() minimal", got, out)
			_, err = r.WalletTransactions.Get(ctx, org, core.NewID[core.WalletTransactionID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// The pending purchase of the rule.
			got, err = r.WalletTransactions.GetPendingPurchase(ctx, org, rule.ID)
			noErr(t, "GetPendingPurchase()", err)
			equal(t, "GetPendingPurchase()", got, full)
			_, err = r.WalletTransactions.GetPendingPurchase(ctx, other, rule.ID)
			wantErr(t, "GetPendingPurchase() of another organization", err, core.ErrNotFound)

			// Settling it; the update clears optional fields.
			noErr(t, "Settle()", full.Settle(at(time.Minute)))
			full.Name, full.CreditNoteID, full.Metadata, full.PaymentMethodType = nil, core.CreditNoteID{}, nil, ""
			noErr(t, "Update()", r.WalletTransactions.Update(ctx, full))
			got, err = r.WalletTransactions.Get(ctx, org, full.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, full)
			_, err = r.WalletTransactions.GetPendingPurchase(ctx, org, rule.ID)
			wantErr(t, "GetPendingPurchase() once settled", err, core.ErrNotFound)

			// Isolation.
			_, err = r.WalletTransactions.Get(ctx, other, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			moved := *out
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.WalletTransactions.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Update() unknown", r.WalletTransactions.Update(ctx, tx(w.ID, wallet.TransactionTypeInbound, wallet.TransactionStatusGranted, 1, now)), core.ErrNotFound)
			p, err := r.WalletTransactions.List(ctx, other, wallet.TransactionFilter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(p.Items), 0)
			byInv, err := r.WalletTransactions.ListByInvoice(ctx, other, inv)
			noErr(t, "ListByInvoice() of another organization", err)
			equal(t, "ListByInvoice() of another organization", len(byInv), 0)
			avail, err := r.WalletTransactions.ListAvailableInbound(ctx, other, w.ID)
			noErr(t, "ListAvailableInbound() of another organization", err)
			equal(t, "ListAvailableInbound() of another organization", len(avail), 0)

			// Listing and filters.
			list := listAll(t, 3, func(p core.PageRequest) (core.Page[*wallet.Transaction], error) {
				return r.WalletTransactions.List(ctx, org, wallet.TransactionFilter{}, p)
			})
			equal(t, "List()", list, all)
			for _, c := range []struct {
				name string
				f    wallet.TransactionFilter
				want []*wallet.Transaction
			}{
				{"wallet", wallet.TransactionFilter{WalletID: w2.ID}, []*wallet.Transaction{untraced, void}},
				{"invoice", wallet.TransactionFilter{InvoiceID: inv}, []*wallet.Transaction{full, void}},
				{"type", wallet.TransactionFilter{TransactionType: wallet.TransactionTypeOutbound}, []*wallet.Transaction{out, void}},
				{"kind", wallet.TransactionFilter{TransactionStatus: wallet.TransactionStatusPurchased}, []*wallet.Transaction{full, p1, p2}},
				{"settlement", wallet.TransactionFilter{Status: wallet.SettlementStatusSettled, WalletID: w2.ID}, []*wallet.Transaction{untraced, void}},
				{"all filters", wallet.TransactionFilter{
					WalletID: w.ID, InvoiceID: inv2, TransactionType: wallet.TransactionTypeOutbound,
					TransactionStatus: wallet.TransactionStatusInvoiced, Status: wallet.SettlementStatusSettled,
				}, []*wallet.Transaction{out}},
				{"none", wallet.TransactionFilter{Status: wallet.SettlementStatusFailed}, nil},
			} {
				got := listAll(t, 1, func(p core.PageRequest) (core.Page[*wallet.Transaction], error) {
					return r.WalletTransactions.List(ctx, org, c.f, p)
				})
				equal(t, "List() by "+c.name, ids(got, walletTransactionID), ids(c.want, walletTransactionID))
			}
			byInv, err = r.WalletTransactions.ListByInvoice(ctx, org, inv)
			noErr(t, "ListByInvoice()", err)
			equal(t, "ListByInvoice()", byInv, []*wallet.Transaction{full, void})

			// Consumption order: priority, granted before purchased, oldest,
			// then ID.
			avail, err = r.WalletTransactions.ListAvailableInbound(ctx, org, w.ID)
			noErr(t, "ListAvailableInbound()", err)
			equal(t, "ListAvailableInbound()", ids(avail, walletTransactionID),
				[]core.WalletTransactionID{p1.ID, g0.ID, g1.ID, g2.ID, full.ID, p2.ID})

			// Consumptions.
			cons := func(in, outbound *wallet.Transaction, amount int64) *wallet.Consumption {
				return &wallet.Consumption{
					ID: core.NewID[core.WalletTransactionConsumptionID](e.ids), OrganizationID: org,
					InboundTransactionID: in.ID, OutboundTransactionID: outbound.ID, ConsumedAmount: amount,
					CreatedAt: at(3 * time.Hour), UpdatedAt: at(3 * time.Hour),
				}
			}
			out2 := tx(w.ID, wallet.TransactionTypeOutbound, wallet.TransactionStatusInvoiced, 50, at(4*time.Hour))
			noErr(t, "Create() second outbound", r.WalletTransactions.Create(ctx, out2))
			k1, k2, k3 := cons(p1, out, 5), cons(g0, out, 1), cons(g1, out2, 4)
			for _, k := range []*wallet.Consumption{k1, k2, k3} {
				noErr(t, "CreateConsumption()", r.WalletTransactions.CreateConsumption(ctx, k))
			}
			wantErr(t, "CreateConsumption() with the same ID", r.WalletTransactions.CreateConsumption(ctx, k1), core.ErrConflict)
			wantErr(t, "CreateConsumption() of a linked pair", r.WalletTransactions.CreateConsumption(ctx, cons(p1, out, 1)), core.ErrConflict)
			k4 := cons(p1, out2, 2)
			noErr(t, "CreateConsumption() after conflicts", r.WalletTransactions.CreateConsumption(ctx, k4))
			got2, err := r.WalletTransactions.ListConsumptionsByOutbound(ctx, org, out.ID)
			noErr(t, "ListConsumptionsByOutbound()", err)
			equal(t, "ListConsumptionsByOutbound()", got2, []*wallet.Consumption{k1, k2})
			got2, err = r.WalletTransactions.ListConsumptionsByInbound(ctx, org, p1.ID)
			noErr(t, "ListConsumptionsByInbound()", err)
			equal(t, "ListConsumptionsByInbound()", ids(got2, walletConsumptionID), []core.WalletTransactionConsumptionID{k1.ID, k4.ID})
			got2, err = r.WalletTransactions.ListConsumptionsByOutbound(ctx, other, out.ID)
			noErr(t, "ListConsumptionsByOutbound() of another organization", err)
			equal(t, "ListConsumptionsByOutbound() of another organization", len(got2), 0)
			got2, err = r.WalletTransactions.ListConsumptionsByInbound(ctx, other, p1.ID)
			noErr(t, "ListConsumptionsByInbound() of another organization", err)
			equal(t, "ListConsumptionsByInbound() of another organization", len(got2), 0)
			return nil
		})
	})

	t.Run("recurring transaction rules", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		c := walletFixtureCustomer(e, a, "c")
		w1, w2 := walletNew(e, org, c), walletNew(e, org, c)
		walletCreate(e, w1, w2)
		rule := func(w *wallet.Wallet, trigger wallet.RecurringTrigger) *wallet.RecurringTransactionRule {
			x := &wallet.RecurringTransactionRule{
				ID: core.NewID[core.RecurringTransactionRuleID](e.ids), OrganizationID: org, WalletID: w.ID,
				Trigger: trigger, Method: wallet.RecurringMethodFixed, PaidCredits: money.MustParse("10"),
				Status: wallet.RuleStatusActive, CreatedAt: now, UpdatedAt: now,
			}
			if trigger == wallet.RecurringTriggerInterval {
				x.Interval = period.Monthly
			} else {
				x.ThresholdCredits = ptr(money.MustParse("5"))
			}
			return x
		}
		// An interval rule with every field set.
		full := rule(w1, wallet.RecurringTriggerInterval)
		full.Interval = period.Quarterly
		full.Method = wallet.RecurringMethodTarget
		full.PaidCredits, full.GrantedCredits = money.MustParse("1.5"), money.MustParse("2.25")
		full.TargetOngoingBalance = ptr(money.MustParse("100.12345"))
		full.GrantsTargetTopUp = true
		full.StartedAt = ptr(at(time.Hour))
		full.ExpirationAt = ptr(at(48 * time.Hour))
		full.InvoiceRequiresSuccessfulPayment = true
		full.TransactionMetadata = core.Metadata{{Key: "source", Value: "auto"}}
		full.TransactionName = ptr("Monthly top-up")
		full.IgnorePaidTopUpLimits = true
		full.PaymentMethodType = core.PaymentMethodTypeManual
		threshold := rule(w1, wallet.RecurringTriggerThreshold)
		expiredInterval := rule(w2, wallet.RecurringTriggerInterval)
		expiredInterval.ExpirationAt = ptr(at(24 * time.Hour))
		terminated := rule(w2, wallet.RecurringTriggerInterval)
		noErr(t, "Terminate()", terminated.Terminate(now))
		otherThreshold := rule(w2, wallet.RecurringTriggerThreshold)
		otherThreshold.ExpirationAt = ptr(at(100 * time.Hour))
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range []*wallet.RecurringTransactionRule{full, threshold, expiredInterval, terminated, otherThreshold} {
				noErr(t, "Create()", r.RecurringTransactionRules.Create(ctx, x))
			}
			wantErr(t, "Create() with the same ID", r.RecurringTransactionRules.Create(ctx, full), core.ErrConflict)
			got, err := r.RecurringTransactionRules.Get(ctx, org, full.ID)
			noErr(t, "Get() after a conflict", err)
			equal(t, "Get()", got, full)
			got, err = r.RecurringTransactionRules.Get(ctx, org, threshold.ID)
			noErr(t, "Get() threshold", err)
			equal(t, "Get() threshold", got, threshold)
			_, err = r.RecurringTransactionRules.Get(ctx, org, core.NewID[core.RecurringTransactionRuleID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Update clears optional fields.
			threshold.ThresholdCredits = ptr(money.MustParse("7.5"))
			threshold.TransactionName = ptr("Refill")
			threshold.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.RecurringTransactionRules.Update(ctx, threshold))
			got, err = r.RecurringTransactionRules.Get(ctx, org, threshold.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, threshold)

			// Isolation.
			_, err = r.RecurringTransactionRules.Get(ctx, other, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			moved := *full
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.RecurringTransactionRules.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Delete() through another organization", r.RecurringTransactionRules.Delete(ctx, other, full.ID), core.ErrNotFound)
			wantErr(t, "Update() unknown", r.RecurringTransactionRules.Update(ctx, rule(w1, wallet.RecurringTriggerThreshold)), core.ErrNotFound)
			for _, list := range []func() ([]*wallet.RecurringTransactionRule, error){
				func() ([]*wallet.RecurringTransactionRule, error) {
					return r.RecurringTransactionRules.ListByWallet(ctx, other, w1.ID)
				},
				func() ([]*wallet.RecurringTransactionRule, error) {
					return r.RecurringTransactionRules.ListActiveInterval(ctx, other, now)
				},
				func() ([]*wallet.RecurringTransactionRule, error) {
					return r.RecurringTransactionRules.ListActiveThreshold(ctx, other, w1.ID, now)
				},
				func() ([]*wallet.RecurringTransactionRule, error) {
					return r.RecurringTransactionRules.ListEligibleForTermination(ctx, other, at(1000*time.Hour))
				},
			} {
				got, err := list()
				noErr(t, "list of another organization", err)
				equal(t, "list of another organization", len(got), 0)
			}

			// Listing.
			list, err := r.RecurringTransactionRules.ListByWallet(ctx, org, w1.ID)
			noErr(t, "ListByWallet()", err)
			equal(t, "ListByWallet()", list, []*wallet.RecurringTransactionRule{full, threshold})
			list, err = r.RecurringTransactionRules.ListActiveInterval(ctx, org, at(time.Hour))
			noErr(t, "ListActiveInterval()", err)
			equal(t, "ListActiveInterval()", ids(list, walletRuleID), []core.RecurringTransactionRuleID{full.ID, expiredInterval.ID})
			list, err = r.RecurringTransactionRules.ListActiveInterval(ctx, org, at(24*time.Hour))
			noErr(t, "ListActiveInterval() at an expiration", err)
			equal(t, "ListActiveInterval() at an expiration", ids(list, walletRuleID), []core.RecurringTransactionRuleID{full.ID})
			list, err = r.RecurringTransactionRules.ListActiveThreshold(ctx, org, w2.ID, now)
			noErr(t, "ListActiveThreshold()", err)
			equal(t, "ListActiveThreshold()", ids(list, walletRuleID), []core.RecurringTransactionRuleID{otherThreshold.ID})
			list, err = r.RecurringTransactionRules.ListActiveThreshold(ctx, org, w2.ID, at(100*time.Hour))
			noErr(t, "ListActiveThreshold() expired", err)
			equal(t, "ListActiveThreshold() expired", len(list), 0)
			list, err = r.RecurringTransactionRules.ListEligibleForTermination(ctx, org, at(24*time.Hour-time.Microsecond))
			noErr(t, "ListEligibleForTermination() before", err)
			equal(t, "ListEligibleForTermination() before", len(list), 0)
			list, err = r.RecurringTransactionRules.ListEligibleForTermination(ctx, org, at(48*time.Hour))
			noErr(t, "ListEligibleForTermination()", err)
			equal(t, "ListEligibleForTermination()", ids(list, walletRuleID), []core.RecurringTransactionRuleID{full.ID, expiredInterval.ID})

			// Deleting a rule keeps the transactions it created.
			created := &wallet.Transaction{
				ID: core.NewID[core.WalletTransactionID](e.ids), OrganizationID: org, WalletID: w1.ID,
				TransactionType: wallet.TransactionTypeInbound, TransactionStatus: wallet.TransactionStatusGranted,
				Status: wallet.SettlementStatusSettled, Source: wallet.TransactionSourceInterval, Currency: walletEUR,
				Amount: 10, CreditAmount: money.MustParse("10"), Priority: 50, RecurringTransactionRuleID: full.ID,
				CreatedAt: now, UpdatedAt: now,
			}
			noErr(t, "Create() transaction of the rule", r.WalletTransactions.Create(ctx, created))
			noErr(t, "Delete()", r.RecurringTransactionRules.Delete(ctx, org, full.ID))
			_, err = r.RecurringTransactionRules.Get(ctx, org, full.ID)
			wantErr(t, "Get() deleted", err, core.ErrNotFound)
			wantErr(t, "Delete() again", r.RecurringTransactionRules.Delete(ctx, org, full.ID), core.ErrNotFound)
			kept, err := r.WalletTransactions.Get(ctx, org, created.ID)
			noErr(t, "Get() transaction of a deleted rule", err)
			equal(t, "Get() transaction of a deleted rule", kept, created)
			list, err = r.RecurringTransactionRules.ListByWallet(ctx, org, w1.ID)
			noErr(t, "ListByWallet() after Delete()", err)
			equal(t, "ListByWallet() after Delete()", ids(list, walletRuleID), []core.RecurringTransactionRuleID{threshold.ID})
			return nil
		})
	})
}
