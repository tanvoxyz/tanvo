package usage

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

func TestProject(t *testing.T) {
	t.Parallel()
	utc := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	eur := money.MustCurrency("EUR")
	filterID := core.MustParseID[core.ChargeFilterID]("0190a000-0000-7000-8000-000000000001")
	unit, err := money.NewCustomCurrency("CR", "Credit", catalog.PricingUnitExponent)
	if err != nil {
		t.Fatal(err)
	}
	pu := &pricing.AppliedPricingUnit{Unit: unit, ConversionRate: money.MustParse("0.5")}
	standard := `{"amount":"0.1"}`
	type want struct {
		units  string
		amount int64
		pu     *int64
	}
	i64 := func(v int64) *int64 { return &v }
	cases := []struct {
		name      string
		model     catalog.ChargeModel
		props     string
		filter    string
		recurring bool
		units     string
		amount    int64
		precise   string
		currency  money.Currency
		puUsage   *invoice.PricingUnitUsage
		pu        *pricing.AppliedPricingUnit
		from, to  time.Time
		now       time.Time
		loc       string
		want      want
	}{
		// source: spec/services/fees/projection_service_spec.rb:63
		{name: "reprices the projected units", want: want{"20", 200, nil}},
		// source: spec/services/fees/projection_service_spec.rb:96
		{
			name: "graduated", model: catalog.ChargeModelGraduated,
			props: `{"graduated_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"0.1","flat_amount":"0"},{"from_value":11,"to_value":null,"per_unit_amount":"0.05","flat_amount":"0"}]}`,
			want:  want{"20", 150, nil},
		},
		// source: spec/services/fees/projection_service_spec.rb:104
		{name: "package", model: catalog.ChargeModelPackage, props: `{"amount":"1","free_units":5,"package_size":10}`, want: want{"20", 200, nil}},
		// source: spec/services/fees/projection_service_spec.rb:123
		{
			name: "volume", model: catalog.ChargeModelVolume,
			props: `{"volume_ranges":[{"from_value":0,"to_value":10,"per_unit_amount":"0.1","flat_amount":"0"},{"from_value":11,"to_value":null,"per_unit_amount":"0.05","flat_amount":"0"}]}`,
			want:  want{"20", 100, nil},
		},
		// source: spec/services/fees/projection_service_spec.rb:132
		{name: "negative units", units: "-4", amount: 0, precise: "0", want: want{"0", 0, nil}},
		// source: spec/services/fees/projection_service_spec.rb:140
		{name: "filter properties", filter: `{"amount":"1"}`, want: want{"20", 2000, nil}},
		// source: spec/services/fees/projection_service_spec.rb:148
		{name: "grouped charge", props: `{"amount":"0.1","pricing_group_keys":["region"]}`, want: want{"20", 200, nil}},
		// source: spec/services/fees/projection_service_spec.rb:157
		{name: "percentage scales", model: catalog.ChargeModelPercentage, props: `{"rate":"1"}`, precise: "123.4", want: want{"20", 247, nil}},
		// source: spec/services/fees/projection_service_spec.rb:172
		{
			name: "pricing unit repriced", amount: 50, precise: "50", pu: pu,
			puUsage: &invoice.PricingUnitUsage{Amount: money.NewFromInt(100), PreciseAmount: money.MustParse("100.6"), ConversionRate: money.MustParse("0.5")},
			want:    want{"20", 100, i64(200)},
		},
		// source: spec/services/fees/projection_service_spec.rb:179
		{
			name: "pricing unit scaled", model: catalog.ChargeModelPercentage, props: `{"rate":"1"}`, amount: 50, precise: "50", pu: pu,
			puUsage: &invoice.PricingUnitUsage{Amount: money.NewFromInt(100), PreciseAmount: money.MustParse("100.6"), ConversionRate: money.MustParse("0.5")},
			want:    want{"20", 100, i64(201)},
		},
		// source: spec/services/fees/projection_service_spec.rb:204
		{name: "recurring metric", recurring: true, want: want{"10", 100, nil}},
		// source: spec/services/fees/projection_service_spec.rb:216
		{name: "period not started", now: utc("2024-12-31T00:00:00Z"), want: want{"0", 0, nil}},
		// source: spec/services/fees/projection_service_spec.rb:224
		{name: "period over", now: utc("2025-01-11T00:59:59Z"), want: want{"10", 100, nil}},
		// source: spec/services/fees/projection_service_spec.rb:235
		// 10 days after the local start is a local midnight: lago counts
		// that day, 11 of 31.
		{
			name: "customer time zone", loc: "America/New_York",
			from: utc("2025-01-01T05:00:00Z"), to: utc("2025-02-01T05:00:00Z"), now: utc("2025-01-11T05:00:00Z"),
			want: want{"28.18", 282, nil},
		},
		// source: spec/services/fees/projection_service_spec.rb:244
		{name: "currency exponent", currency: money.MustCurrency("KWD"), amount: 1000, precise: "1000", want: want{"20", 2000, nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.model == "" {
				tc.model = catalog.ChargeModelStandard
			}
			if tc.props == "" {
				tc.props = standard
			}
			if tc.units == "" {
				tc.units = "10"
			}
			if tc.precise == "" {
				tc.amount, tc.precise = 100, "100"
			}
			if tc.currency.IsZero() {
				tc.currency = eur
			}
			if tc.from.IsZero() {
				tc.from, tc.to = utc("2025-01-01T00:00:00Z"), utc("2025-01-11T00:00:00Z")
			}
			if tc.now.IsZero() {
				tc.now = utc("2025-01-05T12:00:00Z")
			}
			loc := time.UTC
			if tc.loc != "" {
				var err error
				if loc, err = time.LoadLocation(tc.loc); err != nil {
					t.Fatal(err)
				}
			}
			c := &catalog.Charge{ChargeModel: tc.model, Properties: catalog.MustProperties(tc.props)}
			f := &invoice.Fee{
				Units: money.MustParse(tc.units), Amount: tc.amount, PreciseAmount: money.MustParse(tc.precise),
				Currency: tc.currency, PricingUnitUsage: tc.puUsage,
				Properties: invoice.FeeProperties{ChargesFrom: tc.from, ChargesTo: tc.to},
			}
			if tc.filter != "" {
				c.Filters = []catalog.ChargeFilter{{ID: filterID, Properties: catalog.MustProperties(tc.filter)}}
				f.ChargeFilterID = filterID
			}
			p, err := project(f, c, &catalog.BillableMetric{Recurring: tc.recurring}, tc.pu, tc.now, loc)
			if err != nil {
				t.Fatalf("project() error = %v", err)
			}
			if !p.Units.Equal(money.MustParse(tc.want.units)) || p.Amount != tc.want.amount {
				t.Errorf("project() = (%s, %d), want = (%s, %d)", p.Units, p.Amount, tc.want.units, tc.want.amount)
			}
			switch {
			case (p.PricingUnitAmount == nil) != (tc.want.pu == nil):
				t.Errorf("pricing unit amount = %v, want = %v", p.PricingUnitAmount, tc.want.pu)
			case p.PricingUnitAmount != nil && *p.PricingUnitAmount != *tc.want.pu:
				t.Errorf("pricing unit amount = %d, want = %d", *p.PricingUnitAmount, *tc.want.pu)
			}
		})
	}
}

