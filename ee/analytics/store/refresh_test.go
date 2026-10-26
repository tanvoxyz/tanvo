// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package store_test

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tanvoxyz/tanvo/ee/analytics/store"
)

// refreshedAt returns the refresh time of org, or ok false when org was
// never built.
func (e *env) refreshedAt(t *testing.T, org string) (at time.Time, ok bool) {
	t.Helper()
	err := e.pool.QueryRow(e.ctx, "SELECT refreshed_at FROM analytics.refresh_state WHERE organization_id = $1", org).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return at, true
}

// ending returns the USD MRR of org at the end of January 2026.
func (e *env) ending(t *testing.T, s *store.Store, org string) string {
	t.Helper()
	jan := day(2026, 1, 1)
	got, err := s.MRR(e.ctx, org, store.Filter{Periods: months(t, jan, jan), Currency: "USD"})
	if err != nil {
		t.Fatalf("MRR() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("MRR() got = %d rows, want = 1", len(got))
	}
	n, _ := norm(got[0].Ending)
	return n
}

func TestRefresh(t *testing.T) {
	e := newEnv(t)
	jan, feb := day(2026, 1, 1), day(2026, 2, 1)
	o := e.org(t)
	c := o.customer(customerOpts{ExternalID: "a"})
	s := o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")})
	fee := o.subscriptionFee(c, s, 3000, jan, feb)
	// Another organization, never reported on.
	other := e.org(t)
	oc := other.customer(customerOpts{ExternalID: "a"})
	other.subscriptionFee(oc, other.subscription(subOpts{Customer: oc, Plan: other.plan("basic", "monthly")}), 5000, jan, feb)

	if _, ok := e.refreshedAt(t, o.id); ok {
		t.Fatalf("refresh state before the first report: got = present, want = absent")
	}
	// The first report builds the organization.
	if got := e.ending(t, e.store, o.id); got != "3000" {
		t.Errorf("MRR on the first report: got = %s, want = 3000", got)
	}
	built, ok := e.refreshedAt(t, o.id)
	if !ok {
		t.Fatalf("refresh state after the first report: got = absent, want = present")
	}

	// Nothing changed: no rebuild.
	if err := e.store.Refresh(e.ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if at, _ := e.refreshedAt(t, o.id); !at.Equal(built) {
		t.Errorf("refreshed_at without a change: got = %v, want = %v", at, built)
	}
	if _, ok := e.refreshedAt(t, other.id); ok {
		t.Errorf("refresh state of an organization never built: got = present, want = absent")
	}

	// A changed fee moves the watermark: Refresh rebuilds.
	e.exec("UPDATE fees SET amount = 4000, precise_amount = 4000, updated_at = clock_timestamp() WHERE organization_id = $1 AND id = $2", o.id, fee)
	if err := e.store.Refresh(e.ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	rebuilt, _ := e.refreshedAt(t, o.id)
	if !rebuilt.After(built) {
		t.Errorf("refreshed_at after a fee change: got = %v, want = after %v", rebuilt, built)
	}
	if got := e.ending(t, e.store, o.id); got != "4000" {
		t.Errorf("MRR after a fee change: got = %s, want = 4000", got)
	}

	// A voided invoice moves it too: its fee no longer counts.
	e.exec(`UPDATE invoices SET status = 'voided', updated_at = clock_timestamp()
		WHERE organization_id = $1 AND id = (SELECT invoice_id FROM fees WHERE organization_id = $1 AND id = $2)`, o.id, fee)
	if err := e.store.Refresh(e.ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if at, _ := e.refreshedAt(t, o.id); !at.After(rebuilt) {
		t.Errorf("refreshed_at after a void: got = %v, want = after %v", at, rebuilt)
	}
	if got := e.ending(t, e.store, o.id); got != "0" {
		t.Errorf("MRR after a void: got = %s, want = 0", got)
	}
}

func TestRefreshMarksDeletedPlans(t *testing.T) {
	e := newEnv(t)
	jan, feb := day(2026, 1, 1), day(2026, 2, 1)
	o := e.org(t)
	plan := o.plan("old", "monthly")
	c := o.customer(customerOpts{ExternalID: "a"})
	o.subscriptionFee(c, o.subscription(subOpts{Customer: c, Plan: plan}), 3000, jan, feb)
	if err := e.store.Rebuild(e.ctx, o.id); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	// The export views drop deleted plans, which moves no watermark;
	// Refresh still rebuilds, and the analytics keep the plan, marked
	// deleted, for the reports by plan.
	e.exec("UPDATE plans SET status = 'deleted', updated_at = clock_timestamp() WHERE organization_id = $1 AND id = $2", o.id, plan)
	if err := e.store.Refresh(e.ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	got, _, err := e.store.MRRByPlan(e.ctx, o.id, store.Filter{Periods: months(t, jan, jan)}, store.Page{Size: 10})
	if err != nil {
		t.Fatalf("MRRByPlan() error = %v", err)
	}
	want := []store.PlanMRR{{
		Plan:     store.Plan{ID: plan, Code: "old", Name: "Plan old", Interval: "monthly", Deleted: true},
		Currency: "USD", MRR: "3000", Share: "1", Customers: 1, CustomersShare: "1",
	}}
	checkRows(t, "MRRByPlan()", got, want)
}

// TestRowLevelSecurity runs the store as a role subject to row-level
// security: the reports see the rows of their organization, which they
// would not without tanvo.organization_id, and none of another's.
func TestRowLevelSecurity(t *testing.T) {
	e := newEnv(t)
	e.exec("GRANT USAGE, CREATE ON SCHEMA analytics TO " + readerRole)
	e.exec("GRANT ALL ON ALL TABLES IN SCHEMA analytics TO " + readerRole)
	jan, feb := day(2026, 1, 1), day(2026, 2, 1)
	amounts := map[string]int64{}
	var orgs []*org
	for _, amount := range []int64{3000, 7000} {
		o := e.org(t)
		c := o.customer(customerOpts{ExternalID: "same"})
		o.subscriptionFee(c, o.subscription(subOpts{Customer: c, Plan: o.plan("basic", "monthly")}), amount, jan, feb)
		amounts[o.id] = amount
		orgs = append(orgs, o)
	}
	reader := e.openPool(server.DSN(e.db, readerRole, readerRole))
	var visible int
	if err := reader.QueryRow(e.ctx, "SELECT count(*) FROM exports_fees").Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Fatalf("fees the reader sees without an organization: got = %d, want = 0", visible)
	}
	s := e.newStore(reader)
	f := store.Filter{Periods: months(t, jan, jan)}
	for _, o := range orgs {
		want, _ := norm(strconv.FormatInt(amounts[o.id], 10))
		// Built by the reader on the first report.
		if got := e.ending(t, s, o.id); got != want {
			t.Errorf("MRR of %s: got = %s, want = %s", o.id, got, want)
		}
		rev, err := s.Revenue(e.ctx, o.id, f)
		if err != nil {
			t.Fatalf("Revenue() error = %v", err)
		}
		if len(rev) != 1 || must(norm(rev[0].Gross)) != want {
			t.Errorf("Revenue() of %s: got = %+v, want = one USD row of gross %s", o.id, rev, want)
		}
		list, total, err := s.RevenueByCustomer(e.ctx, o.id, f, store.ByGross, store.Page{Size: 10})
		if err != nil {
			t.Fatalf("RevenueByCustomer() error = %v", err)
		}
		if total != 1 || len(list) != 1 || must(norm(list[0].Gross)) != want {
			t.Errorf("RevenueByCustomer() of %s: got = %+v (total %d), want = one row of gross %s", o.id, list, total, want)
		}
	}
	if err := s.Refresh(e.ctx); err != nil {
		t.Errorf("Refresh() as the reader error = %v", err)
	}
}

func must(s string, _ bool) string { return s }
