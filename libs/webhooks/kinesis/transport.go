package kinesis

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"time"
)

// Client limits, so that a hanging endpoint cannot hold a worker: the
// connection is opened within [OpenTimeout], the answer starts within
// [ReadTimeout], and a call makes at most [MaxAttempts] attempts whatever
// the environment (AWS_MAX_ATTEMPTS) asks for.
//
// source: lib/lago/kinesis/producer.rb:8-24
const (
	OpenTimeout = 2 * time.Second
	ReadTimeout = 5 * time.Second
	MaxAttempts = 2
)

// maxBody bounds the AWS answers read.
const maxBody = 1 << 20

// NewHTTPClient returns the HTTP client of the producer: [OpenTimeout] to
// connect and complete TLS, [ReadTimeout] to the start of the answer, and
// both together for the whole attempt.
func NewHTTPClient() *http.Client {
	d := &net.Dialer{Timeout: OpenTimeout, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: OpenTimeout + ReadTimeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           d.DialContext,
			TLSHandshakeTimeout:   OpenTimeout,
			ResponseHeaderTimeout: ReadTimeout,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// answer is a received HTTP answer.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// caller sends AWS requests with the standard retry mode.
type caller struct {
	hc    *http.Client
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// call sends the request build returns, up to [MaxAttempts] times while
// decode classifies the answer as a retryable error. build is called per
// attempt, so each attempt is signed afresh. A transport failure is a
// *NetworkError; a cancelled context is returned as is.
func (c caller) call(ctx context.Context, build func(now time.Time) (*http.Request, error), decode func(answer) error) error {
	for attempt := 1; ; attempt++ {
		err := c.attempt(ctx, build, decode)
		if err == nil || attempt >= MaxAttempts || !retryable(err) || ctx.Err() != nil {
			return err
		}
		// Standard mode: full jitter over 2^(attempt-1) seconds, capped
		// at 20 s; with two attempts, under one second.
		if err := c.sleep(ctx, time.Duration(rand.Int64N(int64(time.Second)<<(attempt-1)))); err != nil {
			return err
		}
	}
}

func (c caller) attempt(ctx context.Context, build func(now time.Time) (*http.Request, error), decode func(answer) error) error {
	req, err := build(c.now())
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &NetworkError{Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &NetworkError{Err: err}
	}
	return decode(answer{status: resp.StatusCode, header: resp.Header, body: body})
}

// sleep waits d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// newRequest returns a POST of body to url with the given content type.
func newRequest(url, contentType string, body []byte) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("kinesis: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	return req, nil
}
