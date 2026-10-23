package catalog

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

func TestPricingUnitValidate(t *testing.T) {
	type tc struct {
		Name              string   `yaml:"name"`
		Source            string   `yaml:"source"`
		Code              *string  `yaml:"code"`
		UnitName          *string  `yaml:"unit_name"`
		ShortName         *string  `yaml:"short_name"`
		DescriptionLength int      `yaml:"description_length"`
		NewCode           string   `yaml:"new_code"`
		Errors            []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "pricing_unit.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			old := &PricingUnit{ID: catalogTestUnit, OrganizationID: catalogTestOrg, Code: "credits", Name: "Credits", ShortName: "CR"}
			p := *old
			if c.Code != nil {
				p.Code = *c.Code
				old.Code = *c.Code
			}
			if c.UnitName != nil {
				p.Name = *c.UnitName
			}
			if c.ShortName != nil {
				p.ShortName = *c.ShortName
			}
			if c.DescriptionLength > 0 {
				p.Description = catalogPtr(strings.Repeat("a", c.DescriptionLength))
			}
			if c.NewCode != "" {
				p.Code = c.NewCode
			}
			catalogCheckErrors(t, "ValidatePricingUnitUpdate()", ValidatePricingUnitUpdate(old, &p), c.Errors, c.Source)
		})
	}
}

func TestPricingUnitExponent(t *testing.T) {
	p := &PricingUnit{}
	// spec/models/pricing_unit_spec.rb:17
	if got := p.Exponent(); got != 2 {
		t.Errorf("Exponent() got = %d, want = 2", got)
	}
	// spec/models/pricing_unit_spec.rb:27
	if got := p.SubunitToUnit(); got != 100 {
		t.Errorf("SubunitToUnit() got = %d, want = 100", got)
	}
}
