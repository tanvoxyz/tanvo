package products

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// RateCardInput is a rate card to create.
type RateCardInput struct {
	// Product references the priced product.
	Product refs.Ref
	// Filter references the priced filter of the product; zero prices the
	// whole product.
	Filter refs.Ref
	// Code is the unique code; surrounding spaces are trimmed.
	Code string
	// Name is the display name.
	Name string
	// Description is free text.
	Description *string
	// Currency is the card's currency.
	Currency money.Currency
	// BillingTiming is when the card bills; arrears when empty.
	BillingTiming catalog.BillingTiming
	// Proration prorates partial intervals; false when nil.
	Proration *bool
	// DisplayOnInvoice prints the card's fees; true when nil.
	DisplayOnInvoice *bool
	// RegroupPaidFees regroups paid advance fees; empty keeps them
	// standalone.
	RegroupPaidFees catalog.RegroupPaidFees
	// PricingUnit references the pricing unit of the rates; zero for none.
	PricingUnit refs.Ref
	// Taxes reference the card's taxes; nil sets none.
	Taxes []refs.Ref
	// Rates are the card's first rates, created in order; their errors
	// are reported under rates.
	Rates []RateInput
}

// card is a rate card with what its rules read: product, filter and the
// product's metric (nil for a fixed product).
type card struct {
	c *catalog.RateCard
	p *catalog.Product
	f *catalog.ProductFilter
	m *catalog.BillableMetric
}

// CreateRateCard creates a rate card with its taxes and rates. A missing
// product is core.NotFound("product"), a missing filter
// core.NotFound("product_filter"), a missing tax core.NotFound("tax"), an
// unknown pricing unit pricing_unit_id value_is_invalid.
//
// source: app/services/rate_cards/create_service.rb:20-62, app/controllers/api/v2/rate_cards_controller.rb:8-25
func (s *Service) CreateRateCard(ctx context.Context, org core.OrganizationID, in RateCardInput) (*catalog.RateCard, error) {
	var out *catalog.RateCard
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := productsOf(r, org).One(ctx, in.Product)
		if err != nil {
			return err
		}
		k := card{p: p}
		if !in.Filter.IsZero() {
			if k.f, err = filtersOf(r, org, p.ID).One(ctx, in.Filter); err != nil {
				return err
			}
		}
		var unit core.PricingUnitID
		if !in.PricingUnit.IsZero() {
			u, err := refs.PricingUnits(r, org).One(ctx, in.PricingUnit)
			if isNotFound(err) {
				return fieldError("pricing_unit_id", core.CodeInvalid)
			}
			if err != nil {
				return err
			}
			unit = u.ID
		}
		if k.m, err = productMetric(ctx, r, p); err != nil {
			return err
		}
		now := s.d.Now()
		c := &catalog.RateCard{
			ID:               core.NewID[core.RateCardID](s.d.IDs),
			OrganizationID:   org,
			ProductID:        p.ID,
			Code:             strip(in.Code),
			Name:             in.Name,
			Description:      in.Description,
			Currency:         in.Currency,
			BillingTiming:    in.BillingTiming,
			DisplayOnInvoice: true,
			RegroupPaidFees:  in.RegroupPaidFees,
			PricingUnitID:    unit,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if c.BillingTiming == "" {
			c.BillingTiming = catalog.BillingTimingArrears
		}
		if k.f != nil {
			c.ProductFilterID = k.f.ID
		}
		if in.Proration != nil {
			c.Proration = *in.Proration
		}
		if in.DisplayOnInvoice != nil {
			c.DisplayOnInvoice = *in.DisplayOnInvoice
		}
		k.c = c
		if err := validateRateCard(ctx, r, k); err != nil {
			return err
		}
		if in.Taxes != nil {
			if c.TaxIDs, err = refs.Taxes(r, org).IDs(ctx, in.Taxes); err != nil {
				return err
			}
		}
		if err := r.RateCards.Create(ctx, c); err != nil {
			return conflict(err, "code")
		}
		if len(in.Rates) > 0 {
			loc, err := orgLocation(ctx, r, org)
			if err != nil {
				return err
			}
			for _, ri := range in.Rates {
				if _, err := s.createRate(ctx, r, k, ri, loc); err != nil {
					return prefixed("rates", err)
				}
			}
		}
		out = c
		// The rates created with the card log nothing of their own
		// (create_service.rb:84, emit_activity_log: false).
		//
		// source: app/services/rate_cards/create_service.rb:15-18
		return s.logRateCard(ctx, r, auditlog.ActivityRateCardCreated, nil, c)
	})
	if err != nil {
		return nil, fmt.Errorf("create rate card: %w", err)
	}
	return out, nil
}

