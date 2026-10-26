package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlcheck"
	"ariga.io/atlas/sql/sqlcheck/condrop"
	"ariga.io/atlas/sql/sqlcheck/datadepend"
	"ariga.io/atlas/sql/sqlcheck/destructive"
	"ariga.io/atlas/sql/sqlcheck/incompatible"
)

// A finding is a diagnostic of a linted migration that no nolint directive
// covers.
type finding struct {
	file     string
	line     int
	analyzer string
	code     string
	text     string
}

func (f finding) String() string {
	return fmt.Sprintf("%s:%d: %s (%s): %s", f.file, f.line, f.code, f.analyzer, f.text)
}

// directiveFile is the part of *migrate.LocalFile that reads the file
// directives of its header.
type directiveFile interface {
	Directive(name string) []string
}

// transactional reports whether f runs in a transaction, as store.Migrate
// decides: unless its header has "-- atlas:txmode none".
func transactional(f migrate.File) (bool, error) {
	d, ok := f.(directiveFile)
	if !ok {
		return true, nil
	}
	modes := d.Directive("txmode")
	switch {
	case len(modes) == 0:
		return true, nil
	case len(modes) > 1:
		return false, fmt.Errorf("%s: more than one atlas:txmode directive", f.Name())
	case modes[0] == "file":
		return true, nil
	case modes[0] == "none":
		return false, nil
	default:
		return false, fmt.Errorf("%s: unsupported atlas:txmode %q (want file or none)", f.Name(), modes[0])
	}
}

// execFile executes f on db as store.Migrate does: as a whole, or one
// statement at a time for a file without a transaction (CONCURRENTLY
// cannot run in the implicit transaction of a multi-statement query).
func execFile(ctx context.Context, db *sql.DB, f migrate.File) error {
	tx, err := transactional(f)
	if err != nil {
		return err
	}
	if tx {
		if _, err := db.ExecContext(ctx, string(f.Bytes())); err != nil {
			return fmt.Errorf("%s: %w", f.Name(), err)
		}
		return nil
	}
	stmts, err := f.StmtDecls()
	if err != nil {
		return fmt.Errorf("%s: %w", f.Name(), err)
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s.Text); err != nil {
			return fmt.Errorf("%s: %w", f.Name(), err)
		}
	}
	return nil
}

// newMigrations returns the names of the files of dir that the merge base
// of HEAD and base does not have: the migrations of the branch, which the
// lint checks. Merged migrations are never modified, so they are never
// linted again. An empty base is $NX_BASE (set in CI), or else origin/main.
func newMigrations(ctx context.Context, dir string, files []migrate.File, base string) ([]string, error) {
	if base == "" {
		base = os.Getenv("NX_BASE")
	}
	if base == "" {
		base = "origin/main"
	}
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(string(out)), nil
	}
	mergeBase, err := git("merge-base", "HEAD", base)
	if err != nil {
		return nil, fmt.Errorf("find the merge base with %s: %w", base, err)
	}
	listed, err := git("ls-tree", "--name-only", mergeBase, "--", ".")
	if err != nil {
		return nil, fmt.Errorf("list the migrations of %s: %w", mergeBase, err)
	}
	merged := map[string]bool{}
	for _, name := range strings.Split(listed, "\n") {
		merged[filepath.Base(name)] = true
	}
	var out []string
	for _, f := range files {
		if !merged[f.Name()] {
			out = append(out, f.Name())
		}
	}
	return out, nil
}

// analyzers returns the analyzers of the lint: Atlas's destructive,
// backward-incompatible, data-dependent and constraint-drop checks, and
// the lock checks of this command. Each only reports; every finding fails
// the lint unless a nolint directive covers it.
func analyzers() ([]sqlcheck.NamedAnalyzer, error) {
	report := new(bool)
	ds, err := destructive.New(nil)
	if err != nil {
		return nil, err
	}
	ds.Error = report
	bc, err := incompatible.New(nil)
	if err != nil {
		return nil, err
	}
	bc.Error = report
	dd, err := datadepend.New(nil, datadepend.Handler{AddNotNull: addNotNull})
	if err != nil {
		return nil, err
	}
	dd.Error = report
	cd, err := condrop.New(nil)
	if err != nil {
		return nil, err
	}
	cd.Error = report
	return []sqlcheck.NamedAnalyzer{ds, bc, dd, cd, concurrentIndex{}, blockingTableLock{}}, nil
}

// addNotNull reports a non-nullable column without a default added to an
// existing table, as Atlas's PostgreSQL analyzers do.
func addNotNull(p *datadepend.ColumnPass) ([]sqlcheck.Diagnostic, error) {
	t, err := postgres.FormatType(p.Column.Type.Type)
	if err != nil {
		return nil, err
	}
	return []sqlcheck.Diagnostic{{
		Pos:  p.Change.Stmt.Pos,
		Code: "MF103",
		Text: fmt.Sprintf("Adding a non-nullable %q column %q will fail in case table %q is not empty", t, p.Column.Name, p.Table.Name),
	}}, nil
}

