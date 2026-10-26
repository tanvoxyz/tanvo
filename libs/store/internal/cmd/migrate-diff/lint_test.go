package main

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"ariga.io/atlas/sql/migrate"

	"github.com/tanvoxyz/tanvo/libs/store/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }

func TestLint(t *testing.T) {
	tests := []struct {
		dir  string
		want []string // codes of the findings
	}{
		{"drop_column", []string{"DS103"}},
		{"drop_column_nolint", nil},
		{"drop_column_file_nolint", nil},
		{"index_blocking", []string{"PG101"}},
		{"index_concurrent", nil},
		{"index_concurrent_in_tx", []string{"PG103"}},
		{"index_concurrent_not_idempotent", []string{"PG104"}},
		{"index_on_new_table", nil},
		{"unique_index", []string{"MF101"}},
		{"unique_index_nolint", nil},
		{"foreign_key", []string{"PG301"}},
		{"foreign_key_not_valid", nil},
		{"column_type", []string{"PG302"}},
		{"set_not_null", []string{"PG303"}},
		{"add_column", []string{"MF103"}},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			db, err := sql.Open("pgx", pgtest.New(t, pgtest.Template{Name: "migrate-lint-empty"}).AdminDSN)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(1)
			dir, err := migrate.NewLocalDir(filepath.Join("testdata", "lint", tt.dir))
			if err != nil {
				t.Fatal(err)
			}
			files, err := dir.Files()
			if err != nil {
				t.Fatal(err)
			}
			findings, err := lint(t.Context(), db, files, func(f migrate.File) bool { return f.Version() != "1" })
			if err != nil {
				t.Fatalf("lint() error got = %v, want = nil", err)
			}
			var got []string
			for _, f := range findings {
				got = append(got, f.code)
				t.Log(f)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("lint() codes got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestNewMigrations(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	migrations := filepath.Join(repo, "migrations")
	write := func(name string) {
		t.Helper()
		if err := os.MkdirAll(migrations, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(migrations, name), []byte("SELECT 1;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("1_merged.sql")
	git("add", ".")
	git("commit", "-q", "-m", "merged")
	git("checkout", "-q", "-b", "feature")
	write("2_committed.sql")
	git("add", ".")
	git("commit", "-q", "-m", "branch")
	write("3_uncommitted.sql")

	dir, err := migrate.NewLocalDir(migrations)
	if err != nil {
		t.Fatal(err)
	}
	files, err := dir.Files()
	if err != nil {
		t.Fatal(err)
	}
	got, err := newMigrations(t.Context(), migrations, files, "main")
	if err != nil {
		t.Fatalf("newMigrations() error got = %v, want = nil", err)
	}
	if want := []string{"2_committed.sql", "3_uncommitted.sql"}; !slices.Equal(got, want) {
		t.Errorf("newMigrations() got = %v, want = %v", got, want)
	}
	if _, err := newMigrations(t.Context(), migrations, files, "no-such-branch"); err == nil {
		t.Error("newMigrations() of an unknown base error got = nil, want = an error")
	}
}
