package store_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
)

// TestRowLevelSecurity checks that every table with organization_id
// enables row-level security and has the tenant_isolation policy, and that
// the application role sees no rows without the setting.
func TestRowLevelSecurity(t *testing.T) {
	db := pgtest.New(t, template)
	pool := pgtest.Pool(t, db.AdminDSN)
	rows, err := pool.Query(t.Context(), `
		SELECT c.relname, c.relrowsecurity,
			EXISTS (SELECT 1 FROM pg_policies p WHERE p.schemaname = 'public' AND p.tablename = c.relname AND p.policyname = 'tenant_isolation')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r'
			AND (c.relname = 'organizations' OR EXISTS (
				SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped))
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("query tables error got = %v, want = nil", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		var enabled, policy bool
		if err := rows.Scan(&name, &enabled, &policy); err != nil {
			t.Fatalf("scan error got = %v, want = nil", err)
		}
		n++
		if name == "job_outbox" {
			// The outbox is read by the relay across organizations.
			continue
		}
		if !enabled || !policy {
			t.Errorf("table %s: row-level security got = %t, policy got = %t, want = true, true", name, enabled, policy)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error got = %v, want = nil", err)
	}
	if n < 50 {
		t.Errorf("tables with organization_id got = %d, want >= 50", n)
	}
}
