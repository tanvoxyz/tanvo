package streaming_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/tanvoxyz/tanvo/libs/billing/streaming"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var eur = money.MustCurrency("EUR")

// serializerFixture is the usage of the serializer spec: two fees of one
// charge on the api_calls metric, and one EUR wallet that absorbed 15
// credits.
type serializerFixture struct {
	u       *usage.Usage
	ws      []*wallet.Wallet
	charges map[core.ChargeID]streaming.ChargeInfo
	charge  core.ChargeID
	unused  core.ChargeID
}

// mustID parses a fixed identifier.
func mustID[I core.Identifier[I]](s string) I {
	v, err := core.ParseID[I](s)
	if err != nil {
		panic(err)
	}
	return v
}

var (
	chargeID = mustID[core.ChargeID]("0190a000-0000-7000-8000-000000000001")
	unusedID = mustID[core.ChargeID]("0190a000-0000-7000-8000-000000000002")
)

func newSerializerFixture() *serializerFixture {
	f := &serializerFixture{charge: chargeID, unused: unusedID}
	metric := mustID[core.BillableMetricID]("0190a000-0000-7000-8000-000000000003")
	other := mustID[core.BillableMetricID]("0190a000-0000-7000-8000-000000000004")
	f.charges = map[core.ChargeID]streaming.ChargeInfo{
		f.charge: {Code: "api_calls_charge", MetricID: metric, MetricCode: "api_calls"},
		f.unused: {Code: "unused_charge", MetricID: other, MetricCode: "unused"},
	}
	f.u = &usage.Usage{
		From:        time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		To:          time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		IssuingDate: core.NewDate(2026, 9, 30),
		Currency:    eur,
		Amount:      1500,
		TotalAmount: 1500,
		Fees: []*invoice.Fee{
			fee(f.charge, "10.0", 4, 1000),
			fee(f.charge, "5.0", 2, 500),
		},
	}
	f.ws = []*wallet.Wallet{walletOf("0190a000-0000-7000-8000-000000000010", eur, 1500, "15.0")}
	return f
}

func fee(charge core.ChargeID, units string, events, amount int64) *invoice.Fee {
	return &invoice.Fee{Type: core.FeeTypeCharge, ChargeID: charge, Units: money.MustParse(units), EventsCount: events, Amount: amount, Currency: eur}
}

func walletOf(id string, c money.Currency, cents int64, credits string) *wallet.Wallet {
	return &wallet.Wallet{
		ID: mustID[core.WalletID](id), Currency: c,
		OngoingUsageBalanceAmount: cents, CreditsOngoingUsageBalance: money.MustParse(credits),
	}
}

// keys returns the JSON keys of v.
func keys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// source: spec/serializers/event_destinations/customer_usage_serializer_spec.rb:36
func TestSerializeAggregates(t *testing.T) {
	f := newSerializerFixture()
	got := streaming.SerializeUsage(f.u, f.ws, f.charges)
	if got.Currency != "EUR" || got.AmountCents != 1500 {
		t.Errorf("currency, amount: got = %s %d, want = EUR 1500", got.Currency, got.AmountCents)
	}
	want := []streaming.ChargeUsage{{
		Units: "15.0", EventsCount: 6, AmountCents: 1500, AmountCurrency: "EUR",
		Charge:         streaming.Ref{LagoID: f.charge.String(), Code: "api_calls_charge"},
		BillableMetric: streaming.Ref{LagoID: f.charges[f.charge].MetricID.String(), Code: "api_calls"},
	}}
	if diff := cmp.Diff(want, got.ChargesUsage); diff != "" {
		t.Errorf("charges_usage (-want +got):\n%s", diff)
	}
}

