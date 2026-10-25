package store_test

import (
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store"
	"github.com/tanvoxyz/tanvo/libs/store/porttest"
)

// viewQuery runs a query as the RLS-bound application role, in a
// transaction bound to org, as a reader of the export views does.
func viewQuery(t *testing.T, tr port.Transactor, org core.OrganizationID, query string, args ...any) []string {
	t.Helper()
	ctx := t.Context()
	tx, err := tr.(*store.Store).Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("Begin() error got = %v, want = nil", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('tanvo.organization_id', $1, true)`, org.String()); err != nil {
		t.Fatalf("bind organization error got = %v, want = nil", err)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		t.Fatalf("Query(%s) error got = %v, want = nil", query, err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var v *string
		if err := row.Scan(&v); err != nil || v == nil {
			return "<nil>", err
		}
		return *v, nil
	})
	if err != nil {
		t.Fatalf("Query(%s) error got = %v, want = nil", query, err)
	}
	return out
}

// TestExportViewsPostgres runs the export view tests as the RLS-bound role,
// whose row-level security the security_invoker views apply.
func TestExportViewsPostgres(t *testing.T) {
	t.Parallel()
	porttest.RunExportViews(t, storeHarness(true), viewQuery)
}
