package invoicing_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// wantRule fails unless err is a rule error with the code.
func wantRule(t *testing.T, err error, code string) {
	t.Helper()
	var re *core.RuleError
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("error got = %v, want = rule %s", err, code)
	}
}

// wantField fails unless err is a validation error of the field and code.
func wantField(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) || !ve.Has(field, code) {
		t.Fatalf("error got = %v, want = validation %s: %s", err, field, code)
	}
}

// wantNotFound fails unless err is a not found error of the resource.
func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error got = %v, want = %s_not_found", err, resource)
	}
}

// unattachedFee stores a paid in advance fee of s attached to no invoice.
func (f *fixture) unattachedFee(s *subscription.Subscription, opts ...func(*invoice.Fee)) *invoice.Fee {
	f.T.Helper()
	fee := &invoice.Fee{
		ID: core.NewID[core.FeeID](f.IDs), OrganizationID: f.OrgID(), SubscriptionID: s.ID, Type: core.FeeTypeCharge,
		Amount: 100, PreciseAmount: money.NewFromInt(100), Units: money.One, Currency: f.plan.Currency,
		TaxesPreciseAmount: money.Zero, TaxesRate: money.Zero, TaxesBaseRate: money.One,
		PreciseCouponsAmount: money.Zero, PreciseCreditNotesAmount: money.Zero,
		PayInAdvance: true, Invoiceable: true, PaymentStatus: invoice.FeePaymentStatusPending,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	for _, o := range opts {
		o(fee)
	}
	f.Do(func(ctx context.Context, r port.Repos) error { return r.Fees.Create(ctx, fee) })
	return fee
}

// feeOf returns the fee of the invoice matching ok.
func (f *fixture) feeOf(inv core.InvoiceID, ok func(*invoice.Fee) bool) *invoice.Fee {
	f.T.Helper()
	for _, fee := range f.Fees(inv) {
		if ok(fee) {
			return fee
		}
	}
	f.T.Fatalf("no matching fee on invoice %s", inv)
	return nil
}

// chargeFeeOf returns the charge fee of the charge on the invoice.
func (f *fixture) chargeFeeOf(inv core.InvoiceID, c core.ChargeID) *invoice.Fee {
	f.T.Helper()
	return f.feeOf(inv, func(fee *invoice.Fee) bool { return fee.Type == core.FeeTypeCharge && fee.ChargeID == c })
}

// source: spec/services/fees/update_service_spec.rb
func TestUpdateFeePaymentStatus(t *testing.T) {
	for _, c := range []struct {
		name   string
		status invoice.FeePaymentStatus
		stamp  func(*invoice.Fee) *time.Time
	}{
		// source: spec/services/fees/update_service_spec.rb:16
		{"succeeded", invoice.FeePaymentStatusSucceeded, func(f *invoice.Fee) *time.Time { return f.SucceededAt }},
		// source: spec/services/fees/update_service_spec.rb:63
		{"failed", invoice.FeePaymentStatusFailed, func(f *invoice.Fee) *time.Time { return f.FailedAt }},
		// source: spec/services/fees/update_service_spec.rb:78
		{"refunded", invoice.FeePaymentStatusRefunded, func(f *invoice.Fee) *time.Time { return f.RefundedAt }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, billingDay)
			sub := f.subscribe()
			then := f.Now().Add(-time.Hour)
			fee := f.unattachedFee(sub, func(fee *invoice.Fee) { fee.SucceededAt, fee.FailedAt, fee.RefundedAt = &then, &then, &then })
			got, err := f.svc.UpdateFeePaymentStatus(f.Ctx, f.OrgID(), fee.ID, c.status)
			if err != nil {
				t.Fatalf("UpdateFeePaymentStatus() error got = %v, want = nil", err)
			}
			if got.Fee.PaymentStatus != c.status {
				t.Errorf("payment status got = %s, want = %s", got.Fee.PaymentStatus, c.status)
			}
			stamps := 0
			for _, s := range []*time.Time{got.Fee.SucceededAt, got.Fee.FailedAt, got.Fee.RefundedAt} {
				if s != nil {
					stamps++
				}
			}
			if s := c.stamp(got.Fee); s == nil || !s.Equal(f.Now()) || stamps != 1 {
				t.Errorf("stamps got = %v (%d set), want = only %s at %s", s, stamps, c.status, f.Now())
			}
			if got.CustomerID != f.cust.ID {
				t.Errorf("customer got = %s, want = %s", got.CustomerID, f.cust.ID)
			}
			stored, err := f.svc.GetFee(f.Ctx, f.OrgID(), fee.ID)
			if err != nil || stored.Fee.PaymentStatus != c.status {
				t.Errorf("GetFee() got = %v, %v, want = %s", stored.Fee, err, c.status)
			}
		})
	}

	f := newFixture(t, billingDay)
	sub := f.subscribe()
	fee := f.unattachedFee(sub)
	// source: spec/services/fees/update_service_spec.rb:27
	_, err := f.svc.UpdateFeePaymentStatus(f.Ctx, f.OrgID(), core.NewID[core.FeeID](f.IDs), invoice.FeePaymentStatusSucceeded)
	wantNotFound(t, err, "fee")
	// source: spec/services/fees/update_service_spec.rb:51
	_, err = f.svc.UpdateFeePaymentStatus(f.Ctx, f.OrgID(), fee.ID, "foo")
	wantField(t, err, "payment_status", core.CodeInvalid)
	// source: spec/services/fees/update_service_spec.rb:39
	inv := f.billOne(sub)
	_, err = f.svc.UpdateFeePaymentStatus(f.Ctx, f.OrgID(), f.Fees(inv.ID)[0].ID, invoice.FeePaymentStatusSucceeded)
	wantRule(t, err, invoicing.CodeInvoicedFee)
}

