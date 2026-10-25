// Command migrate-diff writes the versioned migration that brings the
// migration directory up to the declarative schema.
//
// It replays migrations/ on one empty development database, loads the
// files of schema/ into another, and diffs the two:
//
//   - tables, columns, indexes, checks and foreign keys with the Atlas
//     planner (ariga.io/atlas, used as a library);
//   - row-level security (ENABLE ROW LEVEL SECURITY and policies), which
//     the open-source Atlas planner does not handle, by comparing
//     pg_class.relrowsecurity and pg_policies;
//   - views, which the open-source Atlas planner does not handle either, by
//     comparing their pg_get_viewdef and options: a view that changed, or
//     reads a table whose planned change it would block, is dropped before
//     the table changes and created after them;
//   - check constraints whose expression changed and foreign keys the
//     planner cannot express (such as ON DELETE SET NULL (column)): the
//     plan is applied to a replay of the directory and the checks and
//     foreign keys still differing from schema/ are dropped and recreated.
//     Any other constraint or index still differing fails the run.
//
// Both sets of statements go into one new file migrations/<version>_<name>.sql,
// and migrations/atlas.sum is rewritten. With -check it writes nothing and
// fails when the directory is not up to date (used by CI).
//
// The development databases live on the server of -dev-url, or of
// TANVO_TEST_POSTGRES_DSN, or of a postgres:18 container started on the
// local Docker engine and removed afterwards.
//
// Usage, from libs/store:
//
//	go run ./internal/cmd/migrate-diff -name add_wallets
//	go run ./internal/cmd/migrate-diff -check
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/postgres"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"

	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
)

func main() {
	if err := run(); err != nil {
		slog.Error("migrate-diff", "error", err)
		os.Exit(1)
	}
}

