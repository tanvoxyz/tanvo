package wallets_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/wallets"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/wallet"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// createInput is the wallet of lago spec/services/wallets/create_service_spec.rb:19.
func createInput(c *customer.Customer) wallets.CreateInput {
	exp := t0.AddDate(1, 0, 0)
	return wallets.CreateInput{
		CustomerID:         c.ID,
		Name:               ptr("New Wallet"),
		Priority:           ptr(5),
		Currency:           eur,
		RateAmount:         money.MustParse("5.00"),
		ExpirationAt:       &exp,
		PaidCredits:        dec("1.00"),
		GrantedCredits:     dec("0.00"),
		PaidTopUpMinAmount: ptr[int64](1_00),
		PaidTopUpMaxAmount: ptr[int64](1_000_00),
	}
}

func eurCustomer(h *billingtest.Harness) *customer.Customer {
	return h.Customer(func(c *customer.Customer) { c.Currency = eur })
}

func paymentMethod(h *billingtest.Harness, c *customer.Customer) *payment.Method {
	m := &payment.Method{
		ID: core.NewID[core.PaymentMethodID](h.IDs), OrganizationID: h.OrgID(), CustomerID: c.ID,
		ProviderMethodID: "pm_1", CreatedAt: h.Now(), UpdatedAt: h.Now(),
	}
	h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Create(ctx, m) })
	return m
}

