package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// fundingCall records a funding instructions request.
type fundingCall struct {
	providerCustomerID, currency, customerCountry, entityCountry string
}

// fundingGateway is a Stripe adapter giving funding instructions.
type fundingGateway struct {
	*custGateway
	raw   json.RawMessage
	err   error
	calls []fundingCall
}

func (g *fundingGateway) FundingInstructions(_ context.Context, _ payment.Connection, id string, c money.Currency, customerCountry, entityCountry string) (json.RawMessage, error) {
	g.calls = append(g.calls, fundingCall{id, c.Code(), customerCountry, entityCountry})
	return g.raw, g.err
}

// fakeFormatter prints the funding instructions as they are.
type fakeFormatter struct {
	locales []string
}

func (f *fakeFormatter) FormatFundingInstructions(raw json.RawMessage, locale string) (string, string, error) {
	f.locales = append(f.locales, locale)
	return "Pay with a bank transfer", "formatted " + string(raw), nil
}

// fundingFixture is a customer fixture paying by bank transfer through a
// gateway giving funding instructions.
type fundingFixture struct {
	*custFixture
	fg  *fundingGateway
	fmt *fakeFormatter
}

func newFundingFixture(t *testing.T, methods ...string) *fundingFixture {
	t.Helper()
	f := &fundingFixture{custFixture: newCustFixture(t), fmt: &fakeFormatter{}}
	f.fg = &fundingGateway{custGateway: f.cg, raw: json.RawMessage(`{"type":"us_bank_transfer"}`)}
	set, err := port.NewPaymentProviderSet(f.fg)
	if err != nil {
		t.Fatal(err)
	}
	f.svc = payments.New(f.h.Deps).WithProviders(payments.Providers{Gateways: set}).WithFundingFormatter(f.fmt)
	if len(methods) == 0 {
		methods = []string{"customer_balance"}
	}
	id := "cus_Rw5Qso78STEap3"
	f.pcust.ProviderCustomerID, f.pcust.ProviderPaymentMethods = &id, methods
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Update(ctx, f.pcust) })
	return f
}

func (f *fundingFixture) customer() *customer.Customer {
	return billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*customer.Customer, error) {
		return r.Customers.Get(ctx, f.h.OrgID(), f.cust.ID)
	})
}

// fundingJobs returns the funding sync jobs enqueued.
func fundingJobs(h *billingtest.Harness) int {
	n := 0
	for _, j := range h.Store.Jobs() {
		var a kernel.ProviderCustomerArgs
		if j.Kind == kernel.JobSyncProviderCustomer && json.Unmarshal(j.Args, &a) == nil && a.Funding {
			n++
		}
	}
	return n
}