// source: spec/services/fees/destroy_service_spec.rb
func TestDeleteFee(t *testing.T) {
	f := newFixture(t, billingDay)
	sub := f.subscribe()
	fee := f.unattachedFee(sub)
	inv := f.billOne(sub)
	invoiced := f.Fees(inv.ID)[0]

	// source: spec/services/fees/destroy_service_spec.rb:9
	_, err := f.svc.DeleteFee(f.Ctx, f.OrgID(), core.NewID[core.FeeID](f.IDs))
	wantNotFound(t, err, "fee")
	// source: spec/services/fees/destroy_service_spec.rb:21-34
	_, err = f.svc.DeleteFee(f.Ctx, f.OrgID(), invoiced.ID)
	wantRule(t, err, invoicing.CodeInvoicedFee)
	if _, err := f.svc.GetFee(f.Ctx, f.OrgID(), invoiced.ID); err != nil {
		t.Errorf("GetFee() of the invoiced fee error got = %v, want = nil", err)
	}
	// source: spec/services/fees/destroy_service_spec.rb:37-49
	got, err := f.svc.DeleteFee(f.Ctx, f.OrgID(), fee.ID)
	if err != nil {
		t.Fatalf("DeleteFee() error got = %v, want = nil", err)
	}
	if got.Fee.ID != fee.ID || got.Fee.DeletedAt == nil || !got.Fee.DeletedAt.Equal(f.Now()) {
		t.Errorf("DeleteFee() got = fee %s deleted at %v, want = fee %s deleted at %v", got.Fee.ID, got.Fee.DeletedAt, fee.ID, f.Now())
	}
	// Fee's default scope skips discarded fees (app/models/fee.rb:9).
	_, err = f.svc.GetFee(f.Ctx, f.OrgID(), fee.ID)
	wantNotFound(t, err, "fee")
	_, err = f.svc.DeleteFee(f.Ctx, f.OrgID(), fee.ID)
	wantNotFound(t, err, "fee")
	_, err = f.svc.UpdateFeePaymentStatus(f.Ctx, f.OrgID(), fee.ID, invoice.FeePaymentStatusSucceeded)
	wantNotFound(t, err, "fee")
	page, err := f.svc.ListFees(f.Ctx, f.OrgID(), invoice.FeeFilter{SubscriptionID: sub.ID}, core.PageRequest{})
	if err != nil {
		t.Fatalf("ListFees() error got = %v, want = nil", err)
	}
	for _, fv := range page.Items {
		if fv.Fee.ID == fee.ID {
			t.Errorf("ListFees() got = discarded fee %s, want = none", fee.ID)
		}
	}
}

