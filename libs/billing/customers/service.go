package customers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/tax"
)

// Resource is the resource name of the not-found errors.
const Resource = "customer"

// Resource names of the related objects' not-found errors.
const (
	// ResourceBillingEntity names a missing billing entity.
	ResourceBillingEntity = "billing_entity"
	// ResourceIntegration names a missing integration.
	ResourceIntegration = "integration"
	// ResourceConnection names a missing customer connection.
	ResourceConnection = "connection"
)

// Field error codes of the customer service.
const (
	// CodeSkipSectionsAndSelectedIDs: invoice custom sections were
	// selected while skipping them (lago
	// manage_invoice_custom_sections_service.rb:52-54).
	CodeSkipSectionsAndSelectedIDs = "skip_sections_and_selected_ids_sent_together"
	// CodeNoLinkedPaymentProvider: a checkout URL was asked for a
	// customer without a payment connection
	// (generate_checkout_url_service.rb:16-18).
	CodeNoLinkedPaymentProvider = "no_linked_payment_provider"
	// CodeOnlyOneConnectionAllowed: a second payment connection was
	// asked for (payment_provider_customers/create_or_update_batch_service.rb:24-29).
	CodeOnlyOneConnectionAllowed = "only_one_connection_allowed"
	// CodePartnerAccount: integration connections were asked for a
	// partner account, which has none
	// (integration_customers/create_or_update_batch_service.rb:16).
	CodePartnerAccount = "partner_account"
)

// ErrCheckoutUnavailable is returned by [Service.CheckoutURL] once its
// checks pass when the service has no [CheckoutURLGenerator].
var ErrCheckoutUnavailable = errors.New("customers: no checkout URL generator is configured")

// Relations are the services the termination of a deleted customer's
// relations uses (lago Customers::TerminateRelationsService); a nil one
// makes [Service.TerminateRelations] fail.
type Relations struct {
	// Lifecycle terminates active subscriptions and cancels pending ones.
	Lifecycle *lifecycle.Service
	// Coupons terminates applied coupons.
	Coupons *deduct.Service
	// Wallets terminates wallets.
	Wallets *wallets.Service
}

// Service manages customers. It uses d.Tx, d.Clock, d.IDs and d.Webhooks.
type Service struct {
	d        *kernel.Deps
	rel      Relations
	checkout CheckoutURLGenerator
	// vies checks VAT numbers ([WithVIES]); viesInvoices finalizes the
	// invoices waiting for a check ([WithVIESInvoiceFinalizer]).
	vies         tax.VIESChecker
	viesInvoices VIESInvoiceFinalizer
}

// Option configures a [Service].
type Option func(*Service)

// WithCheckout generates the checkout URLs of payment connections.
func WithCheckout(g CheckoutURLGenerator) Option { return func(s *Service) { s.checkout = g } }

