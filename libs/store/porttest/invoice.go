package porttest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var invoiceEUR = money.MustCurrency("EUR")

// invoiceParents are the rows of other aggregates the invoice tests point
// at, all in one organization.
type invoiceParents struct {
	org           core.OrganizationID
	be            core.BillingEntityID
	be2           core.BillingEntityID
	customers     [2]core.CustomerID
	subscriptions [2]core.SubscriptionID
	walletTx      core.WalletTransactionID
	method        core.PaymentMethodID
	connection    core.X402ConnectionID
}

// invoiceFixtureParents creates the parent rows of the invoice tests in the
// organization of be.
func invoiceFixtureParents(e *env, be *organization.BillingEntity) invoiceParents {
	e.t.Helper()
	p := invoiceParents{org: be.OrganizationID, be: be.ID}
	e.tx(func(ctx context.Context, r port.Repos) error {
		be2 := organization.NewBillingEntity(p.org, core.NewID[core.BillingEntityID](e.ids), "second", "Second", now)
		if err := r.BillingEntities.Create(ctx, be2); err != nil {
			return fmt.Errorf("billing entity: %w", err)
		}
		p.be2 = be2.ID
		for i := range p.customers {
			c := customer.New(p.org, core.NewID[core.CustomerID](e.ids), fmt.Sprintf("inv-cust-%d", i), be.ID, now)
			if err := r.Customers.Create(ctx, c); err != nil {
				return fmt.Errorf("customer: %w", err)
			}
			p.customers[i] = c.ID
		}
		plan := &catalog.Plan{
			ID: core.NewID[core.PlanID](e.ids), OrganizationID: p.org, Code: "inv-plan", Name: "Invoice plan",
			Interval: period.Monthly, Currency: invoiceEUR, Status: catalog.PlanStatusActive, CreatedAt: now, UpdatedAt: now,
		}
		if err := r.Plans.Create(ctx, plan); err != nil {
			return fmt.Errorf("plan: %w", err)
		}
		for i := range p.subscriptions {
			s := subscription.New(p.org, core.NewID[core.SubscriptionID](e.ids), p.customers[0], plan.ID,
				fmt.Sprintf("inv-sub-%d", i), now, subscription.PlanFacts{}, now)
			s.BillingEntityID = be.ID
			if err := r.Subscriptions.Create(ctx, s); err != nil {
				return fmt.Errorf("subscription: %w", err)
			}
			p.subscriptions[i] = s.ID
		}
		w := wallet.New(p.org, core.NewID[core.WalletID](e.ids), p.customers[0], invoiceEUR, money.MustParse("1"), now)
		if err := r.Wallets.Create(ctx, w); err != nil {
			return fmt.Errorf("wallet: %w", err)
		}
		wt := &wallet.Transaction{
			ID: core.NewID[core.WalletTransactionID](e.ids), OrganizationID: p.org, WalletID: w.ID,
			TransactionType: wallet.TransactionTypeInbound, TransactionStatus: wallet.TransactionStatusPurchased,
			Status: wallet.InitialStatus(wallet.TransactionStatusPurchased), Source: wallet.TransactionSourceManual,
			Currency: invoiceEUR, Amount: 1000, CreditAmount: money.MustParse("1000"), Priority: 50,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := r.WalletTransactions.Create(ctx, wt); err != nil {
			return fmt.Errorf("wallet transaction: %w", err)
		}
		p.walletTx = wt.ID
		m := &payment.Method{
			ID: core.NewID[core.PaymentMethodID](e.ids), OrganizationID: p.org, CustomerID: p.customers[0],
			ProviderMethodID: "pm_inv", CreatedAt: now, UpdatedAt: now,
		}
		if err := r.PaymentMethods.Create(ctx, m); err != nil {
			return fmt.Errorf("payment method: %w", err)
		}
		p.method = m.ID
		conn := x402FixtureConnection(e, p.org, "inv-x402")
		if err := r.X402Connections.Create(ctx, conn); err != nil {
			return fmt.Errorf("x402 connection: %w", err)
		}
		p.connection = conn.ID
		return nil
	})
	return p
}

// invoiceFixtureMinimal returns a draft invoice with only the required
// fields set.
func invoiceFixtureMinimal(e *env, p invoiceParents, created time.Time) *invoice.Invoice {
	return &invoice.Invoice{
		ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: p.org, CustomerID: p.customers[0], BillingEntityID: p.be,
		Type: invoice.TypeSubscription, Status: invoice.StatusDraft, PaymentStatus: core.PaymentStatusPending,
		Currency: invoiceEUR, CreatedAt: created, UpdatedAt: created,
	}
}

// invoiceFixtureFull returns a finalized invoice with every field set.
func invoiceFixtureFull(e *env, p invoiceParents, number string) *invoice.Invoice {
	inv := invoiceFixtureMinimal(e, p, now)
	inv.Type = invoice.TypeOneOff
	inv.Status = invoice.StatusFinalized
	inv.PaymentStatus = core.PaymentStatusFailed
	inv.TaxStatus = invoice.TaxStatusSucceeded
	inv.Number = number
	inv.SequentialID = ptr(int64(7))
	inv.BillingEntitySequentialID = ptr(int64(42))
	inv.FeesAmount, inv.ProgressiveBillingCreditAmount, inv.CouponsAmount = 10000, 100, 200
	inv.SubTotalExcludingTaxesAmount, inv.TaxesAmount, inv.TaxesRate = 9700, 1940, money.MustParse("20.12345")
	inv.SubTotalIncludingTaxesAmount, inv.CreditNotesAmount = 11640, 300
	inv.PrepaidCreditAmount, inv.PrepaidGrantedCreditAmount, inv.PrepaidPurchasedCreditAmount = 400, 150, 250
	inv.TotalAmount, inv.TotalPaidAmount, inv.OffsettedAmount = 10940, 500, 50
	inv.IssuingDate = core.NewDate(2025, time.March, 14)
	inv.PaymentDueDate = core.NewDate(2025, time.April, 13)
	inv.ExpectedFinalizationDate = core.NewDate(2025, time.March, 17)
	inv.AppliedGracePeriodDays, inv.NetPaymentTermDays = 3, 30
	inv.PaymentOverdue = true
	inv.PaymentDisputeLostAt = ptr(at(2 * time.Hour))
	inv.SelfBilled = true
	inv.SkipAutomaticPayment = true
	inv.PaymentMethodType = core.PaymentMethodTypeProvider
	inv.PaymentMethodID = p.method
	inv.PurchaseOrderNumber = ptr("PO-" + number)
	inv.X402ConnectionID = p.connection
	// The token is unique across organizations, so it derives from the ID.
	inv.X402PaymentToken = ptr("x402-" + inv.ID.String())
	inv.Metadata = core.Metadata{{Key: "po", Value: "PO-1"}, {Key: "dept", Value: "R&D"}}
	inv.File = core.BlobRef("invoices/" + number + ".pdf")
	inv.XMLFile = core.BlobRef("invoices/" + number + ".xml")
	inv.ReadyForPaymentProcessing = true
	inv.RefreshRequested = true
	inv.FinalizedAt = ptr(at(time.Hour))
	inv.VoidedAt = ptr(at(3 * time.Hour))
	inv.UpdatedAt = at(4 * time.Hour)
	return inv
}

// invoiceFixtureCreate creates the invoices in one transaction.
func invoiceFixtureCreate(e *env, invs ...*invoice.Invoice) {
	e.t.Helper()
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, inv := range invs {
			if err := r.Invoices.Create(ctx, inv); err != nil {
				return err
			}
		}
		return nil
	})
}

// invoiceFixtureFee returns a minimal charge fee of an invoice (zero for
// none).
func invoiceFixtureFee(e *env, p invoiceParents, inv core.InvoiceID, created time.Time) *invoice.Fee {
	return &invoice.Fee{
		ID: core.NewID[core.FeeID](e.ids), OrganizationID: p.org, InvoiceID: inv, Type: core.FeeTypeCharge,
		Currency: invoiceEUR, CreatedAt: created, UpdatedAt: created,
	}
}

func invoiceID(inv *invoice.Invoice) core.InvoiceID                            { return inv.ID }
func invoiceFeeID(f *invoice.Fee) core.FeeID                                   { return f.ID }
func invoiceSubscriptionID(s *invoice.Subscription) core.InvoiceSubscriptionID { return s.ID }

func testInvoice(t *testing.T, h Harness) {
	t.Run("invoices", func(t *testing.T) {
		t.Parallel()
		testInvoiceInvoices(t, h)
	})
	t.Run("invoice queries", func(t *testing.T) {
		t.Parallel()
		testInvoiceQueries(t, h)
	})
	t.Run("delete draft", func(t *testing.T) {
		t.Parallel()
		testInvoiceDeleteDraft(t, h)
	})
	t.Run("invoice subscriptions", func(t *testing.T) {
		t.Parallel()
		testInvoiceSubscriptions(t, h)
	})
	t.Run("applied custom sections", func(t *testing.T) {
		t.Parallel()
		testAppliedInvoiceCustomSections(t, h)
	})
	t.Run("refresh by metric", func(t *testing.T) {
		t.Parallel()
		testInvoiceRefreshByMetric(t, h)
	})
	t.Run("fees", func(t *testing.T) {
		t.Parallel()
		testInvoiceFees(t, h)
	})
	t.Run("applied taxes", func(t *testing.T) {
		t.Parallel()
		testInvoiceAppliedTaxes(t, h)
	})
	t.Run("adjusted fees", func(t *testing.T) {
		t.Parallel()
		testInvoiceAdjustedFees(t, h)
	})
	t.Run("settlements and error details", func(t *testing.T) {
		t.Parallel()
		testInvoiceSettlementsAndErrors(t, h)
	})
	t.Run("credits", func(t *testing.T) {
		t.Parallel()
		testInvoiceCredits(t, h)
	})
	t.Run("organization isolation", func(t *testing.T) {
		t.Parallel()
		testInvoiceIsolation(t, h)
	})
}

