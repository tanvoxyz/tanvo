package exports_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/exports"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var now = time.Date(2024, 7, 1, 10, 0, 0, 0, time.UTC)

// blobs keeps files in memory and links them under files.test.
type blobs struct {
	mu    sync.Mutex
	files map[core.BlobRef][]byte
	types map[core.BlobRef]string
}

var _ port.BlobStore = (*blobs)(nil)

func (b *blobs) Put(_ context.Context, _ core.OrganizationID, name, contentType string, body io.Reader) (core.BlobRef, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.files == nil {
		b.files, b.types = map[core.BlobRef][]byte{}, map[core.BlobRef]string{}
	}
	ref := core.BlobRef(name)
	b.files[ref], b.types[ref] = data, contentType
	return ref, nil
}

func (b *blobs) Get(_ context.Context, _ core.OrganizationID, ref core.BlobRef) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.files[ref]
	if !ok {
		return nil, core.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *blobs) Delete(context.Context, core.OrganizationID, core.BlobRef) error { return nil }

func (b *blobs) URL(_ context.Context, _ core.OrganizationID, ref core.BlobRef) (string, error) {
	return "https://files.test/" + string(ref), nil
}

type env struct {
	*billingtest.Harness
	svc    *exports.Service
	blobs  *blobs
	member core.MembershipID
}

func newEnv(t *testing.T, f exports.Filters) *env {
	t.Helper()
	h := billingtest.New(t, now)
	b := &blobs{}
	return &env{Harness: h, svc: exports.New(h.Deps, b, f), blobs: b, member: core.NewID[core.MembershipID](h.IDs)}
}

// create stores an export of resource directly, pending.
func (e *env) create(resource export.Resource, opts ...func(*export.DataExport)) *export.DataExport {
	e.T.Helper()
	x := export.New(core.NewID[core.DataExportID](e.IDs), e.OrgID(), e.member, resource, []byte(`{}`), e.Now())
	for _, o := range opts {
		o(x)
	}
	e.Do(func(ctx context.Context, r port.Repos) error { return r.DataExports.Create(ctx, x) })
	return x
}

func (e *env) export(id core.DataExportID) *export.DataExport {
	e.T.Helper()
	return billingtest.Read(e.Harness, func(ctx context.Context, r port.Repos) (*export.DataExport, error) {
		return r.DataExports.Get(ctx, e.OrgID(), id)
	})
}

func (e *env) parts(id core.DataExportID) []*export.Part {
	e.T.Helper()
	return billingtest.Read(e.Harness, func(ctx context.Context, r port.Repos) ([]*export.Part, error) {
		return r.DataExportParts.ListByExport(ctx, e.OrgID(), id)
	})
}

// part stores a part of x holding ids.
func (e *env) part(x *export.DataExport, index int, ids ...uuid.UUID) *export.Part {
	e.T.Helper()
	p := &export.Part{
		ID: core.NewID[core.DataExportPartID](e.IDs), OrganizationID: e.OrgID(), DataExportID: x.ID, Index: index,
		ObjectIDs: ids, CreatedAt: e.Now(), UpdatedAt: e.Now(),
	}
	e.Do(func(ctx context.Context, r port.Repos) error { return r.DataExportParts.Create(ctx, p) })
	return p
}