// New returns the service over d, terminating the relations of deleted
// customers with rel. It fails when a dependency the service uses is nil.
func New(d *kernel.Deps, rel Relations, opts ...Option) (*Service, error) {
	switch {
	case d == nil, d.Tx == nil:
		return nil, fmt.Errorf("%w: Tx", kernel.ErrMissingDependency)
	case d.Clock == nil:
		return nil, fmt.Errorf("%w: Clock", kernel.ErrMissingDependency)
	case d.IDs == nil:
		return nil, fmt.Errorf("%w: IDs", kernel.ErrMissingDependency)
	case d.Webhooks == nil:
		return nil, fmt.Errorf("%w: Webhooks", kernel.ErrMissingDependency)
	}
	s := &Service{d: d, rel: rel}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// Address is the update of an address: only the set fields are assigned.
type Address struct {
	// Line1 is the first line.
	Line1 core.Optional[string]
	// Line2 is the second line.
	Line2 core.Optional[string]
	// City is the city.
	City core.Optional[string]
	// State is the state or region.
	State core.Optional[string]
	// Zipcode is the postal code.
	Zipcode core.Optional[string]
	// Country is the ISO 3166-1 alpha-2 code; it is upper-cased.
	Country core.Optional[string]
}

// Input is the attributes of a customer to create or update: only the set
// fields are assigned (lago assigns a parameter only when it is given,
// upsert_from_api_service.rb:53-81).
type Input struct {
	// ExternalID changes the external ID; only on update, and only while
	// the customer is editable.
	ExternalID core.Optional[string]
	// BillingEntity is the billing entity issuing the customer's
	// documents, by ID or code; a zero ref is the default entity.
	BillingEntity core.Optional[refs.Ref]
	// CustomerType is company or individual; "" clears it.
	CustomerType core.Optional[customer.Type]
	// AccountType is customer or partner; it changes only while the
	// customer is editable.
	AccountType core.Optional[customer.AccountType]
	// Name is the display name.
	Name core.Optional[*string]
	// Firstname is the first name.
	Firstname core.Optional[*string]
	// Lastname is the last name.
	Lastname core.Optional[*string]
	// LegalName is the legal name.
	LegalName core.Optional[*string]
	// LegalNumber is the registration number.
	LegalNumber core.Optional[*string]
	// TaxIdentificationNumber is the tax ID.
	TaxIdentificationNumber core.Optional[*string]
	// Email is the comma-separated email list; it is sanitized as stored.
	Email core.Optional[*string]
	// Phone is the phone number.
	Phone core.Optional[*string]
	// URL is the website.
	URL core.Optional[*string]
	// LogoURL is the logo URL.
	LogoURL core.Optional[*string]
	// BillingAddress updates the billing address.
	BillingAddress Address
	// ShippingAddress updates the shipping address.
	ShippingAddress Address
	// Currency is the default currency of the customer.
	Currency core.Optional[money.Currency]
	// Timezone is the time zone; nil inherits the billing entity's.
	Timezone core.Optional[*time.Location]
	// DocumentLocale is the documents' locale; nil inherits.
	DocumentLocale core.Optional[*string]
	// InvoiceGracePeriodDays is the grace period; nil inherits.
	InvoiceGracePeriodDays core.Optional[*int]
	// NetPaymentTermDays is the payment term; nil inherits.
	NetPaymentTermDays core.Optional[*int]
	// FinalizeZeroAmountInvoice says what happens to zero invoices.
	FinalizeZeroAmountInvoice core.Optional[customer.ZeroAmountFinalization]
	// IssuingDateAnchor anchors recurring invoices; "" inherits.
	IssuingDateAnchor core.Optional[core.IssuingDateAnchor]
	// IssuingDateAdjustment adjusts recurring invoices; "" inherits.
	IssuingDateAdjustment core.Optional[core.IssuingDateAdjustment]
	// SkipInvoiceCustomSections prints no invoice custom section.
	SkipInvoiceCustomSections core.Optional[bool]
	// InvoiceCustomSections select the sections printed, by ID or code;
	// empty falls back to the billing entity's.
	InvoiceCustomSections core.Optional[[]refs.Ref]
	// AppliedDunningCampaign is the customer's dunning campaign; a zero
	// ref falls back to the billing entity's.
	AppliedDunningCampaign core.Optional[refs.Ref]
	// ExcludeFromDunningCampaign excludes the customer from dunning.
	ExcludeFromDunningCampaign core.Optional[bool]
	// Taxes replace the customer's taxes, by ID or code.
	Taxes core.Optional[[]refs.Ref]
	// Metadata replaces the metadata list.
	Metadata core.Optional[core.Metadata]
}

// Create creates the customer externalID in org with the set attributes
// of in. An external ID used by a live customer is external_id
// value_already_exist.
//
// Lago's POST creates or updates (find_or_initialize_by external_id,
// upsert_from_api_service.rb:21); Tanvo's create only creates, and
// [Service.Upsert] carries the upsert.
//
// source: app/services/customers/upsert_from_api_service.rb:15-176
func (s *Service) Create(ctx context.Context, org core.OrganizationID, externalID string, in Input) (*customer.Customer, error) {
	var out *customer.Customer
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		if _, err := r.Customers.GetByExternalID(ctx, org, strings.TrimSpace(externalID)); err == nil {
			return fieldError("external_id", core.CodeAlreadyExists)
		} else if !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("find customer %q: %w", externalID, err)
		}
		c, err := s.write(ctx, r, org, nil, externalID, in)
		out = c
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("create customer: %w", err)
	}
	return out, nil
}

