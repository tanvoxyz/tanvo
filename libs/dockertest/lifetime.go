package dockertest

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// LifetimeEnv names the environment variable that caps the lifetime of
	// every container, as a Go duration ("2h", "30m").
	LifetimeEnv = "TANVO_TEST_CONTAINER_LIFETIME"
	// DefaultLifetime is the lifetime cap when [LifetimeEnv] is unset.
	DefaultLifetime = 2 * time.Hour
	// Grace is the time a container has to stop after its stop signal
	// before it is killed.
	Grace = 10 * time.Second
	// SidecarImage is the image of the sidecar that enforces the lifetime
	// of an image without a shell.
	SidecarImage = "busybox:1.37.0"
)

// exitPrefix starts the line the wrapper writes to standard error when the
// command of a kept container exits.
const exitPrefix = "tanvo-dockertest: exit status "

// wrapperScript runs the image's entrypoint and command ("$@" after four
// arguments: the lifetime and grace in seconds, the stop signal and the
// keep flag) as a child of the shell, PID 1. It forwards the usual signals
// to the child. When the lifetime is over it sends the stop signal to a
// child still running and, after the grace period, SIGKILL, which the
// child, not being PID 1, cannot ignore. The shell exits with the child's
// status, which stops the container, and the engine removes it. A kept
// container outlives its command until the end of its lifetime, after
// writing its exit status.
const wrapperScript = `life=$1 grace=$2 sig=$3 keep=$4
shift 4
"$@" &
child=$!
for s in HUP INT QUIT TERM USR1 USR2; do trap "kill -$s $child 2>/dev/null" "$s"; done
(
	sleep "$life"
	echo "tanvo-dockertest: lifetime of ${life}s reached, stopping the container" >&2
	kill "-$sig" "$child" 2>/dev/null || exit 0
	sleep "$grace"
	kill -KILL "$child" 2>/dev/null
) &
timer=$!
while :; do
	wait "$child"
	status=$?
	kill -0 "$child" 2>/dev/null || break
done
if [ "$keep" = 1 ]; then
	echo "` + exitPrefix + `$status" >&2
	wait "$timer"
fi
exit "$status"
`

// sidecarScript runs in a busybox container sharing the PID namespace of
// a container without a shell, whose PID 1 is the engine's init process
// (HostConfig.Init). When the lifetime is over it sends the stop signal to
// PID 1, which forwards it to the image's process, and after the grace
// period kills every process but PID 1; the init process then exits, the
// container stops and the engine removes it. The sidecar dies with the PID
// namespace when the container stops first.
const sidecarScript = `sleep "$1"
echo "tanvo-dockertest: lifetime of ${1}s reached, stopping the container" >&2
kill "-$3" 1
sleep "$2"
kill -KILL -1
`

// lifetime returns the lifetime cap of a container: d when set, else
// [LifetimeEnv], else [DefaultLifetime].
func lifetime(d time.Duration) (time.Duration, error) {
	if d > 0 {
		return d, nil
	}
	v := os.Getenv(LifetimeEnv)
	if v == "" {
		return DefaultLifetime, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("dockertest: %s=%q: want a positive duration", LifetimeEnv, v)
	}
	return d, nil
}

func seconds(d time.Duration) string {
	return strconv.Itoa(max(1, int(math.Ceil(d.Seconds()))))
}

// lifetimeArgs returns the leading arguments of the wrapper script.
func lifetimeArgs(life time.Duration, sig string, keep bool) []string {
	k := "0"
	if keep {
		k = "1"
	}
	return []string{seconds(life), seconds(Grace), sig, k}
}

// stopSignal returns the image's stop signal as an argument of kill: a
// name without its SIG prefix or a number; SIGTERM when unset.
func stopSignal(s string) string {
	s = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "SIG")
	if s == "" {
		return "TERM"
	}
	return s
}

// startSidecar starts the sidecar that enforces the lifetime of container
// id, which runs under the engine's init process.
func (e *Engine) startSidecar(ctx context.Context, id string, labels map[string]string, life time.Duration, sig string) (string, error) {
	if _, err := e.image(ctx, SidecarImage); err != nil {
		return "", err
	}
	var created struct{ ID string }
	err := e.do(ctx, http.MethodPost, "/containers/create?name=tanvo-lifetime-"+randomHex(), map[string]any{
		"Image":  SidecarImage,
		"Cmd":    []string{"sh", "-c", sidecarScript, "tanvo-lifetime", seconds(life), seconds(Grace), sig},
		"Labels": labels,
		"HostConfig": map[string]any{
			"AutoRemove": true,
			"PidMode":    "container:" + id,
		},
	}, &created)
	if err != nil {
		return "", err
	}
	if err := e.do(ctx, http.MethodPost, "/containers/"+created.ID+"/start", nil, nil); err != nil {
		_ = e.remove(context.WithoutCancel(ctx), created.ID)
		return "", err
	}
	return created.ID, nil
}