// lint replays on db, an empty database, the files of dir in order and
// analyzes those that lint selects: each statement is executed and the
// public schema inspected after it, which gives the changes the analyzers
// read. It returns the findings no nolint directive covers.
func lint(ctx context.Context, db *sql.DB, files []migrate.File, selected func(migrate.File) bool) ([]finding, error) {
	drv, err := postgres.Open(db)
	if err != nil {
		return nil, fmt.Errorf("open driver: %w", err)
	}
	azs, err := analyzers()
	if err != nil {
		return nil, err
	}
	inspect := func() (*schema.Schema, error) {
		s, err := drv.InspectSchema(ctx, "public", nil)
		if err != nil {
			return nil, fmt.Errorf("inspect: %w", err)
		}
		return s, nil
	}
	var out []finding
	for _, f := range files {
		if !selected(f) {
			if err := execFile(ctx, db, f); err != nil {
				return nil, fmt.Errorf("replay %w", err)
			}
			continue
		}
		stmts, err := f.StmtDecls()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name(), err)
		}
		file := &sqlcheck.File{File: f}
		before, err := inspect()
		if err != nil {
			return nil, err
		}
		for _, s := range stmts {
			if _, err := db.ExecContext(ctx, s.Text); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", f.Name(), line(f, s.Pos), err)
			}
			after, err := inspect()
			if err != nil {
				return nil, err
			}
			changes, err := drv.SchemaDiff(before, after)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: diff: %w", f.Name(), line(f, s.Pos), err)
			}
			file.Changes = append(file.Changes, &sqlcheck.Change{Changes: changes, Stmt: s})
			before = after
		}
		var fileNolint []string
		if d, ok := f.(directiveFile); ok {
			fileNolint = d.Directive("nolint")
		}
		for _, az := range azs {
			var diags []sqlcheck.Diagnostic
			pass := &sqlcheck.Pass{File: file, Reporter: sqlcheck.ReportWriterFunc(func(r sqlcheck.Report) {
				diags = append(diags, r.Diagnostics...)
			})}
			if err := az.Analyze(ctx, pass); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", f.Name(), az.Name(), err)
			}
			for _, d := range diags {
				i := slices.IndexFunc(stmts, func(s *migrate.Stmt) bool { return s.Pos == d.Pos })
				var stmtNolint []string
				if i >= 0 {
					stmtNolint = stmts[i].Directive("nolint")
				}
				if covered(fileNolint, az.Name(), d.Code) || covered(stmtNolint, az.Name(), d.Code) {
					continue
				}
				out = append(out, finding{file: f.Name(), line: line(f, d.Pos), analyzer: az.Name(), code: d.Code, text: d.Text})
			}
		}
	}
	return out, nil
}

// covered reports whether one of the nolint directives covers the
// diagnostic code of the analyzer: a directive without arguments covers
// everything, otherwise its arguments name analyzers or codes.
func covered(directives []string, analyzer, code string) bool {
	for _, d := range directives {
		args := strings.Fields(d)
		if len(args) == 0 || slices.Contains(args, analyzer) || slices.Contains(args, code) {
			return true
		}
	}
	return false
}

// line returns the line of the byte offset pos of f, from 1.
func line(f migrate.File, pos int) int {
	b := f.Bytes()
	pos = min(max(pos, 0), len(b))
	return bytes.Count(b[:pos], []byte("\n")) + 1
}

var (
	reConcurrently = regexp.MustCompile(`(?i)\bCONCURRENTLY\b`)
	reCreateIndex  = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\b`)
	reIfNotExists  = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\b`)
	reDropIndex    = regexp.MustCompile(`(?is)^\s*DROP\s+INDEX\s+CONCURRENTLY\b`)
	reDropIfExists = regexp.MustCompile(`(?is)^\s*DROP\s+INDEX\s+CONCURRENTLY\s+IF\s+EXISTS\b`)
	reUsingIndex   = regexp.MustCompile(`(?i)\bUSING\s+INDEX\b`)
	reNotValid     = regexp.MustCompile(`(?i)\bNOT\s+VALID\b`)
)

// existingTables calls fn for each change of an existing table (one the
// file did not create) made by the statements of the pass.
func existingTables(p *sqlcheck.Pass, fn func(sc *sqlcheck.Change, t *schema.Table, c schema.Change)) {
	for _, sc := range p.File.Changes {
		for _, c := range sc.Changes {
			m, ok := c.(*schema.ModifyTable)
			if !ok || p.File.TableSpan(m.T)&sqlcheck.SpanAdded != 0 {
				continue
			}
			for _, mc := range m.Changes {
				fn(sc, m.T, mc)
			}
		}
	}
}

// concurrentIndex checks that indexes of existing tables are built and
// dropped CONCURRENTLY, which is possible only in a file without a
// transaction, and that such a file can run again after a failure.
//
//   - PG101: an index is created on an existing table without CONCURRENTLY;
//     the build holds a SHARE lock, blocking the table's writes throughout.
//   - PG102: an index of an existing table is dropped without CONCURRENTLY,
//     which takes an ACCESS EXCLUSIVE lock.
//   - PG103: CONCURRENTLY in a transactional file, where PostgreSQL refuses
//     it; the file needs the header directive "-- atlas:txmode none".
//   - PG104: a CREATE INDEX CONCURRENTLY without IF NOT EXISTS, or a DROP
//     INDEX CONCURRENTLY without IF EXISTS, in a file without a transaction:
//     a failed file runs again from its first statement.
type concurrentIndex struct{}