func run() error {
	name := flag.String("name", "", "name of the new migration (required unless -check)")
	check := flag.Bool("check", false, "fail when migrations/ is not up to date with schema/, writing nothing")
	devURL := flag.String("dev-url", "", "superuser DSN of a PostgreSQL 18 server for the development databases")
	schemaDir := flag.String("schema", "schema", "directory of the declarative schema")
	migrationsDir := flag.String("migrations", "migrations", "migration directory")
	flag.Parse()
	if !*check && *name == "" {
		return errors.New("-name is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	server := *devURL
	if server == "" {
		dsn, stopServer, err := pgtest.Standalone(ctx)
		if err != nil {
			return err
		}
		defer func() {
			if err := stopServer(context.WithoutCancel(ctx)); err != nil {
				slog.WarnContext(ctx, "removing the development server", "error", err)
			}
		}()
		server = dsn
	}

	dir, err := migrate.NewLocalDir(*migrationsDir)
	if err != nil {
		return fmt.Errorf("open %s: %w", *migrationsDir, err)
	}
	if err := migrate.Validate(dir); err != nil && !errors.Is(err, migrate.ErrChecksumNotFound) {
		return fmt.Errorf("validate %s: %w", *migrationsDir, err)
	}

	current, err := openDev(ctx, server)
	if err != nil {
		return err
	}
	defer current.Close()
	desired, err := openDev(ctx, server)
	if err != nil {
		return err
	}
	defer desired.Close()
	if err := loadSchema(ctx, desired, *schemaDir); err != nil {
		return err
	}

	// The planner replays dir on current.
	drv, err := postgres.Open(current)
	if err != nil {
		return fmt.Errorf("open driver: %w", err)
	}
	planner := migrate.NewPlanner(drv, dir, migrate.PlanWithSchemaQualifier(""))
	desiredDrv, err := postgres.Open(desired)
	if err != nil {
		return fmt.Errorf("open driver: %w", err)
	}
	plan, err := planner.PlanSchema(ctx, *name, migrate.SchemaConn(desiredDrv, "public", nil))
	switch {
	case errors.Is(err, migrate.ErrNoPlan):
		plan = &migrate.Plan{Name: *name}
	case err != nil:
		return fmt.Errorf("plan: %w", err)
	}
	// The planner restores its database after replaying, so the RLS diff
	// replays the directory on a database of its own.
	replayed, err := openDev(ctx, server)
	if err != nil {
		return err
	}
	defer replayed.Close()
	if err := replay(ctx, replayed, dir); err != nil {
		return err
	}
	viewDrops, viewCreates, err := diffViews(ctx, replayed, desired, plan)
	if err != nil {
		return err
	}
	rls, err := diffRLS(ctx, replayed, desired)
	if err != nil {
		return err
	}
	plan.Changes = slices.Concat(viewDrops, plan.Changes, rls, viewCreates)
	// The Atlas planner misses some changes, such as a new expression of an
	// existing check constraint or a column list on ON DELETE SET NULL. The
	// plan is applied to the replayed database and what still differs from
	// the schema is checked: checks and foreign keys are replaced, anything
	// else fails.
	for _, c := range plan.Changes {
		if _, err := replayed.ExecContext(ctx, c.Cmd); err != nil {
			return fmt.Errorf("apply planned statement %q: %w", c.Cmd, err)
		}
	}
	residual, err := diffCatalog(ctx, replayed, desired)
	if err != nil {
		return err
	}
	plan.Changes = append(plan.Changes, residual...)

	if *check {
		if len(plan.Changes) > 0 {
			for _, c := range plan.Changes {
				fmt.Fprintf(os.Stderr, "%s;\n", c.Cmd)
			}
			return fmt.Errorf("migrations/ is not up to date with schema/ (%d pending statements above); run migrate-diff -name <name>", len(plan.Changes))
		}
		slog.InfoContext(ctx, "migrations are up to date")
		return nil
	}
	if len(plan.Changes) == 0 {
		slog.InfoContext(ctx, "no changes")
		return nil
	}
	if err := planner.WritePlan(plan); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}
	slog.InfoContext(ctx, "wrote migration", "name", *name, "statements", len(plan.Changes))
	return nil
}

// openDev creates an empty database on the server and opens it.
func openDev(ctx context.Context, server string) (*sql.DB, error) {
	dsn, err := pgtest.CreateDatabase(ctx, server)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dsn, err)
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// loadSchema executes the files of dir in name order.
func loadSchema(ctx context.Context, db *sql.DB, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}
	slices.Sort(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		if _, err := db.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("load %s: %w", f, err)
		}
	}
	return nil
}

// replay executes the migration files of dir in order.
func replay(ctx context.Context, db *sql.DB, dir migrate.Dir) error {
	files, err := dir.Files()
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	for _, f := range files {
		if _, err := db.ExecContext(ctx, string(f.Bytes())); err != nil {
			return fmt.Errorf("replay %s: %w", f.Name(), err)
		}
	}
	return nil
}

// policy is a row-level security policy of the public schema.
type policy struct {
	table, name, command, permissive string
	roles                            []string
	using, check                     sql.NullString
}

func (p policy) create() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE POLICY %s ON %s AS %s FOR %s TO %s", quote(p.name), quote(p.table), p.permissive, p.command, strings.Join(p.roles, ", "))
	if p.using.Valid {
		fmt.Fprintf(&b, " USING (%s)", p.using.String)
	}
	if p.check.Valid {
		fmt.Fprintf(&b, " WITH CHECK (%s)", p.check.String)
	}
	return b.String()
}

func (p policy) equal(q policy) bool {
	return p.command == q.command && p.permissive == q.permissive && slices.Equal(p.roles, q.roles) &&
		p.using == q.using && p.check == q.check
}

// rlsState is the row-level security of the tables of the public schema.
type rlsState struct {
	enabled  map[string]bool   // table → relrowsecurity
	forced   map[string]bool   // table → relforcerowsecurity
	policies map[string]policy // "table.policy" → policy
}

