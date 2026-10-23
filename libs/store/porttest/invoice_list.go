package porttest

import (
	"cmp"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// invoiceListSkipped are the cases of the invoice list spec that are not
// ported.
var invoiceListSkipped = []skippedCase{
	{"spec/queries/invoices_query_spec.rb:136", "offset pagination (page, total_count); the cursor pagination is checked by every case paging two by two"},
	{"spec/queries/invoices_query_spec.rb:350", "an invalid issuing_date_from string is a parsing error of the API layer; Go filters are typed (core.Date)"},
	{"spec/queries/invoices_query_spec.rb:373", "an invalid issuing_date_to string is a parsing error of the API layer; Go filters are typed (core.Date)"},
	{"spec/queries/invoices_query_spec.rb:628", "amounts given as strings or decimals are cast by the Rails parameter handling; Go filters are typed (int64 minor units)"},
	{"spec/queries/invoices_query_spec.rb:895", "settlements given as a string instead of an array is a JSON typing error; Go filters are typed"},
}

// invoiceListToday is the day the invoice list fixture is built on.
var invoiceListToday = core.NewDate(now.Year(), now.Month(), now.Day())

// invoiceListFixture is the data set of spec/queries/invoices_query_spec.rb.
type invoiceListFixture struct {
	org                                        core.OrganizationID
	entity, entity2                            *organization.BillingEntity
	rick, morty                                *customer.Customer
	first, second, third, fourth, fifth, sixth *invoice.Invoice
	all                                        []*invoice.Invoice
	n                                          int
}

// newInvoiceListFixture creates the two customers and the six invoices of
// the spec (and an invoice of another organization that no list may
// return); edit adjusts the invoices before they are stored.
func newInvoiceListFixture(e *env, edit func(f *invoiceListFixture)) *invoiceListFixture {
	e.t.Helper()
	a, b := e.twoOrgs()
	f := &invoiceListFixture{org: a.OrganizationID, entity: a}
	f.entity2 = organization.NewBillingEntity(f.org, core.NewID[core.BillingEntityID](e.ids), "second", "Second", now)
	f.rick = customer.New(f.org, core.NewID[core.CustomerID](e.ids), "c-first", a.ID, now)
	f.rick.Name, f.rick.Firstname, f.rick.Lastname = ptr("Rick Sanchez"), ptr("RickFirst"), ptr("SanchezLast")
	f.rick.Email = ptr("pickle@hotmail.com")
	f.morty = customer.New(f.org, core.NewID[core.CustomerID](e.ids), "c-second", a.ID, now)
	f.morty.Name, f.morty.Firstname, f.morty.Lastname = ptr("Morty Smith"), ptr("MortyFirst"), ptr("SmithLast")
	f.morty.Email = ptr("ilovejessica@gmail.com")
	yesterday := invoiceListToday.AddDays(-1)
	f.first = f.invoice(e, f.rick, "1111111111", invoiceListToday.AddDays(-7))
	f.first.PaymentStatus = core.PaymentStatusSucceeded
	f.second = f.invoice(e, f.morty, "2222222222", invoiceListToday.AddDays(-14))
	f.third = f.invoice(e, f.rick, "3333333333", invoiceListToday.AddDays(-21))
	f.third.PaymentStatus, f.third.PaymentOverdue = core.PaymentStatusFailed, true
	f.fourth = f.invoice(e, f.morty, "4444444444", yesterday)
	f.fourth.BillingEntityID, f.fourth.Status, f.fourth.Currency = f.entity2.ID, invoice.StatusDraft, money.MustCurrency("USD")
	f.fifth = f.invoice(e, f.rick, "5555555555", yesterday)
	f.fifth.BillingEntityID, f.fifth.Status, f.fifth.Type = f.entity2.ID, invoice.StatusDraft, invoice.TypeCredit
	f.sixth = f.invoice(e, f.rick, "6666666666", yesterday)
	f.sixth.BillingEntityID, f.sixth.PaymentDisputeLostAt = f.entity2.ID, ptr(at(-24*time.Hour))
	f.all = []*invoice.Invoice{f.first, f.second, f.third, f.fourth, f.fifth, f.sixth}
	if edit != nil {
		edit(f)
	}
	other := &invoice.Invoice{
		ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: b.OrganizationID, CustomerID: core.NewID[core.CustomerID](e.ids),
		BillingEntityID: b.ID, Type: invoice.TypeSubscription, Status: invoice.StatusFinalized,
		PaymentStatus: core.PaymentStatusPending, Number: "1111111111", Currency: invoiceEUR, CreatedAt: now, UpdatedAt: now,
	}
	otherCustomer := customer.New(b.OrganizationID, other.CustomerID, "c-first", b.ID, now)
	otherCustomer.Name = ptr("Rick Sanchez")
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(e.t, "BillingEntities.Create()", r.BillingEntities.Create(ctx, f.entity2))
		for _, c := range []*customer.Customer{f.rick, f.morty, otherCustomer} {
			noErr(e.t, "Customers.Create()", r.Customers.Create(ctx, c))
		}
		for _, inv := range append(slices.Clone(f.all), other) {
			noErr(e.t, "Invoices.Create()", r.Invoices.Create(ctx, inv))
		}
		return nil
	})
	return f
}

