package contracts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/contracts"
	"github.com/tanvoxyz/tanvo/libs/billing/products"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/money"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// now is the clock of the tests.
var now = billingtest.Date(2026, time.January, 1)

// fixture is a customer, the contracts service and the catalog.
type fixture struct {
	h     *billingtest.Harness
	svc   *contracts.Service
	cards *products.Service
	cust  *customer.Customer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := billingtest.New(t, now)
	svc, err := contracts.New(h.Deps)
	if err != nil {
		t.Fatalf("contracts.New() error = %v", err)
	}
	cards, err := products.New(h.Deps)
	if err != nil {
		t.Fatalf("products.New() error = %v", err)
	}
	return &fixture{h: h, svc: svc, cards: cards, cust: h.Customer()}
}

// card creates a rate card of a new fixed product in the currency, with a
// monthly standard rate in force from now, in arrears without proration.
func (f *fixture) card(t *testing.T, code string, cur money.Currency, opts ...func(*products.RateCardInput)) *catalog.RateCard {
	t.Helper()
	p, err := f.cards.CreateProduct(t.Context(), f.h.OrgID(), products.ProductInput{Code: code, Name: code, ProductType: catalog.ProductTypeFixed})
	if err != nil {
		t.Fatalf("CreateProduct() error = %v", err)
	}
	in := products.RateCardInput{
		Product: refs.ByID(p.ID), Code: code, Name: code, Currency: cur,
		Rates: []products.RateInput{{
			Code: "base", EffectiveFrom: f.h.Now(), RateModel: catalog.ChargeModelStandard,
			Properties: catalog.MustProperties(`{"amount":"10"}`), BillingIntervalUnit: catalog.IntervalUnitMonth,
		}},
	}
	for _, o := range opts {
		o(&in)
	}
	c, err := f.cards.CreateRateCard(t.Context(), f.h.OrgID(), in)
	if err != nil {
		t.Fatalf("CreateRateCard() error = %v", err)
	}
	return c
}

// create creates the contract "contract-1" of the customer with the
// options.
func (f *fixture) create(t *testing.T, opts ...func(*contracts.CreateInput)) (*contracts.View, error) {
	t.Helper()
	in := contracts.CreateInput{ExternalCustomerID: f.cust.ExternalID, ExternalID: "contract-1"}
	for _, o := range opts {
		o(&in)
	}
	return f.svc.Create(t.Context(), f.h.OrgID(), in)
}

// mustCreate is create failing the test on error.
func (f *fixture) mustCreate(t *testing.T, opts ...func(*contracts.CreateInput)) *contracts.View {
	t.Helper()
	v, err := f.create(t, opts...)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return v
}

// startingIn starts the contract d after now.
func startingIn(d time.Duration) func(*contracts.CreateInput) {
	return func(in *contracts.CreateInput) {
		at := now.Add(d)
		in.StartedAt = &at
	}
}

// paymentMethod stores a payment method of the customer.
func (f *fixture) paymentMethod(c *customer.Customer) *payment.Method {
	m := &payment.Method{
		ID: core.NewID[core.PaymentMethodID](f.h.IDs), OrganizationID: f.h.OrgID(), CustomerID: c.ID,
		ProviderMethodID: "pm_" + c.ID.String()[30:], CreatedAt: f.h.Now(), UpdatedAt: f.h.Now(),
	}
	f.h.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentMethods.Create(ctx, m) })
	return m
}

func wantFieldError(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *core.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a validation error", err)
	}
	if !ve.Has(field, code) {
		t.Fatalf("errors = %v, want %s %s", ve.Errors, field, code)
	}
}

func wantNotFound(t *testing.T, err error, resource string) {
	t.Helper()
	var nf *core.NotFoundError
	if !errors.As(err, &nf) || nf.Resource != resource {
		t.Fatalf("error = %v, want %s_not_found", err, resource)
	}
}

