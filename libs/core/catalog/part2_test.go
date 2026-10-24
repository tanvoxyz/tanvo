package catalog

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

func part2FieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func part2CheckErrors(t *testing.T, err error, want []string) {
	t.Helper()
	if got := part2FieldErrors(err); !slices.Equal(got, want) {
		t.Errorf("errors: got = %v, want = %v", got, want)
	}
}

func part2Ptr(s string) *string { return &s }

var (
	part2Plan         = core.MustParseID[core.PlanID]("0190a0a0-0000-7000-8000-000000000001")
	part2Subscription = core.MustParseID[core.SubscriptionID]("0190a0a0-0000-7000-8000-000000000002")
	part2Feature      = core.MustParseID[core.FeatureID]("0190a0a0-0000-7000-8000-000000000003")
	part2TaxA         = core.MustParseID[core.TaxID]("0190a0a0-0000-7000-8000-00000000000a")
	part2TaxB         = core.MustParseID[core.TaxID]("0190a0a0-0000-7000-8000-00000000000b")
	part2TaxC         = core.MustParseID[core.TaxID]("0190a0a0-0000-7000-8000-00000000000c")
)

func TestTaxValidate(t *testing.T) {
	type tc struct {
		Name    string           `yaml:"name"`
		Source  string           `yaml:"source"`
		Code    string           `yaml:"code"`
		TaxName string           `yaml:"tax_name"`
		Rate    spectest.Decimal `yaml:"rate"`
		Errors  []string         `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "tax.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			tax := Tax{Code: c.Code, Name: c.TaxName, Rate: c.Rate.Decimal}
			part2CheckErrors(t, tax.Validate(), c.Errors)
		})
	}
}

func TestEUTaxCodes(t *testing.T) {
	tests := []struct {
		code string
		want EUTaxCode
		ok   bool
	}{
		{"eu_fr_standard", EUTaxCode{Kind: EUTaxStandard, Country: "FR"}, true},
		{"eu_de_exception_heligoland", EUTaxCode{Kind: EUTaxException, Country: "DE", Territory: "heligoland"}, true},
		{EUReverseChargeTaxCode, EUTaxCode{Kind: EUTaxReverseCharge}, true},
		{EUTaxExemptCode, EUTaxCode{Kind: EUTaxExempt}, true},
		{"eu_us_standard", EUTaxCode{}, false},
		{"eu_fr_exception_", EUTaxCode{}, false},
		{"eu_fr_reduced", EUTaxCode{}, false},
		{"lago_eu_fr_standard", EUTaxCode{}, false},
		{"vat", EUTaxCode{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseEUTaxCode(tt.code)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseEUTaxCode(%q): got = %+v, %v, want = %+v, %v", tt.code, got, ok, tt.want, tt.ok)
		}
		if ok && got.String() != tt.code {
			t.Errorf("String(): got = %q, want = %q", got.String(), tt.code)
		}
	}
	if got := EUStandardTaxCode("FR"); got != "eu_fr_standard" {
		t.Errorf("EUStandardTaxCode: got = %q, want = eu_fr_standard", got)
	}
	if got := EUExceptionTaxCode("FI", "Åland Islands"); got != "eu_fi_exception_aland_islands" {
		t.Errorf("EUExceptionTaxCode: got = %q, want = eu_fi_exception_aland_islands", got)
	}
	if got := EUStandardTaxName("fr"); got != "FR Standard" {
		t.Errorf("EUStandardTaxName: got = %q, want = FR Standard", got)
	}
	if got := EUExceptionTaxName("de", "Heligoland"); got != "DE Heligoland Standard" {
		t.Errorf("EUExceptionTaxName: got = %q, want = DE Heligoland Standard", got)
	}
	for in, want := range map[string]string{
		"Åland Islands":         "aland_islands",
		"Büsingen am Hochrhein": "busingen_am_hochrhein",
		"Mount Athos":           "mount_athos",
		"Campione d'Italia":     "campione_d_italia",
		"  Açores ":             "acores",
	} {
		if got := EUTerritorySlug(in); got != want {
			t.Errorf("EUTerritorySlug(%q): got = %q, want = %q", in, got, want)
		}
	}
}

func TestResolveTaxes(t *testing.T) {
	a, b, c := []core.TaxID{part2TaxA}, []core.TaxID{part2TaxB}, []core.TaxID{part2TaxC}
	all := TaxCandidates{Explicit: nil, AddOn: a, Charge: a, FixedCharge: b, Commitment: a, Plan: b, Customer: c, BillingEntity: c}
	tests := []struct {
		name   string
		fee    core.FeeType
		c      TaxCandidates
		want   []core.TaxID
		source TaxSource
	}{
		{"explicit wins", core.FeeTypeAddOn, TaxCandidates{Explicit: b, AddOn: a}, b, TaxSourceExplicit},
		{"add-on fee", core.FeeTypeAddOn, all, a, TaxSourceAddOn},
		{"add-on fee ignores plan", core.FeeTypeAddOn, TaxCandidates{Plan: a, Customer: c}, c, TaxSourceCustomer},
		{"fixed charge prefers add-on", core.FeeTypeFixedCharge, all, a, TaxSourceAddOn},
		{"fixed charge", core.FeeTypeFixedCharge, TaxCandidates{FixedCharge: b, Plan: a}, b, TaxSourceFixedCharge},
		{"charge fee", core.FeeTypeCharge, all, a, TaxSourceCharge},
		{"charge fee ignores add-on", core.FeeTypeCharge, TaxCandidates{AddOn: a, Plan: b}, b, TaxSourcePlan},
		// source: app/services/fees/apply_taxes_service.rb:70-81
		{"product fee ignores charge and plan", core.FeeTypeProduct, TaxCandidates{Charge: a, Plan: b, Customer: c}, c, TaxSourceCustomer},
		{"commitment fee", core.FeeTypeCommitment, all, a, TaxSourceCommitment},
		{"subscription fee", core.FeeTypeSubscription, all, b, TaxSourcePlan},
		{"customer", core.FeeTypeSubscription, TaxCandidates{Customer: c, BillingEntity: a}, c, TaxSourceCustomer},
		{"billing entity", core.FeeTypeCharge, TaxCandidates{BillingEntity: a}, a, TaxSourceBillingEntity},
		{"empty lists fall through", core.FeeTypeCharge, TaxCandidates{Charge: []core.TaxID{}, BillingEntity: a}, a, TaxSourceBillingEntity},
		{"none", core.FeeTypeCharge, TaxCandidates{}, nil, ""},
		{"credit never taxed", core.FeeTypeCredit, all, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source := ResolveTaxes(tt.fee, tt.c)
			if !slices.Equal(got, tt.want) || source != tt.source {
				t.Errorf("got = %v, %q, want = %v, %q", got, source, tt.want, tt.source)
			}
		})
	}
}

func TestCommitmentValidate(t *testing.T) {
	type tc struct {
		Name           string   `yaml:"name"`
		Source         string   `yaml:"source"`
		Plan           bool     `yaml:"plan"`
		CommitmentType string   `yaml:"commitment_type"`
		Amount         int64    `yaml:"amount"`
		DisplayName    *string  `yaml:"display_name"`
		DuplicateTax   bool     `yaml:"duplicate_tax"`
		InvoiceName    string   `yaml:"invoice_name"`
		Errors         []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "commitment.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			m := Commitment{
				CommitmentType:     CommitmentType(c.CommitmentType),
				Amount:             c.Amount,
				InvoiceDisplayName: c.DisplayName,
				TaxIDs:             []core.TaxID{part2TaxA},
			}
			if c.Plan {
				m.PlanID = part2Plan
			}
			if c.DuplicateTax {
				m.TaxIDs = append(m.TaxIDs, part2TaxA)
			}
			part2CheckErrors(t, m.Validate(), c.Errors)
			if c.InvoiceName != "" && m.InvoiceName() != c.InvoiceName {
				t.Errorf("InvoiceName(): got = %q, want = %q", m.InvoiceName(), c.InvoiceName)
			}
		})
	}
}

func TestUsageThresholdValidate(t *testing.T) {
	type tc struct {
		Name              string   `yaml:"name"`
		Source            string   `yaml:"source"`
		Plan              bool     `yaml:"plan"`
		Subscription      bool     `yaml:"subscription"`
		Amount            int64    `yaml:"amount"`
		DisplayNameLength int      `yaml:"display_name_length"`
		Errors            []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "usage_threshold.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			u := UsageThreshold{Amount: c.Amount}
			if c.Plan {
				u.PlanID = part2Plan
			}
			if c.Subscription {
				u.SubscriptionID = part2Subscription
			}
			if c.DisplayNameLength > 0 {
				u.ThresholdDisplayName = part2Ptr(strings.Repeat("é", c.DisplayNameLength))
			}
			part2CheckErrors(t, u.Validate(), c.Errors)
		})
	}
}

func TestValidateUsageThresholds(t *testing.T) {
	th := func(amount int64, recurring bool) UsageThreshold {
		return UsageThreshold{PlanID: part2Plan, Amount: amount, Recurring: recurring}
	}
	tests := []struct {
		name   string
		in     []UsageThreshold
		errors []string
	}{
		{"valid", []UsageThreshold{th(100, false), th(200, false), th(50, true)}, []string{}},
		{"duplicate amount", []UsageThreshold{th(100, false), th(100, false)}, []string{"usage_thresholds[1].amount: duplicated_value"}},
		{"recurring may share an amount", []UsageThreshold{th(100, false), th(100, true)}, []string{}},
		{"two recurring", []UsageThreshold{th(100, true), th(200, true)}, []string{"usage_thresholds: multiple_recurring_thresholds"}},
		{"nested error", []UsageThreshold{th(0, false)}, []string{"usage_thresholds[0].amount: value_must_be_positive"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			part2CheckErrors(t, ValidateUsageThresholds(tt.in), tt.errors)
		})
	}
}

func TestEffectiveUsageThresholds(t *testing.T) {
	th := func(amount int64, recurring bool) UsageThreshold {
		return UsageThreshold{Amount: amount, Recurring: recurring}
	}
	amounts := func(us []UsageThreshold) []string {
		out := []string{}
		for _, u := range us {
			s := fmt.Sprint(u.Amount)
			if u.Recurring {
				s += "r"
			}
			out = append(out, s)
		}
		return out
	}
	plan := []UsageThreshold{th(50, true), th(300, false), th(100, false)}
	sub := []UsageThreshold{th(1000, false)}
	tests := []struct {
		name     string
		plan     []UsageThreshold
		sub      []UsageThreshold
		disabled bool
		want     []string
	}{
		{"plan sorted, recurring last", plan, nil, false, []string{"100", "300", "50r"}},
		{"subscription overrides plan", plan, sub, false, []string{"1000"}},
		{"progressive billing disabled", plan, sub, true, []string{}},
		{"none", nil, nil, false, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := amounts(EffectiveUsageThresholds(tt.plan, tt.sub, tt.disabled))
			if !slices.Equal(got, tt.want) {
				t.Errorf("got = %v, want = %v", got, tt.want)
			}
		})
	}
	if plan[0].Amount != 50 {
		t.Errorf("input reordered: got = %d, want = 50", plan[0].Amount)
	}
}

func TestPrivilegeValidate(t *testing.T) {
	type tc struct {
		Name          string   `yaml:"name"`
		Source        string   `yaml:"source"`
		Code          string   `yaml:"code"`
		CodeLength    int      `yaml:"code_length"`
		NameLength    int      `yaml:"name_length"`
		ValueType     string   `yaml:"value_type"`
		SelectOptions []string `yaml:"select_options"`
		Errors        []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "entitlement_privilege.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			p := Privilege{Code: c.Code, ValueType: PrivilegeValueType(c.ValueType), SelectOptions: c.SelectOptions}
			if c.CodeLength > 0 {
				p.Code = strings.Repeat("a", c.CodeLength)
			}
			if c.NameLength > 0 {
				p.Name = part2Ptr(strings.Repeat("a", c.NameLength))
			}
			part2CheckErrors(t, p.Validate(), c.Errors)
		})
	}
}

func TestFeatureValidate(t *testing.T) {
	type tc struct {
		Name              string   `yaml:"name"`
		Source            string   `yaml:"source"`
		Code              string   `yaml:"code"`
		CodeLength        int      `yaml:"code_length"`
		NameLength        int      `yaml:"name_length"`
		DescriptionLength int      `yaml:"description_length"`
		RepeatPrivilege   bool     `yaml:"repeat_privilege"`
		BadPrivilege      bool     `yaml:"bad_privilege"`
		Errors            []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "entitlement_feature.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			f := Feature{Code: c.Code, Privileges: []Privilege{{Code: "max", ValueType: PrivilegeValueTypeInteger}}}
			if c.CodeLength > 0 {
				f.Code = strings.Repeat("a", c.CodeLength)
			}
			if c.NameLength > 0 {
				f.Name = part2Ptr(strings.Repeat("a", c.NameLength))
			}
			if c.DescriptionLength > 0 {
				f.Description = part2Ptr(strings.Repeat("a", c.DescriptionLength))
			}
			if c.RepeatPrivilege {
				f.Privileges = append(f.Privileges, Privilege{Code: "max", ValueType: PrivilegeValueTypeString})
			}
			if c.BadPrivilege {
				f.Privileges = append(f.Privileges, Privilege{Code: "min"})
			}
			part2CheckErrors(t, f.Validate(), c.Errors)
		})
	}
}

func TestEntitlementValidate(t *testing.T) {
	type tc struct {
		Name         string   `yaml:"name"`
		Source       string   `yaml:"source"`
		Plan         bool     `yaml:"plan"`
		Subscription bool     `yaml:"subscription"`
		Feature      bool     `yaml:"feature"`
		Errors       []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "entitlement.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var e Entitlement
			if c.Plan {
				e.PlanID = part2Plan
			}
			if c.Subscription {
				e.SubscriptionID = part2Subscription
			}
			if c.Feature {
				e.FeatureID = part2Feature
			}
			part2CheckErrors(t, e.Validate(), c.Errors)
		})
	}
}

func TestPrivilegeNormalizeValue(t *testing.T) {
	type tc struct {
		Name          string   `yaml:"name"`
		Source        string   `yaml:"source"`
		ValueType     string   `yaml:"value_type"`
		SelectOptions []string `yaml:"select_options"`
		Value         string   `yaml:"value"`
		Want          string   `yaml:"want"`
		Errors        []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "entitlement_value.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			p := Privilege{Code: "p", ValueType: PrivilegeValueType(c.ValueType), SelectOptions: c.SelectOptions}
			got, err := p.NormalizeValue(c.Value)
			part2CheckErrors(t, err, c.Errors)
			if got != c.Want {
				t.Errorf("value: got = %q, want = %q", got, c.Want)
			}
		})
	}
}

func TestEntitlementNormalizeValues(t *testing.T) {
	f := &Feature{ID: part2Feature, Code: "seats", Privileges: []Privilege{
		{Code: "max", ValueType: PrivilegeValueTypeInteger},
		{Code: "sso", ValueType: PrivilegeValueTypeBoolean},
	}}
	e := Entitlement{FeatureID: part2Feature, PlanID: part2Plan, Values: map[string]string{"max": "010", "sso": "t"}}
	if err := e.NormalizeValues(f); err != nil {
		t.Fatalf("NormalizeValues: got = %v, want = nil", err)
	}
	if e.Values["max"] != "10" || e.Values["sso"] != "true" {
		t.Errorf("values: got = %v, want = map[max:10 sso:true]", e.Values)
	}
	bad := Entitlement{FeatureID: part2Feature, PlanID: part2Plan, Values: map[string]string{"max": "x", "nope": "1", "sso": ""}}
	part2CheckErrors(t, bad.NormalizeValues(f), []string{
		"values.max: value_is_invalid", "values.nope: privilege_not_found", "values.sso: value_is_mandatory",
	})
	if bad.Values["max"] != "x" {
		t.Errorf("values changed on error: got = %v", bad.Values)
	}
	other := Entitlement{FeatureID: core.MustParseID[core.FeatureID]("0190a0a0-0000-7000-8000-0000000000ff")}
	part2CheckErrors(t, other.NormalizeValues(f), []string{"feature_id: invalid_value"})
}

func TestEffectiveEntitlements(t *testing.T) {
	featureA := part2Feature
	featureB := core.MustParseID[core.FeatureID]("0190a0a0-0000-7000-8000-000000000004")
	featureC := core.MustParseID[core.FeatureID]("0190a0a0-0000-7000-8000-000000000005")
	planA := core.MustParseID[core.EntitlementID]("0190a0a0-0000-7000-8000-000000000101")
	planB := core.MustParseID[core.EntitlementID]("0190a0a0-0000-7000-8000-000000000102")
	subA := core.MustParseID[core.EntitlementID]("0190a0a0-0000-7000-8000-000000000201")
	subC := core.MustParseID[core.EntitlementID]("0190a0a0-0000-7000-8000-000000000203")
	plan := []Entitlement{
		{ID: planA, FeatureID: featureA, Values: map[string]string{"max": "10", "sso": "false", "region": "eu"}},
		{ID: planB, FeatureID: featureB, Values: map[string]string{"enabled": "true"}},
	}
	sub := []Entitlement{
		{ID: subA, FeatureID: featureA, Values: map[string]string{"max": "20"}},
		{ID: subC, FeatureID: featureC, Values: map[string]string{"tier": "gold"}},
	}
	removals := []EntitlementRemoval{
		{FeatureID: featureA, PrivilegeCode: "region"},
		{FeatureID: featureB},
	}
	got := EffectiveEntitlements(plan, sub, removals)
	want := []EffectiveEntitlement{
		{FeatureID: featureA, Values: map[string]string{"max": "20", "sso": "false"}, PlanEntitlementID: planA, SubscriptionEntitlementID: subA},
		{FeatureID: featureC, Values: map[string]string{"tier": "gold"}, SubscriptionEntitlementID: subC},
	}
	if len(got) != len(want) {
		t.Fatalf("len: got = %d, want = %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.FeatureID != w.FeatureID || g.PlanEntitlementID != w.PlanEntitlementID ||
			g.SubscriptionEntitlementID != w.SubscriptionEntitlementID || fmt.Sprint(g.Values) != fmt.Sprint(w.Values) {
			t.Errorf("[%d]: got = %+v, want = %+v", i, g, w)
		}
	}
	if plan[0].Values["max"] != "10" || plan[0].Values["region"] != "eu" {
		t.Errorf("plan values changed: got = %v", plan[0].Values)
	}
	if got := EffectiveEntitlements(plan, nil, nil); len(got) != 2 {
		t.Errorf("plan only: got = %d entitlements, want = 2", len(got))
	}
}

func TestEntitlementRemovalValidate(t *testing.T) {
	part2CheckErrors(t, (&EntitlementRemoval{}).Validate(), []string{
		"subscription_id: value_is_mandatory", "feature_id: value_is_mandatory",
	})
	part2CheckErrors(t, (&EntitlementRemoval{SubscriptionID: part2Subscription, FeatureID: part2Feature}).Validate(), []string{})
}
