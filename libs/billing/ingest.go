package billing

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// EnqueueInAdvanceCharges enqueues the in-advance billing of the stored
// events events of organization org (lago Events::PayInAdvanceJob, enqueued
// by Events::PostProcessService on the PostgreSQL path and by the
// EventsChargedInAdvanceConsumer on the Kafka path): one
// [kernel.JobCreateInAdvanceChargeFees] per event, unique per
// subscription and transaction ID ([kernel.InAdvanceKey]), so that a
// redelivered event enqueues nothing while its job waits. It runs in the
// transaction carried by ctx (the ingestion transaction on the PostgreSQL
// path), or in a new one.
func (e *Engine) EnqueueInAdvanceCharges(ctx context.Context, org core.OrganizationID, events []kernel.InAdvanceChargeArgs) error {
	if len(events) == 0 {
		return nil
	}
	now := e.d.Now()
	return e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		for _, a := range events {
			if a.SubscriptionID.IsZero() || a.TransactionID == "" {
				return fmt.Errorf("in-advance event %q: %w: no subscription or transaction ID", a.TransactionID, core.ErrInvalidValue)
			}
			if err := kernel.Enqueue(ctx, r, org, kernel.JobCreateInAdvanceChargeFees, a, kernel.InAdvanceKey(a.SubscriptionID, a.TransactionID), now); err != nil {
				return err
			}
		}
		return nil
	})
}
