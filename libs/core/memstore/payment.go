package memstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
)

// paymentData holds the tables of package payment.
type paymentData struct {
	payments          *table[core.PaymentID, payment.Payment]
	receipts          *table[core.PaymentReceiptID, payment.Receipt]
	refunds           *table[core.RefundID, payment.Refund]
	requests          *table[core.PaymentRequestID, payment.Request]
	methods           *table[core.PaymentMethodID, payment.Method]
	providerCustomers *table[core.PaymentProviderCustomerID, payment.ProviderCustomer]
	intents           *table[payment.IntentID, payment.Intent]
	inboundWebhooks   *table[core.InboundWebhookID, payment.InboundWebhook]
}

func newPaymentData() *paymentData {
	return &paymentData{
		payments: newTable[core.PaymentID](func(p payment.Payment) payment.Payment {
			p.ProviderPaymentData = slices.Clone(p.ProviderPaymentData)
			return p
		}),
		receipts: newTable[core.PaymentReceiptID, payment.Receipt](nil),
		refunds:  newTable[core.RefundID, payment.Refund](nil),
		requests: newTable[core.PaymentRequestID](func(r payment.Request) payment.Request {
			r.InvoiceIDs = slices.Clone(r.InvoiceIDs)
			return r
		}),
		methods: newTable[core.PaymentMethodID, payment.Method](nil),
		providerCustomers: newTable[core.PaymentProviderCustomerID](func(c payment.ProviderCustomer) payment.ProviderCustomer {
			c.ProviderPaymentMethods = slices.Clone(c.ProviderPaymentMethods)
			return c
		}),
		intents: newTable[payment.IntentID, payment.Intent](nil),
		inboundWebhooks: newTable[core.InboundWebhookID](func(w payment.InboundWebhook) payment.InboundWebhook {
			w.Payload = slices.Clone(w.Payload)
			return w
		}),
	}
}

func (d *paymentData) snapshot() *paymentData {
	return &paymentData{
		payments:          d.payments.snapshot(),
		receipts:          d.receipts.snapshot(),
		refunds:           d.refunds.snapshot(),
		requests:          d.requests.snapshot(),
		methods:           d.methods.snapshot(),
		providerCustomers: d.providerCustomers.snapshot(),
		intents:           d.intents.snapshot(),
		inboundWebhooks:   d.inboundWebhooks.snapshot(),
	}
}

// paymentRepo implements payment.Repository.
type paymentRepo struct {
	d  *paymentData
	st *state // the whole transaction state, for the list filters
}

var _ payment.Repository = paymentRepo{}

func (r paymentRepo) check(p *payment.Payment) error {
	if p.Kind != payment.KindProvider || p.Final() {
		return nil
	}
	if r.d.payments.any(func(x *payment.Payment) bool {
		return x.ID != p.ID && x.OrganizationID == p.OrganizationID && x.Payable == p.Payable &&
			x.Kind == payment.KindProvider && !x.Final()
	}) {
		return conflict("payment", "pending provider payment of "+p.Payable.String())
	}
	return nil
}

func (r paymentRepo) Create(_ context.Context, p *payment.Payment) error {
	if r.d.payments.has(p.ID) {
		return conflict("payment", "id")
	}
	if err := r.check(p); err != nil {
		return err
	}
	r.d.payments.put(p.ID, *p)
	return nil
}

func (r paymentRepo) Update(_ context.Context, p *payment.Payment) error {
	cur, ok := r.d.payments.get(p.ID)
	if !ok || cur.OrganizationID != p.OrganizationID {
		return notFound("payment", p.ID)
	}
	if err := r.check(p); err != nil {
		return err
	}
	r.d.payments.put(p.ID, *p)
	return nil
}

func (r paymentRepo) Delete(_ context.Context, org core.OrganizationID, id core.PaymentID) error {
	cur, ok := r.d.payments.get(id)
	if !ok || cur.OrganizationID != org {
		return notFound("payment", id)
	}
	r.d.payments.del(id)
	return nil
}

