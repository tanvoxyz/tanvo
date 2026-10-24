package integrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// fakeTax is a tax adapter recording its calls. By default every line is
// taxed 5 cents of VAT at 2.5 %.
type fakeTax struct {
	provider integration.Provider

	mu        sync.Mutex
	quotes    []*integration.TaxTransaction
	reports   []*integration.TaxTransaction
	voids     []string
	negations [][2]string

	err       error
	voidErr   error
	negateErr error

	company    string
	companyErr error

	// lineTax, when set, is the tax of a line in place of 5 cents.
	lineTax func(integration.TaxLine) int64
	// breakdown, when set, is the breakdown of every line in place of 5
	// cents of VAT.
	breakdown []integration.TaxBreakdown
}

// FindCompany implements [integration.TaxCompanyFinder].
func (g *fakeTax) FindCompany(context.Context, integration.Connection) (string, error) {
	return g.company, g.companyErr
}

func (g *fakeTax) Provider() integration.Provider                  { return g.provider }
func (g *fakeTax) ValidateConnection(integration.Connection) error { return nil }

func (g *fakeTax) answer(t *integration.TaxTransaction) (*integration.TaxResult, error) {
	if g.err != nil {
		return nil, g.err
	}
	res := &integration.TaxResult{TransactionID: "txn_" + t.ID}
	rate := money.MustParse("0.025")
	for _, l := range t.Lines {
		amount := int64(5)
		if g.lineTax != nil {
			amount = g.lineTax(l)
		}
		bd := []integration.TaxBreakdown{{Name: "VAT", Type: "tax", Rate: &rate, TaxAmount: money.NewFromInt(amount)}}
		if g.breakdown != nil {
			bd = g.breakdown
		}
		res.Lines = append(res.Lines, integration.TaxLineResult{
			ItemKey: l.ItemKey, ItemID: l.ItemID, ItemCode: l.ItemCode,
			AmountCents: money.NewFromInt(l.AmountCents), TaxAmountCents: &amount, Breakdown: bd,
		})
	}
	return res, nil
}

func clone(t *integration.TaxTransaction) *integration.TaxTransaction {
	c := *t
	c.Lines = slices.Clone(t.Lines)
	return &c
}

func (g *fakeTax) QuoteTaxes(_ context.Context, _ integration.Connection, t *integration.TaxTransaction) (*integration.TaxResult, error) {
	g.mu.Lock()
	g.quotes = append(g.quotes, clone(t))
	g.mu.Unlock()
	return g.answer(t)
}

func (g *fakeTax) ReportTaxes(_ context.Context, _ integration.Connection, t *integration.TaxTransaction) (*integration.TaxResult, error) {
	g.mu.Lock()
	g.reports = append(g.reports, clone(t))
	g.mu.Unlock()
	return g.answer(t)
}

func (g *fakeTax) VoidTaxes(_ context.Context, _ integration.Connection, id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.voids = append(g.voids, id)
	return g.voidErr
}

func (g *fakeTax) NegateTaxes(_ context.Context, _ integration.Connection, id, negationID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.negations = append(g.negations, [2]string{id, negationID})
	return g.negateErr
}

// taxFixture is a customer linked to a tax integration whose fallback
// item is "1", with a charge of a plan it subscribed to.
type taxFixture struct {
	h      *billingtest.Harness
	svc    *integrations.Service
	taxes  *integrations.Taxes
	gw     *fakeTax
	integ  *integration.Integration
	cust   *customer.Customer
	ic     *integration.IntegrationCustomer
	sub    *subscription.Subscription
	charge *catalog.Charge
}

