package postgres

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// schemaMarker prefixes every identifier of the event store in the SQL of
// the package: tables, sequence, functions, and the partition names and
// advisory-lock keys the functions build. Every statement goes through a
// [schemaDB], which replaces it with the quoted schema and a dot, or with
// nothing when the store has no schema of its own.
const schemaMarker = "__schema__."

// schemaPattern is the form of a schema name: an unquoted lowercase
// PostgreSQL identifier, so that its quoted form may also appear in the
// string literals and format strings of the functions.
var schemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// ValidateSchema reports whether name can hold the event store (see
// [WithSchema]): a lowercase identifier of letters, digits and
// underscores, not starting with a digit or "pg_", of at most 63 bytes.
func ValidateSchema(name string) error {
	switch {
	case !schemaPattern.MatchString(name):
		return fmt.Errorf("eventstore: postgres: schema %q: want lowercase letters, digits and underscores, not starting with a digit", name)
	case len(name) > 63:
		return fmt.Errorf("eventstore: postgres: schema %q: longer than 63 bytes", name)
	case strings.HasPrefix(name, "pg_"):
		return fmt.Errorf("eventstore: postgres: schema %q: the pg_ prefix is reserved", name)
	}
	return nil
}

// qualifier renders the SQL of a schema: schemaMarker becomes prefix.
type qualifier struct{ prefix string }

// newQualifier returns the qualifier of a schema; the empty name leaves
// identifiers unqualified, resolved by the search_path.
func newQualifier(name string) (qualifier, error) {
	if name == "" {
		return qualifier{}, nil
	}
	if err := ValidateSchema(name); err != nil {
		return qualifier{}, err
	}
	return qualifier{prefix: pgx.Identifier{name}.Sanitize() + "."}, nil
}

func (q qualifier) sql(s string) string { return strings.ReplaceAll(s, schemaMarker, q.prefix) }

// schemaDB qualifies the statements run over db.
type schemaDB struct {
	db DB
	q  qualifier
}

func (d schemaDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return d.db.Exec(ctx, d.q.sql(sql), args...)
}

func (d schemaDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return d.db.Query(ctx, d.q.sql(sql), args...)
}

func (d schemaDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return d.db.QueryRow(ctx, d.q.sql(sql), args...)
}

func (d schemaDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return schemaTx{Tx: tx, q: d.q}, nil
}

// schemaTx qualifies the statements run in a transaction.
type schemaTx struct {
	pgx.Tx
	q qualifier
}

func (t schemaTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.Tx.Exec(ctx, t.q.sql(sql), args...)
}

func (t schemaTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.Tx.Query(ctx, t.q.sql(sql), args...)
}

func (t schemaTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.Tx.QueryRow(ctx, t.q.sql(sql), args...)
}

func (t schemaTx) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := t.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return schemaTx{Tx: tx, q: t.q}, nil
}

// failedDB is the database of a store configured with an invalid schema:
// every statement fails with err.
type failedDB struct{ err error }

func (d failedDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, d.err
}

func (d failedDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, d.err }

func (d failedDB) QueryRow(context.Context, string, ...any) pgx.Row { return failedRow(d) }

func (d failedDB) Begin(context.Context) (pgx.Tx, error) { return nil, d.err }

type failedRow struct{ err error }

func (r failedRow) Scan(...any) error { return r.err }
