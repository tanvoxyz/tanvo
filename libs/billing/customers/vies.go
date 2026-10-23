package customers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// JobVIESCheck is the job kind checking a customer's VAT number with VIES
// (lago Customers::ViesCheckJob). It runs on [port.QueueDefault].
const JobVIESCheck = "customers.vies_check"

// VIESCheckArgs are the arguments of [JobVIESCheck].
type VIESCheckArgs struct {
	// CustomerID is the checked customer.
	CustomerID core.CustomerID `json:"customer_id"`
}

// VIESInvoiceFinalizer finalizes the invoices whose taxes waited for a
// customer's VIES check (lago Invoices::FinalizePendingViesInvoiceJob).
// Inside its transaction the finalization applies the invoice custom
// sections with kernel.ApplyCustomSections(ctx, d, r, cenv, inv, nil, nil),
// as lago finalize_pending_vies_invoice_service.rb:25 does, after computing
// the amounts and before the credits.
type VIESInvoiceFinalizer interface {
	// EnqueueFinalizePendingVIES queues, in r's transaction, the
	// finalization of inv, an open invoice whose taxes are pending.
	EnqueueFinalizePendingVIES(ctx context.Context, r port.Repos, inv *invoice.Invoice) error
}

// WithVIES checks VAT numbers with c in [JobVIESCheck].
func WithVIES(c tax.VIESChecker) Option { return func(s *Service) { s.vies = c } }

// WithVIESInvoiceFinalizer finalizes, after a VIES check, the customer's
// invoices waiting for it; without one they are left as they are.
func WithVIESInvoiceFinalizer(f VIESInvoiceFinalizer) Option {
	return func(s *Service) { s.viesInvoices = f }
}

// viesAction is what a customer write does to the customer's pending VIES
// check.
type viesAction int

const (
	// viesKeep leaves it.
	viesKeep viesAction = iota
	// viesSchedule (re)schedules it.
	viesSchedule
	// viesCancel deletes it.
	viesCancel
)

// VIESCheckPayload is the payload of customer.vies_check: the customer
// and the outcome of the check.
//
// source: app/services/webhooks/customers/vies_check_service.rb:12-19
type VIESCheckPayload struct {
	*customer.Customer
	// VIESCheck is VIES's lookup ([tax.VIESLookup]) or a [VIESCheckFailure].
	VIESCheck any `json:"vies_check"`
}

// VIESCheckFailure is the outcome of a VIES check that found no valid
// number or failed.
type VIESCheckFailure struct {
	// Valid is always false.
	Valid bool `json:"valid"`
	// ValidFormat reports whether the number is well formed.
	ValidFormat bool `json:"valid_format"`
	// Error is the VIES failure, empty when VIES answered.
	Error string `json:"error,omitempty"`
}

// applyVIES schedules or cancels the pending VIES check of c, once c is
// stored.
//
// source: app/services/customers/eu_auto_taxes_service.rb:91-105
func (s *Service) applyVIES(ctx context.Context, r port.Repos, c *customer.Customer, a viesAction) error {
	switch a {
	case viesSchedule:
		now := s.d.Now()
		p := &customer.PendingVIESCheck{
			OrganizationID:          c.OrganizationID,
			CustomerID:              c.ID,
			BillingEntityID:         c.BillingEntityID,
			TaxIdentificationNumber: c.TaxIdentificationNumber,
			CreatedAt:               now,
			UpdatedAt:               now,
		}
		if err := r.PendingVIESChecks.Save(ctx, p); err != nil {
			return fmt.Errorf("save pending vies check of %s: %w", c.ID, err)
		}
		return enqueueVIESCheck(ctx, r, c, 0, time.Time{})
	case viesCancel:
		if _, err := r.PendingVIESChecks.Delete(ctx, c.OrganizationID, c.ID); err != nil {
			return fmt.Errorf("delete pending vies check of %s: %w", c.ID, err)
		}
	}
	return nil
}

