package invoicing_test

import (
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
)

// selects returns a subscription option selecting the sections, or
// skipping them.
func selects(skip bool, sections ...*invoice.CustomSection) func(*subscription.Subscription) {
	return func(s *subscription.Subscription) {
		s.SkipInvoiceCustomSections = skip
		for _, cs := range sections {
			s.InvoiceCustomSectionIDs = append(s.InvoiceCustomSectionIDs, cs.ID)
		}
	}
}

// TestSubscriptionInvoiceCustomSections bills one invoice for subscriptions
// with these selections: the invoice follows them
// (Invoices::SubscriptionService, subscription_service.rb:61-64).
func TestSubscriptionInvoiceCustomSections(t *testing.T) {
	type sel struct {
		skip  bool
		codes []string
	}
	tests := []struct {
		name, source string
		customer     []string
		entity       []string
		subs         []sel
		want         []string
	}{
		{"subscription skips", "spec/services/invoices/apply_invoice_custom_sections_service_spec.rb:129", []string{"c"}, nil, []sel{{skip: true}}, nil},
		{"no selection, customer's", "spec/services/invoices/apply_invoice_custom_sections_service_spec.rb:143", []string{"c"}, nil, []sel{{}}, []string{"c"}},
		{"no selection, billing entity's", "spec/services/invoices/apply_invoice_custom_sections_service_spec.rb:155", nil, []string{"e"}, []sel{{}}, []string{"e"}},
		{"subscription's selection", "spec/services/invoices/apply_invoice_custom_sections_service_spec.rb:172", []string{"c"}, nil, []sel{{codes: []string{"s1"}}}, []string{"s1"}},
		{"none selects, customer's", "spec/services/invoices/apply_invoice_custom_sections_service_spec.rb:192", []string{"c"}, nil, []sel{{}, {}}, []string{"c"}},
		{"all select, merged", "spec/services/invoices/apply_invoice_custom_sections_service_spec.rb:206", []string{"c"}, nil, []sel{{codes: []string{"s1"}}, {codes: []string{"s2", "s1"}}}, []string{"s1", "s2"}},
		{"some select, merged with the customer's", "spec/services/invoices/apply_invoice_custom_sections_service_spec.rb:220", []string{"c"}, nil, []sel{{codes: []string{"s1"}}, {}}, []string{"c", "s1"}},
		{"all skip", "spec/services/invoices/apply_invoice_custom_sections_service_spec.rb:232", []string{"c"}, nil, []sel{{skip: true}, {skip: true}}, nil},
		{"one skips, one selects", "spec/services/invoices/apply_invoice_custom_sections_service_spec.rb:247", []string{"c"}, nil, []sel{{skip: true, codes: []string{"s2"}}, {codes: []string{"s1"}}}, []string{"s1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			f := newFixture(t, billingDay)
			sections := map[string]*invoice.CustomSection{}
			for _, code := range []string{"c", "e", "s1", "s2"} {
				sections[code] = f.customSection(code)
			}
			ids := func(codes []string) []core.InvoiceCustomSectionID {
				var out []core.InvoiceCustomSectionID
				for _, c := range codes {
					out = append(out, sections[c].ID)
				}
				return out
			}
			f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.InvoiceCustomSectionIDs = ids(tt.customer) })
			f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceCustomSectionIDs = ids(tt.entity) })
			var subs []*subscription.Subscription
			for _, s := range tt.subs {
				var cs []*invoice.CustomSection
				for _, c := range s.codes {
					cs = append(cs, sections[c])
				}
				subs = append(subs, f.subscribe(selects(s.skip, cs...)))
			}
			invs := f.bill(billingDay, invoice.InvoicingReasonSubscriptionPeriodic, subs...)
			if len(invs) != 1 {
				t.Fatalf("invoices = %d, want = 1", len(invs))
			}
			got := f.appliedSections(invs[0].ID)
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("applied sections got = %v, want = %v", got, tt.want)
			}
		})
	}
}

// TestRefreshedDraftCustomSections refreshes a draft: its sections are
// applied again, the customer's (refresh_draft_service.rb:63, :129).
func TestRefreshedDraftCustomSections(t *testing.T) {
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
	c, s1 := f.customSection("c"), f.customSection("s1")
	f.UpdateCustomer(f.cust, func(cu *customer.Customer) { cu.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{c.ID} })
	sub := f.subscribe(selects(false, s1))
	draft := f.billOne(sub)
	if got := f.appliedSections(draft.ID); !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("applied sections got = %v, want = [s1]", got)
	}
	f.Clock.Set(billingtest.Date(2026, time.February, 2))
	if _, err := f.svc.Refresh(f.Ctx, f.OrgID(), draft.ID); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := f.appliedSections(draft.ID); !slices.Equal(got, []string{"c"}) {
		t.Errorf("applied sections got = %v, want = [c]", got)
	}
}
