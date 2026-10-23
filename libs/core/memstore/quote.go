package memstore

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
)

// quoteData holds the tables of package quote.
type quoteData struct {
	quotes     *table[core.QuoteID, quote.Quote]
	versions   *table[core.QuoteVersionID, quote.Version]
	orderForms *table[core.OrderFormID, quote.OrderForm]
	orders     *table[core.OrderID, quote.Order]
}

func newQuoteData() *quoteData {
	return &quoteData{
		quotes: newTable[core.QuoteID](func(q quote.Quote) quote.Quote {
			q.Owners = slices.Clone(q.Owners)
			return q
		}),
		versions: newTable[core.QuoteVersionID](func(v quote.Version) quote.Version {
			v.Content = slices.Clone(v.Content)
			return v
		}),
		orderForms: newTable[core.OrderFormID, quote.OrderForm](nil),
		orders: newTable[core.OrderID](func(o quote.Order) quote.Order {
			o.ExecutionRecord = slices.Clone(o.ExecutionRecord)
			return o
		}),
	}
}

func (d *quoteData) snapshot() *quoteData {
	return &quoteData{
		quotes:     d.quotes.snapshot(),
		versions:   d.versions.snapshot(),
		orderForms: d.orderForms.snapshot(),
		orders:     d.orders.snapshot(),
	}
}

// quoteNextSequence returns 1 + the highest sequential id among rows.
func quoteNextSequence[V any](rows []*V, seq func(*V) int) int {
	n := 0
	for _, r := range rows {
		n = max(n, seq(r))
	}
	return n + 1
}

// quoteRepo implements quote.Repository.
type quoteRepo struct{ d *quoteData }

func (r quoteRepo) check(q *quote.Quote) error {
	if r.d.quotes.any(func(x *quote.Quote) bool {
		return x.ID != q.ID && x.OrganizationID == q.OrganizationID && x.Number == q.Number
	}) {
		return conflict("quote", "number")
	}
	return nil
}

func (r quoteRepo) Create(_ context.Context, q *quote.Quote) error {
	if r.d.quotes.has(q.ID) {
		return conflict("quote", "id")
	}
	if err := r.check(q); err != nil {
		return err
	}
	r.d.quotes.put(q.ID, *q)
	return nil
}

func (r quoteRepo) Update(_ context.Context, q *quote.Quote) error {
	cur, ok := r.d.quotes.get(q.ID)
	if !ok || cur.OrganizationID != q.OrganizationID {
		return notFound("quote", q.ID)
	}
	if err := r.check(q); err != nil {
		return err
	}
	r.d.quotes.put(q.ID, *q)
	return nil
}

func (r quoteRepo) Get(_ context.Context, org core.OrganizationID, id core.QuoteID) (*quote.Quote, error) {
	q, ok := r.d.quotes.get(id)
	if !ok || q.OrganizationID != org {
		return nil, notFound("quote", id)
	}
	return q, nil
}

func (r quoteRepo) GetByNumber(_ context.Context, org core.OrganizationID, number string) (*quote.Quote, error) {
	q, ok := r.d.quotes.first(func(x *quote.Quote) bool { return x.OrganizationID == org && x.Number == number })
	if !ok {
		return nil, notFound("quote", stringer(number))
	}
	return q, nil
}

func (r quoteRepo) NextSequence(_ context.Context, org core.OrganizationID, year int) (int, error) {
	rows := r.d.quotes.find(func(x *quote.Quote) bool { return x.OrganizationID == org && x.CreatedAt.UTC().Year() == year })
	return quoteNextSequence(rows, func(x *quote.Quote) int { return x.SequentialID }), nil
}

func (r quoteRepo) List(_ context.Context, org core.OrganizationID, f quote.Filter, page core.PageRequest) (core.Page[*quote.Quote], error) {
	rows := r.d.quotes.find(func(x *quote.Quote) bool {
		return x.OrganizationID == org &&
			(f.CustomerID.IsZero() || x.CustomerID == f.CustomerID) &&
			(len(f.OrderTypes) == 0 || slices.Contains(f.OrderTypes, x.OrderType)) &&
			(f.OwnerID.IsZero() || slices.Contains(x.Owners, f.OwnerID))
	})
	return paginate(rows, func(x *quote.Quote) core.QuoteID { return x.ID }, page)
}

// quoteVersionRepo implements quote.VersionRepository.
type quoteVersionRepo struct{ d *quoteData }

