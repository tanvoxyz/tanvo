package quotes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/billing/deduct"
	"github.com/tanvoxyz/tanvo/libs/billing/fees"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/billing/plans"
	"github.com/tanvoxyz/tanvo/libs/billing/quotes/validators"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/billing/subscriptions"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// calendarDate matches a bare YYYY-MM-DD date (lago
// Orders::SubscriptionCreation::ExecuteService::CALENDAR_DATE).
var calendarDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// execution is one attempt at creating the records of an order in the
// transaction of r.
type execution struct {
	s       *Service
	r       port.Repos
	order   *quote.Order
	quote   *quote.Quote
	version *quote.Version
	items   object
	loc     *time.Location
	// billingEntity is the quoted billing entity the subscriptions and
	// wallets are bound to; zero inherits the customer's.
	billingEntity core.BillingEntityID
}

func (x *execution) org() core.OrganizationID { return x.order.OrganizationID }

// oneOff issues the one-off invoice of the add-ons (lago
// Orders::OneOff::ExecuteService, app/services/orders/one_off/execute_service.rb:8-48).
// A discarded add-on is still billed by ID.
func (x *execution) oneOff(ctx context.Context, rec *ExecutionRecord) error {
	var lines []fees.OneOff
	for _, a := range objects(x.items["addOns"]) {
		var l fees.OneOff
		if s := str(a["id"]); s != nil {
			if id, err := core.ParseID[core.AddOnID](*s); err == nil {
				l.AddOnID = id
			}
		}
		if s := str(dig(a, "payload", "code")); s != nil {
			l.AddOnCode = *s
		}
		var err error
		if l.Units, err = decimal("units", effective(a, "units")); err != nil {
			return err
		}
		if l.UnitAmount, err = integer("unit_amount_cents", effective(a, "unitAmountCents")); err != nil {
			return err
		}
		l.InvoiceDisplayName = str(effective(a, "invoiceDisplayName"))
		l.Description = str(effective(a, "description"))
		if l.From, err = instant("from_datetime", effective(a, "fromDatetime")); err != nil {
			return err
		}
		if l.To, err = instant("to_datetime", effective(a, "toDatetime")); err != nil {
			return err
		}
		if l.AddOnID.IsZero() && l.AddOnCode == "" {
			return core.NotFound("add_on")
		}
		lines = append(lines, l)
	}
	inv, err := x.s.svc.Invoices.CreateOneOff(ctx, invoicing.OneOffInput{
		OrganizationID:  x.org(),
		CustomerID:      x.order.CustomerID,
		BillingEntityID: x.version.BillingEntityID,
		Currency:        x.version.Currency,
		Fees:            lines,
	})
	if err != nil {
		if errors.Is(err, core.ErrNotFound) && !isNotFoundError(err) {
			return core.NotFound("add_on")
		}
		return err
	}
	rec.InvoiceID = &inv.ID
	return nil
}

func isNotFoundError(err error) bool {
	var nf *core.NotFoundError
	return errors.As(err, &nf)
}

// subscriptionCreation creates the subscriptions, then applies the coupons
// and creates the wallets, which reuse the currency the subscriptions
// settle (lago Orders::SubscriptionCreation::ExecuteService#create_records,
// app/services/orders/subscription_creation/execute_service.rb:13-25). The
// customer's coupons lock is taken by the coupon application after the
// billing_schedule lock of the subscriptions, in the kernel lock order.
func (x *execution) subscriptionCreation(ctx context.Context, rec *ExecutionRecord) error {
	x.billingEntity = x.version.BillingEntityID
	for _, item := range objects(x.items["plans"]) {
		id, err := x.createSubscription(ctx, item)
		if err != nil {
			return err
		}
		rec.SubscriptionIDs = append(rec.SubscriptionIDs, id)
	}
	return x.couponsAndWallets(ctx, rec)
}

func (x *execution) couponsAndWallets(ctx context.Context, rec *ExecutionRecord) error {
	for _, item := range objects(x.items["coupons"]) {
		id, err := x.applyCoupon(ctx, item)
		if err != nil {
			return err
		}
		rec.AppliedCouponIDs = append(rec.AppliedCouponIDs, id)
	}
	for _, item := range objects(x.items["walletCredits"]) {
		id, err := x.createWallet(ctx, item)
		if err != nil {
			return err
		}
		rec.WalletIDs = append(rec.WalletIDs, id)
	}
	return nil
}

