package porttest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/export"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// exportFixtureMembership creates a user and its admin membership of org.
func exportFixtureMembership(e *env, org core.OrganizationID, email string) core.MembershipID {
	e.t.Helper()
	u := access.NewUser(core.NewID[core.UserID](e.ids), email, []byte("hash"), now)
	m := access.NewMembership(core.NewID[core.MembershipID](e.ids), org, u.ID, []core.RoleID{access.BuiltInRoleID(permission.BuiltInRoleAdmin)}, now)
	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := r.Users.Create(ctx, u); err != nil {
			return err
		}
		return r.Memberships.Create(ctx, m)
	})
	return m.ID
}

func exportID(x *export.DataExport) core.DataExportID { return x.ID }

func exportPartID(p *export.Part) core.DataExportPartID { return p.ID }

func testExport(t *testing.T, h Harness) {
	t.Run("data exports", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		m1 := exportFixtureMembership(e, org, "one@acme.test")
		m2 := exportFixtureMembership(e, org, "two@acme.test")

		x1 := export.New(core.NewID[core.DataExportID](e.ids), org, m1, export.ResourceInvoices,
			json.RawMessage(`{"status":"finalized","currency":"EUR"}`), now)
		x2 := export.New(core.NewID[core.DataExportID](e.ids), org, m1, export.ResourceCreditNotes, nil, now)
		x3 := export.New(core.NewID[core.DataExportID](e.ids), org, m2, export.ResourceInvoiceFees, json.RawMessage(`{}`), now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range []*export.DataExport{x1, x2, x3} {
				noErr(t, "Create()", r.DataExports.Create(ctx, x))
			}
			wantErr(t, "Create() with the same ID", r.DataExports.Create(ctx, x1), core.ErrConflict)
			got, err := r.DataExports.Get(ctx, org, x1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, x1)
			got, err = r.DataExports.Get(ctx, org, x2.ID)
			noErr(t, "Get() without query", err)
			equal(t, "Get() without query", got, x2)
			_, err = r.DataExports.Get(ctx, org, core.NewID[core.DataExportID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Every field set.
			noErr(t, "Start()", x1.Start(at(time.Minute)))
			noErr(t, "Complete()", x1.Complete("exports/x1.csv", at(2*time.Minute)))
			noErr(t, "Update()", r.DataExports.Update(ctx, x1))
			got, err = r.DataExports.Get(ctx, org, x1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, x1)

			// Isolation.
			_, err = r.DataExports.Get(ctx, other, x1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			moved := *x1
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.DataExports.Update(ctx, &moved), core.ErrNotFound)
			wantErr(t, "Delete() through another organization", r.DataExports.Delete(ctx, other, x1.ID), core.ErrNotFound)
			p, err := r.DataExports.ListByMembership(ctx, other, m1, core.PageRequest{})
			noErr(t, "ListByMembership() of another organization", err)
			equal(t, "ListByMembership() of another organization", len(p.Items), 0)

			// Listing by member, in creation order, paginated.
			list := listAll(t, 1, func(p core.PageRequest) (core.Page[*export.DataExport], error) {
				return r.DataExports.ListByMembership(ctx, org, m1, p)
			})
			equal(t, "ListByMembership()", list, []*export.DataExport{x1, x2})
			list = listAll(t, 5, func(p core.PageRequest) (core.Page[*export.DataExport], error) {
				return r.DataExports.ListByMembership(ctx, org, m2, p)
			})
			equal(t, "ListByMembership() of the second member", ids(list, exportID), []core.DataExportID{x3.ID})

			missing := export.New(core.NewID[core.DataExportID](e.ids), org, m1, export.ResourceInvoices, nil, now)
			wantErr(t, "Update() unknown", r.DataExports.Update(ctx, missing), core.ErrNotFound)
			wantErr(t, "Delete() unknown", r.DataExports.Delete(ctx, org, missing.ID), core.ErrNotFound)
			return nil
		})
	})

	t.Run("data exports referencing a missing membership", func(t *testing.T) {
		t.Parallel()
		if h.Name == "memstore" {
			t.Skip("memstore does not check references")
		}
		e := newEnv(t, h)
		_, a := e.org("Acme")
		x := export.New(core.NewID[core.DataExportID](e.ids), a.OrganizationID, core.NewID[core.MembershipID](e.ids), export.ResourceInvoices, nil, now)
		err := e.txErr(func(ctx context.Context, r port.Repos) error { return r.DataExports.Create(ctx, x) })
		wantErr(t, "Create() with an unknown membership", err, core.ErrNotFound)
	})

	t.Run("expired data exports across organizations", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		ma := exportFixtureMembership(e, a.OrganizationID, "a@acme.test")
		mb := exportFixtureMembership(e, b.OrganizationID, "b@globex.test")
		completed := func(org core.OrganizationID, m core.MembershipID, done time.Time) *export.DataExport {
			x := export.New(core.NewID[core.DataExportID](e.ids), org, m, export.ResourceInvoices, nil, now)
			noErr(t, "Start()", x.Start(done))
			noErr(t, "Complete()", x.Complete(core.BlobRef("exports/"+x.ID.String()+".csv"), done))
			return x
		}
		oldA := completed(a.OrganizationID, ma, now)
		oldB := completed(b.OrganizationID, mb, at(time.Hour))
		fresh := completed(a.OrganizationID, ma, at(10*24*time.Hour))
		exact := completed(b.OrganizationID, mb, at(2*time.Hour))
		cleaned := completed(a.OrganizationID, ma, now)
		cleaned.File = ""
		pending := export.New(core.NewID[core.DataExportID](e.ids), a.OrganizationID, ma, export.ResourceInvoices, nil, now)
		failed := export.New(core.NewID[core.DataExportID](e.ids), b.OrganizationID, mb, export.ResourceInvoices, nil, now)
		noErr(t, "Fail()", failed.Fail(now))
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, x := range []*export.DataExport{oldA, oldB, fresh, exact, cleaned, pending, failed} {
				noErr(t, "Create()", r.DataExports.Create(ctx, x))
			}
			// At exactly expires_at the file has not expired yet.
			cutoff := exact.ExpiresAt.UTC()
			got, err := r.DataExports.ListExpired(ctx, cutoff, 0)
			noErr(t, "ListExpired()", err)
			equal(t, "ListExpired()", got, []*export.DataExport{oldA, oldB})
			got, err = r.DataExports.ListExpired(ctx, cutoff, 1)
			noErr(t, "ListExpired() limited", err)
			equal(t, "ListExpired() limited", ids(got, exportID), []core.DataExportID{oldA.ID})
			got, err = r.DataExports.ListExpired(ctx, cutoff.Add(time.Microsecond), 10)
			noErr(t, "ListExpired() later", err)
			equal(t, "ListExpired() later", ids(got, exportID), []core.DataExportID{oldA.ID, oldB.ID, exact.ID})
			return nil
		})
	})

	t.Run("data export parts", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		org, other := a.OrganizationID, b.OrganizationID
		m := exportFixtureMembership(e, org, "parts@acme.test")
		x := export.New(core.NewID[core.DataExportID](e.ids), org, m, export.ResourceInvoices, nil, now)
		y := export.New(core.NewID[core.DataExportID](e.ids), org, m, export.ResourceInvoices, nil, now)
		part := func(exp *export.DataExport, index int) *export.Part {
			return &export.Part{
				ID: core.NewID[core.DataExportPartID](e.ids), OrganizationID: org, DataExportID: exp.ID, Index: index,
				ObjectIDs: []uuid.UUID{uuid.New(), uuid.New()}, CreatedAt: now, UpdatedAt: now,
			}
		}
		// Created out of index order.
		p2, p0, p1 := part(x, 2), part(x, 0), part(x, 1)
		q0 := part(y, 0)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create() export", r.DataExports.Create(ctx, x))
			noErr(t, "Create() export", r.DataExports.Create(ctx, y))
			for _, p := range []*export.Part{p2, p0, p1, q0} {
				noErr(t, "Create()", r.DataExportParts.Create(ctx, p))
			}
			wantErr(t, "Create() with the same ID", r.DataExportParts.Create(ctx, p0), core.ErrConflict)
			wantErr(t, "Create() with a taken index", r.DataExportParts.Create(ctx, part(x, 1)), core.ErrConflict)
			got, err := r.DataExportParts.Get(ctx, org, p0.ID)
			noErr(t, "Get() after conflicts", err)
			equal(t, "Get()", got, p0)
			_, err = r.DataExportParts.Get(ctx, org, core.NewID[core.DataExportPartID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			list, err := r.DataExportParts.ListByExport(ctx, org, x.ID)
			noErr(t, "ListByExport()", err)
			equal(t, "ListByExport()", list, []*export.Part{p0, p1, p2})

			next, err := r.DataExportParts.NextUnprocessed(ctx, org, x.ID)
			noErr(t, "NextUnprocessed()", err)
			equal(t, "NextUnprocessed()", next, p0)
			p0.Finish("a,b\nc,d\n", at(time.Minute))
			noErr(t, "Update()", r.DataExportParts.Update(ctx, p0))
			got, err = r.DataExportParts.Get(ctx, org, p0.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, p0)
			next, err = r.DataExportParts.NextUnprocessed(ctx, org, x.ID)
			noErr(t, "NextUnprocessed() after one", err)
			equal(t, "NextUnprocessed() after one", next.ID, p1.ID)
			for _, p := range []*export.Part{p1, p2} {
				p.Finish("x\n", at(time.Minute))
				noErr(t, "Update()", r.DataExportParts.Update(ctx, p))
			}
			_, err = r.DataExportParts.NextUnprocessed(ctx, org, x.ID)
			wantErr(t, "NextUnprocessed() when complete", err, core.ErrNotFound)

			// Isolation.
			_, err = r.DataExportParts.Get(ctx, other, p0.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			list, err = r.DataExportParts.ListByExport(ctx, other, x.ID)
			noErr(t, "ListByExport() of another organization", err)
			equal(t, "ListByExport() of another organization", len(list), 0)
			_, err = r.DataExportParts.NextUnprocessed(ctx, other, y.ID)
			wantErr(t, "NextUnprocessed() of another organization", err, core.ErrNotFound)
			moved := *q0
			moved.OrganizationID = other
			wantErr(t, "Update() through another organization", r.DataExportParts.Update(ctx, &moved), core.ErrNotFound)
			missing := part(x, 9)
			wantErr(t, "Update() unknown", r.DataExportParts.Update(ctx, missing), core.ErrNotFound)

			// Deleting an export deletes its parts only.
			noErr(t, "Delete()", r.DataExports.Delete(ctx, org, x.ID))
			_, err = r.DataExports.Get(ctx, org, x.ID)
			wantErr(t, "Get() deleted", err, core.ErrNotFound)
			_, err = r.DataExportParts.Get(ctx, org, p1.ID)
			wantErr(t, "Get() part of a deleted export", err, core.ErrNotFound)
			list, err = r.DataExportParts.ListByExport(ctx, org, y.ID)
			noErr(t, "ListByExport() of the other export", err)
			equal(t, "ListByExport() of the other export", ids(list, exportPartID), []core.DataExportPartID{q0.ID})
			return nil
		})
	})
}
