package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindCustomer binds the repositories of the customer aggregates.
func (t *txState) bindCustomer(r *port.Repos) {
	r.Customers = customerRepo{t}
	r.PendingVIESChecks = pendingVIESCheckRepo{t}
	r.CustomerErrorDetails = customerErrorDetailRepo{t}
}

// customerRepo implements customer.Repository.
type customerRepo struct{ t *txState }

var _ customer.Repository = customerRepo{}

func customerParams(c *customer.Customer) (db.CreateCustomerParams, error) {
	attempts := c.DunningCampaignAttempts
	if attempts == nil {
		attempts = map[string]int{}
	}
	att, err := encodeJSON(attempts)
	if err != nil {
		return db.CreateCustomerParams{}, fmt.Errorf("customer %s: dunning attempts: %w", c.ID, err)
	}
	return db.CreateCustomerParams{
		OrganizationID:               c.OrganizationID.UUID(),
		ID:                           c.ID.UUID(),
		ExternalID:                   c.ExternalID,
		BillingEntityID:              c.BillingEntityID.UUID(),
		CustomerType:                 optString(string(c.CustomerType)),
		AccountType:                  string(c.AccountType),
		Name:                         c.Name,
		Firstname:                    c.Firstname,
		Lastname:                     c.Lastname,
		LegalName:                    c.LegalName,
		LegalNumber:                  c.LegalNumber,
		TaxIdentificationNumber:      c.TaxIdentificationNumber,
		Email:                        c.Email,
		Phone:                        c.Phone,
		Url:                          c.URL,
		LogoUrl:                      c.LogoURL,
		BillingAddressLine1:          c.BillingAddress.Line1,
		BillingAddressLine2:          c.BillingAddress.Line2,
		BillingAddressCity:           c.BillingAddress.City,
		BillingAddressState:          c.BillingAddress.State,
		BillingAddressZipcode:        c.BillingAddress.Zipcode,
		BillingAddressCountry:        c.BillingAddress.Country,
		ShippingAddressLine1:         c.ShippingAddress.Line1,
		ShippingAddressLine2:         c.ShippingAddress.Line2,
		ShippingAddressCity:          c.ShippingAddress.City,
		ShippingAddressState:         c.ShippingAddress.State,
		ShippingAddressZipcode:       c.ShippingAddress.Zipcode,
		ShippingAddressCountry:       c.ShippingAddress.Country,
		Currency:                     currencyCode(c.Currency),
		Timezone:                     locationName(c.Timezone),
		DocumentLocale:               c.DocumentLocale,
		InvoiceGracePeriodDays:       optInt32(c.InvoiceGracePeriodDays),
		NetPaymentTermDays:           optInt32(c.NetPaymentTermDays),
		FinalizeZeroAmountInvoice:    string(c.FinalizeZeroAmountInvoice),
		IssuingDateAnchor:            optString(string(c.IssuingDateAnchor)),
		IssuingDateAdjustment:        optString(string(c.IssuingDateAdjustment)),
		SkipInvoiceCustomSections:    c.SkipInvoiceCustomSections,
		AppliedDunningCampaignID:     optUUID(c.AppliedDunningCampaignID),
		ExcludeFromDunningCampaign:   c.ExcludeFromDunningCampaign,
		DunningCampaignAttempts:      att,
		LastDunningCampaignAttemptAt: optUTC(c.LastDunningCampaignAttemptAt),
		SequentialID:                 c.SequentialID,
		Slug:                         c.Slug,
		PaymentReceiptCounter:        c.PaymentReceiptCounter,
		Metadata:                     encodeMetadata(c.Metadata),
		X402AgentAddress:             c.X402AgentAddress,
		DeletedAt:                    optUTC(c.DeletedAt),
		CreatedAt:                    utc(c.CreatedAt),
		UpdatedAt:                    utc(c.UpdatedAt),
	}, nil
}

// customerRow is the common shape of the customer reads; the row types
// sqlc generates for each query convert to it.
type customerRow struct {
	Customer                      db.Customer
	TaxIds                        []uuid.UUID
	InvoiceCustomSectionIds       []uuid.UUID
	SystemInvoiceCustomSectionIds []uuid.UUID
}

