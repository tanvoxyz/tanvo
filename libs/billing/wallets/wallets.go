package wallets

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// CreateInput describes a new wallet (domain §14.1) with its optional
// initial top-up and recurring rule.
type CreateInput struct {
	// CustomerID is the owning customer.
	CustomerID core.CustomerID
	// Name is the display name.
	Name *string
	// Code is unique among the customer's active wallets; nil derives it
	// from the name.
	Code *string
	// Currency defaults to the customer's; it becomes the customer's
	// currency when the customer has none.
	Currency money.Currency
	// RateAmount is the value of one credit.
	RateAmount money.Decimal
	// Priority defaults to 50.
	Priority *int
	// ExpirationAt terminates the wallet when it passes.
	ExpirationAt *time.Time
	// PaidCredits are bought at creation.
	PaidCredits *money.Decimal
	// GrantedCredits are granted at creation.
	GrantedCredits *money.Decimal
	// InvoiceRequiresSuccessfulPayment keeps purchased credits pending
	// until their invoice is paid.
	InvoiceRequiresSuccessfulPayment bool
	// IgnorePaidTopUpLimitsOnCreation lets the initial purchase ignore the
	// paid top-up limits.
	IgnorePaidTopUpLimitsOnCreation bool
	// PaidTopUpMinAmount is the minimum paid top-up, in minor units.
	PaidTopUpMinAmount *int64
	// PaidTopUpMaxAmount is the maximum paid top-up, in minor units.
	PaidTopUpMaxAmount *int64
	// AllowedFeeTypes limits the fee types the wallet pays.
	AllowedFeeTypes []core.FeeType
	// BillableMetricIDs limits the charge fees the wallet pays.
	BillableMetricIDs []core.BillableMetricID
	// RecurringTransactionRules holds at most one rule.
	RecurringTransactionRules []RuleInput
	// TransactionMetadata is the metadata of the initial top-up.
	TransactionMetadata core.Metadata
	// TransactionName is the name of the initial top-up.
	TransactionName *string
	// TransactionPriority is the priority of the initial top-up.
	TransactionPriority *int
	// PaymentMethod collects paid top-ups.
	PaymentMethod *PaymentMethod
	// BillingEntityID issues the credit invoices; zero uses the customer's.
	BillingEntityID core.BillingEntityID
}