func testInvoiceInvoices(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	full := invoiceFixtureFull(e, p, "ACM-0001")
	minimal := invoiceFixtureMinimal(e, p, at(time.Minute))
	_, otherBE := e.org("Other")
	other := invoiceFixtureMinimal(e, invoiceFixtureParents(e, otherBE), now)
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Create() full", r.Invoices.Create(ctx, full))
		noErr(t, "Create() minimal", r.Invoices.Create(ctx, minimal))
		wantErr(t, "Create() same ID", r.Invoices.Create(ctx, full), core.ErrConflict)
		dup := invoiceFixtureMinimal(e, p, now)
		dup.Number = "ACM-0001"
		wantErr(t, "Create() same number", r.Invoices.Create(ctx, dup), core.ErrConflict)
		dup = invoiceFixtureMinimal(e, p, now)
		dup.X402PaymentToken = full.X402PaymentToken
		wantErr(t, "Create() same x402 payment token", r.Invoices.Create(ctx, dup), core.ErrConflict)
		// The token is unique across organizations (index_invoices_on_x402_payment_token).
		other.X402PaymentToken = full.X402PaymentToken
		wantErr(t, "Create() same x402 payment token in another organization", r.Invoices.Create(ctx, other), core.ErrConflict)

		got, err := r.Invoices.Get(ctx, p.org, full.ID)
		noErr(t, "Get() after a conflict", err)
		equal(t, "Get() full", got, full)
		got, err = r.Invoices.Get(ctx, p.org, minimal.ID)
		noErr(t, "Get() minimal", err)
		equal(t, "Get() minimal", got, minimal)
		_, err = r.Invoices.Get(ctx, p.org, core.NewID[core.InvoiceID](e.ids))
		wantErr(t, "Get() unknown", err, core.ErrNotFound)

		got, err = r.Invoices.GetByNumber(ctx, p.org, "ACM-0001")
		noErr(t, "GetByNumber()", err)
		equal(t, "GetByNumber()", got, full)
		_, err = r.Invoices.GetByNumber(ctx, p.org, "ACM-9999")
		wantErr(t, "GetByNumber() unknown", err, core.ErrNotFound)
		return nil
	})

	// Draft numbers and empty numbers repeat.
	e.tx(func(ctx context.Context, r port.Repos) error {
		for range 2 {
			d := invoiceFixtureMinimal(e, p, now)
			d.Number = invoice.DraftNumber("ACM")
			noErr(t, "Create() draft number", r.Invoices.Create(ctx, d))
			noErr(t, "Create() empty number", r.Invoices.Create(ctx, invoiceFixtureMinimal(e, p, now)))
		}
		return nil
	})

	// Update every field, then back to nil and empty.
	e.tx(func(ctx context.Context, r port.Repos) error {
		upd := *full
		upd.Status = invoice.StatusVoided
		upd.PaymentStatus = core.PaymentStatusSucceeded
		upd.TaxStatus = invoice.TaxStatusFailed
		upd.Number = "ACM-0002"
		upd.TotalPaidAmount = 10940
		upd.TaxesRate = money.MustParse("5.5")
		upd.IssuingDate = core.NewDate(2025, time.December, 31)
		upd.VoidedInvoiceID = minimal.ID
		upd.PaymentMethodType = core.PaymentMethodTypeManual
		upd.Metadata = core.Metadata{{Key: "po", Value: "PO-2"}}
		upd.PurchaseOrderNumber = ptr("PO-2")
		upd.UpdatedAt = at(5 * time.Hour)
		noErr(t, "Update()", r.Invoices.Update(ctx, &upd))
		got, err := r.Invoices.Get(ctx, p.org, full.ID)
		noErr(t, "Get() updated", err)
		equal(t, "Get() updated", got, &upd)

		cleared := invoiceFixtureMinimal(e, p, now)
		cleared.ID = full.ID
		noErr(t, "Update() cleared", r.Invoices.Update(ctx, cleared))
		got, err = r.Invoices.Get(ctx, p.org, full.ID)
		noErr(t, "Get() cleared", err)
		equal(t, "Get() cleared", got, cleared)

		taken := *minimal
		taken.Number = "ACM-0003"
		noErr(t, "Update() number", r.Invoices.Update(ctx, &taken))
		other := *cleared
		other.Number = "ACM-0003"
		wantErr(t, "Update() number taken", r.Invoices.Update(ctx, &other), core.ErrConflict)
		_, err = r.Invoices.Get(ctx, p.org, full.ID)
		noErr(t, "Get() after an update conflict", err)

		unknown := invoiceFixtureMinimal(e, p, now)
		wantErr(t, "Update() unknown", r.Invoices.Update(ctx, unknown), core.ErrNotFound)
		return nil
	})
}

