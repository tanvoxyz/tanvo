package kernel_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/internal/cachetest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestUsageCache(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	entry := func(amount int64) kernel.UsageCacheEntry {
		return kernel.UsageCacheEntry{
			Fees:      []*invoice.Fee{{Amount: amount, GroupedBy: map[string]string{"k": "v"}}},
			CachedAt:  now.Add(-time.Hour),
			ExpiresAt: now.Add(time.Hour),
		}
	}
	clock := func() time.Time { return now }

	t.Run("off without a backend", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewUsageCache(nil)
		if c != nil {
			t.Errorf("got = %v, want = nil", c)
		}
		c.Put(ctx, "a", entry(10), now)
		if _, ok := c.Get(ctx, "a", now); ok {
			t.Errorf("got = hit, want = miss")
		}
	})
	// source: spec/services/cache_service_spec.rb:30
	// source: spec/services/cache_service_spec.rb:48
	t.Run("get returns what was put", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewUsageCache(cachetest.New(clock))
		c.Put(ctx, "a", entry(10), now)
		e, ok := c.Get(ctx, "a", now)
		if !ok || len(e.Fees) != 1 || e.Fees[0].Amount != 10 || !e.CachedAt.Equal(now.Add(-time.Hour)) {
			t.Fatalf("got = %+v %v, want = the entry", e, ok)
		}
		if _, ok := c.Get(ctx, "b", now); ok {
			t.Errorf("missing key: got = hit, want = miss")
		}
	})
	t.Run("the entry lives until its end", func(t *testing.T) {
		t.Parallel()
		at := now
		backend := cachetest.New(func() time.Time { return at })
		c := kernel.NewUsageCache(backend)
		c.Put(ctx, "a", entry(10), now)
		at = now.Add(time.Hour - time.Nanosecond)
		if _, ok := c.Get(ctx, "a", at); !ok {
			t.Errorf("before the end: got = miss, want = hit")
		}
		at = now.Add(time.Hour)
		if _, ok := c.Get(ctx, "a", at); ok {
			t.Errorf("at the end: got = hit, want = miss")
		}
		if backend.Len() != 0 {
			t.Errorf("got = %d entries, want = 0", backend.Len())
		}
	})
	t.Run("an entry read past its end is dropped", func(t *testing.T) {
		t.Parallel()
		backend := cachetest.New(clock)
		c := kernel.NewUsageCache(backend)
		c.Put(ctx, "a", entry(10), now)
		if _, ok := c.Get(ctx, "a", now.Add(time.Hour)); ok {
			t.Errorf("got = hit at expiry, want = miss")
		}
		if backend.Len() != 0 {
			t.Errorf("got = %d entries, want = 0", backend.Len())
		}
	})
	t.Run("an ended entry is not stored", func(t *testing.T) {
		t.Parallel()
		backend := cachetest.New(clock)
		kernel.NewUsageCache(backend).Put(ctx, "a", entry(10), now.Add(time.Hour))
		if backend.Len() != 0 {
			t.Errorf("got = %d entries, want = 0", backend.Len())
		}
	})
	t.Run("an undecodable entry is a miss", func(t *testing.T) {
		t.Parallel()
		backend := cachetest.New(clock)
		backend.Set(ctx, "a", []byte("not json"), time.Hour)
		if _, ok := kernel.NewUsageCache(backend).Get(ctx, "a", now); ok {
			t.Errorf("got = hit, want = miss")
		}
		if backend.Len() != 0 {
			t.Errorf("got = %d entries, want = 0", backend.Len())
		}
	})
	t.Run("fees are copied in and out", func(t *testing.T) {
		t.Parallel()
		c := kernel.NewUsageCache(cachetest.New(clock))
		in := entry(10)
		c.Put(ctx, "a", in, now)
		in.Fees[0].Amount = 99
		in.Fees[0].GroupedBy["k"] = "changed"
		out, _ := c.Get(ctx, "a", now)
		out.Fees[0].Amount = 77
		again, _ := c.Get(ctx, "a", now)
		if f := again.Fees[0]; f.Amount != 10 || f.GroupedBy["k"] != "v" {
			t.Errorf("got = %d %v, want = 10 map[k:v]", f.Amount, f.GroupedBy)
		}
	})
	// Two server replicas over one Redis: an entry either computes is
	// served by the other.
	t.Run("caches over one backend share their entries", func(t *testing.T) {
		t.Parallel()
		backend := cachetest.New(clock)
		a, b := kernel.NewUsageCache(backend), kernel.NewUsageCache(backend)
		a.Put(ctx, "k", entry(10), now)
		e, ok := b.Get(ctx, "k", now)
		if !ok || e.Fees[0].Amount != 10 {
			t.Fatalf("got = %+v %v, want = the other cache's entry", e, ok)
		}
		b.Put(ctx, "k", entry(20), now)
		if e, _ := a.Get(ctx, "k", now); e.Fees[0].Amount != 20 {
			t.Errorf("replaced: got = %d, want = 20", e.Fees[0].Amount)
		}
	})
}

