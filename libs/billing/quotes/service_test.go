package quotes_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing"
	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/quotes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

var testNow = billingtest.Date(2026, time.March, 15, 12, 0)

// fix is a customer and the quote service over a memstore.
type fix struct {
	h    *billingtest.Harness
	svc  *quotes.Service
	cust *customer.Customer
}

func newFix(t *testing.T) *fix {
	t.Helper()
	h := billingtest.New(t, testNow)
	e, err := billing.New(*h.Deps)
	if err != nil {
		t.Fatalf("billing.New() error = %v", err)
	}
	return &fix{h: h, svc: e.Quotes, cust: h.Customer()}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// oneOffItems quotes add-on a once at its amount.
func (f *fix) oneOffItems(t *testing.T, a *catalog.AddOn) json.RawMessage {
	return mustJSON(t, map[string]any{"addOns": []any{map[string]any{
		"id": a.ID.String(), "localId": "3d08b2df-4e4c-4d58-b415-a525c1663735", "type": "add_on",
		"payload": map[string]any{"code": a.Code, "units": 1, "unitAmountCents": a.Amount, "totalAmountCents": a.Amount},
	}}})
}

// planItems quotes plan p starting today.
func (f *fix) planItems(t *testing.T, p *catalog.Plan) json.RawMessage {
	return mustJSON(t, map[string]any{"plans": []any{map[string]any{
		"id": p.ID.String(), "type": "plan",
		"payload": map[string]any{"code": p.Code, "startDate": f.h.Now().Format(time.DateOnly), "charges": []any{}, "fixedCharges": []any{}},
	}}})
}

func (f *fix) quote(t *testing.T, typ quote.OrderType, items json.RawMessage) *quotes.QuoteView {
	t.Helper()
	q, err := f.svc.Create(f.h.Ctx, quotes.CreateInput{OrganizationID: f.h.OrgID(), CustomerID: f.cust.ID, OrderType: typ, BillingItems: items})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return q
}

// approved returns a one-off quote approved into an order form.
func (f *fix) approved(t *testing.T) (*quotes.QuoteView, *quotes.Approval) {
	t.Helper()
	q := f.quote(t, quote.OrderTypeOneOff, f.oneOffItems(t, f.h.AddOn(10_000)))
	a, err := f.svc.ApproveVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID, nil)
	if err != nil {
		t.Fatalf("ApproveVersion() error = %v", err)
	}
	return q, a
}

// signed returns the order of a signed one-off order form.
func (f *fix) signed(t *testing.T, in quotes.SignInput) *quotes.OrderView {
	t.Helper()
	_, a := f.approved(t)
	sig, err := f.svc.SignOrderForm(f.h.Ctx, f.h.OrgID(), a.OrderForm.OrderForm.ID, in)
	if err != nil {
		t.Fatalf("SignOrderForm() error = %v", err)
	}
	return sig.Order
}

// wantField fails unless err is a validation error of field with code.
func wantField(t *testing.T, err error, field, code string) {
	t.Helper()
	var v *core.ValidationError
	if !errors.As(err, &v) || !v.Has(field, code) {
		t.Fatalf("got error = %v, want = %s: %s", err, field, code)
	}
}

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("got error = %v, want = %s_not_found", err, resource)
	}
}

func wantWebhooks(t *testing.T, h *billingtest.Harness, want ...webhook.EventType) {
	t.Helper()
	got := h.Webhooks()
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("got webhooks = %v, want %s among them", got, w)
		}
	}
}

