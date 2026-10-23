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

// creditNoteListSkipped are the cases of the credit note list spec that
// are not ported.
var creditNoteListSkipped = []skippedCase{
	{"spec/queries/credit_notes_query_spec.rb:34", "offset pagination (page, total_count); the cursor pagination is checked by every case paging two by two"},
	{"spec/queries/credit_notes_query_spec.rb:343", "an unparsable date string is a JSON typing error; Go filters take a core.Date"},
	{"spec/queries/credit_notes_query_spec.rb:366", "an unparsable date string is a JSON typing error; Go filters take a core.Date"},
}

// creditNoteListFixture is the data set of the ported cases of
// spec/queries/credit_notes_query_spec.rb, in one organization: two
// customers, three invoices of three billing entities and five finalized
// credit notes (cn[0] to cn[4], in creation order), plus a draft.
type creditNoteListFixture struct {
	org          core.OrganizationID
	be, be2, be3 core.BillingEntityID
	rick, morty  *customer.Customer
	inv          [3]*invoice.Invoice
	cn           [5]*creditnote.CreditNote
	draft        *creditnote.CreditNote
}

func newCreditNoteListFixture(e *env) creditNoteListFixture {
	e.t.Helper()
	a, b := e.twoOrgs()
	f := creditNoteListFixture{org: a.OrganizationID, be: a.ID}
	be2 := organization.NewBillingEntity(f.org, core.NewID[core.BillingEntityID](e.ids), "second", "Second", now)
	be3 := organization.NewBillingEntity(f.org, core.NewID[core.BillingEntityID](e.ids), "third", "Third", now)
	f.be2, f.be3 = be2.ID, be3.ID
	f.rick = customer.New(f.org, core.NewID[core.CustomerID](e.ids), "rick-ext", a.ID, now)
	f.rick.Name, f.rick.Firstname, f.rick.Lastname = ptr("Rick Sanchez"), ptr("Rick Ramon"), ptr("Sanchez Spencer")
	f.rick.Email = ptr("rick@citadel.test")
	f.morty = customer.New(f.org, core.NewID[core.CustomerID](e.ids), "morty-ext", a.ID, now)
	f.morty.Name, f.morty.Firstname, f.morty.Lastname = ptr("Morty Smith"), ptr("Morty Elias"), ptr("Smith Murray")
	f.morty.Email = ptr("morty@earth.test")
	// The legal name is not searched.
	f.morty.LegalName = ptr("Legalese Corp")
	mkInv := func(c *customer.Customer, be core.BillingEntityID, number string, selfBilled bool) *invoice.Invoice {
		return &invoice.Invoice{
			ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: f.org, CustomerID: c.ID, BillingEntityID: be,
			Type: invoice.TypeOneOff, Status: invoice.StatusFinalized, PaymentStatus: core.PaymentStatusSucceeded,
			Number: number, Currency: creditNoteEUR, TotalAmount: 10000, SelfBilled: selfBilled,
			CreatedAt: now, UpdatedAt: now,
		}
	}
	f.inv = [3]*invoice.Invoice{
		mkInv(f.rick, f.be, "INV-001", false),
		mkInv(f.rick, f.be2, "INV-002", true),
		mkInv(f.morty, f.be3, "FOO-01", false),
	}
	f.inv[0].PurchaseOrderNumber = ptr("PO-123")
	f.inv[2].PurchaseOrderNumber = ptr("PO-999")
	type spec struct {
		c                      *customer.Customer
		inv                    *invoice.Invoice
		currency               string
		reason                 creditnote.Reason
		credit                 creditnote.CreditStatus
		refund                 core.RefundStatus
		creditA, refundA, offA int64
	}
	specs := []spec{
		{f.rick, f.inv[0], "EUR", creditnote.ReasonDuplicatedCharge, creditnote.CreditStatusAvailable, core.RefundStatusPending, 10, 0, 0},
		{f.rick, f.inv[0], "USD", creditnote.ReasonOrderChange, creditnote.CreditStatusConsumed, core.RefundStatusSucceeded, 0, 10, 0},
		{f.rick, f.inv[1], "EUR", creditnote.ReasonOther, creditnote.CreditStatusVoided, core.RefundStatusFailed, 0, 0, 10},
		{f.morty, f.inv[2], "EUR", creditnote.ReasonFraudulentCharge, creditnote.CreditStatusAvailable, "", 10, 10, 0},
		{f.morty, f.inv[2], "EUR", creditnote.ReasonProductUnsatisfactory, creditnote.CreditStatusConsumed, core.RefundStatusPending, 10, 0, 10},
	}
	for i, s := range specs {
		created := at(time.Duration(i) * time.Second)
		cn := &creditnote.CreditNote{
			ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: f.org, InvoiceID: s.inv.ID, CustomerID: s.c.ID,
			Number: fmt.Sprintf("CN-%03d", i+1), SequentialID: int64(i + 1), Status: creditnote.StatusFinalized,
			CreditStatus: s.credit, RefundStatus: s.refund, Reason: s.reason,
			CreditAmount: s.creditA, RefundAmount: s.refundA, OffsetAmount: s.offA,
			TotalAmount: int64(i+2) * 1000, Currency: money.MustCurrency(s.currency),
			IssuingDate: core.NewDate(2025, time.March, i+1), CreatedAt: created, UpdatedAt: created,
		}
		f.cn[i] = cn
	}
	f.draft = &creditnote.CreditNote{
		ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: f.org, InvoiceID: f.inv[0].ID, CustomerID: f.rick.ID,
		Number: "CN-DRAFT", Status: creditnote.StatusDraft, Reason: creditnote.ReasonOther, Currency: creditNoteEUR,
		CreatedAt: at(10 * time.Second), UpdatedAt: at(10 * time.Second),
	}
	// A credit note of another organization that every search would match.
	otherCustomer := customer.New(b.OrganizationID, core.NewID[core.CustomerID](e.ids), "rick-ext", b.ID, now)
	otherCustomer.Name = ptr("Rick Sanchez")
	otherInv := &invoice.Invoice{
		ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: b.OrganizationID, CustomerID: otherCustomer.ID, BillingEntityID: b.ID,
		Type: invoice.TypeOneOff, Status: invoice.StatusFinalized, PaymentStatus: core.PaymentStatusSucceeded,
		Number: "INV-001", Currency: creditNoteEUR,
		CreatedAt: now, UpdatedAt: now,
	}
	otherCN := &creditnote.CreditNote{
		ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: b.OrganizationID, InvoiceID: otherInv.ID, CustomerID: otherCustomer.ID,
		Number: "CN-001", Status: creditnote.StatusFinalized, Reason: creditnote.ReasonDuplicatedCharge, Currency: creditNoteEUR,
		CreditStatus: creditnote.CreditStatusAvailable, CreditAmount: 10, TotalAmount: 2000,
		IssuingDate: core.NewDate(2025, time.March, 1), CreatedAt: now, UpdatedAt: now,
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(e.t, "BillingEntities.Create()", r.BillingEntities.Create(ctx, be2))
		noErr(e.t, "BillingEntities.Create()", r.BillingEntities.Create(ctx, be3))
		for _, c := range []*customer.Customer{f.rick, f.morty, otherCustomer} {
			noErr(e.t, "Customers.Create()", r.Customers.Create(ctx, c))
		}
		for _, inv := range append(f.inv[:], otherInv) {
			noErr(e.t, "Invoices.Create()", r.Invoices.Create(ctx, inv))
		}
		for _, cn := range append(append(f.cn[:], f.draft), otherCN) {
			noErr(e.t, "CreditNotes.Create()", r.CreditNotes.Create(ctx, cn))
		}
		return nil
	})
	return f
}

