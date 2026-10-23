package clickhouse

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

//go:embed schema.sql
var schema string

// Schema returns the DDL statements of the event store.
func Schema() []string {
	var out []string
	for stmt := range strings.SplitSeq(schema, "\n;\n") {
		if s := strings.TrimSpace(stmt); s != "" && !onlyComments(s) {
			out = append(out, s)
		}
	}
	return out
}

func onlyComments(s string) bool {
	for line := range strings.SplitSeq(s, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			return false
		}
	}
	return true
}

// Migrate applies the schema of the event store, idempotently.
func Migrate(ctx context.Context, conn driver.Conn) error {
	for _, stmt := range Schema() {
		if err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("eventstore: clickhouse: migrate: %w", err)
		}
	}
	return nil
}
