package creditnotes_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/creditnotes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/catalog"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// now is the clock of the tests.
var now = billingtest.Date(2026, time.March, 10, 12)

// fixture is a customer with one invoice to credit.
type fixture struct {
	h    *billingtest.Harness
	svc  *creditnotes.Service
	cust *customer.Customer
	tax  *catalog.Tax
	inv  *invoice.Invoice
	fees []*invoice.Fee
}

// newHarness returns a harness and the service over it.
func newHarness(t *testing.T) (*billingtest.Harness, *creditnotes.Service) {
	t.Helper()
	h := billingtest.New(t, now)
	return h, creditnotes.New(h.Deps)
}

// newFixture creates, as the lago create_service_spec does, an invoice of
// two fees of 10 taxed at 20% (total 24, 6 paid, payment succeeded).
func newFixture(t *testing.T) *fixture {
	t.Helper()
	h, svc := newHarness(t)
	f := &fixture{h: h, svc: svc, cust: h.Customer()}
	f.tax = h.Tax("vat", "20")
	f.inv, f.fees = h.CreditableInvoice(f.cust, []int64{10, 10}, []*catalog.Tax{f.tax},
		[]func(*invoice.Invoice){func(inv *invoice.Invoice) { inv.TotalPaidAmount = 6 }})
	return f
}

// input returns a credit note input on the fixture's invoice.
func (f *fixture) input(credit, refund int64, items ...creditnotes.ItemInput) creditnotes.CreateInput {
	return creditnotes.CreateInput{
		OrganizationID: f.h.OrgID(), InvoiceID: f.inv.ID, Items: items,
		CreditAmount: credit, RefundAmount: refund,
	}
}

// item is an item of amount on fee.
func item(fee *invoice.Fee, amount string) creditnotes.ItemInput {
	return creditnotes.ItemInput{FeeID: fee.ID, Amount: money.MustParse(amount)}
}

// fieldCodes returns the field errors of err as "field:code".
func fieldCodes(err error) []string {
	var out []string
	for _, e := range core.FieldErrors(err) {
		out = append(out, e.Field+":"+e.Code)
	}
	slices.Sort(out)
	return out
}

// ruleCode returns the code of a rule error, or "".
func ruleCode(err error) string {
	var re *core.RuleError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// wantFields fails unless err carries exactly the field errors want.
func wantFields(t *testing.T, err error, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := fieldCodes(err); !slices.Equal(got, want) {
		t.Fatalf("field errors: got = %v (%v), want = %v", got, err, want)
	}
}

// wantRule fails unless err is a rule error with code.
func wantRule(t *testing.T, err error, code string) {
	t.Helper()
	if got := ruleCode(err); got != code {
		t.Fatalf("rule: got = %q (%v), want = %q", got, err, code)
	}
}

// must fails on err.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("got = %v, want = nil", err)
	}
}

// eq fails unless got equals want.
func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got = %v, want = %v", what, got, want)
	}
}

// contains reports whether s contains v.
func contains[T comparable](s []T, v T) bool { return slices.Contains(s, v) }

// wantFieldIncluded fails unless err carries the field error want.
func wantFieldIncluded(t *testing.T, err error, want string) {
	t.Helper()
	if got := fieldCodes(err); !slices.Contains(got, want) {
		t.Fatalf("field errors: got = %v (%v), want = including %v", got, err, want)
	}
}

// decEq fails unless got equals the decimal want.
func decEq(t *testing.T, what string, got money.Decimal, want string) {
	t.Helper()
	if !got.Equal(money.MustParse(want)) {
		t.Errorf("%s: got = %v, want = %v", what, got, want)
	}
}
