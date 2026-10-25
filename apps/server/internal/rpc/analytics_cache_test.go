package rpc_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	analyticsv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/analytics/v1"
	portalv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/portal/v1/portalv1connect"
)

// cacheFixture is a server whose analytics reports are cached 4 hours.
type cacheFixture struct {
	t *testing.T
	s *rpctest.Server
}

func newCacheFixture(t *testing.T) *cacheFixture {
	t.Helper()
	s := rpctest.New(t, func(o *app.APIOptions) { o.AnalyticsCacheTTL = 4 * time.Hour })
	// The reports start at the organization's creation, 3 months ago in
	// the spec.
	if _, err := s.Admin.Exec(t.Context(), `UPDATE organizations SET created_at = $1 WHERE id = $2`, rpctest.Now.AddDate(0, -3, 0), s.Org.ID.UUID()); err != nil {
		t.Fatalf("backdate organization: %v", err)
	}
	return &cacheFixture{t: t, s: s}
}

// report reads one variant of a report and returns the sum of its amounts.
type report struct {
	customer *customer.Customer
	currency string
	months   int32
	expire   bool
}

func (r report) scope() *analyticsv1.AnalyticsScope {
	return scope(func(b *analyticsv1.AnalyticsScope_builder) {
		if r.currency != "" {
			b.Currency = new(r.currency)
		}
		if r.months != 0 {
			b.Months = new(r.months)
		}
	})
}

func (r report) customerRef() *string {
	if r.customer == nil {
		return nil
	}
	return new("external_id:" + r.customer.ExternalID)
}

func sum(amounts ...string) string {
	total := money.MustParse("0")
	for _, a := range amounts {
		if a != "" {
			total = total.Add(money.MustParse(a))
		}
	}
	return total.StringFixed(2)
}

func (f *cacheFixture) overdue(r report) string {
	f.t.Helper()
	res, err := analyticsClient(f.s, f.s.APIKey).GetOverdueBalance(f.t.Context(), analyticsv1.GetOverdueBalanceRequest_builder{
		Scope: r.scope(), CustomerId: r.customerRef(), ExpireCache: new(r.expire),
	}.Build())
	if err != nil {
		f.t.Fatalf("GetOverdueBalance() error got = %v, want = nil", err)
	}
	var amounts []string
	for _, p := range res.GetPoints() {
		amounts = append(amounts, p.GetAmount().GetAmount())
	}
	return sum(amounts...)
}

func (f *cacheFixture) gross(r report) string {
	f.t.Helper()
	res, err := analyticsClient(f.s, f.s.APIKey).GetGrossRevenue(f.t.Context(), analyticsv1.GetGrossRevenueRequest_builder{
		Scope: r.scope(), CustomerId: r.customerRef(), ExpireCache: new(r.expire),
	}.Build())
	if err != nil {
		f.t.Fatalf("GetGrossRevenue() error got = %v, want = nil", err)
	}
	var amounts []string
	for _, p := range res.GetPoints() {
		amounts = append(amounts, p.GetAmount().GetAmount())
	}
	return sum(amounts...)
}

func (f *cacheFixture) collection(c *customer.Customer, expire bool) string {
	f.t.Helper()
	cl := portalv1connect.NewCustomerPortalServiceClient(f.s.Connect(f.s.Portal(f.t, c.ID)))
	res, err := cl.GetPortalInvoiceCollection(f.t.Context(), portalv1.GetPortalInvoiceCollectionRequest_builder{ExpireCache: new(expire)}.Build())
	if err != nil {
		f.t.Fatalf("GetPortalInvoiceCollection() error got = %v, want = nil", err)
	}
	var amounts []string
	for _, p := range res.GetPoints() {
		amounts = append(amounts, p.GetAmount().GetAmount())
	}
	return sum(amounts...)
}

// invoice stores a finalized invoice of c in cur, due (and, overdue,
// overdue) months months ago.
func (f *cacheFixture) invoice(c *customer.Customer, months int, cents int64, cur string, overdue bool) {
	f.t.Helper()
	at := rpctest.Now.AddDate(0, -months, 0)
	analyticsInvoice(f.t, f.s, c, at, cents, func(i *invoice.Invoice) {
		i.Currency = money.MustCurrency(cur)
		i.PaymentDueDate = core.DateOf(at, time.UTC)
		i.PaymentOverdue = overdue
	})
}

func (f *cacheFixture) expect(what, got, want string) {
	f.t.Helper()
	if got != want {
		f.t.Errorf("%s got = %s, want = %s", what, got, want)
	}
}

