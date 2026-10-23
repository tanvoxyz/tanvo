package validators

import (
	"context"
	"strconv"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/money"
)

const unknownID = "11111111-2222-3333-4444-555555555555"

// oneOffFix is the world of the one-off specs: a customer, an add-on and a
// EUR quote quoting it.
type oneOffFix struct {
	h       *billingtest.Harness
	q       *quote.Quote
	v       *quote.Version
	addOn   *catalog.AddOn
	item    obj
	payload obj
	items   any
}

func newOneOff(t *testing.T) *oneOffFix {
	h := billingtest.New(t, testNow)
	cust := h.Customer()
	f := &oneOffFix{h: h, addOn: h.AddOn(10_000)}
	f.q, f.v = newQuote(h, cust, quote.OrderTypeOneOff, eur)
	f.payload = obj{"code": f.addOn.Code, "units": 1, "unitAmountCents": 10_000, "totalAmountCents": 10_000}
	f.item = obj{"id": f.addOn.ID.String(), "localId": "3d08b2df-4e4c-4d58-b415-a525c1663735", "payload": f.payload}
	f.items = obj{"addOns": arr{f.item}}
	return f
}

func TestOneOffBusiness(t *testing.T) {
	const source = "spec/services/quote_versions/validators/one_off/business_validator_spec.rb"
	cases := []struct {
		line  int
		name  string
		scope Scope
		edit  func(f *oneOffFix)
		want  []string
	}{
		{line: 34, name: "valid", scope: ScopeUpdate},
		{line: 41, name: "valid at approve", scope: ScopeApprove},
		{line: 50, name: "missing currency at update", scope: ScopeUpdate, edit: func(f *oneOffFix) { f.v.Currency = money.Currency{} }},
		{
			line: 57, name: "missing currency at approve", scope: ScopeApprove,
			edit: func(f *oneOffFix) { f.v.Currency = money.Currency{} },
			want: []string{"currency: value_is_mandatory"},
		},
		{
			line: 67, name: "currency not ISO 4217", scope: ScopeUpdate,
			edit: func(f *oneOffFix) { f.v.Currency = doubloon() },
			want: []string{"currency: invalid_currency"},
		},
		{
			line: 77, name: "billing entity of the organization", scope: ScopeUpdate,
			edit: func(f *oneOffFix) { f.v.BillingEntityID = billingEntity(f.h, f.h.OrgID()).ID },
		},
		{
			line: 86, name: "billing entity of another organization", scope: ScopeUpdate,
			edit: func(f *oneOffFix) { f.v.BillingEntityID = billingEntity(f.h, otherOrg(f.h)).ID },
			want: []string{"billing_entity_id: billing_entity_not_found"},
		},
		{
			line: 96, name: "archived billing entity", scope: ScopeUpdate,
			edit: func(f *oneOffFix) {
				f.v.BillingEntityID = billingEntity(f.h, f.h.OrgID(), func(be *organization.BillingEntity) {
					now := f.h.Now()
					be.ArchivedAt = &now
				}).ID
			},
			want: []string{"billing_entity_id: billing_entity_not_found"},
		},
		{
			line: 105, name: "unknown add-on", scope: ScopeUpdate,
			edit: func(f *oneOffFix) { f.item["id"] = unknownID },
			want: []string{"billing_items.addOns.0.id: add_on_not_found"},
		},
		{
			line: 114, name: "discarded add-on", scope: ScopeUpdate,
			edit: func(f *oneOffFix) {
				f.h.Do(func(ctx context.Context, r port.Repos) error {
					return r.AddOns.Discard(ctx, f.h.OrgID(), f.addOn.ID, f.h.Now())
				})
			},
		},
		{
			line: 122, name: "add-on of another organization", scope: ScopeUpdate,
			edit: func(f *oneOffFix) {
				org := otherOrg(f.h)
				f.item["id"] = f.h.AddOn(100, func(a *catalog.AddOn) { a.OrganizationID = org }).ID.String()
			},
			want: []string{"billing_items.addOns.0.id: add_on_not_found"},
		},
		{
			line: 131, name: "only fromDatetime", scope: ScopeUpdate,
			edit: func(f *oneOffFix) { f.payload["fromDatetime"], f.payload["toDatetime"] = "2026-01-01T00:00:00Z", nil },
			want: []string{"billing_items.addOns.0.payload.toDatetime: value_is_mandatory"},
		},
		{
			line: 140, name: "only toDatetime", scope: ScopeUpdate,
			edit: func(f *oneOffFix) { f.payload["fromDatetime"], f.payload["toDatetime"] = nil, "2026-02-01T00:00:00Z" },
			want: []string{"billing_items.addOns.0.payload.fromDatetime: value_is_mandatory"},
		},
		{
			line: 149, name: "fromDatetime after toDatetime", scope: ScopeUpdate,
			edit: func(f *oneOffFix) {
				f.payload["fromDatetime"], f.payload["toDatetime"] = "2026-02-01T00:00:00Z", "2026-01-01T00:00:00Z"
			},
			want: []string{"billing_items.addOns.0.payload.fromDatetime: invalid_date_range"},
		},
		{
			line: 158, name: "fromDatetime equals toDatetime", scope: ScopeUpdate,
			edit: func(f *oneOffFix) {
				f.payload["fromDatetime"], f.payload["toDatetime"] = "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"
			},
		},
		{
			line: 166, name: "only overrides fromDatetime", scope: ScopeUpdate,
			edit: func(f *oneOffFix) { f.item["overrides"] = obj{"fromDatetime": "2026-01-01T00:00:00Z"} },
			want: []string{"billing_items.addOns.0.overrides.toDatetime: value_is_mandatory"},
		},
		{
			line: 175, name: "only overrides toDatetime", scope: ScopeUpdate,
			edit: func(f *oneOffFix) { f.item["overrides"] = obj{"toDatetime": "2026-02-01T00:00:00Z"} },
			want: []string{"billing_items.addOns.0.overrides.fromDatetime: value_is_mandatory"},
		},
		{
			line: 186, name: "overrides fromDatetime after toDatetime", scope: ScopeUpdate,
			edit: func(f *oneOffFix) {
				f.item["overrides"] = obj{"fromDatetime": "2026-02-01T00:00:00Z", "toDatetime": "2026-01-01T00:00:00Z"}
			},
			want: []string{"billing_items.addOns.0.overrides.fromDatetime: invalid_date_range"},
		},
		{
			line: 197, name: "overrides fromDatetime equals toDatetime", scope: ScopeUpdate,
			edit: func(f *oneOffFix) {
				f.item["overrides"] = obj{"fromDatetime": "2026-01-01T00:00:00Z", "toDatetime": "2026-01-01T00:00:00Z"}
			},
		},
		{
			line: 212, name: "errors on several add-ons", scope: ScopeUpdate,
			edit: func(f *oneOffFix) {
				second := obj{"id": unknownID, "localId": f.item["localId"], "payload": f.payload}
				f.items = obj{"addOns": arr{f.item, second}}
			},
			want: []string{"billing_items.addOns.1.id: add_on_not_found"},
		},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.line)+"/"+c.name, func(t *testing.T) {
			t.Logf("source: %s:%d", source, c.line)
			f := newOneOff(t)
			if c.edit != nil {
				c.edit(f)
			}
			checkErrors(t, runBusiness(t, f.h, f.q, f.v, c.scope, f.items), c.want)
		})
	}
}