func testCreditNoteList(t *testing.T, h Harness) {
	const spec = "spec/queries/credit_notes_query_spec.rb:"
	const query = "app/queries/credit_notes_query.rb:"
	checkSkipped(t, creditNoteListSkipped)

	t.Run("filters", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newCreditNoteListFixture(e)
		cn := f.cn
		cns := func(c ...*creditnote.CreditNote) []*creditnote.CreditNote { return c }
		// finalized is the base filter of the API's list (lago lists only
		// finalized credit notes: app/queries/credit_notes_query.rb:85).
		finalized := func(fl creditnote.Filter) creditnote.Filter {
			fl.Statuses = []creditnote.Status{creditnote.StatusFinalized}
			return fl
		}
		eur := money.MustCurrency("EUR")
		for _, tc := range []struct {
			name, source string
			f            creditnote.Filter
			want         []*creditnote.CreditNote
		}{
			{"finalized credit notes", spec + "24", finalized(creditnote.Filter{}), cn[:]},
			{"every status", "", creditnote.Filter{}, append(cn[:], f.draft)},
			{"currency", spec + "50", finalized(creditnote.Filter{Currency: eur}), cns(cn[0], cn[2], cn[3], cn[4])},
			{"customer external ID", spec + "66", finalized(creditnote.Filter{CustomerExternalID: "rick-ext"}), cns(cn[0], cn[1], cn[2])},
			{"customer ID", spec + "82", finalized(creditnote.Filter{CustomerID: f.morty.ID}), cns(cn[3], cn[4])},
			{"invoice ID", "", creditnote.Filter{InvoiceID: f.inv[0].ID}, cns(cn[0], cn[1], f.draft)},
			{"reasons", spec + "106", finalized(creditnote.Filter{Reasons: []creditnote.Reason{creditnote.ReasonDuplicatedCharge, creditnote.ReasonOrderChange}}), cns(cn[0], cn[1])},
			{"type credit", spec + "151", finalized(creditnote.Filter{Types: []creditnote.Type{creditnote.TypeCredit}}), cns(cn[0], cn[3], cn[4])},
			{"type refund", spec + "160", finalized(creditnote.Filter{Types: []creditnote.Type{creditnote.TypeRefund}}), cns(cn[1], cn[3])},
			{"type offset", spec + "169", finalized(creditnote.Filter{Types: []creditnote.Type{creditnote.TypeOffset}}), cns(cn[2], cn[4])},
			{"types credit and refund", spec + "179", finalized(creditnote.Filter{Types: []creditnote.Type{creditnote.TypeCredit, creditnote.TypeRefund}}), cns(cn[0], cn[1], cn[3], cn[4])},
			{"type without match", spec + "201", finalized(creditnote.Filter{Types: []creditnote.Type{creditnote.TypeRefund}, InvoiceID: f.inv[1].ID}), nil},
			{"credit statuses", spec + "228", finalized(creditnote.Filter{CreditStatuses: []creditnote.CreditStatus{creditnote.CreditStatusAvailable, creditnote.CreditStatusConsumed}}), cns(cn[0], cn[1], cn[3], cn[4])},
			{"refund statuses", spec + "266", finalized(creditnote.Filter{RefundStatuses: []core.RefundStatus{core.RefundStatusPending, core.RefundStatusSucceeded}}), cns(cn[0], cn[1], cn[4])},
			{"invoice number", spec + "294", finalized(creditnote.Filter{InvoiceNumber: "INV-001"}), cns(cn[0], cn[1])},
			{"purchase order number, case-insensitive", spec + "313", finalized(creditnote.Filter{PurchaseOrderNumber: "po-123"}), cns(cn[0], cn[1])},
			{"unknown purchase order number", "", finalized(creditnote.Filter{PurchaseOrderNumber: "PO-1"}), nil},
			{"issuing date from", spec + "334", finalized(creditnote.Filter{IssuingDateFrom: cn[1].IssuingDate}), cns(cn[1], cn[2], cn[3], cn[4])},
			{"issuing date to", spec + "357", finalized(creditnote.Filter{IssuingDateTo: cn[3].IssuingDate}), cns(cn[0], cn[1], cn[2], cn[3])},
			{"issuing dates", spec + "378", finalized(creditnote.Filter{IssuingDateFrom: cn[1].IssuingDate, IssuingDateTo: cn[3].IssuingDate}), cns(cn[1], cn[2], cn[3])},
			{"undated draft outside a date range", "", creditnote.Filter{IssuingDateTo: cn[4].IssuingDate}, cn[:]},
			{"amount from", spec + "398", finalized(creditnote.Filter{AmountFrom: ptr(cn[1].TotalAmount)}), cns(cn[1], cn[2], cn[3], cn[4])},
			{"amount to", spec + "408", finalized(creditnote.Filter{AmountTo: ptr(cn[3].TotalAmount)}), cns(cn[0], cn[1], cn[2], cn[3])},
			{"amounts", spec + "418", finalized(creditnote.Filter{AmountFrom: ptr(cn[1].TotalAmount), AmountTo: ptr(cn[3].TotalAmount)}), cns(cn[1], cn[2], cn[3])},
			{"self-billed", spec + "446", finalized(creditnote.Filter{SelfBilled: ptr(true)}), cns(cn[2])},
			{"not self-billed", spec + "455", finalized(creditnote.Filter{SelfBilled: ptr(false)}), cns(cn[0], cn[1], cn[3], cn[4])},
			{"billing entity", spec + "483", finalized(creditnote.Filter{BillingEntityIDs: []core.BillingEntityID{f.be2}}), cns(cn[2])},
			{"billing entities", spec + "498", finalized(creditnote.Filter{BillingEntityIDs: []core.BillingEntityID{f.be2, f.be3}}), cns(cn[2], cn[3], cn[4])},
			// UUIDv7 IDs created together share their leading (time)
			// digits, so the term is taken from the random tail.
			{"search ID", spec + "512", finalized(creditnote.Filter{Search: cn[3].ID.String()[24:]}), cns(cn[3])},
			{"search number", spec + "527", finalized(creditnote.Filter{Search: "n-004"}), cns(cn[3])},
			{"search customer name", spec + "563", finalized(creditnote.Filter{Search: "Rick S"}), cns(cn[0], cn[1], cn[2])},
			{"search customer first name", spec + "601", finalized(creditnote.Filter{Search: "Morty E"}), cns(cn[3], cn[4])},
			{"search customer last name", spec + "639", finalized(creditnote.Filter{Search: "Smith Mu"}), cns(cn[3], cn[4])},
			{"search customer external ID", spec + "654", finalized(creditnote.Filter{Search: "rick-e"}), cns(cn[0], cn[1], cn[2])},
			{"search customer email", spec + "669", finalized(creditnote.Filter{Search: "morty@"}), cns(cn[3], cn[4])},
			{"search ignores the customer fields with a customer ID", query + "71", finalized(creditnote.Filter{CustomerID: f.morty.ID, Search: "Morty"}), nil},
			{"search number with a customer ID", query + "71", finalized(creditnote.Filter{CustomerID: f.morty.ID, Search: "CN-005"}), cns(cn[4])},
			{"search does not look at the legal name", "", finalized(creditnote.Filter{Search: "Legalese"}), nil},
			{"search with LIKE wildcards", "", finalized(creditnote.Filter{Search: "CN_00"}), nil},
			{"search trimmed", "", finalized(creditnote.Filter{Search: "  CN-002 "}), cns(cn[1])},
			{"multiple filters", spec + "699", finalized(creditnote.Filter{
				Search: "CN-00", Currency: eur, CustomerExternalID: "rick-ext", CustomerID: f.rick.ID,
				Reasons: []creditnote.Reason{creditnote.ReasonDuplicatedCharge}, CreditStatuses: []creditnote.CreditStatus{creditnote.CreditStatusAvailable},
				RefundStatuses: []core.RefundStatus{core.RefundStatusPending}, InvoiceNumber: "INV-001",
				IssuingDateFrom: cn[0].IssuingDate, IssuingDateTo: cn[0].IssuingDate,
				AmountFrom: ptr(cn[0].TotalAmount), AmountTo: ptr(cn[0].TotalAmount),
			}), cns(cn[0])},
		} {
			checkCreditNoteList(t, e, f.org, tc.name+" ("+tc.source+")", tc.f, tc.want)
		}
	})

	t.Run("invalid filters", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, a := e.org("Acme")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, tc := range []struct {
				name, source string
				f            creditnote.Filter
				field        string
			}{
				{"reason", spec + "115", creditnote.Filter{Reasons: []creditnote.Reason{"invalid-reason"}}, "reasons[0]"},
				{"type", spec + "188", creditnote.Filter{Types: []creditnote.Type{creditnote.TypeCredit, "invalid-type"}}, "types[1]"},
				{"credit status", spec + "237", creditnote.Filter{CreditStatuses: []creditnote.CreditStatus{"invalid-credit-status"}}, "credit_statuses[0]"},
				{"refund status", spec + "275", creditnote.Filter{RefundStatuses: []core.RefundStatus{"invalid-refund-status"}}, "refund_statuses[0]"},
				{"status", "", creditnote.Filter{Statuses: []creditnote.Status{"open"}}, "statuses[0]"},
				{"order", "", creditnote.Filter{Order: 7}, "order"},
			} {
				_, err := r.CreditNotes.List(ctx, a.OrganizationID, tc.f, core.PageRequest{})
				wantErr(t, "List() invalid "+tc.name+" ("+tc.source+")", err, core.ErrInvalidValue)
				if !hasFieldError(err, tc.field) {
					t.Errorf("List() invalid %s error got = %v, want = a field error on %s", tc.name, err, tc.field)
				}
			}
			for _, order := range []core.ListOrder{core.OrderCreated, core.OrderRecent} {
				_, err := r.CreditNotes.List(ctx, a.OrganizationID, creditnote.Filter{Order: order}, core.PageRequest{Token: "not-a-token"})
				wantErr(t, "List() with a malformed page token ("+order.String()+")", err, core.ErrInvalidValue)
			}
			return nil
		})
	})
}

// checkCreditNoteList lists with f page by page (two items a page) in both
// orders and compares the IDs with want (in creation order).
func checkCreditNoteList(t *testing.T, e *env, org core.OrganizationID, name string, f creditnote.Filter, want []*creditnote.CreditNote) {
	t.Helper()
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, order := range []core.ListOrder{core.OrderCreated, core.OrderRecent} {
			fl := f
			fl.Order = order
			got := listAll(t, 2, func(p core.PageRequest) (core.Page[*creditnote.CreditNote], error) {
				return r.CreditNotes.List(ctx, org, fl, p)
			})
			w := want
			if order == core.OrderRecent {
				w = reversed(w)
			}
			equal(t, "List() "+name+" ("+order.String()+")", ids(got, creditNoteID), ids(w, creditNoteID))
		}
		return nil
	})
}
