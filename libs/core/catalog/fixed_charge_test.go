package catalog

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestFixedChargeValidate(t *testing.T) {
	type tc struct {
		Name         string   `yaml:"name"`
		Source       string   `yaml:"source"`
		Code         *string  `yaml:"code"`
		ChargeModel  string   `yaml:"charge_model"`
		Units        string   `yaml:"units"`
		PayInAdvance bool     `yaml:"pay_in_advance"`
		Prorated     bool     `yaml:"prorated"`
		Errors       []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "fixed_charge.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			f := &FixedCharge{
				ID:             core.MustParseID[core.FixedChargeID]("0190a0a0-0000-7000-8000-000000000030"),
				OrganizationID: catalogTestOrg,
				PlanID:         catalogTestPlan,
				Code:           "seats",
				AddOnID:        catalogTestAddOn,
				ChargeModel:    FixedChargeModel(c.ChargeModel),
				Properties:     MustProperties(`{"amount":"10"}`),
				Units:          money.MustParse(c.Units),
				PayInAdvance:   c.PayInAdvance,
				Prorated:       c.Prorated,
			}
			if c.Code != nil {
				f.Code = *c.Code
			}
			catalogCheckErrors(t, "Validate()", f.Validate(), c.Errors, c.Source)
		})
	}
}
