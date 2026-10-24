package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindContract binds the contracts, their rate cards and their billing
// segments.
func (t *txState) bindContract(r *port.Repos) {
	r.Contracts = contractRepo{t}
	r.ContractRateCards = contractRateCardRepo{t}
	r.BillingSegments = billingSegmentRepo{t}
}

// contractRepo implements subscription.ContractRepository.
type contractRepo struct{ t *txState }

var _ subscription.ContractRepository = contractRepo{}

func (r contractRepo) GetLive(ctx context.Context, org core.OrganizationID, externalID string) (*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetLiveContract(ctx, db.GetLiveContractParams{OrganizationID: org.UUID(), ExternalID: externalID})
	if err != nil {
		return nil, getErr("contract", externalID, err)
	}
	return subscriptionOf(subscriptionRow(row))
}

func (r contractRepo) GetByStatus(ctx context.Context, org core.OrganizationID, externalID string, status subscription.Status) (*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetContractByStatus(ctx, db.GetContractByStatusParams{OrganizationID: org.UUID(), ExternalID: externalID, Status: string(status)})
	if err != nil {
		return nil, getErr("contract", externalID, err)
	}
	return subscriptionOf(subscriptionRow(row))
}

func (r contractRepo) GetTerminatable(ctx context.Context, org core.OrganizationID, externalID string) (*subscription.Subscription, error) {
	s, err := r.GetByStatus(ctx, org, externalID, subscription.StatusActive)
	if err == nil || !errors.Is(err, core.ErrNotFound) {
		return s, err
	}
	return r.GetByStatus(ctx, org, externalID, subscription.StatusPending)
}

func (r contractRepo) List(ctx context.Context, org core.OrganizationID, f subscription.ContractFilter, page core.PageRequest) (core.Page[*subscription.Subscription], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*subscription.Subscription]{}, fmt.Errorf("list contracts: %w", err)
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*subscription.Subscription]{}, err
	}
	after, afterAt, lim, err := timeCursorArgs(page)
	if err != nil {
		return core.Page[*subscription.Subscription]{}, err
	}
	search := searchArg(f.Search)
	var searchID *uuid.UUID
	if f.ExternalID != "" {
		search = nil
	} else if t := core.SearchTerm(f.Search); len(t) == 36 {
		if u, err := uuid.Parse(t); err == nil {
			searchID = &u
		}
	}
	rows, err := q.ListContracts(ctx, db.ListContractsParams{
		OrganizationID:     org.UUID(),
		ExternalCustomerID: optString(f.ExternalCustomerID),
		PlanCode:           optString(f.PlanCode),
		ExternalID:         optString(f.ExternalID),
		Statuses:           enumStrings(f.Statuses),
		BillingEntityIds:   uuids(f.BillingEntityIDs),
		HasRateOverrides:   f.HasRateOverrides,
		Search:             search,
		SearchID:           searchID,
		SearchCustomers:    f.ExternalCustomerID == "",
		After:              after,
		AfterAt:            afterAt,
		Lim:                lim,
	})
	if err != nil {
		return core.Page[*subscription.Subscription]{}, fmt.Errorf("list contracts: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListContractsRow) (*subscription.Subscription, error) {
			return subscriptionOf(subscriptionRow(r))
		},
		func(s *subscription.Subscription) string {
			return core.Cursor{Key: s.CreatedAt.UTC().Format(time.RFC3339Nano), ID: s.ID.UUID()}.Encode()
		})
}

func (r contractRepo) ListDueForActivation(ctx context.Context, org core.OrganizationID, t time.Time) ([]*subscription.Subscription, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListContractsDueForActivation(ctx, db.ListContractsDueForActivationParams{OrganizationID: org.UUID(), At: utc(t)})
	if err != nil {
		return nil, fmt.Errorf("list contracts due for activation: %w", mapErr(err))
	}
	return convAll(rows, func(r db.ListContractsDueForActivationRow) (*subscription.Subscription, error) {
		return subscriptionOf(subscriptionRow(r))
	})
}

