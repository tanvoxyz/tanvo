package pgtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// errNoDocker marks the absence of a reachable Docker engine.
var errNoDocker = errors.New("no Docker engine reachable")

// container is a PostgreSQL container started through the Docker Engine
// API.
type container struct {
	client *http.Client
	base   string
	id     string
	dsn    string
}

// dockerEndpoint finds the Docker engine: DOCKER_HOST, the current docker
// context, then the well-known sockets. It returns an HTTP client, the base
// URL of the API and the host where published ports are reachable.
func dockerEndpoint(ctx context.Context) (*http.Client, string, string, error) {
	var candidates []string
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		candidates = append(candidates, h)
	}
	if h := dockerContextHost(ctx); h != "" {
		candidates = append(candidates, h)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			"unix://"+filepath.Join(home, ".orbstack", "run", "docker.sock"),
			"unix://"+filepath.Join(home, ".docker", "run", "docker.sock"),
			"unix://"+filepath.Join(home, ".colima", "default", "docker.sock"),
		)
	}
	candidates = append(candidates, "unix:///var/run/docker.sock")
	var errs []error
	for _, h := range candidates {
		client, base, host, err := dockerClient(h)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = ping(pctx, client, base)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", h, err))
			continue
		}
		return client, base, host, nil
	}
	return nil, "", "", fmt.Errorf("%w: %w", errNoDocker, errors.Join(errs...))
}

// dockerContextHost returns the endpoint of the current docker context, when
// the docker CLI is installed.
func dockerContextHost(ctx context.Context) string {
	if _, err := exec.LookPath("docker"); err != nil {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func dockerClient(host string) (*http.Client, string, string, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, "", "", fmt.Errorf("parse docker host %q: %w", host, err)
	}
	switch u.Scheme {
	case "unix":
		path := u.Path
		if _, err := os.Stat(path); err != nil {
			return nil, "", "", fmt.Errorf("docker socket: %w", err)
		}
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}}
		return &http.Client{Transport: tr}, "http://docker", "127.0.0.1", nil
	case "tcp":
		return &http.Client{}, "http://" + u.Host, u.Hostname(), nil
	default:
		return nil, "", "", fmt.Errorf("unsupported docker host %q", host)
	}
}

func ping(ctx context.Context, c *http.Client, base string) error {
	return call(ctx, c, http.MethodGet, base+"/_ping", nil, nil)
}

// call sends a Docker Engine API request and decodes the JSON response into
// out (when not nil).
func call(ctx context.Context, c *http.Client, method, u string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return fmt.Errorf("docker request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("docker %s %s: %w", method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker %s %s: %s: %s", method, req.URL.Path, resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode docker response: %w", err)
	}
	return nil
}

// startContainer starts a throw-away postgres:18 container with durability
// turned off and its data on a tmpfs.
func startContainer(ctx context.Context) (*container, error) {
	client, base, host, err := dockerEndpoint(ctx)
	if err != nil {
		return nil, err
	}
	name, tag, _ := strings.Cut(Image, ":")
	q := url.Values{"fromImage": {name}, "tag": {tag}}
	// Pulling streams progress; reading it to the end waits for the pull.
	if err := call(ctx, client, http.MethodPost, base+"/images/create?"+q.Encode(), nil, nil); err != nil {
		return nil, fmt.Errorf("pull %s: %w", Image, err)
	}
	const password = "postgres"
	spec := map[string]any{
		"Image": Image,
		"Env":   []string{"POSTGRES_PASSWORD=" + password, "POSTGRES_INITDB_ARGS=--no-sync"},
		"Cmd": []string{
			"postgres",
			"-c", "fsync=off",
			"-c", "synchronous_commit=off",
			"-c", "full_page_writes=off",
			"-c", "max_connections=500",
			"-c", "shared_buffers=256MB",
		},
		"ExposedPorts": map[string]any{"5432/tcp": map[string]any{}},
		"Labels":       map[string]string{"dev.tanvo.pgtest": "true"},
		"HostConfig": map[string]any{
			"AutoRemove":   true,
			"PortBindings": map[string]any{"5432/tcp": []map[string]string{{"HostIp": "127.0.0.1", "HostPort": ""}}},
			"Tmpfs":        map[string]string{"/var/lib/postgresql": "rw"},
		},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := call(ctx, client, http.MethodPost, base+"/containers/create?name="+randomName("tanvo-pgtest-"), spec, &created); err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}
	c := &container{client: client, base: base, id: created.ID}
	if err := call(ctx, client, http.MethodPost, base+"/containers/"+c.id+"/start", nil, nil); err != nil {
		_ = c.remove(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("start container: %w", err)
	}
	var info struct {
		NetworkSettings struct {
			Ports map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := call(ctx, client, http.MethodGet, base+"/containers/"+c.id+"/json", nil, &info); err != nil {
		_ = c.remove(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("inspect container: %w", err)
	}
	ports := info.NetworkSettings.Ports["5432/tcp"]
	if len(ports) == 0 || ports[0].HostPort == "" {
		_ = c.remove(context.WithoutCancel(ctx))
		return nil, errors.New("container has no published port")
	}
	c.dsn = fmt.Sprintf("postgres://postgres:%s@%s/postgres?sslmode=disable", password, net.JoinHostPort(host, ports[0].HostPort))
	return c, nil
}

// remove stops and deletes the container.
func (c *container) remove(ctx context.Context) error {
	return call(ctx, c.client, http.MethodDelete, c.base+"/containers/"+c.id+"?force=true&v=true", nil, nil)
}
