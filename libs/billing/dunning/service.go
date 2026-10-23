package dunning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Resource is the resource name of the errors.
const Resource = "dunning_campaign"

// Service manages dunning campaigns.
type Service struct {
	d        *kernel.Deps
	payments *payments.Service
}

// New returns the service over d.
func New(d *kernel.Deps) *Service { return &Service{d: d, payments: payments.New(d)} }

// ThresholdInput is a threshold of a campaign: the overdue balance in a
// currency, in minor units, from which customers are dunned.
type ThresholdInput struct {
	// ID, on update, selects the campaign's threshold changed in place;
	// zero or any other ID adds a threshold. It is ignored on create.
	ID core.DunningCampaignThresholdID
	// Currency is the threshold's currency.
	Currency money.Currency
	// Amount is the minimum overdue balance, in minor units.
	Amount int64
}

// CreateInput is a campaign to create.
type CreateInput struct {
	// Code is unique among the organization's live campaigns.
	Code string
	// Name is the campaign's name.
	Name string
	// Description is free text.
	Description *string
	// DaysBetweenAttempts is the minimum number of days between attempts.
	DaysBetweenAttempts int
	// MaxAttempts is the number of attempts per currency.
	MaxAttempts int
	// BCCEmails are copied on every dunning email.
	BCCEmails []string
	// Thresholds are required, one per currency.
	Thresholds []ThresholdInput
	// AppliedToOrganization applies the campaign to the default billing
	// entity.
	AppliedToOrganization bool
}

// Create creates a campaign (lago DunningCampaigns::CreateService). With
// AppliedToOrganization, the default billing entity applies it and the
// attempts of the customers falling back to the entity are reset.
//
// source: app/services/dunning_campaigns/create_service.rb:15-36
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*dunning.Campaign, error) {
	if len(in.Thresholds) == 0 {
		return nil, fieldError("thresholds", core.CodeRequired)
	}
	now := s.d.Now()
	c := dunning.New(org, core.NewID[core.DunningCampaignID](s.d.IDs), in.Code, in.Name, now)
	c.Description = in.Description
	c.DaysBetweenAttempts = in.DaysBetweenAttempts
	c.MaxAttempts = in.MaxAttempts
	c.BCCEmails = in.BCCEmails
	s.setThresholds(c, in.Thresholds)
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*dunning.Campaign, error) {
		if err := validate(ctx, r, c); err != nil {
			return nil, err
		}
		if err := r.DunningCampaigns.Create(ctx, c); err != nil {
			return nil, codeConflict(err)
		}
		if in.AppliedToOrganization {
			if err := s.applyToOrganization(ctx, r, org, c.ID); err != nil {
				return nil, err
			}
		}
		return c, nil
	})
}

// UpdateInput changes a campaign; unset fields are kept.
type UpdateInput struct {
	// Code is the unique code.
	Code core.Optional[string]
	// Name is the campaign's name.
	Name core.Optional[string]
	// Description is free text.
	Description core.Optional[*string]
	// DaysBetweenAttempts is the minimum number of days between attempts.
	DaysBetweenAttempts core.Optional[int]
	// MaxAttempts is the number of attempts per currency.
	MaxAttempts core.Optional[int]
	// BCCEmails are copied on every dunning email.
	BCCEmails core.Optional[[]string]
	// Thresholds replace the campaign's thresholds, matched by ID; an
	// empty list removes them all.
	Thresholds core.Optional[[]ThresholdInput]
	// AppliedToOrganization applies the campaign to the default billing
	// entity (true) or removes it from there when applied (false).
	AppliedToOrganization core.Optional[bool]
}

// Update changes the live campaign id (lago DunningCampaigns::UpdateService).
// When a threshold is removed or an existing one changes, the customers
// dunned by the campaign whose overdue balances reach none of the new
// thresholds have their attempts reset. When AppliedToOrganization changes
// the default billing entity's campaign, the attempts of the customers
// falling back to the entity are reset.
//
// source: app/services/dunning_campaigns/update_service.rb:16-105
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.DunningCampaignID, in UpdateInput) (*dunning.Campaign, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*dunning.Campaign, error) {
		c, err := live(ctx, r, org, id)
		if err != nil {
			return nil, err
		}
		changed := in.Code.Apply(&c.Code)
		changed = in.Name.Apply(&c.Name) || changed
		changed = in.Description.Apply(&c.Description) || changed
		changed = in.DaysBetweenAttempts.Apply(&c.DaysBetweenAttempts) || changed
		changed = in.MaxAttempts.Apply(&c.MaxAttempts) || changed
		changed = in.BCCEmails.Apply(&c.BCCEmails) || changed
		var thresholdsUpdated bool
		if in.Thresholds.Set {
			thresholdsUpdated = s.setThresholds(c, in.Thresholds.Value)
			changed = true
		}
		if changed {
			c.UpdatedAt = s.d.Now()
			if err := validate(ctx, r, c); err != nil {
				return nil, err
			}
			if err := r.DunningCampaigns.Update(ctx, c); err != nil {
				return nil, codeConflict(err)
			}
		}
		if thresholdsUpdated {
			if err := s.resetUnmatched(ctx, r, c); err != nil {
				return nil, err
			}
		}
		if in.AppliedToOrganization.Set {
			applied := core.DunningCampaignID{}
			if in.AppliedToOrganization.Value {
				applied = c.ID
			}
			if err := s.applyToOrganization(ctx, r, org, applied); err != nil {
				return nil, err
			}
		}
		return c, nil
	})
}

