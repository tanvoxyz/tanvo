package customers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
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
	// IsDefault marks the default connection of its category.
	IsDefault bool
	// TargetedObject is the HubSpot object of the customer
	// (integration.TargetedObjectCompanies or TargetedObjectContacts),
	// "" while unset or for other providers.
	TargetedObject string
	// Email is the email HubSpot stored for the customer.
	Email string
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
	// TargetedObject is the HubSpot object of the customer; other
	// providers ignore it (lago create_service.rb:84-87).
	TargetedObject core.Optional[string]
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
		IsDefault:              pc.IsDefault,
		CreatedAt:              pc.CreatedAt,
		UpdatedAt:              pc.UpdatedAt,
	}
}

func integrationConnection(in *integration.Integration, ic *integration.IntegrationCustomer) Connection {
	object, _ := ic.Setting(settingTargetedObject)
	email, _ := ic.Setting(settingEmail)
	return Connection{
		TargetedObject:     object,
		Email:              email,
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
// A payment connection is a provider customer; an accounting, tax or CRM
// connection is an integration customer. A customer has at most one
// connection per provider (customer_id value_already_exist). The first of
// its category, or one asked to be, is the default and the others of the
// category stop being it (payment_provider_customers and
// integration_customers create_connection_service.rb,
// set_as_default_service.rb). None is created or changed for a partner
// account (integration customers), and none is created without an
// external customer ID or sync_with_provider: the result is
// the zero Connection, whose Integration is nil
// (create_connection_service.rb:18-19,
// create_or_update_batch_service.rb:16, 90-94). An update assigns a
// non-blank external customer ID and targeted object only, and neither to
// an Anrok or Salesforce connection (update_service.rb:18-22); the sync
// flag and the subsidiary are set at creation.
//
// source: app/services/integration_customers/create_or_update_batch_service.rb:14-29
// source: app/services/payment_provider_customers/create_connection_service.rb:14-37
// source: app/services/payment_provider_customers/set_as_default_service.rb:14-31
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
		switch integ.Category {
		case integration.CategoryPayment:
			out, err = s.upsertPayment(ctx, r, c, integ, in)
			return err
		case integration.CategoryAuth:
			// A connection has no category (create_connection_service.rb:17).
			return fieldError("integration_id", core.CodeInvalid)
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
	links, err := r.PaymentProviderCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID)
	if err != nil {
		return Connection{}, fmt.Errorf("list payment connections: %w", err)
	}
	links = slices.DeleteFunc(links, (*payment.ProviderCustomer).Deleted)
	i := slices.IndexFunc(links, func(l *payment.ProviderCustomer) bool { return l.IntegrationID == integ.ID })
	var pc *payment.ProviderCustomer
	var v core.Validator
	created := i < 0
	if created && in.ExternalCustomerID.Value == "" && !in.SyncWithProvider.Value {
		// create_connection_service.rb:18, 54-56.
		return Connection{}, nil
	}
	if created {
		pc = &payment.ProviderCustomer{
			ID:             core.NewID[core.PaymentProviderCustomerID](s.d.IDs),
			OrganizationID: c.OrganizationID,
			CustomerID:     c.ID,
			IntegrationID:  integ.ID,
			// create_connection_service.rb:21, 27: the first is the default.
			IsDefault: len(links) == 0,
			CreatedAt: now,
		}
		// One connection per provider (base_customer.rb:32, customer_id
		// value_already_exist).
		for _, l := range links {
			li, err := r.Integrations.Get(ctx, c.OrganizationID, l.IntegrationID)
			if err != nil {
				return Connection{}, fmt.Errorf("load integration %s: %w", l.IntegrationID, err)
			}
			if li.Provider == integ.Provider {
				v.Add("customer_id", core.CodeAlreadyExists)
				break
			}
		}
	} else {
		pc = links[i]
	}
	ch := kernel.ConnectionChange{Created: created, ProviderCustomerIDSet: in.ExternalCustomerID.Set}
	if pc.ProviderCustomerID != nil {
		ch.PreviousProviderCustomerID = *pc.ProviderCustomerID
	}
	if in.ExternalCustomerID.Set {
		pc.ProviderCustomerID = nil
		if in.ExternalCustomerID.Value != "" {
			pc.ProviderCustomerID = new(in.ExternalCustomerID.Value)
		}
	}
	in.SyncWithProvider.Apply(&pc.SyncWithProvider)
	// stripe/customers/create_service.rb:60-70: an empty list keeps an
	// existing connection's types and gives a new one the defaults.
	types := in.ProviderPaymentMethods.Set && len(in.ProviderPaymentMethods.Value) > 0
	if types {
		pc.ProviderPaymentMethods = in.ProviderPaymentMethods.Value
	}
	pc.UpdatedAt = now
	v.Merge("", pc.Validate())
	if gw := s.customerGateway(integ.Provider); gw != nil && (created || types) {
		if created && len(pc.ProviderPaymentMethods) == 0 {
			pc.ProviderPaymentMethods = gw.DefaultMethodTypes()
		}
		// stripe_customer.rb:9-12, validated on every save.
		v.Merge("", gw.ValidateMethodTypes(pc.ProviderPaymentMethods))
	}
	if err := v.Err(); err != nil {
		return Connection{}, err
	}
	if in.IsDefault.Set && in.IsDefault.Value && !pc.IsDefault {
		// set_as_default_service.rb:23-27: the others stop being it.
		for _, l := range links {
			if l.ID == pc.ID || !l.IsDefault {
				continue
			}
			l.IsDefault, l.UpdatedAt = false, now
			if err := r.PaymentProviderCustomers.Update(ctx, l); err != nil {
				return Connection{}, fmt.Errorf("unset default payment connection %s: %w", l.ID, err)
			}
		}
		pc.IsDefault = true
	} else if in.IsDefault.Set && !in.IsDefault.Value {
		pc.IsDefault = false
	}
	if created {
		err = r.PaymentProviderCustomers.Create(ctx, pc)
	} else {
		err = r.PaymentProviderCustomers.Update(ctx, pc)
	}
	if errors.Is(err, core.ErrConflict) {
		return Connection{}, fieldError("customer_id", core.CodeAlreadyExists)
	}
	if err != nil {
		return Connection{}, fmt.Errorf("store payment connection: %w", err)
	}
	if err := kernel.EnqueueConnectionSync(ctx, r, pc, ch); err != nil {
		return Connection{}, err
	}
	return paymentConnection(integ, pc), nil
}

// customerGateway returns the adapter of payment vendor p, or nil without
// one.
func (s *Service) customerGateway(p integration.Provider) payment.CustomerGateway {
	if s.gateways == nil {
		return nil
	}
	gw, err := s.gateways.PaymentProvider(p)
	if err != nil {
		return nil
	}
	return gw
}

func (s *Service) upsertIntegration(ctx context.Context, r port.Repos, c *customer.Customer, integ *integration.Integration, in ConnectionInput) (Connection, error) {
	now := s.d.Now()
	links, err := r.IntegrationCustomers.ListByCustomer(ctx, c.OrganizationID, c.ID, integ.Category)
	if err != nil {
		return Connection{}, fmt.Errorf("list integration connections: %w", err)
	}
	i := slices.IndexFunc(links, func(l *integration.IntegrationCustomer) bool { return l.IntegrationID == integ.ID })
	var ic *integration.IntegrationCustomer
	var v core.Validator
	created := i < 0
	switch {
	case created && (c.SelfBilled() || in.ExternalCustomerID.Value == "" && !in.SyncWithProvider.Value):
		return Connection{}, nil
	case c.SelfBilled():
		// create_or_update_batch_service.rb:16: a partner's connections
		// are left as they are.
		return integrationConnection(integ, links[i]), nil
	}
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
		// One connection per provider (lago base_customer.rb:42,
		// customer_id value_already_exist).
		for _, l := range links {
			li, err := r.Integrations.Get(ctx, c.OrganizationID, l.IntegrationID)
			if err != nil {
				return Connection{}, fmt.Errorf("load integration %s: %w", l.IntegrationID, err)
			}
			if li.Provider == integ.Provider {
				v.Add("customer_id", core.CodeAlreadyExists)
				break
			}
		}
	} else {
		ic = links[i]
	}
	fixed := integ.Provider == integration.ProviderAnrok || integ.Provider == integration.ProviderSalesforce
	switch {
	case created:
		in.ExternalCustomerID.Apply(&ic.ExternalCustomerID)
		in.SyncWithProvider.Apply(&ic.SyncWithProvider)
		in.SubsidiaryID.Apply(&ic.SubsidiaryID)
		if ic.ExternalCustomerID != "" {
			// A linked record is not synced, Salesforce's excepted (lago
			// create_service.rb:66-75).
			ic.SyncWithProvider = integ.Provider == integration.ProviderSalesforce
		}
	case !fixed && in.ExternalCustomerID.Value != "":
		ic.ExternalCustomerID = in.ExternalCustomerID.Value
	}
	if in.TargetedObject.Set && integ.Provider == integration.ProviderHubspot {
		if err := setTargetedObject(ic, in.TargetedObject.Value); err != nil {
			return Connection{}, err
		}
	}
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
	v.Merge("", ic.Validate())
	// base_customer.rb:42-43: the provider and tax rules are reported
	// together.
	linkErr := integration.CheckCustomerLinks(ic, links)
	if _, ok := errors.AsType[*core.ValidationError](linkErr); ok {
		v.Merge("", linkErr)
		linkErr = nil
	}
	if err := v.Err(); err != nil {
		return Connection{}, err
	}
	if linkErr != nil {
		return Connection{}, linkErr
	}
	if created {
		err = r.IntegrationCustomers.Create(ctx, ic)
	} else {
		err = r.IntegrationCustomers.Update(ctx, ic)
	}
	if err != nil {
		return Connection{}, fmt.Errorf("store integration connection: %w", err)
	}
	if err := kernel.EnqueueContactSync(ctx, r, c, integ, ic, created); err != nil {
		return Connection{}, err
	}
	return integrationConnection(integ, ic), nil
}