// createSubscription subscribes the customer to a quoted plan
// (execute_service.rb:38-70). An external ID of a live subscription of the
// customer changes its plan instead.
func (x *execution) createSubscription(ctx context.Context, item object) (core.SubscriptionID, error) {
	plan, err := x.plan(ctx, item)
	if err != nil {
		return core.SubscriptionID{}, err
	}
	payload, _ := item["payload"].(object)
	in := subscriptions.CreateInput{
		OrganizationID: x.org(),
		Customer:       subscriptions.CustomerRef{ID: x.order.CustomerID},
		Plan:           refs.ByID(plan.ID),
		Name:           str(payload["subscriptionName"]),
	}
	// Both quoted ids are stable, so a retry reuses the external id.
	switch {
	case presence(payload["subscriptionExternalId"]) != nil:
		in.ExternalID = *presence(payload["subscriptionExternalId"])
	case presence(item["localId"]) != nil:
		in.ExternalID = *presence(item["localId"])
	default:
		in.ExternalID = uuid.NewString()
	}
	if s := str(payload["billingTime"]); s != nil {
		if in.BillingTime, err = period.ParseBillingTime(*s); err != nil {
			return core.SubscriptionID{}, fieldError("billing_time", core.CodeInvalid)
		}
	}
	if in.SubscriptionAt, err = x.datetime(ctx, "subscription_at", payload["startDate"]); err != nil {
		return core.SubscriptionID{}, err
	}
	if in.EndingAt, err = x.datetime(ctx, "ending_at", payload["endDate"]); err != nil {
		return core.SubscriptionID{}, err
	}
	if in.PaymentMethod, err = paymentMethod(payload); err != nil {
		return core.SubscriptionID{}, err
	}
	if !x.billingEntity.IsZero() {
		in.BillingEntity = refs.ByID(x.billingEntity)
	}
	if in.PlanOverrides, err = x.planOverrides(ctx, item, plan); err != nil {
		return core.SubscriptionID{}, err
	}
	if in.UsageThresholds, err = thresholds(item); err != nil {
		return core.SubscriptionID{}, err
	}
	v, err := x.s.svc.Subscriptions.Create(ctx, in)
	if err != nil {
		return core.SubscriptionID{}, err
	}
	return v.Subscription.ID, nil
}