// Delete soft-deletes the live campaign id: the customers it applies to
// lose it, the billing entities applying it lose it, and the attempts of
// both their customers are reset (lago DunningCampaigns::DestroyService).
// It returns the deleted campaign.
//
// source: app/services/dunning_campaigns/destroy_service.rb:13-25
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.DunningCampaignID) (*dunning.Campaign, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*dunning.Campaign, error) {
		c, err := live(ctx, r, org, id)
		if err != nil {
			return nil, err
		}
		now := s.d.Now()
		if _, err := r.Customers.ResetDunningCampaign(ctx, org, c.ID); err != nil {
			return nil, fmt.Errorf("reset customers of dunning campaign %s: %w", c.ID, err)
		}
		entities, err := applyingEntities(ctx, r, c)
		if err != nil {
			return nil, err
		}
		if err := eachFallback(ctx, r, org, entities, func(cu *customer.Customer) error {
			return resetAttempts(ctx, r, cu, now)
		}); err != nil {
			return nil, err
		}
		for _, be := range entities {
			be.AppliedDunningCampaignID = core.DunningCampaignID{}
			be.UpdatedAt = now
			if err := r.BillingEntities.Update(ctx, be); err != nil {
				return nil, fmt.Errorf("clear dunning campaign of billing entity %s: %w", be.ID, err)
			}
		}
		if err := c.Delete(now); err != nil {
			return nil, err
		}
		if err := r.DunningCampaigns.Update(ctx, c); err != nil {
			return nil, fmt.Errorf("delete dunning campaign %s: %w", c.ID, err)
		}
		return c, nil
	})
}

// Get returns the live campaign id.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.DunningCampaignID) (*dunning.Campaign, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*dunning.Campaign, error) {
		return live(ctx, r, org, id)
	})
}

// GetByCode returns the live campaign with code.
func (s *Service) GetByCode(ctx context.Context, org core.OrganizationID, code string) (*dunning.Campaign, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (*dunning.Campaign, error) {
		c, err := r.DunningCampaigns.GetByCode(ctx, org, code)
		return c, notFound(err)
	})
}

// List returns the live campaigns matching f by name (lago
// DunningCampaignsQuery: default order by name, search on name and code,
// currency and applied_to_organization filters). f.Applied compares with
// the campaign of the default billing entity, which sets f.AppliedID.
//
// source: app/queries/dunning_campaigns_query.rb:5-53
func (s *Service) List(ctx context.Context, org core.OrganizationID, f dunning.Filter, page core.PageRequest) (core.Page[*dunning.Campaign], error) {
	f.Order = core.OrderRecent
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*dunning.Campaign], error) {
		if f.Applied != nil {
			be, err := r.BillingEntities.GetDefault(ctx, org)
			if err != nil {
				return core.Page[*dunning.Campaign]{}, fmt.Errorf("load default billing entity: %w", err)
			}
			f.AppliedID = be.AppliedDunningCampaignID
		}
		p, err := r.DunningCampaigns.List(ctx, org, f, page)
		if err != nil {
			return core.Page[*dunning.Campaign]{}, fmt.Errorf("list dunning campaigns: %w", err)
		}
		return p, nil
	})
}

// CustomersCount returns the number of customers the campaign c dunns: the
// customers not excluded from dunning with c applied, and those without a
// campaign of their own whose billing entity applies c.
//
// source: app/graphql/types/dunning_campaigns/object.rb:29-44
func (s *Service) CustomersCount(ctx context.Context, c *dunning.Campaign) (int, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (int, error) {
		n := 0
		count := func(*customer.Customer) error { n++; return nil }
		if err := eachCustomer(ctx, r, c.OrganizationID, c.ID, count); err != nil {
			return 0, err
		}
		entities, err := applyingEntities(ctx, r, c)
		if err != nil {
			return 0, err
		}
		if err := eachFallback(ctx, r, c.OrganizationID, entities, count); err != nil {
			return 0, err
		}
		return n, nil
	})
}

