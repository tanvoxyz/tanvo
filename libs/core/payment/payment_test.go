package payment

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
	"github.com/tanvoxyz/tanvo/libs/money"
)

var testNow = time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)

func newID[I interface{ IsZero() bool }](f func(core.IDGenerator) I) I {
	return f(core.UUIDv7Generator{})
}

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func checkErrors(t *testing.T, err error, want []string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := fieldErrors(err); !slices.Equal(got, want) {
		t.Errorf("errors: got = %v, want = %v (err = %v)", got, want, err)
	}
}

func checkRule(t *testing.T, err error, want string) {
	t.Helper()
	re, ok := errors.AsType[*core.RuleError](err)
	if !ok || re.Code != want || !errors.Is(err, core.ErrNotAllowed) {
		t.Errorf("rule: got = %v, want = %s", err, want)
	}
}

func TestPaymentValidate(t *testing.T) {
	type tc struct {
		Name             string   `yaml:"name"`
		Source           string   `yaml:"source"`
		Kind             string   `yaml:"kind"`
		Payable          string   `yaml:"payable"`
		Amount           int64    `yaml:"amount"`
		Total            int64    `yaml:"total"`
		Paid             int64    `yaml:"paid"`
		Offsets          int64    `yaml:"offsets"`
		Credit           bool     `yaml:"credit"`
		SucceededRequest bool     `yaml:"succeeded_request"`
		Reference        *string  `yaml:"reference"`
		NoReference      bool     `yaml:"no_reference"`
		ReferenceLength  int      `yaml:"reference_length"`
		Errors           []string `yaml:"errors"`
		Rule             string   `yaml:"rule"`
	}
	for _, c := range spectest.Load[tc](t, "payment.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			p := &Payment{
				Payable:  InvoicePayable(newID(core.NewID[core.InvoiceID])),
				Kind:     Kind(c.Kind),
				Status:   StatePending,
				Amount:   c.Amount,
				Currency: money.MustCurrency("EUR"),
			}
			if c.Payable == "payment_request" {
				p.Payable = RequestPayable(newID(core.NewID[core.PaymentRequestID]))
			}
			switch {
			case c.Reference != nil:
				p.Reference = c.Reference
			case c.ReferenceLength > 0:
				r := strings.Repeat("a", c.ReferenceLength)
				p.Reference = &r
			case p.Kind == KindManual && !c.NoReference:
				r := "123"
				p.Reference = &r
			}
			facts := InvoiceFacts{
				Credit:                  c.Credit,
				TotalAmount:             c.Total,
				TotalPaidAmount:         c.Paid,
				FinalizedOffsetAmount:   c.Offsets,
				SucceededPaymentRequest: c.SucceededRequest,
			}
			verr := p.Validate()
			merr := p.ValidateManual(facts)
			if c.Rule != "" {
				checkRule(t, merr, c.Rule)
				return
			}
			got := append(fieldErrors(verr), fieldErrors(merr)...)
			if merr != nil && core.FieldErrors(merr) == nil {
				t.Fatalf("ValidateManual: got = %v, want = field errors", merr)
			}
			want := c.Errors
			if want == nil {
				want = []string{}
			}
			if !slices.Equal(got, want) {
				t.Errorf("errors: got = %v, want = %v", got, want)
			}
		})
	}
}

func TestMethodDetails(t *testing.T) {
	type tc struct {
		Name        string `yaml:"name"`
		Source      string `yaml:"source"`
		Type        string `yaml:"type"`
		Brand       string `yaml:"brand"`
		Last4       string `yaml:"last4"`
		DisplayName string `yaml:"display_name"`
		LastDigits  string `yaml:"last_digits"`
	}
	for _, c := range spectest.Load[tc](t, "method_details.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			d := MethodDetails{Type: c.Type, Brand: c.Brand, Last4: c.Last4}
			if got := d.DisplayName(); got != c.DisplayName {
				t.Errorf("DisplayName: got = %q, want = %q", got, c.DisplayName)
			}
			if got := d.CardLastDigits(); got != c.LastDigits {
				t.Errorf("CardLastDigits: got = %q, want = %q", got, c.LastDigits)
			}
		})
	}
}

