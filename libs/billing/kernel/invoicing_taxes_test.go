package kernel

import (
	"context"
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// countingProvider counts its tax computations; its other methods are
// not called.
type countingProvider struct {
	TaxProvider
	calls int
}

func (p *countingProvider) ComputeTaxes(context.Context, TaxRequest) ([]tax.ProviderTaxResult, error) {
	p.calls++
	return nil, nil
}

// A TaxCall answers only the request it was called for: an invoice that
// changed between the provider call and the transaction applying it is
// asked again (lago PullTaxesAndApplyService reloads the invoice in the
// applying transaction).
func TestTaxCallAsksAgainWhenRequestChanged(t *testing.T) {
	fee := func(amount int64) *invoice.Fee {
		return &invoice.Fee{Amount: amount, PreciseAmount: money.NewFromInt(amount), Units: money.Zero, PreciseCouponsAmount: money.Zero}
	}
	first := TaxRequest{Invoice: &invoice.Invoice{}, Fees: []*invoice.Fee{fee(1000)}}
	changed := TaxRequest{Invoice: &invoice.Invoice{}, Fees: []*invoice.Fee{fee(1200)}}
	tests := []struct {
		name      string
		apply     TaxRequest
		wantCall  bool
		wantCalls int
	}{
		{"same request applies the answer", first, false, 1},
		{"changed request is asked again", changed, true, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &countingProvider{}
			c := &TaxCall{}
			if _, _, err := c.answer(first); !errors.Is(err, ErrTaxCall) {
				t.Fatalf("first answer err = %v, want = %v", err, ErrTaxCall)
			}
			c.Call(t.Context(), p)
			_, _, err := c.answer(tt.apply)
			if got := errors.Is(err, ErrTaxCall); got != tt.wantCall {
				t.Fatalf("needs call = %v, want = %v (err %v)", got, tt.wantCall, err)
			}
			if tt.wantCall {
				c.Call(t.Context(), p)
				if _, _, err := c.answer(tt.apply); err != nil {
					t.Fatalf("answer after second call: %v", err)
				}
			}
			if p.calls != tt.wantCalls {
				t.Errorf("provider calls = %d, want = %d", p.calls, tt.wantCalls)
			}
		})
	}
}