// Upsert updates the live customer externalID of org with in, or creates
// it when there is none, and reports whether it created it.
//
// source: app/services/customers/upsert_from_api_service.rb:15-176
func (s *Service) Upsert(ctx context.Context, org core.OrganizationID, externalID string, in Input) (*customer.Customer, bool, error) {
	var (
		out     *customer.Customer
		created bool
	)
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cur, err := r.Customers.GetByExternalID(ctx, org, strings.TrimSpace(externalID))
		switch {
		case errors.Is(err, core.ErrNotFound):
			cur, created = nil, true
		case err != nil:
			return fmt.Errorf("find customer %q: %w", externalID, err)
		}
		c, err := s.write(ctx, r, org, cur, externalID, in)
		out = c
		return err
	})
	if err != nil {
		return nil, false, fmt.Errorf("upsert customer: %w", err)
	}
	return out, created, nil
}

// Update changes the live customer id of org with the set attributes of
// in, carrying the changes over its draft invoices:
//
//   - a new payment term moves their payment due date when the applicable
//     term changes (update_invoice_payment_due_date_service.rb:14-27);
//   - a new issuing date anchor, adjustment or grace period moves their
//     issuing, expected finalization and payment due dates, then queues
//     the finalization of those now due
//     (update_invoice_issuing_date_settings_service.rb:18-35);
//   - new taxes flag them for refresh (apply_taxes_service.rb:27).
//
// external_id and account_type change only while the customer is
// editable; otherwise the change is ignored, as lago does
// (update_service.rb:103-109).
//
// source: app/services/customers/update_service.rb:21-196
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.CustomerID, in Input) (*customer.Customer, error) {
	var out *customer.Customer
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		cur, err := r.Customers.Get(ctx, org, id)
		if err != nil {
			return notFound(err)
		}
		if cur.Deleted() {
			return core.NotFound(Resource)
		}
		c, err := s.write(ctx, r, org, cur, cur.ExternalID, in)
		out = c
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("update customer %s: %w", id, err)
	}
	return out, nil
}

// Get returns the customer id of org, deleted or not.
func (s *Service) Get(ctx context.Context, org core.OrganizationID, id core.CustomerID) (*customer.Customer, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*customer.Customer, error) {
		c, err := r.Customers.Get(ctx, org, id)
		return c, notFound(err)
	})
}

// GetByExternalID returns the live customer with externalID (lago finds
// customers by external ID among the kept ones,
// customers_controller.rb:96-99).
func (s *Service) GetByExternalID(ctx context.Context, org core.OrganizationID, externalID string) (*customer.Customer, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (*customer.Customer, error) {
		c, err := r.Customers.GetByExternalID(ctx, org, externalID)
		return c, notFound(err)
	})
}

// List returns a page of the organization's customers matching f, in the
// order f.Order (the API lists newest first, lago CustomersQuery).
//
// source: app/queries/customers_query.rb:4-38
func (s *Service) List(ctx context.Context, org core.OrganizationID, f customer.Filter, page core.PageRequest) (core.Page[*customer.Customer], error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) (core.Page[*customer.Customer], error) {
		return r.Customers.List(ctx, org, f, page)
	})
}

// Taxes returns the taxes applied to c, in its order.
func (s *Service) Taxes(ctx context.Context, c *customer.Customer) ([]string, error) {
	return read(ctx, s.d, func(ctx context.Context, r port.Repos) ([]string, error) {
		return taxCodes(ctx, r, c)
	})
}