// TestSyncFundingInstructions ports SyncFundingInstructionsService.
func TestSyncFundingInstructions(t *testing.T) {
	t.Run("not eligible", func(t *testing.T) {
		// source: spec/services/payment_provider_customers/stripe/sync_funding_instructions_service_spec.rb:26
		f := newFundingFixture(t, "card")
		if err := f.sync(kernel.ProviderCustomerArgs{Funding: true}); err != nil {
			t.Fatalf("SyncProviderCustomer: %v", err)
		}
		if len(f.fg.calls) != 0 || len(f.customer().SystemInvoiceCustomSectionIDs) != 0 {
			t.Errorf("calls got = %v, sections %v, want = none", f.fg.calls, f.customer().SystemInvoiceCustomSectionIDs)
		}
	})

	t.Run("creates the section", func(t *testing.T) {
		// source: spec/services/payment_provider_customers/stripe/sync_funding_instructions_service_spec.rb:49
		// source: spec/jobs/payment_provider_customers/stripe_sync_funding_instructions_job_spec.rb:11
		f := newFundingFixture(t)
		f.h.UpdateCustomer(f.cust, func(c *customer.Customer) { c.SkipInvoiceCustomSections = true })
		if err := f.sync(kernel.ProviderCustomerArgs{Funding: true}); err != nil {
			t.Fatalf("SyncProviderCustomer: %v", err)
		}
		want := fundingCall{"cus_Rw5Qso78STEap3", "USD", "", ""}
		if !slices.Equal(f.fg.calls, []fundingCall{want}) {
			t.Errorf("calls got = %v, want = [%v]", f.fg.calls, want)
		}
		c := f.customer()
		secs := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) ([]*invoice.CustomSection, error) {
			return r.InvoiceCustomSections.ListByIDs(ctx, f.h.OrgID(), c.SystemInvoiceCustomSectionIDs)
		})
		if len(secs) != 1 {
			t.Fatalf("system sections got = %d, want = 1", len(secs))
		}
		s := secs[0]
		got := strings.Join([]string{s.Code, s.Name, deref(s.DisplayName), deref(s.Details), string(s.SectionType)}, "|")
		if want := payments.FundingSectionCode(f.cust.ID) + `|Funding Instructions|Pay with a bank transfer|formatted {"type":"us_bank_transfer"}|system_generated`; got != want {
			t.Errorf("section got = %s, want = %s", got, want)
		}
		if !slices.Equal(f.fmt.locales, []string{"en"}) {
			t.Errorf("formatter locales got = %v, want = [en]", f.fmt.locales)
		}
		// The customer's skip flag is cleared, so the section prints.
		if c.SkipInvoiceCustomSections {
			t.Error("skip_invoice_custom_sections got = true, want = false")
		}

		// A customer with the section is not synced again.
		if err := f.sync(kernel.ProviderCustomerArgs{Funding: true}); err != nil {
			t.Fatalf("second SyncProviderCustomer: %v", err)
		}
		if len(f.fg.calls) != 1 || len(f.customer().SystemInvoiceCustomSectionIDs) != 1 {
			t.Errorf("second run calls got = %d, sections %d, want = 1 and 1", len(f.fg.calls), len(f.customer().SystemInvoiceCustomSectionIDs))
		}
	})

	t.Run("billing entity country and currency", func(t *testing.T) {
		// source: spec/services/payment_provider_customers/stripe/sync_funding_instructions_service_spec.rb:81
		f := newFundingFixture(t)
		f.h.Do(func(ctx context.Context, r port.Repos) error {
			be, err := r.BillingEntities.Get(ctx, f.h.OrgID(), f.cust.BillingEntityID)
			if err != nil {
				return err
			}
			be.Address.Country, be.DefaultCurrency, be.DocumentLocale = "IE", money.MustCurrency("EUR"), "fr"
			return r.BillingEntities.Update(ctx, be)
		})
		f.h.UpdateCustomer(f.cust, func(c *customer.Customer) { c.Currency, c.BillingAddress.Country = money.Currency{}, "SE" })
		if err := f.sync(kernel.ProviderCustomerArgs{Funding: true}); err != nil {
			t.Fatalf("SyncProviderCustomer: %v", err)
		}
		if want := (fundingCall{"cus_Rw5Qso78STEap3", "EUR", "SE", "IE"}); !slices.Equal(f.fg.calls, []fundingCall{want}) {
			t.Errorf("calls got = %v, want = [%v]", f.fg.calls, want)
		}
		if !slices.Equal(f.fmt.locales, []string{"fr"}) {
			t.Errorf("formatter locales got = %v, want = [fr]", f.fmt.locales)
		}
	})

	t.Run("existing section reused", func(t *testing.T) {
		// sync_funding_instructions_service.rb:40-42: the organization's
		// section of that code is assigned as it is.
		f := newFundingFixture(t)
		sec := &invoice.CustomSection{
			ID: core.NewID[core.InvoiceCustomSectionID](f.h.IDs), OrganizationID: f.h.OrgID(), Code: payments.FundingSectionCode(f.cust.ID),
			Name: "Funding Instructions", SectionType: invoice.CustomSectionTypeSystemGenerated, CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
		}
		f.h.Do(func(ctx context.Context, r port.Repos) error { return r.InvoiceCustomSections.Create(ctx, sec) })
		if err := f.sync(kernel.ProviderCustomerArgs{Funding: true}); err != nil {
			t.Fatalf("SyncProviderCustomer: %v", err)
		}
		if got := f.customer().SystemInvoiceCustomSectionIDs; !slices.Equal(got, []core.InvoiceCustomSectionID{sec.ID}) || len(f.fmt.locales) != 0 {
			t.Errorf("sections got = %v (formatted %d times), want = [%s] unformatted", got, len(f.fmt.locales), sec.ID)
		}
	})

	t.Run("provider error", func(t *testing.T) {
		// sync_funding_instructions_service.rb:18-19 and the job's
		// raise_if_error!: the job fails and nothing is assigned.
		f := newFundingFixture(t)
		f.fg.err = payment.ErrMissingCountry
		if err := f.sync(kernel.ProviderCustomerArgs{Funding: true}); !errors.Is(err, payment.ErrMissingCountry) {
			t.Errorf("error got = %v, want = %v", err, payment.ErrMissingCountry)
		}
		if got := f.customer().SystemInvoiceCustomSectionIDs; len(got) != 0 {
			t.Errorf("sections got = %v, want = none", got)
		}
	})
}

// TestFundingSyncEnqueued ports the enqueues of StripeSyncFundingInstructionsJob.
func TestFundingSyncEnqueued(t *testing.T) {
	tests := []struct {
		name    string
		methods []string
		create  bool
		want    int
	}{
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:53
		{name: "created with customer_balance", methods: []string{"customer_balance"}, create: true, want: 1},
		{name: "created with card", methods: []string{"card"}, create: true},
		// source: spec/services/payment_provider_customers/stripe_service_spec.rb:344
		{name: "updated with customer_balance", methods: []string{"customer_balance"}, want: 1},
		{name: "updated with card", methods: []string{"card"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFundingFixture(t, tt.methods...)
			args := kernel.ProviderCustomerArgs{Update: true}
			if tt.create {
				f.pcust.ProviderCustomerID = nil
				f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentProviderCustomers.Update(ctx, f.pcust) })
				args = kernel.ProviderCustomerArgs{}
			}
			if err := f.sync(args); err != nil {
				t.Fatalf("SyncProviderCustomer: %v", err)
			}
			if got := fundingJobs(f.h); got != tt.want {
				t.Errorf("funding jobs got = %d, want = %d", got, tt.want)
			}
		})
	}
}
