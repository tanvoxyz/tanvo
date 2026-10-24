package app

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/providers/gocardless"
)

// gocardlessOAuth connects a GoCardless payment connection by OAuth
// through the installation's partner application (lago
// PaymentProviders::GocardlessService create_or_update with the
// access_code of the OAuth callback): it is the [integration.OAuthGateway]
// of the integrations service's StartOAuth and CompleteOAuth, storing the
// access token in the connection's secrets.
type gocardlessOAuth struct {
	gw     *gocardless.Gateway
	client gocardless.OAuthClient
}

var _ integration.OAuthGateway = gocardlessOAuth{}

// Provider implements [integration.Gateway].
func (g gocardlessOAuth) Provider() integration.Provider { return integration.ProviderGocardless }

// ValidateConnection implements [integration.Gateway] with the payment
// adapter's checks.
func (g gocardlessOAuth) ValidateConnection(conn integration.Connection) error {
	return g.gw.ValidateConnection(paymentConnection(conn))
}

// AuthorizationURL implements [integration.OAuthGateway].
func (g gocardlessOAuth) AuthorizationURL(_ integration.Connection, redirectURI, state string) (string, error) {
	if g.client.ClientID == "" {
		return "", fmt.Errorf("gocardless oauth: no partner application: %w", integration.ErrNotConnected)
	}
	c := g.client
	c.RedirectURI = redirectURI
	return g.gw.AuthorizeURL(c, state), nil
}

// ExchangeCode implements [integration.OAuthGateway]: the token becomes
// the connection's access_token secret.
func (g gocardlessOAuth) ExchangeCode(ctx context.Context, _ integration.Connection, redirectURI, code string) (integration.ConnectionUpdate, error) {
	if g.client.ClientID == "" {
		return integration.ConnectionUpdate{}, fmt.Errorf("gocardless oauth: no partner application: %w", integration.ErrNotConnected)
	}
	c := g.client
	c.RedirectURI = redirectURI
	token, err := g.gw.ExchangeCode(ctx, c, code)
	if err != nil {
		return integration.ConnectionUpdate{}, fmt.Errorf("gocardless oauth: %w", err)
	}
	return integration.ConnectionUpdate{Secrets: map[string]string{"access_token": token}}, nil
}

// paymentConnection is conn as a payment adapter sees it.
func paymentConnection(conn integration.Connection) payment.Connection {
	return payment.Connection{
		ID: conn.ID, OrganizationID: conn.OrganizationID, Code: conn.Code, Provider: conn.Provider,
		Settings: conn.Settings, Secrets: conn.Secrets,
	}
}
