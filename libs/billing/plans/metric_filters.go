package plans

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// handleDestroyMetricFilters runs a [kernel.JobDestroyMetricFilters]: the
// filters of the metric are discarded once the metric is. A metric that is
// missing or kept is left alone, so the job may run more than once. The
// charge filters selecting those filters were discarded with the metric's
// charges (charges store their filter values on their filters).
//
// source: app/jobs/billable_metric_filters/destroy_all_job.rb:7-10, app/services/billable_metric_filters/destroy_all_service.rb:12-28
func (s *Service) handleDestroyMetricFilters(ctx context.Context, j port.Job) error {
	var a kernel.MetricArgs
	if err := decodeArgs(j, &a); err != nil {
		return err
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		m, err := r.Metrics.GetWithDiscarded(ctx, j.OrganizationID, a.BillableMetricID)
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load billable metric %s: %w", a.BillableMetricID, err)
		}
		if m.DeletedAt == nil {
			return nil
		}
		if _, err := r.Metrics.DiscardFilters(ctx, j.OrganizationID, m.ID, s.d.Now()); err != nil {
			return fmt.Errorf("discard filters of billable metric %s: %w", m.ID, err)
		}
		return nil
	})
}
