package plans

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// planJobs returns the handlers of the plan update jobs.
func (s *Service) planJobs() map[string]func(context.Context, port.Job) error {
	return map[string]func(context.Context, port.Job) error{
		kernel.JobUpdatePlanAmount: func(ctx context.Context, j port.Job) error {
			var a kernel.PlanAmountArgs
			if err := json.Unmarshal(j.Args, &a); err != nil {
				return fmt.Errorf("decode %s args: %w", j.Kind, err)
			}
			_, err := s.UpdateAmount(ctx, j.OrganizationID, a.PlanID, a.Amount, a.ExpectedAmount)
			return err
		},
	}
}

// UpdateAmount runs a [kernel.JobUpdatePlanAmount]: it sets the
// subscription fee of the plan id to amount, then upgrades the pending
// subscriptions the new fee turns into upgrades, unless the plan's fee is
// no longer expected (the override changed it since).
//
// source: app/services/plans/update_amount_service.rb:15-33
func (s *Service) UpdateAmount(ctx context.Context, org core.OrganizationID, id core.PlanID, amount, expected int64) (*catalog.Plan, error) {
	var out *catalog.Plan
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := kept(ctx, r, org, id)
		if err != nil {
			return err
		}
		out = p
		if p.Amount != expected {
			return nil
		}
		p.Amount, p.UpdatedAt = amount, s.d.Now()
		if err := p.Validate(); err != nil {
			return err
		}
		if err := r.Plans.Update(ctx, p); err != nil {
			return fmt.Errorf("update plan: %w", err)
		}
		return s.processPending(ctx, r, p)
	})
	if err != nil {
		return nil, fmt.Errorf("update amount of plan %s: %w", id, err)
	}
	return out, nil
}
