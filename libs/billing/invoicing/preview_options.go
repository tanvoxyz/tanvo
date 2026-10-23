package invoicing

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/billing/kernel"
	"github.com/tanvoxyz/tanvo/libs/billing/refs"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/coupon"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/subscription"
	"github.com/tanvoxyz/tanvo/libs/period"
)

// Codes of the field errors of [Service.PreviewRequest] (lago
// Invoices::Preview services).
const (
	// CodeOnlyOneSubscriptionForTermination rejects a termination preview
	// of several subscriptions.
	CodeOnlyOneSubscriptionForTermination = "only_one_subscription_allowed_for_termination"
	// CodeOnlyOneSubscriptionForPlanChange rejects a plan change preview of
	// several subscriptions.
	CodeOnlyOneSubscriptionForPlanChange = "only_one_subscription_allowed_for_plan_change"
	// CodeSamePlan rejects a plan change preview to the subscription's
	// plan.
	CodeSamePlan = "new_plan_should_be_different_from_existing_plan"
	// CodeCannotBeInPast rejects a termination preview not in the future.
	CodeCannotBeInPast = "cannot_be_in_past"
)

// previewPrefix starts the external ID of the subscriptions a proposal
// creates in the rolled-back transaction; the view shows none.
const previewPrefix = "preview-"

// PreviewSubscription is a subscription not created yet that a
// [PreviewRequest] proposes (lago Invoices::Preview::BuildSubscriptionService).
type PreviewSubscription struct {
	// Plan is the plan, an original plan.
	Plan refs.Ref
	// SubscriptionAt is the start; now when zero.
	SubscriptionAt time.Time
	// BillingTime is calendar or anniversary billing; calendar when zero.
	BillingTime period.BillingTime
}

// PreviewRequest asks for the next invoice of a customer as the API
// states it (lago Api::V1::InvoicesController#preview with
// Invoices::PreviewContextService): a new subscription (Subscription), or
// stored ones (Subscriptions) as they are, terminated at TerminatedAt, or
// changed to TargetPlan.
type PreviewRequest struct {
	// OrganizationID is the organization.
	OrganizationID core.OrganizationID
	// CustomerID is the customer, else CustomerExternalID.
	CustomerID core.CustomerID
	// CustomerExternalID is the customer's external ID.
	CustomerExternalID string
	// BillingEntity is the billing entity of a new subscription; the
	// organization's default when zero (lago BillingEntities::ResolveService).
	BillingEntity refs.Ref
	// Subscription is the proposed subscription; used when Subscriptions
	// is empty.
	Subscription *PreviewSubscription
	// Subscriptions are stored subscriptions of the customer.
	Subscriptions []refs.SubscriptionRef
	// TerminatedAt previews the termination of the one subscription then.
	TerminatedAt *time.Time
	// TargetPlan previews the change of the one subscription to that
	// original plan.
	TargetPlan refs.Ref
	// Coupons are applied when the customer has no active coupon; refs to
	// no coupon are ignored.
	Coupons []refs.Ref
	// Customer overrides the customer's attributes; with no CustomerID or
	// CustomerExternalID it describes a customer not created yet.
	Customer *PreviewCustomer
	// CouponAttributes are coupons given by attributes, applied after
	// Coupons: the stored coupon of the code, else one built from the
	// attributes (lago PreviewContextService#find_or_build_applied_coupons).
	CouponAttributes []*coupon.Coupon
}

// previewContext is what a [PreviewRequest] previews (lago
// Invoices::Preview::SubscriptionsService#context).
type previewContext int

const (
	contextProposal previewContext = iota
	contextProjection
	contextTermination
	contextPlanChange
)

// previewSim is the simulation of a [PreviewRequest] in the rolled-back
// transaction of the preview, where it stands for lago's records changed
// in memory.
type previewSim struct {
	context      previewContext
	sub          *subscription.Subscription
	terminatedAt time.Time
	target       *catalog.Plan
	coupons      []*coupon.Coupon
	// built are the coupons of coupons that prepare creates.
	built map[core.CouponID]bool

	// terminated is the subscription whose termination credit note the
	// preview credits (set by prepare).
	terminated core.SubscriptionID
}

