// Package dockertest starts throwaway containers for integration tests on
// the local Docker engine through the Docker Engine API, without a client
// library.
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
}

// sockets lists the known Unix sockets of local Docker engines.
func sockets() []string {
	var out []string
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out,
			filepath.Join(home, ".orbstack/run/docker.sock"),
			filepath.Join(home, ".docker/run/docker.sock"),
			filepath.Join(home, ".colima/default/docker.sock"),
			filepath.Join(home, ".rd/docker.sock"),
		)
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		out = append(out, filepath.Join(dir, "docker.sock"))
	}
	return append(out, "/var/run/docker.sock")
}

func unixEngine(path string) *Engine {
	return &Engine{
		client: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		}},
		base: "http://docker",
	}
}

// Connect finds a reachable engine: DOCKER_HOST (unix:// or tcp://), then
// the known sockets.
func Connect(ctx context.Context) (*Engine, error) {
	var candidates []*Engine
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		u, err := url.Parse(host)
		if err != nil {
			return nil, fmt.Errorf("dockertest: DOCKER_HOST: %w", err)
		}
		switch u.Scheme {
		case "unix":
			candidates = append(candidates, unixEngine(u.Path))
		case "tcp", "http":
			candidates = append(candidates, &Engine{client: http.DefaultClient, base: "http://" + u.Host})
		}
	}
	for _, s := range sockets() {
		if _, err := os.Stat(s); err == nil {
			candidates = append(candidates, unixEngine(s))
		}
	}
	for _, e := range candidates {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := e.do(ctx, http.MethodGet, "/_ping", nil, nil)
		cancel()
		if err == nil {
			return e, nil
		}
	}
	return nil, ErrUnavailable
}

func (e *Engine) do(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("dockertest: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, r)
	if err != nil {
		return fmt.Errorf("dockertest: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("dockertest: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return &apiError{status: resp.StatusCode, message: strings.TrimSpace(string(b))}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("dockertest: %s %s: %w", method, path, err)
		}
		return nil
	}
	// Streams (image pulls) end when the operation does.
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("dockertest: engine: %d %s", e.status, e.message)
}

func isNotFound(err error) bool {
	var a *apiError
	return errors.As(err, &a) && a.status == http.StatusNotFound
}

// Spec describes a container.
type Spec struct {
	Image string
	Env   []string
	Cmd   []string
	// Ports are the container ports to publish on 127.0.0.1, as "5432/tcp".
	Ports []string
}

// Container is a running container.
type Container struct {
	engine *Engine
	ID     string
	// Ports maps the published container ports to their host ports.
	Ports map[string]string
}

// Run pulls the image when missing and starts a container, publishing its
// ports on random host ports.
func (e *Engine) Run(ctx context.Context, spec Spec) (*Container, error) {
	if err := e.do(ctx, http.MethodGet, "/images/"+spec.Image+"/json", nil, &struct{}{}); err != nil {
		if !isNotFound(err) {
			return nil, err
		}
		image, tag, _ := strings.Cut(spec.Image, ":")
		q := url.Values{"fromImage": {image}, "tag": {tag}}
		if err := e.do(ctx, http.MethodPost, "/images/create?"+q.Encode(), nil, nil); err != nil {
			return nil, err
		}
	}
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for _, p := range spec.Ports {
		exposed[p] = struct{}{}
		bindings[p] = []map[string]string{{"HostIp": "127.0.0.1", "HostPort": ""}}
	}
	var created struct{ ID string }
	err := e.do(ctx, http.MethodPost, "/containers/create", map[string]any{
		"Image":        spec.Image,
		"Env":          spec.Env,
		"Cmd":          spec.Cmd,
		"ExposedPorts": exposed,
		"Labels":       map[string]string{"xyz.tanvo.test": "jobs"},
		"HostConfig": map[string]any{
			"PortBindings": bindings,
			"AutoRemove":   true,
		},
	}, &created)
	if err != nil {
		return nil, err
	}
	c := &Container{engine: e, ID: created.ID, Ports: map[string]string{}}
	if err := e.do(ctx, http.MethodPost, "/containers/"+c.ID+"/start", nil, nil); err != nil {
		_ = c.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	var inspect struct {
		NetworkSettings struct {
			Ports map[string][]struct{ HostIP, HostPort string }
		}
	}
	if err := e.do(ctx, http.MethodGet, "/containers/"+c.ID+"/json", nil, &inspect); err != nil {
		_ = c.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	for _, p := range spec.Ports {
		if b := inspect.NetworkSettings.Ports[p]; len(b) > 0 {
			c.Ports[p] = b[0].HostPort
		}
	}
	return c, nil
}

// Close removes the container.
func (c *Container) Close(ctx context.Context) error {
	err := c.engine.do(ctx, http.MethodDelete, "/containers/"+c.ID+"?force=true&v=true", nil, nil)
	if isNotFound(err) {
		return nil
	}
	return err
}

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
