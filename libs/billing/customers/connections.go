package customers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Connection is a customer's link to an integration: a payment provider
// customer, or an accounting, tax or CRM integration customer.
type Connection struct {
	// Integration is the integration.
	Integration *integration.Integration
	// ExternalCustomerID is the customer's ID in the provider, "" while
	// unknown.
	ExternalCustomerID string
	// SyncWithProvider creates and updates the customer in the provider.
	SyncWithProvider bool
	// SubsidiaryID is the NetSuite subsidiary.
	SubsidiaryID *string
	// ProviderPaymentMethods are the payment method types allowed at
	// checkout (payment connections).
	ProviderPaymentMethods []string
	// IsDefault marks the default connection of its category; a payment
	// connection, the only one of its customer, is the default.
	IsDefault bool
	// CreatedAt is the creation instant.
	CreatedAt time.Time
	// UpdatedAt is the instant of the last change.
	UpdatedAt time.Time
}

// ConnectionInput is the attributes of a connection to create or update:
// only the set fields are assigned.
type ConnectionInput struct {
	// ExternalCustomerID is the customer's ID in the provider.
	ExternalCustomerID core.Optional[string]
	// SyncWithProvider creates and updates the customer in the provider.
	SyncWithProvider core.Optional[bool]
	// SubsidiaryID is the NetSuite subsidiary.
	SubsidiaryID core.Optional[*string]
	// ProviderPaymentMethods are the payment method types allowed at
	// checkout (payment connections only).
	ProviderPaymentMethods core.Optional[[]string]
	// IsDefault makes the connection the default of its category.
	IsDefault core.Optional[bool]
}

// CheckoutURLGenerator generates the checkout URL where a customer
// registers a payment method with a payment provider.
type CheckoutURLGenerator interface {
	// GenerateCheckoutURL returns the URL and when it expires.
	GenerateCheckoutURL(ctx context.Context, c *customer.Customer, in *integration.Integration, pc *payment.ProviderCustomer) (string, time.Time, error)
}

func integrations(r port.Repos, org core.OrganizationID) refs.Finder[core.IntegrationID, *integration.Integration] {
	return refs.Finder[core.IntegrationID, *integration.Integration]{
		Resource: ResourceIntegration,
		Get: func(ctx context.Context, id core.IntegrationID) (*integration.Integration, error) {
			return r.Integrations.Get(ctx, org, id)
		},
		GetByCode: func(ctx context.Context, code string) (*integration.Integration, error) {
			return r.Integrations.GetByCode(ctx, org, code)
		},
		ID: func(i *integration.Integration) core.IntegrationID { return i.ID },
	}
}

// liveCustomer returns the live customer id.
func liveCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID) (*customer.Customer, error) {
	c, err := r.Customers.Get(ctx, org, id)
	if err != nil {
		return nil, notFound(err)
	}
	if c.Deleted() {
		return nil, core.NotFound(Resource)
	}
	return c, nil
}

// ListConnections returns the connections of the customer id: the
// payment connections first, then the others, each in creation order.
//
// source: app/serializers/v1/customer_serializer.rb (integration_customers)
func (s *Service) ListConnections(ctx context.Context, org core.OrganizationID, id core.CustomerID) ([]Connection, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]Connection, error) {
		if _, err := r.Customers.Get(ctx, org, id); err != nil {
			return nil, notFound(err)
		}
		return connections(ctx, r, org, id)
	})
}