func (r contractRepo) ExistsForPlan(ctx context.Context, org core.OrganizationID, plan core.PlanID) (bool, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return false, err
	}
	ok, err := q.ContractExistsForPlan(ctx, db.ContractExistsForPlanParams{OrganizationID: org.UUID(), PlanID: optUUID(plan)})
	if err != nil {
		return false, fmt.Errorf("contracts of plan %s: %w", plan, mapErr(err))
	}
	return ok, nil
}

// contractRateCardRepo implements subscription.RateCardRepository.
type contractRateCardRepo struct{ t *txState }

var _ subscription.RateCardRepository = contractRateCardRepo{}

func contractRateCardOf(r db.SubscriptionRateCard) (*subscription.RateCard, error) {
	return &subscription.RateCard{
		ID:                idOf[core.SubscriptionRateCardID](r.ID),
		OrganizationID:    idOf[core.OrganizationID](r.OrganizationID),
		SubscriptionID:    idOf[core.SubscriptionID](r.SubscriptionID),
		RateCardID:        idOf[core.RateCardID](r.RateCardID),
		Units:             r.Units,
		EffectiveDate:     dateOfCol(r.EffectiveDate),
		BillingAnchorDate: dateOfCol(r.BillingAnchorDate),
		NextBillingAt:     optUTC(r.NextBillingAt),
		CreatedAt:         utc(r.CreatedAt),
		UpdatedAt:         utc(r.UpdatedAt),
		DeletedAt:         optUTC(r.DeletedAt),
	}, nil
}

func (r contractRateCardRepo) Create(ctx context.Context, c *subscription.RateCard, phases []*catalog.RatePhase) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateSubscriptionRateCard(ctx, db.CreateSubscriptionRateCardParams{
		OrganizationID: c.OrganizationID.UUID(), ID: c.ID.UUID(), SubscriptionID: c.SubscriptionID.UUID(),
		RateCardID: c.RateCardID.UUID(), Units: c.Units, EffectiveDate: dateCol(c.EffectiveDate),
		BillingAnchorDate: dateCol(c.BillingAnchorDate), NextBillingAt: optUTC(c.NextBillingAt),
		CreatedAt: utc(c.CreatedAt), UpdatedAt: utc(c.UpdatedAt),
	})
	if err := insertErr("contract_rate_card", n, err); err != nil {
		return err
	}
	if err := r.writePhases(ctx, q, c.OrganizationID, c.ID, phases, c.CreatedAt); err != nil {
		return err
	}
	// Ingestion caches which contracts bill a metric in advance.
	return r.t.noteCatalog(nil, CatalogChange{OrganizationID: c.OrganizationID, Kind: CatalogSubscription})
}

func (r contractRateCardRepo) Update(ctx context.Context, c *subscription.RateCard) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.UpdateSubscriptionRateCard(ctx, db.UpdateSubscriptionRateCardParams{
		OrganizationID: c.OrganizationID.UUID(), ID: c.ID.UUID(), Units: c.Units,
		EffectiveDate: dateCol(c.EffectiveDate), BillingAnchorDate: dateCol(c.BillingAnchorDate),
		NextBillingAt: optUTC(c.NextBillingAt), UpdatedAt: utc(c.UpdatedAt),
	})
	return updateErr("contract_rate_card", c.ID, n, err)
}

func (r contractRateCardRepo) Discard(ctx context.Context, org core.OrganizationID, id core.SubscriptionRateCardID, at time.Time) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	n, err := q.DiscardSubscriptionRateCard(ctx, db.DiscardSubscriptionRateCardParams{OrganizationID: org.UUID(), ID: id.UUID(), At: optUTC(&at)})
	if err := updateErr("contract_rate_card", id, n, err); err != nil {
		return err
	}
	if err := q.DiscardSubscriptionRatePhases(ctx, db.DiscardSubscriptionRatePhasesParams{
		OrganizationID: org.UUID(), SubscriptionRateCardID: id.UUID(), Keep: []uuid.UUID{}, At: optUTC(&at),
	}); err != nil {
		return fmt.Errorf("discard phases of contract rate card %s: %w", id, mapErr(err))
	}
	return r.t.noteCatalog(nil, CatalogChange{OrganizationID: org, Kind: CatalogSubscription})
}

