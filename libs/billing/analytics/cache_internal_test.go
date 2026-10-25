package analytics

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachetest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/analytics"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// counter computes a report of one row whose amount counts the
// computations.
type counter struct{ n int64 }

func (c *counter) compute() ([]analytics.Usage, error) {
	c.n++
	return []analytics.Usage{{MetricCode: "calls", Amount: c.n}}, nil
}

func TestCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)
	ids := core.UUIDv7Generator{}
	org := core.NewID[core.OrganizationID](ids)
	cust := core.NewID[core.CustomerID](ids)
	scope := analytics.Scope{Now: now, Currency: money.MustCurrency("EUR"), CustomerID: cust}
	read := func(t *testing.T, c *Cache, cnt *counter, sc analytics.Scope, expire bool) int64 {
		t.Helper()
		rows, err := cached(ctx, c, "usage", org, sc, expire, cnt.compute)
		if err != nil {
			t.Fatal(err)
		}
		return rows[0].Amount
	}

	// source: app/models/analytics/base.rb:14
	t.Run("a report is reused until its TTL", func(t *testing.T) {
		t.Parallel()
		at := now
		c := NewCache(cachetest.New(func() time.Time { return at }), time.Hour)
		var cnt counter
		for range 2 {
			if got := read(t, c, &cnt, scope, false); got != 1 {
				t.Errorf("got = %d, want = 1 (cached)", got)
			}
		}
		at = now.Add(time.Hour)
		sc := scope
		sc.Now = at
		if got := read(t, c, &cnt, sc, false); got != 2 {
			t.Errorf("after the TTL: got = %d, want = 2", got)
		}
	})
	t.Run("off", func(t *testing.T) {
		t.Parallel()
		for name, c := range map[string]*Cache{"nil": nil, "no backend": NewCache(nil, time.Hour), "zero TTL": NewCache(cachetest.New(nil), 0)} {
			var cnt counter
			read(t, c, &cnt, scope, false)
			if got := read(t, c, &cnt, scope, false); got != 2 {
				t.Errorf("%s: got = %d, want = 2 (computed twice)", name, got)
			}
		}
	})
	// source: app/models/analytics/base.rb:10-12
	t.Run("expire recomputes the customer's reports", func(t *testing.T) {
		t.Parallel()
		c := NewCache(cachetest.New(nil), time.Hour)
		var cnt counter
		read(t, c, &cnt, scope, false)
		// Twice on a clock that has not moved: each expiring read
		// recomputes.
		for want := int64(2); want <= 3; want++ {
			if got := read(t, c, &cnt, scope, true); got != want {
				t.Errorf("expiring read: got = %d, want = %d", got, want)
			}
		}
		if got := read(t, c, &cnt, scope, false); got != 3 {
			t.Errorf("after: got = %d, want = 3 (cached)", got)
		}
		// Another currency of the customer is expired too.
		other := scope
		other.Currency = money.MustCurrency("USD")
		read(t, c, &cnt, other, false)
		read(t, c, &cnt, other, true)
		if got := read(t, c, &cnt, other, false); got != 5 {
			t.Errorf("other currency: got = %d, want = 5", got)
		}
	})
	t.Run("expire leaves organization reports", func(t *testing.T) {
		t.Parallel()
		c := NewCache(cachetest.New(nil), time.Hour)
		var cnt counter
		sc := scope
		sc.CustomerID = core.CustomerID{}
		read(t, c, &cnt, sc, false)
		if got := read(t, c, &cnt, sc, true); got != 1 {
			t.Errorf("got = %d, want = 1 (cached)", got)
		}
	})
	// source: app/models/analytics/base.rb:42-49
	t.Run("the version token is kept for a day", func(t *testing.T) {
		t.Parallel()
		at := now
		backend := cachetest.New(func() time.Time { return at })
		c := NewCache(backend, time.Hour)
		k := versionKey("usage", org, cust)
		var cnt counter
		read(t, c, &cnt, scope, false)
		if _, ok := backend.Get(ctx, k); !ok {
			t.Fatal("token: got = none, want = seeded")
		}
		at = now.Add(versionTTL)
		if _, ok := backend.Get(ctx, k); ok {
			t.Error("token after a day: got = kept, want = expired")
		}
	})
	// Two server replicas over one Redis.
	t.Run("caches over one backend share reports and versions", func(t *testing.T) {
		t.Parallel()
		backend := cachetest.New(nil)
		a, b := NewCache(backend, time.Hour), NewCache(backend, time.Hour)
		var cnt counter
		read(t, a, &cnt, scope, false)
		if got := read(t, b, &cnt, scope, false); got != 1 {
			t.Errorf("b: got = %d, want = 1 (a's report)", got)
		}
		read(t, a, &cnt, scope, true)
		if got := read(t, b, &cnt, scope, false); got != 2 {
			t.Errorf("b after a expired: got = %d, want = 2 (a's recomputed report)", got)
		}
		if got := read(t, b, &cnt, scope, true); got != 3 {
			t.Errorf("b expiring: got = %d, want = 3", got)
		}
		if got := read(t, a, &cnt, scope, false); got != 3 {
			t.Errorf("a after b expired: got = %d, want = 3", got)
		}
	})
}

// TestCacheRoundTrip checks that every report row type comes back from
// the cache equal to what was computed.
func TestCacheRoundTrip(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)
	month := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	ids := core.UUIDv7Generator{}
	entity := core.NewID[core.BillingEntityID](ids)
	eur, jpy := money.MustCurrency("EUR"), money.MustCurrency("JPY")
	amount := int64(-12)
	c := NewCache(cachetest.New(nil), time.Hour)
	sc := analytics.Scope{Now: now}

	roundTrip(t, c, "revenue", sc, []analytics.Revenue{
		{Month: month, Currency: eur, Amount: &amount, InvoicesCount: 3, BillingEntityID: entity},
		{Month: month.AddDate(0, -1, 0)},
	})
	roundTrip(t, c, "collection", sc, []analytics.Collection{{Month: month, PaymentStatus: core.PaymentStatusSucceeded, Currency: jpy, InvoicesCount: 2, Amount: 500}})
	roundTrip(t, c, "usage", sc, []analytics.Usage{{Month: month, MetricCode: "calls", Currency: eur, Amount: 1}})
	roundTrip(t, c, "overdue", sc, []analytics.Overdue{
		{Month: month, Currency: eur, Amount: 9, InvoiceIDs: []core.InvoiceID{core.NewID[core.InvoiceID](ids)}, BillingEntityID: entity},
		{Month: month, InvoiceIDs: []core.InvoiceID{}},
	})
	roundTrip(t, c, "empty", sc, []analytics.Usage{})
	roundTrip[analytics.Usage](t, c, "nil", sc, nil)
}

// roundTrip reads the report kind computing rows twice, and checks that
// the second read comes from the cache equal to rows.
func roundTrip[T any](t *testing.T, c *Cache, kind string, sc analytics.Scope, rows []T) {
	t.Helper()
	ctx := context.Background()
	org := core.NewID[core.OrganizationID](core.UUIDv7Generator{})
	n := 0
	compute := func() ([]T, error) {
		n++
		return rows, nil
	}
	for i := range 2 {
		got, err := cached(ctx, c, kind, org, sc, false, compute)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, rows) {
			t.Errorf("%s read %d: got = %+v, want = %+v", kind, i, got, rows)
		}
	}
	if n != 1 {
		t.Errorf("%s: got = %d computations, want = 1", kind, n)
	}
}