func (r paymentRepo) Get(_ context.Context, org core.OrganizationID, id core.PaymentID) (*payment.Payment, error) {
	p, ok := r.d.payments.get(id)
	if !ok || p.OrganizationID != org {
		return nil, notFound("payment", id)
	}
	return p, nil
}

func (r paymentRepo) GetByProviderPaymentID(_ context.Context, org core.OrganizationID, integration core.IntegrationID, providerPaymentID string) (*payment.Payment, error) {
	p, ok := r.d.payments.first(func(x *payment.Payment) bool {
		return x.OrganizationID == org && x.IntegrationID == integration &&
			x.ProviderPaymentID != nil && *x.ProviderPaymentID == providerPaymentID
	})
	if !ok {
		return nil, notFound("payment", stringer(providerPaymentID))
	}
	return p, nil
}

func (r paymentRepo) ListByPayable(_ context.Context, org core.OrganizationID, payable payment.Payable) ([]*payment.Payment, error) {
	return r.d.payments.find(func(x *payment.Payment) bool {
		return x.OrganizationID == org && x.Payable == payable
	}), nil
}

func (r paymentRepo) ListAbandoned(_ context.Context, org core.OrganizationID, from, to time.Time) ([]*payment.Payment, error) {
	return r.d.payments.find(func(x *payment.Payment) bool {
		return x.OrganizationID == org && x.Kind == payment.KindProvider && x.Payable.Type == payment.PayableTypeInvoice &&
			x.Status == payment.StateProcessing && x.ProviderStatus != nil && *x.ProviderStatus == payment.ProviderStatusRequiresAction &&
			!x.UpdatedAt.Before(from) && !x.UpdatedAt.After(to)
	}), nil
}

func (r paymentRepo) List(_ context.Context, org core.OrganizationID, f payment.Filter, page core.PageRequest) (core.Page[*payment.Payment], error) {
	if !f.Order.Valid() {
		return core.Page[*payment.Payment]{}, fmt.Errorf("list payments: %w: order", core.ErrInvalidValue)
	}
	rows := r.d.payments.find(func(x *payment.Payment) bool {
		if x.OrganizationID != org ||
			!f.CustomerID.IsZero() && x.CustomerID != f.CustomerID ||
			f.Status != "" && x.Status != f.Status ||
			!f.Currency.IsZero() && x.Currency.Code() != f.Currency.Code() ||
			!f.InvoiceID.IsZero() && !paysInvoice(r.d, x, f.InvoiceID) ||
			!r.matchesSearch(x, f) {
			return false
		}
		if f.Visible && !x.Payable.InvoiceID.IsZero() {
			inv, ok := r.st.invoice.invoices.get(x.Payable.InvoiceID)
			return ok && inv.Visible()
		}
		return true
	})
	if f.Order == core.OrderRecent {
		return paginateRecent(rows, func(x *payment.Payment) (time.Time, uuid.UUID) { return x.CreatedAt, x.ID.UUID() }, page)
	}
	return paginate(rows, func(x *payment.Payment) core.PaymentID { return x.ID }, page)
}

// matchesSearch reports whether x matches the search term of f (see
// [payment.Filter.Search]).
func (r paymentRepo) matchesSearch(x *payment.Payment, f payment.Filter) bool {
	term := core.SearchTerm(f.Search)
	if term == "" {
		return true
	}
	if id, ok := payment.SearchID(term); ok && x.ID == id {
		return true
	}
	term = strings.ToLower(term)
	contains := func(s *string) bool { return s != nil && strings.Contains(strings.ToLower(*s), term) }
	if contains(x.ProviderPaymentID) || contains(x.Reference) {
		return true
	}
	if f.InvoiceID.IsZero() && !x.Payable.InvoiceID.IsZero() {
		if inv, ok := r.st.invoice.invoices.get(x.Payable.InvoiceID); ok && contains(&inv.Number) {
			return true
		}
	}
	if f.CustomerID.IsZero() {
		if c, ok := r.st.customer.customers.get(x.CustomerID); ok && !c.Deleted() &&
			(contains(c.Name) || contains(c.Firstname) || contains(c.Lastname) || contains(&c.ExternalID) || contains(c.Email)) {
			return true
		}
	}
	return false
}

