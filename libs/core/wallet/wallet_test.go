package wallet

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var testNow = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func newTestWallet(currency string, rate money.Decimal) *Wallet {
	w := New(core.OrganizationID{}, core.WalletID{}, core.CustomerID{}, money.Currency{}, rate, testNow)
	if currency != "" {
		w.Currency = money.MustCurrency(currency)
	}
	return w
}

func decimalOr(d *spectest.Decimal, def string) money.Decimal {
	if d == nil {
		return money.MustParse(def)
	}
	return d.Decimal
}

func TestWalletValidate(t *testing.T) {
	type tc struct {
		Name            string            `yaml:"name"`
		Source          string            `yaml:"source"`
		Rate            *spectest.Decimal `yaml:"rate"`
		Currency        *string           `yaml:"currency"`
		Min             *int64            `yaml:"min"`
		Max             *int64            `yaml:"max"`
		Priority        *int              `yaml:"priority"`
		Traceable       bool              `yaml:"traceable"`
		Balance         int64             `yaml:"balance"`
		CodeLength      int               `yaml:"code_length"`
		CodeBlank       bool              `yaml:"code_blank"`
		AllowedFeeTypes []string          `yaml:"allowed_fee_types"`
		Status          *string           `yaml:"status"`
		Errors          []string          `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "wallet.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			cur := "EUR"
			if c.Currency != nil {
				cur = *c.Currency
			}
			w := newTestWallet(cur, decimalOr(c.Rate, "1"))
			w.PaidTopUpMinAmount, w.PaidTopUpMaxAmount = c.Min, c.Max
			if c.Priority != nil {
				w.Priority = *c.Priority
			}
			w.Traceable, w.BalanceAmount = c.Traceable, c.Balance
			if c.CodeLength > 0 || c.CodeBlank {
				code := strings.Repeat("a", c.CodeLength)
				w.Code = &code
			}
			for _, f := range c.AllowedFeeTypes {
				w.AllowedFeeTypes = append(w.AllowedFeeTypes, core.FeeType(f))
			}
			if c.Status != nil {
				w.Status = Status(*c.Status)
			}
			if got := fieldErrors(w.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, c.Errors, c.Source)
			}
		})
	}
}

func TestWalletPredicates(t *testing.T) {
	type tc struct {
		Name                     string            `yaml:"name"`
		Source                   string            `yaml:"source"`
		AllowedFeeTypes          []string          `yaml:"allowed_fee_types"`
		BillableMetrics          int               `yaml:"billable_metrics"`
		Rate                     *spectest.Decimal `yaml:"rate"`
		Min                      *int64            `yaml:"min"`
		Max                      *int64            `yaml:"max"`
		MinCredits               *spectest.Decimal `yaml:"min_credits"`
		MaxCredits               *spectest.Decimal `yaml:"max_credits"`
		LimitedFeeTypes          bool              `yaml:"limited_fee_types"`
		LimitedToBillableMetrics bool              `yaml:"limited_to_billable_metrics"`
	}
	for _, c := range spectest.Load[tc](t, "wallet_predicates.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			w := newTestWallet("EUR", decimalOr(c.Rate, "1"))
			for _, f := range c.AllowedFeeTypes {
				w.AllowedFeeTypes = append(w.AllowedFeeTypes, core.FeeType(f))
			}
			for range c.BillableMetrics {
				w.BillableMetricIDs = append(w.BillableMetricIDs, core.NewID[core.BillableMetricID](core.UUIDv7Generator{}))
			}
			w.PaidTopUpMinAmount, w.PaidTopUpMaxAmount = c.Min, c.Max
			if got := w.LimitedFeeTypes(); got != c.LimitedFeeTypes {
				t.Errorf("LimitedFeeTypes() got = %v, want = %v (%s)", got, c.LimitedFeeTypes, c.Source)
			}
			if got := w.LimitedToBillableMetrics(); got != c.LimitedToBillableMetrics {
				t.Errorf("LimitedToBillableMetrics() got = %v, want = %v (%s)", got, c.LimitedToBillableMetrics, c.Source)
			}
			checkOptDecimal(t, "PaidTopUpMinCredits()", w.PaidTopUpMinCredits(), c.MinCredits, c.Source)
			checkOptDecimal(t, "PaidTopUpMaxCredits()", w.PaidTopUpMaxCredits(), c.MaxCredits, c.Source)
		})
	}
}

func checkOptDecimal(t *testing.T, what string, got *money.Decimal, want *spectest.Decimal, source string) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s got = %v, want = %v (%s)", what, got, want, source)
	case got.Cmp(want.Decimal) != 0:
		t.Errorf("%s got = %s, want = %s (%s)", what, got, want.Decimal, source)
	}
}

func TestWalletPays(t *testing.T) {
	gen := core.UUIDv7Generator{}
	m1, m2 := core.NewID[core.BillableMetricID](gen), core.NewID[core.BillableMetricID](gen)
	w := newTestWallet("EUR", money.NewFromInt(1))
	if !w.Pays(core.FeeTypeSubscription, core.BillableMetricID{}) {
		t.Errorf("unrestricted Pays(subscription) got = false, want = true")
	}
	w.AllowedFeeTypes = []core.FeeType{core.FeeTypeSubscription}
	w.BillableMetricIDs = []core.BillableMetricID{m1}
	for _, c := range []struct {
		t    core.FeeType
		m    core.BillableMetricID
		want bool
	}{
		{core.FeeTypeSubscription, core.BillableMetricID{}, true},
		{core.FeeTypeCharge, m1, true},
		{core.FeeTypeCharge, m2, false},
		{core.FeeTypeCharge, core.BillableMetricID{}, false},
	} {
		if got := w.Pays(c.t, c.m); got != c.want {
			t.Errorf("Pays(%s, %s) got = %v, want = %v", c.t, c.m, got, c.want)
		}
	}
}

func TestWalletValidateForCustomer(t *testing.T) {
	w := newTestWallet("EUR", money.NewFromInt(1))
	if err := w.ValidateForCustomer(money.MustCurrency("EUR"), 5, 6); err != nil {
		t.Errorf("ValidateForCustomer() got = %v, want = nil", err)
	}
	want := []string{"currency: currencies_does_not_match", "customer_id: wallets_limit_reached"}
	if got := fieldErrors(w.ValidateForCustomer(money.MustCurrency("USD"), 6, 6)); !slices.Equal(got, want) {
		t.Errorf("ValidateForCustomer() got = %v, want = %v", got, want)
	}
	past := testNow.Add(-time.Hour)
	if got := fieldErrors(ValidateExpiration(&past, testNow)); !slices.Equal(got, []string{"expiration_at: invalid_date"}) {
		t.Errorf("ValidateExpiration(past) got = %v, want = [expiration_at: invalid_date]", got)
	}
	if err := ValidateExpiration(nil, testNow); err != nil {
		t.Errorf("ValidateExpiration(nil) got = %v, want = nil", err)
	}
}

func TestWalletTransitions(t *testing.T) {
	for _, from := range StatusValues() {
		for _, to := range StatusValues() {
			want := from == StatusActive && to == StatusTerminated
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) got = %v, want = %v", from, to, got, want)
			}
		}
	}
	w := newTestWallet("EUR", money.NewFromInt(1))
	if err := w.Terminate(testNow); err != nil {
		t.Fatalf("Terminate() got = %v, want = nil", err)
	}
	if w.Status != StatusTerminated || w.TerminatedAt == nil || !w.TerminatedAt.Equal(testNow) {
		t.Errorf("Terminate() status got = %s at %v, want = terminated at %v", w.Status, w.TerminatedAt, testNow)
	}
	if err := w.Terminate(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("second Terminate() got = %v, want = %v", err, core.ErrInvalidTransition)
	}
}

func TestOrder(t *testing.T) {
	type item struct {
		Name     string   `yaml:"name"`
		Priority int      `yaml:"priority"`
		Status   string   `yaml:"status"`
		Created  float64  `yaml:"created"`
		Expires  *float64 `yaml:"expires"`
	}
	type tc struct {
		Name         string   `yaml:"name"`
		Source       string   `yaml:"source"`
		Wallets      []item   `yaml:"wallets"`
		Transactions []item   `yaml:"transactions"`
		Want         []string `yaml:"want"`
	}
	days := func(d float64) time.Time { return testNow.Add(time.Duration(d * float64(24*time.Hour))) }
	gen := core.UUIDv7Generator{}
	for _, c := range spectest.Load[tc](t, "wallet_order.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var got []string
			if len(c.Wallets) > 0 {
				var ws []*Wallet
				names := map[*Wallet]string{}
				for _, it := range c.Wallets {
					w := newTestWallet("EUR", money.NewFromInt(1))
					w.ID = core.NewID[core.WalletID](gen)
					w.Priority, w.CreatedAt = it.Priority, days(it.Created)
					if it.Expires != nil {
						e := days(*it.Expires)
						w.ExpirationAt = &e
					}
					ws = append(ws, w)
					names[w] = it.Name
				}
				slices.SortFunc(ws, CompareForApplication)
				for _, w := range ws {
					got = append(got, names[w])
				}
			} else {
				var ts []*Transaction
				names := map[*Transaction]string{}
				for _, it := range c.Transactions {
					tx := &Transaction{ID: core.NewID[core.WalletTransactionID](gen), Priority: it.Priority, TransactionStatus: TransactionStatus(it.Status), CreatedAt: days(it.Created)}
					ts = append(ts, tx)
					names[tx] = it.Name
				}
				slices.SortFunc(ts, CompareForConsumption)
				for _, tx := range ts {
					got = append(got, names[tx])
				}
			}
			if !slices.Equal(got, c.Want) {
				t.Errorf("order got = %v, want = %v (%s)", got, c.Want, c.Source)
			}
		})
	}
}

func TestCredits(t *testing.T) {
	type tc struct {
		Name        string            `yaml:"name"`
		Source      string            `yaml:"source"`
		Op          string            `yaml:"op"`
		Currency    string            `yaml:"currency"`
		Rate        spectest.Decimal  `yaml:"rate"`
		Invoiceable bool              `yaml:"invoiceable"`
		Credits     spectest.Decimal  `yaml:"credits"`
		Minor       spectest.Decimal  `yaml:"minor"`
		WantCredits *spectest.Decimal `yaml:"want_credits"`
		WantAmount  int64             `yaml:"want_amount"`
		WantBool    bool              `yaml:"want_bool"`
	}
	for _, c := range spectest.Load[tc](t, "credits.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			cur := money.MustCurrency(c.Currency)
			var (
				got Credit
				err error
			)
			switch c.Op {
			case "new":
				got, err = NewCredit(c.Credits.Decimal, cur, c.Rate.Decimal, c.Invoiceable)
			case "from_minor":
				got, err = CreditFromMinor(c.Minor.Decimal, cur, c.Rate.Decimal)
			case "rounds_to_zero":
				if b := RoundsToZero(c.Credits.Decimal, cur, c.Rate.Decimal); b != c.WantBool {
					t.Errorf("RoundsToZero() got = %v, want = %v (%s)", b, c.WantBool, c.Source)
				}
				return
			default:
				t.Fatalf("unknown op %q", c.Op)
			}
			if err != nil {
				t.Fatalf("%s got = %v, want = nil (%s)", c.Op, err, c.Source)
			}
			if got.Credits.Cmp(c.WantCredits.Decimal) != 0 || got.Amount != c.WantAmount {
				t.Errorf("%s got = %s credits for %d, want = %s credits for %d (%s)", c.Op, got.Credits, got.Amount, c.WantCredits.Decimal, c.WantAmount, c.Source)
			}
		})
	}
}

func TestValidatePaidTopUp(t *testing.T) {
	type tc struct {
		Name    string           `yaml:"name"`
		Source  string           `yaml:"source"`
		Min     *int64           `yaml:"min"`
		Max     *int64           `yaml:"max"`
		Credits spectest.Decimal `yaml:"credits"`
		Ignore  bool             `yaml:"ignore"`
		Errors  []string         `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "top_up.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			w := newTestWallet("EUR", money.NewFromInt(1))
			w.PaidTopUpMinAmount, w.PaidTopUpMaxAmount = c.Min, c.Max
			if got := fieldErrors(w.ValidatePaidTopUp("paid_credits", c.Credits.Decimal, c.Ignore)); !slices.Equal(got, c.Errors) {
				t.Errorf("ValidatePaidTopUp() got = %v, want = %v (%s)", got, c.Errors, c.Source)
			}
		})
	}
}

