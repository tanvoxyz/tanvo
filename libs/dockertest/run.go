package dockertest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Spec describes a container.
type Spec struct {
	// Image is the image reference, as "postgres:18".
	Image string
	// Name names the container tanvo-<Name>-<random> (tanvo-test-<random>
	// when empty) and is the value of its [Label].
	Name string
	Env  []string
	// Cmd replaces the image's command; the image's entrypoint is kept.
	Cmd []string
	// Ports are the container ports to publish, as "5432/tcp".
	Ports []string
	// HostPorts fixes the host ports of some Ports (random otherwise), for
	// servers that advertise their address, as Kafka brokers do.
	HostPorts map[string]string
	// Binds are bind mounts, as "/host/path:/container/path:ro".
	Binds []string
	// Tmpfs mounts a tmpfs at each path, with its mount options ("rw").
	Tmpfs map[string]string
	// Keep keeps the container after its command exits, for
	// [Container.Exit] and [Container.Logs], until [Container.Close] or the
	// end of its lifetime.
	Keep bool
	// Lifetime caps the life of the container; zero means [LifetimeEnv],
	// or [DefaultLifetime] when it is unset.
	Lifetime time.Duration
	// NoShell marks an image without /bin/sh: the lifetime is enforced by
	// a sidecar instead of an entrypoint wrapper. It excludes Keep.
	NoShell bool
}

// Container is a running container.
type Container struct {
	engine  *Engine
	sidecar string
	keep    bool
	// ID is the container ID.
	ID string
	// Host is where the published ports are reachable.
	Host string
	// Ports maps the published container ports to their host ports.
	Ports map[string]string
	// IP is the container's address on its network (the engine's default
	// bridge), where the other containers of the engine reach its
	// unpublished ports; empty when the engine reports none.
	IP string
}

// Addr returns the host:port address of a published container port, as
// "5432/tcp".
func (c *Container) Addr(port string) string {
	return net.JoinHostPort(c.Host, c.Ports[port])
}

// imageConfig is the part of an image's configuration the wrapper needs.
type imageConfig struct {
	Config struct {
		Entrypoint []string
		Cmd        []string
		StopSignal string
	}
}

// Run removes the stale containers of this host ([Engine.Reap]), pulls the
// image when missing and starts a container under its lifetime cap,
// publishing its ports on random host ports (or spec.HostPorts).
func (e *Engine) Run(ctx context.Context, spec Spec) (*Container, error) {
	if spec.NoShell && spec.Keep {
		return nil, errors.New("dockertest: Keep needs an image with a shell")
	}
	life, err := lifetime(spec.Lifetime)
	if err != nil {
		return nil, err
	}
	if _, err := e.Reap(ctx); err != nil {
		return nil, err
	}
	img, err := e.image(ctx, spec.Image)
	if err != nil {
		return nil, err
	}
	labels := e.labels(spec.Name, life)
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for _, p := range spec.Ports {
		exposed[p] = struct{}{}
		bindings[p] = []map[string]string{{"HostIp": e.bindIP, "HostPort": spec.HostPorts[p]}}
	}
	cmd := spec.Cmd
	if cmd == nil {
		cmd = img.Config.Cmd
	}
	body := map[string]any{
		"Image":        spec.Image,
		"Env":          spec.Env,
		"ExposedPorts": exposed,
		"Labels":       labels,
		"HostConfig": map[string]any{
			"AutoRemove":   true,
			"Init":         spec.NoShell,
			"PortBindings": bindings,
			"Binds":        spec.Binds,
			"Tmpfs":        spec.Tmpfs,
		},
	}
	sig := stopSignal(img.Config.StopSignal)
	if spec.NoShell {
		body["Cmd"] = cmd
	} else {
		body["Entrypoint"] = []string{"/bin/sh", "-c", wrapperScript, "tanvo-lifetime"}
		body["Cmd"] = append(lifetimeArgs(life, sig, spec.Keep), append(img.Config.Entrypoint, cmd...)...)
	}
	name := spec.Name
	if name == "" {
		name = "test"
	}
	var created struct{ ID string }
	q := url.Values{"name": {"tanvo-" + name + "-" + randomHex()}}
	if err := e.do(ctx, http.MethodPost, "/containers/create?"+q.Encode(), body, &created); err != nil {
		return nil, err
	}
	c := &Container{engine: e, ID: created.ID, Host: e.host, Ports: map[string]string{}, keep: spec.Keep}
	fail := func(err error) (*Container, error) {
		_ = c.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	if err := e.do(ctx, http.MethodPost, "/containers/"+c.ID+"/start", nil, nil); err != nil {
		return fail(err)
	}
	if spec.NoShell {
		if c.sidecar, err = e.startSidecar(ctx, c.ID, labels, life, sig); err != nil {
			return fail(err)
		}
	}
	var inspect struct {
		NetworkSettings struct {
			Ports    map[string][]struct{ HostIP, HostPort string }
			Networks map[string]struct{ IPAddress string }
		}
	}
	if err := e.do(ctx, http.MethodGet, "/containers/"+c.ID+"/json", nil, &inspect); err != nil {
		return fail(err)
	}
	for _, p := range spec.Ports {
		b := inspect.NetworkSettings.Ports[p]
		if len(b) == 0 || b[0].HostPort == "" {
			return fail(fmt.Errorf("dockertest: port %s of %s not published", p, spec.Image))
		}
		c.Ports[p] = b[0].HostPort
	}
	for _, name := range slices.Sorted(maps.Keys(inspect.NetworkSettings.Networks)) {
		if ip := inspect.NetworkSettings.Networks[name].IPAddress; ip != "" {
			c.IP = ip
			break
		}
	}
	return c, nil
}

// image returns the configuration of an image, pulling it when missing; an
// image already present is used as is, so tests run offline.
func (e *Engine) image(ctx context.Context, ref string) (*imageConfig, error) {
	var img imageConfig
	err := e.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, &img)
	if err == nil {
		return &img, nil
	}
	if !hasStatus(err, http.StatusNotFound) {
		return nil, err
	}
	name, tag := ref, ""
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		name, tag = ref[:i], ref[i+1:]
	}
	q := url.Values{"fromImage": {name}, "tag": {tag}}
	resp, err := e.request(ctx, http.MethodPost, "/images/create?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("dockertest: pull %s: %w", ref, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The pull streams its progress as JSON messages and reports a failure
	// in one of them; it ends when the pull does.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct{ Error string }
		if err := dec.Decode(&msg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("dockertest: pull %s: %w", ref, err)
		}
		if msg.Error != "" {
			return nil, fmt.Errorf("dockertest: pull %s: %s", ref, msg.Error)
		}
	}
	if err := e.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, &img); err != nil {
		return nil, err
	}
	return &img, nil
}