func TestCreate(t *testing.T) {
	t.Parallel()
	// source: spec/services/contracts/create_service_spec.rb:20
	t.Run("creates an active contract on the plan with its rate cards", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		rc := f.card(t, "seats", billingtest.USD)
		plan := f.h.Plan(period.Monthly, 0)
		units := money.NewFromInt(5)
		if _, err := f.cards.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), products.PlanRateCardInput{RateCard: refs.ByID(rc.ID), Units: &units}); err != nil {
			t.Fatalf("CreatePlanRateCard() error = %v", err)
		}
		v := f.mustCreate(t, func(in *contracts.CreateInput) { in.PlanCode = plan.Code })
		c := v.Contract
		if !c.Contract || c.PlanID != plan.ID || c.CustomerID != f.cust.ID || c.ExternalID != "contract-1" ||
			c.Status != subscription.StatusActive || c.BillingTime != period.Calendar || !c.SubscriptionAt.Equal(now) {
			t.Errorf("contract = %+v, want an active calendar contract on the plan from now", c)
		}
		if len(v.RateCards) != 1 || v.RateCards[0].RateCard.ID != rc.ID || v.RateCards[0].Card.Units == nil || v.RateCards[0].Card.Units.Cmp(units) != 0 {
			t.Errorf("rate cards = %+v, want = seats with 5 units", v.RateCards)
		}
	})
	// source: spec/services/contracts/create_service_spec.rb:72
	t.Run("creates a contract without a plan or rate cards", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		v := f.mustCreate(t)
		if !v.Contract.PlanID.IsZero() || v.Plan != nil || len(v.RateCards) != 0 {
			t.Errorf("contract = %+v, want no plan and no cards", v)
		}
	})
	// source: spec/services/contracts/create_service_spec.rb:83
	t.Run("creates a pending contract starting later", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		v := f.mustCreate(t, startingIn(30*24*time.Hour))
		if v.Contract.Status != subscription.StatusPending || !v.Contract.SubscriptionAt.Equal(now.Add(30*24*time.Hour)) {
			t.Errorf("status, start = %s, %s, want = pending in a month", v.Contract.Status, v.Contract.SubscriptionAt)
		}
	})
	// source: spec/services/contracts/create_service_spec.rb:99
	t.Run("stores the window and the anchor", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		start, end := billingtest.Date(2026, time.October, 1), billingtest.Date(2027, time.October, 1)
		anchor := core.DateOf(billingtest.Date(2026, time.October, 15), time.UTC)
		v := f.mustCreate(t, func(in *contracts.CreateInput) {
			in.StartedAt, in.EndedAt, in.BillingAnchorDate = &start, &end, &anchor
		})
		c := v.Contract
		if !c.SubscriptionAt.Equal(start) || c.EndingAt == nil || !c.EndingAt.Equal(end) || c.BillingAnchorDate == nil || *c.BillingAnchorDate != anchor {
			t.Errorf("window, anchor = %s, %v, %v, want = %s, %s, %s", c.SubscriptionAt, c.EndingAt, c.BillingAnchorDate, start, end, anchor)
		}
		if v.EffectiveBillingAnchorDate != anchor {
			t.Errorf("effective anchor = %s, want = %s", v.EffectiveBillingAnchorDate, anchor)
		}
	})
	// source: spec/services/contracts/create_service_spec.rb:144
	t.Run("rejects a window already closed", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		start, end := now.AddDate(0, -2, 0), now.AddDate(0, -1, 0)
		_, err := f.create(t, func(in *contracts.CreateInput) { in.StartedAt, in.EndedAt = &start, &end })
		wantFieldError(t, err, "ended_at", subscription.CodeAlreadyEnded)
	})
	// source: spec/services/contracts/create_service_spec.rb:153
	// source: spec/services/contracts/create_service_spec.rb:162
	t.Run("rejects an unknown customer or plan", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		_, err := f.create(t, func(in *contracts.CreateInput) { in.ExternalCustomerID = "unknown" })
		wantNotFound(t, err, "customer")
		_, err = f.create(t, func(in *contracts.CreateInput) { in.PlanCode = "unknown" })
		wantNotFound(t, err, "plan")
	})
	// source: spec/services/contracts/create_service_spec.rb:171
	// source: spec/services/contracts/create_service_spec.rb:180
	t.Run("rejects the external id of a live contract, not of a terminated one", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t)
		_, err := f.create(t)
		wantFieldError(t, err, "external_id", contracts.CodeValueAlreadyExists)
		if _, err := f.svc.Terminate(t.Context(), f.h.OrgID(), "contract-1"); err != nil {
			t.Fatalf("Terminate() error = %v", err)
		}
		f.mustCreate(t)
	})
	// Tanvo routes events by external ID for contracts and subscriptions
	// alike, so they share one namespace.
	t.Run("rejects the external id of a live subscription", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.h.Subscription(f.cust, f.h.Plan(period.Monthly, 0), func(s *subscription.Subscription) { s.ExternalID = "contract-1" })
		_, err := f.create(t)
		wantFieldError(t, err, "external_id", contracts.CodeValueAlreadyExists)
	})
	// source: spec/services/contracts/create_service_spec.rb:217
	// source: spec/services/contracts/create_service_spec.rb:258
	t.Run("stores the settings, defaulting the omitted ones", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		if c := f.mustCreate(t).Contract; !c.ConsolidateInvoice || c.PaymentMethodType != core.PaymentMethodTypeProvider || !c.BillingEntityID.IsZero() {
			t.Errorf("defaults = %t, %s, %s, want = true, provider, none", c.ConsolidateInvoice, c.PaymentMethodType, c.BillingEntityID)
		}
		pm := f.paymentMethod(f.cust)
		provider, no, po := core.PaymentMethodTypeProvider, false, "PO-42"
		c := f.mustCreate(t, func(in *contracts.CreateInput) {
			in.ExternalID = "contract-2"
			in.ConsolidateInvoice = &no
			in.PurchaseOrderNumber = core.Some(&po)
			in.PaymentMethod = &contracts.PaymentMethod{Type: &provider, ID: &pm.ID}
		}).Contract
		if c.ConsolidateInvoice || c.PurchaseOrderNumber == nil || *c.PurchaseOrderNumber != po || c.PaymentMethodID != pm.ID || c.PaymentMethodType != provider {
			t.Errorf("settings = %+v, want = not consolidated, PO-42, the payment method", c)
		}
	})
	// source: spec/services/contracts/create_service_spec.rb:230
	t.Run("rejects a manual payment method type with a payment method", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pm := f.paymentMethod(f.cust)
		manual := core.PaymentMethodTypeManual
		_, err := f.create(t, func(in *contracts.CreateInput) {
			in.PaymentMethod = &contracts.PaymentMethod{Type: &manual, ID: &pm.ID}
		})
		wantFieldError(t, err, "payment_method", contracts.CodeInvalidPaymentMethod)
	})
	// source: spec/services/contracts/create_service_spec.rb:250
	t.Run("rejects another customer's payment method", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		pm := f.paymentMethod(f.h.Customer())
		_, err := f.create(t, func(in *contracts.CreateInput) { in.PaymentMethod = &contracts.PaymentMethod{ID: &pm.ID} })
		wantNotFound(t, err, contracts.ResourcePaymentMethod)
	})
	// source: spec/services/contracts/create_service_spec.rb:239
	t.Run("rejects an unknown billing entity", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		_, err := f.create(t, func(in *contracts.CreateInput) {
			in.BillingEntity = core.Some(core.NewID[core.BillingEntityID](f.h.IDs))
		})
		wantNotFound(t, err, contracts.ResourceBillingEntity)
	})
}

