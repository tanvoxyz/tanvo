// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package store_test

import (
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/ee/analytics/store"
)

func TestMigrate(t *testing.T) {
	e := newDB(t)
	files, err := store.Migrations()
	if err != nil {
		t.Fatalf("Migrations() error = %v", err)
	}
	// Twice: the second run applies nothing and fails on nothing.
	for i := range 2 {
		if err := store.Migrate(e.ctx, e.pool, quietLog()); err != nil {
			t.Fatalf("Migrate() run %d error = %v", i+1, err)
		}
	}
	rows, err := e.pool.Query(e.ctx, "SELECT version, checksum FROM analytics.schema_revisions ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for rows.Next() {
		var v, sum string
		if err := rows.Scan(&v, &sum); err != nil {
			t.Fatal(err)
		}
		if sum == "" {
			t.Errorf("revision %s: got = empty checksum, want = a checksum", v)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, f := range files {
		want = append(want, f.Version())
	}
	if strings.Join(versions, ",") != strings.Join(want, ",") {
		t.Errorf("revisions: got = %v, want = %v", versions, want)
	}
	var tables int
	if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM pg_tables WHERE schemaname = 'analytics'
		AND tablename IN ('refresh_state', 'plans', 'mrr_intervals', 'recognition_schedules')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 4 {
		t.Errorf("analytics tables: got = %d, want = 4", tables)
	}
}

func TestMigrateRejects(t *testing.T) {
	tests := []struct {
		name    string
		tamper  string
		wantErr string
	}{
		{
			name:    "modified migration",
			tamper:  "UPDATE analytics.schema_revisions SET checksum = 'tampered'",
			wantErr: "was modified after it was applied",
		},
		{
			name:    "unknown migration",
			tamper:  "INSERT INTO analytics.schema_revisions (version, checksum) VALUES ('29991231000000', 'x')",
			wantErr: "which this binary does not know",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.exec(tt.tamper)
			err := store.Migrate(e.ctx, e.pool, quietLog())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Migrate() error got = %v, want = %q", err, tt.wantErr)
			}
		})
	}
}
