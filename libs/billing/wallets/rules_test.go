package wallets_test

import (
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

func TestValidateRule(t *testing.T) {
	weekly := func(edit func(*wallets.RuleInput)) wallets.RuleInput {
		in := wallets.RuleInput{Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly}
		if edit != nil {
			edit(&in)
		}
		return in
	}
	target := wallet.RecurringMethodTarget
	tests := []struct {
		name  string
		rule  wallets.RuleInput
		valid bool
	}{
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:16
		{"interval", weekly(nil), true},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:20
		{"invalid interval", weekly(func(in *wallets.RuleInput) { in.Interval = 0 }), false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:33
		{"threshold without credits", wallets.RuleInput{Trigger: wallet.RecurringTriggerThreshold}, false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:46
		{"target without target", weekly(func(in *wallets.RuleInput) { in.Method = target }), false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:61
		{"transaction metadata", weekly(func(in *wallets.RuleInput) {
			in.TransactionMetadata = core.Metadata{{Key: "valid_key", Value: "invalid_value"}}
		}), true},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:75
		{"invalid transaction metadata", weekly(func(in *wallets.RuleInput) {
			in.TransactionMetadata = core.Metadata{{Key: "", Value: "v"}}
		}), false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:89
		{"negative credits", weekly(func(in *wallets.RuleInput) { in.PaidCredits = dec("-1") }), false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:104
		{"grants a target top-up", weekly(func(in *wallets.RuleInput) {
			in.Method, in.TargetOngoingBalance, in.GrantsTargetTopUp = target, dec("100"), ptr(true)
		}), true},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:120
		{"grants a fixed top-up", weekly(func(in *wallets.RuleInput) {
			in.Method, in.GrantsTargetTopUp = wallet.RecurringMethodFixed, ptr(true)
		}), false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:135
		{"grants false on a fixed rule", weekly(func(in *wallets.RuleInput) {
			in.Method, in.GrantsTargetTopUp = wallet.RecurringMethodFixed, ptr(false)
		}), false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:150
		{"grants omitted", weekly(func(in *wallets.RuleInput) { in.Method = wallet.RecurringMethodFixed }), true},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:178
		{"grants without method", weekly(func(in *wallets.RuleInput) { in.GrantsTargetTopUp = ptr(true) }), false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:238
		{"target below threshold", wallets.RuleInput{
			Trigger: wallet.RecurringTriggerThreshold, Method: target, TargetOngoingBalance: dec("50"), ThresholdCredits: dec("100"),
		}, false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:253
		{"target equals threshold", wallets.RuleInput{
			Trigger: wallet.RecurringTriggerThreshold, Method: target, TargetOngoingBalance: dec("100"), ThresholdCredits: dec("100"),
		}, true},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:268
		{"target above threshold", wallets.RuleInput{
			Trigger: wallet.RecurringTriggerThreshold, Method: target, TargetOngoingBalance: dec("150"), ThresholdCredits: dec("100"),
		}, true},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:283
		{"interval target ignores the threshold", weekly(func(in *wallets.RuleInput) {
			in.Method, in.TargetOngoingBalance, in.ThresholdCredits = target, dec("50"), dec("100")
		}), true},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:301
		{"no expiration", weekly(nil), true},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:327
		{"past expiration", weekly(func(in *wallets.RuleInput) { in.ExpirationAt = ptr(t0.Add(-time.Hour)) }), false},
		// source: spec/services/wallets/recurring_transaction_rules/validate_service_spec.rb:340
		{"future expiration", weekly(func(in *wallets.RuleInput) { in.ExpirationAt = ptr(t0.Add(time.Hour)) }), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			in := createInput(eurCustomer(h))
			in.RecurringTransactionRules = []wallets.RuleInput{tt.rule}
			_, err := s.Create(h.Ctx, h.OrgID(), in)
			if tt.valid {
				wantCodes(t, err)
			} else {
				wantCodes(t, err, "recurring_transaction_rules/invalid_recurring_rule")
			}
		})
	}
}

// ruleWallet is the wallet of lago
// spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:6:
// rate 1, a 15.00 minimum paid top-up, 100 paid and 50 granted credits.
func ruleWallet(h *billingtest.Harness, rule wallets.RuleInput) wallets.CreateInput {
	in := createInput(eurCustomer(h))
	in.RateAmount, in.PaidTopUpMinAmount, in.PaidTopUpMaxAmount = money.One, ptr[int64](15_00), nil
	in.PaidCredits, in.GrantedCredits = dec("100.0"), dec("50.0")
	in.RecurringTransactionRules = []wallets.RuleInput{rule}
	return in
}

func TestCreateRule(t *testing.T) {
	threshold := func(edit func(*wallets.RuleInput)) wallets.RuleInput {
		in := wallets.RuleInput{Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: dec("1.0")}
		if edit != nil {
			edit(&in)
		}
		return in
	}
	started := billingtest.Date(2024, time.May, 30, 12, 48)
	tests := []struct {
		name  string
		rule  wallets.RuleInput
		edit  func(*wallets.CreateInput)
		check func(*testing.T, *wallet.RecurringTransactionRule)
	}{
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:35
		{"target interval rule", wallets.RuleInput{
			Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, Method: wallet.RecurringMethodTarget,
			PaidCredits: dec("10.0"), GrantedCredits: dec("5.0"), StartedAt: &started, TargetOngoingBalance: dec("100.0"),
			IgnorePaidTopUpLimits: ptr(true),
		}, nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			wantDec(t, "granted", r.GrantedCredits, "5")
			wantDec(t, "paid", r.PaidCredits, "10")
			wantDec(t, "target", *r.TargetOngoingBalance, "100")
			if r.Interval != period.Monthly || r.Method != wallet.RecurringMethodTarget || !r.StartedAt.Equal(started) ||
				r.InvoiceRequiresSuccessfulPayment || !r.IgnorePaidTopUpLimits {
				t.Errorf("rule: got = %+v", r)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:83
		{"inherits the wallet credits", threshold(nil), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			wantDec(t, "granted", r.GrantedCredits, "50")
			wantDec(t, "paid", r.PaidCredits, "100")
			wantDec(t, "threshold", *r.ThresholdCredits, "1")
			if r.Method != wallet.RecurringMethodFixed || r.TargetOngoingBalance != nil {
				t.Errorf("rule: got = %+v", r)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:99
		{"paid credits within limits", threshold(func(in *wallets.RuleInput) { in.PaidCredits = dec("15") }), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			wantDec(t, "granted", r.GrantedCredits, "0")
			wantDec(t, "paid", r.PaidCredits, "15")
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:124
		{"paid credits zero", threshold(func(in *wallets.RuleInput) { in.PaidCredits = dec("0") }), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			wantDec(t, "paid", r.PaidCredits, "0")
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:139 (a
		// target rule needs its target here)
		{"target rule ignores the limits", threshold(func(in *wallets.RuleInput) {
			in.Method, in.PaidCredits, in.TargetOngoingBalance = wallet.RecurringMethodTarget, dec("5"), dec("10")
		}), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			wantDec(t, "paid", r.PaidCredits, "5")
			if r.GrantsTargetTopUp {
				t.Errorf("grants_target_top_up: got = true, want = false")
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:155
		{"grants target top-ups", threshold(func(in *wallets.RuleInput) {
			in.Method, in.TargetOngoingBalance, in.GrantsTargetTopUp = wallet.RecurringMethodTarget, dec("10"), ptr(true)
		}), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if !r.GrantsTargetTopUp {
				t.Errorf("grants_target_top_up: got = false, want = true")
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:172
		{"does not grant target top-ups", threshold(func(in *wallets.RuleInput) {
			in.Method, in.TargetOngoingBalance, in.GrantsTargetTopUp = wallet.RecurringMethodTarget, dec("10"), ptr(false)
		}), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.GrantsTargetTopUp {
				t.Errorf("grants_target_top_up: got = true, want = false")
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:190
		{"invoice requires successful payment", threshold(func(in *wallets.RuleInput) {
			in.InvoiceRequiresSuccessfulPayment = ptr(true)
		}), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if !r.InvoiceRequiresSuccessfulPayment {
				t.Errorf("invoice_requires_successful_payment: got = false, want = true")
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:205
		{"transaction metadata", threshold(func(in *wallets.RuleInput) {
			in.TransactionMetadata = core.Metadata{{Key: "valid_value", Value: "also_valid"}}
		}), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if len(r.TransactionMetadata) != 1 || r.TransactionMetadata[0].Key != "valid_value" {
				t.Errorf("transaction metadata: got = %v", r.TransactionMetadata)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:221
		{"follows the wallet payment requirement", threshold(nil), func(in *wallets.CreateInput) {
			in.InvoiceRequiresSuccessfulPayment = true
		}, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if !r.InvoiceRequiresSuccessfulPayment {
				t.Errorf("invoice_requires_successful_payment: got = false, want = true")
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:240
		{"expiration", threshold(func(in *wallets.RuleInput) { in.ExpirationAt = ptr(t0.AddDate(1, 0, 0)) }), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.ExpirationAt == nil || !r.ExpirationAt.Equal(t0.AddDate(1, 0, 0)) {
				t.Errorf("expiration: got = %v, want = %s", r.ExpirationAt, t0.AddDate(1, 0, 0))
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:258
		{"transaction name", threshold(func(in *wallets.RuleInput) { in.TransactionName = ptr("Custom Top-up Name") }), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.TransactionName == nil || *r.TransactionName != "Custom Top-up Name" {
				t.Errorf("transaction name: got = %v, want = Custom Top-up Name", r.TransactionName)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:258
		{"empty transaction name", threshold(func(in *wallets.RuleInput) { in.TransactionName = ptr("") }), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.TransactionName != nil {
				t.Errorf("transaction name: got = %q, want none", *r.TransactionName)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:258
		{"blank transaction name", threshold(func(in *wallets.RuleInput) { in.TransactionName = ptr("   ") }), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.TransactionName != nil {
				t.Errorf("transaction name: got = %q, want none", *r.TransactionName)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:258
		{"no transaction name", threshold(nil), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.TransactionName != nil {
				t.Errorf("transaction name: got = %q, want none", *r.TransactionName)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:309
		{"payment method without id", threshold(func(in *wallets.RuleInput) {
			in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider}
		}), nil, func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.PaymentMethodType != core.PaymentMethodTypeProvider || !r.PaymentMethodID.IsZero() {
				t.Errorf("payment method: got = %s %s", r.PaymentMethodType, r.PaymentMethodID)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			in := ruleWallet(h, tt.rule)
			if tt.edit != nil {
				tt.edit(&in)
			}
			w, err := s.Create(h.Ctx, h.OrgID(), in)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			rs := rules(h, w.ID)
			if len(rs) != 1 {
				t.Fatalf("rules: got = %d, want = 1", len(rs))
			}
			tt.check(t, rs[0])
		})
	}
}

func TestCreateRuleErrors(t *testing.T) {
	tests := []struct {
		name   string
		rule   func(*billingtest.Harness, core.CustomerID) wallets.RuleInput
		errors []string
	}{
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:114
		{"paid credits below the minimum", func(*billingtest.Harness, core.CustomerID) wallets.RuleInput {
			return wallets.RuleInput{Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: dec("1.0"), PaidCredits: dec("5")}
		}, []string{"recurring_transaction_rules/invalid_recurring_rule"}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:325
		{"invalid payment method type", func(h *billingtest.Harness, _ core.CustomerID) wallets.RuleInput {
			return wallets.RuleInput{
				Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: dec("1.0"),
				PaymentMethod: &wallets.PaymentMethod{Type: "invalid", ID: core.NewID[core.PaymentMethodID](h.IDs)},
			}
		}, []string{"payment_method/invalid_payment_method"}},
		// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:338
		{"unknown payment method", func(h *billingtest.Harness, _ core.CustomerID) wallets.RuleInput {
			return wallets.RuleInput{
				Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: dec("1.0"),
				PaymentMethod: &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider, ID: core.NewID[core.PaymentMethodID](h.IDs)},
			}
		}, []string{"payment_method/invalid_payment_method"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			in := ruleWallet(h, wallets.RuleInput{})
			in.RecurringTransactionRules = []wallets.RuleInput{tt.rule(h, in.CustomerID)}
			_, err := s.Create(h.Ctx, h.OrgID(), in)
			wantCodes(t, err, tt.errors...)
		})
	}
}

func TestCreateRulePaymentMethod(t *testing.T) {
	// source: spec/services/wallets/recurring_transaction_rules/create_service_spec.rb:299
	h, s := setup(t, t0)
	in := ruleWallet(h, wallets.RuleInput{})
	pm := paymentMethod(h, h.ReloadCustomer(in.CustomerID))
	in.RecurringTransactionRules = []wallets.RuleInput{{
		Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: dec("1.0"),
		PaymentMethod: &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider, ID: pm.ID},
	}}
	w, err := s.Create(h.Ctx, h.OrgID(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if r := rules(h, w.ID)[0]; r.PaymentMethodID != pm.ID || r.PaymentMethodType != core.PaymentMethodTypeProvider {
		t.Errorf("payment method: got = %s %s, want = provider %s", r.PaymentMethodType, r.PaymentMethodID, pm.ID)
	}
}

func TestUpdateRule(t *testing.T) {
	started := billingtest.Date(2024, time.May, 30, 12, 48)
	weekly := func(id core.RecurringTransactionRuleID, edit func(*wallets.RuleInput)) wallets.RuleInput {
		in := wallets.RuleInput{ID: id, Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly, PaidCredits: dec("105"), GrantedCredits: dec("105")}
		if edit != nil {
			edit(&in)
		}
		return in
	}
	tests := []struct {
		name               string
		wallet             func(*wallet.Wallet)
		rule               func(*wallet.RecurringTransactionRule)
		in                 func(*billingtest.Harness, *wallet.RecurringTransactionRule) []wallets.RuleInput
		active, terminated int
		sameID             bool
		check              func(*testing.T, *wallet.RecurringTransactionRule)
	}{
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:31
		{name: "updates the active rule", in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(r.ID, func(in *wallets.RuleInput) { in.StartedAt = &started })}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			wantDec(t, "granted", r.GrantedCredits, "105")
			wantDec(t, "paid", r.PaidCredits, "105")
			if r.Interval != period.Weekly || r.Method != wallet.RecurringMethodFixed || !r.StartedAt.Equal(started) {
				t.Errorf("rule: got = %+v", r)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:107
		{name: "terminated rule is replaced", rule: func(r *wallet.RecurringTransactionRule) {
			r.Status, r.TerminatedAt = wallet.RuleStatusTerminated, &t0
		}, in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(r.ID, nil)}
		}, active: 1, terminated: 1, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			wantDec(t, "paid", r.PaidCredits, "105")
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:140
		{name: "rule without id", in: func(*billingtest.Harness, *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(core.RecurringTransactionRuleID{}, func(in *wallets.RuleInput) {
				in.Method, in.TargetOngoingBalance = wallet.RecurringMethodTarget, dec("300")
				in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeManual}
			})}
		}, active: 1, terminated: 1, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			wantDec(t, "target", *r.TargetOngoingBalance, "300")
			if r.Method != wallet.RecurringMethodTarget || r.PaymentMethodType != core.PaymentMethodTypeManual || !r.PaymentMethodID.IsZero() {
				t.Errorf("rule: got = %+v", r)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:244
		{name: "grants target top-ups", rule: func(r *wallet.RecurringTransactionRule) {
			r.Method, r.TargetOngoingBalance = wallet.RecurringMethodTarget, dec("300")
		}, in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{{
				ID: r.ID, Method: wallet.RecurringMethodTarget, Trigger: wallet.RecurringTriggerInterval,
				Interval: period.Weekly, TargetOngoingBalance: dec("300"), GrantsTargetTopUp: ptr(true),
			}}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if !r.GrantsTargetTopUp {
				t.Errorf("grants_target_top_up: got = false, want = true")
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:270
		{name: "stops granting target top-ups", rule: func(r *wallet.RecurringTransactionRule) {
			r.Method, r.TargetOngoingBalance, r.GrantsTargetTopUp = wallet.RecurringMethodTarget, dec("300"), true
		}, in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{{
				ID: r.ID, Method: wallet.RecurringMethodTarget, Trigger: wallet.RecurringTriggerInterval,
				Interval: period.Weekly, TargetOngoingBalance: dec("300"), GrantsTargetTopUp: ptr(false),
			}}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.GrantsTargetTopUp {
				t.Errorf("grants_target_top_up: got = true, want = false")
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:298
		{name: "granting target rule to fixed", rule: func(r *wallet.RecurringTransactionRule) {
			r.Method, r.TargetOngoingBalance, r.GrantsTargetTopUp = wallet.RecurringMethodTarget, dec("300"), true
		}, in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(r.ID, func(in *wallets.RuleInput) {
				in.Method, in.PaidCredits, in.GrantedCredits = wallet.RecurringMethodFixed, dec("10"), dec("10")
			})}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.Method != wallet.RecurringMethodFixed || r.GrantsTargetTopUp {
				t.Errorf("rule: got = %s grants %t, want = fixed grants false", r.Method, r.GrantsTargetTopUp)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:322
		{name: "fixed rule to target", in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{{
				ID: r.ID, Method: wallet.RecurringMethodTarget, Trigger: wallet.RecurringTriggerInterval,
				Interval: period.Weekly, TargetOngoingBalance: dec("300"),
			}}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.Method != wallet.RecurringMethodTarget || r.GrantsTargetTopUp {
				t.Errorf("rule: got = %s grants %t, want = target grants false", r.Method, r.GrantsTargetTopUp)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:363
		{name: "empty list", in: func(*billingtest.Harness, *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{}
		}, terminated: 1},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:371
		{name: "new rule follows the wallet payment requirement", wallet: func(w *wallet.Wallet) {
			w.InvoiceRequiresSuccessfulPayment = true
		}, in: func(*billingtest.Harness, *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(core.RecurringTransactionRuleID{}, func(in *wallets.RuleInput) {
				in.PaidCredits, in.GrantedCredits = dec("10"), dec("10")
			})}
		}, active: 1, terminated: 1, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if !r.InvoiceRequiresSuccessfulPayment {
				t.Errorf("invoice_requires_successful_payment: got = false, want = true")
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:391
		{name: "transaction metadata", in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(r.ID, func(in *wallets.RuleInput) {
				in.TransactionMetadata = core.Metadata{{Key: "key", Value: "value"}}
			})}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if len(r.TransactionMetadata) != 1 {
				t.Errorf("transaction metadata: got = %v", r.TransactionMetadata)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:420
		{name: "payment method", in: func(h *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			w := h.ReloadWallet(r.WalletID)
			pm := paymentMethod(h, h.ReloadCustomer(w.CustomerID))
			return []wallets.RuleInput{weekly(r.ID, func(in *wallets.RuleInput) {
				in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider, ID: pm.ID}
			})}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.PaymentMethodID.IsZero() || r.PaymentMethodType != core.PaymentMethodTypeProvider {
				t.Errorf("payment method: got = %s %s", r.PaymentMethodType, r.PaymentMethodID)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:426
		{name: "removes the payment method", rule: func(r *wallet.RecurringTransactionRule) {
			r.PaymentMethodType, r.PaymentMethodID = core.PaymentMethodTypeProvider, core.NewID[core.PaymentMethodID](&billingtest.IDs{})
		}, in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(r.ID, func(in *wallets.RuleInput) {
				in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider}
			})}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if !r.PaymentMethodID.IsZero() || r.PaymentMethodType != core.PaymentMethodTypeProvider {
				t.Errorf("payment method: got = %s %s, want provider without id", r.PaymentMethodType, r.PaymentMethodID)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:472
		{name: "transaction name", in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(r.ID, func(in *wallets.RuleInput) { in.TransactionName = ptr("Updated Transaction Name") })}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.TransactionName == nil || *r.TransactionName != "Updated Transaction Name" {
				t.Errorf("transaction name: got = %v", r.TransactionName)
			}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:472
		{name: "blank transaction name", rule: func(r *wallet.RecurringTransactionRule) { r.TransactionName = ptr("Old") }, in: func(_ *billingtest.Harness, r *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(r.ID, func(in *wallets.RuleInput) { in.TransactionName = ptr("   ") })}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.TransactionName != nil {
				t.Errorf("transaction name: got = %q, want none", *r.TransactionName)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) {
				if tt.wallet != nil {
					tt.wallet(w)
				}
			})
			old := addRule(h, w, func(r *wallet.RecurringTransactionRule) {
				if tt.rule != nil {
					tt.rule(r)
				}
			})
			if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{RecurringTransactionRules: wallets.To(tt.in(h, old))}); err != nil {
				t.Fatalf("update: %v", err)
			}
			active := activeRules(h, w.ID)
			if len(active) != tt.active || len(rules(h, w.ID))-len(active) != tt.terminated {
				t.Fatalf("rules: got = %d active %d terminated, want = %d %d", len(active), len(rules(h, w.ID))-len(active), tt.active, tt.terminated)
			}
			if tt.active == 0 {
				return
			}
			if got := active[0].ID == old.ID; got != tt.sameID {
				t.Errorf("same rule: got = %t, want = %t", got, tt.sameID)
			}
			if tt.check != nil {
				tt.check(t, active[0])
			}
		})
	}
}

func TestUpdateRuleErrors(t *testing.T) {
	tests := []struct {
		name string
		pm   func(*billingtest.Harness) *wallets.PaymentMethod
	}{
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:442
		{"invalid payment method type", func(h *billingtest.Harness) *wallets.PaymentMethod {
			return &wallets.PaymentMethod{Type: "invalid", ID: core.NewID[core.PaymentMethodID](h.IDs)}
		}},
		// source: spec/services/wallets/recurring_transaction_rules/update_service_spec.rb:455
		{"unknown payment method", func(h *billingtest.Harness) *wallets.PaymentMethod {
			return &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider, ID: core.NewID[core.PaymentMethodID](h.IDs)}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10")
			r := addRule(h, w)
			_, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{RecurringTransactionRules: wallets.To([]wallets.RuleInput{{
				ID: r.ID, Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly, PaymentMethod: tt.pm(h),
			}})})
			wantCodes(t, err, "payment_method/invalid_payment_method")
		})
	}
}