func TestTerminate(t *testing.T) {
	t.Parallel()
	// source: spec/services/contracts/terminate_service_spec.rb:13
	t.Run("terminates an active contract", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t)
		v, err := f.svc.Terminate(t.Context(), f.h.OrgID(), "contract-1")
		if err != nil {
			t.Fatalf("Terminate() error = %v", err)
		}
		c := v.Contract
		if c.Status != subscription.StatusTerminated || c.TerminatedAt == nil || !c.TerminatedAt.Equal(now) || c.CanceledAt != nil {
			t.Errorf("contract = %s, %v, %v, want = terminated now", c.Status, c.TerminatedAt, c.CanceledAt)
		}
	})
	// source: spec/services/contracts/terminate_service_spec.rb:35
	t.Run("cancels a pending contract", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t, startingIn(24*time.Hour))
		v, err := f.svc.Terminate(t.Context(), f.h.OrgID(), "contract-1")
		if err != nil {
			t.Fatalf("Terminate() error = %v", err)
		}
		c := v.Contract
		if c.Status != subscription.StatusCanceled || c.CanceledAt == nil || !c.CanceledAt.Equal(now) || c.TerminatedAt != nil {
			t.Errorf("contract = %s, %v, %v, want = canceled now", c.Status, c.CanceledAt, c.TerminatedAt)
		}
	})
	// source: spec/services/contracts/terminate_service_spec.rb:47
	// source: spec/services/contracts/terminate_service_spec.rb:65
	t.Run("rejects a terminated contract and a missing one", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t)
		if _, err := f.svc.Terminate(t.Context(), f.h.OrgID(), "contract-1"); err != nil {
			t.Fatalf("Terminate() error = %v", err)
		}
		_, err := f.svc.Terminate(t.Context(), f.h.OrgID(), "contract-1")
		if err == nil {
			t.Fatal("Terminate() error = nil, want an error")
		}
		_, err = f.svc.Terminate(t.Context(), f.h.OrgID(), "missing")
		wantNotFound(t, err, contracts.Resource)
	})
}

