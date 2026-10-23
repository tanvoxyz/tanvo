package payment

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/money"
)

func ptr[T any](v T) *T { return &v }

func TestCustomerParamsOf(t *testing.T) {
	id := core.MustParseID[core.CustomerID]("0190a4c4-0000-7000-8000-000000000001")
	tests := []struct {
		name                string
		nameF, first, last  *string
		email               *string
		wantName, wantEmail string
	}{
		{name: "name and first email", nameF: ptr("Acme"), email: ptr(" a@x.io , b@x.io"), wantName: "Acme", wantEmail: "a@x.io"},
		{name: "blank name uses first and last", nameF: ptr(" "), first: ptr("Ada"), last: ptr("Lovelace"), wantName: "Ada Lovelace"},
		{name: "only a last name", last: ptr("Lovelace"), wantName: "Lovelace"},
		{name: "nothing", wantName: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := customer.New(core.OrganizationID{}, id, "ext-1", core.BillingEntityID{}, testNow)
			c.Name, c.Firstname, c.Lastname, c.Email = tt.nameF, tt.first, tt.last, tt.email
			p := CustomerParamsOf(c)
			if p.Name != tt.wantName {
				t.Errorf("name: got = %q, want = %q", p.Name, tt.wantName)
			}
			if p.Email != tt.wantEmail {
				t.Errorf("email: got = %q, want = %q", p.Email, tt.wantEmail)
			}
			if want := fmt.Sprintf("customer:%s:%d", id, testNow.Unix()); p.IdempotencyKey != want {
				t.Errorf("key: got = %q, want = %q", p.IdempotencyKey, want)
			}
			if p.ExternalID != "ext-1" || p.CustomerID != id {
				t.Errorf("ids: got = %q %v, want = ext-1 %v", p.ExternalID, p.CustomerID, id)
			}
		})
	}
}

func TestSyncActionOf(t *testing.T) {
	tests := []struct {
		name string
		pc   ProviderCustomer
		want SyncAction
	}{
		// source: spec/services/payment_providers/stripe/customers/create_service_spec.rb:83
		{name: "sync without provider id creates", pc: ProviderCustomer{SyncWithProvider: true}, want: SyncCreate},
		{name: "no sync without provider id", pc: ProviderCustomer{}, want: SyncNone},
		// source: spec/services/payment_providers/stripe/customers/create_service_spec.rb:108
		{name: "empty provider id counts as missing", pc: ProviderCustomer{ProviderCustomerID: ptr(""), SyncWithProvider: true}, want: SyncCreate},
		// source: spec/services/payment_providers/stripe/customers/create_service_spec.rb:160,189
		{name: "provider id updates", pc: ProviderCustomer{ProviderCustomerID: ptr("cus_1"), SyncWithProvider: true}, want: SyncUpdate},
		{name: "removed does nothing", pc: ProviderCustomer{ProviderCustomerID: ptr("cus_1"), DeletedAt: &testNow}, want: SyncNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SyncActionOf(&tt.pc); got != tt.want {
				t.Errorf("got = %v, want = %v", got, tt.want)
			}
		})
	}
}

func TestProviderCustomerRemove(t *testing.T) {
	var pc ProviderCustomer
	if err := pc.Remove(testNow); err != nil {
		t.Fatalf("remove: got = %v, want = nil", err)
	}
	if !pc.Deleted() || !pc.UpdatedAt.Equal(testNow) {
		t.Errorf("deleted: got = %v %v, want = true %v", pc.Deleted(), pc.UpdatedAt, testNow)
	}
	if err := pc.Remove(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("remove twice: got = %v, want = %v", err, core.ErrInvalidTransition)
	}
}

func TestIdempotencyKeys(t *testing.T) {
	inv := core.MustParseID[core.InvoiceID]("0190a4c4-0000-7000-8000-0000000000aa")
	req := core.MustParseID[core.PaymentRequestID]("0190a4c4-0000-7000-8000-0000000000bb")
	pay := core.MustParseID[core.PaymentID]("0190a4c4-0000-7000-8000-0000000000cc")
	cn := core.MustParseID[core.CreditNoteID]("0190a4c4-0000-7000-8000-0000000000dd")
	tests := []struct {
		name, got, want string
	}{
		{"invoice", PaymentParams{PaymentID: pay, Payable: InvoicePayable(inv)}.IdempotencyKey(), "payment:invoice:" + inv.String() + ":" + pay.String()},
		{"payment request", PaymentParams{PaymentID: pay, Payable: RequestPayable(req)}.IdempotencyKey(), "payment:payment_request:" + req.String() + ":" + pay.String()},
		{"refund", RefundParams{CreditNoteID: cn}.IdempotencyKey(), cn.String()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got = %q, want = %q", tt.got, tt.want)
			}
		})
	}
}