func customerOf(row customerRow) (*customer.Customer, error) {
	r := row.Customer
	loc, err := location(r.Timezone)
	if err != nil {
		return nil, fmt.Errorf("customer %s: %w", r.ID, err)
	}
	cur, err := currency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("customer %s: %w", r.ID, err)
	}
	md, err := decodeMetadata(r.Metadata)
	if err != nil {
		return nil, fmt.Errorf("customer %s: %w", r.ID, err)
	}
	var attempts map[string]int
	if err := decodeJSON(r.DunningCampaignAttempts, &attempts); err != nil {
		return nil, fmt.Errorf("customer %s: dunning attempts: %w", r.ID, err)
	}
	if len(attempts) == 0 {
		attempts = nil
	}
	return &customer.Customer{
		ID:                      idOf[core.CustomerID](r.ID),
		OrganizationID:          idOf[core.OrganizationID](r.OrganizationID),
		ExternalID:              r.ExternalID,
		BillingEntityID:         idOf[core.BillingEntityID](r.BillingEntityID),
		CustomerType:            customer.Type(deref(r.CustomerType)),
		AccountType:             customer.AccountType(r.AccountType),
		Name:                    r.Name,
		Firstname:               r.Firstname,
		Lastname:                r.Lastname,
		LegalName:               r.LegalName,
		LegalNumber:             r.LegalNumber,
		TaxIdentificationNumber: r.TaxIdentificationNumber,
		Email:                   r.Email,
		Phone:                   r.Phone,
		URL:                     r.Url,
		LogoURL:                 r.LogoUrl,
		BillingAddress: core.Address{
			Line1: r.BillingAddressLine1, Line2: r.BillingAddressLine2, City: r.BillingAddressCity,
			State: r.BillingAddressState, Zipcode: r.BillingAddressZipcode, Country: r.BillingAddressCountry,
		},
		ShippingAddress: core.Address{
			Line1: r.ShippingAddressLine1, Line2: r.ShippingAddressLine2, City: r.ShippingAddressCity,
			State: r.ShippingAddressState, Zipcode: r.ShippingAddressZipcode, Country: r.ShippingAddressCountry,
		},
		Currency:                      cur,
		Timezone:                      loc,
		DocumentLocale:                r.DocumentLocale,
		InvoiceGracePeriodDays:        optInt(r.InvoiceGracePeriodDays),
		NetPaymentTermDays:            optInt(r.NetPaymentTermDays),
		FinalizeZeroAmountInvoice:     customer.ZeroAmountFinalization(r.FinalizeZeroAmountInvoice),
		IssuingDateAnchor:             core.IssuingDateAnchor(deref(r.IssuingDateAnchor)),
		IssuingDateAdjustment:         core.IssuingDateAdjustment(deref(r.IssuingDateAdjustment)),
		SkipInvoiceCustomSections:     r.SkipInvoiceCustomSections,
		InvoiceCustomSectionIDs:       idsOf[core.InvoiceCustomSectionID](row.InvoiceCustomSectionIds),
		SystemInvoiceCustomSectionIDs: idsOf[core.InvoiceCustomSectionID](row.SystemInvoiceCustomSectionIds),
		AppliedDunningCampaignID:      optIDOf[core.DunningCampaignID](r.AppliedDunningCampaignID),
		ExcludeFromDunningCampaign:    r.ExcludeFromDunningCampaign,
		DunningCampaignAttempts:       attempts,
		LastDunningCampaignAttemptAt:  optUTC(r.LastDunningCampaignAttemptAt),
		SequentialID:                  r.SequentialID,
		Slug:                          r.Slug,
		PaymentReceiptCounter:         r.PaymentReceiptCounter,
		Metadata:                      md,
		TaxIDs:                        idsOf[core.TaxID](row.TaxIds),
		X402AgentAddress:              r.X402AgentAddress,
		DeletedAt:                     optUTC(r.DeletedAt),
		CreatedAt:                     utc(r.CreatedAt),
		UpdatedAt:                     utc(r.UpdatedAt),
	}, nil
}