func newTaxFixture(t *testing.T, p integration.Provider, cache *integrations.DraftTaxCache) *taxFixture {
	t.Helper()
	ctx := context.Background()
	h := billingtest.New(t, now)
	gw := &fakeTax{provider: p}
	set, err := port.NewIntegrationProviderSet(gw)
	if err != nil {
		t.Fatal(err)
	}
	svc := integrations.New(h.Deps, sealer{}, integrations.WithProviders(set))
	in := integrations.Input{Code: string(p), Name: string(p), Provider: p}
	switch p {
	case integration.ProviderAnrok:
		in.Values = map[string]string{integrations.KeyAPIKey: "acct/key"}
	default:
		in.Values = map[string]string{
			integrations.KeyAccountID: "acc", integrations.KeyCompanyCode: "DEFAULT", integrations.KeyLicenseKey: "lic",
		}
	}
	integ, err := svc.Create(ctx, h.OrgID(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	c := h.Customer()
	ic := &integration.IntegrationCustomer{
		ID: core.NewID[core.IntegrationCustomerID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: integ.ID,
		CustomerID: c.ID, Category: integration.CategoryTax, SyncWithProvider: true,
		CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	one := "1"
	cm := &integration.CollectionMapping{
		ID: core.NewID[core.IntegrationCollectionMappingID](h.IDs), OrganizationID: h.OrgID(), IntegrationID: integ.ID,
		Type: integration.CollectionMappingTypeFallbackItem, ExternalID: &one, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error {
		if err := r.IntegrationCustomers.Create(ctx, ic); err != nil {
			return err
		}
		return r.IntegrationCollectionMappings.Create(ctx, cm)
	})
	m := h.Metric("api", catalog.AggregationTypeCount, "")
	plan := h.Plan(period.Monthly, 0)
	ch := h.Charge(plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
	return &taxFixture{
		h: h, svc: svc, taxes: integrations.NewTaxes(svc, cache), gw: gw, integ: integ, cust: c, ic: ic,
		sub: h.Subscription(c, plan), charge: ch,
	}
}

// fee stores a fee of amount on inv, created at at with the ID id when
// not empty.
func (f *taxFixture) fee(inv *invoice.Invoice, amount int64, opts ...func(*invoice.Fee)) *invoice.Fee {
	return f.h.StoredFee(inv, amount, opts...)
}

func (f *taxFixture) chargeFee(inv *invoice.Invoice, amount int64, opts ...func(*invoice.Fee)) *invoice.Fee {
	return f.fee(inv, amount, append([]func(*invoice.Fee){billingtest.ChargeFee(f.sub, f.charge.ID)}, opts...)...)
}

func withID(id string) func(*invoice.Fee) {
	return func(fe *invoice.Fee) { fe.ID = core.MustParseID[core.FeeID](id) }
}

func createdAt(t time.Time) func(*invoice.Fee) {
	return func(fe *invoice.Fee) { fe.CreatedAt = t }
}

func addOnFee(id core.AddOnID) func(*invoice.Fee) {
	return func(fe *invoice.Fee) { fe.Type, fe.AddOnID = core.FeeTypeAddOn, id }
}

func taxesOf(rs []tax.ProviderTaxResult) map[string]int64 {
	out := map[string]int64{}
	for _, r := range rs {
		var sum int64
		for _, a := range r.AllocatedAmounts() {
			sum += a
		}
		out[r.ItemID] = sum
	}
	return out
}

func itemIDs(rs []tax.ProviderTaxResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ItemID
	}
	return out
}

func lineKeys(t *integration.TaxTransaction) []string {
	out := make([]string, len(t.Lines))
	for i, l := range t.Lines {
		out[i] = l.ItemKey
	}
	return out
}

const (
	firstFeeID  = "00000000-0000-4000-8000-000000000001"
	secondFeeID = "00000000-0000-4000-8000-000000000002"
)

func TestTaxesEnabled(t *testing.T) {
	ctx := context.Background()
	f := newTaxFixture(t, integration.ProviderAnrok, nil)
	on, err := f.taxes.Enabled(ctx, f.cust)
	if err != nil || !on {
		t.Errorf("linked: got = %v (%v), want = true", on, err)
	}
	other := f.h.Customer()
	on, err = f.taxes.Enabled(ctx, other)
	if err != nil || on {
		t.Errorf("unlinked: got = %v (%v), want = false", on, err)
	}
}

func TestComputeTaxesLines(t *testing.T) {
	ctx := context.Background()
	for _, p := range []integration.Provider{integration.ProviderAnrok, integration.ProviderAvalara} {
		for _, draft := range []bool{true, false} {
			name := string(p) + map[bool]string{true: "/draft", false: "/finalized"}[draft]
			t.Run(name, func(t *testing.T) {
				f := newTaxFixture(t, p, nil)
				inv := f.h.StoredInvoice(f.cust)
				second := f.chargeFee(inv, 100, withID(secondFeeID))
				first := f.chargeFee(inv, 100, withID(firstFeeID))
				calls := func() []*integration.TaxTransaction {
					if draft {
						return f.gw.quotes
					}
					return f.gw.reports
				}

				// source: spec/services/integrations/aggregator/taxes/invoices/base_service_spec.rb:51
				res, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{
					Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{second, first}, Draft: draft,
				})
				if err != nil {
					t.Fatalf("ComputeTaxes: %v", err)
				}
				wantTaxes := map[string]int64{first.ID.String(): 5, second.ID.String(): 5}
				if p == integration.ProviderAnrok {
					wantTaxes = map[string]int64{first.ID.String(): 3, second.ID.String(): 2}
				}
				if got := itemIDs(res); !slices.Equal(got, []string{first.ID.String(), second.ID.String()}) {
					t.Errorf("order: got = %v, want = [first second]", got)
				}
				if got := taxesOf(res); !maps.Equal(got, wantTaxes) {
					t.Errorf("taxes: got = %v, want = %v", got, wantTaxes)
				}
				tx := calls()[0]
				if p == integration.ProviderAnrok {
					// source: spec/services/integrations/aggregator/taxes/invoices/base_service_spec.rb:58
					want := []integration.TaxLine{{
						ItemKey: f.charge.ID.String(), ItemID: f.charge.ID.String(), ItemCode: "1",
						AmountCents: 200, Units: money.NewFromInt(2),
					}}
					if !linesEqual(tx.Lines, want) {
						t.Errorf("lines: got = %+v, want = %+v", tx.Lines, want)
					}
				} else {
					// source: spec/services/integrations/aggregator/taxes/invoices/base_service_spec.rb:66
					want := []integration.TaxLine{
						{ItemKey: first.ID.String(), ItemID: first.ID.String(), ItemCode: "1", AmountCents: 100, Units: money.One},
						{ItemKey: second.ID.String(), ItemID: second.ID.String(), ItemCode: "1", AmountCents: 100, Units: money.One},
					}
					if !linesEqual(tx.Lines, want) {
						t.Errorf("lines: got = %+v, want = %+v", tx.Lines, want)
					}
				}
				wantID := inv.ID.String()
				if draft {
					wantID = ""
				}
				if tx.ID != wantID || tx.IssuingDate != inv.IssuingDate || tx.Currency != inv.Currency || tx.Return {
					t.Errorf("transaction: got = %q %v %v return=%v, want = %q %v %v false",
						tx.ID, tx.IssuingDate, tx.Currency, tx.Return, wantID, inv.IssuingDate, inv.Currency)
				}
				if tx.Customer.ExternalID != f.cust.ExternalID {
					t.Errorf("customer: got = %q, want = %q", tx.Customer.ExternalID, f.cust.ExternalID)
				}
			})
		}
	}
}

func TestComputeTaxesFeeSelection(t *testing.T) {
	ctx := context.Background()
	for _, p := range []integration.Provider{integration.ProviderAnrok, integration.ProviderAvalara} {
		t.Run(string(p), func(t *testing.T) {
			t.Run("singleton charge and unrelated fee", func(t *testing.T) {
				// source: spec/services/integrations/aggregator/taxes/invoices/base_service_spec.rb:81
				f := newTaxFixture(t, p, nil)
				inv := f.h.StoredInvoice(f.cust)
				first := f.chargeFee(inv, 100)
				other := f.fee(inv, 100, addOnFee(f.h.AddOn(100).ID))
				res, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{first, other}})
				if err != nil {
					t.Fatalf("ComputeTaxes: %v", err)
				}
				want := map[string]int64{first.ID.String(): 5, other.ID.String(): 5}
				if got := taxesOf(res); !maps.Equal(got, want) {
					t.Errorf("taxes: got = %v, want = %v", got, want)
				}
				if got := lineKeys(f.gw.reports[0]); !slices.Equal(got, []string{first.ID.String(), other.ID.String()}) {
					t.Errorf("keys: got = %v, want = fee IDs", got)
				}
			})
			t.Run("grouped charge and unrelated fee", func(t *testing.T) {
				// source: spec/services/integrations/aggregator/taxes/invoices/base_service_spec.rb:92
				f := newTaxFixture(t, p, nil)
				inv := f.h.StoredInvoice(f.cust)
				second := f.chargeFee(inv, 100, withID(secondFeeID))
				first := f.chargeFee(inv, 100, withID(firstFeeID))
				other := f.fee(inv, 100, addOnFee(f.h.AddOn(100).ID), createdAt(now.Add(time.Second)))
				res, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{second, first, other}})
				if err != nil {
					t.Fatalf("ComputeTaxes: %v", err)
				}
				want := map[string]int64{first.ID.String(): 5, second.ID.String(): 5, other.ID.String(): 5}
				lines := 3
				if p == integration.ProviderAnrok {
					want[first.ID.String()], want[second.ID.String()] = 3, 2
					lines = 2
				}
				if got := taxesOf(res); !maps.Equal(got, want) {
					t.Errorf("taxes: got = %v, want = %v", got, want)
				}
				if got := len(f.gw.reports[0].Lines); got != lines {
					t.Errorf("lines: got = %d, want = %d", got, lines)
				}
			})
			t.Run("creation times differ", func(t *testing.T) {
				// source: spec/services/integrations/aggregator/taxes/invoices/base_service_spec.rb:126
				f := newTaxFixture(t, p, nil)
				inv := f.h.StoredInvoice(f.cust)
				first := f.chargeFee(inv, 100, withID(firstFeeID))
				second := f.chargeFee(inv, 100, withID(secondFeeID), createdAt(now.Add(-time.Second)))
				res, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{first, second}})
				if err != nil {
					t.Fatalf("ComputeTaxes: %v", err)
				}
				if got := itemIDs(res); !slices.Equal(got, []string{second.ID.String(), first.ID.String()}) {
					t.Errorf("order: got = %v, want = [second first]", got)
				}
				if p == integration.ProviderAnrok {
					want := map[string]int64{second.ID.String(): 3, first.ID.String(): 2}
					if got := taxesOf(res); !maps.Equal(got, want) {
						t.Errorf("taxes: got = %v, want = %v", got, want)
					}
				}
			})
			t.Run("fees in any order", func(t *testing.T) {
				// The fees are ordered by the service, whatever order the
				// caller gives them in.
				// source: spec/services/integrations/aggregator/taxes/invoices/base_service_spec.rb:114
				f := newTaxFixture(t, p, nil)
				inv := f.h.StoredInvoice(f.cust)
				second := f.chargeFee(inv, 100, withID(secondFeeID))
				first := f.chargeFee(inv, 100, withID(firstFeeID))
				var got [][]string
				for _, fees := range [][]*invoice.Fee{{first, second}, {second, first}} {
					res, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: fees})
					if err != nil {
						t.Fatalf("ComputeTaxes: %v", err)
					}
					taxes := taxesOf(res)
					got = append(got, []string{fmt.Sprint(itemIDs(res)), fmt.Sprint(taxes[first.ID.String()], taxes[second.ID.String()])})
				}
				if !slices.Equal(got[0], got[1]) {
					t.Errorf("results: got = %v then %v, want = the same", got[0], got[1])
				}
			})
			t.Run("all fees untaxable", func(t *testing.T) {
				// source: spec/services/integrations/aggregator/taxes/invoices/base_service_spec.rb:135
				f := newTaxFixture(t, p, nil)
				inv := f.h.StoredInvoice(f.cust)
				second := f.chargeFee(inv, 0, withID(secondFeeID))
				first := f.chargeFee(inv, 0, withID(firstFeeID))
				if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{second, first}}); err != nil {
					t.Fatalf("ComputeTaxes: %v", err)
				}
				if got := lineKeys(f.gw.reports[0]); !slices.Equal(got, []string{first.ID.String()}) {
					t.Errorf("keys: got = %v, want = [%s]", got, first.ID)
				}
				// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:184
				// source: spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:174
				// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:521
				if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{second, first}, Draft: true}); err != nil {
					t.Fatalf("ComputeTaxes draft: %v", err)
				}
				if got := lineKeys(f.gw.quotes[0]); !slices.Equal(got, []string{first.ID.String()}) || f.gw.quotes[0].Lines[0].AmountCents != 0 {
					t.Errorf("draft keys: got = %v, want = [%s] of 0", got, first.ID)
				}
			})
			t.Run("zero fee left out", func(t *testing.T) {
				// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:143
				f := newTaxFixture(t, p, nil)
				inv := f.h.StoredInvoice(f.cust)
				paid := f.fee(inv, 100, addOnFee(f.h.AddOn(100).ID))
				zero := f.fee(inv, 0, addOnFee(f.h.AddOn(0).ID), createdAt(now.Add(time.Second)))
				if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{paid, zero}}); err != nil {
					t.Fatalf("ComputeTaxes: %v", err)
				}
				if got := lineKeys(f.gw.reports[0]); !slices.Equal(got, []string{paid.ID.String()}) {
					t.Errorf("keys: got = %v, want = [%s]", got, paid.ID)
				}
				// source: spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:145
				// source: spec/services/invoices/provider_taxes/pull_taxes_and_apply_service_spec.rb:471
				if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{paid, zero}, Draft: true}); err != nil {
					t.Fatalf("ComputeTaxes draft: %v", err)
				}
				if got := lineKeys(f.gw.quotes[0]); !slices.Equal(got, []string{paid.ID.String()}) {
					t.Errorf("draft keys: got = %v, want = [%s]", got, paid.ID)
				}
			})
			t.Run("no fees", func(t *testing.T) {
				// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:122
				f := newTaxFixture(t, p, nil)
				inv := f.h.StoredInvoice(f.cust)
				res, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv})
				if err != nil || len(res) != 0 || len(f.gw.reports) != 0 {
					t.Errorf("got = %v %v calls=%d, want = no result, no call", res, err, len(f.gw.reports))
				}
			})
		})
	}
}