// paysInvoice reports whether p pays inv, directly or through a payment
// request including it.
func paysInvoice(d *paymentData, p *payment.Payment, inv core.InvoiceID) bool {
	if p.Payable.InvoiceID == inv {
		return true
	}
	if p.Payable.PaymentRequestID.IsZero() {
		return false
	}
	req, ok := d.requests.get(p.Payable.PaymentRequestID)
	return ok && slices.Contains(req.InvoiceIDs, inv)
}

// paymentReceiptRepo implements payment.ReceiptRepository.
type paymentReceiptRepo struct {
	d  *paymentData
	st *state
}

var _ payment.ReceiptRepository = paymentReceiptRepo{}

func (r paymentReceiptRepo) Create(_ context.Context, x *payment.Receipt) error {
	if r.d.receipts.has(x.ID) {
		return conflict("payment_receipt", "id")
	}
	if err := r.check(x); err != nil {
		return err
	}
	r.d.receipts.put(x.ID, *x)
	return nil
}

// check enforces the uniqueness of the payment and of the number of x
// among the other receipts of its organization.
func (r paymentReceiptRepo) check(x *payment.Receipt) error {
	if r.d.receipts.any(func(y *payment.Receipt) bool {
		return y.ID != x.ID && y.OrganizationID == x.OrganizationID && y.PaymentID == x.PaymentID
	}) {
		return conflict("payment_receipt", "payment_id")
	}
	if r.d.receipts.any(func(y *payment.Receipt) bool {
		return y.ID != x.ID && y.OrganizationID == x.OrganizationID && y.Number == x.Number
	}) {
		return conflict("payment_receipt", "number")
	}
	return nil
}

func (r paymentReceiptRepo) Update(_ context.Context, x *payment.Receipt) error {
	cur, ok := r.d.receipts.get(x.ID)
	if !ok || cur.OrganizationID != x.OrganizationID {
		return notFound("payment_receipt", x.ID)
	}
	if err := r.check(x); err != nil {
		return err
	}
	r.d.receipts.put(x.ID, *x)
	return nil
}

func (r paymentReceiptRepo) Get(_ context.Context, org core.OrganizationID, id core.PaymentReceiptID) (*payment.Receipt, error) {
	x, ok := r.d.receipts.get(id)
	if !ok || x.OrganizationID != org {
		return nil, notFound("payment_receipt", id)
	}
	return x, nil
}

func (r paymentReceiptRepo) GetByPayment(_ context.Context, org core.OrganizationID, p core.PaymentID) (*payment.Receipt, error) {
	x, ok := r.d.receipts.first(func(y *payment.Receipt) bool { return y.OrganizationID == org && y.PaymentID == p })
	if !ok {
		return nil, notFound("payment_receipt", p)
	}
	return x, nil
}

func (r paymentReceiptRepo) List(_ context.Context, org core.OrganizationID, f payment.ReceiptFilter, page core.PageRequest) (core.Page[*payment.Receipt], error) {
	if !f.Order.Valid() {
		return core.Page[*payment.Receipt]{}, fmt.Errorf("list payment receipts: %w: order", core.ErrInvalidValue)
	}
	rows := r.d.receipts.find(func(y *payment.Receipt) bool {
		if y.OrganizationID != org {
			return false
		}
		if f.InvoiceID.IsZero() {
			return true
		}
		p, ok := r.d.payments.get(y.PaymentID)
		return ok && paysInvoice(r.d, p, f.InvoiceID)
	})
	if f.Order == core.OrderRecent {
		return paginateRecent(rows, func(y *payment.Receipt) (time.Time, uuid.UUID) { return y.CreatedAt, y.ID.UUID() }, page)
	}
	return paginate(rows, func(y *payment.Receipt) core.PaymentReceiptID { return y.ID }, page)
}

