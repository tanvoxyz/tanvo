package deduct_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// pbFee is a fee of a progressive billing invoice on charge (index into
// the plan's charges, -1 for a charge that is not on the final invoice).
type pbFee struct {
	charge  int
	amount  int64
	coupons int64
}

// pbInvoice is a finalized progressive billing invoice issued days (and
// minutes) before the final invoice, for charges over the month before.
type pbInvoice struct {
	days, minutes int
	fees          []pbFee
	coupons       int64
	// creditNote is (credit amount, item amount on the first fee).
	creditNote *[2]int64
	status     invoice.Status
}

// finalFee is a fee of the final invoice on charge (index, -1 for a
// subscription fee).
type finalFee struct {
	charge int
	amount int64
}

type pbScene struct {
	f       *fixture
	sub     *subscription.Subscription
	charges []*catalog.Charge
	inv     *invoice.Invoice
	fees    []*invoice.Fee
	pbs     []*invoice.Invoice
	pbFees  [][]*invoice.Fee
}

func newPBScene(t *testing.T, final []finalFee, pbs []pbInvoice) *pbScene {
	t.Helper()
	f := newFixture(t)
	h := f.h
	p := h.Plan(period.Monthly, 0)
	sc := &pbScene{f: f}
	for i := range 3 {
		m := h.Metric("m"+string(rune('a'+i)), catalog.AggregationTypeCount, "")
		sc.charges = append(sc.charges, h.Charge(p, m, catalog.ChargeModelStandard, `{"amount":"1"}`))
	}
	sc.sub = h.Subscription(f.cust, p, billingtest.SubscribedAt(billingtest.Date(2023, time.January, 1)))
	chargeOf := func(i int) core.ChargeID {
		if i < 0 {
			return sc.charges[2].ID
		}
		return sc.charges[i].ID
	}
	now := h.Now()
	sc.inv = h.StoredInvoice(f.cust)
	h.LinkSubscription(sc.inv, sc.sub, invoice.InvoicingReasonSubscriptionPeriodic, now.AddDate(0, -1, 0), now)
	for _, ff := range final {
		opt := billingtest.SubscriptionFee(sc.sub)
		if ff.charge >= 0 {
			opt = billingtest.ChargeFee(sc.sub, chargeOf(ff.charge))
		}
		sc.fees = append(sc.fees, h.StoredFee(sc.inv, ff.amount, opt))
	}
	for _, pb := range pbs {
		issued := now.AddDate(0, 0, -pb.days)
		created := issued.Add(time.Duration(pb.minutes) * time.Minute)
		status := pb.status
		if status == "" {
			status = invoice.StatusFinalized
		}
		var total int64
		for _, fe := range pb.fees {
			total += fe.amount
		}
		inv := h.StoredInvoice(f.cust, func(inv *invoice.Invoice) {
			inv.Type, inv.Status = invoice.TypeProgressiveBilling, status
			inv.IssuingDate, inv.CreatedAt = core.DateOf(issued, time.UTC), created
			inv.FeesAmount, inv.CouponsAmount = total, pb.coupons
		})
		from := issued.AddDate(0, -1, 0).Add(time.Duration(pb.minutes) * time.Minute)
		h.LinkSubscription(inv, sc.sub, invoice.InvoicingReasonProgressiveBilling, from, issued)
		var fees []*invoice.Fee
		for _, fe := range pb.fees {
			fees = append(fees, h.StoredFee(inv, fe.amount, billingtest.ChargeFee(sc.sub, chargeOf(fe.charge)), func(x *invoice.Fee) {
				x.PreciseCouponsAmount = money.NewFromInt(fe.coupons)
			}))
		}
		if pb.creditNote != nil {
			cn := pb.creditNote
			h.StoredCreditNote(inv, cn[0], func(c *creditnote.CreditNote) {
				c.Items = []creditnote.Item{{ID: core.NewID[core.CreditNoteItemID](h.IDs), FeeID: fees[0].ID, Amount: cn[1], PreciseAmount: money.NewFromInt(cn[1])}}
			})
		}
		sc.pbs = append(sc.pbs, inv)
		sc.pbFees = append(sc.pbFees, fees)
	}
	return sc
}