// previewNote is the termination credit note of a preview (lago
// Invoices::Preview::CreditsService#credits_from_terminated_subscription).
type previewNote struct {
	note *creditnote.CreditNote
}

// fieldError returns the validation error of code on field.
func fieldError(field, code string) error {
	return &core.ValidationError{Errors: []core.FieldError{{Field: field, Code: code}}}
}

// PreviewRequest computes the invoice in without storing anything and
// returns its API view (lago Invoices::PreviewService serialized with its
// customer, credits, applied taxes, subscriptions and fees). It must not
// run inside a transaction.
func (s *Service) PreviewRequest(ctx context.Context, in PreviewRequest) (*InvoiceView, error) {
	var out *InvoiceView
	err := s.d.Tx.InTx(ctx, func(ctx context.Context, r port.Repos) error {
		pin, err := s.previewInput(ctx, r, in)
		if err != nil {
			return err
		}
		p, err := s.preview(ctx, r, pin)
		if err != nil {
			return err
		}
		out = p.view
		return errPreview
	})
	if err != nil && !errors.Is(err, errPreview) {
		return nil, err
	}
	return out, nil
}

// previewInput resolves in into the input of a preview and its
// simulation, in the order of lago Invoices::PreviewContextService.
func (s *Service) previewInput(ctx context.Context, r port.Repos, in PreviewRequest) (PreviewInput, error) {
	org := in.OrganizationID
	cust, built, err := s.previewCustomer(ctx, r, in)
	if err != nil {
		return PreviewInput{}, err
	}
	entity, err := resolveBillingEntity(ctx, r, org, in.BillingEntity)
	if err != nil {
		return PreviewInput{}, err
	}
	sim := &previewSim{}
	out := PreviewInput{OrganizationID: org, CustomerID: cust.ID, sim: sim}
	if sim.coupons, sim.built, err = s.previewCoupons(ctx, r, org, cust.ID, in.Coupons, in.CouponAttributes); err != nil {
		return PreviewInput{}, err
	}
	if len(in.Subscriptions) == 0 {
		sim.context = contextProposal
		ns, err := proposedSubscription(ctx, r, org, s.d.Now(), cust.BillingEntityID, entity, in.Subscription)
		if err != nil {
			return PreviewInput{}, err
		}
		out.New = []*subscription.Subscription{ns}
		return out, nil
	}
	if built {
		return PreviewInput{}, fieldError("customer", CodeMustBePersisted)
	}
	subs, err := previewSubscriptions(ctx, r, org, cust.ID, in.Subscriptions, s.d.Now())
	if err != nil {
		return PreviewInput{}, err
	}
	var auto *time.Time
	if in.TerminatedAt == nil && len(subs) == 1 {
		if auto, err = endingInCurrentPeriod(ctx, r, subs[0], s.d.Now()); err != nil {
			return PreviewInput{}, err
		}
	}
	switch {
	case in.TerminatedAt != nil || auto != nil:
		sim.context = contextTermination
		if len(subs) > 1 {
			return PreviewInput{}, fieldError("subscription_ids", CodeOnlyOneSubscriptionForTermination)
		}
		if len(subs) == 0 {
			return PreviewInput{}, core.NotFound("subscription")
		}
		at := auto
		if in.TerminatedAt != nil {
			at = in.TerminatedAt
		}
		if !at.After(s.d.Now()) {
			return PreviewInput{}, fieldError("terminated_at", CodeCannotBeInPast)
		}
		sim.sub, sim.terminatedAt = subs[0], at.UTC()
	case !in.TargetPlan.IsZero():
		sim.context = contextPlanChange
		if len(subs) > 1 {
			return PreviewInput{}, fieldError("subscription_ids", CodeOnlyOneSubscriptionForPlanChange)
		}
		if len(subs) == 0 {
			return PreviewInput{}, core.NotFound("subscription")
		}
		if sim.target, err = originalPlan(ctx, r, org, in.TargetPlan); err != nil {
			return PreviewInput{}, err
		}
		if sim.target.ID == subs[0].PlanID {
			return PreviewInput{}, fieldError("plan_id", CodeSamePlan)
		}
		sim.sub = subs[0]
	default:
		sim.context = contextProjection
		if len(subs) == 0 {
			return PreviewInput{}, core.NotFound("subscription")
		}
		if len(subs) == 1 && subs[0].Status == subscription.StatusPending {
			// A subscription starting in the future is previewed as if it
			// were new (lago FindSubscriptionsService duplicates it).
			ns := *subs[0]
			ns.ID = core.SubscriptionID{}
			out.New = []*subscription.Subscription{&ns}
			return out, nil
		}
		for _, sub := range subs {
			out.SubscriptionIDs = append(out.SubscriptionIDs, sub.ID)
		}
	}
	return out, nil
}

