package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/invoicing"
	"github.com/tanvoxyz/tanvo/libs/billing/lifecycle"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/core/webhook"
)

// biller records the bill requests.
type biller struct{ reqs []invoicing.BillRequest }

func (b *biller) Bill(_ context.Context, _ port.Repos, req invoicing.BillRequest) ([]*invoice.Invoice, error) {
	b.reqs = append(b.reqs, req)
	return nil, nil
}

// crediter records the termination credit note requests.
type crediter struct {
	ins []creditnotes.TerminationInput
}

func (c *crediter) CreateFromTermination(_ context.Context, _ port.Repos, in creditnotes.TerminationInput) (*creditnote.CreditNote, error) {
	c.ins = append(c.ins, in)
	return nil, nil
}

// fixture is a harness with the lifecycle service over fakes.
type fixture struct {
	*billingtest.Harness
	svc     *lifecycle.Service
	biller  *biller
	credits *crediter
	names   map[core.SubscriptionID]string
}

func newFixture(t *testing.T, now time.Time) *fixture {
	t.Helper()
	h := billingtest.New(t, now)
	b, c := &biller{}, &crediter{}
	return &fixture{
		Harness: h, svc: lifecycle.New(h.Deps).WithBiller(b).WithCreditNotes(c), biller: b, credits: c,
		names: map[core.SubscriptionID]string{},
	}
}

// name names a subscription in the bill descriptions.
func (f *fixture) name(s *subscription.Subscription, n string) *subscription.Subscription {
	f.names[s.ID] = n
	return s
}

// bills describes the bill requests as "at skip? name:reason,…".
func (f *fixture) bills() []string {
	out := make([]string, 0, len(f.biller.reqs))
	for _, r := range f.biller.reqs {
		items := make([]string, 0, len(r.Items))
		for _, it := range r.Items {
			n, ok := f.names[it.SubscriptionID]
			if !ok {
				n = "new"
			}
			items = append(items, n+":"+string(it.Reason))
		}
		skip := ""
		if r.SkipCharges {
			skip = " skip"
		}
		out = append(out, r.BillingAt.Format(time.DateTime)+skip+" "+strings.Join(items, ","))
	}
	return out
}

func (f *fixture) reload(id core.SubscriptionID) *subscription.Subscription {
	f.T.Helper()
	return billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) (*subscription.Subscription, error) {
		return r.Subscriptions.Get(ctx, f.OrgID(), id)
	})
}

func (f *fixture) rule(s *subscription.Subscription, status subscription.ActivationRuleStatus) *subscription.ActivationRule {
	f.T.Helper()
	rule := subscription.NewActivationRule(f.OrgID(), core.NewID[core.ActivationRuleID](f.IDs), s.ID, subscription.ActivationRuleTypePayment, 0, f.Now())
	rule.Status = status
	f.Do(func(ctx context.Context, r port.Repos) error { return r.ActivationRules.Create(ctx, rule) })
	return rule
}

func (f *fixture) rules(s *subscription.Subscription) []subscription.ActivationRuleStatus {
	f.T.Helper()
	rules := billingtest.Read(f.Harness, func(ctx context.Context, r port.Repos) ([]*subscription.ActivationRule, error) {
		return r.ActivationRules.ListBySubscription(ctx, f.OrgID(), s.ID)
	})
	out := make([]subscription.ActivationRuleStatus, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Status)
	}
	return out
}

// pending creates a subscription that has not started.
func (f *fixture) pending(p *catalog.Plan, opts ...func(*subscription.Subscription)) *subscription.Subscription {
	f.T.Helper()
	c := f.Customer()
	return f.Subscription(c, p, append([]func(*subscription.Subscription){billingtest.SubscribedAt(f.Now().Add(time.Hour))}, opts...)...)
}

// inAdvanceFixed adds a pay-in-advance fixed charge to the plan.
func (f *fixture) inAdvanceFixed(p *catalog.Plan, inAdvance bool) {
	f.FixedCharge(p, f.AddOn(0), catalog.FixedChargeModelStandard, `{"amount":"10"}`, "1",
		func(fc *catalog.FixedCharge) { fc.PayInAdvance = inAdvance })
}

func inAdvance(p *catalog.Plan) { p.PayInAdvance = true }

func trial(days int) func(*catalog.Plan) { return func(p *catalog.Plan) { p.TrialPeriodDays = days } }

func check[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got = %v, want = %v", what, got, want)
	}
}

func checkSlice[T comparable](t *testing.T, what string, got, want []T) {
	t.Helper()
	if !slices.Equal(got, want) && (len(got) > 0 || len(want) > 0) {
		t.Errorf("%s: got = %v, want = %v", what, got, want)
	}
}

func checkRule(t *testing.T, err error, code string) {
	t.Helper()
	var re *core.RuleError
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("error: got = %v, want = rule %s", err, code)
	}
}

func hooks(evs ...webhook.EventType) []webhook.EventType { return evs }

func stamp(t time.Time) string { return t.Format(time.DateTime) }

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func asValidation(err error, ve **core.ValidationError) bool { return errors.As(err, ve) }

var _ lifecycle.Biller = (*invoicing.Service)(nil)
