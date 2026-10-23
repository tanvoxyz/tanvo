package einvoice

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// --- XML queries -----------------------------------------------------------

// parseXML reads a rendered document back into a node tree, keeping the
// namespace prefixes, so tests can query it with [selectNodes].
func parseXML(t *testing.T, b []byte) *node {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(b))
	root := &node{name: "#document"}
	stack := []*node{root}
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("parse rendered XML: %v\n%s", err, b)
		}
		top := stack[len(stack)-1]
		switch tok := tok.(type) {
		case xml.StartElement:
			n := &node{name: qname(tok.Name)}
			for _, a := range tok.Attr {
				n.attrs = append(n.attrs, attr{qname(a.Name), a.Value})
			}
			top.children = append(top.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if hasElements(top) {
				top.text = ""
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			top.text += string(tok)
		case xml.Comment:
			top.children = append(top.children, &node{comment: true, text: string(tok)})
		}
	}
	if len(stack) != 1 {
		t.Fatalf("unbalanced rendered XML:\n%s", b)
	}
	return root
}

func qname(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

func hasElements(n *node) bool {
	for _, c := range n.children {
		if !c.comment {
			return true
		}
	}
	return false
}

// selectNodes evaluates the XPath subset the ported specs use: "/" child
// and "//" descendant steps, "*" and qualified names, and the predicates
// [n] (position among the matching siblings) and [path='value'].
func selectNodes(root *node, path string) []*node {
	ctx := []*node{root}
	for path != "" {
		desc := strings.HasPrefix(path, "//")
		path = strings.TrimPrefix(strings.TrimPrefix(path, "/"), "/")
		end, depth := len(path), 0
		for i, r := range path {
			switch {
			case r == '[':
				depth++
			case r == ']':
				depth--
			case r == '/' && depth == 0:
				end = i
			}
			if end != len(path) {
				break
			}
		}
		step := path[:end]
		path = path[end:]
		name, preds := splitStep(step)
		var next []*node
		seen := map[*node]bool{}
		for _, parent := range parents(ctx, desc) {
			var matching []*node
			for _, c := range parent.children {
				if !c.comment && (name == "*" || c.name == name) {
					matching = append(matching, c)
				}
			}
			for _, p := range preds {
				matching = applyPredicate(matching, p)
			}
			for _, m := range matching {
				if !seen[m] {
					seen[m] = true
					next = append(next, m)
				}
			}
		}
		ctx = next
	}
	return ctx
}

// parents are the nodes whose children a step selects: ctx itself, or ctx
// and all their descendants.
func parents(ctx []*node, desc bool) []*node {
	if !desc {
		return ctx
	}
	var out []*node
	var walk func(n *node)
	walk = func(n *node) {
		out = append(out, n)
		for _, c := range n.children {
			if !c.comment {
				walk(c)
			}
		}
	}
	for _, n := range ctx {
		walk(n)
	}
	return out
}

func splitStep(step string) (string, []string) {
	i := strings.IndexByte(step, '[')
	if i < 0 {
		return step, nil
	}
	name, rest := step[:i], step[i:]
	var preds []string
	for rest != "" {
		depth := 0
		for j, r := range rest {
			if r == '[' {
				depth++
			} else if r == ']' {
				depth--
				if depth == 0 {
					preds = append(preds, rest[1:j])
					rest = rest[j+1:]
					break
				}
			}
		}
	}
	return name, preds
}

func applyPredicate(nodes []*node, pred string) []*node {
	if n, err := strconv.Atoi(pred); err == nil {
		if n < 1 || n > len(nodes) {
			return nil
		}
		return nodes[n-1 : n]
	}
	sub, want, _ := strings.Cut(pred, "=")
	want = strings.Trim(want, "'")
	var out []*node
	for _, n := range nodes {
		for _, m := range selectNodes(n, "/"+sub) {
			if m.text == want {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// comments are the texts of every comment of the tree.
func comments(n *node) []string {
	var out []string
	for _, c := range n.children {
		if c.comment {
			out = append(out, c.text)
		} else {
			out = append(out, comments(c)...)
		}
	}
	return out
}

// check is one expectation about a document.
type check func(t *testing.T, doc *node)

// val expects the first node at path to hold want.
func val(path, want string) check {
	return func(t *testing.T, doc *node) {
		t.Helper()
		got := selectNodes(doc, path)
		if len(got) == 0 {
			t.Errorf("%s: got = no node, want = %q", path, want)
			return
		}
		if got[0].text != want {
			t.Errorf("%s: got = %q, want = %q", path, got[0].text, want)
		}
	}
}

// attrVal expects the first node at path to have attribute name = want.
func attrVal(path, name, want string) check {
	return func(t *testing.T, doc *node) {
		t.Helper()
		got := selectNodes(doc, path)
		if len(got) == 0 {
			t.Errorf("%s: got = no node, want = @%s=%q", path, name, want)
			return
		}
		for _, a := range got[0].attrs {
			if a.name == name {
				if a.value != want {
					t.Errorf("%s/@%s: got = %q, want = %q", path, name, a.value, want)
				}
				return
			}
		}
		t.Errorf("%s/@%s: got = no attribute, want = %q", path, name, want)
	}
}

// has expects a node at path.
func has(path string) check {
	return func(t *testing.T, doc *node) {
		t.Helper()
		if len(selectNodes(doc, path)) == 0 {
			t.Errorf("%s: got = no node, want = a node", path)
		}
	}
}

// none expects no node at path.
func none(path string) check {
	return func(t *testing.T, doc *node) {
		t.Helper()
		if got := selectNodes(doc, path); len(got) != 0 {
			t.Errorf("%s: got = %d nodes, want = none", path, len(got))
		}
	}
}

// count expects n nodes at path.
func count(path string, n int) check {
	return func(t *testing.T, doc *node) {
		t.Helper()
		if got := len(selectNodes(doc, path)); got != n {
			t.Errorf("%s: got = %d nodes, want = %d", path, got, n)
		}
	}
}

// sum expects the values at path to add up to want.
func sum(path, want string) check {
	return func(t *testing.T, doc *node) {
		t.Helper()
		total := money.Zero
		for _, n := range selectNodes(doc, path) {
			total = total.Add(money.MustParse(n.text))
		}
		if !total.Equal(money.MustParse(want)) {
			t.Errorf("Σ %s: got = %s, want = %s", path, total, want)
		}
	}
}

// values expects the values at path, in order.
func values(path string, want ...string) check {
	return func(t *testing.T, doc *node) {
		t.Helper()
		var got []string
		for _, n := range selectNodes(doc, path) {
			got = append(got, n.text)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: got = %q, want = %q", path, got, want)
		}
	}
}

// comment expects a comment with text want.
func comment(want string) check {
	return func(t *testing.T, doc *node) {
		t.Helper()
		got := comments(doc)
		for _, c := range got {
			if c == want {
				return
			}
		}
		t.Errorf("comments: got = %q, want = %q among them", got, want)
	}
}

// xmlCase is a ported spec example: a document and what it must contain.
type xmlCase struct {
	name   string
	source string
	doc    func(t *testing.T) []byte
	checks []check
}

func runXMLCases(t *testing.T, cases []xmlCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Logf("source: %s", c.source)
			doc := parseXML(t, c.doc(t))
			for _, chk := range c.checks {
				chk(t, doc)
			}
		})
	}
}

// fragment renders the nodes fn appends under a wrapper element.
func fragment(fn func(n *node)) func(t *testing.T) []byte {
	return func(*testing.T) []byte {
		root := newRoot("fragment")
		fn(root)
		return root.document()
	}
}

// must renders a builder's output, failing on error.
func must(fn func() ([]byte, error)) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		b, err := fn()
		if err != nil {
			t.Fatalf("render: got = %v, want = nil", err)
		}
		return b
	}
}