func TestCreateQuote(t *testing.T) {
	t.Parallel()
	t.Run("creates an empty draft quote", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quotes/create_service_spec.rb:33")
		f := newFix(t)
		q := f.quote(t, quote.OrderTypeSubscriptionCreation, nil)
		v := q.Current.Version
		if q.Quote.Number == "" || q.Quote.CustomerID != f.cust.ID || v.Version != 1 || v.Status != quote.VersionStatusDraft {
			t.Errorf("got quote = %+v, version = %+v, want a numbered quote with draft version 1", q.Quote, v)
		}
		if v.Currency.Code() != "USD" {
			t.Errorf("got currency = %s, want = USD", v.Currency.Code())
		}
		wantWebhooks(t, f.h, webhook.EventTypeQuoteCreated)
	})
	t.Run("takes the currency from the subscription plan", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quotes/create_service_spec.rb:179")
		f := newFix(t)
		p := f.h.Plan(period.Monthly, 100, func(p *catalog.Plan) { p.Currency = money.MustCurrency("EUR") })
		s := f.h.Subscription(f.cust, p)
		q, err := f.svc.Create(f.h.Ctx, quotes.CreateInput{
			OrganizationID: f.h.OrgID(), CustomerID: f.cust.ID, OrderType: quote.OrderTypeSubscriptionAmendment, SubscriptionID: s.ID,
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if q.Quote.SubscriptionID != s.ID || q.Current.Version.Currency.Code() != "EUR" {
			t.Errorf("got subscription = %s, currency = %s, want = %s, EUR", q.Quote.SubscriptionID, q.Current.Version.Currency.Code(), s.ID)
		}
	})
	tests := []struct {
		name, source string
		in           func(f *fix) quotes.CreateInput
		check        func(t *testing.T, err error)
	}{
		{
			name: "subscription of another customer", source: "spec/services/quotes/create_service_spec.rb:192",
			in: func(f *fix) quotes.CreateInput {
				s := f.h.Subscription(f.h.Customer(), f.h.Plan(period.Monthly, 100))
				return quotes.CreateInput{OrganizationID: f.h.OrgID(), CustomerID: f.cust.ID, OrderType: quote.OrderTypeSubscriptionAmendment, SubscriptionID: s.ID}
			},
			check: func(t *testing.T, err error) { wantNotFound(t, err, "subscription") },
		},
		{
			name: "subscription required but missing", source: "spec/services/quotes/create_service_spec.rb:268",
			in: func(f *fix) quotes.CreateInput {
				return quotes.CreateInput{OrganizationID: f.h.OrgID(), CustomerID: f.cust.ID, OrderType: quote.OrderTypeSubscriptionAmendment}
			},
			check: func(t *testing.T, err error) { wantNotFound(t, err, "subscription") },
		},
		{
			name: "customer does not exist", source: "spec/services/quotes/create_service_spec.rb:258",
			in: func(f *fix) quotes.CreateInput {
				return quotes.CreateInput{OrganizationID: f.h.OrgID(), CustomerID: core.NewID[core.CustomerID](f.h.IDs), OrderType: quote.OrderTypeOneOff}
			},
			check: func(t *testing.T, err error) { wantNotFound(t, err, "customer") },
		},
		{
			name: "owners include unknown members", source: "spec/services/quotes/create_service_spec.rb:236",
			in: func(f *fix) quotes.CreateInput {
				return quotes.CreateInput{
					OrganizationID: f.h.OrgID(), CustomerID: f.cust.ID, OrderType: quote.OrderTypeOneOff,
					Owners: []core.MembershipID{core.NewID[core.MembershipID](f.h.IDs)},
				}
			},
			check: func(t *testing.T, err error) { wantField(t, err, "owners", quotes.CodeOwnersInvalid) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newFix(t)
			_, err := f.svc.Create(f.h.Ctx, tt.in(f))
			tt.check(t, err)
		})
	}
}

func TestUpdateQuote(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/services/quotes/update_service_spec.rb:45")
	f := newFix(t)
	q := f.quote(t, quote.OrderTypeOneOff, nil)
	_, err := f.svc.Update(f.h.Ctx, f.h.OrgID(), q.Quote.ID, quotes.UpdateInput{Owners: core.Some([]core.MembershipID{core.NewID[core.MembershipID](f.h.IDs)})})
	wantField(t, err, "owners", quotes.CodeOwnersInvalid)

	t.Log("source: spec/services/quotes/update_service_spec.rb:58")
	_, err = f.svc.Update(f.h.Ctx, f.h.OrgID(), core.NewID[core.QuoteID](f.h.IDs), quotes.UpdateInput{})
	wantNotFound(t, err, "quote")
}