// Delete deletes the live customer id of org: it is archived at once and
// a job terminates its relations ([Service.TerminateRelations]). Lago
// sends no webhook (destroy_service.rb records an activity log only).
//
// source: app/services/customers/destroy_service.rb:18-28
func (s *Service) Delete(ctx context.Context, org core.OrganizationID, id core.CustomerID) (*customer.Customer, error) {
	var out *customer.Customer
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Customers.Get(ctx, org, id)
		if err != nil {
			return notFound(err)
		}
		if c.Deleted() {
			return core.NotFound(Resource)
		}
		if err := c.Archive(s.d.Now()); err != nil {
			return err
		}
		if err := r.Customers.Update(ctx, c); err != nil {
			return fmt.Errorf("archive customer: %w", err)
		}
		args := TerminateRelationsArgs{CustomerID: c.ID}
		if err := kernel.Enqueue(ctx, r, org, JobTerminateRelations, args, "customer:"+c.ID.String(), time.Time{}); err != nil {
			return err
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("delete customer %s: %w", id, err)
	}
	return out, nil
}

// write assigns in over cur (nil creates the customer externalID) and
// stores it with its side effects.
func (s *Service) write(ctx context.Context, r port.Repos, org core.OrganizationID, cur *customer.Customer, externalID string, in Input) (*customer.Customer, error) {
	if in.SkipInvoiceCustomSections.Set && in.SkipInvoiceCustomSections.Value && in.InvoiceCustomSections.Set {
		return nil, fieldError("invoice_custom_sections", CodeSkipSectionsAndSelectedIDs)
	}
	now := s.d.Now()
	created := cur == nil
	var c *customer.Customer
	var prev customer.Customer
	if created {
		be, err := s.billingEntity(ctx, r, org, in.BillingEntity.Value)
		if err != nil {
			return nil, err
		}
		c = customer.New(org, core.NewID[core.CustomerID](s.d.IDs), externalID, be.ID, now)
		if err := assignSequence(ctx, r, c); err != nil {
			return nil, err
		}
		prev = *c
	} else {
		prev = clone(cur)
		c = cur
		if in.BillingEntity.Set {
			be, err := s.billingEntity(ctx, r, org, in.BillingEntity.Value)
			if err != nil {
				return nil, err
			}
			c.BillingEntityID = be.ID
		}
		if in.ExternalID.Apply(&c.ExternalID) {
			c.ExternalID = strings.TrimSpace(c.ExternalID)
		}
	}
	assign(c, in)
	if !created && (c.ExternalID != prev.ExternalID || c.AccountType != prev.AccountType) {
		docs, err := documents(ctx, r, c)
		if err != nil {
			return nil, err
		}
		customer.KeepLocked(&prev, c, docs)
	}
	if err := s.dunning(ctx, r, c, &prev, in, created); err != nil {
		return nil, err
	}
	if in.Metadata.Set {
		c.Metadata = metadata(in.Metadata.Value)
	}
	var v core.Validator
	v.Merge("", c.Validate())
	if err := v.Err(); err != nil {
		return nil, err
	}
	if err := s.sections(ctx, r, c, in); err != nil {
		return nil, err
	}
	taxesChanged, vies, err := s.taxes(ctx, r, c, &prev, in, created)
	if err != nil {
		return nil, err
	}
	c.UpdatedAt = now
	if created {
		if err := r.Customers.Create(ctx, c); err != nil {
			return nil, externalIDConflict(err)
		}
	} else {
		if err := s.updateDrafts(ctx, r, &prev, c, in, taxesChanged); err != nil {
			return nil, err
		}
		if err := r.Customers.Update(ctx, c); err != nil {
			return nil, externalIDConflict(err)
		}
	}
	if err := s.applyVIES(ctx, r, c, vies); err != nil {
		return nil, err
	}
	t := webhook.EventTypeCustomerUpdated
	if created {
		t = webhook.EventTypeCustomerCreated
	}
	if err := kernel.Emit(ctx, s.d, org, t, Resource, c.ID.UUID(), c); err != nil {
		return nil, err
	}
	return c, nil
}

