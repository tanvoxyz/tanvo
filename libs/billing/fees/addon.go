package fees

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// OneOff is one line of a one-off invoice: an add-on, by ID or code, with
// optional overrides.
type OneOff struct {
	// AddOnID is the add-on; used when set.
	AddOnID core.AddOnID
	// AddOnCode is the add-on's code, used when AddOnID is zero.
	AddOnCode string
	// UnitAmount is the price of one unit in minor units; nil means the
	// add-on's amount.
	UnitAmount *int64
	// Units is the quantity; nil means 1.
	Units *money.Decimal
	// InvoiceDisplayName overrides the fee's name on the invoice.
	InvoiceDisplayName *string
	// Description overrides the add-on's description.
	Description *string
	// From and To are the service period; both or neither are set, and
	// both default to the clock's instant.
	From, To *time.Time
	// TaxCodes are the taxes applied to the fee instead of the derived
	// ones.
	TaxCodes []string
}

// OneOffFee is a built add-on fee with its explicit taxes.
type OneOffFee struct {
	// Fee is the fee, on the invoice.
	Fee *invoice.Fee
	// TaxIDs are the taxes of OneOff.TaxCodes; nil means the taxes are
	// derived (see [kernel.TaxResolver.SetExplicit]).
	TaxIDs []core.TaxID
}

// OneOffFees builds the add-on fees of a one-off invoice (lago
// Fees::OneOffService). It fails with a not-found error for an unknown
// add-on or tax code, and with a validation error on boundaries given
// only in part or in the wrong order.
func (s *Service) OneOffFees(ctx context.Context, r port.Repos, inv *invoice.Invoice, items []OneOff) ([]OneOffFee, error) {
	out := make([]OneOffFee, 0, len(items))
	now := s.d.Now()
	for _, it := range items {
		a, err := oneOffAddOn(ctx, r, inv.OrganizationID, it)
		if err != nil {
			return nil, err
		}
		var v core.Validator
		v.Check((it.From == nil) == (it.To == nil) && (it.From == nil || !it.From.After(*it.To)), "boundaries", core.CodeInvalid)
		if err := v.Err(); err != nil {
			return nil, err
		}

		unit := a.Amount
		if it.UnitAmount != nil {
			unit = *it.UnitAmount
		}
		units := money.One
		if it.Units != nil {
			units = *it.Units
		}
		precise := kernel.Cents(unit).Mul(units)
		from, to := now, now
		if it.From != nil {
			from, to = it.From.UTC(), it.To.UTC()
		}
		f := &invoice.Fee{
			ID:                   core.NewID[core.FeeID](s.d.IDs),
			OrganizationID:       inv.OrganizationID,
			InvoiceID:            inv.ID,
			Type:                 core.FeeTypeAddOn,
			AddOnID:              a.ID,
			InvoiceDisplayName:   nonEmpty(it.InvoiceDisplayName),
			Description:          a.Description,
			Amount:               kernel.RoundCents(precise),
			PreciseAmount:        precise,
			UnitAmount:           unit,
			PreciseUnitAmount:    inv.Currency.MinorToUnits(kernel.Cents(unit)),
			Units:                units,
			TotalAggregatedUnits: units,
			TaxesPreciseAmount:   money.Zero,
			TaxesRate:            money.Zero,
			Currency:             inv.Currency,
			Invoiceable:          true,
			PaymentStatus:        invoice.FeePaymentStatusPending,
			Properties:           invoice.FeeProperties{From: from, To: to, Timestamp: now},
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		if it.Description != nil {
			f.Description = it.Description
		}
		of := OneOffFee{Fee: f}
		if len(it.TaxCodes) > 0 {
			if of.TaxIDs, err = taxIDs(ctx, r, inv.OrganizationID, it.TaxCodes); err != nil {
				return nil, err
			}
		}
		out = append(out, of)
	}
	return out, nil
}

// oneOffAddOn loads the add-on of a one-off line.
func oneOffAddOn(ctx context.Context, r port.Repos, org core.OrganizationID, it OneOff) (*catalog.AddOn, error) {
	var (
		a   *catalog.AddOn
		err error
	)
	if !it.AddOnID.IsZero() {
		a, err = r.AddOns.Get(ctx, org, it.AddOnID)
	} else {
		a, err = r.AddOns.GetByCode(ctx, org, it.AddOnCode)
	}
	if err != nil {
		return nil, fmt.Errorf("load add-on: %w", err)
	}
	return a, nil
}

// taxIDs resolves tax codes, failing with a not-found error for an unknown
// one.
func taxIDs(ctx context.Context, r port.Repos, org core.OrganizationID, codes []string) ([]core.TaxID, error) {
	ts, err := r.Taxes.ListByCodes(ctx, org, codes)
	if err != nil {
		return nil, fmt.Errorf("load taxes: %w", err)
	}
	byCode := make(map[string]core.TaxID, len(ts))
	for _, t := range ts {
		byCode[t.Code] = t.ID
	}
	out := make([]core.TaxID, 0, len(codes))
	for _, c := range codes {
		id, ok := byCode[c]
		if !ok {
			return nil, fmt.Errorf("tax %q: %w", c, core.ErrNotFound)
		}
		out = append(out, id)
	}
	return out, nil
}

// nonEmpty returns p, or nil when it points to an empty string.
func nonEmpty(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}
