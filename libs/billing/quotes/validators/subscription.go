package validators

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/pricing"
)

// subscriptionCreation is lago SubscriptionCreation::BusinessValidator.
func (b *business) subscriptionCreation() error {
	b.validateCurrency()
	if err := b.validateBillingEntity(); err != nil {
		return err
	}
	if err := b.validatePlans(); err != nil {
		return err
	}
	if err := b.validateCoupons(); err != nil {
		return err
	}
	return b.validateWalletCredits()
}

// subscriptionAmendment is lago SubscriptionAmendment::BusinessValidator:
// one plan, a running target subscription, then the subscription_creation
// checks with the billing entity refused and only the ending date checked.
func (b *business) subscriptionAmendment() error {
	if len(b.plans()) > 1 {
		b.errs.add("billing_items.plans", "single_plan_expected")
	}
	if err := b.validateTargetSubscription(); err != nil {
		return err
	}
	return b.subscriptionCreation()
}

// validateTargetSubscription requires the quote's target subscription to
// be active.
func (b *business) validateTargetSubscription() error {
	id := b.q.SubscriptionID
	if id.IsZero() {
		b.errs.add("subscription_id", core.CodeRequired)
		return nil
	}
	s, err := b.r.Subscriptions.Get(b.ctx, b.org(), id)
	missing, err := notFound(err)
	if err != nil {
		return fmt.Errorf("get subscription %s: %w", id, err)
	}
	if missing {
		b.errs.add("subscription_id", core.CodeRequired)
		return nil
	}
	if s.Status != subscription.StatusActive {
		b.errs.add("subscription_id", "subscription_not_active")
	}
	return nil
}

func (b *business) plans() []*value         { return b.items.get("plans").items() }
func (b *business) coupons() []*value       { return b.items.get("coupons").items() }
func (b *business) walletCredits() []*value { return b.items.get("walletCredits").items() }

func planField(i int, suffix string) string {
	return "billing_items.plans." + strconv.Itoa(i) + "." + suffix
}

func couponField(i int, suffix string) string {
	return "billing_items.coupons." + strconv.Itoa(i) + "." + suffix
}

func walletCreditField(i int, suffix string) string {
	return "billing_items.walletCredits." + strconv.Itoa(i) + "." + suffix
}

func ruleField(wallet, rule int, suffix string) string {
	return walletCreditField(wallet, "payload.recurringTransactionRules."+strconv.Itoa(rule)+"."+suffix)
}

// presence is Ruby's x.presence on a quoted string: the string unless it
// is absent, null or blank.
func presence(v *value) (string, bool) {
	s, ok := v.str()
	if !ok || !rubyPresent(s) {
		return "", false
	}
	return s, true
}

// validatePlans checks each quoted plan (lago validate_plans).
func (b *business) validatePlans() error {
	for i, item := range b.plans() {
		plan, err := b.quotedPlan(item)
		if err != nil {
			return err
		}
		if plan == nil {
			b.errs.add(planField(i, "id"), "plan_not_found")
			continue
		}
		b.validatePlanCurrency(plan, item, i)
		if err := b.validateMinimumCommitment(plan, item, i); err != nil {
			return err
		}
		b.validatePlanDates(item, i)
		if err := b.validatePlanPaymentMethod(item, i); err != nil {
			return err
		}
		if err := b.validateChargeOverrides(item, plan, i); err != nil {
			return err
		}
		if err := b.validateFixedChargeOverrides(item, plan, i); err != nil {
			return err
		}
	}
	return nil
}

// quotedPlan returns the quoted plan, deleted or not; nil when unknown.
func (b *business) quotedPlan(item *value) (*catalog.Plan, error) {
	id, ok := lookup(item.get("id"), core.ParseID[core.PlanID])
	if !ok {
		return nil, nil
	}
	p, err := b.r.Plans.Get(b.ctx, b.org(), id)
	if missing, err := notFound(err); missing || err != nil {
		if err != nil {
			return nil, fmt.Errorf("get plan %s: %w", id, err)
		}
		return nil, nil
	}
	return p, nil
}

