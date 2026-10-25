package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
)

// view is a view of the public schema as PostgreSQL prints it.
type view struct {
	def     string   // pg_get_viewdef, without the final semicolon
	options string   // reloptions, such as security_invoker=true
	views   []string // the views it reads
	tables  []string // the tables it reads
}

func (v view) create(name string) string {
	var with string
	if v.options != "" {
		with = " WITH (" + v.options + ")"
	}
	return fmt.Sprintf("CREATE VIEW %s%s AS\n%s", quote(name), with, v.def)
}

// readViews returns the views of the public schema by name. Materialized
// views are refused: nothing migrates them.
func readViews(ctx context.Context, db *sql.DB) (map[string]*view, error) {
	out := map[string]*view{}
	rows, err := db.QueryContext(ctx, `SELECT c.relname, c.relkind, pg_get_viewdef(c.oid), coalesce(array_to_string(c.reloptions, ', '), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('v', 'm')`)
	if err != nil {
		return nil, fmt.Errorf("read views: %w", err)
	}
	for rows.Next() {
		var name, kind, def, options string
		if err := rows.Scan(&name, &kind, &def, &options); err != nil {
			return nil, fmt.Errorf("read views: %w", err)
		}
		if kind == "m" {
			_ = rows.Close()
			return nil, fmt.Errorf("materialized view %s: not supported", name)
		}
		out[name] = &view{def: strings.TrimSuffix(strings.TrimSpace(def), ";"), options: options}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("read views: %w", err)
	}
	// A view's rewrite rule depends on the relations it reads.
	rows, err = db.QueryContext(ctx, `SELECT DISTINCT v.relname, t.relname, t.relkind
		FROM pg_depend d
		JOIN pg_rewrite r ON r.oid = d.objid
		JOIN pg_class v ON v.oid = r.ev_class
		JOIN pg_class t ON t.oid = d.refobjid
		JOIN pg_namespace n ON n.oid = v.relnamespace
		WHERE d.classid = 'pg_rewrite'::regclass AND d.refclassid = 'pg_class'::regclass
			AND n.nspname = 'public' AND v.relkind = 'v' AND t.oid <> v.oid`)
	if err != nil {
		return nil, fmt.Errorf("read view dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, rel, kind string
		if err := rows.Scan(&name, &rel, &kind); err != nil {
			return nil, fmt.Errorf("read view dependencies: %w", err)
		}
		v, ok := out[name]
		if !ok {
			continue
		}
		if kind == "v" {
			v.views = append(v.views, rel)
		} else {
			v.tables = append(v.tables, rel)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read view dependencies: %w", err)
	}
	return out, nil
}

// blockingTables returns the tables of plan whose change PostgreSQL refuses
// while a view reads them: a dropped, renamed or retyped column, a dropped
// or renamed table.
func blockingTables(plan *migrate.Plan) map[string]bool {
	out := map[string]bool{}
	for _, c := range plan.Changes {
		switch s := c.Source.(type) {
		case *schema.DropTable:
			out[s.T.Name] = true
		case *schema.RenameTable:
			out[s.From.Name] = true
		case *schema.ModifyTable:
			for _, tc := range s.Changes {
				switch tc.(type) {
				case *schema.DropColumn, *schema.ModifyColumn, *schema.RenameColumn:
					out[s.T.Name] = true
				}
			}
		}
	}
	return out
}

// diffViews returns the statements that bring the views of current to
// those of desired around the table changes of plan: the drops go before
// them and the creations after. A view is dropped when desired lacks it,
// defines it otherwise or would block a change of plan on a table it
// reads, and with it the views that read it; every dropped view desired
// keeps is created again.
func diffViews(ctx context.Context, current, desired *sql.DB, plan *migrate.Plan) (drops, creates []*migrate.Change, err error) {
	from, err := readViews(ctx, current)
	if err != nil {
		return nil, nil, err
	}
	to, err := readViews(ctx, desired)
	if err != nil {
		return nil, nil, err
	}
	blocking := blockingTables(plan)
	dropped := map[string]bool{}
	for name, v := range from {
		w, ok := to[name]
		if !ok || w.def != v.def || w.options != v.options || slices.ContainsFunc(v.tables, func(t string) bool { return blocking[t] }) {
			dropped[name] = true
		}
	}
	// The views reading a dropped view go with it.
	for changed := true; changed; {
		changed = false
		for name, v := range from {
			if !dropped[name] && slices.ContainsFunc(v.views, func(r string) bool { return dropped[r] }) {
				dropped[name], changed = true, true
			}
		}
	}
	order, err := viewOrder(from)
	if err != nil {
		return nil, nil, err
	}
	for _, name := range slices.Backward(order) {
		if dropped[name] {
			drops = append(drops, &migrate.Change{Cmd: "DROP VIEW " + quote(name), Comment: fmt.Sprintf("drop view %q", name)})
		}
	}
	if order, err = viewOrder(to); err != nil {
		return nil, nil, err
	}
	for _, name := range order {
		if _, ok := from[name]; !ok || dropped[name] {
			creates = append(creates, &migrate.Change{Cmd: to[name].create(name), Comment: fmt.Sprintf("create view %q", name)})
		}
	}
	return drops, creates, nil
}

// viewOrder returns the names of views with every view after those it
// reads, else in name order.
func viewOrder(views map[string]*view) ([]string, error) {
	var out []string
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("view %s reads itself", name)
		case 2:
			return nil
		}
		state[name] = 1
		for _, r := range slices.Sorted(slices.Values(views[name].views)) {
			if _, ok := views[r]; ok {
				if err := visit(r); err != nil {
					return err
				}
			}
		}
		state[name] = 2
		out = append(out, name)
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(views)) {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return out, nil
}