func TestValidateTopUp(t *testing.T) {
	w := newTestWallet("EUR", money.MustParse("0.01"))
	for _, c := range []struct {
		credits string
		want    []string
	}{
		{"1", []string{}},
		{"0.4", []string{"granted_credits: amount_rounds_to_zero"}},
		{"0", []string{"granted_credits: invalid_amount"}},
		{"-1", []string{"granted_credits: invalid_amount"}},
	} {
		if got := fieldErrors(w.ValidateTopUp("granted_credits", money.MustParse(c.credits))); !slices.Equal(got, c.want) {
			t.Errorf("ValidateTopUp(%s) got = %v, want = %v", c.credits, got, c.want)
		}
	}
}

func TestTransactionValidate(t *testing.T) {
	type tc struct {
		Name              string   `yaml:"name"`
		Source            string   `yaml:"source"`
		Priority          *int     `yaml:"priority"`
		NameLength        *int     `yaml:"name_length"`
		Status            *string  `yaml:"status"`
		Type              *string  `yaml:"type"`
		TxSource          *string  `yaml:"tx_source"`
		TransactionStatus *string  `yaml:"transaction_status"`
		Remaining         *int64   `yaml:"remaining"`
		MetadataItems     int      `yaml:"metadata_items"`
		Errors            []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "transaction.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			tx := newTestTransaction()
			if c.Priority != nil {
				tx.Priority = *c.Priority
			}
			if c.NameLength != nil {
				n := strings.Repeat("a", *c.NameLength)
				tx.Name = &n
			}
			if c.Status != nil {
				tx.Status = SettlementStatus(*c.Status)
			}
			if c.Type != nil {
				tx.TransactionType = TransactionType(*c.Type)
			}
			if c.TxSource != nil {
				tx.Source = TransactionSource(*c.TxSource)
			}
			if c.TransactionStatus != nil {
				tx.TransactionStatus = TransactionStatus(*c.TransactionStatus)
			}
			tx.RemainingAmount = c.Remaining
			for i := range c.MetadataItems {
				tx.Metadata = append(tx.Metadata, core.MetadataItem{Key: fmt.Sprintf("k%d", i), Value: "v"})
			}
			if got := fieldErrors(tx.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, c.Errors, c.Source)
			}
		})
	}
}