// lines processes a part of x holding ids and returns its CSV rows.
func (e *env) lines(x *export.DataExport, ids ...uuid.UUID) [][]string {
	e.T.Helper()
	p := e.part(x, len(e.parts(x.ID)), ids...)
	got, err := e.svc.ProcessPart(e.Ctx, e.OrgID(), p.ID)
	if err != nil {
		e.T.Fatalf("ProcessPart: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(got.CSVLines)).ReadAll()
	if err != nil {
		e.T.Fatalf("parse %q: %v", got.CSVLines, err)
	}
	return rows
}

func finalized(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized }

func issued(d core.Date) func(*invoice.Invoice) {
	return func(inv *invoice.Invoice) { inv.Status, inv.IssuingDate = invoice.StatusFinalized, d }
}

func jobs(e *env, kind string) []port.Job {
	return slices.DeleteFunc(e.Store.Jobs(), func(j port.Job) bool { return j.Kind != kind })
}

func ruleCode(err error) string {
	if re, ok := errors.AsType[*core.RuleError](err); ok {
		return re.Code
	}
	return ""
}

func TestCreate(t *testing.T) {
	e := newEnv(t, exports.Filters{})
	// source: spec/services/data_exports/create_service_spec.rb:29
	x, err := e.svc.Create(e.Ctx, e.OrgID(), e.member, export.ResourceInvoices, exports.Query{Filter: `currency = "EUR"`, Search: "abc"}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := e.export(x.ID)
	if got.Status != export.StatusPending || got.Format != export.FormatCSV || got.ResourceType != export.ResourceInvoices || got.MembershipID != e.member {
		t.Errorf("got = %s %s %s %s, want = pending csv invoices %s", got.Status, got.Format, got.ResourceType, got.MembershipID, e.member)
	}
	if string(got.ResourceQuery) != `{"filter":"currency = \"EUR\"","search":"abc"}` {
		t.Errorf("resource_query got = %s", got.ResourceQuery)
	}
	// source: spec/services/data_exports/create_service_spec.rb:42
	js := jobs(e, exports.JobExportResources)
	if len(js) != 1 || js[0].Queue != port.QueueExports {
		t.Errorf("jobs got = %+v, want = one %s on exports", js, exports.JobExportResources)
	}

	if _, err := e.svc.Create(e.Ctx, e.OrgID(), core.MembershipID{}, export.ResourceInvoices, exports.Query{}, nil); ruleCode(err) != exports.CodeMembershipRequired {
		t.Errorf("without member got = %v, want = %s", err, exports.CodeMembershipRequired)
	}
	if _, err := e.svc.Create(e.Ctx, e.OrgID(), e.member, "unknown", exports.Query{}, nil); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("unknown resource got = %v, want = invalid value", err)
	}
	bad := newEnv(t, exports.Filters{Invoices: func(exports.Query) (invoice.Filter, error) {
		return invoice.Filter{}, core.ErrInvalidValue
	}})
	if _, err := bad.svc.Create(bad.Ctx, bad.OrgID(), bad.member, export.ResourceInvoiceFees, exports.Query{Filter: "x"}, nil); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("invalid filter got = %v, want = invalid value", err)
	}
}

func TestExportResources(t *testing.T) {
	day := core.NewDate(2023, 12, 1)
	t.Run("one part", func(t *testing.T) {
		e := newEnv(t, exports.Filters{})
		c := e.Customer()
		inv := e.StoredInvoice(c, issued(day))
		e.StoredInvoice(c) // generating: not listed
		x := e.create(export.ResourceInvoices)
		// source: spec/services/data_exports/export_resources_service_spec.rb:28
		parts, err := e.svc.ExportResources(e.Ctx, e.OrgID(), x.ID, 100)
		if err != nil {
			t.Fatalf("ExportResources: %v", err)
		}
		if len(parts) != 1 || !slices.Equal(parts[0].ObjectIDs, []uuid.UUID{inv.ID.UUID()}) {
			t.Errorf("parts got = %+v, want = one part with %s", parts, inv.ID)
		}
		// source: spec/services/data_exports/export_resources_service_spec.rb:64
		// source: spec/services/data_exports/export_resources_service_spec.rb:21
		got := e.export(x.ID)
		if got.Status != export.StatusProcessing || got.StartedAt == nil || !got.File.IsZero() {
			t.Errorf("export got = %s %v %q, want = processing, started, no file", got.Status, got.StartedAt, got.File)
		}
		if n := len(jobs(e, exports.JobProcessPart)); n != 1 {
			t.Errorf("part jobs got = %d, want = 1", n)
		}
	})
	t.Run("many parts", func(t *testing.T) {
		// source: spec/services/data_exports/export_resources_service_spec.rb:51
		e := newEnv(t, exports.Filters{})
		c := e.Customer()
		var invs []*invoice.Invoice
		for i := range 5 {
			invs = append(invs, e.StoredInvoice(c, issued(day.AddDays(i))))
		}
		x := e.create(export.ResourceInvoices)
		if _, err := e.svc.ExportResources(e.Ctx, e.OrgID(), x.ID, 2); err != nil {
			t.Fatalf("ExportResources: %v", err)
		}
		parts := e.parts(x.ID)
		want := [][]uuid.UUID{
			{invs[4].ID.UUID(), invs[3].ID.UUID()}, {invs[2].ID.UUID(), invs[1].ID.UUID()}, {invs[0].ID.UUID()},
		}
		if len(parts) != len(want) {
			t.Fatalf("parts got = %d, want = %d", len(parts), len(want))
		}
		for i, p := range parts {
			if p.Index != i || !slices.Equal(p.ObjectIDs, want[i]) {
				t.Errorf("part %d got = %d %v, want = %d %v", i, p.Index, p.ObjectIDs, i, want[i])
			}
		}
	})
	t.Run("expired", func(t *testing.T) {
		// source: spec/services/data_exports/export_resources_service_spec.rb:74
		e := newEnv(t, exports.Filters{})
		past := now.Add(-time.Hour)
		x := e.create(export.ResourceInvoices, func(x *export.DataExport) { x.ExpiresAt = &past })
		if _, err := e.svc.ExportResources(e.Ctx, e.OrgID(), x.ID, 0); ruleCode(err) != exports.CodeExpired {
			t.Errorf("got = %v, want = %s", err, exports.CodeExpired)
		}
	})
	t.Run("processed", func(t *testing.T) {
		// source: spec/services/data_exports/export_resources_service_spec.rb:83
		e := newEnv(t, exports.Filters{})
		x := e.create(export.ResourceInvoices, func(x *export.DataExport) { x.Status = export.StatusProcessing })
		if _, err := e.svc.ExportResources(e.Ctx, e.OrgID(), x.ID, 0); ruleCode(err) != exports.CodeProcessed {
			t.Errorf("got = %v, want = %s", err, exports.CodeProcessed)
		}
	})
	t.Run("error", func(t *testing.T) {
		// source: spec/services/data_exports/export_resources_service_spec.rb:96
		boom := errors.New("error_message")
		e := newEnv(t, exports.Filters{Invoices: func(exports.Query) (invoice.Filter, error) { return invoice.Filter{}, boom }})
		x := e.create(export.ResourceInvoices)
		if _, err := e.svc.ExportResources(e.Ctx, e.OrgID(), x.ID, 0); !errors.Is(err, boom) {
			t.Errorf("got = %v, want = %v", err, boom)
		}
		if got := e.export(x.ID).Status; got != export.StatusFailed {
			t.Errorf("status got = %s, want = failed", got)
		}
	})
	t.Run("credit notes types filter", func(t *testing.T) {
		// source: spec/services/data_exports/export_resources_service_spec.rb:116
		e := newEnv(t, exports.Filters{CreditNotes: func(exports.Query) (creditnote.Filter, error) {
			return creditnote.Filter{Types: []creditnote.Type{creditnote.TypeCredit}}, nil
		}})
		c := e.Customer()
		inv := e.StoredInvoice(c, finalized)
		credit := e.StoredCreditNote(inv, 100)
		refund := e.StoredCreditNote(inv, 0, func(cn *creditnote.CreditNote) {
			cn.RefundAmount, cn.CreditStatus = 100, ""
			cn.RefundStatus = core.RefundStatusPending
		})
		x := e.create(export.ResourceCreditNotes)
		if _, err := e.svc.ExportResources(e.Ctx, e.OrgID(), x.ID, 0); err != nil {
			t.Fatalf("ExportResources: %v", err)
		}
		parts := e.parts(x.ID)
		if len(parts) != 1 || !slices.Contains(parts[0].ObjectIDs, credit.ID.UUID()) || slices.Contains(parts[0].ObjectIDs, refund.ID.UUID()) {
			t.Errorf("parts got = %+v, want = %s without %s", parts, credit.ID, refund.ID)
		}
	})
}

func TestProcessAndCombine(t *testing.T) {
	e := newEnv(t, exports.Filters{})
	c := e.Customer()
	inv1 := e.StoredInvoice(c, issued(core.NewDate(2024, 6, 2)))
	inv2 := e.StoredInvoice(c, issued(core.NewDate(2024, 6, 1)))
	x := e.create(export.ResourceInvoices, func(x *export.DataExport) { x.Status = export.StatusProcessing })
	p0 := e.part(x, 0, inv1.ID.UUID())
	p1 := e.part(x, 1, inv2.ID.UUID())

	// source: spec/services/data_exports/process_part_service_spec.rb:76
	got, err := e.svc.ProcessPart(e.Ctx, e.OrgID(), p1.ID)
	if err != nil {
		t.Fatalf("ProcessPart: %v", err)
	}
	if !got.Completed || !strings.HasPrefix(got.CSVLines, inv2.ID.String()+",") {
		t.Errorf("part got = %v %q, want = completed with %s", got.Completed, got.CSVLines, inv2.ID)
	}
	if n := len(jobs(e, exports.JobCombineParts)); n != 0 {
		t.Errorf("combine jobs got = %d, want = 0", n)
	}
	// source: spec/services/data_exports/process_part_service_spec.rb:58
	// source: spec/services/data_exports/process_part_service_spec.rb:66
	if _, err := e.svc.ProcessPart(e.Ctx, e.OrgID(), p0.ID); err != nil {
		t.Fatalf("ProcessPart: %v", err)
	}
	if n := len(jobs(e, exports.JobCombineParts)); n != 1 {
		t.Errorf("combine jobs got = %d, want = 1", n)
	}

	// source: spec/services/data_exports/combine_parts_service_spec.rb:59
	done, err := e.svc.CombineParts(e.Ctx, e.OrgID(), x.ID)
	if err != nil {
		t.Fatalf("CombineParts: %v", err)
	}
	// source: spec/services/data_exports/combine_parts_service_spec.rb:38
	if done.Status != export.StatusCompleted || done.ExpiresAt == nil || !done.ExpiresAt.Equal(now.Add(export.ExpirationPeriod)) {
		t.Errorf("export got = %s %v, want = completed expiring in 7 days", done.Status, done.ExpiresAt)
	}
	if !strings.HasPrefix(string(done.File), "data_exports/"+x.ID.String()+"-") || e.blobs.types[done.File] != "text/csv" {
		t.Errorf("file got = %q %q, want = data_exports/<id>-<hex>.csv text/csv", done.File, e.blobs.types[done.File])
	}
	lines := strings.Split(strings.TrimSuffix(string(e.blobs.files[done.File]), "\n"), "\n")
	// source: spec/services/data_exports/combine_parts_service_spec.rb:22
	if lines[0] != strings.Join(exports.InvoiceHeaders, ",") {
		t.Errorf("header got = %q", lines[0])
	}
	if len(lines) != 3 || !strings.HasPrefix(lines[1], inv1.ID.String()) || !strings.HasPrefix(lines[2], inv2.ID.String()) {
		t.Errorf("rows got = %q, want = %s then %s", lines[1:], inv1.ID, inv2.ID)
	}
	if u, _ := e.svc.FileURL(e.Ctx, done); u != "https://files.test/"+string(done.File) {
		t.Errorf("url got = %q", u)
	}

	noBlobs := exports.New(e.Deps, nil, exports.Filters{})
	if _, err := noBlobs.CombineParts(e.Ctx, e.OrgID(), x.ID); !errors.Is(err, exports.ErrNoBlobStore) {
		t.Errorf("without blob store got = %v, want = %v", err, exports.ErrNoBlobStore)
	}
}

func TestInvoicesCSV(t *testing.T) {
	e := newEnv(t, exports.Filters{})
	c := e.Customer(func(c *customer.Customer) {
		c.Name, c.Email, c.TaxIdentificationNumber = new("customer name"), new("customer@eamil.com"), new("123456789")
		c.BillingAddress.Country = "US"
	})
	lost := time.Date(2023, 12, 22, 9, 30, 0, 0, time.UTC)
	inv := e.StoredInvoice(c, func(inv *invoice.Invoice) {
		inv.Status, inv.Type, inv.PaymentStatus = invoice.StatusFinalized, invoice.TypeCredit, core.PaymentStatusPending
		inv.SequentialID, inv.Number, inv.PurchaseOrderNumber = new(int64(123)), "INV123", new("PO-12345")
		inv.IssuingDate, inv.PaymentDueDate = core.NewDate(2023, 1, 1), core.NewDate(2023, 2, 1)
		inv.FeesAmount, inv.CouponsAmount, inv.TaxesAmount, inv.CreditNotesAmount = 70000, 1655, 10500, 334
		inv.PrepaidCreditAmount, inv.TotalAmount, inv.TotalPaidAmount, inv.OffsettedAmount = 1000, 77511, 50000, 334
		inv.ProgressiveBillingCreditAmount, inv.PaymentDisputeLostAt = 999, &lost
		inv.File = "invoice.pdf"
	})
	x := e.create(export.ResourceInvoices)
	want := []string{
		inv.ID.String(), "123", "false", "2023-01-01", c.ID.String(), c.ExternalID, "customer name",
		"customer@eamil.com", "US", "123456789", "INV123", "PO-12345", "credit", "pending", "finalized",
		"https://files.test/invoice.pdf", "USD", "70000", "1655", "10500", "334", "1000", "77511", "2023-02-01",
		"2023-12-22 09:30:00 UTC", "false", "27177", "50000", "334", "999",
	}
	// source: spec/services/data_exports/csv/invoices_spec.rb:98
	if got := e.lines(x, inv.ID.UUID()); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Errorf("got = %q, want = %q", got, want)
	}

	// source: spec/services/data_exports/csv/invoices_spec.rb:117
	be := organization.NewBillingEntity(e.OrgID(), core.NewID[core.BillingEntityID](e.IDs), "the-test-bil-ent", "Second", now)
	e.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
	if got := e.lines(x, inv.ID.UUID()); len(got) != 1 || !slices.Equal(got[0], append(want, "org")) {
		t.Errorf("multiple billing entities got = %q, want = %q", got, append(want, "org"))
	}
	header, _, err := e.svc.File(e.Ctx, e.OrgID(), x.ID)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if !strings.HasPrefix(string(header), strings.Join(exports.InvoiceHeaders, ",")+",billing_entity_code\n") {
		t.Errorf("header got = %q", strings.SplitN(string(header), "\n", 2)[0])
	}
}

