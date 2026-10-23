package validators

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/money"
)

type (
	obj = map[string]any
	arr = []any
)

// testNow is the clock of the business tests (lago Time.current).
var testNow = billingtest.Date(2026, time.March, 15, 12, 0)

var eur = money.MustCurrency("EUR")

// iso is Ruby Time#iso8601 of a UTC instant.
func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// isoDay is Ruby Date#iso8601.
func isoDay(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// newQuote returns an unsaved quote of the customer and its draft version
// in currency c (zero for none).
func newQuote(h *billingtest.Harness, cust *customer.Customer, order quote.OrderType, c money.Currency) (*quote.Quote, *quote.Version) {
	q := &quote.Quote{
		ID: core.NewID[core.QuoteID](h.IDs), OrganizationID: h.OrgID(), CustomerID: cust.ID,
		OrderType: order, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	v := &quote.Version{
		ID: core.NewID[core.QuoteVersionID](h.IDs), OrganizationID: h.OrgID(), QuoteID: q.ID, Version: 1,
		Status: quote.VersionStatusDraft, Currency: c, CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	return q, v
}

// otherOrg returns the ID of a second organization.
func otherOrg(h *billingtest.Harness) core.OrganizationID {
	h.T.Helper()
	o := organization.New(core.NewID[core.OrganizationID](h.IDs), "Other", h.Now())
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Organizations.Create(ctx, o) })
	return o.ID
}

// billingEntity creates a billing entity of org.
func billingEntity(h *billingtest.Harness, org core.OrganizationID, opts ...func(*organization.BillingEntity)) *organization.BillingEntity {
	h.T.Helper()
	id := core.NewID[core.BillingEntityID](h.IDs)
	be := organization.NewBillingEntity(org, id, "be_"+id.String()[30:], "Entity", h.Now())
	for _, o := range opts {
		o(be)
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
	return be
}

func marshal(t *testing.T, items any) json.RawMessage {
	t.Helper()
	if items == nil {
		return nil
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// business runs the business pass alone over items, as lago's
// BusinessValidator specs do.
func runBusiness(t *testing.T, h *billingtest.Harness, q *quote.Quote, v *quote.Version, scope Scope, items any) []string {
	t.Helper()
	decoded, err := billingItems(marshal(t, items))
	if err != nil {
		t.Fatal(err)
	}
	var verr error
	h.Do(func(ctx context.Context, r port.Repos) error {
		verr = validateBusiness(ctx, r, q, v, scope, h.Now(), decoded)
		return nil
	})
	return fieldErrors(t, verr)
}

// runValidate runs Validate with items as the version's billing items.
func runValidate(t *testing.T, h *billingtest.Harness, q *quote.Quote, v *quote.Version, scope Scope, items any) []string {
	t.Helper()
	v.BillingItems = marshal(t, items)
	var verr error
	h.Do(func(ctx context.Context, r port.Repos) error {
		verr = Validate(ctx, r, q, v, scope, h.Now())
		return nil
	})
	return fieldErrors(t, verr)
}

func checkErrors(t *testing.T, got, want []string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors: got = %q, want = %q", got, want)
	}
}

// doubloon is a currency that is not ISO 4217 (lago "DOUBLOON").
func doubloon() money.Currency {
	c, err := money.NewCustomCurrency("DOUBLOON", "Doubloon", 2)
	if err != nil {
		panic(err)
	}
	return c
}
