// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package license

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	// CheckInterval is how often a [Checker] re-verifies its key.
	CheckInterval = 24 * time.Hour
	// ExpiryWarning is how long before the expiry a [Checker] starts
	// warning, at each check, that the license is about to expire.
	ExpiryWarning = 30 * 24 * time.Hour
)

// Checker holds the license state of one service: it verifies the key at
// start-up, then again every [CheckInterval] and at the license's expiry. It
// is safe for concurrent use; a service consults [Checker.Valid] or
// [Checker.Err] before serving a request.
type Checker struct {
	verifier *Verifier
	key      string
	service  string
	logger   *slog.Logger

	mu      sync.RWMutex
	started bool
	lic     License
	err     error
}

// NewChecker returns a checker of key for service. A nil logger discards
// the log. The checker schedules its checks with the runtime's timers, so v
// should read the time from time.Now (tests run in a testing/synctest
// bubble, whose clock is fake).
func NewChecker(v *Verifier, key, service string, logger *slog.Logger) *Checker {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Checker{
		verifier: v,
		key:      key,
		service:  service,
		logger:   logger.With(slog.String("service", service)),
		err:      ErrNotChecked,
	}
}

// Start verifies the key and returns its error when it is invalid or does
// not enable the service; the service should then refuse to start. On
// success it re-verifies the key in the background, every [CheckInterval]
// and at the expiry, until ctx is done or the license expires. Start may be
// called once.
func (c *Checker) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("license: checker already started")
	}
	c.started = true
	c.mu.Unlock()

	if err := c.check(ctx); err != nil {
		return err
	}
	lic := c.License()
	c.logger.InfoContext(ctx, "license verified",
		slog.String("license_id", lic.ID),
		slog.String("licensee", lic.Licensee),
		slog.Time("expires_at", lic.ExpiresAt))
	go c.run(ctx)
	return nil
}

// Valid reports whether the last check found a license that is in force and
// enables the service.
func (c *Checker) Valid() bool {
	return c.Err() == nil
}

// Err returns the error of the last check: nil when the license is valid,
// [ErrNotChecked] before [Checker.Start], otherwise the reason it is not.
func (c *Checker) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

// License returns the license found by the last check whose signature
// verified, or a zero License.
func (c *Checker) License() License {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lic
}

// run re-verifies the key until ctx is done or the license is no longer
// valid. A key does not change, so once it fails it fails for good.
func (c *Checker) run(ctx context.Context) {
	for {
		now := c.verifier.Now()
		next := now.Add(CheckInterval)
		if exp := c.License().ExpiresAt; exp.Before(next) {
			next = exp
		}
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := c.check(ctx); err != nil {
			return
		}
	}
}

// check verifies the key, records the outcome and logs a failure or an
// approaching expiry.
func (c *Checker) check(ctx context.Context) error {
	lic, err := c.verifier.Verify(c.key)
	if err == nil && !lic.Allows(c.service) {
		err = fmt.Errorf("%w: license %s does not enable %q", ErrServiceNotAllowed, lic.ID, c.service)
	}
	c.mu.Lock()
	c.lic, c.err = lic, err
	c.mu.Unlock()

	if err != nil {
		c.logger.ErrorContext(ctx, "license invalid", slog.String("license_id", lic.ID), slog.Any("error", err))
		return err
	}
	if left := lic.ExpiresAt.Sub(c.verifier.Now()); left <= ExpiryWarning {
		c.logger.WarnContext(ctx, "license expires soon",
			slog.String("license_id", lic.ID),
			slog.Time("expires_at", lic.ExpiresAt),
			slog.Duration("remaining", left))
	}
	return nil
}