func (r contractRateCardRepo) Get(ctx context.Context, org core.OrganizationID, id core.SubscriptionRateCardID) (*subscription.RateCard, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetSubscriptionRateCard(ctx, db.GetSubscriptionRateCardParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("contract_rate_card", id, err)
	}
	return contractRateCardOf(row)
}

func (r contractRateCardRepo) GetByRateCard(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID, rateCard core.RateCardID) (*subscription.RateCard, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetSubscriptionRateCardByRateCard(ctx, db.GetSubscriptionRateCardByRateCardParams{
		OrganizationID: org.UUID(), SubscriptionID: sub.UUID(), RateCardID: rateCard.UUID(),
	})
	if err != nil {
		return nil, getErr("contract_rate_card", rateCard, err)
	}
	return contractRateCardOf(row)
}

// list reads one page of the cards matching f (lim rows at most).
func (r contractRateCardRepo) list(ctx context.Context, org core.OrganizationID, f subscription.RateCardQuery, after *uuid.UUID, afterDate *time.Time, lim int32) ([]db.SubscriptionRateCard, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSubscriptionRateCards(ctx, db.ListSubscriptionRateCardsParams{
		OrganizationID: org.UUID(), SubscriptionID: optUUID(f.SubscriptionID), RateCardID: optUUID(f.RateCardID),
		ProductID: optUUID(f.ProductID), ProductFilterID: optUUID(f.ProductFilterID), CategoryID: optUUID(f.ProductCategoryID),
		After: after, AfterDate: afterDate, Lim: lim,
	})
	if err != nil {
		return nil, fmt.Errorf("list contract rate cards: %w", mapErr(err))
	}
	return rows, nil
}

func (r contractRateCardRepo) ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.RateCard, error) {
	var out []*subscription.RateCard
	page := core.PageRequest{Size: core.MaxPageSize}
	for {
		p, err := r.List(ctx, org, subscription.RateCardQuery{SubscriptionID: sub}, page)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		if p.NextToken == "" {
			return out, nil
		}
		page.Token = p.NextToken
	}
}

func (r contractRateCardRepo) List(ctx context.Context, org core.OrganizationID, f subscription.RateCardQuery, page core.PageRequest) (core.Page[*subscription.RateCard], error) {
	after, afterAt, lim, err := timeCursorArgs(page)
	if err != nil {
		return core.Page[*subscription.RateCard]{}, err
	}
	rows, err := r.list(ctx, org, f, after, afterAt, lim)
	if err != nil {
		return core.Page[*subscription.RateCard]{}, err
	}
	return pageOf(rows, page, contractRateCardOf, func(c *subscription.RateCard) string {
		return core.Cursor{Key: dateCol(c.EffectiveDate).Format(time.RFC3339Nano), ID: c.ID.UUID()}.Encode()
	})
}

func (r contractRateCardRepo) Exists(ctx context.Context, org core.OrganizationID, f subscription.RateCardQuery) (bool, error) {
	rows, err := r.list(ctx, org, f, nil, nil, 1)
	return len(rows) > 0, err
}

func (r contractRateCardRepo) due(ctx context.Context, org core.OrganizationID, customer *uuid.UUID, t time.Time) ([]db.SubscriptionRateCard, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSubscriptionRateCardsDueForBilling(ctx, db.ListSubscriptionRateCardsDueForBillingParams{
		OrganizationID: org.UUID(), At: utc(t), CustomerID: customer,
	})
	if err != nil {
		return nil, fmt.Errorf("list contract rate cards due: %w", mapErr(err))
	}
	return rows, nil
}