// source: spec/queries/fees_query_spec.rb:168
func TestListFees(t *testing.T) {
	f := newFixture(t, billingDay)
	sub := f.subscribe()
	a := f.unattachedFee(sub, func(fee *invoice.Fee) { fee.PayInAdvanceEventTransactionID = "transaction-id" })
	f.unattachedFee(sub, func(fee *invoice.Fee) { fee.PayInAdvanceEventTransactionID = "other" })
	b := f.unattachedFee(sub, func(fee *invoice.Fee) { fee.PayInAdvanceEventTransactionID = "transaction-id" })
	page, err := f.svc.ListFees(f.Ctx, f.OrgID(), invoice.FeeFilter{EventTransactionID: "transaction-id", CustomerExternalID: f.cust.ExternalID}, core.PageRequest{})
	if err != nil {
		t.Fatalf("ListFees() error got = %v, want = nil", err)
	}
	var got []core.FeeID
	for _, fv := range page.Items {
		got = append(got, fv.Fee.ID)
		if fv.CustomerID != f.cust.ID {
			t.Errorf("customer got = %s, want = %s", fv.CustomerID, f.cust.ID)
		}
	}
	if len(got) != 2 || got[0] != a.ID || got[1] != b.ID {
		t.Errorf("fees got = %v, want = [%s %s]", got, a.ID, b.ID)
	}
	// Oldest first, then by ID (app/queries/fees_query.rb:33), across pages.
	late := f.unattachedFee(sub, func(fee *invoice.Fee) {
		fee.PayInAdvanceEventTransactionID = "ordered"
		fee.CreatedAt = f.Now().Add(time.Hour)
	})
	early := f.unattachedFee(sub, func(fee *invoice.Fee) { fee.PayInAdvanceEventTransactionID = "ordered" })
	twin := f.unattachedFee(sub, func(fee *invoice.Fee) { fee.PayInAdvanceEventTransactionID = "ordered" })
	got = nil
	req := core.PageRequest{Size: 1}
	for {
		page, err := f.svc.ListFees(f.Ctx, f.OrgID(), invoice.FeeFilter{EventTransactionID: "ordered"}, req)
		if err != nil {
			t.Fatalf("ListFees() page error got = %v, want = nil", err)
		}
		for _, fv := range page.Items {
			got = append(got, fv.Fee.ID)
		}
		if page.NextToken == "" {
			break
		}
		req.Token = page.NextToken
	}
	if want := []core.FeeID{early.ID, twin.ID, late.ID}; !slices.Equal(got, want) {
		t.Errorf("ordered fees got = %v, want = %v", got, want)
	}
	// source: spec/queries/fees_query_spec.rb:132
	_, err = f.svc.ListFees(f.Ctx, f.OrgID(), invoice.FeeFilter{Types: []core.FeeType{"foo"}}, core.PageRequest{})
	if !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("ListFees() invalid error got = %v, want = %v", err, core.ErrInvalidValue)
	}
}

// adjustFixture is the draft fixture with a second, unused charge and a
// percentage charge used once.
type adjustFixture struct {
	*fixture
	sub           *subscription.Subscription
	draft         *invoice.Invoice
	calls         *catalog.Charge
	idle, percent *catalog.Charge
}