func newTestTransaction() *Transaction {
	return &Transaction{
		TransactionType:   TransactionTypeInbound,
		TransactionStatus: TransactionStatusGranted,
		Status:            SettlementStatusSettled,
		Source:            TransactionSourceManual,
		Currency:          money.MustCurrency("EUR"),
		Amount:            1000,
		CreditAmount:      money.NewFromInt(10),
		Priority:          DefaultPriority,
		CreatedAt:         testNow,
		UpdatedAt:         testNow,
	}
}

func TestTransactionMisc(t *testing.T) {
	type tc struct {
		Name                 string            `yaml:"name"`
		Source               string            `yaml:"source"`
		Check                string            `yaml:"check"`
		Rate                 *spectest.Decimal `yaml:"rate"`
		Remaining            *int64            `yaml:"remaining"`
		Consumed             int64             `yaml:"consumed"`
		Status               *string           `yaml:"status"`
		Type                 *string           `yaml:"type"`
		WantRemainingCredits *spectest.Decimal `yaml:"want_remaining_credits"`
		WantConsumedCredits  *spectest.Decimal `yaml:"want_consumed_credits"`
		WantAvailable        bool              `yaml:"want_available"`
	}
	for _, c := range spectest.Load[tc](t, "transaction_misc.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			rate := decimalOr(c.Rate, "1")
			tx := newTestTransaction()
			tx.RemainingAmount = c.Remaining
			if c.Status != nil {
				tx.Status = SettlementStatus(*c.Status)
			}
			if c.Type != nil {
				tx.TransactionType = TransactionType(*c.Type)
			}
			switch c.Check {
			case "consumed":
				cons := &Consumption{ConsumedAmount: c.Consumed}
				if got := cons.CreditAmount(tx.Currency, rate); got.Cmp(c.WantConsumedCredits.Decimal) != 0 {
					t.Errorf("CreditAmount() got = %s, want = %s (%s)", got, c.WantConsumedCredits.Decimal, c.Source)
				}
			case "remaining":
				checkOptDecimal(t, "RemainingCredits()", tx.RemainingCredits(rate), c.WantRemainingCredits, c.Source)
			case "available":
				if got := tx.Available(); got != c.WantAvailable {
					t.Errorf("Available() got = %v, want = %v (%s)", got, c.WantAvailable, c.Source)
				}
			default:
				t.Fatalf("unknown check %q", c.Check)
			}
		})
	}
}