func TestGatewayParamsValidate(t *testing.T) {
	inv := core.MustParseID[core.InvoiceID]("0190a4c4-0000-7000-8000-0000000000aa")
	pay := core.MustParseID[core.PaymentID]("0190a4c4-0000-7000-8000-0000000000cc")
	cn := core.MustParseID[core.CreditNoteID]("0190a4c4-0000-7000-8000-0000000000dd")
	eur := money.MustCurrency("EUR")
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{"payment ok", PaymentParams{PaymentID: pay, Payable: InvoicePayable(inv), ProviderCustomerID: "cus_1", Amount: 100, Currency: eur}.Validate(), nil},
		{"payment empty", PaymentParams{}.Validate(), []string{"payment_id: value_is_mandatory", "payable_type: value_is_mandatory", "provider_customer_id: value_is_mandatory", "amount: value_must_be_positive", "currency: currency_invalid"}},
		{"refund ok", RefundParams{CreditNoteID: cn, ProviderPaymentID: "pi_1", Amount: 1, Currency: eur}.Validate(), nil},
		{"refund without credit note", RefundParams{ProviderPaymentID: "pi_1", Amount: 1, Currency: eur}.Validate(), []string{"credit_note_id: value_is_mandatory"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { checkErrors(t, tt.err, tt.want) })
	}
}

func TestProviderErrorClasses(t *testing.T) {
	tests := []struct {
		kind                          ErrorKind
		onSession                     bool
		retryable, rejected, authFail bool
	}{
		{kind: KindRateLimited, retryable: true},
		{kind: KindUnavailable, retryable: true},
		{kind: KindAuthentication, authFail: true},
		{kind: KindCardDeclined, rejected: true},
		{kind: KindAuthenticationRequired, rejected: true},
		{kind: KindAuthenticationRequired, onSession: true},
		{kind: KindAmountTooSmall},
		{kind: KindIdempotency},
		{kind: KindNotFound, rejected: true},
		{kind: KindInvalidRequest, rejected: true},
		{kind: KindPermanent, rejected: true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%v", tt.kind, tt.onSession), func(t *testing.T) {
			err := fmt.Errorf("charge: %w", &ProviderError{Provider: "stripe", Kind: tt.kind, RetryOnSession: tt.onSession})
			for _, c := range []struct {
				target error
				want   bool
			}{{ErrRetryable, tt.retryable}, {ErrRejected, tt.rejected}, {ErrAuthentication, tt.authFail}} {
				if got := errors.Is(err, c.target); got != c.want {
					t.Errorf("is %v: got = %v, want = %v", c.target, got, c.want)
				}
			}
			if got := ErrorKindOf(err); got != tt.kind {
				t.Errorf("kind: got = %v, want = %v", got, tt.kind)
			}
		})
	}
	if got := ErrorKindOf(errors.New("x")); got != "" {
		t.Errorf("kind of plain error: got = %q, want = \"\"", got)
	}
	e := &ProviderError{Provider: "stripe", Kind: KindCardDeclined, Code: "card_declined", Message: "Your card was declined."}
	if got, want := e.Error(), "stripe: card_declined (card_declined): Your card was declined."; got != want {
		t.Errorf("message: got = %q, want = %q", got, want)
	}
}

func TestUnknownErrorClasses(t *testing.T) {
	tests := []struct {
		err       error
		rejected  bool
		retryable bool
	}{
		{ErrUnknownPayable, true, false},
		{ErrUnknownStatus, false, false},
	}
	for _, tt := range tests {
		if got := errors.Is(tt.err, ErrRejected); got != tt.rejected {
			t.Errorf("%v rejected: got = %v, want = %v", tt.err, got, tt.rejected)
		}
		if got := errors.Is(tt.err, ErrRetryable); got != tt.retryable {
			t.Errorf("%v retryable: got = %v, want = %v", tt.err, got, tt.retryable)
		}
	}
}
