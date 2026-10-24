package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period/schedule"
)

// ScheduleSegments stores the billing segments the customer's due
// contract rate cards owe at ts and advances their clocks, under the
// customer's billing lock (lago BillingSegments::ScheduleService). A
// segment overlapping a stored one fails with billing_segment
// overlapping_periods and stores nothing.
//
// source: app/services/billing_segments/schedule_service.rb:15-71
func (s *Service) ScheduleSegments(ctx context.Context, org core.OrganizationID, c core.CustomerID, ts time.Time) ([]*subscription.BillingSegment, error) {
	var out []*subscription.BillingSegment
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		out = nil
		if err := kernel.LockCustomer(ctx, r, org, c, customer.LockScopeBillingSchedule); err != nil {
			return err
		}
		cards, err := r.ContractRateCards.ListDueForBilling(ctx, org, c, ts)
		if err != nil {
			return fmt.Errorf("list due rate cards of %s: %w", c, err)
		}
		for _, card := range cards {
			segs, err := s.scheduleCard(ctx, r, card, ts)
			if err != nil {
				return err
			}
			out = append(out, segs...)
		}
		return nil
	})
	if errors.Is(err, core.ErrConflict) && strings.Contains(err.Error(), subscription.CodeOverlappingPeriods) {
		return nil, fieldError("billing_segment", subscription.CodeOverlappingPeriods)
	}
	return out, err
}

// scheduleCard stores the segments card owes at ts and advances its
// clock.
//
// source: app/services/billing_segments/schedule_service.rb:57-66
func (s *Service) scheduleCard(ctx context.Context, r port.Repos, card *subscription.RateCard, ts time.Time) ([]*subscription.BillingSegment, error) {
	org := card.OrganizationID
	c, err := r.Subscriptions.Get(ctx, org, card.SubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("load contract %s: %w", card.SubscriptionID, err)
	}
	cenv, err := customerEnv(ctx, r, c)
	if err != nil {
		return nil, err
	}
	sch, err := buildSchedule(ctx, r, c, cenv.Location, card)
	if err != nil {
		return nil, err
	}
	rc := sch.rateCard
	if !rc.PricingUnitID.IsZero() {
		if _, err := r.PricingUnits.Get(ctx, org, rc.PricingUnitID); errors.Is(err, core.ErrNotFound) {
			return nil, core.NotFound(ResourcePricingUnit)
		} else if err != nil {
			return nil, fmt.Errorf("load pricing unit %s: %w", rc.PricingUnitID, err)
		}
	}
	missing, err := missingSegments(ctx, r, card, sch, ts)
	if err != nil {
		return nil, err
	}
	product, err := r.Products.Get(ctx, org, rc.ProductID)
	if err != nil {
		return nil, fmt.Errorf("load product %s: %w", rc.ProductID, err)
	}
	status := subscription.SegmentPending
	if product.ProductType == catalog.ProductTypeMetered && rc.BillingTiming == catalog.BillingTimingAdvance {
		// Priced per event, so never invoiced by the segment run.
		status = subscription.SegmentProcessing
	}
	now := s.d.Now()
	rows := make([]*subscription.BillingSegment, 0, len(missing))
	for _, seg := range missing {
		row, err := s.segmentRow(c, card, sch, seg, status, now)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		if err := r.BillingSegments.InsertAll(ctx, rows); err != nil {
			return nil, fmt.Errorf("insert billing segments of %s: %w", card.ID, err)
		}
	}
	if err := s.advanceClock(ctx, r, card, sch, ts); err != nil {
		return nil, err
	}
	return rows, nil
}

// missingSegments returns the segments of sch due by ts that overlap no
// stored segment of card (lago BillingSegments::MissingBillableSegmentsService).
//
// source: app/services/billing_segments/missing_billable_segments_service.rb:15-40
func missingSegments(ctx context.Context, r port.Repos, card *subscription.RateCard, sch *cardSchedule, ts time.Time) ([]schedule.Segment, error) {
	due := sch.SegmentsDueBy(ts)
	if len(due) == 0 {
		return nil, nil
	}
	from, to := due[0].StartedAt, due[0].EndedAt
	for _, seg := range due[1:] {
		if seg.StartedAt.Before(from) {
			from = seg.StartedAt
		}
		if seg.EndedAt.After(to) {
			to = seg.EndedAt
		}
	}
	stored, err := r.BillingSegments.ListOverlapping(ctx, card.OrganizationID, card.ID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list billing segments of %s: %w", card.ID, err)
	}
	out := make([]schedule.Segment, 0, len(due))
	for _, seg := range due {
		settled := false
		for _, st := range stored {
			if st.Overlaps(seg.StartedAt, seg.EndedAt) || st.StartedAt.Equal(seg.StartedAt) {
				settled = true
				break
			}
		}
		if !settled {
			out = append(out, seg)
		}
	}
	return out, nil
}