// Create creates a wallet (lago Wallets::CreateService), emits
// wallet.created and runs the initial top-up.
func (s *Service) Create(ctx context.Context, org core.OrganizationID, in CreateInput) (*wallet.Wallet, error) {
	var out *wallet.Wallet
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		w, err := s.create(ctx, r, org, &in)
		out = w
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) create(ctx context.Context, r port.Repos, org core.OrganizationID, in *CreateInput) (*wallet.Wallet, error) {
	now := s.d.Now()
	o, err := r.Organizations.Get(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("load organization %s: %w", org, err)
	}
	c, err := r.Customers.Get(ctx, org, in.CustomerID)
	if errors.Is(err, core.ErrNotFound) {
		return nil, fieldError("customer_id", CodeCustomerNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("load customer %s: %w", in.CustomerID, err)
	}
	if err := kernel.LockCustomer(ctx, r, org, c.ID, customer.LockScopePrepaidCredits); err != nil {
		return nil, err
	}

	var v core.Validator
	checkCredits(&v, "paid_credits", CodeInvalidPaidCredits, in.PaidCredits)
	checkCredits(&v, "granted_credits", CodeInvalidGrantedCredits, in.GrantedCredits)
	v.Merge("", wallet.ValidateExpiration(in.ExpirationAt, now))
	validateRuleInputs(&v, in.RecurringTransactionRules, now)
	if in.TransactionMetadata != nil {
		v.Merge("transaction_metadata", in.TransactionMetadata.Validate(core.WalletTransactionMetadataLimits()))
	}
	if err := validateLimitations(ctx, r, org, &v, in.AllowedFeeTypes, in.BillableMetricIDs); err != nil {
		return nil, err
	}
	if err := validatePaymentMethod(ctx, r, org, c.ID, &v, in.PaymentMethod); err != nil {
		return nil, err
	}
	if err := validateRulePaymentMethods(ctx, r, org, c.ID, &v, in.RecurringTransactionRules); err != nil {
		return nil, err
	}
	if !in.BillingEntityID.IsZero() {
		if _, err := r.BillingEntities.Get(ctx, org, in.BillingEntityID); err != nil {
			return nil, fmt.Errorf("load billing entity %s: %w", in.BillingEntityID, err)
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}

	actives, err := kernel.All(func(p core.PageRequest) (core.Page[*wallet.Wallet], error) {
		return r.Wallets.List(ctx, org, wallet.Filter{CustomerID: c.ID, Status: wallet.StatusActive}, p)
	})
	if err != nil {
		return nil, fmt.Errorf("list wallets of customer %s: %w", c.ID, err)
	}

	currency := in.Currency
	if currency.IsZero() {
		currency = c.Currency
	}
	w := wallet.New(org, core.NewID[core.WalletID](s.d.IDs), c.ID, currency, in.RateAmount, now)
	w.Name = in.Name
	if in.Code != nil && *in.Code != "" && codeTaken(actives, *in.Code, core.WalletID{}) {
		return nil, fieldError("code", core.CodeAlreadyExists)
	}
	w.Code = walletCode(in.Code, in.Name, actives, now)
	if in.Priority != nil {
		w.Priority = *in.Priority
	}
	w.ExpirationAt = in.ExpirationAt
	w.PaidTopUpMinAmount, w.PaidTopUpMaxAmount = in.PaidTopUpMinAmount, in.PaidTopUpMaxAmount
	w.InvoiceRequiresSuccessfulPayment = in.InvoiceRequiresSuccessfulPayment
	w.AllowedFeeTypes = slices.Clone(in.AllowedFeeTypes)
	w.BillableMetricIDs = slices.Clone(in.BillableMetricIDs)
	w.Traceable = !slices.ContainsFunc(actives, func(x *wallet.Wallet) bool { return !x.Traceable })
	w.BillingEntityID = in.BillingEntityID
	if in.PaymentMethod != nil {
		if in.PaymentMethod.Type != "" {
			w.PaymentMethodType = in.PaymentMethod.Type
		}
		w.PaymentMethodID = in.PaymentMethod.ID
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	if err := w.ValidateForCustomer(c.Currency, len(actives), o.MaxWalletsPerCustomer); err != nil {
		return nil, err
	}
	if c.Currency.IsZero() {
		c.Currency = currency
		c.UpdatedAt = now
		if err := r.Customers.Update(ctx, c); err != nil {
			return nil, fmt.Errorf("update customer %s currency: %w", c.ID, err)
		}
	}
	if err := validateInitialCredits(w, in); err != nil {
		return nil, err
	}
	if err := r.Wallets.Create(ctx, w); err != nil {
		return nil, fmt.Errorf("create wallet: %w", err)
	}
	var rule *wallet.RecurringTransactionRule
	if len(in.RecurringTransactionRules) == 1 {
		rule, err = s.newRule(w, &in.RecurringTransactionRules[0], in.PaidCredits, in.GrantedCredits)
		if err != nil {
			return nil, err
		}
		if err := r.RecurringTransactionRules.Create(ctx, rule); err != nil {
			return nil, fmt.Errorf("create rule of wallet %s: %w", w.ID, err)
		}
	}
	if err := kernel.EnqueueWalletRefresh(ctx, r, w); err != nil {
		return nil, err
	}
	if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeWalletCreated, "wallet", kernel.ObjectID(w.ID), w); err != nil {
		return nil, err
	}
	if positive(in.PaidCredits) || positive(in.GrantedCredits) {
		_, err := s.createTransactions(ctx, r, org, &TransactionInput{
			WalletID:              w.ID,
			PaidCredits:           in.PaidCredits,
			GrantedCredits:        in.GrantedCredits,
			Source:                wallet.TransactionSourceManual,
			Metadata:              in.TransactionMetadata,
			Name:                  in.TransactionName,
			Priority:              in.TransactionPriority,
			IgnorePaidTopUpLimits: in.IgnorePaidTopUpLimitsOnCreation,
		})
		if err != nil {
			return nil, err
		}
	}
	return r.Wallets.Get(ctx, org, w.ID)
}

// validateInitialCredits rejects initial credits that round to zero and a
// purchase outside the paid top-up limits.
func validateInitialCredits(w *wallet.Wallet, in *CreateInput) error {
	for _, c := range []struct {
		field   string
		credits *money.Decimal
	}{{"paid_credits", in.PaidCredits}, {"granted_credits", in.GrantedCredits}} {
		if c.credits != nil && wallet.RoundsToZero(*c.credits, w.Currency, w.RateAmount) {
			return fieldError(c.field, wallet.CodeRoundsToZero)
		}
	}
	if positive(in.PaidCredits) {
		return w.ValidatePaidTopUp("paid_credits", *in.PaidCredits, in.IgnorePaidTopUpLimitsOnCreation)
	}
	return nil
}

// walletCode returns the code of a new wallet: the given one, or the name
// in snake case ("default" without a name), suffixed with the Unix time when
// an active wallet of the customer already has it.
func walletCode(code, name *string, actives []*wallet.Wallet, now time.Time) *string {
	if code != nil && *code != "" {
		return code
	}
	c := "default"
	if name != nil {
		if p := parameterize(*name); p != "" {
			c = p
		}
	}
	if codeTaken(actives, c, core.WalletID{}) {
		c += "_" + strconv.FormatInt(now.Unix(), 10)
	}
	return &c
}

// codeTaken reports whether a wallet of actives other than except has code.
func codeTaken(actives []*wallet.Wallet, code string, except core.WalletID) bool {
	return slices.ContainsFunc(actives, func(w *wallet.Wallet) bool { return w.ID != except && w.Code != nil && *w.Code == code })
}

// parameterize lowercases s and joins its runs of letters and digits with
// underscores.
func parameterize(s string) string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r > unicode.MaxASCII || !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, "_")
}