func testInvoiceQueries(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	mk := func(created time.Time, mod func(*invoice.Invoice)) *invoice.Invoice {
		inv := invoiceFixtureMinimal(e, p, created)
		mod(inv)
		return inv
	}
	today := core.NewDate(2025, time.March, 14)
	// Drafts: due (no date, today, yesterday), not yet due, refresh requested.
	dNoDate := mk(at(1*time.Minute), func(*invoice.Invoice) {})
	dToday := mk(at(2*time.Minute), func(i *invoice.Invoice) { i.ExpectedFinalizationDate = today })
	dLater := mk(at(3*time.Minute), func(i *invoice.Invoice) {
		i.ExpectedFinalizationDate = core.NewDate(2025, time.March, 15)
		i.RefreshRequested = true
		i.CustomerID = p.customers[1]
	})
	// Finalized: overdue candidates and not.
	fin := func(created time.Time, due core.Date, mod func(*invoice.Invoice)) *invoice.Invoice {
		return mk(created, func(i *invoice.Invoice) {
			i.Status, i.Type = invoice.StatusFinalized, invoice.TypeOneOff
			i.PaymentDueDate = due
			i.SequentialID = ptr(int64(created.Sub(now) / time.Minute))
			i.BillingEntitySequentialID = i.SequentialID
			mod(i)
		})
	}
	yesterday := core.NewDate(2025, time.March, 13)
	fOverdue := fin(at(4*time.Minute), yesterday, func(*invoice.Invoice) {})
	fDueToday := fin(at(5*time.Minute), today, func(*invoice.Invoice) {})
	fPaid := fin(at(6*time.Minute), yesterday, func(i *invoice.Invoice) { i.PaymentStatus = core.PaymentStatusSucceeded })
	fFlagged := fin(at(7*time.Minute), yesterday, func(i *invoice.Invoice) { i.PaymentOverdue = true })
	fNoDue := fin(at(8*time.Minute), core.Date{}, func(i *invoice.Invoice) {
		i.PaymentStatus = core.PaymentStatusFailed
		i.BillingEntityID = p.be2
		i.SequentialID = ptr(int64(99))
	})
	fSelf := fin(at(9*time.Minute), yesterday, func(i *invoice.Invoice) {
		i.SelfBilled = true
		i.BillingEntitySequentialID = ptr(int64(500))
		i.RefreshRequested = true // not a draft: not listed
	})
	gen := mk(at(10*time.Minute), func(i *invoice.Invoice) { i.Status = invoice.StatusGenerating; i.Type = invoice.TypeCredit })
	all := []*invoice.Invoice{dNoDate, dToday, dLater, fOverdue, fDueToday, fPaid, fFlagged, fNoDue, fSelf, gen}
	invoiceFixtureCreate(e, all...)

	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, c := range []struct {
			name string
			f    invoice.Filter
			want []*invoice.Invoice
		}{
			{"none", invoice.Filter{}, all[:9]},
			{"generating", invoice.Filter{IncludeGenerating: true}, all},
			{"customer", invoice.Filter{CustomerID: p.customers[1]}, []*invoice.Invoice{dLater}},
			{"billing entity", invoice.Filter{BillingEntityID: p.be2}, []*invoice.Invoice{fNoDue}},
			{
				"statuses",
				invoice.Filter{Statuses: []invoice.Status{invoice.StatusDraft, invoice.StatusGenerating}, IncludeGenerating: true},
				[]*invoice.Invoice{dNoDate, dToday, dLater, gen},
			},
			{
				"payment statuses",
				invoice.Filter{PaymentStatuses: []core.PaymentStatus{core.PaymentStatusSucceeded, core.PaymentStatusFailed}},
				[]*invoice.Invoice{fPaid, fNoDue},
			},
			{"types", invoice.Filter{Types: []invoice.Type{invoice.TypeCredit}, IncludeGenerating: true}, []*invoice.Invoice{gen}},
			{"ids", invoice.Filter{IDs: []core.InvoiceID{fPaid.ID, dToday.ID, gen.ID}}, []*invoice.Invoice{dToday, fPaid}},
			{"all filters", invoice.Filter{
				CustomerID: p.customers[0], BillingEntityID: p.be, Statuses: []invoice.Status{invoice.StatusFinalized},
				PaymentStatuses: []core.PaymentStatus{core.PaymentStatusPending}, Types: []invoice.Type{invoice.TypeOneOff},
				IDs: []core.InvoiceID{fOverdue.ID, fPaid.ID, fSelf.ID},
			}, []*invoice.Invoice{fOverdue, fSelf}},
			{"no match", invoice.Filter{IncludeUnlisted: true, Statuses: []invoice.Status{invoice.StatusClosed}}, nil},
		} {
			got := listAll(t, 2, func(pr core.PageRequest) (core.Page[*invoice.Invoice], error) {
				return r.Invoices.List(ctx, p.org, c.f, pr)
			})
			equal(t, "List() by "+c.name, ids(got, invoiceID), ids(c.want, invoiceID))
		}
		page, err := r.Invoices.List(ctx, p.org, invoice.Filter{}, core.PageRequest{Size: 3})
		noErr(t, "List() first page", err)
		equal(t, "List() first page", page.Items, all[:3])
		_, err = r.Invoices.List(ctx, p.org, invoice.Filter{}, core.PageRequest{Size: 3, Token: "not-a-token"})
		wantErr(t, "List() bad token", err, core.ErrInvalidValue)

		byIDs, err := r.Invoices.ListByIDs(ctx, p.org, []core.InvoiceID{gen.ID, dNoDate.ID, core.NewID[core.InvoiceID](e.ids)})
		noErr(t, "ListByIDs()", err)
		equal(t, "ListByIDs()", byIDs, []*invoice.Invoice{dNoDate, gen})
		byIDs, err = r.Invoices.ListByIDs(ctx, p.org, nil)
		noErr(t, "ListByIDs() none", err)
		equal(t, "ListByIDs() none", len(byIDs), 0)

		drafts, err := r.Invoices.ListDraftsToFinalize(ctx, p.org, today)
		noErr(t, "ListDraftsToFinalize()", err)
		equal(t, "ListDraftsToFinalize()", ids(drafts, invoiceID), []core.InvoiceID{dNoDate.ID, dToday.ID})
		drafts, err = r.Invoices.ListDraftsToFinalize(ctx, p.org, core.NewDate(2025, time.March, 15))
		noErr(t, "ListDraftsToFinalize() tomorrow", err)
		equal(t, "ListDraftsToFinalize() tomorrow", ids(drafts, invoiceID), []core.InvoiceID{dNoDate.ID, dToday.ID, dLater.ID})

		refresh, err := r.Invoices.ListRefreshRequested(ctx, p.org)
		noErr(t, "ListRefreshRequested()", err)
		equal(t, "ListRefreshRequested()", refresh, []*invoice.Invoice{dLater})

		overdue, err := r.Invoices.ListOverdueCandidates(ctx, p.org, today)
		noErr(t, "ListOverdueCandidates()", err)
		equal(t, "ListOverdueCandidates()", ids(overdue, invoiceID), []core.InvoiceID{fOverdue.ID, fSelf.ID})
		overdue, err = r.Invoices.ListOverdueCandidates(ctx, p.org, core.NewDate(2025, time.March, 15))
		noErr(t, "ListOverdueCandidates() tomorrow", err)
		equal(t, "ListOverdueCandidates() tomorrow", ids(overdue, invoiceID), []core.InvoiceID{fOverdue.ID, fDueToday.ID, fSelf.ID})

		// Sequences: max+1 per customer and billing entity; the billing
		// entity's sequence leaves self-billed invoices out.
		n, err := r.Invoices.NextSequentialID(ctx, p.org, p.customers[0], p.be)
		noErr(t, "NextSequentialID()", err)
		equal(t, "NextSequentialID()", n, int64(10))
		n, err = r.Invoices.NextSequentialID(ctx, p.org, p.customers[0], p.be2)
		noErr(t, "NextSequentialID() second entity", err)
		equal(t, "NextSequentialID() second entity", n, int64(100))
		n, err = r.Invoices.NextSequentialID(ctx, p.org, p.customers[1], p.be)
		noErr(t, "NextSequentialID() no invoice", err)
		equal(t, "NextSequentialID() no invoice", n, int64(1))
		n, err = r.Invoices.NextBillingEntitySequentialID(ctx, p.org, p.be)
		noErr(t, "NextBillingEntitySequentialID()", err)
		equal(t, "NextBillingEntitySequentialID()", n, int64(8))
		n, err = r.Invoices.NextBillingEntitySequentialID(ctx, p.org, core.NewID[core.BillingEntityID](e.ids))
		noErr(t, "NextBillingEntitySequentialID() no invoice", err)
		equal(t, "NextBillingEntitySequentialID() no invoice", n, int64(1))

		// A lost dispute leaves the overdue candidates
		// (mark_invoices_as_payment_overdue_job.rb:12).
		lost := *fOverdue
		lost.PaymentDisputeLostAt = ptr(now)
		noErr(t, "Update() dispute lost", r.Invoices.Update(ctx, &lost))
		overdue, err = r.Invoices.ListOverdueCandidates(ctx, p.org, today)
		noErr(t, "ListOverdueCandidates() dispute lost", err)
		equal(t, "ListOverdueCandidates() dispute lost", ids(overdue, invoiceID), []core.InvoiceID{fSelf.ID})
		return nil
	})
}

func testInvoiceDeleteDraft(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	draft := invoiceFixtureMinimal(e, p, now)
	referenced := invoiceFixtureMinimal(e, p, at(time.Minute))
	final := invoiceFixtureFull(e, p, "ACM-0001")
	invoiceFixtureCreate(e, draft, referenced, final)
	fee := invoiceFixtureFee(e, p, draft.ID, now)
	is := &invoice.Subscription{
		ID: core.NewID[core.InvoiceSubscriptionID](e.ids), OrganizationID: p.org, InvoiceID: draft.ID,
		SubscriptionID: p.subscriptions[0], Reason: invoice.InvoicingReasonSubscriptionPeriodic,
		Period: period.Period{From: now, To: at(time.Hour)}, Timestamp: now, CreatedAt: now,
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Fees.Create()", r.Fees.Create(ctx, fee))
		noErr(t, "InvoiceSubscriptions.Create()", r.InvoiceSubscriptions.Create(ctx, is))
		return r.Payments.Create(ctx, &payment.Payment{
			ID: core.NewID[core.PaymentID](e.ids), OrganizationID: p.org, Payable: payment.InvoicePayable(referenced.ID),
			CustomerID: p.customers[0], Kind: payment.KindManual, Status: payment.StateSucceeded, Amount: 100,
			Currency: invoiceEUR, CreatedAt: now, UpdatedAt: now,
		})
	})
	e.tx(func(ctx context.Context, r port.Repos) error {
		wantErr(t, "DeleteDraft() unknown", r.Invoices.DeleteDraft(ctx, p.org, core.NewID[core.InvoiceID](e.ids)), core.ErrNotFound)
		wantErr(t, "DeleteDraft() finalized", r.Invoices.DeleteDraft(ctx, p.org, final.ID), core.ErrConflict)
		wantErr(t, "DeleteDraft() referenced by a payment", r.Invoices.DeleteDraft(ctx, p.org, referenced.ID), core.ErrConflict)
		_, err := r.Invoices.Get(ctx, p.org, referenced.ID)
		noErr(t, "Get() after a refused delete", err)
		noErr(t, "DeleteDraft()", r.Invoices.DeleteDraft(ctx, p.org, draft.ID))
		_, err = r.Invoices.Get(ctx, p.org, draft.ID)
		wantErr(t, "Get() deleted", err, core.ErrNotFound)
		wantErr(t, "DeleteDraft() again", r.Invoices.DeleteDraft(ctx, p.org, draft.ID), core.ErrNotFound)
		_, err = r.Fees.Get(ctx, p.org, fee.ID)
		wantErr(t, "Fees.Get() of a deleted draft", err, core.ErrNotFound)
		subs, err := r.InvoiceSubscriptions.ListByInvoice(ctx, p.org, draft.ID)
		noErr(t, "InvoiceSubscriptions.ListByInvoice() of a deleted draft", err)
		equal(t, "InvoiceSubscriptions.ListByInvoice() of a deleted draft", len(subs), 0)
		return nil
	})
}