func TestApproveVersion(t *testing.T) {
	t.Parallel()
	t.Run("approves and creates an order form", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:25")
		f := newFix(t)
		q, a := f.approved(t)
		if a.Version.Version.Status != quote.VersionStatusApproved || a.Version.Version.ApprovedAt == nil {
			t.Errorf("got version = %+v, want approved", a.Version.Version)
		}
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:33")
		of := a.OrderForm
		if of.OrderForm.Status != quote.OrderFormStatusGenerated || of.OrderForm.QuoteVersionID != q.Current.Version.ID || of.QuoteID != q.Quote.ID {
			t.Errorf("got order form = %+v, want a generated form of the version", of.OrderForm)
		}
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:43")
		wantWebhooks(t, f.h, webhook.EventTypeQuoteApproved, webhook.EventTypeOrderFormCreated)

		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:255")
		_, err := f.svc.ApproveVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID, nil)
		wantField(t, err, "status", quote.CodeNotApprovable)
	})
	t.Run("expires_at in the future", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:102")
		f := newFix(t)
		q := f.quote(t, quote.OrderTypeOneOff, f.oneOffItems(t, f.h.AddOn(10_000)))
		at := f.h.Now().Add(72 * time.Hour)
		a, err := f.svc.ApproveVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID, &at)
		if err != nil {
			t.Fatalf("ApproveVersion() error = %v", err)
		}
		if got := a.OrderForm.OrderForm.ExpiresAt; got == nil || !got.Equal(at) {
			t.Errorf("got expires_at = %v, want = %v", got, at)
		}
	})
	t.Run("expires_at in the past", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:113")
		f := newFix(t)
		q := f.quote(t, quote.OrderTypeOneOff, f.oneOffItems(t, f.h.AddOn(10_000)))
		at := f.h.Now().Add(-time.Hour)
		_, err := f.svc.ApproveVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID, &at)
		wantField(t, err, "expires_at", quotes.CodeInvalidDate)
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:123")
		v, err := f.svc.GetVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID)
		if err != nil || v.Version.Status != quote.VersionStatusDraft {
			t.Errorf("got version = %+v, %v, want a draft", v, err)
		}
	})
	t.Run("incomplete one-off items", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:220")
		f := newFix(t)
		q := f.quote(t, quote.OrderTypeOneOff, nil)
		if _, err := f.svc.ApproveVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID, nil); err == nil {
			t.Error("ApproveVersion() error = nil, want a validation failure")
		}
	})
	t.Run("version does not exist", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/approve_service_spec.rb:265")
		f := newFix(t)
		q := f.quote(t, quote.OrderTypeOneOff, nil)
		_, err := f.svc.ApproveVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, core.NewID[core.QuoteVersionID](f.h.IDs), nil)
		wantNotFound(t, err, "quote_version")
	})
}

func TestVoidAndCloneVersion(t *testing.T) {
	t.Parallel()
	t.Run("voids a draft", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/void_service_spec.rb:16")
		f := newFix(t)
		q := f.quote(t, quote.OrderTypeOneOff, nil)
		v, err := f.svc.VoidVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID)
		if err != nil {
			t.Fatalf("VoidVersion() error = %v", err)
		}
		if v.Version.Status != quote.VersionStatusVoided || v.Version.VoidReason != quote.VersionVoidReasonManual || v.Version.VoidedAt == nil {
			t.Errorf("got version = %+v, want voided manually", v.Version)
		}
		wantWebhooks(t, f.h, webhook.EventTypeQuoteVoided)

		t.Log("source: spec/services/quote_versions/clone_service_spec.rb:24")
		c, err := f.svc.CloneVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID)
		if err != nil {
			t.Fatalf("CloneVersion() error = %v", err)
		}
		if c.Version.Version != 2 || c.Version.Status != quote.VersionStatusDraft {
			t.Errorf("got clone = %+v, want draft version 2", c.Version)
		}
	})
	t.Run("clone of a draft voids the source", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/quote_versions/clone_service_spec.rb:72")
		f := newFix(t)
		q := f.quote(t, quote.OrderTypeOneOff, nil)
		if _, err := f.svc.CloneVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID); err != nil {
			t.Fatalf("CloneVersion() error = %v", err)
		}
		src, err := f.svc.GetVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID)
		if err != nil || src.Version.Status != quote.VersionStatusVoided || src.Version.VoidReason != quote.VersionVoidReasonSuperseded {
			t.Errorf("got source = %+v, %v, want voided as superseded", src, err)
		}
	})
	t.Run("approved version", func(t *testing.T) {
		t.Parallel()
		f := newFix(t)
		q, _ := f.approved(t)
		t.Log("source: spec/services/quote_versions/void_service_spec.rb:79")
		_, err := f.svc.VoidVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID)
		wantField(t, err, "status", quote.CodeNotVoidable)
		t.Log("source: spec/services/quote_versions/clone_service_spec.rb:117")
		_, err = f.svc.CloneVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID)
		wantField(t, err, "status", quote.CodeNotClonable)
	})
}