// segmentRow builds the stored segment of seg (lago
// BillingSegments::CreateService#row_for).
//
// source: app/services/billing_segments/create_service.rb:25-44
func (s *Service) segmentRow(c *subscription.Subscription, card *subscription.RateCard, sch *cardSchedule, seg schedule.Segment, status subscription.SegmentStatus, now time.Time) (*subscription.BillingSegment, error) {
	rate, ok := sch.rates[seg.Rate.ID]
	if !ok {
		return nil, fmt.Errorf("segment of %s: unknown rate %s", card.ID, seg.Rate.ID)
	}
	row := &subscription.BillingSegment{
		ID:             core.NewID[core.BillingSegmentID](s.d.IDs),
		OrganizationID: card.OrganizationID,
		SubscriptionID: c.ID,
		CustomerID:     c.CustomerID,
		RateCardID:     card.ID,
		RateCardRateID: rate.ID,
		PricingUnitID:  sch.rateCard.PricingUnitID,
		CycleStartedAt: seg.CycleStartedAt,
		StartedAt:      seg.StartedAt,
		EndedAt:        seg.EndedAt,
		BillingAt:      seg.BillingAt,
		RateModel:      rate.RateModel,
		RateProperties: rate.Properties,
		Currency:       sch.rateCard.Currency,
		ProrationRatio: seg.Proration.Decimal(),
		Status:         status,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if seg.Override != nil {
		ph, ok := sch.phases[seg.Override.ID]
		if !ok {
			return nil, fmt.Errorf("segment of %s: unknown phase %s", card.ID, seg.Override.ID)
		}
		row.RatePhaseID = ph.ID
		row.RateModel = ph.Override.RateModel
		row.RateProperties = ph.Override.Properties
	}
	if err := row.Validate(); err != nil {
		return nil, err
	}
	return row, nil
}

// HandleJob runs the contract jobs: the activation clock and
// [kernel.JobActivateContract], the segment clock and
// [kernel.JobScheduleBillingSegments]. It reports whether the kind is
// one of them.
func (s *Service) HandleJob(ctx context.Context, j port.Job) (bool, error) {
	org := j.OrganizationID
	switch j.Kind {
	case kernel.JobActivateContracts, kernel.JobCreateBillingSegments:
		var a kernel.InstantArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
		}
		if a.At.IsZero() {
			return true, fmt.Errorf("%s: %w: no instant", j.Kind, core.ErrInvalidValue)
		}
		if j.Kind == kernel.JobActivateContracts {
			_, err := s.ActivateAllPending(ctx, org, a.At)
			return true, err
		}
		return true, s.enqueueDueCustomers(ctx, org, a.At)
	case kernel.JobActivateContract:
		var a kernel.SubscriptionArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
		}
		if _, err := s.Activate(ctx, org, a.SubscriptionID); err != nil {
			var ve *core.ValidationError
			if errors.As(err, &ve) {
				// Lago never retries Contracts::ActivateJob: the failure is
				// left for the next clock tick to meet again.
				s.logActivationFailure(ctx, org, a.SubscriptionID, err)
				return true, nil
			}
			return true, err
		}
		return true, nil
	case kernel.JobScheduleBillingSegments:
		var a kernel.CustomerArgs
		if err := json.Unmarshal(j.Args, &a); err != nil {
			return true, fmt.Errorf("decode %s args: %w", j.Kind, err)
		}
		return true, s.scheduleJob(ctx, org, a.CustomerID)
	}
	return false, nil
}

// enqueueDueCustomers enqueues the segment scheduling of every customer
// with a card due at at (lago Clock::CreateBillingSegmentsJob).
//
// source: app/jobs/clock/create_billing_segments_job.rb:7-15
func (s *Service) enqueueDueCustomers(ctx context.Context, org core.OrganizationID, at time.Time) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ids, err := r.ContractRateCards.CustomersDueForBilling(ctx, org, at)
		if err != nil {
			return fmt.Errorf("list customers due for billing: %w", err)
		}
		for _, id := range ids {
			if err := kernel.EnqueueScheduleSegments(ctx, r, org, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// scheduleJob schedules the customer's segments now, then enqueues their
// invoicing when some await it (lago BillingSegments::ScheduleJob).
//
// source: app/jobs/billing_segments/schedule_job.rb:17-20
func (s *Service) scheduleJob(ctx context.Context, org core.OrganizationID, c core.CustomerID) error {
	if _, err := s.ScheduleSegments(ctx, org, c, s.d.Now()); err != nil {
		return err
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		awaiting, err := r.BillingSegments.ListAwaitingInvoicing(ctx, org, c)
		if err != nil {
			return fmt.Errorf("list segments awaiting invoicing of %s: %w", c, err)
		}
		if len(awaiting) == 0 {
			return nil
		}
		return kernel.EnqueueProcessSegments(ctx, r, org, c)
	})
}
