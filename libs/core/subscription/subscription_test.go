package subscription

import (
	"errors"
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

var testIDs = core.UUIDv7Generator{}

func at(hours float64) time.Time { return testNow.Add(time.Duration(hours * float64(time.Hour))) }

func atp(hours *float64) *time.Time {
	if hours == nil {
		return nil
	}
	t := at(*hours)
	return &t
}

func fieldErrors(err error) []string {
	out := []string{}
	for _, fe := range core.FieldErrors(err) {
		out = append(out, fe.String())
	}
	return out
}

func testSubscription(facts PlanFacts) *Subscription {
	s := New(core.NewID[core.OrganizationID](testIDs), core.NewID[core.SubscriptionID](testIDs), core.NewID[core.CustomerID](testIDs), core.NewID[core.PlanID](testIDs),
		"sub_1", testNow, facts, testNow)
	return s
}

func TestValidate(t *testing.T) {
	type tc struct {
		Name                      string   `yaml:"name"`
		Source                    string   `yaml:"source"`
		ExternalID                *string  `yaml:"external_id"`
		ExternalIDLength          int      `yaml:"external_id_length"`
		PlanPayInAdvance          bool     `yaml:"plan_pay_in_advance"`
		CreditNote                *string  `yaml:"credit_note"`
		Status                    *string  `yaml:"status"`
		Unstarted                 bool     `yaml:"unstarted"`
		TerminatedAt              *float64 `yaml:"terminated_at"`
		CanceledAt                *float64 `yaml:"canceled_at"`
		CancellationReason        string   `yaml:"cancellation_reason"`
		EndingAt                  *float64 `yaml:"ending_at"`
		BillingTime               *string  `yaml:"billing_time"`
		PaymentMethodType         *string  `yaml:"payment_method_type"`
		PaymentMethodID           bool     `yaml:"payment_method_id"`
		PurchaseOrderNumberLength int      `yaml:"purchase_order_number_length"`
		NameLength                int      `yaml:"name_length"`
		DuplicateSections         bool     `yaml:"duplicate_sections"`
		Errors                    []string `yaml:"errors"`
	}
	for _, c := range spectest.Load[tc](t, "subscription.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			facts := PlanFacts{PayInAdvance: c.PlanPayInAdvance}
			s := testSubscription(PlanFacts{})
			s.Status = StatusActive
			s.StartedAt = &testNow
			if c.ExternalID != nil {
				s.ExternalID = *c.ExternalID
			}
			if c.ExternalIDLength > 0 {
				s.ExternalID = strings.Repeat("a", c.ExternalIDLength)
			}
			if c.CreditNote != nil {
				s.OnTerminationCreditNote = TerminationCreditNote(*c.CreditNote)
			}
			if c.Status != nil {
				s.Status = Status(*c.Status)
			}
			if c.Unstarted {
				s.StartedAt = nil
			}
			s.TerminatedAt = atp(c.TerminatedAt)
			s.CanceledAt = atp(c.CanceledAt)
			s.CancellationReason = CancellationReason(c.CancellationReason)
			s.EndingAt = atp(c.EndingAt)
			if c.BillingTime != nil {
				bt, err := period.ParseBillingTime(*c.BillingTime)
				if err != nil {
					bt = 0 // not a billing time: the case under test
				}
				s.BillingTime = bt
			}
			if c.PaymentMethodType != nil {
				s.PaymentMethodType = core.PaymentMethodType(*c.PaymentMethodType)
			}
			if c.PaymentMethodID {
				s.PaymentMethodID = core.NewID[core.PaymentMethodID](testIDs)
			}
			if c.PurchaseOrderNumberLength > 0 {
				po := strings.Repeat("p", c.PurchaseOrderNumberLength)
				s.PurchaseOrderNumber = &po
			}
			if c.NameLength > 0 {
				n := strings.Repeat("n", c.NameLength)
				s.Name = &n
			}
			if c.DuplicateSections {
				id := core.NewID[core.InvoiceCustomSectionID](testIDs)
				s.InvoiceCustomSectionIDs = []core.InvoiceCustomSectionID{id, core.NewID[core.InvoiceCustomSectionID](testIDs), id}
			}
			got := append(fieldErrors(s.Validate()), fieldErrors(s.ValidateForPlan(facts))...)
			if !slices.Equal(got, c.Errors) {
				t.Errorf("errors: got = %v, want = %v", got, c.Errors)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	s := testSubscription(PlanFacts{PayInAdvance: true})
	if s.Status != StatusPending || !s.ConsolidateInvoice || s.OnTerminationInvoice != TerminationInvoiceGenerate ||
		s.PaymentMethodType != core.PaymentMethodTypeProvider || s.OnTerminationCreditNote != TerminationCreditNoteCredit {
		t.Errorf("New: got = %+v, want = the domain §9.1 defaults", s)
	}
	if a := testSubscription(PlanFacts{}); a.OnTerminationCreditNote != "" {
		t.Errorf("arrears credit note: got = %q, want = \"\"", a.OnTerminationCreditNote)
	}
	for _, c := range []struct {
		at   float64
		want Status
	}{{-1, StatusActive}, {0, StatusActive}, {1, StatusPending}} {
		if got := InitialStatus(at(c.at), testNow); got != c.want {
			t.Errorf("InitialStatus(%v): got = %s, want = %s", c.at, got, c.want)
		}
	}
}

func TestPredicates(t *testing.T) {
	type tc struct {
		Name                   string   `yaml:"name"`
		Source                 string   `yaml:"source"`
		Status                 string   `yaml:"status"`
		HasPrevious            bool     `yaml:"has_previous"`
		StartingInTheFuture    *bool    `yaml:"starting_in_the_future"`
		SubName                *string  `yaml:"sub_name"`
		PlanName               string   `yaml:"plan_name"`
		PlanInvoiceDisplayName *string  `yaml:"plan_invoice_display_name"`
		DisplayName            string   `yaml:"display_name"`
		InvoiceName            string   `yaml:"invoice_name"`
		TerminatedAt           *float64 `yaml:"terminated_at"`
		At                     *float64 `yaml:"at"`
		TerminatedBy           *bool    `yaml:"terminated_by"`
		OwnBillingEntity       bool     `yaml:"own_billing_entity"`
		BillingEntity          string   `yaml:"billing_entity"`
	}
	for _, c := range spectest.Load[tc](t, "predicates.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			s := testSubscription(PlanFacts{})
			if c.Status != "" {
				s.Status = Status(c.Status)
			}
			if c.HasPrevious {
				s.PreviousSubscriptionID = core.NewID[core.SubscriptionID](testIDs)
			}
			s.Name = c.SubName
			s.TerminatedAt = atp(c.TerminatedAt)
			if c.StartingInTheFuture != nil {
				if got := s.StartingInTheFuture(); got != *c.StartingInTheFuture {
					t.Errorf("StartingInTheFuture: got = %v, want = %v", got, *c.StartingInTheFuture)
				}
			}
			if c.DisplayName != "" {
				if got := s.DisplayName(c.PlanName); got != c.DisplayName {
					t.Errorf("DisplayName: got = %q, want = %q", got, c.DisplayName)
				}
				if got := s.InvoiceName(c.PlanName, c.PlanInvoiceDisplayName); got != c.InvoiceName {
					t.Errorf("InvoiceName: got = %q, want = %q", got, c.InvoiceName)
				}
			}
			if c.TerminatedBy != nil {
				if got := s.TerminatedBy(at(*c.At)); got != *c.TerminatedBy {
					t.Errorf("TerminatedBy: got = %v, want = %v", got, *c.TerminatedBy)
				}
			}
			if c.BillingEntity != "" {
				own, customer := core.NewID[core.BillingEntityID](testIDs), core.NewID[core.BillingEntityID](testIDs)
				want := customer
				if c.OwnBillingEntity {
					s.BillingEntityID = own
				}
				if c.BillingEntity == "own" {
					want = own
				}
				if got := s.ApplicableBillingEntity(customer); got != want {
					t.Errorf("ApplicableBillingEntity: got = %v, want = %v", got, want)
				}
			}
		})
	}
}

func TestCanTransition(t *testing.T) {
	allowed := map[Status][]Status{
		StatusPending:    {StatusIncomplete, StatusActive, StatusCanceled},
		StatusIncomplete: {StatusActive, StatusCanceled},
		StatusActive:     {StatusTerminated},
	}
	for _, from := range StatusValues() {
		for _, to := range StatusValues() {
			want := slices.Contains(allowed[from], to)
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s): got = %v, want = %v", from, to, got, want)
			}
		}
	}
}

