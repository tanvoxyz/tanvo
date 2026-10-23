package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/money"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1/catalogv1connect"
)

// RateCards serves tanvo.catalog.v1.RateCardService over the billing
// library's product catalog service (lago Api::V2::RateCardsController and
// Api::V2::RateCards::RatesController).
type RateCards struct {
	catalogv1connect.UnimplementedRateCardServiceHandler

	svc *products.Service
}

var _ catalogv1connect.RateCardServiceHandler = (*RateCards)(nil)

// NewRateCards returns the service over svc.
func NewRateCards(svc *products.Service) *RateCards { return &RateCards{svc: svc} }

var (
	billingTimings = NewEnum(map[catalog.BillingTiming]catalogv1.BillingTiming{
		catalog.BillingTimingArrears: catalogv1.BillingTiming_BILLING_TIMING_ARREARS,
		catalog.BillingTimingAdvance: catalogv1.BillingTiming_BILLING_TIMING_ADVANCE,
	})
	intervalUnits = NewEnum(map[catalog.IntervalUnit]catalogv1.IntervalUnit{
		catalog.IntervalUnitDay:   catalogv1.IntervalUnit_INTERVAL_UNIT_DAY,
		catalog.IntervalUnitWeek:  catalogv1.IntervalUnit_INTERVAL_UNIT_WEEK,
		catalog.IntervalUnitMonth: catalogv1.IntervalUnit_INTERVAL_UNIT_MONTH,
		catalog.IntervalUnitYear:  catalogv1.IntervalUnit_INTERVAL_UNIT_YEAR,
	})
	rateStatuses = NewEnum(map[catalog.RateStatus]catalogv1.RateStatus{
		catalog.RateStatusPending:    catalogv1.RateStatus_RATE_STATUS_PENDING,
		catalog.RateStatusActive:     catalogv1.RateStatus_RATE_STATUS_ACTIVE,
		catalog.RateStatusTerminated: catalogv1.RateStatus_RATE_STATUS_TERMINATED,
	})
)

// The update_mask paths of the update RPCs.
var (
	rateCardUpdateFields = []string{
		"code", "name", "description", "currency", "billing_timing", "proration",
		"display_on_invoice", "regroup_paid_fees", "pricing_unit_id", "tax_ids",
	}
	rateUpdateFields = []string{
		"code", "effective_from", "rate_model", "properties", "min_amount",
		"billing_interval.count", "billing_interval.unit", "pricing_unit_conversion_rate",
	}
)

// CreateRateCard implements RateCardService.CreateRateCard: the card with
// its taxes and first rates.
func (s *RateCards) CreateRateCard(ctx context.Context, req *catalogv1.CreateRateCardRequest) (*catalogv1.CreateRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetRateCard()
	in := products.RateCardInput{
		Code:             b.GetCode(),
		Name:             b.GetName(),
		Description:      Opt(b.HasDescription(), b.GetDescription()),
		BillingTiming:    billingTimings.Core(b.GetBillingTiming()),
		Proration:        Opt(b.HasProration(), b.GetProration()),
		DisplayOnInvoice: Opt(b.HasDisplayOnInvoice(), b.GetDisplayOnInvoice()),
		RegroupPaidFees:  chargeRegroups.Core(b.GetRegroupPaidFees()),
	}
	if in.Currency, err = rateCardCurrency("rate_card.currency", b.GetCurrency()); err != nil {
		return nil, err
	}
	if in.Product, err = CatalogRef("rate_card.product_id", b.GetProductId()); err != nil {
		return nil, err
	}
	if in.Filter, err = CatalogRef("rate_card.product_filter_id", b.GetProductFilterId()); err != nil {
		return nil, err
	}
	if in.PricingUnit, err = CatalogRef("rate_card.pricing_unit_id", b.GetPricingUnitId()); err != nil {
		return nil, err
	}
	if in.Taxes, err = CatalogRefs("rate_card.tax_ids", b.GetTaxIds()); err != nil {
		return nil, err
	}
	for i, r := range b.GetRates() {
		ri, err := rateInput(fmt.Sprintf("rate_card.rates[%d]", i), r, in.Currency)
		if err != nil {
			return nil, err
		}
		in.Rates = append(in.Rates, ri)
	}
	c, err := s.svc.CreateRateCard(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, catalogError(ctx, "rate_card.", "invalid rate card", err)
	}
	return cardResponse(ctx, s, c, func(rc *catalogv1.RateCard) *catalogv1.CreateRateCardResponse {
		return catalogv1.CreateRateCardResponse_builder{RateCard: rc}.Build()
	})
}

