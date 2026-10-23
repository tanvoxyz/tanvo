package webhooks

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// DefaultTimeout bounds one delivery attempt.
const DefaultTimeout = 30 * time.Second

// UserAgent is sent with every delivery.
const UserAgent = "Tanvo-Webhooks/1"

// successCodes are the response statuses that complete a delivery.
var successCodes = []int{http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent}

// Stored responses of attempts that got no HTTP response. The connection
// error itself is not stored: telling a refused port from a timeout would
// make deliveries a port scanner.
const (
	responseBlocked          = "Destination address is not allowed"
	responseConnectionFailed = "Connection failed"
	responseHTTPSRequired    = "HTTPS is required"
)

// ClientConfig configures [NewClient].
type ClientConfig struct {
	// Timeout bounds one attempt, connection to response body; zero uses
	// [DefaultTimeout].
	Timeout time.Duration
	// AllowPrivate disables the address guard and allows plain HTTP, for
	// self-hosted installations whose receivers are on a private network.
	AllowPrivate bool
	// Resolver resolves endpoint hosts; nil uses net.DefaultResolver.
	Resolver Resolver
}

// NewClient returns the HTTP client of deliveries. It does not follow
// redirects (a redirect is a failed attempt) and, unless AllowPrivate,
// connects only to addresses that are not [Blocked] and ignores proxy
// settings, since a proxy would connect on its behalf.
func NewClient(cfg ClientConfig) *http.Client {
	timeout := cmp.Or(cfg.Timeout, DefaultTimeout)
	d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	t := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
	if !cfg.AllowPrivate {
		var r Resolver = net.DefaultResolver
		if cfg.Resolver != nil {
			r = cfg.Resolver
		}
		t.Proxy = nil
		t.DialContext = newGuardedDialer(r, d).DialContext
	}
	return &http.Client{
		Transport: t,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// CheckURL validates an endpoint URL when it is created or changed: an
// absolute URL, HTTPS unless allowPrivate, and, unless allowPrivate, a
// host that does not resolve to a blocked address. A host that does not
// resolve is accepted; the address is checked again at every attempt.
func CheckURL(ctx context.Context, r Resolver, raw string, allowPrivate bool) error {
	if !webhook.ValidURL(raw) {
		return fmt.Errorf("webhook url %q: %s", raw, webhook.CodeInvalidURL)
	}
	if allowPrivate {
		return nil
	}
	u, _ := url.Parse(raw)
	if u.Scheme != "https" {
		return fmt.Errorf("webhook url %q: https is required", raw)
	}
	if r == nil {
		r = net.DefaultResolver
	}
	if _, err := Resolve(ctx, r, u.Hostname()); errors.Is(err, ErrBlockedAddress) {
		return err
	}
	return nil
}

// Attempt is the outcome of one delivery attempt.
type Attempt struct {
	// Succeeded reports a success status.
	Succeeded bool
	// HTTPStatus is the response status, nil without a response.
	HTTPStatus *int
	// Response is the response body or a summary of why there is none.
	Response []byte
}

// Sender makes delivery attempts.
type Sender struct {
	// Client sends the requests; see [NewClient].
	Client *http.Client
	// AllowPrivate allows plain HTTP endpoints; it matches the client's
	// configuration.
	AllowPrivate bool
}

// Send POSTs the delivery's payload to the endpoint, signed with the
// endpoint's secret at now. It returns an error only when ctx ends (the
// worker stops), in which case the attempt does not count.
func (s *Sender) Send(ctx context.Context, e *webhook.Endpoint, d *webhook.Delivery, now time.Time) (Attempt, error) {
	u, err := url.Parse(e.URL)
	if err != nil {
		return Attempt{Response: []byte(responseConnectionFailed)}, nil
	}
	if u.Scheme != "https" && !s.AllowPrivate {
		return Attempt{Response: []byte(responseHTTPSRequired)}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, bytes.NewReader(d.Payload))
	if err != nil {
		return Attempt{Response: []byte(responseConnectionFailed)}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if err := SetHeaders(req.Header, []string{e.Secret}, d.EventID.String(), now, d.Payload); err != nil {
		return Attempt{}, fmt.Errorf("sign webhook %s: %w", d.ID, err)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Attempt{}, fmt.Errorf("send webhook %s: %w", d.ID, ctx.Err())
		}
		if errors.Is(err, ErrBlockedAddress) {
			return Attempt{Response: []byte(responseBlocked)}, nil
		}
		return Attempt{Response: []byte(responseConnectionFailed)}, nil
	}
	defer resp.Body.Close()
	// Read a little past the stored limit so truncation is visible.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, webhook.MaxResponseBytes+4))
	status := resp.StatusCode
	return Attempt{Succeeded: slices.Contains(successCodes, status), HTTPStatus: &status, Response: body}, nil
}