// customerHasLinks reports whether c has taxes or invoice custom sections.
func customerHasLinks(c *customer.Customer) bool {
	return len(c.TaxIDs) > 0 || len(c.InvoiceCustomSectionIDs) > 0 || len(c.SystemInvoiceCustomSectionIDs) > 0
}

// writeLinks replaces the taxes and invoice custom sections of c.
func (r customerRepo) writeLinks(ctx context.Context, q *db.Queries, c *customer.Customer, replace bool) error {
	org, id := c.OrganizationID.UUID(), c.ID.UUID()
	if replace {
		if err := q.DeleteCustomerTaxes(ctx, db.DeleteCustomerTaxesParams{OrganizationID: org, CustomerID: id}); err != nil {
			return err
		}
		if err := q.DeleteCustomerSections(ctx, db.DeleteCustomerSectionsParams{OrganizationID: org, CustomerID: id}); err != nil {
			return err
		}
	}
	if len(c.TaxIDs) > 0 {
		if err := q.InsertCustomerTaxes(ctx, db.InsertCustomerTaxesParams{OrganizationID: org, CustomerID: id, TaxIds: uuids(c.TaxIDs)}); err != nil {
			return err
		}
	}
	for _, s := range []struct {
		system bool
		ids    []core.InvoiceCustomSectionID
	}{{false, c.InvoiceCustomSectionIDs}, {true, c.SystemInvoiceCustomSectionIDs}} {
		if len(s.ids) == 0 {
			continue
		}
		if err := q.InsertCustomerSections(ctx, db.InsertCustomerSectionsParams{
			OrganizationID: org, CustomerID: id, System: s.system, SectionIds: uuids(s.ids),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r customerRepo) Create(ctx context.Context, c *customer.Customer) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	p, err := customerParams(c)
	if err != nil {
		return err
	}
	if !customerHasLinks(c) {
		n, err := q.CreateCustomer(ctx, p)
		return insertErr("customer", n, err)
	}
	// A missing tax or section must not abort the transaction: guard the
	// insert with its links.
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.CreateCustomer(ctx, p)
		if err != nil || n == 0 {
			return err
		}
		return r.writeLinks(ctx, q, c, false)
	})
	return insertErr("customer", n, err)
}

func (r customerRepo) Update(ctx context.Context, c *customer.Customer) error {
	q, err := r.t.queries(ctx, c.OrganizationID)
	if err != nil {
		return err
	}
	p, err := customerParams(c)
	if err != nil {
		return err
	}
	// The external ID is unique among live customers: guard the update so
	// that a conflict keeps the transaction usable.
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateCustomer(ctx, db.UpdateCustomerParams(p))
		if err != nil || n == 0 {
			return err
		}
		return r.writeLinks(ctx, q, c, true)
	})
	return updateErr("customer", c.ID, n, err)
}

func (r customerRepo) Get(ctx context.Context, org core.OrganizationID, id core.CustomerID) (*customer.Customer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetCustomer(ctx, db.GetCustomerParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("customer", id, err)
	}
	return customerOf(customerRow(row))
}

func (r customerRepo) GetByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (*customer.Customer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetCustomerByExternalID(ctx, db.GetCustomerByExternalIDParams{OrganizationID: org.UUID(), ExternalID: externalID})
	if err != nil {
		return nil, getErr("customer", externalID, err)
	}
	return customerOf(customerRow(row))
}

func (r customerRepo) GetByX402AgentAddress(ctx context.Context, org core.OrganizationID, address string) (*customer.Customer, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetCustomerByX402AgentAddress(ctx, db.GetCustomerByX402AgentAddressParams{OrganizationID: org.UUID(), X402AgentAddress: &address})
	if err != nil {
		return nil, getErr("customer", address, err)
	}
	return customerOf(customerRow(row))
}

