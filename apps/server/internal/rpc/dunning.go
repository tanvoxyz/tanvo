package rpc

import (
	"context"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/dunning"
	"github.com/tanvoxyz/tanvo/libs/core"
	coredunning "github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/money"
	dunningv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/dunning/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/dunning/v1/dunningv1connect"
)

// DunningCampaigns serves tanvo.dunning.v1.DunningCampaignService over the
// billing library's dunning service (lago DunningCampaign GraphQL
// mutations and resolvers).
type DunningCampaigns struct {
	dunningv1connect.UnimplementedDunningCampaignServiceHandler

	svc *dunning.Service
}

var _ dunningv1connect.DunningCampaignServiceHandler = (*DunningCampaigns)(nil)

// NewDunningCampaigns returns the service over svc.
func NewDunningCampaigns(svc *dunning.Service) *DunningCampaigns {
	return &DunningCampaigns{svc: svc}
}

// dunUpdateFields are the update_mask paths of UpdateDunningCampaign; the
// code stays editable, as in lago's UpdateService.
var dunUpdateFields = []string{"code", "name", "description", "days_between_attempts", "max_attempts", "bcc_emails", "thresholds", "applied_to_organization"}

// CreateDunningCampaign implements DunningCampaignService.CreateDunningCampaign.
func (s *DunningCampaigns) CreateDunningCampaign(ctx context.Context, req *dunningv1.CreateDunningCampaignRequest) (*dunningv1.CreateDunningCampaignResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetDunningCampaign()
	thresholds, err := dunThresholds(b.GetThresholds())
	if err != nil {
		return nil, err
	}
	c, err := s.svc.Create(ctx, p.OrganizationID, dunning.CreateInput{
		Code:                  b.GetCode(),
		Name:                  b.GetName(),
		Description:           Opt(b.HasDescription(), b.GetDescription()),
		DaysBetweenAttempts:   int(b.GetDaysBetweenAttempts()),
		MaxAttempts:           int(b.GetMaxAttempts()),
		BCCEmails:             b.GetBccEmails(),
		Thresholds:            thresholds,
		AppliedToOrganization: b.GetAppliedToOrganization(),
	})
	if err != nil {
		return nil, dunError(ctx, err)
	}
	m, err := s.message(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return dunningv1.CreateDunningCampaignResponse_builder{DunningCampaign: m}.Build(), nil
}

// GetDunningCampaign implements DunningCampaignService.GetDunningCampaign.
func (s *DunningCampaigns) GetDunningCampaign(ctx context.Context, req *dunningv1.GetDunningCampaignRequest) (*dunningv1.GetDunningCampaignResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.lookup(p.OrganizationID).Find(ctx, "dunning_campaign_id", req.GetDunningCampaignId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	m, err := s.message(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return dunningv1.GetDunningCampaignResponse_builder{DunningCampaign: m}.Build(), nil
}

// ListDunningCampaigns implements DunningCampaignService.ListDunningCampaigns:
// the live campaigns by name, searched on name and code; the filter takes
// equalities on currency and applied_to_organization (lago
// DunningCampaignsQuery).
func (s *DunningCampaigns) ListDunningCampaigns(ctx context.Context, req *dunningv1.ListDunningCampaignsRequest) (*dunningv1.ListDunningCampaignsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := dunListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out := make([]*dunningv1.DunningCampaign, len(page.Items))
	for i, c := range page.Items {
		if out[i], err = s.message(ctx, c); err != nil {
			return nil, Error(ctx, err)
		}
	}
	return dunningv1.ListDunningCampaignsResponse_builder{
		DunningCampaigns: out,
		NextPageToken:    new(page.NextToken),
	}.Build(), nil
}

// UpdateDunningCampaign implements DunningCampaignService.UpdateDunningCampaign.
// dunning_campaign.id is an ID or code:<code>; the fields updated are the
// update_mask paths or, without one, the fields set. The thresholds
// replace the campaign's, matched by ID; only an update_mask naming
// thresholds can remove them all.
func (s *DunningCampaigns) UpdateDunningCampaign(ctx context.Context, req *dunningv1.UpdateDunningCampaignRequest) (*dunningv1.UpdateDunningCampaignResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetDunningCampaign()
	paths, err := UpdatePaths(req.GetUpdateMask(), dunUpdateFields, func(path string) bool {
		switch path {
		case "code":
			return b.HasCode()
		case "name":
			return b.HasName()
		case "description":
			return b.HasDescription()
		case "days_between_attempts":
			return b.HasDaysBetweenAttempts()
		case "max_attempts":
			return b.HasMaxAttempts()
		case "bcc_emails":
			return len(b.GetBccEmails()) > 0
		case "thresholds":
			return len(b.GetThresholds()) > 0
		case "applied_to_organization":
			return b.HasAppliedToOrganization()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "dunning_campaign.id", b.GetId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	var in dunning.UpdateInput
	for _, path := range paths {
		switch path {
		case "code":
			in.Code = core.Some(b.GetCode())
		case "name":
			in.Name = core.Some(b.GetName())
		case "description":
			in.Description = core.Some(Opt(b.HasDescription(), b.GetDescription()))
		case "days_between_attempts":
			in.DaysBetweenAttempts = core.Some(int(b.GetDaysBetweenAttempts()))
		case "max_attempts":
			in.MaxAttempts = core.Some(int(b.GetMaxAttempts()))
		case "bcc_emails":
			in.BCCEmails = core.Some(b.GetBccEmails())
		case "thresholds":
			t, err := dunThresholds(b.GetThresholds())
			if err != nil {
				return nil, err
			}
			in.Thresholds = core.Some(t)
		case "applied_to_organization":
			in.AppliedToOrganization = core.Some(b.GetAppliedToOrganization())
		}
	}
	c, err := s.svc.Update(ctx, p.OrganizationID, cur.ID, in)
	if err != nil {
		return nil, dunError(ctx, err)
	}
	m, err := s.message(ctx, c)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return dunningv1.UpdateDunningCampaignResponse_builder{DunningCampaign: m}.Build(), nil
}

// DeleteDunningCampaign implements DunningCampaignService.DeleteDunningCampaign:
// the campaign is soft-deleted and unapplied.
func (s *DunningCampaigns) DeleteDunningCampaign(ctx context.Context, req *dunningv1.DeleteDunningCampaignRequest) (*dunningv1.DeleteDunningCampaignResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.lookup(p.OrganizationID).Find(ctx, "dunning_campaign_id", req.GetDunningCampaignId())
	if err != nil {
		return nil, Error(ctx, err)
	}
	if _, err := s.svc.Delete(ctx, p.OrganizationID, cur.ID); err != nil {
		return nil, Error(ctx, err)
	}
	return &dunningv1.DeleteDunningCampaignResponse{}, nil
}

func (s *DunningCampaigns) lookup(org core.OrganizationID) Lookup[core.DunningCampaignID, *coredunning.Campaign] {
	return Lookup[core.DunningCampaignID, *coredunning.Campaign]{
		ByID: func(ctx context.Context, id core.DunningCampaignID) (*coredunning.Campaign, error) {
			return s.svc.Get(ctx, org, id)
		},
		ByCode: func(ctx context.Context, code string) (*coredunning.Campaign, error) {
			return s.svc.GetByCode(ctx, org, code)
		},
	}
}

// message converts a campaign to its proto message with its customers
// count and whether the default billing entity applies it.
func (s *DunningCampaigns) message(ctx context.Context, c *coredunning.Campaign) (*dunningv1.DunningCampaign, error) {
	n, err := s.svc.CustomersCount(ctx, c)
	if err != nil {
		return nil, err
	}
	applied, err := s.svc.AppliedToOrganization(ctx, c)
	if err != nil {
		return nil, err
	}
	thresholds := make([]*dunningv1.DunningThreshold, len(c.Thresholds))
	for i, t := range c.Thresholds {
		thresholds[i] = dunningv1.DunningThreshold_builder{Id: new(t.ID.String()), Amount: Money(t.Amount, t.Currency)}.Build()
	}
	return dunningv1.DunningCampaign_builder{
		Id:                    new(c.ID.String()),
		Code:                  new(c.Code),
		Name:                  new(c.Name),
		Description:           c.Description,
		DaysBetweenAttempts:   new(int32(c.DaysBetweenAttempts)),
		MaxAttempts:           new(int32(c.MaxAttempts)),
		BccEmails:             c.BCCEmails,
		Thresholds:            thresholds,
		CustomersCount:        new(int32(n)),
		CreatedAt:             Timestamp(c.CreatedAt),
		UpdatedAt:             Timestamp(c.UpdatedAt),
		AppliedToOrganization: new(applied),
	}.Build(), nil
}

// dunThresholds converts the thresholds of a request, whose amounts are in
// major units, to minor units, with their IDs.
func dunThresholds(in []*dunningv1.DunningThreshold) ([]dunning.ThresholdInput, error) {
	out := make([]dunning.ThresholdInput, len(in))
	for i, t := range in {
		minor, c, err := FromMoney(fmt.Sprintf("dunning_campaign.thresholds[%d].amount", i), t.GetAmount())
		if err != nil {
			return nil, err
		}
		out[i] = dunning.ThresholdInput{Currency: c, Amount: minor}
		if t.HasId() {
			if out[i].ID, err = ParseID[core.DunningCampaignThresholdID](fmt.Sprintf("dunning_campaign.thresholds[%d].id", i), t.GetId()); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// dunListFilter parses the filter of ListDunningCampaigns: equalities on
// currency and applied_to_organization.
func dunListFilter(s string) (coredunning.Filter, error) {
	var f coredunning.Filter
	e, err := ParseFilter(s)
	if err != nil {
		return f, err
	}
	eq, err := Equalities(e, "currency", "applied_to_organization")
	if err != nil {
		return f, err
	}
	if f.Applied, err = FilterBool("applied_to_organization", eq["applied_to_organization"]); err != nil {
		return f, err
	}
	for _, v := range eq["currency"] {
		c, err := money.LookupCurrency(v)
		if err != nil {
			return f, filterError(fmt.Sprintf("unknown currency %q", v))
		}
		f.Currencies = append(f.Currencies, c.Code())
	}
	return f, nil
}

// dunError maps the service errors, whose field paths are the campaign's,
// onto the request's dunning_campaign message.
func dunError(ctx context.Context, err error) error {
	if v, ok := validationViolations("dunning_campaign.", err); ok {
		return withReason(InvalidArgument("invalid dunning campaign", v), ReasonValidationErrors)
	}
	return Error(ctx, err)
}
