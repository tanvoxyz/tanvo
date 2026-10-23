package rpc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/billing/usage"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
	subscriptionv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/subscription/v1/subscriptionv1connect"
)

// Subscriptions serves tanvo.subscription.v1.SubscriptionService over the
// billing library's subscription service (lago
// Api::V1::SubscriptionsController and
// Api::V1::Subscriptions::LifetimeUsagesController). Rate cards, the
// billing anchor date and billing segments have no implementation: they
// belong to contracts.
type Subscriptions struct {
	subscriptionv1connect.UnimplementedSubscriptionServiceHandler

	svc *subscriptions.Service
}

var _ subscriptionv1connect.SubscriptionServiceHandler = (*Subscriptions)(nil)

// NewSubscriptions returns the service over svc.
func NewSubscriptions(svc *subscriptions.Service) *Subscriptions { return &Subscriptions{svc: svc} }

var (
	subStatuses = NewEnum(map[subscription.Status]subscriptionv1.SubscriptionStatus{
		subscription.StatusPending:    subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_PENDING,
		subscription.StatusIncomplete: subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_INCOMPLETE,
		subscription.StatusActive:     subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE,
		subscription.StatusTerminated: subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_TERMINATED,
		subscription.StatusCanceled:   subscriptionv1.SubscriptionStatus_SUBSCRIPTION_STATUS_CANCELED,
	})
	subBillingTimes = NewEnum(map[period.BillingTime]subscriptionv1.BillingTime{
		period.Calendar:    subscriptionv1.BillingTime_BILLING_TIME_CALENDAR,
		period.Anniversary: subscriptionv1.BillingTime_BILLING_TIME_ANNIVERSARY,
	})
	subCancellationReasons = NewEnum(map[subscription.CancellationReason]subscriptionv1.CancellationReason{
		subscription.CancellationReasonPaymentFailed: subscriptionv1.CancellationReason_CANCELLATION_REASON_PAYMENT_FAILED,
		subscription.CancellationReasonTimeout:       subscriptionv1.CancellationReason_CANCELLATION_REASON_TIMEOUT,
		subscription.CancellationReasonManual:        subscriptionv1.CancellationReason_CANCELLATION_REASON_MANUAL,
	})
	subCreditNotes = NewEnum(map[subscription.TerminationCreditNote]subscriptionv1.TerminationCreditNote{
		subscription.TerminationCreditNoteCredit: subscriptionv1.TerminationCreditNote_TERMINATION_CREDIT_NOTE_CREDIT,
		subscription.TerminationCreditNoteRefund: subscriptionv1.TerminationCreditNote_TERMINATION_CREDIT_NOTE_REFUND,
		subscription.TerminationCreditNoteOffset: subscriptionv1.TerminationCreditNote_TERMINATION_CREDIT_NOTE_OFFSET,
		subscription.TerminationCreditNoteSkip:   subscriptionv1.TerminationCreditNote_TERMINATION_CREDIT_NOTE_SKIP,
	})
	subTerminationInvoices = NewEnum(map[subscription.TerminationInvoice]subscriptionv1.TerminationInvoice{
		subscription.TerminationInvoiceGenerate: subscriptionv1.TerminationInvoice_TERMINATION_INVOICE_GENERATE,
		subscription.TerminationInvoiceSkip:     subscriptionv1.TerminationInvoice_TERMINATION_INVOICE_SKIP,
	})
	subRuleTypes = NewEnum(map[subscription.ActivationRuleType]subscriptionv1.ActivationRuleType{
		subscription.ActivationRuleTypePayment: subscriptionv1.ActivationRuleType_ACTIVATION_RULE_TYPE_PAYMENT,
	})
	subRuleStatuses = NewEnum(map[subscription.ActivationRuleStatus]subscriptionv1.ActivationRuleStatus{
		subscription.ActivationRuleStatusInactive:      subscriptionv1.ActivationRuleStatus_ACTIVATION_RULE_STATUS_INACTIVE,
		subscription.ActivationRuleStatusPending:       subscriptionv1.ActivationRuleStatus_ACTIVATION_RULE_STATUS_PENDING,
		subscription.ActivationRuleStatusSatisfied:     subscriptionv1.ActivationRuleStatus_ACTIVATION_RULE_STATUS_SATISFIED,
		subscription.ActivationRuleStatusNotApplicable: subscriptionv1.ActivationRuleStatus_ACTIVATION_RULE_STATUS_NOT_APPLICABLE,
		subscription.ActivationRuleStatusDeclined:      subscriptionv1.ActivationRuleStatus_ACTIVATION_RULE_STATUS_DECLINED,
		subscription.ActivationRuleStatusFailed:        subscriptionv1.ActivationRuleStatus_ACTIVATION_RULE_STATUS_FAILED,
		subscription.ActivationRuleStatusExpired:       subscriptionv1.ActivationRuleStatus_ACTIVATION_RULE_STATUS_EXPIRED,
	})
	subPaymentMethodTypes = NewEnum(map[core.PaymentMethodType]commonv1.PaymentMethodType{
		core.PaymentMethodTypeProvider: commonv1.PaymentMethodType_PAYMENT_METHOD_TYPE_PROVIDER,
		core.PaymentMethodTypeManual:   commonv1.PaymentMethodType_PAYMENT_METHOD_TYPE_MANUAL,
	})
)

