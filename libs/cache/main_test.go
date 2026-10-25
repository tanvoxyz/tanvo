package cache_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/dockertest"
)

// Images of the server tests, pinned by tag.
const (
	valkeyImage = "valkey/valkey:8.1.10"
	redisImage  = "redis:7.4.11"
)

// servers holds the URLs of the servers of the integration tests:
// TANVO_TEST_VALKEY_URL and TANVO_TEST_REDIS_URL, or containers on the
// local Docker engine. skip says why they are missing.
var servers struct {
	valkey, redis string
	engine        *dockertest.Engine
	skip          string
}

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	servers.valkey = os.Getenv("TANVO_TEST_VALKEY_URL")
	servers.redis = os.Getenv("TANVO_TEST_REDIS_URL")
	if testing.Short() {
		servers.skip = "integration test skipped with -short"
		return m.Run()
	}
	if servers.valkey != "" && servers.redis != "" {
		return m.Run()
	}
	engine, err := dockertest.Connect(ctx)
	if err != nil {
		servers.skip = err.Error()
		return m.Run()
	}
	servers.engine = engine
	for _, s := range []struct {
		url   *string
		image string
		cmd   []string
	}{
		{&servers.valkey, valkeyImage, []string{"valkey-server", "--save", "", "--appendonly", "no"}},
		{&servers.redis, redisImage, []string{"redis-server", "--save", "", "--appendonly", "no"}},
	} {
		if *s.url != "" {
			continue
		}
		c, err := startServer(ctx, s.image, s.cmd, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer func() { _ = c.Close(context.Background()) }()
		*s.url = "redis://" + c.Addr("6379/tcp")
	}
	return m.Run()
}

// startServer starts a server container and waits until it answers.
func startServer(ctx context.Context, image string, cmd []string, hostPorts map[string]string) (*dockertest.Container, error) {
	c, err := servers.engine.Run(ctx, dockertest.Spec{
		Name:      "cache",
		Image:     image,
		Cmd:       cmd,
		Ports:     []string{"6379/tcp"},
		HostPorts: hostPorts,
		Tmpfs:     map[string]string{"/data": "rw"},
	})
	if err != nil {
		return nil, err
	}
	if err := dockertest.Wait(ctx, time.Minute, func(ctx context.Context) error { return ping(ctx, c.Addr("6379/tcp")) }); err != nil {
		_ = c.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	return c, nil
}
