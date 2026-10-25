package quotes_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/quotes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// mentionFix is the spec's organization, its billing entity Globex SARL
// and the customer Hooli.
type mentionFix struct {
	*fix
}

func newMentionFix(t *testing.T, cust ...func(*customer.Customer)) *mentionFix {
	t.Helper()
	f := newFix(t)
	f.h.UpdateBillingEntity(func(be *organization.BillingEntity) {
		be.Name = "Globex SARL"
		be.LegalName = new("Globex SARL")
		be.TaxID = new("FR12345678901")
		be.Email = new("billing@globex.example")
		be.Address = core.Address{Line1: "4 rue de la Paix", Zipcode: "75002", City: "Paris", Country: "FR"}
		be.NetPaymentTermDays = 30
	})
	f.h.UpdateCustomer(f.cust, func(c *customer.Customer) {
		c.Name, c.LegalName = new("Hooli"), new("Hooli Inc")
		c.Firstname, c.Lastname = new("Gavin"), new("Belson")
		c.Email = new("procurement@hooli.example")
		c.BillingEntityID = f.h.BillingEntity.ID
		for _, o := range cust {
			o(c)
		}
	})
	return &mentionFix{fix: f}
}

// entity stores another billing entity of the organization.
func (f *mentionFix) entity(t *testing.T, fn func(*organization.BillingEntity)) *organization.BillingEntity {
	t.Helper()
	be := *f.h.BillingEntity
	be.ID = core.NewID[core.BillingEntityID](f.h.IDs)
	be.Code = "entity-" + be.ID.String()
	be.IsDefault = false
	be.LegalName, be.TaxID, be.Email, be.Address = nil, nil, nil, core.Address{}
	fn(&be)
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.BillingEntities.Create(ctx, &be) })
	return &be
}

