// Package dockertest starts throwaway containers for integration tests on
// the local Docker engine through the Docker Engine API, without a client
// library.
//
// [Connect] finds the engine: DOCKER_HOST (unix:// or tcp://), the
// endpoint of the Docker CLI's current context, then the sockets of
// OrbStack, Docker Desktop, Colima, Rancher Desktop, rootless Docker,
// Podman and /var/run/docker.sock. [Engine.Run] pulls the image when it is
// missing and starts a container.
//
// A test removes its containers with [Container.Close], from TestMain or
// t.Cleanup. Two mechanisms remove the containers of a test process that
// dies before its cleanup runs (a go test -timeout panic, SIGKILL, SIGINT):
//
//   - Every container carries the label [Label] and its owner's hostname
//     and process ID. Before it starts a container, [Engine.Run] removes
//     the labeled containers of the same host whose owner process is gone
//     or whose lifetime has expired ([Engine.Reap]).
//   - Every container has a lifetime cap ([LifetimeEnv], two hours by
//     default). The image's entrypoint runs under a /bin/sh wrapper that,
//     when the lifetime is over, sends the image's stop signal to it, then
//     SIGKILL after a grace period; the wrapper, PID 1, then exits and the
//     engine removes the container (AutoRemove). For an image without a
//     shell ([Spec.NoShell]), the engine's init process is PID 1 and a
//     busybox sidecar sharing the container's PID namespace sends the
//     signals instead.
package dockertest