func TestApplyProgressiveBilling(t *testing.T) {
	type wantNote struct {
		pb     int
		amount int64
	}
	tests := []struct {
		name      string
		final     []finalFee
		pbs       []pbInvoice
		wantPB    int // index of the credited invoice, -1 for none
		want      int64
		wantFees  []string
		wantNotes []wantNote
	}{
		// source: spec/services/credits/progressive_billing_service_spec.rb:35
		{name: "no progressive billing invoice", final: []finalFee{{0, 500}, {1, 500}}, wantPB: -1, wantFees: []string{"0", "0"}},
		// source: spec/services/credits/progressive_billing_service_spec.rb:76
		{
			name: "one invoice", final: []finalFee{{0, 500}, {1, 500}},
			pbs:    []pbInvoice{{days: 1, fees: []pbFee{{charge: 0, amount: 20}}}},
			wantPB: 0, want: 20, wantFees: []string{"20", "0"},
		},
		// source: spec/services/credits/progressive_billing_service_spec.rb:98
		{
			name: "credit above the fee", final: []finalFee{{0, 19}, {1, 500}},
			pbs:    []pbInvoice{{days: 1, fees: []pbFee{{charge: 0, amount: 20}}}},
			wantPB: 0, want: 19, wantFees: []string{"19", "0"},
			wantNotes: []wantNote{{0, 1}},
		},
		// source: spec/services/credits/progressive_billing_service_spec.rb:116
		{
			name: "additional subscription fee", final: []finalFee{{0, 300}, {1, 300}, {-1, 400}},
			pbs:    []pbInvoice{{days: 1, fees: []pbFee{{charge: 0, amount: 20}}}},
			wantPB: 0, want: 20, wantFees: []string{"20", "0", "0"},
		},
		// source: spec/services/credits/progressive_billing_service_spec.rb:153
		{
			name: "same charges as the final invoice", final: []finalFee{{0, 500}, {1, 500}},
			pbs:    []pbInvoice{{days: 1, fees: []pbFee{{charge: 0, amount: 20}, {charge: 1, amount: 20}}}},
			wantPB: 0, want: 40, wantFees: []string{"20", "20"},
		},
		// source: spec/services/credits/progressive_billing_service_spec.rb:219
		{
			name: "latest of several invoices", final: []finalFee{{0, 500}, {1, 500}},
			pbs: []pbInvoice{
				{days: 2, fees: []pbFee{{charge: 0, amount: 20}}},
				{days: 1, fees: []pbFee{{charge: 0, amount: 200}}},
			},
			wantPB: 1, want: 200, wantFees: []string{"200", "0"},
		},
		// source: spec/services/credits/progressive_billing_service_spec.rb:291
		{
			name: "latest created on the same date", final: []finalFee{{0, 500}, {1, 500}},
			pbs: []pbInvoice{
				{days: 1, fees: []pbFee{{charge: 0, amount: 20}}},
				{days: 1, minutes: 10, fees: []pbFee{{charge: 0, amount: 200}}},
			},
			wantPB: 1, want: 200, wantFees: []string{"200", "0"},
		},
		// source: spec/services/credits/progressive_billing_service_spec.rb:397
		// source: spec/services/credits/progressive_billing_service_spec.rb:406
		{
			name: "billed above the charges", final: []finalFee{{0, 500}, {1, 500}},
			pbs: []pbInvoice{
				{days: 3, fees: []pbFee{{charge: 0, amount: 20}}},
				{days: 2, fees: []pbFee{{charge: 0, amount: 1000}}},
				{days: 1, fees: []pbFee{{charge: 0, amount: 2000}}},
			},
			wantPB: 2, want: 500, wantFees: []string{"500", "0"},
			wantNotes: []wantNote{{2, 1500}},
		},
		// source: spec/services/credits/progressive_billing_service_spec.rb:470
		{
			name: "outside the billing boundaries", final: []finalFee{{0, 500}, {1, 500}},
			pbs:    []pbInvoice{{days: 60, fees: []pbFee{{charge: 0, amount: 20}}}},
			wantPB: -1, wantFees: []string{"0", "0"},
		},
		// source: spec/services/credits/progressive_billing_service_spec.rb:501
		{
			name: "coupon and previous credit note", final: []finalFee{{0, 100_00}},
			pbs: []pbInvoice{{
				days: 1, fees: []pbFee{{charge: 0, amount: 100_00, coupons: 20_00}},
				coupons: 20_00, creditNote: &[2]int64{20_00, 25_00},
			}},
			wantPB: 0, want: 75_00, wantFees: []string{"7500"},
		},
		// source: spec/services/credits/progressive_billing_service_spec.rb:534
		// lago skips the credit note because its credit amount, net of the
		// fee's coupons, is zero; Tanvo reports the items and the credit
		// notes service makes that decision.
		{
			name: "discounted fee no longer on the final invoice", final: []finalFee{{0, 50_00}},
			pbs: []pbInvoice{{
				days: 1, fees: []pbFee{{charge: 0, amount: 40_00}, {charge: -1, amount: 20_00, coupons: 20_00}},
				coupons: 20_00,
			}},
			wantPB: 0, want: 40_00, wantFees: []string{"4000"},
			wantNotes: []wantNote{{0, 20_00}},
		},
		{
			name: "voided invoice ignored", final: []finalFee{{0, 500}},
			pbs:    []pbInvoice{{days: 1, fees: []pbFee{{charge: 0, amount: 20}}, status: invoice.StatusVoided}},
			wantPB: -1, wantFees: []string{"0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := newPBScene(t, tt.final, tt.pbs)
			out, fees, err := sc.f.before(sc.inv, deduct.Options{})
			mustOK(t, err)
			eq(t, "progressive credit", out.ProgressiveBillingCredit, tt.want)
			eq(t, "invoice progressive credit", sc.inv.ProgressiveBillingCreditAmount, tt.want)
			credits := sc.f.h.InvoiceCredits(sc.inv.ID)
			if tt.wantPB < 0 {
				eq(t, "credits", len(credits), 0)
			} else {
				if len(credits) != 1 {
					t.Fatalf("credits: got = %d, want = 1", len(credits))
				}
				eq(t, "credited invoice", credits[0].ProgressiveBillingInvoiceID, sc.pbs[tt.wantPB].ID)
				eq(t, "credit amount", credits[0].Amount, tt.want)
			}
			byID := map[core.FeeID]*invoice.Fee{}
			for _, fe := range fees {
				byID[fe.ID] = fe
			}
			for i, want := range tt.wantFees {
				eqDec(t, "fee coupons", byID[sc.fees[i].ID].PreciseCouponsAmount, want)
			}
			if len(out.ProgressiveCreditNotes) != len(tt.wantNotes) {
				t.Fatalf("credit notes: got = %d, want = %d", len(out.ProgressiveCreditNotes), len(tt.wantNotes))
			}
			for i, w := range tt.wantNotes {
				eq(t, "note invoice", out.ProgressiveCreditNotes[i].InvoiceID, sc.pbs[w.pb].ID)
				eq(t, "note amount", out.ProgressiveCreditNotes[i].Amount, w.amount)
			}
		})
	}
}

