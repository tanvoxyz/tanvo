package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// customerListSkipped are the cases of the customer list spec that are not
// ported.
var customerListSkipped = []skippedCase{
	{"spec/queries/customers_query_spec.rb:127", "an unknown filter key (customer_types) is ignored by the Rails parameter handling; Go filters are typed"},
	{"spec/queries/customers_query_spec.rb:208", "offset pagination (page, total_count); the cursor pagination is checked by every case paging two by two"},
	{"spec/queries/customers_query_spec.rb:338", "a state given as a string instead of an array is a JSON typing error; Go filters are typed"},
	{"spec/queries/customers_query_spec.rb:355", "a zipcode given as a string instead of an array is a JSON typing error; Go filters are typed"},
}

// customerListFixture is the data set of spec/queries/customers_query_spec.rb.
type customerListFixture struct {
	org                   core.OrganizationID
	entity, entity2       *organization.BillingEntity
	first, second, third  *customer.Customer
	all                   []*customer.Customer
	otherOrganizationsRow *customer.Customer
}

// newCustomerListFixture creates the three customers of the spec (and a
// customer of another organization that no list may return).
func newCustomerListFixture(e *env) customerListFixture {
	e.t.Helper()
	a, b := e.twoOrgs()
	f := customerListFixture{org: a.OrganizationID, entity: a}
	f.entity2 = organization.NewBillingEntity(f.org, core.NewID[core.BillingEntityID](e.ids), "second", "Second", now)
	mk := func(created time.Time, fn func(c *customer.Customer)) *customer.Customer {
		c := customer.New(f.org, core.NewID[core.CustomerID](e.ids), "", a.ID, created)
		fn(c)
		return c
	}
	f.first = mk(now, func(c *customer.Customer) {
		c.Name, c.Firstname, c.Lastname, c.LegalName = ptr("defgh"), ptr("John"), ptr("Doe"), ptr("Legalname")
		c.ExternalID, c.Email = "11", ptr("1@example.com")
		c.BillingAddress = core.Address{Country: "US", State: "CA", Zipcode: "90001"}
		c.Currency = money.MustCurrency("USD")
		c.TaxIdentificationNumber = ptr("US123456789")
		c.CustomerType = customer.TypeCompany
		c.Metadata = core.Metadata{{Key: "id", Value: "1"}, {Key: "name", Value: "John Doe"}}
	})
	f.second = mk(at(time.Second), func(c *customer.Customer) {
		c.Name, c.Firstname, c.Lastname, c.LegalName = ptr("abcde"), ptr("Jane"), ptr("Smith"), ptr("other name")
		c.ExternalID, c.Email = "22", ptr("2@example.com")
		c.BillingAddress = core.Address{Country: "FR", State: "Paris", Zipcode: "75001"}
		c.Currency = money.MustCurrency("EUR")
		c.CustomerType = customer.TypeIndividual
		c.Metadata = core.Metadata{{Key: "id", Value: "2"}}
	})
	f.third = mk(at(2*time.Second), func(c *customer.Customer) {
		c.AccountType = customer.AccountTypePartner
		c.Name, c.Firstname, c.Lastname, c.LegalName = ptr("presuv"), ptr("Mary"), ptr("Johnson"), ptr("Company name")
		c.ExternalID, c.Email = "33", ptr("3@example.com")
		c.BillingAddress = core.Address{Country: "DE", State: "Berlin", Zipcode: "10115"}
		c.BillingEntityID = f.entity2.ID
		c.Currency = money.MustCurrency("EUR")
	})
	f.all = []*customer.Customer{f.first, f.second, f.third}
	f.otherOrganizationsRow = customer.New(b.OrganizationID, core.NewID[core.CustomerID](e.ids), "11", b.ID, now)
	f.otherOrganizationsRow.Name = ptr("defgh")
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(e.t, "BillingEntities.Create()", r.BillingEntities.Create(ctx, f.entity2))
		for _, c := range append(f.all, f.otherOrganizationsRow) {
			noErr(e.t, "Customers.Create()", r.Customers.Create(ctx, c))
		}
		return nil
	})
	return f
}

