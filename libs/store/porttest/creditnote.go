package porttest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var creditNoteEUR = money.MustCurrency("EUR")

// creditNoteParents are the rows of other aggregates the credit note tests
// point at, all in one organization.
type creditNoteParents struct {
	org       core.OrganizationID
	customers [2]core.CustomerID
	invoices  [2]core.InvoiceID
	// fees are fees of invoices[0].
	fees [3]core.FeeID
}

// creditNoteFixtureParents creates the parent rows of the credit note tests
// in the organization of be.
func creditNoteFixtureParents(e *env, be *organization.BillingEntity) creditNoteParents {
	e.t.Helper()
	p := creditNoteParents{org: be.OrganizationID}
	e.tx(func(ctx context.Context, r port.Repos) error {
		for i := range p.customers {
			c := customer.New(p.org, core.NewID[core.CustomerID](e.ids), fmt.Sprintf("cn-cust-%d", i), be.ID, now)
			if err := r.Customers.Create(ctx, c); err != nil {
				return fmt.Errorf("customer: %w", err)
			}
			p.customers[i] = c.ID
		}
		for i := range p.invoices {
			inv := &invoice.Invoice{
				ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: p.org, CustomerID: p.customers[0], BillingEntityID: be.ID,
				Type: invoice.TypeOneOff, Status: invoice.StatusFinalized, PaymentStatus: core.PaymentStatusSucceeded,
				Number: fmt.Sprintf("CN-INV-%03d", i+1), Currency: creditNoteEUR, TotalAmount: 10000,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := r.Invoices.Create(ctx, inv); err != nil {
				return fmt.Errorf("invoice: %w", err)
			}
			p.invoices[i] = inv.ID
		}
		for i := range p.fees {
			f := &invoice.Fee{
				ID: core.NewID[core.FeeID](e.ids), OrganizationID: p.org, InvoiceID: p.invoices[0], Type: core.FeeTypeAddOn,
				Amount: 1000, Currency: creditNoteEUR, CreatedAt: now, UpdatedAt: now,
			}
			if err := r.Fees.Create(ctx, f); err != nil {
				return fmt.Errorf("fee: %w", err)
			}
			p.fees[i] = f.ID
		}
		return nil
	})
	return p
}

// creditNoteFixtureMinimal returns a draft credit note of invoices[0] with
// only the required fields set.
func creditNoteFixtureMinimal(e *env, p creditNoteParents, created time.Time) *creditnote.CreditNote {
	return &creditnote.CreditNote{
		ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: p.org, InvoiceID: p.invoices[0], CustomerID: p.customers[0],
		Status: creditnote.StatusDraft, Reason: creditnote.ReasonOther, Currency: creditNoteEUR,
		CreatedAt: created, UpdatedAt: created,
	}
}

// creditNoteFixtureItem returns an item on a fee.
func creditNoteFixtureItem(e *env, fee core.FeeID, amount int64) creditnote.Item {
	return creditnote.Item{
		ID: core.NewID[core.CreditNoteItemID](e.ids), FeeID: fee, Amount: amount,
		PreciseAmount: money.MustParse(fmt.Sprintf("%d.0001", amount)),
	}
}

// creditNoteFixtureFull returns a finalized credit note with every field
// set.
func creditNoteFixtureFull(e *env, p creditNoteParents, number string) *creditnote.CreditNote {
	cn := creditNoteFixtureMinimal(e, p, now)
	cn.Number = number
	cn.SequentialID = 3
	cn.Status = creditnote.StatusFinalized
	cn.CreditStatus = creditnote.CreditStatusAvailable
	cn.RefundStatus = core.RefundStatusSucceeded
	cn.Reason = creditnote.ReasonDuplicatedCharge
	cn.Description = ptr("Charged twice")
	cn.CreditAmount, cn.RefundAmount, cn.OffsetAmount, cn.BalanceAmount = 1200, 600, 100, 1100
	cn.SubTotalExcludingTaxesAmount, cn.CouponsAdjustmentAmount = 1500, 50
	cn.PreciseCouponsAdjustmentAmount = money.MustParse("49.99999")
	cn.TaxesAmount, cn.PreciseTaxesAmount, cn.TaxesRate = 400, money.MustParse("400.0001"), money.MustParse("20.5")
	cn.TotalAmount = 1900
	cn.Items = []creditnote.Item{creditNoteFixtureItem(e, p.fees[0], 1000), creditNoteFixtureItem(e, p.fees[1], 500)}
	cn.AppliedTaxes = []creditnote.AppliedTax{
		{
			ID: core.NewID[core.CreditNoteAppliedTaxID](e.ids), TaxID: core.NewID[core.TaxID](e.ids), TaxCode: "vat", TaxName: "VAT",
			TaxDescription: ptr("French VAT"), TaxRate: money.MustParse("20.5"), BaseAmount: 1500, Amount: 400,
			PreciseAmount: money.MustParse("400.0001"),
		},
		{
			ID: core.NewID[core.CreditNoteAppliedTaxID](e.ids), TaxCode: "zero", TaxName: "Zero",
			TaxRate: money.MustParse("0"), PreciseAmount: money.MustParse("0"),
		},
	}
	cn.IssuingDate = core.NewDate(2025, time.March, 14)
	cn.Metadata = core.Metadata{{Key: "ticket", Value: "T-1"}}
	cn.File = core.BlobRef("credit_notes/" + number + ".pdf")
	cn.XMLFile = core.BlobRef("credit_notes/" + number + ".xml")
	cn.RefundedAt = ptr(at(time.Hour))
	cn.VoidedAt = ptr(at(2 * time.Hour))
	cn.UpdatedAt = at(3 * time.Hour)
	return cn
}

func creditNoteID(cn *creditnote.CreditNote) core.CreditNoteID { return cn.ID }

func testCreditNote(t *testing.T, h Harness) {
	t.Run("crud", func(t *testing.T) {
		t.Parallel()
		testCreditNoteCRUD(t, h)
	})
	t.Run("queries", func(t *testing.T) {
		t.Parallel()
		testCreditNoteQueries(t, h)
	})
	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		testCreditNoteDelete(t, h)
	})
	t.Run("organization isolation", func(t *testing.T) {
		t.Parallel()
		testCreditNoteIsolation(t, h)
	})
}

