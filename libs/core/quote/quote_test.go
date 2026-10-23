package quote

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/internal/spectest"
)

var testNow = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

var (
	customerID = core.MustParseID[core.CustomerID]("0190a0a0-0000-7000-8000-000000000001")
	quoteID    = core.MustParseID[core.QuoteID]("0190a0a0-0000-7000-8000-000000000002")
)

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func versionID(n byte) core.QuoteVersionID {
	return core.MustParseID[core.QuoteVersionID]("0190a0a0-0000-7000-8000-0000000001" + string("0123456789"[n/10]) + string("0123456789"[n%10]))
}

func TestQuote(t *testing.T) {
	type tc struct {
		Name         string   `yaml:"name"`
		Source       string   `yaml:"source"`
		OrderType    string   `yaml:"order_type"`
		Subscription bool     `yaml:"subscription"`
		Seq          int      `yaml:"seq"`
		Year         int      `yaml:"year"`
		Number       string   `yaml:"number"`
		WantNumber   string   `yaml:"want_number"`
		Errors       []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "quote.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			q := &Quote{
				ID:         quoteID,
				Number:     c.Number,
				CustomerID: customerID,
				OrderType:  OrderType(c.OrderType),
				CreatedAt:  time.Date(c.Year, 1, 2, 0, 0, 0, 0, time.UTC),
			}
			if c.Subscription {
				q.SubscriptionID = core.MustParseID[core.SubscriptionID]("0190a0a0-0000-7000-8000-000000000003")
			}
			q.AssignNumber(c.Seq)
			if q.Number != c.WantNumber || q.SequentialID != c.Seq {
				t.Errorf("number = %q, seq = %d, want = %q, %d", q.Number, q.SequentialID, c.WantNumber, c.Seq)
			}
			if got := fieldErrors(q.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() = %v, want = %v", got, c.Errors)
			}
		})
	}
}

func TestQuoteOwners(t *testing.T) {
	m := core.MustParseID[core.MembershipID]("0190a0a0-0000-7000-8000-000000000004")
	q := &Quote{Number: "QT-2026-0001", CustomerID: customerID, OrderType: OrderTypeOneOff, Owners: []core.MembershipID{m, m, {}}}
	want := []string{"owners: invalid_value", "owners: duplicated_value"}
	if got := fieldErrors(q.Validate()); !slices.Equal(got, want) {
		t.Errorf("Validate() = %v, want = %v", got, want)
	}
}

func TestNumberPrefixes(t *testing.T) {
	f := &OrderForm{CreatedAt: testNow}
	f.AssignNumber(12)
	o := &Order{CreatedAt: testNow}
	o.AssignNumber(3)
	if f.Number != "OF-2026-0012" || o.Number != "OR-2026-0003" {
		t.Errorf("numbers = %q, %q, want = OF-2026-0012, OR-2026-0003", f.Number, o.Number)
	}
}

func TestVersionValidate(t *testing.T) {
	type tc struct {
		Name       string   `yaml:"name"`
		Source     string   `yaml:"source"`
		Status     string   `yaml:"status"`
		VoidReason string   `yaml:"void_reason"`
		VoidedAt   bool     `yaml:"voided_at"`
		ApprovedAt bool     `yaml:"approved_at"`
		Errors     []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "version.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			v := &Version{QuoteID: quoteID, Version: 1, Status: VersionStatus(c.Status), VoidReason: VersionVoidReason(c.VoidReason)}
			if c.VoidedAt {
				v.VoidedAt = &testNow
			}
			if c.ApprovedAt {
				v.ApprovedAt = &testNow
			}
			if got := fieldErrors(v.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() = %v, want = %v", got, c.Errors)
			}
		})
	}
}