func TestCreateCard(t *testing.T) {
	t.Parallel()
	pending := startingIn(30 * 24 * time.Hour)
	// source: spec/services/contract_rate_cards/create_service_spec.rb:15
	t.Run("applies the rate card with a default phase", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		v := f.mustCreate(t, pending)
		// Without a rate the card has no schedule, so its clock stays at
		// the contract's start.
		rc := f.card(t, "seats", billingtest.USD, func(in *products.RateCardInput) { in.Rates = nil })
		units := money.NewFromInt(10)
		cv, err := f.svc.CreateCard(t.Context(), f.h.OrgID(), "contract-1", contracts.CardInput{RateCardCode: "seats", Units: &units})
		if err != nil {
			t.Fatalf("CreateCard() error = %v", err)
		}
		card, start := cv.Card, v.Contract.SubscriptionAt
		if card.RateCardID != rc.ID || card.Units.Cmp(units) != 0 || card.EffectiveDate != core.DateOf(start, time.UTC) ||
			card.BillingAnchorDate != v.EffectiveBillingAnchorDate || card.NextBillingAt == nil || !card.NextBillingAt.Equal(start) {
			t.Errorf("card = %+v, want seats from the contract's start", card)
		}
		if len(cv.Phases) != 1 || cv.Phases[0].Code != "default" {
			t.Errorf("phases = %+v, want = [default]", cv.Phases)
		}
	})
	// source: spec/services/contract_rate_cards/create_service_spec.rb:31
	t.Run("uses the card's own anchor", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t, pending)
		f.card(t, "seats", billingtest.USD)
		anchor := core.DateOf(billingtest.Date(2026, time.March, 1), time.UTC)
		cv, err := f.svc.CreateCard(t.Context(), f.h.OrgID(), "contract-1", contracts.CardInput{RateCardCode: "seats", BillingAnchorDate: &anchor})
		if err != nil {
			t.Fatalf("CreateCard() error = %v", err)
		}
		if cv.Card.BillingAnchorDate != anchor {
			t.Errorf("anchor = %s, want = %s", cv.Card.BillingAnchorDate, anchor)
		}
	})
	// source: spec/services/contract_rate_cards/create_service_spec.rb:62
	t.Run("refuses an active contract", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t)
		f.card(t, "seats", billingtest.USD)
		_, err := f.svc.CreateCard(t.Context(), f.h.OrgID(), "contract-1", contracts.CardInput{RateCardCode: "seats"})
		wantFieldError(t, err, "contract", subscription.CodeContractLocked)
	})
	// source: spec/services/contract_rate_cards/create_service_spec.rb:71
	t.Run("rejects an unknown rate card", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t, pending)
		_, err := f.svc.CreateCard(t.Context(), f.h.OrgID(), "contract-1", contracts.CardInput{RateCardCode: "unknown"})
		wantNotFound(t, err, contracts.ResourceRateCard)
	})
	// source: spec/services/contract_rate_cards/create_service_spec.rb:80
	t.Run("rejects a card in another currency than the customer's", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t, pending)
		f.card(t, "seats", money.MustCurrency("EUR"))
		_, err := f.svc.CreateCard(t.Context(), f.h.OrgID(), "contract-1", contracts.CardInput{RateCardCode: "seats"})
		wantFieldError(t, err, "currency", contracts.CodeCurrencyDoesNotMatch)
	})
	// source: spec/services/contract_rate_cards/create_service_spec.rb:97
	t.Run("rejects a second card of the same product", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t, pending)
		first := f.card(t, "seats", billingtest.USD)
		if _, err := f.svc.CreateCard(t.Context(), f.h.OrgID(), "contract-1", contracts.CardInput{RateCardCode: "seats"}); err != nil {
			t.Fatalf("CreateCard() error = %v", err)
		}
		if _, err := f.cards.CreateRateCard(t.Context(), f.h.OrgID(), products.RateCardInput{
			Product: refs.ByID(first.ProductID), Code: "seats-2", Name: "seats-2", Currency: billingtest.USD,
		}); err != nil {
			t.Fatalf("CreateRateCard() error = %v", err)
		}
		_, err := f.svc.CreateCard(t.Context(), f.h.OrgID(), "contract-1", contracts.CardInput{RateCardCode: "seats-2"})
		wantFieldError(t, err, "rate_card", catalog.CodeProductAlreadyPriced)
	})
}

