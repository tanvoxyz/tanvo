package rpc

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tanvoxyz/tanvo/libs/billing/contracts"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	subscriptionv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1/subscriptionv1connect"
)

// Contracts serves tanvo.subscription.v1.ContractService over the billing
// library's contract service (lago Api::V2::ContractsController,
// Api::V2::ContractRateCardsController and
// Api::V2::ContractRateCards::RatePhasesController, with the billing
// entity and payment method of the GraphQL mutations).
type Contracts struct {
	subscriptionv1connect.UnimplementedContractServiceHandler

	svc     *contracts.Service
	catalog *products.Service
}

var _ subscriptionv1connect.ContractServiceHandler = (*Contracts)(nil)

// NewContracts returns the service over svc, reading the currency of
// the rate cards applied with phases from catalog.
func NewContracts(svc *contracts.Service, catalog *products.Service) *Contracts {
	return &Contracts{svc: svc, catalog: catalog}
}

// The update_mask paths of the update RPCs (lago update_params, with the
// GraphQL billing entity and payment method).
var (
	contractUpdateFields = []string{
		"name", "plan_code", "billing_time", "billing_anchor_date", "started_at", "ended_at", "consolidate_invoice",
		"purchase_order_number", "payment_method_type", "payment_method_id", "billing_entity_id",
		"skip_invoice_custom_sections", "invoice_custom_section_ids",
	}
	contractRateCardUpdateFields = []string{"units", "billing_anchor_date"}
	contractFilterFields         = []string{"status", "external_customer_id", "plan_code", "external_id", "billing_entity_id", "has_rate_overrides"}
)

// contractFields renames the domain's field paths to the proto's.
var contractFields = map[string]string{
	"customer":       "external_customer_id",
	"plan":           "plan_code",
	"billing_entity": "billing_entity_id",
	"payment_method": "payment_method_id",
	"rate_card":      "rate_card_code",
}

// CreateContract implements ContractService.CreateContract (lago
// Contracts::CreateService).
func (s *Contracts) CreateContract(ctx context.Context, req *subscriptionv1.CreateContractRequest) (*subscriptionv1.CreateContractResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetContract()
	in := contracts.CreateInput{
		ExternalCustomerID: b.GetExternalCustomerId(),
		ExternalID:         b.GetExternalId(),
		Name:               Opt(b.HasName(), b.GetName()),
		PlanCode:           b.GetPlanCode(),
		BillingTime:        subBillingTimes.Core(b.GetBillingTime()),
		StartedAt:          TimePtr(b.GetStartedAt()),
		EndedAt:            TimePtr(b.GetEndedAt()),
	}
	if b.HasBillingAnchorDate() {
		d, err := FromDate("contract.billing_anchor_date", b.GetBillingAnchorDate())
		if err != nil {
			return nil, err
		}
		in.BillingAnchorDate = &d
	}
	set := func(path string) bool { return contractHas(b, path) }
	if in.Settings, err = contractSettings(b, contractUpdateFields, set); err != nil {
		return nil, err
	}
	v, err := s.svc.Create(ctx, p.OrganizationID, in)
	if err != nil {
		return nil, contractError(ctx, "contract.", err)
	}
	return subscriptionv1.CreateContractResponse_builder{Contract: contractProto(v)}.Build(), nil
}

// GetContract implements ContractService.GetContract (lago
// ContractsController#show).
func (s *Contracts) GetContract(ctx context.Context, req *subscriptionv1.GetContractRequest) (*subscriptionv1.GetContractResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var status subscription.Status
	if req.HasStatus() {
		status = subStatuses.Core(req.GetStatus())
	}
	v, err := s.svc.Get(ctx, p.OrganizationID, req.GetExternalId(), status)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.GetContractResponse_builder{Contract: contractProto(v)}.Build(), nil
}