func TestCreate(t *testing.T) {
	// source: spec/services/wallets/create_service_spec.rb:38
	h, s := setup(t, t0)
	c := eurCustomer(h)
	in := createInput(c)
	w, err := s.Create(h.Ctx, h.OrgID(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.CustomerID != c.ID || *w.Name != "New Wallet" || w.Priority != 5 || w.Currency != eur {
		t.Errorf("wallet: got = %+v", w)
	}
	wantDec(t, "rate", w.RateAmount, "5")
	if !w.ExpirationAt.Equal(*in.ExpirationAt) {
		t.Errorf("expiration: got = %s, want = %s", w.ExpirationAt, in.ExpirationAt)
	}
	if w.InvoiceRequiresSuccessfulPayment {
		t.Errorf("invoice_requires_successful_payment: got = true, want = false")
	}
	if *w.PaidTopUpMinAmount != 1_00 || *w.PaidTopUpMaxAmount != 1_000_00 {
		t.Errorf("limits: got = %d..%d, want = 100..100000", *w.PaidTopUpMinAmount, *w.PaidTopUpMaxAmount)
	}
	if got := len(rules(h, w.ID)); got != 0 {
		t.Errorf("rules: got = %d, want = 0", got)
	}
	// source: spec/services/wallets/create_service_spec.rb:56
	if got := h.Webhooks(); !slices.Contains(got, webhook.EventTypeWalletCreated) {
		t.Errorf("webhooks: got = %v, want wallet.created", got)
	}
	// source: spec/services/wallets/create_service_spec.rb:66
	if got := h.JobKinds(); !slices.Contains(got, kernel.JobRefreshWalletOngoingBalance) {
		t.Errorf("jobs: got = %v, want a refresh", got)
	}
	// source: spec/services/wallets/create_service_spec.rb:67
	if !awaitingRefresh(h, c.ID) {
		t.Errorf("awaiting_wallet_refresh: got = false, want = true")
	}
	// source: spec/services/wallets/create_service_spec.rb:127
	if !w.Traceable {
		t.Errorf("traceable: got = false, want = true")
	}
	// source: spec/services/wallets/create_service_spec.rb:70 (the
	// initial top-up runs inline: 1 paid credit at 5 EUR, pending until
	// its credit invoice is paid).
	ts := transactions(h, w.ID)
	if len(ts) != 1 || ts[0].TransactionStatus != wallet.TransactionStatusPurchased || ts[0].Source != wallet.TransactionSourceManual {
		t.Fatalf("transactions: got = %+v, want one manual purchase", ts)
	}
	if ts[0].Amount != 500 || ts[0].Status != wallet.SettlementStatusPending {
		t.Errorf("purchase: got = %d %s, want = 500 pending", ts[0].Amount, ts[0].Status)
	}
	if w.BalanceAmount != 0 {
		t.Errorf("balance: got = %d, want = 0", w.BalanceAmount)
	}
}

func TestCreateInitialTopUp(t *testing.T) {
	tests := []struct {
		name          string
		paid, granted *money.Decimal
		want          int
	}{
		// source: spec/services/wallets/create_service_spec.rb:83
		{"granted zero", dec("10.00"), dec("0.00"), 1},
		// source: spec/services/wallets/create_service_spec.rb:84
		{"granted nil", dec("10.00"), nil, 1},
		// source: spec/services/wallets/create_service_spec.rb:85
		{"both zero", dec("0.00"), dec("0.00"), 0},
		// source: spec/services/wallets/create_service_spec.rb:86
		{"both nil", nil, nil, 0},
		// source: spec/services/wallets/create_service_spec.rb:87
		{"nil and zero", nil, dec("0.00"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			in := createInput(eurCustomer(h))
			in.PaidCredits, in.GrantedCredits = tt.paid, tt.granted
			w, err := s.Create(h.Ctx, h.OrgID(), in)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if got := len(transactions(h, w.ID)); got != tt.want {
				t.Errorf("transactions: got = %d, want = %d", got, tt.want)
			}
		})
	}
}

func TestCreateTraceable(t *testing.T) {
	tests := []struct {
		name     string
		existing func(*wallet.Wallet)
		want     bool
	}{
		// source: spec/services/wallets/create_service_spec.rb:132
		{"active traceable", func(w *wallet.Wallet) { w.Traceable = true }, true},
		// source: spec/services/wallets/create_service_spec.rb:141
		{"active non traceable", func(w *wallet.Wallet) { w.Traceable = false }, false},
		// source: spec/services/wallets/create_service_spec.rb:150
		{"terminated non traceable", func(w *wallet.Wallet) { w.Traceable = false; w.Status = wallet.StatusTerminated }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			c := eurCustomer(h)
			h.Wallet(c, "0", func(w *wallet.Wallet) { w.Currency = eur; w.Code = ptr("old"); tt.existing(w) })
			w, err := s.Create(h.Ctx, h.OrgID(), createInput(c))
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if w.Traceable != tt.want {
				t.Errorf("traceable: got = %t, want = %t", w.Traceable, tt.want)
			}
		})
	}
}

func TestCreateErrors(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*billingtest.Harness, *customer.Customer, *wallets.CreateInput)
		errors []string
	}{
		// source: spec/services/wallets/create_service_spec.rb:159
		{"negative paid credits", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.PaidCredits = dec("-1")
		}, []string{"paid_credits/invalid_paid_credits", "paid_credits/invalid_amount"}},
		// source: spec/services/wallets/validate_service_spec.rb:94
		{"oversized granted credits", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.GrantedCredits = dec("1e26")
		}, []string{"granted_credits/invalid_granted_credits", "granted_credits/invalid_amount"}},
		// source: spec/services/wallets/create_service_spec.rb:168
		{"purchase order number too long", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.PurchaseOrderNumber = ptr(strings.Repeat("a", 256))
		}, []string{"purchase_order_number/value_is_too_long"}},
		// source: spec/services/wallets/create_service_spec.rb:181
		{"credits round to zero", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RateAmount, in.PaidCredits, in.GrantedCredits = money.MustParse("0.01"), nil, dec("0.4")
			in.PaidTopUpMinAmount, in.PaidTopUpMaxAmount = nil, nil
		}, []string{"granted_credits/amount_rounds_to_zero"}},
		// source: spec/services/wallets/create_service_spec.rb:201
		{"wallet limit reached", func(h *billingtest.Harness, c *customer.Customer, _ *wallets.CreateInput) {
			for range organizationLimit {
				h.Wallet(c, "0", func(w *wallet.Wallet) { w.Currency = eur })
			}
		}, []string{"customer_id/wallets_limit_reached"}},
		// source: spec/services/wallets/create_service_spec.rb:213
		{"paid credits above maximum", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RateAmount, in.PaidCredits = money.One, dec("1002.0")
		}, []string{"paid_credits/amount_above_maximum"}},
		// source: spec/services/wallets/create_service_spec.rb:237
		{"priority out of bounds", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.Priority = ptr(55)
		}, []string{"priority/" + core.CodeOutOfRange}},
		// source: spec/services/wallets/create_service_spec.rb:344
		{"no currency", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			h.UpdateCustomer(c, func(c *customer.Customer) { c.Currency = money.Currency{} })
			in.Currency = money.Currency{}
		}, []string{"currency/" + core.CodeInvalidCurrency}},
		// source: spec/services/wallets/validate_service_spec.rb:69
		{"unknown customer", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.CustomerID = core.CustomerID{}
		}, []string{"customer_id/customer_not_found"}},
		// source: spec/services/wallets/validate_service_spec.rb:122
		{"expiration in the past", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.ExpirationAt = ptr(t0.Add(-time.Hour))
		}, []string{"expiration_at/invalid_date"}},
		// source: spec/services/wallets/validate_service_spec.rb:140
		{"invalid transaction metadata", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.TransactionMetadata = core.Metadata{{Key: "", Value: "v"}}
		}, []string{"transaction_metadata.items[0].key/" + core.CodeRequired}},
		// source: spec/services/wallets/validate_service_spec.rb:158
		{"two rules", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RecurringTransactionRules = []wallets.RuleInput{
				{Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly},
				{Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: dec("-1.0")},
			}
		}, []string{"recurring_transaction_rules/invalid_number_of_recurring_rules"}},
		// source: spec/services/wallets/create_service_spec.rb:678
		{"invalid trigger", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RecurringTransactionRules = []wallets.RuleInput{{Trigger: "invalid", Interval: period.Monthly}}
		}, []string{"recurring_transaction_rules/invalid_recurring_rule"}},
		// source: spec/services/wallets/create_service_spec.rb:694
		{"invalid threshold credits", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RecurringTransactionRules = []wallets.RuleInput{{Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: dec("-1")}}
		}, []string{"recurring_transaction_rules/invalid_recurring_rule"}},
		// source: spec/services/wallets/create_service_spec.rb:710
		{"rule paid credits above limits", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RateAmount = money.One
			in.RecurringTransactionRules = []wallets.RuleInput{{Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, PaidCredits: dec("100000")}}
		}, []string{"recurring_transaction_rules/invalid_recurring_rule"}},
		// source: spec/services/wallets/create_service_spec.rb:757
		{"invalid fee type", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.AllowedFeeTypes = []core.FeeType{"invalid", core.FeeTypeCharge}
		}, []string{"allowed_fee_types/invalid_fee_types", "applies_to/invalid_limitations"}},
		// source: spec/services/wallets/create_service_spec.rb:790
		{"invalid billable metric", func(h *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			m := h.Metric("m", catalog.AggregationTypeCount, "")
			in.BillableMetricIDs = []core.BillableMetricID{m.ID, core.NewID[core.BillableMetricID](h.IDs)}
		}, []string{"billable_metrics/invalid_identifier", "applies_to/invalid_limitations"}},
		// source: spec/services/wallets/create_service_spec.rb:879
		{"invalid payment method type", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			in.PaymentMethod = &wallets.PaymentMethod{Type: "invalid", ID: paymentMethod(h, c).ID}
		}, []string{"payment_method/invalid_payment_method"}},
		// source: spec/services/wallets/create_service_spec.rb:893
		{"unknown payment method", func(h *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider, ID: core.NewID[core.PaymentMethodID](h.IDs)}
		}, []string{"payment_method/invalid_payment_method"}},
		// source: spec/services/wallets/create_service_spec.rb:1021
		{"code taken", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			h.Wallet(c, "0", func(w *wallet.Wallet) { w.Currency = eur; w.Code = ptr("existing_code") })
			in.Code = ptr("existing_code")
		}, []string{"code/" + core.CodeAlreadyExists}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			c := eurCustomer(h)
			in := createInput(c)
			tt.edit(h, c, &in)
			before := h.Webhooks()
			_, err := s.Create(h.Ctx, h.OrgID(), in)
			wantCodes(t, err, tt.errors...)
			if got := len(h.Webhooks()); got != len(before) {
				t.Errorf("webhooks: got = %d, want = %d", got, len(before))
			}
		})
	}
}

const organizationLimit = 6

