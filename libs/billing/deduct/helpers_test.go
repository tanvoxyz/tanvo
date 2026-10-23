package deduct_test

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// fixture is a harness with a customer and the service.
type fixture struct {
	h    *billingtest.Harness
	s    *deduct.Service
	cust *customer.Customer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := billingtest.New(t, billingtest.Date(2024, time.March, 1))
	return &fixture{h: h, s: deduct.New(h.Deps), cust: h.Customer()}
}

// env returns the environment of the fixture's customer.
func (f *fixture) env() *kernel.CustomerEnv {
	return kernel.NewCustomerEnv(f.cust, f.h.BillingEntity)
}

// before runs ApplyBeforeTaxes on the stored fees of inv and saves the
// fees and the invoice (unless opts.Preview).
func (f *fixture) before(inv *invoice.Invoice, opts deduct.Options) (deduct.BeforeTaxes, []*invoice.Fee, error) {
	f.h.T.Helper()
	var out deduct.BeforeTaxes
	var fees []*invoice.Fee
	err := f.h.Deps.Tx.InTx(f.h.Ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if fees, err = r.Fees.ListByInvoice(ctx, inv.OrganizationID, inv.ID); err != nil {
			return err
		}
		if out, err = f.s.ApplyBeforeTaxes(ctx, r, f.env(), inv, fees, opts); err != nil {
			return err
		}
		if opts.Preview {
			return nil
		}
		return save(ctx, r, inv, fees)
	})
	return out, fees, err
}

// after sets the invoice's subtotal including taxes (taxes of the fees'
// TaxesAmount) and runs ApplyAfterTaxes on its stored fees, then saves.
func (f *fixture) after(inv *invoice.Invoice, opts deduct.Options) (deduct.AfterTaxes, []*invoice.Fee, error) {
	f.h.T.Helper()
	var out deduct.AfterTaxes
	var fees []*invoice.Fee
	err := f.h.Deps.Tx.InTx(f.h.Ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		if fees, err = r.Fees.ListByInvoice(ctx, inv.OrganizationID, inv.ID); err != nil {
			return err
		}
		var taxes int64
		for _, fe := range fees {
			taxes += fe.TaxesAmount
		}
		inv.TaxesAmount = taxes
		inv.SubTotalIncludingTaxesAmount = inv.SubTotalExcludingTaxesAmount + taxes
		if out, err = f.s.ApplyAfterTaxes(ctx, r, f.env(), inv, fees, nil, opts); err != nil {
			return err
		}
		if opts.Preview {
			return nil
		}
		return save(ctx, r, inv, fees)
	})
	return out, fees, err
}

func save(ctx context.Context, r port.Repos, inv *invoice.Invoice, fees []*invoice.Fee) error {
	for _, fe := range fees {
		if err := r.Fees.Update(ctx, fe); err != nil {
			return err
		}
	}
	return r.Invoices.Update(ctx, inv)
}

// mustOK fails the test on err.
func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("got error = %v, want = nil", err)
	}
}

// eqDec checks a decimal against its string form.
func eqDec(t *testing.T, what string, got money.Decimal, want string) {
	t.Helper()
	if !got.Equal(money.MustParse(want)) {
		t.Errorf("%s: got = %s, want = %s", what, got, want)
	}
}

// eq checks a comparable value.
func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got = %v, want = %v", what, got, want)
	}
}

func ptr[T any](v T) *T { return &v }