// RateCardUpdate is a partial update of a rate card: only the set fields
// change.
type RateCardUpdate struct {
	// Code is the unique code; locked once a plan applies the card.
	Code core.Optional[string]
	// Name is the display name.
	Name core.Optional[string]
	// Description is free text.
	Description core.Optional[*string]
	// Currency is locked once the card has rates or a plan applies it.
	Currency core.Optional[money.Currency]
	// BillingTiming is locked once the card has rates.
	BillingTiming core.Optional[catalog.BillingTiming]
	// Proration is locked once the card has rates.
	Proration core.Optional[bool]
	// DisplayOnInvoice is locked once the card has rates.
	DisplayOnInvoice core.Optional[bool]
	// RegroupPaidFees is locked once the card has rates.
	RegroupPaidFees core.Optional[catalog.RegroupPaidFees]
	// PricingUnit references the pricing unit, a zero reference none;
	// locked once the card has rates.
	PricingUnit core.Optional[refs.Ref]
	// Taxes replace the card's taxes; an empty list removes them.
	Taxes core.Optional[[]refs.Ref]
}

// UpdateRateCard changes the rate card id. Once the card has rates, a
// changed currency, pricing unit, billing timing, proration, regrouping
// or display is <field> not_editable_with_rates; once a plan applies it,
// a changed currency or code is <field> attached_to_plan_or_subscription.
//
// source: app/services/rate_cards/update_service.rb:21-58
func (s *Service) UpdateRateCard(ctx context.Context, org core.OrganizationID, id core.RateCardID, in RateCardUpdate) (*catalog.RateCard, error) {
	var out *catalog.RateCard
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		k, err := loadCard(ctx, r, org, id)
		if err != nil {
			return err
		}
		c := k.c
		var unit core.PricingUnitID
		if in.PricingUnit.Set && !in.PricingUnit.Value.IsZero() {
			u, err := refs.PricingUnits(r, org).One(ctx, in.PricingUnit.Value)
			if isNotFound(err) {
				return fieldError("pricing_unit_id", core.CodeInvalid)
			}
			if err != nil {
				return err
			}
			unit = u.ID
		}
		rates, err := r.Rates.ListByRateCard(ctx, org, id)
		if err != nil {
			return fmt.Errorf("list rates: %w", err)
		}
		before, err := auditlog.ObjectOf(rateCardObject{RateCard: c, Rates: rates})
		if err != nil {
			return err
		}
		if len(rates) > 0 {
			locked := []struct {
				field   string
				changed bool
			}{
				{"currency", in.Currency.Set && in.Currency.Value != c.Currency},
				{"pricing_unit_id", in.PricingUnit.Set && unit != c.PricingUnitID},
				{"billing_timing", in.BillingTiming.Set && in.BillingTiming.Value != c.BillingTiming},
				{"proration", in.Proration.Set && in.Proration.Value != c.Proration},
				{"regroup_paid_fees", in.RegroupPaidFees.Set && in.RegroupPaidFees.Value != c.RegroupPaidFees},
				{"display_on_invoice", in.DisplayOnInvoice.Set && in.DisplayOnInvoice.Value != c.DisplayOnInvoice},
			}
			for _, l := range locked {
				if l.changed {
					return fieldError(l.field, catalog.CodeNotEditableWithRates)
				}
			}
		}
		currencyChanged := in.Currency.Set && in.Currency.Value != c.Currency
		codeChanged := in.Code.Set && strip(in.Code.Value) != c.Code
		if currencyChanged || codeChanged {
			ok, err := attached(ctx, r, org, catalog.PlanRateCardQuery{RateCardID: id})
			if err != nil {
				return err
			}
			if ok && currencyChanged {
				return fieldError("currency", catalog.CodeAttachedToPlanOrSubscription)
			}
			if ok {
				return fieldError("code", catalog.CodeAttachedToPlanOrSubscription)
			}
		}
		if in.Code.Set {
			c.Code = strip(in.Code.Value)
		}
		in.Name.Apply(&c.Name)
		in.Description.Apply(&c.Description)
		in.Currency.Apply(&c.Currency)
		in.BillingTiming.Apply(&c.BillingTiming)
		in.Proration.Apply(&c.Proration)
		in.DisplayOnInvoice.Apply(&c.DisplayOnInvoice)
		in.RegroupPaidFees.Apply(&c.RegroupPaidFees)
		if in.PricingUnit.Set {
			c.PricingUnitID = unit
		}
		if err := validateRateCard(ctx, r, k); err != nil {
			return err
		}
		if in.Taxes.Set {
			if c.TaxIDs, err = refs.Taxes(r, org).IDs(ctx, in.Taxes.Value); err != nil {
				return err
			}
			if c.TaxIDs == nil {
				c.TaxIDs = []core.TaxID{}
			}
		}
		c.UpdatedAt = s.d.Now()
		if err := r.RateCards.Update(ctx, c); err != nil {
			return conflict(err, "code")
		}
		out = c
		// source: app/services/rate_cards/update_service.rb:19-22
		return s.logRateCard(ctx, r, auditlog.ActivityRateCardUpdated, before, c)
	})
	if err != nil {
		return nil, fmt.Errorf("update rate card %s: %w", id, err)
	}
	return out, nil
}

