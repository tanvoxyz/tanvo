package payments

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// CheckoutURLGenerated is the payload of customer.checkout_url_generated
// (lago V1::PaymentProviders::CustomerCheckoutSerializer).
type CheckoutURLGenerated struct {
	// CustomerID is the customer.
	CustomerID core.CustomerID `json:"customer_id"`
	// ExternalCustomerID is its external ID.
	ExternalCustomerID string `json:"external_customer_id"`
	// PaymentProvider is the connection's vendor.
	PaymentProvider integration.Provider `json:"payment_provider"`
	// PaymentProviderCode is the connection's code.
	PaymentProviderCode string `json:"payment_provider_code"`
	// CheckoutURL is the page where the customer saves a payment method.
	CheckoutURL string `json:"checkout_url"`
}

// syncTarget is a provider customer to sync with its customer and open
// connection.
type syncTarget struct {
	pcust *payment.ProviderCustomer
	cust  *customer.Customer
	pc    providerConn
}

// SyncProviderCustomer runs a provider customer job ([kernel.ProviderCustomerArgs]):
//
//   - creation (lago PaymentProviderCustomers::*Service#create): a
//     provider customer without an ID is created at the provider, its ID
//     stored, customer.payment_provider_created emitted and a checkout URL
//     generated; a provider keeping no customers ([payment.ErrUnsupported],
//     Adyen) gets only the checkout URL;
//   - update (#update): the customer's details are pushed to the
//     provider;
//   - checkout (#generate_checkout_url): the page where the customer saves
//     a payment method is made and announced by
//     customer.checkout_url_generated; nothing is made for an organization
//     without webhook endpoints or a provider without such pages, or when
//     none of the customer's method types can be saved;
//   - default method (FetchDefaultPaymentMethodService): the provider
//     customer's default method, else its newest card, is recorded.
//
// A removed connection, provider customer or customer is ignored. A
// provider refusal is announced by customer.payment_provider_error and
// ends the job; a rate-limited or unavailable provider returns an error
// wrapping [payment.ErrRetryable].
func (s *Service) SyncProviderCustomer(ctx context.Context, org core.OrganizationID, args kernel.ProviderCustomerArgs) error {
	if s.providers == nil {
		return ErrNoProviders
	}
	t, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*syncTarget, error) {
		pcust, err := r.PaymentProviderCustomers.Get(ctx, org, args.ProviderCustomerID)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, wrap(err, "load provider customer %s", args.ProviderCustomerID)
		}
		if pcust.Deleted() {
			return nil, nil
		}
		c, err := r.Customers.Get(ctx, org, pcust.CustomerID)
		if isNotFound(err) || err == nil && c.Deleted() {
			return nil, nil
		}
		if err != nil {
			return nil, wrap(err, "load customer %s", pcust.CustomerID)
		}
		integ, err := liveIntegration(ctx, r, org, pcust.IntegrationID)
		if err != nil || integ == nil {
			return nil, err
		}
		pc, err := s.open(ctx, integ)
		if err != nil {
			return nil, err
		}
		return &syncTarget{pcust: pcust, cust: c, pc: pc}, nil
	})
	if err != nil || t == nil {
		return err
	}
	switch {
	case args.Checkout:
		return s.generateCheckout(ctx, t)
	case args.FetchMethod:
		return s.fetchDefaultMethod(ctx, t)
	case args.Update:
		return s.pushCustomer(ctx, t)
	default:
		return s.createProviderCustomer(ctx, t)
	}
}

// providerRefusal handles the error of a provider customer operation: a
// provider refusal is announced by customer.payment_provider_error and
// logged, a retryable error and the others are returned.
func (s *Service) providerRefusal(ctx context.Context, t *syncTarget, op string, err error) error {
	if errors.Is(err, payment.ErrRetryable) {
		return wrap(err, "%s customer %s", op, t.cust.ID)
	}
	if _, ok := payment.AsProviderError(err); !ok {
		return wrap(err, "%s customer %s", op, t.cust.ID)
	}
	slog.WarnContext(ctx, "payments: provider refused a customer operation",
		"organization_id", t.cust.OrganizationID.String(), "customer_id", t.cust.ID.String(),
		"provider", string(t.pc.integ.Provider), "operation", op, "error", err)
	ref := &customerRef{id: t.cust.ID, externalID: t.cust.ExternalID}
	return s.emitCustomerProviderError(ctx, t.pc.integ, ref, providerErrorOf(err))
}

