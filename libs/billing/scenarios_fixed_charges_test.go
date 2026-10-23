package billing_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// TestScenarioRefreshDraftKeepsPayInAdvanceFixedChargeFee ports
// spec/scenarios/fixed_charges/refresh_draft_keeps_pay_in_advance_fee_spec.rb
// "when a fee that billed nothing exists for the period": a zero-unit fee
// of the fixed charge on another invoice is not proof of billing, so a
// refresh of the draft rebuilds the fee. Lago brings the 2 units as a
// subscription override; tanvo has no override service yet, so the plan
// carries them.
//
// source: spec/scenarios/fixed_charges/refresh_draft_keeps_pay_in_advance_fee_spec.rb:76
func TestScenarioRefreshDraftKeepsPayInAdvanceFixedChargeFee(t *testing.T) {
	const want = 47_980 // 2 units × 239.90, a full calendar month
	s := newScenario(t, chgParse(t, "2024-03-01T00:00:00Z"))
	grace := 3
	c := s.Customer(func(c *customer.Customer) { c.InvoiceGracePeriodDays = &grace })
	p := s.Plan(period.Monthly, 0, func(p *catalog.Plan) { p.PayInAdvance = true })
	fc := s.FixedCharge(p, s.AddOn(0), catalog.FixedChargeModelStandard, `{"amount":"239.9"}`, "2", func(f *catalog.FixedCharge) {
		f.Prorated, f.PayInAdvance = true, true
	})
	sub := chgSubscribe(s, c, p)

	invs := chgSubInvoices(s, sub.ID)
	if len(invs) != 1 || invs[0].Status != invoice.StatusDraft {
		t.Fatalf("invoices = %d, want = 1 draft", len(invs))
	}
	draft := invs[0]
	fs := chgFeesOf(s, draft, core.FeeTypeFixedCharge)
	if len(fs) != 1 || fs[0].Amount != want {
		t.Fatalf("draft fixed charge fees = %d (amount %d), want = 1 (amount %d)", len(fs), chgSum(fs), want)
	}

	placeholder := s.StoredInvoice(c, func(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized })
	s.StoredFee(placeholder, 0, func(f *invoice.Fee) {
		f.Type, f.SubscriptionID, f.FixedChargeID = core.FeeTypeFixedCharge, sub.ID, fc.ID
		f.Units, f.PreciseAmount, f.Properties = money.Zero, money.Zero, fs[0].Properties
	})

	s.travelTo(chgParse(t, "2024-03-02T00:00:00Z"))
	_, err := s.E.Invoices.RefreshDraft(s.Ctx, s.OrgID(), draft.ID)
	s.must(err)
	s.drain()

	draft = s.Invoice(draft.ID)
	if got := chgSum(chgFeesOf(s, draft, core.FeeTypeFixedCharge)); got != want {
		t.Errorf("fixed charge fees amount = %d, want = %d", got, want)
	}
	if draft.TotalAmount != want {
		t.Errorf("total amount = %d, want = %d", draft.TotalAmount, want)
	}
}