// A second application finds the credit already taken: what is left to
// invoice is net of active credits.
func TestProgressiveBilledAmount(t *testing.T) {
	sc := newPBScene(t, []finalFee{{0, 500}}, []pbInvoice{{days: 1, fees: []pbFee{{charge: 0, amount: 200}}, coupons: 50}})
	_, _, err := sc.f.before(sc.inv, deduct.Options{})
	mustOK(t, err)
	var got deduct.ProgressiveBilled
	sc.f.h.Do(func(ctx context.Context, r port.Repos) error {
		var err error
		got, err = sc.f.s.ProgressiveBilledAmount(ctx, r, sc.f.h.OrgID(), sc.sub.ID, sc.f.h.Now().AddDate(0, -1, 0), false)
		return err
	})
	eq(t, "invoice", got.Invoice.ID, sc.pbs[0].ID)
	eq(t, "billed", got.BilledAmount, 200)
	eq(t, "total billed", got.TotalBilledAmount, 200)
	eq(t, "to invoice", got.ToInvoice, 0)
	eq(t, "to credit", got.ToCredit, 0)

	sc.inv.Status = invoice.StatusVoided
	sc.f.h.SaveInvoice(sc.inv)
	sc.f.h.Do(func(ctx context.Context, r port.Repos) error {
		var err error
		got, err = sc.f.s.ProgressiveBilledAmount(ctx, r, sc.f.h.OrgID(), sc.sub.ID, sc.f.h.Now().AddDate(0, -1, 0), false)
		return err
	})
	eq(t, "to invoice after void", got.ToInvoice, 200)
	eq(t, "to credit after void", got.ToCredit, 150)
}