// UpdateInput changes a wallet (lago Wallets::UpdateService).
type UpdateInput struct {
	// Name changes the display name.
	Name Change[*string]
	// Code changes the code.
	Code *string
	// Priority changes the priority.
	Priority *int
	// ExpirationAt changes or clears the expiration.
	ExpirationAt Change[*time.Time]
	// InvoiceRequiresSuccessfulPayment changes the payment requirement.
	InvoiceRequiresSuccessfulPayment *bool
	// PaidTopUpMinAmount changes or clears the minimum paid top-up.
	PaidTopUpMinAmount Change[*int64]
	// PaidTopUpMaxAmount changes or clears the maximum paid top-up.
	PaidTopUpMaxAmount Change[*int64]
	// RecurringTransactionRules replaces the rules: listed rules are
	// updated or created, the others terminated.
	RecurringTransactionRules Change[[]RuleInput]
	// AllowedFeeTypes replaces the fee type limitations.
	AllowedFeeTypes Change[[]core.FeeType]
	// BillableMetricIDs replaces the metric limitations.
	BillableMetricIDs Change[[]core.BillableMetricID]
	// PaymentMethod changes the payment method.
	PaymentMethod *PaymentMethod
	// BillingEntityID changes or clears (zero) the issuing billing entity.
	BillingEntityID Change[core.BillingEntityID]
}

