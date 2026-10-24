package gocardless

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/oauth2"

	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// GoCardless Connect OAuth servers (lago GocardlessProvider.auth_site).
const (
	LiveOAuthURL    = "https://connect.gocardless.com"
	SandboxOAuthURL = "https://connect-sandbox.gocardless.com"

	// oauthScope is the access a connection asks for.
	oauthScope = "read_write"
)

// OAuthClient is the Tanvo deployment's GoCardless Connect application
// (lago GOCARDLESS_CLIENT_ID and GOCARDLESS_CLIENT_SECRET).
type OAuthClient struct {
	// ClientID and ClientSecret identify the application.
	ClientID, ClientSecret string
	// RedirectURI is the callback apps/server serves, registered with the
	// application; lago sends its OAuth proxy's (LAGO_OAUTH_PROXY_URL).
	RedirectURI string
	// Sandbox selects the sandbox environment. Lago picks it from the
	// Rails environment (gocardless_provider.rb:16-22).
	Sandbox bool
}

func (g *Gateway) oauthConfig(c OAuthClient) *oauth2.Config {
	base := LiveOAuthURL
	if c.Sandbox {
		base = SandboxOAuthURL
	}
	if g.oauthURL != "" {
		base = g.oauthURL
	}
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  c.RedirectURI,
		Scopes:       []string{oauthScope},
		Endpoint: oauth2.Endpoint{
			AuthURL:  base + "/oauth/authorize",
			TokenURL: base + "/oauth/access_token",
			// Lago sends the client credentials in the body
			// (gocardless_service.rb oauth auth_scheme: :request_body).
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// AuthorizeURL returns the page where a merchant grants Tanvo access to
// its GoCardless account; state is echoed to the callback.
func (g *Gateway) AuthorizeURL(c OAuthClient, state string) string {
	return g.oauthConfig(c).AuthCodeURL(state)
}

// ExchangeCode trades the authorization code of the OAuth callback for
// the connection's access token (lago gocardless_service.rb
// create_or_update: oauth.auth_code.get_token). A refused code is a
// [payment.ProviderError] of [payment.KindInvalidRequest] with the OAuth
// error code; lago reports it as internal_error with its description.
func (g *Gateway) ExchangeCode(ctx context.Context, c OAuthClient, code string) (string, error) {
	ctx, span := g.tracer.Start(ctx, "gocardless.exchange_code",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("payment.provider", string(Provider))))
	defer span.End()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.http)
	tok, err := g.oauthConfig(c).Exchange(ctx, code)
	if err != nil {
		err = classifyOAuth(err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "exchange_code")
		if pe, ok := payment.AsProviderError(err); ok {
			span.SetAttributes(attribute.String("payment.error.kind", string(pe.Kind)), attribute.String("payment.error.code", pe.Code))
		}
		return "", fmt.Errorf("gocardless exchange code: %w", err)
	}
	return tok.AccessToken, nil
}

// classifyOAuth classifies a failed token exchange: 4xx answers →
// invalid_request, 429 → rate_limited, 5xx and transport failures →
// unavailable.
func classifyOAuth(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	pe := &payment.ProviderError{Provider: string(Provider), Kind: payment.KindUnavailable, Message: err.Error()}
	re, ok := errors.AsType[*oauth2.RetrieveError](err)
	if !ok {
		if !isTransport(err) {
			pe.Kind = payment.KindPermanent
		}
		return pe
	}
	pe.Code = re.ErrorCode
	if re.ErrorDescription != "" {
		pe.Message = re.ErrorDescription
	}
	if re.Response != nil {
		pe.HTTPStatus = re.Response.StatusCode
	}
	switch s := pe.HTTPStatus; {
	case s == http.StatusTooManyRequests:
		pe.Kind = payment.KindRateLimited
	case s >= 500 || s == 0:
		pe.Kind = payment.KindUnavailable
	case s >= 400:
		pe.Kind = payment.KindInvalidRequest
	default:
		pe.Kind = payment.KindPermanent
	}
	return pe
}