// setThresholds replaces the thresholds of c with in: an input with the
// ID of one of c's thresholds changes it in place (the last such input
// wins), any other adds a threshold, and c's thresholds left out are
// removed. It reports whether an existing threshold was removed or
// changed.
//
// source: app/services/dunning_campaigns/update_service.rb:43-66
func (s *Service) setThresholds(c *dunning.Campaign, in []ThresholdInput) bool {
	old := map[core.DunningCampaignThresholdID]dunning.Threshold{}
	for _, t := range c.Thresholds {
		old[t.ID] = t
	}
	updated := false
	at := map[core.DunningCampaignThresholdID]int{}
	out := make([]dunning.Threshold, 0, len(in))
	for _, t := range in {
		prev, ok := old[t.ID]
		if !ok {
			out = append(out, dunning.Threshold{ID: core.NewID[core.DunningCampaignThresholdID](s.d.IDs), Currency: t.Currency, Amount: t.Amount})
			continue
		}
		next := dunning.Threshold{ID: t.ID, Currency: t.Currency, Amount: t.Amount}
		updated = updated || next.Currency.Code() != prev.Currency.Code() || next.Amount != prev.Amount
		if i, dup := at[t.ID]; dup {
			out[i] = next
			continue
		}
		at[t.ID] = len(out)
		out = append(out, next)
	}
	updated = updated || len(at) < len(old)
	c.Thresholds = out
	return updated
}

// applyToOrganization makes the default billing entity apply the campaign
// id (none when zero) and, when that changes the entity's campaign,
// resets the attempts of the customers falling back to it.
//
// source: app/services/dunning_campaigns/update_service.rb:98-105
func (s *Service) applyToOrganization(ctx context.Context, r port.Repos, org core.OrganizationID, id core.DunningCampaignID) error {
	be, err := r.BillingEntities.GetDefault(ctx, org)
	if err != nil {
		return fmt.Errorf("load default billing entity: %w", err)
	}
	if be.AppliedDunningCampaignID == id {
		return nil
	}
	now := s.d.Now()
	be.AppliedDunningCampaignID = id
	be.UpdatedAt = now
	if err := r.BillingEntities.Update(ctx, be); err != nil {
		return fmt.Errorf("apply dunning campaign to billing entity %s: %w", be.ID, err)
	}
	return eachFallback(ctx, r, org, map[core.BillingEntityID]*organization.BillingEntity{be.ID: be}, func(cu *customer.Customer) error {
		return resetAttempts(ctx, r, cu, now)
	})
}

// AppliedToOrganization reports whether the default billing entity
// applies the campaign c.
//
// source: app/graphql/types/dunning_campaigns/object.rb:24-26
func (s *Service) AppliedToOrganization(ctx context.Context, c *dunning.Campaign) (bool, error) {
	return inTx(ctx, s.d, func(ctx context.Context, r port.Repos) (bool, error) {
		be, err := r.BillingEntities.GetDefault(ctx, c.OrganizationID)
		if err != nil {
			return false, fmt.Errorf("load default billing entity: %w", err)
		}
		return be.AppliedDunningCampaignID == c.ID, nil
	})
}

// resetUnmatched resets the attempts of the customers c dunns that have
// overdue invoices whose balances reach none of c's thresholds.
//
// source: app/services/dunning_campaigns/update_service.rb:68-92
func (s *Service) resetUnmatched(ctx context.Context, r port.Repos, c *dunning.Campaign) error {
	now := s.d.Now()
	reset := func(cu *customer.Customer) error {
		balances, err := overdueBalances(ctx, r, cu)
		if err != nil || len(balances) == 0 {
			return err
		}
		for _, t := range c.Thresholds {
			if balances[t.Currency.Code()] >= t.Amount {
				return nil
			}
		}
		return resetAttempts(ctx, r, cu, now)
	}
	if err := eachCustomer(ctx, r, c.OrganizationID, c.ID, reset); err != nil {
		return err
	}
	entities, err := applyingEntities(ctx, r, c)
	if err != nil {
		return err
	}
	return eachFallback(ctx, r, c.OrganizationID, entities, reset)
}

