package invoicing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// OneOffRequest is a one-off invoice as the API asks for it (lago
// Api::V1::InvoicesController#create): records by reference and amounts
// in currency units, resolved into a [OneOffInput].
type OneOffRequest struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// CustomerID is the customer, or zero for CustomerExternalID.
	CustomerID core.CustomerID
	// CustomerExternalID is the customer's external ID.
	CustomerExternalID string
	// BillingEntity is the issuing billing entity; zero uses the
	// customer's.
	BillingEntity refs.Ref
	// Currency is the invoice currency; zero uses the customer's.
	Currency money.Currency
	// Fees are the add-on lines.
	Fees []OneOffRequestFee
	// SkipAutomaticPayment leaves the payment to the customer.
	SkipAutomaticPayment bool
	// PaymentMethodType selects the payment method kind.
	PaymentMethodType core.PaymentMethodType
	// PaymentMethodID is the explicit payment method.
	PaymentMethodID core.PaymentMethodID
	// SkipInvoiceCustomSections prints no custom section.
	SkipInvoiceCustomSections bool
	// InvoiceCustomSections are the custom sections to print.
	InvoiceCustomSections []refs.Ref
	// PurchaseOrderNumber is printed on the invoice (blank is none).
	PurchaseOrderNumber *string
}

// OneOffRequestFee is one line of a [OneOffRequest].
type OneOffRequestFee struct {
	// AddOn is the add-on.
	AddOn refs.Ref
	// Units is the quantity; nil means 1.
	Units *money.Decimal
	// UnitAmount is the unit price in currency units; nil means the
	// add-on's.
	UnitAmount *money.Decimal
	// InvoiceDisplayName overrides the line name.
	InvoiceDisplayName *string
	// Description overrides the add-on's description.
	Description *string
	// From and To are the service period; both or neither.
	From, To *time.Time
	// Taxes replace the derived taxes.
	Taxes []refs.Ref
}

// CreateOneOffRequest resolves in and issues the one-off invoice
// ([Service.CreateOneOff]), returning its view. An unknown customer,
// billing entity, add-on or tax is not found, an unknown custom section
// is ignored; a unit price finer than the currency's minor unit is
// invalid.
func (s *Service) CreateOneOffRequest(ctx context.Context, in OneOffRequest) (*InvoiceView, error) {
	var out OneOffInput
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		var err error
		out, err = resolveOneOff(ctx, r, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.CreateOneOffView(ctx, out)
}

// resolveOneOff resolves the references of in.
func resolveOneOff(ctx context.Context, r port.Repos, in OneOffRequest) (OneOffInput, error) {
	org := in.OrganizationID
	out := OneOffInput{
		OrganizationID: org, Currency: in.Currency, SkipAutomaticPayment: in.SkipAutomaticPayment,
		PaymentMethodType: in.PaymentMethodType, PaymentMethodID: in.PaymentMethodID,
		PurchaseOrderNumber: in.PurchaseOrderNumber, SkipInvoiceCustomSections: in.SkipInvoiceCustomSections,
	}
	var (
		c   *customer.Customer
		err error
	)
	if in.CustomerID.IsZero() {
		c, err = r.Customers.GetByExternalID(ctx, org, in.CustomerExternalID)
	} else {
		c, err = r.Customers.Get(ctx, org, in.CustomerID)
		if err == nil && c.DeletedAt != nil {
			err = core.ErrNotFound
		}
	}
	if err != nil {
		return out, fmt.Errorf("load customer: %w", asNotFound(err, "customer"))
	}
	out.CustomerID = c.ID
	switch {
	case in.BillingEntity.ID != uuid.Nil:
		be, err := r.BillingEntities.Get(ctx, org, refID[core.BillingEntityID](in.BillingEntity))
		if err != nil {
			return out, fmt.Errorf("load billing entity: %w", asNotFound(err, "billing_entity"))
		}
		out.BillingEntityID = be.ID
	case in.BillingEntity.Code != "":
		be, err := r.BillingEntities.GetByCode(ctx, org, in.BillingEntity.Code)
		if err != nil {
			return out, fmt.Errorf("load billing entity: %w", asNotFound(err, "billing_entity"))
		}
		out.BillingEntityID = be.ID
	}
	cur := in.Currency
	if cur.IsZero() {
		cur = c.Currency
	}
	// Unknown custom sections are ignored (lago
	// app/services/invoices/create_one_off_service.rb:166-173).
	if !in.SkipInvoiceCustomSections {
		for _, ref := range in.InvoiceCustomSections {
			var cs *invoice.CustomSection
			if ref.Code != "" {
				cs, err = r.InvoiceCustomSections.GetByCode(ctx, org, ref.Code)
			} else {
				cs, err = r.InvoiceCustomSections.Get(ctx, org, refID[core.InvoiceCustomSectionID](ref))
			}
			if err := ignoreNotFound(err); err != nil {
				return out, fmt.Errorf("load invoice custom section %s: %w", ref, err)
			}
			if cs != nil && cs.DeletedAt == nil && !slices.Contains(out.InvoiceCustomSectionIDs, cs.ID) {
				out.InvoiceCustomSectionIDs = append(out.InvoiceCustomSectionIDs, cs.ID)
			}
		}
	}
	taxes := refs.Taxes(r, org)
	for i, f := range in.Fees {
		it := fees.OneOff{
			AddOnCode: f.AddOn.Code, Units: f.Units, InvoiceDisplayName: f.InvoiceDisplayName,
			Description: f.Description, From: f.From, To: f.To,
		}
		if f.AddOn.Code == "" {
			it.AddOnID = refID[core.AddOnID](f.AddOn)
			a, err := r.AddOns.Get(ctx, org, it.AddOnID)
			if err == nil && a.DeletedAt != nil {
				err = core.ErrNotFound
			}
			if err != nil {
				return out, fmt.Errorf("load add-on %s: %w", f.AddOn, asNotFound(err, "add_on"))
			}
		} else if _, err := r.AddOns.GetByCode(ctx, org, f.AddOn.Code); err != nil {
			return out, fmt.Errorf("load add-on %s: %w", f.AddOn, asNotFound(err, "add_on"))
		}
		if f.UnitAmount != nil {
			field := fmt.Sprintf("fees[%d].unit_amount", i)
			if cur.IsZero() {
				return out, fieldError("currency", core.CodeRequired)
			}
			if !f.UnitAmount.Mul(cur.SubunitToUnit()).IsInteger() {
				return out, fieldError(field, core.CodeInvalid)
			}
			minor, err := cur.ToMinor(*f.UnitAmount, money.HalfUp)
			if errors.Is(err, money.ErrOverflow) {
				return out, fieldError(field, core.CodeInvalid)
			}
			if err != nil {
				return out, err
			}
			it.UnitAmount = &minor
		}
		for _, ref := range f.Taxes {
			t, err := taxes.One(ctx, ref)
			if err != nil {
				return out, err
			}
			it.TaxCodes = append(it.TaxCodes, t.Code)
		}
		out.Fees = append(out.Fees, it)
	}
	return out, nil
}

// refID returns the ID of the reference by ID r (zero when invalid).
func refID[I core.Identifier[I]](r refs.Ref) I {
	id, _ := core.ParseID[I](r.ID.String())
	return id
}
