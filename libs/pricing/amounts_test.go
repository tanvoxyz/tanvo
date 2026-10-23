package pricing

import (
	"encoding/json"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

type amountsCase struct {
	Source   string `json:"source"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
	Result   *struct {
		Amount     money.Decimal `json:"amount"`
		UnitAmount money.Decimal `json:"unit_amount"`
		Units      money.Decimal `json:"units"`
	} `json:"result"`
	InAdvance *struct {
		AmountCents        money.Decimal `json:"amount_cents"`
		PreciseAmountCents money.Decimal `json:"precise_amount_cents"`
		UnitAmount         money.Decimal `json:"unit_amount"`
		Units              money.Decimal `json:"units"`
	} `json:"in_advance"`
	// PricingUnitRate is the conversion rate of a pricing unit with 2
	// decimals.
	PricingUnitRate *money.Decimal `json:"pricing_unit_rate"`
	Deduction       *struct {
		AmountCents int64 `json:"amount_cents"`
		BilledDays  int64 `json:"billed_days"`
		PeriodDays  int64 `json:"period_days"`
	} `json:"deduction"`
	TrueUp *struct {
		MinimumAmountCents     int64          `json:"minimum_amount_cents"`
		BilledDays             int64          `json:"billed_days"`
		PeriodDays             int64          `json:"period_days"`
		UsedAmountCents        *money.Decimal `json:"used_amount_cents"`
		UsedPreciseAmountCents *money.Decimal `json:"used_precise_amount_cents"`
	} `json:"true_up"`
	Want json.RawMessage `json:"want"`
}

type pricingUnitUsageView struct {
	AmountCents        int64         `json:"amount_cents"`
	PreciseAmountCents money.Decimal `json:"precise_amount_cents"`
	UnitAmountCents    int64         `json:"unit_amount_cents"`
	PreciseUnitAmount  money.Decimal `json:"precise_unit_amount"`
}

type feeAmountView struct {
	AmountCents        int64                 `json:"amount_cents"`
	PreciseAmountCents money.Decimal         `json:"precise_amount_cents"`
	UnitAmountCents    int64                 `json:"unit_amount_cents"`
	PreciseUnitAmount  money.Decimal         `json:"precise_unit_amount"`
	PricingUnitUsage   *pricingUnitUsageView `json:"pricing_unit_usage"`
}

func feeView(a *FeeAmount) *feeAmountView {
	if a == nil {
		return nil
	}
	v := &feeAmountView{
		AmountCents: a.AmountCents, PreciseAmountCents: a.PreciseAmountCents,
		UnitAmountCents: a.UnitAmountCents, PreciseUnitAmount: a.PreciseUnitAmount,
	}
	if u := a.PricingUnitUsage; u != nil {
		v.PricingUnitUsage = &pricingUnitUsageView{
			AmountCents: u.AmountCents, PreciseAmountCents: u.PreciseAmountCents,
			UnitAmountCents: u.UnitAmountCents, PreciseUnitAmount: u.PreciseUnitAmount,
		}
	}
	return v
}

func TestComputeAmounts(t *testing.T) {
	credits, err := money.NewCustomCurrency("CREDITS", "Credits", 2)
	if err != nil {
		t.Fatal(err)
	}
	for file, cases := range loadCases[amountsCase](t, "amounts.json") {
		t.Run(file, func(t *testing.T) {
			for _, c := range cases {
				t.Run(caseName(c.Source, c.Name), func(t *testing.T) {
					in := AmountsInput{Currency: money.MustCurrency(c.Currency)}
					if r := c.Result; r != nil {
						in.Result = &Result{Amount: r.Amount, UnitAmount: r.UnitAmount, Units: r.Units}
					}
					if r := c.InAdvance; r != nil {
						in.InAdvance = &InAdvanceResult{
							AmountCents: r.AmountCents, PreciseAmountCents: r.PreciseAmountCents,
							UnitAmount: r.UnitAmount, Units: r.Units, Count: 1,
						}
					}
					if c.PricingUnitRate != nil {
						in.PricingUnit = &AppliedPricingUnit{Unit: credits, ConversionRate: *c.PricingUnitRate}
					}
					if d := c.Deduction; d != nil {
						in.Deduction = &Deduction{AmountCents: d.AmountCents, BilledDays: d.BilledDays, PeriodDays: d.PeriodDays}
					}
					if tu := c.TrueUp; tu != nil {
						in.TrueUp = &TrueUp{
							MinimumAmountCents: tu.MinimumAmountCents, BilledDays: tu.BilledDays, PeriodDays: tu.PeriodDays,
							UsedAmountCents: tu.UsedAmountCents, UsedPreciseAmountCents: tu.UsedPreciseAmountCents,
						}
					}
					got, err := ComputeAmounts(in)
					if err != nil {
						t.Fatal(err)
					}
					check(t, c.Want, struct {
						Amount *feeAmountView `json:"amount"`
						TrueUp *feeAmountView `json:"true_up"`
					}{feeView(got.Amount), feeView(got.TrueUp)})
				})
			}
		})
	}
}
