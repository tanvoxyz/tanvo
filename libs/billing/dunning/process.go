package dunning

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/payments"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/dunning"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// ProcessCampaigns enqueues the [kernel.JobProcessDunningCustomer] of
// every live customer of org not excluded from dunning that has overdue
// invoices which are not self-billed (lago DunningCampaigns::BulkProcessService),
// once per customer while one is pending.
//
// source: app/services/dunning_campaigns/bulk_process_service.rb:7-31
func (s *Service) ProcessCampaigns(ctx context.Context, org core.OrganizationID, at time.Time) error {
	if at.IsZero() {
		return fmt.Errorf("process dunning campaigns: zero instant: %w", core.ErrInvalidValue)
	}
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		ids := map[core.CustomerID]bool{}
		overdue, selfBilled := true, false
		f := invoice.Filter{PaymentOverdue: &overdue, SelfBilled: &selfBilled}
		for page := (core.PageRequest{Size: core.MaxPageSize}); ; {
			p, err := r.Invoices.List(ctx, org, f, page)
			if err != nil {
				return fmt.Errorf("list overdue invoices: %w", err)
			}
			for _, inv := range p.Items {
				ids[inv.CustomerID] = true
			}
			if p.NextToken == "" {
				break
			}
			page.Token = p.NextToken
		}
		for _, id := range slices.SortedFunc(maps.Keys(ids), func(a, b core.CustomerID) int { return a.Compare(b) }) {
			cu, err := r.Customers.Get(ctx, org, id)
			switch {
			case errors.Is(err, core.ErrNotFound):
				continue
			case err != nil:
				return fmt.Errorf("load customer %s: %w", id, err)
			}
			// Archived customers are out of the default scope the eligible
			// customers query and the jobs load through
			// (app/models/customer.rb:139, bulk_process_service.rb:19-28).
			if cu.Deleted() || cu.ExcludeFromDunningCampaign {
				continue
			}
			if err := kernel.Enqueue(ctx, r, org, kernel.JobProcessDunningCustomer, kernel.CustomerArgs{CustomerID: id},
				kernel.DunningCustomerKey(id), time.Time{}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ProcessCustomer makes the dunning attempt of a customer (lago
// DunningCampaigns::ProcessCustomerService): under the campaign that
// applies to it, once [dunning.Campaign.DaysBetweenAttempts] days have
// passed since its last attempt, every currency below the campaign's
// maximum attempts whose overdue balance reaches its threshold is dunned.
// The customer's counters and last attempt are stored, a
// [kernel.JobProcessDunningAttempt] is enqueued per dunned threshold and
// billing entity holding overdue invoices ready for payment processing in
// its currency, and dunning_campaign.finished is emitted once every dunned
// currency reached the maximum.
//
// source: app/services/dunning_campaigns/process_customer_service.rb:15-46
func (s *Service) ProcessCustomer(ctx context.Context, org core.OrganizationID, id core.CustomerID) error {
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cu, err := r.Customers.Get(ctx, org, id)
		switch {
		case errors.Is(err, core.ErrNotFound):
			return nil
		case err != nil:
			return fmt.Errorf("load customer %s: %w", id, err)
		}
		if cu.Deleted() || cu.ExcludeFromDunningCampaign {
			return nil
		}
		c, err := applicableCampaign(ctx, r, cu)
		if err != nil || c == nil {
			return err
		}
		balances, err := overdueBalances(ctx, r, cu)
		if err != nil {
			return err
		}
		now := s.d.Now()
		a := c.Plan(dunning.CustomerState{Attempts: cu.DunningCampaignAttempts, LastAttemptAt: cu.LastDunningCampaignAttemptAt}, balances, now)
		if !a.Due() {
			return nil
		}
		cu.DunningCampaignAttempts = a.Attempts
		cu.LastDunningCampaignAttemptAt = &now
		cu.UpdatedAt = now
		if err := r.Customers.Update(ctx, cu); err != nil {
			return fmt.Errorf("store dunning attempts of customer %s: %w", cu.ID, err)
		}
		invs, err := dunnableInvoices(ctx, r, cu)
		if err != nil {
			return err
		}
		for _, t := range a.Thresholds {
			var entities []core.BillingEntityID
			for _, inv := range invs {
				if inv.Currency.Code() == t.Currency.Code() && !slices.Contains(entities, inv.BillingEntityID) {
					entities = append(entities, inv.BillingEntityID)
				}
			}
			for _, be := range entities {
				args := kernel.DunningAttemptArgs{CustomerID: cu.ID, DunningCampaignID: c.ID, ThresholdID: t.ID, BillingEntityID: be}
				if err := kernel.Enqueue(ctx, r, org, kernel.JobProcessDunningAttempt, args, "", time.Time{}); err != nil {
					return err
				}
			}
		}
		if !a.Finished {
			return nil
		}
		return kernel.Emit(ctx, s.d, org, webhook.EventTypeDunningCampaignFinished, Resource, cu.ID.UUID(), finishedPayload(cu, c, balances))
	})
}

// ProcessAttempt asks a customer to pay its overdue invoices of one
// billing entity in a threshold's currency (lago
// DunningCampaigns::ProcessAttemptService): nothing when the campaign no
// longer applies to the customer, the threshold is gone or the customer's
// overdue invoices ready for payment processing in its currency no longer
// reach it; otherwise a payment request of the campaign
// ([payments.Service.CreateRequest]), whose failure fails the job.
//
// source: app/services/dunning_campaigns/process_attempt_service.rb:18-41
func (s *Service) ProcessAttempt(ctx context.Context, org core.OrganizationID, args kernel.DunningAttemptArgs) error {
	var in *payments.RequestInput
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		in = nil
		cu, err := r.Customers.Get(ctx, org, args.CustomerID)
		switch {
		case errors.Is(err, core.ErrNotFound):
			return nil
		case err != nil:
			return fmt.Errorf("load customer %s: %w", args.CustomerID, err)
		}
		if cu.Deleted() {
			return nil
		}
		c, err := applicableCampaign(ctx, r, cu)
		if err != nil || c == nil || c.ID != args.DunningCampaignID {
			return err
		}
		i := slices.IndexFunc(c.Thresholds, func(t dunning.Threshold) bool { return t.ID == args.ThresholdID })
		if i < 0 {
			return nil
		}
		t := c.Thresholds[i]
		invs, err := dunnableInvoices(ctx, r, cu)
		if err != nil {
			return err
		}
		var total int64
		var ids []core.InvoiceID
		for _, inv := range invs {
			if inv.Currency.Code() != t.Currency.Code() {
				continue
			}
			total += inv.TotalAmount
			if inv.BillingEntityID == args.BillingEntityID {
				ids = append(ids, inv.ID)
			}
		}
		if total < t.Amount {
			return nil
		}
		in = &payments.RequestInput{OrganizationID: org, Customer: payments.CustomerRef{ID: cu.ID}, InvoiceIDs: ids, DunningCampaignID: c.ID}
		return nil
	})
	if err != nil || in == nil {
		return err
	}
	if _, err := s.payments.CreateRequest(ctx, *in); err != nil {
		return fmt.Errorf("dunning payment request of customer %s: %w", args.CustomerID, err)
	}
	return nil
}