func TestComputeTaxesItemCode(t *testing.T) {
	// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:133
	ctx := context.Background()
	f := newTaxFixture(t, integration.ProviderAvalara, nil)
	a := f.h.AddOn(100)
	m := &integration.Mapping{
		ID: core.NewID[core.IntegrationMappingID](f.h.IDs), OrganizationID: f.h.OrgID(), IntegrationID: f.integ.ID,
		MappableType: integration.MappableTypeAddOn, MappableID: a.ID.UUID(), ExternalID: "m1",
		CreatedAt: now, UpdatedAt: now,
	}
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.IntegrationMappings.Create(ctx, m) })
	inv := f.h.StoredInvoice(f.cust)
	mapped := f.fee(inv, 100, addOnFee(a.ID))
	sub := f.fee(inv, 100, billingtest.SubscriptionFee(f.sub), createdAt(now.Add(time.Second)))
	if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{mapped, sub}}); err != nil {
		t.Fatalf("ComputeTaxes: %v", err)
	}
	var got []string
	for _, l := range f.gw.reports[0].Lines {
		got = append(got, l.ItemCode)
	}
	if !slices.Equal(got, []string{"m1", "1"}) {
		t.Errorf("codes: got = %v, want = [m1 1]", got)
	}
}

func TestComputeTaxesReported(t *testing.T) {
	ctx := context.Background()
	t.Run("avalara records the transaction", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:357
		f := newTaxFixture(t, integration.ProviderAvalara, nil)
		inv := f.h.StoredInvoice(f.cust)
		fe := f.chargeFee(inv, 100)
		if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{fe}}); err != nil {
			t.Fatalf("ComputeTaxes: %v", err)
		}
		res := resource(t, f, integration.ResourceTypeInvoice, inv.ID.UUID())
		if res == nil || res.ExternalID != "txn_"+inv.ID.String() {
			t.Errorf("resource: got = %+v, want = txn_%s", res, inv.ID)
		}
		// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:371
		ic := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*integration.IntegrationCustomer, error) {
			return r.IntegrationCustomers.Get(ctx, f.h.OrgID(), f.ic.ID)
		})
		if ic.ExternalCustomerID != f.cust.ExternalID {
			t.Errorf("external customer: got = %q, want = %q", ic.ExternalCustomerID, f.cust.ExternalID)
		}
	})
	t.Run("anrok records nothing", func(t *testing.T) {
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		inv := f.h.StoredInvoice(f.cust)
		fe := f.chargeFee(inv, 100)
		if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{fe}}); err != nil {
			t.Fatalf("ComputeTaxes: %v", err)
		}
		if res := resource(t, f, integration.ResourceTypeInvoice, inv.ID.UUID()); res != nil {
			t.Errorf("resource: got = %+v, want = none", res)
		}
	})
	t.Run("draft records nothing", func(t *testing.T) {
		f := newTaxFixture(t, integration.ProviderAvalara, nil)
		inv := f.h.StoredInvoice(f.cust)
		fe := f.chargeFee(inv, 100)
		if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{fe}, Draft: true}); err != nil {
			t.Fatalf("ComputeTaxes: %v", err)
		}
		if res := resource(t, f, integration.ResourceTypeInvoice, inv.ID.UUID()); res != nil {
			t.Errorf("resource: got = %+v, want = none", res)
		}
	})
	t.Run("avalara voided invoice is a return", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/invoices/base_service_spec.rb:103
		f := newTaxFixture(t, integration.ProviderAvalara, nil)
		inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusVoided })
		a := f.chargeFee(inv, 100)
		b := f.chargeFee(inv, 100, createdAt(now.Add(time.Second)))
		if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{a, b}}); err != nil {
			t.Fatalf("ComputeTaxes: %v", err)
		}
		tx := f.gw.reports[0]
		if !tx.Return || tx.Lines[0].AmountCents != -100 || tx.Lines[1].AmountCents != -100 {
			t.Errorf("got = return=%v %+v, want = return with -100 lines", tx.Return, tx.Lines)
		}
	})
}

func resource(t *testing.T, f *taxFixture, rt integration.ResourceType, id uuid.UUID) *integration.Resource {
	t.Helper()
	var out *integration.Resource
	f.h.Do(func(ctx context.Context, r port.Repos) error {
		res, err := r.IntegrationResources.Find(ctx, f.h.OrgID(), f.integ.ID, rt, id)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		out = res
		return err
	})
	return out
}

func TestComputeTaxesStandalone(t *testing.T) {
	// source: spec/services/fees/apply_provider_taxes_to_standalone_fees_service_spec.rb:61
	ctx := context.Background()
	f := newTaxFixture(t, integration.ProviderAvalara, nil)
	kiritimati, err := time.LoadLocation("Pacific/Kiritimati")
	if err != nil {
		t.Skip(err)
	}
	f.h.UpdateCustomer(f.cust, func(c *customer.Customer) { c.Timezone = kiritimati })
	fe := &invoice.Fee{
		ID: core.NewID[core.FeeID](f.h.IDs), OrganizationID: f.h.OrgID(), Type: core.FeeTypeAddOn,
		AddOnID: f.h.AddOn(100).ID, Amount: 100, PreciseAmount: money.NewFromInt(100), Units: money.One,
		Currency: billingtest.USD, PreciseCouponsAmount: money.Zero, CreatedAt: now,
	}
	res, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Fees: []*invoice.Fee{fe}})
	if err != nil {
		t.Fatalf("ComputeTaxes: %v", err)
	}
	if got := taxesOf(res)[fe.ID.String()]; got != 5 {
		t.Errorf("taxes: got = %d, want = 5", got)
	}
	tx := f.gw.reports[0]
	if _, err := core.ParseID[core.InvoiceID](tx.ID); err != nil || tx.ID == "" {
		t.Errorf("id: got = %q, want = a random ID", tx.ID)
	}
	if want := core.NewDate(2025, time.March, 2); tx.IssuingDate != want || tx.Currency != billingtest.USD {
		t.Errorf("date: got = %v %v, want = %v USD", tx.IssuingDate, tx.Currency, want)
	}
}

