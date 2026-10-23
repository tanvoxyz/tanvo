package quotes

import (
	"context"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// CustomerByExternalID returns the ID of the customer of org with that
// external ID; not found customer otherwise.
func (s *Service) CustomerByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (core.CustomerID, error) {
	var id core.CustomerID
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Customers.GetByExternalID(ctx, org, externalID)
		if err != nil {
			return notFound(err, "customer")
		}
		id = c.ID
		return nil
	})
	return id, err
}

// SubscriptionByExternalID returns the ID of the live subscription of org
// with that external ID; not found subscription otherwise.
func (s *Service) SubscriptionByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (core.SubscriptionID, error) {
	var id core.SubscriptionID
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		sub, err := r.Subscriptions.GetByExternalID(ctx, org, externalID)
		if err != nil {
			return notFound(err, "subscription")
		}
		id = sub.ID
		return nil
	})
	return id, err
}

// BillingEntityByCode returns the ID of the billing entity of org with
// that code; not found billing_entity otherwise.
func (s *Service) BillingEntityByCode(ctx context.Context, org core.OrganizationID, code string) (core.BillingEntityID, error) {
	var id core.BillingEntityID
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		be, err := r.BillingEntities.GetByCode(ctx, org, code)
		if err != nil {
			return notFound(err, "billing_entity")
		}
		id = be.ID
		return nil
	})
	return id, err
}