// resolveBillingEntity finds the billing entity of ref, else the
// organization's default (lago BillingEntities::ResolveService).
func resolveBillingEntity(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (*organization.BillingEntity, error) {
	var (
		b   *organization.BillingEntity
		err error
	)
	switch {
	case ref.Code != "":
		b, err = r.BillingEntities.GetByCode(ctx, org, ref.Code)
	case !ref.IsZero():
		var id core.BillingEntityID
		if id, err = core.ParseID[core.BillingEntityID](ref.ID.String()); err == nil {
			b, err = r.BillingEntities.Get(ctx, org, id)
		}
	default:
		b, err = r.BillingEntities.GetDefault(ctx, org)
	}
	if errors.Is(err, core.ErrNotFound) {
		return nil, core.NotFound("billing_entity")
	}
	if err != nil {
		return nil, fmt.Errorf("resolve billing entity %s: %w", ref, err)
	}
	return b, nil
}

// originalPlan finds the original plan of ref (lago plans.parents).
func originalPlan(ctx context.Context, r port.Repos, org core.OrganizationID, ref refs.Ref) (*catalog.Plan, error) {
	p, err := refs.Plans(r, org).One(ctx, ref)
	if err != nil {
		return nil, err
	}
	if p.IsChild() || p.Status == catalog.PlanStatusDeleted {
		return nil, core.NotFound("plan")
	}
	return p, nil
}

// proposedSubscription builds the subscription of a proposal (lago
// Invoices::Preview::BuildSubscriptionService): the entity overrides the
// customer's billing entity when it differs.
func proposedSubscription(ctx context.Context, r port.Repos, org core.OrganizationID, now time.Time, custEntity core.BillingEntityID, entity *organization.BillingEntity, ps *PreviewSubscription) (*subscription.Subscription, error) {
	if ps == nil {
		return nil, core.NotFound("plan")
	}
	p, err := originalPlan(ctx, r, org, ps.Plan)
	if err != nil {
		return nil, err
	}
	bt := ps.BillingTime
	if !bt.Valid() {
		bt = period.Calendar
	}
	at := ps.SubscriptionAt
	if at.IsZero() {
		at = now
	}
	// The ID and the external ID are given by the preview.
	ns := subscription.New(org, core.SubscriptionID{}, core.CustomerID{}, p.ID, "", at, subscription.PlanFacts{PayInAdvance: p.PayInAdvance}, now)
	ns.BillingTime = bt
	if entity.ID != custEntity {
		ns.BillingEntityID = entity.ID
	}
	return ns, nil
}

// previewSubscriptions returns the customer's subscriptions of refs: the
// active ones, or the one subscription starting in the future (pending,
// without a previous subscription) of a single reference (lago
// Invoices::Preview::SubscriptionsService#customer_subscriptions).
func previewSubscriptions(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, rs []refs.SubscriptionRef, now time.Time) ([]*subscription.Subscription, error) {
	all, err := r.Subscriptions.ListByCustomer(ctx, org, cust)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions of customer %s: %w", cust, err)
	}
	matched := slices.DeleteFunc(all, func(sub *subscription.Subscription) bool {
		return !slices.ContainsFunc(rs, func(ref refs.SubscriptionRef) bool {
			if ref.ExternalID != "" {
				return ref.ExternalID == sub.ExternalID
			}
			return ref.ID == sub.ID
		})
	})
	if len(rs) == 1 && len(matched) == 1 {
		sub := matched[0]
		if sub.Status == subscription.StatusPending && sub.PreviousSubscriptionID.IsZero() && sub.SubscriptionAt.After(now) {
			return matched, nil
		}
	}
	return slices.DeleteFunc(matched, func(sub *subscription.Subscription) bool {
		return sub.Status != subscription.StatusActive
	}), nil
}