func connections(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID) ([]Connection, error) {
	cache := map[core.IntegrationID]*integration.Integration{}
	load := func(iid core.IntegrationID) (*integration.Integration, error) {
		if in, ok := cache[iid]; ok {
			return in, nil
		}
		in, err := r.Integrations.Get(ctx, org, iid)
		if err != nil {
			return nil, fmt.Errorf("load integration %s: %w", iid, err)
		}
		cache[iid] = in
		return in, nil
	}
	pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, id)
	if err != nil {
		return nil, fmt.Errorf("list payment connections: %w", err)
	}
	var out []Connection
	for _, pc := range pcs {
		in, err := load(pc.IntegrationID)
		if err != nil {
			return nil, err
		}
		out = append(out, paymentConnection(in, pc))
	}
	ics, err := r.IntegrationCustomers.ListByCustomer(ctx, org, id, "")
	if err != nil {
		return nil, fmt.Errorf("list integration connections: %w", err)
	}
	for _, ic := range ics {
		in, err := load(ic.IntegrationID)
		if err != nil {
			return nil, err
		}
		out = append(out, integrationConnection(in, ic))
	}
	return out, nil
}

func paymentConnection(in *integration.Integration, pc *payment.ProviderCustomer) Connection {
	return Connection{
		Integration:            in,
		ExternalCustomerID:     value(pc.ProviderCustomerID),
		SyncWithProvider:       pc.SyncWithProvider,
		ProviderPaymentMethods: slices.Clone(pc.ProviderPaymentMethods),
		IsDefault:              true,
		CreatedAt:              pc.CreatedAt,
		UpdatedAt:              pc.UpdatedAt,
	}
}

func integrationConnection(in *integration.Integration, ic *integration.IntegrationCustomer) Connection {
	return Connection{
		Integration:        in,
		ExternalCustomerID: ic.ExternalCustomerID,
		SyncWithProvider:   ic.SyncWithProvider,
		SubsidiaryID:       ic.SubsidiaryID,
		IsDefault:          ic.IsDefault,
		CreatedAt:          ic.CreatedAt,
		UpdatedAt:          ic.UpdatedAt,
	}
}

// UpsertConnection creates or updates the connection of the live customer
// id to the integration ref names.
//
// A payment connection is a provider customer; a customer has at most one
// (only_one_connection_allowed, lago
// payment_provider_customers/create_or_update_batch_service.rb:24-29). An
// accounting, tax or CRM connection is an integration customer: the first
// of its category, or one asked to be, is the default and the others of
// the category stop being it
// (integration_customers/create_connection_service.rb,
// set_as_default_service.rb); a partner account has none
// (create_or_update_batch_service.rb:16, a failure here, a silent no-op in
// lago). Creating the customer in the provider when sync_with_provider is
// set and no external ID is known needs the provider adapters (phase 5):
// the connection is stored and awaits them.
//
// source: app/services/integration_customers/create_or_update_batch_service.rb:14-29
// source: app/services/payment_provider_customers/create_or_update_batch_service.rb:20-80
func (s *Service) UpsertConnection(ctx context.Context, org core.OrganizationID, id core.CustomerID, ref refs.Ref, in ConnectionInput) (Connection, error) {
	var out Connection
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := liveCustomer(ctx, r, org, id)
		if err != nil {
			return err
		}
		integ, err := integrations(r, org).One(ctx, ref)
		if err != nil {
			return err
		}
		if integ.Category == integration.CategoryPayment {
			out, err = s.upsertPayment(ctx, r, c, integ, in)
			return err
		}
		if c.SelfBilled() {
			return core.NewRuleError(CodePartnerAccount, "a partner account has no integration connections")
		}
		out, err = s.upsertIntegration(ctx, r, c, integ, in)
		return err
	})
	if err != nil {
		return Connection{}, fmt.Errorf("upsert connection of customer %s: %w", id, err)
	}
	return out, nil
}

