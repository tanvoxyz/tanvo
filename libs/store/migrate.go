package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ariga.io/atlas/sql/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationFiles holds the versioned migrations written by
// internal/cmd/migrate-diff and their atlas.sum.
//
//go:embed migrations
var migrationFiles embed.FS

// migrateLockKey is the session advisory lock serializing concurrent
// Migrate calls ("tanvo.migrate" as a 64-bit key).
const migrateLockKey int64 = 0x74616e766f6d6967

// revisionsTable records the applied migrations.
const revisionsTable = "tanvo_schema_revisions"

// DefaultLockTimeout is the lock_timeout of the migrations unless
// [WithLockTimeout] sets another.
const DefaultLockTimeout = 5 * time.Second

// MigrateOption configures [Migrate].
type MigrateOption func(*migrateConfig)

type migrateConfig struct {
	lockTimeout time.Duration
}

// WithLockTimeout sets the lock_timeout of each migration statement
// (default [DefaultLockTimeout]); 0 waits for locks indefinitely. A
// statement waiting for a lock queues every later lock request on its
// table behind it, so a migration blocked by a long transaction would stop
// the writes of the running release: with a timeout it fails instead, and
// can be retried.
func WithLockTimeout(d time.Duration) MigrateOption {
	return func(c *migrateConfig) { c.lockTimeout = d }
}

// Migrate applies the embedded migrations that the database has not applied
// yet, in version order. It holds an advisory lock meanwhile, so concurrent
// server instances apply them once. It fails when atlas.sum does not match
// the files, when an applied migration was modified (merged migrations are
// never modified), or when the database has applied a migration this binary
// does not know (it is older than the database).
//
// Each file runs in its own transaction with a lock_timeout
// ([WithLockTimeout]). A file whose header has the directive
// "-- atlas:txmode none" runs without a transaction, one statement at a
// time, so that it can hold CREATE INDEX CONCURRENTLY; its revision is
// recorded once every statement has succeeded, so a failed file runs again
// from its first statement, and its statements must be idempotent (IF NOT
// EXISTS). A concurrent index build that failed leaves an invalid index:
// before a CREATE INDEX CONCURRENTLY, an invalid index of the same name is
// dropped, so the retry builds it again instead of skipping it.
func Migrate(ctx context.Context, pool *pgxpool.Pool, opts ...MigrateOption) error {
	files, sum, err := migrations()
	if err != nil {
		return err
	}
	return migrateFiles(ctx, pool, files, sum, opts...)
}

func migrateFiles(ctx context.Context, pool *pgxpool.Pool, files []migrate.File, sum migrate.HashFile, opts ...MigrateOption) error {
	cfg := migrateConfig{lockTimeout: DefaultLockTimeout}
	for _, o := range opts {
		o(&cfg)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: migrate: acquire: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("store: migrate: lock: %w", err)
	}
	defer func() {
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockKey); err != nil {
			slog.WarnContext(ctx, "store: migrate: unlock", "error", err)
		}
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+revisionsTable+` (
		version text PRIMARY KEY,
		description text NOT NULL,
		checksum text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("store: migrate: create %s: %w", revisionsTable, err)
	}
	applied, err := appliedRevisions(ctx, conn.Conn())
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(files))
	for _, f := range files {
		known[f.Version()] = true
		checksum, err := sum.SumByName(f.Name())
		if err != nil {
			return fmt.Errorf("store: migrate: checksum of %s: %w", f.Name(), err)
		}
		if prev, ok := applied[f.Version()]; ok {
			if prev != checksum {
				return fmt.Errorf("store: migrate: applied migration %s was modified", f.Name())
			}
			continue
		}
		transactional, err := migrationTx(f)
		if err != nil {
			return fmt.Errorf("store: migrate: %s: %w", f.Name(), err)
		}
		if transactional {
			err = applyInTx(ctx, conn.Conn(), f, checksum, cfg.lockTimeout)
		} else {
			err = applyWithoutTx(ctx, conn.Conn(), f, checksum, cfg.lockTimeout)
		}
		if err != nil {
			return fmt.Errorf("store: migrate: apply %s: %w", f.Name(), err)
		}
		slog.InfoContext(ctx, "store: applied migration", "version", f.Version(), "description", f.Desc(), "transaction", transactional)
	}
	for v := range applied {
		if !known[v] {
			return fmt.Errorf("store: migrate: the database has applied migration %s, unknown to this version", v)
		}
	}
	return nil
}

// migrationTx reports whether f runs in a transaction: it does unless its
// header has the directive "-- atlas:txmode none".
func migrationTx(f migrate.File) (bool, error) {
	d, ok := f.(interface{ Directive(string) []string })
	if !ok {
		return true, nil
	}
	modes := d.Directive("txmode")
	switch {
	case len(modes) == 0:
		return true, nil
	case len(modes) > 1:
		return false, errors.New("more than one atlas:txmode directive")
	case modes[0] == "file":
		return true, nil
	case modes[0] == "none":
		return false, nil
	default:
		return false, fmt.Errorf("unsupported atlas:txmode %q (want file or none)", modes[0])
	}
}

// lockTimeoutSetting formats d for set_config('lock_timeout', …).
func lockTimeoutSetting(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}

func recordRevision(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, f migrate.File, checksum string,
) error {
	_, err := q.Exec(ctx, "INSERT INTO "+revisionsTable+" (version, description, checksum) VALUES ($1, $2, $3)",
		f.Version(), f.Desc(), checksum)
	return err
}

// applyInTx runs f and records its revision in one transaction.
func applyInTx(ctx context.Context, conn *pgx.Conn, f migrate.File, checksum string, lockTimeout time.Duration) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", lockTimeoutSetting(lockTimeout)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(f.Bytes())); err != nil {
			return mapErr(err)
		}
		return recordRevision(ctx, tx, f, checksum)
	})
}

// concurrentIndex matches CREATE INDEX CONCURRENTLY and captures the
// index name.
var concurrentIndex = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+(?:IF\s+NOT\s+EXISTS\s+)?("(?:[^"]|"")+"|[a-z_][a-z0-9_$]*)`)