func (r quoteVersionRepo) check(v *quote.Version) error {
	siblings := r.d.versions.find(func(x *quote.Version) bool { return x.QuoteID == v.QuoteID })
	if slices.ContainsFunc(siblings, func(x *quote.Version) bool { return x.ID != v.ID && x.Version == v.Version }) {
		return conflict("quote_version", "version")
	}
	if err := quote.CheckSingleActive(v, siblings); err != nil {
		return conflict("quote_version", err.Error())
	}
	return nil
}

func (r quoteVersionRepo) Create(_ context.Context, v *quote.Version) error {
	if r.d.versions.has(v.ID) {
		return conflict("quote_version", "id")
	}
	if err := r.check(v); err != nil {
		return err
	}
	r.d.versions.put(v.ID, *v)
	return nil
}

func (r quoteVersionRepo) Update(_ context.Context, v *quote.Version) error {
	cur, ok := r.d.versions.get(v.ID)
	if !ok || cur.OrganizationID != v.OrganizationID {
		return notFound("quote_version", v.ID)
	}
	if err := r.check(v); err != nil {
		return err
	}
	r.d.versions.put(v.ID, *v)
	return nil
}

func (r quoteVersionRepo) Get(_ context.Context, org core.OrganizationID, id core.QuoteVersionID) (*quote.Version, error) {
	v, ok := r.d.versions.get(id)
	if !ok || v.OrganizationID != org {
		return nil, notFound("quote_version", id)
	}
	return v, nil
}

func (r quoteVersionRepo) ListByQuote(_ context.Context, org core.OrganizationID, q core.QuoteID) ([]*quote.Version, error) {
	rows := r.d.versions.find(func(x *quote.Version) bool { return x.OrganizationID == org && x.QuoteID == q })
	slices.SortStableFunc(rows, func(a, b *quote.Version) int { return cmp.Compare(b.Version, a.Version) })
	return rows, nil
}

func (r quoteVersionRepo) GetCurrent(ctx context.Context, org core.OrganizationID, q core.QuoteID) (*quote.Version, error) {
	rows, _ := r.ListByQuote(ctx, org, q)
	if len(rows) == 0 {
		return nil, notFound("quote_version", q)
	}
	return rows[0], nil
}

// orderFormRepo implements quote.OrderFormRepository.
type orderFormRepo struct{ d *quoteData }

func (r orderFormRepo) check(f *quote.OrderForm) error {
	if r.d.orderForms.any(func(x *quote.OrderForm) bool {
		return x.ID != f.ID && x.OrganizationID == f.OrganizationID && x.Number == f.Number
	}) {
		return conflict("order_form", "number")
	}
	if r.d.orderForms.any(func(x *quote.OrderForm) bool { return x.ID != f.ID && x.QuoteVersionID == f.QuoteVersionID }) {
		return conflict("order_form", "quote_version_id")
	}
	return nil
}

func (r orderFormRepo) Create(_ context.Context, f *quote.OrderForm) error {
	if r.d.orderForms.has(f.ID) {
		return conflict("order_form", "id")
	}
	if err := r.check(f); err != nil {
		return err
	}
	r.d.orderForms.put(f.ID, *f)
	return nil
}

func (r orderFormRepo) Update(_ context.Context, f *quote.OrderForm) error {
	cur, ok := r.d.orderForms.get(f.ID)
	if !ok || cur.OrganizationID != f.OrganizationID {
		return notFound("order_form", f.ID)
	}
	if err := r.check(f); err != nil {
		return err
	}
	r.d.orderForms.put(f.ID, *f)
	return nil
}

func (r orderFormRepo) Get(_ context.Context, org core.OrganizationID, id core.OrderFormID) (*quote.OrderForm, error) {
	f, ok := r.d.orderForms.get(id)
	if !ok || f.OrganizationID != org {
		return nil, notFound("order_form", id)
	}
	return f, nil
}

func (r orderFormRepo) GetByNumber(_ context.Context, org core.OrganizationID, number string) (*quote.OrderForm, error) {
	f, ok := r.d.orderForms.first(func(x *quote.OrderForm) bool { return x.OrganizationID == org && x.Number == number })
	if !ok {
		return nil, notFound("order_form", stringer(number))
	}
	return f, nil
}

func (r orderFormRepo) GetByVersion(_ context.Context, org core.OrganizationID, v core.QuoteVersionID) (*quote.OrderForm, error) {
	f, ok := r.d.orderForms.first(func(x *quote.OrderForm) bool { return x.OrganizationID == org && x.QuoteVersionID == v })
	if !ok {
		return nil, notFound("order_form", v)
	}
	return f, nil
}