func TestComputeTaxesStandaloneGroup(t *testing.T) {
	// Fees of one charge are one Anrok line, its taxes split over them by
	// amount.
	// source: spec/services/fees/apply_provider_taxes_to_standalone_fees_service_spec.rb:107
	ctx := context.Background()
	f := newTaxFixture(t, integration.ProviderAnrok, nil)
	f.gw.lineTax = func(l integration.TaxLine) int64 { return l.AmountCents / 10 }
	fee := func(amount int64) *invoice.Fee {
		fe := &invoice.Fee{
			ID: core.NewID[core.FeeID](f.h.IDs), OrganizationID: f.h.OrgID(), Amount: amount,
			PreciseAmount: money.NewFromInt(amount), Units: money.One, Currency: billingtest.USD,
			PreciseCouponsAmount: money.Zero, CreatedAt: now,
		}
		billingtest.ChargeFee(f.sub, f.charge.ID)(fe)
		return fe
	}
	a, b := fee(1000), fee(500)
	res, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Fees: []*invoice.Fee{a, b}})
	if err != nil {
		t.Fatalf("ComputeTaxes: %v", err)
	}
	if n := len(f.gw.reports[0].Lines); n != 1 {
		t.Errorf("lines: got = %d, want = 1", n)
	}
	want := map[string]int64{a.ID.String(): 100, b.ID.String(): 50}
	if got := taxesOf(res); !maps.Equal(got, want) {
		t.Errorf("taxes: got = %v, want = %v", got, want)
	}
}

func TestComputeTaxesFinalizedResource(t *testing.T) {
	// Only Avalara records the reported transaction; Anrok's is the
	// invoice itself.
	// source: spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:139
	ctx := context.Background()
	f := newTaxFixture(t, integration.ProviderAnrok, nil)
	inv := f.h.StoredInvoice(f.cust)
	fe := f.chargeFee(inv, 100)
	if _, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{fe}}); err != nil {
		t.Fatalf("ComputeTaxes: %v", err)
	}
	if res := resource(t, f, integration.ResourceTypeInvoice, inv.ID.UUID()); res != nil {
		t.Errorf("resource: got = %+v, want = none", res)
	}
}

func TestComputeTaxesUnmapped(t *testing.T) {
	// An item with no mapping and no fallback item is sent with an empty
	// code, and the provider's refusal is the failure.
	// source: spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:329
	ctx := context.Background()
	f := newTaxFixture(t, integration.ProviderAnrok, nil)
	f.h.Do(func(ctx context.Context, r port.Repos) error {
		ms, err := r.IntegrationCollectionMappings.ListByType(ctx, f.h.OrgID(), f.integ.ID, "")
		if err != nil {
			return err
		}
		for _, m := range ms {
			if err := r.IntegrationCollectionMappings.Delete(ctx, f.h.OrgID(), m.ID); err != nil {
				return err
			}
		}
		return nil
	})
	f.gw.err = &integration.ProviderError{Provider: integration.ProviderAnrok, Kind: integration.KindInvalidRequest, Code: "validationError", Message: "productExternalId: String must contain at least 1 character(s)."}
	inv := f.h.StoredInvoice(f.cust)
	fe := f.chargeFee(inv, 100)
	_, err := f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{fe}, Draft: true})
	var pe *kernel.TaxProviderError
	if !errors.As(err, &pe) || pe.Code != "validationError" {
		t.Errorf("error: got = %v, want = validationError", err)
	}
	if got := f.gw.quotes[0].Lines[0].ItemCode; got != "" {
		t.Errorf("item code: got = %q, want = empty", got)
	}
}

func TestDraftTaxCache(t *testing.T) {
	ctx := context.Background()
	request := func(f *taxFixture, amount int64) (*invoice.Invoice, kernel.TaxRequest) {
		inv := f.h.StoredInvoice(f.cust)
		fe := f.chargeFee(inv, amount)
		return inv, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{fe}, Draft: true}
	}
	t.Run("identical request reuses the answer", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:72
		// source: spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:261
		f := newTaxFixture(t, integration.ProviderAnrok, integrations.NewDraftTaxCache(time.Hour, nil))
		_, req1 := request(f, 100)
		_, req2 := request(f, 100)
		if _, err := f.taxes.ComputeTaxes(ctx, req1); err != nil {
			t.Fatal(err)
		}
		res, err := f.taxes.ComputeTaxes(ctx, req2)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.gw.quotes) != 1 {
			t.Errorf("calls: got = %d, want = 1", len(f.gw.quotes))
		}
		// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:84
		if got := itemIDs(res); !slices.Equal(got, []string{req2.Fees[0].ID.String()}) {
			t.Errorf("identifiers: got = %v, want = [%s]", got, req2.Fees[0].ID)
		}
	})
	t.Run("different amount misses", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:92
		f := newTaxFixture(t, integration.ProviderAnrok, integrations.NewDraftTaxCache(time.Hour, nil))
		_, req1 := request(f, 100)
		_, req2 := request(f, 200)
		for _, r := range []kernel.TaxRequest{req1, req2} {
			if _, err := f.taxes.ComputeTaxes(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.gw.quotes) != 2 {
			t.Errorf("calls: got = %d, want = 2", len(f.gw.quotes))
		}
	})
	t.Run("expired entry misses", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:124
		clock := now
		f := newTaxFixture(t, integration.ProviderAnrok, integrations.NewDraftTaxCache(time.Hour, func() time.Time { return clock }))
		_, req1 := request(f, 100)
		if _, err := f.taxes.ComputeTaxes(ctx, req1); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(2 * time.Hour)
		if _, err := f.taxes.ComputeTaxes(ctx, req1); err != nil {
			t.Fatal(err)
		}
		if len(f.gw.quotes) != 2 {
			t.Errorf("calls: got = %d, want = 2", len(f.gw.quotes))
		}
	})
	t.Run("failures are not cached", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:145
		// source: spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:304
		f := newTaxFixture(t, integration.ProviderAnrok, integrations.NewDraftTaxCache(time.Hour, nil))
		_, req1 := request(f, 100)
		f.gw.err = &integration.ProviderError{Provider: integration.ProviderAnrok, Kind: integration.KindInvalidRequest, Code: "invalid", Message: "bad"}
		if _, err := f.taxes.ComputeTaxes(ctx, req1); err == nil {
			t.Fatal("got = nil, want = an error")
		}
		f.gw.err = nil
		if _, err := f.taxes.ComputeTaxes(ctx, req1); err != nil {
			t.Fatal(err)
		}
		if len(f.gw.quotes) != 2 {
			t.Errorf("calls: got = %d, want = 2", len(f.gw.quotes))
		}
	})
	t.Run("off with a zero TTL", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:173
		f := newTaxFixture(t, integration.ProviderAnrok, integrations.NewDraftTaxCache(0, nil))
		_, req1 := request(f, 100)
		for range 2 {
			if _, err := f.taxes.ComputeTaxes(ctx, req1); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.gw.quotes) != 2 {
			t.Errorf("calls: got = %d, want = 2", len(f.gw.quotes))
		}
	})
	t.Run("avalara is not cached", func(t *testing.T) {
		f := newTaxFixture(t, integration.ProviderAvalara, integrations.NewDraftTaxCache(time.Hour, nil))
		_, req1 := request(f, 100)
		for range 2 {
			if _, err := f.taxes.ComputeTaxes(ctx, req1); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.gw.quotes) != 2 {
			t.Errorf("calls: got = %d, want = 2", len(f.gw.quotes))
		}
	})
}

