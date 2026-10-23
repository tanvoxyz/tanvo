package storetest_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

// template is a database migrated with store.Migrate.
var template = pgtest.Template{
	Name: "billing",
	Setup: func(ctx context.Context, conn *pgx.Conn) error {
		s, err := store.Open(ctx, conn.Config().ConnString())
		if err != nil {
			return err
		}
		defer s.Pool().Close()
		return store.Migrate(ctx, s.Pool())
	},
}

// discard is a webhook emitter that drops the events.
type discard struct{}

func (discard) Emit(context.Context, webhook.Event) error { return nil }

// newHarness returns a billing harness on a fresh PostgreSQL database
// whose clock reads now.
func newHarness(t *testing.T, now time.Time) (*billingtest.Harness, *store.Store) {
	t.Helper()
	db := pgtest.New(t, template)
	s, err := store.Open(t.Context(), db.AdminDSN)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(s.Pool().Close)
	return billingtest.NewOn(t, now, s, discard{}), s
}

// concurrently runs fn n times at once and returns the errors.
func concurrently(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = fn(i)
		})
	}
	close(start)
	wg.Wait()
	return errs
}
