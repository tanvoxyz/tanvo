package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/event"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// ValidateEvents post-validates the events org received in the hour
// before the hour of the clock instant at ([event.ValidationWindow]) and,
// when any fails, emits one events.errors webhook listing them, and
// returns the errors (lago Clock::EventsValidationJob,
// Events::PostValidationService and Webhooks::Events::ValidationErrorsService,
// app/jobs/clock/events_validation_job.rb:7-23,
// app/services/events/post_validation_service.rb:9-23,
// app/services/webhooks/events/validation_errors_service.rb:12-24).
func (e *Engine) ValidateEvents(ctx context.Context, org core.OrganizationID, at time.Time) (event.ValidationErrors, error) {
	from, to := event.ValidationWindow(at)
	var errs event.ValidationErrors
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		errs, err = r.Events.ValidationErrors(ctx, org, from, to)
		if err != nil {
			return fmt.Errorf("validate events: %w", err)
		}
		if errs.Empty() {
			return nil
		}
		return kernel.Emit(ctx, e.d, org, webhook.EventTypeEventsErrors, "events_errors", org.UUID(), errs)
	})
	return errs, err
}

// CleanRecordDeletions removes the record deletions of org older than one
// month before the clock instant at ([export.RecordDeletionCutoff]), in
// batches of [export.RecordDeletionBatch] rows each committed on its own,
// and returns their number (lago Clock::RecordDeletionsCleanupJob,
// app/jobs/clock/record_deletions_cleanup_job.rb:5-21).
func (e *Engine) CleanRecordDeletions(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	cutoff := export.RecordDeletionCutoff(at)
	total := 0
	for {
		var n int
		err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
			var err error
			n, err = r.RecordDeletions.DeleteExpired(ctx, org, cutoff, export.RecordDeletionBatch)
			if err != nil {
				return fmt.Errorf("delete expired record deletions: %w", err)
			}
			return nil
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < export.RecordDeletionBatch {
			return total, nil
		}
	}
}