func testCreditNoteCRUD(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := creditNoteFixtureParents(e, be)
	full := creditNoteFixtureFull(e, p, "CN-0001")
	minimal := creditNoteFixtureMinimal(e, p, at(time.Minute))
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Create() full", r.CreditNotes.Create(ctx, full))
		noErr(t, "Create() minimal", r.CreditNotes.Create(ctx, minimal))
		wantErr(t, "Create() same ID", r.CreditNotes.Create(ctx, full), core.ErrConflict)
		dup := creditNoteFixtureFull(e, p, "CN-0001")
		wantErr(t, "Create() same number", r.CreditNotes.Create(ctx, dup), core.ErrConflict)
		// Empty numbers repeat.
		noErr(t, "Create() second empty number", r.CreditNotes.Create(ctx, creditNoteFixtureMinimal(e, p, now)))

		got, err := r.CreditNotes.Get(ctx, p.org, full.ID)
		noErr(t, "Get() after a conflict", err)
		equal(t, "Get() full", got, full)
		got, err = r.CreditNotes.Get(ctx, p.org, minimal.ID)
		noErr(t, "Get() minimal", err)
		equal(t, "Get() minimal", got, minimal)
		_, err = r.CreditNotes.Get(ctx, p.org, dup.ID)
		wantErr(t, "Get() of a refused create", err, core.ErrNotFound)
		_, err = r.CreditNotes.Get(ctx, p.org, core.NewID[core.CreditNoteID](e.ids))
		wantErr(t, "Get() unknown", err, core.ErrNotFound)

		items, err := r.CreditNotes.ListItemsByCreditNote(ctx, p.org, full.ID)
		noErr(t, "ListItemsByCreditNote()", err)
		equal(t, "ListItemsByCreditNote()", items, full.Items)
		items, err = r.CreditNotes.ListItemsByCreditNote(ctx, p.org, minimal.ID)
		noErr(t, "ListItemsByCreditNote() none", err)
		equal(t, "ListItemsByCreditNote() none", len(items), 0)
		_, err = r.CreditNotes.ListItemsByCreditNote(ctx, p.org, core.NewID[core.CreditNoteID](e.ids))
		wantErr(t, "ListItemsByCreditNote() unknown", err, core.ErrNotFound)
		taxes, err := r.CreditNotes.ListAppliedTaxes(ctx, p.org, full.ID)
		noErr(t, "ListAppliedTaxes()", err)
		equal(t, "ListAppliedTaxes()", taxes, full.AppliedTaxes)
		_, err = r.CreditNotes.ListAppliedTaxes(ctx, p.org, core.NewID[core.CreditNoteID](e.ids))
		wantErr(t, "ListAppliedTaxes() unknown", err, core.ErrNotFound)
		return nil
	})

	// Update replaces the items and taxes; then everything cleared.
	e.tx(func(ctx context.Context, r port.Repos) error {
		upd := *full
		upd.CreditStatus = creditnote.CreditStatusConsumed
		upd.RefundStatus = core.RefundStatusFailed
		upd.BalanceAmount = 0
		upd.Number = "CN-0002"
		upd.Items = []creditnote.Item{creditNoteFixtureItem(e, p.fees[2], 7), full.Items[0]}
		upd.AppliedTaxes = full.AppliedTaxes[1:]
		upd.UpdatedAt = at(4 * time.Hour)
		noErr(t, "Update()", r.CreditNotes.Update(ctx, &upd))
		got, err := r.CreditNotes.Get(ctx, p.org, full.ID)
		noErr(t, "Get() updated", err)
		equal(t, "Get() updated", got, &upd)

		taken := *minimal
		taken.Number = "CN-0003"
		noErr(t, "Update() number", r.CreditNotes.Update(ctx, &taken))
		clash := upd
		clash.Number = "CN-0003"
		clash.Items = nil
		wantErr(t, "Update() number taken", r.CreditNotes.Update(ctx, &clash), core.ErrConflict)
		got, err = r.CreditNotes.Get(ctx, p.org, full.ID)
		noErr(t, "Get() after an update conflict", err)
		equal(t, "Get() after an update conflict", got, &upd)

		cleared := creditNoteFixtureMinimal(e, p, now)
		cleared.ID = full.ID
		noErr(t, "Update() cleared", r.CreditNotes.Update(ctx, cleared))
		got, err = r.CreditNotes.Get(ctx, p.org, full.ID)
		noErr(t, "Get() cleared", err)
		equal(t, "Get() cleared", got, cleared)

		wantErr(t, "Update() unknown", r.CreditNotes.Update(ctx, creditNoteFixtureMinimal(e, p, now)), core.ErrNotFound)
		return nil
	})

	if h.Name != "memstore" { // memstore has no referential integrity
		bad := creditNoteFixtureMinimal(e, p, now)
		bad.Items = []creditnote.Item{creditNoteFixtureItem(e, core.NewID[core.FeeID](e.ids), 1)}
		wantErr(t, "Create() item on an unknown fee", e.txErr(func(ctx context.Context, r port.Repos) error {
			return r.CreditNotes.Create(ctx, bad)
		}), core.ErrNotFound)
	}
}

