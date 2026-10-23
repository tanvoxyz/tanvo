package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/tax"
)

// ErrTaxesPending is returned (wrapped) by a [TaxProvider] whose taxes are
// computed asynchronously, and by [TaxResolver.Compute] for an invoice of
// a provider-taxed customer computed in a transaction: the invoice then
// waits with tax status pending, and [JobPullProviderTaxes] asks the
// provider later, outside any transaction (lago
// Invoices::ComputeTaxesAndTotalsService sets tax_status pending and
// enqueues PullTaxesAndApplyJob after commit).
var ErrTaxesPending = errors.New("billing: provider taxes pending")

// ErrTaxCall is returned (wrapped) by [TaxResolver.Compute] in a
// transaction run with a [TaxCall] that holds no result for the request:
// the caller rolls the transaction back, calls the provider with
// [TaxCall.Call] and runs it again.
var ErrTaxCall = errors.New("billing: tax provider call needed")

// TaxCall carries a tax provider call made outside a transaction into the
// transaction that applies it (lago PullTaxesAndApplyService calls the
// provider, then applies its taxes in a transaction). A first run of the
// transaction records the request and fails with [ErrTaxCall]; after
// [TaxCall.Call], a run computing the same request gets the provider's
// answer. A run computing another request (the invoice changed meanwhile)
// fails with ErrTaxCall again.
type TaxCall struct {
	req     *TaxRequest
	key     string
	called  string // the key of the request results answer
	results []tax.ProviderTaxResult
	err     error
}

type taxCallKey struct{}

// WithTaxCall returns ctx carrying c to [TaxResolver.Compute].
func WithTaxCall(ctx context.Context, c *TaxCall) context.Context {
	return context.WithValue(ctx, taxCallKey{}, c)
}

func taxCallFrom(ctx context.Context) *TaxCall {
	c, _ := ctx.Value(taxCallKey{}).(*TaxCall)
	return c
}

// Call asks p for the taxes of the recorded request. It must run outside
// any transaction.
func (c *TaxCall) Call(ctx context.Context, p TaxProvider) {
	if c.req == nil {
		return
	}
	c.results, c.err = p.ComputeTaxes(ctx, *c.req)
	c.called = c.key
}

// answer returns the provider's answer to req, or ErrTaxCall after
// recording req when it was not called for it.
func (c *TaxCall) answer(req TaxRequest) ([]tax.ProviderTaxResult, error, error) {
	key, err := taxRequestKey(req)
	if err != nil {
		return nil, nil, err
	}
	if c.called == "" || c.called != key {
		c.req, c.key = &req, key
		return nil, nil, ErrTaxCall
	}
	return c.results, c.err, nil
}

// taxRequestKey identifies what a provider is asked: the customer, the
// invoice and the taxed amounts of its fees.
func taxRequestKey(req TaxRequest) (string, error) {
	type fee struct {
		ID, Type, Amount, Precise, Coupons, Units string
	}
	k := struct {
		Customer, Invoice, Currency, Issuing string
		Draft                                bool
		Fees                                 []fee
	}{Draft: req.Draft}
	if req.Customer != nil {
		k.Customer = req.Customer.ID.String()
	}
	if inv := req.Invoice; inv != nil {
		k.Invoice, k.Currency, k.Issuing = inv.ID.String(), inv.Currency.Code(), inv.IssuingDate.String()
	}
	for _, f := range req.Fees {
		k.Fees = append(k.Fees, fee{
			ID: f.ID.String(), Type: string(f.Type), Amount: fmt.Sprint(f.Amount),
			Precise: f.PreciseAmount.String(), Coupons: f.PreciseCouponsAmount.String(), Units: f.Units.String(),
		})
	}
	b, err := json.Marshal(k)
	if err != nil {
		return "", fmt.Errorf("tax request key: %w", err)
	}
	return string(b), nil
}