// endingInCurrentPeriod returns the ending of sub when it falls by the end
// of the first day of its next period, nil otherwise (lago
// SubscriptionsService#subscription_ending_in_current_period?).
func endingInCurrentPeriod(ctx context.Context, r port.Repos, sub *subscription.Subscription, now time.Time) (*time.Time, error) {
	if sub.EndingAt == nil {
		return nil, nil
	}
	env, err := kernel.LoadSubscriptionEnvOf(ctx, r, sub)
	if err != nil {
		return nil, err
	}
	d, err := env.Dates(now, period.CurrentUsage)
	if err != nil {
		return nil, err
	}
	next := d.CurrentPeriodEnd().In(env.Location()).AddDate(0, 0, 1)
	if sub.EndingAt.Before(next) {
		end := *sub.EndingAt
		return &end, nil
	}
	return nil, nil
}

// previewCoupons returns the coupons of rs, then of attrs, to apply when
// the customer has no active applied coupon, with the ones built from
// attrs that prepare creates; refs to no coupon, or to a deleted one, are
// ignored, and attributes without a stored coupon of their code build one
// (lago PreviewContextService#find_or_build_applied_coupons).
func (s *Service) previewCoupons(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, rs []refs.Ref, attrs []*coupon.Coupon) ([]*coupon.Coupon, map[core.CouponID]bool, error) {
	if len(rs) == 0 && len(attrs) == 0 {
		return nil, nil, nil
	}
	active, err := r.AppliedCoupons.ListActiveByCustomer(ctx, org, cust)
	if err != nil {
		return nil, nil, fmt.Errorf("list applied coupons of customer %s: %w", cust, err)
	}
	if len(active) > 0 {
		return nil, nil, nil
	}
	var out []*coupon.Coupon
	add := func(c *coupon.Coupon) {
		if c.DeletedAt == nil && !slices.ContainsFunc(out, func(o *coupon.Coupon) bool { return o.ID == c.ID }) {
			out = append(out, c)
		}
	}
	for _, ref := range rs {
		var (
			c   *coupon.Coupon
			err error
		)
		if ref.Code != "" {
			c, err = r.Coupons.GetByCode(ctx, org, ref.Code)
		} else {
			var id core.CouponID
			if id, err = core.ParseID[core.CouponID](ref.ID.String()); err == nil {
				c, err = r.Coupons.Get(ctx, org, id)
			}
		}
		switch {
		case errors.Is(err, core.ErrNotFound):
			continue
		case err != nil:
			return nil, nil, fmt.Errorf("find coupon %s: %w", ref, err)
		}
		add(c)
	}
	built := map[core.CouponID]bool{}
	for _, a := range attrs {
		if a.Code != "" {
			c, err := r.Coupons.GetByCode(ctx, org, a.Code)
			if err == nil {
				add(c)
				continue
			}
			if !errors.Is(err, core.ErrNotFound) {
				return nil, nil, fmt.Errorf("find coupon %s: %w", a.Code, err)
			}
		}
		if c := s.builtCoupon(org, a, s.d.Now()); c != nil {
			built[c.ID] = true
			add(c)
		}
	}
	return out, built, nil
}