func TestPayable(t *testing.T) {
	inv := newID(core.NewID[core.InvoiceID])
	req := newID(core.NewID[core.PaymentRequestID])
	// source: tanvo
	tests := []struct {
		name string
		p    Payable
		want []string
	}{
		{"invoice", InvoicePayable(inv), []string{}},
		{"request", RequestPayable(req), []string{}},
		{"missing type", Payable{InvoiceID: inv}, []string{"payable_type: value_is_mandatory"}},
		{"invoice without id", Payable{Type: PayableTypeInvoice}, []string{"payable_id: invalid_value"}},
		{"both ids", Payable{Type: PayableTypePaymentRequest, InvoiceID: inv, PaymentRequestID: req}, []string{"payable_id: invalid_value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { checkErrors(t, tt.p.Validate(), tt.want) })
	}
	if got, want := InvoicePayable(inv).String(), "invoice:"+inv.String(); got != want {
		t.Errorf("String: got = %q, want = %q", got, want)
	}
	if got, want := RequestPayable(req).String(), "payment_request:"+req.String(); got != want {
		t.Errorf("String: got = %q, want = %q", got, want)
	}
}

func TestDueAmount(t *testing.T) {
	// source: spec/models/payment_spec.rb:230 (never below zero)
	tests := []struct {
		f    InvoiceFacts
		want int64
	}{
		{InvoiceFacts{TotalAmount: 10000}, 10000},
		{InvoiceFacts{TotalAmount: 10000, TotalPaidAmount: 3000, FinalizedOffsetAmount: 2000}, 5000},
		{InvoiceFacts{TotalAmount: 10000, TotalPaidAmount: 7000, FinalizedOffsetAmount: 4000}, 0},
	}
	for _, tt := range tests {
		if got := tt.f.DueAmount(); got != tt.want {
			t.Errorf("DueAmount(%+v): got = %d, want = %d", tt.f, got, tt.want)
		}
	}
}

func TestPaymentTransitions(t *testing.T) {
	allowed := map[[2]State]bool{
		{StatePending, StateProcessing}:   true,
		{StatePending, StateSucceeded}:    true,
		{StatePending, StateFailed}:       true,
		{StateProcessing, StateSucceeded}: true,
		{StateProcessing, StateFailed}:    true,
	}
	for _, from := range StateValues() {
		for _, to := range StateValues() {
			want := allowed[[2]State{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s): got = %t, want = %t", from, to, got, want)
			}
			p := &Payment{Status: from}
			ps := "raw"
			err := p.Transition(to, &ps, testNow)
			if want {
				if err != nil || p.Status != to || p.ProviderStatus == nil || *p.ProviderStatus != "raw" || !p.UpdatedAt.Equal(testNow) {
					t.Errorf("Transition(%s → %s): got = %v (%+v), want = nil", from, to, err, p)
				}
				if got := p.Final(); got != (to == StateSucceeded || to == StateFailed) {
					t.Errorf("Final(%s): got = %t", to, got)
				}
			} else if !errors.Is(err, core.ErrInvalidTransition) || p.Status != from {
				t.Errorf("Transition(%s → %s): got = %v, want = ErrInvalidTransition", from, to, err)
			}
		}
	}
}

func TestReceipt(t *testing.T) {
	// source: tanvo (receipt numbering, domain §15.2)
	if got, want := FormatReceiptNumber("ACM-1234", 7), "ACM-1234-RCPT-000007"; got != want {
		t.Errorf("FormatReceiptNumber: got = %q, want = %q", got, want)
	}
	checkErrors(t, (&Receipt{}).Validate(), []string{"payment_id: value_is_mandatory", "number: value_is_mandatory"})
	r := &Receipt{PaymentID: newID(core.NewID[core.PaymentID]), Number: FormatReceiptNumber("ACM", 1)}
	checkErrors(t, r.Validate(), nil)
}

func TestRefundValidate(t *testing.T) {
	type tc struct {
		Name         string   `yaml:"name"`
		Source       string   `yaml:"source"`
		Reason       string   `yaml:"reason"`
		CreditNote   bool     `yaml:"credit_note"`
		Subscription bool     `yaml:"subscription"`
		Amount       *int64   `yaml:"amount"`
		Errors       []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "refund.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			r := &Refund{
				PaymentID: newID(core.NewID[core.PaymentID]),
				Reason:    RefundReason(c.Reason),
				Amount:    100,
				Currency:  money.MustCurrency("EUR"),
				Status:    core.RefundStatusPending,
			}
			if c.Amount != nil {
				r.Amount = *c.Amount
			}
			if c.CreditNote {
				r.CreditNoteID = newID(core.NewID[core.CreditNoteID])
			}
			if c.Subscription {
				r.SubscriptionID = newID(core.NewID[core.SubscriptionID])
			}
			checkErrors(t, r.Validate(), c.Errors)
		})
	}
}