func TestConsumptionValidate(t *testing.T) {
	type tc struct {
		Name         string   `yaml:"name"`
		Source       string   `yaml:"source"`
		Consumed     int64    `yaml:"consumed"`
		InboundType  string   `yaml:"inbound_type"`
		OutboundType string   `yaml:"outbound_type"`
		Errors       []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "consumption.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			in, out := newTestTransaction(), newTestTransaction()
			in.TransactionType, out.TransactionType = TransactionType(c.InboundType), TransactionType(c.OutboundType)
			cons := &Consumption{ConsumedAmount: c.Consumed}
			if got := fieldErrors(cons.Validate(in, out)); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, c.Errors, c.Source)
			}
		})
	}
}

func TestTransactionSettlement(t *testing.T) {
	for _, from := range SettlementStatusValues() {
		for _, to := range SettlementStatusValues() {
			want := from == SettlementStatusPending && to != SettlementStatusPending
			if got := CanSettlementTransition(from, to); got != want {
				t.Errorf("CanSettlementTransition(%s, %s) got = %v, want = %v", from, to, got, want)
			}
		}
	}
	for _, s := range TransactionStatusValues() {
		want := SettlementStatusSettled
		if s == TransactionStatusPurchased {
			want = SettlementStatusPending
		}
		if got := InitialStatus(s); got != want {
			t.Errorf("InitialStatus(%s) got = %s, want = %s", s, got, want)
		}
	}
	// spec/models/wallet_transaction_spec.rb:128
	tx := newTestTransaction()
	tx.TransactionStatus, tx.Status = TransactionStatusPurchased, SettlementStatusPending
	if err := tx.Fail(testNow); err != nil || tx.Status != SettlementStatusFailed || tx.FailedAt == nil {
		t.Errorf("Fail() got = %v (status %s, failed_at %v), want = nil (failed, set)", err, tx.Status, tx.FailedAt)
	}
	if err := tx.Settle(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Settle() after Fail got = %v, want = %v", err, core.ErrInvalidTransition)
	}
	tx.Status = SettlementStatusPending
	if err := tx.Settle(testNow); err != nil || tx.Status != SettlementStatusSettled || tx.SettledAt == nil {
		t.Errorf("Settle() got = %v (status %s), want = nil (settled)", err, tx.Status)
	}
}