// TestAnalyticsCache ports the analytics cache and its per-customer
// invalidation (lago app/models/analytics/base.rb).
func TestAnalyticsCache(t *testing.T) {
	t.Parallel()
	family := func(t *testing.T, read func(*cacheFixture, report) string, overdue bool) {
		f := newCacheFixture(t)
		c := f.s.Fixtures.Customer()
		f.invoice(c, 1, 100, "EUR", overdue)
		f.invoice(c, 0, 50, "EUR", overdue)
		f.invoice(c, 1, 70, "USD", overdue)
		variants := []report{{customer: c}, {customer: c, currency: "EUR"}, {customer: c, currency: "USD"}, {customer: c, months: 1}}
		for i, want := range []string{"2.20", "1.50", "0.70", "0.50"} {
			f.expect("first read", read(f, variants[i]), want)
		}
		f.invoice(c, 1, 300, "EUR", overdue)
		f.invoice(c, 1, 130, "USD", overdue)
		f.invoice(c, 0, 50, "EUR", overdue)
		for i, want := range []string{"2.20", "1.50", "0.70", "0.50"} {
			f.expect("cached read", read(f, variants[i]), want)
		}
		expiring := variants[0]
		expiring.expire = true
		f.expect("expiring read", read(f, expiring), "7.00")
		for i, want := range []string{"5.00", "2.00", "1.00"} {
			f.expect("sibling read", read(f, variants[i+1]), want)
		}
	}
	orgLevel := func(t *testing.T, read func(*cacheFixture, report) string, overdue bool) {
		f := newCacheFixture(t)
		c := f.s.Fixtures.Customer()
		f.invoice(c, 1, 100, "EUR", overdue)
		f.invoice(c, 0, 50, "EUR", overdue)
		f.invoice(c, 1, 70, "USD", overdue)
		f.expect("customer read", read(f, report{customer: c}), "2.20")
		f.expect("organization read", read(f, report{}), "2.20")
		f.invoice(c, 1, 300, "EUR", overdue)
		f.expect("expiring read", read(f, report{customer: c, expire: true}), "5.20")
		f.expect("organization read", read(f, report{}), "2.20")
	}

	t.Run("overdue balances family", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/scenarios/analytics/customer_cache_invalidation_spec.rb:86")
		family(t, (*cacheFixture).overdue, true)
	})
	t.Run("overdue balances organization entries", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/scenarios/analytics/customer_cache_invalidation_spec.rb:121")
		orgLevel(t, (*cacheFixture).overdue, true)
	})
	t.Run("gross revenues family", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/scenarios/analytics/customer_cache_invalidation_spec.rb:148")
		family(t, (*cacheFixture).gross, false)
	})
	t.Run("gross revenues organization entries", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/scenarios/analytics/customer_cache_invalidation_spec.rb:177")
		orgLevel(t, (*cacheFixture).gross, false)
	})
	t.Run("per-customer isolation", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/scenarios/analytics/customer_cache_invalidation_spec.rb:202")
		f := newCacheFixture(t)
		a, b := f.s.Fixtures.Customer(), f.s.Fixtures.Customer()
		f.invoice(a, 1, 100, "EUR", true)
		f.invoice(b, 1, 70, "EUR", true)
		f.expect("first customer", f.overdue(report{customer: a}), "1.00")
		f.expect("second customer", f.overdue(report{customer: b}), "0.70")
		f.invoice(a, 1, 300, "EUR", true)
		f.invoice(b, 1, 130, "EUR", true)
		f.expect("first customer expiring", f.overdue(report{customer: a, expire: true}), "4.00")
		f.expect("second customer", f.overdue(report{customer: b}), "0.70")
	})
	t.Run("organization expiry is a no-op", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/scenarios/analytics/customer_cache_invalidation_spec.rb:230")
		f := newCacheFixture(t)
		c := f.s.Fixtures.Customer()
		f.invoice(c, 1, 100, "EUR", true)
		f.expect("customer read", f.overdue(report{customer: c}), "1.00")
		f.invoice(c, 1, 300, "EUR", true)
		f.expect("organization expiring read", f.overdue(report{expire: true}), "4.00")
		f.expect("customer read", f.overdue(report{customer: c}), "1.00")
	})
	t.Run("cross-report isolation", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/scenarios/analytics/customer_cache_invalidation_spec.rb:257")
		f := newCacheFixture(t)
		c := f.s.Fixtures.Customer()
		f.invoice(c, 1, 100, "EUR", true)
		f.expect("gross revenue", f.gross(report{customer: c}), "1.00")
		f.invoice(c, 1, 300, "EUR", true)
		f.overdue(report{customer: c, expire: true})
		f.expect("gross revenue", f.gross(report{customer: c}), "1.00")
	})
	t.Run("invoice collections family", func(t *testing.T) {
		t.Parallel()
		// The model-level read of the spec is the customer portal's report,
		// the one taking expire_cache with a customer.
		t.Log("source: spec/scenarios/analytics/customer_cache_invalidation_spec.rb:291")
		f := newCacheFixture(t)
		c := f.s.Fixtures.Customer()
		// The portal reads the customer's currency, USD.
		f.invoice(c, 1, 100, "USD", false)
		f.invoice(c, 1, 70, "USD", false)
		f.invoice(c, 1, 900, "EUR", false)
		f.expect("first read", f.collection(c, false), "1.70")
		f.invoice(c, 1, 300, "USD", false)
		f.expect("cached read", f.collection(c, false), "1.70")
		f.expect("expiring read", f.collection(c, true), "4.70")
	})
	t.Run("expiry and day change", func(t *testing.T) {
		t.Parallel()
		// app/models/analytics/base.rb:14 and :22: kept 4 hours, under a key
		// of the current day.
		f := newCacheFixture(t)
		c := f.s.Fixtures.Customer()
		f.invoice(c, 1, 100, "EUR", true)
		f.expect("first read", f.overdue(report{}), "1.00")
		f.invoice(c, 1, 300, "EUR", true)
		f.s.Clock.Advance(3 * time.Hour)
		f.expect("within 4 hours", f.overdue(report{}), "1.00")
		f.s.Clock.Advance(time.Hour)
		f.expect("after 4 hours", f.overdue(report{}), "4.00")
		f.invoice(c, 1, 500, "EUR", true)
		f.s.Clock.Advance(8 * time.Hour) // 2025-03-16 00:00
		f.expect("next day", f.overdue(report{}), "9.00")
	})
}