// overdueBalances sums the total amounts of the customer's overdue
// invoices not self-billed, per currency code.
//
// source: app/models/customer.rb:361-364
func overdueBalances(ctx context.Context, r port.Repos, cu *customer.Customer) (map[string]int64, error) {
	out := map[string]int64{}
	overdue, selfBilled := true, false
	f := invoice.Filter{CustomerID: cu.ID, PaymentOverdue: &overdue, SelfBilled: &selfBilled}
	for page := (core.PageRequest{Size: core.MaxPageSize}); ; {
		p, err := r.Invoices.List(ctx, cu.OrganizationID, f, page)
		if err != nil {
			return nil, fmt.Errorf("list overdue invoices of customer %s: %w", cu.ID, err)
		}
		for _, inv := range p.Items {
			out[inv.Currency.Code()] += inv.TotalAmount
		}
		if p.NextToken == "" {
			return out, nil
		}
		page.Token = p.NextToken
	}
}

// resetAttempts clears the customer's dunning attempts.
func resetAttempts(ctx context.Context, r port.Repos, cu *customer.Customer, now time.Time) error {
	if len(cu.DunningCampaignAttempts) == 0 && cu.LastDunningCampaignAttemptAt == nil {
		return nil
	}
	cu.DunningCampaignAttempts = map[string]int{}
	cu.LastDunningCampaignAttemptAt = nil
	cu.UpdatedAt = now
	if err := r.Customers.Update(ctx, cu); err != nil {
		return fmt.Errorf("reset dunning attempts of customer %s: %w", cu.ID, err)
	}
	return nil
}

// applyingEntities returns the billing entities applying c.
func applyingEntities(ctx context.Context, r port.Repos, c *dunning.Campaign) (map[core.BillingEntityID]*organization.BillingEntity, error) {
	out := map[core.BillingEntityID]*organization.BillingEntity{}
	f := organization.BillingEntityFilter{IncludeArchived: true}
	for page := (core.PageRequest{Size: core.MaxPageSize}); ; {
		p, err := r.BillingEntities.List(ctx, c.OrganizationID, f, page)
		if err != nil {
			return nil, fmt.Errorf("list billing entities: %w", err)
		}
		for _, be := range p.Items {
			if be.AppliedDunningCampaignID == c.ID {
				out[be.ID] = be
			}
		}
		if p.NextToken == "" {
			return out, nil
		}
		page.Token = p.NextToken
	}
}

// eachFallback calls fn with every customer not excluded from dunning and
// without a campaign of its own whose billing entity is in entities.
func eachFallback(ctx context.Context, r port.Repos, org core.OrganizationID, entities map[core.BillingEntityID]*organization.BillingEntity, fn func(*customer.Customer) error) error {
	if len(entities) == 0 {
		return nil
	}
	return eachCustomer(ctx, r, org, core.DunningCampaignID{}, func(cu *customer.Customer) error {
		if entities[cu.BillingEntityID] == nil {
			return nil
		}
		return fn(cu)
	})
}

// eachCustomer calls fn with every customer not excluded from dunning
// whose own campaign is campaign (none when zero).
func eachCustomer(ctx context.Context, r port.Repos, org core.OrganizationID, campaign core.DunningCampaignID, fn func(*customer.Customer) error) error {
	for page := (core.PageRequest{Size: core.MaxPageSize}); ; {
		p, err := r.Customers.ListForDunning(ctx, org, campaign, page)
		if err != nil {
			return fmt.Errorf("list customers: %w", err)
		}
		for _, cu := range p.Items {
			if err := fn(cu); err != nil {
				return err
			}
		}
		if p.NextToken == "" {
			return nil
		}
		page.Token = p.NextToken
	}
}

// live loads the campaign id unless it was deleted.
func live(ctx context.Context, r port.Repos, org core.OrganizationID, id core.DunningCampaignID) (*dunning.Campaign, error) {
	c, err := r.DunningCampaigns.Get(ctx, org, id)
	if err != nil {
		return nil, notFound(err)
	}
	if c.Deleted() {
		return nil, core.NotFound(Resource)
	}
	return c, nil
}

// validate checks c and the uniqueness of its code.
func validate(ctx context.Context, r port.Repos, c *dunning.Campaign) error {
	if err := c.Validate(); err != nil {
		return err
	}
	other, err := r.DunningCampaigns.GetByCode(ctx, c.OrganizationID, c.Code)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("load dunning campaign %q: %w", c.Code, err)
	case other.ID != c.ID:
		return fieldError("code", core.CodeAlreadyExists)
	}
	return nil
}

func inTx[T any](ctx context.Context, d *kernel.Deps, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// notFound turns a missing campaign into core.NotFound(Resource).
func notFound(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(Resource)
	}
	return err
}

// codeConflict turns a uniqueness violation, which a concurrent write can
// still cause after validate, into code value_already_exist.
func codeConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		return fieldError("code", core.CodeAlreadyExists)
	}
	return err
}

func fieldError(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}
