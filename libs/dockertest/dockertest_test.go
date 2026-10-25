package dockertest

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStopSignal(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", "TERM"},
		{"SIGINT", "INT"},
		{"sigquit", "QUIT"},
		{"9", "9"},
	} {
		if got := stopSignal(tt.in); got != tt.want {
			t.Errorf("stopSignal(%q) got = %q, want = %q", tt.in, got, tt.want)
		}
	}
}

func TestLifetime(t *testing.T) {
	for _, tt := range []struct {
		name, env string
		spec      time.Duration
		want      time.Duration
		wantErr   bool
	}{
		{name: "default", want: DefaultLifetime},
		{name: "env", env: "30m", want: 30 * time.Minute},
		{name: "spec wins", env: "30m", spec: time.Minute, want: time.Minute},
		{name: "invalid", env: "soon", wantErr: true},
		{name: "zero", env: "0s", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(LifetimeEnv, tt.env)
			got, err := lifetime(tt.spec)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("lifetime(%v) got = %v, %v, want = %v, error %v", tt.spec, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestStale(t *testing.T) {
	e := &Engine{owner: owner{host: "h", pid: 100}}
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	gone := deadPID(t)
	for _, tt := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{name: "own", labels: map[string]string{LabelPID: "100", LabelExpires: future}},
		{name: "live owner", labels: map[string]string{LabelPID: "1", LabelExpires: future}},
		{name: "owner gone", labels: map[string]string{LabelPID: strconv.Itoa(gone), LabelExpires: future}, want: true},
		{name: "expired", labels: map[string]string{LabelPID: "100", LabelExpires: past}, want: true},
		{name: "no owner", labels: map[string]string{}},
	} {
		if got := e.stale(tt.labels) != ""; got != tt.want {
			t.Errorf("%s: stale got = %v, want = %v", tt.name, got, tt.want)
		}
	}
}

// deadPID returns the ID of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func engine(t *testing.T) *Engine {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped with -short")
	}
	e, err := Connect(t.Context())
	if errors.Is(err, ErrUnavailable) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func run(t *testing.T, e *Engine, spec Spec) *Container {
	t.Helper()
	c, err := e.Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// gone waits until the container id no longer exists.
func gone(t *testing.T, e *Engine, id string, timeout time.Duration) {
	t.Helper()
	err := Wait(t.Context(), timeout, func(ctx context.Context) error {
		err := e.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil)
		if hasStatus(err, http.StatusNotFound) {
			return nil
		}
		if err == nil {
			return errors.New("still exists")
		}
		return err
	})
	if err != nil {
		t.Fatalf("container %s: %v", id[:12], err)
	}
}

func TestRunKeep(t *testing.T) {
	e := engine(t)
	c := run(t, e, Spec{Image: SidecarImage, Cmd: []string{"sh", "-c", "echo out; echo err >&2; exit 3"}, Keep: true})
	code, err := c.Exit(t.Context())
	if err != nil || code != 3 {
		t.Fatalf("Exit() got = %d, %v, want = 3", code, err)
	}
	out, err := c.Logs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "out\n") || !strings.Contains(out, "err\n") || strings.Contains(out, exitPrefix) {
		t.Errorf("Logs() got = %q, want = out and err, without the exit status line", out)
	}
	var inspect struct {
		Config struct{ Labels map[string]string }
	}
	if err := e.do(t.Context(), http.MethodGet, "/containers/"+c.ID+"/json", nil, &inspect); err != nil {
		t.Fatalf("a kept container got = %v, want = still there", err)
	}
	if l := inspect.Config.Labels; l[Label] != "test" || l[LabelHost] != e.owner.host || l[LabelPID] != strconv.Itoa(e.owner.pid) {
		t.Errorf("labels got = %v, want = the owner's", l)
	}
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	gone(t, e, c.ID, 30*time.Second)
}

func TestRunPorts(t *testing.T) {
	e := engine(t)
	port, err := FreePort()
	if err != nil {
		t.Fatal(err)
	}
	c := run(t, e, Spec{
		Image:     SidecarImage,
		Cmd:       []string{"httpd", "-f", "-p", "80", "-h", "/srv"},
		Ports:     []string{"80/tcp", "81/tcp"},
		HostPorts: map[string]string{"81/tcp": port},
		Tmpfs:     map[string]string{"/srv": "rw"},
	})
	if c.Ports["81/tcp"] != port || c.Ports["80/tcp"] == "" {
		t.Errorf("Ports got = %v, want = 81/tcp on %s and 80/tcp on a random port", c.Ports, port)
	}
	if err := Wait(t.Context(), 30*time.Second, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.Addr("80/tcp")+"/", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestLifetimeCap checks that a container is removed at the end of its
// lifetime, with or without a shell in the image, and that a command
// ignoring its stop signal is killed after the grace period.
func TestLifetimeCap(t *testing.T) {
	e := engine(t)
	for _, tt := range []struct {
		name    string
		spec    Spec
		timeout time.Duration
	}{
		{name: "wrapper", spec: Spec{Cmd: []string{"sleep", "3600"}}, timeout: 30 * time.Second},
		{name: "sidecar", spec: Spec{Cmd: []string{"sleep", "3600"}, NoShell: true}, timeout: 30 * time.Second},
		{name: "kept", spec: Spec{Cmd: []string{"true"}, Keep: true}, timeout: 30 * time.Second},
		{name: "ignores TERM", spec: Spec{Cmd: []string{"sh", "-c", `trap "" TERM; sleep 3600`}}, timeout: Grace + 30*time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.spec.Image, tt.spec.Lifetime = SidecarImage, 2*time.Second
			c := run(t, e, tt.spec)
			gone(t, e, c.ID, tt.timeout)
			if c.sidecar != "" {
				gone(t, e, c.sidecar, tt.timeout)
			}
		})
	}
}

// TestReap checks that a container whose owner process is gone is removed
// before the next container starts.
func TestReap(t *testing.T) {
	e := engine(t)
	orphan := *e
	orphan.owner.pid = deadPID(t)
	c := run(t, &orphan, Spec{Image: SidecarImage, Cmd: []string{"sleep", "3600"}})
	live := run(t, e, Spec{Image: SidecarImage, Cmd: []string{"sleep", "3600"}})
	gone(t, e, c.ID, 30*time.Second)
	if err := e.do(t.Context(), http.MethodGet, "/containers/"+live.ID+"/json", nil, nil); err != nil {
		t.Errorf("the live owner's container got = %v, want = still there", err)
	}
}