func TestTerminationFor(t *testing.T) {
	want := map[Status]TerminationAction{
		StatusPending: TerminationCancel, StatusIncomplete: TerminationDecline, StatusActive: TerminationTerminate,
		StatusTerminated: TerminationRefused, StatusCanceled: TerminationRefused,
	}
	for _, st := range StatusValues() {
		if got := TerminationFor(st); got != want[st] {
			t.Errorf("TerminationFor(%s): got = %v, want = %v", st, got, want[st])
		}
	}
}

func TestLifecycle(t *testing.T) {
	type tc struct {
		Name        string   `yaml:"name"`
		Source      string   `yaml:"source"`
		From        string   `yaml:"from"`
		Started     *float64 `yaml:"started"`
		HasPrevious bool     `yaml:"has_previous"`
		TrialEnded  bool     `yaml:"trial_ended"`
		Action      string   `yaml:"action"`
		Start       float64  `yaml:"start"`
		Error       bool     `yaml:"error"`
		Status      string   `yaml:"status"`
		StartedAt   *float64 `yaml:"started_at"`
	}
	for _, c := range spectest.Load[tc](t, "lifecycle.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			s := testSubscription(PlanFacts{})
			s.Status = Status(c.From)
			s.StartedAt = atp(c.Started)
			if c.HasPrevious {
				s.PreviousSubscriptionID = core.NewID[core.SubscriptionID](testIDs)
			}
			if c.TrialEnded {
				s.TrialEndedAt = &testNow
			}
			var err error
			switch c.Action {
			case "activate":
				err = s.Activate(at(c.Start), testNow)
			case "incomplete":
				err = s.MarkIncomplete(testNow)
			case "cancel":
				err = s.Cancel(testNow, CancellationReasonManual)
			case "terminate":
				err = s.Terminate(testNow)
			case "trial_ended":
				err = s.TrialEnded(testNow)
			default:
				t.Fatalf("action %q", c.Action)
			}
			if c.Error {
				var te *core.TransitionError
				if !errors.As(err, &te) || !errors.Is(err, core.ErrInvalidTransition) {
					t.Errorf("error: got = %v, want = a *core.TransitionError", err)
				}
			} else if err != nil {
				t.Fatalf("error: got = %v, want = nil", err)
			}
			if s.Status != Status(c.Status) {
				t.Errorf("status: got = %s, want = %s", s.Status, c.Status)
			}
			if want := atp(c.StartedAt); (s.StartedAt == nil) != (want == nil) || (want != nil && !s.StartedAt.Equal(*want)) {
				t.Errorf("started_at: got = %v, want = %v", s.StartedAt, want)
			}
			if !c.Error {
				switch c.Action {
				case "cancel":
					if s.CanceledAt == nil || s.CancellationReason != CancellationReasonManual {
						t.Errorf("cancel: got = %v %q, want = now manual", s.CanceledAt, s.CancellationReason)
					}
				case "terminate":
					if s.TerminatedAt == nil || !s.TerminatedAt.Equal(testNow) {
						t.Errorf("terminated_at: got = %v, want = %v", s.TerminatedAt, testNow)
					}
				case "trial_ended":
					if s.TrialEndedAt == nil {
						t.Errorf("trial_ended_at: got = nil, want = %v", testNow)
					}
				}
				if err := s.Validate(); err != nil {
					t.Errorf("Validate after %s: got = %v, want = nil", c.Action, err)
				}
			}
		})
	}
	s := testSubscription(PlanFacts{})
	if err := s.Cancel(testNow, "bogus"); !errors.Is(err, core.ErrInvalidValue) {
		t.Errorf("Cancel with a bogus reason: got = %v, want = core.ErrInvalidValue", err)
	}
}