// subscriptionAmendment moves the target subscription onto the quoted
// plan (lago Orders::SubscriptionAmendment::ExecuteService,
// app/services/orders/subscription_amendment/execute_service.rb:11-127):
// it re-checks the target (one plan, the customer's, active), builds the
// override plan of the quoted catalog plan, which always carries a fresh
// ID, and changes the target's plan: an upgrade now, a downgrade at the
// end of the period. The usage thresholds and the payment method go on
// the subscription on the new plan; coupons and wallets follow as for a
// creation, the wallets bound to the target's billing entity.
func (x *execution) subscriptionAmendment(ctx context.Context, rec *ExecutionRecord) error {
	items := objects(x.items["plans"])
	if len(items) != 1 {
		return fieldError("plans", CodeSinglePlanExpected)
	}
	item := items[0]
	var target *subscription.Subscription
	if !x.quote.SubscriptionID.IsZero() {
		t, err := x.r.Subscriptions.Get(ctx, x.org(), x.quote.SubscriptionID)
		switch {
		case err == nil:
			target = t
		case !errors.Is(err, core.ErrNotFound):
			return fmt.Errorf("load subscription %s: %w", x.quote.SubscriptionID, err)
		}
	}
	if target == nil || target.CustomerID != x.order.CustomerID {
		return core.NotFound("subscription")
	}
	if target.Status != subscription.StatusActive {
		return fieldError("subscription", CodeSubscriptionNotActive)
	}
	x.billingEntity = target.BillingEntityID

	catalogPlan, err := x.plan(ctx, item)
	if err != nil {
		return err
	}
	po, err := x.planOverrides(ctx, item, catalogPlan)
	if err != nil {
		return err
	}
	if po == nil {
		po = &plans.OverrideInput{}
	}
	quoted, err := x.s.svc.Plans.Override(ctx, x.r, catalogPlan, *po, nil)
	if err != nil {
		return err
	}
	payload, _ := item["payload"].(object)
	name := presence(payload["subscriptionName"])
	if name == nil {
		name = target.Name
	}
	endingAt, err := x.datetime(ctx, "ending_at", payload["endDate"])
	if err != nil {
		return err
	}
	if endingAt == nil {
		endingAt = target.EndingAt
	}
	pm, err := paymentMethod(payload)
	if err != nil {
		return err
	}
	th, err := thresholds(item)
	if err != nil {
		return err
	}
	res, err := x.s.svc.Lifecycle.ChangePlan(ctx, lifecycle.ChangePlanInput{
		OrganizationID: x.org(),
		SubscriptionID: target.ID,
		PlanID:         quoted.ID,
		Name:           name,
		EndingAt:       endingAt,
	})
	if err != nil {
		return err
	}
	if len(th) > 0 || pm != nil {
		up := subscriptions.UpdateInput{
			OrganizationID: x.org(),
			Subscription:   subscriptions.SubscriptionRef{ID: res.ID},
			PaymentMethod:  pm,
		}
		if len(th) > 0 {
			up.UsageThresholds = core.Some(th)
		}
		if _, err := x.s.svc.Subscriptions.Update(ctx, up); err != nil {
			return err
		}
	}
	rec.SubscriptionIDs = append(rec.SubscriptionIDs, res.ID)
	if err := x.couponsAndWallets(ctx, rec); err != nil {
		return err
	}
	t, err := x.r.Subscriptions.Get(ctx, x.org(), target.ID)
	if err != nil {
		return fmt.Errorf("reload subscription %s: %w", target.ID, err)
	}
	if t.Status == subscription.StatusTerminated {
		rec.TerminatedSubscriptionIDs = append(rec.TerminatedSubscriptionIDs, t.ID)
	}
	return nil
}

// plan loads the quoted plan of item (execute_service.rb:329-334).
func (x *execution) plan(ctx context.Context, item object) (*catalog.Plan, error) {
	s := str(item["id"])
	if s == nil {
		return nil, core.NotFound("plan")
	}
	id, err := core.ParseID[core.PlanID](*s)
	if err != nil {
		return nil, core.NotFound("plan")
	}
	p, err := x.r.Plans.Get(ctx, x.org(), id)
	if err != nil {
		return nil, notFound(err, "plan")
	}
	return p, nil
}

// datetime reads a quoted date (execute_service.rb:82-96): a bare date is
// that day in the customer's time zone, anything else an ISO 8601 instant;
// field invalid_date otherwise.
func (x *execution) datetime(ctx context.Context, field string, v any) (*time.Time, error) {
	s := presence(v)
	if s == nil {
		return nil, nil
	}
	if calendarDate.MatchString(*s) {
		if d, ok := validators.ParseDate(*s); ok {
			if x.loc == nil {
				env, err := kernel.LoadCustomerEnv(ctx, x.r, x.org(), x.order.CustomerID, core.BillingEntityID{})
				if err != nil {
					return nil, err
				}
				x.loc = env.Location
			}
			t := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, x.loc).UTC()
			return &t, nil
		}
	}
	return instant(field, *s)
}

// instant reads an ISO 8601 instant; nil when v is blank.
func instant(field string, v any) (*time.Time, error) {
	s := presence(v)
	if s == nil {
		return nil, nil
	}
	t, ok := validators.ParseInstant(*s)
	if !ok {
		return nil, fieldError(field, CodeInvalidDate)
	}
	return &t, nil
}

// paymentMethod is the provider payment method of the quoted
// paymentMethodId (execute_service.rb:98-104).
func paymentMethod(payload object) (*subscriptions.PaymentMethod, error) {
	s := str(payload["paymentMethodId"])
	if s == nil {
		return nil, nil
	}
	id, err := core.ParseID[core.PaymentMethodID](*s)
	if err != nil {
		return nil, core.NotFound("payment_method")
	}
	t := core.PaymentMethodTypeProvider
	return &subscriptions.PaymentMethod{Type: &t, ID: &id}, nil
}

