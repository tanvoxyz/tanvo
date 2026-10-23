package porttest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// now is the base instant of the fixtures: UTC with microsecond precision.
var now = time.Date(2025, time.March, 14, 15, 9, 26, 535897000, time.UTC)

// env is one test's transactor with helpers.
type env struct {
	t   *testing.T
	h   Harness
	tr  port.Transactor
	ids core.IDGenerator
}

// newEnv returns an environment on a fresh transactor.
func newEnv(t *testing.T, h Harness) *env {
	t.Helper()
	return &env{t: t, h: h, tr: h.New(t), ids: core.UUIDv7Generator{}}
}

// at returns now shifted by d.
func at(d time.Duration) time.Time { return now.Add(d) }

// ptr returns &v.
func ptr[T any](v T) *T { return &v }

// tx runs fn in a transaction and fails the test on error.
func (e *env) tx(fn func(ctx context.Context, r port.Repos) error) {
	e.t.Helper()
	if err := e.tr.InTx(e.t.Context(), fn); err != nil {
		e.t.Fatalf("InTx() error got = %v, want = nil", err)
	}
}

// txErr runs fn in a transaction and returns its error.
func (e *env) txErr(fn func(ctx context.Context, r port.Repos) error) error {
	return e.tr.InTx(e.t.Context(), fn)
}

// jobs returns the committed jobs.
func (e *env) jobs() []port.Job {
	e.t.Helper()
	return e.h.Jobs(e.t, e.tr)
}

// org creates an organization named name with a default billing entity
// and returns both.
func (e *env) org(name string) (*organization.Organization, *organization.BillingEntity) {
	e.t.Helper()
	o := organization.New(core.NewID[core.OrganizationID](e.ids), name, now)
	b := organization.NewBillingEntity(o.ID, core.NewID[core.BillingEntityID](e.ids), "main", name, now)
	b.IsDefault = true
	e.tx(func(ctx context.Context, r port.Repos) error {
		if err := r.Organizations.Create(ctx, o); err != nil {
			return err
		}
		return r.BillingEntities.Create(ctx, b)
	})
	return o, b
}

// twoOrgs creates two organizations, for isolation tests.
func (e *env) twoOrgs() (a, b *organization.BillingEntity) {
	e.t.Helper()
	_, a = e.org("Acme")
	_, b = e.org("Globex")
	return a, b
}

// cmpOptions compare the domain's value types by value: decimals with
// Equal, currencies by code, time zones by name, JSON by content, and nil
// and empty slices and maps alike. Instants compare with time.Time.Equal.
var cmpOptions = []cmp.Option{
	cmp.Comparer(func(a, b money.Decimal) bool { return a.Equal(b) }),
	cmp.Comparer(func(a, b money.Currency) bool { return a.Code() == b.Code() }),
	cmp.Comparer(func(a, b *time.Location) bool {
		if a == nil || b == nil {
			return a == b
		}
		return a.String() == b.String()
	}),
	cmp.Comparer(func(a, b catalog.Properties) bool { return jsonEqual(a.Raw(), b.Raw()) }),
	// Only for a non-empty pair: EquateEmpty covers two empty values, and
	// both options applying to one pair makes cmp panic.
	cmp.FilterValues(func(a, b json.RawMessage) bool { return len(a) > 0 || len(b) > 0 },
		cmp.Comparer(func(a, b json.RawMessage) bool { return jsonEqual(a, b) })),
	cmpopts.EquateEmpty(),
	// Value types of the domain (IDs and the like) compare by their
	// unexported fields.
	cmp.Exporter(func(t reflect.Type) bool { return strings.HasPrefix(t.PkgPath(), "github.com/tanvoxyz/tanvo/libs/") }),
}

// jsonEqual reports whether a and b are the same JSON value (two empty
// documents are equal).
func jsonEqual(a, b []byte) bool {
	a, b = bytes.TrimSpace(a), bytes.TrimSpace(b)
	if len(a) == 0 || len(b) == 0 || bytes.Equal(a, []byte("null")) || bytes.Equal(b, []byte("null")) {
		return isEmptyJSON(a) && isEmptyJSON(b)
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

func isEmptyJSON(b []byte) bool { return len(b) == 0 || bytes.Equal(b, []byte("null")) }

// Diff returns the differences between got and want ("" when equal) with
// the suite's comparison options and opts.
func Diff(got, want any, opts ...cmp.Option) string {
	return cmp.Diff(want, got, append(append([]cmp.Option{}, cmpOptions...), opts...)...)
}

// equal fails the test when got differs from want.
func equal(t *testing.T, what string, got, want any, opts ...cmp.Option) {
	t.Helper()
	if d := Diff(got, want, opts...); d != "" {
		t.Errorf("%s mismatch (-want +got):\n%s", what, d)
	}
}

// wantErr fails the test unless errors.Is(err, target).
func wantErr(t *testing.T, what string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s error got = %v, want = %v", what, err, target)
	}
}

// noErr fails the test now when err is not nil.
func noErr(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s error got = %v, want = nil", what, err)
	}
}

// ids returns the IDs of items, for comparing list results.
func ids[T any, I any](items []T, id func(T) I) []I {
	out := make([]I, len(items))
	for i, it := range items {
		out[i] = id(it)
	}
	return out
}

// listAll pages through list with page size size and returns every item,
// failing the test on error.
func listAll[T any](t *testing.T, size int, list func(core.PageRequest) (core.Page[T], error)) []T {
	t.Helper()
	var out []T
	req := core.PageRequest{Size: size}
	for range 1000 {
		p, err := list(req)
		noErr(t, "list", err)
		out = append(out, p.Items...)
		if p.NextToken == "" {
			return out
		}
		req.Token = p.NextToken
	}
	t.Fatalf("list: too many pages")
	return nil
}