func (r contractRateCardRepo) ListDueForBilling(ctx context.Context, org core.OrganizationID, customer core.CustomerID, t time.Time) ([]*subscription.RateCard, error) {
	rows, err := r.due(ctx, org, optUUID(customer), t)
	if err != nil {
		return nil, err
	}
	return convAll(rows, contractRateCardOf)
}

func (r contractRateCardRepo) CustomersDueForBilling(ctx context.Context, org core.OrganizationID, t time.Time) ([]core.CustomerID, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	ids, err := q.ListCustomersDueForContractBilling(ctx, db.ListCustomersDueForContractBillingParams{OrganizationID: org.UUID(), At: utc(t)})
	if err != nil {
		return nil, fmt.Errorf("list customers due for contract billing: %w", mapErr(err))
	}
	return idsOf[core.CustomerID](ids), nil
}

// subscriptionRatePhaseOf converts a contract rate card phase.
func subscriptionRatePhaseOf(r db.SubscriptionRatePhase) (*catalog.RatePhase, error) {
	p, err := ratePhaseOf(db.RatePhase{
		OrganizationID: r.OrganizationID, ID: r.ID, Code: r.Code, Position: r.Position, Name: r.Name,
		BillingIntervalCycleCount: r.BillingIntervalCycleCount, OverrideRateModel: r.OverrideRateModel,
		OverrideProperties: r.OverrideProperties, OverrideMinAmount: r.OverrideMinAmount,
		OverrideBillingIntervalCount: r.OverrideBillingIntervalCount, OverrideBillingIntervalUnit: r.OverrideBillingIntervalUnit,
		OverridePricingUnitConversionRate: r.OverridePricingUnitConversionRate,
		CreatedAt:                         r.CreatedAt, UpdatedAt: r.UpdatedAt, DeletedAt: r.DeletedAt,
	})
	if err != nil {
		return nil, err
	}
	p.PlanRateCardID = core.PlanRateCardID{}
	p.SubscriptionRateCardID = idOf[core.SubscriptionRateCardID](r.SubscriptionRateCardID)
	return p, nil
}