// DeleteRateCard deletes the rate card id with its rates. A card a plan
// applies is rate_card attached_to_plan_or_subscription.
//
// source: app/services/rate_cards/destroy_service.rb:15-31
func (s *Service) DeleteRateCard(ctx context.Context, org core.OrganizationID, id core.RateCardID) (*catalog.RateCard, error) {
	var out *catalog.RateCard
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := getRateCard(ctx, r, org, id)
		if err != nil {
			return notFound(err, ResourceRateCard)
		}
		ok, err := attached(ctx, r, org, catalog.PlanRateCardQuery{RateCardID: id})
		if err != nil {
			return err
		}
		if ok {
			return fieldError("rate_card", catalog.CodeAttachedToPlanOrSubscription)
		}
		now := s.d.Now()
		if err := r.RateCards.Discard(ctx, org, id, now); err != nil {
			return fmt.Errorf("discard: %w", err)
		}
		c.DeletedAt = &now
		out = c
		// source: app/services/rate_cards/destroy_service.rb:12-15
		return s.logRateCard(ctx, r, auditlog.ActivityRateCardDeleted, nil, c)
	})
	if err != nil {
		return nil, fmt.Errorf("delete rate card %s: %w", id, err)
	}
	return out, nil
}

// RateCard returns the rate card ref references.
func (s *Service) RateCard(ctx context.Context, org core.OrganizationID, ref refs.Ref) (*catalog.RateCard, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*catalog.RateCard, error) {
		return rateCardsOf(r, org).One(ctx, ref)
	})
}

// RateCardList narrows [Service.ListRateCards].
type RateCardList struct {
	// Search keeps cards whose name or code contains the term.
	Search string
	// Products keep cards of these products; a missing one is
	// core.NotFound("product").
	Products []refs.Ref
	// Filters keep cards of these product filters.
	Filters []core.ProductFilterID
	// Categories keep cards of the products of these categories; a
	// missing one is core.NotFound("product_category").
	Categories []refs.Ref
	// WithoutCategory keeps cards of uncategorized products; with
	// Categories, either matches.
	WithoutCategory bool
}