// GetRateCard implements RateCardService.GetRateCard.
func (s *RateCards) GetRateCard(ctx context.Context, req *catalogv1.GetRateCardRequest) (*catalogv1.GetRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.card(ctx, p.OrganizationID, "rate_card_id", req.GetRateCardId())
	if err != nil {
		return nil, err
	}
	return cardResponse(ctx, s, c, func(rc *catalogv1.RateCard) *catalogv1.GetRateCardResponse {
		return catalogv1.GetRateCardResponse_builder{RateCard: rc}.Build()
	})
}

// ListRateCards implements RateCardService.ListRateCards: most recent
// first, searched by name or code. The filter takes product_id,
// product_filter_id and product_category_id (each an OR of several) and
// without_product_category.
func (s *RateCards) ListRateCards(ctx context.Context, req *catalogv1.ListRateCardsRequest) (*catalogv1.ListRateCardsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	q, err := rateCardList(req.GetFilter())
	if err != nil {
		return nil, err
	}
	q.Search = req.GetSearch()
	page, err := s.svc.ListRateCards(ctx, p.OrganizationID, q, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*catalogv1.RateCard, len(page.Items))
	for i, c := range page.Items {
		if out[i], err = s.cardProto(ctx, c); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return catalogv1.ListRateCardsResponse_builder{RateCards: out, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdateRateCard implements RateCardService.UpdateRateCard. rate_card.id
// is an ID or code:<code>; an empty pricing_unit_id in the mask removes
// the pricing unit, an empty tax_ids in the mask the taxes.
func (s *RateCards) UpdateRateCard(ctx context.Context, req *catalogv1.UpdateRateCardRequest) (*catalogv1.UpdateRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetRateCard()
	paths, err := UpdatePaths(req.GetUpdateMask(), rateCardUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "currency":
			return b.HasCurrency()
		case "billing_timing":
			return b.HasBillingTiming()
		case "proration":
			return b.HasProration()
		case "display_on_invoice":
			return b.HasDisplayOnInvoice()
		case "regroup_paid_fees":
			return b.HasRegroupPaidFees()
		case "pricing_unit_id":
			return b.HasPricingUnitId()
		case "tax_ids":
			return len(b.GetTaxIds()) > 0
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.card(ctx, p.OrganizationID, "rate_card.id", b.GetId())
	if err != nil {
		return nil, err
	}
	var in products.RateCardUpdate
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "currency":
			c, err := rateCardCurrency("rate_card.currency", b.GetCurrency())
			if err != nil {
				return nil, err
			}
			in.Currency = core.Some(c)
		case "billing_timing":
			in.BillingTiming = core.Some(billingTimings.Core(b.GetBillingTiming()))
		case "proration":
			in.Proration = core.Some(b.GetProration())
		case "display_on_invoice":
			in.DisplayOnInvoice = core.Some(b.GetDisplayOnInvoice())
		case "regroup_paid_fees":
			in.RegroupPaidFees = core.Some(chargeRegroups.Core(b.GetRegroupPaidFees()))
		case "pricing_unit_id":
			unit, err := CatalogRef("rate_card.pricing_unit_id", b.GetPricingUnitId())
			if err != nil {
				return nil, err
			}
			in.PricingUnit = core.Some(unit)
		case "tax_ids":
			taxes, err := CatalogRefs("rate_card.tax_ids", b.GetTaxIds())
			if err != nil {
				return nil, err
			}
			if taxes == nil {
				taxes = []refs.Ref{}
			}
			in.Taxes = core.Some(taxes)
		}
	}
	c, err := s.svc.UpdateRateCard(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, catalogError(ctx, "rate_card.", "invalid rate card", err)
	}
	return cardResponse(ctx, s, c, func(rc *catalogv1.RateCard) *catalogv1.UpdateRateCardResponse {
		return catalogv1.UpdateRateCardResponse_builder{RateCard: rc}.Build()
	})
}

// DeleteRateCard implements RateCardService.DeleteRateCard; the card's
// rates are deleted with it.
func (s *RateCards) DeleteRateCard(ctx context.Context, req *catalogv1.DeleteRateCardRequest) (*catalogv1.DeleteRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.card(ctx, p.OrganizationID, "rate_card_id", req.GetRateCardId())
	if err != nil {
		return nil, err
	}
	shown, err := s.cardProto(ctx, cur)
	if err != nil {
		return nil, Error(ctx, err)
	}
	if _, err := s.svc.DeleteRateCard(ctx, p.OrganizationID, cur.ID); err != nil {
		return nil, catalogError(ctx, "rate_card.", "rate card not deletable", err)
	}
	return catalogv1.DeleteRateCardResponse_builder{RateCard: shown}.Build(), nil
}

// CreateRate implements RateCardService.CreateRate: a rate appended to the
// card's timeline.
func (s *RateCards) CreateRate(ctx context.Context, req *catalogv1.CreateRateRequest) (*catalogv1.CreateRateResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.card(ctx, p.OrganizationID, "rate_card_id", req.GetRateCardId())
	if err != nil {
		return nil, err
	}
	in, err := rateInput("rate", req.GetRate(), c.Currency)
	if err != nil {
		return nil, err
	}
	r, err := s.svc.CreateRate(ctx, p.OrganizationID, refs.ByID(c.ID), in)
	if err != nil {
		return nil, catalogError(ctx, "rate.", "invalid rate", err)
	}
	out, err := s.rateProto(ctx, p.OrganizationID, c, r)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.CreateRateResponse_builder{Rate: out}.Build(), nil
}

// GetRate implements RateCardService.GetRate; rate_id is an ID or
// code:<code> within the card.
func (s *RateCards) GetRate(ctx context.Context, req *catalogv1.GetRateRequest) (*catalogv1.GetRateResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	r, cr, err := s.rate(ctx, p.OrganizationID, req.GetRateCardId(), "rate_id", req.GetRateId())
	if err != nil {
		return nil, err
	}
	return catalogv1.GetRateResponse_builder{Rate: rateProtoOf(r, cr.Card.Currency, cr.Status(r))}.Build(), nil
}

// ListRates implements RateCardService.ListRates: the card's timeline,
// latest effective first.
func (s *RateCards) ListRates(ctx context.Context, req *catalogv1.ListRatesRequest) (*catalogv1.ListRatesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := CatalogRef("rate_card_id", req.GetRateCardId())
	if err != nil {
		return nil, err
	}
	cr, err := s.svc.Rates(ctx, p.OrganizationID, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	page, err := slicePage(cr.Rates, Page(req.GetPageSize(), req.GetPageToken()), func(r *catalog.Rate) string { return r.ID.String() })
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListRatesResponse_builder{
		Rates:         Items(page, func(r *catalog.Rate) *catalogv1.Rate { return rateProtoOf(r, cr.Card.Currency, cr.Status(r)) }),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateRate implements RateCardService.UpdateRate. rate.id is an ID or
// code:<code> within the card; billing_interval.count and
// billing_interval.unit are separate update_mask paths.
func (s *RateCards) UpdateRate(ctx context.Context, req *catalogv1.UpdateRateRequest) (*catalogv1.UpdateRateResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetRate()
	paths, err := UpdatePaths(req.GetUpdateMask(), rateUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "effective_from":
			return b.HasEffectiveFrom()
		case "rate_model":
			return b.HasRateModel()
		case "properties":
			return b.HasProperties()
		case "min_amount":
			return b.HasMinAmount()
		case "billing_interval.count":
			return b.GetBillingInterval().HasCount()
		case "billing_interval.unit":
			return b.GetBillingInterval().HasUnit()
		case "pricing_unit_conversion_rate":
			return b.HasPricingUnitConversionRate()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, cr, err := s.rate(ctx, p.OrganizationID, req.GetRateCardId(), "rate.id", b.GetId())
	if err != nil {
		return nil, err
	}
	c := cr.Card
	var in products.RateUpdate
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "effective_from":
			in.EffectiveFrom = core.Some(Time(b.GetEffectiveFrom()))
		case "rate_model":
			in.RateModel = core.Some(chargeModels.Core(b.GetRateModel()))
		case "properties":
			props, err := catalogProperties("rate.properties", b.GetProperties())
			if err != nil {
				return nil, err
			}
			in.Properties = core.Some(props)
		case "min_amount":
			minor, err := minorAmount("rate.min_amount", orZero(b.GetMinAmount()), c.Currency)
			if err != nil {
				return nil, err
			}
			in.MinAmount = core.Some(minor)
		case "billing_interval.count":
			in.BillingIntervalCount = core.Some(int(b.GetBillingInterval().GetCount()))
		case "billing_interval.unit":
			in.BillingIntervalUnit = core.Some(intervalUnits.Core(b.GetBillingInterval().GetUnit()))
		case "pricing_unit_conversion_rate":
			rate, err := decimalOpt("rate.pricing_unit_conversion_rate", b.HasPricingUnitConversionRate(), b.GetPricingUnitConversionRate())
			if err != nil {
				return nil, err
			}
			in.PricingUnitConversionRate = core.Some(rate)
		}
	}
	r, err := s.svc.UpdateRate(ctx, p.OrganizationID, refs.ByID(c.ID), refs.ByID(cur.ID), in)
	if err != nil {
		return nil, catalogError(ctx, "rate.", "invalid rate", err)
	}
	out, err := s.rateProto(ctx, p.OrganizationID, c, r)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.UpdateRateResponse_builder{Rate: out}.Build(), nil
}

// DeleteRate implements RateCardService.DeleteRate; only pending rates
// are deleted.
func (s *RateCards) DeleteRate(ctx context.Context, req *catalogv1.DeleteRateRequest) (*catalogv1.DeleteRateResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, cr, err := s.rate(ctx, p.OrganizationID, req.GetRateCardId(), "rate_id", req.GetRateId())
	if err != nil {
		return nil, err
	}
	shown := rateProtoOf(cur, cr.Card.Currency, cr.Status(cur))
	if _, err := s.svc.DeleteRate(ctx, p.OrganizationID, refs.ByID(cr.Card.ID), refs.ByID(cur.ID)); err != nil {
		return nil, catalogError(ctx, "rate.", "rate not deletable", err)
	}
	return catalogv1.DeleteRateResponse_builder{Rate: shown}.Build(), nil
}

// card returns the rate card the reference ref of field names.
func (s *RateCards) card(ctx context.Context, org core.OrganizationID, field, ref string) (*catalog.RateCard, error) {
	r, err := CatalogRef(field, ref)
	if err != nil {
		return nil, err
	}
	c, err := s.svc.RateCard(ctx, org, r)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return c, nil
}

// rate returns the rate ref of field of the card cardRef with the card's
// rates.
func (s *RateCards) rate(ctx context.Context, org core.OrganizationID, cardRef, field, ref string) (*catalog.Rate, products.CardRates, error) {
	cr, err := CatalogRef("rate_card_id", cardRef)
	if err != nil {
		return nil, products.CardRates{}, err
	}
	rr, err := CatalogRef(field, ref)
	if err != nil {
		return nil, products.CardRates{}, err
	}
	r, rates, err := s.svc.Rate(ctx, org, cr, rr)
	if err != nil {
		return nil, products.CardRates{}, Error(ctx, err)
	}
	return r, rates, nil
}

// rateProto converts the rate r of the card c with its status now.
func (s *RateCards) rateProto(ctx context.Context, org core.OrganizationID, c *catalog.RateCard, r *catalog.Rate) (*catalogv1.Rate, error) {
	cr, err := s.svc.Rates(ctx, org, refs.ByID(c.ID))
	if err != nil {
		return nil, err
	}
	return rateProtoOf(r, c.Currency, cr.Status(r)), nil
}

func (s *RateCards) cardProto(ctx context.Context, c *catalog.RateCard) (*catalogv1.RateCard, error) {
	d, err := s.svc.Details(ctx, c)
	if err != nil {
		return nil, err
	}
	return rateCardProto(c, d), nil
}

// cardResponse builds a response holding the converted card c.
func cardResponse[R any](ctx context.Context, s *RateCards, c *catalog.RateCard, build func(*catalogv1.RateCard) R) (R, error) {
	rc, err := s.cardProto(ctx, c)
	if err != nil {
		var zero R
		return zero, Error(ctx, err)
	}
	return build(rc), nil
}

// rateCardList parses the filter of ListRateCards.
func rateCardList(s string) (products.RateCardList, error) {
	var q products.RateCardList
	e, err := ParseFilter(s)
	if err != nil {
		return q, err
	}
	eq, err := Equalities(e, "product_id", "product_filter_id", "product_category_id", "without_product_category")
	if err != nil {
		return q, err
	}
	if q.Products, err = filterRefs(eq["product_id"]); err != nil {
		return q, err
	}
	for _, v := range eq["product_filter_id"] {
		id, err := core.ParseID[core.ProductFilterID](v)
		if err != nil {
			return q, filterError(fmt.Sprintf("field %q takes product filter IDs", "product_filter_id"))
		}
		q.Filters = append(q.Filters, id)
	}
	if q.Categories, err = filterRefs(eq["product_category_id"]); err != nil {
		return q, err
	}
	without, err := FilterBool("without_product_category", eq["without_product_category"])
	if err != nil {
		return q, err
	}
	q.WithoutCategory = without != nil && *without
	return q, nil
}

// rateCardCurrency parses the currency of a rate card.
func rateCardCurrency(field, s string) (money.Currency, error) {
	c, err := money.LookupCurrency(s)
	if err != nil {
		return money.Currency{}, InvalidArgument("invalid currency", []FieldViolation{{Field: field, Reason: core.CodeInvalidCurrency}})
	}
	return c, nil
}

// rateInput converts the rate b of field, its amounts in currency c.
func rateInput(field string, b *catalogv1.Rate, c money.Currency) (products.RateInput, error) {
	in := products.RateInput{
		Code:                 b.GetCode(),
		EffectiveFrom:        Time(b.GetEffectiveFrom()),
		RateModel:            chargeModels.Core(b.GetRateModel()),
		BillingIntervalCount: OptInt(b.GetBillingInterval().HasCount(), b.GetBillingInterval().GetCount()),
		BillingIntervalUnit:  intervalUnits.Core(b.GetBillingInterval().GetUnit()),
	}
	var err error
	if b.HasProperties() {
		if in.Properties, err = catalogProperties(field+".properties", b.GetProperties()); err != nil {
			return in, err
		}
	}
	if b.HasMinAmount() {
		minor, err := minorAmount(field+".min_amount", b.GetMinAmount(), c)
		if err != nil {
			return in, err
		}
		in.MinAmount = &minor
	}
	if in.PricingUnitConversionRate, err = decimalOpt(field+".pricing_unit_conversion_rate", b.HasPricingUnitConversionRate(), b.GetPricingUnitConversionRate()); err != nil {
		return in, err
	}
	return in, nil
}

// catalogProperties converts the prices of a rate or rate override: the
// charge properties, whose tiers name only their upper bound (an unset
// from_value is left out; a set one is refused by the service).
func catalogProperties(field string, pb *catalogv1.ChargeProperties) (catalog.Properties, error) {
	props, err := chargePropertiesInput(field, pb)
	if err != nil {
		return catalog.Properties{}, err
	}
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(props.Raw()))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		return catalog.Properties{}, fmt.Errorf("%s: %w", field, err)
	}
	for _, key := range []string{"graduated_ranges", "graduated_percentage_ranges", "volume_ranges"} {
		tiers, _ := m[key].([]any)
		for _, t := range tiers {
			if o, ok := t.(map[string]any); ok && o["from_value"] == nil {
				delete(o, "from_value")
			}
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return catalog.Properties{}, fmt.Errorf("%s: %w", field, err)
	}
	return catalog.NewProperties(raw)
}

// catalogPropertiesProto converts stored rate prices as the catalog shows
// them: tiers without their lower bound.
func catalogPropertiesProto(model catalog.ChargeModel, p catalog.Properties) *catalogv1.ChargeProperties {
	if shown, err := products.PresentRanges(p); err == nil {
		p = shown
	}
	return chargePropertiesProto(model, p)
}

// decimalOpt parses the decimal s of field when has.
func decimalOpt(field string, has bool, s string) (*money.Decimal, error) {
	if !has {
		return nil, nil
	}
	d, err := ParseDecimal(field, s)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// orZero is s, or "0" when s is empty.
func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// minorString is the decimal text of minor units of c.
func minorString(minor int64, c money.Currency) *string {
	return new(c.FromMinor(minor).StringFixed(c.Exponent()))
}

// slicePage returns the page of items after the item whose key is the
// page token.
func slicePage[T any](items []T, page core.PageRequest, key func(T) string) (core.Page[T], error) {
	start := 0
	if page.Token != "" {
		id, err := core.ParsePageToken(page.Token)
		if err != nil {
			return core.Page[T]{}, err
		}
		i := slices.IndexFunc(items, func(it T) bool { return key(it) == id.String() })
		if i < 0 {
			return core.Page[T]{}, fmt.Errorf("%w: page token", core.ErrInvalidValue)
		}
		start = i + 1
	}
	end := min(start+page.Limit(), len(items))
	out := core.Page[T]{Items: items[start:end]}
	if end < len(items) {
		out.NextToken = key(items[end-1])
	}
	return out, nil
}

func rateProtoOf(r *catalog.Rate, c money.Currency, status catalog.RateStatus) *catalogv1.Rate {
	return catalogv1.Rate_builder{
		Id:            new(r.ID.String()),
		Code:          new(r.Code),
		EffectiveFrom: Timestamp(r.EffectiveFrom),
		RateModel:     new(chargeModels.Proto(r.RateModel)),
		Properties:    catalogPropertiesProto(r.RateModel, r.Properties),
		MinAmount:     minorString(r.MinAmount, c),
		BillingInterval: catalogv1.BillingInterval_builder{
			Count: new(int32(r.BillingIntervalCount)), //nolint:gosec // validated to 1..1000
			Unit:  new(intervalUnits.Proto(r.BillingIntervalUnit)),
		}.Build(),
		PricingUnitConversionRate: DecimalPtr(r.PricingUnitConversionRate),
		CreatedAt:                 Timestamp(r.CreatedAt),
		UpdatedAt:                 Timestamp(r.UpdatedAt),
		Status:                    new(rateStatuses.Proto(status)),
	}.Build()
}

func rateCardProto(c *catalog.RateCard, d products.RateCardDetails) *catalogv1.RateCard {
	taxIDs := make([]string, len(c.TaxIDs))
	for i, id := range c.TaxIDs {
		taxIDs[i] = id.String()
	}
	var active *catalogv1.Rate
	if d.ActiveRate != nil {
		active = rateProtoOf(d.ActiveRate, c.Currency, catalog.RateStatusAt(d.ActiveRate, d.Rates, d.Now))
	}
	b := catalogv1.RateCard_builder{
		Id:               new(c.ID.String()),
		Code:             new(c.Code),
		Name:             new(c.Name),
		Description:      c.Description,
		ProductId:        new(c.ProductID.String()),
		ProductFilterId:  StringPtr(c.ProductFilterID.String()),
		Currency:         new(c.Currency.Code()),
		BillingTiming:    new(billingTimings.Proto(c.BillingTiming)),
		Proration:        new(c.Proration),
		DisplayOnInvoice: new(c.DisplayOnInvoice),
		PricingUnitId:    StringPtr(c.PricingUnitID.String()),
		CreatedAt:        Timestamp(c.CreatedAt),
		UpdatedAt:        Timestamp(c.UpdatedAt),
		TaxIds:           taxIDs,
		RatesCount:       new(int32(d.RatesCount)), //nolint:gosec // bounded by the card's rates
		ActiveRate:       active,
	}
	if c.RegroupPaidFees != "" {
		b.RegroupPaidFees = new(chargeRegroups.Proto(c.RegroupPaidFees))
	}
	return b.Build()
}