// feeEnv is an invoice of a customer in tz with its subscription.
type feeEnv struct {
	*env
	inv  *invoice.Invoice
	plan *catalog.Plan
	is   *invoice.Subscription
	sub  *subscription.Subscription
	x    *export.DataExport
}

func newFeeEnv(t *testing.T, tz string, payInAdvance bool, from, to time.Time) *feeEnv {
	t.Helper()
	e := newEnv(t, exports.Filters{})
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	c := e.Customer(func(c *customer.Customer) { c.Timezone = loc })
	plan := e.Plan(period.Monthly, 1000, func(p *catalog.Plan) { p.PayInAdvance = payInAdvance })
	sub := e.Subscription(c, plan)
	inv := e.StoredInvoice(c, func(inv *invoice.Invoice) {
		inv.Status, inv.Number, inv.IssuingDate = invoice.StatusFinalized, "TWI-2B86-170-001", core.NewDate(2024, 6, 6)
	})
	is := e.LinkSubscription(inv, sub, invoice.InvoicingReasonSubscriptionPeriodic, from, to)
	return &feeEnv{env: e, inv: inv, plan: plan, is: is, sub: sub, x: e.create(export.ResourceInvoiceFees)}
}

// dates returns the fee_from_date and fee_to_date of fee f.
func (e *feeEnv) dates(f *invoice.Fee) [2]string {
	e.T.Helper()
	for _, row := range e.lines(e.x, e.inv.ID.UUID()) {
		if row[3] == f.ID.String() {
			return [2]string{row[13], row[14]}
		}
	}
	e.T.Fatalf("no row of fee %s", f.ID)
	return [2]string{}
}

