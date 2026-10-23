package porttest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/access"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/permission"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// quoteParents are the rows of other aggregates the quote tests reference.
type quoteParents struct {
	org          core.OrganizationID
	be           core.BillingEntityID
	customers    [2]core.CustomerID
	owners       [2]core.MembershipID
	subscription core.SubscriptionID
}

// quoteFixtureParents creates two customers, two members and a
// subscription in the organization of be.
func quoteFixtureParents(e *env, be *organization.BillingEntity) quoteParents {
	e.t.Helper()
	org := be.OrganizationID
	p := quoteParents{org: org, be: be.ID}
	e.tx(func(ctx context.Context, r port.Repos) error {
		for i := range p.customers {
			c := customer.New(org, core.NewID[core.CustomerID](e.ids), "cust-"+string(rune('a'+i)), be.ID, now)
			if err := r.Customers.Create(ctx, c); err != nil {
				return err
			}
			p.customers[i] = c.ID
		}
		for i := range p.owners {
			u := access.NewUser(core.NewID[core.UserID](e.ids), "", []byte("hash"), now)
			u.Email = u.ID.String() + "@quote.test"
			if err := r.Users.Create(ctx, u); err != nil {
				return err
			}
			m := access.NewMembership(core.NewID[core.MembershipID](e.ids), org, u.ID, access.BuiltInRoleID(permission.BuiltInRoleAdmin), now)
			if err := r.Memberships.Create(ctx, m); err != nil {
				return err
			}
			p.owners[i] = m.ID
		}
		plan := &catalog.Plan{
			ID: core.NewID[core.PlanID](e.ids), OrganizationID: org, Code: "quote-plan", Name: "Quote plan",
			Interval: period.Monthly, Amount: 1000, Currency: money.MustCurrency("EUR"),
			Status: catalog.PlanStatusActive, CreatedAt: now, UpdatedAt: now,
		}
		if err := r.Plans.Create(ctx, plan); err != nil {
			return err
		}
		s := subscription.New(org, core.NewID[core.SubscriptionID](e.ids), p.customers[0], plan.ID, "quote-sub", now, subscription.PlanFacts{}, now)
		if err := r.Subscriptions.Create(ctx, s); err != nil {
			return err
		}
		p.subscription = s.ID
		return nil
	})
	return p
}

// quoteFixtureQuote returns an unsaved quote numbered seq in the creation
// year of createdAt.
func quoteFixtureQuote(e *env, p quoteParents, seq int, createdAt time.Time) *quote.Quote {
	q := &quote.Quote{
		ID:             core.NewID[core.QuoteID](e.ids),
		OrganizationID: p.org,
		CustomerID:     p.customers[0],
		OrderType:      quote.OrderTypeSubscriptionCreation,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}
	q.AssignNumber(seq)
	return q
}

