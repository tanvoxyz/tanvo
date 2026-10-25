package pgtest

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/dockertest"
)

// startContainer starts a throw-away postgres:18 container with durability
// turned off and its data on a tmpfs, and returns it with its superuser
// DSN.
func startContainer(ctx context.Context) (*dockertest.Container, string, error) {
	engine, err := dockertest.Connect(ctx)
	if err != nil {
		return nil, "", err
	}
	const password = "postgres"
	c, err := engine.Run(ctx, dockertest.Spec{
		Image: Image,
		Name:  "pgtest",
		Env:   []string{"POSTGRES_PASSWORD=" + password, "POSTGRES_INITDB_ARGS=--no-sync"},
		Cmd: []string{
			"postgres",
			"-c", "fsync=off",
			"-c", "synchronous_commit=off",
			"-c", "full_page_writes=off",
			"-c", "max_connections=500",
			"-c", "shared_buffers=256MB",
		},
		Ports: []string{"5432/tcp"},
		Tmpfs: map[string]string{"/var/lib/postgresql": "rw"},
	})
	if err != nil {
		return nil, "", fmt.Errorf("start %s: %w", Image, err)
	}
	return c, fmt.Sprintf("postgres://postgres:%s@%s/postgres?sslmode=disable", password, c.Addr("5432/tcp")), nil
}