// ListContracts implements ContractService.ListContracts (lago
// ContractsController#index and ContractsQuery).
func (s *Contracts) ListContracts(ctx context.Context, req *subscriptionv1.ListContractsRequest) (*subscriptionv1.ListContractsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := contractListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.ListContractsResponse_builder{
		Contracts:     Items(page, contractProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateContract implements ContractService.UpdateContract (lago
// Contracts::UpdateService); unset ended_at, purchase_order_number,
// billing_anchor_date, plan_code or billing_entity_id in the mask clears
// it.
func (s *Contracts) UpdateContract(ctx context.Context, req *subscriptionv1.UpdateContractRequest) (*subscriptionv1.UpdateContractResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetContract()
	set := func(path string) bool { return contractHas(b, path) }
	paths, err := UpdatePaths(req.GetUpdateMask(), contractUpdateFields, set)
	if err != nil {
		return nil, err
	}
	var in contracts.UpdateInput
	for _, path := range paths {
		switch path {
		case "name":
			in.Name = core.Some(Opt(b.HasName(), b.GetName()))
		case "plan_code":
			in.PlanCode = core.Some(b.GetPlanCode())
		case "billing_time":
			in.BillingTime = subBillingTimes.Core(b.GetBillingTime())
		case "billing_anchor_date":
			var d *core.Date
			if b.HasBillingAnchorDate() {
				v, err := FromDate("contract.billing_anchor_date", b.GetBillingAnchorDate())
				if err != nil {
					return nil, err
				}
				d = &v
			}
			in.BillingAnchorDate = core.Some(d)
		case "started_at":
			in.StartedAt = TimePtr(b.GetStartedAt())
		case "ended_at":
			in.EndedAt = core.Some(TimePtr(b.GetEndedAt()))
		}
	}
	if in.Settings, err = contractSettings(b, paths, func(path string) bool { return slices.Contains(paths, path) }); err != nil {
		return nil, err
	}
	v, err := s.svc.Update(ctx, p.OrganizationID, b.GetExternalId(), in)
	if err != nil {
		return nil, contractError(ctx, "contract.", err)
	}
	return subscriptionv1.UpdateContractResponse_builder{Contract: contractProto(v)}.Build(), nil
}

// TerminateContract implements ContractService.TerminateContract (lago
// Contracts::TerminateService).
func (s *Contracts) TerminateContract(ctx context.Context, req *subscriptionv1.TerminateContractRequest) (*subscriptionv1.TerminateContractResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	v, err := s.svc.Terminate(ctx, p.OrganizationID, req.GetExternalId())
	if err != nil {
		return nil, contractError(ctx, "", err)
	}
	return subscriptionv1.TerminateContractResponse_builder{Contract: contractProto(v)}.Build(), nil
}

// CreateContractRateCard implements ContractService.CreateContractRateCard
// (lago ContractRateCards::CreateService).
func (s *Contracts) CreateContractRateCard(ctx context.Context, req *subscriptionv1.CreateContractRateCardRequest) (*subscriptionv1.CreateContractRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetRateCard()
	in := contracts.CardInput{RateCardCode: b.GetRateCardCode()}
	if in.Units, err = decimalOpt("rate_card.units", b.HasUnits(), b.GetUnits()); err != nil {
		return nil, err
	}
	if b.HasBillingAnchorDate() {
		d, err := FromDate("rate_card.billing_anchor_date", b.GetBillingAnchorDate())
		if err != nil {
			return nil, err
		}
		in.BillingAnchorDate = &d
	}
	if phases := b.GetRatePhases(); phases != nil {
		// The override amounts are in the card's currency: the card is read
		// first only when a phase overrides its rates.
		var cur money.Currency
		if slices.ContainsFunc(phases, (*catalogv1.RatePhase).HasRateOverride) {
			card, err := s.catalog.RateCard(ctx, p.OrganizationID, refs.ByCode(b.GetRateCardCode()))
			if err != nil {
				return nil, Error(ctx, err)
			}
			cur = card.Currency
		}
		list := make([]products.PhaseInput, len(phases))
		for i, ph := range phases {
			if list[i], err = phaseInput(fmt.Sprintf("rate_card.rate_phases[%d]", i), ph, cur); err != nil {
				return nil, err
			}
		}
		in.Phases = &list
	}
	cv, err := s.svc.CreateCard(ctx, p.OrganizationID, req.GetExternalId(), in)
	if err != nil {
		return nil, contractError(ctx, "rate_card.", err)
	}
	return subscriptionv1.CreateContractRateCardResponse_builder{RateCard: contractRateCardProto(req.GetExternalId(), cv)}.Build(), nil
}

// GetContractRateCard implements ContractService.GetContractRateCard.
func (s *Contracts) GetContractRateCard(ctx context.Context, req *subscriptionv1.GetContractRateCardRequest) (*subscriptionv1.GetContractRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cv, err := s.svc.Card(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.GetContractRateCardResponse_builder{RateCard: contractRateCardProto(req.GetExternalId(), cv)}.Build(), nil
}

// ListContractRateCards implements ContractService.ListContractRateCards.
func (s *Contracts) ListContractRateCards(ctx context.Context, req *subscriptionv1.ListContractRateCardsRequest) (*subscriptionv1.ListContractRateCardsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListCards(ctx, p.OrganizationID, req.GetExternalId(), Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.ListContractRateCardsResponse_builder{
		RateCards: Items(page, func(cv *contracts.CardView) *subscriptionv1.ContractRateCard {
			return contractRateCardProto(req.GetExternalId(), cv)
		}),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateContractRateCard implements ContractService.UpdateContractRateCard
// (lago ContractRateCards::UpdateService); unset units in the mask remove
// them.
func (s *Contracts) UpdateContractRateCard(ctx context.Context, req *subscriptionv1.UpdateContractRateCardRequest) (*subscriptionv1.UpdateContractRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetRateCard()
	paths, err := UpdatePaths(req.GetUpdateMask(), contractRateCardUpdateFields, func(path string) bool {
		switch path {
		case "units":
			return b.HasUnits()
		case "billing_anchor_date":
			return b.HasBillingAnchorDate()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var in contracts.CardUpdate
	for _, path := range paths {
		switch path {
		case "units":
			units, err := decimalOpt("rate_card.units", b.HasUnits(), b.GetUnits())
			if err != nil {
				return nil, err
			}
			in.Units = core.Some(units)
		case "billing_anchor_date":
			var d *core.Date
			if b.HasBillingAnchorDate() {
				v, err := FromDate("rate_card.billing_anchor_date", b.GetBillingAnchorDate())
				if err != nil {
					return nil, err
				}
				d = &v
			}
			in.BillingAnchorDate = core.Some(d)
		}
	}
	cv, err := s.svc.UpdateCard(ctx, p.OrganizationID, req.GetExternalId(), b.GetRateCardCode(), in)
	if err != nil {
		return nil, contractError(ctx, "rate_card.", err)
	}
	return subscriptionv1.UpdateContractRateCardResponse_builder{RateCard: contractRateCardProto(req.GetExternalId(), cv)}.Build(), nil
}

// DeleteContractRateCard implements ContractService.DeleteContractRateCard
// (lago ContractRateCards::DestroyService).
func (s *Contracts) DeleteContractRateCard(ctx context.Context, req *subscriptionv1.DeleteContractRateCardRequest) (*subscriptionv1.DeleteContractRateCardResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cv, err := s.svc.DeleteCard(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode())
	if err != nil {
		return nil, contractError(ctx, "", err)
	}
	return subscriptionv1.DeleteContractRateCardResponse_builder{RateCard: contractRateCardProto(req.GetExternalId(), cv)}.Build(), nil
}

// ListContractRatePhases implements ContractService.ListContractRatePhases.
func (s *Contracts) ListContractRatePhases(ctx context.Context, req *subscriptionv1.ListContractRatePhasesRequest) (*subscriptionv1.ListContractRatePhasesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.cardCurrency(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode())
	if err != nil {
		return nil, err
	}
	phases, err := s.svc.ListPhases(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.ListContractRatePhasesResponse_builder{RatePhases: ratePhasesProto(phases, cur)}.Build(), nil
}

// CreateContractRatePhase implements ContractService.CreateContractRatePhase.
func (s *Contracts) CreateContractRatePhase(ctx context.Context, req *subscriptionv1.CreateContractRatePhaseRequest) (*subscriptionv1.CreateContractRatePhaseResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.cardCurrency(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode())
	if err != nil {
		return nil, err
	}
	in, err := phaseInput("rate_phase", req.GetRatePhase(), cur)
	if err != nil {
		return nil, err
	}
	ph, err := s.svc.CreatePhase(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode(), in)
	if err != nil {
		return nil, catalogError(ctx, "rate_phase.", "invalid rate phase", err)
	}
	return subscriptionv1.CreateContractRatePhaseResponse_builder{RatePhase: ratePhaseProto(ph, cur)}.Build(), nil
}

// UpdateContractRatePhase implements ContractService.UpdateContractRatePhase;
// an unset billing_interval_cycle_count or rate_override in the mask
// clears it.
func (s *Contracts) UpdateContractRatePhase(ctx context.Context, req *subscriptionv1.UpdateContractRatePhaseRequest) (*subscriptionv1.UpdateContractRatePhaseResponse, error) {
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
	phaseRef, err := CatalogRef("rate_phase.id", b.GetId())
	if err != nil {
		return nil, err
	}
	cur, err := s.cardCurrency(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode())
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
				ov, err := overrideInput("rate_phase.rate_override", b.GetRateOverride(), cur)
				if err != nil {
					return nil, err
				}
				o = &ov
			}
			in.Override = core.Some(o)
		}
	}
	ph, err := s.svc.UpdatePhase(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode(), phaseRef, in)
	if err != nil {
		return nil, catalogError(ctx, "rate_phase.", "invalid rate phase", err)
	}
	return subscriptionv1.UpdateContractRatePhaseResponse_builder{RatePhase: ratePhaseProto(ph, cur)}.Build(), nil
}

// DeleteContractRatePhase implements ContractService.DeleteContractRatePhase.
func (s *Contracts) DeleteContractRatePhase(ctx context.Context, req *subscriptionv1.DeleteContractRatePhaseRequest) (*subscriptionv1.DeleteContractRatePhaseResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	phaseRef, err := CatalogRef("rate_phase_id", req.GetRatePhaseId())
	if err != nil {
		return nil, err
	}
	cur, err := s.cardCurrency(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode())
	if err != nil {
		return nil, err
	}
	ph, err := s.svc.DeletePhase(ctx, p.OrganizationID, req.GetExternalId(), req.GetRateCardCode(), phaseRef)
	if err != nil {
		return nil, catalogError(ctx, "rate_phase.", "rate phase not deletable", err)
	}
	return subscriptionv1.DeleteContractRatePhaseResponse_builder{RatePhase: ratePhaseProto(ph, cur)}.Build(), nil
}

// cardCurrency returns the currency of the rate card applied to the live
// contract with the external ID.
func (s *Contracts) cardCurrency(ctx context.Context, org core.OrganizationID, externalID, code string) (money.Currency, error) {
	cv, err := s.svc.Card(ctx, org, externalID, code)
	if err != nil {
		return money.Currency{}, Error(ctx, err)
	}
	return cv.RateCard.Currency, nil
}

// contractHas reports whether the contract field of an update path is
// present in b.
func contractHas(b *subscriptionv1.Contract, path string) bool {
	return b.ProtoReflect().Has(b.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(path)))
}

// contractSettings converts the invoicing and payment settings of b
// among paths that set reports: present on create, masked on update,
// where an unset field clears the setting.
func contractSettings(b *subscriptionv1.Contract, paths []string, set func(string) bool) (contracts.Settings, error) {
	var out contracts.Settings
	has := func(path string) bool { return slices.Contains(paths, path) && set(path) }
	if has("consolidate_invoice") {
		out.ConsolidateInvoice = Opt(b.HasConsolidateInvoice(), b.GetConsolidateInvoice())
	}
	if has("purchase_order_number") {
		out.PurchaseOrderNumber = core.Some(Opt(b.HasPurchaseOrderNumber(), b.GetPurchaseOrderNumber()))
	}
	if has("billing_entity_id") {
		var id core.BillingEntityID
		if b.HasBillingEntityId() {
			var err error
			if id, err = ParseID[core.BillingEntityID]("contract.billing_entity_id", b.GetBillingEntityId()); err != nil {
				return out, err
			}
		}
		out.BillingEntity = core.Some(id)
	}
	if has("payment_method_type") || has("payment_method_id") {
		pm := &contracts.PaymentMethod{}
		if has("payment_method_type") && b.HasPaymentMethodType() {
			pm.Type = new(subPaymentMethodTypes.Core(b.GetPaymentMethodType()))
		}
		if has("payment_method_id") {
			var id core.PaymentMethodID
			if b.HasPaymentMethodId() {
				var err error
				if id, err = ParseID[core.PaymentMethodID]("contract.payment_method_id", b.GetPaymentMethodId()); err != nil {
					return out, err
				}
			}
			pm.ID = &id
		}
		out.PaymentMethod = pm
	}
	if has("skip_invoice_custom_sections") || has("invoice_custom_section_ids") {
		cs := &subscriptions.CustomSections{}
		if has("skip_invoice_custom_sections") {
			cs.Skip = new(b.GetSkipInvoiceCustomSections())
		}
		if has("invoice_custom_section_ids") {
			rs, err := CatalogRefs("contract.invoice_custom_section_ids", b.GetInvoiceCustomSectionIds())
			if err != nil {
				return out, err
			}
			if rs == nil {
				rs = []refs.Ref{}
			}
			cs.Sections = rs
		}
		out.InvoiceCustomSections = cs
	}
	return out, nil
}

// contractListFilter parses the filter of ListContracts.
func contractListFilter(s string) (subscription.ContractFilter, error) {
	var f subscription.ContractFilter
	e, err := ParseFilter(s)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, contractFilterFields...)
	if err != nil {
		return f, err
	}
	statuses, err := FilterEnum[subscriptionv1.SubscriptionStatus]("status", eq["status"], subscriptionv1.SubscriptionStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, st := range statuses {
		f.Statuses = append(f.Statuses, subStatuses.Core(st))
	}
	if f.ExternalCustomerID, err = FilterSingle("external_customer_id", eq["external_customer_id"]); err != nil {
		return f, err
	}
	if f.PlanCode, err = FilterSingle("plan_code", eq["plan_code"]); err != nil {
		return f, err
	}
	if f.ExternalID, err = FilterSingle("external_id", eq["external_id"]); err != nil {
		return f, err
	}
	for _, v := range eq["billing_entity_id"] {
		id, err := ParseID[core.BillingEntityID](FilterField, v)
		if err != nil {
			return f, err
		}
		f.BillingEntityIDs = append(f.BillingEntityIDs, id)
	}
	f.HasRateOverrides, err = FilterBool("has_rate_overrides", eq["has_rate_overrides"])
	return f, err
}

// contractError maps a service error, naming the request's fields.
func contractError(ctx context.Context, prefix string, err error) error {
	v, ok := validationViolations("", err)
	if !ok {
		return Error(ctx, err)
	}
	for i := range v {
		if f, ok := contractFields[v[i].Field]; ok {
			v[i].Field = f
		}
		v[i].Field = prefix + v[i].Field
	}
	return withReason(InvalidArgument("invalid contract", v), ReasonValidationErrors)
}

// contractProto converts a contract view (lago V2::ContractSerializer with
// its applied rate cards and invoice custom sections).
func contractProto(v *contracts.View) *subscriptionv1.Contract {
	c := v.Contract
	b := subscriptionv1.Contract_builder{
		Id:                         new(c.ID.String()),
		ExternalId:                 new(c.ExternalID),
		ExternalCustomerId:         new(v.Customer.ExternalID),
		CustomerId:                 new(c.CustomerID.String()),
		Name:                       c.Name,
		Status:                     subEnum(subStatuses.Proto(c.Status)),
		BillingTime:                subEnum(subBillingTimes.Proto(c.BillingTime)),
		EffectiveBillingAnchorDate: Date(v.EffectiveBillingAnchorDate),
		StartedAt:                  Timestamp(c.SubscriptionAt),
		EndedAt:                    TimestampPtr(c.EndingAt),
		TerminatedAt:               TimestampPtr(c.TerminatedAt),
		CanceledAt:                 TimestampPtr(c.CanceledAt),
		ConsolidateInvoice:         new(c.ConsolidateInvoice),
		PurchaseOrderNumber:        c.PurchaseOrderNumber,
		PaymentMethodType:          subEnum(subPaymentMethodTypes.Proto(c.PaymentMethodType)),
		PaymentMethodId:            subID(c.PaymentMethodID),
		BillingEntityId:            subID(c.BillingEntityID),
		SkipInvoiceCustomSections:  new(c.SkipInvoiceCustomSections),
		Currency:                   new(v.Currency.Code()),
		RateCardsCount:             new(int32(len(v.RateCards))), //nolint:gosec // bounded by the contract's rate cards
		CreatedAt:                  Timestamp(c.CreatedAt),
		UpdatedAt:                  Timestamp(c.UpdatedAt),
	}
	if c.BillingAnchorDate != nil {
		b.BillingAnchorDate = Date(*c.BillingAnchorDate)
	}
	if v.Plan != nil {
		b.PlanCode = new(v.Plan.Code)
		b.PlanId = new(v.Plan.ID.String())
	}
	for _, id := range c.InvoiceCustomSectionIDs {
		b.InvoiceCustomSectionIds = append(b.InvoiceCustomSectionIds, id.String())
	}
	for _, cv := range v.RateCards {
		b.RateCards = append(b.RateCards, contractRateCardProto(c.ExternalID, cv))
	}
	return b.Build()
}

// contractRateCardProto converts a contract rate card view of the contract
// with the external ID (lago V2::ContractAppliedRateCardSerializer, with
// its phases).
func contractRateCardProto(externalID string, cv *contracts.CardView) *subscriptionv1.ContractRateCard {
	card := cv.Card
	phases := ratePhasesProto(cv.Phases, cv.RateCard.Currency)
	return subscriptionv1.ContractRateCard_builder{
		Id:                 new(card.ID.String()),
		ExternalContractId: new(externalID),
		RateCardCode:       new(cv.RateCard.Code),
		Units:              DecimalPtr(card.Units),
		EffectiveDate:      Date(card.EffectiveDate),
		BillingAnchorDate:  Date(card.BillingAnchorDate),
		NextBillingAt:      TimestampPtr(card.NextBillingAt),
		RatePhases:         phases,
		RatePhasesCount:    new(int32(len(phases))), //nolint:gosec // at most 50 phases
		CreatedAt:          Timestamp(card.CreatedAt),
		UpdatedAt:          Timestamp(card.UpdatedAt),
	}.Build()
}
