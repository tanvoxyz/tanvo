package quotes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/quotes/validators"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// UpdateVersionInput changes a draft version (lago
// QuoteVersions::UpdateService); the fields set are the ones updated.
type UpdateVersionInput struct {
	// BillingItems replace the quoted items; nil clears them.
	BillingItems core.Optional[json.RawMessage]
	// Content replaces the document; nil clears it.
	Content core.Optional[*string]
	// Currency replaces the deal currency.
	Currency core.Optional[money.Currency]
	// BillingEntityID replaces the issuer; zero clears it.
	BillingEntityID core.Optional[core.BillingEntityID]
}

// UpdateVersion changes draft version id of quote q (lago
// QuoteVersions::UpdateService,
// app/services/quote_versions/update_service.rb:22-43): status
// not_editable unless draft, currency not_supported_for_order_type when an
// amendment changes it, then the billing items are realigned on the deal
// currency and validated for update.
func (s *Service) UpdateVersion(ctx context.Context, org core.OrganizationID, q core.QuoteID, id core.QuoteVersionID, in UpdateVersionInput) (*VersionView, error) {
	var out *VersionView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := loadVersion(ctx, r, org, q, id)
		if err != nil {
			return err
		}
		qt, err := r.Quotes.Get(ctx, org, v.QuoteID)
		if err != nil {
			return fmt.Errorf("load quote %s: %w", v.QuoteID, err)
		}
		if v.Status != quote.VersionStatusDraft {
			return fieldError("status", CodeNotEditable)
		}
		in.BillingItems.Apply(&v.BillingItems)
		in.Content.Apply(&v.Content)
		in.BillingEntityID.Apply(&v.BillingEntityID)
		if in.Currency.Set && in.Currency.Value != v.Currency {
			if qt.OrderType == quote.OrderTypeSubscriptionAmendment {
				return fieldError("currency", CodeNotSupportedForOrderType)
			}
			v.Currency = in.Currency.Value
		}
		if err := realignCurrency(ctx, r, v); err != nil {
			return err
		}
		if err := s.validateVersion(ctx, r, qt, v, validators.ScopeUpdate); err != nil {
			return err
		}
		v.UpdatedAt = s.d.Now()
		if err := r.QuoteVersions.Update(ctx, v); err != nil {
			return fmt.Errorf("update quote version %s: %w", id, err)
		}
		out, err = versionView(ctx, r, v)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("update quote version %s: %w", id, err)
	}
	return out, nil
}

// realignCurrency is lago realign_billing_items_currency!
// (update_service.rb:70-170): the amountCurrency override of each plan
// and fixed amount coupon states the deal currency only while the catalog
// record is priced in another one, and each wallet credit stating a
// currency states the deal's. Shapes and values the validators would
// refuse are left as they are.
func realignCurrency(ctx context.Context, r port.Repos, v *quote.Version) error {
	cur := v.Currency.Code()
	if !validators.CurrencyListed(cur) {
		return nil
	}
	items := decodeItems(v.BillingItems)
	if len(items) == 0 {
		return nil
	}
	changed := false
	if plans, ok := items["plans"].([]any); ok {
		for i, e := range plans {
			item, ok := e.(object)
			if !ok {
				continue
			}
			id, err := core.ParseID[core.PlanID](stringOf(item["id"]))
			if err != nil {
				continue
			}
			p, err := r.Plans.Get(ctx, v.OrganizationID, id)
			if errors.Is(err, core.ErrNotFound) {
				continue
			} else if err != nil {
				return fmt.Errorf("load plan %s: %w", id, err)
			}
			if withCurrencyOverride(item, p.Currency.Code(), cur) {
				plans[i], changed = item, true
			}
		}
	}
	if coupons, ok := items["coupons"].([]any); ok {
		for i, e := range coupons {
			item, ok := e.(object)
			if !ok {
				continue
			}
			id, err := core.ParseID[core.CouponID](stringOf(item["id"]))
			if err != nil {
				continue
			}
			c, err := r.Coupons.Get(ctx, v.OrganizationID, id)
			if errors.Is(err, core.ErrNotFound) {
				continue
			} else if err != nil {
				return fmt.Errorf("load coupon %s: %w", id, err)
			}
			// A percentage coupon is never priced in a currency.
			catalog := cur
			if c.Type == coupon.TypeFixedAmount {
				catalog = c.Currency.Code()
			}
			if withCurrencyOverride(item, catalog, cur) {
				coupons[i], changed = item, true
			}
		}
	}
	if credits, ok := items["walletCredits"].([]any); ok {
		for _, e := range credits {
			item, ok := e.(object)
			if !ok {
				continue
			}
			payload, ok := item["payload"].(object)
			if !ok {
				continue
			}
			if c, ok := payload["currency"].(string); ok && validators.CurrencyListed(c) && c != cur {
				payload["currency"], changed = cur, true
			}
		}
	}
	if !changed {
		return nil
	}
	raw, err := encode(items)
	if err != nil {
		return err
	}
	v.BillingItems = raw
	return nil
}

