package catalog

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var catalogTestCharge = core.MustParseID[core.ChargeID]("0190a0a0-0000-7000-8000-000000000010")

func catalogTestChargeOf(model ChargeModel) *Charge {
	return &Charge{
		ID:               catalogTestCharge,
		OrganizationID:   catalogTestOrg,
		PlanID:           catalogTestPlan,
		Code:             "api_calls",
		BillableMetricID: catalogTestMetric,
		ChargeModel:      model,
		Properties:       MustProperties(`{"amount":"1"}`),
		Invoiceable:      true,
		CreatedAt:        catalogTestNow,
		UpdatedAt:        catalogTestNow,
	}
}

func TestValidateCharge(t *testing.T) {
	type tc struct {
		Name                string   `yaml:"name"`
		Source              string   `yaml:"source"`
		AggregationType     string   `yaml:"aggregation_type"`
		MetricRecurring     bool     `yaml:"metric_recurring"`
		Code                *string  `yaml:"code"`
		ChargeModel         string   `yaml:"charge_model"`
		PayInAdvance        bool     `yaml:"pay_in_advance"`
		Invoiceable         bool     `yaml:"invoiceable"`
		RegroupPaidFees     string   `yaml:"regroup_paid_fees"`
		Prorated            bool     `yaml:"prorated"`
		AcceptsTargetWallet bool     `yaml:"accepts_target_wallet"`
		MinAmount           int64    `yaml:"min_amount"`
		Properties          string   `yaml:"properties"`
		ConversionRate      string   `yaml:"conversion_rate"`
		Errors              []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "charge.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			m := catalogTestMetricOf(AggregationType(c.AggregationType), c.MetricRecurring)
			ch := catalogTestChargeOf(ChargeModel(c.ChargeModel))
			if c.Code != nil {
				ch.Code = *c.Code
			}
			ch.PayInAdvance = c.PayInAdvance
			ch.Invoiceable = c.Invoiceable
			ch.RegroupPaidFees = RegroupPaidFees(c.RegroupPaidFees)
			ch.Prorated = c.Prorated
			ch.AcceptsTargetWallet = c.AcceptsTargetWallet
			ch.MinAmount = c.MinAmount
			if c.Properties != "" {
				ch.Properties = Properties{raw: []byte(c.Properties)}
			}
			if c.ConversionRate != "" {
				ch.AppliedPricingUnit = &AppliedPricingUnit{PricingUnitID: catalogTestUnit, ConversionRate: money.MustParse(c.ConversionRate)}
			}
			catalogCheckErrors(t, "ValidateCharge()", ValidateCharge(ch, m), c.Errors, c.Source)
		})
	}
}

func TestEqualProperties(t *testing.T) {
	type tc struct {
		Name        string `yaml:"name"`
		Source      string `yaml:"source"`
		Kind        string `yaml:"kind"`
		ModelA      string `yaml:"model_a"`
		PropertiesA string `yaml:"properties_a"`
		UnitsA      string `yaml:"units_a"`
		ModelB      string `yaml:"model_b"`
		PropertiesB string `yaml:"properties_b"`
		UnitsB      string `yaml:"units_b"`
		Want        bool   `yaml:"want"`
	}
	for _, c := range spectest.Load[tc](t, "equal_properties.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var got bool
			switch c.Kind {
			case "charge":
				a := Charge{ChargeModel: ChargeModel(c.ModelA), Properties: MustProperties(c.PropertiesA)}
				b := Charge{ChargeModel: ChargeModel(c.ModelB), Properties: MustProperties(c.PropertiesB)}
				got = a.EqualProperties(&b)
			case "fixed_charge":
				a := FixedCharge{ChargeModel: FixedChargeModel(c.ModelA), Properties: MustProperties(c.PropertiesA), Units: money.MustParse(c.UnitsA)}
				b := FixedCharge{ChargeModel: FixedChargeModel(c.ModelB), Properties: MustProperties(c.PropertiesB), Units: money.MustParse(c.UnitsB)}
				got = a.EqualProperties(&b)
			default:
				t.Fatalf("kind = %q, want = charge or fixed_charge", c.Kind)
			}
			if got != c.Want {
				t.Errorf("EqualProperties() got = %v, want = %v (%s)", got, c.Want, c.Source)
			}
		})
	}
}

func TestDefaultChargeCode(t *testing.T) {
	tests := []struct {
		taken []string
		want  string
	}{
		{nil, "api_calls"},
		{[]string{"api_calls"}, "api_calls_2"},
		{[]string{"api_calls", "api_calls_2"}, "api_calls_3"},
		{[]string{"api_calls_2"}, "api_calls"},
	}
	for _, tt := range tests {
		if got := DefaultChargeCode("api_calls", tt.taken); got != tt.want {
			t.Errorf("DefaultChargeCode(%v) got = %q, want = %q", tt.taken, got, tt.want)
		}
	}
}

func TestChargeOverride(t *testing.T) {
	ch := catalogTestChargeOf(ChargeModelStandard)
	ch.Filters = []ChargeFilter{{ID: core.MustParseID[core.ChargeFilterID]("0190a0a0-0000-7000-8000-000000000020"), ChargeID: ch.ID, Code: "card_x", Values: map[string][]string{"card": {"card"}}}}
	childID := core.MustParseID[core.ChargeID]("0190a0a0-0000-7000-8000-000000000011")
	childPlan := core.MustParseID[core.PlanID]("0190a0a0-0000-7000-8000-000000000012")
	filterID := core.MustParseID[core.ChargeFilterID]("0190a0a0-0000-7000-8000-000000000021")
	o := ch.NewOverride(childID, childPlan, func() core.ChargeFilterID { return filterID }, catalogTestNow)
	if o.ParentID != ch.ID || o.PlanID != childPlan || o.Filters[0].ID != filterID || o.Filters[0].ChargeID != childID || o.Filters[0].Code != "card_x" {
		t.Errorf("NewOverride() got = %+v, want = a copy on the child plan keeping filter codes", o)
	}
	o.Filters[0].Values["card"][0] = "changed"
	if ch.Filters[0].Values["card"][0] != "card" {
		t.Error("NewOverride() shares filter values with the parent, want = a deep copy")
	}
}

func TestAppliedPricingUnitToFiat(t *testing.T) {
	a := AppliedPricingUnit{PricingUnitID: catalogTestUnit, ConversionRate: money.MustParse("0.333")}
	if got := a.ToFiat(money.MustParse("10"), money.MustCurrency("EUR")); !got.Equal(money.MustParse("3.33")) {
		t.Errorf("ToFiat() got = %s, want = 3.33", got)
	}
	if got := a.ToFiat(money.MustParse("15"), money.MustCurrency("EUR")); !got.Equal(money.MustParse("5.00")) {
		t.Errorf("ToFiat() got = %s, want = 5.00", got)
	}
}