// TestComputeTaxesFailures checks how a failed tax call ends, for a
// finalized and a draft invoice, a void and a negation: a refusal is a
// [kernel.TaxProviderError] emitting customer.tax_provider_error;
// contention (the provider's limits, its unavailability, a timeout) is
// [kernel.ErrTaxContention], retried by the jobs.
func TestComputeTaxesFailures(t *testing.T) {
	ctx := context.Background()
	const (
		finalized = "finalized"
		draft     = "draft"
		void      = "void"
		negate    = "negate"
	)
	tests := []struct {
		name       string
		err        error
		contention bool
		code       string
		webhook    bool
		sources    map[string]string
	}{
		{
			name:    "rejected",
			err:     &integration.ProviderError{Kind: integration.KindInvalidRequest, Code: "taxDateTooFarInFuture", Message: "Service failure"},
			code:    "taxDateTooFarInFuture",
			webhook: true,
			sources: map[string]string{
				finalized: "spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:348, 357",
				draft:     "spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:278, 287",
				void:      "spec/services/integrations/aggregator/taxes/invoices/void_service_spec.rb:184, 192",
				negate:    "spec/services/integrations/aggregator/taxes/invoices/negate_service_spec.rb:88, 96",
			},
		},
		{
			name:       "api limit",
			err:        &integration.ProviderError{Kind: integration.KindInvalidRequest, Code: "x", Message: "API limit exceeded"},
			contention: true,
			sources: map[string]string{
				finalized: "spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:403",
				draft:     "spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:365",
				void:      "spec/services/integrations/aggregator/taxes/invoices/void_service_spec.rb:221",
				negate:    "spec/services/integrations/aggregator/taxes/invoices/negate_service_spec.rb:125",
			},
		},
		{
			name:       "request limit",
			err:        &integration.ProviderError{Kind: integration.KindRequestLimit, Code: "429", Message: "slow down"},
			contention: true,
		},
		{
			name:       "unavailable",
			err:        &integration.ProviderError{Kind: integration.KindUnavailable, Code: "502", Message: "bad gateway"},
			contention: true,
			sources: map[string]string{
				finalized: "spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:383, 415",
				draft:     "spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:345, 385, 394",
			},
		},
		{
			name: "authentication",
			err:  &integration.ProviderError{Kind: integration.KindAuthentication, Code: "401", Message: "unauthorized"},
			code: "401",
		},
		{
			name:       "timeout",
			err:        context.DeadlineExceeded,
			contention: true,
			sources: map[string]string{
				finalized: "spec/services/integrations/aggregator/taxes/invoices/create_service_spec.rb:469",
				draft:     "spec/services/integrations/aggregator/taxes/invoices/create_draft_service_spec.rb:459",
				void:      "spec/services/integrations/aggregator/taxes/invoices/void_service_spec.rb:287",
				negate:    "spec/services/integrations/aggregator/taxes/invoices/negate_service_spec.rb:191",
			},
		},
	}
	for _, tc := range tests {
		for _, op := range []string{finalized, draft, void, negate} {
			t.Run(tc.name+"/"+op, func(t *testing.T) {
				if s := tc.sources[op]; s != "" {
					t.Log("source:", s)
				}
				f := newTaxFixture(t, integration.ProviderAnrok, nil)
				inv := f.h.StoredInvoice(f.cust)
				fe := f.chargeFee(inv, 100)
				var err error
				switch op {
				case finalized, draft:
					f.gw.err = tc.err
					_, err = f.taxes.ComputeTaxes(ctx, kernel.TaxRequest{Customer: f.cust, Invoice: inv, Fees: []*invoice.Fee{fe}, Draft: op == draft})
				case void:
					f.gw.voidErr = tc.err
					err = f.taxes.VoidInvoice(ctx, f.cust, inv)
				case negate:
					f.gw.voidErr = &integration.ProviderError{Kind: integration.KindLocked, Code: "transactionFrozenForFiling", Message: "locked"}
					f.gw.negateErr = tc.err
					err = f.taxes.VoidInvoice(ctx, f.cust, inv)
				}
				if got := errors.Is(err, kernel.ErrTaxContention); got != tc.contention {
					t.Errorf("contention: got = %v (%v), want = %v", got, err, tc.contention)
				}
				var pe *kernel.TaxProviderError
				if tc.code != "" && (!errors.As(err, &pe) || pe.Code != tc.code) {
					t.Errorf("code: got = %v, want = %s", err, tc.code)
				}
				evs := f.h.Emitted.Events()
				i := slices.IndexFunc(evs, func(e webhook.Event) bool { return e.Type == webhook.EventTypeCustomerTaxProviderError })
				if got := i >= 0; got != tc.webhook {
					t.Errorf("webhook: got = %v, want = %v", got, tc.webhook)
				}
				if !tc.webhook || i < 0 {
					return
				}
				var p integrations.TaxProviderCustomerError
				if err := json.Unmarshal(evs[i].Payload, &p); err != nil {
					t.Fatal(err)
				}
				want := integrations.TaxProviderCustomerError{
					CustomerID: f.cust.ID, ExternalCustomerID: f.cust.ExternalID,
					TaxProvider: integration.ProviderAnrok, TaxProviderCode: f.integ.Code,
					ProviderError: integrations.ProviderErrorDetails{Message: "Service failure", ErrorCode: tc.code},
				}
				if p != want {
					t.Errorf("payload: got = %+v, want = %+v", p, want)
				}
			})
		}
	}
}

func TestVoidInvoice(t *testing.T) {
	ctx := context.Background()
	locked := func(p integration.Provider, code string) error {
		return &integration.ProviderError{Provider: p, Kind: integration.KindLocked, Code: code, Message: "locked"}
	}
	t.Run("never reported", func(t *testing.T) {
		// source: spec/services/invoices/provider_taxes/void_service_spec.rb:132
		// source: spec/services/invoices/provider_taxes/void_service_spec.rb:139
		// source: spec/services/integrations/aggregator/taxes/invoices/void_service_spec.rb:67
		f := newTaxFixture(t, integration.ProviderAvalara, nil)
		inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusVoided })
		if err := f.taxes.VoidInvoice(ctx, f.cust, inv); err != nil {
			t.Fatalf("got = %v, want = nil (no tax voiding error)", err)
		}
		if len(f.gw.voids) != 0 {
			t.Errorf("calls: got = %v, want = none", f.gw.voids)
		}
	})
	t.Run("no fee but reported before", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/invoices/void_service_spec.rb:90
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusVoided })
		f.h.Do(func(ctx context.Context, r port.Repos) error {
			return r.IntegrationResources.Create(ctx, &integration.Resource{
				ID: core.NewID[core.IntegrationResourceID](f.h.Deps.IDs), OrganizationID: f.h.OrgID(), IntegrationID: f.integ.ID,
				ResourceType: integration.ResourceTypeInvoice, SyncableID: inv.ID.UUID(), ExternalID: "txn_1", CreatedAt: now,
			})
		})
		if err := f.taxes.VoidInvoice(ctx, f.cust, inv); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(f.gw.voids, []string{inv.ID.String()}) {
			t.Errorf("voids: got = %v, want = [%s]", f.gw.voids, inv.ID)
		}
	})
	t.Run("zero-amount fees", func(t *testing.T) {
		// source: spec/services/invoices/provider_taxes/void_service_spec.rb:154
		// source: spec/services/integrations/aggregator/taxes/invoices/void_service_spec.rb:111
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusVoided })
		f.chargeFee(inv, 0)
		f.chargeFee(inv, 0)
		if err := f.taxes.VoidInvoice(ctx, f.cust, inv); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(f.gw.voids, []string{inv.ID.String()}) {
			t.Errorf("voids: got = %v, want = [%s]", f.gw.voids, inv.ID)
		}
	})
	t.Run("voided", func(t *testing.T) {
		// source: spec/services/invoices/provider_taxes/void_service_spec.rb:163
		// source: spec/services/integrations/aggregator/taxes/invoices/void_service_spec.rb:133
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusVoided })
		f.chargeFee(inv, 100)
		if err := f.taxes.VoidInvoice(ctx, f.cust, inv); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(f.gw.voids, []string{inv.ID.String()}) || len(f.gw.negations) != 0 {
			t.Errorf("calls: got = %v %v, want = [%s] and no negation", f.gw.voids, f.gw.negations, inv.ID)
		}
	})
	t.Run("anrok frozen for filing negates", func(t *testing.T) {
		// source: app/services/invoices/provider_taxes/void_service.rb:21-27 (the spec covers only the failing negation, :213)
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusVoided })
		f.chargeFee(inv, 100)
		f.gw.voidErr = locked(integration.ProviderAnrok, "transactionFrozenForFiling")
		if err := f.taxes.VoidInvoice(ctx, f.cust, inv); err != nil {
			t.Fatal(err)
		}
		want := [][2]string{{inv.ID.String(), inv.ID.String() + "_voided"}}
		if !slices.Equal(f.gw.negations, want) {
			t.Errorf("negations: got = %v, want = %v", f.gw.negations, want)
		}
	})
	t.Run("avalara locked reports a return", func(t *testing.T) {
		// source: app/services/invoices/provider_taxes/void_service.rb:27-33 (the spec covers only the failing return, :248)
		f := newTaxFixture(t, integration.ProviderAvalara, nil)
		inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
		f.chargeFee(inv, 100)
		f.gw.voidErr = locked(integration.ProviderAvalara, "CannotModifyLockedTransaction")
		if err := f.taxes.VoidInvoice(ctx, f.cust, inv); err != nil {
			t.Fatal(err)
		}
		if len(f.gw.reports) != 1 || !f.gw.reports[0].Return || f.gw.reports[0].Lines[0].AmountCents != -100 {
			t.Errorf("reports: got = %+v, want = one return of -100", f.gw.reports)
		}
	})
	t.Run("other failure", func(t *testing.T) {
		// source: spec/services/invoices/provider_taxes/void_service_spec.rb:182
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusVoided })
		f.chargeFee(inv, 100)
		f.gw.voidErr = &integration.ProviderError{Kind: integration.KindNotFound, Code: "transactionNotFound", Message: "missing"}
		var pe *kernel.TaxProviderError
		if err := f.taxes.VoidInvoice(ctx, f.cust, inv); !errors.As(err, &pe) || pe.Code != "transactionNotFound" {
			t.Errorf("got = %v, want = transactionNotFound", err)
		}
	})
}

