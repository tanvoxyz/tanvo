package kernel

import "github.com/tanvoxyz/tanvo/libs/core"

// JobUpdatePlanAmount cascades a parent plan's new subscription fee to one
// of its override plans that still has the parent's previous fee (lago
// Plans::UpdateAmountJob); its args are [PlanAmountArgs].
const JobUpdatePlanAmount = "billing.update_plan_amount"

// PlanAmountArgs are the args of a [JobUpdatePlanAmount].
type PlanAmountArgs struct {
	// PlanID is the override plan.
	PlanID core.PlanID `json:"plan_id"`
	// Amount is the new subscription fee, in minor units.
	Amount int64 `json:"amount"`
	// ExpectedAmount is the fee the override plan must still have.
	ExpectedAmount int64 `json:"expected_amount"`
}

// planJobKinds returns the job kinds of the plan updates and overrides.
func planJobKinds() []string { return []string{JobUpdatePlanAmount} }