func TestSignOrderForm(t *testing.T) {
	t.Parallel()
	t.Run("signs and creates an order", func(t *testing.T) {
		t.Parallel()
		f := newFix(t)
		_, a := f.approved(t)
		sig, err := f.svc.SignOrderForm(f.h.Ctx, f.h.OrgID(), a.OrderForm.OrderForm.ID, quotes.SignInput{})
		if err != nil {
			t.Fatalf("SignOrderForm() error = %v", err)
		}
		t.Log("source: spec/services/order_forms/mark_as_signed_service_spec.rb:109")
		if sig.OrderForm.OrderForm.Status != quote.OrderFormStatusSigned || sig.OrderForm.OrderForm.SignedAt == nil {
			t.Errorf("got order form = %+v, want signed", sig.OrderForm.OrderForm)
		}
		t.Log("source: spec/services/order_forms/mark_as_signed_service_spec.rb:121")
		o := sig.Order.Order
		if o.Status != quote.OrderStatusCreated || o.OrderFormID != a.OrderForm.OrderForm.ID || o.CustomerID != f.cust.ID || o.ExecutionMode != "" {
			t.Errorf("got order = %+v, want a created order of the form without execution mode", o)
		}
		t.Log("source: spec/services/order_forms/mark_as_signed_service_spec.rb:130")
		wantWebhooks(t, f.h, webhook.EventTypeOrderFormSigned, webhook.EventTypeOrderCreated)

		t.Log("source: spec/services/order_forms/mark_as_signed_service_spec.rb:75")
		_, err = f.svc.SignOrderForm(f.h.Ctx, f.h.OrgID(), a.OrderForm.OrderForm.ID, quotes.SignInput{})
		wantField(t, err, "status", quote.CodeNotSignable)
	})
	t.Run("with execution mode and execute_at", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/order_forms/mark_as_signed_service_spec.rb:289")
		f := newFix(t)
		at := f.h.Now().Add(48 * time.Hour)
		o := f.signed(t, quotes.SignInput{ExecutionMode: quote.ExecutionModeExecute, ExecuteAt: &at})
		if o.Order.ExecutionMode != quote.ExecutionModeExecute || o.Order.ExecuteAt == nil || !o.Order.ExecuteAt.Equal(at) {
			t.Errorf("got order = %+v, want mode execute at %v", o.Order, at)
		}
	})
	future, past := testNow.Add(48*time.Hour), testNow.Add(-time.Hour)
	tests := []struct {
		name, source, field, code string
		in                        quotes.SignInput
	}{
		{"invalid execution mode", "spec/services/order_forms/mark_as_signed_service_spec.rb:302", "execution_mode", quotes.CodeValueIsInvalid, quotes.SignInput{ExecutionMode: "later"}},
		{"execute_at without execution mode", "spec/services/order_forms/mark_as_signed_service_spec.rb:314", "execution_mode", core.CodeRequired, quotes.SignInput{ExecuteAt: &future}},
		{"execute_at in the past", "spec/services/order_forms/mark_as_signed_service_spec.rb:340", "execute_at", quotes.CodeInvalidDate, quotes.SignInput{ExecutionMode: quote.ExecutionModeExecute, ExecuteAt: &past}},
		{"malformed signed document", "spec/services/order_forms/mark_as_signed_service_spec.rb:198", "signed_document", core.CodeInvalidFormat, quotes.SignInput{SignedDocument: "not a data uri"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			t.Logf("source: %s", tt.source)
			f := newFix(t)
			_, a := f.approved(t)
			_, err := f.svc.SignOrderForm(f.h.Ctx, f.h.OrgID(), a.OrderForm.OrderForm.ID, tt.in)
			wantField(t, err, tt.field, tt.code)
			form, err := f.svc.GetOrderForm(f.h.Ctx, f.h.OrgID(), a.OrderForm.OrderForm.ID)
			if err != nil || form.OrderForm.Status != quote.OrderFormStatusGenerated {
				t.Errorf("got order form = %+v, %v, want it still generated", form, err)
			}
		})
	}
}

