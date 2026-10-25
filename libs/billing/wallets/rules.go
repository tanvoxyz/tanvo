package wallets

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// RuleInput describes a recurring transaction rule to create, or the
// attributes to change on an existing one (domain §14.3). Unset fields keep
// their defaults on creation and their value on update.
type RuleInput struct {
	// ID selects the active rule to update; zero creates a rule.
	ID core.RecurringTransactionRuleID
	// Trigger is interval or threshold.
	Trigger wallet.RecurringTrigger
	// Interval is the period of interval rules.
	Interval period.Interval
	// ThresholdCredits is the ongoing balance threshold of threshold rules;
	// nil defaults to 0.
	ThresholdCredits *money.Decimal
	// Method is fixed (the default) or target.
	Method wallet.RecurringMethod
	// PaidCredits are the credits each run buys (fixed rules).
	PaidCredits *money.Decimal
	// GrantedCredits are the credits each run grants (fixed rules).
	GrantedCredits *money.Decimal
	// TargetOngoingBalance is the ongoing balance target rules reach.
	TargetOngoingBalance *money.Decimal
	// GrantsTargetTopUp grants target top-ups instead of selling them.
	GrantsTargetTopUp *bool
	// StartedAt anchors the interval schedule.
	StartedAt *time.Time
	// ExpirationAt ends the rule.
	ExpirationAt *time.Time
	// InvoiceRequiresSuccessfulPayment defaults to the wallet's.
	InvoiceRequiresSuccessfulPayment *bool
	// TransactionMetadata is copied to the transactions; nil keeps it.
	TransactionMetadata core.Metadata
	// TransactionName is copied to the transactions; blank clears it.
	TransactionName *string
	// IgnorePaidTopUpLimits lets runs buy outside the wallet's limits.
	IgnorePaidTopUpLimits *bool
	// PaymentMethod collects the runs' purchases.
	PaymentMethod *PaymentMethod
	// PurchaseOrderNumber is copied to the runs' transactions (blank is
	// none, falling back to the wallet's); nil keeps it on update.
	PurchaseOrderNumber *string
	// Connections, keyed by category, choose the rule's connections
	// (domain §15.5); categories left out keep theirs (inherit on
	// creation).
	Connections map[string]kernel.ConnectionChoice
}

func (in *RuleInput) method() wallet.RecurringMethod {
	if in.Method == "" {
		return wallet.RecurringMethodFixed
	}
	return in.Method
}

// validRuleInput reports whether in describes a valid rule (lago
// RecurringTransactionRules::ValidateService).
func validRuleInput(in *RuleInput, now time.Time) bool {
	switch in.Trigger {
	case wallet.RecurringTriggerInterval:
		if !in.Interval.Valid() {
			return false
		}
	case wallet.RecurringTriggerThreshold:
		if in.ThresholdCredits == nil {
			return false
		}
	default:
		return false
	}
	target := in.Method == wallet.RecurringMethodTarget
	if target && in.TargetOngoingBalance == nil {
		return false
	}
	if target && in.Trigger == wallet.RecurringTriggerThreshold && in.TargetOngoingBalance.Cmp(*in.ThresholdCredits) < 0 {
		return false
	}
	for _, d := range []*money.Decimal{in.ThresholdCredits, in.PaidCredits, in.GrantedCredits, in.TargetOngoingBalance} {
		if d != nil && d.IsNegative() {
			return false
		}
	}
	if in.TransactionMetadata != nil && in.TransactionMetadata.Validate(core.WalletTransactionMetadataLimits()) != nil {
		return false
	}
	if in.ExpirationAt != nil && !in.ExpirationAt.After(now) {
		return false
	}
	return in.GrantsTargetTopUp == nil || target
}

// validateRuleInputs checks the rules sent with a wallet: at most one, and
// valid.
func validateRuleInputs(v *core.Validator, ins []RuleInput, now time.Time) {
	switch {
	case len(ins) > 1:
		v.Add("recurring_transaction_rules", CodeInvalidNumberOfRecurringRules)
	case len(ins) == 1 && !validRuleInput(&ins[0], now):
		v.Add("recurring_transaction_rules", CodeInvalidRecurringRule)
	}
	// The first error code of the first rule with invalid connections.
	//
	// source: app/services/wallets/recurring_transaction_rules/create_service.rb:117-124
	// source: app/services/wallets/recurring_transaction_rules/update_service.rb:149-160
	for i := range ins {
		if codes := kernel.ConnectionErrorCodes(ins[i].Connections); len(codes) > 0 {
			v.Add(kernel.FieldConnections, codes[0])
			return
		}
	}
}

