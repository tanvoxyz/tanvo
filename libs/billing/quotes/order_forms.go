package quotes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// OrderFormView is an order form with what its API representation shows
// (lago V1::OrderFormSerializer).
type OrderFormView struct {
	// OrderForm is the order form.
	OrderForm *quote.OrderForm
	// QuoteID is the quote of its version.
	QuoteID core.QuoteID
	// SignedDocumentURL downloads the signed document; empty without one.
	SignedDocumentURL string
	// BillingSnapshot are the billing items of its version.
	BillingSnapshot json.RawMessage
}

// GetOrderForm returns order form id of org.
func (s *Service) GetOrderForm(ctx context.Context, org core.OrganizationID, id core.OrderFormID) (*OrderFormView, error) {
	var out *OrderFormView
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.OrderForms.Get(ctx, org, id)
		if err != nil {
			return notFound(err, "order_form")
		}
		out, err = s.orderFormView(ctx, r, f)
		return err
	})
	return out, err
}

// ListOrderForms returns the order forms of org matching f, newest first
// (lago OrderFormsQuery).
func (s *Service) ListOrderForms(ctx context.Context, org core.OrganizationID, f quote.OrderFormFilter, page core.PageRequest) (core.Page[*OrderFormView], error) {
	var out core.Page[*OrderFormView]
	err := s.read(ctx, func(ctx context.Context, r port.Repos) error {
		p, err := r.OrderForms.List(ctx, org, f, page)
		if err != nil {
			return fmt.Errorf("list order forms: %w", err)
		}
		out.NextToken = p.NextToken
		for _, x := range p.Items {
			v, err := s.orderFormView(ctx, r, x)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, v)
		}
		return nil
	})
	return out, err
}

func (s *Service) orderFormView(ctx context.Context, r port.Repos, f *quote.OrderForm) (*OrderFormView, error) {
	v, err := r.QuoteVersions.Get(ctx, f.OrganizationID, f.QuoteVersionID)
	if err != nil {
		return nil, fmt.Errorf("load quote version %s: %w", f.QuoteVersionID, err)
	}
	u, err := s.blobURL(ctx, f.OrganizationID, f.SignedDocument)
	if err != nil {
		return nil, err
	}
	return &OrderFormView{OrderForm: f, QuoteID: v.QuoteID, SignedDocumentURL: u, BillingSnapshot: v.BillingItems}, nil
}

// SignInput marks an order form signed (lago OrderForms::MarkAsSignedService).
type SignInput struct {
	// SignedDocument is the signed document as a base64 data URI; empty
	// for none.
	SignedDocument string
	// ExecutionMode is the order's execution mode; zero leaves it unset.
	ExecutionMode quote.ExecutionMode
	// ExecuteAt schedules the order's execution.
	ExecuteAt *time.Time
}

// Signature is the result of [Service.SignOrderForm].
type Signature struct {
	// OrderForm is the signed order form.
	OrderForm *OrderFormView
	// Order is the order it created.
	Order *OrderView
}

// SignOrderForm marks generated order form id signed, attaches its signed
// document and creates its order, emitting order_form.signed then
// order.created (lago OrderForms::MarkAsSignedService,
// app/services/order_forms/mark_as_signed_service.rb:19-99). The execution
// settings are validated first (execution_settings_validation.rb); the
// signed document must be a data URI (invalid_format) of a PDF, JPEG or
// PNG smaller than 10 MB. It runs under the lock of the quote: status
// not_signable unless generated.
func (s *Service) SignOrderForm(ctx context.Context, org core.OrganizationID, id core.OrderFormID, in SignInput) (*Signature, error) {
	var out Signature
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.OrderForms.Get(ctx, org, id)
		if err != nil {
			return notFound(err, "order_form")
		}
		v, err := r.QuoteVersions.Get(ctx, org, f.QuoteVersionID)
		if err != nil {
			return fmt.Errorf("load quote version %s: %w", f.QuoteVersionID, err)
		}
		now := s.d.Now()
		if err := checkExecutionSettings(in.ExecutionMode, in.ExecuteAt, in.ExecuteAt, v, now); err != nil {
			return err
		}
		var doc *dataURI
		if in.SignedDocument != "" {
			d, ok := decodeDataURI(in.SignedDocument)
			if !ok {
				return fieldError("signed_document", core.CodeInvalidFormat)
			}
			doc = &d
		}
		if err := lockQuote(ctx, r, org, v.QuoteID); err != nil {
			return err
		}
		if f, err = r.OrderForms.Get(ctx, org, id); err != nil {
			return fmt.Errorf("reload order form %s: %w", id, err)
		}
		if f.Status != quote.OrderFormStatusGenerated {
			return fieldError("status", quote.CodeNotSignable)
		}
		var ref core.BlobRef
		if doc != nil {
			if err := checkUpload("signed_document", *doc, quote.SignedDocumentMaxSize, quote.SignedDocumentContentTypes); err != nil {
				return err
			}
			if s.blobs == nil {
				return core.NewRuleError(CodeBlobStoreNotConfigured, "signed documents cannot be stored: no blob store is configured")
			}
			if ref, err = s.blobs.Put(ctx, org, f.Number, doc.contentType, bytes.NewReader(doc.content)); err != nil {
				return fmt.Errorf("store signed document: %w", err)
			}
		}
		if err := f.Sign(ref, now); err != nil {
			return err
		}
		if err := r.OrderForms.Update(ctx, f); err != nil {
			return fmt.Errorf("update order form %s: %w", id, err)
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeOrderFormSigned, "order_form", f.ID.UUID(), f); err != nil {
			return err
		}
		// source: app/services/order_forms/mark_as_signed_service.rb:42-43
		if err := LogOrderForm(ctx, s.d, r, auditlog.ActivityOrderFormSigned, f); err != nil {
			return err
		}
		if doc != nil {
			if err := LogOrderForm(ctx, s.d, r, auditlog.ActivityOrderFormFileUploaded, f); err != nil {
				return err
			}
		}
		var executeAt *time.Time
		if in.ExecuteAt != nil {
			t := in.ExecuteAt.UTC()
			executeAt = &t
		}
		o, err := quote.NewOrder(core.NewID[core.OrderID](s.d.IDs), f, in.ExecutionMode, executeAt, now)
		if err != nil {
			return err
		}
		seq, err := r.Orders.NextSequence(ctx, org)
		if err != nil {
			return fmt.Errorf("next order number: %w", err)
		}
		o.AssignNumber(seq)
		if err := o.Validate(); err != nil {
			return err
		}
		if err := r.Orders.Create(ctx, o); err != nil {
			if errors.Is(err, core.ErrConflict) {
				return fieldError("order_form_id", core.CodeAlreadyExists)
			}
			return fmt.Errorf("create order: %w", err)
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeOrderCreated, "order", o.ID.UUID(), o); err != nil {
			return err
		}
		// source: app/services/order_forms/mark_as_signed_service.rb:54
		if err := s.logOrder(ctx, r, auditlog.ActivityOrderCreated, o); err != nil {
			return err
		}
		if out.OrderForm, err = s.orderFormView(ctx, r, f); err != nil {
			return err
		}
		out.Order, err = orderView(ctx, r, o)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("sign order form %s: %w", id, err)
	}
	return &out, nil
}