func TestReportCreditNote(t *testing.T) {
	ctx := context.Background()
	for _, p := range []integration.Provider{integration.ProviderAnrok, integration.ProviderAvalara} {
		t.Run(string(p), func(t *testing.T) {
			// source: spec/services/integrations/aggregator/taxes/credit_notes/create_service_spec.rb:146
			f := newTaxFixture(t, p, nil)
			inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) {
				i.Status, i.IssuingDate = invoice.StatusFinalized, core.NewDate(2025, time.February, 1)
			})
			a := f.chargeFee(inv, 1000)
			b := f.chargeFee(inv, 1000, billingtest.Coupons("500"))
			cn := &creditnote.CreditNote{
				ID: core.NewID[core.CreditNoteID](f.h.IDs), OrganizationID: f.h.OrgID(), InvoiceID: inv.ID,
				CustomerID: f.cust.ID, Currency: billingtest.USD, IssuingDate: core.NewDate(2025, time.March, 1),
				Items: []creditnote.Item{{FeeID: a.ID, Amount: 333}, {FeeID: b.ID, Amount: 500}},
			}
			if err := f.taxes.ReportCreditNote(ctx, f.cust, inv, cn); err != nil {
				t.Fatal(err)
			}
			tx := f.gw.reports[0]
			if tx.ID != "cn_"+cn.ID.String() || !tx.Return || tx.IssuingDate != cn.IssuingDate {
				t.Errorf("transaction: got = %q return=%v %v, want = cn_%s return %v", tx.ID, tx.Return, tx.IssuingDate, cn.ID, cn.IssuingDate)
			}
			var want []integration.TaxLine
			if p == integration.ProviderAnrok {
				// anrok.rb: one line per charge, at the invoice's date.
				if tx.TaxDate != inv.IssuingDate {
					t.Errorf("tax date: got = %v, want = %v", tx.TaxDate, inv.IssuingDate)
				}
				want = []integration.TaxLine{{ItemID: f.charge.ID.String(), ItemCode: "1", AmountCents: -583, Units: money.One}}
			} else {
				metric := f.charge.BillableMetricID.String()
				want = []integration.TaxLine{
					{ItemID: metric, ItemCode: "1", AmountCents: -333, Units: money.One},
					{ItemID: metric, ItemCode: "1", AmountCents: -250, Units: money.One},
				}
			}
			if !linesEqual(tx.Lines, want) {
				t.Errorf("lines: got = %+v, want = %+v", tx.Lines, want)
			}
			// source: spec/services/integrations/aggregator/taxes/credit_notes/create_service_spec.rb:157
			if res := resource(t, f, integration.ResourceTypeCreditNote, cn.ID.UUID()); res == nil || res.ExternalID != "txn_"+tx.ID {
				t.Errorf("resource: got = %+v, want = txn_%s", res, tx.ID)
			}
		})
	}
	t.Run("failures", func(t *testing.T) {
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		inv := f.h.StoredInvoice(f.cust)
		a := f.chargeFee(inv, 1000)
		cn := &creditnote.CreditNote{
			ID: core.NewID[core.CreditNoteID](f.h.IDs), OrganizationID: f.h.OrgID(), InvoiceID: inv.ID,
			CustomerID: f.cust.ID, Currency: billingtest.USD, IssuingDate: inv.IssuingDate,
			Items: []creditnote.Item{{FeeID: a.ID, Amount: 1000}},
		}
		for _, tt := range []struct {
			name, source string
			err          *integration.ProviderError
			code         string
		}{
			{
				"refusal", "spec/services/integrations/aggregator/taxes/credit_notes/create_service_spec.rb:170",
				&integration.ProviderError{Kind: integration.KindInvalidRequest, Code: "invalid", Message: "bad"}, "invalid",
			},
			{
				"server error", "spec/services/integrations/aggregator/taxes/credit_notes/create_service_spec.rb:216",
				&integration.ProviderError{Kind: integration.KindUnavailable, Code: "action_script_runtime_error", Message: "down"}, "action_script_runtime_error",
			},
			{
				"contention", "app/services/integrations/aggregator/taxes/base_service.rb:68",
				&integration.ProviderError{Kind: integration.KindContention, Message: "resource contention"}, "",
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				t.Logf("source: %s", tt.source)
				f.gw.err = tt.err
				err := f.taxes.ReportCreditNote(ctx, f.cust, inv, cn)
				var pe *kernel.TaxProviderError
				switch {
				case tt.code == "" && !errors.Is(err, kernel.ErrTaxContention):
					t.Errorf("got = %v, want = ErrTaxContention", err)
				case tt.code != "" && (!errors.As(err, &pe) || pe.Code != tt.code):
					t.Errorf("got = %v, want = tax provider error %s", err, tt.code)
				}
				if tt.name != "refusal" {
					return
				}
				// source: spec/services/integrations/aggregator/taxes/credit_notes/create_service_spec.rb:179
				if !slices.Contains(f.h.Webhooks(), webhook.EventTypeCustomerTaxProviderError) {
					t.Errorf("webhooks: got = %v, want = customer.tax_provider_error", f.h.Webhooks())
				}
				// source: spec/services/integrations/aggregator/taxes/credit_notes/create_service_spec.rb:193
				if res := resource(t, f, integration.ResourceTypeCreditNote, cn.ID.UUID()); res != nil {
					t.Errorf("resource: got = %+v, want = none", res)
				}
				ic := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*integration.IntegrationCustomer, error) {
					return r.IntegrationCustomers.Get(ctx, f.h.OrgID(), f.ic.ID)
				})
				if ic.ExternalCustomerID != "" {
					t.Errorf("external customer ID: got = %q, want = none", ic.ExternalCustomerID)
				}
			})
		}
	})
}

