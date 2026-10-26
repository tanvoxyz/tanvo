// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package license_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tanvoxyz/tanvo/ee/license"
)

// logBuffer is a log destination safe for concurrent use.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) count(msg string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), "msg=\""+msg+"\"")
}

// newChecker returns a checker of a license signed for the services, valid
// from now for lifetime, with its log.
func newChecker(t *testing.T, service string, services []string, lifetime time.Duration) (*license.Checker, *logBuffer) {
	t.Helper()
	pub, priv := testKey(1)
	l := testLicense()
	l.Services = services
	l.IssuedAt = time.Now()
	l.ExpiresAt = l.IssuedAt.Add(lifetime)
	key := mustSign(t, priv, l)
	logs := &logBuffer{}
	v := license.NewVerifier(map[string]ed25519.PublicKey{"test-1": pub}, nil)
	return license.NewChecker(v, key, service, slog.New(slog.NewTextHandler(logs, nil))), logs
}

func checkState(t *testing.T, step string, c *license.Checker, wantErr error) {
	t.Helper()
	err := c.Err()
	if !errors.Is(err, wantErr) || (wantErr == nil) != (err == nil) {
		t.Errorf("%s: Err() = %v, want %v", step, err, wantErr)
	}
	if got, want := c.Valid(), wantErr == nil; got != want {
		t.Errorf("%s: Valid() = %v, want %v", step, got, want)
	}
}

func TestCheckerRechecksDailyAndAtExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const lifetime = 32*24*time.Hour + 6*time.Hour
		c, logs := newChecker(t, license.ServiceAnalytics, []string{license.ServiceAnalytics}, lifetime)
		checkState(t, "before Start", c, license.ErrNotChecked)
		if err := c.Start(t.Context()); err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		checkState(t, "after Start", c, nil)
		if got := c.License().ID; got != "lic-1" {
			t.Errorf("License().ID = %q, want %q", got, "lic-1")
		}

		// Days 1 and 2: more than ExpiryWarning left, no warning.
		for day := 1; day <= 2; day++ {
			time.Sleep(24 * time.Hour)
			synctest.Wait()
			checkState(t, "day check", c, nil)
		}
		if got := logs.count("license expires soon"); got != 0 {
			t.Errorf("warnings after day 2 = %d, want 0", got)
		}
		// Day 3 leaves 29 days and 6 hours: the check warns.
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		if got := logs.count("license expires soon"); got != 1 {
			t.Errorf("warnings after day 3 = %d, want 1", got)
		}

		// The last daily check is at day 32; the next one is at the expiry,
		// six hours later, not at day 33.
		time.Sleep(29*24*time.Hour + 6*time.Hour - time.Second)
		synctest.Wait()
		checkState(t, "just before expiry", c, nil)
		time.Sleep(time.Second)
		synctest.Wait()
		checkState(t, "at expiry", c, license.ErrExpired)
		if got := logs.count("license invalid"); got != 1 {
			t.Errorf("errors logged at expiry = %d, want 1", got)
		}
		// The checker has stopped: the bubble ends without cancelling ctx.
	})
}

func TestCheckerStopsWithContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, _ := newChecker(t, license.ServiceMCP, []string{license.AllServices}, 365*24*time.Hour)
		ctx, cancel := context.WithCancel(t.Context())
		if err := c.Start(ctx); err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		cancel()
		synctest.Wait() // The background goroutine must have exited.
		checkState(t, "after cancel", c, nil)
		if err := c.Start(t.Context()); err == nil {
			t.Errorf("second Start() error = nil, want an error")
		}
	})
}

func TestCheckerStartFails(t *testing.T) {
	tests := []struct {
		name     string
		service  string
		services []string
		sleep    time.Duration
		wantErr  error
	}{
		{name: "service not enabled", service: license.ServiceAssistant, services: []string{license.ServiceAnalytics}, wantErr: license.ErrServiceNotAllowed},
		{name: "expired", service: license.ServiceAnalytics, services: []string{license.ServiceAnalytics}, sleep: 48 * time.Hour, wantErr: license.ErrExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, logs := newChecker(t, tt.service, tt.services, 24*time.Hour)
				time.Sleep(tt.sleep)
				if err := c.Start(t.Context()); !errors.Is(err, tt.wantErr) {
					t.Fatalf("Start() error = %v, want %v", err, tt.wantErr)
				}
				checkState(t, "after Start", c, tt.wantErr)
				if got := logs.count("license invalid"); got != 1 {
					t.Errorf("errors logged = %d, want 1", got)
				}
			})
		})
	}
}

func TestCheckerMalformedKey(t *testing.T) {
	c := license.NewChecker(license.NewVerifier(license.TrustedKeys(), nil), "garbage", license.ServiceAnalytics, nil)
	if err := c.Start(t.Context()); !errors.Is(err, license.ErrMalformed) {
		t.Fatalf("Start() error = %v, want %v", err, license.ErrMalformed)
	}
	checkState(t, "after Start", c, license.ErrMalformed)
}
