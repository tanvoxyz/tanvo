package app

import (
	"net/http"

	"github.com/tanvoxyz/tanvo/libs/auth"
	"github.com/tanvoxyz/tanvo/libs/webhooks"
)

// newOIDCClient returns the HTTP client of the OpenID Connect providers:
// the guarded client of libs/webhooks, which connects only to globally
// reachable addresses (resolving the host itself, so DNS rebinding cannot
// reach a private one), ignores proxies and does not follow redirects,
// unless allowPrivate. libs/auth bounds its responses.
func newOIDCClient(allowPrivate bool) *http.Client {
	return webhooks.NewClient(webhooks.ClientConfig{Timeout: auth.DefaultProviderTimeout, AllowPrivate: allowPrivate})
}
