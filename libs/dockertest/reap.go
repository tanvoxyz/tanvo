package dockertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Labels of every container: Label carries the [Spec.Name], the others
// the owner process and the end of the lifetime.
const (
	Label        = "xyz.tanvo.dockertest"
	LabelHost    = Label + ".host"
	LabelPID     = Label + ".pid"
	LabelExpires = Label + ".expires"
)

// labels returns the labels of a container started now.
func (e *Engine) labels(name string, life time.Duration) map[string]string {
	if name == "" {
		name = "test"
	}
	return map[string]string{
		Label:     name,
		LabelHost: e.owner.host,
		LabelPID:  strconv.Itoa(e.owner.pid),
		// The wrapper stops the container by then; a minute covers the
		// time to start it.
		LabelExpires: time.Now().Add(life + Grace + time.Minute).UTC().Format(time.RFC3339),
	}
}

// Reap removes the containers of this host whose owner process is gone or
// whose lifetime has expired, and returns how many it removed. [Engine.Run]
// calls it before starting a container.
func (e *Engine) Reap(ctx context.Context) (int, error) {
	filters, err := json.Marshal(map[string][]string{"label": {LabelHost + "=" + e.owner.host}})
	if err != nil {
		return 0, fmt.Errorf("dockertest: %w", err)
	}
	var list []struct {
		ID     string `json:"Id"`
		Names  []string
		Labels map[string]string
	}
	q := url.Values{"all": {"true"}, "filters": {string(filters)}}
	if err := e.do(ctx, http.MethodGet, "/containers/json?"+q.Encode(), nil, &list); err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, c := range list {
		reason := e.stale(c.Labels)
		if reason == "" {
			continue
		}
		if err := e.remove(ctx, c.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
		slog.InfoContext(ctx, "dockertest: removed a stale container", "id", c.ID[:min(12, len(c.ID))], "names", c.Names, "reason", reason)
	}
	return n, errors.Join(errs...)
}

// stale returns why a container of this host is stale, or "".
func (e *Engine) stale(labels map[string]string) string {
	if t, err := time.Parse(time.RFC3339, labels[LabelExpires]); err == nil && time.Now().After(t) {
		return "lifetime expired"
	}
	pid, err := strconv.Atoi(labels[LabelPID])
	if err != nil || pid <= 0 || pid == e.owner.pid {
		return ""
	}
	if !alive(pid) {
		return "owner process " + strconv.Itoa(pid) + " is gone"
	}
	return ""
}