// customerListCase is one ported case: the filter and the customers it
// lists, in creation order.
type customerListCase struct {
	name   string
	source string
	f      customer.Filter
	want   []*customer.Customer
}

// checkCustomerList lists with every case's filter, page by page (two
// items a page) in both orders, and compares the IDs.
func checkCustomerList(t *testing.T, e *env, org core.OrganizationID, cases []customerListCase) {
	t.Helper()
	cid := func(c *customer.Customer) core.CustomerID { return c.ID }
	e.tx(func(ctx context.Context, r port.Repos) error {
		for _, tc := range cases {
			for _, order := range []core.ListOrder{core.OrderCreated, core.OrderRecent} {
				f := tc.f
				f.Order = order
				got := listAll(t, 2, func(p core.PageRequest) (core.Page[*customer.Customer], error) {
					return r.Customers.List(ctx, org, f, p)
				})
				want := tc.want
				if order == core.OrderRecent {
					want = reversed(want)
				}
				equal(t, "List() "+tc.name+" ("+order.String()+", "+tc.source+")", ids(got, cid), ids(want, cid))
			}
		}
		return nil
	})
}

func testCustomerList(t *testing.T, h Harness) {
	const spec = "spec/queries/customers_query_spec.rb:"
	checkSkipped(t, customerListSkipped)
	t.Run("filters", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newCustomerListFixture(e)
		first, second, third := f.first, f.second, f.third
		cs := func(c ...*customer.Customer) []*customer.Customer { return c }
		checkCustomerList(t, e, f.org, []customerListCase{
			{"all customers", spec + "85", customer.Filter{}, f.all},
			{"external ID", spec + "96", customer.Filter{ExternalID: "11"}, cs(first)},
			{"external ID ignores the search term", spec + "103", customer.Filter{ExternalID: "11", Search: "22"}, cs(first)},
			{"company type", spec + "113", customer.Filter{CustomerType: customer.TypeCompany}, cs(first)},
			{"individual type", spec + "122", customer.Filter{CustomerType: customer.TypeIndividual}, cs(second)},
			{"has customer type", spec + "140", customer.Filter{HasCustomerType: ptr(true)}, cs(first, second)},
			{"has no customer type", spec + "148", customer.Filter{HasCustomerType: ptr(false)}, cs(third)},
			{"partner account type", spec + "157", customer.Filter{AccountTypes: []customer.AccountType{customer.AccountTypePartner}}, cs(third)},
			{"customer account type", spec + "166", customer.Filter{AccountTypes: []customer.AccountType{customer.AccountTypeCustomer}}, cs(first, second)},
			{"billing entity", spec + "176", customer.Filter{BillingEntityIDs: []core.BillingEntityID{f.entity2.ID}}, cs(third)},
			{"search name", spec + "226", customer.Filter{Search: "de"}, cs(first, second)},
			{"search first name", spec + "234", customer.Filter{Search: "Jane"}, cs(second)},
			{"search last name", spec + "242", customer.Filter{Search: "Johnson"}, cs(third)},
			{"search legal name", spec + "250", customer.Filter{Search: "Company"}, cs(third)},
			{"search external ID", spec + "258", customer.Filter{Search: "11"}, cs(first)},
			{"search email", spec + "266", customer.Filter{Search: "1@e"}, cs(first)},
			{"search across fields without duplicates", spec + "274", customer.Filter{Search: "example"}, f.all},
			{"search without match", spec + "282", customer.Filter{Search: "nonexistent"}, nil},
			{"search with LIKE wildcards", spec + "290", customer.Filter{Search: "d_fgh"}, nil},
			{"search trimmed", "", customer.Filter{Search: "  Jane  "}, cs(second)},
			{"blank search", "", customer.Filter{Search: "   "}, f.all},
			{"countries", spec + "317", customer.Filter{Countries: []string{"US", "FR"}}, cs(first, second)},
			{"states", spec + "334", customer.Filter{States: []string{"CA", "Paris"}}, cs(first, second)},
			{"zipcodes", spec + "351", customer.Filter{Zipcodes: []string{"10115", "75001"}}, cs(second, third)},
			{"currencies", spec + "441", customer.Filter{Currencies: []money.Currency{money.MustCurrency("USD")}}, cs(first)},
			{"has tax identification number", spec + "459", customer.Filter{HasTaxIdentificationNumber: ptr(true)}, cs(first)},
			{"has no tax identification number", spec + "467", customer.Filter{HasTaxIdentificationNumber: ptr(false)}, cs(second, third)},
			{"metadata presence", spec + "477", customer.Filter{Metadata: core.MetadataFilter{"id": "1"}}, cs(first)},
			{"metadata absence", spec + "485", customer.Filter{Metadata: core.MetadataFilter{"name": ""}}, cs(second, third)},
			{"several metadata", spec + "493", customer.Filter{Metadata: core.MetadataFilter{"id": "1", "name": "John Doe"}}, cs(first)},
			{"metadata matching one but not the other", spec + "501", customer.Filter{Metadata: core.MetadataFilter{"id": "1", "name": "Jane Smith"}}, nil},
			{"metadata presence and absence", spec + "509", customer.Filter{Metadata: core.MetadataFilter{"id": "2", "name": ""}}, cs(second)},
			{"combined filters", "", customer.Filter{Currencies: []money.Currency{money.MustCurrency("EUR")}, Search: "e"}, cs(second, third)},
		})
	})

	t.Run("archived customers", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newCustomerListFixture(e)
		noErr(t, "Archive()", f.first.Archive(at(time.Hour)))
		e.tx(func(ctx context.Context, r port.Repos) error {
			return r.Customers.Update(ctx, f.first)
		})
		checkCustomerList(t, e, f.org, []customerListCase{
			{"search excludes archived customers", spec + "300", customer.Filter{Search: "defgh"}, nil},
			{"search with deleted", spec + "307", customer.Filter{Search: "defgh", IncludeDeleted: true}, []*customer.Customer{f.first}},
			{"live customers", "", customer.Filter{}, []*customer.Customer{f.second, f.third}},
		})
	})

	t.Run("active subscriptions count", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newCustomerListFixture(e)
		none := customer.New(f.org, core.NewID[core.CustomerID](e.ids), "44", f.entity.ID, at(3*time.Second))
		e.tx(func(ctx context.Context, r port.Repos) error {
			noErr(t, "Customers.Create()", r.Customers.Create(ctx, none))
			plan := subscriptionFixturePlan(ctx, e, r, f.org, "plan")
			for n, c := range []*customer.Customer{f.first, f.second, f.third} {
				for i := range n + 1 {
					s := subscription.New(f.org, core.NewID[core.SubscriptionID](e.ids), c.ID, plan.ID,
						c.ExternalID+"-"+string(rune('a'+i)), now, subscription.PlanFacts{}, now)
					s.Status = subscription.StatusActive
					s.StartedAt = ptr(now)
					noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, s))
				}
			}
			// Subscriptions that are not active do not count.
			for _, st := range []subscription.Status{subscription.StatusPending, subscription.StatusTerminated} {
				s := subscription.New(f.org, core.NewID[core.SubscriptionID](e.ids), none.ID, plan.ID,
					"44-"+string(st), now, subscription.PlanFacts{}, now)
				s.Status = st
				noErr(t, "Subscriptions.Create()", r.Subscriptions.Create(ctx, s))
			}
			return nil
		})
		count := func(from, to *int) customer.Filter {
			return customer.Filter{ActiveSubscriptionsCountFrom: from, ActiveSubscriptionsCountTo: to}
		}
		cs := func(c ...*customer.Customer) []*customer.Customer { return c }
		checkCustomerList(t, e, f.org, []customerListCase{
			{"without subscriptions", spec + "388", count(ptr(0), ptr(0)), cs(none)},
			{"exact count", spec + "398", count(ptr(2), ptr(2)), cs(f.second)},
			{"more than a number", spec + "408", count(ptr(1), nil), cs(f.second, f.third)},
			{"in a range", spec + "419", count(ptr(1), ptr(2)), cs(f.first, f.second)},
			{"less than a number", spec + "430", count(nil, ptr(2)), cs(f.first, none)},
		})
	})

	t.Run("ordering", func(t *testing.T) {
		t.Parallel()
		e := newEnv(t, h)
		f := newCustomerListFixture(e)
		// Customers created at the same instant are ordered by ID
		// ascending in both orders (spec/queries/customers_query_spec.rb:199,
		// app/queries/base_query.rb:111-112): consistently, page after page.
		twin := customer.New(f.org, core.NewID[core.CustomerID](e.ids), "twin", f.entity.ID, f.first.CreatedAt)
		e.tx(func(ctx context.Context, r port.Repos) error { return r.Customers.Create(ctx, twin) })
		cid := func(c *customer.Customer) core.CustomerID { return c.ID }
		e.tx(func(ctx context.Context, r port.Repos) error {
			for _, size := range []int{1, 2, 10} {
				got := listAll(t, size, func(p core.PageRequest) (core.Page[*customer.Customer], error) {
					return r.Customers.List(ctx, f.org, customer.Filter{Order: core.OrderRecent}, p)
				})
				equal(t, "List() newest first", ids(got, cid), ids([]*customer.Customer{f.third, f.second, f.first, twin}, cid))
				got = listAll(t, size, func(p core.PageRequest) (core.Page[*customer.Customer], error) {
					return r.Customers.List(ctx, f.org, customer.Filter{}, p)
				})
				equal(t, "List() oldest first", ids(got, cid), ids([]*customer.Customer{f.first, f.second, f.third, twin}, cid))
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
				name, source string
				f            customer.Filter
				field        string
			}{
				{"country", spec + "324", customer.Filter{Countries: []string{"US", "INVALID"}}, "countries[1]"},
				{"currency", spec + "448", customer.Filter{Currencies: []money.Currency{{}}}, "currencies[0]"},
				{"account type", spec + "518", customer.Filter{AccountTypes: []customer.AccountType{"random"}}, "account_types[0]"},
				{"customer type", "", customer.Filter{CustomerType: "random"}, "customer_type"},
				{
					"customer type without a customer type", "app/contracts/queries/customers_query_filters_contract.rb:27",
					customer.Filter{HasCustomerType: ptr(false), CustomerType: customer.TypeCompany},
					"customer_type",
				},
				{"negative count", "", customer.Filter{ActiveSubscriptionsCountFrom: ptr(-1)}, "active_subscriptions_count_from"},
				{"order", "", customer.Filter{Order: 7}, "order"},
			} {
				_, err := r.Customers.List(ctx, a.OrganizationID, tc.f, core.PageRequest{})
				wantErr(t, "List() invalid "+tc.name+" ("+tc.source+")", err, core.ErrInvalidValue)
				if !hasFieldError(err, tc.field) {
					t.Errorf("List() invalid %s error got = %v, want = a field error on %s", tc.name, err, tc.field)
				}
			}
			for _, order := range []core.ListOrder{core.OrderCreated, core.OrderRecent} {
				_, err := r.Customers.List(ctx, a.OrganizationID, customer.Filter{Order: order}, core.PageRequest{Token: "not-a-token"})
				wantErr(t, "List() with a malformed page token", err, core.ErrInvalidValue)
			}
			return nil
		})
	})
}

// hasFieldError reports whether err carries a field error on field.
func hasFieldError(err error, field string) bool {
	for _, fe := range core.FieldErrors(err) {
		if fe.Field == field {
			return true
		}
	}
	return false
}