// assign sets the plain attributes of in on c, sanitized as lago's model
// stores them: null bytes are removed, blank address fields cleared, the
// email list sanitized and countries upper-cased (customer.rb:197-204).
//
// source: app/services/customers/upsert_from_api_service.rb:53-87
func assign(c *customer.Customer, in Input) {
	for _, f := range []struct {
		in  core.Optional[*string]
		dst **string
	}{
		{in.Name, &c.Name},
		{in.Firstname, &c.Firstname},
		{in.Lastname, &c.Lastname},
		{in.LegalName, &c.LegalName},
		{in.LegalNumber, &c.LegalNumber},
		{in.TaxIdentificationNumber, &c.TaxIdentificationNumber},
		{in.Phone, &c.Phone},
		{in.URL, &c.URL},
		{in.LogoURL, &c.LogoURL},
	} {
		if f.in.Apply(f.dst) && *f.dst != nil {
			*f.dst = new(stripNull(**f.dst))
		}
	}
	if in.Email.Apply(&c.Email) && c.Email != nil {
		c.Email = new(sanitizeEmail(*c.Email))
	}
	in.CustomerType.Apply(&c.CustomerType)
	in.AccountType.Apply(&c.AccountType)
	c.BillingAddress = assignAddress(c.BillingAddress, in.BillingAddress)
	c.ShippingAddress = assignAddress(c.ShippingAddress, in.ShippingAddress)
	in.Currency.Apply(&c.Currency)
	in.Timezone.Apply(&c.Timezone)
	in.DocumentLocale.Apply(&c.DocumentLocale)
	in.InvoiceGracePeriodDays.Apply(&c.InvoiceGracePeriodDays)
	in.NetPaymentTermDays.Apply(&c.NetPaymentTermDays)
	in.FinalizeZeroAmountInvoice.Apply(&c.FinalizeZeroAmountInvoice)
	in.IssuingDateAnchor.Apply(&c.IssuingDateAnchor)
	in.IssuingDateAdjustment.Apply(&c.IssuingDateAdjustment)
}

func assignAddress(a core.Address, in Address) core.Address {
	in.Line1.Apply(&a.Line1)
	in.Line2.Apply(&a.Line2)
	in.City.Apply(&a.City)
	in.State.Apply(&a.State)
	in.Zipcode.Apply(&a.Zipcode)
	if in.Country.Apply(&a.Country) {
		a.Country = strings.ToUpper(a.Country)
	}
	for _, p := range []*string{&a.Line1, &a.Line2, &a.City, &a.State, &a.Zipcode, &a.Country} {
		*p = stripNull(*p)
	}
	return a.Normalize()
}

// stripNull removes the null bytes of s.
func stripNull(s string) string { return strings.ReplaceAll(s, "\x00", "") }

// sanitizeEmail removes the null bytes of an email list and sanitizes it
// as lago's EmailSanitizer does.
func sanitizeEmail(s string) string { return payment.SanitizeEmail(stripNull(s)) }

// metadata returns the list m with display_in_invoice as given (lago
// defaults it to false, metadata/update_service.rb:40).
func metadata(m core.Metadata) core.Metadata {
	if len(m) == 0 {
		return nil
	}
	return slices.Clone(m)
}