func TestFetchTaxCompany(t *testing.T) {
	ctx := context.Background()
	run := func(f *taxFixture) error {
		args, _ := json.Marshal(kernel.IntegrationArgs{IntegrationID: f.integ.ID})
		_, err := f.svc.HandleJob(ctx, port.Job{Kind: kernel.JobFetchTaxCompany, OrganizationID: f.h.OrgID(), Args: args}, 1)
		return err
	}
	t.Run("found", func(t *testing.T) {
		// source: spec/jobs/integrations/avalara/fetch_company_id_job_spec.rb:17
		// source: spec/models/integrations/avalara_integration_spec.rb:50
		// source: spec/services/integrations/aggregator/taxes/avalara/fetch_company_id_service_spec.rb:49
		// source: spec/services/integrations/avalara/fetch_company_id_service_spec.rb:23, 27
		f := newTaxFixture(t, integration.ProviderAvalara, nil)
		f.gw.company = "12345"
		if err := run(f); err != nil {
			t.Fatal(err)
		}
		i, err := f.svc.Get(ctx, f.h.OrgID(), f.integ.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := i.Setting(integrations.KeyCompanyID); got != "12345" {
			t.Errorf("company_id: got = %q, want = 12345", got)
		}
	})
	for _, tt := range []struct {
		name, source string
		err          error
		want         integrations.ProviderErrorDetails
	}{
		{
			"not found", "spec/services/integrations/aggregator/taxes/avalara/fetch_company_id_service_spec.rb:63, 71",
			fmt.Errorf("avalara: %w", integration.ErrCompanyNotFound),
			integrations.ProviderErrorDetails{ErrorCode: "company_not_found", Message: "Company cannot be found in Avalara based on the provided code"},
		},
		{
			"server error", "spec/services/integrations/aggregator/taxes/avalara/fetch_company_id_service_spec.rb:102, 110",
			&integration.ProviderError{Provider: integration.ProviderAvalara, Kind: integration.KindUnavailable, Code: "action_script_runtime_error", Message: "submitFields: Missing a required argument: type"},
			integrations.ProviderErrorDetails{ErrorCode: "action_script_runtime_error", Message: "submitFields: Missing a required argument: type"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newTaxFixture(t, integration.ProviderAvalara, nil)
			f.gw.companyErr = tt.err
			if err := run(f); err == nil || !strings.Contains(err.Error(), tt.want.ErrorCode) {
				t.Errorf("HandleJob() error = %v, want = %s", err, tt.want.ErrorCode)
			}
			evs := f.h.Emitted.Events()
			n := slices.IndexFunc(evs, func(e webhook.Event) bool { return e.Type == webhook.EventTypeIntegrationProviderError })
			if n < 0 {
				t.Fatalf("webhooks: got = %v, want = integration.provider_error", evs)
			}
			// source: spec/services/webhooks/integrations/provider_error_service_spec.rb:13
			if evs[n].ObjectType != "provider_error" {
				t.Errorf("object type: got = %q, want = provider_error", evs[n].ObjectType)
			}
			var p integrations.ProviderErrorPayload
			if err := json.Unmarshal(evs[n].Payload, &p); err != nil {
				t.Fatal(err)
			}
			want := integrations.ProviderErrorPayload{IntegrationID: f.integ.ID, Provider: integration.ProviderAvalara, ProviderCode: f.integ.Code, ProviderError: tt.want}
			if p != want {
				t.Errorf("payload: got = %+v, want = %+v", p, want)
			}
		})
	}
}

// creditNote returns a credit note of inv crediting amounts[i] of fees[i],
// in that order.
func (f *taxFixture) creditNote(inv *invoice.Invoice, fees []*invoice.Fee, amounts ...int64) *creditnote.CreditNote {
	cn := &creditnote.CreditNote{
		ID: core.NewID[core.CreditNoteID](f.h.IDs), OrganizationID: f.h.OrgID(), InvoiceID: inv.ID,
		CustomerID: f.cust.ID, Currency: billingtest.USD, IssuingDate: inv.IssuingDate,
	}
	for i, fe := range fees {
		cn.Items = append(cn.Items, creditnote.Item{FeeID: fe.ID, Amount: amounts[i]})
	}
	return cn
}

// otherCharge returns a second charge of the fixture's metric.
func (f *taxFixture) otherCharge() *catalog.Charge {
	m := f.h.Metric("other", catalog.AggregationTypeCount, "")
	return f.h.Charge(f.h.Plan(period.Monthly, 0), m, catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
		c.BillableMetricID = f.charge.BillableMetricID
	})
}

// reportLines reports cn and returns the lines sent.
func (f *taxFixture) reportLines(t *testing.T, inv *invoice.Invoice, cn *creditnote.CreditNote) *integration.TaxTransaction {
	t.Helper()
	n := len(f.gw.reports)
	if err := f.taxes.ReportCreditNote(context.Background(), f.cust, inv, cn); err != nil {
		t.Fatal(err)
	}
	return f.gw.reports[n]
}

func TestCreditNoteLines(t *testing.T) {
	t.Run("anrok credits each charge once", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/credit_notes/payloads/anrok_spec.rb:91
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		other := f.otherCharge()
		inv := f.h.StoredInvoice(f.cust)
		a := f.chargeFee(inv, 300)
		b := f.fee(inv, 500, billingtest.ChargeFee(f.sub, other.ID))
		c := f.chargeFee(inv, 700)
		tx := f.reportLines(t, inv, f.creditNote(inv, []*invoice.Fee{a, b, c}, 300, 500, 700))
		want := []integration.TaxLine{
			{ItemID: f.charge.ID.String(), ItemCode: "1", AmountCents: -1000, Units: money.One},
			{ItemID: other.ID.String(), ItemCode: "1", AmountCents: -500, Units: money.One},
		}
		if !linesEqual(tx.Lines, want) {
			t.Errorf("lines: got = %+v, want = %+v", tx.Lines, want)
		}
	})
	t.Run("anrok charges sharing a metric", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/credit_notes/payloads/anrok_spec.rb:129, 133
		// Lago orders the items by creation time and ID; Tanvo keeps them
		// in the credit note's order, which is stable across reports.
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		other := f.otherCharge()
		inv := f.h.StoredInvoice(f.cust)
		a := f.chargeFee(inv, 100)
		b := f.fee(inv, 100, billingtest.ChargeFee(f.sub, other.ID))
		cn := f.creditNote(inv, []*invoice.Fee{a, b}, 100, 100)
		first := f.reportLines(t, inv, cn)
		if got, want := []string{first.Lines[0].ItemID, first.Lines[1].ItemID}, []string{f.charge.ID.String(), other.ID.String()}; !slices.Equal(got, want) {
			t.Errorf("item IDs: got = %v, want = %v", got, want)
		}
		again := f.reportLines(t, inv, cn)
		if again.ID != first.ID || !linesEqual(again.Lines, first.Lines) {
			t.Errorf("again: got = %s %+v, want = %s %+v", again.ID, again.Lines, first.ID, first.Lines)
		}
	})
	for _, tt := range []struct {
		name, source string
		others       bool
	}{
		{"anrok single charge fee", "spec/services/integrations/aggregator/taxes/credit_notes/payloads/anrok_spec.rb:140", false},
		{"anrok partial credit of a grouped line", "spec/services/integrations/aggregator/taxes/credit_notes/payloads/anrok_spec.rb:150", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newTaxFixture(t, integration.ProviderAnrok, nil)
			inv := f.h.StoredInvoice(f.cust)
			a := f.chargeFee(inv, 100)
			if tt.others {
				f.chargeFee(inv, 100)
			}
			cn := f.creditNote(inv, []*invoice.Fee{a}, 100)
			tx := f.reportLines(t, inv, cn)
			if tx.ID != "cn_"+cn.ID.String() || len(tx.Lines) != 1 || tx.Lines[0].ItemID != f.charge.ID.String() {
				t.Errorf("transaction: got = %s %+v, want = cn_%s with the charge %s", tx.ID, tx.Lines, cn.ID, f.charge.ID)
			}
		})
	}
	t.Run("anrok precision", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/credit_notes/payloads/anrok_spec.rb:185
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		inv := f.h.StoredInvoice(f.cust)
		a := f.chargeFee(inv, 167, func(fe *invoice.Fee) { fe.PreciseAmount = money.MustParse("166.666666666") })
		tx := f.reportLines(t, inv, f.creditNote(inv, []*invoice.Fee{a}, 100))
		if got := tx.Lines[0].AmountCents; got != -100 {
			t.Errorf("amount: got = %d, want = -100", got)
		}
	})
	t.Run("avalara credits each item", func(t *testing.T) {
		// source: spec/services/integrations/aggregator/taxes/credit_notes/payloads/avalara_spec.rb:34
		f := newTaxFixture(t, integration.ProviderAvalara, nil)
		inv := f.h.StoredInvoice(f.cust)
		units := func(n int64) func(*invoice.Fee) { return func(fe *invoice.Fee) { fe.Units = money.NewFromInt(n) } }
		a := f.chargeFee(inv, 115, units(2))
		b := f.chargeFee(inv, 115, units(3))
		tx := f.reportLines(t, inv, f.creditNote(inv, []*invoice.Fee{a, b}, 115, 115))
		metric := f.charge.BillableMetricID.String()
		want := []integration.TaxLine{
			{ItemID: metric, ItemCode: "1", AmountCents: -115, Units: money.NewFromInt(2)},
			{ItemID: metric, ItemCode: "1", AmountCents: -115, Units: money.NewFromInt(3)},
		}
		if !linesEqual(tx.Lines, want) {
			t.Errorf("lines: got = %+v, want = %+v", tx.Lines, want)
		}
	})
	billing := core.Address{Line1: "Billing 1", City: "Billing City", Zipcode: "12345", State: "Billing State", Country: "FR"}
	shipping := core.Address{Line1: "Shipping 1", City: "Shipping City", Zipcode: "67890", State: "Shipping State", Country: "US"}
	for _, tt := range []struct {
		name, source string
		p            integration.Provider
		shipping     core.Address
		want         core.Address
	}{
		// Go strings have no nil: empty and missing shipping fields are the same case.
		{"anrok without shipping address", "spec/services/integrations/aggregator/taxes/credit_notes/payloads/anrok_spec.rb:220, 236", integration.ProviderAnrok, core.Address{}, billing},
		{"anrok shipping address", "spec/services/integrations/aggregator/taxes/credit_notes/payloads/anrok_spec.rb:252", integration.ProviderAnrok, shipping, shipping},
		{"avalara without shipping address", "spec/services/integrations/aggregator/taxes/credit_notes/payloads/avalara_spec.rb:75, 93", integration.ProviderAvalara, core.Address{}, billing},
		{"avalara shipping address", "spec/services/integrations/aggregator/taxes/credit_notes/payloads/avalara_spec.rb:111", integration.ProviderAvalara, shipping, shipping},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source:", tt.source)
			f := newTaxFixture(t, tt.p, nil)
			f.cust.BillingAddress, f.cust.ShippingAddress = billing, tt.shipping
			inv := f.h.StoredInvoice(f.cust)
			a := f.chargeFee(inv, 100)
			if got := f.reportLines(t, inv, f.creditNote(inv, []*invoice.Fee{a}, 100)).Customer.Address; got != tt.want {
				t.Errorf("address: got = %+v, want = %+v", got, tt.want)
			}
		})
	}
}