func readRLS(ctx context.Context, db *sql.DB) (*rlsState, error) {
	s := &rlsState{enabled: map[string]bool{}, forced: map[string]bool{}, policies: map[string]policy{}}
	rows, err := db.QueryContext(ctx, `SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')`)
	if err != nil {
		return nil, fmt.Errorf("read tables: %w", err)
	}
	for rows.Next() {
		var t string
		var on, force bool
		if err := rows.Scan(&t, &on, &force); err != nil {
			return nil, fmt.Errorf("read tables: %w", err)
		}
		s.enabled[t], s.forced[t] = on, force
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("read tables: %w", err)
	}
	rows, err = db.QueryContext(ctx, `SELECT tablename, policyname, permissive, array_to_string(roles, ','), cmd, qual, with_check
		FROM pg_policies WHERE schemaname = 'public'`)
	if err != nil {
		return nil, fmt.Errorf("read policies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var p policy
		var roles string
		if err := rows.Scan(&p.table, &p.name, &p.permissive, &roles, &p.command, &p.using, &p.check); err != nil {
			return nil, fmt.Errorf("read policies: %w", err)
		}
		p.roles = strings.Split(roles, ",")
		slices.Sort(p.roles)
		s.policies[p.table+"."+p.name] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read policies: %w", err)
	}
	return s, nil
}

// diffRLS returns the statements that bring the row-level security of
// current to that of desired. Tables the Atlas plan drops lose their
// policies with them; tables it creates exist when these statements run,
// as they come last.
func diffRLS(ctx context.Context, current, desired *sql.DB) ([]*migrate.Change, error) {
	from, err := readRLS(ctx, current)
	if err != nil {
		return nil, err
	}
	to, err := readRLS(ctx, desired)
	if err != nil {
		return nil, err
	}
	var out []*migrate.Change
	add := func(cmd, comment string) {
		out = append(out, &migrate.Change{Cmd: cmd, Comment: comment})
	}
	for _, key := range sortedKeys(from.policies) {
		p := from.policies[key]
		q, ok := to.policies[key]
		if _, tableKept := to.enabled[p.table]; !tableKept {
			continue // dropped with its table
		}
		if !ok || !p.equal(q) {
			add(fmt.Sprintf("DROP POLICY %s ON %s", quote(p.name), quote(p.table)), fmt.Sprintf("drop policy %q of table %q", p.name, p.table))
		}
	}
	for _, t := range sortedKeys(to.enabled) {
		if on := to.enabled[t]; on != from.enabled[t] {
			if on {
				add(fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", quote(t)), fmt.Sprintf("enable row-level security on table %q", t))
			} else {
				add(fmt.Sprintf("ALTER TABLE %s DISABLE ROW LEVEL SECURITY", quote(t)), fmt.Sprintf("disable row-level security on table %q", t))
			}
		}
		if force := to.forced[t]; force != from.forced[t] {
			if force {
				add(fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", quote(t)), fmt.Sprintf("force row-level security on table %q", t))
			} else {
				add(fmt.Sprintf("ALTER TABLE %s NO FORCE ROW LEVEL SECURITY", quote(t)), fmt.Sprintf("stop forcing row-level security on table %q", t))
			}
		}
	}
	for _, key := range sortedKeys(to.policies) {
		q := to.policies[key]
		if p, ok := from.policies[key]; !ok || !p.equal(q) {
			add(q.create(), fmt.Sprintf("create policy %q on table %q", q.name, q.table))
		}
	}
	return out, nil
}

// catalog is the constraints and indexes of the tables of the public schema,
// keyed by "table.name", with their definitions as PostgreSQL prints them.
type catalog struct {
	checks  map[string]string // check and foreign key constraints, which are replaced when they differ
	others  map[string]string // primary key, unique and exclusion constraints
	indexes map[string]string
}

func readCatalog(ctx context.Context, db *sql.DB) (*catalog, error) {
	c := &catalog{checks: map[string]string{}, others: map[string]string{}, indexes: map[string]string{}}
	// NOT NULL constraints (contype 'n') follow the columns, which the
	// planner handles.
	rows, err := db.QueryContext(ctx, `SELECT t.relname, k.conname, k.contype, pg_get_constraintdef(k.oid)
		FROM pg_constraint k
		JOIN pg_class t ON t.oid = k.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = 'public' AND k.contype IN ('c', 'p', 'u', 'f', 'x')`)
	if err != nil {
		return nil, fmt.Errorf("read constraints: %w", err)
	}
	for rows.Next() {
		var table, name, kind, def string
		if err := rows.Scan(&table, &name, &kind, &def); err != nil {
			return nil, fmt.Errorf("read constraints: %w", err)
		}
		if kind == "c" || kind == "f" {
			c.checks[table+"."+name] = def
		} else {
			c.others[table+"."+name] = def
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("read constraints: %w", err)
	}
	rows, err = db.QueryContext(ctx, `SELECT tablename, indexname, indexdef FROM pg_indexes WHERE schemaname = 'public'`)
	if err != nil {
		return nil, fmt.Errorf("read indexes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var table, name, def string
		if err := rows.Scan(&table, &name, &def); err != nil {
			return nil, fmt.Errorf("read indexes: %w", err)
		}
		c.indexes[table+"."+name] = def
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read indexes: %w", err)
	}
	return c, nil
}

// diffCatalog compares current, on which the plan has been applied, with
// desired. It returns the statements that replace the check and foreign
// key constraints that still differ, and an error naming any other
// constraint or index that differs, as no statement is generated for those.
func diffCatalog(ctx context.Context, current, desired *sql.DB) ([]*migrate.Change, error) {
	from, err := readCatalog(ctx, current)
	if err != nil {
		return nil, err
	}
	to, err := readCatalog(ctx, desired)
	if err != nil {
		return nil, err
	}
	var out []*migrate.Change
	for _, key := range sortedKeys(from.checks) {
		if def, ok := to.checks[key]; !ok || def != from.checks[key] {
			table, name, _ := strings.Cut(key, ".")
			out = append(out, &migrate.Change{
				Cmd:     fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", quote(table), quote(name)),
				Comment: fmt.Sprintf("drop constraint %q of table %q", name, table),
			})
		}
	}
	for _, key := range sortedKeys(to.checks) {
		if def, ok := from.checks[key]; !ok || def != to.checks[key] {
			table, name, _ := strings.Cut(key, ".")
			out = append(out, &migrate.Change{
				Cmd:     fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s", quote(table), quote(name), to.checks[key]),
				Comment: fmt.Sprintf("create constraint %q on table %q", name, table),
			})
		}
	}
	var missed []string
	for _, kind := range []struct {
		what     string
		from, to map[string]string
	}{{"constraint", from.others, to.others}, {"index", from.indexes, to.indexes}} {
		for _, key := range sortedKeys(kind.from) {
			if def, ok := kind.to[key]; !ok {
				missed = append(missed, fmt.Sprintf("%s %s: not in schema/", kind.what, key))
			} else if def != kind.from[key] {
				missed = append(missed, fmt.Sprintf("%s %s: %s, schema/ has %s", kind.what, key, kind.from[key], def))
			}
		}
		for _, key := range sortedKeys(kind.to) {
			if _, ok := kind.from[key]; !ok {
				missed = append(missed, fmt.Sprintf("%s %s: missing from migrations/", kind.what, key))
			}
		}
	}
	if len(missed) > 0 {
		return nil, fmt.Errorf("the migration plan leaves differences it cannot repair; write them by hand:\n%s", strings.Join(missed, "\n"))
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// quote quotes a PostgreSQL identifier when needed.
func quote(id string) string {
	plain := id != ""
	for i, r := range id {
		lower := r == '_' || r >= 'a' && r <= 'z'
		digit := i > 0 && r >= '0' && r <= '9'
		if !lower && !digit {
			plain = false
		}
	}
	if plain {
		return id
	}
	return `"` + strings.ReplaceAll(id, `"`, `""`) + `"`
}