// DeleteConnection deletes the connection of the customer id to the
// integration ref names: a payment connection is discarded with its
// payment methods, an integration customer destroyed, and the billing
// objects' choices of either deleted (lago
// PaymentProviderCustomers::DestroyService,
// IntegrationCustomers::DestroyService). Another connection does not
// become the default.
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
			return s.discardPayment(ctx, r, pc)
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

// discardPayment discards the payment connection pc, which stops being
// the default, with its payment methods and the billing objects' choices of
// it.
//
// source: app/services/payment_provider_customers/destroy_service.rb:14-26
func (s *Service) discardPayment(ctx context.Context, r port.Repos, pc *payment.ProviderCustomer) error {
	now := s.d.Now()
	pc.IsDefault = false
	pc.DeletedAt, pc.UpdatedAt = &now, now
	if err := r.PaymentProviderCustomers.Update(ctx, pc); err != nil {
		return fmt.Errorf("discard payment connection: %w", err)
	}
	methods, err := r.PaymentMethods.ListByCustomer(ctx, pc.OrganizationID, pc.CustomerID)
	if err != nil {
		return fmt.Errorf("list payment methods: %w", err)
	}
	for _, m := range methods {
		if m.PaymentProviderCustomerID != pc.ID || m.Deleted() {
			continue
		}
		if err := m.Delete(now); err != nil {
			return err
		}
		if err := r.PaymentMethods.Update(ctx, m); err != nil {
			return fmt.Errorf("discard payment method %s: %w", m.ID, err)
		}
	}
	if err := r.ObjectConnections.DeleteByPaymentProviderCustomer(ctx, pc.OrganizationID, pc.ID); err != nil {
		return fmt.Errorf("delete billing object connections: %w", err)
	}
	return nil
}

// CheckoutURL returns the checkout URL of the live customer id with its
// payment connection to the integration ref names, or its payment
// connection in effect for a zero ref (the only one, else the default). A
// customer without one fails with
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
		if f.pc = payment.DefaultProviderCustomer(pcs); f.pc == nil {
			return f, fieldError("base", CodeNoLinkedPaymentProvider)
		}
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

// Settings keys of a HubSpot integration customer.
const (
	settingTargetedObject = "targeted_object"
	settingEmail          = "email"
)

// setTargetedObject sets the HubSpot object of ic; "" keeps it (lago
// update_service.rb:23, targeted_object.present?).
func setTargetedObject(ic *integration.IntegrationCustomer, object string) error {
	switch object {
	case "":
		return nil
	case integration.TargetedObjectCompanies, integration.TargetedObjectContacts:
		return ic.SetSetting(settingTargetedObject, object)
	default:
		return fieldError("targeted_object", core.CodeInvalid)
	}
}
