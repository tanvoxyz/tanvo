package kernel

import "github.com/tanvoxyz/tanvo/libs/core"

// JobDestroyPlan ends the deletion of a plan prepared for deletion: its
// override plans, then the plan itself (lago Plans::DestroyJob); its args
// are [PlanArgs].
const JobDestroyPlan = "billing.destroy_plan"

// PlanArgs are the args of jobs about one plan.
type PlanArgs struct {
	// PlanID is the plan.
	PlanID core.PlanID `json:"plan_id"`
}