func TestVersionTransitionMatrix(t *testing.T) {
	allowed := map[[2]VersionStatus]bool{
		{VersionStatusDraft, VersionStatusApproved}:  true,
		{VersionStatusDraft, VersionStatusVoided}:    true,
		{VersionStatusApproved, VersionStatusVoided}: true,
	}
	for _, from := range VersionStatusValues() {
		for _, to := range VersionStatusValues() {
			if got := CanTransitionVersion(from, to); got != allowed[[2]VersionStatus{from, to}] {
				t.Errorf("CanTransitionVersion(%s, %s) = %v, want = %v", from, to, got, !got)
			}
		}
	}
}

// Ported from lago QuoteVersions::VoidService: a draft is voidable for any
// reason, an approved version only by cascade.
func TestVersionVoid(t *testing.T) {
	for _, reason := range VersionVoidReasonValues() {
		cascade := reason == VersionVoidReasonCascadeOfExpired || reason == VersionVoidReasonCascadeOfVoided
		d := &Version{Status: VersionStatusDraft}
		if err := d.Void(reason, testNow); err != nil || d.Status != VersionStatusVoided || d.VoidReason != reason {
			t.Errorf("draft.Void(%s) = %v, status = %s", reason, err, d.Status)
		}
		a := &Version{Status: VersionStatusDraft}
		_ = a.Approve(testNow)
		err := a.Void(reason, testNow)
		if cascade {
			if err != nil || a.ApprovedAt != nil || a.VoidedAt == nil {
				t.Errorf("approved.Void(%s) = %v, approved_at = %v", reason, err, a.ApprovedAt)
			}
		} else if re, ok := errors.AsType[*core.RuleError](err); !ok || re.Code != CodeNotVoidable {
			t.Errorf("approved.Void(%s) = %v, want = %s", reason, err, CodeNotVoidable)
		}
	}
	v := &Version{Status: VersionStatusVoided}
	if err := v.Void(VersionVoidReasonManual, testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("voided.Void() = %v, want = ErrInvalidTransition", err)
	}
	if err := v.Void("lost", testNow); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Void(lost) = %v, want = ErrInvalidValue", err)
	}
	if err := v.Approve(testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("voided.Approve() = %v, want = ErrInvalidTransition", err)
	}
}

// Ported from lago QuoteVersions::CloneService.
func TestNewVersion(t *testing.T) {
	q := &Quote{ID: quoteID}
	v1 := FirstVersion(versionID(1), q, json.RawMessage(`{"plans":[]}`), testNow)
	_ = v1.Void(VersionVoidReasonManual, testNow)
	v2 := &Version{ID: versionID(2), QuoteID: quoteID, Version: 2, Status: VersionStatusDraft, BillingItems: json.RawMessage(`{"plan":"q"}`)}
	next, superseded, err := NewVersion(versionID(3), v1, []*Version{v1, v2}, testNow)
	if err != nil {
		t.Fatalf("NewVersion() = %v", err)
	}
	if next.Version != 3 || next.Status != VersionStatusDraft || string(next.BillingItems) != `{"plans":[]}` {
		t.Errorf("next = v%d %s %s, want = v3 draft cloned from v1", next.Version, next.Status, next.BillingItems)
	}
	if superseded != v2 || v2.Status != VersionStatusVoided || v2.VoidReason != VersionVoidReasonSuperseded {
		t.Errorf("superseded = %v, v2 = %s %s, want = v2 voided as superseded", superseded, v2.Status, v2.VoidReason)
	}
	if err := CheckSingleActive(next, []*Version{v1, v2}); err != nil {
		t.Errorf("CheckSingleActive() = %v, want = nil", err)
	}
	other := &Version{ID: versionID(4), QuoteID: quoteID, Status: VersionStatusDraft}
	if err := CheckSingleActive(other, []*Version{v1, v2, next}); !errors.Is(err, core.ErrNotAllowed) {
		t.Errorf("CheckSingleActive() with an active draft = %v, want = %s", err, CodeActiveVersionExists)
	}
	_ = next.Approve(testNow)
	if _, _, err := NewVersion(versionID(5), next, []*Version{v1, v2, next}, testNow); err == nil {
		t.Error("NewVersion() with an approved version = nil, want = not_clonable")
	}
}

