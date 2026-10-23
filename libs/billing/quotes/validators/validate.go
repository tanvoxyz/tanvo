package validators

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Scope is the operation a version is validated for.
type Scope int

const (
	// ScopeUpdate validates a draft being written: the shape, and the
	// references it already holds.
	ScopeUpdate Scope = iota + 1
	// ScopeApprove validates a version being approved: everything the
	// execution needs must be there.
	ScopeApprove
)

// String returns "update" or "approve".
func (s Scope) String() string {
	switch s {
	case ScopeUpdate:
		return "update"
	case ScopeApprove:
		return "approve"
	default:
		return fmt.Sprintf("Scope(%d)", int(s))
	}
}

// Validate checks the billing items of version v of quote q for scope
// (lago QuoteVersions::Validators.for(...).valid?). It returns nil when they
// are valid and a *core.ValidationError with lago's field paths and codes
// otherwise; repository failures pass through. now is the instant future
// dates are measured against. An order type without validator validates
// nothing.
func Validate(ctx context.Context, r port.Repos, q *quote.Quote, v *quote.Version, scope Scope, now time.Time) error {
	if q == nil || v == nil {
		return errors.New("validators: nil quote or version")
	}
	if scope != ScopeUpdate && scope != ScopeApprove {
		return fmt.Errorf("validators: invalid scope %d", int(scope))
	}
	var family string
	switch q.OrderType {
	case quote.OrderTypeOneOff:
		family = "one_off"
	case quote.OrderTypeSubscriptionCreation, quote.OrderTypeSubscriptionAmendment:
		// An amendment carries a subscription_creation payload.
		family = "subscription_creation"
	default:
		return nil
	}
	items, err := billingItems(v.BillingItems)
	if err != nil {
		return err
	}
	if err := validateStructure(family, scope, items); err != nil {
		return err
	}
	return validateBusiness(ctx, r, q, v, scope, now, items)
}

// validateBusiness is the business pass (lago <OrderType>::BusinessValidator)
// over decoded billing items of a quote whose order type has a validator.
func validateBusiness(ctx context.Context, r port.Repos, q *quote.Quote, v *quote.Version, scope Scope, now time.Time, items *value) error {
	b := &business{ctx: ctx, r: r, q: q, v: v, scope: scope, now: now, items: items}
	var err error
	switch q.OrderType {
	case quote.OrderTypeOneOff:
		err = b.oneOff()
	case quote.OrderTypeSubscriptionCreation:
		err = b.subscriptionCreation()
	default:
		b.amendment = true
		err = b.subscriptionAmendment()
	}
	if err != nil {
		return err
	}
	return b.errs.err()
}

// billingItems decodes the billing items, absent or null being {} (lago
// BaseOrderTypeValidator#normalized_billing_items).
func billingItems(raw []byte) (*value, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return &value{kind: kindObject, vals: map[string]*value{}}, nil
	}
	items, err := decodeValue(raw)
	if err != nil {
		return nil, fmt.Errorf("validators: billing items: %w", err)
	}
	if items.kind == kindNull {
		return &value{kind: kindObject, vals: map[string]*value{}}, nil
	}
	return items, nil
}

// validateStructure is the structural pass (lago BaseStructuralValidator).
func validateStructure(family string, scope Scope, items *value) error {
	all, err := schemas()
	if err != nil {
		return fmt.Errorf("validators: %w", err)
	}
	s := all[family+"_"+scope.String()]
	var c collector
	s.validate(items, nil, func(path []string, code string) { c.add(structuralField(path), code) })
	return c.err()
}

// business is the state of a business pass.
type business struct {
	ctx   context.Context
	r     port.Repos
	q     *quote.Quote
	v     *quote.Version
	scope Scope
	now   time.Time
	items *value
	// amendment narrows the subscription_creation checks to an amendment.
	amendment bool
	errs      collector
}

func (b *business) approve() bool { return b.scope == ScopeApprove }

func (b *business) org() core.OrganizationID { return b.q.OrganizationID }

// currencyListed reports whether c is one of the accepted ISO currencies
// (lago currency_list); custom pricing units are not.
func currencyListed(c money.Currency) bool {
	if c.IsZero() || c.IsCustom() {
		return false
	}
	iso, err := money.LookupCurrency(c.Code())
	return err == nil && iso.Code() == c.Code()
}

// validateCurrency is lago CurrencyValidation#validate_currency.
func (b *business) validateCurrency() {
	switch {
	case b.v.Currency.IsZero():
		if b.approve() {
			b.errs.add("currency", core.CodeRequired)
		}
	case !currencyListed(b.v.Currency):
		b.errs.add("currency", "invalid_currency")
	}
}

// validateBillingEntity is lago BillingEntityValidation: a set entity must
// be one of the organization's entities that is not archived.
func (b *business) validateBillingEntity() error {
	id := b.v.BillingEntityID
	if id.IsZero() {
		return nil
	}
	if b.amendment {
		b.errs.add("billing_entity_id", "not_supported_for_order_type")
		return nil
	}
	be, err := b.r.BillingEntities.Get(b.ctx, b.org(), id)
	if errors.Is(err, core.ErrNotFound) || (err == nil && be.Archived()) {
		b.errs.add("billing_entity_id", "billing_entity_not_found")
		return nil
	}
	if err != nil {
		return fmt.Errorf("get billing entity %s: %w", id, err)
	}
	return nil
}

// lookup parses a quoted identifier; ok is false when it does not name an
// identifier the store can hold in exactly this spelling (lago compares
// the quoted string with the stored canonical one).
func lookup[I interface {
	comparable
	fmt.Stringer
}](s *value, parse func(string) (I, error)) (I, bool) {
	var zero I
	str, isString := s.str()
	if !isString {
		return zero, false
	}
	id, err := parse(str)
	if err != nil || id.String() != str {
		return zero, false
	}
	return id, true
}

// notFound returns whether err is core.ErrNotFound, and err otherwise.
func notFound(err error) (bool, error) {
	if errors.Is(err, core.ErrNotFound) {
		return true, nil
	}
	return false, err
}