func TestVoidOrderForm(t *testing.T) {
	t.Parallel()
	f := newFix(t)
	q, a := f.approved(t)
	id := a.OrderForm.OrderForm.ID
	of, err := f.svc.VoidOrderForm(f.h.Ctx, f.h.OrgID(), id)
	if err != nil {
		t.Fatalf("VoidOrderForm() error = %v", err)
	}
	t.Log("source: spec/services/order_forms/void_service_spec.rb:72")
	if of.OrderForm.Status != quote.OrderFormStatusVoided || of.OrderForm.VoidReason != quote.OrderFormVoidReasonManual {
		t.Errorf("got order form = %+v, want voided manually", of.OrderForm)
	}
	t.Log("source: spec/services/order_forms/void_service_spec.rb:81")
	v, err := f.svc.GetVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID)
	if err != nil || v.Version.Status != quote.VersionStatusVoided || v.Version.VoidReason != quote.VersionVoidReasonCascadeOfVoided {
		t.Errorf("got version = %+v, %v, want voided by cascade", v, err)
	}
	t.Log("source: spec/services/order_forms/void_service_spec.rb:88")
	wantWebhooks(t, f.h, webhook.EventTypeOrderFormVoided, webhook.EventTypeQuoteVoided)
	t.Log("source: spec/services/order_forms/void_service_spec.rb:62")
	_, err = f.svc.VoidOrderForm(f.h.Ctx, f.h.OrgID(), id)
	wantField(t, err, "status", quote.CodeNotVoidable)
	t.Log("source: spec/services/order_forms/void_service_spec.rb:29")
	_, err = f.svc.VoidOrderForm(f.h.Ctx, f.h.OrgID(), core.NewID[core.OrderFormID](f.h.IDs))
	wantNotFound(t, err, "order_form")
}