// withCurrencyOverride is lago with_currency_override
// (update_service.rb:128-152) on item in place; it reports whether item
// changed.
func withCurrencyOverride(item object, catalog, cur string) bool {
	submitted, present := item["overrides"]
	overrides, isObject := submitted.(object)
	if present && submitted != nil && !isObject {
		return false
	}
	if overrides == nil {
		overrides = object{}
	}
	if stated, ok := overrides["amountCurrency"]; ok && stated != nil {
		s, isString := stated.(string)
		if !isString || !validators.CurrencyListed(s) {
			return false
		}
	}
	realigned := make(object, len(overrides))
	for k, x := range overrides {
		realigned[k] = x
	}
	if catalog == cur {
		delete(realigned, "amountCurrency")
	} else {
		realigned["amountCurrency"] = cur
	}
	if reflect.DeepEqual(realigned, overrides) {
		return false
	}
	item["overrides"] = realigned
	return true
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// Approval is the result of [Service.ApproveVersion].
type Approval struct {
	// Version is the approved version.
	Version *VersionView
	// OrderForm is its generated order form.
	OrderForm *OrderFormView
}

// ApproveVersion approves draft version id of quote q, freezes its mention
// variables, emits quote.approved and generates its order form expiring
// at expiresAt (lago QuoteVersions::ApproveService and
// OrderForms::CreateService, app/services/quote_versions/approve_service.rb:17-50,
// app/services/order_forms/create_service.rb:15-42). It runs under the
// lock of the quote: status not_approvable unless draft; the billing items
// are validated for approval; expires_at invalid_date unless in the
// future and after_deal_expiration from the deal expiration on.
func (s *Service) ApproveVersion(ctx context.Context, org core.OrganizationID, q core.QuoteID, id core.QuoteVersionID, expiresAt *time.Time) (*Approval, error) {
	var out Approval
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := loadVersion(ctx, r, org, q, id)
		if err != nil {
			return err
		}
		if err := lockQuote(ctx, r, org, v.QuoteID); err != nil {
			return err
		}
		if v, err = r.QuoteVersions.Get(ctx, org, id); err != nil {
			return fmt.Errorf("reload quote version %s: %w", id, err)
		}
		qt, err := r.Quotes.Get(ctx, org, v.QuoteID)
		if err != nil {
			return fmt.Errorf("load quote %s: %w", v.QuoteID, err)
		}
		if v.Status != quote.VersionStatusDraft {
			return fieldError("status", quote.CodeNotApprovable)
		}
		if err := s.validateVersion(ctx, r, qt, v, validators.ScopeApprove); err != nil {
			return err
		}
		now := s.d.Now()
		if v.MentionVariables, err = s.mentionVariables(ctx, r, qt, v); err != nil {
			return err
		}
		if err := v.Approve(now); err != nil {
			return err
		}
		if err := r.QuoteVersions.Update(ctx, v); err != nil {
			return fmt.Errorf("update quote version %s: %w", id, err)
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeQuoteApproved, "quote", v.ID.UUID(), quoteWithVersion{Quote: qt, Version: v}); err != nil {
			return err
		}
		f, err := s.createOrderForm(ctx, r, qt, v, expiresAt, now)
		if err != nil {
			return err
		}
		if out.Version, err = versionView(ctx, r, v); err != nil {
			return err
		}
		out.OrderForm, err = s.orderFormView(ctx, r, f)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("approve quote version %s: %w", id, err)
	}
	return &out, nil
}

// createOrderForm is lago OrderForms::CreateService.
func (s *Service) createOrderForm(ctx context.Context, r port.Repos, q *quote.Quote, v *quote.Version, expiresAt *time.Time, now time.Time) (*quote.OrderForm, error) {
	if v.Status != quote.VersionStatusApproved {
		return nil, fieldError("quote_version", CodeNotApproved)
	}
	if expiresAt != nil && !expiresAt.After(now) {
		return nil, fieldError("expires_at", CodeInvalidDate)
	}
	if !dealCovers(v, expiresAt) {
		return nil, fieldError("expires_at", CodeAfterDealExpiration)
	}
	var exp *time.Time
	if expiresAt != nil {
		t := expiresAt.UTC()
		exp = &t
	}
	f, err := quote.NewOrderForm(core.NewID[core.OrderFormID](s.d.IDs), q, v, exp, now)
	if err != nil {
		return nil, err
	}
	seq, err := r.OrderForms.NextSequence(ctx, q.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("next order form number: %w", err)
	}
	f.AssignNumber(seq)
	if err := r.OrderForms.Create(ctx, f); err != nil {
		if errors.Is(err, core.ErrConflict) {
			return nil, fieldError("quote_version_id", core.CodeAlreadyExists)
		}
		return nil, fmt.Errorf("create order form: %w", err)
	}
	if err := kernel.Emit(ctx, s.d, q.OrganizationID, webhook.EventTypeOrderFormCreated, "order_form", f.ID.UUID(), f); err != nil {
		return nil, err
	}
	return f, nil
}