// prepare applies the simulation in the transaction of r and returns the
// subscriptions to preview with the starting ones: a nil starting leaves
// preview to its own path (in.SubscriptionIDs or in.New).
func (m *previewSim) prepare(ctx context.Context, r port.Repos, s *Service, in PreviewInput) ([]core.SubscriptionID, map[core.SubscriptionID]bool, error) {
	now := s.d.Now()
	for _, c := range m.coupons {
		if m.built[c.ID] {
			if err := r.Coupons.Create(ctx, c); err != nil {
				return nil, nil, fmt.Errorf("simulate coupon %s: %w", c.Code, err)
			}
		}
		ac := coupon.Apply(c, core.NewID[core.AppliedCouponID](s.d.IDs), in.CustomerID, coupon.Overrides{}, now)
		if err := r.AppliedCoupons.Create(ctx, ac); err != nil {
			return nil, nil, fmt.Errorf("simulate coupon %s: %w", c.Code, err)
		}
	}
	switch m.context {
	case contextTermination:
		sub := *m.sub
		sub.Status, sub.TerminatedAt = subscription.StatusTerminated, &m.terminatedAt
		if err := r.Subscriptions.Update(ctx, &sub); err != nil {
			return nil, nil, fmt.Errorf("simulate termination of subscription %s: %w", sub.ID, err)
		}
		m.terminated = sub.ID
		return []core.SubscriptionID{sub.ID}, map[core.SubscriptionID]bool{}, nil
	case contextPlanChange:
		return m.changePlan(ctx, r, s, now)
	}
	return in.SubscriptionIDs, nil, nil
}

// changePlan simulates the change of m.sub to m.target (lago
// Invoices::Preview::SubscriptionPlanChangeService): an upgrade
// terminates the subscription now, a downgrade at the end of its period,
// when the new subscription starts; the new subscription is previewed
// when its plan is paid in advance.
func (m *previewSim) changePlan(ctx context.Context, r port.Repos, s *Service, now time.Time) ([]core.SubscriptionID, map[core.SubscriptionID]bool, error) {
	env, err := kernel.LoadSubscriptionEnvOf(ctx, r, m.sub)
	if err != nil {
		return nil, nil, err
	}
	at, started := now, now
	upgrade := kernel.PlanChange(env.Plan, m.target) != period.Downgrade
	if !upgrade {
		d, err := env.Dates(now, period.CurrentUsage)
		if err != nil {
			return nil, nil, err
		}
		// A downgrade ends the current subscription a day after the last
		// instant of its period, and the new one starts with the next
		// period (subscription_plan_change_service.rb:66-76).
		started = d.CurrentPeriodEnd()
		at = started.Add(-time.Microsecond).AddDate(0, 0, 1)
	}
	cur := *m.sub
	cur.Status, cur.TerminatedAt = subscription.StatusTerminated, &at
	if err := r.Subscriptions.Update(ctx, &cur); err != nil {
		return nil, nil, fmt.Errorf("simulate plan change of subscription %s: %w", cur.ID, err)
	}
	ns := *m.sub
	name := m.target.Name
	ns.ID = core.NewID[core.SubscriptionID](s.d.IDs)
	ns.PlanID, ns.Name, ns.PreviousSubscriptionID = m.target.ID, &name, cur.ID
	ns.Status, ns.StartedAt = subscription.StatusActive, &started
	ns.TerminatedAt, ns.CanceledAt, ns.TrialEndedAt, ns.NextBillingAt, ns.LastReceivedEventOn = nil, nil, nil, nil, nil
	ns.CreatedAt, ns.UpdatedAt = now, now
	if err := r.Subscriptions.Create(ctx, &ns); err != nil {
		return nil, nil, fmt.Errorf("simulate new subscription of %s: %w", cur.ID, err)
	}
	if upgrade {
		m.terminated = cur.ID
	}
	ids, starting := []core.SubscriptionID{cur.ID}, map[core.SubscriptionID]bool{}
	if m.target.PayInAdvance {
		ids = append(ids, ns.ID)
		starting[ns.ID] = true
	}
	return ids, starting, nil
}

