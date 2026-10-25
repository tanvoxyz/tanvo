package payments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// ErrNoProviders reports a provider operation without payment provider
// adapters ([Service.WithProviders]).
var ErrNoProviders = errors.New("payments: no payment providers configured")

// ErrNoAdapter reports a connection whose vendor has no adapter among the
// configured providers ([Providers.Gateways]).
var ErrNoAdapter = errors.New("payments: no adapter for the connection's provider")

// Providers are what the provider operations need: the adapters, keyed by
// vendor, the sealer of the connections' credentials and the URL of each
// connection's webhook endpoint.
type Providers struct {
	// Gateways resolves the adapter of a connection's vendor.
	Gateways port.PaymentProviders
	// Secrets opens the sealed credentials of a connection and seals those
	// the provider returns ([payment.ConnectionUpdate]).
	Secrets integrations.Sealer
	// WebhookURL returns the endpoint a connection's provider delivers its
	// webhooks to; nil registers none.
	WebhookURL func(conn payment.Connection) string
}

// PaymentSync syncs a succeeded invoice payment to the accounting
// integrations whose connection syncs payments (lago
// Integrations::Aggregator::Payments::CreateJob when
// Payment#should_sync_payment?); the accounting integrations implement it.
type PaymentSync interface {
	// EnqueuePaymentSync enqueues, in the transaction of r, the sync of p,
	// a succeeded payment of the finalized invoice inv; it enqueues
	// nothing when no accounting connection of the customer syncs
	// payments.
	EnqueuePaymentSync(ctx context.Context, r port.Repos, p *payment.Payment, inv *invoice.Invoice) error
}

// WithProviders sets the payment provider adapters; without them the
// automatic payments, payment links and provider webhooks fail with
// [ErrNoProviders].
func (s *Service) WithProviders(p Providers) *Service {
	s.providers = &p
	return s
}

// WithPaymentSync sets the accounting sync of succeeded payments; without
// one, payments are not synced.
func (s *Service) WithPaymentSync(ps PaymentSync) *Service {
	s.sync = ps
	return s
}

// providerConn is an open connection to a payment provider.
type providerConn struct {
	gw    port.PaymentProvider
	conn  payment.Connection
	integ *integration.Integration
}

// open returns the adapter of integ with its connection, its credentials
// opened.
func (s *Service) open(ctx context.Context, integ *integration.Integration) (providerConn, error) {
	if s.providers == nil || s.providers.Gateways == nil {
		return providerConn{}, ErrNoProviders
	}
	gw, err := s.providers.Gateways.PaymentProvider(integ.Provider)
	if err != nil {
		return providerConn{}, fmt.Errorf("integration %s: %w: %s", integ.ID, ErrNoAdapter, err.Error())
	}
	conn := payment.Connection{
		ID: integ.ID, OrganizationID: integ.OrganizationID, Code: integ.Code, Provider: integ.Provider,
		Settings: integ.Settings,
	}
	if s.providers.WebhookURL != nil {
		conn.WebhookURL = s.providers.WebhookURL(conn)
	}
	if len(integ.Secrets) > 0 {
		if s.providers.Secrets == nil {
			return providerConn{}, integrations.ErrNoSealer
		}
		plain, err := s.providers.Secrets.Open(ctx, integ.OrganizationID, integ.Secrets)
		if err != nil {
			return providerConn{}, fmt.Errorf("open secrets of integration %s: %w", integ.ID, err)
		}
		conn.Secrets = json.RawMessage(plain)
	}
	return providerConn{gw: gw, conn: conn, integ: integ}, nil
}

// customerConnection returns the customer's payment connection in effect
// (the only one, else the default, [payment.DefaultProviderCustomer]) and
// its live integration, or nils when there is none (lago
// Customer#payment_provider with PaymentProviderFinder).
func customerConnection(ctx context.Context, r port.Repos, org core.OrganizationID, id core.CustomerID) (*payment.ProviderCustomer, *integration.Integration, error) {
	pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, id)
	if err != nil {
		return nil, nil, wrap(err, "list payment connections of customer %s", id)
	}
	pc := payment.DefaultProviderCustomer(pcs)
	if pc == nil {
		return nil, nil, nil
	}
	integ, err := liveIntegration(ctx, r, org, pc.IntegrationID)
	if err != nil || integ == nil {
		return nil, nil, err
	}
	return pc, integ, nil
}

// liveIntegration returns a live payment integration, or nil when it is
// missing or discarded.
func liveIntegration(ctx context.Context, r port.Repos, org core.OrganizationID, id core.IntegrationID) (*integration.Integration, error) {
	integ, err := r.Integrations.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap(err, "load integration %s", id)
	}
	if integ.DeletedAt != nil || integ.Category != integration.CategoryPayment {
		return nil, nil
	}
	return integ, nil
}

// providerCustomerID returns the provider's customer ID of pc, or "".
func providerCustomerID(pc *payment.ProviderCustomer) string {
	if pc == nil || pc.ProviderCustomerID == nil {
		return ""
	}
	return *pc.ProviderCustomerID
}

// settingOn reports whether a boolean setting of integ is on: the
// integrations store flags as the string "true".
func settingOn(integ *integration.Integration, key string) bool {
	settings, err := integrations.Settings(integ)
	return err == nil && settings[key] == "true"
}