// testInvoiceRefreshByMetric checks RequestRefreshByMetric: only the
// drafts billing a subscription whose plan charges the metric are flagged.
func testInvoiceRefreshByMetric(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	_, beB := e.org("Other")
	p := invoiceFixtureParents(e, be)
	billed := invoiceFixtureMinimal(e, p, now)
	unbilled := invoiceFixtureMinimal(e, p, at(time.Minute))
	finalized := invoiceFixtureMinimal(e, p, at(2*time.Minute))
	invoiceFixtureCreate(e, billed, unbilled, finalized)
	e.tx(func(ctx context.Context, r port.Repos) error {
		sub, err := r.Subscriptions.Get(ctx, p.org, p.subscriptions[0])
		noErr(t, "Subscriptions.Get()", err)
		metric := catalogFixtureMetric(e, p.org, "refresh")
		other := catalogFixtureMetric(e, p.org, "other")
		for _, m := range []*catalog.BillableMetric{metric, other} {
			noErr(t, "Metrics.Create()", r.Metrics.Create(ctx, m))
		}
		noErr(t, "Charges.Create()", r.Charges.Create(ctx, catalogFixtureCharge(e, p.org, sub.PlanID, metric.ID, "refresh")))
		for i, inv := range []*invoice.Invoice{billed, finalized} {
			noErr(t, "InvoiceSubscriptions.Create()", r.InvoiceSubscriptions.Create(ctx, &invoice.Subscription{
				ID: core.NewID[core.InvoiceSubscriptionID](e.ids), OrganizationID: p.org, InvoiceID: inv.ID, SubscriptionID: sub.ID,
				Reason: invoice.InvoicingReasonInAdvanceCharge, Timestamp: at(time.Duration(i+1) * time.Hour), CreatedAt: now,
			}))
		}
		finalized.Status = invoice.StatusFinalized
		finalized.Number = "ACM-001"
		noErr(t, "Invoices.Update()", r.Invoices.Update(ctx, finalized))

		n, err := r.Invoices.RequestRefreshByMetric(ctx, beB.OrganizationID, metric.ID)
		noErr(t, "RequestRefreshByMetric() from another organization", err)
		equal(t, "RequestRefreshByMetric() from another organization", n, 0)
		n, err = r.Invoices.RequestRefreshByMetric(ctx, p.org, other.ID)
		noErr(t, "RequestRefreshByMetric() of an uncharged metric", err)
		equal(t, "RequestRefreshByMetric() of an uncharged metric", n, 0)
		n, err = r.Invoices.RequestRefreshByMetric(ctx, p.org, metric.ID)
		noErr(t, "RequestRefreshByMetric()", err)
		equal(t, "RequestRefreshByMetric()", n, 1)
		got, err := r.Invoices.ListRefreshRequested(ctx, p.org)
		noErr(t, "ListRefreshRequested()", err)
		equal(t, "ListRefreshRequested()", ids(got, invoiceID), []core.InvoiceID{billed.ID})
		return nil
	})
}

func testInvoiceSubscriptions(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	inv1 := invoiceFixtureMinimal(e, p, now)
	inv2 := invoiceFixtureMinimal(e, p, at(time.Minute))
	invoiceFixtureCreate(e, inv1, inv2)
	march := period.Period{From: time.Date(2025, time.March, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2025, time.April, 1, 0, 0, 0, 0, time.UTC).Add(-time.Microsecond)}
	april := period.Period{From: march.To.Add(time.Microsecond), To: time.Date(2025, time.May, 1, 0, 0, 0, 0, time.UTC).Add(-time.Microsecond)}
	mk := func(inv core.InvoiceID, sub core.SubscriptionID, reason invoice.InvoicingReason, pp period.Period, ts time.Time) *invoice.Subscription {
		return &invoice.Subscription{
			ID: core.NewID[core.InvoiceSubscriptionID](e.ids), OrganizationID: p.org, InvoiceID: inv, SubscriptionID: sub,
			Reason: reason, Period: pp, Timestamp: ts, CreatedAt: now,
		}
	}
	full := mk(inv1.ID, p.subscriptions[0], invoice.InvoicingReasonSubscriptionPeriodic, march, march.To.Add(time.Microsecond))
	full.ChargesPeriod = period.Period{From: march.From.AddDate(0, -1, 0), To: march.From.Add(-time.Microsecond)}
	full.FixedChargesPeriod = march
	full.Recurring = true
	minimal := mk(inv1.ID, p.subscriptions[1], invoice.InvoicingReasonSubscriptionStarting, period.Period{}, time.Time{})
	adv1 := mk(inv2.ID, p.subscriptions[0], invoice.InvoicingReasonInAdvanceCharge, march, at(time.Hour))
	adv2 := mk(inv2.ID, p.subscriptions[0], invoice.InvoicingReasonInAdvanceCharge, march, at(2*time.Hour))
	next := mk(inv2.ID, p.subscriptions[0], invoice.InvoicingReasonSubscriptionPeriodic, april, april.To.Add(time.Microsecond))
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, s := range []*invoice.Subscription{full, minimal, adv1, adv2, next} {
			noErr(t, "Create()", r.InvoiceSubscriptions.Create(ctx, s))
		}
		wantErr(t, "Create() same ID", r.InvoiceSubscriptions.Create(ctx, full), core.ErrConflict)
		dup := mk(inv2.ID, p.subscriptions[0], invoice.InvoicingReasonSubscriptionPeriodic, april, now)
		dup.Recurring, dup.ChargesPeriod = true, full.ChargesPeriod
		wantErr(t, "Create() same charges period", r.InvoiceSubscriptions.Create(ctx, dup), core.ErrConflict)
		dup = mk(inv2.ID, p.subscriptions[0], invoice.InvoicingReasonSubscriptionPeriodic, april, now)
		dup.Recurring, dup.ChargesPeriod, dup.FixedChargesPeriod = true, april, full.FixedChargesPeriod
		wantErr(t, "Create() same fixed charges period", r.InvoiceSubscriptions.Create(ctx, dup), core.ErrConflict)
		dup = mk(inv2.ID, p.subscriptions[1], invoice.InvoicingReasonSubscriptionStarting, april, now)
		wantErr(t, "Create() second starting row", r.InvoiceSubscriptions.Create(ctx, dup), core.ErrConflict)

		got, err := r.InvoiceSubscriptions.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice() after a conflict", err)
		equal(t, "ListByInvoice()", got, []*invoice.Subscription{full, minimal})
		got, err = r.InvoiceSubscriptions.ListBySubscription(ctx, p.org, p.subscriptions[0])
		noErr(t, "ListBySubscription()", err)
		equal(t, "ListBySubscription()", ids(got, invoiceSubscriptionID), ids([]*invoice.Subscription{full, adv1, adv2, next}, invoiceSubscriptionID))

		found, err := r.InvoiceSubscriptions.Find(ctx, p.org, invoice.IdempotencyKey{
			SubscriptionID: p.subscriptions[0], Kind: invoice.KeyCharges, Period: full.ChargesPeriod,
		})
		noErr(t, "Find() charges", err)
		equal(t, "Find() charges", found, full)
		found, err = r.InvoiceSubscriptions.Find(ctx, p.org, invoice.IdempotencyKey{
			SubscriptionID: p.subscriptions[0], Kind: invoice.KeyFixedCharges, Period: march,
		})
		noErr(t, "Find() fixed charges", err)
		equal(t, "Find() fixed charges", found.ID, full.ID)
		found, err = r.InvoiceSubscriptions.Find(ctx, p.org, invoice.IdempotencyKey{SubscriptionID: p.subscriptions[1], Kind: invoice.KeyStarting})
		noErr(t, "Find() starting", err)
		equal(t, "Find() starting", found.ID, minimal.ID)
		_, err = r.InvoiceSubscriptions.Find(ctx, p.org, invoice.IdempotencyKey{
			SubscriptionID: p.subscriptions[0], Kind: invoice.KeyCharges, Period: april,
		})
		wantErr(t, "Find() other charges period", err, core.ErrNotFound)
		_, err = r.InvoiceSubscriptions.Find(ctx, p.org, invoice.IdempotencyKey{SubscriptionID: p.subscriptions[0], Kind: invoice.KeyTerminating})
		wantErr(t, "Find() unknown", err, core.ErrNotFound)

		last, err := r.InvoiceSubscriptions.Last(ctx, p.org, p.subscriptions[0])
		noErr(t, "Last()", err)
		equal(t, "Last()", last, next)
		_, err = r.InvoiceSubscriptions.Last(ctx, p.org, core.NewID[core.SubscriptionID](e.ids))
		wantErr(t, "Last() unknown", err, core.ErrNotFound)

		noErr(t, "DeleteByInvoice()", r.InvoiceSubscriptions.DeleteByInvoice(ctx, p.org, inv2.ID))
		got, err = r.InvoiceSubscriptions.ListByInvoice(ctx, p.org, inv2.ID)
		noErr(t, "ListByInvoice() deleted", err)
		equal(t, "ListByInvoice() deleted", len(got), 0)
		last, err = r.InvoiceSubscriptions.Last(ctx, p.org, p.subscriptions[0])
		noErr(t, "Last() after a delete", err)
		equal(t, "Last() after a delete", last.ID, full.ID)
		// The key is free again.
		noErr(t, "Create() freed key", r.InvoiceSubscriptions.Create(ctx, next))

		// A regenerated invoice's rows give their keys to the copies.
		noErr(t, "MarkRegenerated()", r.InvoiceSubscriptions.MarkRegenerated(ctx, p.org, inv1.ID, inv2.ID))
		got, err = r.InvoiceSubscriptions.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice() regenerated", err)
		for _, s := range got {
			equal(t, "ListByInvoice() regenerated RegeneratedInvoiceID", s.RegeneratedInvoiceID, inv2.ID)
		}
		_, err = r.InvoiceSubscriptions.Find(ctx, p.org, invoice.IdempotencyKey{SubscriptionID: p.subscriptions[1], Kind: invoice.KeyStarting})
		wantErr(t, "Find() regenerated starting", err, core.ErrNotFound)
		copied := *full
		copied.ID, copied.InvoiceID = core.NewID[core.InvoiceSubscriptionID](e.ids), inv2.ID
		noErr(t, "Create() copy of a regenerated row", r.InvoiceSubscriptions.Create(ctx, &copied))
		found, err = r.InvoiceSubscriptions.Find(ctx, p.org, invoice.IdempotencyKey{
			SubscriptionID: p.subscriptions[0], Kind: invoice.KeyCharges, Period: full.ChargesPeriod,
		})
		noErr(t, "Find() copy", err)
		equal(t, "Find() copy", found.ID, copied.ID)
		return nil
	})
}

