package cli_test

import (
	"bytes"
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/apps/server/internal/cli"
	"github.com/tanvoxyz/tanvo/libs/auth"
)

// outputLine returns the value of the "key: value" line of out.
func outputLine(out, key string) string {
	for line := range strings.Lines(out) {
		if v, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), key+": "); ok {
			return v
		}
	}
	return ""
}

// TestAdminBootstrapNeedsPassword checks that the password is required
// before anything is opened.
func TestAdminBootstrapNeedsPassword(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := cli.Run(t.Context(), []string{"admin", "bootstrap", "--organization-name", "Acme", "--email", "a@acme.test"},
		cli.Env{Environ: []string{"TANVO_DATABASE_URL=postgres://127.0.0.1:1/none"}, Stdout: &out, Stderr: &out})
	if err == nil || !strings.Contains(err.Error(), cli.BootstrapPasswordEnv) {
		t.Errorf("error got = %v, want = a password is required", err)
	}
	err = cli.Run(t.Context(), []string{"admin", "bootstrap", "--email", "a@acme.test"}, cli.Env{Environ: []string{}, Stdout: &out, Stderr: &out})
	if err == nil || !strings.Contains(err.Error(), "organization-name") {
		t.Errorf("error got = %v, want = organization-name required", err)
	}
}

