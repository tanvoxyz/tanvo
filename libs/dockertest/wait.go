package dockertest

import (
	"context"
	"fmt"
	"net"
	"time"
)

// Wait calls ready until it succeeds or the timeout passes.
func Wait(ctx context.Context, timeout time.Duration, ready func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		err := ready(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("dockertest: not ready: %w", err)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// FreePort returns a TCP port free on 127.0.0.1 at the time of the call.
func FreePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("dockertest: free port: %w", err)
	}
	defer func() { _ = l.Close() }()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return "", fmt.Errorf("dockertest: free port: %w", err)
	}
	return port, nil
}