func testInvoiceFees(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	inv1 := invoiceFixtureMinimal(e, p, now)
	inv2 := invoiceFixtureMinimal(e, p, at(time.Minute))
	invoiceFixtureCreate(e, inv1, inv2)
	charge := core.NewID[core.ChargeID](e.ids)
	from := time.Date(2025, time.March, 1, 0, 0, 0, 0, time.UTC)

	full := invoiceFixtureFee(e, p, inv1.ID, now)
	full.SubscriptionID = p.subscriptions[0]
	full.ChargeID = charge
	full.ChargeFilterID = core.NewID[core.ChargeFilterID](e.ids)
	full.FixedChargeID = core.NewID[core.FixedChargeID](e.ids)
	full.AddOnID = core.NewID[core.AddOnID](e.ids)
	full.CommitmentID = core.NewID[core.CommitmentID](e.ids)
	full.WalletTransactionID = p.walletTx
	full.GroupedBy = map[string]string{"region": "eu", "tier": "gold"}
	full.InvoiceDisplayName = ptr("API calls")
	full.Description = ptr("Calls to the API")
	full.Amount, full.PreciseAmount = 12346, money.MustParse("12345.6789012345")
	full.UnitAmount, full.PreciseUnitAmount = 12, money.MustParse("0.0123456789")
	full.Units, full.TotalAggregatedUnits = money.MustParse("1000000.5"), money.MustParse("1000001")
	full.EventsCount = 31
	full.AmountDetails = json.RawMessage(`{"graduated_ranges": [{"from_value": 0, "to_value": 10}]}`)
	full.TaxesAmount, full.TaxesPreciseAmount = 2469, money.MustParse("2469.13578")
	full.TaxesRate, full.TaxesBaseRate = money.MustParse("20"), money.MustParse("0.95")
	full.PreciseCouponsAmount, full.PreciseCreditNotesAmount = money.MustParse("1.5"), money.MustParse("2.25")
	full.PayInAdvance, full.Invoiceable = true, true
	full.PaymentStatus = invoice.FeePaymentStatusRefunded
	full.Properties = invoice.FeeProperties{
		From: from, To: from.AddDate(0, 1, 0).Add(-time.Microsecond), ChargesFrom: from.AddDate(0, -1, 0),
		ChargesTo: from.Add(-time.Microsecond), Timestamp: from.AddDate(0, 1, 0), ChargesDurationDays: 31,
		FixedChargesFrom: from.AddDate(0, 1, 0), FixedChargesTo: from.AddDate(0, 2, 0), FixedChargesDurationDays: 30,
	}
	full.PresentationBreakdowns = []invoice.PresentationBreakdown{
		{PresentationBy: map[string]*string{"region": ptr("eu"), "team": nil}, Units: money.MustParse("600000.5")},
		{PresentationBy: map[string]*string{"region": ptr("us")}, Units: money.MustParse("400000")},
	}
	full.PricingUnitUsage = &invoice.PricingUnitUsage{
		PricingUnitID: core.NewID[core.PricingUnitID](e.ids), ShortName: "CR", ConversionRate: money.MustParse("0.5"),
		Amount: money.MustParse("24691.36"), PreciseAmount: money.MustParse("24691.3578024"),
		UnitAmount: money.MustParse("0.02"), PreciseUnitAmount: money.MustParse("0.0246913578"),
	}
	full.AppliedTaxes = []invoice.FeeAppliedTax{
		{
			ID: core.NewID[core.FeeAppliedTaxID](e.ids), TaxID: core.NewID[core.TaxID](e.ids), TaxCode: "vat", TaxName: "VAT",
			TaxDescription: ptr("French VAT"), TaxRate: money.MustParse("20"), Amount: 2469, PreciseAmount: money.MustParse("2469.13578"),
		},
		{ID: core.NewID[core.FeeAppliedTaxID](e.ids), TaxCode: "zero", TaxName: "Zero", TaxRate: money.MustParse("0"), PreciseAmount: money.MustParse("0")},
	}
	full.PayInAdvanceEventID = core.NewID[core.EventID](e.ids)
	full.PayInAdvanceEventTransactionID = "tx-1"
	full.SucceededAt, full.FailedAt, full.RefundedAt = ptr(at(time.Hour)), ptr(at(2*time.Hour)), ptr(at(3*time.Hour))
	full.UpdatedAt = at(4 * time.Hour)

	minimal := invoiceFixtureFee(e, p, core.InvoiceID{}, at(time.Minute))
	minimal.Type = core.FeeTypeAddOn

	// Unbilled pay-in-advance fees: listed when all conditions hold.
	unbilled := func(created time.Time, mod func(*invoice.Fee)) *invoice.Fee {
		f := invoiceFixtureFee(e, p, core.InvoiceID{}, created)
		f.SubscriptionID = p.subscriptions[1]
		f.PayInAdvance, f.Invoiceable, f.PaymentStatus = true, true, invoice.FeePaymentStatusSucceeded
		f.Properties.From = from
		f.PayInAdvanceEventTransactionID = "tx-2"
		mod(f)
		return f
	}
	u1 := unbilled(at(2*time.Minute), func(*invoice.Fee) {})
	uLate := unbilled(at(time.Hour), func(*invoice.Fee) {})
	uPending := unbilled(at(3*time.Minute), func(f *invoice.Fee) { f.PaymentStatus = invoice.FeePaymentStatusPending })
	uNotInvoiceable := unbilled(at(4*time.Minute), func(f *invoice.Fee) { f.Invoiceable = false })
	uArrears := unbilled(at(5*time.Minute), func(f *invoice.Fee) { f.PayInAdvance = false; f.PayInAdvanceEventTransactionID = "" })
	billed := unbilled(at(6*time.Minute), func(f *invoice.Fee) {
		f.InvoiceID = inv2.ID
		f.ChargeID = charge
		f.Type = core.FeeTypeSubscription
	})
	trueUp := invoiceFixtureFee(e, p, inv1.ID, at(7*time.Minute))
	trueUp.TrueUpParentFeeID = full.ID
	all := []*invoice.Fee{full, minimal, u1, uLate, uPending, uNotInvoiceable, uArrears, billed, trueUp}

	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, f := range all {
			noErr(t, "Create()", r.Fees.Create(ctx, f))
		}
		wantErr(t, "Create() same ID", r.Fees.Create(ctx, full), core.ErrConflict)
		got, err := r.Fees.Get(ctx, p.org, full.ID)
		noErr(t, "Get() after a conflict", err)
		equal(t, "Get() full", got, full)
		got, err = r.Fees.Get(ctx, p.org, minimal.ID)
		noErr(t, "Get() minimal", err)
		equal(t, "Get() minimal", got, minimal)
		_, err = r.Fees.Get(ctx, p.org, core.NewID[core.FeeID](e.ids))
		wantErr(t, "Get() unknown", err, core.ErrNotFound)

		for _, c := range []struct {
			name string
			f    invoice.FeeFilter
			want []*invoice.Fee
		}{
			{"none", invoice.FeeFilter{}, all},
			{"invoice", invoice.FeeFilter{InvoiceID: inv1.ID}, []*invoice.Fee{full, trueUp}},
			{"subscription", invoice.FeeFilter{SubscriptionID: p.subscriptions[1]}, []*invoice.Fee{u1, uLate, uPending, uNotInvoiceable, uArrears, billed}},
			{"charge", invoice.FeeFilter{ChargeID: charge}, []*invoice.Fee{full, billed}},
			{"types", invoice.FeeFilter{Types: []core.FeeType{core.FeeTypeAddOn, core.FeeTypeSubscription}}, []*invoice.Fee{minimal, billed}},
			{"all filters", invoice.FeeFilter{InvoiceID: inv2.ID, SubscriptionID: p.subscriptions[1], ChargeID: charge, Types: []core.FeeType{core.FeeTypeSubscription}}, []*invoice.Fee{billed}},
			{"no match", invoice.FeeFilter{Types: []core.FeeType{core.FeeTypeProduct}}, nil},
		} {
			got := listAll(t, 2, func(pr core.PageRequest) (core.Page[*invoice.Fee], error) {
				return r.Fees.List(ctx, p.org, c.f, pr)
			})
			equal(t, "List() by "+c.name, ids(got, invoiceFeeID), ids(feesOldestFirst(c.want), invoiceFeeID))
		}

		byInv, err := r.Fees.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice()", err)
		equal(t, "ListByInvoice()", byInv, []*invoice.Fee{full, trueUp})
		byPeriod, err := r.Fees.ListBySubscriptionPeriod(ctx, p.org, p.subscriptions[1], from)
		noErr(t, "ListBySubscriptionPeriod()", err)
		equal(t, "ListBySubscriptionPeriod()", ids(byPeriod, invoiceFeeID), ids([]*invoice.Fee{u1, uLate, uPending, uNotInvoiceable, uArrears, billed}, invoiceFeeID))
		byPeriod, err = r.Fees.ListBySubscriptionPeriod(ctx, p.org, p.subscriptions[1], from.Add(time.Microsecond))
		noErr(t, "ListBySubscriptionPeriod() other period", err)
		equal(t, "ListBySubscriptionPeriod() other period", len(byPeriod), 0)
		unb, err := r.Fees.ListUnbilledInAdvance(ctx, p.org, at(30*time.Minute))
		noErr(t, "ListUnbilledInAdvance()", err)
		equal(t, "ListUnbilledInAdvance()", ids(unb, invoiceFeeID), []core.FeeID{u1.ID})
		unb, err = r.Fees.ListUnbilledInAdvance(ctx, p.org, at(2*time.Hour))
		noErr(t, "ListUnbilledInAdvance() later", err)
		equal(t, "ListUnbilledInAdvance() later", ids(unb, invoiceFeeID), []core.FeeID{u1.ID, uLate.ID})
		byCharge, err := r.Fees.ListByCharge(ctx, p.org, charge)
		noErr(t, "ListByCharge()", err)
		equal(t, "ListByCharge()", ids(byCharge, invoiceFeeID), []core.FeeID{full.ID, billed.ID})
		byTx, err := r.Fees.ListByEventTransaction(ctx, p.org, p.subscriptions[1], "tx-2")
		noErr(t, "ListByEventTransaction()", err)
		equal(t, "ListByEventTransaction()", ids(byTx, invoiceFeeID), []core.FeeID{u1.ID, uLate.ID, uPending.ID, uNotInvoiceable.ID, billed.ID})
		byTx, err = r.Fees.ListByEventTransaction(ctx, p.org, p.subscriptions[0], "tx-2")
		noErr(t, "ListByEventTransaction() other subscription", err)
		equal(t, "ListByEventTransaction() other subscription", len(byTx), 0)

		// Update to every field cleared, then to every field set.
		cleared := invoiceFixtureFee(e, p, core.InvoiceID{}, now)
		cleared.ID = full.ID
		noErr(t, "Update() cleared", r.Fees.Update(ctx, cleared))
		got, err = r.Fees.Get(ctx, p.org, full.ID)
		noErr(t, "Get() cleared", err)
		equal(t, "Get() cleared", got, cleared)
		upd := *full
		upd.InvoiceID = inv2.ID
		upd.Amount = 1
		upd.Units = money.MustParse("3.000001")
		upd.GroupedBy = map[string]string{"region": "us"}
		noErr(t, "Update()", r.Fees.Update(ctx, &upd))
		got, err = r.Fees.Get(ctx, p.org, full.ID)
		noErr(t, "Get() updated", err)
		equal(t, "Get() updated", got, &upd)
		wantErr(t, "Update() unknown", r.Fees.Update(ctx, invoiceFixtureFee(e, p, core.InvoiceID{}, now)), core.ErrNotFound)

		noErr(t, "DeleteByInvoice()", r.Fees.DeleteByInvoice(ctx, p.org, inv2.ID))
		byInv, err = r.Fees.ListByInvoice(ctx, p.org, inv2.ID)
		noErr(t, "ListByInvoice() deleted", err)
		equal(t, "ListByInvoice() deleted", len(byInv), 0)
		_, err = r.Fees.Get(ctx, p.org, u1.ID)
		noErr(t, "Get() fee of no invoice after DeleteByInvoice()", err)
		return nil
	})
	if h.Name != "memstore" { // memstore has no referential integrity
		orphan := invoiceFixtureFee(e, p, core.NewID[core.InvoiceID](e.ids), now)
		wantErr(t, "Create() unknown invoice", e.txErr(func(ctx context.Context, r port.Repos) error {
			return r.Fees.Create(ctx, orphan)
		}), core.ErrNotFound)
	}
}

