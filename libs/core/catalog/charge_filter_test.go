package catalog

import (
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

func catalogTestCardMetric() *BillableMetric {
	m := catalogTestMetricOf(AggregationTypeSum, false)
	m.Filters = []MetricFilter{
		{Key: "card", Values: []string{"card", "apple_pay"}},
		{Key: "scheme", Values: []string{"visa", "mastercard"}},
	}
	return m
}

func TestValidateChargeFilter(t *testing.T) {
	type tc struct {
		Name               string              `yaml:"name"`
		Source             string              `yaml:"source"`
		Values             map[string][]string `yaml:"values"`
		InvoiceDisplayName *string             `yaml:"invoice_display_name"`
		WantDisplayName    *string             `yaml:"want_display_name"`
		Errors             []string            `yaml:"errors"`
	}
	m := catalogTestCardMetric()
	for _, c := range spectest.Load[tc](t, "charge_filter.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			f := &ChargeFilter{ChargeID: catalogTestCharge, Values: c.Values, InvoiceDisplayName: c.InvoiceDisplayName}
			catalogCheckErrors(t, "ValidateChargeFilter()", ValidateChargeFilter(f, m), c.Errors, c.Source)
			if c.WantDisplayName != nil {
				if got := f.DisplayName(", "); got != *c.WantDisplayName {
					t.Errorf("DisplayName() got = %q, want = %q (%s)", got, *c.WantDisplayName, c.Source)
				}
			}
		})
	}
}

func TestChargeFilterCode(t *testing.T) {
	// spec/models/charge_filter_spec.rb:461
	a := ChargeFilterCode(map[string][]string{"type": {"pages"}, "model": {"b", "a"}})
	b := ChargeFilterCode(map[string][]string{"model": {"a", "b"}, "type": {"pages"}})
	if a != b {
		t.Errorf("ChargeFilterCode() order-dependent: got = %q and %q, want = equal", a, b)
	}
	// spec/models/charge_filter_spec.rb:468
	if ChargeFilterCode(map[string][]string{"model": {"ocr-2411"}}) == ChargeFilterCode(map[string][]string{"model": {"ocr-2503"}}) {
		t.Error("ChargeFilterCode() got = equal codes for different values, want = different")
	}
	// spec/models/charge_filter_spec.rb:475
	code := ChargeFilterCode(map[string][]string{"type": {"pages"}, "model": {"ocr-2411"}})
	if !strings.HasPrefix(code, "model_ocr-2411_type_pages_") || len(code) > ChargeFilterCodeSlugLimit+9 || len(code) != len("model_ocr-2411_type_pages_")+8 {
		t.Errorf("ChargeFilterCode() got = %q, want = model_ocr-2411_type_pages_<8 hex>", code)
	}
	// spec/models/charge_filter_spec.rb:482
	long := strings.Repeat("m", 200)
	la := ChargeFilterCode(map[string][]string{"model": {long + "a"}})
	lb := ChargeFilterCode(map[string][]string{"model": {long + "b"}})
	if la == lb || len(la) != ChargeFilterCodeSlugLimit+9 {
		t.Errorf("ChargeFilterCode() long values got = %q / %q, want = distinct codes of %d characters", la, lb, ChargeFilterCodeSlugLimit+9)
	}
	// spec/models/charge_filter_spec.rb:506
	taken := map[string]bool{code: true}
	if got := NextFreeCode(code, func(s string) bool { return taken[s] }); got != code+"_2" {
		t.Errorf("NextFreeCode() got = %q, want = %q", got, code+"_2")
	}
	// tanvo: separators collapse and trim like ActiveSupport's parameterize.
	if got := chargeFilterParameterize("  Model:A B+c|__x "); got != "model_a_b_c_x" {
		t.Errorf("chargeFilterParameterize() got = %q, want = %q", got, "model_a_b_c_x")
	}
}

func TestMatchChargeFilter(t *testing.T) {
	m := catalogTestCardMetric()
	id := func(n string) core.ChargeFilterID {
		return core.MustParseID[core.ChargeFilterID]("0190a0a0-0000-7000-8000-0000000000" + n)
	}
	t0 := catalogTestNow
	filters := []ChargeFilter{
		{ID: id("01"), Code: "all_cards", Values: map[string][]string{"card": {AllFilterValues}}, CreatedAt: t0},
		{ID: id("02"), Code: "card", Values: map[string][]string{"card": {"card"}}, CreatedAt: t0.Add(time.Hour)},
		{ID: id("03"), Code: "card_visa", Values: map[string][]string{"card": {"card"}, "scheme": {"visa"}}, CreatedAt: t0.Add(2 * time.Hour)},
		{ID: id("04"), Code: "card_any_scheme", Values: map[string][]string{"card": {"card"}, "scheme": {"visa", "mastercard"}}, CreatedAt: t0},
		{ID: id("05"), Code: "card_visa_late", Values: map[string][]string{"card": {"card"}, "scheme": {"visa"}}, CreatedAt: t0.Add(3 * time.Hour)},
	}
	tests := []struct {
		name  string
		props map[string]string
		want  string
	}{
		{"more keys and fewer values win, then the older", map[string]string{"card": "card", "scheme": "visa"}, "card_visa"},
		{"two keys beat one", map[string]string{"card": "card", "scheme": "mastercard"}, "card_any_scheme"},
		{"fewer values beat the sentinel", map[string]string{"card": "card"}, "card"},
		{"sentinel matches any metric value", map[string]string{"card": "apple_pay"}, "all_cards"},
		{"value outside the metric filter falls to the default bucket", map[string]string{"card": "cash"}, ""},
		{"missing property falls to the default bucket", map[string]string{"scheme": "visa"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ""
			if f := MatchChargeFilter(filters, tt.props, m); f != nil {
				got = f.Code
			}
			if got != tt.want {
				t.Errorf("MatchChargeFilter() got = %q, want = %q", got, tt.want)
			}
		})
	}
	sorted := append([]ChargeFilter(nil), filters...)
	SortChargeFilters(sorted, m)
	var codes []string
	for _, f := range sorted {
		codes = append(codes, f.Code)
	}
	want := "card_visa card_visa_late card_any_scheme card all_cards"
	if got := strings.Join(codes, " "); got != want {
		t.Errorf("SortChargeFilters() got = %s, want = %s", got, want)
	}
}
