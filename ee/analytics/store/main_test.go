// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package store_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/apd/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanvoxyz/tanvo/ee/analytics/internal/pgtest"
	"github.com/tanvoxyz/tanvo/ee/analytics/period"
	"github.com/tanvoxyz/tanvo/ee/analytics/store"
)

// The PostgreSQL server of the package's tests and the template database
// holding the core migrations; server is nil when none is available, and
// skipReason says why.
var (
	server     *pgtest.Server
	template   string
	skipReason string
)

// readerRole is a login role subject to row-level security.
const readerRole = "tanvo_analytics_reader"

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	if err := setup(ctx); err != nil {
		if !errors.Is(err, pgtest.ErrUnavailable) {
			fmt.Fprintf(os.Stderr, "store tests: %v\n", err)
			cancel()
			if server != nil {
				_ = server.Close(context.Background())
			}
			return 1
		}
		skipReason = err.Error()
	}
	cancel()
	code := m.Run()
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "store tests: %v\n", err)
		}
	}
	return code
}

// setup starts the server and builds the template: the core migrations
// and the reader role's grants on the public schema.
func setup(ctx context.Context) error {
	if testing.Short() {
		return fmt.Errorf("%w: -short", pgtest.ErrUnavailable)
	}
	s, err := pgtest.Start(ctx)
	if err != nil {
		return err
	}
	server = s
	conn, err := s.Connect(ctx, "")
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, fmt.Sprintf(`DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%[1]s') THEN
			CREATE ROLE %[1]s LOGIN PASSWORD '%[1]s' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE;
		END IF;
	END $$`, readerRole))
	_ = conn.Close(ctx)
	if err != nil {
		return fmt.Errorf("create role: %w", err)
	}
	template, err = s.CreateDatabase(ctx, "")
	if err != nil {
		return err
	}
	tconn, err := s.Connect(ctx, template)
	if err != nil {
		return err
	}
	defer func() { _ = tconn.Close(ctx) }()
	if err := pgtest.ApplyCoreMigrations(ctx, tconn); err != nil {
		return err
	}
	if _, err := tconn.Exec(ctx, fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %[1]s;
		GRANT SELECT ON ALL TABLES IN SCHEMA public TO %[1]s`, readerRole)); err != nil {
		return fmt.Errorf("grant: %w", err)
	}
	return tconn.Close(ctx)
}

// env is one test's database and store.
type env struct {
	t     *testing.T
	ctx   context.Context
	db    string
	pool  *pgxpool.Pool
	store *store.Store
	ticks atomic.Int64
}

// clockStart is the store's clock at its first reading; each reading
// advances it by a second.
var clockStart = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// newDB creates t's database from the template and opens a superuser pool
// on it, without the analytics schema.
func newDB(t *testing.T) *env {
	t.Helper()
	if server == nil {
		t.Skipf("no PostgreSQL server: %s (set %s or start a Docker engine)", skipReason, pgtest.DSNEnv)
	}
	ctx := t.Context()
	db, err := server.CreateDatabase(ctx, template)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.DropDatabase(ctx, db); err != nil {
			t.Error(err)
		}
	})
	e := &env{t: t, ctx: ctx, db: db}
	e.pool = e.openPool(server.DSN(db, "", ""))
	return e
}

// newEnv returns newDB's environment with the analytics schema migrated
// and a store on the superuser pool.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := newDB(t)
	if err := store.Migrate(e.ctx, e.pool, quietLog()); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	e.store = e.newStore(e.pool)
	return e
}

func (e *env) openPool(dsn string) *pgxpool.Pool {
	e.t.Helper()
	pool, err := pgxpool.New(e.ctx, dsn)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(pool.Close)
	return pool
}

func (e *env) newStore(pool *pgxpool.Pool) *store.Store {
	return store.New(pool, store.Options{
		Now: func() time.Time { return clockStart.Add(time.Duration(e.ticks.Add(1)) * time.Second) },
		Log: quietLog(),
	})
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// exec runs a statement as the superuser.
func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("exec %.40q…: %v", sql, err)
	}
}

// Dates and instants.

func day(y int, m time.Month, d int) time.Time { return period.Date(y, m, d) }

// at returns the instant of y-m-d hh:mm in UTC.
func at(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, time.UTC)
}

// lastSecond returns the last second of the day before d, the to_datetime
// of a period ending before d.
func lastSecond(d time.Time) time.Time { return d.Add(-time.Second) }

// months returns the monthly periods from the month of from to the month
// of to.
func months(t *testing.T, from, to time.Time) []period.Period {
	t.Helper()
	ps, err := period.Split(from, to.AddDate(0, 1, -1), period.Monthly)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// Seeds: each helper inserts one core row with defaults for what a test
// does not state, and returns its id.

// org is an organization with its default billing entity (no time zone:
// customers without one are in UTC).
type org struct {
	t  *testing.T
	e  *env
	id string
	be string
}

// org adds an organization whose seeds fail t.
func (e *env) org(t *testing.T) *org {
	t.Helper()
	o := &org{t: t, e: e}
	slug := pgtest.RandomName("org_")
	o.id = o.insert(`INSERT INTO organizations (id, name, slug, authentication_methods, audit_logs_retention_days,
		max_wallets_per_customer, document_number_prefix, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $1, '{}', 30, 5, 'ORG', now(), now()) RETURNING id::text`, slug)
	o.be = o.insert(`INSERT INTO billing_entities (organization_id, id, code, name, document_locale, document_numbering,
		document_number_prefix, finalize_zero_amount_invoice, issuing_date_anchor, issuing_date_adjustment, is_default,
		created_at, updated_at)
		VALUES ($1, gen_random_uuid(), 'main', 'Main', 'en', 'per_customer', 'ORG', true, 'current_period_end', 'keep_anchor',
		true, now(), now()) RETURNING id::text`, o.id)
	return o
}

// run runs a statement as the superuser.
func (o *org) run(sql string, args ...any) {
	o.t.Helper()
	if _, err := o.e.pool.Exec(o.e.ctx, sql, args...); err != nil {
		o.t.Fatalf("exec %.40q…: %v", sql, err)
	}
}

// insert runs an INSERT … RETURNING id and returns the id.
func (o *org) insert(sql string, args ...any) string {
	o.t.Helper()
	var id string
	if err := o.e.pool.QueryRow(o.e.ctx, sql, args...).Scan(&id); err != nil {
		o.t.Fatalf("insert %.40q…: %v", sql, err)
	}
	return id
}

// customerOpts describes a customer; ExternalID is required.
type customerOpts struct {
	ExternalID string
	Type       string // company or individual; NULL when empty
	TIN        string // tax identification number; NULL when empty
	Country    string
	Timezone   string // IANA; NULL (the billing entity's, UTC) when empty
}

func (o *org) customer(c customerOpts) string {
	o.t.Helper()
	return o.insert(`INSERT INTO customers (organization_id, id, external_id, billing_entity_id, customer_type, account_type,
		name, tax_identification_number, billing_address_country, timezone, finalize_zero_amount_invoice, created_at, updated_at)
		VALUES ($1, gen_random_uuid(), $2, $3, NULLIF($4, ''), 'customer', $2, NULLIF($5, ''), $6, NULLIF($7, ''), 'inherit',
		now(), now()) RETURNING id::text`,
		o.id, c.ExternalID, o.be, c.Type, c.TIN, c.Country, c.Timezone)
}

func (o *org) plan(code, interval string) string {
	o.t.Helper()
	return o.insert(`INSERT INTO plans (organization_id, id, code, name, billing_interval, amount, currency, status, created_at, updated_at)
		VALUES ($1, gen_random_uuid(), $2, 'Plan ' || $2, $3, 0, 'USD', 'active', now(), now()) RETURNING id::text`,
		o.id, code, interval)
}

// subOpts describes a subscription; Customer and Plan are required.
type subOpts struct {
	Customer, Plan string
	Terminated     time.Time // zero: active
}

func (o *org) subscription(s subOpts) string {
	o.t.Helper()
	status := "active"
	var terminated *time.Time
	if !s.Terminated.IsZero() {
		status, terminated = "terminated", &s.Terminated
	}
	return o.insert(`INSERT INTO subscriptions (organization_id, id, external_id, customer_id, plan_id, billing_time, status,
		subscription_at, started_at, terminated_at, on_termination_invoice, payment_method_type, progressive_billing_disabled,
		consolidate_invoice, skip_invoice_custom_sections, created_at, updated_at)
		VALUES ($1, gen_random_uuid(), $2, $3, $4, 'calendar', $5, '2025-01-01', '2025-01-01', $6, 'generate', 'provider',
		false, false, false, now(), now()) RETURNING id::text`,
		o.id, pgtest.RandomName("sub_"), s.Customer, s.Plan, status, terminated)
}

// invOpts describes an invoice; Customer and Issued are required. The
// status is finalized and the currency USD unless stated.
type invOpts struct {
	Customer string
	Issued   time.Time
	Status   string
	Type     string
	Currency string
	// The credits of the invoice, allocated to its fees by the reports.
	Coupons, CreditNotes, PrepaidGranted, PrepaidPurchased, Progressive int64
	SelfBilled                                                          bool
	DisputeLost                                                         bool
}

func (o *org) invoice(i invOpts) string {
	o.t.Helper()
	if i.Status == "" {
		i.Status = "finalized"
	}
	if i.Type == "" {
		i.Type = "subscription"
	}
	if i.Currency == "" {
		i.Currency = "USD"
	}
	var lost *time.Time
	if i.DisputeLost {
		lost = &i.Issued
	}
	return o.insert(`INSERT INTO invoices (organization_id, id, customer_id, billing_entity_id, invoice_type, status, payment_status,
		number, currency, fees_amount, progressive_billing_credit_amount, coupons_amount, sub_total_excluding_taxes_amount,
		taxes_amount, taxes_rate, sub_total_including_taxes_amount, credit_notes_amount, prepaid_credit_amount,
		prepaid_granted_credit_amount, prepaid_purchased_credit_amount, total_amount, total_paid_amount, offsetted_amount,
		issuing_date, applied_grace_period_days, net_payment_term_days, payment_overdue, payment_dispute_lost_at, self_billed,
		skip_automatic_payment, metadata, ready_for_payment_processing, refresh_requested, created_at, updated_at)
		VALUES ($1, gen_random_uuid(), $2, $3, $4, $5, 'pending', '', $6, 0, $7, $8, 0, 0, 0, 0, $9, $10::bigint + $11::bigint, $10, $11,
		0, 0, 0, $12, 0, 0, false, $13, $14, false, '[]', true, false, now(), now()) RETURNING id::text`,
		o.id, i.Customer, o.be, i.Type, i.Status, i.Currency, i.Progressive, i.Coupons, i.CreditNotes,
		i.PrepaidGranted, i.PrepaidPurchased, i.Issued, lost, i.SelfBilled)
}

// feeOpts describes a fee; Type and Amount are required. From and To are
// the service period (to_datetime is the last second of To's day before);
// charge fees store it as their charges period. Currency is USD unless
// stated.
type feeOpts struct {
	Invoice, Subscription string
	Type                  string
	Amount                int64
	Coupons               string // precise_coupons_amount
	From, To              time.Time
	Currency              string
	PayInAdvance          bool
	Charge, ChargeFilter  string
	GroupedBy             string // JSON object
	Created               time.Time
	Deleted               bool
}

func (o *org) fee(f feeOpts) string {
	o.t.Helper()
	if f.Currency == "" {
		f.Currency = "USD"
	}
	if f.Coupons == "" {
		f.Coupons = "0"
	}
	if f.GroupedBy == "" {
		f.GroupedBy = "{}"
	}
	if f.Created.IsZero() {
		f.Created = time.Now()
	}
	var from, to, chFrom, chTo time.Time // zero instants: 0001-01-01 UTC
	if !f.From.IsZero() {
		from, to = f.From, lastSecond(f.To)
		if f.Type == "charge" {
			chFrom, chTo = from, to
		}
	}
	var deleted *time.Time
	if f.Deleted {
		deleted = &f.Created
	}
	return o.insert(`INSERT INTO fees (organization_id, id, invoice_id, subscription_id, fee_type, charge_id, charge_filter_id,
		grouped_by, amount, precise_amount, unit_amount, precise_unit_amount, units, total_aggregated_units, events_count,
		taxes_amount, taxes_precise_amount, taxes_rate, taxes_base_rate, precise_coupons_amount, precise_credit_notes_amount,
		currency, pay_in_advance, invoiceable, from_datetime, to_datetime, charges_from_datetime, charges_to_datetime,
		billing_timestamp, charges_duration_days, fixed_charges_from_datetime, fixed_charges_to_datetime,
		fixed_charges_duration_days, applied_taxes, presentation_breakdowns, created_at, updated_at, deleted_at)
		VALUES ($1, gen_random_uuid(), NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, NULLIF($5, '')::uuid,
		NULLIF($6, '')::uuid, $7::jsonb, $8::bigint, $8::numeric, 0, 0, 1, 1, 1, 0, 0, 0, 0, $9::numeric, 0, $10, $11, true, $12, $13, $14, $15,
		$12, 0, '0001-01-01 00:00:00+00', '0001-01-01 00:00:00+00', 0, '[]', '[]', $16, now(), $17) RETURNING id::text`,
		o.id, f.Invoice, f.Subscription, f.Type, f.Charge, f.ChargeFilter, f.GroupedBy, f.Amount, f.Coupons,
		f.Currency, f.PayInAdvance, from, to, chFrom, chTo, f.Created, deleted)
}

// subscriptionFee adds a finalized invoice issued on from with one
// subscription fee of amount over [from, to), and returns the fee.
func (o *org) subscriptionFee(customer, sub string, amount int64, from, to time.Time) string {
	o.t.Helper()
	inv := o.invoice(invOpts{Customer: customer, Issued: from})
	return o.fee(feeOpts{Invoice: inv, Subscription: sub, Type: "subscription", Amount: amount, From: from, To: to})
}

func (o *org) metric(code string) string {
	o.t.Helper()
	return o.insert(`INSERT INTO billable_metrics (organization_id, id, code, name, aggregation_type, created_at, updated_at)
		VALUES ($1, gen_random_uuid(), $2, $2, 'count', now(), now()) RETURNING id::text`, o.id, code)
}

func (o *org) charge(plan, metric string) string {
	o.t.Helper()
	return o.insert(`INSERT INTO charges (organization_id, id, plan_id, code, billable_metric_id, charge_model, properties,
		created_at, updated_at)
		VALUES ($1, gen_random_uuid(), $2, $3, $4, 'standard', '{}', now(), now()) RETURNING id::text`,
		o.id, plan, pgtest.RandomName("ch_"), metric)
}

// chargeFilter adds a filter of charge allowing values, a JSON object of
// keys to arrays of values.
func (o *org) chargeFilter(charge, values string) string {
	o.t.Helper()
	return o.insert(`INSERT INTO charge_filters (organization_id, id, charge_id, filter_values, properties, position, created_at)
		VALUES ($1, gen_random_uuid(), $2, $3::jsonb, '{}', 0, now()) RETURNING id::text`, o.id, charge, values)
}

func (o *org) wallet(customer, currency string) string {
	o.t.Helper()
	return o.insert(`INSERT INTO wallets (organization_id, id, customer_id, status, currency, rate_amount, priority, created_at, updated_at)
		VALUES ($1, gen_random_uuid(), $2, 'active', $3, 1, 1, now(), now()) RETURNING id::text`, o.id, customer, currency)
}

// walletTx adds a wallet transaction of amount for credits, settled at
// settled unless status says otherwise.
func (o *org) walletTx(wallet, typ, txStatus, status string, amount int64, credits string, settled time.Time) {
	o.t.Helper()
	var at *time.Time
	if status == "settled" {
		at = &settled
	}
	o.run(`INSERT INTO wallet_transactions (organization_id, id, wallet_id, transaction_type, transaction_status, status,
		source, currency, amount, credit_amount, priority, settled_at, created_at, updated_at)
		SELECT $1, gen_random_uuid(), $2, $3, $4, $5, 'manual', w.currency, $6, $7::numeric, 1, $8, now(), now()
		FROM wallets w WHERE w.organization_id = $1 AND w.id = $2`,
		o.id, wallet, typ, txStatus, status, amount, credits, at)
}

// dailyUsage adds the daily usage of sub on d with usage_diff diff.
func (o *org) dailyUsage(customer, sub string, d time.Time, diff string) {
	o.t.Helper()
	o.run(`INSERT INTO daily_usages (organization_id, id, customer_id, subscription_id, external_subscription_id, usage,
		usage_diff, from_datetime, to_datetime, refreshed_at, usage_date, created_at, updated_at)
		SELECT $1, gen_random_uuid(), $2, $3, s.external_id, '{}', $4::jsonb, $5, $5, $5, $6, now(), now()
		FROM subscriptions s WHERE s.organization_id = $1 AND s.id = $3`,
		o.id, customer, sub, diff, d, d)
}

// Comparisons.

// round6 rounds every decimal string field of v, a pointer to a struct
// (nested structs included), to 6 decimals without trailing zeros, so
// that numeric results compare by value.
func round6(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		round6(v.Elem())
	case reflect.Struct:
		for i := range v.NumField() {
			round6(v.Field(i))
		}
	case reflect.String:
		if s, ok := norm(v.String()); ok {
			v.SetString(s)
		}
	}
}

// norm returns the decimal s rounded to 6 decimals, without trailing
// zeros; ok is false when s is not a decimal.
func norm(s string) (string, bool) {
	d, _, err := apd.NewFromString(s)
	if err != nil {
		return s, false
	}
	var r apd.Decimal
	if _, err := apd.BaseContext.WithPrecision(40).Quantize(&r, d, -6); err != nil {
		return s, false
	}
	r.Reduce(&r)
	if r.IsZero() {
		return "0", true
	}
	return r.Text('f'), true
}

// normalized returns rows with their decimal strings rounded by round6.
func normalized[R any](rows []R) []R {
	out := make([]R, len(rows))
	copy(out, rows)
	for i := range out {
		round6(reflect.ValueOf(&out[i]))
	}
	return out
}

// checkRows fails t unless got and want hold the same rows, decimals
// compared by value to 6 decimals.
func checkRows[R any](t *testing.T, name string, got, want []R) {
	t.Helper()
	g, w := normalized(got), normalized(want)
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\ngot  = %+v\nwant = %+v", name, g, w)
	}
}