// CloneVersion makes the next draft version of quote q from version id
// (lago QuoteVersions::CloneService,
// app/services/quote_versions/clone_service.rb:25-86): status not_clonable
// while a version is approved; the active draft is voided as superseded
// (emitting quote.voided).
func (s *Service) CloneVersion(ctx context.Context, org core.OrganizationID, q core.QuoteID, id core.QuoteVersionID) (*VersionView, error) {
	var out *VersionView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := loadVersion(ctx, r, org, q, id)
		if err != nil {
			return err
		}
		if err := lockQuote(ctx, r, org, v.QuoteID); err != nil {
			return err
		}
		if v, err = r.QuoteVersions.Get(ctx, org, id); err != nil {
			return fmt.Errorf("reload quote version %s: %w", id, err)
		}
		qt, err := r.Quotes.Get(ctx, org, v.QuoteID)
		if err != nil {
			return fmt.Errorf("load quote %s: %w", v.QuoteID, err)
		}
		all, err := r.QuoteVersions.ListByQuote(ctx, org, v.QuoteID)
		if err != nil {
			return fmt.Errorf("list versions of quote %s: %w", v.QuoteID, err)
		}
		for i, x := range all {
			if x.ID == v.ID {
				all[i] = v
			}
		}
		next, superseded, err := quote.NewVersion(core.NewID[core.QuoteVersionID](s.d.IDs), v, all, s.d.Now())
		var rule *core.RuleError
		if errors.As(err, &rule) && rule.Code == quote.CodeNotClonable {
			return fieldError("status", quote.CodeNotClonable)
		} else if err != nil {
			return err
		}
		if superseded != nil {
			if err := r.QuoteVersions.Update(ctx, superseded); err != nil {
				return fmt.Errorf("void quote version %s: %w", superseded.ID, err)
			}
			if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeQuoteVoided, "quote", superseded.ID.UUID(), quoteWithVersion{Quote: qt, Version: superseded}); err != nil {
				return err
			}
		}
		if err := r.QuoteVersions.Create(ctx, next); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return fieldError("status", quote.CodeActiveVersionExists)
			}
			return fmt.Errorf("create quote version: %w", err)
		}
		out, err = versionView(ctx, r, next)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("clone quote version %s: %w", id, err)
	}
	return out, nil
}

// VoidVersion voids draft version id of quote q manually and emits
// quote.voided (lago QuoteVersions::VoidService with reason manual,
// app/services/quote_versions/void_service.rb:17-46): status not_voidable
// unless draft.
func (s *Service) VoidVersion(ctx context.Context, org core.OrganizationID, q core.QuoteID, id core.QuoteVersionID) (*VersionView, error) {
	var out *VersionView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := loadVersion(ctx, r, org, q, id)
		if err != nil {
			return err
		}
		if err := lockQuote(ctx, r, org, v.QuoteID); err != nil {
			return err
		}
		if v, err = r.QuoteVersions.Get(ctx, org, id); err != nil {
			return fmt.Errorf("reload quote version %s: %w", id, err)
		}
		qt, err := r.Quotes.Get(ctx, org, v.QuoteID)
		if err != nil {
			return fmt.Errorf("load quote %s: %w", v.QuoteID, err)
		}
		if err := s.voidVersion(ctx, r, qt, v, quote.VersionVoidReasonManual); err != nil {
			return err
		}
		out, err = versionView(ctx, r, v)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("void quote version %s: %w", id, err)
	}
	return out, nil
}

// voidVersion voids v for reason and emits quote.voided; the caller holds
// the lock of the quote.
func (s *Service) voidVersion(ctx context.Context, r port.Repos, q *quote.Quote, v *quote.Version, reason quote.VersionVoidReason) error {
	cascade := reason == quote.VersionVoidReasonCascadeOfExpired || reason == quote.VersionVoidReasonCascadeOfVoided
	if v.Status != quote.VersionStatusDraft && (v.Status != quote.VersionStatusApproved || !cascade) {
		return fieldError("status", quote.CodeNotVoidable)
	}
	if err := v.Void(reason, s.d.Now()); err != nil {
		return err
	}
	if err := r.QuoteVersions.Update(ctx, v); err != nil {
		return fmt.Errorf("update quote version %s: %w", v.ID, err)
	}
	return kernel.Emit(ctx, s.d, q.OrganizationID, webhook.EventTypeQuoteVoided, "quote", v.ID.UUID(), quoteWithVersion{Quote: q, Version: v})
}