// refundRepo implements payment.RefundRepository.
type refundRepo struct{ d *paymentData }

var _ payment.RefundRepository = refundRepo{}

func (r refundRepo) Create(_ context.Context, x *payment.Refund) error {
	if r.d.refunds.has(x.ID) {
		return conflict("refund", "id")
	}
	r.d.refunds.put(x.ID, *x)
	return nil
}

func (r refundRepo) Update(_ context.Context, x *payment.Refund) error {
	cur, ok := r.d.refunds.get(x.ID)
	if !ok || cur.OrganizationID != x.OrganizationID {
		return notFound("refund", x.ID)
	}
	r.d.refunds.put(x.ID, *x)
	return nil
}

func (r refundRepo) Get(_ context.Context, org core.OrganizationID, id core.RefundID) (*payment.Refund, error) {
	x, ok := r.d.refunds.get(id)
	if !ok || x.OrganizationID != org {
		return nil, notFound("refund", id)
	}
	return x, nil
}

func (r refundRepo) GetByProviderRefundID(_ context.Context, org core.OrganizationID, integration core.IntegrationID, providerRefundID string) (*payment.Refund, error) {
	x, ok := r.d.refunds.first(func(y *payment.Refund) bool {
		return y.OrganizationID == org && y.IntegrationID == integration &&
			y.ProviderRefundID != nil && *y.ProviderRefundID == providerRefundID
	})
	if !ok {
		return nil, notFound("refund", stringer(providerRefundID))
	}
	return x, nil
}

func (r refundRepo) ListByCreditNote(_ context.Context, org core.OrganizationID, creditNote core.CreditNoteID) ([]*payment.Refund, error) {
	return r.d.refunds.find(func(y *payment.Refund) bool {
		return y.OrganizationID == org && y.CreditNoteID == creditNote
	}), nil
}

func (r refundRepo) ListByPayment(_ context.Context, org core.OrganizationID, p core.PaymentID) ([]*payment.Refund, error) {
	return r.d.refunds.find(func(y *payment.Refund) bool {
		return y.OrganizationID == org && y.PaymentID == p
	}), nil
}

// paymentRequestRepo implements payment.RequestRepository.
type paymentRequestRepo struct {
	d  *paymentData
	st *state // the whole transaction state, for the list filters
}

var _ payment.RequestRepository = paymentRequestRepo{}

func (r paymentRequestRepo) Create(_ context.Context, x *payment.Request) error {
	if r.d.requests.has(x.ID) {
		return conflict("payment_request", "id")
	}
	r.d.requests.put(x.ID, *x)
	return nil
}

func (r paymentRequestRepo) Update(_ context.Context, x *payment.Request) error {
	cur, ok := r.d.requests.get(x.ID)
	if !ok || cur.OrganizationID != x.OrganizationID {
		return notFound("payment_request", x.ID)
	}
	r.d.requests.put(x.ID, *x)
	return nil
}

func (r paymentRequestRepo) Get(_ context.Context, org core.OrganizationID, id core.PaymentRequestID) (*payment.Request, error) {
	x, ok := r.d.requests.get(id)
	if !ok || x.OrganizationID != org {
		return nil, notFound("payment_request", id)
	}
	return x, nil
}

func (r paymentRequestRepo) List(_ context.Context, org core.OrganizationID, f payment.RequestFilter, page core.PageRequest) (core.Page[*payment.Request], error) {
	if !f.Order.Valid() {
		return core.Page[*payment.Request]{}, fmt.Errorf("list payment requests: %w: order", core.ErrInvalidValue)
	}
	rows := r.d.requests.find(func(y *payment.Request) bool {
		return y.OrganizationID == org &&
			(f.CustomerID.IsZero() || y.CustomerID == f.CustomerID) &&
			(f.PaymentStatus == "" || y.PaymentStatus == f.PaymentStatus) &&
			(f.Currency.IsZero() || y.Currency.Code() == f.Currency.Code()) &&
			(len(f.BillingEntityIDs) == 0 || slices.ContainsFunc(y.InvoiceIDs, func(id core.InvoiceID) bool {
				inv, ok := r.st.invoice.invoices.get(id)
				return ok && slices.Contains(f.BillingEntityIDs, inv.BillingEntityID)
			}))
	})
	if f.Order == core.OrderRecent {
		return paginateRecent(rows, func(y *payment.Request) (time.Time, uuid.UUID) { return y.CreatedAt, y.ID.UUID() }, page)
	}
	return paginate(rows, func(y *payment.Request) core.PaymentRequestID { return y.ID }, page)
}

