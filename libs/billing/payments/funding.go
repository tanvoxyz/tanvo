package payments

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// methodCustomerBalance is the provider method type of bank transfers to a
// customer balance (Stripe's customer_balance).
const methodCustomerBalance = "customer_balance"

// fundingSectionName is the name of the funding instructions sections
// (sync_funding_instructions_service.rb:53).
const fundingSectionName = "Funding Instructions"

// FundingFormatter prints a provider's funding instructions as the title
// and text of an invoice custom section in a document locale;
// libs/documents implements it.
type FundingFormatter interface {
	// FormatFundingInstructions returns the printed title and details of
	// the provider's raw funding instructions.
	FormatFundingInstructions(raw json.RawMessage, locale string) (displayName, details string, err error)
}

// WithFundingFormatter sets the formatter of the funding instructions
// sections; without one, customers paying by bank transfer get none.
func (s *Service) WithFundingFormatter(f FundingFormatter) *Service {
	s.funding = f
	return s
}

// FundingSectionCode is the code of the system-generated invoice custom
// section holding the funding instructions of customer c
// (sync_funding_instructions_service.rb:107-109).
func FundingSectionCode(c core.CustomerID) string { return "funding_instructions_" + c.String() }

// fundsByTransfer reports whether pc pays by bank transfer to a customer
// balance through a gateway giving funding instructions, with a formatter
// to print them.
func (s *Service) fundsByTransfer(gw port.PaymentProvider, pc *payment.ProviderCustomer) bool {
	_, ok := gw.(payment.FundingGateway)
	return ok && s.funding != nil && providerCustomerID(pc) != "" && slices.Contains(pc.ProviderPaymentMethods, methodCustomerBalance)
}

// enqueueFunding enqueues, in the transaction of r, the sync of pc's
// funding instructions when it pays by bank transfer (stripe_service.rb
// sync_funding_instructions, called on creation and update).
func (s *Service) enqueueFunding(ctx context.Context, r port.Repos, gw port.PaymentProvider, pc *payment.ProviderCustomer) error {
	if !s.fundsByTransfer(gw, pc) {
		return nil
	}
	return kernel.Enqueue(ctx, r, pc.OrganizationID, kernel.JobSyncProviderCustomer,
		kernel.ProviderCustomerArgs{ProviderCustomerID: pc.ID, Funding: true}, "provider_customer:"+pc.ID.String()+":funding", time.Time{})
}

// syncFunding gives t's customer, paying by bank transfer, the
// system-generated invoice custom section printing the bank details it
// transfers to: the provider's funding instructions in the customer's
// currency, else its billing entity's, formatted in the customer's
// document locale. A customer that already has the section is left alone;
// an organization section of that code is reused as it is. Assigning the
// section clears the customer's skip_invoice_custom_sections, since a
// customer skipping sections prints none, system ones included
// (sync_funding_instructions_service.rb:32-36,
// manage_invoice_custom_sections_service.rb:22-28).
//
// source: app/services/payment_provider_customers/stripe/sync_funding_instructions_service.rb:13-20
func (s *Service) syncFunding(ctx context.Context, t *syncTarget) error {
	if !s.fundsByTransfer(t.pc.gw, t.pcust) {
		return nil
	}
	org, code := t.cust.OrganizationID, FundingSectionCode(t.cust.ID)
	be, err := inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*organization.BillingEntity, error) {
		if has, err := hasFundingSection(ctx, r, t.cust.OrganizationID, t.cust.SystemInvoiceCustomSectionIDs, code); err != nil || has {
			return nil, err
		}
		be, err := r.BillingEntities.Get(ctx, org, t.cust.BillingEntityID)
		if err != nil {
			return nil, wrap(err, "load billing entity %s", t.cust.BillingEntityID)
		}
		return be, nil
	})
	if err != nil || be == nil {
		return err
	}
	currency := t.cust.Currency
	if currency.IsZero() {
		currency = be.DefaultCurrency
	}
	raw, err := t.pc.gw.(payment.FundingGateway).FundingInstructions(ctx, t.pc.conn, providerCustomerID(t.pcust), currency,
		t.cust.BillingAddress.Country, be.Address.Country)
	if err != nil {
		return wrap(err, "funding instructions of customer %s", t.cust.ID)
	}
	locale := t.cust.ApplicableDocumentLocale(kernel.DefaultsOf(be))
	return s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		c, err := r.Customers.Get(ctx, org, t.cust.ID)
		if isNotFound(err) || err == nil && c.Deleted() {
			return nil
		}
		if err != nil {
			return wrap(err, "load customer %s", t.cust.ID)
		}
		if has, err := hasFundingSection(ctx, r, org, c.SystemInvoiceCustomSectionIDs, code); err != nil || has {
			return err
		}
		sec, err := r.InvoiceCustomSections.GetByCode(ctx, org, code)
		switch {
		case isNotFound(err):
			if sec, err = s.createFundingSection(ctx, r, org, code, raw, locale); err != nil {
				return err
			}
		case err != nil:
			return wrap(err, "load invoice custom section %s", code)
		case sec.SectionType != invoice.CustomSectionTypeSystemGenerated:
			return fmt.Errorf("funding instructions section %s: a manual section has its code: %w", code, core.ErrConflict)
		}
		c.SystemInvoiceCustomSectionIDs = append(c.SystemInvoiceCustomSectionIDs, sec.ID)
		c.SkipInvoiceCustomSections = false
		c.UpdatedAt = s.d.Now()
		if err := r.Customers.Update(ctx, c); err != nil {
			return wrap(err, "update customer %s", c.ID)
		}
		return nil
	})
}

// hasFundingSection reports whether the system sections ids hold the
// section of code (sync_funding_instructions_service.rb:117).
func hasFundingSection(ctx context.Context, r port.Repos, org core.OrganizationID, ids []core.InvoiceCustomSectionID, code string) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	secs, err := r.InvoiceCustomSections.ListByIDs(ctx, org, ids)
	if err != nil {
		return false, wrap(err, "list invoice custom sections")
	}
	return slices.ContainsFunc(secs, func(s *invoice.CustomSection) bool { return s.Code == code }), nil
}

// createFundingSection creates the system-generated section of code
// printing the funding instructions raw in locale
// (sync_funding_instructions_service.rb:40-62).
func (s *Service) createFundingSection(ctx context.Context, r port.Repos, org core.OrganizationID, code string, raw json.RawMessage, locale string) (*invoice.CustomSection, error) {
	title, details, err := s.funding.FormatFundingInstructions(raw, locale)
	if err != nil {
		return nil, wrap(err, "format funding instructions")
	}
	now := s.d.Now()
	sec := &invoice.CustomSection{
		ID: core.NewID[core.InvoiceCustomSectionID](s.d.IDs), OrganizationID: org, Code: code, Name: fundingSectionName,
		DisplayName: &title, Details: &details, SectionType: invoice.CustomSectionTypeSystemGenerated, CreatedAt: now, UpdatedAt: now,
	}
	if err := sec.Validate(); err != nil {
		return nil, err
	}
	if err := r.InvoiceCustomSections.Create(ctx, sec); err != nil {
		return nil, wrap(err, "create invoice custom section %s", code)
	}
	return sec, nil
}