func TestRefundTransitions(t *testing.T) {
	for _, from := range core.RefundStatusValues() {
		for _, to := range core.RefundStatusValues() {
			want := from == core.RefundStatusPending && to != core.RefundStatusPending
			if got := CanRefundTransition(from, to); got != want {
				t.Errorf("CanRefundTransition(%s, %s): got = %t, want = %t", from, to, got, want)
			}
			r := &Refund{Status: from}
			if err := r.Transition(to, testNow); (err == nil) != want || (!want && !errors.Is(err, core.ErrInvalidTransition)) {
				t.Errorf("Transition(%s → %s): got = %v, want ok = %t", from, to, err, want)
			}
		}
	}
}

func TestRequestInvoices(t *testing.T) {
	type invoice struct {
		Finalized     *bool  `yaml:"finalized"`
		Overdue       *bool  `yaml:"overdue"`
		Ready         *bool  `yaml:"ready"`
		Status        string `yaml:"status"`
		Currency      string `yaml:"currency"`
		BillingEntity string `yaml:"billing_entity"`
		Due           *int64 `yaml:"due"`
	}
	type tc struct {
		Name       string    `yaml:"name"`
		Source     string    `yaml:"source"`
		Invoices   []invoice `yaml:"invoices"`
		WantAmount int64     `yaml:"want_amount"`
		NotFound   bool      `yaml:"not_found"`
		Rule       string    `yaml:"rule"`
	}
	entities := map[string]core.BillingEntityID{
		"a": newID(core.NewID[core.BillingEntityID]),
		"b": newID(core.NewID[core.BillingEntityID]),
	}
	or := func(b *bool) bool { return b == nil || *b }
	for _, c := range spectest.Load[tc](t, "request.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var invs []RequestInvoice
			for _, i := range c.Invoices {
				ri := RequestInvoice{
					ID:                        newID(core.NewID[core.InvoiceID]),
					Finalized:                 or(i.Finalized),
					Overdue:                   or(i.Overdue),
					ReadyForPaymentProcessing: or(i.Ready),
					PaymentStatus:             core.PaymentStatusPending,
					Currency:                  money.MustCurrency("EUR"),
					BillingEntityID:           entities["a"],
					TotalDueAmount:            1000,
				}
				if i.Status != "" {
					ri.PaymentStatus = core.PaymentStatus(i.Status)
				}
				if i.Currency != "" {
					ri.Currency = money.MustCurrency(i.Currency)
				}
				if i.BillingEntity != "" {
					ri.BillingEntityID = entities[i.BillingEntity]
				}
				if i.Due != nil {
					ri.TotalDueAmount = *i.Due
				}
				invs = append(invs, ri)
			}
			amount, cur, err := CheckRequestInvoices(invs)
			switch {
			case c.NotFound:
				if !errors.Is(err, core.ErrNotFound) {
					t.Errorf("err: got = %v, want = ErrNotFound", err)
				}
			case c.Rule != "":
				checkRule(t, err, c.Rule)
			default:
				if err != nil || amount != c.WantAmount || cur.Code() != "EUR" {
					t.Errorf("CheckRequestInvoices: got = %d %s %v, want = %d EUR nil", amount, cur.Code(), err, c.WantAmount)
				}
			}
		})
	}
}

