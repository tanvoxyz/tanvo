package integrations

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachetest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// cacheTransaction is a draft tax request of two lines.
func cacheTransaction() *integration.TaxTransaction {
	return &integration.TaxTransaction{
		IssuingDate: core.NewDate(2026, time.March, 1),
		Currency:    money.MustCurrency("USD"),
		Customer:    integration.TaxCustomer{ExternalID: "cus_1", Address: core.Address{City: "Paris", Country: "FR"}},
		Lines: []integration.TaxLine{
			{ItemKey: "key_0", ItemID: "id_0", ItemCode: "m1", AmountCents: 100, Units: money.One},
			{ItemKey: "key_1", ItemID: "id_1", ItemCode: "m2", AmountCents: 200, Units: money.One},
		},
	}
}

// cacheAnswer is the provider's answer to [cacheTransaction].
func cacheAnswer(t *integration.TaxTransaction) *integration.TaxResult {
	res := &integration.TaxResult{}
	for _, l := range t.Lines {
		res.Lines = append(res.Lines, integration.TaxLineResult{
			ItemKey: l.ItemKey, ItemID: l.ItemID, ItemCode: l.ItemCode, AmountCents: money.NewFromInt(l.AmountCents),
		})
	}
	return res
}

func TestDraftTaxCacheKey(t *testing.T) {
	updated := time.Date(2026, time.October, 5, 10, 0, 0, 100_000_000, time.UTC)
	integ := &integration.Integration{ID: core.MustParseID[core.IntegrationID]("01000000-0000-7000-8000-000000000001"), UpdatedAt: updated}
	c := NewDraftTaxCache(cachetest.New(nil), time.Hour, nil)
	key := c.key(integ, cacheTransaction())

	// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:76
	if want := "anrok-draft-taxes/1/" + integ.ID.String() + "/2026-10-05T10:00:00.100000Z/"; !strings.HasPrefix(key, want) {
		t.Errorf("key: got = %q, want = prefix %q", key, want)
	}
	tests := []struct {
		name, source string
		change       func(*integration.TaxTransaction)
	}{
		{"customer address", "spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:100", func(t *integration.TaxTransaction) {
			t.Customer.Address.City = "Lyon"
		}},
		{"product mapping", "spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:108", func(t *integration.TaxTransaction) {
			t.Lines[0].ItemCode = "m3"
		}},
		{"line order", "spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:116", func(t *integration.TaxTransaction) {
			t.Lines[0], t.Lines[1] = t.Lines[1], t.Lines[0]
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Log("source: " + tt.source)
			other := cacheTransaction()
			tt.change(other)
			if c.key(integ, other) == key {
				t.Errorf("key: got = unchanged, want = changed")
			}
		})
	}
}

func TestDraftTaxCacheHit(t *testing.T) {
	ctx := context.Background()
	integ := &integration.Integration{ID: core.MustParseID[core.IntegrationID](uuid.NewString()), UpdatedAt: time.Now()}
	c := NewDraftTaxCache(cachetest.New(nil), time.Hour, nil)
	first := cacheTransaction()
	answer := cacheAnswer(first)
	c.put(ctx, c.key(integ, first), answer)

	next := cacheTransaction()
	for n := range next.Lines {
		next.Lines[n].ItemKey, next.Lines[n].ItemID = "new_"+next.Lines[n].ItemKey, "new_"+next.Lines[n].ItemID
	}
	got, ok := c.get(ctx, c.key(integ, next), next)
	if !ok {
		t.Fatal("hit: got = miss, want = hit")
	}
	// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:153
	for n, l := range got.Lines {
		if l.ItemKey != next.Lines[n].ItemKey || l.ItemID != next.Lines[n].ItemID || l.AmountCents.Cmp(answer.Lines[n].AmountCents) != 0 {
			t.Errorf("line %d: got = %s %s %s, want = %s %s %s", n, l.ItemKey, l.ItemID, l.AmountCents, next.Lines[n].ItemKey, next.Lines[n].ItemID, answer.Lines[n].AmountCents)
		}
	}
	// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:165
	again, ok := c.get(ctx, c.key(integ, first), first)
	if !ok || again.Lines[0].ItemKey != "key_0" || answer.Lines[0].ItemKey != "key_0" {
		t.Errorf("cached answer: got = %+v, want = untouched", answer.Lines)
	}
}