// terminationNote computes, without storing it, the credit note of the
// terminated subscription of a plan paid in advance that is not
// downgraded (lago Invoices::Preview::CreditsService).
func (m *previewSim) terminationNote(ctx context.Context, r port.Repos, s *Service) (*previewNote, error) {
	if m.terminated.IsZero() {
		return nil, nil
	}
	env, err := kernel.LoadSubscriptionEnv(ctx, r, m.sub.OrganizationID, m.terminated)
	if err != nil {
		return nil, err
	}
	if !env.Plan.PayInAdvance || (env.Next != nil && env.Change == period.Downgrade) {
		return nil, nil
	}
	cn, err := s.creditNotes.CreateFromTermination(ctx, r, creditnotes.TerminationInput{
		OrganizationID: env.OrganizationID(),
		SubscriptionID: env.Subscription.ID,
		Reason:         creditnote.ReasonOrderCancellation,
		Upgrade:        env.Next != nil && env.Change == period.Upgrade,
		OnTermination:  env.Subscription.OnTerminationCreditNote,
		Preview:        true,
	})
	if err != nil || cn == nil {
		return nil, err
	}
	return &previewNote{note: cn}, nil
}

// credit returns the credit of the note on inv, at most what inv still
// owes, and adds it to inv's credit notes amount; nil when nothing is
// credited.
func (n *previewNote) credit(d *kernel.Deps, inv *invoice.Invoice) *invoice.Credit {
	amount := min(n.note.BalanceAmount, inv.SubTotalIncludingTaxesAmount-inv.CreditNotesAmount-inv.PrepaidCreditAmount)
	if amount <= 0 {
		return nil
	}
	inv.CreditNotesAmount += amount
	return &invoice.Credit{
		ID:             core.NewID[core.InvoiceCreditID](d.IDs),
		OrganizationID: inv.OrganizationID,
		InvoiceID:      inv.ID,
		CreditNoteID:   n.note.ID,
		Amount:         amount,
		Currency:       inv.Currency,
		CreatedAt:      d.Now(),
	}
}

// view builds the API view of p in the transaction of r, before the
// simulated rows are rolled back.
func (m *previewSim) view(ctx context.Context, r port.Repos, p *Preview, rows []*billed, note *previewNote) (*InvoiceView, error) {
	inv := p.Invoice
	v := newViewer(r, inv.OrganizationID)
	v.invCust[inv.ID] = inv.CustomerID
	out := &InvoiceView{Invoice: inv, AppliedTaxes: p.Taxes}
	ext := map[core.SubscriptionID]string{}
	for _, b := range rows {
		if id := b.env.Subscription.ExternalID; !strings.HasPrefix(id, previewPrefix) {
			ext[b.env.Subscription.ID] = id
		}
	}
	for _, is := range p.Subscriptions {
		out.Subscriptions = append(out.Subscriptions, SubscriptionView{Subscription: is, ExternalID: ext[is.SubscriptionID]})
	}
	var err error
	if out.Fees, err = v.fees(ctx, p.Fees, map[core.InvoiceID][]*invoice.Fee{inv.ID: p.Fees}, map[core.InvoiceID]map[core.FeeID]bool{inv.ID: {}}); err != nil {
		return nil, err
	}
	for _, c := range p.Credits {
		var label string
		if note != nil && c.CreditNoteID == note.note.ID {
			label = note.note.Number
		} else if label, err = v.creditLabel(ctx, c); err != nil {
			return nil, err
		}
		out.Credits = append(out.Credits, CreditView{Credit: c, Label: label})
	}
	return out, nil
}
