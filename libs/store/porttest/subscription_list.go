package porttest

import (
	"cmp"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// subscriptionListSkipped are the cases of the subscription list spec that
// are not ported.
var subscriptionListSkipped = []skippedCase{
	{"spec/queries/subscriptions_query_spec.rb:71", "offset pagination (page, current_page); the cursor pagination is checked by every case paging two by two"},
}

// subscriptionListEnv builds the data set of one scenario of
// spec/queries/subscriptions_query_spec.rb: an organization (and another
// one whose rows no list may return), a customer and a plan.
type subscriptionListEnv struct {
	e        *env
	org      core.OrganizationID
	entity   *organization.BillingEntity
	customer *customer.Customer
	plan     *catalog.Plan
	n        int
}

func newSubscriptionListEnv(e *env) *subscriptionListEnv {
	e.t.Helper()
	a, b := e.twoOrgs()
	x := &subscriptionListEnv{e: e, org: a.OrganizationID, entity: a}
	x.customer = x.newCustomer("cust_ext", func(c *customer.Customer) {
		c.Name, c.Firstname, c.Lastname, c.Email = ptr("Customer Name"), ptr("Firstname"), ptr("Lastname"), ptr("customer@example.com")
	})
	x.plan = x.newPlan("test_plan", "Test Plan", nil)
	// A subscription of another organization matching every search.
	e.tx(func(ctx context.Context, r port.Repos) error {
		c := subscriptionFixtureCustomer(ctx, e, r, b, "cust_ext")
		p := subscriptionFixturePlan(ctx, e, r, b.OrganizationID, "test_plan")
		s := subscription.New(b.OrganizationID, core.NewID[core.SubscriptionID](e.ids), c.ID, p.ID, "ext-alpha", now, subscription.PlanFacts{}, now)
		s.Name = ptr("Test Subscription")
		return r.Subscriptions.Create(ctx, s)
	})
	return x
}

func (x *subscriptionListEnv) newCustomer(ext string, fn func(c *customer.Customer)) *customer.Customer {
	c := customer.New(x.org, core.NewID[core.CustomerID](x.e.ids), ext, x.entity.ID, now)
	if fn != nil {
		fn(c)
	}
	x.e.tx(func(ctx context.Context, r port.Repos) error { return r.Customers.Create(ctx, c) })
	return c
}

func (x *subscriptionListEnv) newPlan(code, name string, fn func(p *catalog.Plan)) *catalog.Plan {
	p := &catalog.Plan{
		ID: core.NewID[core.PlanID](x.e.ids), OrganizationID: x.org, Code: code, Name: name,
		Interval: period.Monthly, Amount: 1000, Currency: money.MustCurrency("EUR"), Status: catalog.PlanStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if fn != nil {
		fn(p)
	}
	x.e.tx(func(ctx context.Context, r port.Repos) error { return r.Plans.Create(ctx, p) })
	return p
}

// sub creates a subscription of c to p in status st. Each subscription has
// a later subscription_at than the previous one, as the factories of the
// spec create them.
func (x *subscriptionListEnv) sub(c *customer.Customer, p *catalog.Plan, ext string, st subscription.Status, fn func(s *subscription.Subscription)) *subscription.Subscription {
	x.n++
	s := subscription.New(x.org, core.NewID[core.SubscriptionID](x.e.ids), c.ID, p.ID, ext,
		at(time.Duration(x.n)*time.Second), subscription.PlanFacts{}, now)
	s.Status = st
	if st != subscription.StatusPending {
		s.StartedAt = ptr(s.SubscriptionAt)
	}
	switch st {
	case subscription.StatusTerminated:
		s.TerminatedAt = ptr(at(time.Hour))
	case subscription.StatusCanceled:
		s.CanceledAt = ptr(at(time.Hour))
	}
	if fn != nil {
		fn(s)
	}
	x.e.tx(func(ctx context.Context, r port.Repos) error { return r.Subscriptions.Create(ctx, s) })
	return s
}

// subscriptionListCase is one ported case: the filter and the
// subscriptions it lists, in any order.
type subscriptionListCase struct {
	name   string
	source string
	f      subscription.Filter
	want   []*subscription.Subscription
}

// checkSubscriptionList lists with every case's filter, page by page (two
// items a page) in both orders, and compares the IDs with the expected
// subscriptions in creation order, and by subscription_at then ID
// descending.
func checkSubscriptionList(t *testing.T, x *subscriptionListEnv, cases []subscriptionListCase) {
	t.Helper()
	x.e.tx(func(ctx context.Context, r port.Repos) error {
		for _, tc := range cases {
			for _, order := range []core.ListOrder{core.OrderCreated, core.OrderRecent} {
				f := tc.f
				f.Order = order
				got := listAll(t, 2, func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
					return r.Subscriptions.List(ctx, x.org, f, p)
				})
				want := slices.Clone(tc.want)
				if order == core.OrderRecent {
					slices.SortFunc(want, func(a, b *subscription.Subscription) int {
						return cmp.Or(b.SubscriptionAt.Compare(a.SubscriptionAt), b.ID.Compare(a.ID))
					})
				} else {
					slices.SortFunc(want, func(a, b *subscription.Subscription) int { return a.ID.Compare(b.ID) })
				}
				equal(t, "List() "+tc.name+" ("+order.String()+", "+tc.source+")", ids(got, subID), ids(want, subID))
			}
		}
		return nil
	})
}

func testSubscriptionList(t *testing.T, h Harness) {
	const spec = "spec/queries/subscriptions_query_spec.rb:"
	checkSkipped(t, subscriptionListSkipped)
	ss := func(s ...*subscription.Subscription) []*subscription.Subscription { return s }
	active := subscription.StatusActive
	pending, canceled, terminated := subscription.StatusPending, subscription.StatusCanceled, subscription.StatusTerminated

	t.Run("filters", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		x := newSubscriptionListEnv(e)
		base := x.sub(x.customer, x.plan, "ext-base", active, nil)
		checkSubscriptionList(t, x, []subscriptionListCase{
			{"all subscriptions", spec + "21", subscription.Filter{}, ss(base)},
			{"external ID", spec + "200", subscription.Filter{ExternalID: "ext-base"}, ss(base)},
			{"external customer ID", spec + "222", subscription.Filter{ExternalCustomerID: "cust_ext"}, ss(base)},
			{"unknown external customer ID", "", subscription.Filter{ExternalCustomerID: "nope"}, nil},
			{"plan code", spec + "231", subscription.Filter{PlanCode: "test_plan"}, ss(base)},
			{"unknown plan code", "", subscription.Filter{PlanCode: "nope"}, nil},
		})
		// The search term is ignored with an external ID filter.
		named := x.sub(x.newCustomer("other", nil), x.plan, "ext-named", active, func(s *subscription.Subscription) { s.Name = ptr("ext-base") })
		checkSubscriptionList(t, x, []subscriptionListCase{
			{"external ID ignores the search term", spec + "212", subscription.Filter{ExternalID: "ext-base", Search: "ext-base"}, ss(base)},
			{"search without the external ID filter", "", subscription.Filter{Search: "ext-base"}, ss(base, named)},
		})
	})

	t.Run("search", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		x := newSubscriptionListEnv(e)
		s1 := x.sub(x.customer, x.plan, "ext-alpha", active, func(s *subscription.Subscription) { s.Name = ptr("Test Subscription") })
		s2 := x.sub(x.customer, x.plan, "ext-beta", active, func(s *subscription.Subscription) { s.Name = ptr("Test Subscription 2") })
		otherPlan := x.newPlan("other_plan", "Other Plan", nil)
		// The spec's other subscription has the same customer, but is only
		// created for the plan searches; it has its own customer here so
		// that every search runs on one data set.
		otherCustomer := x.newCustomer("someone", func(c *customer.Customer) { c.Name = ptr("Someone Else") })
		other := x.sub(otherCustomer, otherPlan, "ext-gamma", active, func(s *subscription.Subscription) { s.Name = ptr("Other Subscription") })
		checkSubscriptionList(t, x, []subscriptionListCase{
			{"ID", spec + "90", subscription.Filter{Search: s1.ID.String()}, ss(s1)},
			{"name", spec + "100", subscription.Filter{Search: "Test Subscription 2"}, ss(s2)},
			{"external ID", spec + "110", subscription.Filter{Search: "ext-alpha"}, ss(s1)},
			{"plan name", spec + "122", subscription.Filter{Search: "Test Plan"}, ss(s1, s2)},
			{"plan code", spec + "134", subscription.Filter{Search: "test_plan"}, ss(s1, s2)},
			{"customer name", spec + "144", subscription.Filter{Search: "Customer Name"}, ss(s1, s2)},
			{"customer first name", spec + "154", subscription.Filter{Search: "Firstname"}, ss(s1, s2)},
			{"customer last name", spec + "164", subscription.Filter{Search: "Lastname"}, ss(s1, s2)},
			{"customer external ID", spec + "174", subscription.Filter{Search: "cust_ext"}, ss(s1, s2)},
			{"customer email", "app/queries/subscriptions_query.rb:92", subscription.Filter{Search: "customer@example"}, ss(s1, s2)},
			{"case-insensitive and trimmed", "", subscription.Filter{Search: "  other SUBSCRIPTION "}, ss(other)},
			{"LIKE wildcards matched literally", "", subscription.Filter{Search: "ext_alpha"}, nil},
			{
				"customers not searched with an external customer ID filter", "app/queries/subscriptions_query.rb:99",
				subscription.Filter{Search: "Customer Name", ExternalCustomerID: "cust_ext"},
				nil,
			},
			{"search and external customer ID", "", subscription.Filter{Search: "Test", ExternalCustomerID: "cust_ext"}, ss(s1, s2)},
		})
		// A deleted customer is not searched.
		noErr(t, "Archive()", otherCustomer.Archive(at(time.Hour)))
		e.tx(func(ctx context.Context, r port.Repos) error { return r.Customers.Update(ctx, otherCustomer) })
		checkSubscriptionList(t, x, []subscriptionListCase{
			{"deleted customer not searched", "app/models/customer.rb:139", subscription.Filter{Search: "Someone Else"}, nil},
			{"deleted customer not filtered by external ID", "app/models/customer.rb:139", subscription.Filter{ExternalCustomerID: "someone"}, nil},
		})
	})

	t.Run("deleted plans", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		x := newSubscriptionListEnv(e)
		gone := x.newPlan("gone_plan", "Gone Plan", nil)
		old := x.sub(x.customer, gone, "ext-old", terminated, nil)
		noErr(t, "MarkDeleted()", gone.MarkDeleted(at(2*time.Hour)))
		e.tx(func(ctx context.Context, r port.Repos) error { return r.Plans.Update(ctx, gone) })
		// The code of the deleted plan is used again.
		again := x.newPlan("gone_plan", "Gone Plan", nil)
		cur := x.sub(x.customer, again, "ext-new", active, nil)
		checkSubscriptionList(t, x, []subscriptionListCase{
			{"subscriptions of a deleted plan", "", subscription.Filter{}, ss(old, cur)},
			{"plan ID of a deleted plan", "", subscription.Filter{PlanID: gone.ID}, ss(old)},
			{"plan code skips deleted plans", "app/models/plan.rb:63", subscription.Filter{PlanCode: "gone_plan"}, ss(cur)},
			{"currency skips deleted plans", "app/queries/subscriptions_query.rb:129", subscription.Filter{Currency: money.MustCurrency("EUR")}, ss(cur)},
			{"overridden skips deleted plans", "app/queries/subscriptions_query.rb:124", subscription.Filter{Overridden: ptr(false)}, ss(cur)},
			{"plan search skips deleted plans", "app/queries/subscriptions_query.rb:81", subscription.Filter{Search: "Gone Plan"}, ss(cur)},
		})
	})

	t.Run("search and overridden", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		x := newSubscriptionListEnv(e)
		x.sub(x.customer, x.plan, "ext-alpha", active, func(s *subscription.Subscription) { s.Name = ptr("Test Subscription") })
		x.sub(x.customer, x.plan, "ext-beta", active, func(s *subscription.Subscription) { s.Name = ptr("Test Subscription 2") })
		child := x.newPlan("test_plan", "Override", func(p *catalog.Plan) { p.ParentID = x.plan.ID })
		s3 := x.sub(x.customer, child, "ext-delta", active, func(s *subscription.Subscription) { s.Name = ptr("Test Subscription 3") })
		checkSubscriptionList(t, x, []subscriptionListCase{
			{"search and overridden", spec + "189", subscription.Filter{Search: "Firstname", Overridden: ptr(true)}, ss(s3)},
		})
	})

	t.Run("statuses", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		x := newSubscriptionListEnv(e)
		act := x.sub(x.customer, x.plan, "ext-active", active, nil)
		pen := x.sub(x.customer, x.plan, "ext-pending", pending, nil)
		can := x.sub(x.customer, x.plan, "ext-canceled", canceled, nil)
		ter := x.sub(x.customer, x.plan, "ext-terminated", terminated, nil)
		pen2 := x.sub(x.customer, x.plan, "ext-pending-2", pending, nil)
		checkSubscriptionList(t, x, []subscriptionListCase{
			{"active and pending", spec + "240", subscription.Filter{Statuses: []subscription.Status{active, pending}}, ss(act, pen, pen2)},
			{"pending", spec + "265", subscription.Filter{Statuses: []subscription.Status{pending}}, ss(pen, pen2)},
			{"canceled", spec + "284", subscription.Filter{Statuses: []subscription.Status{canceled}}, ss(can)},
			{"terminated", spec + "301", subscription.Filter{Statuses: []subscription.Status{terminated}}, ss(ter)},
			{"no status", spec + "316", subscription.Filter{}, ss(act, pen, can, ter, pen2)},
		})
		// The most recent subscription_at comes first
		// (spec/queries/subscriptions_query_spec.rb:276).
		e.tx(func(ctx context.Context, r port.Repos) error {
			p, err := r.Subscriptions.List(ctx, x.org, subscription.Filter{Statuses: []subscription.Status{pending}, Order: core.OrderRecent}, core.PageRequest{})
			noErr(t, "List()", err)
			equal(t, "List() first pending", ids(p.Items, subID), []core.SubscriptionID{pen2.ID, pen.ID})
			return nil
		})
	})

	t.Run("overridden", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		x := newSubscriptionListEnv(e)
		child := x.newPlan("test_plan", "Override", func(p *catalog.Plan) { p.ParentID = x.plan.ID })
		overridden := x.sub(x.customer, child, "ext-child", active, nil)
		original := x.sub(x.customer, x.plan, "ext-parent", active, nil)
		checkSubscriptionList(t, x, []subscriptionListCase{
			{"overridden", spec + "343", subscription.Filter{Overridden: ptr(true)}, ss(overridden)},
			{"not overridden", spec + "353", subscription.Filter{Overridden: ptr(false)}, ss(original)},
			{"without the filter", spec + "361", subscription.Filter{}, ss(overridden, original)},
			{"plan code includes override plans", "app/queries/subscriptions_query.rb:112", subscription.Filter{PlanCode: "test_plan"}, ss(overridden, original)},
		})
	})

	t.Run("billing entities", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		x := newSubscriptionListEnv(e)
		us := organization.NewBillingEntity(x.org, core.NewID[core.BillingEntityID](e.ids), "us", "US", now)
		eu := organization.NewBillingEntity(x.org, core.NewID[core.BillingEntityID](e.ids), "eu", "EU", now)
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "BillingEntities.Create()", r.BillingEntities.Create(ctx, us))
			return r.BillingEntities.Create(ctx, eu)
		})
		stamp := func(be *organization.BillingEntity) func(s *subscription.Subscription) {
			return func(s *subscription.Subscription) {
				if be != nil {
					s.BillingEntityID = be.ID
				}
			}
		}
		usSub := x.sub(x.customer, x.plan, "ext-us", active, stamp(us))
		euSub := x.sub(x.customer, x.plan, "ext-eu", active, stamp(eu))
		usCustomer := x.newCustomer("us_customer", func(c *customer.Customer) { c.BillingEntityID = us.ID })
		euCustomer := x.newCustomer("eu_customer", func(c *customer.Customer) { c.BillingEntityID = eu.ID })
		inheritsEU := x.sub(euCustomer, x.plan, "ext-inherits-eu", active, stamp(nil))
		inheritsUS := x.sub(usCustomer, x.plan, "ext-inherits-us", active, stamp(nil))
		explicitUSInheritEU := x.sub(euCustomer, x.plan, "ext-explicit-us", active, stamp(us))
		explicitEUInheritEU := x.sub(euCustomer, x.plan, "ext-explicit-eu", active, stamp(eu))
		checkSubscriptionList(t, x, []subscriptionListCase{
			{
				"one entity", spec + "384",
				subscription.Filter{BillingEntityIDs: []core.BillingEntityID{eu.ID}},
				ss(euSub, inheritsEU, explicitEUInheritEU),
			},
			{
				"several entities", spec + "393",
				subscription.Filter{BillingEntityIDs: []core.BillingEntityID{eu.ID, us.ID}},
				ss(usSub, euSub, inheritsEU, inheritsUS, explicitUSInheritEU, explicitEUInheritEU),
			},
			{
				"no entities", spec + "402",
				subscription.Filter{BillingEntityIDs: []core.BillingEntityID{}},
				ss(usSub, euSub, inheritsEU, inheritsUS, explicitUSInheritEU, explicitEUInheritEU),
			},
			{
				"explicit and inherited matches", spec + "424",
				subscription.Filter{BillingEntityIDs: []core.BillingEntityID{eu.ID}},
				ss(euSub, inheritsEU, explicitEUInheritEU),
			},
			{
				"explicit and inherited counted once", spec + "435",
				subscription.Filter{BillingEntityIDs: []core.BillingEntityID{eu.ID, us.ID}, Search: "ext-explicit-eu"},
				ss(explicitEUInheritEU),
			},
		})
	})

	t.Run("currency", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		x := newSubscriptionListEnv(e)
		eurPlan := x.newPlan("eur", "EUR", nil)
		usdPlan := x.newPlan("usd", "USD", func(p *catalog.Plan) { p.Currency = money.MustCurrency("USD") })
		eur := x.sub(x.customer, eurPlan, "ext-eur", active, nil)
		usd := x.sub(x.customer, usdPlan, "ext-usd", active, nil)
		checkSubscriptionList(t, x, []subscriptionListCase{
			{"currency", spec + "457", subscription.Filter{Currency: money.MustCurrency("EUR")}, ss(eur)},
			{"without the filter", spec + "466", subscription.Filter{}, ss(eur, usd)},
		})
	})

	t.Run("exclude next subscriptions", func(t *testing.T) {
		t.Parallel()
		// spec/queries/subscriptions_query_spec.rb:473: an active
		// subscription with a pending next one, a pending and a terminated
		// subscription.
		chain := func(t *testing.T, prevStatus, nextStatus subscription.Status) (x *subscriptionListEnv, prev, next, pen, ter *subscription.Subscription) {
			e := newEnv(t, h)
			x = newSubscriptionListEnv(e)
			prev = x.sub(x.customer, x.plan, "ext-chain", prevStatus, nil)
			next = x.sub(x.customer, x.plan, "ext-chain", nextStatus, func(s *subscription.Subscription) { s.PreviousSubscriptionID = prev.ID })
			pen = x.sub(x.customer, x.plan, "ext-pending", pending, nil)
			ter = x.sub(x.customer, x.plan, "ext-terminated", terminated, nil)
			return x, prev, next, pen, ter
		}
		excl := func(st ...subscription.Status) subscription.Filter {
			return subscription.Filter{ExcludeNextSubscriptions: true, Statuses: st}
		}
		t.Run("active previous", func(t *testing.T) {
			t.Parallel()
			x, prev, next, pen, ter := chain(t, active, pending)
			checkSubscriptionList(t, x, []subscriptionListCase{
				{"no status", spec + "489", excl(), ss(prev, pen, ter)},
				{"previous status", spec + "500", excl(active), ss(prev)},
				{"next status", spec + "510", excl(pending), ss(pen, next)},
				{"both statuses", spec + "520", excl(pending, active), ss(prev, pen)},
				{"neither status", spec + "530", excl(terminated), ss(ter)},
				{"without the filter", "", subscription.Filter{}, ss(prev, next, pen, ter)},
			})
		})
		t.Run("terminated previous", func(t *testing.T) {
			t.Parallel()
			x, prev, next, pen, ter := chain(t, terminated, pending)
			checkSubscriptionList(t, x, []subscriptionListCase{
				{"all subscriptions", spec + "578", excl(), ss(prev, next, pen, ter)},
				{"with a search term", spec + "587", subscription.Filter{ExcludeNextSubscriptions: true, Search: "Customer Name"}, ss(prev, next, pen, ter)},
			})
		})
		t.Run("canceled next", func(t *testing.T) {
			t.Parallel()
			x, prev, next, pen, ter := chain(t, active, canceled)
			checkSubscriptionList(t, x, []subscriptionListCase{
				{"all subscriptions", spec + "599", excl(), ss(prev, next, pen, ter)},
			})
		})
		t.Run("several statuses", func(t *testing.T) {
			t.Parallel()
			x, _, next, pen, _ := chain(t, terminated, pending)
			withPrev := func(ext string, st, prevStatus subscription.Status) (*subscription.Subscription, *subscription.Subscription) {
				p := x.sub(x.customer, x.plan, ext+"-prev", prevStatus, nil)
				return x.sub(x.customer, x.plan, ext, st, func(s *subscription.Subscription) { s.PreviousSubscriptionID = p.ID }), p
			}
			pendingWithout := x.sub(x.customer, x.plan, "ext-pending-without", pending, nil)
			activeWithout := x.sub(x.customer, x.plan, "ext-active-without", active, nil)
			pendingTermPrev, _ := withPrev("ext-pending-term", pending, terminated)
			activeTermPrev, _ := withPrev("ext-active-term", active, terminated)
			pendingPendPrev, pendPrev := withPrev("ext-pending-pend", pending, pending)
			checkSubscriptionList(t, x, []subscriptionListCase{
				{
					"without previous or with a non-matching previous", spec + "556", excl(pending, active),
					ss(next, pen, pendingWithout, activeWithout, pendingTermPrev, activeTermPrev, pendPrev),
				},
			})
			x.e.tx(func(ctx context.Context, r port.Repos) error {
				got := listAll(t, 3, func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
					return r.Subscriptions.List(ctx, x.org, excl(pending, active), p)
				})
				if slices.Contains(ids(got, subID), pendingPendPrev.ID) {
					t.Errorf("List() got = %v, want = without %v (%s)", ids(got, subID), pendingPendPrev.ID, spec+"570")
				}
				return nil
			})
		})
	})

	t.Run("ordering", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		x := newSubscriptionListEnv(e)
		first := x.sub(x.customer, x.plan, "ext-first", active, nil)
		// Same subscription_at: the latest created first
		// (spec/queries/subscriptions_query_spec.rb:41); same created_at too:
		// by ID ascending (spec/queries/subscriptions_query_spec.rb:62,
		// base_query.rb:111-112), consistently page after page.
		second := x.sub(x.customer, x.plan, "ext-second", active, func(s *subscription.Subscription) {
			s.SubscriptionAt, s.CreatedAt = first.SubscriptionAt, first.CreatedAt.Add(time.Second)
		})
		twin := x.sub(x.customer, x.plan, "ext-twin", active, func(s *subscription.Subscription) {
			s.SubscriptionAt, s.CreatedAt = first.SubscriptionAt, first.CreatedAt
		})
		older := x.sub(x.customer, x.plan, "ext-older", active, func(s *subscription.Subscription) { s.SubscriptionAt = at(-time.Hour) })
		latest := x.sub(x.customer, x.plan, "ext-latest", active, nil)
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, size := range []int{1, 2, 10} {
				got := listAll(t, size, func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
					return r.Subscriptions.List(ctx, x.org, subscription.Filter{Order: core.OrderRecent}, p)
				})
				equal(t, "List() most recent first", ids(got, subID), ids(ss(latest, second, first, twin, older), subID))
				got = listAll(t, size, func(p core.PageRequest) (core.Page[*subscription.Subscription], error) {
					return r.Subscriptions.List(ctx, x.org, subscription.Filter{}, p)
				})
				equal(t, "List() oldest first", ids(got, subID), ids(ss(first, second, twin, older, latest), subID))
			}
			return nil
		})
	})

	t.Run("invalid filters", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		_, a := e.org("Acme")
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, tc := range []struct {
				name  string
				f     subscription.Filter
				field string
			}{
				{"status", subscription.Filter{Statuses: []subscription.Status{active, "random"}}, "statuses[1]"},
				{"order", subscription.Filter{Order: 7}, "order"},
			} {
				_, err := r.Subscriptions.List(ctx, a.OrganizationID, tc.f, core.PageRequest{})
				wantErr(t, "List() invalid "+tc.name, err, core.ErrInvalidValue)
				if !hasFieldError(err, tc.field) {
					t.Errorf("List() invalid %s error got = %v, want = a field error on %s", tc.name, err, tc.field)
				}
			}
			for _, tc := range []struct {
				order core.ListOrder
				token string
			}{
				{core.OrderCreated, "not-a-token"},
				{core.OrderRecent, "not-a-token"},
				{core.OrderRecent, core.Cursor{Key: "yesterday", ID: core.NewID[core.SubscriptionID](e.ids).UUID()}.Encode()},
				{core.OrderRecent, core.NewID[core.SubscriptionID](e.ids).String()},
			} {
				_, err := r.Subscriptions.List(ctx, a.OrganizationID, subscription.Filter{Order: tc.order}, core.PageRequest{Token: tc.token})
				wantErr(t, "List() with a malformed page token ("+tc.order.String()+")", err, core.ErrInvalidValue)
			}
			return nil
		})
	})
}