// subscriptionUpdateFields are the update_mask paths of
// UpdateSubscription (lago SubscriptionsController#update_params).
var subscriptionUpdateFields = []string{
	"name", "subscription_at", "ending_at", "purchase_order_number", "progressive_billing_disabled",
	"consolidate_invoice", "on_termination_credit_note", "on_termination_invoice", "payment_method_type",
	"payment_method_id", "billing_entity_id", "skip_invoice_custom_sections", "invoice_custom_section_ids",
	"activation_rules", "plan_overrides", "usage_thresholds",
}

// subscriptionFields renames the domain's field paths to the proto's.
var subscriptionFields = map[string]string{
	"customer":       "customer_id",
	"plan":           "plan_id",
	"billing_entity": "billing_entity_id",
	"payment_method": "payment_method_id",
	"timeout_hours":  "activation_rules.timeout_hours",
}

// subscriptionFilterFields are the fields ListSubscriptions filters on.
var subscriptionFilterFields = []string{"status", "plan_id", "external_id", "billing_entity_id", "overridden"}

// CreateSubscription implements SubscriptionService.CreateSubscription
// (lago Subscriptions::CreateService): a customer referenced by
// `external_id:` that does not exist is created; an external ID that
// already has a subscription is a plan change, made with
// ChangeSubscriptionPlan instead.
func (s *Subscriptions) CreateSubscription(ctx context.Context, req *subscriptionv1.CreateSubscriptionRequest) (*subscriptionv1.CreateSubscriptionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetSubscription()
	if err := subscriptionUnsupported(b); err != nil {
		return nil, err
	}
	in := subscriptions.CreateInput{
		OrganizationID:             p.OrganizationID,
		ExternalID:                 b.GetExternalId(),
		Name:                       Opt(b.HasName(), b.GetName()),
		BillingTime:                subBillingTimes.Core(b.GetBillingTime()),
		EndingAt:                   TimePtr(b.GetEndingAt()),
		ProgressiveBillingDisabled: b.GetProgressiveBillingDisabled(),
		ConsolidateInvoice:         Opt(b.HasConsolidateInvoice(), b.GetConsolidateInvoice()),
		PurchaseOrderNumber:        Opt(b.HasPurchaseOrderNumber(), b.GetPurchaseOrderNumber()),
		OnTerminationInvoice:       subTerminationInvoices.Core(b.GetOnTerminationInvoice()),
		OnTerminationCreditNote:    subCreditNotes.Core(b.GetOnTerminationCreditNote()),
	}
	if b.HasSubscriptionAt() {
		in.SubscriptionAt = new(Time(b.GetSubscriptionAt()))
	}
	if in.Customer, err = subCustomerRef("subscription.customer_id", b.GetCustomerId()); err != nil {
		return nil, err
	}
	if in.Plan, err = CatalogRef("subscription.plan_id", b.GetPlanId()); err != nil {
		return nil, err
	}
	if in.BillingEntity, err = CatalogRef("subscription.billing_entity_id", b.GetBillingEntityId()); err != nil {
		return nil, err
	}
	if in.PaymentMethod, err = subPaymentMethod(b, b.HasPaymentMethodType(), b.HasPaymentMethodId()); err != nil {
		return nil, err
	}
	if b.HasSkipInvoiceCustomSections() || len(b.GetInvoiceCustomSectionIds()) > 0 {
		if in.InvoiceCustomSections, err = subCustomSections(b); err != nil {
			return nil, err
		}
	}
	in.ActivationRules = subActivationRules(b.GetActivationRules())
	if b.HasPlanOverrides() || len(b.GetUsageThresholds()) > 0 {
		cur, err := s.svc.PlanCurrency(ctx, p.OrganizationID, in.Plan)
		if err != nil {
			return nil, subscriptionError(ctx, "subscription.", err)
		}
		if b.HasPlanOverrides() {
			po, err := subPlanOverrides("subscription.plan_overrides", b.GetPlanOverrides(), cur)
			if err != nil {
				return nil, err
			}
			in.PlanOverrides = &po
			if po.Currency.Set {
				cur = po.Currency.Value
			}
		}
		if in.UsageThresholds, err = subThresholds("subscription.usage_thresholds", b.GetUsageThresholds(), cur); err != nil {
			return nil, err
		}
	}
	v, err := s.svc.Create(ctx, in)
	if err != nil {
		return nil, subscriptionError(ctx, "subscription.", err)
	}
	return subscriptionv1.CreateSubscriptionResponse_builder{Subscription: subscriptionProto(v)}.Build(), nil
}

