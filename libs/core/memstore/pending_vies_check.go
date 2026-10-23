package memstore

import (
	"context"
	"fmt"
	"slices"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
)

// pendingVIESCheckRepo implements customer.PendingVIESCheckRepository; st
// is the whole transaction state, for the billing entity reference.
type pendingVIESCheckRepo struct {
	d  *customerData
	st *state
}

var _ customer.PendingVIESCheckRepository = pendingVIESCheckRepo{}

func (r pendingVIESCheckRepo) Get(_ context.Context, org core.OrganizationID, c core.CustomerID) (*customer.PendingVIESCheck, error) {
	p, ok := r.d.pendingVIES.get(c)
	if !ok || p.OrganizationID != org {
		return nil, fmt.Errorf("pending_vies_check %s: %w", c, core.ErrNotFound)
	}
	return p, nil
}

func (r pendingVIESCheckRepo) Save(_ context.Context, p *customer.PendingVIESCheck) error {
	if c, ok := r.d.customers.get(p.CustomerID); !ok || c.OrganizationID != p.OrganizationID {
		return fmt.Errorf("pending_vies_check: customer %s: %w", p.CustomerID, core.ErrNotFound)
	}
	if b, ok := r.st.organization.billingEntities.get(p.BillingEntityID); !ok || b.OrganizationID != p.OrganizationID {
		return fmt.Errorf("pending_vies_check: billing entity %s: %w", p.BillingEntityID, core.ErrNotFound)
	}
	if p.AttemptsCount < 0 || (p.LastErrorType != nil && !slices.Contains(customer.VIESErrorTypes(), *p.LastErrorType)) {
		return fmt.Errorf("pending_vies_check %s: %w", p.CustomerID, core.ErrInvalidValue)
	}
	v := *p
	if old, ok := r.d.pendingVIES.get(p.CustomerID); ok {
		v.CreatedAt = old.CreatedAt
	}
	r.d.pendingVIES.put(p.CustomerID, v)
	return nil
}

func (r pendingVIESCheckRepo) Delete(_ context.Context, org core.OrganizationID, c core.CustomerID) (bool, error) {
	p, ok := r.d.pendingVIES.get(c)
	if !ok || p.OrganizationID != org {
		return false, nil
	}
	return r.d.pendingVIES.del(c), nil
}