// validatePlanCurrency requires the plan, in its overridden currency if
// any, to be priced in the deal currency.
func (b *business) validatePlanCurrency(plan *catalog.Plan, item *value, i int) {
	if !currencyListed(b.v.Currency) {
		return
	}
	effective, ok := presence(item.get("overrides", "amountCurrency"))
	if !ok {
		effective = plan.Currency.Code()
	}
	if effective != b.v.Currency.Code() {
		b.errs.add(planField(i, "id"), core.CodeCurrencyMismatch)
	}
}

// validateMinimumCommitment requires an overridden minimum commitment to
// have an amount, its own or the plan's, at approval.
func (b *business) validateMinimumCommitment(plan *catalog.Plan, item *value, i int) error {
	if !b.approve() {
		return nil
	}
	mc := item.get("overrides", "minimumCommitment")
	if mc.isNull() || !mc.get("amountCents").isNull() {
		return nil
	}
	_, err := b.r.Commitments.GetByPlan(b.ctx, b.org(), plan.ID, catalog.CommitmentTypeMinimumCommitment)
	missing, err := notFound(err)
	if err != nil {
		return fmt.Errorf("get minimum commitment of plan %s: %w", plan.ID, err)
	}
	if missing {
		b.errs.add(planField(i, "overrides.minimumCommitment.amountCents"), core.CodeRequired)
	}
	return nil
}

// validatePlanDates checks the subscription dates: valid ISO 8601, the end
// in the future at approval and after the start. An amendment checks the
// ending date only.
func (b *business) validatePlanDates(item *value, i int) {
	start, end := item.get("payload", "startDate"), item.get("payload", "endDate")
	endField := planField(i, "payload.endDate")
	if b.amendment {
		if b.validatePlanDate(end, endField) {
			b.validateFutureEndDate(end, endField)
		}
		return
	}
	validStart := b.validatePlanDate(start, planField(i, "payload.startDate"))
	validEnd := b.validatePlanDate(end, endField)
	if !validStart || !validEnd {
		return
	}
	b.validateFutureEndDate(end, endField)
	s, sok := isoDate(start)
	e, eok := isoDate(end)
	if !sok || !eok || e.After(s) {
		return
	}
	b.errs.add(endField, "invalid_date_range")
}

// validatePlanDate is lago validate_plan_date: null or a valid ISO 8601
// date and time.
func (b *business) validatePlanDate(v *value, field string) bool {
	if v.isNull() {
		return true
	}
	if s, ok := v.str(); ok && validISO8601(s) {
		return true
	}
	b.errs.add(field, "invalid_date")
	return false
}

// validateFutureEndDate requires the ending date to be after today at
// approval.
func (b *business) validateFutureEndDate(v *value, field string) {
	if !b.approve() {
		return
	}
	end, ok := isoDate(v)
	if !ok {
		return
	}
	y, m, d := b.now.UTC().Date()
	if end.After(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) {
		return
	}
	b.errs.add(field, "invalid_date")
}

func isoDate(v *value) (time.Time, bool) {
	s, ok := v.str()
	if !ok {
		return time.Time{}, false
	}
	return parseISO8601Date(s)
}

// validatePlanPaymentMethod requires a quoted payment method to be one of
// the customer's.
func (b *business) validatePlanPaymentMethod(item *value, i int) error {
	pm := item.get("payload", "paymentMethodId")
	if pm.isNull() {
		return nil
	}
	methods, err := b.r.PaymentMethods.ListByCustomer(b.ctx, b.org(), b.q.CustomerID)
	if err != nil {
		return fmt.Errorf("list payment methods of customer %s: %w", b.q.CustomerID, err)
	}
	s, _ := pm.str()
	for _, m := range methods {
		if m.ID.String() == s {
			return nil
		}
	}
	b.errs.add(planField(i, "payload.paymentMethodId"), "payment_method_not_found")
	return nil
}

// snapshotMatches returns the entries of a catalog snapshot whose code at
// path equals code, with their index.
func snapshotMatches(snapshots []*value, code *value, path ...string) (matches []*value, index []int) {
	for i, s := range snapshots {
		if s.get(path...).equal(code) {
			matches = append(matches, s)
			index = append(index, i)
		}
	}
	return matches, index
}

// modelChanged is lago model_changed?: a quoted model other than the
// current one.
func modelChanged(quoted *value, current string) bool {
	if quoted.isNull() {
		return false
	}
	s, ok := quoted.str()
	return !ok || s != current
}

