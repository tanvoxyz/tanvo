// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package pgtest

import (
	"slices"
	"testing"
)

func TestStatements(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "comments and blank lines dropped",
			sql:  "-- atlas:txmode none\n\n-- a comment\nCREATE INDEX CONCURRENTLY a ON t (x);\n-- atlas:nolint MF101\nCREATE INDEX CONCURRENTLY b ON t (y);\n",
			want: []string{"CREATE INDEX CONCURRENTLY a ON t (x);", "CREATE INDEX CONCURRENTLY b ON t (y);"},
		},
		{
			name: "multi-line statement",
			sql:  "CREATE TABLE t (\n  x int\n);\nSELECT 1",
			want: []string{"CREATE TABLE t (\n  x int\n);", "SELECT 1"},
		},
		{name: "empty", sql: "-- only a comment\n", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Statements(tt.sql); !slices.Equal(got, tt.want) {
				t.Errorf("Statements() got = %q, want = %q", got, tt.want)
			}
		})
	}
}