// invoice returns a finalized, pending EUR invoice of c issued on issued
// (zero for none), created a second after the previous one; it is not
// stored.
func (f *invoiceListFixture) invoice(e *env, c *customer.Customer, number string, issued core.Date) *invoice.Invoice {
	f.n++
	created := at(time.Duration(f.n) * time.Second)
	return &invoice.Invoice{
		ID: core.NewID[core.InvoiceID](e.ids), OrganizationID: f.org, CustomerID: c.ID, BillingEntityID: f.entity.ID,
		Type: invoice.TypeSubscription, Status: invoice.StatusFinalized, PaymentStatus: core.PaymentStatusPending,
		Number: number, Currency: invoiceEUR, TotalAmount: 100, IssuingDate: issued, CreatedAt: created, UpdatedAt: created,
	}
}

// create stores more invoices of the fixture.
func (f *invoiceListFixture) create(e *env, invs ...*invoice.Invoice) {
	e.t.Helper()
	invoiceFixtureCreate(e, invs...)
}

// invoiceListCase is one ported case: the filter and the invoices it
// lists, in creation order.
type invoiceListCase struct {
	name   string
	source string
	f      invoice.Filter
	want   []*invoice.Invoice
}

// invoiceListRecent returns invs in the core.OrderRecent order: issuing
// date, latest first (invoices without one first), then ID, newest first.
func invoiceListRecent(invs []*invoice.Invoice) []*invoice.Invoice {
	out := slices.Clone(invs)
	slices.SortStableFunc(out, func(a, b *invoice.Invoice) int {
		switch {
		case a.IssuingDate.IsZero() != b.IssuingDate.IsZero():
			if a.IssuingDate.IsZero() {
				return -1
			}
			return 1
		case a.IssuingDate != b.IssuingDate:
			return b.IssuingDate.Compare(a.IssuingDate)
		}
		return cmp.Compare(0, a.ID.Compare(b.ID))
	})
	return out
}

// checkInvoiceList lists with every case's filter, page by page (two
// items a page) in both orders, and compares the IDs.
func checkInvoiceList(t *testing.T, e *env, org core.OrganizationID, cases []invoiceListCase) {
	t.Helper()
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, tc := range cases {
			for _, order := range []core.ListOrder{core.OrderCreated, core.OrderRecent} {
				f := tc.f
				f.Order = order
				got := listAll(t, 2, func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
					return r.Invoices.List(ctx, org, f, p)
				})
				want := tc.want
				if order == core.OrderRecent {
					want = invoiceListRecent(want)
				}
				equal(t, "List() "+tc.name+" ("+order.String()+", "+tc.source+")", ids(got, invoiceID), ids(want, invoiceID))
			}
		}
		return nil
	})
}