func testInvoiceAppliedTaxes(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	inv1 := invoiceFixtureMinimal(e, p, now)
	inv2 := invoiceFixtureMinimal(e, p, at(time.Minute))
	invoiceFixtureCreate(e, inv1, inv2)
	mk := func(code string, inv core.InvoiceID) *invoice.AppliedTax {
		return &invoice.AppliedTax{
			ID: core.NewID[core.InvoiceAppliedTaxID](e.ids), OrganizationID: p.org, InvoiceID: inv, TaxCode: code, TaxName: "Tax " + code,
			TaxRate: money.MustParse("0"), PreciseAmount: money.MustParse("0"), Currency: invoiceEUR, CreatedAt: now,
		}
	}
	full := mk("vat", inv1.ID)
	full.TaxID = core.NewID[core.TaxID](e.ids)
	full.TaxDescription = ptr("French VAT")
	full.TaxRate, full.FeesAmount, full.Amount, full.PreciseAmount = money.MustParse("20.5"), 10000, 2050, money.MustParse("2050.00001")
	full.TaxableBaseAmount = 9000
	minimal := mk("zero", inv1.ID)
	other := mk("gst", inv2.ID)
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "ReplaceForInvoice()", r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, p.org, inv1.ID, []*invoice.AppliedTax{full, minimal}))
		noErr(t, "ReplaceForInvoice() other", r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, p.org, inv2.ID, []*invoice.AppliedTax{other}))
		got, err := r.InvoiceAppliedTaxes.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice()", err)
		equal(t, "ListByInvoice()", got, []*invoice.AppliedTax{full, minimal})

		// Replacing drops the old rows; the invoice argument wins.
		repl := mk("new", inv2.ID)
		noErr(t, "ReplaceForInvoice() again", r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, p.org, inv1.ID, []*invoice.AppliedTax{repl}))
		got, err = r.InvoiceAppliedTaxes.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice() replaced", err)
		want := *repl
		want.InvoiceID = inv1.ID
		equal(t, "ListByInvoice() replaced", got, []*invoice.AppliedTax{&want})
		got, err = r.InvoiceAppliedTaxes.ListByInvoice(ctx, p.org, inv2.ID)
		noErr(t, "ListByInvoice() other", err)
		equal(t, "ListByInvoice() other", got, []*invoice.AppliedTax{other})

		noErr(t, "ReplaceForInvoice() none", r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, p.org, inv1.ID, nil))
		got, err = r.InvoiceAppliedTaxes.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice() none", err)
		equal(t, "ListByInvoice() none", len(got), 0)
		return nil
	})
}

func testInvoiceAdjustedFees(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	inv1 := invoiceFixtureMinimal(e, p, now)
	inv2 := invoiceFixtureMinimal(e, p, at(time.Minute))
	invoiceFixtureCreate(e, inv1, inv2)
	fee := core.NewID[core.FeeID](e.ids) // adjustments outlive the fees they adjust: no fee row
	mk := func(inv core.InvoiceID, mode invoice.AdjustmentMode) *invoice.AdjustedFee {
		return &invoice.AdjustedFee{
			ID: core.NewID[core.AdjustedFeeID](e.ids), OrganizationID: p.org, InvoiceID: inv, FeeType: core.FeeTypeCharge,
			Mode: mode, ChargeID: core.NewID[core.ChargeID](e.ids), CreatedAt: now,
		}
	}
	full := mk(inv1.ID, invoice.AdjustmentModeUnitsAndUnitPrice)
	full.FeeID = fee
	full.SubscriptionID = p.subscriptions[0]
	full.ChargeFilterID = core.NewID[core.ChargeFilterID](e.ids)
	full.FixedChargeID = core.NewID[core.FixedChargeID](e.ids)
	full.GroupedBy = map[string]string{"region": "eu"}
	pf := time.Date(2025, time.March, 1, 0, 0, 0, 0, time.UTC)
	full.ChargesFrom, full.ChargesTo = pf, pf.AddDate(0, 1, 0)
	full.FixedChargesFrom, full.FixedChargesTo = pf.AddDate(0, 1, 0), pf.AddDate(0, 2, 0)
	full.InvoiceDisplayName = ptr("Adjusted")
	full.Units, full.UnitPreciseAmount = money.MustParse("12.5"), money.MustParse("0.000123")
	minimal := mk(inv1.ID, invoice.AdjustmentModeDisplayName)
	other := mk(inv2.ID, invoice.AdjustmentModeUnits)
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, a := range []*invoice.AdjustedFee{full, minimal, other} {
			noErr(t, "Create()", r.AdjustedFees.Create(ctx, a))
		}
		wantErr(t, "Create() same ID", r.AdjustedFees.Create(ctx, full), core.ErrConflict)
		dup := mk(inv2.ID, invoice.AdjustmentModeUnits)
		dup.FeeID = fee
		wantErr(t, "Create() same fee", r.AdjustedFees.Create(ctx, dup), core.ErrConflict)
		got, err := r.AdjustedFees.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice() after a conflict", err)
		equal(t, "ListByInvoice()", got, []*invoice.AdjustedFee{full, minimal})

		noErr(t, "Delete()", r.AdjustedFees.Delete(ctx, p.org, full.ID))
		wantErr(t, "Delete() again", r.AdjustedFees.Delete(ctx, p.org, full.ID), core.ErrNotFound)
		noErr(t, "Create() freed fee", r.AdjustedFees.Create(ctx, dup))
		got, err = r.AdjustedFees.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice() after a delete", err)
		equal(t, "ListByInvoice() after a delete", got, []*invoice.AdjustedFee{minimal})

		noErr(t, "DeleteByInvoice()", r.AdjustedFees.DeleteByInvoice(ctx, p.org, inv2.ID))
		got, err = r.AdjustedFees.ListByInvoice(ctx, p.org, inv2.ID)
		noErr(t, "ListByInvoice() deleted", err)
		equal(t, "ListByInvoice() deleted", len(got), 0)
		return nil
	})
}