// createProviderCustomer creates t's customer at the provider
// (stripe_service.rb:17-38, gocardless_service.rb, moneyhash_service.rb,
// adyen_service.rb create).
func (s *Service) createProviderCustomer(ctx context.Context, t *syncTarget) error {
	if providerCustomerID(t.pcust) != "" {
		return nil
	}
	params := payment.CustomerParamsOf(t.cust)
	params.IdempotencyKey = payment.CustomerIdempotencyKey(t.cust.ID, t.cust.UpdatedAt)
	id, err := t.pc.gw.CreateCustomer(ctx, t.pc.conn, params)
	if errors.Is(err, payment.ErrUnsupported) {
		// adyen_service.rb create: the shopper is known once a method is
		// saved through the checkout page.
		return s.generateCheckout(ctx, t)
	}
	if err != nil {
		return s.providerRefusal(ctx, t, "create", err)
	}
	if id == "" {
		return nil
	}
	org := t.cust.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		pcust, err := r.PaymentProviderCustomers.Get(ctx, org, t.pcust.ID)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return wrap(err, "load provider customer %s", t.pcust.ID)
		}
		if pcust.Deleted() || providerCustomerID(pcust) != "" {
			return nil
		}
		pcust.ProviderCustomerID = &id
		pcust.UpdatedAt = s.d.Now()
		if err := r.PaymentProviderCustomers.Update(ctx, pcust); err != nil {
			return wrap(err, "update provider customer %s", pcust.ID)
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeCustomerPaymentProviderCreated, "customer", t.cust.ID.UUID(), t.cust); err != nil {
			return err
		}
		return kernel.Enqueue(ctx, r, org, kernel.JobSyncProviderCustomer,
			kernel.ProviderCustomerArgs{ProviderCustomerID: pcust.ID, Checkout: true}, "provider_customer:"+pcust.ID.String()+":checkout", time.Time{})
	})
}

// pushCustomer pushes t's customer to its provider customer
// (stripe_service.rb update).
func (s *Service) pushCustomer(ctx context.Context, t *syncTarget) error {
	id := providerCustomerID(t.pcust)
	if id == "" {
		return nil
	}
	err := t.pc.gw.UpdateCustomer(ctx, t.pc.conn, id, payment.CustomerParamsOf(t.cust))
	if err == nil || errors.Is(err, payment.ErrUnsupported) {
		return nil
	}
	return s.providerRefusal(ctx, t, "update", err)
}

// generateCheckout makes and announces t's checkout URL
// (stripe_service.rb:79-106 generate_checkout_url with send_webhook).
func (s *Service) generateCheckout(ctx context.Context, t *syncTarget) error {
	org := t.cust.OrganizationID
	cur := t.cust.Currency
	skip, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (bool, error) {
		n, err := r.WebhookEndpoints.Count(ctx, org)
		if err != nil {
			return false, wrap(err, "count webhook endpoints")
		}
		if cur.IsZero() {
			be, err := r.BillingEntities.Get(ctx, org, t.cust.BillingEntityID)
			if err != nil {
				return false, wrap(err, "load billing entity %s", t.cust.BillingEntityID)
			}
			cur = be.DefaultCurrency
		}
		return n == 0, nil
	})
	if err != nil || skip {
		return err
	}
	u, err := t.pc.gw.SetupURL(ctx, t.pc.conn, payment.SetupParams{
		CustomerID: t.cust.ID, ProviderCustomerID: providerCustomerID(t.pcust), MethodTypes: t.pcust.ProviderPaymentMethods,
		Customer: payment.CustomerParamsOf(t.cust), Currency: cur,
	})
	if errors.Is(err, payment.ErrUnsupported) || errors.Is(err, payment.ErrNoSetupMethods) {
		return nil
	}
	if err != nil {
		return s.providerRefusal(ctx, t, "checkout url", err)
	}
	payload := CheckoutURLGenerated{
		CustomerID: t.cust.ID, ExternalCustomerID: t.cust.ExternalID, PaymentProvider: t.pc.integ.Provider,
		PaymentProviderCode: t.pc.integ.Code, CheckoutURL: u,
	}
	return kernel.Emit(ctx, s.d, org, webhook.EventTypeCustomerCheckoutURLGenerated, "customer", t.cust.ID.UUID(), payload)
}

// fetchDefaultMethod records t's default method at the provider
// (app/services/payment_providers/stripe/customers/fetch_default_payment_method_service.rb).
func (s *Service) fetchDefaultMethod(ctx context.Context, t *syncTarget) error {
	id := providerCustomerID(t.pcust)
	if id == "" {
		return nil
	}
	methodID, err := t.pc.gw.DefaultPaymentMethod(ctx, t.pc.conn, id)
	if errors.Is(err, payment.ErrUnsupported) {
		return nil
	}
	if err != nil {
		return wrap(err, "fetch default payment method of customer %s", t.cust.ID)
	}
	if methodID == "" {
		return nil
	}
	pm, err := t.pc.gw.PaymentMethod(ctx, t.pc.conn, methodID)
	switch {
	case errors.Is(err, payment.ErrUnsupported):
	case err != nil:
		return wrap(err, "fetch payment method %s", methodID)
	}
	org := t.cust.OrganizationID
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		pcust, err := r.PaymentProviderCustomers.Get(ctx, org, t.pcust.ID)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return wrap(err, "load provider customer %s", t.pcust.ID)
		}
		if pcust.Deleted() {
			return nil
		}
		_, err = s.findOrCreateMethod(ctx, r, pcust, methodID, pm.Details, false)
		return err
	})
}

