package billing

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/metering"
)

// DefineUsageBuckets defines the usage bucket definition d of the
// organization org in the event store and backfills its buckets
// ([kernel.JobDefineUsageBuckets]). A store keeping no usage buckets has
// nothing to define: its reads stay on the events.
func (e *Engine) DefineUsageBuckets(ctx context.Context, org core.OrganizationID, d metering.BucketDefinition) error {
	if d.OrganizationID != org.String() || d.Code == "" {
		return fmt.Errorf("define usage buckets: %w: definition of %q for %q", core.ErrInvalidValue, d.OrganizationID, d.Code)
	}
	definer, ok := e.d.Events.(kernel.BucketDefiner)
	if !ok {
		return nil
	}
	err := definer.DefineBuckets(ctx, d)
	if errors.Is(err, metering.ErrBucketsUnavailable) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("define usage buckets of %s: %w", d.Code, err)
	}
	return nil
}
