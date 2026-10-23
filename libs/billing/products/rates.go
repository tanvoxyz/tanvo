package products

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// RateInput is a rate to append to a rate card.
type RateInput struct {
	// Code is unique among the card's rates.
	Code string
	// EffectiveFrom is when the rate takes effect; not before today in the
	// organization's time zone, and at midnight UTC on an arrears card.
	EffectiveFrom time.Time
	// RateModel is the pricing model.
	RateModel catalog.ChargeModel
	// Properties are the prices; tiers name only their upper bound.
	Properties catalog.Properties
	// MinAmount is the minimum per billing interval in minor units; 0 when
	// nil.
	MinAmount *int64
	// BillingIntervalCount is the number of interval units; 1 when nil.
	BillingIntervalCount *int
	// BillingIntervalUnit is the unit of the interval.
	BillingIntervalUnit catalog.IntervalUnit
	// PricingUnitConversionRate converts the card's pricing unit.
	PricingUnitConversionRate *money.Decimal
}

// CreateRate appends a rate to the card ref references. A missing card is
// core.NotFound("rate_card"); an instant before today is effective_from
// must_not_be_before_today.
//
// source: app/services/rate_card_rates/create_service.rb:20-48
func (s *Service) CreateRate(ctx context.Context, org core.OrganizationID, cardRef refs.Ref, in RateInput) (*catalog.Rate, error) {
	var out *catalog.Rate
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := rateCardsOf(r, org).One(ctx, cardRef)
		if err != nil {
			return err
		}
		k, err := cardOf(ctx, r, c)
		if err != nil {
			return err
		}
		loc, err := orgLocation(ctx, r, org)
		if err != nil {
			return err
		}
		out, err = s.createRate(ctx, r, k, in, loc)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create rate: %w", err)
	}
	return out, nil
}

// createRate appends the rate in to the card k.
//
// source: app/services/rate_card_rates/create_service.rb:23-44
func (s *Service) createRate(ctx context.Context, r port.Repos, k card, in RateInput, loc *time.Location) (*catalog.Rate, error) {
	now := s.d.Now()
	if !in.EffectiveFrom.IsZero() && beforeToday(in.EffectiveFrom, now, loc) {
		return nil, fieldError("effective_from", catalog.CodeMustNotBeBeforeToday)
	}
	props, err := NormalizeRanges(in.Properties)
	if err != nil {
		return nil, err
	}
	rate := &catalog.Rate{
		ID:                        core.NewID[core.RateCardRateID](s.d.IDs),
		OrganizationID:            k.c.OrganizationID,
		RateCardID:                k.c.ID,
		Code:                      in.Code,
		EffectiveFrom:             in.EffectiveFrom,
		RateModel:                 in.RateModel,
		Properties:                props,
		BillingIntervalCount:      1,
		BillingIntervalUnit:       in.BillingIntervalUnit,
		PricingUnitConversionRate: in.PricingUnitConversionRate,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}
	if in.MinAmount != nil {
		rate.MinAmount = *in.MinAmount
	}
	if in.BillingIntervalCount != nil {
		rate.BillingIntervalCount = *in.BillingIntervalCount
	}
	if err := s.saveRate(ctx, r, k, rate, true, true); err != nil {
		return nil, err
	}
	return rate, nil
}

// RateUpdate is a partial update of a rate: only the set fields change.
type RateUpdate struct {
	// Code is the unique code; locked once a plan applies the card.
	Code core.Optional[string]
	// EffectiveFrom is frozen once the rate is active.
	EffectiveFrom core.Optional[time.Time]
	// RateModel is frozen once the rate is active.
	RateModel core.Optional[catalog.ChargeModel]
	// Properties are the prices.
	Properties core.Optional[catalog.Properties]
	// MinAmount is frozen once the rate is active.
	MinAmount core.Optional[int64]
	// BillingIntervalCount is frozen once the rate is active.
	BillingIntervalCount core.Optional[int]
	// BillingIntervalUnit is frozen once the rate is active.
	BillingIntervalUnit core.Optional[catalog.IntervalUnit]
	// PricingUnitConversionRate converts the card's pricing unit.
	PricingUnitConversionRate core.Optional[*money.Decimal]
}