func (s *Service) upsertPayment(ctx context.Context, r port.Repos, c *customer.Customer, integ *integration.Integration, in ConnectionInput) (Connection, error) {
	now := s.d.Now()
	pc, err := r.PaymentProviderCustomers.GetByCustomer(ctx, c.OrganizationID, c.ID, integ.ID)
	created := errors.Is(err, core.ErrNotFound)
	switch {
	case created:
		others, err := r.PaymentProviderCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID)
		if err != nil {
			return Connection{}, fmt.Errorf("list payment connections: %w", err)
		}
		if len(others) > 0 {
			return Connection{}, fieldError("connection", CodeOnlyOneConnectionAllowed)
		}
		pc = &payment.ProviderCustomer{
			ID:             core.NewID[core.PaymentProviderCustomerID](s.d.IDs),
			OrganizationID: c.OrganizationID,
			CustomerID:     c.ID,
			IntegrationID:  integ.ID,
			CreatedAt:      now,
		}
	case err != nil:
		return Connection{}, fmt.Errorf("find payment connection: %w", err)
	}
	if in.ExternalCustomerID.Set {
		pc.ProviderCustomerID = nil
		if in.ExternalCustomerID.Value != "" {
			pc.ProviderCustomerID = new(in.ExternalCustomerID.Value)
		}
	}
	in.SyncWithProvider.Apply(&pc.SyncWithProvider)
	in.ProviderPaymentMethods.Apply(&pc.ProviderPaymentMethods)
	pc.UpdatedAt = now
	var v core.Validator
	v.Merge("", pc.Validate())
	if err := v.Err(); err != nil {
		return Connection{}, err
	}
	if created {
		err = r.PaymentProviderCustomers.Create(ctx, pc)
	} else {
		err = r.PaymentProviderCustomers.Update(ctx, pc)
	}
	if errors.Is(err, core.ErrConflict) {
		return Connection{}, fieldError("connection", CodeOnlyOneConnectionAllowed)
	}
	if err != nil {
		return Connection{}, fmt.Errorf("store payment connection: %w", err)
	}
	return paymentConnection(integ, pc), nil
}

func (s *Service) upsertIntegration(ctx context.Context, r port.Repos, c *customer.Customer, integ *integration.Integration, in ConnectionInput) (Connection, error) {
	now := s.d.Now()
	links, err := r.IntegrationCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID, integ.Category)
	if err != nil {
		return Connection{}, fmt.Errorf("list integration connections: %w", err)
	}
	i := slices.IndexFunc(links, func(l *integration.IntegrationCustomer) bool { return l.IntegrationID == integ.ID })
	var ic *integration.IntegrationCustomer
	created := i < 0
	if created {
		ic = &integration.IntegrationCustomer{
			ID:             core.NewID[core.IntegrationCustomerID](s.d.IDs),
			OrganizationID: c.OrganizationID,
			IntegrationID:  integ.ID,
			CustomerID:     c.ID,
			Category:       integ.Category,
			IsDefault:      !slices.ContainsFunc(links, func(l *integration.IntegrationCustomer) bool { return l.IsDefault }),
			CreatedAt:      now,
		}
	} else {
		ic = links[i]
	}
	in.ExternalCustomerID.Apply(&ic.ExternalCustomerID)
	in.SyncWithProvider.Apply(&ic.SyncWithProvider)
	in.SubsidiaryID.Apply(&ic.SubsidiaryID)
	if in.IsDefault.Set && in.IsDefault.Value {
		ic.IsDefault = true
		for _, l := range links {
			if l.ID == ic.ID || !l.IsDefault {
				continue
			}
			l.IsDefault, l.UpdatedAt = false, now
			if err := r.IntegrationCustomers.Update(ctx, l); err != nil {
				return Connection{}, fmt.Errorf("unset default connection %s: %w", l.ID, err)
			}
		}
	} else if in.IsDefault.Set {
		ic.IsDefault = false
	}
	ic.UpdatedAt = now
	var v core.Validator
	v.Merge("", ic.Validate())
	if err := v.Err(); err != nil {
		return Connection{}, err
	}
	if err := integration.CheckCustomerLinks(ic, links); err != nil {
		return Connection{}, err
	}
	if created {
		err = r.IntegrationCustomers.Create(ctx, ic)
	} else {
		err = r.IntegrationCustomers.Update(ctx, ic)
	}
	if errors.Is(err, core.ErrConflict) {
		return Connection{}, fieldError("external_customer_id", core.CodeAlreadyExists)
	}
	if err != nil {
		return Connection{}, fmt.Errorf("store integration connection: %w", err)
	}
	return integrationConnection(integ, ic), nil
}

