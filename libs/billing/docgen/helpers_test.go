package docgen_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/billing/billingtest"
	"github.com/tanvoxyz/tanvo/libs/billing/docgen"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/auditlog"
	"github.com/tanvoxyz/tanvo/libs/core/creditnote"
	"github.com/tanvoxyz/tanvo/libs/core/customer"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/core/organization"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// blobs keeps files in memory and links them under files.test.
type blobs struct {
	mu      sync.Mutex
	n       int
	files   map[core.BlobRef][]byte
	types   map[core.BlobRef]string
	deleted []core.BlobRef
}

var _ port.BlobStore = (*blobs)(nil)

func (b *blobs) Put(_ context.Context, _ core.OrganizationID, name, contentType string, body io.Reader) (core.BlobRef, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.files == nil {
		b.files, b.types = map[core.BlobRef][]byte{}, map[core.BlobRef]string{}
	}
	b.n++
	ref := core.BlobRef(strconv.Itoa(b.n) + "/" + name)
	b.files[ref], b.types[ref] = data, contentType
	return ref, nil
}

func (b *blobs) Get(_ context.Context, _ core.OrganizationID, ref core.BlobRef) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.files[ref]
	if !ok {
		return nil, core.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *blobs) Delete(_ context.Context, _ core.OrganizationID, ref core.BlobRef) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.files, ref)
	b.deleted = append(b.deleted, ref)
	return nil
}

func (b *blobs) URL(_ context.Context, _ core.OrganizationID, ref core.BlobRef) (string, error) {
	return "https://files.test/" + string(ref), nil
}

// put stores a file directly, as an earlier generation would have.
func (b *blobs) put(t *testing.T, name, contentType string) core.BlobRef {
	t.Helper()
	ref, err := b.Put(context.Background(), core.OrganizationID{}, name, contentType, bytes.NewReader([]byte("old")))
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// renderer is a fake [docgen.Renderer] that counts its calls; it issues
// e-invoices for e-invoicing billing entities in France, as the real one
// does for its supported countries.
type renderer struct {
	mu      sync.Mutex
	calls   map[string]int
	failPDF error

	invoice    *docgen.Invoice
	creditNote *docgen.CreditNote
	receipt    *docgen.PaymentReceipt
}

var _ docgen.Renderer = (*renderer)(nil)

func (r *renderer) count(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	r.calls[name]++
}

func (r *renderer) n(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[name]
}

func (r *renderer) EInvoicing(be *organization.BillingEntity) bool {
	return be.EInvoicing && be.Address.Country == "FR"
}

func (r *renderer) pdf(name string) ([]byte, error) {
	r.count(name)
	if r.failPDF != nil {
		return nil, r.failPDF
	}
	return []byte("%PDF " + name), nil
}

func (r *renderer) InvoicePDF(_ context.Context, d *docgen.Invoice) ([]byte, error) {
	r.invoice = d
	return r.pdf("invoice.pdf")
}

func (r *renderer) InvoiceXML(_ context.Context, d *docgen.Invoice) ([]byte, error) {
	r.invoice = d
	r.count("invoice.xml")
	return []byte("<Invoice/>"), nil
}

func (r *renderer) CreditNotePDF(_ context.Context, d *docgen.CreditNote) ([]byte, error) {
	r.creditNote = d
	return r.pdf("credit_note.pdf")
}

func (r *renderer) CreditNoteXML(_ context.Context, d *docgen.CreditNote) ([]byte, error) {
	r.creditNote = d
	r.count("credit_note.xml")
	return []byte("<CreditNote/>"), nil
}

func (r *renderer) PaymentReceiptPDF(_ context.Context, d *docgen.PaymentReceipt) ([]byte, error) {
	r.receipt = d
	return r.pdf("receipt.pdf")
}

func (r *renderer) PaymentReceiptXML(_ context.Context, d *docgen.PaymentReceipt) ([]byte, error) {
	r.receipt = d
	r.count("receipt.xml")
	return []byte("<Receipt/>"), nil
}

// errAttach is a failure to embed the e-invoice in a PDF.
var errAttach = errors.New("attach factur-x.xml: failed")

// env is a docgen service over a harness, a fake blob store and a fake
// renderer.
type env struct {
	*billingtest.Harness
	svc    *docgen.Service
	blobs  *blobs
	render *renderer
	cust   *customer.Customer
}

func newEnv(t *testing.T, opts ...docgen.Options) *env {
	t.Helper()
	h := billingtest.New(t, billingtest.Date(2025, time.March, 10))
	h.LogActivities(auditlog.Actor{})
	e := &env{Harness: h, blobs: &blobs{}, render: &renderer{}}
	var o docgen.Options
	if len(opts) > 0 {
		o = opts[0]
	}
	svc, err := docgen.New(h.Deps, e.blobs, e.render, o)
	if err != nil {
		t.Fatal(err)
	}
	e.svc = svc
	e.cust = h.Customer(func(c *customer.Customer) { c.Email = new("billing@customer.test") })
	return e
}

// einvoicing makes the billing entity issue e-invoices from country.
func (e *env) einvoicing(country string) {
	e.UpdateBillingEntity(func(be *organization.BillingEntity) {
		be.EInvoicing = true
		be.Address.Country = country
	})
}

func finalized(inv *invoice.Invoice) { inv.Status = invoice.StatusFinalized }

func (e *env) invoice(opts ...func(*invoice.Invoice)) *invoice.Invoice {
	return e.StoredInvoice(e.cust, append([]func(*invoice.Invoice){finalized}, opts...)...)
}

func (e *env) creditNote(opts ...func(*creditnote.CreditNote)) *creditnote.CreditNote {
	return e.StoredCreditNote(e.invoice(), 1000, opts...)
}

// receipt stores a receipt of a payment of inv.
func (e *env) receipt(inv *invoice.Invoice, opts ...func(*payment.Receipt)) *payment.Receipt {
	e.T.Helper()
	p := e.StoredPayment(payment.InvoicePayable(inv.ID), e.cust.ID, 1000, payment.StateSucceeded, core.IntegrationID{})
	rc := &payment.Receipt{
		ID: core.NewID[core.PaymentReceiptID](e.IDs), OrganizationID: e.OrgID(), PaymentID: p.ID,
		BillingEntityID: inv.BillingEntityID, Number: payment.FormatReceiptNumber("ORG", 1),
		CreatedAt: e.Now(), UpdatedAt: e.Now(),
	}
	for _, o := range opts {
		o(rc)
	}
	e.Do(func(ctx context.Context, r port.Repos) error { return r.PaymentReceipts.Create(ctx, rc) })
	return rc
}

func (e *env) reloadInvoice(id core.InvoiceID) *invoice.Invoice { return e.Invoice(id) }

func (e *env) reloadReceipt(id core.PaymentReceiptID) *payment.Receipt {
	return billingtest.Read(e.Harness, func(ctx context.Context, r port.Repos) (*payment.Receipt, error) {
		return r.PaymentReceipts.Get(ctx, e.OrgID(), id)
	})
}

// jobs returns the args of the enqueued jobs of kind.
func (e *env) jobs(kind string) []port.Job {
	var out []port.Job
	for _, j := range e.Store.Jobs() {
		if j.Kind == kind {
			out = append(out, j)
		}
	}
	return out
}