// validateChargeOverrides resolves each charge override through the
// snapshot of the plan's charges and checks it against the charge.
func (b *business) validateChargeOverrides(item *value, plan *catalog.Plan, i int) error {
	overrides := item.get("overrides", "charges").items()
	if len(overrides) == 0 {
		return nil
	}
	charges, err := b.r.Charges.ListByPlan(b.ctx, b.org(), plan.ID)
	if err != nil {
		return fmt.Errorf("list charges of plan %s: %w", plan.ID, err)
	}
	snapshots := item.get("payload", "charges").items()
	for ci, override := range overrides {
		field := planField(i, "overrides.charges."+strconv.Itoa(ci)+".billableMetricCode")
		matches, index := snapshotMatches(snapshots, override.get("billableMetricCode"), "billableMetric", "code")
		if len(matches) == 0 {
			b.errs.add(field, "charge_not_found")
			continue
		}
		if len(matches) > 1 {
			b.errs.add(field, "ambiguous_charge_override")
			continue
		}
		snapshot := matches[0]
		id, _ := snapshot.get("id").str()
		k := slices.IndexFunc(charges, func(c *catalog.Charge) bool { return c.ID.String() == id && id != "" })
		if k < 0 {
			b.errs.add(field, "charge_not_found")
			continue
		}
		charge := charges[k]
		model := string(charge.ChargeModel)
		if modelChanged(override.get("chargeModel"), model) {
			b.errs.add(planField(i, "overrides.charges."+strconv.Itoa(ci)+".chargeModel"), "cannot_override_charge_model")
		}
		if modelChanged(snapshot.get("chargeModel"), model) {
			b.errs.add(planField(i, "payload.charges."+strconv.Itoa(index[0])+".chargeModel"), "charge_model_changed")
		}
		if modelChanged(override.get("chargeModel"), model) || modelChanged(snapshot.get("chargeModel"), model) {
			continue
		}
		if err := b.validateChargeProperties(override, charge, planField(i, "overrides.charges."+strconv.Itoa(ci)+".properties")); err != nil {
			return err
		}
		if charge.PayInAdvance {
			if d, ok := override.get("minAmountCents").decimal(); ok && d.Truncate(0).IsPositive() {
				b.errs.add(planField(i, "overrides.charges."+strconv.Itoa(ci)+".minAmountCents"), "not_compatible_with_pay_in_advance")
			}
		}
	}
	return nil
}

// validateChargeProperties validates the overridden properties of a charge
// with its model.
func (b *business) validateChargeProperties(override *value, charge *catalog.Charge, field string) error {
	raw := override.get("properties")
	if raw.isNull() || raw.kind != kindObject {
		return nil
	}
	latest := false
	if charge.ChargeModel == catalog.ChargeModelPercentage || charge.ChargeModel == catalog.ChargeModelGraduatedPercentage {
		m, err := b.r.Metrics.GetWithDiscarded(b.ctx, b.org(), charge.BillableMetricID)
		missing, err := notFound(err)
		if err != nil {
			return fmt.Errorf("get billable metric %s: %w", charge.BillableMetricID, err)
		}
		latest = !missing && m.AggregationType == catalog.AggregationTypeLatest
	}
	b.addPropertyErrors(pricing.ChargeModel(charge.ChargeModel), underscoreKeys(raw), latest, field)
	return nil
}

func (b *business) addPropertyErrors(model pricing.ChargeModel, props map[string]any, latest bool, field string) {
	keys, codes, raised := propertyErrors(model, props, latest)
	if raised {
		b.errs.add(field, core.CodeInvalid)
		return
	}
	for _, k := range keys {
		for _, c := range codes[k] {
			b.errs.add(field+"."+k, c)
		}
	}
}