func TestSerializeChargesWithoutUsage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		extra  *invoice.Fee
		want   []string
	}{
		{
			name: "omits a charge with neither units nor amount", source: "spec/serializers/event_destinations/customer_usage_serializer_spec.rb:56",
			extra: fee(unusedID, "0.0", 0, 0),
			want:  []string{"api_calls_charge"},
		},
		{
			name: "keeps a charge billed without units", source: "spec/serializers/event_destinations/customer_usage_serializer_spec.rb:63",
			extra: fee(unusedID, "0.0", 0, 250),
			want:  []string{"api_calls_charge", "unused_charge"},
		},
		{
			name: "keeps a charge whose events cancelled out", source: "spec/serializers/event_destinations/customer_usage_serializer_spec.rb:70",
			extra: fee(unusedID, "0.0", 5, 0),
			want:  []string{"api_calls_charge", "unused_charge"},
		},
		{
			name: "keeps a charge with units but nothing to pay", source: "spec/serializers/event_destinations/customer_usage_serializer_spec.rb:79",
			extra: fee(unusedID, "3.0", 3, 0),
			want:  []string{"api_calls_charge", "unused_charge"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSerializerFixture()
			f.u.Fees = append(f.u.Fees, tc.extra)
			got := streaming.SerializeUsage(f.u, f.ws, f.charges)
			var codes []string
			for _, c := range got.ChargesUsage {
				codes = append(codes, c.Charge.Code)
			}
			if !slices.Equal(codes, tc.want) {
				t.Errorf("charges: got = %v, want = %v", codes, tc.want)
			}
			if tc.extra.EventsCount == 5 {
				e := got.ChargesUsage[1]
				if e.Units != "0.0" || e.AmountCents != 0 || e.EventsCount != 5 {
					t.Errorf("entry: got = %s %d %d, want = 0.0 0 5", e.Units, e.AmountCents, e.EventsCount)
				}
			}
		})
	}
}

// source: spec/serializers/event_destinations/customer_usage_serializer_spec.rb:87
func TestSerializeNoBreakdown(t *testing.T) {
	f := newSerializerFixture()
	got := keys(t, streaming.SerializeUsage(f.u, f.ws, f.charges).ChargesUsage[0])
	want := []string{"amount_cents", "amount_currency", "billable_metric", "charge", "events_count", "units"}
	if !slices.Equal(got, want) {
		t.Errorf("keys: got = %v, want = %v", got, want)
	}
}

