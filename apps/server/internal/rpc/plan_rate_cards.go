package rpc

import (
	"context"
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

// PlanRateCards serves tanvo.catalog.v1.PlanRateCardService over the
// billing library's product catalog service (lago
// Api::V2::PlanRateCardsController and
// Api::V2::PlanRateCards::RatePhasesController).
type PlanRateCards struct {
	catalogv1connect.UnimplementedPlanRateCardServiceHandler

	svc *products.Service
}

var _ catalogv1connect.PlanRateCardServiceHandler = (*PlanRateCards)(nil)

// NewPlanRateCards returns the service over svc.
func NewPlanRateCards(svc *products.Service) *PlanRateCards { return &PlanRateCards{svc: svc} }

// The update_mask paths of the update RPCs.
var (
	planRateCardUpdateFields = []string{"units"}
	ratePhaseUpdateFields    = []string{"code", "position", "name", "billing_interval_cycle_count", "rate_override"}
)

// CreatePlanRateCard implements PlanRateCardService.CreatePlanRateCard:
// the rate card applied to the plan with its phases.
func (s *PlanRateCards) CreatePlanRateCard(ctx context.Context, req *catalogv1.CreatePlanRateCardRequest) (*catalogv1.CreatePlanRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := CatalogRef("plan_id", req.GetPlanId())
	if err != nil {
		return nil, err
	}
	b := req.GetPlanRateCard()
	cardRef, err := CatalogRef("plan_rate_card.rate_card_id", b.GetRateCardId())
	if err != nil {
		return nil, err
	}
	in := products.PlanRateCardInput{RateCard: cardRef}
	if in.Units, err = decimalOpt("plan_rate_card.units", b.HasUnits(), b.GetUnits()); err != nil {
		return nil, err
	}
	if phases := b.GetRatePhases(); phases != nil {
		// The override amounts are in the card's currency: the card is
		// read first only when a phase overrides its rates.
		var cur money.Currency
		if slices.ContainsFunc(phases, (*catalogv1.RatePhase).HasRateOverride) {
			card, err := s.svc.RateCard(ctx, p.OrganizationID, cardRef)
			if err != nil {
				return nil, Error(ctx, err)
			}
			cur = card.Currency
		}
		list := make([]products.PhaseInput, len(phases))
		for i, ph := range phases {
			if list[i], err = phaseInput(fmt.Sprintf("plan_rate_card.rate_phases[%d]", i), ph, cur); err != nil {
				return nil, err
			}
		}
		in.Phases = &list
	}
	prc, err := s.svc.CreatePlanRateCard(ctx, p.OrganizationID, plan, in)
	if err != nil {
		return nil, catalogError(ctx, "plan_rate_card.", "invalid plan rate card", err)
	}
	out, err := s.planRateCardProto(ctx, prc)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.CreatePlanRateCardResponse_builder{PlanRateCard: out}.Build(), nil
}

// GetPlanRateCard implements PlanRateCardService.GetPlanRateCard;
// plan_rate_card_id is an ID or the code:<code> of the applied rate card.
func (s *PlanRateCards) GetPlanRateCard(ctx context.Context, req *catalogv1.GetPlanRateCardRequest) (*catalogv1.GetPlanRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, ref, err := planRateCardRefs(req.GetPlanId(), "plan_rate_card_id", req.GetPlanRateCardId())
	if err != nil {
		return nil, err
	}
	prc, err := s.svc.PlanRateCard(ctx, p.OrganizationID, plan, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := s.planRateCardProto(ctx, prc)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.GetPlanRateCardResponse_builder{PlanRateCard: out}.Build(), nil
}

// ListPlanRateCards implements PlanRateCardService.ListPlanRateCards: the
// plan's rate cards in creation order.
func (s *PlanRateCards) ListPlanRateCards(ctx context.Context, req *catalogv1.ListPlanRateCardsRequest) (*catalogv1.ListPlanRateCardsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := CatalogRef("plan_id", req.GetPlanId())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListPlanRateCards(ctx, p.OrganizationID, plan, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*catalogv1.PlanRateCard, len(page.Items))
	for i, prc := range page.Items {
		if out[i], err = s.planRateCardProto(ctx, prc); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return catalogv1.ListPlanRateCardsResponse_builder{PlanRateCards: out, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdatePlanRateCard implements PlanRateCardService.UpdatePlanRateCard:
// only the units change; unset units in the mask remove them.
func (s *PlanRateCards) UpdatePlanRateCard(ctx context.Context, req *catalogv1.UpdatePlanRateCardRequest) (*catalogv1.UpdatePlanRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetPlanRateCard()
	paths, err := UpdatePaths(req.GetUpdateMask(), planRateCardUpdateFields, func(path string) bool {
		return path == "units" && b.HasUnits()
	})
	if err != nil {
		return nil, err
	}
	plan, ref, err := planRateCardRefs(req.GetPlanId(), "plan_rate_card.id", b.GetId())
	if err != nil {
		return nil, err
	}
	var in products.PlanRateCardUpdate
	for _, path := range paths {
		if path == "units" {
			units, err := decimalOpt("plan_rate_card.units", b.HasUnits(), b.GetUnits())
			if err != nil {
				return nil, err
			}
			in.Units = core.Some(units)
		}
	}
	prc, err := s.svc.UpdatePlanRateCard(ctx, p.OrganizationID, plan, ref, in)
	if err != nil {
		return nil, catalogError(ctx, "plan_rate_card.", "invalid plan rate card", err)
	}
	out, err := s.planRateCardProto(ctx, prc)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.UpdatePlanRateCardResponse_builder{PlanRateCard: out}.Build(), nil
}

// DeletePlanRateCard implements PlanRateCardService.DeletePlanRateCard;
// the phases are deleted with it.
func (s *PlanRateCards) DeletePlanRateCard(ctx context.Context, req *catalogv1.DeletePlanRateCardRequest) (*catalogv1.DeletePlanRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, ref, err := planRateCardRefs(req.GetPlanId(), "plan_rate_card_id", req.GetPlanRateCardId())
	if err != nil {
		return nil, err
	}
	cur, err := s.svc.PlanRateCard(ctx, p.OrganizationID, plan, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	shown, err := s.planRateCardProto(ctx, cur)
	if err != nil {
		return nil, Error(ctx, err)
	}
	if _, err := s.svc.DeletePlanRateCard(ctx, p.OrganizationID, plan, refs.ByID(cur.ID)); err != nil {
		return nil, catalogError(ctx, "plan_rate_card.", "plan rate card not deletable", err)
	}
	return catalogv1.DeletePlanRateCardResponse_builder{PlanRateCard: shown}.Build(), nil
}

// ListRatePhases implements PlanRateCardService.ListRatePhases: the ramp
// by position.
func (s *PlanRateCards) ListRatePhases(ctx context.Context, req *catalogv1.ListRatePhasesRequest) (*catalogv1.ListRatePhasesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, ref, err := planRateCardRefs(req.GetPlanId(), "plan_rate_card_id", req.GetPlanRateCardId())
	if err != nil {
		return nil, err
	}
	_, card, err := s.parent(ctx, p.OrganizationID, plan, ref)
	if err != nil {
		return nil, err
	}
	phases, err := s.svc.ListPhases(ctx, p.OrganizationID, plan, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return catalogv1.ListRatePhasesResponse_builder{RatePhases: ratePhasesProto(phases, card.Currency)}.Build(), nil
}

// CreateRatePhase implements PlanRateCardService.CreateRatePhase: a phase
// inserted into the ramp.
func (s *PlanRateCards) CreateRatePhase(ctx context.Context, req *catalogv1.CreateRatePhaseRequest) (*catalogv1.CreateRatePhaseResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, ref, err := planRateCardRefs(req.GetPlanId(), "plan_rate_card_id", req.GetPlanRateCardId())
	if err != nil {
		return nil, err
	}
	prc, card, err := s.parent(ctx, p.OrganizationID, plan, ref)
	if err != nil {
		return nil, err
	}
	in, err := phaseInput("rate_phase", req.GetRatePhase(), card.Currency)
	if err != nil {
		return nil, err
	}
	ph, err := s.svc.CreatePhase(ctx, p.OrganizationID, plan, refs.ByID(prc.ID), in)
	if err != nil {
		return nil, catalogError(ctx, "rate_phase.", "invalid rate phase", err)
	}
	return catalogv1.CreateRatePhaseResponse_builder{RatePhase: ratePhaseProto(ph, card.Currency)}.Build(), nil
}

// UpdateRatePhase implements PlanRateCardService.UpdateRatePhase.
// rate_phase.id is an ID or code:<code> within the plan rate card; an
// unset billing_interval_cycle_count or rate_override in the mask clears
// it.
func (s *PlanRateCards) UpdateRatePhase(ctx context.Context, req *catalogv1.UpdateRatePhaseRequest) (*catalogv1.UpdateRatePhaseResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetRatePhase()
	paths, err := UpdatePaths(req.GetUpdateMask(), ratePhaseUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "position":
			return b.HasPosition()
		case "name":
			return b.HasName()
		case "billing_interval_cycle_count":
			return b.HasBillingIntervalCycleCount()
		case "rate_override":
			return b.HasRateOverride()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	plan, ref, err := planRateCardRefs(req.GetPlanId(), "plan_rate_card_id", req.GetPlanRateCardId())
	if err != nil {
		return nil, err
	}
	phaseRef, err := CatalogRef("rate_phase.id", b.GetId())
	if err != nil {
		return nil, err
	}
	prc, card, err := s.parent(ctx, p.OrganizationID, plan, ref)
	if err != nil {
		return nil, err
	}
	var in products.PhaseUpdate
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "position":
			in.Position = core.Some(int(b.GetPosition()))
		case "name":
			in.Name = core.Some(Opt(b.HasName(), b.GetName()))
		case "billing_interval_cycle_count":
			in.BillingIntervalCycleCount = core.Some(OptInt(b.HasBillingIntervalCycleCount(), b.GetBillingIntervalCycleCount()))
		case "rate_override":
			var o *products.RateOverrideInput
			if b.HasRateOverride() {
				ov, err := overrideInput("rate_phase.rate_override", b.GetRateOverride(), card.Currency)
				if err != nil {
					return nil, err
				}
				o = &ov
			}
			in.Override = core.Some(o)
		}
	}
	ph, err := s.svc.UpdatePhase(ctx, p.OrganizationID, plan, refs.ByID(prc.ID), phaseRef, in)
	if err != nil {
		return nil, catalogError(ctx, "rate_phase.", "invalid rate phase", err)
	}
	return catalogv1.UpdateRatePhaseResponse_builder{RatePhase: ratePhaseProto(ph, card.Currency)}.Build(), nil
}

// DeleteRatePhase implements PlanRateCardService.DeleteRatePhase; later
// phases move up.
func (s *PlanRateCards) DeleteRatePhase(ctx context.Context, req *catalogv1.DeleteRatePhaseRequest) (*catalogv1.DeleteRatePhaseResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	plan, ref, err := planRateCardRefs(req.GetPlanId(), "plan_rate_card_id", req.GetPlanRateCardId())
	if err != nil {
		return nil, err
	}
	phaseRef, err := CatalogRef("rate_phase_id", req.GetRatePhaseId())
	if err != nil {
		return nil, err
	}
	prc, card, err := s.parent(ctx, p.OrganizationID, plan, ref)
	if err != nil {
		return nil, err
	}
	ph, err := s.svc.DeletePhase(ctx, p.OrganizationID, plan, refs.ByID(prc.ID), phaseRef)
	if err != nil {
		return nil, catalogError(ctx, "rate_phase.", "rate phase not deletable", err)
	}
	return catalogv1.DeleteRatePhaseResponse_builder{RatePhase: ratePhaseProto(ph, card.Currency)}.Build(), nil
}

// parent returns the plan rate card ref of the plan with its rate card.
func (s *PlanRateCards) parent(ctx context.Context, org core.OrganizationID, plan, ref refs.Ref) (*catalog.PlanRateCard, *catalog.RateCard, error) {
	prc, err := s.svc.PlanRateCard(ctx, org, plan, ref)
	if err != nil {
		return nil, nil, Error(ctx, err)
	}
	card, err := s.svc.RateCard(ctx, org, refs.ByID(prc.RateCardID))
	if err != nil {
		return nil, nil, Error(ctx, err)
	}
	return prc, card, nil
}

func (s *PlanRateCards) planRateCardProto(ctx context.Context, prc *catalog.PlanRateCard) (*catalogv1.PlanRateCard, error) {
	d, err := s.svc.PlanRateCardDetails(ctx, prc)
	if err != nil {
		return nil, err
	}
	phases := ratePhasesProto(d.Phases, d.RateCard.Currency)
	return catalogv1.PlanRateCard_builder{
		Id:              new(prc.ID.String()),
		PlanId:          new(prc.PlanID.String()),
		RateCardId:      new(prc.RateCardID.String()),
		Units:           DecimalPtr(prc.Units),
		RatePhases:      phases,
		CreatedAt:       Timestamp(prc.CreatedAt),
		UpdatedAt:       Timestamp(prc.UpdatedAt),
		RatePhasesCount: new(int32(len(phases))), //nolint:gosec // at most 50 phases
	}.Build(), nil
}

// planRateCardRefs parses the plan and plan rate card references of a
// request.
func planRateCardRefs(planID, field, ref string) (plan, prc refs.Ref, err error) {
	if plan, err = CatalogRef("plan_id", planID); err != nil {
		return refs.Ref{}, refs.Ref{}, err
	}
	if prc, err = CatalogRef(field, ref); err != nil {
		return refs.Ref{}, refs.Ref{}, err
	}
	return plan, prc, nil
}

// phaseInput converts the phase b of field, its amounts in currency c.
func phaseInput(field string, b *catalogv1.RatePhase, c money.Currency) (products.PhaseInput, error) {
	in := products.PhaseInput{
		Code:                      b.GetCode(),
		Position:                  OptInt(b.HasPosition(), b.GetPosition()),
		Name:                      Opt(b.HasName(), b.GetName()),
		BillingIntervalCycleCount: OptInt(b.HasBillingIntervalCycleCount(), b.GetBillingIntervalCycleCount()),
	}
	if b.HasRateOverride() {
		o, err := overrideInput(field+".rate_override", b.GetRateOverride(), c)
		if err != nil {
			return in, err
		}
		in.Override = &o
	}
	return in, nil
}

// overrideInput converts the rate override b of field, its amounts in
// currency c.
func overrideInput(field string, b *catalogv1.RateOverride, c money.Currency) (products.RateOverrideInput, error) {
	in := products.RateOverrideInput{
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

func ratePhasesProto(phases []*catalog.RatePhase, c money.Currency) []*catalogv1.RatePhase {
	out := make([]*catalogv1.RatePhase, len(phases))
	for i, ph := range phases {
		out[i] = ratePhaseProto(ph, c)
	}
	return out
}

func ratePhaseProto(ph *catalog.RatePhase, c money.Currency) *catalogv1.RatePhase {
	b := catalogv1.RatePhase_builder{
		Id:       new(ph.ID.String()),
		Code:     new(ph.Code),
		Name:     ph.Name,
		Position: new(int32(ph.Position)), //nolint:gosec // at most 50 phases
	}
	if n := ph.BillingIntervalCycleCount; n != nil {
		b.BillingIntervalCycleCount = new(int32(*n)) //nolint:gosec // validated cycle count
	}
	if o := ph.Override; o != nil {
		ob := catalogv1.RateOverride_builder{
			RateModel:                 new(chargeModels.Proto(o.RateModel)),
			Properties:                catalogPropertiesProto(o.RateModel, o.Properties),
			MinAmount:                 minorString(o.MinAmount, c),
			PricingUnitConversionRate: DecimalPtr(o.PricingUnitConversionRate),
		}
		if o.BillingIntervalCount != nil || o.BillingIntervalUnit != "" {
			ib := catalogv1.BillingInterval_builder{}
			if o.BillingIntervalCount != nil {
				ib.Count = new(int32(*o.BillingIntervalCount)) //nolint:gosec // validated interval count
			}
			if o.BillingIntervalUnit != "" {
				ib.Unit = new(intervalUnits.Proto(o.BillingIntervalUnit))
			}
			ob.BillingInterval = ib.Build()
		}
		b.RateOverride = ob.Build()
	}
	return b.Build()
}
