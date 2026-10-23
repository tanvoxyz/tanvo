package rpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	couponv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/coupon/v1"
	invoicev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/invoice/v1"
)

// PreviewInvoice implements InvoiceService.PreviewInvoice (lago
// Api::V1::InvoicesController#preview): the next invoice of a new
// subscription, or of stored ones as they are, terminated or changed to
// another plan, computed without storing anything.
func (s *Invoices) PreviewInvoice(ctx context.Context, req *invoicev1.PreviewInvoiceRequest) (*invoicev1.PreviewInvoiceResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in, err := previewRequest(req)
	if err != nil {
		return nil, err
	}
	in.OrganizationID = p.OrganizationID
	v, err := s.svc.PreviewRequest(ctx, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return invoicev1.PreviewInvoiceResponse_builder{Invoice: invoiceProto(v)}.Build(), nil
}

// previewRequest converts req; the organization is left to the caller.
func previewRequest(req *invoicev1.PreviewInvoiceRequest) (invoicing.PreviewRequest, error) {
	var (
		in  invoicing.PreviewRequest
		c   Ref
		err error
	)
	// With no customer_id, the customer does not exist yet.
	if req.HasCustomerId() {
		if c, err = ParseRef("customer_id", req.GetCustomerId()); err != nil {
			return in, err
		}
	}
	switch {
	case !req.HasCustomerId():
	case c.Code != "":
		return in, InvalidArgument("invalid reference", []FieldViolation{{
			Field: "customer_id", Reason: core.CodeInvalid, Description: "a customer has no code",
		}})
	case c.ExternalID != "":
		in.CustomerExternalID = c.ExternalID
	default:
		if in.CustomerID, err = ParseID[core.CustomerID]("customer_id", c.ID.String()); err != nil {
			return in, err
		}
	}
	if in.BillingEntity, err = CatalogRef("billing_entity_id", req.GetBillingEntityId()); err != nil {
		return in, err
	}
	if req.HasSubscription() {
		b := req.GetSubscription()
		ps := &invoicing.PreviewSubscription{BillingTime: period.Calendar}
		if ps.Plan, err = CatalogRef("subscription.plan_id", b.GetPlanId()); err != nil {
			return in, err
		}
		if b.HasSubscriptionAt() {
			ps.SubscriptionAt = b.GetSubscriptionAt().AsTime()
		}
		if b.HasCalendarBilling() && !b.GetCalendarBilling() {
			ps.BillingTime = period.Anniversary
		}
		in.Subscription = ps
	}
	for i, id := range req.GetSubscriptionIds() {
		field := fmt.Sprintf("subscription_ids[%d]", i)
		r, err := ParseRef(field, id)
		if err != nil {
			return in, err
		}
		sr := refs.SubscriptionRef{ExternalID: r.ExternalID}
		switch {
		case r.Code != "":
			return in, InvalidArgument("invalid reference", []FieldViolation{{
				Field: field, Reason: core.CodeInvalid, Description: "a subscription has no code",
			}})
		case r.ExternalID == "":
			if sr.ID, err = ParseID[core.SubscriptionID](field, r.ID.String()); err != nil {
				return in, err
			}
		}
		in.Subscriptions = append(in.Subscriptions, sr)
	}
	if req.HasTerminatedAt() {
		at := req.GetTerminatedAt().AsTime()
		in.TerminatedAt = &at
	}
	if in.TargetPlan, err = CatalogRef("plan_id", req.GetPlanId()); err != nil {
		return in, err
	}
	if in.Coupons, err = CatalogRefs("coupon_ids", req.GetCouponIds()); err != nil {
		return in, err
	}
	if req.HasCustomer() {
		if in.Customer, err = previewCustomer(req.GetCustomer()); err != nil {
			return in, err
		}
	}
	for i, b := range req.GetCoupons() {
		c, err := previewCoupon(fmt.Sprintf("coupons[%d]", i), b)
		if err != nil {
			return in, err
		}
		in.CouponAttributes = append(in.CouponAttributes, c)
	}
	return in, nil
}

// previewCustomer converts the customer attributes of a preview.
func previewCustomer(b *invoicev1.PreviewCustomer) (*invoicing.PreviewCustomer, error) {
	pc := &invoicing.PreviewCustomer{
		Name:                    Opt(b.HasName(), b.GetName()),
		TaxIdentificationNumber: Opt(b.HasTaxIdentificationNumber(), b.GetTaxIdentificationNumber()),
	}
	if code := b.GetCurrency(); code != "" {
		c, err := money.LookupCurrency(strings.ToUpper(code))
		if err != nil {
			return nil, previewViolation("customer.currency", core.CodeInvalidCurrency)
		}
		pc.Currency = c
	}
	if b.HasTimezone() {
		name := b.GetTimezone()
		loc, err := time.LoadLocation(name)
		if name == "" || name == "Local" || err != nil {
			return nil, previewViolation("customer.timezone", core.CodeInvalidTimezone)
		}
		pc.Timezone = loc
	}
	if a := b.GetBillingAddress(); a != nil {
		pc.BillingAddress = invoicing.PreviewAddress{
			Line1:   Opt(a.HasLine1(), a.GetLine1()),
			Line2:   Opt(a.HasLine2(), a.GetLine2()),
			City:    Opt(a.HasCity(), a.GetCity()),
			State:   Opt(a.HasState(), a.GetState()),
			Zipcode: Opt(a.HasZipcode(), a.GetZipcode()),
			Country: Opt(a.HasCountry(), a.GetCountry()),
		}
	}
	if a := b.GetShippingAddress(); a != nil {
		pc.ShippingAddress = core.Address{
			Line1: a.GetLine1(), Line2: a.GetLine2(), City: a.GetCity(),
			State: a.GetState(), Zipcode: a.GetZipcode(), Country: a.GetCountry(),
		}
	}
	for i, ic := range b.GetIntegrationCustomers() {
		r, err := CatalogRef(fmt.Sprintf("customer.integration_customers[%d].integration_id", i), ic.GetIntegrationId())
		if err != nil {
			return nil, err
		}
		pc.Integrations = append(pc.Integrations, r)
	}
	return pc, nil
}

// previewCoupon converts a coupon given by attributes (lago Coupon.new):
// reusable defaults to true, and the limitations, the ID and the status
// are ignored.
func previewCoupon(field string, b *couponv1.Coupon) (*coupon.Coupon, error) {
	c := &coupon.Coupon{
		Code:              b.GetCode(),
		Name:              b.GetName(),
		Description:       Opt(b.HasDescription(), b.GetDescription()),
		Type:              couponTypes.Core(b.GetCouponType()),
		Frequency:         couponFrequencies.Core(b.GetFrequency()),
		FrequencyDuration: OptInt(b.HasFrequencyDuration(), b.GetFrequencyDuration()),
		Reusable:          !b.HasReusable() || b.GetReusable(),
		Expiration:        couponExpirations.Core(b.GetExpiration()),
		ExpirationAt:      TimePtr(b.GetExpirationAt()),
	}
	var err error
	if c.Amount, c.Currency, err = couponAmount(field+".amount", b.HasAmount(), b); err != nil {
		return nil, err
	}
	if c.PercentageRate, err = couponRate(field+".percentage_rate", b.HasPercentageRate(), b.GetPercentageRate()); err != nil {
		return nil, err
	}
	return c, nil
}

// previewViolation is the validation error of a preview field.
func previewViolation(field, code string) error {
	return withReason(InvalidArgument("invalid preview", []FieldViolation{{Field: field, Reason: code}}), ReasonValidationErrors)
}