func TestRequest(t *testing.T) {
	// source: spec/models/payment_request_spec.rb:31
	if got, want := SanitizeEmail(" hello@some\u200Bthing\u2013other.com "), "hello@something-other.com"; got != want {
		t.Errorf("SanitizeEmail: got = %q, want = %q", got, want)
	}
	valid := func() *Request {
		return &Request{
			CustomerID:    newID(core.NewID[core.CustomerID]),
			InvoiceIDs:    []core.InvoiceID{newID(core.NewID[core.InvoiceID])},
			Amount:        1000,
			Currency:      money.MustCurrency("EUR"),
			PaymentStatus: core.PaymentStatusPending,
		}
	}
	tests := []struct {
		name   string
		source string
		mutate func(*Request)
		want   []string
	}{
		{"valid", "spec/models/payment_request_spec.rb:38", func(*Request) {}, nil},
		{"currency missing", "spec/models/payment_request_spec.rb:47", func(r *Request) { r.Currency = money.Currency{} }, []string{"currency: currency_invalid"}},
		{"negative amount", "spec/models/payment_request_spec.rb:42", func(r *Request) { r.Amount = -1 }, []string{"amount: value_is_negative"}},
		{"bad email", "tanvo", func(r *Request) { e := "nope"; r.Email = &e }, []string{"email: invalid_email_format"}},
		{"no invoices", "tanvo", func(r *Request) { r.InvoiceIDs = nil }, []string{"invoice_ids: value_is_mandatory"}},
		{"bad method type", "tanvo", func(r *Request) { r.PaymentMethodType = "x" }, []string{"payment_method_type: invalid_value"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := valid()
			tt.mutate(r)
			checkErrors(t, r.Validate(), tt.want)
		})
	}
	// source: spec/models/payment_request_spec.rb:92
	r := valid()
	r.IncrementPaymentAttempts(testNow)
	r.IncrementPaymentAttempts(testNow)
	if r.PaymentAttempts != 2 || !r.UpdatedAt.Equal(testNow) {
		t.Errorf("PaymentAttempts: got = %d, want = 2", r.PaymentAttempts)
	}
}

func TestRequestTransitions(t *testing.T) {
	allowed := map[[2]core.PaymentStatus]bool{
		{core.PaymentStatusPending, core.PaymentStatusSucceeded}: true,
		{core.PaymentStatusPending, core.PaymentStatusFailed}:    true,
		{core.PaymentStatusFailed, core.PaymentStatusPending}:    true,
		{core.PaymentStatusFailed, core.PaymentStatusSucceeded}:  true,
	}
	for _, from := range core.PaymentStatusValues() {
		for _, to := range core.PaymentStatusValues() {
			want := allowed[[2]core.PaymentStatus{from, to}]
			if got := CanRequestTransition(from, to); got != want {
				t.Errorf("CanRequestTransition(%s, %s): got = %t, want = %t", from, to, got, want)
			}
			r := &Request{PaymentStatus: from}
			err := r.TransitionPaymentStatus(to, testNow)
			if (err == nil) != want || (!want && !errors.Is(err, core.ErrInvalidTransition)) || (want && r.PaymentStatus != to) {
				t.Errorf("TransitionPaymentStatus(%s → %s): got = %v, want ok = %t", from, to, err, want)
			}
		}
	}
}

func TestResolvePaymentMethod(t *testing.T) {
	type level struct {
		Type   string `yaml:"type"`
		Method string `yaml:"method"`
	}
	type tc struct {
		Name      string  `yaml:"name"`
		Source    string  `yaml:"source"`
		Levels    []level `yaml:"levels"`
		Want      string  `yaml:"want"`
		Automatic bool    `yaml:"automatic"`
	}
	ids := map[string]core.PaymentMethodID{"": {}}
	id := func(name string) core.PaymentMethodID {
		if v, ok := ids[name]; ok {
			return v
		}
		ids[name] = newID(core.NewID[core.PaymentMethodID])
		return ids[name]
	}
	for _, c := range spectest.Load[tc](t, "resolution.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var levels []MethodLevel
			for _, l := range c.Levels {
				levels = append(levels, MethodLevel{Type: core.PaymentMethodType(l.Type), MethodID: id(l.Method)})
			}
			got, automatic := ResolvePaymentMethod(id("default"), levels...)
			if got != id(c.Want) || automatic != c.Automatic {
				t.Errorf("ResolvePaymentMethod: got = %s %t, want = %s %t", got, automatic, id(c.Want), c.Automatic)
			}
		})
	}
}