func TestDeleteCard(t *testing.T) {
	t.Parallel()
	// source: spec/services/contract_rate_cards/destroy_service_spec.rb:13
	// source: spec/services/contract_rate_cards/destroy_service_spec.rb:47
	t.Run("discards the card, then finds it no more", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.mustCreate(t, startingIn(24*time.Hour))
		f.card(t, "seats", billingtest.USD)
		if _, err := f.svc.CreateCard(t.Context(), f.h.OrgID(), "contract-1", contracts.CardInput{RateCardCode: "seats"}); err != nil {
			t.Fatalf("CreateCard() error = %v", err)
		}
		if _, err := f.svc.DeleteCard(t.Context(), f.h.OrgID(), "contract-1", "seats"); err != nil {
			t.Fatalf("DeleteCard() error = %v", err)
		}
		_, err := f.svc.Card(t.Context(), f.h.OrgID(), "contract-1", "seats")
		wantNotFound(t, err, contracts.ResourceAppliedRateCard)
		_, err = f.svc.DeleteCard(t.Context(), f.h.OrgID(), "contract-1", "seats")
		wantNotFound(t, err, contracts.ResourceAppliedRateCard)
	})
	// source: spec/services/contract_rate_cards/destroy_service_spec.rb:26
	t.Run("refuses an active contract", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		rc := f.card(t, "seats", billingtest.USD)
		plan := f.h.Plan(period.Monthly, 0)
		if _, err := f.cards.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), products.PlanRateCardInput{RateCard: refs.ByID(rc.ID)}); err != nil {
			t.Fatalf("CreatePlanRateCard() error = %v", err)
		}
		f.mustCreate(t, func(in *contracts.CreateInput) { in.PlanCode = plan.Code })
		_, err := f.svc.DeleteCard(t.Context(), f.h.OrgID(), "contract-1", "seats")
		wantFieldError(t, err, "contract", subscription.CodeContractLocked)
	})
}