func testCreditNoteQueries(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := creditNoteFixtureParents(e, be)
	mk := func(created time.Time, mod func(*creditnote.CreditNote)) *creditnote.CreditNote {
		cn := creditNoteFixtureMinimal(e, p, created)
		mod(cn)
		return cn
	}
	avail := func(created time.Time, balance int64, mod func(*creditnote.CreditNote)) *creditnote.CreditNote {
		return mk(created, func(cn *creditnote.CreditNote) {
			cn.Status, cn.CreditStatus, cn.BalanceAmount, cn.CreditAmount = creditnote.StatusFinalized, creditnote.CreditStatusAvailable, balance, balance
			mod(cn)
		})
	}
	// Created out of ID order: the oldest is created last.
	a2 := avail(at(2*time.Hour), 300, func(cn *creditnote.CreditNote) {
		cn.SequentialID = 1
		cn.Items = []creditnote.Item{creditNoteFixtureItem(e, p.fees[0], 100), creditNoteFixtureItem(e, p.fees[0], 50)}
	})
	a1 := avail(at(time.Hour), 200, func(cn *creditnote.CreditNote) {
		cn.SequentialID = 2
		cn.Items = []creditnote.Item{creditNoteFixtureItem(e, p.fees[1], 200)}
	})
	empty := avail(at(3*time.Hour), 0, func(cn *creditnote.CreditNote) {
		cn.SequentialID = 3
		cn.CreditStatus = creditnote.CreditStatusConsumed
		cn.Reason = creditnote.ReasonOrderChange
	})
	voided := avail(at(4*time.Hour), 100, func(cn *creditnote.CreditNote) {
		cn.SequentialID = 4
		cn.CreditStatus = creditnote.CreditStatusVoided
		cn.Items = []creditnote.Item{creditNoteFixtureItem(e, p.fees[0], 1000)}
	})
	draft := mk(at(5*time.Hour), func(cn *creditnote.CreditNote) {
		cn.SequentialID = 5
		cn.BalanceAmount = 100
		cn.Items = []creditnote.Item{creditNoteFixtureItem(e, p.fees[1], 25)}
	})
	otherCust := avail(at(6*time.Hour), 100, func(cn *creditnote.CreditNote) {
		cn.CustomerID = p.customers[1]
		cn.InvoiceID = p.invoices[1]
		cn.SequentialID = 9
		cn.Reason = creditnote.ReasonFraudulentCharge
	})
	usd := avail(at(7*time.Hour), 100, func(cn *creditnote.CreditNote) {
		cn.Currency = money.MustCurrency("USD")
		cn.SequentialID = 6
	})
	all := []*creditnote.CreditNote{a2, a1, empty, voided, draft, otherCust, usd}
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, cn := range all {
			noErr(t, "Create()", r.CreditNotes.Create(ctx, cn))
		}
		return nil
	})
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, c := range []struct {
			name string
			f    creditnote.Filter
			want []*creditnote.CreditNote
		}{
			{"none", creditnote.Filter{}, all},
			{"customer", creditnote.Filter{CustomerID: p.customers[1]}, []*creditnote.CreditNote{otherCust}},
			{"invoice", creditnote.Filter{InvoiceID: p.invoices[0]}, []*creditnote.CreditNote{a2, a1, empty, voided, draft, usd}},
			{"statuses", creditnote.Filter{Statuses: []creditnote.Status{creditnote.StatusDraft}}, []*creditnote.CreditNote{draft}},
			{
				"credit statuses",
				creditnote.Filter{CreditStatuses: []creditnote.CreditStatus{creditnote.CreditStatusConsumed, creditnote.CreditStatusVoided}},
				[]*creditnote.CreditNote{empty, voided},
			},
			{"no credit status", creditnote.Filter{CreditStatuses: []creditnote.CreditStatus{""}}, []*creditnote.CreditNote{draft}},
			{
				"reasons",
				creditnote.Filter{Reasons: []creditnote.Reason{creditnote.ReasonOrderChange, creditnote.ReasonFraudulentCharge}},
				[]*creditnote.CreditNote{empty, otherCust},
			},
			{"all filters", creditnote.Filter{
				CustomerID: p.customers[0], InvoiceID: p.invoices[0], Statuses: []creditnote.Status{creditnote.StatusFinalized},
				CreditStatuses: []creditnote.CreditStatus{creditnote.CreditStatusAvailable}, Reasons: []creditnote.Reason{creditnote.ReasonOther},
			}, []*creditnote.CreditNote{a2, a1, usd}},
			{"no match", creditnote.Filter{Reasons: []creditnote.Reason{creditnote.ReasonProductUnsatisfactory}}, nil},
		} {
			got := listAll(t, 2, func(pr core.PageRequest) (core.Page[*creditnote.CreditNote], error) {
				return r.CreditNotes.List(ctx, p.org, c.f, pr)
			})
			equal(t, "List() by "+c.name, ids(got, creditNoteID), ids(c.want, creditNoteID))
		}
		page, err := r.CreditNotes.List(ctx, p.org, creditnote.Filter{}, core.PageRequest{Size: 2})
		noErr(t, "List() first page", err)
		equal(t, "List() first page with items", page.Items, all[:2])

		byInv, err := r.CreditNotes.ListByInvoice(ctx, p.org, p.invoices[0])
		noErr(t, "ListByInvoice()", err)
		equal(t, "ListByInvoice()", byInv, []*creditnote.CreditNote{a2, a1, empty, voided, draft, usd})

		av, err := r.CreditNotes.ListAvailable(ctx, p.org, p.customers[0], creditNoteEUR)
		noErr(t, "ListAvailable()", err)
		equal(t, "ListAvailable() oldest first", av, []*creditnote.CreditNote{a1, a2})
		av, err = r.CreditNotes.ListAvailable(ctx, p.org, p.customers[0], money.MustCurrency("USD"))
		noErr(t, "ListAvailable() USD", err)
		equal(t, "ListAvailable() USD", ids(av, creditNoteID), []core.CreditNoteID{usd.ID})
		av, err = r.CreditNotes.ListAvailable(ctx, p.org, core.NewID[core.CustomerID](e.ids), creditNoteEUR)
		noErr(t, "ListAvailable() no customer", err)
		equal(t, "ListAvailable() no customer", len(av), 0)

		// Voided credit notes do not count; drafts do; in the order asked,
		// nothing credited omitted.
		credited, err := r.CreditNotes.CreditedByFee(ctx, p.org, []core.FeeID{p.fees[2], p.fees[1], p.fees[0]})
		noErr(t, "CreditedByFee()", err)
		equal(t, "CreditedByFee()", credited, []creditnote.FeeCredit{{FeeID: p.fees[1], Amount: 225}, {FeeID: p.fees[0], Amount: 150}})
		credited, err = r.CreditNotes.CreditedByFee(ctx, p.org, nil)
		noErr(t, "CreditedByFee() none", err)
		equal(t, "CreditedByFee() none", len(credited), 0)

		n, err := r.CreditNotes.NextSequentialID(ctx, p.org, p.invoices[0])
		noErr(t, "NextSequentialID()", err)
		equal(t, "NextSequentialID()", n, int64(7))
		n, err = r.CreditNotes.NextSequentialID(ctx, p.org, core.NewID[core.InvoiceID](e.ids))
		noErr(t, "NextSequentialID() no credit note", err)
		equal(t, "NextSequentialID() no credit note", n, int64(1))
		return nil
	})
}