func (r customerRepo) List(ctx context.Context, org core.OrganizationID, f customer.Filter, page core.PageRequest) (core.Page[*customer.Customer], error) {
	if err := f.Validate(); err != nil {
		return core.Page[*customer.Customer]{}, fmt.Errorf("list customers: %w", err)
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*customer.Customer]{}, err
	}
	recent := f.Order == core.OrderRecent
	var (
		after   *uuid.UUID
		afterAt *time.Time
		lim     int32
	)
	if recent {
		after, afterAt, lim, err = timeCursorArgs(page)
	} else {
		after, lim, err = pageArgs(page)
	}
	if err != nil {
		return core.Page[*customer.Customer]{}, err
	}
	search := searchArg(f.Search)
	if f.ExternalID != "" {
		search = nil
	}
	var minActive, maxActive *int32
	if lo, hi, ok := f.ActiveSubscriptionsRange(); ok {
		minActive, maxActive = new(int32(lo)), new(int32(hi)) //nolint:gosec // validated non-negative, at most MaxInt32
	}
	present, absent := metadataFilterArgs(f.Metadata)
	rows, err := q.ListCustomers(ctx, db.ListCustomersParams{
		OrganizationID:             org.UUID(),
		IncludeDeleted:             f.IncludeDeleted,
		ExternalID:                 optString(f.ExternalID),
		BillingEntityIds:           uuids(f.BillingEntityIDs),
		AccountTypes:               enumStrings(f.AccountTypes),
		CustomerType:               optString(string(f.CustomerType)),
		HasCustomerType:            f.HasCustomerType,
		Countries:                  nonNil(f.Countries),
		States:                     nonNil(f.States),
		Zipcodes:                   nonNil(f.Zipcodes),
		Currencies:                 currencyCodes(f.Currencies),
		HasTaxIdentificationNumber: f.HasTaxIdentificationNumber,
		MetadataPresent:            present,
		MetadataAbsent:             absent,
		ActiveSubscriptionsMin:     minActive,
		ActiveSubscriptionsMax:     maxActive,
		Search:                     search,
		After:                      after,
		AfterAt:                    afterAt,
		Recent:                     recent,
		Lim:                        lim,
	})
	if err != nil {
		return core.Page[*customer.Customer]{}, fmt.Errorf("list customers: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListCustomersRow) (*customer.Customer, error) { return customerOf(customerRow(r)) },
		func(c *customer.Customer) string {
			if recent {
				return core.Cursor{Key: c.CreatedAt.UTC().Format(time.RFC3339Nano), ID: c.ID.UUID()}.Encode()
			}
			return c.ID.String()
		})
}

func (r customerRepo) ListForDunning(ctx context.Context, org core.OrganizationID, campaign core.DunningCampaignID, page core.PageRequest) (core.Page[*customer.Customer], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*customer.Customer]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*customer.Customer]{}, err
	}
	rows, err := q.ListCustomersForDunning(ctx, db.ListCustomersForDunningParams{
		OrganizationID: org.UUID(), Campaign: optUUID(campaign), After: after, Lim: lim,
	})
	if err != nil {
		return core.Page[*customer.Customer]{}, fmt.Errorf("list customers for dunning: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListCustomersForDunningRow) (*customer.Customer, error) { return customerOf(customerRow(r)) },
		func(c *customer.Customer) string { return c.ID.String() })
}