// planOverrides maps the negotiated values of item onto plan overrides
// (execute_service.rb:106-156); nil when it negotiates nothing.
func (x *execution) planOverrides(ctx context.Context, item object, plan *catalog.Plan) (*plans.OverrideInput, error) {
	ov, _ := item["overrides"].(object)
	if len(ov) == 0 {
		return nil, nil
	}
	var in plans.OverrideInput
	set := false
	if n, err := integer("amount_cents", ov["amountCents"]); err != nil {
		return nil, err
	} else if n != nil {
		in.Amount, set = core.Some(*n), true
	}
	if s := str(ov["amountCurrency"]); s != nil {
		c, err := money.LookupCurrency(*s)
		if err != nil {
			return nil, fieldError("amount_currency", core.CodeInvalid)
		}
		in.Currency, set = core.Some(c), true
	}
	if s := str(ov["invoiceDisplayName"]); s != nil {
		in.InvoiceDisplayName, set = core.Some(s), true
	}
	if s := str(ov["name"]); s != nil {
		in.Name, set = core.Some(*s), true
	}
	if s := str(ov["description"]); s != nil {
		in.Description, set = core.Some(s), true
	}
	if n, err := integer("trial_period", ov["trialPeriod"]); err != nil {
		return nil, err
	} else if n != nil {
		in.TrialPeriodDays, set = core.Some(int(*n)), true
	}
	mc, err := x.minimumCommitment(ctx, ov, plan)
	if err != nil {
		return nil, err
	}
	if mc != nil {
		in.MinimumCommitment, set = mc, true
	}
	cur := plan.Currency
	in.Currency.Apply(&cur)
	for _, co := range objects(ov["charges"]) {
		id, err := x.chargeID(ctx, item, plan, co["billableMetricCode"])
		if err != nil {
			return nil, err
		}
		var c plans.ChargeInput
		if c.Properties, err = properties(co["properties"]); err != nil {
			return nil, err
		}
		if n, err := integer("min_amount_cents", co["minAmountCents"]); err != nil {
			return nil, err
		} else if n != nil {
			c.MinAmount = core.Some(cur.MinorToUnits(money.NewFromInt(*n)))
		}
		if s := str(co["invoiceDisplayName"]); s != nil {
			c.InvoiceDisplayName = core.Some(s)
		}
		if in.Charges == nil {
			in.Charges = map[core.ChargeID]plans.ChargeInput{}
		}
		in.Charges[id], set = c, true
	}
	for _, fo := range objects(ov["fixedCharges"]) {
		id, err := x.fixedChargeID(ctx, item, plan, fo["addOnCode"])
		if err != nil {
			return nil, err
		}
		var f plans.FixedChargeInput
		if d, err := decimal("units", fo["units"]); err != nil {
			return nil, err
		} else if d != nil {
			f.Units = core.Some(*d)
		}
		if f.Properties, err = properties(fo["properties"]); err != nil {
			return nil, err
		}
		if s := str(fo["invoiceDisplayName"]); s != nil {
			f.InvoiceDisplayName = core.Some(s)
		}
		if in.FixedCharges == nil {
			in.FixedCharges = map[core.FixedChargeID]plans.FixedChargeInput{}
		}
		in.FixedCharges[id], set = f, true
	}
	if !set {
		return nil, nil
	}
	return &in, nil
}

// minimumCommitment forwards the plan's own commitment amount when the
// override only renames it (execute_service.rb:122-133).
func (x *execution) minimumCommitment(ctx context.Context, ov object, plan *catalog.Plan) (*plans.CommitmentInput, error) {
	c, _ := ov["minimumCommitment"].(object)
	if len(c) == 0 {
		return nil, nil
	}
	amount, err := integer("minimum_commitment.amount_cents", c["amountCents"])
	if err != nil {
		return nil, err
	}
	if amount == nil {
		pc, err := x.r.Commitments.GetByPlan(ctx, x.org(), plan.ID, catalog.CommitmentTypeMinimumCommitment)
		switch {
		case err == nil:
			amount = &pc.Amount
		case !errors.Is(err, core.ErrNotFound):
			return nil, fmt.Errorf("load commitment of plan %s: %w", plan.ID, err)
		}
	}
	idn := str(c["invoiceDisplayName"])
	if amount == nil && idn == nil {
		return nil, nil
	}
	var out plans.CommitmentInput
	if amount != nil {
		out.Amount = core.Some(*amount)
	}
	if idn != nil {
		out.InvoiceDisplayName = core.Some(idn)
	}
	return &out, nil
}