// DeleteConnection deletes the connection of the customer id to the
// integration ref names: a payment connection is discarded, an integration
// customer destroyed (lago PaymentProviderCustomers::DestroyService,
// IntegrationCustomers::DestroyService).
func (s *Service) DeleteConnection(ctx context.Context, org core.OrganizationID, id core.CustomerID, ref refs.Ref) error {
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.Customers.Get(ctx, org, id); err != nil {
			return notFound(err)
		}
		integ, err := integrations(r, org).One(ctx, ref)
		if err != nil {
			return err
		}
		if integ.Category == integration.CategoryPayment {
			pc, err := r.PaymentProviderCustomers.GetByCustomer(ctx, org, id, integ.ID)
			if errors.Is(err, core.ErrNotFound) {
				return core.NotFound(ResourceConnection)
			} else if err != nil {
				return fmt.Errorf("find payment connection: %w", err)
			}
			now := s.d.Now()
			pc.DeletedAt, pc.UpdatedAt = &now, now
			if err := r.PaymentProviderCustomers.Update(ctx, pc); err != nil {
				return fmt.Errorf("discard payment connection: %w", err)
			}
			return nil
		}
		links, err := r.IntegrationCustomers.ListByCustomer(ctx, org, id, integ.Category)
		if err != nil {
			return fmt.Errorf("list integration connections: %w", err)
		}
		i := slices.IndexFunc(links, func(l *integration.IntegrationCustomer) bool { return l.IntegrationID == integ.ID })
		if i < 0 {
			return core.NotFound(ResourceConnection)
		}
		if err := r.IntegrationCustomers.Delete(ctx, org, links[i].ID); err != nil {
			return fmt.Errorf("delete integration connection: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete connection of customer %s: %w", id, err)
	}
	return nil
}

// CheckoutURL returns the checkout URL of the live customer id with its
// payment connection to the integration ref names, or its only payment
// connection for a zero ref. A customer without one fails with
// no_linked_payment_provider; without a [CheckoutURLGenerator] the call
// fails with [ErrCheckoutUnavailable] once these checks pass.
//
// source: app/services/customers/generate_checkout_url_service.rb:12-31
func (s *Service) CheckoutURL(ctx context.Context, org core.OrganizationID, id core.CustomerID, ref refs.Ref) (string, time.Time, error) {
	type found struct {
		c     *customer.Customer
		integ *integration.Integration
		pc    *payment.ProviderCustomer
	}
	f, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) (found, error) {
		var f found
		c, err := liveCustomer(ctx, r, org, id)
		if err != nil {
			return f, err
		}
		f.c = c
		pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, id)
		if err != nil {
			return f, fmt.Errorf("list payment connections: %w", err)
		}
		if !ref.IsZero() {
			integ, err := integrations(r, org).One(ctx, ref)
			if err != nil {
				return f, err
			}
			pcs = slices.DeleteFunc(pcs, func(pc *payment.ProviderCustomer) bool { return pc.IntegrationID != integ.ID })
		}
		if len(pcs) == 0 {
			return f, fieldError("base", CodeNoLinkedPaymentProvider)
		}
		f.pc = pcs[0]
		if f.integ, err = r.Integrations.Get(ctx, org, f.pc.IntegrationID); err != nil {
			return f, fmt.Errorf("load integration %s: %w", f.pc.IntegrationID, err)
		}
		return f, nil
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("checkout url of customer %s: %w", id, err)
	}
	if s.checkout == nil {
		return "", time.Time{}, ErrCheckoutUnavailable
	}
	u, exp, err := s.checkout.GenerateCheckoutURL(ctx, f.c, f.integ, f.pc)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("checkout url of customer %s: %w", id, err)
	}
	return u, exp, nil
}
