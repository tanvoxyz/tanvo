package quotes

import (
	"context"
	"errors"
	"fmt"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
)

// Codes of the validation errors of the quote flow (lago's error codes).
const (
	// CodeNotEditable: only a draft version, or a created order, can be
	// edited.
	CodeNotEditable = "not_editable"
	// CodeNotApproved: an order form needs an approved version.
	CodeNotApproved = "not_approved"
	// CodeOwnersInvalid: an owner is not an active member.
	CodeOwnersInvalid = "invalid"
	// CodeNotSupportedForOrderType: the currency of an amendment is the
	// subscription's.
	CodeNotSupportedForOrderType = "not_supported_for_order_type"
	// CodeInvalidDate: a date that must be in the future is not.
	CodeInvalidDate = "invalid_date"
	// CodeAfterDealExpiration: a date falls on or after the earliest date
	// the deal is built on.
	CodeAfterDealExpiration = "after_deal_expiration"
	// CodeValueIsInvalid: an unknown execution mode.
	CodeValueIsInvalid = "value_is_invalid"
	// CodeFileTooLarge: an uploaded file is too large.
	CodeFileTooLarge = "file_too_large"
	// CodeInvalidContentType: an uploaded file has a refused type.
	CodeInvalidContentType = "invalid_content_type"
	// CodeSinglePlanExpected: an amendment quotes exactly one plan.
	CodeSinglePlanExpected = "single_plan_expected"
	// CodeSubscriptionNotActive: the amended subscription is not active.
	CodeSubscriptionNotActive = "subscription_not_active"
	// CodeChargeModelChanged: the quoted charge changed model since.
	CodeChargeModelChanged = "charge_model_changed"
	// CodeFixedChargeModelChanged: the quoted fixed charge changed model
	// since.
	CodeFixedChargeModelChanged = "fixed_charge_model_changed"
	// CodeUnsupportedOrderType: an order of an unknown order type.
	CodeUnsupportedOrderType = "unsupported_order_type"
	// CodeBlobStoreNotConfigured: files cannot be stored.
	CodeBlobStoreNotConfigured = "blob_store_not_configured"
)

// Services are the billing services an order execution goes through.
type Services struct {
	// Subscriptions creates subscriptions (lago Subscriptions::CreateService)
	// and replaces their usage thresholds.
	Subscriptions *subscriptions.Service
	// Lifecycle changes the plan of an amended subscription.
	Lifecycle *lifecycle.Service
	// Plans builds the override plan of an amendment.
	Plans *plans.Service
	// Invoices bills one-off orders.
	Invoices *invoicing.Service
	// Coupons applies the quoted coupons.
	Coupons *deduct.Service
	// Wallets creates the quoted wallets.
	Wallets *wallets.Service
}

// Service runs the quote flow (domain §21): quotes and their versions,
// order forms and orders, and the order execution (lago Quotes::*,
// QuoteVersions::*, OrderForms::* and Orders::* services).
type Service struct {
	d     *kernel.Deps
	svc   Services
	blobs port.BlobStore
}

// New returns the service over d and svc.
func New(d *kernel.Deps, svc Services) (*Service, error) {
	if d == nil {
		return nil, errors.New("quotes: nil deps")
	}
	if svc.Subscriptions == nil || svc.Lifecycle == nil || svc.Plans == nil || svc.Invoices == nil || svc.Coupons == nil || svc.Wallets == nil {
		return nil, errors.New("quotes: missing billing service")
	}
	return &Service{d: d, svc: svc}, nil
}

// WithBlobs returns a copy of the service that keeps the quote images and
// signed documents in blobs; without one, they are refused with
// [CodeBlobStoreNotConfigured] and no URL is shown.
func (s *Service) WithBlobs(blobs port.BlobStore) *Service {
	c := *s
	c.blobs = blobs
	return &c
}

// read runs fn in a transaction (one snapshot for the reads).
func (s *Service) read(ctx context.Context, fn func(ctx context.Context, r port.Repos) error) error {
	return s.d.Tx.InTx(ctx, fn)
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

// fieldError is the single validation failure of field.
func fieldError(field, code string) error {
	var v core.Validator
	v.Add(field, code)
	return v.Err()
}

// notFound maps core.ErrNotFound to the not-found error of resource.
func notFound(err error, resource string) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(resource)
	}
	return err
}

// quoteWithVersion is the payload of the quote webhooks: a quote with one
// of its versions (lago V1::QuoteWithVersionSerializer).
type quoteWithVersion struct {
	Quote   *quote.Quote   `json:"quote"`
	Version *quote.Version `json:"version"`
}