// variables creates a quote and returns the live mention variables of its
// draft version.
func (f *mentionFix) variables(t *testing.T, typ quote.OrderType, sub core.SubscriptionID, be core.BillingEntityID, items any) map[string]any {
	t.Helper()
	q, err := f.svc.Create(f.h.Ctx, quotes.CreateInput{
		OrganizationID: f.h.OrgID(), CustomerID: f.cust.ID, OrderType: typ,
		SubscriptionID: sub, BillingEntityID: be, BillingItems: mustJSON(t, items),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(q.Current.MentionVariables, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// plan is a quoted new plan from start to end, either omitted when empty.
func (f *mentionFix) plan(start, end string) map[string]any {
	p := f.h.Plan(period.Monthly, 1000)
	payload := map[string]any{"code": p.Code, "charges": []any{}, "fixedCharges": []any{}}
	if start != "" {
		payload["startDate"] = start
	}
	if end != "" {
		payload["endDate"] = end
	}
	return map[string]any{"id": p.ID.String(), "type": "plan", "payload": payload}
}

// planTerm is the spec's :with_subscription_creation_billing_items
// trait: one plan from start to end.
func (f *mentionFix) planTerm(start, end string) map[string]any {
	return map[string]any{"plans": []any{f.plan(start, end)}}
}

// addOn is a quoted new add-on with the service period of payload and
// overrides (nil for none).
func (f *mentionFix) addOn(payload, overrides map[string]any) map[string]any {
	a := f.h.AddOn(10_000)
	p := map[string]any{"code": a.Code, "units": 1, "unitAmountCents": a.Amount, "totalAmountCents": a.Amount}
	for k, v := range payload {
		p[k] = v
	}
	item := map[string]any{"id": a.ID.String(), "localId": core.NewID[core.AddOnID](f.h.IDs).String(), "type": "add_on", "payload": p}
	if overrides != nil {
		item["overrides"] = overrides
	}
	return item
}

func wantVariables(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for k, w := range want {
		if g := got[k]; !reflect.DeepEqual(g, w) {
			t.Errorf("%s: got = %#v, want = %#v", k, g, w)
		}
	}
}

func duration(unit string, count float64) map[string]any {
	return map[string]any{"unit": unit, "count": count}
}

// TestComputeMentionVariables ports QuoteVersions::ComputeMentionVariablesService.
func TestComputeMentionVariables(t *testing.T) {
	t.Parallel()
	address := map[string]any{
		"address_line1": "4 rue de la Paix", "address_line2": nil, "locality": "Paris",
		"postal_code": "75002", "administrative_area": nil, "country_code": "FR",
	}
	creation := quote.OrderTypeSubscriptionCreation
	terms := []struct {
		name, source, start, end string
		want                     map[string]any
	}{
		{"whole months", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:164", "2026-01-01", "2026-04-01", map[string]any{"commercial_terms_term_duration": duration("months", 3)}},
		{"less than a month", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:173", "2026-01-01", "2026-01-15", map[string]any{"commercial_terms_term_duration": duration("days", 14)}},
		{"no end date", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:181", "2026-04-01", "", map[string]any{"commercial_terms_term_duration": nil}},
		{"shorter month-end", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:190", "2026-01-31", "2026-02-28", map[string]any{"commercial_terms_term_duration": duration("days", 28)}},
		{"more than a year", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:199", "2026-01-01", "2027-02-01", map[string]any{"commercial_terms_term_duration": duration("months", 13)}},
		{
			"full datetime", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:241", "2026-01-01T09:30:00Z", "2027-01-01T09:30:00Z",
			map[string]any{"commercial_terms_start_date": "2026-01-01", "commercial_terms_term_duration": duration("years", 1)},
		},
	}

	t.Run("raw dictionary", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/compute_mention_variables_service_spec.rb:72")
		f := newMentionFix(t)
		got := f.variables(t, creation, core.SubscriptionID{}, core.BillingEntityID{}, f.planTerm("2026-04-01", "2027-04-01"))
		wantVariables(t, got, map[string]any{
			"customer_name": "Hooli Inc - Gavin Belson", "customer_email": "procurement@hooli.example",
			"organization_name": f.h.Org.Name, "organization_logo": nil,
			"billing_entity_name": "Globex SARL", "billing_entity_legal_name": "Globex SARL", "billing_entity_address": address,
			"billing_entity_tax_id": "FR12345678901", "billing_entity_email": "billing@globex.example",
			"quote_version": "1", "quote_currency": "USD", "quote_date": testNow.Format(time.DateOnly),
			"commercial_terms_term_duration": duration("years", 1), "commercial_terms_start_date": "2026-04-01",
			"commercial_terms_payment_terms": float64(30),
		})
		if got["quote_number"] == nil {
			t.Errorf("got quote_number = nil, want the quote's number")
		}
	})
	t.Run("version billing entity", func(t *testing.T) {
		t.Parallel()
		f := newMentionFix(t)
		be := f.entity(t, func(be *organization.BillingEntity) {
			be.Name, be.LegalName = "Globex Inc", new("Globex Incorporated")
			be.TaxID, be.Email = new("US987654321"), new("billing@globex.us.example")
			be.Address = core.Address{Line1: "1 Market Street", Zipcode: "94105", City: "San Francisco", State: "CA", Country: "US"}
			be.NetPaymentTermDays = 60
		})
		got := f.variables(t, creation, core.SubscriptionID{}, be.ID, f.planTerm("2026-04-01", "2027-04-01"))
		t.Log("source: spec/services/quote_versions/compute_mention_variables_service_spec.rb:123")
		wantVariables(t, got, map[string]any{
			"billing_entity_name": "Globex Inc", "billing_entity_legal_name": "Globex Incorporated",
			"billing_entity_tax_id": "US987654321", "billing_entity_email": "billing@globex.us.example",
		})
		t.Log("source: spec/services/quote_versions/compute_mention_variables_service_spec.rb:134")
		wantVariables(t, got, map[string]any{"commercial_terms_payment_terms": float64(30)})
	})
	t.Run("customer payment term", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/compute_mention_variables_service_spec.rb:142")
		f := newMentionFix(t, func(c *customer.Customer) { c.NetPaymentTermDays = new(45) })
		got := f.variables(t, creation, core.SubscriptionID{}, core.BillingEntityID{}, f.planTerm("2026-04-01", "2027-04-01"))
		wantVariables(t, got, map[string]any{"commercial_terms_payment_terms": float64(45)})
	})
	t.Run("locale independent", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/compute_mention_variables_service_spec.rb:150")
		f := newMentionFix(t, func(c *customer.Customer) { c.DocumentLocale = new("fr") })
		got := f.variables(t, creation, core.SubscriptionID{}, core.BillingEntityID{}, f.planTerm("2026-04-01", "2027-04-01"))
		wantVariables(t, got, map[string]any{
			"commercial_terms_term_duration": duration("years", 1), "commercial_terms_start_date": "2026-04-01",
			"commercial_terms_payment_terms": float64(30), "billing_entity_address": address,
		})
	})
	for _, tt := range terms {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newMentionFix(t)
			wantVariables(t, f.variables(t, creation, core.SubscriptionID{}, core.BillingEntityID{}, f.planTerm(tt.start, tt.end)), tt.want)
		})
	}
	t.Run("several plans", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/compute_mention_variables_service_spec.rb:220")
		f := newMentionFix(t)
		items := map[string]any{"plans": []any{f.plan("2026-03-01", "2027-06-01"), f.plan("2026-01-01", "2027-01-01")}}
		wantVariables(t, f.variables(t, creation, core.SubscriptionID{}, core.BillingEntityID{}, items), map[string]any{
			"commercial_terms_start_date": "2026-01-01", "commercial_terms_term_duration": duration("months", 17),
		})
	})

	amendment := func(t *testing.T, f *mentionFix, opts ...func(*subscription.Subscription)) core.SubscriptionID {
		t.Helper()
		opts = append([]func(*subscription.Subscription){func(s *subscription.Subscription) {
			s.SubscriptionAt = time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
		}}, opts...)
		return f.h.Subscription(f.cust, f.h.Plan(period.Monthly, 1000), opts...).ID
	}
	t.Run("amendment anniversary", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/compute_mention_variables_service_spec.rb:268")
		f := newMentionFix(t)
		got := f.variables(t, quote.OrderTypeSubscriptionAmendment, amendment(t, f), core.BillingEntityID{}, f.planTerm("", "2027-02-01"))
		wantVariables(t, got, map[string]any{"commercial_terms_start_date": "2026-02-01", "commercial_terms_term_duration": duration("years", 1)})
	})
	t.Run("amendment target entity", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/compute_mention_variables_service_spec.rb:291")
		f := newMentionFix(t)
		be := f.entity(t, func(be *organization.BillingEntity) { be.Name, be.LegalName = "Globex Inc", new("Globex Incorporated") })
		sub := amendment(t, f, func(s *subscription.Subscription) { s.BillingEntityID = be.ID })
		got := f.variables(t, quote.OrderTypeSubscriptionAmendment, sub, core.BillingEntityID{}, f.planTerm("", "2027-02-01"))
		wantVariables(t, got, map[string]any{"billing_entity_name": "Globex Inc", "billing_entity_legal_name": "Globex Incorporated"})
	})
	t.Run("amendment plan start date", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/compute_mention_variables_service_spec.rb:313")
		f := newMentionFix(t)
		got := f.variables(t, quote.OrderTypeSubscriptionAmendment, amendment(t, f), core.BillingEntityID{}, f.planTerm("2026-05-01", "2027-02-01"))
		wantVariables(t, got, map[string]any{"commercial_terms_start_date": "2026-05-01"})
	})

	oneOff := []struct {
		name, source string
		items        func(f *mentionFix) []any
		want         map[string]any
	}{
		{
			"no service period", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:325",
			func(f *mentionFix) []any { return []any{f.addOn(nil, nil)} },
			map[string]any{"commercial_terms_start_date": nil, "commercial_terms_term_duration": nil},
		},
		{
			"service period", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:345",
			func(f *mentionFix) []any {
				return []any{f.addOn(map[string]any{"fromDatetime": "2026-04-01T00:00:00Z", "toDatetime": "2027-04-01T00:00:00Z"}, nil)}
			},
			map[string]any{"commercial_terms_start_date": "2026-04-01", "commercial_terms_term_duration": duration("years", 1)},
		},
		{
			"several add-ons", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:369",
			func(f *mentionFix) []any {
				return []any{
					f.addOn(map[string]any{"fromDatetime": "2026-03-01T00:00:00Z", "toDatetime": "2027-06-01T00:00:00Z"}, nil),
					f.addOn(map[string]any{"fromDatetime": "2026-01-01T00:00:00Z", "toDatetime": "2027-01-01T00:00:00Z"}, nil),
				}
			},
			map[string]any{"commercial_terms_start_date": "2026-01-01", "commercial_terms_term_duration": duration("months", 17)},
		},
		{
			"overridden service period", "spec/services/quote_versions/compute_mention_variables_service_spec.rb:397",
			func(f *mentionFix) []any {
				return []any{f.addOn(
					map[string]any{"fromDatetime": "2026-01-01T00:00:00Z", "toDatetime": "2027-01-01T00:00:00Z"},
					map[string]any{"fromDatetime": "2026-02-01T00:00:00Z", "toDatetime": "2026-08-01T00:00:00Z"},
				)}
			},
			map[string]any{"commercial_terms_start_date": "2026-02-01", "commercial_terms_term_duration": duration("months", 6)},
		},
	}
	for _, tt := range oneOff {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newMentionFix(t)
			got := f.variables(t, quote.OrderTypeOneOff, core.SubscriptionID{}, core.BillingEntityID{}, map[string]any{"addOns": tt.items(f)})
			wantVariables(t, got, tt.want)
		})
	}
}