// writePhases makes phases the kept phases of the card id, as
// planRateCardRepo.writePhases does for a plan rate card.
func (r contractRateCardRepo) writePhases(ctx context.Context, q *db.Queries, org core.OrganizationID, id core.SubscriptionRateCardID, phases []*catalog.RatePhase, at time.Time) error {
	rows, err := q.ListSubscriptionRatePhases(ctx, db.ListSubscriptionRatePhasesParams{OrganizationID: org.UUID(), SubscriptionRateCardID: id.UUID()})
	if err != nil {
		return fmt.Errorf("contract rate card %s phases: %w", id, mapErr(err))
	}
	existing := make(map[uuid.UUID]bool, len(rows))
	for _, row := range rows {
		existing[row.ID] = true
	}
	keep := []uuid.UUID{}
	for _, p := range phases {
		if existing[p.ID.UUID()] {
			keep = append(keep, p.ID.UUID())
		}
	}
	if err := q.DiscardSubscriptionRatePhases(ctx, db.DiscardSubscriptionRatePhasesParams{
		OrganizationID: org.UUID(), SubscriptionRateCardID: id.UUID(), Keep: keep, At: optUTC(&at),
	}); err != nil {
		return fmt.Errorf("contract rate card %s phases: %w", id, mapErr(err))
	}
	if err := q.ParkSubscriptionRatePhases(ctx, db.ParkSubscriptionRatePhasesParams{OrganizationID: org.UUID(), SubscriptionRateCardID: id.UUID(), Ids: keep}); err != nil {
		return fmt.Errorf("contract rate card %s phases: %w", id, mapErr(err))
	}
	for _, p := range phases {
		arg := db.InsertSubscriptionRatePhaseParams{
			OrganizationID: org.UUID(), ID: p.ID.UUID(), SubscriptionRateCardID: id.UUID(), Code: p.Code,
			Position: int32(p.Position), Name: p.Name, //nolint:gosec // a handful of phases
			BillingIntervalCycleCount: optInt32(p.BillingIntervalCycleCount),
			CreatedAt:                 utc(p.CreatedAt), UpdatedAt: utc(p.UpdatedAt),
		}
		if o := p.Override; o != nil {
			model := string(o.RateModel)
			arg.OverrideRateModel = &model
			arg.OverrideProperties = o.Properties.Raw()
			arg.OverrideMinAmount = &o.MinAmount
			arg.OverrideBillingIntervalCount = optInt32(o.BillingIntervalCount)
			arg.OverrideBillingIntervalUnit = optString(o.BillingIntervalUnit.String())
			arg.OverridePricingUnitConversionRate = o.PricingUnitConversionRate
		}
		if !existing[p.ID.UUID()] {
			n, err := q.InsertSubscriptionRatePhase(ctx, arg)
			if err := insertErr("rate_phase", n, err); err != nil {
				return err
			}
			continue
		}
		n, err := q.UpdateSubscriptionRatePhase(ctx, db.UpdateSubscriptionRatePhaseParams{
			OrganizationID: arg.OrganizationID, ID: arg.ID, SubscriptionRateCardID: arg.SubscriptionRateCardID, Code: arg.Code,
			Position: arg.Position, Name: arg.Name, BillingIntervalCycleCount: arg.BillingIntervalCycleCount,
			OverrideRateModel: arg.OverrideRateModel, OverrideProperties: arg.OverrideProperties,
			OverrideMinAmount: arg.OverrideMinAmount, OverrideBillingIntervalCount: arg.OverrideBillingIntervalCount,
			OverrideBillingIntervalUnit:       arg.OverrideBillingIntervalUnit,
			OverridePricingUnitConversionRate: arg.OverridePricingUnitConversionRate, UpdatedAt: arg.UpdatedAt,
		})
		if err := updateErr("rate_phase", p.ID, n, err); err != nil {
			return err
		}
	}
	return nil
}