// enqueueVIESCheck queues the [JobVIESCheck] of c. A first check is unique
// per customer while pending or running; a retry, queued from the running
// check, is unique per attempt.
func enqueueVIESCheck(ctx context.Context, r port.Repos, c *customer.Customer, attempt int, runAt time.Time) error {
	raw, err := json.Marshal(VIESCheckArgs{CustomerID: c.ID})
	if err != nil {
		return fmt.Errorf("encode %s args: %w", JobVIESCheck, err)
	}
	key := "customer:" + c.ID.String()
	if attempt > 0 {
		key += ":attempt:" + strconv.Itoa(attempt)
	}
	j := port.Job{Kind: JobVIESCheck, Queue: port.QueueDefault, OrganizationID: c.OrganizationID, Args: raw, UniqueKey: key, RunAt: runAt}
	if err := r.Jobs.Enqueue(ctx, j); err != nil {
		return fmt.Errorf("enqueue %s: %w", JobVIESCheck, err)
	}
	return nil
}

// CheckVIES checks the VAT number of customer id with VIES, for a billing
// entity managing EU VAT (otherwise it does nothing), and sends
// customer.vies_check:
//
//   - when VIES answers, the customer's taxes become the single EU tax the
//     answer decides (left as they are when that tax does not exist), its
//     draft invoices are flagged for refresh, its pending check is deleted
//     and its open invoices waiting for taxes are finalized;
//   - when VIES fails, the pending check records the failure and the check
//     is retried after 5, 5, 10, 20 and 40 minutes, then hourly;
//   - an unknown VIES fault, or VIES out of reach, fails the job.
//
// source: app/jobs/customers/vies_check_job.rb:22-62
// source: app/services/customers/vies_check_service.rb:15-91
// source: app/services/customers/apply_taxes_service.rb:14-30
func (s *Service) CheckVIES(ctx context.Context, org core.OrganizationID, id core.CustomerID) error {
	if s.vies == nil {
		return fmt.Errorf("%w: VIES checker", kernel.ErrMissingDependency)
	}
	type facts struct {
		c  *customer.Customer
		be *organization.BillingEntity
	}
	f, err := read(ctx, s.d, func(ctx context.Context, r port.Repos) (facts, error) {
		c, err := r.Customers.Get(ctx, org, id)
		if err != nil {
			return facts{}, notFound(err)
		}
		be, err := r.BillingEntities.Get(ctx, org, c.BillingEntityID)
		if err != nil {
			return facts{}, fmt.Errorf("load billing entity %s: %w", c.BillingEntityID, err)
		}
		return facts{c, be}, nil
	})
	if err != nil {
		return fmt.Errorf("vies check of customer %s: %w", id, err)
	}
	if !f.be.EUTaxManagement {
		return nil
	}
	tin := value(f.c.TaxIdentificationNumber)
	var lookup *tax.VIESLookup
	if tin != "" {
		lookup, err = s.vies.CheckVAT(ctx, tin)
	}
	var ve *tax.VIESError
	if err != nil && (!errors.As(err, &ve) || ve.Type == tax.VIESErrorUnknown) {
		return fmt.Errorf("vies check of customer %s: %w", id, err)
	}
	err = s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Customers.Get(ctx, org, id)
		if err != nil {
			return notFound(err)
		}
		if ve != nil {
			return s.viesFailed(ctx, r, c, tin, ve)
		}
		return s.viesChecked(ctx, r, c, f.be, tin, lookup)
	})
	if err != nil {
		return fmt.Errorf("vies check of customer %s: %w", id, err)
	}
	return nil
}

