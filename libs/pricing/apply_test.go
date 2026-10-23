package pricing

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// testAggregation is the JSON form of an [AggregationResult].
type testAggregation struct {
	Units                   money.Decimal     `json:"units"`
	FullUnits               *money.Decimal    `json:"full_units"`
	CurrentUsageUnits       *money.Decimal    `json:"current_usage_units"`
	Count                   int64             `json:"count"`
	TotalAggregatedUnits    *money.Decimal    `json:"total_aggregated_units"`
	PreciseTotalAmountCents *money.Decimal    `json:"precise_total_amount_cents"`
	RunningTotal            []money.Decimal   `json:"running_total"`
	PerEvent                *testPerEvent     `json:"per_event"`
	Groups                  []testAggregation `json:"groups"`
	GroupedBy               map[string]string `json:"grouped_by"`
	CurrentAggregation      *money.Decimal    `json:"current_aggregation"`
	MaxAggregation          *money.Decimal    `json:"max_aggregation"`
	UnitsApplied            *money.Decimal    `json:"units_applied"`
}

type testPerEvent struct {
	Values   []money.Decimal `json:"values"`
	Prorated []money.Decimal `json:"prorated"`
}

func (a testAggregation) result() AggregationResult {
	r := AggregationResult{
		Units:                   a.Units,
		FullUnits:               a.FullUnits,
		CurrentUsageUnits:       a.CurrentUsageUnits,
		Count:                   a.Count,
		TotalAggregatedUnits:    a.TotalAggregatedUnits,
		PreciseTotalAmountCents: a.PreciseTotalAmountCents,
		RunningTotal:            a.RunningTotal,
		GroupedBy:               a.GroupedBy,
		CurrentAggregation:      a.CurrentAggregation,
		MaxAggregation:          a.MaxAggregation,
		UnitsApplied:            a.UnitsApplied,
	}
	if a.PerEvent != nil {
		r.PerEvent = &PerEventAggregation{Values: a.PerEvent.Values, Prorated: a.PerEvent.Prorated}
	}
	if a.Groups != nil {
		r.Groups = make([]AggregationResult, 0, len(a.Groups))
		for _, g := range a.Groups {
			r.Groups = append(r.Groups, g.result())
		}
	}
	return r
}

// testStructure is the JSON form of a [PricingStructure].
type testStructure struct {
	Model               ChargeModel     `json:"model"`
	Properties          json.RawMessage `json:"properties"`
	Prorated            bool            `json:"prorated"`
	ProductCatalog      bool            `json:"product_catalog"`
	AcceptsTargetWallet bool            `json:"accepts_target_wallet"`
	Currency            string          `json:"currency"`
	// NoCurrency leaves the currency out.
	NoCurrency bool `json:"no_currency"`
}

func (s testStructure) structure(t *testing.T) PricingStructure {
	t.Helper()
	p, err := DecodeProperties(s.Model, s.Properties)
	if err != nil {
		t.Fatal(err)
	}
	code := s.Currency
	if code == "" {
		code = "EUR"
	}
	ps := PricingStructure{
		Properties:          p,
		Prorated:            s.Prorated,
		ProductCatalog:      s.ProductCatalog,
		AcceptsTargetWallet: s.AcceptsTargetWallet,
		Currency:            money.MustCurrency(code),
	}
	if s.NoCurrency {
		ps.Currency = money.Currency{}
	}
	return ps
}

type applyCase struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	testStructure
	Aggregation testAggregation `json:"aggregation"`
	Project     bool            `json:"project"`
	PeriodRatio *money.Decimal  `json:"period_ratio"`
	// Want is matched against the [resultView] of the result.
	Want json.RawMessage `json:"want"`
	// WantError is the sentinel error expected: "invalid_properties" or
	// "unknown_model".
	WantError string `json:"want_error"`
}

// resultView is the JSON form of a [Result] the expectations match.
type resultView struct {
	Units                money.Decimal     `json:"units"`
	FullUnits            *money.Decimal    `json:"full_units"`
	CurrentUsageUnits    *money.Decimal    `json:"current_usage_units"`
	Count                int64             `json:"count"`
	Amount               money.Decimal     `json:"amount"`
	UnitAmount           money.Decimal     `json:"unit_amount"`
	Details              Details           `json:"details"`
	TotalAggregatedUnits *money.Decimal    `json:"total_aggregated_units"`
	GroupedBy            map[string]string `json:"grouped_by"`
	Groups               []resultView      `json:"groups"`
	ProjectedUnits       money.Decimal     `json:"projected_units"`
	ProjectedAmount      money.Decimal     `json:"projected_amount"`
}

func view(r Result) resultView {
	v := resultView{
		Units: r.Units, FullUnits: r.FullUnits, CurrentUsageUnits: r.CurrentUsageUnits, Count: r.Count,
		Amount: r.Amount, UnitAmount: r.UnitAmount, Details: r.Details,
		TotalAggregatedUnits: r.TotalAggregatedUnits, GroupedBy: r.GroupedBy,
		ProjectedUnits: r.ProjectedUnits, ProjectedAmount: r.ProjectedAmount,
	}
	for _, g := range r.Groups {
		v.Groups = append(v.Groups, view(g))
	}
	return v
}

func wantSentinel(t *testing.T, err error, want string) {
	t.Helper()
	var sentinel error
	switch want {
	case "":
		if err != nil {
			t.Fatalf("error: got = %v, want = nil", err)
		}
		return
	case "invalid_properties":
		sentinel = ErrInvalidProperties
	case "unknown_model":
		sentinel = ErrUnknownModel
	case "no_currency":
		sentinel = ErrNoCurrency
	default:
		t.Fatalf("unknown want_error %q", want)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error: got = %v, want = %v", err, sentinel)
	}
}

func TestApply(t *testing.T) {
	for file, cases := range loadCases[applyCase](t, "apply_*.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					opts := ApplyOptions{Project: c.Project, PeriodRatio: money.One}
					if c.PeriodRatio != nil {
						opts.PeriodRatio = *c.PeriodRatio
					}
					r, err := Apply(c.structure(t), c.Aggregation.result(), opts)
					wantSentinel(t, err, c.WantError)
					if err != nil {
						return
					}
					check(t, c.Want, view(r))
				})
			}
		})
	}
}
