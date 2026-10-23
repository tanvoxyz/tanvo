package auth

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// JobKindApplyAPIKeyUsage is the kind of the per-organization jobs of the
// API key usage clock job: [ApplyUsage] runs them. They have no
// arguments.
const JobKindApplyAPIKeyUsage = "api_key.apply_usage"

// DispatchUsage runs the API key usage clock job (lago-api's
// Clock::ApiKeys::TrackUsageJob): it enqueues a [JobKindApplyAPIKeyUsage]
// job for every organization with recorded uses and returns how many.
func DispatchUsage(ctx context.Context, tx port.Transactor) (int, error) {
	var n int
	err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		orgs, err := r.APIKeys.UseOrganizations(ctx)
		if err != nil {
			return err
		}
		jobs := make([]port.Job, len(orgs))
		for i, org := range orgs {
			jobs[i] = port.Job{
				Kind: JobKindApplyAPIKeyUsage, Queue: port.QueueDefault, OrganizationID: org,
				Args: []byte("{}"), UniqueKey: JobKindApplyAPIKeyUsage,
			}
		}
		n = len(jobs)
		return r.Jobs.Enqueue(ctx, jobs...)
	})
	if err != nil {
		return 0, fmt.Errorf("dispatch api key usage: %w", err)
	}
	return n, nil
}

// ApplyUsage runs a [JobKindApplyAPIKeyUsage] job: it moves org's recorded
// API key uses to the keys' last_used_at and returns how many keys were
// updated.
func ApplyUsage(ctx context.Context, tx port.Transactor, org core.OrganizationID) (int, error) {
	var n int
	err := tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		n, err = r.APIKeys.ApplyUses(ctx, org)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("apply api key usage: %w", err)
	}
	return n, nil
}