// UpdateRate changes the rate ref of the card cardRef. On a card a
// contract applies, only pending rates change (rate_card
// attached_to_subscriptions); a terminated rate is status
// terminated_rate_not_editable; on an active rate, setting the instant,
// model, minimum or interval is <field> not_editable_on_active_rate.
//
// source: app/services/rate_card_rates/update_service.rb:22-63
func (s *Service) UpdateRate(ctx context.Context, org core.OrganizationID, cardRef, ref refs.Ref, in RateUpdate) (*catalog.Rate, error) {
	var out *catalog.Rate
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := rateCardsOf(r, org).One(ctx, cardRef)
		if err != nil {
			return err
		}
		rates, err := r.Rates.ListByRateCard(ctx, org, c.ID)
		if err != nil {
			return fmt.Errorf("list rates: %w", err)
		}
		rate, err := rateOf(rates, ref)
		if err != nil {
			return err
		}
		now := s.d.Now()
		status := catalog.RateStatusAt(rate, rates, now)
		if status != catalog.RateStatusPending {
			subscribed, err := attachedToContracts(ctx, r, org, catalog.PlanRateCardQuery{RateCardID: c.ID})
			if err != nil {
				return err
			}
			if subscribed {
				return fieldError("rate_card", catalog.CodeAttachedToSubscriptions)
			}
		}
		if status == catalog.RateStatusTerminated {
			return fieldError("status", catalog.CodeTerminatedRateNotEditable)
		}
		if status == catalog.RateStatusActive {
			frozen := []struct {
				field string
				set   bool
			}{
				{"effective_from", in.EffectiveFrom.Set},
				{"rate_model", in.RateModel.Set},
				{"min_amount", in.MinAmount.Set},
				{"billing_interval.count", in.BillingIntervalCount.Set},
				{"billing_interval.unit", in.BillingIntervalUnit.Set},
			}
			for _, f := range frozen {
				if f.set {
					return fieldError(f.field, catalog.CodeNotEditableOnActiveRate)
				}
			}
		}
		if in.Code.Set && strip(in.Code.Value) != rate.Code {
			ok, err := attached(ctx, r, org, catalog.PlanRateCardQuery{RateCardID: c.ID})
			if err != nil {
				return err
			}
			if ok {
				return fieldError("code", catalog.CodeAttachedToPlanOrSubscription)
			}
		}
		if in.EffectiveFrom.Set && !in.EffectiveFrom.Value.IsZero() {
			loc, err := orgLocation(ctx, r, org)
			if err != nil {
				return err
			}
			if beforeToday(in.EffectiveFrom.Value, now, loc) {
				return fieldError("effective_from", catalog.CodeMustNotBeBeforeToday)
			}
		}
		if in.Properties.Set {
			if in.Properties.Value, err = NormalizeRanges(in.Properties.Value); err != nil {
				return err
			}
		}
		k, err := cardOf(ctx, r, c)
		if err != nil {
			return err
		}
		before := rate.EffectiveFrom
		if in.Code.Set {
			rate.Code = strip(in.Code.Value)
		}
		in.EffectiveFrom.Apply(&rate.EffectiveFrom)
		in.RateModel.Apply(&rate.RateModel)
		in.Properties.Apply(&rate.Properties)
		in.MinAmount.Apply(&rate.MinAmount)
		in.BillingIntervalCount.Apply(&rate.BillingIntervalCount)
		in.BillingIntervalUnit.Apply(&rate.BillingIntervalUnit)
		in.PricingUnitConversionRate.Apply(&rate.PricingUnitConversionRate)
		rate.UpdatedAt = now
		placed := !catalog.NormalizeEffectiveFrom(rate.EffectiveFrom, c.BillingTiming).Equal(before)
		if err := s.saveRate(ctx, r, k, rate, placed, false); err != nil {
			return err
		}
		out = rate
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("update rate: %w", err)
	}
	return out, nil
}

// DeleteRate deletes the rate ref of the card cardRef. Only pending rates
// are deleted (status only_pending_rates_can_be_deleted): active and
// terminated ones are kept for audit.
//
// source: app/services/rate_card_rates/destroy_service.rb:17-30
func (s *Service) DeleteRate(ctx context.Context, org core.OrganizationID, cardRef, ref refs.Ref) (*catalog.Rate, error) {
	var out *catalog.Rate
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := rateCardsOf(r, org).One(ctx, cardRef)
		if err != nil {
			return err
		}
		rates, err := r.Rates.ListByRateCard(ctx, org, c.ID)
		if err != nil {
			return fmt.Errorf("list rates: %w", err)
		}
		rate, err := rateOf(rates, ref)
		if err != nil {
			return err
		}
		if catalog.RateStatusAt(rate, rates, s.d.Now()) != catalog.RateStatusPending {
			return fieldError("status", catalog.CodeOnlyPendingRatesCanBeDeleted)
		}
		now := s.d.Now()
		if err := r.Rates.Discard(ctx, org, rate.ID, now); err != nil {
			return fmt.Errorf("discard: %w", err)
		}
		rate.DeletedAt = &now
		out = rate
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("delete rate: %w", err)
	}
	return out, nil
}