func testCreditNoteDelete(t *testing.T, h Harness) {
	e := newEnv(t, h)
	_, be := e.org("Acme")
	p := creditNoteFixtureParents(e, be)
	draft := creditNoteFixtureMinimal(e, p, now)
	draft.Items = []creditnote.Item{creditNoteFixtureItem(e, p.fees[0], 10)}
	draft.AppliedTaxes = []creditnote.AppliedTax{{
		ID: core.NewID[core.CreditNoteAppliedTaxID](e.ids), TaxCode: "vat", TaxName: "VAT",
		TaxRate: money.MustParse("20"), PreciseAmount: money.MustParse("2"),
	}}
	referenced := creditNoteFixtureMinimal(e, p, at(time.Minute))
	final := creditNoteFixtureFull(e, p, "CN-0001")
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, cn := range []*creditnote.CreditNote{draft, referenced, final} {
			if err := r.CreditNotes.Create(ctx, cn); err != nil {
				return err
			}
		}
		return r.InvoiceSettlements.Create(ctx, &invoice.Settlement{
			ID: core.NewID[core.InvoiceSettlementID](e.ids), OrganizationID: p.org, InvoiceID: p.invoices[1],
			Type: invoice.SettlementTypeCreditNote, CreditNoteID: referenced.ID, Amount: 10, Currency: creditNoteEUR, CreatedAt: now,
		})
	})
	e.tx(func(ctx context.Context, r port.Repos) error {
		wantErr(t, "Delete() unknown", r.CreditNotes.Delete(ctx, p.org, core.NewID[core.CreditNoteID](e.ids)), core.ErrNotFound)
		wantErr(t, "Delete() finalized", r.CreditNotes.Delete(ctx, p.org, final.ID), core.ErrConflict)
		if h.Name != "memstore" { // memstore has no referential integrity
			wantErr(t, "Delete() referenced by a settlement", r.CreditNotes.Delete(ctx, p.org, referenced.ID), core.ErrConflict)
			_, err := r.CreditNotes.Get(ctx, p.org, referenced.ID)
			noErr(t, "Get() after a refused delete", err)
		}
		noErr(t, "Delete()", r.CreditNotes.Delete(ctx, p.org, draft.ID))
		_, err := r.CreditNotes.Get(ctx, p.org, draft.ID)
		wantErr(t, "Get() deleted", err, core.ErrNotFound)
		_, err = r.CreditNotes.ListItemsByCreditNote(ctx, p.org, draft.ID)
		wantErr(t, "ListItemsByCreditNote() deleted", err, core.ErrNotFound)
		wantErr(t, "Delete() again", r.CreditNotes.Delete(ctx, p.org, draft.ID), core.ErrNotFound)
		// The deleted items no longer credit their fee.
		credited, err := r.CreditNotes.CreditedByFee(ctx, p.org, []core.FeeID{p.fees[0]})
		noErr(t, "CreditedByFee() after a delete", err)
		equal(t, "CreditedByFee() after a delete", credited, []creditnote.FeeCredit{{FeeID: p.fees[0], Amount: 1000}})
		return nil
	})
}