// Update changes an active wallet and emits wallet.updated.
func (s *Service) Update(ctx context.Context, org core.OrganizationID, id core.WalletID, in UpdateInput) (*wallet.Wallet, error) {
	var out *wallet.Wallet
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		w, err := s.update(ctx, r, org, id, &in)
		out = w
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) update(ctx context.Context, r port.Repos, org core.OrganizationID, id core.WalletID, in *UpdateInput) (*wallet.Wallet, error) {
	now := s.d.Now()
	w, err := lockWallet(ctx, r, org, id)
	if err != nil {
		return nil, err
	}
	if !w.Active() {
		return nil, fieldError("wallet_id", CodeWalletIsTerminated)
	}
	var v core.Validator
	if in.ExpirationAt.Set {
		v.Merge("", wallet.ValidateExpiration(in.ExpirationAt.Value, now))
	}
	if in.RecurringTransactionRules.Set {
		validateRuleInputs(&v, in.RecurringTransactionRules.Value, now)
	}
	if err := validateLimitations(ctx, r, org, &v, in.AllowedFeeTypes.Value, in.BillableMetricIDs.Value); err != nil {
		return nil, err
	}
	if err := validatePaymentMethod(ctx, r, org, w.CustomerID, &v, in.PaymentMethod); err != nil {
		return nil, err
	}
	if err := validateRulePaymentMethods(ctx, r, org, w.CustomerID, &v, in.RecurringTransactionRules.Value); err != nil {
		return nil, err
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	if in.BillingEntityID.Set {
		if be := in.BillingEntityID.Value; !be.IsZero() {
			if _, err := r.BillingEntities.Get(ctx, org, be); err != nil {
				return nil, fmt.Errorf("load billing entity %s: %w", be, err)
			}
		}
		w.BillingEntityID = in.BillingEntityID.Value
	}

	before := *w
	if in.Name.Set {
		w.Name = in.Name.Value
	}
	if in.Code != nil && (w.Code == nil || *w.Code != *in.Code) {
		actives, err := kernel.All(func(p core.PageRequest) (core.Page[*wallet.Wallet], error) {
			return r.Wallets.List(ctx, org, wallet.Filter{CustomerID: w.CustomerID, Status: wallet.StatusActive}, p)
		})
		if err != nil {
			return nil, fmt.Errorf("list wallets of customer %s: %w", w.CustomerID, err)
		}
		if codeTaken(actives, *in.Code, w.ID) {
			return nil, fieldError("code", core.CodeAlreadyExists)
		}
		w.Code = in.Code
	}
	if in.Priority != nil {
		w.Priority = *in.Priority
	}
	if in.ExpirationAt.Set {
		w.ExpirationAt = in.ExpirationAt.Value
	}
	if in.InvoiceRequiresSuccessfulPayment != nil {
		w.InvoiceRequiresSuccessfulPayment = *in.InvoiceRequiresSuccessfulPayment
	}
	if in.PaidTopUpMinAmount.Set {
		w.PaidTopUpMinAmount = in.PaidTopUpMinAmount.Value
	}
	if in.PaidTopUpMaxAmount.Set {
		w.PaidTopUpMaxAmount = in.PaidTopUpMaxAmount.Value
	}
	if in.RecurringTransactionRules.Set {
		if err := s.updateRules(ctx, r, w, in.RecurringTransactionRules.Value); err != nil {
			return nil, err
		}
	}
	rules, err := r.RecurringTransactionRules.ListByWallet(ctx, org, w.ID)
	if err != nil {
		return nil, fmt.Errorf("list rules of wallet %s: %w", w.ID, err)
	}
	for _, rule := range rules {
		if rule.Status != wallet.RuleStatusActive {
			continue
		}
		if err := validateRulePaidCredits(w, rule); err != nil {
			return nil, err
		}
	}
	if in.AllowedFeeTypes.Set {
		w.AllowedFeeTypes = slices.Clone(in.AllowedFeeTypes.Value)
	}
	if in.BillableMetricIDs.Set {
		w.BillableMetricIDs = slices.Clone(in.BillableMetricIDs.Value)
	}
	if in.PaymentMethod != nil {
		if in.PaymentMethod.Type != "" {
			w.PaymentMethodType = in.PaymentMethod.Type
		}
		w.PaymentMethodID = in.PaymentMethod.ID
	}
	w.UpdatedAt = now
	if err := w.Validate(); err != nil {
		return nil, err
	}
	if err := r.Wallets.Update(ctx, w); err != nil {
		return nil, fmt.Errorf("update wallet %s: %w", w.ID, err)
	}
	if refreshRelevantChange(&before, w) {
		if err := kernel.EnqueueWalletRefresh(ctx, r, w); err != nil {
			return nil, err
		}
	}
	if err := kernel.Emit(ctx, s.d, org, webhook.EventTypeWalletUpdated, "wallet", kernel.ObjectID(w.ID), w); err != nil {
		return nil, err
	}
	return w, nil
}

// refreshRelevantChange reports whether an update changes what the ongoing
// balance allocation reads: code, priority or limitations.
func refreshRelevantChange(a, b *wallet.Wallet) bool {
	return !ptrEqual(a.Code, b.Code) || a.Priority != b.Priority ||
		!slices.Equal(a.AllowedFeeTypes, b.AllowedFeeTypes) || !slices.Equal(a.BillableMetricIDs, b.BillableMetricIDs)
}

func ptrEqual[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// Terminate terminates a wallet and its rules and emits wallet.terminated;
// a terminated wallet is returned unchanged.
func (s *Service) Terminate(ctx context.Context, org core.OrganizationID, id core.WalletID) (*wallet.Wallet, error) {
	var out *wallet.Wallet
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		w, err := lockWallet(ctx, r, org, id)
		if err != nil {
			return err
		}
		out = w
		return s.terminate(ctx, r, w)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) terminate(ctx context.Context, r port.Repos, w *wallet.Wallet) error {
	if !w.Active() {
		return nil
	}
	if err := w.Terminate(s.d.Now()); err != nil {
		return fmt.Errorf("terminate wallet %s: %w", w.ID, err)
	}
	if err := r.Wallets.Update(ctx, w); err != nil {
		return fmt.Errorf("update wallet %s: %w", w.ID, err)
	}
	if err := s.terminateRules(ctx, r, w); err != nil {
		return err
	}
	if err := kernel.EnqueueWalletRefresh(ctx, r, w); err != nil {
		return err
	}
	return kernel.Emit(ctx, s.d, w.OrganizationID, webhook.EventTypeWalletTerminated, "wallet", kernel.ObjectID(w.ID), w)
}

// validateLimitations checks the fee types and billable metrics a wallet is
// limited to (lago Wallets::ValidateLimitationsService).
func validateLimitations(ctx context.Context, r port.Repos, org core.OrganizationID, v *core.Validator, types []core.FeeType, metrics []core.BillableMetricID) error {
	ok := true
	if slices.ContainsFunc(types, func(t core.FeeType) bool { return !t.Valid() }) {
		v.Add("allowed_fee_types", CodeInvalidFeeTypes)
		ok = false
	}
	if len(metrics) > 0 {
		ids := slices.Compact(slices.SortedFunc(slices.Values(metrics), func(a, b core.BillableMetricID) int { return a.Compare(b) }))
		found, err := r.Metrics.ListByIDs(ctx, org, ids)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("load billable metrics: %w", err)
		}
		if err != nil || len(found) != len(ids) {
			v.Add("billable_metrics", CodeInvalidIdentifier)
			ok = false
		}
	}
	if !ok {
		v.Add("applies_to", CodeInvalidLimitations)
	}
	return nil
}

// validatePaymentMethod checks a payment method choice (lago
// PaymentMethods::ValidateService): a provider type may name one of the
// customer's methods, a manual type may not name any.
func validatePaymentMethod(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, v *core.Validator, pm *PaymentMethod) error {
	if pm == nil || pm.Type == "" && pm.ID.IsZero() {
		return nil
	}
	switch {
	case pm.ID.IsZero() && (pm.Type == core.PaymentMethodTypeProvider || pm.Type == core.PaymentMethodTypeManual):
		return nil
	case !pm.ID.IsZero() && pm.Type == core.PaymentMethodTypeProvider:
		m, err := r.PaymentMethods.Get(ctx, org, pm.ID)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			return fmt.Errorf("load payment method %s: %w", pm.ID, err)
		}
		if err == nil && m.CustomerID == c {
			return nil
		}
	}
	v.Add("payment_method", CodeInvalidPaymentMethod)
	return nil
}

// validateRulePaymentMethods checks the payment methods of the rules sent
// with a wallet.
func validateRulePaymentMethods(ctx context.Context, r port.Repos, org core.OrganizationID, c core.CustomerID, v *core.Validator, ins []RuleInput) error {
	for i := range ins {
		if err := validatePaymentMethod(ctx, r, org, c, v, ins[i].PaymentMethod); err != nil {
			return err
		}
	}
	return nil
}

// maxCredits returns the bound of credit inputs (lago 10^25 − 1).
func maxCredits() money.Decimal { return money.New(1, 25).Sub(money.One) }

// checkCredits rejects negative or oversized credits on field.
func checkCredits(v *core.Validator, field, code string, d *money.Decimal) bool {
	if d == nil || !d.IsNegative() && d.Cmp(maxCredits()) <= 0 {
		return true
	}
	v.Add(field, code)
	v.Add(field, wallet.CodeInvalidAmount)
	return false
}

func positive(d *money.Decimal) bool { return d != nil && wallet.NormalizeCredits(*d).IsPositive() }