// concurrentStmt matches the statements that run CONCURRENTLY: they never
// block the writes of the table, but wait for the transactions in progress,
// so they run without a lock_timeout.
var concurrentStmt = regexp.MustCompile(`(?is)^\s*(?:CREATE\s+(?:UNIQUE\s+)?INDEX|DROP\s+INDEX|REINDEX\s+(?:\(.*?\)\s*)?(?:INDEX|TABLE))\s+CONCURRENTLY\b`)

// applyWithoutTx runs the statements of f one at a time outside of a
// transaction, then records its revision.
func applyWithoutTx(ctx context.Context, conn *pgx.Conn, f migrate.File, checksum string, lockTimeout time.Duration) (err error) {
	stmts, err := f.StmtDecls()
	if err != nil {
		return err
	}
	defer func() {
		if _, rerr := conn.Exec(context.WithoutCancel(ctx), "RESET lock_timeout"); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}()
	for _, s := range stmts {
		timeout := lockTimeout
		if concurrentStmt.MatchString(s.Text) {
			timeout = 0
		}
		if _, err := conn.Exec(ctx, "SELECT set_config('lock_timeout', $1, false)", lockTimeoutSetting(timeout)); err != nil {
			return err
		}
		if m := concurrentIndex.FindStringSubmatch(s.Text); m != nil {
			if err := dropInvalidIndex(ctx, conn, m[1]); err != nil {
				return err
			}
		}
		if _, err := conn.Exec(ctx, s.Text); err != nil {
			return fmt.Errorf("statement at offset %d: %w", s.Pos, mapErr(err))
		}
	}
	return recordRevision(ctx, conn, f, checksum)
}

// dropInvalidIndex drops the index named name (an SQL identifier, quoted
// or not) when it is invalid: a CREATE INDEX CONCURRENTLY that failed left
// it, and CREATE INDEX CONCURRENTLY IF NOT EXISTS would skip it.
func dropInvalidIndex(ctx context.Context, conn *pgx.Conn, name string) error {
	var invalid bool
	err := conn.QueryRow(ctx, `SELECT NOT i.indisvalid FROM pg_index i WHERE i.indexrelid = to_regclass($1)`, name).Scan(&invalid)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("check index %s: %w", name, err)
	case !invalid:
		return nil
	}
	slog.WarnContext(ctx, "store: migrate: dropping an invalid index left by a failed concurrent build", "index", name)
	if _, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+name); err != nil {
		return fmt.Errorf("drop invalid index %s: %w", name, err)
	}
	return nil
}

// CheckSchema verifies, without changing anything, that the database has
// applied every embedded migration, unmodified: it compares the versions
// and checksums recorded in tanvo_schema_revisions with the migrations of
// this binary. Versions the binary does not know are accepted, as a
// database migrated by a newer release serves the previous one during a
// rolling deploy (migrations are expand-only within a release); [Migrate]
// refuses them.
func CheckSchema(ctx context.Context, pool *pgxpool.Pool) error {
	files, sum, err := migrations()
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("store: check schema: acquire: %w", err)
	}
	defer conn.Release()
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+revisionsTable).Scan(&exists); err != nil {
		return fmt.Errorf("store: check schema: %w", err)
	}
	if !exists {
		return errors.New("store: the database is not migrated (run tanvo migrate)")
	}
	applied, err := appliedRevisions(ctx, conn.Conn())
	if err != nil {
		return err
	}
	var missing, modified []string
	for _, f := range files {
		checksum, err := sum.SumByName(f.Name())
		if err != nil {
			return fmt.Errorf("store: check schema: checksum of %s: %w", f.Name(), err)
		}
		prev, ok := applied[f.Version()]
		switch {
		case !ok:
			missing = append(missing, f.Version())
		case prev != checksum:
			modified = append(modified, f.Version())
		}
	}
	var errs []error
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("store: migrations %s are not applied (run tanvo migrate)", strings.Join(missing, ", ")))
	}
	if len(modified) > 0 {
		errs = append(errs, fmt.Errorf("store: applied migrations %s differ from this version's", strings.Join(modified, ", ")))
	}
	return errors.Join(errs...)
}

// migrations returns the embedded migration files in version order and
// their validated sum file.
func migrations() ([]migrate.File, migrate.HashFile, error) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	dir := &migrate.MemDir{}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(sub, e.Name())
		if err != nil {
			return nil, nil, fmt.Errorf("store: migrations: %w", err)
		}
		if err := dir.WriteFile(e.Name(), b); err != nil {
			return nil, nil, fmt.Errorf("store: migrations: %w", err)
		}
	}
	if err := migrate.Validate(dir); err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	files, err := dir.Files()
	if err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	sum, err := dir.Checksum()
	if err != nil {
		return nil, nil, fmt.Errorf("store: migrations: %w", err)
	}
	return files, sum, nil
}

func appliedRevisions(ctx context.Context, conn *pgx.Conn) (map[string]string, error) {
	rows, err := conn.Query(ctx, "SELECT version, checksum FROM "+revisionsTable)
	if err != nil {
		return nil, fmt.Errorf("store: migrate: read %s: %w", revisionsTable, err)
	}
	out := map[string]string{}
	var version, checksum string
	if _, err := pgx.ForEachRow(rows, []any{&version, &checksum}, func() error {
		out[version] = checksum
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: migrate: read %s: %w", revisionsTable, err)
	}
	return out, nil
}
