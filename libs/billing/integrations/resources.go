package integrations

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// recordResource records that the local object syncable of type rt is
// the provider's object externalID in the integration i (lago
// IntegrationResource); a record already kept is left as it is.
func (s *Service) recordResource(ctx context.Context, r port.Repos, i *integration.Integration, rt integration.ResourceType, syncable uuid.UUID, externalID string) error {
	_, err := r.IntegrationResources.Find(ctx, i.OrganizationID, i.ID, rt, syncable)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, core.ErrNotFound):
		return fmt.Errorf("load %s resource %s: %w", rt, syncable, err)
	}
	now := s.d.Now()
	res := &integration.Resource{
		ID: core.NewID[core.IntegrationResourceID](s.d.IDs), OrganizationID: i.OrganizationID, IntegrationID: i.ID,
		ResourceType: rt, SyncableID: syncable, ExternalID: externalID, CreatedAt: now, UpdatedAt: now,
	}
	if err := res.Validate(); err != nil {
		return err
	}
	if err := r.IntegrationResources.Create(ctx, res); err != nil {
		return fmt.Errorf("record %s resource %s: %w", rt, syncable, err)
	}
	return nil
}

// billingEntityOf loads the billing entity of c, or the organization's
// default one.
func billingEntityOf(ctx context.Context, r port.Repos, c *customer.Customer) (*organization.BillingEntity, error) {
	var (
		be  *organization.BillingEntity
		err error
	)
	if c.BillingEntityID.IsZero() {
		be, err = r.BillingEntities.GetDefault(ctx, c.OrganizationID)
	} else {
		be, err = r.BillingEntities.Get(ctx, c.OrganizationID, c.BillingEntityID)
	}
	if err != nil {
		return nil, fmt.Errorf("load billing entity of customer %s: %w", c.ID, err)
	}
	return be, nil
}