func testInvoiceList(t *testing.T, h Harness) {
	const spec = "spec/queries/invoices_query_spec.rb:"
	checkSkipped(t, invoiceListSkipped)
	is := func(i ...*invoice.Invoice) []*invoice.Invoice { return i }

	t.Run("filters", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, nil)
		first, second, third, fourth, fifth, sixth := f.first, f.second, f.third, f.fourth, f.fifth, f.sixth
		ps := func(s ...core.PaymentStatus) []core.PaymentStatus { return s }
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"all invoices", spec + "101", invoice.Filter{}, f.all},
			{"draft status", spec + "150", invoice.Filter{Statuses: []invoice.Status{invoice.StatusDraft}}, is(fourth, fifth)},
			{"failed payment status", spec + "163", invoice.Filter{PaymentStatuses: ps(core.PaymentStatusFailed)}, is(third)},
			{
				"succeeded and failed payment statuses", spec + "176",
				invoice.Filter{PaymentStatuses: ps(core.PaymentStatusSucceeded, core.PaymentStatusFailed)},
				is(first, third),
			},
			{"payment dispute lost", spec + "189", invoice.Filter{PaymentDisputeLost: ptr(true)}, is(sixth)},
			{"payment dispute not lost", spec + "203", invoice.Filter{PaymentDisputeLost: ptr(false)}, is(first, second, third, fourth, fifth)},
			{"payment overdue", spec + "217", invoice.Filter{PaymentOverdue: ptr(true)}, is(third)},
			{"payment not overdue", spec + "225", invoice.Filter{PaymentOverdue: ptr(false)}, is(first, second, fourth, fifth, sixth)},
			{"credit type", spec + "296", invoice.Filter{Types: []invoice.Type{invoice.TypeCredit}}, is(fifth)},
			{"USD currency", spec + "304", invoice.Filter{Currency: money.MustCurrency("USD")}, is(fourth)},
			{"customer external ID", spec + "312", invoice.Filter{CustomerExternalID: f.morty.ExternalID}, is(second, fourth)},
			{
				"customer external ID searches the number", spec + "324",
				invoice.Filter{CustomerExternalID: f.morty.ExternalID, Search: "2222"},
				is(second),
			},
			{"customer", "", invoice.Filter{CustomerID: f.rick.ID}, is(first, third, fifth, sixth)},
			{"issuing date from", spec + "338", invoice.Filter{IssuingDateFrom: invoiceListToday.AddDays(-2)}, is(fourth, fifth, sixth)},
			{"issuing date to", spec + "361", invoice.Filter{IssuingDateTo: invoiceListToday.AddDays(-14)}, is(second, third)},
			{
				"issuing date from and to", spec + "384",
				invoice.Filter{IssuingDateFrom: invoiceListToday.AddDays(-14), IssuingDateTo: invoiceListToday.AddDays(-7)},
				is(first, second),
			},
			{"search full ID", spec + "410", invoice.Filter{Search: fourth.ID.String()}, is(fourth)},
			{"search partial ID", spec + "418", invoice.Filter{Search: fourth.ID.String()[:10]}, nil},
			{"search ID-like string", spec + "426", invoice.Filter{Search: "abcdef12"}, nil},
			{"search number", spec + "435", invoice.Filter{Search: first.Number}, is(first)},
			{"search customer external ID", spec + "448", invoice.Filter{Search: f.morty.ExternalID}, is(second, fourth)},
			{"search customer name", spec + "461", invoice.Filter{Search: "rick"}, is(first, third, fifth, sixth)},
			{"search customer email", spec + "475", invoice.Filter{Search: "gmail"}, is(second, fourth)},
			{"search number of a customer", spec + "488", invoice.Filter{CustomerID: f.morty.ID, Search: "44444444"}, is(fourth)},
			{"customer without invoices", spec + "502", invoice.Filter{CustomerID: core.NewID[core.CustomerID](e.ids)}, nil},
			{"search customer last name", spec + "514", invoice.Filter{Search: "SanchezLast"}, is(first, third, fifth, sixth)},
			{"search customer first name", spec + "528", invoice.Filter{Search: "MortyFirst"}, is(second, fourth)},
			{
				"search with a customer filter matches the number only", spec + "579",
				invoice.Filter{CustomerID: f.rick.ID, Search: "Rick"},
				nil,
			},
			{"search with LIKE wildcards", "", invoice.Filter{Search: "1_1"}, nil},
			{"search trimmed", "", invoice.Filter{Search: "  MortyFirst  "}, is(second, fourth)},
			{"billing entities", spec + "819", invoice.Filter{BillingEntityIDs: []core.BillingEntityID{f.entity.ID}}, is(first, second, third)},
			{"billing entity", "", invoice.Filter{BillingEntityID: f.entity2.ID}, is(fourth, fifth, sixth)},
			{"IDs", "", invoice.Filter{IDs: []core.InvoiceID{second.ID, sixth.ID}}, is(second, sixth)},
			{"positive due amount", "", invoice.Filter{PositiveDueAmount: ptr(true)}, f.all},
			{"no due amount", "", invoice.Filter{PositiveDueAmount: ptr(false)}, nil},
			{"finalized status", spec + "951", invoice.Filter{Statuses: []invoice.Status{invoice.StatusFinalized}}, is(first, second, third, sixth)},
		})
	})

	t.Run("search across numbers and customers", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, func(f *invoiceListFixture) { f.rick.LegalName = ptr("Sanchez Holdings") })
		byNumber := f.invoice(e, f.morty, "RICK-001", invoiceListToday.AddDays(-1))
		f.create(e, byNumber)
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"number and customer name", spec + "542", invoice.Filter{Search: "Rick"}, is(f.first, f.third, f.fifth, f.sixth, byNumber)},
			{"customer legal name", spec + "559", invoice.Filter{Search: "Sanchez Holdings"}, is(f.first, f.third, f.fifth, f.sixth)},
		})
	})

	t.Run("purchase order number", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, func(f *invoiceListFixture) { f.first.PurchaseOrderNumber = ptr("PO-4242") })
		withPO := f.invoice(e, f.rick, "WITH-PO", invoiceListToday)
		withPO.PurchaseOrderNumber = ptr("PO-123")
		f.create(e, withPO)
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"search only matching a purchase order number", spec + "569", invoice.Filter{Search: "PO-4242"}, is(f.first)},
			{"purchase order number, case-insensitive", spec + "844", invoice.Filter{PurchaseOrderNumber: "po-123"}, is(withPO)},
			{
				"purchase order number not searched with a customer filter", "app/queries/invoices_query.rb:81",
				invoice.Filter{Search: "PO-4242", CustomerID: f.rick.ID},
				nil,
			},
			{"unknown purchase order number", "", invoice.Filter{PurchaseOrderNumber: "PO-1"}, nil},
		})
	})

	t.Run("partially paid", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, func(f *invoiceListFixture) {
			f.first.TotalAmount, f.first.TotalPaidAmount = 2000, 2000
			f.second.TotalAmount, f.second.TotalPaidAmount = 2000, 1500
		})
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"partially paid", spec + "267", invoice.Filter{PartiallyPaid: ptr(true)}, is(f.second)},
			{"not partially paid", spec + "277", invoice.Filter{PartiallyPaid: ptr(false)}, is(f.first, f.third, f.fourth, f.fifth, f.sixth)},
			{"partially paid unset", spec + "287", invoice.Filter{}, f.all},
			{"no due amount", "", invoice.Filter{PositiveDueAmount: ptr(false)}, is(f.first)},
		})
	})

	t.Run("self billed", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, func(f *invoiceListFixture) { f.first.SelfBilled = true })
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"self billed", spec + "791", invoice.Filter{SelfBilled: ptr(true)}, is(f.first)},
			{"not self billed", spec + "799", invoice.Filter{SelfBilled: ptr(false)}, is(f.second, f.third, f.fourth, f.fifth, f.sixth)},
			{"self billed unset", spec + "808", invoice.Filter{}, f.all},
		})
	})

	t.Run("amounts", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, func(f *invoiceListFixture) { f.all = nil })
		var invs []*invoice.Invoice
		for i := range 5 {
			inv := f.invoice(e, f.rick, "", invoiceListToday)
			inv.TotalAmount = int64(i+2) * 1000
			invs = append(invs, inv)
		}
		f.create(e, invs...)
		amount := func(from, to *int64) invoice.Filter { return invoice.Filter{AmountFrom: from, AmountTo: to} }
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"amount from", spec + "598", amount(ptr(invs[1].TotalAmount), nil), invs[1:]},
			{"amount to", spec + "608", amount(ptr(int64(100)), ptr(invs[3].TotalAmount)), invs[:4]},
			{"amount from and to", spec + "618", amount(ptr(invs[1].TotalAmount), ptr(invs[3].TotalAmount)), invs[1:4]},
		})
	})

	t.Run("metadata", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, func(f *invoiceListFixture) {
			for _, inv := range f.all {
				inv.Metadata = core.Metadata{{Key: "red", Value: "5"}}
			}
		})
		mk := func(md core.Metadata) *invoice.Invoice {
			inv := f.invoice(e, f.morty, "", invoiceListToday)
			inv.Metadata = md
			return inv
		}
		bare := mk(nil)
		orange := mk(core.Metadata{{Key: "orange", Value: "3"}})
		redOrange := mk(core.Metadata{{Key: "red", Value: "5"}, {Key: "orange", Value: "3"}})
		redPink := mk(core.Metadata{{Key: "red", Value: "5"}, {Key: "pink", Value: "7"}})
		redOrangeGreen := mk(core.Metadata{{Key: "red", Value: "5"}, {Key: "orange", Value: "3"}, {Key: "green", Value: "1"}})
		f.create(e, bare, orange, redOrange, redPink, redOrangeGreen)
		md := func(m core.MetadataFilter) invoice.Filter { return invoice.Filter{Metadata: m} }
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"value present", spec + "643", md(core.MetadataFilter{"red": "5"}), append(slices.Clone(f.all), redOrange, redPink, redOrangeGreen)},
			{"two values present", "", md(core.MetadataFilter{"red": "5", "orange": "3"}), is(redOrange, redOrangeGreen)},
			{"value absent", spec + "661", md(core.MetadataFilter{"red": ""}), is(bare, orange)},
			{"several values", spec + "690", md(core.MetadataFilter{"red": "5", "orange": "3", "green": ""}), is(redOrange)},
			{"other value", "", md(core.MetadataFilter{"red": "6"}), nil},
		})
	})

	t.Run("combined filters", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, nil)
		inv := f.invoice(e, f.rick, "COMBI-0001", invoiceListToday)
		inv.TotalAmount, inv.PaymentStatus = 4200, core.PaymentStatusSucceeded
		inv.Metadata = core.Metadata{{Key: "po", Value: "PO-1"}}
		usd := f.invoice(e, f.rick, "COMBI-0002", invoiceListToday)
		usd.TotalAmount, usd.PaymentStatus, usd.Currency = 4200, core.PaymentStatusSucceeded, money.MustCurrency("USD")
		usd.Metadata = inv.Metadata
		f.create(e, inv, usd)
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"every filter", spec + "730", invoice.Filter{
				Currency: inv.Currency, CustomerExternalID: f.rick.ExternalID, CustomerID: f.rick.ID,
				Types: []invoice.Type{inv.Type}, IssuingDateFrom: inv.IssuingDate, IssuingDateTo: inv.IssuingDate,
				Statuses: []invoice.Status{inv.Status}, PaymentStatuses: []core.PaymentStatus{inv.PaymentStatus},
				PaymentDisputeLost: ptr(false), PaymentOverdue: ptr(false),
				AmountFrom: ptr(inv.TotalAmount), AmountTo: ptr(inv.TotalAmount),
				Metadata: core.MetadataFilter{"po": "PO-1"}, Search: inv.Number[:5],
			}, is(inv)},
		})
	})

	t.Run("subscriptions", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, be := e.org("Acme")
		p := invoiceFixtureParents(e, be)
		a := invoiceFixtureMinimal(e, p, now)
		b := invoiceFixtureMinimal(e, p, at(time.Second))
		c := invoiceFixtureMinimal(e, p, at(2*time.Second))
		invoiceFixtureCreate(e, a, b, c)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for i, inv := range []*invoice.Invoice{a, b} {
				// Two rows of one invoice: the filter still lists it once.
				for j := range 2 {
					noErr(t, "InvoiceSubscriptions.Create()", r.InvoiceSubscriptions.Create(ctx, &invoice.Subscription{
						ID: core.NewID[core.InvoiceSubscriptionID](e.ids), OrganizationID: p.org, InvoiceID: inv.ID,
						SubscriptionID: p.subscriptions[i], Reason: invoice.InvoicingReasonSubscriptionPeriodic,
						Period:    period.Period{From: at(time.Duration(j) * time.Hour), To: at(time.Duration(j+1) * time.Hour)},
						Timestamp: at(time.Duration(j) * time.Hour), CreatedAt: now,
					}))
				}
			}
			return nil
		})
		sub := func(s ...core.SubscriptionID) invoice.Filter { return invoice.Filter{SubscriptionIDs: s} }
		checkInvoiceList(t, e, p.org, []invoiceListCase{
			{"subscription", spec + "828", sub(p.subscriptions[0]), is(a)},
			{"subscriptions", "", sub(p.subscriptions[0], p.subscriptions[1]), is(a, b)},
			{"unknown subscription", "", sub(core.NewID[core.SubscriptionID](e.ids)), nil},
		})
	})

	t.Run("settlements", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, nil)
		e.tx(func(ctx context.Context, r port.Repos) error {
			// settle settles inv by a new payment or credit note.
			settle := func(inv *invoice.Invoice, typ invoice.SettlementType) {
				s := &invoice.Settlement{
					ID: core.NewID[core.InvoiceSettlementID](e.ids), OrganizationID: f.org, InvoiceID: inv.ID, Type: typ,
					Amount: 10, Currency: inv.Currency, CreatedAt: now,
				}
				if typ == invoice.SettlementTypePayment {
					p := &payment.Payment{
						ID: core.NewID[core.PaymentID](e.ids), OrganizationID: f.org, Payable: payment.InvoicePayable(inv.ID),
						CustomerID: inv.CustomerID, Kind: payment.KindManual, Status: payment.StateSucceeded, Amount: 10,
						Currency: inv.Currency, CreatedAt: now, UpdatedAt: now,
					}
					noErr(t, "Payments.Create()", r.Payments.Create(ctx, p))
					s.PaymentID = p.ID
				} else {
					cn := &creditnote.CreditNote{
						ID: core.NewID[core.CreditNoteID](e.ids), OrganizationID: f.org, InvoiceID: inv.ID, CustomerID: inv.CustomerID,
						Status: creditnote.StatusDraft, Reason: creditnote.ReasonOther, Currency: inv.Currency,
						CreatedAt: now, UpdatedAt: now,
					}
					noErr(t, "CreditNotes.Create()", r.CreditNotes.Create(ctx, cn))
					s.CreditNoteID = cn.ID
				}
				noErr(t, "InvoiceSettlements.Create()", r.InvoiceSettlements.Create(ctx, s))
			}
			settle(f.first, invoice.SettlementTypeCreditNote)
			settle(f.second, invoice.SettlementTypePayment)
			// A second settlement of one invoice: still listed once.
			settle(f.second, invoice.SettlementTypePayment)
			settle(f.third, invoice.SettlementTypeCreditNote)
			return nil
		})
		st := func(s ...invoice.SettlementType) invoice.Filter { return invoice.Filter{Settlements: s} }
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"credit note settlements", spec + "879", st(invoice.SettlementTypeCreditNote), is(f.first, f.third)},
			{"payment settlements", spec + "887", st(invoice.SettlementTypePayment), is(f.second)},
			{"several settlement types", spec + "903", st(invoice.SettlementTypeCreditNote, invoice.SettlementTypePayment), is(f.first, f.second, f.third)},
			{"no matching settlement", spec + "911", invoice.Filter{Settlements: []invoice.SettlementType{invoice.SettlementTypePayment}, CustomerID: f.rick.ID}, nil},
		})
	})

	t.Run("invisible statuses", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, nil)
		mk := func(s invoice.Status, number string) *invoice.Invoice {
			inv := f.invoice(e, f.rick, number, core.Date{})
			inv.Status = s
			return inv
		}
		generating := mk(invoice.StatusGenerating, "GEN-1")
		open := mk(invoice.StatusOpen, "OPEN-1")
		closed := mk(invoice.StatusClosed, "CLOSED-1")
		f.create(e, generating, open, closed)
		all := append(slices.Clone(f.all), open, closed)
		checkInvoiceList(t, e, f.org, []invoiceListCase{
			{"listed statuses", spec + "934", invoice.Filter{}, f.all},
			{"listed statuses with a customer search", spec + "943", invoice.Filter{Search: "Rick"}, is(f.first, f.third, f.fifth, f.sixth)},
			{
				"a listed status", spec + "951",
				invoice.Filter{Statuses: []invoice.Status{invoice.StatusFinalized}},
				is(f.first, f.second, f.third, f.sixth),
			},
			{"unlisted statuses", "", invoice.Filter{IncludeUnlisted: true}, all},
			{"an unlisted status", "", invoice.Filter{IncludeUnlisted: true, Statuses: []invoice.Status{invoice.StatusOpen}}, is(open)},
			{"generating invoices", "", invoice.Filter{IncludeGenerating: true, Statuses: []invoice.Status{invoice.StatusGenerating}}, is(generating)},
			{"listed and generating invoices", "", invoice.Filter{IncludeGenerating: true}, append(slices.Clone(f.all), generating)},
		})
	})

	t.Run("ordering", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newInvoiceListFixture(e, nil)
		// Invoices issued on the same day are ordered by ID
		// (spec/queries/invoices_query_spec.rb:112): consistently, page
		// after page; invoices without an issuing date come first.
		twin := f.invoice(e, f.morty, "TWIN", f.first.IssuingDate)
		twin.CreatedAt = f.first.CreatedAt
		undated := f.invoice(e, f.morty, "", core.Date{})
		undated2 := f.invoice(e, f.rick, "", core.Date{})
		f.create(e, twin, undated, undated2)
		all := append(slices.Clone(f.all), twin, undated, undated2)
		recent := []*invoice.Invoice{undated2, undated, f.sixth, f.fifth, f.fourth, twin, f.first, f.second, f.third}
		equal(t, "invoiceListRecent()", ids(invoiceListRecent(all), invoiceID), ids(recent, invoiceID))
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, size := range []int{1, 2, 4, 10} {
				got := listAll(t, size, func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
					return r.Invoices.List(ctx, f.org, invoice.Filter{Order: core.OrderRecent}, p)
				})
				equal(t, "List() latest issued first", ids(got, invoiceID), ids(recent, invoiceID))
				got = listAll(t, size, func(p core.PageRequest) (core.Page[*invoice.Invoice], error) {
					return r.Invoices.List(ctx, f.org, invoice.Filter{}, p)
				})
				equal(t, "List() oldest first", ids(got, invoiceID), ids(all, invoiceID))
			}
			return nil
		})
	})

	t.Run("invalid filters", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, a := e.org("Acme")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, tc := range []struct {
				name, source string
				f            invoice.Filter
				field        string
			}{
				{"settlement type", spec + "923", invoice.Filter{Settlements: []invoice.SettlementType{"invalid_type"}}, "settlements[0]"},
				{"status", "app/contracts/queries/invoices_query_filters_contract.rb", invoice.Filter{Statuses: []invoice.Status{invoice.StatusDraft, "random"}}, "statuses[1]"},
				{
					"unlisted status", "app/contracts/queries/invoices_query_filters_contract.rb",
					invoice.Filter{Statuses: []invoice.Status{invoice.StatusOpen}},
					"statuses[0]",
				},
				{"generating status", "", invoice.Filter{Statuses: []invoice.Status{invoice.StatusGenerating}}, "statuses[0]"},
				{
					"payment status", "app/contracts/queries/invoices_query_filters_contract.rb",
					invoice.Filter{PaymentStatuses: []core.PaymentStatus{"random"}},
					"payment_statuses[0]",
				},
				{"type", "app/contracts/queries/invoices_query_filters_contract.rb", invoice.Filter{Types: []invoice.Type{"random"}}, "types[0]"},
				{"order", "", invoice.Filter{Order: 7}, "order"},
			} {
				_, err := r.Invoices.List(ctx, a.OrganizationID, tc.f, core.PageRequest{})
				wantErr(t, "List() invalid "+tc.name+" ("+tc.source+")", err, core.ErrInvalidValue)
				if !hasFieldError(err, tc.field) {
					t.Errorf("List() invalid %s error got = %v, want = a field error on %s", tc.name, err, tc.field)
				}
			}
			for _, order := range []core.ListOrder{core.OrderCreated, core.OrderRecent} {
				_, err := r.Invoices.List(ctx, a.OrganizationID, invoice.Filter{Order: order}, core.PageRequest{Token: "not-a-token"})
				wantErr(t, "List() with a malformed page token ("+order.String()+")", err, core.ErrInvalidValue)
			}
			badDate := core.Cursor{Key: "2025-13-45", ID: core.NewID[core.InvoiceID](e.ids).UUID()}.Encode()
			_, err := r.Invoices.List(ctx, a.OrganizationID, invoice.Filter{Order: core.OrderRecent}, core.PageRequest{Token: badDate})
			wantErr(t, "List() with a malformed issuing date in the page token", err, core.ErrInvalidValue)
			return nil
		})
	})
}