// properties underscores negotiated charge properties
// (Utils::ChargeProperties.underscore_keys); unset when absent.
func properties(v any) (core.Optional[catalog.Properties], error) {
	if v == nil {
		return core.Optional[catalog.Properties]{}, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return core.Optional[catalog.Properties]{}, fmt.Errorf("encode properties: %w", err)
	}
	u, err := validators.UnderscoreProperties(raw)
	if err != nil {
		return core.Optional[catalog.Properties]{}, fieldError("properties", core.CodeInvalid)
	}
	p, err := catalog.NewProperties(u)
	if err != nil {
		return core.Optional[catalog.Properties]{}, fieldError("properties", core.CodeInvalid)
	}
	return core.Some(p), nil
}

// chargeID resolves the plan charge of a negotiated metric through the
// snapshot: not found charge when it left the plan, charge_model
// charge_model_changed when its model changed since the approval
// (execute_service.rb:158-204).
func (x *execution) chargeID(ctx context.Context, item object, plan *catalog.Plan, code any) (core.ChargeID, error) {
	snap := find(objects(dig(item, "payload", "charges")), func(c object) bool {
		return code != nil && dig(c, "billableMetric", "code") == code
	})
	charges, err := x.r.Charges.ListByPlan(ctx, x.org(), plan.ID)
	if err != nil {
		return core.ChargeID{}, fmt.Errorf("load charges of plan %s: %w", plan.ID, err)
	}
	var id *string
	if snap != nil {
		id = str(snap["id"])
	}
	i := slices.IndexFunc(charges, func(c *catalog.Charge) bool { return id != nil && c.ID.String() == *id })
	if i < 0 {
		return core.ChargeID{}, core.NotFound("charge")
	}
	if m := str(snap["chargeModel"]); m != nil && *m != string(charges[i].ChargeModel) {
		return core.ChargeID{}, fieldError("charge_model", CodeChargeModelChanged)
	}
	return charges[i].ID, nil
}

// fixedChargeID is chargeID for the fixed charges, by add-on code.
func (x *execution) fixedChargeID(ctx context.Context, item object, plan *catalog.Plan, code any) (core.FixedChargeID, error) {
	snap := find(objects(dig(item, "payload", "fixedCharges")), func(c object) bool {
		return code != nil && dig(c, "addOn", "code") == code
	})
	fcs, err := x.r.FixedCharges.ListByPlan(ctx, x.org(), plan.ID)
	if err != nil {
		return core.FixedChargeID{}, fmt.Errorf("load fixed charges of plan %s: %w", plan.ID, err)
	}
	var id *string
	if snap != nil {
		id = str(snap["id"])
	}
	i := slices.IndexFunc(fcs, func(c *catalog.FixedCharge) bool { return id != nil && c.ID.String() == *id })
	if i < 0 {
		return core.FixedChargeID{}, core.NotFound("fixed_charge")
	}
	if m := str(snap["chargeModel"]); m != nil && *m != string(fcs[i].ChargeModel) {
		return core.FixedChargeID{}, fieldError("fixed_charge_model", CodeFixedChargeModelChanged)
	}
	return fcs[i].ID, nil
}

func find(items []object, pred func(object) bool) object {
	for _, it := range items {
		if pred(it) {
			return it
		}
	}
	return nil
}

// thresholds are the negotiated usage thresholds of item, which go on the
// subscription (execute_service.rb:206-216).
func thresholds(item object) ([]plans.ThresholdInput, error) {
	var out []plans.ThresholdInput
	for _, t := range objects(dig(item, "overrides", "usageThresholds")) {
		var in plans.ThresholdInput
		var err error
		if in.Amount, err = integer("usage_thresholds.amount_cents", t["amountCents"]); err != nil {
			return nil, err
		}
		b, err := boolean("usage_thresholds.recurring", t["recurring"])
		if err != nil {
			return nil, err
		}
		in.Recurring = b != nil && *b
		in.ThresholdDisplayName = str(t["thresholdDisplayName"])
		out = append(out, in)
	}
	return out, nil
}

