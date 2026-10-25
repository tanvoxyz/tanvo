package dockertest

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

// ErrUnavailable is returned when no Docker engine is reachable.
var ErrUnavailable = errors.New("dockertest: no Docker engine reachable")

// Engine is a Docker engine endpoint.
type Engine struct {
	client *http.Client
	base   string
	// host is where published ports are reachable, bindIP the address
	// they are published on.
	host, bindIP string
	owner        owner
}

// owner identifies the test process that starts containers.
type owner struct {
	host string
	pid  int
}

// Host returns the host where the published ports of the engine's
// containers are reachable: 127.0.0.1 for a local engine, the host of
// DOCKER_HOST for a TCP one.
func (e *Engine) Host() string { return e.host }

// Connect finds a reachable engine: DOCKER_HOST, the endpoint of the
// current docker context, then the well-known sockets. The error wraps
// [ErrUnavailable] when none answers.
func Connect(ctx context.Context) (*Engine, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("dockertest: hostname: %w", err)
	}
	var candidates []string
	if h := os.Getenv("DOCKER_HOST"); h != "" {
		candidates = append(candidates, h)
	}
	if h := contextHost(ctx); h != "" {
		candidates = append(candidates, h)
	}
	for _, s := range sockets() {
		candidates = append(candidates, "unix://"+s)
	}
	var errs []error
	seen := map[string]bool{}
	for _, h := range candidates {
		if seen[h] {
			continue
		}
		seen[h] = true
		e, err := newEngine(h)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		e.owner = owner{host: hostname, pid: os.Getpid()}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = e.do(pctx, http.MethodGet, "/_ping", nil, nil)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", h, err))
			continue
		}
		return e, nil
	}
	return nil, fmt.Errorf("%w: %w", ErrUnavailable, errors.Join(errs...))
}

// contextHost returns the endpoint of the Docker CLI's current context,
// when the CLI is installed.
func contextHost(ctx context.Context) string {
	if _, err := exec.LookPath("docker"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sockets lists the Unix sockets of local Docker-compatible engines that
// exist.
func sockets() []string {
	var all []string
	if home, err := os.UserHomeDir(); err == nil {
		all = append(all,
			filepath.Join(home, ".orbstack", "run", "docker.sock"),
			filepath.Join(home, ".docker", "run", "docker.sock"),
			filepath.Join(home, ".colima", "default", "docker.sock"),
			filepath.Join(home, ".rd", "docker.sock"),
			filepath.Join(home, ".local", "share", "containers", "podman", "machine", "podman.sock"),
		)
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		all = append(all, filepath.Join(dir, "docker.sock"), filepath.Join(dir, "podman", "podman.sock"))
	}
	all = append(all, "/var/run/docker.sock")
	var out []string
	for _, s := range all {
		if _, err := os.Stat(s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

func newEngine(host string) (*Engine, error) {
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("dockertest: docker host %q: %w", host, err)
	}
	switch u.Scheme {
	case "unix":
		path := u.Path
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("dockertest: docker socket: %w", err)
		}
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}}
		return &Engine{client: &http.Client{Transport: tr}, base: "http://docker", host: "127.0.0.1", bindIP: "127.0.0.1"}, nil
	case "tcp", "http":
		return &Engine{client: &http.Client{}, base: "http://" + u.Host, host: u.Hostname()}, nil
	default:
		return nil, fmt.Errorf("dockertest: unsupported docker host %q", host)
	}
}

// request sends a Docker Engine API request and returns the response of a
// successful one; the caller closes its body.
func (e *Engine) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("dockertest: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, r)
	if err != nil {
		return nil, fmt.Errorf("dockertest: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dockertest: %s %s: %w", method, req.URL.Path, err)
	}
	if resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &apiError{method: method, path: req.URL.Path, status: resp.StatusCode, message: strings.TrimSpace(string(b))}
	}
	return resp, nil
}

// do sends a request and decodes its JSON response into out (when not
// nil); otherwise it reads the response to its end, which for streams
// such as image pulls is the end of the operation.
func (e *Engine) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := e.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("dockertest: %s %s: %w", method, path, err)
		}
		return nil
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return fmt.Errorf("dockertest: %s %s: %w", method, path, err)
	}
	return nil
}

type apiError struct {
	method, path string
	status       int
	message      string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("dockertest: %s %s: %d %s", e.method, e.path, e.status, e.message)
}

func hasStatus(err error, status ...int) bool {
	var a *apiError
	if !errors.As(err, &a) {
		return false
	}
	for _, s := range status {
		if a.status == s {
			return true
		}
	}
	return false
}