func TestUpdateOrder(t *testing.T) {
	t.Parallel()
	t.Run("only execution mode", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/orders/update_service_spec.rb:74")
		f := newFix(t)
		o := f.signed(t, quotes.SignInput{})
		got, err := f.svc.UpdateOrder(f.h.Ctx, f.h.OrgID(), o.Order.ID, quotes.UpdateOrderInput{ExecutionMode: core.Some(quote.ExecutionModeOrderOnly)})
		if err != nil {
			t.Fatalf("UpdateOrder() error = %v", err)
		}
		if got.Order.ExecutionMode != quote.ExecutionModeOrderOnly || got.Order.ExecuteAt != nil {
			t.Errorf("got order = %+v, want order_only without execute_at", got.Order)
		}
	})
	t.Run("execute_at without execution mode", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/orders/update_service_spec.rb:98")
		f := newFix(t)
		o := f.signed(t, quotes.SignInput{})
		at := f.h.Now().Add(48 * time.Hour)
		_, err := f.svc.UpdateOrder(f.h.Ctx, f.h.OrgID(), o.Order.ID, quotes.UpdateOrderInput{ExecuteAt: core.Some(&at)})
		wantField(t, err, "execution_mode", core.CodeRequired)
	})
	t.Run("clearing execute_at keeps the mode", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/orders/update_service_spec.rb:207")
		f := newFix(t)
		at := f.h.Now().Add(48 * time.Hour)
		o := f.signed(t, quotes.SignInput{ExecutionMode: quote.ExecutionModeExecute, ExecuteAt: &at})
		got, err := f.svc.UpdateOrder(f.h.Ctx, f.h.OrgID(), o.Order.ID, quotes.UpdateOrderInput{ExecuteAt: core.Some[*time.Time](nil)})
		if err != nil {
			t.Fatalf("UpdateOrder() error = %v", err)
		}
		if got.Order.ExecutionMode != quote.ExecutionModeExecute || got.Order.ExecuteAt != nil {
			t.Errorf("got order = %+v, want mode execute without execute_at", got.Order)
		}
	})
	t.Run("executed order", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/orders/update_service_spec.rb:62")
		f := newFix(t)
		o := f.signed(t, quotes.SignInput{ExecutionMode: quote.ExecutionModeOrderOnly})
		if _, err := f.svc.Execute(f.h.Ctx, f.h.OrgID(), o.Order.ID); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		_, err := f.svc.UpdateOrder(f.h.Ctx, f.h.OrgID(), o.Order.ID, quotes.UpdateOrderInput{ExecutionMode: core.Some(quote.ExecutionModeExecute)})
		wantField(t, err, "status", quotes.CodeNotEditable)
	})
}

func TestExecuteOneOff(t *testing.T) {
	t.Parallel()
	t.Run("creates a one-off invoice", func(t *testing.T) {
		t.Parallel()
		f := newFix(t)
		o := f.signed(t, quotes.SignInput{})
		got, err := f.svc.ExecuteOrder(f.h.Ctx, f.h.OrgID(), o.Order.ID, quote.ExecutionModeExecute)
		if err != nil {
			t.Fatalf("ExecuteOrder() error = %v", err)
		}
		t.Log("source: spec/services/orders/one_off/execute_service_spec.rb:40")
		if got.Order.Status != quote.OrderStatusExecuted || got.Order.ExecutedAt == nil || got.Record == nil || got.Record.InvoiceID == nil {
			t.Fatalf("got order = %+v, record = %+v, want executed with an invoice", got.Order, got.Record)
		}
		inv := f.h.Invoice(*got.Record.InvoiceID)
		if inv.CustomerID != f.cust.ID || inv.FeesAmount != 10_000 {
			t.Errorf("got invoice customer = %s, fees = %d, want = %s, 10000", inv.CustomerID, inv.FeesAmount, f.cust.ID)
		}
		t.Log("source: spec/services/orders/one_off/execute_service_spec.rb:59")
		r := got.Record
		if len(r.SubscriptionIDs)+len(r.TerminatedSubscriptionIDs)+len(r.AppliedCouponIDs)+len(r.WalletIDs)+len(r.Errors) != 0 {
			t.Errorf("got record = %+v, want empty lists besides the invoice", r)
		}
		t.Log("source: spec/services/orders/one_off/execute_service_spec.rb:68")
		wantWebhooks(t, f.h, webhook.EventTypeOrderExecuted)

		t.Log("source: spec/services/orders/one_off/execute_service_spec.rb:296")
		again, err := f.svc.Execute(f.h.Ctx, f.h.OrgID(), o.Order.ID)
		if err != nil || again.Record.InvoiceID == nil || *again.Record.InvoiceID != *got.Record.InvoiceID {
			t.Errorf("got second execution = %+v, %v, want the same record", again, err)
		}
		if n := len(f.h.Invoices(f.cust.ID)); n != 1 {
			t.Errorf("got %d invoices, want = 1", n)
		}
	})
	t.Run("order only", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/orders/one_off/execute_service_spec.rb:279")
		f := newFix(t)
		o := f.signed(t, quotes.SignInput{ExecutionMode: quote.ExecutionModeOrderOnly})
		got, err := f.svc.Execute(f.h.Ctx, f.h.OrgID(), o.Order.ID)
		if err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if got.Order.Status != quote.OrderStatusExecuted || got.Record.InvoiceID != nil || len(f.h.Invoices(f.cust.ID)) != 0 {
			t.Errorf("got order = %+v, record = %+v, want executed without billing", got.Order, got.Record)
		}
	})
	t.Run("without execution mode", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/orders/one_off/execute_service_spec.rb:342")
		f := newFix(t)
		o := f.signed(t, quotes.SignInput{})
		_, err := f.svc.Execute(f.h.Ctx, f.h.OrgID(), o.Order.ID)
		wantField(t, err, "execution_mode", core.CodeRequired)
		got, err := f.svc.GetOrder(f.h.Ctx, f.h.OrgID(), o.Order.ID)
		if err != nil || got.Order.Status != quote.OrderStatusCreated {
			t.Errorf("got order = %+v, %v, want it untouched", got, err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		t.Log("source: spec/services/orders/execute_service_spec.rb:31")
		f := newFix(t)
		_, err := f.svc.Execute(f.h.Ctx, f.h.OrgID(), core.NewID[core.OrderID](f.h.IDs))
		wantNotFound(t, err, "order")
	})
}