func (s *Service) viesFailed(ctx context.Context, r port.Repos, c *customer.Customer, tin string, ve *tax.VIESError) error {
	now := s.d.Now()
	p, err := r.PendingVIESChecks.Get(ctx, c.OrganizationID, c.ID)
	switch {
	case errors.Is(err, core.ErrNotFound):
		p = &customer.PendingVIESCheck{OrganizationID: c.OrganizationID, CustomerID: c.ID, CreatedAt: now}
	case err != nil:
		return fmt.Errorf("load pending vies check of %s: %w", c.ID, err)
	}
	t := string(ve.Type)
	p.BillingEntityID = c.BillingEntityID
	p.TaxIdentificationNumber = c.TaxIdentificationNumber
	p.AttemptsCount++
	p.LastAttemptAt = &now
	p.LastErrorType = &t
	p.LastErrorMessage = &ve.Message
	p.UpdatedAt = now
	if err := r.PendingVIESChecks.Save(ctx, p); err != nil {
		return fmt.Errorf("save pending vies check of %s: %w", c.ID, err)
	}
	failure := VIESCheckFailure{ValidFormat: tax.ValidVATNumberSyntax(tin), Error: ve.Message}
	if err := s.emitVIESCheck(ctx, c, failure); err != nil {
		return err
	}
	return enqueueVIESCheck(ctx, r, c, p.AttemptsCount, now.Add(tax.VIESRetryDelay(p.AttemptsCount)))
}

func (s *Service) viesChecked(ctx context.Context, r port.Repos, c *customer.Customer, be *organization.BillingEntity, tin string, lookup *tax.VIESLookup) error {
	in := tax.EUTaxInput{
		BillingCountry: be.Address.Country,
		Customer: tax.EUCustomer{
			Country:                 c.BillingAddress.Country,
			Zipcode:                 c.BillingAddress.Zipcode,
			TaxIdentificationNumber: tin,
		},
		At: s.d.Now(),
	}
	var (
		vies  tax.VIESResult
		check any = VIESCheckFailure{ValidFormat: tax.ValidVATNumberSyntax(tin)}
	)
	if lookup != nil {
		vies = tax.VIESResult{Valid: true, CountryCode: lookup.CountryCode}
		check = lookup
	}
	code := tax.ResolveEUTaxAfterVIES(in, vies)
	if err := s.emitVIESCheck(ctx, c, check); err != nil {
		return err
	}
	if _, err := r.PendingVIESChecks.Delete(ctx, c.OrganizationID, c.ID); err != nil {
		return fmt.Errorf("delete pending vies check of %s: %w", c.ID, err)
	}
	t, err := r.Taxes.GetByCode(ctx, c.OrganizationID, code)
	switch {
	case errors.Is(err, core.ErrNotFound):
		// lago's job ignores the failure of ApplyTaxesService.
	case err != nil:
		return fmt.Errorf("load tax %s: %w", code, err)
	default:
		prev := clone(c)
		if !slices.Equal(c.TaxIDs, []core.TaxID{t.ID}) {
			c.TaxIDs = []core.TaxID{t.ID}
			if err := r.Customers.Update(ctx, c); err != nil {
				return fmt.Errorf("update customer %s: %w", c.ID, err)
			}
		}
		if err := s.updateDrafts(ctx, r, &prev, c, Input{}, true); err != nil {
			return err
		}
	}
	return s.finalizePendingVIES(ctx, r, c)
}

// finalizePendingVIES finalizes the open invoices of c whose taxes are
// pending.
//
// source: app/jobs/customers/vies_check_job.rb:46-57
func (s *Service) finalizePendingVIES(ctx context.Context, r port.Repos, c *customer.Customer) error {
	if s.viesInvoices == nil {
		return nil
	}
	f := invoice.Filter{CustomerID: c.ID, Statuses: []invoice.Status{invoice.StatusOpen}, IncludeUnlisted: true}
	invs, err := kernel.All(func(page core.PageRequest) (core.Page[*invoice.Invoice], error) {
		return r.Invoices.List(ctx, c.OrganizationID, f, page)
	})
	if err != nil {
		return fmt.Errorf("list open invoices: %w", err)
	}
	for _, inv := range invs {
		if inv.TaxStatus != invoice.TaxStatusPending {
			continue
		}
		if err := s.viesInvoices.EnqueueFinalizePendingVIES(ctx, r, inv); err != nil {
			return fmt.Errorf("finalize pending vies invoice %s: %w", inv.ID, err)
		}
	}
	return nil
}

func (s *Service) emitVIESCheck(ctx context.Context, c *customer.Customer, check any) error {
	return kernel.Emit(ctx, s.d, c.OrganizationID, webhook.EventTypeCustomerVIESCheck, Resource, c.ID.UUID(), VIESCheckPayload{Customer: c, VIESCheck: check})
}