func TestValidateMethodChoice(t *testing.T) {
	type tc struct {
		Name   string   `yaml:"name"`
		Source string   `yaml:"source"`
		Type   string   `yaml:"type"`
		ID     bool     `yaml:"id"`
		Found  bool     `yaml:"found"`
		Errors []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "method_choice.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var id core.PaymentMethodID
			if c.ID {
				id = newID(core.NewID[core.PaymentMethodID])
			}
			checkErrors(t, ValidateMethodChoice(core.PaymentMethodType(c.Type), id, c.Found), c.Errors)
		})
	}
}

func TestResolveConnection(t *testing.T) {
	def := newID(core.NewID[core.IntegrationID])
	specific := newID(core.NewID[core.IntegrationID])
	// source: tanvo (domain §15.5)
	tests := []struct {
		name   string
		levels []ConnectionLevel
		want   core.IntegrationID
	}{
		{"no levels", nil, def},
		{"inherit", []ConnectionLevel{{Mode: ConnectionModeInherit}, {}}, def},
		{"specific", []ConnectionLevel{{Mode: ConnectionModeInherit}, {Mode: ConnectionModeSpecific, IntegrationID: specific}}, specific},
		{"skip", []ConnectionLevel{{Mode: ConnectionModeSkip}, {Mode: ConnectionModeSpecific, IntegrationID: specific}}, core.IntegrationID{}},
	}
	for _, tt := range tests {
		if got := ResolveConnection(def, tt.levels...); got != tt.want {
			t.Errorf("%s: got = %s, want = %s", tt.name, got, tt.want)
		}
	}
}

func TestProviderCustomerAndMethod(t *testing.T) {
	type tc struct {
		Name             string   `yaml:"name"`
		Source           string   `yaml:"source"`
		Op               string   `yaml:"op"`
		ProviderMethodID string   `yaml:"provider_method_id"`
		ExpMonth         int      `yaml:"exp_month"`
		NoIntegration    bool     `yaml:"no_integration"`
		Errors           []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "provider_customer.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			switch c.Op {
			case "method":
				m := &Method{
					CustomerID:       newID(core.NewID[core.CustomerID]),
					ProviderMethodID: c.ProviderMethodID,
					Details:          MethodDetails{ExpMonth: c.ExpMonth},
				}
				checkErrors(t, m.Validate(), c.Errors)
			case "provider_customer":
				pc := &ProviderCustomer{CustomerID: newID(core.NewID[core.CustomerID])}
				if !c.NoIntegration {
					pc.IntegrationID = newID(core.NewID[core.IntegrationID])
				}
				checkErrors(t, pc.Validate(), c.Errors)
			default:
				t.Fatalf("op: got = %q, want = method or provider_customer", c.Op)
			}
		})
	}
}

func TestMethodDelete(t *testing.T) {
	// source: spec/models/payment_method_spec.rb:15
	m := &Method{IsDefault: true}
	if err := m.Delete(testNow); err != nil || !m.Deleted() || m.IsDefault {
		t.Errorf("Delete: got = %v (%+v), want = deleted, not default", err, m)
	}
	if err := m.Delete(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Delete twice: got = %v, want = ErrInvalidTransition", err)
	}
}

func TestIntent(t *testing.T) {
	type tc struct {
		Name         string   `yaml:"name"`
		Source       string   `yaml:"source"`
		Op           string   `yaml:"op"`
		Status       *string  `yaml:"status"`
		NoExpiration bool     `yaml:"no_expiration"`
		At           int      `yaml:"at"`
		Want         bool     `yaml:"want"`
		Errors       []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "intent.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			i := NewIntent(core.OrganizationID{}, IntentID{}, newID(core.NewID[core.InvoiceID]), testNow)
			if c.Status != nil {
				i.Status = IntentStatus(*c.Status)
			}
			if c.NoExpiration {
				i.ExpiresAt = time.Time{}
			}
			at := testNow.Add(time.Duration(c.At) * time.Hour)
			switch c.Op {
			case "validate":
				checkErrors(t, i.Validate(), c.Errors)
			case "non_expired":
				if got := i.NonExpired(at); got != c.Want {
					t.Errorf("NonExpired: got = %t, want = %t", got, c.Want)
				}
			case "awaiting_expiration":
				if got := i.AwaitingExpiration(at); got != c.Want {
					t.Errorf("AwaitingExpiration: got = %t, want = %t", got, c.Want)
				}
			default:
				t.Fatalf("op: got = %q", c.Op)
			}
		})
	}
	i := NewIntent(core.OrganizationID{}, IntentID{}, core.InvoiceID{}, testNow)
	if err := i.Expire(testNow); err != nil || i.Status != IntentStatusExpired {
		t.Errorf("Expire: got = %v %s, want = nil expired", err, i.Status)
	}
	if err := i.Expire(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Expire twice: got = %v, want = ErrInvalidTransition", err)
	}
}