func (r orderFormRepo) NextSequence(_ context.Context, org core.OrganizationID, year int) (int, error) {
	rows := r.d.orderForms.find(func(x *quote.OrderForm) bool { return x.OrganizationID == org && x.CreatedAt.UTC().Year() == year })
	return quoteNextSequence(rows, func(x *quote.OrderForm) int { return x.SequentialID }), nil
}

func (r orderFormRepo) List(_ context.Context, org core.OrganizationID, f quote.OrderFormFilter, page core.PageRequest) (core.Page[*quote.OrderForm], error) {
	rows := r.d.orderForms.find(func(x *quote.OrderForm) bool {
		return x.OrganizationID == org &&
			(f.CustomerID.IsZero() || x.CustomerID == f.CustomerID) &&
			(len(f.Statuses) == 0 || slices.Contains(f.Statuses, x.Status))
	})
	return paginate(rows, func(x *quote.OrderForm) core.OrderFormID { return x.ID }, page)
}

func (r orderFormRepo) ListExpiring(_ context.Context, before time.Time, limit int) ([]*quote.OrderForm, error) {
	rows := r.d.orderForms.find(func(x *quote.OrderForm) bool {
		return x.Status == quote.OrderFormStatusGenerated && x.ExpiresAt != nil && x.ExpiresAt.Before(before)
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// orderRepo implements quote.OrderRepository.
type orderRepo struct{ d *quoteData }

func (r orderRepo) check(o *quote.Order) error {
	if r.d.orders.any(func(x *quote.Order) bool {
		return x.ID != o.ID && x.OrganizationID == o.OrganizationID && x.Number == o.Number
	}) {
		return conflict("order", "number")
	}
	if r.d.orders.any(func(x *quote.Order) bool { return x.ID != o.ID && x.OrderFormID == o.OrderFormID }) {
		return conflict("order", "order_form_id")
	}
	return nil
}

func (r orderRepo) Create(_ context.Context, o *quote.Order) error {
	if r.d.orders.has(o.ID) {
		return conflict("order", "id")
	}
	if err := r.check(o); err != nil {
		return err
	}
	r.d.orders.put(o.ID, *o)
	return nil
}

func (r orderRepo) Update(_ context.Context, o *quote.Order) error {
	cur, ok := r.d.orders.get(o.ID)
	if !ok || cur.OrganizationID != o.OrganizationID {
		return notFound("order", o.ID)
	}
	if err := r.check(o); err != nil {
		return err
	}
	r.d.orders.put(o.ID, *o)
	return nil
}

func (r orderRepo) Get(_ context.Context, org core.OrganizationID, id core.OrderID) (*quote.Order, error) {
	o, ok := r.d.orders.get(id)
	if !ok || o.OrganizationID != org {
		return nil, notFound("order", id)
	}
	return o, nil
}

func (r orderRepo) GetByNumber(_ context.Context, org core.OrganizationID, number string) (*quote.Order, error) {
	o, ok := r.d.orders.first(func(x *quote.Order) bool { return x.OrganizationID == org && x.Number == number })
	if !ok {
		return nil, notFound("order", stringer(number))
	}
	return o, nil
}

func (r orderRepo) NextSequence(_ context.Context, org core.OrganizationID, year int) (int, error) {
	rows := r.d.orders.find(func(x *quote.Order) bool { return x.OrganizationID == org && x.CreatedAt.UTC().Year() == year })
	return quoteNextSequence(rows, func(x *quote.Order) int { return x.SequentialID }), nil
}

func (r orderRepo) List(_ context.Context, org core.OrganizationID, f quote.OrderFilter, page core.PageRequest) (core.Page[*quote.Order], error) {
	rows := r.d.orders.find(func(x *quote.Order) bool {
		return x.OrganizationID == org &&
			(f.CustomerID.IsZero() || x.CustomerID == f.CustomerID) &&
			(len(f.Statuses) == 0 || slices.Contains(f.Statuses, x.Status))
	})
	return paginate(rows, func(x *quote.Order) core.OrderID { return x.ID }, page)
}

func (r orderRepo) ListExecutable(_ context.Context, now time.Time, limit int) ([]*quote.Order, error) {
	rows := r.d.orders.find(func(x *quote.Order) bool { return x.Executable(now) })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

var (
	_ quote.Repository          = quoteRepo{}
	_ quote.VersionRepository   = quoteVersionRepo{}
	_ quote.OrderFormRepository = orderFormRepo{}
	_ quote.OrderRepository     = orderRepo{}
)
