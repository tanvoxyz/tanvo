package memstore

import (
	"context"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
)

// dunningData holds the tables of package dunning.
type dunningData struct {
	campaigns *table[core.DunningCampaignID, dunning.Campaign]
}

func newDunningData() *dunningData {
	return &dunningData{
		campaigns: newTable[core.DunningCampaignID](func(c dunning.Campaign) dunning.Campaign {
			c.BCCEmails = slices.Clone(c.BCCEmails)
			c.Thresholds = slices.Clone(c.Thresholds)
			return c
		}),
	}
}

func (d *dunningData) snapshot() *dunningData {
	return &dunningData{campaigns: d.campaigns.snapshot()}
}

// dunningCampaignRepo implements dunning.Repository.
type dunningCampaignRepo struct{ d *dunningData }

var _ dunning.Repository = dunningCampaignRepo{}

func (r dunningCampaignRepo) check(c *dunning.Campaign) error {
	if c.Deleted() {
		return nil
	}
	if r.d.campaigns.any(func(x *dunning.Campaign) bool {
		return x.ID != c.ID && x.OrganizationID == c.OrganizationID && !x.Deleted() && x.Code == c.Code
	}) {
		return conflict("dunning_campaign", "code")
	}
	return nil
}

func (r dunningCampaignRepo) Create(_ context.Context, c *dunning.Campaign) error {
	if r.d.campaigns.has(c.ID) {
		return conflict("dunning_campaign", "id")
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.campaigns.put(c.ID, *c)
	return nil
}

func (r dunningCampaignRepo) Update(_ context.Context, c *dunning.Campaign) error {
	cur, ok := r.d.campaigns.get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID {
		return notFound("dunning_campaign", c.ID)
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.campaigns.put(c.ID, *c)
	return nil
}

func (r dunningCampaignRepo) Get(_ context.Context, org core.OrganizationID, id core.DunningCampaignID) (*dunning.Campaign, error) {
	c, ok := r.d.campaigns.get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("dunning_campaign", id)
	}
	return c, nil
}

func (r dunningCampaignRepo) GetByCode(_ context.Context, org core.OrganizationID, code string) (*dunning.Campaign, error) {
	c, ok := r.d.campaigns.first(func(x *dunning.Campaign) bool {
		return x.OrganizationID == org && !x.Deleted() && x.Code == code
	})
	if !ok {
		return nil, notFound("dunning_campaign", stringer(code))
	}
	return c, nil
}

func (r dunningCampaignRepo) List(_ context.Context, org core.OrganizationID, f dunning.Filter, page core.PageRequest) (core.Page[*dunning.Campaign], error) {
	rows := r.d.campaigns.find(func(x *dunning.Campaign) bool {
		if x.OrganizationID != org || x.Deleted() {
			return false
		}
		if len(f.Currencies) == 0 {
			return true
		}
		return slices.ContainsFunc(x.Thresholds, func(t dunning.Threshold) bool {
			return slices.Contains(f.Currencies, t.Currency.Code())
		})
	})
	return paginate(rows, func(x *dunning.Campaign) core.DunningCampaignID { return x.ID }, page)
}