// VoidOrderForm voids generated order form id manually, emits
// order_form.voided and voids its version as cascade_of_voided (lago
// OrderForms::VoidService, app/services/order_forms/void_service.rb:15-42):
// status not_voidable unless generated.
func (s *Service) VoidOrderForm(ctx context.Context, org core.OrganizationID, id core.OrderFormID) (*OrderFormView, error) {
	var out *OrderFormView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		f, err := r.OrderForms.Get(ctx, org, id)
		if err != nil {
			return notFound(err, "order_form")
		}
		v, err := r.QuoteVersions.Get(ctx, org, f.QuoteVersionID)
		if err != nil {
			return fmt.Errorf("load quote version %s: %w", f.QuoteVersionID, err)
		}
		if err := lockQuote(ctx, r, org, v.QuoteID); err != nil {
			return err
		}
		if f, err = r.OrderForms.Get(ctx, org, id); err != nil {
			return fmt.Errorf("reload order form %s: %w", id, err)
		}
		if f.Status != quote.OrderFormStatusGenerated {
			return fieldError("status", quote.CodeNotVoidable)
		}
		if v, err = r.QuoteVersions.Get(ctx, org, f.QuoteVersionID); err != nil {
			return fmt.Errorf("reload quote version %s: %w", f.QuoteVersionID, err)
		}
		q, err := r.Quotes.Get(ctx, org, v.QuoteID)
		if err != nil {
			return fmt.Errorf("load quote %s: %w", v.QuoteID, err)
		}
		if err := f.Void(quote.OrderFormVoidReasonManual, v, s.d.Now()); err != nil {
			return err
		}
		if err := r.OrderForms.Update(ctx, f); err != nil {
			return fmt.Errorf("update order form %s: %w", id, err)
		}
		if err := r.QuoteVersions.Update(ctx, v); err != nil {
			return fmt.Errorf("update quote version %s: %w", v.ID, err)
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeOrderFormVoided, "order_form", f.ID.UUID(), f); err != nil {
			return err
		}
		// source: app/services/order_forms/void_service.rb:31,33
		if err := LogOrderForm(ctx, s.d, r, auditlog.ActivityOrderFormVoided, f); err != nil {
			return err
		}
		if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeQuoteVoided, "quote", v.ID.UUID(), quoteWithVersion{Quote: q, Version: v}); err != nil {
			return err
		}
		if err := LogVoided(ctx, s.d, r, q, v); err != nil {
			return err
		}
		out, err = s.orderFormView(ctx, r, f)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("void order form %s: %w", id, err)
	}
	return out, nil
}

// checkExecutionSettings is lago OrderForms::ExecutionSettingsValidation
// (app/services/order_forms/execution_settings_validation.rb:9-35) with
// the effective mode and execute_at, and the requested execute_at: mode
// value_is_mandatory when execute_at is set without it, execute_at
// invalid_date unless the requested one is in the future, and
// after_deal_expiration from the deal expiration on.
func checkExecutionSettings(mode quote.ExecutionMode, executeAt, requested *time.Time, v *quote.Version, now time.Time) error {
	if mode == "" && executeAt != nil {
		return fieldError("execution_mode", core.CodeRequired)
	}
	if mode != "" && !mode.Valid() {
		return fieldError("execution_mode", CodeValueIsInvalid)
	}
	if requested != nil && !requested.After(now) {
		return fieldError("execute_at", CodeInvalidDate)
	}
	if !dealCovers(v, executeAt) {
		return fieldError("execute_at", CodeAfterDealExpiration)
	}
	return nil
}