// TestDraftTaxCacheRoundTrip checks that a fully populated answer comes
// back from the cache equal to what was put, its identifiers aside.
func TestDraftTaxCacheRoundTrip(t *testing.T) {
	ctx := context.Background()
	integ := &integration.Integration{ID: core.MustParseID[core.IntegrationID](uuid.NewString()), UpdatedAt: time.Now()}
	c := NewDraftTaxCache(cachetest.New(nil), time.Hour, nil)
	tx := cacheTransaction()
	rate, tax := money.MustParse("0.0825"), int64(17)
	answer := &integration.TaxResult{TransactionID: "tr_1", Lines: []integration.TaxLineResult{
		{ItemKey: "key_0", ItemID: "id_0", ItemCode: "m1", AmountCents: money.MustParse("100.50"), TaxAmountCents: &tax, Breakdown: []integration.TaxBreakdown{
			{Name: "State", Reason: "taxable", Type: "sales", Rate: &rate, TaxAmount: money.MustParse("8.29")},
			{Name: "Exempt", TaxAmount: money.Zero},
		}},
		{ItemKey: "key_1", ItemID: "id_1", ItemCode: "m2", AmountCents: money.MustParse("200")},
	}}
	c.put(ctx, c.key(integ, tx), answer)
	got, ok := c.get(ctx, c.key(integ, tx), tx)
	if !ok {
		t.Fatal("got = miss, want = hit")
	}
	if !reflect.DeepEqual(got, answer) {
		t.Errorf("got = %+v, want = %+v", got, answer)
	}
}

// TestDraftTaxCacheShared checks that two caches over one backend, as two
// server replicas over Redis, reuse each other's answers.
func TestDraftTaxCacheShared(t *testing.T) {
	ctx := context.Background()
	integ := &integration.Integration{ID: core.MustParseID[core.IntegrationID](uuid.NewString()), UpdatedAt: time.Now()}
	backend := cachetest.New(nil)
	a, b := NewDraftTaxCache(backend, time.Hour, nil), NewDraftTaxCache(backend, time.Hour, nil)
	tx := cacheTransaction()
	if _, ok := b.get(ctx, b.key(integ, tx), tx); ok {
		t.Fatal("before: got = hit, want = miss")
	}
	a.put(ctx, a.key(integ, tx), cacheAnswer(tx))
	if _, ok := b.get(ctx, b.key(integ, tx), tx); !ok {
		t.Error("after: got = miss, want = the other cache's answer")
	}
}

func TestDraftTaxCacheTTL(t *testing.T) {
	ctx := context.Background()
	integ := &integration.Integration{ID: core.MustParseID[core.IntegrationID](uuid.NewString()), UpdatedAt: time.Now()}
	tx := cacheTransaction()

	// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:187
	t.Run("configured", func(t *testing.T) {
		clock := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
		backend := cachetest.New(func() time.Time { return clock })
		c := NewDraftTaxCache(backend, time.Minute, func() time.Time { return clock })
		key := c.key(integ, tx)
		c.put(ctx, key, cacheAnswer(tx))
		clock = clock.Add(time.Minute - time.Nanosecond)
		if _, ok := c.get(ctx, key, tx); !ok {
			t.Error("before the TTL: got = miss, want = hit")
		}
		clock = clock.Add(time.Nanosecond)
		if _, ok := c.get(ctx, key, tx); ok {
			t.Error("at the TTL: got = hit, want = miss")
		}
	})
	// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:200
	t.Run("zero", func(t *testing.T) {
		backend := cachetest.New(nil)
		c := NewDraftTaxCache(backend, 0, nil)
		c.put(ctx, c.key(integ, tx), cacheAnswer(tx))
		if backend.Len() != 0 {
			t.Errorf("entries: got = %d, want = 0", backend.Len())
		}
	})
	// source: spec/services/integrations/aggregator/taxes/invoices/draft_taxes_cache_service_spec.rb:211
	t.Run("zero with an answer cached before", func(t *testing.T) {
		backend := cachetest.New(nil)
		c := NewDraftTaxCache(backend, time.Hour, nil)
		c.put(ctx, c.key(integ, tx), cacheAnswer(tx))
		c = NewDraftTaxCache(backend, 0, nil)
		if _, ok := c.get(ctx, c.key(integ, tx), tx); ok {
			t.Error("get: got = hit, want = miss")
		}
	})
	t.Run("off without a backend", func(t *testing.T) {
		c := NewDraftTaxCache(nil, time.Hour, nil)
		c.put(ctx, c.key(integ, tx), cacheAnswer(tx))
		if _, ok := c.get(ctx, c.key(integ, tx), tx); ok {
			t.Error("get: got = hit, want = miss")
		}
	})
}