// RegisterProviderWebhook registers the webhook endpoint of a new payment
// connection at its provider and stores the endpoint and its signing
// secret (lago PaymentProviders::Stripe::RegisterWebhookService): a
// provider refusal is announced by payment_provider.error with the action
// payment_provider.register_webhook; a provider without registration, a
// removed connection or no endpoint URL registers nothing.
func (s *Service) RegisterProviderWebhook(ctx context.Context, org core.OrganizationID, args kernel.IntegrationArgs) error {
	if s.providers == nil {
		return ErrNoProviders
	}
	integ, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*integration.Integration, error) {
		return liveIntegration(ctx, r, org, args.IntegrationID)
	})
	if err != nil || integ == nil || s.providers.WebhookURL == nil {
		return err
	}
	pc, err := s.open(ctx, integ)
	if err != nil {
		return err
	}
	endpoint := s.providers.WebhookURL(pc.conn)
	if endpoint == "" {
		return nil
	}
	up, err := pc.gw.RegisterWebhook(ctx, pc.conn, endpoint)
	if errors.Is(err, payment.ErrUnsupported) {
		return nil
	}
	if err != nil {
		if _, ok := payment.AsProviderError(err); ok && !errors.Is(err, payment.ErrRetryable) {
			return s.emitProviderFailure(ctx, integ, "payment_provider.register_webhook", err)
		}
		return wrap(err, "register webhook of integration %s", integ.ID)
	}
	return s.storeConnectionUpdate(ctx, integ.ID, org, up)
}

// storeConnectionUpdate stores the settings and secrets a provider
// returned for the connection id, the secrets sealed.
func (s *Service) storeConnectionUpdate(ctx context.Context, id core.IntegrationID, org core.OrganizationID, up payment.ConnectionUpdate) error {
	if up.Settings == nil && up.Secrets == nil {
		return nil
	}
	var sealed []byte
	if up.Secrets != nil {
		if s.providers.Secrets == nil {
			return errors.New("payments: no sealer for the connection secrets")
		}
		var err error
		if sealed, err = s.providers.Secrets.Seal(ctx, org, up.Secrets); err != nil {
			return wrap(err, "seal secrets of integration %s", id)
		}
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		integ, err := r.Integrations.Get(ctx, org, id)
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return wrap(err, "load integration %s", id)
		}
		if up.Settings != nil {
			integ.Settings = slices.Clone(up.Settings)
		}
		if up.Secrets != nil {
			integ.Secrets = sealed
		}
		integ.UpdatedAt = s.d.Now()
		if err := r.Integrations.Update(ctx, integ); err != nil {
			return wrap(err, "update integration %s", id)
		}
		return nil
	})
}

// CodeNoPaymentMethodsToSetupAvailable rejects a checkout URL for a
// customer none of whose method types can be saved through a setup page
// (lago stripe_service.rb generate_checkout_url), a
// [core.ValidationError] on "provider_payment_methods".
const CodeNoPaymentMethodsToSetupAvailable = "no_payment_methods_to_setup_available"

// GenerateCheckoutURL returns the page where the customer c saves a
// payment method at its connection integ (lago
// Customers::GenerateCheckoutUrlService, which generates it without the
// customer.checkout_url_generated webhook). The URL carries no expiry: a
// zero time is returned.
func (s *Service) GenerateCheckoutURL(ctx context.Context, c *customer.Customer, integ *integration.Integration, pcust *payment.ProviderCustomer) (string, time.Time, error) {
	if s.providers == nil {
		return "", time.Time{}, ErrNoProviders
	}
	pc, err := s.open(ctx, integ)
	if err != nil {
		return "", time.Time{}, err
	}
	cur := c.Currency
	if cur.IsZero() {
		be, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*organization.BillingEntity, error) {
			return r.BillingEntities.Get(ctx, c.OrganizationID, c.BillingEntityID)
		})
		if err != nil {
			return "", time.Time{}, wrap(err, "load billing entity %s", c.BillingEntityID)
		}
		cur = be.DefaultCurrency
	}
	u, err := pc.gw.SetupURL(ctx, pc.conn, payment.SetupParams{
		CustomerID: c.ID, ProviderCustomerID: providerCustomerID(pcust), MethodTypes: pcust.ProviderPaymentMethods,
		Customer: payment.CustomerParamsOf(c), Currency: cur,
	})
	if errors.Is(err, payment.ErrNoSetupMethods) {
		return "", time.Time{}, &core.ValidationError{Errors: []core.FieldError{{Field: "provider_payment_methods", Code: CodeNoPaymentMethodsToSetupAvailable}}}
	}
	if err != nil {
		if _, ok := payment.AsProviderError(err); ok && !errors.Is(err, payment.ErrRetryable) {
			ref := &customerRef{id: c.ID, externalID: c.ExternalID}
			if eerr := s.emitCustomerProviderError(ctx, integ, ref, providerErrorOf(err)); eerr != nil {
				return "", time.Time{}, eerr
			}
		}
		return "", time.Time{}, wrap(err, "checkout url of customer %s", c.ID)
	}
	return u, time.Time{}, nil
}
