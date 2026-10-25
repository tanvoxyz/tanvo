package storetest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// schemaEnv set to "source" loads libs/store/schema into the template
// instead of applying the migrations, while a schema change has no
// migration yet.
const schemaEnv = "TANVO_STORE_TEST_SCHEMA"

// template is a database migrated with store.Migrate (or loaded from
// libs/store/schema, see schemaEnv).
var template = pgtest.Template{
	Name: "billing-" + os.Getenv(schemaEnv),
	Setup: func(ctx context.Context, conn *pgx.Conn) error {
		if os.Getenv(schemaEnv) == "source" {
			return loadSchema(ctx, conn)
		}
		s, err := store.Open(ctx, conn.Config().ConnString())
		if err != nil {
			return err
		}
		defer s.Pool().Close()
		return store.Migrate(ctx, s.Pool())
	},
}

// loadSchema runs the SQL files of libs/store/schema in order.
func loadSchema(ctx context.Context, conn *pgx.Conn) error {
	files, err := filepath.Glob("../../store/schema/*.sql")
	if err != nil {
		return fmt.Errorf("schema files: %w", err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no schema files")
	}
	slices.Sort(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, string(b)); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
	}
	return nil
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
