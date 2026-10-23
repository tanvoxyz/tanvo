package billing_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestScenarioCustomersPartnerBilling: a partner's invoices are
// self-billed, numbered on their own per customer, and get no payment.
// The analytics part of the spec is out of billing.
// source: spec/scenarios/customers/create_partner_and_run_billing_spec.rb:52
func TestScenarioCustomersPartnerBilling(t *testing.T) {
	s := newScenario(t, invAt("2024-04-24T00:00:00Z"))
	s.UpdateBillingEntity(invNumbering(organization.DocumentNumberingPerBillingEntity, ""))
	prefix := s.BillingEntity.DocumentNumberPrefix
	partner := s.Customer(func(c *customer.Customer) { c.AccountType = customer.AccountTypePartner })
	customers := []*customer.Customer{s.Customer(), s.Customer()}
	p := s.Plan(period.Monthly, 1000)
	m := s.Metric("latest", catalog.AggregationTypeLatest, "value")
	s.Charge(p, m, catalog.ChargeModelPackage, `{"amount":"2","free_units":1000,"package_size":1000}`)
	all := append(slices.Clone(customers), partner)
	for _, c := range all {
		sub := s.invCreateSub(c, p)
		s.ingest(sub, m.Code)
	}

	check := func(month string, n int, want []string) {
		t.Helper()
		pi := s.invCustomerInvoices(partner.ID)
		if len(pi) != n {
			t.Fatalf("%s: partner invoices = %d, want = %d", month, len(pi), n)
		}
		last := pi[n-1]
		if wantNum := fmt.Sprintf("%s-001-%03d", prefix, n); !last.SelfBilled || last.Number != wantNum {
			t.Errorf("%s: partner invoice = %v %s, want = self-billed %s", month, last.SelfBilled, last.Number, wantNum)
		}
		var got []string
		for _, c := range customers {
			ci := s.invCustomerInvoices(c.ID)
			if len(ci) != n {
				t.Fatalf("%s: customer invoices = %d, want = %d", month, len(ci), n)
			}
			if ci[n-1].SelfBilled {
				t.Errorf("%s: customer invoice is self-billed", month)
			}
			got = append(got, strings.TrimPrefix(ci[n-1].Number, prefix+"-"+month+"-"))
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: customer numbers = %v, want = %v", month, got, want)
		}
	}

	s.travelTo(invAt("2024-05-01T00:00:00Z"))
	s.performBilling()
	s.performBilling()
	check("202405", 1, []string{"001", "002"})
	s.travelTo(invAt("2024-06-01T00:00:00Z"))
	s.performBilling()
	check("202406", 2, []string{"003", "004"})

	partnerInvoices := map[core.InvoiceID]bool{}
	for _, inv := range s.invCustomerInvoices(partner.ID) {
		partnerInvoices[inv.ID] = true
	}
	for _, j := range s.Unhandled {
		if j.Kind != kernel.JobCreateInvoicePayment {
			continue
		}
		var a kernel.InvoiceArgs
		s.must(json.Unmarshal(j.Args, &a))
		if partnerInvoices[a.InvoiceID] {
			t.Errorf("payment enqueued for partner invoice %s", a.InvoiceID)
		}
	}
	if n := len(s.invOfType(partner.ID, invoice.TypeSubscription)); n != 2 {
		t.Errorf("partner subscription invoices = %d, want = 2", n)
	}
}