// TestAdminBootstrap bootstraps a migrated database twice: the first run
// prints a generated API key, the second creates nothing.
func TestAdminBootstrap(t *testing.T) {
	dsn := startPostgres(t)
	logs := &lockedBuffer{}
	environ := []string{"TANVO_DATABASE_URL=" + dsn, "TANVO_LOG_FORMAT=text", "OTEL_SDK_DISABLED=true"}
	migrate(t, dsn, environ, logs)
	ctx := t.Context()
	args := []string{"admin", "bootstrap", "--organization-name", "Acme Corp", "--email", "admin@acme.test", "--password-stdin"}

	var out bytes.Buffer
	if err := cli.Run(ctx, args, cli.Env{Environ: environ, Stdin: strings.NewReader("s3cret!\n"), Stdout: &out}); err != nil {
		t.Fatalf("bootstrap error got = %v, want = nil; output %s", err, out.String())
	}
	org, key := outputLine(out.String(), "organization_id"), outputLine(out.String(), "api_key")
	if org == "" || key == "" {
		t.Fatalf("output got = %s, want = organization_id and api_key lines", out.String())
	}

	out.Reset()
	if err := cli.Run(ctx, args, cli.Env{Environ: environ, Stdin: strings.NewReader("s3cret!"), Stdout: &out}); err != nil {
		t.Fatalf("second bootstrap error got = %v, want = nil", err)
	}
	if got := outputLine(out.String(), "organization_id"); got != org || outputLine(out.String(), "api_key") != "" {
		t.Errorf("second output got = %s, want = organization %s and no api_key", out.String(), org)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var got string
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM organizations) || ':' || (SELECT count(*) FROM billing_entities)
		|| ':' || (SELECT count(*) FROM users) || ':' || (SELECT count(*) FROM memberships) || ':' || (SELECT count(*) FROM api_keys)`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "1:1:1:1:1" {
		t.Errorf("organizations:billing_entities:users:memberships:api_keys got = %s, want = 1:1:1:1:1", got)
	}
}

// TestAdminServiceTokenKey generates two keys and prints the public keys
// of a configuration rotating from the second to the first.
func TestAdminServiceTokenKey(t *testing.T) {
	t.Parallel()
	gen := func(id string) string {
		t.Helper()
		var out bytes.Buffer
		if err := cli.Run(t.Context(), []string{"admin", "service-token-key", "generate", "--key-id", id}, cli.Env{Environ: []string{}, Stdout: &out}); err != nil {
			t.Fatalf("generate error got = %v, want = nil", err)
		}
		return strings.TrimSpace(out.String())
	}
	k1, k2 := gen("k1"), gen("k2")
	want := make([]string, 2)
	for i, entry := range []string{k2, k1} {
		k, err := auth.ParseServiceTokenKey(entry)
		if err != nil {
			t.Fatalf("generated key %q: %v", entry, err)
		}
		want[i] = k.Public().String()
	}
	if !strings.HasPrefix(k1, "k1:") || !strings.HasPrefix(k2, "k2:") || k1[3:] == k2[3:] {
		t.Errorf("generated keys got = %q, %q, want = two distinct keys named k1 and k2", k1, k2)
	}
	var out bytes.Buffer
	environ := []string{"TANVO_DATABASE_URL=postgres://127.0.0.1:1/none", "TANVO_SERVICE_TOKEN_KEYS=" + k2 + "," + k1}
	if err := cli.Run(t.Context(), []string{"admin", "service-token-key", "public"}, cli.Env{Environ: environ, Stdout: &out}); err != nil {
		t.Fatalf("public error got = %v, want = nil", err)
	}
	if got := strings.TrimSpace(out.String()); got != strings.Join(want, ",") {
		t.Errorf("public got = %q, want = %q", got, strings.Join(want, ","))
	}
	err := cli.Run(t.Context(), []string{"admin", "service-token-key", "public"}, cli.Env{Environ: environ[:1], Stdout: &out})
	if err == nil || !strings.Contains(err.Error(), "TANVO_SERVICE_TOKEN_KEYS") {
		t.Errorf("unconfigured error got = %v, want = no service token key", err)
	}
}

// TestAdminGrantAnalytics grants a role the export views twice and reads
// them as that role: one organization at a time under row-level
// security, the analytics schema writable, the other tables refused.
func TestAdminGrantAnalytics(t *testing.T) {
	dsn := startPostgres(t)
	logs := &lockedBuffer{}
	environ := []string{"TANVO_DATABASE_URL=" + dsn, "TANVO_LOG_FORMAT=text", "OTEL_SDK_DISABLED=true"}
	migrate(t, dsn, environ, logs)
	ctx := t.Context()
	var out bytes.Buffer
	if err := cli.Run(ctx, []string{"admin", "bootstrap", "--organization-name", "Acme", "--email", "admin@acme.test", "--password-stdin"},
		cli.Env{Environ: environ, Stdin: strings.NewReader("s3cret!\n"), Stdout: &out}); err != nil {
		t.Fatalf("bootstrap error got = %v, want = nil", err)
	}
	org := outputLine(out.String(), "organization_id")

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	const role = "tanvo-analytics" // needs quoting
	for _, q := range []string{
		`CREATE ROLE "tanvo-analytics" LOGIN PASSWORD 'analytics'`,
		`CREATE ROLE bypass LOGIN BYPASSRLS`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	err = cli.Run(ctx, []string{"admin", "grant-analytics", "bypass"}, cli.Env{Environ: environ, Stdout: &out})
	if err == nil || !strings.Contains(err.Error(), "BYPASSRLS") {
		t.Errorf("BYPASSRLS role error got = %v, want = refused", err)
	}
	err = cli.Run(ctx, []string{"admin", "grant-analytics", "tanvo"}, cli.Env{Environ: environ, Stdout: &out})
	if err == nil || !strings.Contains(err.Error(), "bypass") {
		t.Errorf("owner role error got = %v, want = refused", err)
	}
	for range 2 {
		out.Reset()
		if err := cli.Run(ctx, []string{"admin", "grant-analytics", role}, cli.Env{Environ: environ, Stdout: &out}); err != nil {
			t.Fatalf("grant-analytics error got = %v, want = nil; output %s", err, out.String())
		}
	}
	views, tables := strings.Split(outputLine(out.String(), "views"), ","), strings.Split(outputLine(out.String(), "tables"), ",")
	if !slices.Contains(views, "exports_invoices") || !slices.Contains(tables, "invoices") || !slices.Contains(tables, "invoice_error_details") || slices.Contains(tables, "api_keys") {
		t.Errorf("output got = %s, want = the export views and the tables they read", out.String())
	}
	var nViews int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_views WHERE schemaname = current_schema() AND viewname LIKE 'exports\_%'`).Scan(&nViews); err != nil {
		t.Fatal(err)
	}
	if len(views) != nViews {
		t.Errorf("views got = %d, want = %d", len(views), nViews)
	}

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "analytics")
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	count := func(setting string) (int, error) {
		var n int
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if setting != "" {
				if _, err := tx.Exec(ctx, `SELECT set_config('tanvo.organization_id', $1, true)`, setting); err != nil {
					return err
				}
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM exports_billing_entities`).Scan(&n)
		})
		return n, err
	}
	for _, tt := range []struct {
		setting string
		want    int
	}{{"", 0}, {org, 1}, {"0190a000-0000-7000-8000-000000000999", 0}} {
		if got, err := count(tt.setting); err != nil || got != tt.want {
			t.Errorf("billing entities with tanvo.organization_id %q got = %d, %v, want = %d", tt.setting, got, err, tt.want)
		}
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE analytics.rollups (id int); DROP TABLE analytics.rollups`); err != nil {
		t.Errorf("write the analytics schema error got = %v, want = nil", err)
	}
	for _, v := range views {
		if _, err := conn.Exec(ctx, "SELECT * FROM "+pgx.Identifier{v}.Sanitize()+" LIMIT 1"); err != nil {
			t.Errorf("read %s error got = %v, want = nil", v, err)
		}
	}
	for _, q := range []string{`SELECT count(*) FROM api_keys`, `SELECT secrets FROM integrations`} {
		if _, err := conn.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s error got = %v, want = permission denied", q, err)
		}
	}
}