// applyCoupon applies a quoted coupon with its effective terms; only the
// override states a currency (execute_service.rb:218-241).
func (x *execution) applyCoupon(ctx context.Context, item object) (core.AppliedCouponID, error) {
	var c *coupon.Coupon
	if s := str(item["id"]); s != nil {
		if id, err := core.ParseID[core.CouponID](*s); err == nil {
			got, err := x.r.Coupons.Get(ctx, x.org(), id)
			switch {
			case err == nil:
				c = got
			case !errors.Is(err, core.ErrNotFound):
				return core.AppliedCouponID{}, fmt.Errorf("load coupon %s: %w", id, err)
			}
		}
	}
	if c == nil {
		return core.AppliedCouponID{}, core.NotFound("coupon")
	}
	var o coupon.Overrides
	var err error
	if o.Amount, err = integer("amount_cents", effective(item, "amountCents")); err != nil {
		return core.AppliedCouponID{}, err
	}
	if s := str(dig(item, "overrides", "amountCurrency")); s != nil {
		if o.Currency, err = money.LookupCurrency(*s); err != nil {
			return core.AppliedCouponID{}, fieldError("amount_currency", core.CodeInvalid)
		}
	}
	if o.PercentageRate, err = decimal("percentage_rate", effective(item, "percentageRate")); err != nil {
		return core.AppliedCouponID{}, err
	}
	if s := str(effective(item, "frequency")); s != nil {
		if o.Frequency, err = coupon.ParseFrequency(*s); err != nil {
			return core.AppliedCouponID{}, fieldError("frequency", core.CodeInvalid)
		}
	}
	if n, err := integer("frequency_duration", effective(item, "frequencyDuration")); err != nil {
		return core.AppliedCouponID{}, err
	} else if n != nil {
		d := int(*n)
		o.FrequencyDuration = &d
	}
	a, err := x.s.svc.Coupons.ApplyCoupon(ctx, deduct.ApplyCouponInput{
		OrganizationID: x.org(),
		CouponID:       c.ID,
		CustomerID:     x.order.CustomerID,
		Overrides:      o,
	})
	if err != nil {
		return core.AppliedCouponID{}, err
	}
	return a.ID, nil
}

// createWallet creates a quoted wallet (execute_service.rb:243-312): the
// payload currency or the deal's, bound to the quoted billing entity.
func (x *execution) createWallet(ctx context.Context, item object) (core.WalletID, error) {
	payload, _ := item["payload"].(object)
	in := wallets.CreateInput{
		CustomerID:          x.order.CustomerID,
		Currency:            x.version.Currency,
		BillingEntityID:     x.billingEntity,
		Name:                str(payload["name"]),
		PurchaseOrderNumber: str(payload["purchaseOrderNumber"]),
	}
	var err error
	if s := str(payload["currency"]); s != nil {
		if in.Currency, err = money.LookupCurrency(*s); err != nil {
			return core.WalletID{}, fieldError("currency", core.CodeInvalid)
		}
	}
	if d, err := decimal("rate_amount", payload["rateAmount"]); err != nil {
		return core.WalletID{}, err
	} else if d != nil {
		in.RateAmount = *d
	}
	if in.PaidCredits, err = decimal("paid_credits", payload["paidCredits"]); err != nil {
		return core.WalletID{}, err
	}
	if in.GrantedCredits, err = decimal("granted_credits", payload["grantedCredits"]); err != nil {
		return core.WalletID{}, err
	}
	if in.ExpirationAt, err = instant("expiration_at", payload["expirationAt"]); err != nil {
		return core.WalletID{}, err
	}
	if b, err := boolean("invoice_requires_successful_payment", payload["invoiceRequiresSuccessfulPayment"]); err != nil {
		return core.WalletID{}, err
	} else if b != nil {
		in.InvoiceRequiresSuccessfulPayment = *b
	}
	if err := x.appliesTo(ctx, payload, &in); err != nil {
		return core.WalletID{}, err
	}
	for _, rule := range objects(payload["recurringTransactionRules"]) {
		ri, err := ruleInput(rule)
		if err != nil {
			return core.WalletID{}, err
		}
		in.RecurringTransactionRules = append(in.RecurringTransactionRules, ri)
	}
	w, err := x.s.svc.Wallets.Create(ctx, x.org(), in)
	if err != nil {
		return core.WalletID{}, err
	}
	return w.ID, nil
}