// validateFixedChargeOverrides resolves each fixed charge override through
// the snapshot of the plan's fixed charges and checks it.
func (b *business) validateFixedChargeOverrides(item *value, plan *catalog.Plan, i int) error {
	overrides := item.get("overrides", "fixedCharges").items()
	if len(overrides) == 0 {
		return nil
	}
	fixed, err := b.r.FixedCharges.ListByPlan(b.ctx, b.org(), plan.ID)
	if err != nil {
		return fmt.Errorf("list fixed charges of plan %s: %w", plan.ID, err)
	}
	snapshots := item.get("payload", "fixedCharges").items()
	for fi, override := range overrides {
		prefix := "overrides.fixedCharges." + strconv.Itoa(fi) + "."
		if units := override.get("units"); !units.isNull() && !validAmount(units) {
			b.errs.add(planField(i, prefix+"units"), core.CodeInvalid)
		}
		field := planField(i, prefix+"addOnCode")
		matches, index := snapshotMatches(snapshots, override.get("addOnCode"), "addOn", "code")
		if len(matches) == 0 {
			b.errs.add(field, "fixed_charge_not_found")
			continue
		}
		if len(matches) > 1 {
			b.errs.add(field, "ambiguous_fixed_charge_override")
			continue
		}
		snapshot := matches[0]
		id, _ := snapshot.get("id").str()
		k := slices.IndexFunc(fixed, func(f *catalog.FixedCharge) bool { return f.ID.String() == id && id != "" })
		if k < 0 {
			b.errs.add(field, "fixed_charge_not_found")
			continue
		}
		fc := fixed[k]
		if modelChanged(snapshot.get("chargeModel"), string(fc.ChargeModel)) {
			b.errs.add(planField(i, "payload.fixedCharges."+strconv.Itoa(index[0])+".chargeModel"), "fixed_charge_model_changed")
			continue
		}
		raw := override.get("properties")
		if raw.isNull() || raw.kind != kindObject {
			continue
		}
		propsField := planField(i, prefix+"properties")
		props := underscoreKeys(raw)
		if filterRaises(props) {
			b.errs.add(propsField, core.CodeInvalid)
			continue
		}
		model := pricing.ChargeModel(fc.ChargeModel)
		filtered := pricing.FilterProperties(pricing.FixedCharge, model, props)
		if len(filtered) == 0 {
			b.errs.add(propsField, core.CodeInvalid)
			continue
		}
		b.addPropertyErrors(model, filtered, false, propsField)
	}
	return nil
}

// validateCoupons checks each quoted coupon (lago validate_coupons).
func (b *business) validateCoupons() error {
	items := b.coupons()
	if len(items) == 0 {
		return nil
	}
	known := make([]*coupon.Coupon, len(items))
	for i, item := range items {
		id, ok := lookup(item.get("id"), core.ParseID[core.CouponID])
		if !ok {
			continue
		}
		c, err := b.r.Coupons.Get(b.ctx, b.org(), id)
		missing, err := notFound(err)
		if err != nil {
			return fmt.Errorf("get coupon %s: %w", id, err)
		}
		if !missing {
			known[i] = c
		}
	}
	planMetrics, err := b.planMetrics(known)
	if err != nil {
		return err
	}
	var earlier []*coupon.Coupon
	for i, item := range items {
		c := known[i]
		if c == nil {
			b.errs.add(couponField(i, "id"), "coupon_not_found")
			continue
		}
		b.validateCouponCurrency(c, item, i)
		b.validateCouponFrequency(c, item, i)
		b.validateCouponPreconditions(c, earlier, planMetrics, i)
		if b.approve() {
			b.validateCouponSnapshot(c, item, i)
		}
		earlier = append(earlier, c)
	}
	return nil
}

// planMetrics maps each plan a quoted coupon targets to the metrics its
// charges use.
func (b *business) planMetrics(coupons []*coupon.Coupon) (map[core.PlanID][]core.BillableMetricID, error) {
	out := map[core.PlanID][]core.BillableMetricID{}
	for _, c := range coupons {
		if c == nil {
			continue
		}
		for _, p := range c.LimitedPlanIDs {
			if _, done := out[p]; done {
				continue
			}
			metrics := []core.BillableMetricID{}
			// lago joins the charges to their plan, whose default scope
			// leaves deleted plans out.
			plan, err := b.r.Plans.Get(b.ctx, b.org(), p)
			if missing, err := notFound(err); err != nil {
				return nil, fmt.Errorf("get plan %s: %w", p, err)
			} else if missing || plan.Deleted() {
				out[p] = metrics
				continue
			}
			charges, err := b.r.Charges.ListByPlan(b.ctx, b.org(), p)
			if err != nil {
				return nil, fmt.Errorf("list charges of plan %s: %w", p, err)
			}
			for _, ch := range charges {
				metrics = append(metrics, ch.BillableMetricID)
			}
			out[p] = metrics
		}
	}
	return out, nil
}