func TestInboundWebhook(t *testing.T) {
	type tc struct {
		Name       string   `yaml:"name"`
		Source     string   `yaml:"source"`
		Op         string   `yaml:"op"`
		EventType  *string  `yaml:"event_type"`
		Payload    *string  `yaml:"payload"`
		Src        *string  `yaml:"src"`
		Status     *string  `yaml:"status"`
		Processing *int     `yaml:"processing"`
		At         int      `yaml:"at"`
		Want       bool     `yaml:"want"`
		Errors     []string `yaml:"errors"`
	}
	str := func(p *string, def string) string {
		if p == nil {
			return def
		}
		return *p
	}
	for _, c := range spectest.Load[tc](t, "webhook.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			w := NewInboundWebhook(core.OrganizationID{}, core.InboundWebhookID{}, str(c.Src, "stripe"), str(c.EventType, "payment_intent.succeeded"), json.RawMessage(str(c.Payload, `{"id":"evt_1"}`)), testNow)
			if c.Status != nil {
				w.Status = InboundWebhookStatus(*c.Status)
			}
			if c.Processing != nil {
				p := testNow.Add(time.Duration(*c.Processing) * time.Minute)
				w.ProcessingAt = &p
			}
			at := testNow.Add(time.Duration(c.At) * time.Minute)
			switch c.Op {
			case "validate":
				checkErrors(t, w.Validate(), c.Errors)
			case "retriable":
				if got := w.Retriable(at); got != c.Want {
					t.Errorf("Retriable: got = %t, want = %t", got, c.Want)
				}
			case "expired":
				if got := w.Expired(at); got != c.Want {
					t.Errorf("Expired: got = %t, want = %t", got, c.Want)
				}
			default:
				t.Fatalf("op: got = %q", c.Op)
			}
		})
	}
}

func TestInboundWebhookTransitions(t *testing.T) {
	allowed := map[[2]InboundWebhookStatus]bool{
		{InboundWebhookStatusPending, InboundWebhookStatusProcessing}:    true,
		{InboundWebhookStatusFailed, InboundWebhookStatusProcessing}:     true,
		{InboundWebhookStatusProcessing, InboundWebhookStatusProcessing}: true,
		{InboundWebhookStatusProcessing, InboundWebhookStatusSucceeded}:  true,
		{InboundWebhookStatusProcessing, InboundWebhookStatusFailed}:     true,
	}
	for _, from := range InboundWebhookStatusValues() {
		for _, to := range InboundWebhookStatusValues() {
			want := allowed[[2]InboundWebhookStatus{from, to}]
			if got := CanWebhookTransition(from, to); got != want {
				t.Errorf("CanWebhookTransition(%s, %s): got = %t, want = %t", from, to, got, want)
			}
			w := &InboundWebhook{Status: from}
			err := w.Transition(to, testNow)
			if (err == nil) != want || (!want && !errors.Is(err, core.ErrInvalidTransition)) {
				t.Errorf("Transition(%s → %s): got = %v, want ok = %t", from, to, err, want)
			}
		}
	}
	// source: spec/models/inbound_webhook_spec.rb:18
	w := NewInboundWebhook(core.OrganizationID{}, core.InboundWebhookID{}, "stripe", "e", json.RawMessage(`{}`), testNow)
	later := testNow.Add(time.Minute)
	if err := w.Transition(InboundWebhookStatusProcessing, later); err != nil || w.ProcessingAt == nil || !w.ProcessingAt.Equal(later) || w.Attempts != 1 {
		t.Errorf("processing: got = %v %v %d, want = processing_at set, 1 attempt", err, w.ProcessingAt, w.Attempts)
	}
}