func TestExecuteSubscriptionCreation(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/services/orders/subscription_creation/execute_service_spec.rb")
	f := newFix(t)
	p := f.h.Plan(period.Monthly, 100)
	q := f.quote(t, quote.OrderTypeSubscriptionCreation, f.planItems(t, p))
	a, err := f.svc.ApproveVersion(f.h.Ctx, f.h.OrgID(), q.Quote.ID, q.Current.Version.ID, nil)
	if err != nil {
		t.Fatalf("ApproveVersion() error = %v", err)
	}
	sig, err := f.svc.SignOrderForm(f.h.Ctx, f.h.OrgID(), a.OrderForm.OrderForm.ID, quotes.SignInput{ExecutionMode: quote.ExecutionModeExecute})
	if err != nil {
		t.Fatalf("SignOrderForm() error = %v", err)
	}
	got, err := f.svc.Execute(f.h.Ctx, f.h.OrgID(), sig.Order.Order.ID)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got.Order.Status != quote.OrderStatusExecuted || len(got.Record.SubscriptionIDs) != 1 {
		t.Fatalf("got order = %+v, record = %+v, want executed with one subscription", got.Order, got.Record)
	}
	s := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) (*subscription.Subscription, error) {
		return r.Subscriptions.Get(ctx, f.h.OrgID(), got.Record.SubscriptionIDs[0])
	})
	if s.CustomerID != f.cust.ID || s.PlanID != p.ID {
		t.Errorf("got subscription customer = %s, plan = %s, want = %s, %s", s.CustomerID, s.PlanID, f.cust.ID, p.ID)
	}
}

func TestExecuteScheduledOrders(t *testing.T) {
	t.Parallel()
	t.Log("source: spec/jobs/clock/execute_scheduled_orders_job_spec.rb:15")
	f := newFix(t)
	at := f.h.Now().Add(48 * time.Hour)
	o := f.signed(t, quotes.SignInput{ExecutionMode: quote.ExecutionModeOrderOnly, ExecuteAt: &at})
	if n, err := f.svc.ExecuteScheduledOrders(f.h.Ctx, f.h.OrgID(), f.h.Now()); err != nil || n != 0 {
		t.Errorf("got ExecuteScheduledOrders() before execute_at = %d, %v, want = 0, nil", n, err)
	}
	if n, err := f.svc.ExecuteScheduledOrders(f.h.Ctx, f.h.OrgID(), at.Add(time.Minute)); err != nil || n != 1 {
		t.Errorf("got ExecuteScheduledOrders() after execute_at = %d, %v, want = 1, nil", n, err)
	}
	if !slices.Contains(f.h.JobKinds(), "billing.execute_order") {
		t.Errorf("got jobs = %v, want billing.execute_order of %s", f.h.JobKinds(), o.Order.ID)
	}
}