func TestCheckEditable(t *testing.T) {
	for _, st := range StatusValues() {
		for _, f := range EditableFieldValues() {
			want := st == StatusPending || (st == StatusActive && f != EditableFieldSubscriptionAt)
			err := CheckEditable(&Subscription{Status: st}, f)
			if got := err == nil; got != want {
				t.Errorf("CheckEditable(%s, %s): got = %v, want editable = %v", st, f, err, want)
			}
			if err != nil && !slices.Equal(fieldErrors(err), []string{string(f) + ": " + core.CodeImmutable}) {
				t.Errorf("CheckEditable(%s, %s) errors: got = %v, want = [%s: %s]", st, f, fieldErrors(err), f, core.CodeImmutable)
			}
		}
	}
}

func TestGrouping(t *testing.T) {
	usd, eur := money.MustCurrency("USD"), money.MustCurrency("EUR")
	entity := core.NewID[core.BillingEntityID](testIDs)
	a, b := testSubscription(PlanFacts{}), testSubscription(PlanFacts{})
	b.CustomerID = a.CustomerID
	if Grouping(a, usd, entity) != Grouping(b, usd, entity) {
		t.Errorf("same settings: got = different keys, want = same")
	}
	if Grouping(a, usd, entity) == Grouping(b, eur, entity) {
		t.Errorf("different currencies: got = same key, want = different")
	}
	po := "PO-1"
	b.PurchaseOrderNumber = &po
	if Grouping(a, usd, entity) == Grouping(b, usd, entity) {
		t.Errorf("different purchase orders: got = same key, want = different")
	}
	b.PurchaseOrderNumber = nil
	b.BillingEntityID = core.NewID[core.BillingEntityID](testIDs)
	if Grouping(a, usd, entity) == Grouping(b, usd, entity) {
		t.Errorf("different billing entities: got = same key, want = different")
	}
	b.BillingEntityID = core.BillingEntityID{}
	b.ConsolidateInvoice = false
	if Grouping(a, usd, entity) == Grouping(b, usd, entity) {
		t.Errorf("not consolidated: got = same key, want = different")
	}
}