// Exit waits for the command of a kept container ([Spec.Keep]) to exit and
// returns its exit code.
func (c *Container) Exit(ctx context.Context) (int, error) {
	if !c.keep {
		return 0, errors.New("dockertest: Exit needs a kept container (Spec.Keep)")
	}
	resp, err := c.engine.request(ctx, http.MethodGet, "/containers/"+c.ID+"/logs?follow=true&stdout=false&stderr=true", nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	var code int
	err = frames(resp.Body, func(_ byte, b []byte) bool {
		n, ok := exitStatus(b)
		code = n
		return !ok
	})
	if errors.Is(err, errStop) {
		return code, nil
	}
	if err != nil {
		return 0, fmt.Errorf("dockertest: logs: %w", err)
	}
	return 0, errors.New("dockertest: the container stopped before its command exited")
}

// Logs returns the standard output and error of the container's command,
// in order.
func (c *Container) Logs(ctx context.Context) (string, error) {
	resp, err := c.engine.request(ctx, http.MethodGet, "/containers/"+c.ID+"/logs?stdout=true&stderr=true", nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out strings.Builder
	err = frames(resp.Body, func(stream byte, b []byte) bool {
		if _, ok := exitStatus(b); !ok || stream != 2 {
			out.Write(b)
		}
		return true
	})
	if err != nil {
		return "", fmt.Errorf("dockertest: logs: %w", err)
	}
	return out.String(), nil
}

var errStop = errors.New("stop")

// frames reads a multiplexed log stream: without a TTY each frame is a
// stream byte (1 standard output, 2 standard error), three zero bytes, a
// big-endian length and the payload. It returns errStop when f returns
// false and nil at the end of the stream.
func frames(r io.Reader, f func(stream byte, b []byte) bool) error {
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		n := int(hdr[4])<<24 | int(hdr[5])<<16 | int(hdr[6])<<8 | int(hdr[7])
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			return err
		}
		if !f(hdr[0], b) {
			return errStop
		}
	}
}

// Close removes the container (and its sidecar).
func (c *Container) Close(ctx context.Context) error {
	err := c.engine.remove(ctx, c.ID)
	if c.sidecar != "" {
		err = errors.Join(err, c.engine.remove(ctx, c.sidecar))
	}
	return err
}

// remove force-removes a container with its volumes; a container already
// gone or being removed is not an error.
func (e *Engine) remove(ctx context.Context, id string) error {
	err := e.do(ctx, http.MethodDelete, "/containers/"+id+"?force=true&v=true", nil, nil)
	if hasStatus(err, http.StatusNotFound, http.StatusConflict) {
		return nil
	}
	return err
}

func randomHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// exitStatus parses the line the wrapper writes when the command of a kept
// container exits.
func exitStatus(b []byte) (int, bool) {
	s, ok := strings.CutPrefix(string(b), exitPrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil
}
