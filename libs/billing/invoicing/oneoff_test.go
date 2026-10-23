package invoicing_test

import (
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestCreateOneOff(t *testing.T) {
	two := money.MustParse("2")
	tests := []struct {
		name      string
		unit      int64
		wantFees  int64
		wantTaxes int64
		wantPay   core.PaymentStatus
	}{
		// source: spec/services/invoices/create_one_off_service_spec.rb:37
		{"amount", 1200, 2400 + 400, 560, core.PaymentStatusPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			f.taxCustomer("20")
			first, second := f.AddOn(1000), f.AddOn(400)
			unit := tt.unit
			inv, err := f.svc.CreateOneOff(f.Ctx, invoicing.OneOffInput{
				OrganizationID: f.OrgID(), CustomerID: f.cust.ID,
				Fees: []fees.OneOff{{AddOnID: first.ID, UnitAmount: &unit, Units: &two}, {AddOnID: second.ID}},
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if inv.Type != invoice.TypeOneOff || inv.Status != invoice.StatusFinalized || inv.PaymentStatus != tt.wantPay {
				t.Fatalf("type, status, payment = %s, %s, %s, want = one_off, finalized, %s", inv.Type, inv.Status, inv.PaymentStatus, tt.wantPay)
			}
			checkAmounts(t, f.Invoice(inv.ID), tt.wantFees, tt.wantTaxes, tt.wantFees+tt.wantTaxes)
			if got := len(f.Fees(inv.ID)); got != 2 {
				t.Errorf("fees = %d, want = 2", got)
			}
			// source: spec/services/invoices/create_one_off_service_spec.rb:172
			if !f.emitted(webhook.EventTypeInvoiceOneOffCreated) {
				t.Errorf("webhooks = %v, want = invoice.one_off_created", f.Webhooks())
			}
			// source: spec/services/invoices/create_one_off_service_spec.rb:154
			if f.enqueued(kernel.JobCreateInvoicePayment) != 1 || f.enqueued(kernel.JobGenerateInvoiceDocument) != 1 {
				t.Errorf("jobs = %v, want = a payment and a document", f.JobKinds())
			}
		})
	}
}

// source: spec/services/invoices/create_one_off_service_spec.rb:290
func TestCreateOneOffZeroTotal(t *testing.T) {
	f := newFixture(t, billingDay)
	addOn := f.AddOn(1000)
	zero := int64(0)
	inv, err := f.svc.CreateOneOff(f.Ctx, invoicing.OneOffInput{
		OrganizationID: f.OrgID(), CustomerID: f.cust.ID, Fees: []fees.OneOff{{AddOnID: addOn.ID, UnitAmount: &zero}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if inv.PaymentStatus != core.PaymentStatusSucceeded || inv.TotalAmount != 0 || inv.Status != invoice.StatusFinalized {
		t.Fatalf("payment, total, status = %s, %d, %s, want = succeeded, 0, finalized", inv.PaymentStatus, inv.TotalAmount, inv.Status)
	}
	if f.enqueued(kernel.JobCreateInvoicePayment) != 0 {
		t.Errorf("jobs = %v, want = no payment", f.JobKinds())
	}
}

// source: spec/services/invoices/create_one_off_service_spec.rb:353
func TestCreateOneOffCurrency(t *testing.T) {
	tests := []struct {
		name     string
		currency money.Currency
		customer money.Currency
		wantErr  bool
	}{
		// source: spec/services/invoices/create_one_off_service_spec.rb:356
		{"other than the customer's", money.MustCurrency("NOK"), money.MustCurrency("USD"), false},
		// source: spec/services/invoices/create_one_off_service_spec.rb:369
		{"missing", money.Currency{}, money.Currency{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			f.UpdateCustomer(f.cust, func(c *customer.Customer) { c.Currency = tt.customer })
			addOn := f.AddOn(1000)
			inv, err := f.svc.CreateOneOff(f.Ctx, invoicing.OneOffInput{
				OrganizationID: f.OrgID(), CustomerID: f.cust.ID, Currency: tt.currency,
				Fees: []fees.OneOff{{AddOnID: addOn.ID}},
			})
			if tt.wantErr {
				var ve *core.ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("err = %v, want = a validation error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if inv.Currency != tt.currency {
				t.Errorf("currency = %v, want = %v", inv.Currency, tt.currency)
			}
		})
	}
}

// source: spec/services/invoices/create_one_off_service_spec.rb:391
func TestCreateOneOffWithoutFees(t *testing.T) {
	f := newFixture(t, billingDay)
	var re *core.RuleError
	_, err := f.svc.CreateOneOff(f.Ctx, invoicing.OneOffInput{OrganizationID: f.OrgID(), CustomerID: f.cust.ID})
	if !errors.As(err, &re) || re.Code != invoicing.CodeNoFees {
		t.Fatalf("err = %v, want = %s", err, invoicing.CodeNoFees)
	}
}