// ListRateCards returns a page of the rate cards matching q, most recent
// first.
//
// source: app/queries/rate_cards_query.rb:16-34
func (s *Service) ListRateCards(ctx context.Context, org core.OrganizationID, q RateCardList, page core.PageRequest) (core.Page[*catalog.RateCard], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*catalog.RateCard], error) {
		products, err := productsOf(r, org).IDs(ctx, q.Products)
		if err != nil {
			return core.Page[*catalog.RateCard]{}, err
		}
		cats, err := categories(r, org).IDs(ctx, q.Categories)
		if err != nil {
			return core.Page[*catalog.RateCard]{}, err
		}
		return r.RateCards.List(ctx, org, catalog.RateCardQuery{
			Search:                 q.Search,
			ProductIDs:             products,
			ProductFilterIDs:       q.Filters,
			ProductCategoryIDs:     cats,
			WithoutProductCategory: q.WithoutCategory,
		}, page)
	})
}

// RateCardDetails are the read-only fields lago serializes with a card:
// its product and filter codes, its rate count and the rate in effect.
type RateCardDetails struct {
	// Product is the priced product.
	Product *catalog.Product
	// Filter is the priced filter, nil for the whole product.
	Filter *catalog.ProductFilter
	// RatesCount is the number of the card's rates.
	RatesCount int
	// ActiveRate is the rate in effect now, nil before the first.
	ActiveRate *catalog.Rate
	// Rates are the card's rates, latest effective first.
	Rates []*catalog.Rate
	// Now is the instant the statuses are read at.
	Now time.Time
}

// Details returns the details of the card c.
//
// source: app/serializers/v2/rate_card_serializer.rb:5-31
func (s *Service) Details(ctx context.Context, c *catalog.RateCard) (RateCardDetails, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (RateCardDetails, error) {
		var d RateCardDetails
		var err error
		if d.Product, err = getProduct(ctx, r, c.OrganizationID, c.ProductID); err != nil {
			return d, fmt.Errorf("load product: %w", err)
		}
		if !c.ProductFilterID.IsZero() {
			if d.Filter, err = getFilter(ctx, r, c.OrganizationID, c.ProductFilterID); err != nil {
				return d, fmt.Errorf("load product filter: %w", err)
			}
		}
		if d.Rates, err = r.Rates.ListByRateCard(ctx, c.OrganizationID, c.ID); err != nil {
			return d, fmt.Errorf("list rates: %w", err)
		}
		d.Now = s.d.Now()
		d.RatesCount = len(d.Rates)
		d.ActiveRate = catalog.ActiveRate(d.Rates, d.Now)
		return d, nil
	})
}

// loadCard loads the card id with its product, filter and metric.
func loadCard(ctx context.Context, r port.Repos, org core.OrganizationID, id core.RateCardID) (card, error) {
	c, err := getRateCard(ctx, r, org, id)
	if err != nil {
		return card{}, notFound(err, ResourceRateCard)
	}
	return cardOf(ctx, r, c)
}

// cardOf completes c with its product, filter and metric.
func cardOf(ctx context.Context, r port.Repos, c *catalog.RateCard) (card, error) {
	k := card{c: c}
	var err error
	if k.p, err = getProduct(ctx, r, c.OrganizationID, c.ProductID); err != nil {
		return k, fmt.Errorf("load product: %w", err)
	}
	if !c.ProductFilterID.IsZero() {
		if k.f, err = getFilter(ctx, r, c.OrganizationID, c.ProductFilterID); err != nil {
			return k, fmt.Errorf("load product filter: %w", err)
		}
	}
	if k.m, err = productMetric(ctx, r, k.p); err != nil {
		return k, err
	}
	return k, nil
}

// validateRateCard checks the card as lago's model does, the code's
// uniqueness included.
//
// source: app/models/rate_card.rb:37-104
func validateRateCard(ctx context.Context, r port.Repos, k card) error {
	var v core.Validator
	v.Merge("", catalog.ValidateRateCard(k.c, k.p, k.f, k.m))
	if k.c.Code != "" {
		other, err := r.RateCards.GetByCode(ctx, k.c.OrganizationID, k.c.Code)
		if err := uniqueCode(&v, err, other != nil && other.ID != k.c.ID); err != nil {
			return err
		}
	}
	return v.Err()
}

// prefixed moves the field errors of err under prefix (lago's
// transform_keys of nested services); other errors are returned as they
// are.
func prefixed(prefix string, err error) error {
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	var v core.Validator
	v.Merge(prefix, err)
	return v.Err()
}
