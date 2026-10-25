package pgtest

import (
	"context"
	"fmt"
)

// Standalone starts a PostgreSQL server outside of a test, for tools such
// as migrate-diff: it uses [DSNEnv] when set and a container otherwise. It
// returns the superuser DSN and a function that removes the container.
func Standalone(ctx context.Context) (dsn string, stop func(context.Context) error, err error) {
	s, err := startServer(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("pgtest: %w", err)
	}
	stop = func(ctx context.Context) error {
		if s.container == nil {
			return nil
		}
		return s.container.Close(ctx)
	}
	return s.adminDSN, stop, nil
}

// CreateDatabase creates an empty database on the server of the superuser
// DSN dsn and returns its DSN.
func CreateDatabase(ctx context.Context, dsn string) (string, error) {
	s := &server{adminDSN: dsn}
	conn, err := s.admin(ctx, "")
	if err != nil {
		return "", fmt.Errorf("pgtest: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	name := randomName("dev_")
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return "", fmt.Errorf("pgtest: create database: %w", err)
	}
	out, err := withDatabase(dsn, name, "", "")
	if err != nil {
		return "", fmt.Errorf("pgtest: %w", err)
	}
	return out, nil
}