// CardRates are the rates of a card with the instant their statuses are
// read at.
type CardRates struct {
	// Card is the rate card.
	Card *catalog.RateCard
	// Rates are the card's rates, latest effective first.
	Rates []*catalog.Rate
	// Now is the instant of the statuses.
	Now time.Time
}

// Status returns the status of the rate r of the card.
func (c CardRates) Status(r *catalog.Rate) catalog.RateStatus {
	return catalog.RateStatusAt(r, c.Rates, c.Now)
}

// Rates returns every rate of the card ref references, latest effective
// first: a card's timeline is returned whole.
//
// source: app/controllers/api/v2/rate_cards/rates_controller.rb:12-31, app/queries/rate_card_rates_query.rb
func (s *Service) Rates(ctx context.Context, org core.OrganizationID, cardRef refs.Ref) (CardRates, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (CardRates, error) {
		c, err := rateCardsOf(r, org).One(ctx, cardRef)
		if err != nil {
			return CardRates{}, err
		}
		rates, err := r.Rates.ListByRateCard(ctx, org, c.ID)
		if err != nil {
			return CardRates{}, fmt.Errorf("list rates: %w", err)
		}
		return CardRates{Card: c, Rates: rates, Now: s.d.Now()}, nil
	})
}

// Rate returns the rate ref of the card cardRef with its card's rates. A
// missing rate is core.NotFound("rate_card_rate").
func (s *Service) Rate(ctx context.Context, org core.OrganizationID, cardRef, ref refs.Ref) (*catalog.Rate, CardRates, error) {
	rates, err := s.Rates(ctx, org, cardRef)
	if err != nil {
		return nil, rates, err
	}
	rate, err := rateOf(rates.Rates, ref)
	return rate, rates, err
}

// rateOf returns the rate ref references among rates.
func rateOf(rates []*catalog.Rate, ref refs.Ref) (*catalog.Rate, error) {
	i := slices.IndexFunc(rates, func(r *catalog.Rate) bool {
		if ref.Code != "" {
			return r.Code == ref.Code
		}
		return r.ID.UUID() == ref.ID
	})
	if i < 0 || ref.IsZero() {
		return nil, core.NotFound(ResourceRate)
	}
	return rates[i], nil
}

// saveRate normalizes, validates and stores the rate of card k: the
// model's rules, the code's uniqueness, then the properties with the
// model's validator (only on a valid, compatible model).
//
// source: app/models/rate_card_rate.rb:45-58, :111-178
func (s *Service) saveRate(ctx context.Context, r port.Repos, k card, rate *catalog.Rate, placed, create bool) error {
	if !rate.EffectiveFrom.IsZero() {
		rate.EffectiveFrom = catalog.NormalizeEffectiveFrom(rate.EffectiveFrom, k.c.BillingTiming)
	}
	others, err := r.Rates.ListByRateCard(ctx, rate.OrganizationID, k.c.ID)
	if err != nil {
		return fmt.Errorf("list rates: %w", err)
	}
	var v core.Validator
	verr := catalog.ValidateRate(rate, k.c, k.p, k.m, others, placed, s.d.Now())
	v.Merge("", verr)
	if rate.Code != "" && slices.ContainsFunc(others, func(o *catalog.Rate) bool { return o.ID != rate.ID && o.Code == rate.Code }) {
		v.Add("code", core.CodeAlreadyExists)
	}
	if !hasField(verr, "rate_model") {
		rateProperties(&v, "properties", rate.RateModel, rate.Properties, k.m)
	}
	if err := v.Err(); err != nil {
		return err
	}
	if create {
		err = r.Rates.Create(ctx, rate)
	} else {
		err = r.Rates.Update(ctx, rate)
	}
	return conflict(err, "code")
}

// rateProperties adds the errors of the model's validator on props to v
// under field, once per code (lago RateCardRate#validate_properties).
//
// source: app/models/rate_card_rate.rb:162-172
func rateProperties(v *core.Validator, field string, model catalog.ChargeModel, props catalog.Properties, m *catalog.BillableMetric) {
	if !model.Valid() {
		return
	}
	opts := pricing.ValidateOptions{
		LatestAggregation: m != nil && m.AggregationType == catalog.AggregationTypeLatest,
		ProductCatalog:    true,
	}
	errs, err := pricing.ValidateJSON(pricing.ChargeModel(model), props.Raw(), opts)
	if err != nil {
		v.Add(field, core.CodeInvalid)
		return
	}
	var seen []string
	for _, e := range errs {
		if !slices.Contains(seen, e.Code) {
			seen = append(seen, e.Code)
			v.Add(field, e.Code)
		}
	}
}

// hasField reports whether the validation error err has an error on
// field.
func hasField(err error, field string) bool {
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	return slices.ContainsFunc(ve.Errors, func(e core.FieldError) bool { return e.Field == field })
}
