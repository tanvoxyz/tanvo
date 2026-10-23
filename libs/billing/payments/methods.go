package payments

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// methodLockScope is the scope of the lock a customer's default payment
// method changes under (lago Customers::LockService scope payment_method).
const methodLockScope = "customer:payment_method"

// ListMethods returns a page of a customer's payment methods, newest
// first (lago PaymentMethodsQuery on a live customer).
func (s *Service) ListMethods(ctx context.Context, org core.OrganizationID, c CustomerRef, page core.PageRequest) (core.Page[*payment.Method], error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (core.Page[*payment.Method], error) {
		cu, err := c.find(ctx, r, org)
		if err != nil {
			return core.Page[*payment.Method]{}, err
		}
		ms, err := r.PaymentMethods.ListByCustomer(ctx, org, cu.ID)
		if err != nil {
			return core.Page[*payment.Method]{}, wrap(err, "list payment methods of customer %s", cu.ID)
		}
		slices.SortStableFunc(ms, func(a, b *payment.Method) int {
			return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), a.ID.Compare(b.ID))
		})
		return pageMethods(ms, page)
	})
}

// pageMethods cuts the page of req out of ms; the token is the ID of the
// last method of the previous page.
func pageMethods(ms []*payment.Method, req core.PageRequest) (core.Page[*payment.Method], error) {
	start := 0
	if req.Token != "" {
		u, err := uuid.Parse(req.Token)
		if err != nil {
			return core.Page[*payment.Method]{}, core.ErrInvalidValue
		}
		i := slices.IndexFunc(ms, func(m *payment.Method) bool { return m.ID.UUID() == u })
		if i < 0 {
			return core.Page[*payment.Method]{}, core.ErrInvalidValue
		}
		start = i + 1
	}
	end := min(start+req.Limit(), len(ms))
	p := core.Page[*payment.Method]{Items: ms[start:end]}
	if end < len(ms) {
		p.NextToken = ms[end-1].ID.String()
	}
	return p, nil
}

// SetDefaultMethod makes a customer's payment method the default, clearing
// the others' flag under the customer's payment method lock (lago
// PaymentMethods::SetAsDefaultService); the default is returned as it is.
func (s *Service) SetDefaultMethod(ctx context.Context, org core.OrganizationID, c CustomerRef, id core.PaymentMethodID) (*payment.Method, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Method, error) {
		m, err := customerMethod(ctx, r, org, c, id)
		if err != nil || m.IsDefault {
			return m, err
		}
		if err := r.Locks.Lock(ctx, org, port.LockKey{Scope: methodLockScope, ID: m.CustomerID.UUID()}); err != nil {
			return nil, wrap(err, "lock payment methods of customer %s", m.CustomerID)
		}
		now := s.d.Now()
		ms, err := r.PaymentMethods.ListByCustomer(ctx, org, m.CustomerID)
		if err != nil {
			return nil, wrap(err, "list payment methods of customer %s", m.CustomerID)
		}
		for _, o := range ms {
			if o.ID == m.ID || !o.IsDefault {
				continue
			}
			o.IsDefault = false
			o.UpdatedAt = now
			if err := r.PaymentMethods.Update(ctx, o); err != nil {
				return nil, wrap(err, "update payment method %s", o.ID)
			}
		}
		m.IsDefault = true
		m.UpdatedAt = now
		if err := r.PaymentMethods.Update(ctx, m); err != nil {
			return nil, wrap(err, "update payment method %s", m.ID)
		}
		return m, nil
	})
}

// DeleteMethod deletes a customer's payment method, which stops being the
// default (lago PaymentMethods::DestroyService).
func (s *Service) DeleteMethod(ctx context.Context, org core.OrganizationID, c CustomerRef, id core.PaymentMethodID) (*payment.Method, error) {
	return inTx(ctx, s.d.Tx, func(ctx context.Context, r port.Repos) (*payment.Method, error) {
		m, err := customerMethod(ctx, r, org, c, id)
		if err != nil {
			return nil, err
		}
		if err := m.Delete(s.d.Now()); err != nil {
			return nil, err
		}
		if err := r.PaymentMethods.Update(ctx, m); err != nil {
			return nil, wrap(err, "delete payment method %s", m.ID)
		}
		return m, nil
	})
}

// customerMethod returns the live payment method id of the customer c
// names.
func customerMethod(ctx context.Context, r port.Repos, org core.OrganizationID, c CustomerRef, id core.PaymentMethodID) (*payment.Method, error) {
	cu, err := c.find(ctx, r, org)
	if err != nil {
		return nil, err
	}
	m, err := r.PaymentMethods.Get(ctx, org, id)
	if errors.Is(err, core.ErrNotFound) || err == nil && (m.CustomerID != cu.ID || m.Deleted()) {
		return nil, notFound("payment_method")
	}
	if err != nil {
		return nil, wrap(err, "load payment method %s", id)
	}
	return m, nil
}