// newRule builds the rule of a new wallet (lago
// RecurringTransactionRules::CreateService): a fixed rule without credits
// takes the wallet's initial credits.
func (s *Service) newRule(w *wallet.Wallet, in *RuleInput, walletPaid, walletGranted *money.Decimal) (*wallet.RecurringTransactionRule, error) {
	now := s.d.Now()
	method := in.method()
	paid, granted := in.PaidCredits, in.GrantedCredits
	if method == wallet.RecurringMethodFixed && paid == nil && granted == nil {
		paid, granted = walletPaid, walletGranted
	}
	r := &wallet.RecurringTransactionRule{
		ID:                   core.NewID[core.RecurringTransactionRuleID](s.d.IDs),
		OrganizationID:       w.OrganizationID,
		WalletID:             w.ID,
		Trigger:              in.Trigger,
		Interval:             in.Interval,
		ThresholdCredits:     decimalOr(in.ThresholdCredits),
		Method:               method,
		PaidCredits:          *decimalOr(paid),
		GrantedCredits:       *decimalOr(granted),
		TargetOngoingBalance: in.TargetOngoingBalance,
		StartedAt:            in.StartedAt,
		ExpirationAt:         in.ExpirationAt,
		Status:               wallet.RuleStatusActive,
		TransactionMetadata:  in.TransactionMetadata,
		TransactionName:      blankToNil(in.TransactionName),
		PurchaseOrderNumber:  core.NormalizePurchaseOrderNumber(in.PurchaseOrderNumber),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if r.Trigger == wallet.RecurringTriggerInterval {
		r.ThresholdCredits = nil
	}
	if in.IgnorePaidTopUpLimits != nil {
		r.IgnorePaidTopUpLimits = *in.IgnorePaidTopUpLimits
	}
	if method == wallet.RecurringMethodTarget && in.GrantsTargetTopUp != nil {
		r.GrantsTargetTopUp = *in.GrantsTargetTopUp
	}
	if in.PaymentMethod != nil {
		r.PaymentMethodType, r.PaymentMethodID = in.PaymentMethod.Type, in.PaymentMethod.ID
	}
	r.InvoiceRequiresSuccessfulPayment = w.InvoiceRequiresSuccessfulPayment
	if in.InvoiceRequiresSuccessfulPayment != nil {
		r.InvoiceRequiresSuccessfulPayment = *in.InvoiceRequiresSuccessfulPayment
	}
	// A purchase order number too long is reported on its own field
	// (spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:67).
	var v core.Validator
	if !v.OptionalString("purchase_order_number", r.PurchaseOrderNumber, core.MaxPurchaseOrderNumberLength) {
		return nil, v.Err()
	}
	if r.Validate() != nil {
		return nil, fieldError("recurring_transaction_rules", CodeInvalidRecurringRule)
	}
	if err := validateRulePaidCredits(w, r); err != nil {
		return nil, err
	}
	return r, nil
}

// validateRulePaidCredits checks the paid credits of a fixed rule against
// the wallet's paid top-up limits.
func validateRulePaidCredits(w *wallet.Wallet, r *wallet.RecurringTransactionRule) error {
	if r.Method != wallet.RecurringMethodFixed || wallet.NormalizeCredits(r.PaidCredits).IsZero() {
		return nil
	}
	if w.ValidatePaidTopUp("paid_credits", r.PaidCredits, r.IgnorePaidTopUpLimits) != nil {
		return fieldError("recurring_transaction_rules", CodeInvalidRecurringRule)
	}
	return nil
}

// updateRules applies the rules sent on a wallet update (lago
// RecurringTransactionRules::UpdateService): listed active rules are
// updated, new ones created, and the others terminated.
func (s *Service) updateRules(ctx context.Context, r port.Repos, w *wallet.Wallet, ins []RuleInput) error {
	now := s.d.Now()
	rules, err := r.RecurringTransactionRules.ListByWallet(ctx, w.OrganizationID, w.ID)
	if err != nil {
		return fmt.Errorf("list rules of wallet %s: %w", w.ID, err)
	}
	keep := map[core.RecurringTransactionRuleID]bool{}
	for i := range ins {
		in := &ins[i]
		idx := slices.IndexFunc(rules, func(x *wallet.RecurringTransactionRule) bool {
			return !in.ID.IsZero() && x.ID == in.ID && x.Status == wallet.RuleStatusActive
		})
		if idx < 0 {
			rule, err := s.updatedRule(w, &wallet.RecurringTransactionRule{
				ID:                               core.NewID[core.RecurringTransactionRuleID](s.d.IDs),
				OrganizationID:                   w.OrganizationID,
				WalletID:                         w.ID,
				Method:                           wallet.RecurringMethodFixed,
				Status:                           wallet.RuleStatusActive,
				InvoiceRequiresSuccessfulPayment: w.InvoiceRequiresSuccessfulPayment,
				CreatedAt:                        now,
			}, in)
			if err != nil {
				return err
			}
			if err := r.RecurringTransactionRules.Create(ctx, rule); err != nil {
				return fmt.Errorf("create rule of wallet %s: %w", w.ID, err)
			}
			// update_service.rb:76.
			if err := kernel.AttachConnections(ctx, s.d, r, w.OrganizationID, w.CustomerID, integration.ObjectOwner{RecurringTransactionRuleID: rule.ID}, in.Connections); err != nil {
				return err
			}
			keep[rule.ID] = true
			continue
		}
		rule, err := s.updatedRule(w, rules[idx], in)
		if err != nil {
			return err
		}
		if err := r.RecurringTransactionRules.Update(ctx, rule); err != nil {
			return fmt.Errorf("update rule %s: %w", rule.ID, err)
		}
		// update_service.rb:59.
		if err := kernel.AttachConnections(ctx, s.d, r, w.OrganizationID, w.CustomerID, integration.ObjectOwner{RecurringTransactionRuleID: rule.ID}, in.Connections); err != nil {
			return err
		}
		keep[rule.ID] = true
	}
	for _, rule := range rules {
		if keep[rule.ID] || rule.Status != wallet.RuleStatusActive {
			continue
		}
		if err := rule.Terminate(now); err != nil {
			return fmt.Errorf("terminate rule %s: %w", rule.ID, err)
		}
		if err := r.RecurringTransactionRules.Update(ctx, rule); err != nil {
			return fmt.Errorf("update rule %s: %w", rule.ID, err)
		}
	}
	return nil
}

// updatedRule applies the attributes sent in in to rule.
func (s *Service) updatedRule(w *wallet.Wallet, rule *wallet.RecurringTransactionRule, in *RuleInput) (*wallet.RecurringTransactionRule, error) {
	if in.Trigger != "" {
		rule.Trigger = in.Trigger
	}
	if in.Interval.Valid() {
		rule.Interval = in.Interval
	}
	if in.ThresholdCredits != nil {
		rule.ThresholdCredits = in.ThresholdCredits
	}
	if rule.Trigger == wallet.RecurringTriggerThreshold && rule.ThresholdCredits == nil {
		rule.ThresholdCredits = decimalOr(nil)
	}
	if in.Method != "" {
		rule.Method = in.Method
	}
	if in.PaidCredits != nil {
		rule.PaidCredits = *in.PaidCredits
	}
	if in.GrantedCredits != nil {
		rule.GrantedCredits = *in.GrantedCredits
	}
	if in.TargetOngoingBalance != nil {
		rule.TargetOngoingBalance = in.TargetOngoingBalance
	}
	if rule.Method == wallet.RecurringMethodTarget {
		if in.GrantsTargetTopUp != nil {
			rule.GrantsTargetTopUp = *in.GrantsTargetTopUp
		}
	} else {
		rule.GrantsTargetTopUp = false
	}
	if in.StartedAt != nil {
		rule.StartedAt = in.StartedAt
	}
	if in.ExpirationAt != nil {
		rule.ExpirationAt = in.ExpirationAt
	}
	if in.InvoiceRequiresSuccessfulPayment != nil {
		rule.InvoiceRequiresSuccessfulPayment = *in.InvoiceRequiresSuccessfulPayment
	}
	if in.TransactionMetadata != nil {
		rule.TransactionMetadata = in.TransactionMetadata
	}
	if in.TransactionName != nil {
		rule.TransactionName = blankToNil(in.TransactionName)
	}
	if in.IgnorePaidTopUpLimits != nil {
		rule.IgnorePaidTopUpLimits = *in.IgnorePaidTopUpLimits
	}
	if in.PaymentMethod != nil {
		rule.PaymentMethodType, rule.PaymentMethodID = in.PaymentMethod.Type, in.PaymentMethod.ID
	}
	if in.PurchaseOrderNumber != nil {
		rule.PurchaseOrderNumber = core.NormalizePurchaseOrderNumber(in.PurchaseOrderNumber)
	}
	rule.UpdatedAt = s.d.Now()
	// A purchase order number too long is reported on its own field
	// (spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:67).
	var v core.Validator
	if !v.OptionalString("purchase_order_number", rule.PurchaseOrderNumber, core.MaxPurchaseOrderNumberLength) {
		return nil, v.Err()
	}
	if rule.Validate() != nil {
		return nil, fieldError("recurring_transaction_rules", CodeInvalidRecurringRule)
	}
	return rule, nil
}

// terminateRules terminates the active rules of wallet w.
func (s *Service) terminateRules(ctx context.Context, r port.Repos, w *wallet.Wallet) error {
	rules, err := r.RecurringTransactionRules.ListByWallet(ctx, w.OrganizationID, w.ID)
	if err != nil {
		return fmt.Errorf("list rules of wallet %s: %w", w.ID, err)
	}
	for _, rule := range rules {
		if rule.Status != wallet.RuleStatusActive {
			continue
		}
		if err := rule.Terminate(s.d.Now()); err != nil {
			return fmt.Errorf("terminate rule %s: %w", rule.ID, err)
		}
		if err := r.RecurringTransactionRules.Update(ctx, rule); err != nil {
			return fmt.Errorf("update rule %s: %w", rule.ID, err)
		}
	}
	return nil
}

func decimalOr(d *money.Decimal) *money.Decimal {
	if d != nil {
		return d
	}
	z := money.Zero
	return &z
}

func blankToNil(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	return s
}