func TestScheduleSegments(t *testing.T) {
	t.Parallel()
	// scheduled returns a contract started now with the arrears card
	// seats, billed monthly from its start.
	scheduled := func(t *testing.T) (*fixture, *contracts.View) {
		t.Helper()
		f := newFixture(t)
		rc := f.card(t, "seats", billingtest.USD)
		plan := f.h.Plan(period.Monthly, 0)
		if _, err := f.cards.CreatePlanRateCard(t.Context(), f.h.OrgID(), refs.ByID(plan.ID), products.PlanRateCardInput{RateCard: refs.ByID(rc.ID)}); err != nil {
			t.Fatalf("CreatePlanRateCard() error = %v", err)
		}
		return f, f.mustCreate(t, func(in *contracts.CreateInput) { in.PlanCode = plan.Code })
	}
	march := billingtest.Date(2026, time.March, 1)
	// source: spec/services/billing_segments/schedule_service_spec.rb:43
	// source: spec/services/billing_segments/schedule_service_spec.rb:52
	t.Run("stores the due segments and advances the card's clock", func(t *testing.T) {
		t.Parallel()
		f, v := scheduled(t)
		f.h.Clock.Set(march)
		segs, err := f.svc.ScheduleSegments(t.Context(), f.h.OrgID(), f.cust.ID, march)
		if err != nil {
			t.Fatalf("ScheduleSegments() error = %v", err)
		}
		feb := billingtest.Date(2026, time.February, 1)
		want := [][2]time.Time{{now, feb}, {feb, march}}
		if len(segs) != len(want) {
			t.Fatalf("segments = %d, want = %d", len(segs), len(want))
		}
		for i, s := range segs {
			if !s.StartedAt.Equal(want[i][0]) || !s.BillingAt.Equal(want[i][1]) || !s.EndedAt.Equal(want[i][1]) || s.Status != subscription.SegmentPending {
				t.Errorf("segment %d = %s..%s billed %s %s, want = %s..%s billed at its end, pending", i, s.StartedAt, s.EndedAt, s.BillingAt, s.Status, want[i][0], want[i][1])
			}
		}
		cards := billingtest.Read(f.h, func(ctx context.Context, r port.Repos) ([]*subscription.RateCard, error) {
			return r.ContractRateCards.ListBySubscription(ctx, f.h.OrgID(), v.Contract.ID)
		})
		if next := cards[0].NextBillingAt; next == nil || !next.Equal(billingtest.Date(2026, time.April, 1)) {
			t.Errorf("next billing = %v, want = 2026-04-01", next)
		}
	})
	// source: spec/services/billing_segments/schedule_service_spec.rb:58
	t.Run("stores nothing more on a second run", func(t *testing.T) {
		t.Parallel()
		f, _ := scheduled(t)
		f.h.Clock.Set(march)
		if _, err := f.svc.ScheduleSegments(t.Context(), f.h.OrgID(), f.cust.ID, march); err != nil {
			t.Fatalf("ScheduleSegments() error = %v", err)
		}
		segs, err := f.svc.ScheduleSegments(t.Context(), f.h.OrgID(), f.cust.ID, march)
		if err != nil || len(segs) != 0 {
			t.Errorf("rerun = %d, %v, want = 0, nil", len(segs), err)
		}
	})
}