// validateCouponCurrency requires a fixed amount coupon, in its overridden
// currency if any, to be in the deal currency.
func (b *business) validateCouponCurrency(c *coupon.Coupon, item *value, i int) {
	if c.Type != coupon.TypeFixedAmount || !currencyListed(b.v.Currency) {
		return
	}
	effective, ok := presence(item.get("overrides", "amountCurrency"))
	if !ok {
		effective = c.Currency.Code()
	}
	if effective != b.v.Currency.Code() {
		b.errs.add(couponField(i, "id"), core.CodeCurrencyMismatch)
	}
}

// quotedCouponValue is overrides[field] || payload[field].
func quotedCouponValue(item *value, field string) *value {
	return item.get("overrides", field).orElse(item.get("payload", field))
}

// validateCouponFrequency requires a duration for a recurring coupon at
// approval.
func (b *business) validateCouponFrequency(c *coupon.Coupon, item *value, i int) {
	if !b.approve() {
		return
	}
	frequency := string(c.Frequency)
	if q := quotedCouponValue(item, "frequency"); !q.isNull() {
		frequency, _ = q.str()
	}
	if frequency != string(coupon.FrequencyRecurring) {
		return
	}
	if !quotedCouponValue(item, "frequencyDuration").isNull() || c.FrequencyDuration != nil {
		return
	}
	section := "payload"
	if s, _ := item.get("overrides", "frequency").str(); s == string(coupon.FrequencyRecurring) {
		section = "overrides"
	}
	b.errs.add(couponField(i, section+".frequencyDuration"), core.CodeRequired)
}

// validateCouponPreconditions refuses a coupon quoted twice that is not
// reusable, and a limited coupon overlapping an earlier one.
func (b *business) validateCouponPreconditions(c *coupon.Coupon, earlier []*coupon.Coupon, planMetrics map[core.PlanID][]core.BillableMetricID, i int) {
	if !c.Reusable && slices.ContainsFunc(earlier, func(e *coupon.Coupon) bool { return e.ID == c.ID }) {
		b.errs.add(couponField(i, "id"), "coupon_is_not_reusable")
	}
	if !c.Limited() {
		return
	}
	if slices.ContainsFunc(earlier, func(e *coupon.Coupon) bool { return coupon.Overlaps(c, e, planMetrics) }) {
		b.errs.add(couponField(i, "id"), "plan_overlapping")
	}
}

// validateCouponSnapshot requires the quoted type to be the coupon's and
// its value to be quoted.
func (b *business) validateCouponSnapshot(c *coupon.Coupon, item *value, i int) {
	if t, ok := item.get("payload", "type").str(); !ok || t != string(c.Type) {
		b.errs.add(couponField(i, "payload.type"), "coupon_type_does_not_match")
		return
	}
	if c.Type == coupon.TypeFixedAmount && quotedCouponValue(item, "amountCents").isNull() {
		b.errs.add(couponField(i, "payload.amountCents"), core.CodeRequired)
	}
	if c.Type == coupon.TypePercentage && quotedCouponValue(item, "percentageRate").isNull() {
		b.errs.add(couponField(i, "payload.percentageRate"), core.CodeRequired)
	}
}

// validateWalletCredits checks each quoted wallet (lago
// validate_wallet_credits).
func (b *business) validateWalletCredits() error {
	for i, item := range b.walletCredits() {
		payload := item.get("payload")
		b.validateWalletAmounts(payload, i)
		b.validateWalletCurrency(payload, i)
		if err := b.validateWalletAppliesTo(payload, i); err != nil {
			return err
		}
		b.validateExpiration(payload.get("expirationAt"), walletCreditField(i, "payload.expirationAt"))
		b.validateRecurringRules(payload, i)
	}
	return nil
}

