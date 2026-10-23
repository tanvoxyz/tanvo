package customers

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// View is a customer with what its presentation derives from other
// records.
type View struct {
	// Customer is the customer.
	Customer *customer.Customer
	// ApplicableTimezone is the customer's time zone, else its billing
	// entity's, else UTC.
	ApplicableTimezone *time.Location
	// Connections are the customer's connections, as [Service.ListConnections]
	// returns them.
	Connections []Connection
}

// Views returns the views of cs, in order.
//
// source: app/serializers/v1/customer_serializer.rb (applicable_timezone,
// integration_customers)
func (s *Service) Views(ctx context.Context, cs ...*customer.Customer) ([]View, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]View, error) {
		entities := map[core.BillingEntityID]*organization.BillingEntity{}
		out := make([]View, len(cs))
		for i, c := range cs {
			be, ok := entities[c.BillingEntityID]
			if !ok {
				var err error
				if be, err = r.BillingEntities.Get(ctx, c.OrganizationID, c.BillingEntityID); err != nil {
					return nil, fmt.Errorf("load billing entity %s: %w", c.BillingEntityID, err)
				}
				entities[c.BillingEntityID] = be
			}
			conns, err := connections(ctx, r, c.OrganizationID, c.ID)
			if err != nil {
				return nil, err
			}
			out[i] = View{Customer: c, ApplicableTimezone: c.ApplicableTimezone(kernel.DefaultsOf(be)), Connections: conns}
		}
		return out, nil
	})
}

// BillingEntityIDs resolves the billing entities of rs (IDs or codes,
// archived entities included) for a customer list filter; one that does
// not exist is billing_entity not found.
//
// source: app/controllers/api/v1/customers_controller.rb:53-57
func (s *Service) BillingEntityIDs(ctx context.Context, org core.OrganizationID, rs []refs.Ref) ([]core.BillingEntityID, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]core.BillingEntityID, error) {
		f := refs.Finder[core.BillingEntityID, *organization.BillingEntity]{
			Resource: ResourceBillingEntity,
			Get: func(ctx context.Context, id core.BillingEntityID) (*organization.BillingEntity, error) {
				return r.BillingEntities.Get(ctx, org, id)
			},
			GetByCode: func(ctx context.Context, code string) (*organization.BillingEntity, error) {
				return r.BillingEntities.GetByCode(ctx, org, code)
			},
			ID: func(b *organization.BillingEntity) core.BillingEntityID { return b.ID },
		}
		return f.IDs(ctx, rs)
	})
}