// --- fixtures (mirroring the spec factories) -------------------------------

func dec(s string) money.Decimal { return money.MustParse(s) }

func ptr(s string) *string { return &s }

func cur(code string) money.Currency { return money.MustCurrency(code) }

func date(s string) core.Date {
	d, err := core.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func instant(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

const (
	invoiceUUID    = "01960000-0000-7000-8000-000000000001"
	creditNoteUUID = "01960000-0000-7000-8000-000000000002"
	invoice2UUID   = "01960000-0000-7000-8000-000000000003"
)

func newBillingEntity(country string) *organization.BillingEntity {
	return &organization.BillingEntity{
		Code:      "BE_CODE",
		Name:      "Billing Entity",
		LegalName: ptr("BE Legal Name"),
		TaxID:     ptr("BR987654321"),
		Email:     ptr("billing@example.com"),
		Phone:     ptr("+33 1 23 45 67 89"),
		Address: core.Address{
			Line1:   "Rue quelque part",
			Line2:   "Tourne au deuxième angle",
			City:    "Eine Stadt",
			Zipcode: "60192460",
			Country: country,
		},
		EInvoicing: true,
	}
}

func newCustomer() *customer.Customer {
	return &customer.Customer{
		ExternalID: "cust-ext-1",
		Name:       ptr("Its Mii"),
		Email:      ptr("customer@example.com"),
		BillingAddress: core.Address{
			Line1:   "Streets of Tomorrow",
			Line2:   "on AC",
			City:    "SP",
			Zipcode: "123654789",
			Country: "BR",
		},
	}
}

func newInvoice(typ invoice.Type, code string) *invoice.Invoice {
	return &invoice.Invoice{
		ID:             core.MustParseID[core.InvoiceID](invoiceUUID),
		Type:           typ,
		Number:         "LAGO-TEST-123",
		Currency:       cur(code),
		IssuingDate:    date("2025-03-15"),
		PaymentDueDate: date("2025-03-15"),
		CreatedAt:      instant("2025-03-15T10:00:00Z"),
	}
}

// newFee is the fee factory: 2.00 EUR with 0.02 of tax.
func newFee(mod func(f *invoice.Fee)) invoice.Fee {
	f := invoice.Fee{
		Type:               core.FeeTypeSubscription,
		Amount:             200,
		PreciseAmount:      dec("200.0000000001"),
		PreciseUnitAmount:  money.Zero,
		Units:              money.Zero,
		TaxesAmount:        2,
		TaxesPreciseAmount: dec("2.0000000001"),
		TaxesRate:          money.Zero,
		Currency:           cur("EUR"),
	}
	if mod != nil {
		mod(&f)
	}
	return f
}

func line(f invoice.Fee) Line {
	return Line{Fee: f, Name: "Premium", Description: "Premium plan"}
}

func newInvoiceInput(typ invoice.Type, code string, fees ...invoice.Fee) *Invoice {
	in := &Invoice{
		Invoice:       newInvoice(typ, code),
		BillingEntity: newBillingEntity("FR"),
		Customer:      newCustomer(),
	}
	for _, f := range fees {
		in.Lines = append(in.Lines, line(f))
	}
	return in
}

func newCreditNote() *creditnote.CreditNote {
	return &creditnote.CreditNote{
		ID:           core.MustParseID[core.CreditNoteID](creditNoteUUID),
		Number:       "LAGO-TEST-123-CN001",
		Reason:       creditnote.Reason("duplicated_charge"),
		TotalAmount:  120,
		TaxesAmount:  20,
		CreditAmount: 120,
		Currency:     cur("EUR"),
		IssuingDate:  date("2025-03-17"),
		CreatedAt:    instant("2025-03-17T09:30:00Z"),
	}
}

// cnLine is the credit note item factory: 1.00 EUR of fee.
func cnLine(f invoice.Fee, mod func(it *creditnote.Item)) CreditNoteLine {
	it := creditnote.Item{Amount: 100, PreciseAmount: dec("100")}
	if mod != nil {
		mod(&it)
	}
	return CreditNoteLine{Item: it, Fee: f, Name: "Premium", Description: "Premium plan"}
}

func newCreditNoteInput(inv *invoice.Invoice, lines ...CreditNoteLine) *CreditNote {
	return &CreditNote{
		CreditNote:    newCreditNote(),
		Invoice:       inv,
		BillingEntity: newBillingEntity("FR"),
		Customer:      newCustomer(),
		Lines:         lines,
	}
}

func newPaymentInput(kind payment.Kind, method payment.MethodDetails) *Payment {
	inv := newInvoice(invoice.TypeSubscription, "BRL")
	inv.Number = "INV-24680-OIC-E"
	inv.TotalAmount = 1000
	return &Payment{
		Payment: &payment.Payment{
			Kind:                  kind,
			Amount:                1000,
			Currency:              cur("BRL"),
			ProviderPaymentMethod: method,
			CreatedAt:             instant("2025-10-22T10:00:00Z"),
		},
		Receipt: &payment.Receipt{
			Number:    "LAG-1234-001-001",
			CreatedAt: instant("2025-10-22T10:00:05Z"),
		},
		BillingEntity: func() *organization.BillingEntity {
			be := newBillingEntity("FR")
			be.TaxID = ptr("MAR1234BR")
			return be
		}(),
		Customer:     newCustomer(),
		Invoices:     []*invoice.Invoice{inv},
		ProviderType: "stripe",
	}
}