// dunning applies the dunning settings of in: a campaign stops the
// exclusion, an exclusion clears the campaign, a partner is always
// excluded, and a change resets the dunning attempts.
//
// source: app/services/customers/update_service.rb:111-136
func (s *Service) dunning(ctx context.Context, r port.Repos, c, prev *customer.Customer, in Input, created bool) error {
	if in.AppliedDunningCampaign.Set {
		c.AppliedDunningCampaignID = core.DunningCampaignID{}
		if !in.AppliedDunningCampaign.Value.IsZero() {
			dc, err := dunningCampaigns(r, c.OrganizationID).One(ctx, in.AppliedDunningCampaign.Value)
			if err != nil {
				return err
			}
			c.AppliedDunningCampaignID = dc.ID
		}
		c.ExcludeFromDunningCampaign = false
	}
	if in.ExcludeFromDunningCampaign.Apply(&c.ExcludeFromDunningCampaign) && c.ExcludeFromDunningCampaign {
		c.AppliedDunningCampaignID = core.DunningCampaignID{}
	}
	if c.SelfBilled() {
		c.ExcludeFromDunningCampaign = true
		c.AppliedDunningCampaignID = core.DunningCampaignID{}
	}
	if !created && (c.AppliedDunningCampaignID != prev.AppliedDunningCampaignID || c.ExcludeFromDunningCampaign != prev.ExcludeFromDunningCampaign) {
		// customer.rb reset_dunning_campaign!
		c.DunningCampaignAttempts = map[string]int{}
		c.LastDunningCampaignAttemptAt = nil
	}
	return nil
}

// sections applies the invoice custom section settings of in: skipping
// clears the selection, a selection stops skipping. Unknown sections are
// ignored, as lago selects them with a where (:66-69).
//
// source: app/services/customers/manage_invoice_custom_sections_service.rb:16-37
func (s *Service) sections(ctx context.Context, r port.Repos, c *customer.Customer, in Input) error {
	if in.SkipInvoiceCustomSections.Set {
		if in.SkipInvoiceCustomSections.Value {
			c.InvoiceCustomSectionIDs = nil
		}
		c.SkipInvoiceCustomSections = in.SkipInvoiceCustomSections.Value
	}
	if !in.InvoiceCustomSections.Set {
		return nil
	}
	c.SkipInvoiceCustomSections = false
	var ids []core.InvoiceCustomSectionID
	f := sectionsFinder(r, c.OrganizationID)
	for _, ref := range in.InvoiceCustomSections.Value {
		sec, err := f.One(ctx, ref)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !slices.Contains(ids, sec.ID) {
			ids = append(ids, sec.ID)
		}
	}
	c.InvoiceCustomSectionIDs = ids
	return nil
}

func read[T any](ctx context.Context, d *kernel.Deps, fn func(context.Context, port.Repos) (T, error)) (T, error) {
	var out T
	err := d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		v, err := fn(ctx, r)
		out = v
		return err
	})
	return out, err
}

// clone returns a copy of c whose slices and maps are its own.
func clone(c *customer.Customer) customer.Customer {
	out := *c
	out.TaxIDs = slices.Clone(c.TaxIDs)
	out.InvoiceCustomSectionIDs = slices.Clone(c.InvoiceCustomSectionIDs)
	out.Metadata = slices.Clone(c.Metadata)
	return out
}

func notFound(err error) error {
	if errors.Is(err, core.ErrNotFound) {
		return core.NotFound(Resource)
	}
	return err
}

// externalIDConflict turns a uniqueness violation into external_id
// value_already_exist (upsert_from_api_service.rb:173-174).
func externalIDConflict(err error) error {
	if errors.Is(err, core.ErrConflict) {
		return fieldError("external_id", core.CodeAlreadyExists)
	}
	return fmt.Errorf("store customer: %w", err)
}

func fieldError(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}

// assignSequence numbers a new customer within its organization and sets
// its slug from the organization's document number prefix
// (customer.rb:140-141, :424-430).
func assignSequence(ctx context.Context, r port.Repos, c *customer.Customer) error {
	o, err := r.Organizations.Get(ctx, c.OrganizationID)
	if err != nil {
		return fmt.Errorf("load organization: %w", err)
	}
	seq, err := r.Sequences.Next(ctx, c.OrganizationID, port.SequenceKey{Name: port.SequenceCustomer})
	if err != nil {
		return fmt.Errorf("next customer sequence: %w", err)
	}
	c.AssignSequence(seq, o.DocumentNumberPrefix)
	return nil
}