func testInvoiceSettlementsAndErrors(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	inv1 := invoiceFixtureFull(e, p, "ACM-0001")
	inv2 := invoiceFixtureMinimal(e, p, at(time.Minute))
	invoiceFixtureCreate(e, inv1, inv2)
	pay := &payment.Payment{
		ID: core.NewID[core.PaymentID](e.ids), OrganizationID: p.org, Payable: payment.InvoicePayable(inv1.ID),
		CustomerID: p.customers[0], Kind: payment.KindManual, Status: payment.StateSucceeded, Amount: 500,
		Currency: invoiceEUR, CreatedAt: now, UpdatedAt: now,
	}
	cn := &creditnote.CreditNote{
		ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: p.org, InvoiceID: inv1.ID, CustomerID: p.customers[0],
		Number: "ACM-0001-CN001", SequentialID: 1, Status: creditnote.StatusFinalized, Reason: creditnote.ReasonOther,
		Currency: invoiceEUR, CreatedAt: now, UpdatedAt: now,
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := r.Payments.Create(ctx, pay); err != nil {
			return err
		}
		return r.CreditNotes.Create(ctx, cn)
	})
	byPayment := &invoice.Settlement{
		ID: core.NewID[core.InvoiceSettlementID](e.ids), OrganizationID: p.org, InvoiceID: inv1.ID,
		Type: invoice.SettlementTypePayment, PaymentID: pay.ID, Amount: 500, Currency: invoiceEUR, CreatedAt: now,
	}
	byCredit := &invoice.Settlement{
		ID: core.NewID[core.InvoiceSettlementID](e.ids), OrganizationID: p.org, InvoiceID: inv1.ID,
		Type: invoice.SettlementTypeCreditNote, CreditNoteID: cn.ID, Amount: 50, Currency: invoiceEUR, CreatedAt: at(time.Minute),
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Settlements.Create() payment", r.InvoiceSettlements.Create(ctx, byPayment))
		noErr(t, "Settlements.Create() credit note", r.InvoiceSettlements.Create(ctx, byCredit))
		wantErr(t, "Settlements.Create() same ID", r.InvoiceSettlements.Create(ctx, byPayment), core.ErrConflict)
		got, err := r.InvoiceSettlements.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "Settlements.ListByInvoice()", err)
		equal(t, "Settlements.ListByInvoice()", got, []*invoice.Settlement{byPayment, byCredit})
		got, err = r.InvoiceSettlements.ListByInvoice(ctx, p.org, inv2.ID)
		noErr(t, "Settlements.ListByInvoice() none", err)
		equal(t, "Settlements.ListByInvoice() none", len(got), 0)
		return nil
	})
	if h.Name != "memstore" { // memstore has no referential integrity
		bad := *byPayment
		bad.ID, bad.PaymentID = core.NewID[core.InvoiceSettlementID](e.ids), core.NewID[core.PaymentID](e.ids)
		wantErr(t, "Settlements.Create() unknown payment", e.txErr(func(ctx context.Context, r port.Repos) error {
			return r.InvoiceSettlements.Create(ctx, &bad)
		}), core.ErrNotFound)
	}

	mk := func(inv core.InvoiceID, code invoice.ErrorCode, created time.Time) *invoice.ErrorDetail {
		return &invoice.ErrorDetail{
			ID: core.NewID[core.ErrorDetailID](e.ids), OrganizationID: p.org, InvoiceID: inv, Code: code, CreatedAt: created,
		}
	}
	tax := mk(inv1.ID, invoice.ErrorCodeTaxError, now)
	tax.Details = json.RawMessage(`{"provider": "anrok", "message": "bad address"}`)
	gen := mk(inv1.ID, invoice.ErrorCodeInvoiceGenerationError, at(time.Minute))
	voiding := mk(inv1.ID, invoice.ErrorCodeTaxVoidingError, at(2*time.Minute))
	other := mk(inv2.ID, invoice.ErrorCodeTaxError, now)
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, d := range []*invoice.ErrorDetail{tax, gen, voiding, other} {
			noErr(t, "ErrorDetails.Create()", r.ErrorDetails.Create(ctx, d))
		}
		wantErr(t, "ErrorDetails.Create() same ID", r.ErrorDetails.Create(ctx, tax), core.ErrConflict)
		got, err := r.ErrorDetails.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ErrorDetails.ListByInvoice() after a conflict", err)
		equal(t, "ErrorDetails.ListByInvoice()", got, []*invoice.ErrorDetail{tax, gen, voiding})

		noErr(t, "ErrorDetails.DeleteByInvoice() one code", r.ErrorDetails.DeleteByInvoice(ctx, p.org, inv1.ID, invoice.ErrorCodeTaxError))
		got, err = r.ErrorDetails.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ErrorDetails.ListByInvoice() one code deleted", err)
		equal(t, "ErrorDetails.ListByInvoice() one code deleted", got, []*invoice.ErrorDetail{gen, voiding})
		noErr(t, "ErrorDetails.DeleteByInvoice() every code", r.ErrorDetails.DeleteByInvoice(ctx, p.org, inv1.ID, ""))
		got, err = r.ErrorDetails.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ErrorDetails.ListByInvoice() every code deleted", err)
		equal(t, "ErrorDetails.ListByInvoice() every code deleted", len(got), 0)
		got, err = r.ErrorDetails.ListByInvoice(ctx, p.org, inv2.ID)
		noErr(t, "ErrorDetails.ListByInvoice() other invoice", err)
		equal(t, "ErrorDetails.ListByInvoice() other invoice", got, []*invoice.ErrorDetail{other})
		return nil
	})
}