// appliesTo resolves the wallet limitation: fee types, and metric codes
// that must all still exist (not found billable_metric otherwise,
// execute_service.rb:269-291).
func (x *execution) appliesTo(ctx context.Context, payload object, in *wallets.CreateInput) error {
	at, _ := payload["appliesTo"].(object)
	if len(at) == 0 {
		return nil
	}
	for _, v := range array(at["feeTypes"]) {
		s, _ := v.(string)
		t, err := core.ParseFeeType(s)
		if err != nil {
			return fieldError("applies_to.fee_types", core.CodeInvalid)
		}
		in.AllowedFeeTypes = append(in.AllowedFeeTypes, t)
	}
	var codes []string
	for _, v := range array(at["billableMetricCodes"]) {
		if s, ok := v.(string); ok && !slices.Contains(codes, s) {
			codes = append(codes, s)
		}
	}
	if len(codes) == 0 {
		return nil
	}
	ms, err := x.r.Metrics.ListByCodes(ctx, x.org(), codes)
	if err != nil {
		return fmt.Errorf("load billable metrics: %w", err)
	}
	if len(ms) != len(codes) {
		return core.NotFound("billable_metric")
	}
	for _, m := range ms {
		in.BillableMetricIDs = append(in.BillableMetricIDs, m.ID)
	}
	return nil
}

// ruleInput maps a quoted recurring transaction rule
// (execute_service.rb:293-312).
func ruleInput(rule object) (wallets.RuleInput, error) {
	var ri wallets.RuleInput
	var err error
	if s := str(rule["trigger"]); s != nil {
		if ri.Trigger, err = wallet.ParseRecurringTrigger(*s); err != nil {
			return ri, fieldError("recurring_transaction_rules.trigger", core.CodeInvalid)
		}
	}
	if s := str(rule["interval"]); s != nil {
		if ri.Interval, err = period.ParseInterval(*s); err != nil {
			return ri, fieldError("recurring_transaction_rules.interval", core.CodeInvalid)
		}
	}
	if s := str(rule["method"]); s != nil {
		if ri.Method, err = wallet.ParseRecurringMethod(*s); err != nil {
			return ri, fieldError("recurring_transaction_rules.method", core.CodeInvalid)
		}
	}
	if ri.ThresholdCredits, err = decimal("recurring_transaction_rules.threshold_credits", rule["thresholdCredits"]); err != nil {
		return ri, err
	}
	if ri.TargetOngoingBalance, err = decimal("recurring_transaction_rules.target_ongoing_balance", rule["targetOngoingBalance"]); err != nil {
		return ri, err
	}
	if ri.GrantsTargetTopUp, err = boolean("recurring_transaction_rules.grants_target_top_up", rule["grantsTargetTopUp"]); err != nil {
		return ri, err
	}
	if ri.PaidCredits, err = decimal("recurring_transaction_rules.paid_credits", rule["paidCredits"]); err != nil {
		return ri, err
	}
	if ri.GrantedCredits, err = decimal("recurring_transaction_rules.granted_credits", rule["grantedCredits"]); err != nil {
		return ri, err
	}
	if ri.StartedAt, err = instant("recurring_transaction_rules.started_at", rule["startedAt"]); err != nil {
		return ri, err
	}
	if ri.ExpirationAt, err = instant("recurring_transaction_rules.expiration_at", rule["expirationAt"]); err != nil {
		return ri, err
	}
	ri.TransactionName = str(rule["transactionName"])
	if ri.InvoiceRequiresSuccessfulPayment, err = boolean("recurring_transaction_rules.invoice_requires_successful_payment", rule["invoiceRequiresSuccessfulPayment"]); err != nil {
		return ri, err
	}
	return ri, nil
}
