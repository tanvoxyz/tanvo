package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func testPendingVIESCheck(t *testing.T, h Harness) {
	e := newEnv(t, h)
	a, b := e.twoOrgs()
	org := a.OrganizationID
	c := customer.New(org, core.NewID[core.CustomerID](e.ids), "vies-1", a.ID, now)
	e.tx(func(ctx context.Context, r port.Repos) error {
		noErr(t, "Customers.Create()", r.Customers.Create(ctx, c))
		_, err := r.PendingVIESChecks.Get(ctx, org, c.ID)
		wantErr(t, "Get() without a check", err, core.ErrNotFound)

		p := &customer.PendingVIESCheck{
			OrganizationID: org, CustomerID: c.ID, BillingEntityID: a.ID,
			TaxIdentificationNumber: ptr("IE6388047V"), CreatedAt: now, UpdatedAt: now,
		}
		noErr(t, "Save()", r.PendingVIESChecks.Save(ctx, p))
		got, err := r.PendingVIESChecks.Get(ctx, org, c.ID)
		noErr(t, "Get()", err)
		equal(t, "Get()", got, p)

		// A second save replaces the check and keeps its creation instant.
		next := *p
		next.AttemptsCount = 2
		next.LastAttemptAt = ptr(at(time.Minute))
		next.LastErrorType = ptr("timeout")
		next.LastErrorMessage = ptr("The VIES web service returned the error: TIMEOUT")
		next.CreatedAt = at(time.Minute)
		next.UpdatedAt = at(time.Minute)
		noErr(t, "Save() again", r.PendingVIESChecks.Save(ctx, &next))
		got, err = r.PendingVIESChecks.Get(ctx, org, c.ID)
		noErr(t, "Get() after the second save", err)
		next.CreatedAt = now
		equal(t, "Get() after the second save", got, &next)

		bad := next
		bad.LastErrorType = ptr("nope")
		wantErr(t, "Save() with an unknown error type", r.PendingVIESChecks.Save(ctx, &bad), core.ErrInvalidValue)
		missing := next
		missing.CustomerID = core.NewID[core.CustomerID](e.ids)
		wantErr(t, "Save() of an unknown customer", r.PendingVIESChecks.Save(ctx, &missing), core.ErrNotFound)

		_, err = r.PendingVIESChecks.Get(ctx, b.OrganizationID, c.ID)
		wantErr(t, "Get() from another organization", err, core.ErrNotFound)
		deleted, err := r.PendingVIESChecks.Delete(ctx, b.OrganizationID, c.ID)
		noErr(t, "Delete() from another organization", err)
		equal(t, "Delete() from another organization", deleted, false)

		deleted, err = r.PendingVIESChecks.Delete(ctx, org, c.ID)
		noErr(t, "Delete()", err)
		equal(t, "Delete()", deleted, true)
		deleted, err = r.PendingVIESChecks.Delete(ctx, org, c.ID)
		noErr(t, "Delete() twice", err)
		equal(t, "Delete() twice", deleted, false)
		_, err = r.PendingVIESChecks.Get(ctx, org, c.ID)
		wantErr(t, "Get() after Delete()", err, core.ErrNotFound)
		return nil
	})
}