func testInvoiceIsolation(t *testing.T, h Harness) {
	e := newEnv(t, h)
	a, b := e.twoOrgs()
	pa := invoiceFixtureParents(e, a)
	pb := invoiceFixtureParents(e, b)
	invA := invoiceFixtureFull(e, pa, "INV-0001")
	// The same number in another organization is allowed.
	invB := invoiceFixtureFull(e, pb, "INV-0001")
	invoiceFixtureCreate(e, invA, invB)
	feeA := invoiceFixtureFee(e, pa, invA.ID, now)
	feeA.SubscriptionID = pa.subscriptions[0]
	feeA.PayInAdvanceEventTransactionID = "tx"
	isA := &invoice.Subscription{
		ID: core.NewID[core.InvoiceSubscriptionID](e.ids), OrganizationID: pa.org, InvoiceID: invA.ID,
		SubscriptionID: pa.subscriptions[0], Reason: invoice.InvoicingReasonSubscriptionPeriodic,
		Period: period.Period{From: now, To: at(time.Hour)}, Timestamp: now, CreatedAt: now,
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Fees.Create()", r.Fees.Create(ctx, feeA))
		noErr(t, "InvoiceSubscriptions.Create()", r.InvoiceSubscriptions.Create(ctx, isA))
		noErr(t, "ErrorDetails.Create()", r.ErrorDetails.Create(ctx, &invoice.ErrorDetail{
			ID: core.NewID[core.ErrorDetailID](e.ids), OrganizationID: pa.org, InvoiceID: invA.ID, Code: invoice.ErrorCodeTaxError, CreatedAt: now,
		}))
		return r.InvoiceAppliedTaxes.ReplaceForInvoice(ctx, pa.org, invA.ID, []*invoice.AppliedTax{{
			ID: core.NewID[core.InvoiceAppliedTaxID](e.ids), OrganizationID: pa.org, InvoiceID: invA.ID, TaxCode: "vat", TaxName: "VAT",
			TaxRate: money.MustParse("20"), PreciseAmount: money.MustParse("0"), Currency: invoiceEUR, CreatedAt: now,
		}})
	})
	e.tx(func(ctx context.Context, r port.Repos) error {
		org := pb.org
		_, err := r.Invoices.Get(ctx, org, invA.ID)
		wantErr(t, "Invoices.Get() of another organization", err, core.ErrNotFound)
		got, err := r.Invoices.GetByNumber(ctx, org, "INV-0001")
		noErr(t, "Invoices.GetByNumber()", err)
		equal(t, "Invoices.GetByNumber() of its organization", got.ID, invB.ID)
		page, err := r.Invoices.List(ctx, org, invoice.Filter{IncludeGenerating: true}, core.PageRequest{})
		noErr(t, "Invoices.List()", err)
		equal(t, "Invoices.List()", ids(page.Items, invoiceID), []core.InvoiceID{invB.ID})
		list, err := r.Invoices.ListByIDs(ctx, org, []core.InvoiceID{invA.ID})
		noErr(t, "Invoices.ListByIDs()", err)
		equal(t, "Invoices.ListByIDs()", len(list), 0)
		list, err = r.Invoices.ListRefreshRequested(ctx, org)
		noErr(t, "Invoices.ListRefreshRequested()", err)
		equal(t, "Invoices.ListRefreshRequested()", len(list), 0)
		n, err := r.Invoices.NextSequentialID(ctx, org, pa.customers[0], pa.be)
		noErr(t, "Invoices.NextSequentialID()", err)
		equal(t, "Invoices.NextSequentialID() of another organization", n, int64(1))
		wantErr(t, "Invoices.Update() of another organization", r.Invoices.Update(ctx, &invoice.Invoice{
			ID: invA.ID, OrganizationID: org, CustomerID: pb.customers[0], BillingEntityID: pb.be,
			Type: invoice.TypeOneOff, Status: invoice.StatusDraft, PaymentStatus: core.PaymentStatusPending, Currency: invoiceEUR,
		}), core.ErrNotFound)
		wantErr(t, "Invoices.DeleteDraft() of another organization", r.Invoices.DeleteDraft(ctx, org, invA.ID), core.ErrNotFound)

		_, err = r.Fees.Get(ctx, org, feeA.ID)
		wantErr(t, "Fees.Get() of another organization", err, core.ErrNotFound)
		fees, err := r.Fees.ListByInvoice(ctx, org, invA.ID)
		noErr(t, "Fees.ListByInvoice()", err)
		equal(t, "Fees.ListByInvoice()", len(fees), 0)
		fees, err = r.Fees.ListByEventTransaction(ctx, org, pa.subscriptions[0], "tx")
		noErr(t, "Fees.ListByEventTransaction()", err)
		equal(t, "Fees.ListByEventTransaction()", len(fees), 0)
		feePage, err := r.Fees.List(ctx, org, invoice.FeeFilter{}, core.PageRequest{})
		noErr(t, "Fees.List()", err)
		equal(t, "Fees.List()", len(feePage.Items), 0)
		noErr(t, "Fees.DeleteByInvoice() of another organization", r.Fees.DeleteByInvoice(ctx, org, invA.ID))

		subs, err := r.InvoiceSubscriptions.ListBySubscription(ctx, org, pa.subscriptions[0])
		noErr(t, "InvoiceSubscriptions.ListBySubscription()", err)
		equal(t, "InvoiceSubscriptions.ListBySubscription()", len(subs), 0)
		_, err = r.InvoiceSubscriptions.Last(ctx, org, pa.subscriptions[0])
		wantErr(t, "InvoiceSubscriptions.Last() of another organization", err, core.ErrNotFound)
		taxes, err := r.InvoiceAppliedTaxes.ListByInvoice(ctx, org, invA.ID)
		noErr(t, "InvoiceAppliedTaxes.ListByInvoice()", err)
		equal(t, "InvoiceAppliedTaxes.ListByInvoice()", len(taxes), 0)
		details, err := r.ErrorDetails.ListByInvoice(ctx, org, invA.ID)
		noErr(t, "ErrorDetails.ListByInvoice()", err)
		equal(t, "ErrorDetails.ListByInvoice()", len(details), 0)
		noErr(t, "ErrorDetails.DeleteByInvoice() of another organization", r.ErrorDetails.DeleteByInvoice(ctx, org, invA.ID, ""))
		return nil
	})
	// Nothing of organization A was touched.
	e.tx(func(ctx context.Context, r port.Repos) error {
		got, err := r.Invoices.Get(ctx, pa.org, invA.ID)
		noErr(t, "Invoices.Get()", err)
		equal(t, "Invoices.Get()", got, invA)
		fees, err := r.Fees.ListByInvoice(ctx, pa.org, invA.ID)
		noErr(t, "Fees.ListByInvoice()", err)
		equal(t, "Fees.ListByInvoice()", fees, []*invoice.Fee{feeA})
		details, err := r.ErrorDetails.ListByInvoice(ctx, pa.org, invA.ID)
		noErr(t, "ErrorDetails.ListByInvoice()", err)
		equal(t, "ErrorDetails.ListByInvoice()", len(details), 1)
		return nil
	})
}

func testInvoiceCredits(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := invoiceFixtureParents(e, be)
	inv1 := invoiceFixtureMinimal(e, p, now)
	inv2 := invoiceFixtureMinimal(e, p, at(time.Minute))
	progressive := invoiceFixtureMinimal(e, p, at(2*time.Minute))
	progressive.Type = invoice.TypeProgressiveBilling
	invoiceFixtureCreate(e, inv1, inv2, progressive)
	cp := couponFixtureFixed(e, p.org, "credit-coupon")
	ac := coupon.Apply(cp, core.NewID[core.AppliedCouponID](e.ids), p.customers[0], coupon.Overrides{}, now)
	cn := &creditnote.CreditNote{
		ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: p.org, InvoiceID: inv2.ID, CustomerID: p.customers[0],
		Status: creditnote.StatusDraft, Reason: creditnote.ReasonOther, Currency: invoiceEUR, CreatedAt: now, UpdatedAt: now,
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Coupons.Create()", r.Coupons.Create(ctx, cp))
		noErr(t, "AppliedCoupons.Create()", r.AppliedCoupons.Create(ctx, ac))
		noErr(t, "CreditNotes.Create()", r.CreditNotes.Create(ctx, cn))
		return nil
	})
	mk := func(inv core.InvoiceID, created time.Time, amount int64) *invoice.Credit {
		return &invoice.Credit{
			ID: core.NewID[core.InvoiceCreditID](e.ids), OrganizationID: p.org, InvoiceID: inv,
			Amount: amount, Currency: invoiceEUR, CreatedAt: created,
		}
	}
	// Created out of creation-instant order: lists are in creation order.
	byCoupon := mk(inv1.ID, at(2*time.Minute), 100)
	byCoupon.AppliedCouponID, byCoupon.BeforeTaxes = ac.ID, true
	byCreditNote := mk(inv1.ID, at(time.Minute), 250)
	byCreditNote.CreditNoteID = cn.ID
	byProgressive := mk(inv1.ID, at(3*time.Minute), 0)
	byProgressive.ProgressiveBillingInvoiceID, byProgressive.BeforeTaxes = progressive.ID, true
	other := mk(inv2.ID, now, 50)
	other.AppliedCouponID = ac.ID
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, c := range []*invoice.Credit{byCoupon, byCreditNote, byProgressive, other} {
			noErr(t, "Create()", r.Credits.Create(ctx, c))
		}
		wantErr(t, "Create() same ID", r.Credits.Create(ctx, byCoupon), core.ErrConflict)
		got, err := r.Credits.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice()", err)
		equal(t, "ListByInvoice()", got, []*invoice.Credit{byCreditNote, byCoupon, byProgressive})
		got, err = r.Credits.ListByAppliedCoupon(ctx, p.org, ac.ID)
		noErr(t, "ListByAppliedCoupon()", err)
		equal(t, "ListByAppliedCoupon()", got, []*invoice.Credit{other, byCoupon})
		got, err = r.Credits.ListByCreditNote(ctx, p.org, cn.ID)
		noErr(t, "ListByCreditNote()", err)
		equal(t, "ListByCreditNote()", got, []*invoice.Credit{byCreditNote})
		got, err = r.Credits.ListByProgressiveBillingInvoice(ctx, p.org, progressive.ID)
		noErr(t, "ListByProgressiveBillingInvoice()", err)
		equal(t, "ListByProgressiveBillingInvoice()", got, []*invoice.Credit{byProgressive})
		got, err = r.Credits.ListByProgressiveBillingInvoice(ctx, p.org, inv2.ID)
		noErr(t, "ListByProgressiveBillingInvoice() none", err)
		equal(t, "ListByProgressiveBillingInvoice() none", len(got), 0)

		noErr(t, "DeleteByInvoice()", r.Credits.DeleteByInvoice(ctx, p.org, inv1.ID))
		got, err = r.Credits.ListByInvoice(ctx, p.org, inv1.ID)
		noErr(t, "ListByInvoice() deleted", err)
		equal(t, "ListByInvoice() deleted", len(got), 0)
		got, err = r.Credits.ListByAppliedCoupon(ctx, p.org, ac.ID)
		noErr(t, "ListByAppliedCoupon() after a delete", err)
		equal(t, "ListByAppliedCoupon() after a delete", got, []*invoice.Credit{other})
		return nil
	})
	if h.Name != "memstore" { // memstore has no referential integrity nor checks
		orphan := mk(core.NewID[core.InvoiceID](e.ids), now, 1)
		orphan.AppliedCouponID = ac.ID
		wantErr(t, "Create() unknown invoice", e.txErr(func(ctx context.Context, r port.Repos) error {
			return r.Credits.Create(ctx, orphan)
		}), core.ErrNotFound)
	}
}
