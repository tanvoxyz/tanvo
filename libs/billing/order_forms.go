package billing

import (
	"context"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// Rule codes of [Engine.ExpireOrderForm], carried by *core.RuleError (lago
// OrderForms::ExpireService forbidden_failure codes).
const (
	// CodeOrderFormIsVoided: a voided order form does not expire.
	CodeOrderFormIsVoided = "order_form_is_voided"
	// CodeOrderFormIsSigned: a signed order form does not expire.
	CodeOrderFormIsSigned = "order_form_is_signed"
)

// ExpireOrderForms enqueues the [kernel.JobExpireOrderForm] of every
// generated order form of org whose expiry date, in its customer's time
// zone, is the date of the clock instant at or earlier, and returns their
// number (lago Clock::ExpireOrderFormsJob and OrderForm.expirable,
// app/jobs/clock/expire_order_forms_job.rb:7-11,
// app/models/order_form.rb:49-56).
func (e *Engine) ExpireOrderForms(ctx context.Context, org core.OrganizationID, at time.Time) (int, error) {
	n := 0
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		locs := map[core.CustomerID]*time.Location{}
		page := core.PageRequest{Size: sweepPage}
		for {
			p, err := r.OrderForms.List(ctx, org, quote.OrderFormFilter{Statuses: []quote.OrderFormStatus{quote.OrderFormStatusGenerated}}, page)
			if err != nil {
				return fmt.Errorf("list generated order forms: %w", err)
			}
			for _, f := range p.Items {
				if f.ExpiresAt == nil {
					continue
				}
				loc, ok := locs[f.CustomerID]
				if !ok {
					env, err := kernel.LoadCustomerEnv(ctx, r, org, f.CustomerID, core.BillingEntityID{})
					if err != nil {
						return err
					}
					loc = env.Location
					locs[f.CustomerID] = loc
				}
				if !f.Expirable(at, loc) {
					continue
				}
				args := kernel.OrderFormArgs{OrderFormID: f.ID}
				if err := kernel.Enqueue(ctx, r, org, kernel.JobExpireOrderForm, args, kernel.ExpireOrderFormKey(f.ID), time.Time{}); err != nil {
					return err
				}
				n++
			}
			if p.NextToken == "" {
				return nil
			}
			page.Token = p.NextToken
		}
	})
	return n, err
}

// ExpireOrderForm expires order form id at now, voids its quote version as
// cascade_of_expired and emits order_form.expired then quote.voided (lago
// OrderForms::ExpireService and QuoteVersions::VoidService,
// app/services/order_forms/expire_service.rb:15-41,
// app/services/quote_versions/void_service.rb:17-41). It runs under the
// lock of the quote. A voided or signed order form is refused with a
// *core.RuleError; an expired one is returned unchanged.
func (e *Engine) ExpireOrderForm(ctx context.Context, org core.OrganizationID, id core.OrderFormID) (*quote.OrderForm, error) {
	var out *quote.OrderForm
	err := e.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.OrderForms.Get(ctx, org, id)
		if err != nil {
			return fmt.Errorf("load order form %s: %w", id, err)
		}
		v, err := r.QuoteVersions.Get(ctx, org, f.QuoteVersionID)
		if err != nil {
			return fmt.Errorf("load quote version %s: %w", f.QuoteVersionID, err)
		}
		if err := lockQuote(ctx, r, org, v.QuoteID); err != nil {
			return err
		}
		// order_form.reload under the lock.
		if f, err = r.OrderForms.Get(ctx, org, id); err != nil {
			return fmt.Errorf("reload order form %s: %w", id, err)
		}
		out = f
		switch f.Status {
		case quote.OrderFormStatusVoided:
			return core.NewRuleError(CodeOrderFormIsVoided, "the order form is voided")
		case quote.OrderFormStatusSigned:
			return core.NewRuleError(CodeOrderFormIsSigned, "the order form is signed")
		case quote.OrderFormStatusExpired:
			return nil
		}
		if v, err = r.QuoteVersions.Get(ctx, org, f.QuoteVersionID); err != nil {
			return fmt.Errorf("reload quote version %s: %w", f.QuoteVersionID, err)
		}
		q, err := r.Quotes.Get(ctx, org, v.QuoteID)
		if err != nil {
			return fmt.Errorf("load quote %s: %w", v.QuoteID, err)
		}
		if err := f.Expire(v, e.d.Now()); err != nil {
			return fmt.Errorf("expire order form %s: %w", id, err)
		}
		if err := r.OrderForms.Update(ctx, f); err != nil {
			return fmt.Errorf("update order form %s: %w", id, err)
		}
		if err := r.QuoteVersions.Update(ctx, v); err != nil {
			return fmt.Errorf("update quote version %s: %w", v.ID, err)
		}
		if err := kernel.Emit(ctx, e.d, org, webhook.EventTypeOrderFormExpired, "order_form", f.ID.UUID(), f); err != nil {
			return err
		}
		return kernel.Emit(ctx, e.d, org, webhook.EventTypeQuoteVoided, "quote", v.ID.UUID(), quoteWithVersion{Quote: q, Version: v})
	})
	if err != nil {
		return nil, fmt.Errorf("expire order form %s: %w", id, err)
	}
	return out, nil
}

// quoteWithVersion is the payload of the quote webhooks: a quote with one
// of its versions (lago V1::QuoteWithVersionSerializer).
type quoteWithVersion struct {
	Quote   *quote.Quote   `json:"quote"`
	Version *quote.Version `json:"version"`
}

// lockQuote takes the lock serializing the changes of quote id, its
// versions, order forms and orders (lago Quotes::LockService,
// app/services/quotes/lock_service.rb).
func lockQuote(ctx context.Context, r port.Repos, org core.OrganizationID, id core.QuoteID) error {
	if err := r.Locks.Lock(ctx, org, port.LockKey{Scope: "quote", ID: id.UUID()}); err != nil {
		return fmt.Errorf("lock quote %s: %w", id, err)
	}
	return nil
}