func TestNextSubscriptionAndChain(t *testing.T) {
	// spec/models/subscription_spec.rb:1170: the most recent non-canceled
	// next subscription.
	older, newer, canceled := testSubscription(PlanFacts{}), testSubscription(PlanFacts{}), testSubscription(PlanFacts{})
	older.CreatedAt, newer.CreatedAt, canceled.CreatedAt = at(-2), at(-1), at(0)
	canceled.Status = StatusCanceled
	if got := NextSubscription([]*Subscription{older, newer, canceled}); got != newer {
		t.Errorf("NextSubscription: got = %v, want = %v", got, newer)
	}
	if got := NextSubscription(nil); got != nil {
		t.Errorf("NextSubscription(nil): got = %v, want = nil", got)
	}
	// spec/models/subscription_spec.rb:778: the chain starts at the first
	// started subscription.
	first, second := testSubscription(PlanFacts{}), testSubscription(PlanFacts{})
	first.StartedAt, second.StartedAt = atp(new(float64(-48))), atp(new(float64(-1)))
	if got := ChainStart([]*Subscription{first, second}); !got.Equal(at(-48)) {
		t.Errorf("ChainStart: got = %v, want = %v", got, at(-48))
	}
}

func TestApplicableUsageThresholds(t *testing.T) {
	own, plan, parent := []string{"own"}, []string{"plan"}, []string{"parent"}
	cases := []struct {
		name, source            string
		disabled                bool
		own, plan, parent, want []string
	}{
		{"own thresholds", "spec/models/subscription_spec.rb:1204", false, own, plan, parent, own},
		{"own thresholds disabled", "spec/models/subscription_spec.rb:1209", true, own, plan, parent, nil},
		{"plan thresholds", "spec/models/subscription_spec.rb:1217", false, nil, plan, parent, plan},
		{"plan thresholds disabled", "spec/models/subscription_spec.rb:1223", true, nil, plan, parent, nil},
		{"parent plan thresholds", "spec/models/subscription_spec.rb:1234", false, nil, nil, parent, parent},
		{"none", "spec/models/subscription_spec.rb:1242", false, nil, nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Subscription{ProgressiveBillingDisabled: c.disabled}
			if got := ApplicableUsageThresholds(s, c.own, c.plan, c.parent); !slices.Equal(got, c.want) {
				t.Errorf("%s: got = %v, want = %v", c.source, got, c.want)
			}
		})
	}
}

func TestMatchEvent(t *testing.T) {
	type sub struct {
		Status       string   `yaml:"status"`
		StartedAt    *float64 `yaml:"started_at"`
		TerminatedAt *float64 `yaml:"terminated_at"`
	}
	type tc struct {
		Name          string  `yaml:"name"`
		Source        string  `yaml:"source"`
		At            float64 `yaml:"at"`
		Recurring     bool    `yaml:"recurring"`
		Subscriptions []sub   `yaml:"subscriptions"`
		Match         int     `yaml:"match"`
	}
	for _, c := range spectest.Load[tc](t, "match_event.yaml") {
		t.Run(c.Name, func(t *testing.T) {
			var subs []*Subscription
			for _, x := range c.Subscriptions {
				s := testSubscription(PlanFacts{})
				s.Status, s.StartedAt, s.TerminatedAt = Status(x.Status), atp(x.StartedAt), atp(x.TerminatedAt)
				subs = append(subs, s)
			}
			var want *Subscription
			if c.Match >= 0 {
				want = subs[c.Match]
			}
			if got := MatchEvent(subs, at(c.At), c.Recurring); got != want {
				t.Errorf("MatchEvent: got = %v, want = %v", got, want)
			}
		})
	}
}