// GetSubscription implements SubscriptionService.GetSubscription (lago
// SubscriptionsController#show): an `external_id:` reference returns the
// active subscription, else the incomplete one, else the pending one.
func (s *Subscriptions) GetSubscription(ctx context.Context, req *subscriptionv1.GetSubscriptionRequest) (*subscriptionv1.GetSubscriptionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := subRef("subscription_id", req.GetSubscriptionId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.Get(ctx, p.OrganizationID, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.GetSubscriptionResponse_builder{Subscription: subscriptionProto(v)}.Build(), nil
}

// ListSubscriptions implements SubscriptionService.ListSubscriptions (lago
// SubscriptionIndex and SubscriptionsQuery): the active subscriptions
// unless the filter names statuses, most recent first.
func (s *Subscriptions) ListSubscriptions(ctx context.Context, req *subscriptionv1.ListSubscriptionsRequest) (*subscriptionv1.ListSubscriptionsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in, err := subscriptionListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	in.OrganizationID, in.Search, in.Page = p.OrganizationID, req.GetSearch(), Page(req.GetPageSize(), req.GetPageToken())
	if req.GetCustomerId() != "" {
		if in.Customer, err = subCustomerRef("customer_id", req.GetCustomerId()); err != nil {
			return nil, err
		}
	}
	page, err := s.svc.List(ctx, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.ListSubscriptionsResponse_builder{
		Subscriptions: Items(page, subscriptionProto),
		NextPageToken: new(page.NextToken),
	}.Build(), nil
}

// UpdateSubscription implements SubscriptionService.UpdateSubscription
// (lago Subscriptions::UpdateService).
func (s *Subscriptions) UpdateSubscription(ctx context.Context, req *subscriptionv1.UpdateSubscriptionRequest) (*subscriptionv1.UpdateSubscriptionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetSubscription()
	paths, err := UpdatePaths(req.GetUpdateMask(), subscriptionUpdateFields, func(path string) bool {
		switch path {
		case "invoice_custom_section_ids":
			return len(b.GetInvoiceCustomSectionIds()) > 0
		case "activation_rules":
			return len(b.GetActivationRules()) > 0
		case "usage_thresholds":
			return len(b.GetUsageThresholds()) > 0
		}
		return b.ProtoReflect().Has(b.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(subFieldName(path))))
	})
	if err != nil {
		return nil, err
	}
	if len(b.GetRateCards()) > 0 || b.HasBillingAnchorDate() {
		return nil, connect.NewError(connect.CodeUnimplemented, "rate cards and billing anchor dates are not supported")
	}
	in := subscriptions.UpdateInput{OrganizationID: p.OrganizationID}
	if in.Subscription, err = subRef("subscription.id", b.GetId()); err != nil {
		return nil, err
	}
	var pmType, pmID, sections bool
	for _, path := range paths {
		switch path {
		case "name":
			in.Name = core.Some(b.GetName())
		case "subscription_at":
			in.SubscriptionAt = core.Some(Time(b.GetSubscriptionAt()))
		case "ending_at":
			in.EndingAt = core.Some(TimePtr(b.GetEndingAt()))
		case "purchase_order_number":
			in.PurchaseOrderNumber = core.Some(Opt(b.HasPurchaseOrderNumber(), b.GetPurchaseOrderNumber()))
		case "progressive_billing_disabled":
			in.ProgressiveBillingDisabled = core.Some(b.GetProgressiveBillingDisabled())
		case "consolidate_invoice":
			in.ConsolidateInvoice = core.Some(b.GetConsolidateInvoice())
		case "on_termination_credit_note":
			in.OnTerminationCreditNote = core.Some(subCreditNotes.Core(b.GetOnTerminationCreditNote()))
		case "on_termination_invoice":
			in.OnTerminationInvoice = core.Some(subTerminationInvoices.Core(b.GetOnTerminationInvoice()))
		case "payment_method_type":
			pmType = true
		case "payment_method_id":
			pmID = true
		case "billing_entity_id":
			r, err := CatalogRef("subscription.billing_entity_id", b.GetBillingEntityId())
			if err != nil {
				return nil, err
			}
			in.BillingEntity = core.Some(r)
		case "skip_invoice_custom_sections", "invoice_custom_section_ids":
			sections = true
		case "activation_rules":
			in.ActivationRules = core.Some(subActivationRules(b.GetActivationRules()))
		}
	}
	if in.PaymentMethod, err = subPaymentMethod(b, pmType, pmID); err != nil {
		return nil, err
	}
	if sections {
		if in.InvoiceCustomSections, err = subCustomSections(b); err != nil {
			return nil, err
		}
	}
	overrides, thresholds := slices.Contains(paths, "plan_overrides"), slices.Contains(paths, "usage_thresholds")
	if overrides || thresholds {
		cur, err := s.svc.SubscriptionCurrency(ctx, p.OrganizationID, in.Subscription)
		if err != nil {
			return nil, subscriptionError(ctx, "subscription.", err)
		}
		if overrides {
			po, err := subPlanOverrides("subscription.plan_overrides", b.GetPlanOverrides(), cur)
			if err != nil {
				return nil, err
			}
			in.PlanOverrides = core.Some(po)
			if po.Currency.Set {
				cur = po.Currency.Value
			}
		}
		if thresholds {
			ts, err := subThresholds("subscription.usage_thresholds", b.GetUsageThresholds(), cur)
			if err != nil {
				return nil, err
			}
			in.UsageThresholds = core.Some(ts)
		}
	}
	v, err := s.svc.Update(ctx, in)
	if err != nil {
		return nil, subscriptionError(ctx, "subscription.", err)
	}
	return subscriptionv1.UpdateSubscriptionResponse_builder{Subscription: subscriptionProto(v)}.Build(), nil
}

// ChangeSubscriptionPlan implements SubscriptionService.ChangeSubscriptionPlan
// (lago Subscriptions::CreateService on an existing external ID): an
// upgrade applies now, a downgrade at the end of the period, and a
// subscription starting in the future changes in place.
func (s *Subscriptions) ChangeSubscriptionPlan(ctx context.Context, req *subscriptionv1.ChangeSubscriptionPlanRequest) (*subscriptionv1.ChangeSubscriptionPlanResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := subscriptions.ChangePlanInput{OrganizationID: p.OrganizationID, Name: Opt(req.HasName(), req.GetName())}
	if in.Subscription, err = subRef("subscription_id", req.GetSubscriptionId()); err != nil {
		return nil, err
	}
	if in.Plan, err = CatalogRef("plan_id", req.GetPlanId()); err != nil {
		return nil, err
	}
	if req.HasPlanOverrides() {
		cur, err := s.svc.PlanCurrency(ctx, p.OrganizationID, in.Plan)
		if err != nil {
			return nil, subscriptionError(ctx, "", err)
		}
		po, err := subPlanOverrides("plan_overrides", req.GetPlanOverrides(), cur)
		if err != nil {
			return nil, err
		}
		in.PlanOverrides = &po
	}
	pc, err := s.svc.ChangePlan(ctx, in)
	if err != nil {
		return nil, subscriptionError(ctx, "", err)
	}
	out := subscriptionv1.ChangeSubscriptionPlanResponse_builder{Subscription: subscriptionProto(pc.Subscription)}
	if pc.Previous != nil {
		out.PreviousSubscription = subscriptionProto(pc.Previous)
	}
	return out.Build(), nil
}

// TerminateSubscription implements SubscriptionService.TerminateSubscription
// (lago SubscriptionsController#terminate): an `external_id:` reference
// terminates the active subscription; an ID terminates the subscription
// in any status, canceling a pending or incomplete one.
func (s *Subscriptions) TerminateSubscription(ctx context.Context, req *subscriptionv1.TerminateSubscriptionRequest) (*subscriptionv1.TerminateSubscriptionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	in := subscriptions.TerminateInput{
		OrganizationID:          p.OrganizationID,
		OnTerminationInvoice:    subTerminationInvoices.Core(req.GetOnTerminationInvoice()),
		OnTerminationCreditNote: subCreditNotes.Core(req.GetOnTerminationCreditNote()),
	}
	if in.Subscription, err = subRef("subscription_id", req.GetSubscriptionId()); err != nil {
		return nil, err
	}
	v, err := s.svc.Terminate(ctx, in)
	if err != nil {
		return nil, subscriptionError(ctx, "", err)
	}
	return subscriptionv1.TerminateSubscriptionResponse_builder{Subscription: subscriptionProto(v)}.Build(), nil
}

// CancelSubscription implements SubscriptionService.CancelSubscription
// (lago SubscriptionsController#terminate with status=pending): it cancels
// a pending or incomplete subscription.
func (s *Subscriptions) CancelSubscription(ctx context.Context, req *subscriptionv1.CancelSubscriptionRequest) (*subscriptionv1.CancelSubscriptionResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := subRef("subscription_id", req.GetSubscriptionId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.Cancel(ctx, p.OrganizationID, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.CancelSubscriptionResponse_builder{Subscription: subscriptionProto(v)}.Build(), nil
}

// GetLifetimeUsage implements SubscriptionService.GetLifetimeUsage (lago
// Subscriptions::LifetimeUsagesController#show). include_metrics has no
// counterpart and is ignored: metric_units stays empty.
func (s *Subscriptions) GetLifetimeUsage(ctx context.Context, req *subscriptionv1.GetLifetimeUsageRequest) (*subscriptionv1.GetLifetimeUsageResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := subRef("subscription_id", req.GetSubscriptionId())
	if err != nil {
		return nil, err
	}
	lv, err := s.svc.LifetimeUsage(ctx, p.OrganizationID, ref)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.GetLifetimeUsageResponse_builder{LifetimeUsage: subLifetimeProto(lv)}.Build(), nil
}

// UpdateLifetimeUsage implements SubscriptionService.UpdateLifetimeUsage
// (lago LifetimeUsages::UpdateService): the amount is in major units of
// the lifetime usage's currency.
func (s *Subscriptions) UpdateLifetimeUsage(ctx context.Context, req *subscriptionv1.UpdateLifetimeUsageRequest) (*subscriptionv1.UpdateLifetimeUsageResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := subRef("subscription_id", req.GetSubscriptionId())
	if err != nil {
		return nil, err
	}
	amount, err := ParseDecimal("external_historical_usage_amount", req.GetExternalHistoricalUsageAmount())
	if err != nil {
		return nil, err
	}
	lv, err := s.svc.UpdateLifetimeUsage(ctx, p.OrganizationID, ref, amount)
	if err != nil {
		return nil, Error(ctx, err)
	}
	return subscriptionv1.UpdateLifetimeUsageResponse_builder{LifetimeUsage: subLifetimeProto(lv)}.Build(), nil
}

// subscriptionUnsupported refuses the create fields without an
// implementation.
func subscriptionUnsupported(b *subscriptionv1.Subscription) error {
	switch {
	case len(b.GetRateCards()) > 0 || b.HasBillingAnchorDate():
		return connect.NewError(connect.CodeUnimplemented, "rate cards and billing anchor dates are not supported")
	case !b.HasPlanId():
		return InvalidArgument("invalid subscription", []FieldViolation{{Field: "subscription.plan_id", Reason: core.CodeRequired}})
	}
	return nil
}

// subFieldName is the proto field name of an update path.
func subFieldName(path string) string {
	return strings.SplitN(path, ".", 2)[0]
}

// subRef parses a subscription reference: an ID or `external_id:`.
func subRef(field, s string) (subscriptions.SubscriptionRef, error) {
	r, err := ParseRef(field, s)
	if err != nil {
		return subscriptions.SubscriptionRef{}, err
	}
	if r.Code != "" {
		return subscriptions.SubscriptionRef{}, InvalidArgument("invalid reference", []FieldViolation{{
			Field: field, Reason: core.CodeInvalid, Description: "a subscription has no code",
		}})
	}
	out := subscriptions.SubscriptionRef{ExternalID: r.ExternalID}
	if r.ExternalID == "" {
		if out.ID, err = ParseID[core.SubscriptionID](field, r.ID.String()); err != nil {
			return subscriptions.SubscriptionRef{}, err
		}
	}
	return out, nil
}

// subCustomerRef parses a customer reference: an ID or `external_id:`.
func subCustomerRef(field, s string) (subscriptions.CustomerRef, error) {
	r, err := ParseRef(field, s)
	if err != nil {
		return subscriptions.CustomerRef{}, err
	}
	if r.Code != "" {
		return subscriptions.CustomerRef{}, InvalidArgument("invalid reference", []FieldViolation{{
			Field: field, Reason: core.CodeInvalid, Description: "a customer has no code",
		}})
	}
	out := subscriptions.CustomerRef{ExternalID: r.ExternalID}
	if r.ExternalID == "" {
		if out.ID, err = ParseID[core.CustomerID](field, r.ID.String()); err != nil {
			return subscriptions.CustomerRef{}, err
		}
	}
	return out, nil
}

// subPaymentMethod returns the payment method of b when its type or ID is
// present.
func subPaymentMethod(b *subscriptionv1.Subscription, hasType, hasID bool) (*subscriptions.PaymentMethod, error) {
	if !hasType && !hasID {
		return nil, nil
	}
	pm := &subscriptions.PaymentMethod{}
	if hasType {
		pm.Type = new(subPaymentMethodTypes.Core(b.GetPaymentMethodType()))
	}
	if hasID && b.GetPaymentMethodId() != "" {
		id, err := ParseID[core.PaymentMethodID]("subscription.payment_method_id", b.GetPaymentMethodId())
		if err != nil {
			return nil, err
		}
		pm.ID = &id
	}
	return pm, nil
}

// subCustomSections returns the invoice custom section selection of b.
func subCustomSections(b *subscriptionv1.Subscription) (*subscriptions.CustomSections, error) {
	rs, err := CatalogRefs("subscription.invoice_custom_section_ids", b.GetInvoiceCustomSectionIds())
	if err != nil {
		return nil, err
	}
	return &subscriptions.CustomSections{
		Skip:     Opt(b.HasSkipInvoiceCustomSections(), b.GetSkipInvoiceCustomSections()),
		Sections: rs,
	}, nil
}

// subActivationRules converts the requested activation rules.
func subActivationRules(in []*subscriptionv1.ActivationRule) []subscriptions.ActivationRule {
	if in == nil {
		return nil
	}
	out := make([]subscriptions.ActivationRule, len(in))
	for i, a := range in {
		out[i] = subscriptions.ActivationRule{Type: subRuleTypes.Core(a.GetRuleType()), TimeoutHours: int(a.GetTimeoutHours())}
	}
	return out
}

// subscriptionListFilter parses the filter of ListSubscriptions.
func subscriptionListFilter(s string) (subscriptions.ListInput, error) {
	var in subscriptions.ListInput
	e, err := ParseFilter(s)
	if err != nil {
		return in, err
	}
	eq, err := Equalities(e, subscriptionFilterFields...)
	if err != nil {
		return in, err
	}
	statuses, err := FilterEnum[subscriptionv1.SubscriptionStatus]("status", eq["status"], subscriptionv1.SubscriptionStatus(0).Descriptor())
	if err != nil {
		return in, err
	}
	for _, st := range statuses {
		in.Statuses = append(in.Statuses, subStatuses.Core(st))
	}
	plan, err := FilterSingle("plan_id", eq["plan_id"])
	if err != nil {
		return in, err
	}
	if in.Plan, err = CatalogRef(FilterField, plan); err != nil {
		return in, err
	}
	if in.ExternalID, err = FilterSingle("external_id", eq["external_id"]); err != nil {
		return in, err
	}
	for _, v := range eq["billing_entity_id"] {
		r, err := CatalogRef(FilterField, v)
		if err != nil {
			return in, err
		}
		in.BillingEntities = append(in.BillingEntities, r)
	}
	in.Overridden, err = FilterBool("overridden", eq["overridden"])
	return in, err
}

// subscriptionError maps a service error, naming the request's fields.
func subscriptionError(ctx context.Context, prefix string, err error) error {
	v, ok := validationViolations("", err)
	if !ok {
		return Error(ctx, err)
	}
	for i := range v {
		if f, ok := subscriptionFields[v[i].Field]; ok {
			v[i].Field = f
		}
		v[i].Field = prefix + v[i].Field
	}
	return withReason(InvalidArgument("invalid subscription", v), ReasonValidationErrors)
}

// subEnum is a pointer to the proto enumeration value p, nil for
// UNSPECIFIED.
func subEnum[P ~int32](p P) *P {
	if p == 0 {
		return nil
	}
	return &p
}

// subID is a pointer to the string form of id, nil for the zero ID.
func subID[I interface {
	IsZero() bool
	String() string
}](id I) *string {
	if id.IsZero() {
		return nil
	}
	return new(id.String())
}

// subscriptionProto converts a subscription view (lago
// V1::SubscriptionSerializer).
func subscriptionProto(v *subscriptions.View) *subscriptionv1.Subscription {
	s := v.Subscription
	b := subscriptionv1.Subscription_builder{
		Id:                         new(s.ID.String()),
		ExternalId:                 new(s.ExternalID),
		CustomerId:                 new(s.CustomerID.String()),
		PlanId:                     new(s.PlanID.String()),
		Name:                       s.Name,
		BillingTime:                subEnum(subBillingTimes.Proto(s.BillingTime)),
		Status:                     subEnum(subStatuses.Proto(s.Status)),
		SubscriptionAt:             Timestamp(s.SubscriptionAt),
		EndingAt:                   TimestampPtr(s.EndingAt),
		StartedAt:                  TimestampPtr(s.StartedAt),
		TerminatedAt:               TimestampPtr(s.TerminatedAt),
		CanceledAt:                 TimestampPtr(s.CanceledAt),
		CancellationReason:         subEnum(subCancellationReasons.Proto(s.CancellationReason)),
		TrialEndedAt:               TimestampPtr(s.TrialEndedAt),
		PreviousSubscriptionId:     subID(s.PreviousSubscriptionID),
		OnTerminationCreditNote:    subEnum(subCreditNotes.Proto(s.OnTerminationCreditNote)),
		OnTerminationInvoice:       subEnum(subTerminationInvoices.Proto(s.OnTerminationInvoice)),
		PaymentMethodType:          subEnum(subPaymentMethodTypes.Proto(s.PaymentMethodType)),
		PaymentMethodId:            subID(s.PaymentMethodID),
		ProgressiveBillingDisabled: new(s.ProgressiveBillingDisabled),
		ConsolidateInvoice:         new(s.ConsolidateInvoice),
		PurchaseOrderNumber:        s.PurchaseOrderNumber,
		BillingEntityId:            subID(s.BillingEntityID),
		SkipInvoiceCustomSections:  new(s.SkipInvoiceCustomSections),
		SkipDailyUsage:             new(s.SkipDailyUsage),
		NextBillingAt:              TimestampPtr(s.NextBillingAt),
		CreatedAt:                  Timestamp(s.CreatedAt),
		UpdatedAt:                  Timestamp(s.UpdatedAt),
	}
	for _, id := range s.InvoiceCustomSectionIDs {
		b.InvoiceCustomSectionIds = append(b.InvoiceCustomSectionIds, id.String())
	}
	if v.Plan != nil && v.Plan.IsChild() {
		b.ParentPlanId = new(v.Plan.ParentID.String())
	}
	if v.Next != nil {
		b.NextSubscriptionId = new(v.Next.ID.String())
		if v.Downgrade {
			b.DowngradePlanId = new(v.Next.PlanID.String())
		}
	}
	for _, r := range v.ActivationRules {
		b.ActivationRules = append(b.ActivationRules, subscriptionv1.ActivationRule_builder{
			RuleType:     subEnum(subRuleTypes.Proto(r.RuleType)),
			TimeoutHours: new(int32(r.TimeoutHours)),
			Status:       subEnum(subRuleStatuses.Proto(r.Status)),
			ExpiresAt:    TimestampPtr(r.ExpiresAt),
		}.Build())
	}
	if v.CurrentPeriod != nil {
		b.CurrentPeriodStartedAt = Timestamp(v.CurrentPeriod.From)
		b.CurrentPeriodEndedAt = Timestamp(v.CurrentPeriod.To)
	}
	for _, t := range v.UsageThresholds {
		b.UsageThresholds = append(b.UsageThresholds, subUsageThresholdProto(t, v.Plan.Currency))
	}
	return b.Build()
}

// subLifetimeProto converts a lifetime usage view (lago
// V1::LifetimeUsageSerializer): the threshold progress gives the amount
// left to reach each threshold.
func subLifetimeProto(lv *subscriptions.LifetimeView) *subscriptionv1.LifetimeUsage {
	lu := lv.LifetimeUsage
	cur := lu.Currency
	b := subscriptionv1.LifetimeUsage_builder{
		SubscriptionId:                new(lu.SubscriptionID.String()),
		ExternalSubscriptionId:        new(lu.ExternalSubscriptionID),
		ExternalHistoricalUsageAmount: Money(lu.HistoricalUsageAmount, cur),
		InvoicedUsageAmount:           Money(lu.InvoicedUsageAmount, cur),
		CurrentUsageAmount:            Money(lu.CurrentUsageAmount, cur),
		RefreshedAt:                   TimestampPtr(subLatest(lu.InvoicedUsageAmountRefreshedAt, lu.CurrentUsageAmountRefreshedAt)),
	}
	total := lu.HistoricalUsageAmount + lu.InvoicedUsageAmount + lu.CurrentUsageAmount
	for _, tp := range lv.Thresholds {
		b.UsageThresholds = append(b.UsageThresholds, subThresholdProto(tp, total, cur))
	}
	return b.Build()
}

// subThresholdProto converts the progress toward one usage threshold.
func subThresholdProto(tp usage.ThresholdProgress, total int64, cur money.Currency) *subscriptionv1.UsageThresholdProgress {
	remaining := max(tp.Amount-total, 0)
	if tp.Reached {
		remaining = 0
	}
	return subscriptionv1.UsageThresholdProgress_builder{
		Amount:          new(cur.FromMinor(tp.Amount).StringFixed(cur.Exponent())),
		Recurring:       new(tp.Threshold != nil && tp.Threshold.Recurring),
		RemainingAmount: new(cur.FromMinor(remaining).StringFixed(cur.Exponent())),
		ReachedAt:       TimestampPtr(tp.ReachedAt),
	}.Build()
}

// subLatest is the later of two optional times.
func subLatest[T interface{ After(T) bool }](a, b *T) *T {
	switch {
	case a == nil:
		return b
	case b == nil || (*a).After(*b):
		return a
	}
	return b
}

// subPlanOverrides converts the plan overrides po at field, whose amounts
// are in the currency of the overridden amount, else in cur. Each charge
// and fixed charge names the one it overrides by its id.
func subPlanOverrides(field string, po *subscriptionv1.PlanOverrides, cur money.Currency) (plans.OverrideInput, error) {
	var in plans.OverrideInput
	if po.HasAmount() {
		amount, c, err := FromMoney(field+".amount", po.GetAmount())
		if err != nil {
			return in, err
		}
		in.Amount, in.Currency, cur = core.Some(amount), core.Some(c), c
	}
	if po.HasName() {
		in.Name = core.Some(po.GetName())
	}
	if po.HasInvoiceDisplayName() {
		in.InvoiceDisplayName = core.Some(new(po.GetInvoiceDisplayName()))
	}
	if po.HasDescription() {
		in.Description = core.Some(new(po.GetDescription()))
	}
	if po.HasTrialPeriodDays() {
		in.TrialPeriodDays = core.Some(int(po.GetTrialPeriodDays()))
	}
	var err error
	if in.Taxes, err = CatalogRefs(field+".tax_ids", po.GetTaxIds()); err != nil {
		return in, err
	}
	for i, c := range po.GetCharges() {
		f := fmt.Sprintf("%s.charges[%d]", field, i)
		if !c.HasId() {
			return in, InvalidArgument("invalid plan overrides", []FieldViolation{{Field: f + ".id", Reason: core.CodeRequired}})
		}
		ci, err := chargeInput(f, c)
		if err != nil {
			return in, err
		}
		if in.Charges == nil {
			in.Charges = map[core.ChargeID]plans.ChargeInput{}
		}
		in.Charges[ci.ID] = ci
	}
	for i, fc := range po.GetFixedCharges() {
		f := fmt.Sprintf("%s.fixed_charges[%d]", field, i)
		if !fc.HasId() {
			return in, InvalidArgument("invalid plan overrides", []FieldViolation{{Field: f + ".id", Reason: core.CodeRequired}})
		}
		fi, err := fixedChargeInput(f, fc)
		if err != nil {
			return in, err
		}
		if in.FixedCharges == nil {
			in.FixedCharges = map[core.FixedChargeID]plans.FixedChargeInput{}
		}
		in.FixedCharges[fi.ID] = fi
	}
	if po.HasMinimumCommitment() {
		m := po.GetMinimumCommitment()
		var ci plans.CommitmentInput
		if m.HasAmount() {
			amount, err := minorAmount(field+".minimum_commitment.amount", m.GetAmount(), cur)
			if err != nil {
				return in, err
			}
			ci.Amount = core.Some(amount)
		}
		if m.HasInvoiceDisplayName() {
			ci.InvoiceDisplayName = core.Some(new(m.GetInvoiceDisplayName()))
		}
		if len(m.GetTaxIds()) > 0 {
			taxes, err := CatalogRefs(field+".minimum_commitment.tax_ids", m.GetTaxIds())
			if err != nil {
				return in, err
			}
			ci.Taxes = core.Some(taxes)
		}
		in.MinimumCommitment = &ci
	}
	if in.UsageThresholds, err = subThresholds(field+".usage_thresholds", po.GetUsageThresholds(), cur); err != nil {
		return in, err
	}
	return in, nil
}

// subThresholds converts the usage thresholds at field, in currency cur;
// a threshold without amount is passed on as missing.
func subThresholds(field string, ts []*catalogv1.UsageThreshold, cur money.Currency) ([]plans.ThresholdInput, error) {
	var out []plans.ThresholdInput
	for i, t := range ts {
		ti := plans.ThresholdInput{
			Recurring:            t.GetRecurring(),
			ThresholdDisplayName: Opt(t.HasThresholdDisplayName(), t.GetThresholdDisplayName()),
		}
		if t.GetAmount() != "" {
			amount, err := minorAmount(fmt.Sprintf("%s[%d].amount", field, i), t.GetAmount(), cur)
			if err != nil {
				return nil, err
			}
			ti.Amount = &amount
		}
		out = append(out, ti)
	}
	return out, nil
}

// subUsageThresholdProto converts a usage threshold in currency cur.
func subUsageThresholdProto(t *catalog.UsageThreshold, cur money.Currency) *catalogv1.UsageThreshold {
	return catalogv1.UsageThreshold_builder{
		Id:                   new(t.ID.String()),
		Amount:               new(cur.FromMinor(t.Amount).StringFixed(cur.Exponent())),
		Recurring:            new(t.Recurring),
		ThresholdDisplayName: t.ThresholdDisplayName,
		CreatedAt:            Timestamp(t.CreatedAt),
		UpdatedAt:            Timestamp(t.UpdatedAt),
	}.Build()
}
