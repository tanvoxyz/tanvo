package porttest

import (
	"context"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/idempotency"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// testIdempotency checks the idempotency key records: claims, replacement
// of expired records, completion, release, isolation and the purge.
func testIdempotency(t *testing.T, h Harness) {
	e := newEnv(t, h)
	a, b := e.twoOrgs()
	org, ob := a.OrganizationID, b.OrganizationID
	rec := func(o core.OrganizationID, key string, created time.Time) *idempotency.Record {
		return &idempotency.Record{
			OrganizationID: o, Key: key, Procedure: "/tanvo.test.v1.TestService/Create",
			RequestHash: []byte{1, 2, 3}, CreatedAt: created, ExpiresAt: created.Add(idempotency.TTL),
		}
	}
	e.tx(func(ctx context.Context, r port.Repos) error {
		first := rec(org, "k1", now)
		ok, err := r.IdempotencyKeys.Claim(ctx, first)
		noErr(t, "Claim()", err)
		equal(t, "Claim()", ok, true)
		ok, err = r.IdempotencyKeys.Claim(ctx, rec(org, "k1", at(time.Hour)))
		noErr(t, "Claim() of a live key", err)
		equal(t, "Claim() of a live key", ok, false)
		ok, err = r.IdempotencyKeys.Claim(ctx, rec(ob, "k1", at(time.Hour)))
		noErr(t, "Claim() in another organization", err)
		equal(t, "Claim() in another organization", ok, true)

		got, err := r.IdempotencyKeys.Get(ctx, org, "k1")
		noErr(t, "Get()", err)
		equal(t, "Get() pending", got, first)
		equal(t, "Pending()", got.Pending(), true)
		_, err = r.IdempotencyKeys.Get(ctx, org, "missing")
		wantErr(t, "Get() unknown", err, core.ErrNotFound)

		// Release drops a pending record only.
		noErr(t, "Release()", r.IdempotencyKeys.Release(ctx, org, "k1"))
		_, err = r.IdempotencyKeys.Get(ctx, org, "k1")
		wantErr(t, "Get() after Release()", err, core.ErrNotFound)
		ok, err = r.IdempotencyKeys.Claim(ctx, first)
		noErr(t, "Claim() after Release()", err)
		equal(t, "Claim() after Release()", ok, true)
		noErr(t, "Complete()", r.IdempotencyKeys.Complete(ctx, org, "k1", []byte("response"), first.ExpiresAt.Add(time.Hour)))
		noErr(t, "Release() of a completed record", r.IdempotencyKeys.Release(ctx, org, "k1"))
		got, err = r.IdempotencyKeys.Get(ctx, org, "k1")
		noErr(t, "Get() completed", err)
		first.Response = []byte("response")
		first.ExpiresAt = first.ExpiresAt.Add(time.Hour)
		equal(t, "Get() completed", got, first)
		wantErr(t, "Complete() unknown", r.IdempotencyKeys.Complete(ctx, org, "missing", nil, now), core.ErrNotFound)
		noErr(t, "Complete() with an empty response", r.IdempotencyKeys.Complete(ctx, ob, "k1", nil, now.Add(24*time.Hour)))
		got, err = r.IdempotencyKeys.Get(ctx, ob, "k1")
		noErr(t, "Get() with an empty response", err)
		equal(t, "Pending() with an empty response", got.Pending(), false)

		// An expired record is replaced by a new claim.
		later := rec(org, "k1", first.ExpiresAt)
		later.RequestHash = []byte{9}
		ok, err = r.IdempotencyKeys.Claim(ctx, later)
		noErr(t, "Claim() of an expired key", err)
		equal(t, "Claim() of an expired key", ok, true)
		got, err = r.IdempotencyKeys.Get(ctx, org, "k1")
		noErr(t, "Get() replaced", err)
		equal(t, "Get() replaced", got, later)

		// Purge deletes the records expired at the instant, per
		// organization and in batches.
		for _, k := range []string{"old1", "old2", "old3"} {
			if _, err := r.IdempotencyKeys.Claim(ctx, rec(org, k, at(-48*time.Hour))); err != nil {
				return err
			}
		}
		orgs, err := r.IdempotencyKeys.Organizations(ctx, now)
		noErr(t, "Organizations()", err)
		equal(t, "Organizations()", orgs, []core.OrganizationID{org})
		n, err := r.IdempotencyKeys.Purge(ctx, ob, now, 10)
		noErr(t, "Purge() of another organization", err)
		equal(t, "Purge() of another organization", n, 0)
		n, err = r.IdempotencyKeys.Purge(ctx, org, now, 2)
		noErr(t, "Purge()", err)
		equal(t, "Purge() first batch", n, 2)
		n, err = r.IdempotencyKeys.Purge(ctx, org, now, 2)
		noErr(t, "Purge()", err)
		equal(t, "Purge() second batch", n, 1)
		_, err = r.IdempotencyKeys.Get(ctx, org, "k1")
		noErr(t, "Get() of a live record after Purge()", err)
		orgs, err = r.IdempotencyKeys.Organizations(ctx, now)
		noErr(t, "Organizations() after Purge()", err)
		equal(t, "Organizations() after Purge()", len(orgs), 0)
		return nil
	})
}