func TestProjectPresentationBreakdowns(t *testing.T) {
	t.Parallel()
	utc := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name      string
		recurring bool
		units     string
		now       time.Time
		want      string
	}{
		// source: spec/services/fees/projection_service_spec.rb:190
		{name: "scaled", units: "60.33642", now: utc("2025-01-05T12:00:00Z"), want: "120.67"},
		// source: spec/services/fees/projection_service_spec.rb:204
		{name: "recurring metric", recurring: true, units: "60", now: utc("2025-01-05T12:00:00Z"), want: "60"},
		// source: spec/services/fees/projection_service_spec.rb:216
		{name: "period not started", units: "60", now: utc("2024-12-31T00:00:00Z"), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dept := "engineering"
			c := &catalog.Charge{ChargeModel: catalog.ChargeModelStandard, Properties: catalog.MustProperties(`{"amount":"0.1"}`)}
			f := &invoice.Fee{
				Units: money.MustParse("10"), Amount: 100, PreciseAmount: money.MustParse("100"),
				Currency:   money.MustCurrency("EUR"),
				Properties: invoice.FeeProperties{ChargesFrom: utc("2025-01-01T00:00:00Z"), ChargesTo: utc("2025-01-11T00:00:00Z")},
				PresentationBreakdowns: []invoice.PresentationBreakdown{
					{PresentationBy: map[string]*string{"department": &dept}, Units: money.MustParse(tc.units)},
				},
			}
			p, err := project(f, c, &catalog.BillableMetric{Recurring: tc.recurring}, nil, tc.now, time.UTC)
			if err != nil {
				t.Fatalf("project() error = %v", err)
			}
			if tc.want == "" {
				if len(p.PresentationBreakdowns) != 0 {
					t.Errorf("breakdowns = %v, want = none", p.PresentationBreakdowns)
				}
				return
			}
			if len(p.PresentationBreakdowns) != 1 {
				t.Fatalf("got = %d breakdowns, want = 1", len(p.PresentationBreakdowns))
			}
			b := p.PresentationBreakdowns[0]
			if v := b.PresentationBy["department"]; v == nil || *v != dept {
				t.Errorf("presentation_by = %v, want = department=engineering", b.PresentationBy)
			}
			if !b.Units.Equal(money.MustParse(tc.want)) {
				t.Errorf("units = %s, want = %s", b.Units, tc.want)
			}
		})
	}
}
