package fees_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// oneOff computes the one-off fees of items on a new invoice.
func (f *fixture) oneOff(items []fees.OneOff) ([]fees.OneOffFee, *invoice.Invoice, error) {
	f.T.Helper()
	inv := f.StoredInvoice(f.Customer(), func(i *invoice.Invoice) { i.Type = invoice.TypeOneOff })
	var out []fees.OneOffFee
	err := f.Deps.Tx.InTx(context.Background(), func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = f.svc.OneOffFees(ctx, r, inv, items)
		return err
	})
	return out, inv, err
}

// errValidation stands for a *core.ValidationError.
var errValidation = errors.New("validation")

// source: spec/services/fees/one_off_service_spec.rb:39
func TestOneOffFees(t *testing.T) {
	t.Parallel()
	now := date(2023, time.July, 19, 12, 12)
	f := newFixture(t, now)
	first, second := f.AddOn(100), f.AddOn(400)
	tax := f.Tax("tax2", "10")
	got, inv, err := f.oneOff([]fees.OneOff{
		{AddOnCode: first.Code, UnitAmount: ptr(int64(1200)), Units: ptr(dec("2")), Description: ptr("desc-123"), TaxCodes: []string{tax.Code}},
		{AddOnCode: second.Code},
	})
	mustNoErr(t, err)
	if len(got) != 2 {
		t.Fatalf("got %d fees, want = 2", len(got))
	}
	checkFee(t, got[0].Fee, wantFee{2400, "2400", "2", 1200, "12", 0})
	checkFee(t, got[1].Fee, wantFee{400, "400", "1", 400, "4", 0})
	for i, a := range []*catalog.AddOn{first, second} {
		fee := got[i].Fee
		if fee.AddOnID != a.ID || fee.InvoiceID != inv.ID || fee.Type != core.FeeTypeAddOn || fee.PaymentStatus != invoice.FeePaymentStatusPending {
			t.Errorf("got fee = %+v, want a pending add-on fee of the invoice", fee)
		}
		if !fee.Properties.From.Equal(now) || !fee.Properties.To.Equal(now) {
			t.Errorf("got boundaries = [%s, %s], want both at %s", fee.Properties.From, fee.Properties.To, now)
		}
	}
	if d := got[0].Fee.Description; d == nil || *d != "desc-123" {
		t.Errorf("got description = %v, want = desc-123", d)
	}
	if len(got[0].TaxIDs) != 1 || got[0].TaxIDs[0] != tax.ID || got[1].TaxIDs != nil {
		t.Errorf("got tax IDs = %v and %v, want = [%s] and nil", got[0].TaxIDs, got[1].TaxIDs, tax.ID)
	}
}

// source: spec/services/fees/one_off_service_spec.rb:111
func TestOneOffFeesBoundaries(t *testing.T) {
	t.Parallel()
	f := newFixture(t, date(2023, time.July, 19))
	a := f.AddOn(100)
	from, to := date(2022, time.January, 1), date(2022, time.January, 31)
	got, _, err := f.oneOff([]fees.OneOff{{AddOnID: a.ID, UnitAmount: ptr(int64(1200)), Units: ptr(dec("2")), From: &from, To: &to}})
	mustNoErr(t, err)
	p := got[0].Fee.Properties
	if !p.From.Equal(from) || !p.To.Equal(to) {
		t.Errorf("got boundaries = [%s, %s], want = [%s, %s]", p.From, p.To, from, to)
	}
}

func TestOneOffFeesErrors(t *testing.T) {
	t.Parallel()
	from, to := date(2022, time.February, 1), date(2022, time.January, 1)
	cases := []struct {
		name string
		item func(a *catalog.AddOn) fees.OneOff
		want error
	}{
		// source: spec/services/fees/one_off_service_spec.rb:160
		{name: "unknown add-on code", item: func(*catalog.AddOn) fees.OneOff { return fees.OneOff{AddOnCode: "unknown"} }, want: core.ErrNotFound},
		// source: spec/services/fees/one_off_service_spec.rb:188
		{name: "boundaries in the wrong order", item: func(a *catalog.AddOn) fees.OneOff { return fees.OneOff{AddOnID: a.ID, From: &from, To: &to} }, want: errValidation},
		// source: spec/services/fees/one_off_service_spec.rb:295
		{name: "one boundary missing", item: func(a *catalog.AddOn) fees.OneOff { return fees.OneOff{AddOnID: a.ID, From: &from} }, want: errValidation},
		{name: "unknown tax code", item: func(a *catalog.AddOn) fees.OneOff { return fees.OneOff{AddOnID: a.ID, TaxCodes: []string{"unknown"}} }, want: core.ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, date(2023, time.July, 19))
			_, _, err := f.oneOff([]fees.OneOff{tc.item(f.AddOn(100))})
			var ve *core.ValidationError
			if validation := errors.Is(tc.want, errValidation); validation && !errors.As(err, &ve) || !validation && !errors.Is(err, tc.want) {
				t.Errorf("got error = %v, want = %v", err, tc.want)
			}
		})
	}
}