// quoteFixtureVersion returns an unsaved version n of q in status s.
func quoteFixtureVersion(e *env, q *quote.Quote, n int, s quote.VersionStatus) *quote.Version {
	v := &quote.Version{
		ID:             core.NewID[core.QuoteVersionID](e.ids),
		OrganizationID: q.OrganizationID,
		QuoteID:        q.ID,
		Version:        n,
		Status:         s,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	switch s {
	case quote.VersionStatusApproved:
		v.ApprovedAt = ptr(now)
	case quote.VersionStatusVoided:
		v.VoidReason, v.VoidedAt = quote.VersionVoidReasonSuperseded, ptr(now)
	}
	return v
}

// quoteFixtureForm returns an unsaved generated order form of v numbered seq.
func quoteFixtureForm(e *env, p quoteParents, v *quote.Version, seq int, createdAt time.Time) *quote.OrderForm {
	f := &quote.OrderForm{
		ID:             core.NewID[core.OrderFormID](e.ids),
		OrganizationID: v.OrganizationID,
		CustomerID:     p.customers[0],
		QuoteVersionID: v.ID,
		Status:         quote.OrderFormStatusGenerated,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}
	f.AssignNumber(seq)
	return f
}

// quoteFixtureOrder returns an unsaved created order of f numbered seq.
func quoteFixtureOrder(e *env, f *quote.OrderForm, seq int, createdAt time.Time) *quote.Order {
	o := &quote.Order{
		ID:             core.NewID[core.OrderID](e.ids),
		OrganizationID: f.OrganizationID,
		CustomerID:     f.CustomerID,
		OrderFormID:    f.ID,
		Status:         quote.OrderStatusCreated,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
	}
	o.AssignNumber(seq)
	return o
}

// quoteFixtureChain saves n quotes, each with an approved version, and
// returns their approved versions 1, ready for order forms.
func quoteFixtureChain(e *env, p quoteParents, n int) []*quote.Version {
	e.t.Helper()
	var out []*quote.Version
	e.tx(func(ctx context.Context, r port.Repos) error {
		for i := range n {
			q := quoteFixtureQuote(e, p, 100+i, now)
			if err := r.Quotes.Create(ctx, q); err != nil {
				return err
			}
			v := quoteFixtureVersion(e, q, 1, quote.VersionStatusApproved)
			if err := r.QuoteVersions.Create(ctx, v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	return out
}

// quoteSortByID compares lists whose order is unspecified by ID.
func quoteSortByID[T any, I interface{ Compare(I) int }](id func(T) I) cmp.Option {
	return cmpopts.SortSlices(func(a, b T) bool { return id(a).Compare(id(b)) < 0 })
}

func testQuote(t *testing.T, h Harness) {
	t.Run("quotes", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		p := quoteFixtureParents(e, a)
		pb := quoteFixtureParents(e, b)
		org := p.org

		full := quoteFixtureQuote(e, p, 7, now)
		full.OrderType = quote.OrderTypeSubscriptionAmendment
		full.SubscriptionID = p.subscription
		full.Owners = []core.MembershipID{p.owners[1], p.owners[0]}
		bare := quoteFixtureQuote(e, p, 8, at(time.Hour))
		bare.CustomerID = p.customers[1]
		bare.OrderType = quote.OrderTypeOneOff
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.Quotes.Create(ctx, full))
			noErr(t, "Create() bare", r.Quotes.Create(ctx, bare))
			wantErr(t, "Create() with the same ID", r.Quotes.Create(ctx, full), core.ErrConflict)
			dup := quoteFixtureQuote(e, p, 9, now)
			dup.Number = full.Number
			wantErr(t, "Create() with a used number", r.Quotes.Create(ctx, dup), core.ErrConflict)

			got, err := r.Quotes.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.Quotes.Get(ctx, org, bare.ID)
			noErr(t, "Get() bare", err)
			equal(t, "Get() bare", got, bare)
			got, err = r.Quotes.GetByNumber(ctx, org, "QT-2025-0007")
			noErr(t, "GetByNumber()", err)
			equal(t, "GetByNumber()", got, full)
			_, err = r.Quotes.GetByNumber(ctx, org, "QT-2025-0099")
			wantErr(t, "GetByNumber() unknown", err, core.ErrNotFound)
			_, err = r.Quotes.Get(ctx, org, core.NewID[core.QuoteID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Update replaces the owners and the optional fields.
			full.Owners = []core.MembershipID{p.owners[0]}
			full.OrderType = quote.OrderTypeSubscriptionCreation
			full.SubscriptionID = core.SubscriptionID{}
			full.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.Quotes.Update(ctx, full))
			got, err = r.Quotes.Get(ctx, org, full.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, full)
			bare.Owners = []core.MembershipID{p.owners[1], p.owners[0]}
			noErr(t, "Update() bare", r.Quotes.Update(ctx, bare))
			got, err = r.Quotes.Get(ctx, org, bare.ID)
			noErr(t, "Get() bare after Update()", err)
			equal(t, "Get() bare after Update()", got, bare)

			clash := *bare
			clash.Number = full.Number
			wantErr(t, "Update() to a used number", r.Quotes.Update(ctx, &clash), core.ErrConflict)
			wantErr(t, "Update() unknown", r.Quotes.Update(ctx, quoteFixtureQuote(e, p, 50, now)), core.ErrNotFound)
			// The transaction stays usable after the conflicts.
			got, err = r.Quotes.Get(ctx, org, bare.ID)
			noErr(t, "Get() after the conflicts", err)
			equal(t, "Get() after the conflicts", got, bare)

			// Isolation.
			_, err = r.Quotes.Get(ctx, pb.org, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Quotes.GetByNumber(ctx, pb.org, full.Number)
			wantErr(t, "GetByNumber() from another organization", err, core.ErrNotFound)
			foreign := *full
			foreign.OrganizationID = pb.org
			foreign.CustomerID = pb.customers[0]
			foreign.Owners = nil
			wantErr(t, "Update() from another organization", r.Quotes.Update(ctx, &foreign), core.ErrNotFound)
			page, err := r.Quotes.List(ctx, pb.org, quote.Filter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(page.Items), 0)

			// The same number in another organization is fine.
			other := quoteFixtureQuote(e, pb, 7, now)
			noErr(t, "Create() in another organization", r.Quotes.Create(ctx, other))
			return nil
		})
	})

	t.Run("quote sequences and lists", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		p := quoteFixtureParents(e, a)
		pb := quoteFixtureParents(e, b)
		org := p.org
		lastYear := time.Date(2024, time.December, 31, 23, 59, 59, 0, time.UTC)

		var qs []*quote.Quote
		mk := func(seq int, created time.Time, c core.CustomerID, ot quote.OrderType, owners ...core.MembershipID) {
			q := quoteFixtureQuote(e, p, seq, created)
			q.CustomerID, q.OrderType, q.Owners = c, ot, owners
			if ot == quote.OrderTypeSubscriptionAmendment {
				q.SubscriptionID = p.subscription
			}
			qs = append(qs, q)
		}
		mk(1, now, p.customers[0], quote.OrderTypeSubscriptionCreation, p.owners[0])
		mk(3, now, p.customers[1], quote.OrderTypeOneOff, p.owners[0], p.owners[1])
		mk(5, lastYear, p.customers[0], quote.OrderTypeSubscriptionAmendment)
		mk(2, at(time.Hour), p.customers[1], quote.OrderTypeSubscriptionCreation, p.owners[1])
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, q := range qs {
				noErr(t, "Create()", r.Quotes.Create(ctx, q))
			}
			for _, tc := range []struct {
				org  core.OrganizationID
				year int
				want int
			}{{org, 2025, 4}, {org, 2024, 6}, {org, 2023, 1}, {pb.org, 2025, 1}} {
				got, err := r.Quotes.NextSequence(ctx, tc.org, tc.year)
				noErr(t, "NextSequence()", err)
				if got != tc.want {
					t.Errorf("NextSequence(%d) got = %d, want = %d", tc.year, got, tc.want)
				}
			}
			return nil
		})

		qid := func(q *quote.Quote) core.QuoteID { return q.ID }
		for _, tc := range []struct {
			name string
			f    quote.Filter
			want []*quote.Quote
		}{
			{"all", quote.Filter{}, qs},
			{"customer", quote.Filter{CustomerID: p.customers[1]}, []*quote.Quote{qs[1], qs[3]}},
			{"order types", quote.Filter{OrderTypes: []quote.OrderType{quote.OrderTypeOneOff, quote.OrderTypeSubscriptionAmendment}}, []*quote.Quote{qs[1], qs[2]}},
			{"owner", quote.Filter{OwnerID: p.owners[0]}, []*quote.Quote{qs[0], qs[1]}},
			{"combined", quote.Filter{CustomerID: p.customers[1], OwnerID: p.owners[1], OrderTypes: []quote.OrderType{quote.OrderTypeSubscriptionCreation}}, []*quote.Quote{qs[3]}},
			{"owner of another organization", quote.Filter{OwnerID: pb.owners[0]}, nil},
		} {
			e.tx(func(ctx context.Context, r port.Repos) error {
				got := listAll(t, 1, func(pr core.PageRequest) (core.Page[*quote.Quote], error) {
					return r.Quotes.List(ctx, org, tc.f, pr)
				})
				equal(t, "List() "+tc.name, ids(got, qid), ids(tc.want, qid))
				if tc.name == "all" {
					equal(t, "List() all items", got, tc.want)
				}
				return nil
			})
		}
	})

	t.Run("quote versions", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		p := quoteFixtureParents(e, a)
		pb := quoteFixtureParents(e, b)
		org := p.org
		q := quoteFixtureQuote(e, p, 1, now)
		empty := quoteFixtureQuote(e, p, 2, now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create() quote", r.Quotes.Create(ctx, q))
			noErr(t, "Create() quote", r.Quotes.Create(ctx, empty))

			v1 := quoteFixtureVersion(e, q, 1, quote.VersionStatusDraft)
			v1.Content = json.RawMessage(`{"plan_code": "pro", "overrides": {"amount_cents": 1200}, "add_ons": [1, 2]}`)
			v1.Currency = money.MustCurrency("EUR")
			v1.BillingEntityID = p.be
			noErr(t, "Create()", r.QuoteVersions.Create(ctx, v1))
			wantErr(t, "Create() with the same ID", r.QuoteVersions.Create(ctx, v1), core.ErrConflict)
			got, err := r.QuoteVersions.Get(ctx, org, v1.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, v1)

			// A second active version is refused, and so is a used number.
			second := quoteFixtureVersion(e, q, 2, quote.VersionStatusApproved)
			wantErr(t, "Create() a second active version", r.QuoteVersions.Create(ctx, second), core.ErrConflict)
			sameNumber := quoteFixtureVersion(e, q, 1, quote.VersionStatusVoided)
			wantErr(t, "Create() a used version number", r.QuoteVersions.Create(ctx, sameNumber), core.ErrConflict)

			// Voiding v1 makes room for v2.
			noErr(t, "Void()", v1.Void(quote.VersionVoidReasonManual, at(time.Minute)))
			noErr(t, "Update() voided", r.QuoteVersions.Update(ctx, v1))
			got, err = r.QuoteVersions.Get(ctx, org, v1.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, v1)
			noErr(t, "Create() v2", r.QuoteVersions.Create(ctx, second))
			got, err = r.QuoteVersions.Get(ctx, org, second.ID)
			noErr(t, "Get() v2", err)
			equal(t, "Get() v2 (no content, no currency)", got, second)
			v3 := quoteFixtureVersion(e, q, 3, quote.VersionStatusVoided)
			noErr(t, "Create() voided v3", r.QuoteVersions.Create(ctx, v3))

			// Update follows the same rules.
			back := *v1
			back.Status, back.VoidReason, back.VoidedAt = quote.VersionStatusDraft, "", nil
			wantErr(t, "Update() to a second active version", r.QuoteVersions.Update(ctx, &back), core.ErrConflict)
			renumber := *v3
			renumber.Version = 2
			wantErr(t, "Update() to a used version number", r.QuoteVersions.Update(ctx, &renumber), core.ErrConflict)
			wantErr(t, "Update() unknown", r.QuoteVersions.Update(ctx, quoteFixtureVersion(e, q, 9, quote.VersionStatusVoided)), core.ErrNotFound)

			list, err := r.QuoteVersions.ListByQuote(ctx, org, q.ID)
			noErr(t, "ListByQuote()", err)
			equal(t, "ListByQuote()", list, []*quote.Version{v3, second, v1})
			cur, err := r.QuoteVersions.GetCurrent(ctx, org, q.ID)
			noErr(t, "GetCurrent()", err)
			equal(t, "GetCurrent()", cur, v3)
			_, err = r.QuoteVersions.GetCurrent(ctx, org, empty.ID)
			wantErr(t, "GetCurrent() without versions", err, core.ErrNotFound)
			list, err = r.QuoteVersions.ListByQuote(ctx, org, empty.ID)
			noErr(t, "ListByQuote() without versions", err)
			equal(t, "ListByQuote() without versions", len(list), 0)
			_, err = r.QuoteVersions.Get(ctx, org, core.NewID[core.QuoteVersionID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Isolation.
			_, err = r.QuoteVersions.Get(ctx, pb.org, v1.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.QuoteVersions.GetCurrent(ctx, pb.org, q.ID)
			wantErr(t, "GetCurrent() from another organization", err, core.ErrNotFound)
			list, err = r.QuoteVersions.ListByQuote(ctx, pb.org, q.ID)
			noErr(t, "ListByQuote() from another organization", err)
			equal(t, "ListByQuote() from another organization", len(list), 0)
			foreign := *v3
			foreign.OrganizationID = pb.org
			wantErr(t, "Update() from another organization", r.QuoteVersions.Update(ctx, &foreign), core.ErrNotFound)
			return nil
		})
	})

	t.Run("order forms", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		p := quoteFixtureParents(e, a)
		pb := quoteFixtureParents(e, b)
		org := p.org
		vs := quoteFixtureChain(e, p, 4)
		vb := quoteFixtureChain(e, pb, 1)

		full := quoteFixtureForm(e, p, vs[0], 4, now)
		full.Status = quote.OrderFormStatusSigned
		full.ExpiresAt = ptr(at(72 * time.Hour))
		full.SignedAt = ptr(at(time.Hour))
		full.SignedBy = ptr("Jane Doe")
		full.SignedDocument = "blob/signed.pdf"
		bare := quoteFixtureForm(e, p, vs[1], 5, now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.OrderForms.Create(ctx, full))
			noErr(t, "Create() bare", r.OrderForms.Create(ctx, bare))
			wantErr(t, "Create() with the same ID", r.OrderForms.Create(ctx, full), core.ErrConflict)
			dupVersion := quoteFixtureForm(e, p, vs[0], 6, now)
			wantErr(t, "Create() a second order form of the version", r.OrderForms.Create(ctx, dupVersion), core.ErrConflict)
			dupNumber := quoteFixtureForm(e, p, vs[2], 4, now)
			wantErr(t, "Create() with a used number", r.OrderForms.Create(ctx, dupNumber), core.ErrConflict)

			got, err := r.OrderForms.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.OrderForms.Get(ctx, org, bare.ID)
			noErr(t, "Get() bare", err)
			equal(t, "Get() bare", got, bare)
			got, err = r.OrderForms.GetByNumber(ctx, org, "OF-2025-0004")
			noErr(t, "GetByNumber()", err)
			equal(t, "GetByNumber()", got, full)
			got, err = r.OrderForms.GetByVersion(ctx, org, vs[1].ID)
			noErr(t, "GetByVersion()", err)
			equal(t, "GetByVersion()", got, bare)
			_, err = r.OrderForms.GetByVersion(ctx, org, vs[3].ID)
			wantErr(t, "GetByVersion() without order form", err, core.ErrNotFound)
			_, err = r.OrderForms.GetByNumber(ctx, org, "OF-2025-0099")
			wantErr(t, "GetByNumber() unknown", err, core.ErrNotFound)
			_, err = r.OrderForms.Get(ctx, org, core.NewID[core.OrderFormID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			// Update: void the bare form, clear the full one's signature.
			bare.Status, bare.VoidReason, bare.VoidedAt = quote.OrderFormStatusVoided, quote.OrderFormVoidReasonInvalid, ptr(at(time.Minute))
			bare.UpdatedAt = at(time.Minute)
			noErr(t, "Update()", r.OrderForms.Update(ctx, bare))
			got, err = r.OrderForms.Get(ctx, org, bare.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, bare)
			full.SignedBy, full.SignedDocument, full.ExpiresAt = nil, "", nil
			noErr(t, "Update() full", r.OrderForms.Update(ctx, full))
			got, err = r.OrderForms.Get(ctx, org, full.ID)
			noErr(t, "Get() full after Update()", err)
			equal(t, "Get() full after Update()", got, full)

			clash := *bare
			clash.QuoteVersionID = full.QuoteVersionID
			wantErr(t, "Update() to a used version", r.OrderForms.Update(ctx, &clash), core.ErrConflict)
			clash = *bare
			clash.Number = full.Number
			wantErr(t, "Update() to a used number", r.OrderForms.Update(ctx, &clash), core.ErrConflict)
			wantErr(t, "Update() unknown", r.OrderForms.Update(ctx, quoteFixtureForm(e, p, vs[3], 50, now)), core.ErrNotFound)
			got, err = r.OrderForms.Get(ctx, org, bare.ID)
			noErr(t, "Get() after the conflicts", err)
			equal(t, "Get() after the conflicts", got, bare)

			// Isolation.
			_, err = r.OrderForms.Get(ctx, pb.org, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.OrderForms.GetByNumber(ctx, pb.org, full.Number)
			wantErr(t, "GetByNumber() from another organization", err, core.ErrNotFound)
			_, err = r.OrderForms.GetByVersion(ctx, pb.org, vs[0].ID)
			wantErr(t, "GetByVersion() from another organization", err, core.ErrNotFound)
			foreign := quoteFixtureForm(e, pb, vb[0], 1, now)
			foreign.ID = full.ID
			wantErr(t, "Update() from another organization", r.OrderForms.Update(ctx, foreign), core.ErrNotFound)
			page, err := r.OrderForms.List(ctx, pb.org, quote.OrderFormFilter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(page.Items), 0)
			return nil
		})
	})

	t.Run("order form sequences, lists and expiry", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		p := quoteFixtureParents(e, a)
		pb := quoteFixtureParents(e, b)
		org := p.org
		vs := quoteFixtureChain(e, p, 4)
		vb := quoteFixtureChain(e, pb, 2)
		lastYear := time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC)

		fs := []*quote.OrderForm{
			quoteFixtureForm(e, p, vs[0], 2, now),
			quoteFixtureForm(e, p, vs[1], 9, lastYear),
			quoteFixtureForm(e, p, vs[2], 1, now),
			quoteFixtureForm(e, p, vs[3], 3, now),
		}
		fs[0].ExpiresAt = ptr(at(-time.Hour))
		fs[1].ExpiresAt = ptr(at(-48 * time.Hour))
		fs[1].CustomerID = p.customers[1]
		fs[2].ExpiresAt = ptr(at(-time.Hour))
		fs[2].Status, fs[2].SignedAt = quote.OrderFormStatusSigned, ptr(now) // not expiring once signed
		fs[3].ExpiresAt = ptr(at(time.Hour))                                 // not yet
		fs[3].CustomerID = p.customers[1]
		fb := []*quote.OrderForm{quoteFixtureForm(e, pb, vb[0], 1, now), quoteFixtureForm(e, pb, vb[1], 2, now)}
		fb[0].ExpiresAt = ptr(now) // expires_at must be strictly before
		fb[1].ExpiresAt = ptr(at(-time.Minute))
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, f := range append(append([]*quote.OrderForm{}, fs...), fb...) {
				noErr(t, "Create()", r.OrderForms.Create(ctx, f))
			}
			for _, tc := range []struct {
				org  core.OrganizationID
				year int
				want int
			}{{org, 2025, 4}, {org, 2024, 10}, {org, 2026, 1}, {pb.org, 2025, 3}} {
				got, err := r.OrderForms.NextSequence(ctx, tc.org, tc.year)
				noErr(t, "NextSequence()", err)
				if got != tc.want {
					t.Errorf("NextSequence(%d) got = %d, want = %d", tc.year, got, tc.want)
				}
			}

			fid := func(f *quote.OrderForm) core.OrderFormID { return f.ID }
			exp, err := r.OrderForms.ListExpiring(ctx, now, 0)
			noErr(t, "ListExpiring()", err)
			want := []*quote.OrderForm{fs[0], fs[1], fb[1]}
			equal(t, "ListExpiring()", exp, want, quoteSortByID[*quote.OrderForm](fid))
			exp, err = r.OrderForms.ListExpiring(ctx, now, 2)
			noErr(t, "ListExpiring() limited", err)
			equal(t, "ListExpiring() limited count", len(exp), 2)
			exp, err = r.OrderForms.ListExpiring(ctx, at(-72*time.Hour), 0)
			noErr(t, "ListExpiring() earlier", err)
			equal(t, "ListExpiring() earlier", len(exp), 0)
			return nil
		})

		fid := func(f *quote.OrderForm) core.OrderFormID { return f.ID }
		for _, tc := range []struct {
			name string
			f    quote.OrderFormFilter
			want []*quote.OrderForm
		}{
			{"all", quote.OrderFormFilter{}, fs},
			{"customer", quote.OrderFormFilter{CustomerID: p.customers[1]}, []*quote.OrderForm{fs[1], fs[3]}},
			{"statuses", quote.OrderFormFilter{Statuses: []quote.OrderFormStatus{quote.OrderFormStatusSigned, quote.OrderFormStatusVoided}}, []*quote.OrderForm{fs[2]}},
			{"combined", quote.OrderFormFilter{CustomerID: p.customers[1], Statuses: []quote.OrderFormStatus{quote.OrderFormStatusGenerated}}, []*quote.OrderForm{fs[1], fs[3]}},
		} {
			e.tx(func(ctx context.Context, r port.Repos) error {
				got := listAll(t, 1, func(pr core.PageRequest) (core.Page[*quote.OrderForm], error) {
					return r.OrderForms.List(ctx, org, tc.f, pr)
				})
				equal(t, "List() "+tc.name, ids(got, fid), ids(tc.want, fid))
				if tc.name == "all" {
					equal(t, "List() all items", got, tc.want)
				}
				return nil
			})
		}
	})

	t.Run("orders", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		p := quoteFixtureParents(e, a)
		pb := quoteFixtureParents(e, b)
		org := p.org
		vs := quoteFixtureChain(e, p, 3)
		vb := quoteFixtureChain(e, pb, 1)
		var forms []*quote.OrderForm
		var formB *quote.OrderForm
		e.tx(func(ctx context.Context, r port.Repos) error {
			for i, v := range vs {
				f := quoteFixtureForm(e, p, v, i+1, now)
				noErr(t, "Create() order form", r.OrderForms.Create(ctx, f))
				forms = append(forms, f)
			}
			formB = quoteFixtureForm(e, pb, vb[0], 1, now)
			noErr(t, "Create() order form", r.OrderForms.Create(ctx, formB))
			return nil
		})

		full := quoteFixtureOrder(e, forms[0], 3, now)
		full.Status = quote.OrderStatusExecuted
		full.ExecutionMode = quote.ExecutionModeExecute
		full.ExecuteAt = ptr(at(time.Hour))
		full.ExecutedAt = ptr(at(2 * time.Hour))
		full.ExecutionRecord = json.RawMessage(`{"subscription_ids": ["a", "b"], "errors": []}`)
		bare := quoteFixtureOrder(e, forms[1], 4, now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Create()", r.Orders.Create(ctx, full))
			noErr(t, "Create() bare", r.Orders.Create(ctx, bare))
			wantErr(t, "Create() with the same ID", r.Orders.Create(ctx, full), core.ErrConflict)
			wantErr(t, "Create() a second order of the order form", r.Orders.Create(ctx, quoteFixtureOrder(e, forms[0], 9, now)), core.ErrConflict)
			wantErr(t, "Create() with a used number", r.Orders.Create(ctx, quoteFixtureOrder(e, forms[2], 3, now)), core.ErrConflict)

			got, err := r.Orders.Get(ctx, org, full.ID)
			noErr(t, "Get()", err)
			equal(t, "Get()", got, full)
			got, err = r.Orders.Get(ctx, org, bare.ID)
			noErr(t, "Get() bare", err)
			equal(t, "Get() bare", got, bare)
			got, err = r.Orders.GetByNumber(ctx, org, "OR-2025-0003")
			noErr(t, "GetByNumber()", err)
			equal(t, "GetByNumber()", got, full)
			_, err = r.Orders.GetByNumber(ctx, org, "OR-2025-0099")
			wantErr(t, "GetByNumber() unknown", err, core.ErrNotFound)
			_, err = r.Orders.Get(ctx, org, core.NewID[core.OrderID](e.ids))
			wantErr(t, "Get() unknown", err, core.ErrNotFound)

			noErr(t, "Fail()", bare.Fail(json.RawMessage(`{"errors": ["boom"]}`), at(time.Minute)))
			noErr(t, "Update()", r.Orders.Update(ctx, bare))
			got, err = r.Orders.Get(ctx, org, bare.ID)
			noErr(t, "Get() after Update()", err)
			equal(t, "Get() after Update()", got, bare)
			full.ExecutionRecord, full.ExecuteAt = nil, nil
			noErr(t, "Update() full", r.Orders.Update(ctx, full))
			got, err = r.Orders.Get(ctx, org, full.ID)
			noErr(t, "Get() full after Update()", err)
			equal(t, "Get() full after Update()", got, full)

			clash := *bare
			clash.OrderFormID = full.OrderFormID
			wantErr(t, "Update() to a used order form", r.Orders.Update(ctx, &clash), core.ErrConflict)
			clash = *bare
			clash.Number = full.Number
			wantErr(t, "Update() to a used number", r.Orders.Update(ctx, &clash), core.ErrConflict)
			wantErr(t, "Update() unknown", r.Orders.Update(ctx, quoteFixtureOrder(e, forms[2], 50, now)), core.ErrNotFound)
			got, err = r.Orders.Get(ctx, org, bare.ID)
			noErr(t, "Get() after the conflicts", err)
			equal(t, "Get() after the conflicts", got, bare)

			// Isolation.
			_, err = r.Orders.Get(ctx, pb.org, full.ID)
			wantErr(t, "Get() from another organization", err, core.ErrNotFound)
			_, err = r.Orders.GetByNumber(ctx, pb.org, full.Number)
			wantErr(t, "GetByNumber() from another organization", err, core.ErrNotFound)
			foreign := quoteFixtureOrder(e, formB, 1, now)
			foreign.ID = full.ID
			wantErr(t, "Update() from another organization", r.Orders.Update(ctx, foreign), core.ErrNotFound)
			page, err := r.Orders.List(ctx, pb.org, quote.OrderFilter{}, core.PageRequest{})
			noErr(t, "List() of another organization", err)
			equal(t, "List() of another organization", len(page.Items), 0)
			return nil
		})
	})

	t.Run("order sequences, lists and execution", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		a, b := e.twoOrgs()
		p := quoteFixtureParents(e, a)
		pb := quoteFixtureParents(e, b)
		org := p.org
		vs := quoteFixtureChain(e, p, 4)
		vb := quoteFixtureChain(e, pb, 1)
		lastYear := time.Date(2024, time.June, 1, 0, 0, 0, 0, time.UTC)

		var ords, ob []*quote.Order
		e.tx(func(ctx context.Context, r port.Repos) error {
			for i, v := range vs {
				f := quoteFixtureForm(e, p, v, i+1, now)
				if i%2 == 1 {
					f.CustomerID = p.customers[1]
				}
				noErr(t, "Create() order form", r.OrderForms.Create(ctx, f))
				ords = append(ords, quoteFixtureOrder(e, f, []int{2, 7, 1, 3}[i], []time.Time{now, lastYear, now, now}[i]))
			}
			f := quoteFixtureForm(e, pb, vb[0], 1, now)
			noErr(t, "Create() order form", r.OrderForms.Create(ctx, f))
			ob = append(ob, quoteFixtureOrder(e, f, 1, now))
			return nil
		})
		ords[0].ExecutionMode, ords[0].ExecuteAt = quote.ExecutionModeExecute, ptr(now) // due exactly now
		ords[1].ExecutionMode, ords[1].ExecuteAt = quote.ExecutionModeOrderOnly, ptr(at(-time.Hour))
		ords[2].ExecutionMode, ords[2].ExecuteAt = quote.ExecutionModeExecute, ptr(at(time.Hour)) // not yet
		ords[3].Status, ords[3].ExecutionMode = quote.OrderStatusExecuted, quote.ExecutionModeExecute
		ords[3].ExecuteAt, ords[3].ExecutedAt = ptr(at(-time.Hour)), ptr(now) // already executed
		ob[0].ExecutionMode, ob[0].ExecuteAt = quote.ExecutionModeExecute, ptr(at(-time.Minute))
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, o := range append(append([]*quote.Order{}, ords...), ob...) {
				noErr(t, "Create()", r.Orders.Create(ctx, o))
			}
			for _, tc := range []struct {
				org  core.OrganizationID
				year int
				want int
			}{{org, 2025, 4}, {org, 2024, 8}, {org, 2026, 1}, {pb.org, 2025, 2}} {
				got, err := r.Orders.NextSequence(ctx, tc.org, tc.year)
				noErr(t, "NextSequence()", err)
				if got != tc.want {
					t.Errorf("NextSequence(%d) got = %d, want = %d", tc.year, got, tc.want)
				}
			}

			oid := func(o *quote.Order) core.OrderID { return o.ID }
			due, err := r.Orders.ListExecutable(ctx, now, 0)
			noErr(t, "ListExecutable()", err)
			equal(t, "ListExecutable()", due, []*quote.Order{ords[0], ords[1], ob[0]}, quoteSortByID[*quote.Order](oid))
			due, err = r.Orders.ListExecutable(ctx, now, 1)
			noErr(t, "ListExecutable() limited", err)
			equal(t, "ListExecutable() limited count", len(due), 1)
			due, err = r.Orders.ListExecutable(ctx, at(-2*time.Hour), 0)
			noErr(t, "ListExecutable() earlier", err)
			equal(t, "ListExecutable() earlier", len(due), 0)
			return nil
		})

		oid := func(o *quote.Order) core.OrderID { return o.ID }
		for _, tc := range []struct {
			name string
			f    quote.OrderFilter
			want []*quote.Order
		}{
			{"all", quote.OrderFilter{}, ords},
			{"customer", quote.OrderFilter{CustomerID: p.customers[1]}, []*quote.Order{ords[1], ords[3]}},
			{"statuses", quote.OrderFilter{Statuses: []quote.OrderStatus{quote.OrderStatusExecuted, quote.OrderStatusFailed}}, []*quote.Order{ords[3]}},
			{"combined", quote.OrderFilter{CustomerID: p.customers[0], Statuses: []quote.OrderStatus{quote.OrderStatusCreated}}, []*quote.Order{ords[0], ords[2]}},
		} {
			e.tx(func(ctx context.Context, r port.Repos) error {
				got := listAll(t, 1, func(pr core.PageRequest) (core.Page[*quote.Order], error) {
					return r.Orders.List(ctx, org, tc.f, pr)
				})
				equal(t, "List() "+tc.name, ids(got, oid), ids(tc.want, oid))
				if tc.name == "all" {
					equal(t, "List() all items", got, tc.want)
				}
				return nil
			})
		}
	})
}