func TestOneOffValidator(t *testing.T) {
	const source = "spec/services/quote_versions/validators/one_off_validator_spec.rb"
	cases := []struct {
		line  int
		name  string
		scope Scope
		edit  func(f *oneOffFix)
		want  []string
	}{
		{line: 35, name: "complete valid version", scope: ScopeApprove},
		{
			line: 45, name: "structural errors first", scope: ScopeApprove,
			edit: func(f *oneOffFix) {
				f.payload["units"] = 0
				f.v.Currency = doubloon()
			},
			want: []string{"billing_items.addOns.0.payload.units: invalid_value"},
		},
		{
			line: 55, name: "business error", scope: ScopeApprove,
			edit: func(f *oneOffFix) { f.item["id"] = unknownID },
			want: []string{"billing_items.addOns.0.id: add_on_not_found"},
		},
		{
			line: 65, name: "structural errors skip the business pass", scope: ScopeApprove,
			edit: func(f *oneOffFix) {
				f.item["taxCodes"], f.item["id"] = arr{"vat_20"}, unknownID
				f.v.Currency = money.Currency{}
			},
			want: []string{"billing_items.addOns.0.taxCodes: unsupported_key"},
		},
		{line: 96, name: "nil billing items at update", scope: ScopeUpdate, edit: func(f *oneOffFix) { f.items = nil }},
	}
	for _, c := range cases {
		t.Run(strconv.Itoa(c.line)+"/"+c.name, func(t *testing.T) {
			t.Logf("source: %s:%d", source, c.line)
			f := newOneOff(t)
			f.item["type"] = "add_on"
			if c.edit != nil {
				c.edit(f)
			}
			checkErrors(t, runValidate(t, f.h, f.q, f.v, c.scope, f.items), c.want)
		})
	}
}