func (r contractRateCardRepo) ListPhases(ctx context.Context, org core.OrganizationID, id core.SubscriptionRateCardID) ([]*catalog.RatePhase, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSubscriptionRatePhases(ctx, db.ListSubscriptionRatePhasesParams{OrganizationID: org.UUID(), SubscriptionRateCardID: id.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list contract rate phases: %w", mapErr(err))
	}
	return convAll(rows, subscriptionRatePhaseOf)
}

func (r contractRateCardRepo) SavePhases(ctx context.Context, org core.OrganizationID, id core.SubscriptionRateCardID, phases []*catalog.RatePhase, at time.Time) error {
	c, err := r.Get(ctx, org, id)
	if err != nil {
		return err
	}
	if c.DeletedAt != nil {
		return fmt.Errorf("contract rate card %s: %w", id, core.ErrNotFound)
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	return r.t.guard(ctx, func() error { return r.writePhases(ctx, q, org, id, phases, at) })
}

// billingSegmentRepo implements subscription.BillingSegmentRepository.
type billingSegmentRepo struct{ t *txState }

var _ subscription.BillingSegmentRepository = billingSegmentRepo{}

func billingSegmentParams(s *subscription.BillingSegment) db.InsertBillingSegmentParams {
	return db.InsertBillingSegmentParams{
		OrganizationID: s.OrganizationID.UUID(), ID: s.ID.UUID(), SubscriptionID: s.SubscriptionID.UUID(),
		CustomerID: s.CustomerID.UUID(), SubscriptionRateCardID: s.RateCardID.UUID(), InvoiceID: optUUID(s.InvoiceID),
		RateCardRateID: optUUID(s.RateCardRateID), RatePhaseID: optUUID(s.RatePhaseID), PricingUnitID: optUUID(s.PricingUnitID),
		CycleStartedAt: utc(s.CycleStartedAt), StartedAt: utc(s.StartedAt), EndedAt: utc(s.EndedAt), BillingAt: utc(s.BillingAt),
		RateModel: string(s.RateModel), RateProperties: s.RateProperties.Raw(), Currency: s.Currency.Code(),
		ProrationRatio: s.ProrationRatio, Status: string(s.Status), CreatedAt: utc(s.CreatedAt), UpdatedAt: utc(s.UpdatedAt),
	}
}

func billingSegmentOf(r db.BillingSegment) (*subscription.BillingSegment, error) {
	cur, err := mustCurrency(r.Currency)
	if err != nil {
		return nil, err
	}
	model, err := catalog.ParseChargeModel(r.RateModel)
	if err != nil {
		return nil, fmt.Errorf("billing segment %s: %w", r.ID, err)
	}
	props, err := catalog.NewProperties(r.RateProperties)
	if err != nil {
		return nil, fmt.Errorf("billing segment %s: %w", r.ID, err)
	}
	return &subscription.BillingSegment{
		ID:             idOf[core.BillingSegmentID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		SubscriptionID: idOf[core.SubscriptionID](r.SubscriptionID),
		CustomerID:     idOf[core.CustomerID](r.CustomerID),
		RateCardID:     idOf[core.SubscriptionRateCardID](r.SubscriptionRateCardID),
		InvoiceID:      optIDOf[core.InvoiceID](r.InvoiceID),
		RateCardRateID: optIDOf[core.RateCardRateID](r.RateCardRateID),
		RatePhaseID:    optIDOf[core.RatePhaseID](r.RatePhaseID),
		PricingUnitID:  optIDOf[core.PricingUnitID](r.PricingUnitID),
		CycleStartedAt: utc(r.CycleStartedAt),
		StartedAt:      utc(r.StartedAt),
		EndedAt:        utc(r.EndedAt),
		BillingAt:      utc(r.BillingAt),
		RateModel:      model,
		RateProperties: props,
		Currency:       cur,
		ProrationRatio: r.ProrationRatio,
		Status:         subscription.SegmentStatus(r.Status),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r billingSegmentRepo) InsertAll(ctx context.Context, segs []*subscription.BillingSegment) error {
	if len(segs) == 0 {
		return nil
	}
	org := segs[0].OrganizationID
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	return r.t.guard(ctx, func() error {
		for _, s := range segs {
			if s.OrganizationID != org {
				return fmt.Errorf("billing segment %s: %w", s.ID, core.ErrInvalidValue)
			}
			overlaps, err := q.BillingSegmentOverlaps(ctx, db.BillingSegmentOverlapsParams{
				OrganizationID: org.UUID(), SubscriptionRateCardID: s.RateCardID.UUID(),
				StartedAt: utc(s.StartedAt), EndedAt: utc(s.EndedAt),
			})
			if err != nil {
				return fmt.Errorf("billing segment %s: %w", s.ID, mapErr(err))
			}
			if overlaps {
				return fmt.Errorf("billing segment: overlapping_periods: %w", core.ErrConflict)
			}
			n, err := q.InsertBillingSegment(ctx, billingSegmentParams(s))
			if err := insertErr("billing_segment", n, err); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r billingSegmentRepo) Update(ctx context.Context, s *subscription.BillingSegment) error {
	q, err := r.t.queries(ctx, s.OrganizationID)
	if err != nil {
		return err
	}
	p := billingSegmentParams(s)
	n, err := q.UpdateBillingSegment(ctx, db.UpdateBillingSegmentParams{
		OrganizationID: p.OrganizationID, ID: p.ID, InvoiceID: p.InvoiceID, RateCardRateID: p.RateCardRateID,
		RatePhaseID: p.RatePhaseID, PricingUnitID: p.PricingUnitID, CycleStartedAt: p.CycleStartedAt,
		StartedAt: p.StartedAt, EndedAt: p.EndedAt, BillingAt: p.BillingAt, RateModel: p.RateModel,
		RateProperties: p.RateProperties, Currency: p.Currency, ProrationRatio: p.ProrationRatio,
		Status: p.Status, UpdatedAt: p.UpdatedAt,
	})
	return updateErr("billing_segment", s.ID, n, err)
}

func (r billingSegmentRepo) ListOverlapping(ctx context.Context, org core.OrganizationID, card core.SubscriptionRateCardID, from, to time.Time) ([]*subscription.BillingSegment, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListOverlappingBillingSegments(ctx, db.ListOverlappingBillingSegmentsParams{
		OrganizationID: org.UUID(), SubscriptionRateCardID: card.UUID(), FromAt: utc(from), ToAt: utc(to),
	})
	if err != nil {
		return nil, fmt.Errorf("list billing segments: %w", mapErr(err))
	}
	return convAll(rows, billingSegmentOf)
}

func (r billingSegmentRepo) MaxCycleStartedAt(ctx context.Context, org core.OrganizationID, card core.SubscriptionRateCardID) (*time.Time, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	t, err := q.MaxBillingSegmentCycleStartedAt(ctx, db.MaxBillingSegmentCycleStartedAtParams{OrganizationID: org.UUID(), SubscriptionRateCardID: card.UUID()})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("billing segments of %s: %w", card, mapErr(err))
	}
	t = t.UTC()
	return &t, nil
}

func (r billingSegmentRepo) ListAwaitingInvoicing(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]*subscription.BillingSegment, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListBillingSegmentsAwaitingInvoicing(ctx, db.ListBillingSegmentsAwaitingInvoicingParams{OrganizationID: org.UUID(), CustomerID: customer.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list billing segments awaiting invoicing: %w", mapErr(err))
	}
	return convAll(rows, billingSegmentOf)
}

func (r billingSegmentRepo) CustomersAwaitingInvoicing(ctx context.Context, org core.OrganizationID) ([]core.CustomerID, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	ids, err := q.ListCustomersAwaitingSegmentInvoicing(ctx, org.UUID())
	if err != nil {
		return nil, fmt.Errorf("list customers awaiting segment invoicing: %w", mapErr(err))
	}
	return idsOf[core.CustomerID](ids), nil
}

func (r billingSegmentRepo) GeneratingInvoiceIDs(ctx context.Context, org core.OrganizationID, customer core.CustomerID) ([]core.InvoiceID, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	ids, err := q.ListGeneratingInvoiceIDsOfBillingSegments(ctx, db.ListGeneratingInvoiceIDsOfBillingSegmentsParams{OrganizationID: org.UUID(), CustomerID: customer.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list generating invoices of billing segments: %w", mapErr(err))
	}
	return idsOf[core.InvoiceID](ids), nil
}

func (r billingSegmentRepo) ListByInvoice(ctx context.Context, org core.OrganizationID, inv core.InvoiceID) ([]*subscription.BillingSegment, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListBillingSegmentsByInvoice(ctx, db.ListBillingSegmentsByInvoiceParams{OrganizationID: org.UUID(), InvoiceID: optUUID(inv)})
	if err != nil {
		return nil, fmt.Errorf("list billing segments of invoice %s: %w", inv, mapErr(err))
	}
	return convAll(rows, billingSegmentOf)
}

func (r billingSegmentRepo) ListBySubscription(ctx context.Context, org core.OrganizationID, sub core.SubscriptionID) ([]*subscription.BillingSegment, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListBillingSegmentsBySubscription(ctx, db.ListBillingSegmentsBySubscriptionParams{OrganizationID: org.UUID(), SubscriptionID: sub.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list billing segments of %s: %w", sub, mapErr(err))
	}
	return convAll(rows, billingSegmentOf)
}