func utc(s string) time.Time {
	t, err := time.Parse(time.DateTime, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestInvoiceFeesCSV(t *testing.T) {
	t.Run("row", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:83
		e := newFeeEnv(t, "UTC", false, utc("2024-05-08 00:00:00"), utc("2024-06-07 00:00:00"))
		m := e.Metric("group", catalog.AggregationTypeCount, "", func(m *catalog.BillableMetric) { m.Description = new("charge 1 description") })
		ch := e.Charge(e.plan, m, catalog.ChargeModelStandard, `{"amount":"10"}`)
		sub := e.is.SubscriptionID
		f := e.StoredFee(e.inv, 10000, func(f *invoice.Fee) {
			f.Type, f.ChargeID, f.SubscriptionID = core.FeeTypeCharge, ch.ID, sub
			f.GroupedBy = map[string]string{"models": "model_1"}
			f.Units, f.PreciseUnitAmount, f.Amount, f.TaxesAmount = money.MustParse("100"), money.MustParse("10.000"), 9950, 50
		})
		subscription := billingtest.Read(e.Harness, func(ctx context.Context, r port.Repos) (string, error) {
			s, err := r.Subscriptions.Get(ctx, e.OrgID(), sub)
			if err != nil {
				return "", err
			}
			return s.ExternalID, nil
		})
		want := []string{
			e.inv.ID.String(), "TWI-2B86-170-001", "2024-06-06", f.ID.String(), "charge", "group", "group",
			"charge 1 description", "group", "", `{"models" => "model_1"}`, subscription, e.plan.Code,
			"2024-05-08", "2024-06-06", "USD", "100.0", "10.0", "50", "10000",
		}
		if got := e.lines(e.x, e.inv.ID.UUID()); len(got) != 1 || !slices.Equal(got[0], want) {
			t.Errorf("got = %q, want = %q", got, want)
		}
	})
	for _, tc := range []struct {
		name, source, tz, from, to string
		want                       [2]string
	}{
		{"negative offset", "spec/services/data_exports/csv/invoice_fees_spec.rb:113", "America/New_York", "2026-04-01 04:00:00", "2026-05-01 04:00:00", [2]string{"2026-04-01", "2026-04-30"}},
		{"positive offset", "spec/services/data_exports/csv/invoice_fees_spec.rb:124", "Asia/Tokyo", "2026-03-31 15:00:00", "2026-04-30 15:00:00", [2]string{"2026-04-01", "2026-04-30"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Log("source: " + tc.source)
			e := newFeeEnv(t, tc.tz, false, utc(tc.from), utc(tc.to))
			f := e.StoredFee(e.inv, 100, billingtest.SubscriptionFee(e.subscription()))
			if got := e.dates(f); got != tc.want {
				t.Errorf("got = %v, want = %v", got, tc.want)
			}
		})
	}
	usage := [2]time.Time{utc("2026-05-22 00:00:00"), utc("2026-06-22 00:00:00")}
	advance := invoice.FeeProperties{
		From: utc("2026-06-22 00:00:00"), To: utc("2026-07-22 00:00:00"),
		FixedChargesFrom: utc("2026-06-22 00:00:00"), FixedChargesTo: utc("2026-07-22 00:00:00"),
	}
	t.Run("advance fixed charge", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:175
		e := newFeeEnv(t, "UTC", false, usage[0], usage[1])
		f := e.StoredFee(e.inv, 100, func(f *invoice.Fee) {
			f.Type, f.SubscriptionID, f.PayInAdvance, f.Properties = core.FeeTypeFixedCharge, e.is.SubscriptionID, true, advance
		})
		if got, want := e.dates(f), [2]string{"2026-06-22", "2026-07-21"}; got != want {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	t.Run("advance subscription fee", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:192
		e := newFeeEnv(t, "UTC", false, usage[0], usage[1])
		f := e.StoredFee(e.inv, 100, billingtest.SubscriptionFee(e.subscription()), func(f *invoice.Fee) { f.Properties = advance })
		if got, want := e.dates(f), [2]string{"2026-06-22", "2026-07-21"}; got != want {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	t.Run("advance subscription fee negative offset", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:207
		e := newFeeEnv(t, "America/New_York", false, usage[0], usage[1])
		f := e.StoredFee(e.inv, 100, billingtest.SubscriptionFee(e.subscription()), func(f *invoice.Fee) {
			f.Properties = invoice.FeeProperties{From: utc("2026-06-22 04:00:00"), To: utc("2026-07-22 04:00:00")}
		})
		if got, want := e.dates(f), [2]string{"2026-06-22", "2026-07-21"}; got != want {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	chargeFee := func(e *feeEnv, inAdvance bool, from, to string) *invoice.Fee {
		m := e.Metric("m", catalog.AggregationTypeCount, "")
		ch := e.Charge(e.plan, m, catalog.ChargeModelStandard, `{"amount":"1"}`)
		return e.StoredFee(e.inv, 100, func(f *invoice.Fee) {
			f.Type, f.ChargeID, f.SubscriptionID, f.PayInAdvance = core.FeeTypeCharge, ch.ID, e.is.SubscriptionID, inAdvance
			f.Properties = invoice.FeeProperties{ChargesFrom: utc(from), ChargesTo: utc(to)}
		})
	}
	t.Run("arrears usage fee", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:235
		e := newFeeEnv(t, "UTC", false, usage[0], usage[1])
		f := chargeFee(e, false, "2026-05-22 00:00:00", "2026-06-22 00:00:00")
		if got, want := e.dates(f), [2]string{"2026-05-22", "2026-06-21"}; got != want {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	t.Run("pay in advance usage fee negative offset", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:269
		e := newFeeEnv(t, "America/New_York", false, utc("2026-05-22 04:00:00"), utc("2026-06-22 04:00:00"))
		f := chargeFee(e, true, "2026-06-22 04:00:00", "2026-07-22 04:00:00")
		if got, want := e.dates(f), [2]string{"2026-06-22", "2026-07-21"}; got != want {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	t.Run("commitment fee", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:295
		e := newFeeEnv(t, "UTC", true, usage[0], usage[1])
		f := e.StoredFee(e.inv, 100, func(f *invoice.Fee) {
			f.Type, f.SubscriptionID = core.FeeTypeCommitment, e.is.SubscriptionID
			f.Properties = invoice.FeeProperties{From: utc("2026-04-22 00:00:00"), To: utc("2026-05-22 00:00:00")}
		})
		if got, want := e.dates(f), [2]string{"2026-04-22", "2026-05-21"}; got != want {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	t.Run("commitment fee without period", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:302
		e := newFeeEnv(t, "UTC", true, usage[0], usage[1])
		f := e.StoredFee(e.inv, 100, func(f *invoice.Fee) { f.Type, f.SubscriptionID = core.FeeTypeCommitment, e.is.SubscriptionID })
		if got := e.dates(f); got != [2]string{} {
			t.Errorf("got = %v, want = empty", got)
		}
	})
	t.Run("subscription fee without period", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:325
		e := newFeeEnv(t, "UTC", true, utc("2026-06-22 00:00:00"), utc("2026-07-22 00:00:00"))
		f := e.StoredFee(e.inv, 100, billingtest.SubscriptionFee(e.subscription()))
		if got, want := e.dates(f), [2]string{"2026-06-22", "2026-07-21"}; got != want {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
	t.Run("add-on fee", func(t *testing.T) {
		// source: spec/services/data_exports/csv/invoice_fees_spec.rb:357
		e := newFeeEnv(t, "Asia/Tokyo", false, usage[0], usage[1])
		a := e.AddOn(100)
		f := e.StoredFee(e.inv, 100, func(f *invoice.Fee) {
			f.Type, f.AddOnID = core.FeeTypeAddOn, a.ID
			f.Properties = invoice.FeeProperties{From: utc("2026-08-05 00:00:00"), To: utc("2026-08-05 23:59:59")}
		})
		if got, want := e.dates(f), [2]string{"2026-08-05", "2026-08-05"}; got != want {
			t.Errorf("got = %v, want = %v", got, want)
		}
	})
}

// subscription returns the subscription of the invoice.
func (e *feeEnv) subscription() *subscription.Subscription { return e.sub }

func TestCreditNotesCSV(t *testing.T) {
	e := newEnv(t, exports.Filters{})
	c := e.Customer(func(c *customer.Customer) { c.Name = new("Jane") })
	inv := e.StoredInvoice(c, func(inv *invoice.Invoice) {
		inv.Status, inv.PurchaseOrderNumber = invoice.StatusFinalized, new("PO-12345")
	})
	cn := e.StoredCreditNote(inv, 300, func(cn *creditnote.CreditNote) {
		cn.Description, cn.SubTotalExcludingTaxesAmount, cn.Items = new("a, \"quoted\" note"), 300, []creditnote.Item{
			{ID: core.NewID[core.CreditNoteItemID](e.IDs), FeeID: core.NewID[core.FeeID](e.IDs), Amount: 300, PreciseAmount: money.NewFromInt(300)},
		}
	})
	want := []string{
		cn.ID.String(), "1", "false", cn.IssuingDate.String(), c.ID.String(), c.ExternalID, "Jane", "", "", "",
		cn.Number, inv.Number, "PO-12345", "available", "", "other", `a, "quoted" note`, "USD", "300", "0", "300",
		"0", "0", "300", "300", "0", "",
	}
	x := e.create(export.ResourceCreditNotes)
	// source: spec/services/data_exports/csv/credit_notes_spec.rb:60
	if got := e.lines(x, cn.ID.UUID()); len(got) != 1 || !slices.Equal(got[0], want) {
		t.Errorf("got = %q, want = %q", got, want)
	}
	// source: spec/services/data_exports/csv/credit_notes_spec.rb:112
	be := organization.NewBillingEntity(e.OrgID(), core.NewID[core.BillingEntityID](e.IDs), "second", "Second", now)
	e.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, be) })
	if got := e.lines(x, cn.ID.UUID()); len(got) != 1 || !slices.Equal(got[0], append(want, "org")) {
		t.Errorf("multiple billing entities got = %q, want = %q", got, append(want, "org"))
	}

	// source: spec/services/data_exports/csv/credit_note_items_spec.rb:39
	items := e.create(export.ResourceCreditNoteItems)
	it := cn.Items[0]
	wantItem := []string{cn.ID.String(), cn.Number, inv.Number, cn.IssuingDate.String(), it.ID.String(), it.FeeID.String(), "USD", "300"}
	if got := e.lines(items, cn.ID.UUID()); len(got) != 1 || !slices.Equal(got[0], wantItem) {
		t.Errorf("items got = %q, want = %q", got, wantItem)
	}
	if got := fmt.Sprint(exports.CreditNoteItemHeaders); !strings.Contains(got, "credit_note_item_amount_cents") {
		t.Errorf("headers got = %s", got)
	}
}
