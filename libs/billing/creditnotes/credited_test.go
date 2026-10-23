package creditnotes_test

import (
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
)

func TestCreditedAcrossNotes(t *testing.T) {
	tests := []struct {
		name   string
		source string
		run    func(t *testing.T, f *fixture)
	}{
		{
			name: "a voided note keeps what it credited", source: "app/models/fee.rb:289",
			run: func(t *testing.T, f *fixture) {
				cn, err := f.svc.Create(f.h.Ctx, f.input(12, 0, item(f.fees[0], "10")))
				must(t, err)
				_, err = f.svc.Void(f.h.Ctx, f.h.OrgID(), cn.ID)
				must(t, err)
				_, err = f.svc.Create(f.h.Ctx, f.input(12, 0, item(f.fees[0], "10")))
				wantFields(t, err, "items[0].amount:"+creditnote.CodeHigherThanRemainingFeeAmount)
			},
		},
		{
			name: "a preview of all that is left absorbs the rounding like the note", source: "app/services/credit_notes/create_service.rb:271",
			run: func(t *testing.T, f *fixture) {
				for range 2 {
					_, err := f.svc.Create(f.h.Ctx, f.input(4, 0, item(f.fees[0], "3")))
					must(t, err)
				}
				in := f.input(16, 0, item(f.fees[0], "4"), item(f.fees[1], "10"))
				in.Preview = true
				preview, err := f.svc.Create(f.h.Ctx, in)
				must(t, err)
				in.Preview = false
				cn, err := f.svc.Create(f.h.Ctx, in)
				must(t, err)
				eq(t, "taxes", cn.TaxesAmount, 2)
				eq(t, "preview taxes", preview.TaxesAmount, cn.TaxesAmount)
				eq(t, "preview total", preview.TotalAmount, cn.TotalAmount)
			},
		},
		{
			name: "a credit invoice is not reported to the tax provider", source: "app/jobs/credit_notes/provider_taxes/report_job.rb:9",
			run: func(t *testing.T, f *fixture) {
				f.h.Taxes.Customers = map[core.CustomerID]bool{f.cust.ID: true}
				cn := f.h.StoredCreditNote(f.inv, 5)
				f.inv.Type = invoice.TypeCredit
				f.h.SaveInvoice(f.inv)
				must(t, f.svc.ReportTaxes(f.h.Ctx, f.h.OrgID(), cn.ID))
				eq(t, "reported", len(f.h.Taxes.Reported), 0)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			tt.run(t, newFixture(t))
		})
	}
}
