package invoicing_test

import (
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

func TestRetryTaxVoidingView(t *testing.T) {
	f := newFixture(t, billingDay)
	f.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
	f.Taxes.Compute = providerTaxes("0.1")
	inv := f.billOne(f.subscribe())
	if _, err := f.svc.PullTaxes(f.Ctx, f.OrgID(), inv.ID); err != nil {
		t.Fatalf("pull taxes: %v", err)
	}

	t.Run("not voided", func(t *testing.T) {
		t.Log("source: spec/services/invoices/provider_taxes/void_service_spec.rb:117")
		_, err := f.svc.RetryTaxVoidingView(f.Ctx, f.OrgID(), inv.ID)
		if re, ok := errors.AsType[*core.RuleError](err); !ok || re.Code != invoicing.CodeStatusNotVoided {
			t.Errorf("got = %v, want = %s", err, invoicing.CodeStatusNotVoided)
		}
	})
	if _, err := f.svc.Void(f.Ctx, f.OrgID(), inv.ID, invoicing.VoidInput{}); err != nil {
		t.Fatalf("void: %v", err)
	}
	t.Run("provider failure", func(t *testing.T) {
		t.Log("source: spec/services/invoices/provider_taxes/void_service_spec.rb:176")
		f.Taxes.Fail = true
		defer func() { f.Taxes.Fail = false }()
		_, err := f.svc.RetryTaxVoidingView(f.Ctx, f.OrgID(), inv.ID)
		var ve *core.ValidationError
		if !errors.As(err, &ve) || len(ve.Errors) != 1 || ve.Errors[0].Field != "tax_error" || ve.Errors[0].Code != "internal_error" {
			t.Errorf("got = %v, want = tax_error: internal_error", err)
		}
	})
	t.Run("voided", func(t *testing.T) {
		t.Log("source: spec/services/invoices/provider_taxes/void_service_spec.rb:162")
		v, err := f.svc.RetryTaxVoidingView(f.Ctx, f.OrgID(), inv.ID)
		if err != nil || v.Invoice.Status != invoice.StatusVoided {
			t.Fatalf("got = %v, %v, want = the voided invoice", v, err)
		}
		if n := len(f.Taxes.Voided); n == 0 || f.Taxes.Voided[n-1] != inv.ID {
			t.Errorf("voided = %v, want = %s last", f.Taxes.Voided, inv.ID)
		}
	})
}
