package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/store"
)

// DefaultAnalyticsSchema is the schema the enterprise analytics service
// owns in the core database (architecture §14.1).
const DefaultAnalyticsSchema = "analytics"

// GrantAnalyticsResult is what [GrantAnalytics] granted.
type GrantAnalyticsResult struct {
	// CoreSchema is the core schema the role may read.
	CoreSchema string
	// Views are the export views the role may read.
	Views []string
	// Tables are the relations the views read, whose columns the views
	// read the role may read too: the views are security_invoker.
	Tables []string
}

// GrantAnalytics prepares role for the enterprise analytics service in
// the core database of cfg: it creates the schema schema owned by role,
// which the service migrates, and grants role USAGE on the core schema
// (the URL's current schema) and SELECT on every exports_* view and on
// the columns of the relations those views read, found in the catalogs
// (pg_rewrite and pg_depend). The views are
// security_invoker, so the role reads the tables under its own row-level
// security: it must be neither a superuser, nor BYPASSRLS, nor the
// tables' owner, and sees the rows of the organization it sets as
// tanvo.organization_id in each transaction. Running it again grants
// what new views need and changes nothing else.
func GrantAnalytics(ctx context.Context, cfg config.Config, role, schema string, tp trace.TracerProvider, mp metric.MeterProvider) (*GrantAnalyticsResult, error) {
	pool, err := openPool(ctx, cfg.DatabaseURL, poolOptions{MaxConns: 2, Name: "admin"}, tp, mp)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	if err := store.CheckSchema(ctx, pool); err != nil {
		return nil, fmt.Errorf("grant analytics: %w", err)
	}
	return grantAnalytics(ctx, pool, role, schema)
}

// exportDependencies lists the export views of the current schema and,
// recursively, the relations their rules read: schema, name, whether it
// is an export view, and the columns the views read of it (none when
// they read no column, as count(*) does).
const exportDependencies = `
WITH RECURSIVE deps(oid) AS (
  SELECT c.oid
  FROM pg_class AS c
  JOIN pg_namespace AS n ON n.oid = c.relnamespace
  WHERE n.nspname = current_schema() AND c.relkind = 'v' AND c.relname LIKE 'exports\_%'
  UNION
  SELECT d.refobjid
  FROM deps
  JOIN pg_rewrite AS r ON r.ev_class = deps.oid
  JOIN pg_depend AS d ON d.classid = 'pg_rewrite'::regclass AND d.objid = r.oid
    AND d.refclassid = 'pg_class'::regclass AND d.refobjid <> deps.oid
),
cols(oid, attname) AS (
  SELECT d.refobjid, a.attname
  FROM deps
  JOIN pg_rewrite AS r ON r.ev_class = deps.oid
  JOIN pg_depend AS d ON d.classid = 'pg_rewrite'::regclass AND d.objid = r.oid
    AND d.refclassid = 'pg_class'::regclass AND d.refobjid <> deps.oid AND d.refobjsubid > 0
  JOIN pg_attribute AS a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
)
SELECT n.nspname, c.relname,
  n.nspname = current_schema() AND c.relkind = 'v' AND c.relname LIKE 'exports\_%',
  ARRAY(SELECT DISTINCT cols.attname::text FROM cols WHERE cols.oid = c.oid ORDER BY 1)
FROM deps
JOIN pg_class AS c ON c.oid = deps.oid
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm')
ORDER BY n.nspname, c.relname`

func grantAnalytics(ctx context.Context, pool *pgxpool.Pool, role, schema string) (*GrantAnalyticsResult, error) {
	if role == "" || schema == "" {
		return nil, errors.New("grant analytics: a role and a schema are required")
	}
	res := &GrantAnalyticsResult{}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var super, bypass bool
		err := tx.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = $1`, role).Scan(&super, &bypass)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("role %q does not exist: create it first (CREATE ROLE … LOGIN)", role)
		}
		if err != nil {
			return fmt.Errorf("read role: %w", err)
		}
		if super || bypass {
			return fmt.Errorf("role %q bypasses row-level security (superuser or BYPASSRLS): use a role without it, so that it reads one organization at a time", role)
		}
		var owns bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_class AS c JOIN pg_namespace AS n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p') AND c.relrowsecurity
			  AND pg_has_role($1::name, c.relowner, 'USAGE'))`, role).Scan(&owns); err != nil {
			return fmt.Errorf("read table owners: %w", err)
		}
		if owns {
			return fmt.Errorf("role %q owns (or is a member of the owner of) the core tables, which bypasses their row-level security: use a separate role", role)
		}
		if err := tx.QueryRow(ctx, `SELECT current_schema()`).Scan(&res.CoreSchema); err != nil {
			return fmt.Errorf("read the core schema: %w", err)
		}
		if res.CoreSchema == schema {
			return fmt.Errorf("the analytics schema %q is the core schema", schema)
		}
		roleID, schemaID := pgx.Identifier{role}.Sanitize(), pgx.Identifier{schema}.Sanitize()
		if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schemaID+" AUTHORIZATION "+roleID); err != nil {
			return fmt.Errorf("create schema %s: %w", schema, err)
		}
		var owner string
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT nspowner::regrole::text, nspowner = (SELECT oid FROM pg_roles WHERE rolname = $2)
			FROM pg_namespace WHERE nspname = $1`, schema, role).Scan(&owner, &owned); err != nil {
			return fmt.Errorf("read schema owner: %w", err)
		}
		if !owned {
			return fmt.Errorf("schema %q exists and is owned by %s, not %s", schema, owner, role)
		}
		if _, err := tx.Exec(ctx, "GRANT USAGE ON SCHEMA "+pgx.Identifier{res.CoreSchema}.Sanitize()+" TO "+roleID); err != nil {
			return fmt.Errorf("grant usage on %s: %w", res.CoreSchema, err)
		}
		rows, err := tx.Query(ctx, exportDependencies)
		if err != nil {
			return fmt.Errorf("list the export views: %w", err)
		}
		type rel struct {
			ns, name string
			export   bool
			columns  []string
		}
		rels, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (rel, error) {
			var x rel
			return x, r.Scan(&x.ns, &x.name, &x.export, &x.columns)
		})
		if err != nil {
			return fmt.Errorf("list the export views: %w", err)
		}
		for _, r := range rels {
			if r.ns != res.CoreSchema {
				return fmt.Errorf("export views read %s.%s outside the core schema %s", r.ns, r.name, res.CoreSchema)
			}
			// The export views are granted whole; the relations they
			// read only the columns they read, so that the role sees no
			// other column (such as sealed credentials).
			privilege := "SELECT"
			if !r.export && len(r.columns) > 0 {
				cols := make([]string, len(r.columns))
				for i, c := range r.columns {
					cols[i] = pgx.Identifier{c}.Sanitize()
				}
				privilege = "SELECT (" + strings.Join(cols, ", ") + ")"
			}
			if _, err := tx.Exec(ctx, "GRANT "+privilege+" ON "+pgx.Identifier{r.ns, r.name}.Sanitize()+" TO "+roleID); err != nil {
				return fmt.Errorf("grant select on %s: %w", r.name, err)
			}
			if r.export {
				res.Views = append(res.Views, r.name)
			} else {
				res.Tables = append(res.Tables, r.name)
			}
		}
		if len(res.Views) == 0 {
			return fmt.Errorf("no export view in schema %s: run tanvo migrate first", res.CoreSchema)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("grant analytics: %w", err)
	}
	return res, nil
}