func (concurrentIndex) Name() string { return "concurrent_index" }

func (concurrentIndex) Analyze(_ context.Context, p *sqlcheck.Pass) error {
	tx, err := transactional(p.File.File)
	if err != nil {
		return err
	}
	var diags []sqlcheck.Diagnostic
	report := func(pos int, code, format string, args ...any) {
		diags = append(diags, sqlcheck.Diagnostic{Pos: pos, Code: code, Text: fmt.Sprintf(format, args...)})
	}
	for _, sc := range p.File.Changes {
		text := sc.Stmt.Text
		switch {
		case tx && reConcurrently.MatchString(text):
			report(sc.Stmt.Pos, "PG103", "CONCURRENTLY cannot run in a transaction; start the file with the directive -- atlas:txmode none")
		case !tx && reCreateIndex.MatchString(text) && !reIfNotExists.MatchString(text):
			report(sc.Stmt.Pos, "PG104", "CREATE INDEX CONCURRENTLY in a file without a transaction needs IF NOT EXISTS, as a failed file runs again")
		case !tx && reDropIndex.MatchString(text) && !reDropIfExists.MatchString(text):
			report(sc.Stmt.Pos, "PG104", "DROP INDEX CONCURRENTLY in a file without a transaction needs IF EXISTS, as a failed file runs again")
		}
	}
	existingTables(p, func(sc *sqlcheck.Change, t *schema.Table, c schema.Change) {
		text := sc.Stmt.Text
		if reConcurrently.MatchString(text) || reUsingIndex.MatchString(text) {
			return
		}
		switch c := c.(type) {
		case *schema.AddIndex:
			report(sc.Stmt.Pos, "PG101", "Creating index %q on existing table %q without CONCURRENTLY blocks its writes during the build", c.I.Name, t.Name)
		case *schema.DropIndex:
			report(sc.Stmt.Pos, "PG102", "Dropping index %q of existing table %q without CONCURRENTLY locks the table", c.I.Name, t.Name)
		}
	})
	if len(diags) > 0 {
		p.Reporter.WriteReport(sqlcheck.Report{Text: "concurrent index changes detected", Diagnostics: diags})
	}
	return nil
}

// blockingTableLock checks the changes of existing tables that hold a lock
// blocking their writes (or reads) for a time that grows with the table.
//
//   - PG301: a check or foreign key constraint is added without NOT VALID,
//     so it is validated under the lock; add it NOT VALID, then VALIDATE
//     CONSTRAINT in a later statement, which takes a weaker lock.
//   - PG302: a column type changes, which rewrites the table (or scans it)
//     under an ACCESS EXCLUSIVE lock.
//   - PG303: a column becomes NOT NULL, which scans the table under an
//     ACCESS EXCLUSIVE lock unless a valid CHECK (column IS NOT NULL) exists.
type blockingTableLock struct{}

func (blockingTableLock) Name() string { return "blocking_table_lock" }

func (blockingTableLock) Analyze(_ context.Context, p *sqlcheck.Pass) error {
	var diags []sqlcheck.Diagnostic
	existingTables(p, func(sc *sqlcheck.Change, t *schema.Table, c schema.Change) {
		report := func(code, format string, args ...any) {
			diags = append(diags, sqlcheck.Diagnostic{Pos: sc.Stmt.Pos, Code: code, Text: fmt.Sprintf(format, args...)})
		}
		notValid := reNotValid.MatchString(sc.Stmt.Text)
		switch c := c.(type) {
		case *schema.AddForeignKey:
			if !notValid {
				report("PG301", "Adding foreign key %q to existing table %q validates it under a lock; add it NOT VALID and VALIDATE CONSTRAINT separately", c.F.Symbol, t.Name)
			}
		case *schema.AddCheck:
			if !notValid {
				report("PG301", "Adding check %q to existing table %q validates it under an ACCESS EXCLUSIVE lock; add it NOT VALID and VALIDATE CONSTRAINT separately", c.C.Name, t.Name)
			}
		case *schema.ModifyColumn:
			if c.Change.Is(schema.ChangeType) {
				report("PG302", "Changing the type of column %q of existing table %q rewrites the table under an ACCESS EXCLUSIVE lock", c.To.Name, t.Name)
			}
			if c.Change.Is(schema.ChangeNull) && c.From.Type.Null && !c.To.Type.Null {
				report("PG303", "Setting column %q of existing table %q NOT NULL scans the table under an ACCESS EXCLUSIVE lock", c.To.Name, t.Name)
			}
		}
	})
	if len(diags) > 0 {
		p.Reporter.WriteReport(sqlcheck.Report{Text: "blocking table locks detected", Diagnostics: diags})
	}
	return nil
}
