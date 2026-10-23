package jobs_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/jobs"
)

func TestCheckSchema(t *testing.T) {
	t.Parallel()
	pool, s := schema(t)
	ctx := context.Background()
	if err := jobs.CheckSchema(ctx, pool, s); err != nil {
		t.Errorf("CheckSchema(migrated) = %v, want = nil", err)
	}
	if err := jobs.CheckSchema(ctx, pool, s+"_missing"); err == nil {
		t.Error("CheckSchema(missing) = nil, want = an error")
	}
}

func TestMiddlewareWrapsWork(t *testing.T) {
	t.Parallel()
	_, s := schema(t)
	var wrapped, ran atomic.Int32
	mw := river.WorkerMiddlewareFunc(func(ctx context.Context, _ *rivertype.JobRow, doInner func(context.Context) error) error {
		wrapped.Add(1)
		return doInner(ctx)
	})
	reg := jobs.NewRegistry()
	jobs.MustRegister(reg, jobs.Kind{Name: "test.middleware"}, jobs.HandlerFunc[testArgs](func(context.Context, *jobs.Job[testArgs]) error {
		ran.Add(1)
		return nil
	}))
	c := newClient(t, s, reg, jobs.Config{Work: []port.Queue{port.QueueDefault}, DisablePeriodic: true, Middleware: []rivertype.Middleware{mw}})
	start(t, c)
	if err := c.Insert(context.Background(), job("test.middleware", org(), 1, "")); err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	eventually(t, 10*time.Second, "job worked", func() bool { return ran.Load() == 1 })
	if got := wrapped.Load(); got != 1 {
		t.Errorf("middleware calls got = %d, want = 1", got)
	}
}