// TestCreditNoteMappingFallback ports the shared example of
// spec/support/shared_examples/an_integration_payload.rb:172-230 for the
// credit note tax payloads: the item code is the metric's mapping for the
// customer's billing entity, then the entity's fallback item, then the
// organization's mapping, then the organization's fallback item.
func TestCreditNoteMappingFallback(t *testing.T) {
	for _, p := range []struct {
		provider integration.Provider
		source   string
	}{
		{integration.ProviderAnrok, "spec/services/integrations/aggregator/taxes/credit_notes/payloads/anrok_spec.rb:9"},
		{integration.ProviderAvalara, "spec/services/integrations/aggregator/taxes/credit_notes/payloads/avalara_spec.rb:123"},
	} {
		for _, tt := range []struct {
			name                string
			mappings, fallbacks []bool // on the billing entity, on the organization
			want                string
		}{
			{"mapping on the billing entity", []bool{true, true}, []bool{true, true}, "m-be"},
			{"fallback item of the billing entity", []bool{false, true}, []bool{true, false}, "f-be"},
			{"mapping of the organization", []bool{false, true}, []bool{false, true}, "m-org"},
			{"fallback item of the organization", []bool{false, false}, []bool{false, true}, "1"},
		} {
			t.Run(string(p.provider)+"/"+tt.name, func(t *testing.T) {
				t.Log("source:", p.source)
				f := newTaxFixture(t, p.provider, nil)
				be := f.cust.BillingEntityID
				f.h.Do(func(ctx context.Context, r port.Repos) error {
					for i, on := range tt.mappings {
						if !on {
							continue
						}
						m := &integration.Mapping{
							ID: core.NewID[core.IntegrationMappingID](f.h.IDs), OrganizationID: f.h.OrgID(), IntegrationID: f.integ.ID,
							MappableType: integration.MappableTypeBillableMetric, MappableID: f.charge.BillableMetricID.UUID(),
							ExternalID: "m-org", CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
						}
						if i == 0 {
							m.BillingEntityID, m.ExternalID = be, "m-be"
						}
						if err := r.IntegrationMappings.Create(ctx, m); err != nil {
							return err
						}
					}
					if tt.fallbacks[0] {
						id := "f-be"
						if err := r.IntegrationCollectionMappings.Create(ctx, &integration.CollectionMapping{
							ID: core.NewID[core.IntegrationCollectionMappingID](f.h.IDs), OrganizationID: f.h.OrgID(), IntegrationID: f.integ.ID,
							Type: integration.CollectionMappingTypeFallbackItem, BillingEntityID: be, ExternalID: &id,
							CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
						}); err != nil {
							return err
						}
					}
					return nil
				})
				inv := f.h.StoredInvoice(f.cust)
				a := f.chargeFee(inv, 100)
				tx := f.reportLines(t, inv, f.creditNote(inv, []*invoice.Fee{a}, 100))
				if got := tx.Lines[0].ItemCode; got != tt.want {
					t.Errorf("item code: got = %q, want = %q", got, tt.want)
				}
			})
		}
	}
}

func linesEqual(got, want []integration.TaxLine) bool {
	return slices.EqualFunc(got, want, func(a, b integration.TaxLine) bool {
		return a.ItemKey == b.ItemKey && a.ItemID == b.ItemID && a.ItemCode == b.ItemCode &&
			a.AmountCents == b.AmountCents && a.Units.Cmp(b.Units) == 0
	})
}

func TestReportInvoiceTaxesJob(t *testing.T) {
	ctx := context.Background()
	run := func(f *taxFixture, id core.InvoiceID) error {
		args, _ := json.Marshal(kernel.InvoiceArgs{InvoiceID: id})
		_, err := f.svc.HandleJob(ctx, port.Job{Kind: kernel.JobReportInvoiceTaxes, OrganizationID: f.h.OrgID(), Args: args}, 1)
		return err
	}
	t.Run("the invoice and its fees", func(t *testing.T) {
		// source: spec/jobs/integrations/aggregator/taxes/invoices/create_job_spec.rb:15
		f := newTaxFixture(t, integration.ProviderAvalara, nil)
		inv := f.h.StoredInvoice(f.cust, func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
		a, b := f.chargeFee(inv, 100), f.chargeFee(inv, 200)
		if err := run(f, inv.ID); err != nil {
			t.Fatalf("HandleJob: %v", err)
		}
		sent := append(slices.Clone(f.gw.quotes), f.gw.reports...)
		if len(sent) != 1 {
			t.Fatalf("transactions: got = %d, want = 1", len(sent))
		}
		var keys []string
		for _, l := range sent[0].Lines {
			keys = append(keys, l.ItemKey)
		}
		if want := []string{a.ID.String(), b.ID.String()}; !slices.Equal(keys, want) || sent[0].ID != inv.ID.String() {
			t.Errorf("transaction: got = %s %v, want = %s %v", sent[0].ID, keys, inv.ID, want)
		}
	})
	t.Run("customer taxed by no provider", func(t *testing.T) {
		f := newTaxFixture(t, integration.ProviderAvalara, nil)
		inv := f.h.StoredInvoice(f.h.Customer(), func(i *invoice.Invoice) { i.Status = invoice.StatusFinalized })
		f.chargeFee(inv, 100)
		if err := run(f, inv.ID); err != nil || len(f.gw.quotes)+len(f.gw.reports) != 0 {
			t.Errorf("got = %v with %d calls, want = nothing", err, len(f.gw.quotes)+len(f.gw.reports))
		}
	})
}