// source: spec/serializers/event_destinations/customer_usage_serializer_spec.rb:92
// source: spec/serializers/event_destinations/customer_usage_serializer_spec.rb:111
func TestSerializeDatetimes(t *testing.T) {
	f := newSerializerFixture()
	got := streaming.SerializeUsage(f.u, f.ws, f.charges)
	// The period is half-open (domain §2.3): to_datetime is the next
	// midnight, not 23:59:59 of the last day.
	if got.FromDatetime != "2026-09-01T00:00:00Z" || got.ToDatetime != "2026-10-01T00:00:00Z" || got.IssuingDate != "2026-09-30" {
		t.Errorf("datetimes: got = %s %s %s, want = 2026-09-01T00:00:00Z 2026-10-01T00:00:00Z 2026-09-30",
			got.FromDatetime, got.ToDatetime, got.IssuingDate)
	}
	f.u.From = time.Date(2026, 9, 1, 2, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	if got := streaming.SerializeUsage(f.u, f.ws, f.charges).FromDatetime; got != "2026-09-01T00:00:00Z" {
		t.Errorf("from_datetime of a zoned instant: got = %s, want = 2026-09-01T00:00:00Z", got)
	}
}

// source: spec/serializers/event_destinations/customer_usage_serializer_spec.rb:119
// source: spec/serializers/event_destinations/customer_usage_serializer_spec.rb:130
func TestSerializeTopLevelKeys(t *testing.T) {
	f := newSerializerFixture()
	got := keys(t, streaming.SerializeUsage(f.u, f.ws, f.charges))
	want := []string{"amount_cents", "charges_usage", "currency", "from_datetime", "issuing_date", "to_datetime", "wallets"}
	if !slices.Equal(got, want) {
		t.Errorf("keys: got = %v, want = %v (no taxes, no single wallet)", got, want)
	}
}

func TestSerializeWallets(t *testing.T) {
	usd := money.MustCurrency("USD")
	for _, tc := range []struct {
		name   string
		source string
		ws     []*wallet.Wallet
		want   []streaming.WalletUsage
	}{
		{
			name: "reports the ongoing usage allocated to the wallet", source: "spec/serializers/event_destinations/customer_usage_serializer_spec.rb:124",
			ws:   []*wallet.Wallet{walletOf("0190a000-0000-7000-8000-000000000010", eur, 1500, "15.0")},
			want: []streaming.WalletUsage{{LagoID: "0190a000-0000-7000-8000-000000000010", Credits: "15.0", AmountCents: 1500, AmountCurrency: "EUR"}},
		},
		{
			name: "reports each wallet's own share", source: "spec/serializers/event_destinations/customer_usage_serializer_spec.rb:141",
			ws: []*wallet.Wallet{
				walletOf("0190a000-0000-7000-8000-000000000010", eur, 1500, "15.0"),
				walletOf("0190a000-0000-7000-8000-000000000011", eur, 500, "5.0"),
			},
			want: []streaming.WalletUsage{
				{LagoID: "0190a000-0000-7000-8000-000000000010", Credits: "15.0", AmountCents: 1500, AmountCurrency: "EUR"},
				{LagoID: "0190a000-0000-7000-8000-000000000011", Credits: "5.0", AmountCents: 500, AmountCurrency: "EUR"},
			},
		},
		{
			name: "names each wallet's currency", source: "spec/serializers/event_destinations/customer_usage_serializer_spec.rb:154",
			ws: []*wallet.Wallet{
				walletOf("0190a000-0000-7000-8000-000000000010", eur, 1500, "15.0"),
				walletOf("0190a000-0000-7000-8000-000000000011", usd, 200, "2.00000"),
			},
			want: []streaming.WalletUsage{
				{LagoID: "0190a000-0000-7000-8000-000000000010", Credits: "15.0", AmountCents: 1500, AmountCurrency: "EUR"},
				{LagoID: "0190a000-0000-7000-8000-000000000011", Credits: "2.0", AmountCents: 200, AmountCurrency: "USD"},
			},
		},
		{
			name: "sends an empty list without wallets", source: "spec/serializers/event_destinations/customer_usage_serializer_spec.rb:162",
			want: []streaming.WalletUsage{},
		},
		{
			name: "reports a wallet that absorbed nothing at zero", source: "spec/serializers/event_destinations/customer_usage_serializer_spec.rb:171",
			ws:   []*wallet.Wallet{walletOf("0190a000-0000-7000-8000-000000000010", eur, 0, "0")},
			want: []streaming.WalletUsage{{LagoID: "0190a000-0000-7000-8000-000000000010", Credits: "0.0", AmountCents: 0, AmountCurrency: "EUR"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSerializerFixture()
			got := streaming.SerializeUsage(f.u, tc.ws, f.charges)
			if diff := cmp.Diff(tc.want, got.Wallets); diff != "" {
				t.Errorf("wallets (-want +got):\n%s", diff)
			}
			if got.AmountCents != 1500 {
				t.Errorf("amount_cents: got = %d, want = 1500", got.AmountCents)
			}
			b, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			if len(tc.ws) == 0 && string(m["wallets"]) != "[]" {
				t.Errorf("wallets JSON: got = %s, want = []", m["wallets"])
			}
		})
	}
}

func TestRubyDecimal(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"15", "15.0"},
		{"15.000", "15.0"},
		{"0", "0.0"},
		{"0.00000", "0.0"},
		{"0.5", "0.5"},
		{"1.250", "1.25"},
		{"1500", "1500.0"},
		{"-3", "-3.0"},
		{"0.001", "0.001"},
	} {
		if got := streaming.RubyDecimal(money.MustParse(tc.in)); got != tc.want {
			t.Errorf("RubyDecimal(%s): got = %s, want = %s", tc.in, got, tc.want)
		}
	}
}