// applicableCampaign returns the live campaign applying to cu (its own, or
// its billing entity's), or nil.
func applicableCampaign(ctx context.Context, r port.Repos, cu *customer.Customer) (*dunning.Campaign, error) {
	be, err := r.BillingEntities.Get(ctx, cu.OrganizationID, cu.BillingEntityID)
	if err != nil {
		return nil, fmt.Errorf("load billing entity %s: %w", cu.BillingEntityID, err)
	}
	id := dunning.Applicable(cu.ExcludeFromDunningCampaign, cu.AppliedDunningCampaignID, be.AppliedDunningCampaignID)
	if id.IsZero() {
		return nil, nil
	}
	c, err := r.DunningCampaigns.Get(ctx, cu.OrganizationID, id)
	switch {
	case errors.Is(err, core.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("load dunning campaign %s: %w", id, err)
	case c.DeletedAt != nil:
		return nil, nil
	}
	return c, nil
}

// dunnableInvoices returns the overdue invoices of cu, not self-billed,
// that are ready for payment processing.
func dunnableInvoices(ctx context.Context, r port.Repos, cu *customer.Customer) ([]*invoice.Invoice, error) {
	var out []*invoice.Invoice
	overdue, selfBilled := true, false
	f := invoice.Filter{CustomerID: cu.ID, PaymentOverdue: &overdue, SelfBilled: &selfBilled}
	for page := (core.PageRequest{Size: core.MaxPageSize}); ; {
		p, err := r.Invoices.List(ctx, cu.OrganizationID, f, page)
		if err != nil {
			return nil, fmt.Errorf("list overdue invoices of customer %s: %w", cu.ID, err)
		}
		for _, inv := range p.Items {
			if inv.ReadyForPaymentProcessing {
				out = append(out, inv)
			}
		}
		if p.NextToken == "" {
			return out, nil
		}
		page.Token = p.NextToken
	}
}

// FinishedPayload is the payload of dunning_campaign.finished.
//
// source: app/serializers/v1/dunning_campaign_finished_serializer.rb:5-14
type FinishedPayload struct {
	// ExternalCustomerID is the customer's external ID.
	ExternalCustomerID string `json:"external_customer_id"`
	// DunningCampaignCode is the finished campaign's code.
	DunningCampaignCode string `json:"dunning_campaign_code"`
	// OverdueBalanceCents is the overdue balance in the customer's
	// currency, in minor units.
	OverdueBalanceCents int64 `json:"overdue_balance_cents"`
	// OverdueBalanceCurrency is the customer's currency; nil when it has
	// none.
	OverdueBalanceCurrency *string `json:"overdue_balance_currency"`
	// OverdueBalances are the overdue balances per currency, ordered by
	// currency code.
	OverdueBalances []OverdueBalance `json:"overdue_balances"`
	// CustomerExternalID repeats ExternalCustomerID; deprecated.
	CustomerExternalID string `json:"customer_external_id"`
}

// OverdueBalance is the overdue balance of a customer in one currency.
type OverdueBalance struct {
	// Currency is the currency code.
	Currency string `json:"currency"`
	// AmountCents is the balance in minor units.
	AmountCents int64 `json:"amount_cents"`
}

func finishedPayload(cu *customer.Customer, c *dunning.Campaign, balances map[string]int64) FinishedPayload {
	p := FinishedPayload{
		ExternalCustomerID: cu.ExternalID, DunningCampaignCode: c.Code, OverdueBalances: []OverdueBalance{},
		CustomerExternalID: cu.ExternalID,
	}
	if !cu.Currency.IsZero() {
		code := cu.Currency.Code()
		p.OverdueBalanceCurrency = &code
		p.OverdueBalanceCents = balances[code]
	}
	for _, code := range slices.Sorted(maps.Keys(balances)) {
		p.OverdueBalances = append(p.OverdueBalances, OverdueBalance{Currency: code, AmountCents: balances[code]})
	}
	return p
}