func newAdjustFixture(t *testing.T) *adjustFixture {
	t.Helper()
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
	m := f.Metric("api_calls", catalog.AggregationTypeCount, "")
	calls := f.Charge(f.plan, m, catalog.ChargeModelStandard, `{"amount":"2"}`)
	idleMetric := f.Metric("storage", catalog.AggregationTypeCount, "")
	idle := f.Charge(f.plan, idleMetric, catalog.ChargeModelStandard, `{"amount":"3"}`, func(c *catalog.Charge) {
		c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"4"}`)}
	})
	pm := f.Metric("amounts", catalog.AggregationTypeSum, "amount")
	percent := f.Charge(f.plan, pm, catalog.ChargeModelPercentage, `{"rate":"10"}`)
	sub := f.subscribe()
	f.Event(sub, "api_calls", billingtest.Date(2026, time.January, 10))
	f.Event(sub, "amounts", billingtest.Date(2026, time.January, 10), "amount", "100")
	draft := f.billOne(sub)
	if draft.Status != invoice.StatusDraft {
		t.Fatalf("status = %s, want = draft", draft.Status)
	}
	return &adjustFixture{fixture: f, sub: sub, draft: draft, calls: calls, idle: idle, percent: percent}
}

// source: spec/services/adjusted_fees/create_service_spec.rb:35-63
func TestAdjustFeeUnitsAndPrice(t *testing.T) {
	f := newAdjustFixture(t)
	fee := f.chargeFeeOf(f.draft.ID, f.calls.ID)
	got, err := f.svc.AdjustFee(f.Ctx, f.OrgID(), invoicing.FeeAdjustment{
		InvoiceID: f.draft.ID, FeeID: fee.ID, Mode: invoice.AdjustmentModeUnitsAndUnitPrice,
		Units: money.MustParse("5"), UnitPreciseAmount: money.MustParse("12.002"),
	})
	if err != nil {
		t.Fatalf("AdjustFee() error got = %v, want = nil", err)
	}
	if !got.Adjusted || !got.Fee.Units.Equal(money.MustParse("5")) || got.Fee.Amount != 6001 || got.Fee.ChargeID != f.calls.ID {
		t.Errorf("fee got = adjusted %t units %s amount %d, want = adjusted true units 5 amount 6001", got.Adjusted, got.Fee.Units, got.Fee.Amount)
	}
	as := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.AdjustedFee, error) {
		return r.AdjustedFees.ListByInvoice(ctx, f.OrgID(), f.draft.ID)
	})
	if len(as) != 1 || as[0].FeeID != fee.ID || !as[0].UnitPreciseAmount.Equal(money.MustParse("12.002")) {
		t.Fatalf("adjusted fees got = %+v, want = one of fee %s at 12.002", as, fee.ID)
	}
	// The refreshed invoice carries the adjusted amount, and the view of
	// the regenerated fee is still marked adjusted.
	if inv := f.Invoice(f.draft.ID); inv.FeesAmount != 1000+6001+1000 {
		t.Errorf("fees amount got = %d, want = %d", inv.FeesAmount, 1000+6001+1000)
	}
	again, err := f.svc.GetFee(f.Ctx, f.OrgID(), got.Fee.ID)
	if err != nil || !again.Adjusted {
		t.Errorf("GetFee() adjusted got = %t, %v, want = true", again.Adjusted, err)
	}

	// source: spec/services/adjusted_fees/create_service_spec.rb:137
	_, err = f.svc.AdjustFee(f.Ctx, f.OrgID(), invoicing.FeeAdjustment{
		InvoiceID: f.draft.ID, FeeID: got.Fee.ID, Mode: invoice.AdjustmentModeUnits, Units: money.One,
	})
	wantField(t, err, "adjusted_fee", core.CodeAlreadyExists)

	// source: spec/services/adjusted_fees/destroy_service_spec.rb:22-30
	back, err := f.svc.DeleteFeeAdjustment(f.Ctx, f.OrgID(), got.Fee.ID)
	if err != nil {
		t.Fatalf("DeleteFeeAdjustment() error got = %v, want = nil", err)
	}
	if back.Adjusted || back.Fee.Amount != 200 {
		t.Errorf("fee got = adjusted %t amount %d, want = adjusted false amount 200", back.Adjusted, back.Fee.Amount)
	}
	if inv := f.Invoice(f.draft.ID); inv.FeesAmount != 1000+200+1000 {
		t.Errorf("fees amount got = %d, want = %d", inv.FeesAmount, 1000+200+1000)
	}
	// source: spec/services/adjusted_fees/destroy_service_spec.rb:32
	_, err = f.svc.DeleteFeeAdjustment(f.Ctx, f.OrgID(), back.Fee.ID)
	wantNotFound(t, err, "adjusted_fee")
	// source: spec/services/adjusted_fees/destroy_service_spec.rb:43
	_, err = f.svc.DeleteFeeAdjustment(f.Ctx, f.OrgID(), core.NewID[core.FeeID](f.IDs))
	wantNotFound(t, err, "fee")
}

// source: spec/services/adjusted_fees/create_service_spec.rb:77-123
func TestAdjustFeeChargeModel(t *testing.T) {
	name := "Adjusted"
	for _, c := range []struct {
		name string
		in   invoicing.FeeAdjustment
		ok   bool
	}{
		// source: spec/services/adjusted_fees/create_service_spec.rb:77
		{"amount", invoicing.FeeAdjustment{Mode: invoice.AdjustmentModeUnitsAndUnitPrice, Units: money.MustParse("5"), UnitPreciseAmount: money.One}, true},
		// source: spec/services/adjusted_fees/create_service_spec.rb:88
		{"display name", invoicing.FeeAdjustment{Mode: invoice.AdjustmentModeDisplayName, InvoiceDisplayName: &name}, true},
		// source: spec/services/adjusted_fees/create_service_spec.rb:105
		{"units", invoicing.FeeAdjustment{Mode: invoice.AdjustmentModeUnits, Units: money.MustParse("5")}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newAdjustFixture(t)
			in := c.in
			in.InvoiceID, in.FeeID = f.draft.ID, f.chargeFeeOf(f.draft.ID, f.percent.ID).ID
			got, err := f.svc.AdjustFee(f.Ctx, f.OrgID(), in)
			if !c.ok {
				wantField(t, err, "charge", invoicing.CodeInvalidChargeModel)
				return
			}
			if err != nil {
				t.Fatalf("AdjustFee() error got = %v, want = nil", err)
			}
			if !got.Adjusted {
				t.Errorf("adjusted got = false, want = true")
			}
			if in.Mode == invoice.AdjustmentModeDisplayName && (got.Fee.InvoiceDisplayName == nil || *got.Fee.InvoiceDisplayName != name) {
				t.Errorf("display name got = %v, want = %s", got.Fee.InvoiceDisplayName, name)
			}
		})
	}
}

// source: spec/services/adjusted_fees/create_service_spec.rb:65,125
func TestAdjustFeeRejected(t *testing.T) {
	f := newAdjustFixture(t)
	fee := f.chargeFeeOf(f.draft.ID, f.calls.ID)
	// source: spec/services/adjusted_fees/create_service_spec.rb:125
	other := f.unattachedFee(f.sub)
	_, err := f.svc.AdjustFee(f.Ctx, f.OrgID(), invoicing.FeeAdjustment{
		InvoiceID: f.draft.ID, FeeID: other.ID, Mode: invoice.AdjustmentModeUnits, Units: money.One,
	})
	wantNotFound(t, err, "fee")
	_, err = f.svc.AdjustFee(f.Ctx, f.OrgID(), invoicing.FeeAdjustment{
		InvoiceID: core.NewID[core.InvoiceID](f.IDs), FeeID: fee.ID, Mode: invoice.AdjustmentModeUnits, Units: money.One,
	})
	wantNotFound(t, err, "invoice")
	// source: spec/services/adjusted_fees/create_service_spec.rb:65
	if _, err := f.svc.FinalizeDraft(f.Ctx, f.OrgID(), f.draft.ID); err != nil {
		t.Fatalf("FinalizeDraft() error got = %v, want = nil", err)
	}
	fee = f.chargeFeeOf(f.draft.ID, f.calls.ID)
	_, err = f.svc.AdjustFee(f.Ctx, f.OrgID(), invoicing.FeeAdjustment{
		InvoiceID: f.draft.ID, FeeID: fee.ID, Mode: invoice.AdjustmentModeUnits, Units: money.One,
	})
	wantRule(t, err, invoicing.CodeNotDraft)
	_, err = f.svc.DeleteFeeAdjustment(f.Ctx, f.OrgID(), fee.ID)
	wantNotFound(t, err, "fee")
}

// source: spec/services/adjusted_fees/create_service_spec.rb:151-323
func TestAdjustFeeWithoutFee(t *testing.T) {
	f := newAdjustFixture(t)
	for _, c := range []struct {
		name     string
		in       invoicing.FeeAdjustment
		resource string
	}{
		// source: spec/services/adjusted_fees/create_service_spec.rb:258
		{"subscription", invoicing.FeeAdjustment{SubscriptionID: core.NewID[core.SubscriptionID](f.IDs), ChargeID: f.idle.ID}, "subscription"},
		// source: spec/services/adjusted_fees/create_service_spec.rb:280
		{"charge", invoicing.FeeAdjustment{SubscriptionID: f.sub.ID, ChargeID: core.NewID[core.ChargeID](f.IDs)}, "charge"},
		// source: spec/services/adjusted_fees/create_service_spec.rb:302
		{"charge filter", invoicing.FeeAdjustment{SubscriptionID: f.sub.ID, ChargeID: f.idle.ID, ChargeFilterID: core.NewID[core.ChargeFilterID](f.IDs)}, "charge_filter"},
		{"fixed charge", invoicing.FeeAdjustment{SubscriptionID: f.sub.ID, FixedChargeID: core.NewID[core.FixedChargeID](f.IDs)}, "fixed_charge"},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := c.in
			in.InvoiceID, in.Mode, in.Units, in.UnitPreciseAmount = f.draft.ID, invoice.AdjustmentModeUnitsAndUnitPrice, money.MustParse("5"), money.MustParse("12.002")
			_, err := f.svc.AdjustFee(f.Ctx, f.OrgID(), in)
			wantNotFound(t, err, c.resource)
		})
	}

	// A charge added after billing has no fee on the draft.
	late := f.Charge(f.plan, f.Metric("late_calls", catalog.AggregationTypeCount, ""), catalog.ChargeModelStandard, `{"amount":"1"}`, func(c *catalog.Charge) {
		c.Filters = []catalog.ChargeFilter{billingtest.ChargeFilter(map[string][]string{"region": {"eu"}}, `{"amount":"4"}`)}
	})
	filter := late.Filters[0].ID
	for _, fee := range f.Fees(f.draft.ID) {
		if fee.ChargeID == late.ID {
			t.Fatalf("fee of the late charge got = %s, want = none", fee.ID)
		}
	}
	// source: spec/services/adjusted_fees/create_service_spec.rb:164-224
	got, err := f.svc.AdjustFee(f.Ctx, f.OrgID(), invoicing.FeeAdjustment{
		InvoiceID: f.draft.ID, SubscriptionID: f.sub.ID, ChargeID: late.ID, ChargeFilterID: filter,
		Mode: invoice.AdjustmentModeUnitsAndUnitPrice, Units: money.MustParse("5"), UnitPreciseAmount: money.MustParse("12.002"),
	})
	if err != nil {
		t.Fatalf("AdjustFee() error got = %v, want = nil", err)
	}
	if !got.Adjusted || got.Fee.ChargeID != late.ID || got.Fee.ChargeFilterID != filter || got.Fee.Amount != 6001 || got.Fee.InvoiceID != f.draft.ID {
		t.Errorf("fee got = adjusted %t charge %s filter %s amount %d, want = adjusted true charge %s filter %s amount 6001",
			got.Adjusted, got.Fee.ChargeID, got.Fee.ChargeFilterID, got.Fee.Amount, late.ID, filter)
	}
	as := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*invoice.AdjustedFee, error) {
		return r.AdjustedFees.ListByInvoice(ctx, f.OrgID(), f.draft.ID)
	})
	if len(as) != 1 || !as[0].FeeID.IsZero() || as[0].ChargeFilterID != filter || as[0].FeeType != core.FeeTypeCharge {
		t.Errorf("adjusted fees got = %+v, want = one unlinked of filter %s", *as[0], filter)
	}

	// source: spec/services/adjusted_fees/create_service_spec.rb:237-256
	if len(as) == 1 {
		// The adjustment applies to the regenerated fee by key.
		if again, err := f.svc.GetFee(f.Ctx, f.OrgID(), got.Fee.ID); err != nil || !again.Adjusted {
			t.Errorf("GetFee() adjusted got = %t, %v, want = true", again.Adjusted, err)
		}
	}
	again, err := f.svc.AdjustFee(f.Ctx, f.OrgID(), invoicing.FeeAdjustment{
		InvoiceID: f.draft.ID, SubscriptionID: f.sub.ID, ChargeID: f.calls.ID,
		Mode: invoice.AdjustmentModeUnits, Units: money.MustParse("3"),
	})
	if err != nil {
		t.Fatalf("AdjustFee() of an existing fee error got = %v, want = nil", err)
	}
	if !again.Adjusted || again.Fee.Amount != 600 {
		t.Errorf("fee got = adjusted %t amount %d, want = adjusted true amount 600", again.Adjusted, again.Fee.Amount)
	}
	_, err = f.svc.AdjustFee(f.Ctx, f.OrgID(), invoicing.FeeAdjustment{
		InvoiceID: f.draft.ID, SubscriptionID: f.sub.ID, ChargeID: late.ID, ChargeFilterID: filter,
		Mode: invoice.AdjustmentModeUnits, Units: money.One,
	})
	wantField(t, err, "adjusted_fee", core.CodeAlreadyExists)
}

// source: spec/services/adjusted_fees/create_service_spec.rb:325-405
func TestAdjustFixedChargeFee(t *testing.T) {
	f := newFixture(t, billingDay)
	f.UpdateBillingEntity(func(b *organization.BillingEntity) { b.InvoiceGracePeriodDays = 3 })
	fc := f.FixedCharge(f.plan, f.AddOn(500), catalog.FixedChargeModelStandard, `{"amount":"3"}`, "2")
	sub := f.subscribe()
	draft := f.billOne(sub)
	if draft.Status != invoice.StatusDraft {
		t.Fatalf("status = %s, want = draft", draft.Status)
	}
	got, err := f.svc.AdjustFee(f.Ctx, f.OrgID(), invoicing.FeeAdjustment{
		InvoiceID: draft.ID, SubscriptionID: sub.ID, FixedChargeID: fc.ID,
		Mode: invoice.AdjustmentModeUnitsAndUnitPrice, Units: money.MustParse("10"), UnitPreciseAmount: money.MustParse("15.5"),
	})
	if err != nil {
		t.Fatalf("AdjustFee() error got = %v, want = nil", err)
	}
	if !got.Adjusted || got.Fee.FixedChargeID != fc.ID || got.Fee.Type != core.FeeTypeFixedCharge ||
		!got.Fee.Units.Equal(money.MustParse("10")) || got.Fee.Amount != 15500 {
		t.Errorf("fee got = adjusted %t type %s units %s amount %d, want = adjusted true fixed_charge units 10 amount 15500",
			got.Adjusted, got.Fee.Type, got.Fee.Units, got.Fee.Amount)
	}
	// source: spec/services/adjusted_fees/destroy_service_spec.rb:22
	back, err := f.svc.DeleteFeeAdjustment(f.Ctx, f.OrgID(), got.Fee.ID)
	if err != nil {
		t.Fatalf("DeleteFeeAdjustment() error got = %v, want = nil", err)
	}
	if back.Adjusted || !back.Fee.Units.Equal(money.MustParse("2")) {
		t.Errorf("fee got = adjusted %t units %s, want = adjusted false units 2", back.Adjusted, back.Fee.Units)
	}
}