func testCreditNoteIsolation(t *testing.T, h Harness) {
	e := newEnv(t, h)
	a, b := e.twoOrgs()
	pa := creditNoteFixtureParents(e, a)
	pb := creditNoteFixtureParents(e, b)
	cnA := creditNoteFixtureFull(e, pa, "CN-0001")
	// The same number in another organization is allowed.
	cnB := creditNoteFixtureFull(e, pb, "CN-0001")
	cnA.Status = creditnote.StatusDraft
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Create() A", r.CreditNotes.Create(ctx, cnA))
		noErr(t, "Create() B", r.CreditNotes.Create(ctx, cnB))
		return nil
	})
	e.tx(func(ctx context.Context, r port.Repos) error {
		org := pb.org
		_, err := r.CreditNotes.Get(ctx, org, cnA.ID)
		wantErr(t, "Get() of another organization", err, core.ErrNotFound)
		page, err := r.CreditNotes.List(ctx, org, creditnote.Filter{}, core.PageRequest{})
		noErr(t, "List()", err)
		equal(t, "List()", ids(page.Items, creditNoteID), []core.CreditNoteID{cnB.ID})
		got, err := r.CreditNotes.ListByInvoice(ctx, org, pa.invoices[0])
		noErr(t, "ListByInvoice()", err)
		equal(t, "ListByInvoice()", len(got), 0)
		got, err = r.CreditNotes.ListAvailable(ctx, org, pa.customers[0], creditNoteEUR)
		noErr(t, "ListAvailable()", err)
		equal(t, "ListAvailable()", len(got), 0)
		_, err = r.CreditNotes.ListItemsByCreditNote(ctx, org, cnA.ID)
		wantErr(t, "ListItemsByCreditNote() of another organization", err, core.ErrNotFound)
		_, err = r.CreditNotes.ListAppliedTaxes(ctx, org, cnA.ID)
		wantErr(t, "ListAppliedTaxes() of another organization", err, core.ErrNotFound)
		credited, err := r.CreditNotes.CreditedByFee(ctx, org, []core.FeeID{pa.fees[0]})
		noErr(t, "CreditedByFee()", err)
		equal(t, "CreditedByFee()", len(credited), 0)
		n, err := r.CreditNotes.NextSequentialID(ctx, org, pa.invoices[0])
		noErr(t, "NextSequentialID()", err)
		equal(t, "NextSequentialID() of another organization", n, int64(1))
		upd := *cnA
		upd.OrganizationID = org
		upd.Items = nil
		wantErr(t, "Update() of another organization", r.CreditNotes.Update(ctx, &upd), core.ErrNotFound)
		wantErr(t, "Delete() of another organization", r.CreditNotes.Delete(ctx, org, cnA.ID), core.ErrNotFound)
		return nil
	})
	e.tx(func(ctx context.Context, r port.Repos) error {
		got, err := r.CreditNotes.Get(ctx, pa.org, cnA.ID)
		noErr(t, "Get()", err)
		equal(t, "Get() untouched", got, cnA)
		return nil
	})
}