func (r paymentRequestRepo) ListByInvoice(_ context.Context, org core.OrganizationID, invoice core.InvoiceID) ([]*payment.Request, error) {
	return r.d.requests.find(func(y *payment.Request) bool {
		return y.OrganizationID == org && slices.Contains(y.InvoiceIDs, invoice)
	}), nil
}

// paymentMethodRepo implements payment.MethodRepository.
type paymentMethodRepo struct{ d *paymentData }

var _ payment.MethodRepository = paymentMethodRepo{}

func (r paymentMethodRepo) check(m *payment.Method) error {
	if !m.IsDefault || m.Deleted() {
		return nil
	}
	if r.d.methods.any(func(x *payment.Method) bool {
		return x.ID != m.ID && x.OrganizationID == m.OrganizationID && x.CustomerID == m.CustomerID && x.IsDefault && !x.Deleted()
	}) {
		return conflict("payment_method", "is_default")
	}
	return nil
}

func (r paymentMethodRepo) Create(_ context.Context, m *payment.Method) error {
	if r.d.methods.has(m.ID) {
		return conflict("payment_method", "id")
	}
	if err := r.check(m); err != nil {
		return err
	}
	r.d.methods.put(m.ID, *m)
	return nil
}

func (r paymentMethodRepo) Update(_ context.Context, m *payment.Method) error {
	cur, ok := r.d.methods.get(m.ID)
	if !ok || cur.OrganizationID != m.OrganizationID {
		return notFound("payment_method", m.ID)
	}
	if err := r.check(m); err != nil {
		return err
	}
	r.d.methods.put(m.ID, *m)
	return nil
}

func (r paymentMethodRepo) Get(_ context.Context, org core.OrganizationID, id core.PaymentMethodID) (*payment.Method, error) {
	m, ok := r.d.methods.get(id)
	if !ok || m.OrganizationID != org {
		return nil, notFound("payment_method", id)
	}
	return m, nil
}

func (r paymentMethodRepo) GetDefault(_ context.Context, org core.OrganizationID, customer core.CustomerID) (*payment.Method, error) {
	m, ok := r.d.methods.first(func(x *payment.Method) bool {
		return x.OrganizationID == org && x.CustomerID == customer && x.IsDefault && !x.Deleted()
	})
	if !ok {
		return nil, notFound("payment_method", stringer("default of "+customer.String()))
	}
	return m, nil
}

func (r paymentMethodRepo) GetByProviderMethodID(_ context.Context, org core.OrganizationID, integration core.IntegrationID, providerMethodID string) (*payment.Method, error) {
	m, ok := r.d.methods.first(func(x *payment.Method) bool {
		return x.OrganizationID == org && x.IntegrationID == integration && x.ProviderMethodID == providerMethodID && !x.Deleted()
	})
	if !ok {
		return nil, notFound("payment_method", stringer(providerMethodID))
	}
	return m, nil
}

func (r paymentMethodRepo) ListByCustomer(_ context.Context, org core.OrganizationID, customer core.CustomerID) ([]*payment.Method, error) {
	return r.d.methods.find(func(x *payment.Method) bool {
		return x.OrganizationID == org && x.CustomerID == customer && !x.Deleted()
	}), nil
}

// providerCustomerRepo implements payment.ProviderCustomerRepository.
type providerCustomerRepo struct{ d *paymentData }

