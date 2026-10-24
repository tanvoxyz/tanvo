package integrations_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/integrations"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func TestDraftInvoiceTaxes(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*taxFixture, core.AddOnID, core.AddOnID) {
		t.Helper()
		f := newTaxFixture(t, integration.ProviderAnrok, nil)
		first, second := f.h.AddOn(1000), f.h.AddOn(2000)
		m := &integration.Mapping{
			ID: core.NewID[core.IntegrationMappingID](f.h.IDs), OrganizationID: f.h.OrgID(), IntegrationID: f.integ.ID,
			MappableType: integration.MappableTypeAddOn, MappableID: first.ID.UUID(), ExternalID: "lago_default_b2b",
			CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
		}
		f.h.Do(func(ctx context.Context, r port.Repos) error { return r.IntegrationMappings.Create(ctx, m) })
		return f, first.ID, second.ID
	}
	units := func(s string) *money.Decimal { d := money.MustParse(s); return &d }
	in := func(f *taxFixture, fees ...integrations.DraftInvoiceFee) integrations.DraftInvoiceTaxesInput {
		return integrations.DraftInvoiceTaxesInput{CustomerID: f.cust.ID, Currency: money.MustCurrency("USD"), Fees: fees}
	}
	lineIDs := func(f *taxFixture) []string {
		var out []string
		for _, l := range f.gw.quotes[0].Lines {
			out = append(out, l.ItemID)
		}
		return out
	}

	t.Run("fetches tax results", func(t *testing.T) {
		// source: spec/graphql/mutations/integrations/fetch_draft_invoice_taxes_spec.rb:100
		f, first, second := setup(t)
		rate := money.MustParse("0.10")
		f.gw.lineTax = func(integration.TaxLine) int64 { return 990 }
		f.gw.breakdown = []integration.TaxBreakdown{
			{Name: "GST/HST", Type: "tax_exempt", Rate: &rate, TaxAmount: money.NewFromInt(990)},
			{Reason: "reverseCharge", Type: "exempt"},
		}
		got, cur, err := f.svc.DraftInvoiceTaxes(ctx, f.h.OrgID(), in(f,
			integrations.DraftInvoiceFee{AddOn: refs.ByID(first), UnitAmount: units("12"), Units: units("2")},
			integrations.DraftInvoiceFee{AddOn: refs.ByID(second)},
		))
		if err != nil {
			t.Fatalf("DraftInvoiceTaxes: %v", err)
		}
		if cur.Code() != "USD" {
			t.Errorf("currency: got = %s, want = USD", cur.Code())
		}
		if want := []string{first.String(), second.String()}; !slices.Equal(lineIDs(f), want) {
			t.Errorf("line item ids: got = %v, want = %v", lineIDs(f), want)
		}
		q := f.gw.quotes[0]
		if q.Lines[0].ItemKey != "" || q.Lines[0].AmountCents != 2400 || q.Lines[1].AmountCents != 2000 {
			t.Errorf("lines: got = %+v, want = no item key, 2400 and 2000 cents", q.Lines)
		}
		if q.IssuingDate != core.DateOf(f.h.Now(), time.UTC) || q.ID != "" {
			t.Errorf("transaction: got = %s %q, want = today, no id", q.IssuingDate, q.ID)
		}
		r := got[0]
		if r.ItemID != first.String() || r.ItemCode != "lago_default_b2b" || r.TaxAmountCents == nil || *r.TaxAmountCents != 990 {
			t.Fatalf("fee: got = %+v, want = the first add-on, lago_default_b2b, 990", r)
		}
		if len(r.Breakdown) != 2 {
			t.Fatalf("breakdown: got = %+v, want = 2 taxes", r.Breakdown)
		}
		if b := r.Breakdown[0]; b.Name != "GST/HST" || b.Type != "tax_exempt" || b.Rate.Cmp(rate) != 0 || b.AppliedOnWholeInvoice() {
			t.Errorf("breakdown[0]: got = %+v, want = GST/HST tax_exempt at 0.10", b)
		}
		if b := r.Breakdown[1]; b.Name != "Reverse charge" || b.Type != "exempt" || !b.Rate.IsZero() || !b.AppliedOnWholeInvoice() || b.Code() != "reverse_charge" {
			t.Errorf("breakdown[1]: got = %+v, want = Reverse charge exempt at 0, reverse_charge", b)
		}
		if w := f.h.Webhooks(); len(w) != 0 {
			t.Errorf("webhooks: got = %v, want = none", w)
		}
	})

	t.Run("a fee without amount is left out", func(t *testing.T) {
		// source: spec/graphql/mutations/integrations/fetch_draft_invoice_taxes_spec.rb:169
		f, first, second := setup(t)
		if _, _, err := f.svc.DraftInvoiceTaxes(ctx, f.h.OrgID(), in(f,
			integrations.DraftInvoiceFee{AddOn: refs.ByID(first), UnitAmount: units("12"), Units: units("2")},
			integrations.DraftInvoiceFee{AddOn: refs.ByID(second), UnitAmount: units("0")},
		)); err != nil {
			t.Fatalf("DraftInvoiceTaxes: %v", err)
		}
		if want := []string{first.String()}; !slices.Equal(lineIDs(f), want) {
			t.Errorf("line item ids: got = %v, want = %v", lineIDs(f), want)
		}
	})

	t.Run("no fee with an amount", func(t *testing.T) {
		// source: spec/graphql/mutations/integrations/fetch_draft_invoice_taxes_spec.rb:205
		f, first, _ := setup(t)
		if _, _, err := f.svc.DraftInvoiceTaxes(ctx, f.h.OrgID(), in(f,
			integrations.DraftInvoiceFee{AddOn: refs.ByID(first), UnitAmount: units("0"), Units: units("2")},
		)); err != nil {
			t.Fatalf("DraftInvoiceTaxes: %v", err)
		}
		if want := []string{first.String()}; !slices.Equal(lineIDs(f), want) || f.gw.quotes[0].Lines[0].AmountCents != 0 {
			t.Errorf("lines: got = %+v, want = the first add-on at 0", f.gw.quotes[0].Lines)
		}
	})

	t.Run("tax error", func(t *testing.T) {
		// source: spec/graphql/mutations/integrations/fetch_draft_invoice_taxes_spec.rb:232
		f, first, _ := setup(t)
		f.gw.err = &integration.ProviderError{
			Provider: integration.ProviderAnrok, Kind: integration.KindInvalidRequest,
			Code: "taxDateTooFarInFuture", Message: "Tax date too far in future",
		}
		_, _, err := f.svc.DraftInvoiceTaxes(ctx, f.h.OrgID(), in(f, integrations.DraftInvoiceFee{AddOn: refs.ByID(first)}))
		if got := fieldCodes(err)["tax_error"]; got != "taxDateTooFarInFuture" {
			t.Errorf("error: got = %v, want = tax_error taxDateTooFarInFuture", err)
		}
		// taxes/base_service.rb:73: the customer exists, so the error is
		// announced.
		if w := f.h.Webhooks(); len(w) != 1 || w[0] != webhook.EventTypeCustomerTaxProviderError {
			t.Errorf("webhooks: got = %v, want = [customer.tax_provider_error]", w)
		}
	})

	t.Run("customer taxed by no provider", func(t *testing.T) {
		f, first, _ := setup(t)
		other := f.h.Customer()
		got, _, err := f.svc.DraftInvoiceTaxes(ctx, f.h.OrgID(), integrations.DraftInvoiceTaxesInput{
			CustomerID: other.ID, Fees: []integrations.DraftInvoiceFee{{AddOn: refs.ByID(first)}},
		})
		if err != nil || len(got) != 0 || len(f.gw.quotes) != 0 {
			t.Errorf("got = %v %v (%d quotes), want = empty, no quote", got, err, len(f.gw.quotes))
		}
	})

	t.Run("unknown customer or add-on", func(t *testing.T) {
		f, first, _ := setup(t)
		_, _, err := f.svc.DraftInvoiceTaxes(ctx, f.h.OrgID(), integrations.DraftInvoiceTaxesInput{
			CustomerExternalID: "unknown", Fees: []integrations.DraftInvoiceFee{{AddOn: refs.ByID(first)}},
		})
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("customer: got = %v, want = not found", err)
		}
		_, _, err = f.svc.DraftInvoiceTaxes(ctx, f.h.OrgID(), in(f, integrations.DraftInvoiceFee{AddOn: refs.ByCode("unknown")}))
		if !errors.Is(err, core.ErrNotFound) {
			t.Errorf("add-on: got = %v, want = not found", err)
		}
	})
}