func newTestRule() *RecurringTransactionRule {
	return &RecurringTransactionRule{
		Trigger:        RecurringTriggerInterval,
		Interval:       period.Monthly,
		Method:         RecurringMethodFixed,
		PaidCredits:    money.NewFromInt(10),
		GrantedCredits: money.NewFromInt(10),
		Status:         RuleStatusActive,
		CreatedAt:      testNow,
		UpdatedAt:      testNow,
	}
}

func optDecimal(d *spectest.Decimal) *money.Decimal {
	if d == nil {
		return nil
	}
	v := d.Decimal
	return &v
}

func TestRuleValidate(t *testing.T) {
	type tc struct {
		Name       string            `yaml:"name"`
		Source     string            `yaml:"source"`
		NameLength *int              `yaml:"name_length"`
		Method     *string           `yaml:"method"`
		Trigger    *string           `yaml:"trigger"`
		Interval   *string           `yaml:"interval"`
		Target     *spectest.Decimal `yaml:"target"`
		Threshold  *spectest.Decimal `yaml:"threshold"`
		Grants     bool              `yaml:"grants"`
		Paid       *spectest.Decimal `yaml:"paid"`
		Errors     []string          `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "rule.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			r := newTestRule()
			if c.NameLength != nil {
				n := strings.Repeat("a", *c.NameLength)
				r.TransactionName = &n
			}
			if c.Method != nil {
				r.Method = RecurringMethod(*c.Method)
			}
			if c.Trigger != nil {
				r.Trigger = RecurringTrigger(*c.Trigger)
			}
			if c.Interval != nil {
				r.Interval, _ = period.ParseInterval(*c.Interval)
			}
			r.TargetOngoingBalance, r.ThresholdCredits = optDecimal(c.Target), optDecimal(c.Threshold)
			r.GrantsTargetTopUp = c.Grants
			if c.Paid != nil {
				r.PaidCredits = c.Paid.Decimal
			}
			if got := fieldErrors(r.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() errors got = %v, want = %v (%s)", got, c.Errors, c.Source)
			}
		})
	}
}

func TestRuleCompute(t *testing.T) {
	type tc struct {
		Name          string            `yaml:"name"`
		Source        string            `yaml:"source"`
		Op            string            `yaml:"op"`
		Status        *string           `yaml:"status"`
		Expires       *float64          `yaml:"expires"`
		Method        *string           `yaml:"method"`
		Trigger       *string           `yaml:"trigger"`
		Target        *spectest.Decimal `yaml:"target"`
		Threshold     *spectest.Decimal `yaml:"threshold"`
		Grants        bool              `yaml:"grants"`
		Paid          *spectest.Decimal `yaml:"paid"`
		Granted       *spectest.Decimal `yaml:"granted"`
		Ignore        bool              `yaml:"ignore"`
		Rate          *spectest.Decimal `yaml:"rate"`
		Min           *int64            `yaml:"min"`
		Max           *int64            `yaml:"max"`
		WalletOngoing spectest.Decimal  `yaml:"wallet_ongoing"`
		Ongoing       spectest.Decimal  `yaml:"ongoing"`
		Pending       spectest.Decimal  `yaml:"pending"`
		Credits       spectest.Decimal  `yaml:"credits"`
		Want          spectest.Decimal  `yaml:"want"`
		WantBool      bool              `yaml:"want_bool"`
	}
	for _, c := range spectest.Load[tc](t, "rule_compute.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			w := newTestWallet("EUR", decimalOr(c.Rate, "1"))
			w.PaidTopUpMinAmount, w.PaidTopUpMaxAmount = c.Min, c.Max
			w.CreditsOngoingBalance = c.WalletOngoing.Decimal
			r := newTestRule()
			if c.Status != nil {
				r.Status = RuleStatus(*c.Status)
			}
			if c.Expires != nil {
				e := testNow.Add(time.Duration(*c.Expires * float64(24*time.Hour)))
				r.ExpirationAt = &e
			}
			if c.Method != nil {
				r.Method = RecurringMethod(*c.Method)
			}
			if c.Trigger != nil {
				r.Trigger = RecurringTrigger(*c.Trigger)
			}
			r.TargetOngoingBalance, r.ThresholdCredits = optDecimal(c.Target), optDecimal(c.Threshold)
			r.GrantsTargetTopUp, r.IgnorePaidTopUpLimits = c.Grants, c.Ignore
			if c.Paid != nil {
				r.PaidCredits = c.Paid.Decimal
			}
			if c.Granted != nil {
				r.GrantedCredits = c.Granted.Decimal
			}
			var got money.Decimal
			switch c.Op {
			case "currently_active", "eligible_for_termination", "threshold_reached":
				var b bool
				switch c.Op {
				case "currently_active":
					b = r.CurrentlyActive(testNow)
				case "eligible_for_termination":
					b = r.EligibleForTermination(testNow)
				default:
					b = r.ThresholdReached(c.Ongoing.Decimal)
				}
				if b != c.WantBool {
					t.Errorf("%s got = %v, want = %v (%s)", c.Op, b, c.WantBool, c.Source)
				}
				return
			case "apply_min":
				got = r.ApplyMinTopUpLimit(w, c.Credits.Decimal)
			case "granted":
				got = r.ComputeGrantedCredits(w)
			case "paid":
				got = r.ComputePaidCredits(w, c.Ongoing.Decimal, c.Pending.Decimal)
			default:
				t.Fatalf("unknown op %q", c.Op)
			}
			if got.Cmp(c.Want.Decimal) != 0 {
				t.Errorf("%s got = %s, want = %s (%s)", c.Op, got, c.Want.Decimal, c.Source)
			}
		})
	}
}

func TestRuleTransitions(t *testing.T) {
	for _, from := range RuleStatusValues() {
		for _, to := range RuleStatusValues() {
			want := from == RuleStatusActive && to == RuleStatusTerminated
			if got := CanRuleTransition(from, to); got != want {
				t.Errorf("CanRuleTransition(%s, %s) got = %v, want = %v", from, to, got, want)
			}
		}
	}
	// spec/models/recurring_transaction_rule_spec.rb:147
	r := newTestRule()
	if err := r.Terminate(testNow); err != nil || r.Status != RuleStatusTerminated || r.TerminatedAt == nil {
		t.Errorf("Terminate() got = %v (status %s), want = nil (terminated)", err, r.Status)
	}
	if err := r.Terminate(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("second Terminate() got = %v, want = %v", err, core.ErrInvalidTransition)
	}
	last := testNow.Add(-30 * time.Minute)
	if !ThresholdSuppressed(&last, testNow) || ThresholdSuppressed(&last, testNow.Add(time.Hour)) || ThresholdSuppressed(nil, testNow) {
		t.Errorf("ThresholdSuppressed() got = wrong, want = suppressed only within one hour of the failure")
	}
}