// TestUsageCacheFeeRoundTrip checks that a fee with every field set comes
// back from the cache equal to what was put.
func TestUsageCacheFeeRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	ids := core.UUIDv7Generator{}
	str := func(s string) *string { return &s }
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	eu := "eu"
	pricingUnit, err := money.NewCustomCurrency("credits", "Credits", 4)
	if err != nil {
		t.Fatal(err)
	}
	full := func(cur money.Currency) *invoice.Fee {
		return &invoice.Fee{
			ID: core.NewID[core.FeeID](ids), OrganizationID: core.NewID[core.OrganizationID](ids),
			InvoiceID: core.NewID[core.InvoiceID](ids), SubscriptionID: core.NewID[core.SubscriptionID](ids),
			Type: core.FeeTypeCharge, ChargeID: core.NewID[core.ChargeID](ids), ChargeFilterID: core.NewID[core.ChargeFilterID](ids),
			FixedChargeID: core.NewID[core.FixedChargeID](ids), AddOnID: core.NewID[core.AddOnID](ids),
			CommitmentID: core.NewID[core.CommitmentID](ids), WalletTransactionID: core.NewID[core.WalletTransactionID](ids),
			SubscriptionRateCardID: core.NewID[core.SubscriptionRateCardID](ids), ProductID: core.NewID[core.ProductID](ids),
			ProductFilterID: core.NewID[core.ProductFilterID](ids), RateCardRateID: core.NewID[core.RateCardRateID](ids),
			RatePhaseID: core.NewID[core.RatePhaseID](ids),
			GroupedBy:   map[string]string{"region": "eu", "empty": ""}, InvoiceDisplayName: str("Calls"), Description: str(""),
			Amount: 1234, PreciseAmount: money.MustParse("1234.5678901234567890123"), UnitAmount: 12,
			PreciseUnitAmount: money.MustParse("0.000001"), Units: money.MustParse("100.10"),
			TotalAggregatedUnits: money.MustParse("-3.50"), EventsCount: 7,
			// Whitespace and key order are kept as they are.
			AmountDetails: json.RawMessage(`{"b": 1,  "a": [ "x" ]}`),
			TaxesAmount:   99, TaxesPreciseAmount: money.MustParse("98.765"), TaxesRate: money.MustParse("20"),
			TaxesBaseRate: money.MustParse("1.0"), PreciseCouponsAmount: money.MustParse("0.5"),
			PreciseCreditNotesAmount: money.MustParse("1e-3"), Currency: cur, PayInAdvance: true, Invoiceable: true,
			PaymentStatus: invoice.FeePaymentStatusSucceeded,
			Properties: invoice.FeeProperties{
				From: now.Add(-48 * time.Hour), To: now, ChargesFrom: now.Add(-24 * time.Hour), ChargesTo: now.Add(time.Nanosecond),
				Timestamp: now.Add(time.Second), ChargesDurationDays: 31, FixedChargesFrom: now.Add(-time.Hour),
				FixedChargesTo: now.Add(time.Hour), FixedChargesDurationDays: 30,
			},
			TrueUpParentFeeID: core.NewID[core.FeeID](ids), OriginalFeeID: core.NewID[core.FeeID](ids),
			PricingUnitUsage: &invoice.PricingUnitUsage{
				PricingUnitID: core.NewID[core.PricingUnitID](ids), ShortName: "CR", ConversionRate: money.MustParse("0.25"),
				Amount: money.MustParse("10"), PreciseAmount: money.MustParse("10.0001"), UnitAmount: money.MustParse("1"),
				PreciseUnitAmount: money.MustParse("1.00001"),
			},
			AppliedTaxes: []invoice.FeeAppliedTax{{
				ID: core.NewID[core.FeeAppliedTaxID](ids), TaxID: core.NewID[core.TaxID](ids), TaxCode: "vat", TaxName: "VAT",
				TaxDescription: str("Value added tax"), TaxRate: money.MustParse("20.00"), Amount: 99, PreciseAmount: money.MustParse("98.765"),
			}},
			PresentationBreakdowns: []invoice.PresentationBreakdown{
				{PresentationBy: map[string]*string{"region": &eu, "none": nil}, Units: money.MustParse("60.10")},
				{PresentationBy: map[string]*string{}, Units: money.MustParse("40")},
			},
			PayInAdvanceEventID: core.NewID[core.EventID](ids), PayInAdvanceEventTransactionID: "tr_1",
			SucceededAt: at(time.Minute), FailedAt: at(2 * time.Minute), RefundedAt: at(3 * time.Minute),
			CreatedAt: now.Add(-time.Microsecond), UpdatedAt: now, DeletedAt: at(4 * time.Minute),
		}
	}
	// Every field is set, so that a field added to the fee without an
	// encoding fails here.
	v := reflect.ValueOf(*full(money.MustCurrency("EUR")))
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("field %s: got = zero, want = set", v.Type().Field(i).Name)
		}
	}
	empty := &invoice.Fee{Type: core.FeeTypeCharge, Currency: money.MustCurrency("USD"), Properties: invoice.FeeProperties{}}
	sparse := &invoice.Fee{GroupedBy: map[string]string{}, AppliedTaxes: []invoice.FeeAppliedTax{}, AmountDetails: json.RawMessage{}}

	in := kernel.UsageCacheEntry{
		Fees:      []*invoice.Fee{full(money.MustCurrency("EUR")), full(money.MustCurrency("JPY")), full(pricingUnit), empty, sparse},
		CachedAt:  now.Add(-time.Second),
		ExpiresAt: now.Add(time.Hour),
	}
	c := kernel.NewUsageCache(cachetest.New(func() time.Time { return now }))
	c.Put(ctx, "k", in, now)
	got, ok := c.Get(ctx, "k", now)
	if !ok {
		t.Fatal("got = miss, want = hit")
	}
	if !reflect.DeepEqual(got, in) {
		for i := range in.Fees {
			if i < len(got.Fees) && !reflect.DeepEqual(got.Fees[i], in.Fees[i]) {
				t.Errorf("fee %d: got = %+v, want = %+v", i, got.Fees[i], in.Fees[i])
			}
		}
		t.Errorf("got = %+v, want = %+v", got, in)
	}
}
