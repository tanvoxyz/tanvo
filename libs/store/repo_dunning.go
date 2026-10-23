package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindDunning binds the repositories of the dunning aggregates.
func (t *txState) bindDunning(r *port.Repos) {
	r.DunningCampaigns = dunningCampaignRepo{t}
}

// dunningCampaignRepo implements dunning.Repository.
type dunningCampaignRepo struct{ t *txState }

var _ dunning.Repository = dunningCampaignRepo{}

func dunningCampaignParams(c *dunning.Campaign) db.CreateDunningCampaignParams {
	bcc := c.BCCEmails
	if bcc == nil {
		bcc = []string{}
	}
	return db.CreateDunningCampaignParams{
		OrganizationID:      c.OrganizationID.UUID(),
		ID:                  c.ID.UUID(),
		Code:                c.Code,
		Name:                c.Name,
		Description:         c.Description,
		DaysBetweenAttempts: int32(c.DaysBetweenAttempts), // validated small
		MaxAttempts:         int32(c.MaxAttempts),         // validated small
		BccEmails:           bcc,
		DeletedAt:           optUTC(c.DeletedAt),
		CreatedAt:           utc(c.CreatedAt),
		UpdatedAt:           utc(c.UpdatedAt),
	}
}

// dunningCampaignRow is the common shape of the campaign reads; the row
// types sqlc generates for each query convert to it. The thresholds come as
// parallel arrays in their order.
type dunningCampaignRow struct {
	DunningCampaign     db.DunningCampaign
	ThresholdIds        []uuid.UUID
	ThresholdCurrencies []string
	ThresholdAmounts    []int64
}

func dunningCampaignOf(row dunningCampaignRow) (*dunning.Campaign, error) {
	r := row.DunningCampaign
	if len(row.ThresholdCurrencies) != len(row.ThresholdIds) || len(row.ThresholdAmounts) != len(row.ThresholdIds) {
		return nil, fmt.Errorf("dunning campaign %s: thresholds: %w", r.ID, core.ErrInvalidValue)
	}
	var thresholds []dunning.Threshold
	for i, id := range row.ThresholdIds {
		cur, err := mustCurrency(row.ThresholdCurrencies[i])
		if err != nil {
			return nil, fmt.Errorf("dunning campaign %s: %w", r.ID, err)
		}
		thresholds = append(thresholds, dunning.Threshold{
			ID:       idOf[core.DunningCampaignThresholdID](id),
			Currency: cur,
			Amount:   row.ThresholdAmounts[i],
		})
	}
	var bcc []string
	if len(r.BccEmails) > 0 {
		bcc = r.BccEmails
	}
	return &dunning.Campaign{
		ID:                  idOf[core.DunningCampaignID](r.ID),
		OrganizationID:      idOf[core.OrganizationID](r.OrganizationID),
		Code:                r.Code,
		Name:                r.Name,
		Description:         r.Description,
		DaysBetweenAttempts: int(r.DaysBetweenAttempts),
		MaxAttempts:         int(r.MaxAttempts),
		BCCEmails:           bcc,
		Thresholds:          thresholds,
		DeletedAt:           optUTC(r.DeletedAt),
		CreatedAt:           utc(r.CreatedAt),
		UpdatedAt:           utc(r.UpdatedAt),
	}, nil
}

// writeThresholds replaces the thresholds of c.
func (r dunningCampaignRepo) writeThresholds(ctx context.Context, q *db.Queries, c *dunning.Campaign, replace bool) error {
	org, id := c.OrganizationID.UUID(), c.ID.UUID()
	if replace {
		if err := q.DeleteDunningCampaignThresholds(ctx, db.DeleteDunningCampaignThresholdsParams{OrganizationID: org, DunningCampaignID: id}); err != nil {
			return err
		}
	}
	if len(c.Thresholds) == 0 {
		return nil
	}
	p := db.InsertDunningCampaignThresholdsParams{
		OrganizationID:    org,
		DunningCampaignID: id,
		Ids:               make([]uuid.UUID, len(c.Thresholds)),
		Currencies:        make([]string, len(c.Thresholds)),
		Amounts:           make([]int64, len(c.Thresholds)),
	}
	for i, t := range c.Thresholds {
		p.Ids[i] = t.ID.UUID()
		p.Currencies[i] = t.Currency.Code()
		p.Amounts[i] = t.Amount
	}
	return q.InsertDunningCampaignThresholds(ctx, p)
}

func (r dunningCampaignRepo) Create(ctx context.Context, c *dunning.Campaign) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	// A threshold clash (an ID taken, a currency twice) must not abort the
	// transaction: guard the insert with its thresholds.
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.CreateDunningCampaign(ctx, dunningCampaignParams(c))
		if err != nil || n == 0 {
			return err
		}
		return r.writeThresholds(ctx, q, c, false)
	})
	return insertErr("dunning_campaign", n, err)
}

func (r dunningCampaignRepo) Update(ctx context.Context, c *dunning.Campaign) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateDunningCampaign(ctx, db.UpdateDunningCampaignParams(dunningCampaignParams(c)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeThresholds(ctx, q, c, true)
	})
	return updateErr("dunning_campaign", c.ID, n, err)
}

func (r dunningCampaignRepo) Get(ctx context.Context, org core.OrganizationID, id core.DunningCampaignID) (*dunning.Campaign, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDunningCampaign(ctx, db.GetDunningCampaignParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("dunning_campaign", id, err)
	}
	return dunningCampaignOf(dunningCampaignRow(row))
}

func (r dunningCampaignRepo) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*dunning.Campaign, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetDunningCampaignByCode(ctx, db.GetDunningCampaignByCodeParams{OrganizationID: org.UUID(), Code: code})
	if err != nil {
		return nil, getErr("dunning_campaign", code, err)
	}
	return dunningCampaignOf(dunningCampaignRow(row))
}

func (r dunningCampaignRepo) List(ctx context.Context, org core.OrganizationID, f dunning.Filter, page core.PageRequest) (core.Page[*dunning.Campaign], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*dunning.Campaign]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*dunning.Campaign]{}, err
	}
	currencies := f.Currencies
	if currencies == nil {
		currencies = []string{}
	}
	rows, err := q.ListDunningCampaigns(ctx, db.ListDunningCampaignsParams{
		OrganizationID: org.UUID(), Currencies: currencies, After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*dunning.Campaign]{}, fmt.Errorf("list dunning campaigns: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListDunningCampaignsRow) (*dunning.Campaign, error) {
			return dunningCampaignOf(dunningCampaignRow(r))
		},
		func(c *dunning.Campaign) string { return c.ID.String() })
}