func TestCreateSucceeds(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(*billingtest.Harness, *customer.Customer, *wallets.CreateInput)
		check func(*testing.T, *billingtest.Harness, *wallet.Wallet)
	}{
		// source: spec/services/wallets/create_service_spec.rb:223
		{"paid credits above maximum ignoring limits", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RateAmount, in.PaidCredits, in.IgnorePaidTopUpLimitsOnCreation = money.One, dec("1002.0"), true
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			ts := transactions(h, w.ID)
			if len(ts) != 1 {
				t.Fatalf("transactions: got = %d, want = 1", len(ts))
			}
			wantDec(t, "credit amount", ts[0].CreditAmount, "1002")
		}},
		// source: spec/services/wallets/create_service_spec.rb:255
		{"priority not set", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.Priority = nil
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if w.Priority != 50 {
				t.Errorf("priority: got = %d, want = 50", w.Priority)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:294
		{"invoice requires successful payment", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.InvoiceRequiresSuccessfulPayment = true
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			if !w.InvoiceRequiresSuccessfulPayment {
				t.Errorf("invoice_requires_successful_payment: got = false, want = true")
			}
			if ts := transactions(h, w.ID); len(ts) != 1 || ts[0].Status != wallet.SettlementStatusPending {
				t.Errorf("purchase: got = %+v, want one pending", ts)
			}
			if w.BalanceAmount != 0 {
				t.Errorf("balance: got = %d, want = 0", w.BalanceAmount)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:331
		{"customer without currency", func(h *billingtest.Harness, c *customer.Customer, _ *wallets.CreateInput) {
			h.UpdateCustomer(c, func(c *customer.Customer) { c.Currency = money.Currency{} })
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			if got := h.ReloadCustomer(w.CustomerID).Currency; got != eur {
				t.Errorf("customer currency: got = %s, want = EUR", got.Code())
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:421
		{"currency falls back to the customer's", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			h.UpdateCustomer(c, func(c *customer.Customer) { c.Currency = usd })
			in.Currency = money.Currency{}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if w.Currency != usd {
				t.Errorf("currency: got = %s, want = USD", w.Currency.Code())
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:445
		{"transaction metadata and name", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.TransactionMetadata = core.Metadata{{Key: "k", Value: "v"}}
			in.TransactionName = ptr("Initial")
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			ts := transactions(h, w.ID)
			if len(ts) != 1 || *ts[0].Name != "Initial" || len(ts[0].Metadata) != 1 {
				t.Errorf("transaction: got = %+v", ts)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:522
		{"target rule", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RateAmount, in.PaidTopUpMinAmount, in.PaidTopUpMaxAmount = money.One, nil, ptr[int64](5000)
			in.RecurringTransactionRules = []wallets.RuleInput{{
				Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, Method: wallet.RecurringMethodTarget,
				PaidCredits: dec("10.0"), GrantedCredits: dec("5.0"), TargetOngoingBalance: dec("100.0"),
			}}
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			if got := len(rules(h, w.ID)); got != 1 {
				t.Errorf("rules: got = %d, want = 1", got)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:541
		{"fixed rule above limit ignoring limits", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RateAmount, in.PaidTopUpMinAmount, in.PaidTopUpMaxAmount = money.One, nil, ptr[int64](5000)
			in.RecurringTransactionRules = []wallets.RuleInput{{
				Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, PaidCredits: dec("100.0"), IgnorePaidTopUpLimits: ptr(true),
			}}
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			if got := len(rules(h, w.ID)); got != 1 {
				t.Errorf("rules: got = %d, want = 1", got)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:553
		{"fixed rule granting only", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RateAmount, in.PaidTopUpMinAmount, in.PaidTopUpMaxAmount = money.One, nil, ptr[int64](5000)
			in.RecurringTransactionRules = []wallets.RuleInput{{
				Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, PaidCredits: dec("0"), GrantedCredits: dec("50.0"),
			}}
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			if got := len(rules(h, w.ID)); got != 1 {
				t.Errorf("rules: got = %d, want = 1", got)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:564
		{"limit checked in the customer currency", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			h.UpdateCustomer(c, func(c *customer.Customer) { c.Currency = money.MustCurrency("JPY") })
			in.Currency, in.RateAmount, in.PaidTopUpMinAmount, in.PaidTopUpMaxAmount = money.Currency{}, money.One, nil, ptr[int64](5000)
			in.RecurringTransactionRules = []wallets.RuleInput{{Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, PaidCredits: dec("200.0")}}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if w.Currency.Code() != "JPY" {
				t.Errorf("currency: got = %s, want = JPY", w.Currency.Code())
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:634
		{"rule transaction name", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.RecurringTransactionRules = []wallets.RuleInput{{Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, TransactionName: ptr("Custom Top-up")}}
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			if rs := rules(h, w.ID); len(rs) != 1 || *rs[0].TransactionName != "Custom Top-up" {
				t.Errorf("rules: got = %+v", rs)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:748
		{"fee type limitations", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.AllowedFeeTypes = []core.FeeType{core.FeeTypeCharge}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if !slices.Equal(w.AllowedFeeTypes, []core.FeeType{core.FeeTypeCharge}) {
				t.Errorf("allowed fee types: got = %v, want = [charge]", w.AllowedFeeTypes)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:785
		{"billable metric limitations", func(h *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.BillableMetricIDs = []core.BillableMetricID{h.Metric("m", catalog.AggregationTypeCount, "").ID}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if len(w.BillableMetricIDs) != 1 {
				t.Errorf("billable metrics: got = %v, want one", w.BillableMetricIDs)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:850
		{"payment method", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider, ID: paymentMethod(h, c).ID}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if w.PaymentMethodID.IsZero() || w.PaymentMethodType != core.PaymentMethodTypeProvider {
				t.Errorf("payment method: got = %s %s", w.PaymentMethodType, w.PaymentMethodID)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:860
		{"payment method without id", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if !w.PaymentMethodID.IsZero() || w.PaymentMethodType != core.PaymentMethodTypeProvider {
				t.Errorf("payment method: got = %s %s", w.PaymentMethodType, w.PaymentMethodID)
			}
		}},
		// source: spec/services/wallets/validate_service_spec.rb:343
		{"manual payment method", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.PaymentMethod = &wallets.PaymentMethod{Type: core.PaymentMethodTypeManual}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if w.PaymentMethodType != core.PaymentMethodTypeManual {
				t.Errorf("payment method type: got = %s, want = manual", w.PaymentMethodType)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:1013
		{"code provided", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.Code = ptr("custom_code")
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if *w.Code != "custom_code" {
				t.Errorf("code: got = %s, want = custom_code", *w.Code)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:1044
		{"code of a terminated wallet", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			h.Wallet(c, "0", func(w *wallet.Wallet) {
				w.Currency = eur
				w.Code = ptr("existing_code")
				w.Status = wallet.StatusTerminated
			})
			in.Code = ptr("existing_code")
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if *w.Code != "existing_code" {
				t.Errorf("code: got = %s, want = existing_code", *w.Code)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:1071
		{"code from name", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.Name = ptr("My Premium Wallet")
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if *w.Code != "my_premium_wallet" {
				t.Errorf("code: got = %s, want = my_premium_wallet", *w.Code)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:1096
		{"code from a taken name", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			h.Wallet(c, "0", func(w *wallet.Wallet) { w.Currency = eur; w.Code = ptr("existing_name") })
			in.Name = ptr("Existing Name")
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if want := "existing_name_1709632800"; *w.Code != want {
				t.Errorf("code: got = %s, want = %s", *w.Code, want)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:1104
		{"code from a name taken by a terminated wallet", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			h.Wallet(c, "0", func(w *wallet.Wallet) {
				w.Currency = eur
				w.Code = ptr("existing_name")
				w.Status = wallet.StatusTerminated
			})
			in.Name = ptr("Existing Name")
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if *w.Code != "existing_name" {
				t.Errorf("code: got = %s, want = existing_name", *w.Code)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:1304
		{"default code", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.Name = nil
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if *w.Code != "default" {
				t.Errorf("code: got = %s, want = default", *w.Code)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:1312
		{"default code taken", func(h *billingtest.Harness, c *customer.Customer, in *wallets.CreateInput) {
			h.Wallet(c, "0", func(w *wallet.Wallet) { w.Currency = eur; w.Code = ptr("default") })
			in.Name = nil
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if want := "default_1709632800"; *w.Code != want {
				t.Errorf("code: got = %s, want = %s", *w.Code, want)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:1157
		{"billing entity", func(h *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.BillingEntityID = h.BillingEntity.ID
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			if w.BillingEntityID != h.BillingEntity.ID {
				t.Errorf("billing entity: got = %s, want = %s", w.BillingEntityID, h.BillingEntity.ID)
			}
		}},
		// source: spec/services/wallets/create_service_spec.rb:1166
		{"no billing entity", func(_ *billingtest.Harness, _ *customer.Customer, _ *wallets.CreateInput) {}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if !w.BillingEntityID.IsZero() {
				t.Errorf("billing entity: got = %s, want none", w.BillingEntityID)
			}
		}},
		// source: spec/services/wallets/validate_service_spec.rb:262
		{"one below the wallet limit", func(h *billingtest.Harness, c *customer.Customer, _ *wallets.CreateInput) {
			for range organizationLimit - 1 {
				h.Wallet(c, "0", func(w *wallet.Wallet) { w.Currency = eur })
			}
		}, func(*testing.T, *billingtest.Harness, *wallet.Wallet) {}},
		// source: spec/services/wallets/validate_service_spec.rb:131
		{"expiration in the future", func(_ *billingtest.Harness, _ *customer.Customer, in *wallets.CreateInput) {
			in.ExpirationAt = ptr(t0.Add(time.Hour))
		}, func(*testing.T, *billingtest.Harness, *wallet.Wallet) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			c := eurCustomer(h)
			in := createInput(c)
			tt.edit(h, c, &in)
			w, err := s.Create(h.Ctx, h.OrgID(), in)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			tt.check(t, h, w)
		})
	}
}

func TestCreateWalletLimitFromOrganization(t *testing.T) {
	// source: spec/services/wallets/validate_service_spec.rb:284 (the
	// organization's limit applies; no premium gate)
	h, s := setup(t, t0)
	h.Org.MaxWalletsPerCustomer = organizationLimit + 2
	h.Do(func(ctx context.Context, r port.Repos) error { return r.Organizations.Update(ctx, h.Org) })
	c := eurCustomer(h)
	for range organizationLimit + 1 {
		h.Wallet(c, "0", func(w *wallet.Wallet) { w.Currency = eur })
	}
	if _, err := s.Create(h.Ctx, h.OrgID(), createInput(c)); err != nil {
		t.Errorf("create: got = %v, want = nil", err)
	}
	_, err := s.Create(h.Ctx, h.OrgID(), createInput(c))
	wantCodes(t, err, "customer_id/wallets_limit_reached")
}

func TestUpdate(t *testing.T) {
	// source: spec/services/wallets/update_service_spec.rb:34
	h, s := setup(t, t0)
	c := h.Customer()
	w := h.Wallet(c, "10", func(w *wallet.Wallet) { w.Code = ptr("old") })
	exp := t0.AddDate(1, 0, 0)
	got, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{
		Name:                             wallets.To(ptr("new name")),
		Priority:                         ptr(5),
		ExpirationAt:                     wallets.To(&exp),
		InvoiceRequiresSuccessfulPayment: ptr(true),
		PaidTopUpMinAmount:               wallets.To(ptr[int64](1_00)),
		PaidTopUpMaxAmount:               wallets.To(ptr[int64](1_000_00)),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if *got.Name != "new name" || got.Priority != 5 || !got.ExpirationAt.Equal(exp) || !got.InvoiceRequiresSuccessfulPayment ||
		*got.PaidTopUpMinAmount != 1_00 || *got.PaidTopUpMaxAmount != 1_000_00 {
		t.Errorf("wallet: got = %+v", got)
	}
	// source: spec/services/wallets/update_service_spec.rb:69
	if got := h.Webhooks(); !slices.Equal(got, []webhook.EventType{webhook.EventTypeWalletUpdated}) {
		t.Errorf("webhooks: got = %v, want = [wallet.updated]", got)
	}
}

func TestUpdateRefresh(t *testing.T) {
	tests := []struct {
		name string
		in   func(*billingtest.Harness) wallets.UpdateInput
		want bool
	}{
		// source: spec/services/wallets/update_service_spec.rb:95
		{"code", func(*billingtest.Harness) wallets.UpdateInput { return wallets.UpdateInput{Code: ptr("new_code")} }, true},
		// source: spec/services/wallets/update_service_spec.rb:101
		{"priority", func(*billingtest.Harness) wallets.UpdateInput { return wallets.UpdateInput{Priority: ptr(49)} }, true},
		// source: spec/services/wallets/update_service_spec.rb:107
		{"allowed fee types", func(*billingtest.Harness) wallets.UpdateInput {
			return wallets.UpdateInput{AllowedFeeTypes: wallets.To([]core.FeeType{core.FeeTypeCharge})}
		}, true},
		// source: spec/services/wallets/update_service_spec.rb:113
		{"metric added", func(h *billingtest.Harness) wallets.UpdateInput {
			return wallets.UpdateInput{BillableMetricIDs: wallets.To([]core.BillableMetricID{h.Metric("m", catalog.AggregationTypeCount, "").ID})}
		}, true},
		// source: spec/services/wallets/update_service_spec.rb:146
		{"name", func(*billingtest.Harness) wallets.UpdateInput {
			return wallets.UpdateInput{Name: wallets.To(ptr("new name"))}
		}, false},
		// source: spec/services/wallets/update_service_spec.rb:152
		{"expiration", func(*billingtest.Harness) wallets.UpdateInput {
			return wallets.UpdateInput{ExpirationAt: wallets.To(ptr(t0.AddDate(1, 0, 0)))}
		}, false},
		// source: spec/services/wallets/update_service_spec.rb:158
		{"paid top-up limits", func(*billingtest.Harness) wallets.UpdateInput {
			return wallets.UpdateInput{PaidTopUpMinAmount: wallets.To(ptr[int64](100)), PaidTopUpMaxAmount: wallets.To(ptr[int64](10000))}
		}, false},
		// source: spec/services/wallets/update_service_spec.rb:184
		{"rules", func(*billingtest.Harness) wallets.UpdateInput {
			return wallets.UpdateInput{RecurringTransactionRules: wallets.To([]wallets.RuleInput{{Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly}})}
		}, false},
		// source: spec/services/wallets/update_service_spec.rb:203
		{"same code", func(*billingtest.Harness) wallets.UpdateInput {
			return wallets.UpdateInput{Code: ptr("old"), Name: wallets.To(ptr("new name"))}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.Code = ptr("old") })
			if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, tt.in(h)); err != nil {
				t.Fatalf("update: %v", err)
			}
			if got := slices.Contains(h.JobKinds(), kernel.JobRefreshWalletOngoingBalance); got != tt.want {
				t.Errorf("refresh enqueued: got = %t, want = %t", got, tt.want)
			}
		})
	}
}

func TestUpdateErrors(t *testing.T) {
	tests := []struct {
		name   string
		wallet func(*wallet.Wallet)
		in     func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput
		errors []string
	}{
		// source: spec/services/wallets/update_service_spec.rb:221
		{"terminated", func(w *wallet.Wallet) { w.Status = wallet.StatusTerminated }, func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{Name: wallets.To(ptr("x"))}
		}, []string{"wallet_id/wallet_is_terminated"}},
		// source: spec/services/wallets/update_service_spec.rb:234
		{"invalid priority", nil, func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{Priority: ptr(0)}
		}, []string{"priority/" + core.CodeOutOfRange}},
		// source: spec/services/wallets/update_service_spec.rb:266
		{"expiration in the past", nil, func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{ExpirationAt: wallets.To(ptr(t0.Add(-time.Hour)))}
		}, []string{"expiration_at/invalid_date"}},
		// source: spec/services/wallets/update_service_spec.rb:461
		{"two rules", nil, func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			r := wallets.RuleInput{Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly}
			return wallets.UpdateInput{RecurringTransactionRules: wallets.To([]wallets.RuleInput{r, r})}
		}, []string{"recurring_transaction_rules/invalid_number_of_recurring_rules"}},
		// source: spec/services/wallets/update_service_spec.rb:487
		{"invalid trigger", nil, func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{RecurringTransactionRules: wallets.To([]wallets.RuleInput{{Trigger: "invalid"}})}
		}, []string{"recurring_transaction_rules/invalid_recurring_rule"}},
		// source: spec/services/wallets/update_service_spec.rb:507
		{"invalid threshold", nil, func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{RecurringTransactionRules: wallets.To([]wallets.RuleInput{{Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: dec("-1")}})}
		}, []string{"recurring_transaction_rules/invalid_recurring_rule"}},
		// source: spec/services/wallets/update_service_spec.rb:578
		{"rule paid credits above limits", func(w *wallet.Wallet) { w.PaidTopUpMaxAmount = ptr[int64](1_00) }, func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{RecurringTransactionRules: wallets.To([]wallets.RuleInput{{
				Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly, PaidCredits: dec("1000"), GrantedCredits: dec("105"),
			}})}
		}, []string{"recurring_transaction_rules/invalid_recurring_rule"}},
		// source: spec/services/wallets/update_service_spec.rb:614
		{"existing rule below the new minimum", nil, func(h *billingtest.Harness, w *wallet.Wallet) wallets.UpdateInput {
			addRule(h, w, func(r *wallet.RecurringTransactionRule) { r.PaidCredits = money.One })
			return wallets.UpdateInput{Name: wallets.To(ptr("new name")), PaidTopUpMinAmount: wallets.To(ptr[int64](1000))}
		}, []string{"recurring_transaction_rules/invalid_recurring_rule"}},
		// source: spec/services/wallets/update_service_spec.rb:700
		{"invalid fee type", nil, func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{AllowedFeeTypes: wallets.To([]core.FeeType{"invalid"})}
		}, []string{"allowed_fee_types/invalid_fee_types", "applies_to/invalid_limitations"}},
		// source: spec/services/wallets/update_service_spec.rb:749
		{"invalid billable metric", nil, func(h *billingtest.Harness, _ *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{BillableMetricIDs: wallets.To([]core.BillableMetricID{core.NewID[core.BillableMetricID](h.IDs)})}
		}, []string{"billable_metrics/invalid_identifier", "applies_to/invalid_limitations"}},
		// source: spec/services/wallets/update_service_spec.rb:863
		{"invalid payment method type", nil, func(h *billingtest.Harness, w *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{PaymentMethod: &wallets.PaymentMethod{Type: "invalid", ID: core.NewID[core.PaymentMethodID](h.IDs)}}
		}, []string{"payment_method/invalid_payment_method"}},
		// source: spec/services/wallets/update_service_spec.rb:877
		{"unknown payment method", nil, func(h *billingtest.Harness, _ *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{PaymentMethod: &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider, ID: core.NewID[core.PaymentMethodID](h.IDs)}}
		}, []string{"payment_method/invalid_payment_method"}},
		// source: spec/services/wallets/update_service_spec.rb:950
		{"code taken", nil, func(h *billingtest.Harness, w *wallet.Wallet) wallets.UpdateInput {
			h.Wallet(h.ReloadCustomer(w.CustomerID), "0", func(o *wallet.Wallet) { o.Code = ptr("taken") })
			return wallets.UpdateInput{Code: ptr("taken")}
		}, []string{"code/" + core.CodeAlreadyExists}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) {
				w.Code = ptr("old")
				if tt.wallet != nil {
					tt.wallet(w)
				}
			})
			in := tt.in(h, w)
			_, err := s.Update(h.Ctx, h.OrgID(), w.ID, in)
			wantCodes(t, err, tt.errors...)
			if got := h.Webhooks(); len(got) != 0 {
				t.Errorf("webhooks: got = %v, want none", got)
			}
		})
	}
}

func TestUpdateRules(t *testing.T) {
	weekly := func(id core.RecurringTransactionRuleID) wallets.RuleInput {
		return wallets.RuleInput{ID: id, Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly, PaidCredits: dec("105"), GrantedCredits: dec("105")}
	}
	tests := []struct {
		name                 string
		in                   func(old *wallet.RecurringTransactionRule) []wallets.RuleInput
		active, terminated   int
		sameID               bool
		check                func(*testing.T, *wallet.RecurringTransactionRule)
		walletPaidTopUpLimit *int64
	}{
		// source: spec/services/wallets/update_service_spec.rb:303
		{name: "replaces the rule", in: func(*wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(core.RecurringTransactionRuleID{})}
		}, active: 1, terminated: 1, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.Interval != period.Weekly {
				t.Errorf("interval: got = %v, want = weekly", r.Interval)
			}
			wantDec(t, "paid", r.PaidCredits, "105")
			wantDec(t, "granted", r.GrantedCredits, "105")
		}},
		// source: spec/services/wallets/update_service_spec.rb:320
		{name: "edits the rule", in: func(old *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{weekly(old.ID)}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.Interval != period.Weekly {
				t.Errorf("interval: got = %v, want = weekly", r.Interval)
			}
			wantDec(t, "paid", r.PaidCredits, "105")
		}},
		// source: spec/services/wallets/update_service_spec.rb:417
		{name: "into a threshold rule", in: func(old *wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{{ID: old.ID, Trigger: wallet.RecurringTriggerThreshold, ThresholdCredits: dec("205"), PaidCredits: dec("105"), GrantedCredits: dec("105")}}
		}, active: 1, sameID: true, check: func(t *testing.T, r *wallet.RecurringTransactionRule) {
			if r.Trigger != wallet.RecurringTriggerThreshold {
				t.Errorf("trigger: got = %s, want = threshold", r.Trigger)
			}
			wantDec(t, "threshold", *r.ThresholdCredits, "205")
		}},
		// source: spec/services/wallets/update_service_spec.rb:448
		{name: "empty list", in: func(*wallet.RecurringTransactionRule) []wallets.RuleInput { return []wallets.RuleInput{} }, terminated: 1},
		// source: spec/services/wallets/update_service_spec.rb:560
		{name: "target rule ignores limits", walletPaidTopUpLimit: ptr[int64](1_00), in: func(*wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{{Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly, Method: wallet.RecurringMethodTarget, PaidCredits: dec("10"), TargetOngoingBalance: dec("5")}}
		}, active: 1, terminated: 1},
		// source: spec/services/wallets/update_service_spec.rb:569
		{name: "paid credits floor to zero", walletPaidTopUpLimit: ptr[int64](1_00), in: func(*wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{{Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly, PaidCredits: dec("0.000005"), GrantedCredits: dec("105")}}
		}, active: 1, terminated: 1},
		// source: spec/services/wallets/update_service_spec.rb:590
		{name: "above limits ignoring them", walletPaidTopUpLimit: ptr[int64](1_00), in: func(*wallet.RecurringTransactionRule) []wallets.RuleInput {
			return []wallets.RuleInput{{Trigger: wallet.RecurringTriggerInterval, Interval: period.Weekly, PaidCredits: dec("1000"), IgnorePaidTopUpLimits: ptr(true)}}
		}, active: 1, terminated: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.PaidTopUpMaxAmount = tt.walletPaidTopUpLimit })
			old := addRule(h, w, func(r *wallet.RecurringTransactionRule) { r.PaidCredits = money.Zero })
			if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{RecurringTransactionRules: wallets.To(tt.in(old))}); err != nil {
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

func TestUpdateLimitations(t *testing.T) {
	h, s := setup(t, t0)
	m := h.Metric("m", catalog.AggregationTypeCount, "")
	w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.BillableMetricIDs = []core.BillableMetricID{m.ID} })
	// source: spec/services/wallets/update_service_spec.rb:793
	got, err := s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{Name: wallets.To(ptr("new name"))})
	if err != nil || len(got.BillableMetricIDs) != 1 {
		t.Fatalf("omitted limitations: got = %v %v, want the metric kept", got.BillableMetricIDs, err)
	}
	// source: spec/services/wallets/update_service_spec.rb:678
	got, _ = s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{AllowedFeeTypes: wallets.To([]core.FeeType{core.FeeTypeCharge})})
	if !slices.Equal(got.AllowedFeeTypes, []core.FeeType{core.FeeTypeCharge}) || len(got.BillableMetricIDs) != 1 {
		t.Errorf("fee types: got = %v %v, want = [charge] and the metric kept", got.AllowedFeeTypes, got.BillableMetricIDs)
	}
	// source: spec/services/wallets/update_service_spec.rb:685
	got, _ = s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{AllowedFeeTypes: wallets.To([]core.FeeType{})})
	if len(got.AllowedFeeTypes) != 0 {
		t.Errorf("fee types: got = %v, want none", got.AllowedFeeTypes)
	}
	// source: spec/services/wallets/update_service_spec.rb:763
	got, _ = s.Update(h.Ctx, h.OrgID(), w.ID, wallets.UpdateInput{BillableMetricIDs: wallets.To([]core.BillableMetricID{})})
	if len(got.BillableMetricIDs) != 0 {
		t.Errorf("metrics: got = %v, want none", got.BillableMetricIDs)
	}
}

func TestUpdateAttributes(t *testing.T) {
	tests := []struct {
		name  string
		in    func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput
		check func(*testing.T, *billingtest.Harness, *wallet.Wallet)
	}{
		// source: spec/services/wallets/update_service_spec.rb:833
		{"attaches a payment method", func(h *billingtest.Harness, w *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{PaymentMethod: &wallets.PaymentMethod{Type: core.PaymentMethodTypeProvider, ID: paymentMethod(h, h.ReloadCustomer(w.CustomerID)).ID}}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if w.PaymentMethodID.IsZero() {
				t.Errorf("payment method: got none, want one")
			}
		}},
		// source: spec/services/wallets/update_service_spec.rb:854
		{"removes the payment method", func(h *billingtest.Harness, w *wallet.Wallet) wallets.UpdateInput {
			w.PaymentMethodID = paymentMethod(h, h.ReloadCustomer(w.CustomerID)).ID
			h.Do(func(ctx context.Context, r port.Repos) error { return r.Wallets.Update(ctx, w) })
			return wallets.UpdateInput{PaymentMethod: &wallets.PaymentMethod{Type: core.PaymentMethodTypeManual}}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if !w.PaymentMethodID.IsZero() || w.PaymentMethodType != core.PaymentMethodTypeManual {
				t.Errorf("payment method: got = %s %s, want manual without id", w.PaymentMethodType, w.PaymentMethodID)
			}
		}},
		// source: spec/services/wallets/update_service_spec.rb:901
		{"updates the code", func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{Code: ptr("new_code")}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if *w.Code != "new_code" {
				t.Errorf("code: got = %s, want = new_code", *w.Code)
			}
		}},
		// source: spec/services/wallets/update_service_spec.rb:917
		{"keeps the code", func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{Name: wallets.To(ptr("n"))}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if *w.Code != "old" {
				t.Errorf("code: got = %s, want = old", *w.Code)
			}
		}},
		// source: spec/services/wallets/update_service_spec.rb:937
		{"clears optional values", func(*billingtest.Harness, *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{
				Name: wallets.To[*string](nil), ExpirationAt: wallets.To[*time.Time](nil),
				PaidTopUpMinAmount: wallets.To[*int64](nil), PaidTopUpMaxAmount: wallets.To[*int64](nil),
			}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if w.Name != nil || w.ExpirationAt != nil || w.PaidTopUpMinAmount != nil || w.PaidTopUpMaxAmount != nil {
				t.Errorf("wallet: got = %+v, want cleared values", w)
			}
		}},
		// source: spec/services/wallets/update_service_spec.rb:1053
		{"assigns a billing entity", func(h *billingtest.Harness, _ *wallet.Wallet) wallets.UpdateInput {
			return wallets.UpdateInput{BillingEntityID: wallets.To(h.BillingEntity.ID)}
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			if w.BillingEntityID != h.BillingEntity.ID {
				t.Errorf("billing entity: got = %s, want = %s", w.BillingEntityID, h.BillingEntity.ID)
			}
		}},
		// source: spec/services/wallets/update_service_spec.rb:1177
		{"clears the billing entity", func(h *billingtest.Harness, w *wallet.Wallet) wallets.UpdateInput {
			w.BillingEntityID = h.BillingEntity.ID
			h.Do(func(ctx context.Context, r port.Repos) error { return r.Wallets.Update(ctx, w) })
			return wallets.UpdateInput{BillingEntityID: wallets.To(core.BillingEntityID{})}
		}, func(t *testing.T, _ *billingtest.Harness, w *wallet.Wallet) {
			if !w.BillingEntityID.IsZero() {
				t.Errorf("billing entity: got = %s, want none", w.BillingEntityID)
			}
		}},
		// source: spec/services/wallets/update_service_spec.rb:1062
		{"keeps the billing entity", func(h *billingtest.Harness, w *wallet.Wallet) wallets.UpdateInput {
			w.BillingEntityID = h.BillingEntity.ID
			h.Do(func(ctx context.Context, r port.Repos) error { return r.Wallets.Update(ctx, w) })
			return wallets.UpdateInput{Name: wallets.To(ptr("new name"))}
		}, func(t *testing.T, h *billingtest.Harness, w *wallet.Wallet) {
			if w.BillingEntityID != h.BillingEntity.ID {
				t.Errorf("billing entity: got = %s, want = %s", w.BillingEntityID, h.BillingEntity.ID)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.Code = ptr("old") })
			in := tt.in(h, w)
			if _, err := s.Update(h.Ctx, h.OrgID(), w.ID, in); err != nil {
				t.Fatalf("update: %v", err)
			}
			tt.check(t, h, h.ReloadWallet(w.ID))
		})
	}
}

func TestTerminate(t *testing.T) {
	// source: spec/services/wallets/terminate_service_spec.rb:20
	h, s := setup(t, t0)
	w := h.Wallet(h.Customer(), "10")
	addRule(h, w)
	addRule(h, w)
	got, err := s.Terminate(h.Ctx, h.OrgID(), w.ID)
	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if got.Status != wallet.StatusTerminated || got.TerminatedAt == nil {
		t.Errorf("wallet: got = %s, want = terminated", got.Status)
	}
	// source: spec/services/wallets/terminate_service_spec.rb:27
	if got := h.Webhooks(); !slices.Equal(got, []webhook.EventType{webhook.EventTypeWalletTerminated}) {
		t.Errorf("webhooks: got = %v, want = [wallet.terminated]", got)
	}
	// source: spec/services/wallets/terminate_service_spec.rb:56
	if rs := rules(h, w.ID); len(rs) != 2 || len(activeRules(h, w.ID)) != 0 {
		t.Errorf("rules: got = %d, %d active, want = 2, 0 active", len(rs), len(activeRules(h, w.ID)))
	}

	// source: spec/services/wallets/terminate_service_spec.rb:65
	h.Clock.Advance(time.Hour)
	at := *got.TerminatedAt
	again, err := s.Terminate(h.Ctx, h.OrgID(), w.ID)
	if err != nil || !again.TerminatedAt.Equal(at) {
		t.Errorf("terminate again: got = %v %v, want = %s", again.TerminatedAt, err, at)
	}
	// source: spec/services/wallets/terminate_service_spec.rb:78
	if got := countWebhooks(h, webhook.EventTypeWalletTerminated); got != 1 {
		t.Errorf("wallet.terminated: got = %d, want = 1", got)
	}
	// source: spec/services/wallets/terminate_service_spec.rb:39,81
	if awaitingRefresh(h, w.CustomerID) {
		t.Errorf("awaiting_wallet_refresh after terminating the last wallet: got = true, want = false")
	}
}

func TestTerminateFlagsRefresh(t *testing.T) {
	// source: spec/services/wallets/terminate_service_spec.rb:34
	h, s := setup(t, t0)
	c := h.Customer()
	w := h.Wallet(c, "10")
	h.Wallet(c, "5")
	if awaitingRefresh(h, c.ID) {
		t.Fatalf("awaiting_wallet_refresh before: got = true, want = false")
	}
	if _, err := s.Terminate(h.Ctx, h.OrgID(), w.ID); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if !awaitingRefresh(h, c.ID) {
		t.Errorf("awaiting_wallet_refresh: got = false, want = true")
	}
}

// awaitingRefresh reports whether customer c awaits the refresh of its
// wallets' ongoing balances (lago customer.awaiting_wallet_refresh).
func awaitingRefresh(h *billingtest.Harness, c core.CustomerID) bool {
	h.T.Helper()
	got := false
	h.Do(func(ctx context.Context, r port.Repos) error {
		p, err := r.Customers.ListAwaitingWalletRefresh(ctx, h.OrgID(), core.PageRequest{Size: core.MaxPageSize})
		got = slices.Contains(p.Items, c)
		return err
	})
	return got
}

func TestValidateLimitations(t *testing.T) {
	tests := []struct {
		name    string
		types   []core.FeeType
		metrics func(*billingtest.Harness) []core.BillableMetricID
		errors  []string
	}{
		// source: spec/services/wallets/validate_limitations_service_spec.rb:18
		{"none", nil, nil, nil},
		// source: spec/services/wallets/validate_limitations_service_spec.rb:28
		{"wrong fee type", []core.FeeType{"invalid_fee_type", core.FeeTypeCharge}, nil, []string{"allowed_fee_types/invalid_fee_types", "applies_to/invalid_limitations"}},
		// source: spec/services/wallets/validate_limitations_service_spec.rb:42
		{"unknown metric", nil, func(h *billingtest.Harness) []core.BillableMetricID {
			return []core.BillableMetricID{h.Metric("m", catalog.AggregationTypeCount, "").ID, core.NewID[core.BillableMetricID](h.IDs)}
		}, []string{"billable_metrics/invalid_identifier", "applies_to/invalid_limitations"}},
		// source: spec/services/wallets/validate_limitations_service_spec.rb:61
		{"valid", []core.FeeType{core.FeeTypeCharge, core.FeeTypeSubscription}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s := setup(t, t0)
			in := createInput(eurCustomer(h))
			in.AllowedFeeTypes = tt.types
			if tt.metrics != nil {
				in.BillableMetricIDs = tt.metrics(h)
			}
			_, err := s.Create(h.Ctx, h.OrgID(), in)
			wantCodes(t, err, tt.errors...)
		})
	}
}

func TestCreatePurchaseOrderNumber(t *testing.T) {
	tests := []struct {
		name, source string
		rulePO       *string
		wantRule     *string
		wantTopUpPO  string
	}{
		{"rule and wallet", "spec/services/wallets/create_service_spec.rb:578", ptr("PO-RULE"), ptr("PO-RULE"), "PO-RULE"},
		{"blank rule", "spec/services/wallets/create_service_spec.rb:608", ptr("   "), nil, "PO-WALLET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, s := setup(t, t0)
			in := createInput(eurCustomer(h))
			in.PurchaseOrderNumber = ptr("PO-WALLET")
			in.RecurringTransactionRules = []wallets.RuleInput{{
				Trigger: wallet.RecurringTriggerInterval, Interval: period.Monthly, Method: wallet.RecurringMethodTarget,
				PaidCredits: dec("10.0"), GrantedCredits: dec("5.0"), TargetOngoingBalance: dec("100.0"),
				PurchaseOrderNumber: tt.rulePO,
			}}
			w, err := s.Create(h.Ctx, h.OrgID(), in)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if got := h.ReloadWallet(w.ID).PurchaseOrderNumber; got == nil || *got != "PO-WALLET" {
				t.Errorf("wallet purchase_order_number: got = %v, want = PO-WALLET", got)
			}
			if got := rules(h, w.ID)[0].PurchaseOrderNumber; (got == nil) != (tt.wantRule == nil) || (got != nil && *got != *tt.wantRule) {
				t.Errorf("rule purchase_order_number: got = %v, want = %v", got, tt.wantRule)
			}
			ts := transactions(h, w.ID)
			if len(ts) == 0 {
				t.Fatal("transactions: got = 0, want the initial top-up")
			}
			for _, tr := range ts {
				if tr.PurchaseOrderNumber == nil || *tr.PurchaseOrderNumber != tt.wantTopUpPO {
					t.Errorf("top-up purchase_order_number: got = %v, want = %s", tr.PurchaseOrderNumber, tt.wantTopUpPO)
				}
			}
		})
	}
}

func TestUpdatePurchaseOrderNumber(t *testing.T) {
	tests := []struct {
		name, source string
		in           wallets.UpdateInput
		want         *string
		errors       []string
	}{
		{"set", "spec/services/wallets/update_service_spec.rb:47", wallets.UpdateInput{PurchaseOrderNumber: wallets.To(ptr("PO-WALLET-123"))}, ptr("PO-WALLET-123"), nil},
		{"omitted keeps it", "tanvo", wallets.UpdateInput{Name: wallets.To(ptr("x"))}, ptr("PO-OLD"), nil},
		{"blank clears it", "tanvo", wallets.UpdateInput{PurchaseOrderNumber: wallets.To(ptr("  "))}, nil, nil},
		{"too long", "spec/services/wallets/update_service_spec.rb:58", wallets.UpdateInput{PurchaseOrderNumber: wallets.To(ptr(strings.Repeat("a", 256)))}, ptr("PO-OLD"), []string{"purchase_order_number/value_is_too_long"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("source: %s", tt.source)
			h, s := setup(t, t0)
			w := h.Wallet(h.Customer(), "10", func(w *wallet.Wallet) { w.PurchaseOrderNumber = ptr("PO-OLD") })
			_, err := s.Update(h.Ctx, h.OrgID(), w.ID, tt.in)
			if tt.errors != nil {
				wantCodes(t, err, tt.errors...)
			} else if err != nil {
				t.Fatalf("update: %v", err)
			}
			got := h.ReloadWallet(w.ID).PurchaseOrderNumber
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("purchase_order_number: got = %v, want = %v", got, tt.want)
			}
		})
	}
}