var _ payment.ProviderCustomerRepository = providerCustomerRepo{}

func (r providerCustomerRepo) check(c *payment.ProviderCustomer) error {
	if c.Deleted() {
		return nil
	}
	if r.d.providerCustomers.any(func(x *payment.ProviderCustomer) bool {
		return x.ID != c.ID && x.OrganizationID == c.OrganizationID && x.CustomerID == c.CustomerID &&
			x.IntegrationID == c.IntegrationID && !x.Deleted()
	}) {
		return conflict("payment_provider_customer", "integration_id")
	}
	return nil
}

func (r providerCustomerRepo) Create(_ context.Context, c *payment.ProviderCustomer) error {
	if r.d.providerCustomers.has(c.ID) {
		return conflict("payment_provider_customer", "id")
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.providerCustomers.put(c.ID, *c)
	return nil
}

func (r providerCustomerRepo) Update(_ context.Context, c *payment.ProviderCustomer) error {
	cur, ok := r.d.providerCustomers.get(c.ID)
	if !ok || cur.OrganizationID != c.OrganizationID {
		return notFound("payment_provider_customer", c.ID)
	}
	if err := r.check(c); err != nil {
		return err
	}
	r.d.providerCustomers.put(c.ID, *c)
	return nil
}

func (r providerCustomerRepo) Get(_ context.Context, org core.OrganizationID, id core.PaymentProviderCustomerID) (*payment.ProviderCustomer, error) {
	c, ok := r.d.providerCustomers.get(id)
	if !ok || c.OrganizationID != org {
		return nil, notFound("payment_provider_customer", id)
	}
	return c, nil
}

func (r providerCustomerRepo) GetByCustomer(_ context.Context, org core.OrganizationID, customer core.CustomerID, integration core.IntegrationID) (*payment.ProviderCustomer, error) {
	c, ok := r.d.providerCustomers.first(func(x *payment.ProviderCustomer) bool {
		return x.OrganizationID == org && x.CustomerID == customer && x.IntegrationID == integration && !x.Deleted()
	})
	if !ok {
		return nil, notFound("payment_provider_customer", customer)
	}
	return c, nil
}

func (r providerCustomerRepo) GetByProviderCustomerID(_ context.Context, org core.OrganizationID, integration core.IntegrationID, providerCustomerID string) (*payment.ProviderCustomer, error) {
	c, ok := r.d.providerCustomers.first(func(x *payment.ProviderCustomer) bool {
		return x.OrganizationID == org && x.IntegrationID == integration && !x.Deleted() &&
			x.ProviderCustomerID != nil && *x.ProviderCustomerID == providerCustomerID
	})
	if !ok {
		return nil, notFound("payment_provider_customer", stringer(providerCustomerID))
	}
	return c, nil
}

func (r providerCustomerRepo) ListByCustomer(_ context.Context, org core.OrganizationID, customer core.CustomerID) ([]*payment.ProviderCustomer, error) {
	return r.d.providerCustomers.find(func(x *payment.ProviderCustomer) bool {
		return x.OrganizationID == org && x.CustomerID == customer && !x.Deleted()
	}), nil
}

func (r providerCustomerRepo) ListByIntegration(_ context.Context, org core.OrganizationID, integration core.IntegrationID) ([]*payment.ProviderCustomer, error) {
	return r.d.providerCustomers.find(func(x *payment.ProviderCustomer) bool {
		return x.OrganizationID == org && x.IntegrationID == integration && !x.Deleted()
	}), nil
}

// paymentIntentRepo implements payment.IntentRepository.
type paymentIntentRepo struct{ d *paymentData }

var _ payment.IntentRepository = paymentIntentRepo{}

func (r paymentIntentRepo) check(i *payment.Intent) error {
	if i.Status != payment.IntentStatusActive {
		return nil
	}
	if r.d.intents.any(func(x *payment.Intent) bool {
		return x.ID != i.ID && x.OrganizationID == i.OrganizationID && x.InvoiceID == i.InvoiceID && x.Status == payment.IntentStatusActive
	}) {
		return conflict("payment_intent", "active intent of "+i.InvoiceID.String())
	}
	return nil
}

func (r paymentIntentRepo) Create(_ context.Context, i *payment.Intent) error {
	if r.d.intents.has(i.ID) {
		return conflict("payment_intent", "id")
	}
	if err := r.check(i); err != nil {
		return err
	}
	r.d.intents.put(i.ID, *i)
	return nil
}

func (r paymentIntentRepo) Update(_ context.Context, i *payment.Intent) error {
	cur, ok := r.d.intents.get(i.ID)
	if !ok || cur.OrganizationID != i.OrganizationID {
		return notFound("payment_intent", i.ID)
	}
	if err := r.check(i); err != nil {
		return err
	}
	r.d.intents.put(i.ID, *i)
	return nil
}

func (r paymentIntentRepo) GetActiveByInvoice(_ context.Context, org core.OrganizationID, invoice core.InvoiceID) (*payment.Intent, error) {
	i, ok := r.d.intents.first(func(x *payment.Intent) bool {
		return x.OrganizationID == org && x.InvoiceID == invoice && x.Status == payment.IntentStatusActive
	})
	if !ok {
		return nil, notFound("payment_intent", invoice)
	}
	return i, nil
}

func (r paymentIntentRepo) ListAwaitingExpiration(_ context.Context, org core.OrganizationID, now time.Time) ([]*payment.Intent, error) {
	return r.d.intents.find(func(x *payment.Intent) bool {
		return x.OrganizationID == org && x.AwaitingExpiration(now)
	}), nil
}

func (r paymentIntentRepo) ListActive(_ context.Context, org core.OrganizationID) ([]*payment.Intent, error) {
	return r.d.intents.find(func(x *payment.Intent) bool {
		return x.OrganizationID == org && x.Status == payment.IntentStatusActive
	}), nil
}

func (r paymentIntentRepo) Exists(_ context.Context, org core.OrganizationID) (bool, error) {
	return r.d.intents.any(func(x *payment.Intent) bool { return x.OrganizationID == org }), nil
}

// inboundWebhookRepo implements payment.InboundWebhookRepository.
type inboundWebhookRepo struct{ d *paymentData }

var _ payment.InboundWebhookRepository = inboundWebhookRepo{}

func (r inboundWebhookRepo) Create(_ context.Context, w *payment.InboundWebhook) error {
	if r.d.inboundWebhooks.has(w.ID) {
		return conflict("inbound_webhook", "id")
	}
	r.d.inboundWebhooks.put(w.ID, *w)
	return nil
}

func (r inboundWebhookRepo) Update(_ context.Context, w *payment.InboundWebhook) error {
	cur, ok := r.d.inboundWebhooks.get(w.ID)
	if !ok || cur.OrganizationID != w.OrganizationID {
		return notFound("inbound_webhook", w.ID)
	}
	r.d.inboundWebhooks.put(w.ID, *w)
	return nil
}

func (r inboundWebhookRepo) Get(_ context.Context, org core.OrganizationID, id core.InboundWebhookID) (*payment.InboundWebhook, error) {
	w, ok := r.d.inboundWebhooks.get(id)
	if !ok || w.OrganizationID != org {
		return nil, notFound("inbound_webhook", id)
	}
	return w, nil
}

func (r inboundWebhookRepo) ListRetriable(_ context.Context, org core.OrganizationID, now time.Time) ([]*payment.InboundWebhook, error) {
	return r.d.inboundWebhooks.find(func(x *payment.InboundWebhook) bool {
		return x.OrganizationID == org && x.Retriable(now)
	}), nil
}

func (r inboundWebhookRepo) DeleteExpired(_ context.Context, org core.OrganizationID, now time.Time) (int, error) {
	return r.d.inboundWebhooks.deleteWhere(func(x *payment.InboundWebhook) bool {
		return x.OrganizationID == org && x.Expired(now)
	}), nil
}