func (b *business) validateWalletAmounts(payload *value, i int) {
	for _, key := range []string{"paidCredits", "grantedCredits"} {
		if v := payload.get(key); !v.isNull() && !validAmount(v) {
			b.errs.add(walletCreditField(i, "payload."+key), core.CodeInvalid)
		}
	}
	if v := payload.get("rateAmount"); !v.isNull() && !validPositiveAmount(v) {
		b.errs.add(walletCreditField(i, "payload.rateAmount"), core.CodeInvalid)
	}
}

func (b *business) validateWalletCurrency(payload *value, i int) {
	c := payload.get("currency")
	if c.isNull() || !currencyListed(b.v.Currency) {
		return
	}
	if s, _ := c.str(); s != b.v.Currency.Code() {
		b.errs.add(walletCreditField(i, "payload.currency"), core.CodeCurrencyMismatch)
	}
}

// validateWalletAppliesTo requires every quoted metric code to be a metric
// of the organization.
func (b *business) validateWalletAppliesTo(payload *value, i int) error {
	for _, code := range payload.get("appliesTo", "billableMetricCodes").items() {
		s, _ := code.str()
		_, err := b.r.Metrics.GetByCode(b.ctx, b.org(), s)
		missing, err := notFound(err)
		if err != nil {
			return fmt.Errorf("get billable metric %q: %w", s, err)
		}
		if missing {
			b.errs.add(walletCreditField(i, "payload.appliesTo.billableMetricCodes"), "billable_metric_not_found")
			return nil
		}
	}
	return nil
}

// validateExpiration is lago Validators::ExpirationDateValidator at
// approval: blank, or a time in the future.
func (b *business) validateExpiration(v *value, field string) {
	if !b.approve() {
		return
	}
	s, _ := v.str()
	if v.isNull() || !rubyPresent(s) {
		return
	}
	if t, ok := parseTime(s); ok && t.After(b.now) {
		return
	}
	b.errs.add(field, "invalid_date")
}

func (b *business) validateRecurringRules(payload *value, wi int) {
	for ri, rule := range payload.get("recurringTransactionRules").items() {
		b.validateRuleTrigger(rule, wi, ri)
		b.validateRuleMethod(rule, wi, ri)
		if !rule.get("grantsTargetTopUp").isNull() && !ruleMethodIs(rule, "target") {
			b.errs.add(ruleField(wi, ri, "grantsTargetTopUp"), core.CodeInvalid)
		}
		for _, key := range []string{"paidCredits", "grantedCredits"} {
			if v := rule.get(key); !v.isNull() {
				if _, ok := validDecimal(v); !ok {
					b.errs.add(ruleField(wi, ri, key), core.CodeInvalid)
				}
			}
		}
		b.validateExpiration(rule.get("expirationAt"), ruleField(wi, ri, "expirationAt"))
	}
}

func ruleMethodIs(rule *value, method string) bool {
	s, ok := rule.get("method").str()
	return ok && s == method
}

func (b *business) validateRuleTrigger(rule *value, wi, ri int) {
	trigger, _ := rule.get("trigger").str()
	switch trigger {
	case "interval":
		if rule.get("interval").isNull() {
			b.errs.add(ruleField(wi, ri, "interval"), core.CodeRequired)
		}
	case "threshold":
		threshold := rule.get("thresholdCredits")
		if threshold.isNull() {
			b.errs.add(ruleField(wi, ri, "thresholdCredits"), core.CodeRequired)
		} else if _, ok := validDecimal(threshold); !ok {
			b.errs.add(ruleField(wi, ri, "thresholdCredits"), core.CodeInvalid)
		}
	}
}

func (b *business) validateRuleMethod(rule *value, wi, ri int) {
	if !ruleMethodIs(rule, "target") {
		return
	}
	field := ruleField(wi, ri, "targetOngoingBalance")
	target := rule.get("targetOngoingBalance")
	if target.isNull() {
		b.errs.add(field, core.CodeRequired)
		return
	}
	t, ok := validDecimal(target)
	if !ok {
		b.errs.add(field, core.CodeInvalid)
		return
	}
	trigger, _ := rule.get("trigger").str()
	if trigger != "threshold" {
		return
	}
	if threshold, ok := validDecimal(rule.get("thresholdCredits")); ok && t.LessThan(threshold) {
		b.errs.add(field, core.CodeInvalid)
	}
}