func (r customerRepo) ResetDunningCampaign(ctx context.Context, org core.OrganizationID, campaign core.DunningCampaignID) (int, error) {
	if campaign.IsZero() {
		return 0, nil
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	n, err := q.ResetCustomersDunningCampaign(ctx, db.ResetCustomersDunningCampaignParams{OrganizationID: org.UUID(), Campaign: campaign.UUID()})
	if err != nil {
		return 0, fmt.Errorf("reset dunning campaign %s: %w", campaign, mapErr(err))
	}
	return int(n), nil
}

func (r customerRepo) FlagWalletRefresh(ctx context.Context, org core.OrganizationID, id core.CustomerID) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	// A flag raised and not yet claimed by a refresh needs no write: the
	// key share lock keeps that refresh's claim waiting until this
	// transaction ends (lago writes only a changed flag,
	// app/models/customer.rb:385-389).
	pending, err := q.LockCustomerWalletRefreshPending(ctx, db.LockCustomerWalletRefreshPendingParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		if errors.Is(mapErr(err), core.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("flag wallet refresh of customer %s: %w", id, mapErr(err))
	}
	if pending {
		return nil
	}
	if err := q.FlagCustomerWalletRefresh(ctx, db.FlagCustomerWalletRefreshParams{OrganizationID: org.UUID(), ID: id.UUID()}); err != nil {
		return fmt.Errorf("flag wallet refresh of customer %s: %w", id, mapErr(err))
	}
	return nil
}

func (r customerRepo) ClaimWalletRefresh(ctx context.Context, org core.OrganizationID, id core.CustomerID) (int64, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	gen, err := q.ClaimCustomerWalletRefresh(ctx, db.ClaimCustomerWalletRefreshParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return 0, fmt.Errorf("claim wallet refresh of customer %s: %w", id, mapErr(err))
	}
	return gen, nil
}

func (r customerRepo) ClearWalletRefresh(ctx context.Context, org core.OrganizationID, id core.CustomerID, gen int64) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.ClearCustomerWalletRefresh(ctx, db.ClearCustomerWalletRefreshParams{OrganizationID: org.UUID(), ID: id.UUID(), Generation: gen}); err != nil {
		return fmt.Errorf("clear wallet refresh of customer %s: %w", id, mapErr(err))
	}
	return nil
}

func (r customerRepo) ListAwaitingWalletRefresh(ctx context.Context, org core.OrganizationID, page core.PageRequest) (core.Page[core.CustomerID], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[core.CustomerID]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[core.CustomerID]{}, err
	}
	rows, err := q.ListCustomersAwaitingWalletRefresh(ctx, db.ListCustomersAwaitingWalletRefreshParams{OrganizationID: org.UUID(), After: after, Lim: lim})
	if err != nil {
		return core.Page[core.CustomerID]{}, fmt.Errorf("list customers awaiting wallet refresh: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(u uuid.UUID) (core.CustomerID, error) { return idOf[core.CustomerID](u), nil },
		core.CustomerID.String)
}

// customerErrorDetailRepo implements customer.ErrorDetailRepository.
type customerErrorDetailRepo struct{ t *txState }

var _ customer.ErrorDetailRepository = customerErrorDetailRepo{}

func customerErrorDetailOf(r db.CustomerErrorDetail) (*customer.ErrorDetail, error) {
	return &customer.ErrorDetail{
		ID:             idOf[core.ErrorDetailID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		CustomerID:     idOf[core.CustomerID](r.CustomerID),
		Code:           customer.ErrorCode(r.ErrorCode),
		Details:        rawJSON(r.Details),
		CreatedAt:      utc(r.CreatedAt),
	}, nil
}

func (r customerErrorDetailRepo) Create(ctx context.Context, e *customer.ErrorDetail) error {
	q, err := r.t.queries(ctx, e.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateCustomerErrorDetail(ctx, db.CreateCustomerErrorDetailParams{
		OrganizationID: e.OrganizationID.UUID(),
		ID:             e.ID.UUID(),
		CustomerID:     e.CustomerID.UUID(),
		ErrorCode:      string(e.Code),
		Details:        rawJSON(e.Details),
		CreatedAt:      utc(e.CreatedAt),
	})
	return insertErr("error_detail", n, err)
}

func (r customerErrorDetailRepo) ListByCustomers(ctx context.Context, org core.OrganizationID, ids []core.CustomerID) ([]*customer.ErrorDetail, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListCustomerErrorDetails(ctx, db.ListCustomerErrorDetailsParams{OrganizationID: org.UUID(), CustomerIds: uuids(ids)})
	if err != nil {
		return nil, fmt.Errorf("list customer error details: %w", mapErr(err))
	}
	return convAll(rows, customerErrorDetailOf)
}

func (r customerErrorDetailRepo) DeleteByCustomer(ctx context.Context, org core.OrganizationID, id core.CustomerID, code customer.ErrorCode) error {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return err
	}
	if err := q.DeleteCustomerErrorDetails(ctx, db.DeleteCustomerErrorDetailsParams{
		OrganizationID: org.UUID(), CustomerID: id.UUID(), ErrorCode: string(code),
	}); err != nil {
		return fmt.Errorf("delete customer error details: %w", mapErr(err))
	}
	return nil
}