func TestOrderForm(t *testing.T) {
	type tc struct {
		Name       string         `yaml:"name"`
		Source     string         `yaml:"source"`
		Status     string         `yaml:"status"`
		VoidReason string         `yaml:"void_reason"`
		Signed     bool           `yaml:"signed"`
		ExpiresAt  *spectest.Time `yaml:"expires_at"`
		Now        spectest.Time  `yaml:"now"`
		Timezone   string         `yaml:"timezone"`
		Expirable  bool           `yaml:"expirable"`
		Errors     []string       `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "order_form.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			loc, err := core.LoadZone(c.Timezone)
			if err != nil {
				t.Fatal(err)
			}
			f := &OrderForm{Number: "OF-2026-0001", CustomerID: customerID, QuoteVersionID: versionID(1), Status: OrderFormStatus(c.Status), VoidReason: OrderFormVoidReason(c.VoidReason)}
			if c.ExpiresAt != nil {
				f.ExpiresAt = &c.ExpiresAt.Time
			}
			if c.Signed {
				f.SignedAt = &testNow
			}
			if got := f.Expirable(c.Now.Time, loc); got != c.Expirable {
				t.Errorf("Expirable() = %v, want = %v", got, c.Expirable)
			}
			if got := fieldErrors(f.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() = %v, want = %v", got, c.Errors)
			}
		})
	}
}

func TestOrderFormTransitionMatrix(t *testing.T) {
	for _, from := range OrderFormStatusValues() {
		for _, to := range OrderFormStatusValues() {
			want := from == OrderFormStatusGenerated && to != OrderFormStatusGenerated
			if got := CanTransitionOrderForm(from, to); got != want {
				t.Errorf("CanTransitionOrderForm(%s, %s) = %v, want = %v", from, to, got, want)
			}
		}
	}
}

func approvedVersion() *Version {
	v := &Version{ID: versionID(1), QuoteID: quoteID, Version: 1, Status: VersionStatusDraft}
	_ = v.Approve(testNow)
	return v
}

// Ported from lago OrderForms::ExpireService, VoidService and
// MarkAsSignedService.
func TestOrderFormLifecycle(t *testing.T) {
	q := &Quote{ID: quoteID, CustomerID: customerID}
	if _, err := NewOrderForm(core.OrderFormID{}, q, &Version{Status: VersionStatusDraft}, nil, testNow); !errors.Is(err, core.ErrNotAllowed) {
		t.Errorf("NewOrderForm(draft) = %v, want = ErrNotAllowed", err)
	}

	v := approvedVersion()
	f, err := NewOrderForm(core.MustParseID[core.OrderFormID]("0190a0a0-0000-7000-8000-000000000010"), q, v, nil, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Expire(v, testNow); err != nil {
		t.Fatalf("Expire() = %v", err)
	}
	if f.Status != OrderFormStatusExpired || f.VoidReason != OrderFormVoidReasonExpired || f.VoidedAt == nil ||
		v.Status != VersionStatusVoided || v.VoidReason != VersionVoidReasonCascadeOfExpired {
		t.Errorf("after Expire: form %s/%s, version %s/%s, want = expired/expired, voided/cascade_of_expired", f.Status, f.VoidReason, v.Status, v.VoidReason)
	}
	if err := f.Sign("", testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Sign() when expired = %v, want = ErrInvalidTransition", err)
	}

	v = approvedVersion()
	f, _ = NewOrderForm(core.MustParseID[core.OrderFormID]("0190a0a0-0000-7000-8000-000000000011"), q, v, nil, testNow)
	if err := f.Void(OrderFormVoidReasonExpired, v, testNow); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Void(expired) = %v, want = ErrInvalidValue", err)
	}
	if err := f.Void(OrderFormVoidReasonManual, &Version{ID: versionID(9)}, testNow); err == nil {
		t.Error("Void() with another version = nil, want = an error")
	}
	if err := f.Void(OrderFormVoidReasonManual, v, testNow); err != nil || v.VoidReason != VersionVoidReasonCascadeOfVoided {
		t.Errorf("Void(manual) = %v, version reason = %s, want = cascade_of_voided", err, v.VoidReason)
	}

	v = approvedVersion()
	f, _ = NewOrderForm(core.MustParseID[core.OrderFormID]("0190a0a0-0000-7000-8000-000000000012"), q, v, nil, testNow)
	if err := f.Sign("docs/signed.pdf", testNow); err != nil || f.Status != OrderFormStatusSigned || f.SignedDocument != "docs/signed.pdf" {
		t.Fatalf("Sign() = %v, status = %s", err, f.Status)
	}
	if err := f.Expire(v, testNow); !errors.Is(err, core.ErrInvalidTransition) || v.Status != VersionStatusApproved {
		t.Errorf("Expire() when signed = %v, version = %s, want = ErrInvalidTransition and the version untouched", err, v.Status)
	}
	o, err := NewOrder(core.MustParseID[core.OrderID]("0190a0a0-0000-7000-8000-000000000013"), f, ExecutionModeExecute, nil, testNow)
	if err != nil || o.Status != OrderStatusCreated || o.CustomerID != customerID {
		t.Fatalf("NewOrder() = %v, %+v", err, o)
	}
}

func TestOrder(t *testing.T) {
	type tc struct {
		Name           string   `yaml:"name"`
		Source         string   `yaml:"source"`
		Status         string   `yaml:"status"`
		ExecutionMode  string   `yaml:"execution_mode"`
		ExecuteInHours *int     `yaml:"execute_in_hours"`
		Executable     bool     `yaml:"executable"`
		Errors         []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "order.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			o := &Order{
				Number:        "OR-2026-0001",
				CustomerID:    customerID,
				OrderFormID:   core.MustParseID[core.OrderFormID]("0190a0a0-0000-7000-8000-000000000010"),
				Status:        OrderStatus(c.Status),
				ExecutionMode: ExecutionMode(c.ExecutionMode),
			}
			if c.ExecuteInHours != nil {
				at := testNow.Add(time.Duration(*c.ExecuteInHours) * time.Hour)
				o.ExecuteAt = &at
			}
			if o.Status == OrderStatusExecuted {
				o.ExecutedAt = &testNow
			}
			if got := o.Executable(testNow); got != c.Executable {
				t.Errorf("Executable() = %v, want = %v", got, c.Executable)
			}
			if got := fieldErrors(o.Validate()); !slices.Equal(got, c.Errors) {
				t.Errorf("Validate() = %v, want = %v", got, c.Errors)
			}
		})
	}
}

func TestOrderLifecycle(t *testing.T) {
	for _, from := range OrderStatusValues() {
		for _, to := range OrderStatusValues() {
			// A failed order is executed again on request
			// (app/services/orders/base_execute_service.rb:71-82).
			want := from != OrderStatusExecuted && to != OrderStatusCreated
			if got := CanTransitionOrder(from, to); got != want {
				t.Errorf("CanTransitionOrder(%s, %s) = %v, want = %v", from, to, got, want)
			}
		}
	}
	o := &Order{Status: OrderStatusCreated}
	if err := o.Execute("execute_in_lago", nil, testNow); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Execute(unknown) = %v, want = ErrInvalidValue", err)
	}
	if err := o.Execute(ExecutionModeOrderOnly, json.RawMessage(`{"subscription_ids":[]}`), testNow); err != nil ||
		o.Status != OrderStatusExecuted || o.ExecutionMode != ExecutionModeOrderOnly || o.ExecutedAt == nil {
		t.Errorf("Execute() = %v, %+v", err, o)
	}
	if err := o.Fail(nil, testNow); !errors.Is(err, core.ErrInvalidTransition) {
		t.Errorf("Fail() when executed = %v, want = ErrInvalidTransition", err)
	}
}
