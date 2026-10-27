package clickhouse

import (
	"context"
	_ "embed"
	"fmt"
	"regexp"
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
//
// In a database of the Replicated engine (a self-managed cluster), the
// tables are created with the replicated variants of their engines
// (ReplicatedReplacingMergeTree, …), which such a database requires for
// its tables' data to replicate; their paths in Keeper are the server's
// default_replica_path and default_replica_name. ClickHouse Cloud's Shared
// databases convert the engines themselves, and other databases (a single
// node) keep them as written.
func Migrate(ctx context.Context, conn driver.Conn) error {
	var engine string
	if err := conn.QueryRow(ctx, "SELECT engine FROM system.databases WHERE name = currentDatabase()").Scan(&engine); err != nil {
		return fmt.Errorf("eventstore: clickhouse: migrate: database engine: %w", err)
	}
	replicated := engine == "Replicated"
	for _, stmt := range Schema() {
		if replicated {
			stmt = replicatedEngine.ReplaceAllString(stmt, "ENGINE = Replicated${1}MergeTree")
		}
		if err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("eventstore: clickhouse: migrate: %w", err)
		}
	}
	if !replicated {
		return nil
	}
	// Tables created before the database was replicated would not
	// replicate their data.
	rows, err := conn.Query(ctx, "SELECT name, engine FROM system.tables WHERE database = currentDatabase() AND engine LIKE '%MergeTree' AND engine NOT LIKE 'Replicated%'")
	if err != nil {
		return fmt.Errorf("eventstore: clickhouse: migrate: check engines: %w", err)
	}
	defer rows.Close()
	var bad []string
	for rows.Next() {
		var name, e string
		if err := rows.Scan(&name, &e); err != nil {
			return fmt.Errorf("eventstore: clickhouse: migrate: check engines: %w", err)
		}
		bad = append(bad, name+" ("+e+")")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("eventstore: clickhouse: migrate: check engines: %w", err)
	}
	if len(bad) > 0 {
		return fmt.Errorf("eventstore: clickhouse: migrate: the Replicated database holds tables that do not replicate: %s", strings.Join(bad, ", "))
	}
	return nil
}

// replicatedEngine matches the engine of a CREATE TABLE of the schema.
var replicatedEngine = regexp.MustCompile(`ENGINE = (\w*)MergeTree\b`)
