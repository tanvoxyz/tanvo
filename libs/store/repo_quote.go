package store

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/port"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/store/internal/db"
)

// bindQuote binds the repositories of the quote aggregates.
func (t *txState) bindQuote(r *port.Repos) {
	r.Quotes = quoteRepo{t}
	r.QuoteVersions = quoteVersionRepo{t}
	r.OrderForms = orderFormRepo{t}
	r.Orders = orderRepo{t}
}

// quoteYear returns the bounds [start, end) of a UTC calendar year, the
// scope of the sequential ids.
func quoteYear(year int) (start, end time.Time) {
	start = time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(1, 0, 0)
}

// quoteLimit converts the limit of a scan; zero or less means no limit.
func quoteLimit(limit int) int32 {
	if limit <= 0 || limit > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(limit)
}

// quoteJSON returns b for a json column, nil (NULL) when empty.
func quoteJSON(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}

// quoteRepo implements quote.Repository.
type quoteRepo struct{ t *txState }

var _ quote.Repository = quoteRepo{}

func quoteParams(x *quote.Quote) db.CreateQuoteParams {
	return db.CreateQuoteParams{
		OrganizationID: x.OrganizationID.UUID(),
		ID:             x.ID.UUID(),
		Number:         x.Number,
		SequentialID:   int32(x.SequentialID), //nolint:gosec // sequences are small
		CustomerID:     x.CustomerID.UUID(),
		OrderType:      string(x.OrderType),
		SubscriptionID: optUUID(x.SubscriptionID),
		CreatedAt:      utc(x.CreatedAt),
		UpdatedAt:      utc(x.UpdatedAt),
	}
}

// quoteRow is the common shape of the quote reads.
type quoteRow struct {
	Quote    db.Quote
	OwnerIds []uuid.UUID
}

func quoteOf(row quoteRow) (*quote.Quote, error) {
	r := row.Quote
	return &quote.Quote{
		ID:             idOf[core.QuoteID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Number:         r.Number,
		SequentialID:   int(r.SequentialID),
		CustomerID:     idOf[core.CustomerID](r.CustomerID),
		OrderType:      quote.OrderType(r.OrderType),
		Owners:         idsOf[core.MembershipID](row.OwnerIds),
		SubscriptionID: optIDOf[core.SubscriptionID](r.SubscriptionID),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

// writeOwners replaces the owners of x.
func (r quoteRepo) writeOwners(ctx context.Context, q *db.Queries, x *quote.Quote, replace bool) error {
	org, id := x.OrganizationID.UUID(), x.ID.UUID()
	if replace {
		if err := q.DeleteQuoteOwners(ctx, db.DeleteQuoteOwnersParams{OrganizationID: org, QuoteID: id}); err != nil {
			return err
		}
	}
	if len(x.Owners) == 0 {
		return nil
	}
	return q.InsertQuoteOwners(ctx, db.InsertQuoteOwnersParams{OrganizationID: org, QuoteID: id, MembershipIds: uuids(x.Owners)})
}

func (r quoteRepo) Create(ctx context.Context, x *quote.Quote) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateQuote(ctx, quoteParams(x))
	if err := insertErr("quote", n, err); err != nil {
		return err
	}
	if err := r.writeOwners(ctx, q, x, false); err != nil {
		return fmt.Errorf("create quote %s: %w", x.ID, mapErr(err))
	}
	return nil
}

func (r quoteRepo) Update(ctx context.Context, x *quote.Quote) error {
	q, err := r.t.queries(ctx, x.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() error {
		var err error
		n, err = q.UpdateQuote(ctx, db.UpdateQuoteParams(quoteParams(x)))
		if err != nil || n == 0 {
			return err
		}
		return r.writeOwners(ctx, q, x, true)
	})
	return updateErr("quote", x.ID, n, err)
}

func (r quoteRepo) Get(ctx context.Context, org core.OrganizationID, id core.QuoteID) (*quote.Quote, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetQuote(ctx, db.GetQuoteParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("quote", id, err)
	}
	return quoteOf(quoteRow(row))
}

func (r quoteRepo) GetByNumber(ctx context.Context, org core.OrganizationID, number string) (*quote.Quote, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetQuoteByNumber(ctx, db.GetQuoteByNumberParams{OrganizationID: org.UUID(), Number: number})
	if err != nil {
		return nil, getErr("quote", number, err)
	}
	return quoteOf(quoteRow(row))
}

func (r quoteRepo) NextSequence(ctx context.Context, org core.OrganizationID, year int) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	start, end := quoteYear(year)
	n, err := q.NextQuoteSequence(ctx, db.NextQuoteSequenceParams{OrganizationID: org.UUID(), YearStart: start, YearEnd: end})
	if err != nil {
		return 0, fmt.Errorf("next quote sequence: %w", mapErr(err))
	}
	return int(n), nil
}

func (r quoteRepo) List(ctx context.Context, org core.OrganizationID, f quote.Filter, page core.PageRequest) (core.Page[*quote.Quote], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*quote.Quote]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*quote.Quote]{}, err
	}
	rows, err := q.ListQuotes(ctx, db.ListQuotesParams{
		OrganizationID: org.UUID(),
		CustomerID:     optUUID(f.CustomerID),
		OrderTypes:     enumStrings(f.OrderTypes),
		OwnerID:        optUUID(f.OwnerID),
		After:          after,
		Lim:            lim,
	})
	if err != nil {
		return core.Page[*quote.Quote]{}, fmt.Errorf("list quotes: %w", mapErr(err))
	}
	return pageOf(rows, page,
		func(r db.ListQuotesRow) (*quote.Quote, error) { return quoteOf(quoteRow(r)) },
		func(x *quote.Quote) string { return x.ID.String() })
}

// quoteVersionRepo implements quote.VersionRepository.
type quoteVersionRepo struct{ t *txState }

var _ quote.VersionRepository = quoteVersionRepo{}

func quoteVersionParams(v *quote.Version) db.CreateQuoteVersionParams {
	return db.CreateQuoteVersionParams{
		OrganizationID:  v.OrganizationID.UUID(),
		ID:              v.ID.UUID(),
		QuoteID:         v.QuoteID.UUID(),
		Version:         int32(v.Version), //nolint:gosec // versions are small
		Status:          string(v.Status),
		VoidReason:      optString(string(v.VoidReason)),
		Content:         quoteJSON(v.Content),
		Currency:        currencyCode(v.Currency),
		BillingEntityID: optUUID(v.BillingEntityID),
		ApprovedAt:      optUTC(v.ApprovedAt),
		VoidedAt:        optUTC(v.VoidedAt),
		CreatedAt:       utc(v.CreatedAt),
		UpdatedAt:       utc(v.UpdatedAt),
	}
}

func quoteVersionOf(r db.QuoteVersion) (*quote.Version, error) {
	cur, err := currency(r.Currency)
	if err != nil {
		return nil, fmt.Errorf("quote version %s: %w", r.ID, err)
	}
	return &quote.Version{
		ID:              idOf[core.QuoteVersionID](r.ID),
		OrganizationID:  idOf[core.OrganizationID](r.OrganizationID),
		QuoteID:         idOf[core.QuoteID](r.QuoteID),
		Version:         int(r.Version),
		Status:          quote.VersionStatus(r.Status),
		VoidReason:      quote.VersionVoidReason(deref(r.VoidReason)),
		Content:         rawJSON(r.Content),
		Currency:        cur,
		BillingEntityID: optIDOf[core.BillingEntityID](r.BillingEntityID),
		ApprovedAt:      optUTC(r.ApprovedAt),
		VoidedAt:        optUTC(r.VoidedAt),
		CreatedAt:       utc(r.CreatedAt),
		UpdatedAt:       utc(r.UpdatedAt),
	}, nil
}

func (r quoteVersionRepo) Create(ctx context.Context, v *quote.Version) error {
	q, err := r.t.queries(ctx, v.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateQuoteVersion(ctx, quoteVersionParams(v))
	return insertErr("quote_version", n, err)
}

func (r quoteVersionRepo) Update(ctx context.Context, v *quote.Version) error {
	q, err := r.t.queries(ctx, v.OrganizationID)
	if err != nil {
		return err
	}
	// The version number and the single active version are unique: guard
	// the update so that a conflict keeps the transaction usable.
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateQuoteVersion(ctx, db.UpdateQuoteVersionParams(quoteVersionParams(v)))
		return err
	})
	return updateErr("quote_version", v.ID, n, err)
}

func (r quoteVersionRepo) Get(ctx context.Context, org core.OrganizationID, id core.QuoteVersionID) (*quote.Version, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetQuoteVersion(ctx, db.GetQuoteVersionParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("quote_version", id, err)
	}
	return quoteVersionOf(row)
}

func (r quoteVersionRepo) ListByQuote(ctx context.Context, org core.OrganizationID, id core.QuoteID) ([]*quote.Version, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListQuoteVersionsByQuote(ctx, db.ListQuoteVersionsByQuoteParams{OrganizationID: org.UUID(), QuoteID: id.UUID()})
	if err != nil {
		return nil, fmt.Errorf("list quote versions: %w", mapErr(err))
	}
	return convAll(rows, quoteVersionOf)
}

func (r quoteVersionRepo) GetCurrent(ctx context.Context, org core.OrganizationID, id core.QuoteID) (*quote.Version, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetCurrentQuoteVersion(ctx, db.GetCurrentQuoteVersionParams{OrganizationID: org.UUID(), QuoteID: id.UUID()})
	if err != nil {
		return nil, getErr("quote_version", id, err)
	}
	return quoteVersionOf(row)
}

// orderFormRepo implements quote.OrderFormRepository.
type orderFormRepo struct{ t *txState }

var _ quote.OrderFormRepository = orderFormRepo{}

func orderFormParams(f *quote.OrderForm) db.CreateOrderFormParams {
	return db.CreateOrderFormParams{
		OrganizationID: f.OrganizationID.UUID(),
		ID:             f.ID.UUID(),
		Number:         f.Number,
		SequentialID:   int32(f.SequentialID), //nolint:gosec // sequences are small
		CustomerID:     f.CustomerID.UUID(),
		QuoteVersionID: f.QuoteVersionID.UUID(),
		Status:         string(f.Status),
		VoidReason:     optString(string(f.VoidReason)),
		ExpiresAt:      optUTC(f.ExpiresAt),
		SignedAt:       optUTC(f.SignedAt),
		SignedBy:       f.SignedBy,
		SignedDocument: optString(string(f.SignedDocument)),
		VoidedAt:       optUTC(f.VoidedAt),
		CreatedAt:      utc(f.CreatedAt),
		UpdatedAt:      utc(f.UpdatedAt),
	}
}

func orderFormOf(r db.OrderForm) (*quote.OrderForm, error) {
	return &quote.OrderForm{
		ID:             idOf[core.OrderFormID](r.ID),
		OrganizationID: idOf[core.OrganizationID](r.OrganizationID),
		Number:         r.Number,
		SequentialID:   int(r.SequentialID),
		CustomerID:     idOf[core.CustomerID](r.CustomerID),
		QuoteVersionID: idOf[core.QuoteVersionID](r.QuoteVersionID),
		Status:         quote.OrderFormStatus(r.Status),
		VoidReason:     quote.OrderFormVoidReason(deref(r.VoidReason)),
		ExpiresAt:      optUTC(r.ExpiresAt),
		SignedAt:       optUTC(r.SignedAt),
		SignedBy:       r.SignedBy,
		SignedDocument: core.BlobRef(deref(r.SignedDocument)),
		VoidedAt:       optUTC(r.VoidedAt),
		CreatedAt:      utc(r.CreatedAt),
		UpdatedAt:      utc(r.UpdatedAt),
	}, nil
}

func (r orderFormRepo) Create(ctx context.Context, f *quote.OrderForm) error {
	q, err := r.t.queries(ctx, f.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateOrderForm(ctx, orderFormParams(f))
	return insertErr("order_form", n, err)
}

func (r orderFormRepo) Update(ctx context.Context, f *quote.OrderForm) error {
	q, err := r.t.queries(ctx, f.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateOrderForm(ctx, db.UpdateOrderFormParams(orderFormParams(f)))
		return err
	})
	return updateErr("order_form", f.ID, n, err)
}

func (r orderFormRepo) Get(ctx context.Context, org core.OrganizationID, id core.OrderFormID) (*quote.OrderForm, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetOrderForm(ctx, db.GetOrderFormParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("order_form", id, err)
	}
	return orderFormOf(row)
}

func (r orderFormRepo) GetByNumber(ctx context.Context, org core.OrganizationID, number string) (*quote.OrderForm, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetOrderFormByNumber(ctx, db.GetOrderFormByNumberParams{OrganizationID: org.UUID(), Number: number})
	if err != nil {
		return nil, getErr("order_form", number, err)
	}
	return orderFormOf(row)
}

func (r orderFormRepo) GetByVersion(ctx context.Context, org core.OrganizationID, v core.QuoteVersionID) (*quote.OrderForm, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetOrderFormByVersion(ctx, db.GetOrderFormByVersionParams{OrganizationID: org.UUID(), QuoteVersionID: v.UUID()})
	if err != nil {
		return nil, getErr("order_form", v, err)
	}
	return orderFormOf(row)
}

func (r orderFormRepo) NextSequence(ctx context.Context, org core.OrganizationID, year int) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	start, end := quoteYear(year)
	n, err := q.NextOrderFormSequence(ctx, db.NextOrderFormSequenceParams{OrganizationID: org.UUID(), YearStart: start, YearEnd: end})
	if err != nil {
		return 0, fmt.Errorf("next order form sequence: %w", mapErr(err))
	}
	return int(n), nil
}

func (r orderFormRepo) List(ctx context.Context, org core.OrganizationID, f quote.OrderFormFilter, page core.PageRequest) (core.Page[*quote.OrderForm], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*quote.OrderForm]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*quote.OrderForm]{}, err
	}
	rows, err := q.ListOrderForms(ctx, db.ListOrderFormsParams{
		OrganizationID: org.UUID(),
		CustomerID:     optUUID(f.CustomerID),
		Statuses:       enumStrings(f.Statuses),
		After:          after,
		Lim:            lim,
	})
	if err != nil {
		return core.Page[*quote.OrderForm]{}, fmt.Errorf("list order forms: %w", mapErr(err))
	}
	return pageOf(rows, page, orderFormOf, func(x *quote.OrderForm) string { return x.ID.String() })
}

// ListExpiring scans every organization.
func (r orderFormRepo) ListExpiring(ctx context.Context, before time.Time, limit int) ([]*quote.OrderForm, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	b := utc(before)
	rows, err := q.ListExpiringOrderForms(ctx, db.ListExpiringOrderFormsParams{Before: &b, Lim: quoteLimit(limit)})
	if err != nil {
		return nil, fmt.Errorf("list expiring order forms: %w", mapErr(err))
	}
	return convAll(rows, orderFormOf)
}

// orderRepo implements quote.OrderRepository.
type orderRepo struct{ t *txState }

var _ quote.OrderRepository = orderRepo{}

func orderParams(o *quote.Order) db.CreateOrderParams {
	return db.CreateOrderParams{
		OrganizationID:  o.OrganizationID.UUID(),
		ID:              o.ID.UUID(),
		Number:          o.Number,
		SequentialID:    int32(o.SequentialID), //nolint:gosec // sequences are small
		CustomerID:      o.CustomerID.UUID(),
		OrderFormID:     o.OrderFormID.UUID(),
		Status:          string(o.Status),
		ExecutionMode:   optString(string(o.ExecutionMode)),
		ExecuteAt:       optUTC(o.ExecuteAt),
		ExecutedAt:      optUTC(o.ExecutedAt),
		ExecutionRecord: quoteJSON(o.ExecutionRecord),
		CreatedAt:       utc(o.CreatedAt),
		UpdatedAt:       utc(o.UpdatedAt),
	}
}

func orderOf(r db.Order) (*quote.Order, error) {
	return &quote.Order{
		ID:              idOf[core.OrderID](r.ID),
		OrganizationID:  idOf[core.OrganizationID](r.OrganizationID),
		Number:          r.Number,
		SequentialID:    int(r.SequentialID),
		CustomerID:      idOf[core.CustomerID](r.CustomerID),
		OrderFormID:     idOf[core.OrderFormID](r.OrderFormID),
		Status:          quote.OrderStatus(r.Status),
		ExecutionMode:   quote.ExecutionMode(deref(r.ExecutionMode)),
		ExecuteAt:       optUTC(r.ExecuteAt),
		ExecutedAt:      optUTC(r.ExecutedAt),
		ExecutionRecord: rawJSON(r.ExecutionRecord),
		CreatedAt:       utc(r.CreatedAt),
		UpdatedAt:       utc(r.UpdatedAt),
	}, nil
}

func (r orderRepo) Create(ctx context.Context, o *quote.Order) error {
	q, err := r.t.queries(ctx, o.OrganizationID)
	if err != nil {
		return err
	}
	n, err := q.CreateOrder(ctx, orderParams(o))
	return insertErr("order", n, err)
}

func (r orderRepo) Update(ctx context.Context, o *quote.Order) error {
	q, err := r.t.queries(ctx, o.OrganizationID)
	if err != nil {
		return err
	}
	var n int64
	err = r.t.guard(ctx, func() (err error) {
		n, err = q.UpdateOrder(ctx, db.UpdateOrderParams(orderParams(o)))
		return err
	})
	return updateErr("order", o.ID, n, err)
}

func (r orderRepo) Get(ctx context.Context, org core.OrganizationID, id core.OrderID) (*quote.Order, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetOrder(ctx, db.GetOrderParams{OrganizationID: org.UUID(), ID: id.UUID()})
	if err != nil {
		return nil, getErr("order", id, err)
	}
	return orderOf(row)
}

func (r orderRepo) GetByNumber(ctx context.Context, org core.OrganizationID, number string) (*quote.Order, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return nil, err
	}
	row, err := q.GetOrderByNumber(ctx, db.GetOrderByNumberParams{OrganizationID: org.UUID(), Number: number})
	if err != nil {
		return nil, getErr("order", number, err)
	}
	return orderOf(row)
}

func (r orderRepo) NextSequence(ctx context.Context, org core.OrganizationID, year int) (int, error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return 0, err
	}
	start, end := quoteYear(year)
	n, err := q.NextOrderSequence(ctx, db.NextOrderSequenceParams{OrganizationID: org.UUID(), YearStart: start, YearEnd: end})
	if err != nil {
		return 0, fmt.Errorf("next order sequence: %w", mapErr(err))
	}
	return int(n), nil
}

func (r orderRepo) List(ctx context.Context, org core.OrganizationID, f quote.OrderFilter, page core.PageRequest) (core.Page[*quote.Order], error) {
	q, err := r.t.queries(ctx, org)
	if err != nil {
		return core.Page[*quote.Order]{}, err
	}
	after, lim, err := pageArgs(page)
	if err != nil {
		return core.Page[*quote.Order]{}, err
	}
	rows, err := q.ListOrders(ctx, db.ListOrdersParams{
		OrganizationID: org.UUID(),
		CustomerID:     optUUID(f.CustomerID),
		Statuses:       enumStrings(f.Statuses),
		After:          after,
		Lim:            lim,
	})
	if err != nil {
		return core.Page[*quote.Order]{}, fmt.Errorf("list orders: %w", mapErr(err))
	}
	return pageOf(rows, page, orderOf, func(x *quote.Order) string { return x.ID.String() })
}

// ListExecutable scans every organization.
func (r orderRepo) ListExecutable(ctx context.Context, now time.Time, limit int) ([]*quote.Order, error) {
	q, err := r.t.queriesAll(ctx)
	if err != nil {
		return nil, err
	}
	n := utc(now)
	rows, err := q.ListExecutableOrders(ctx, db.ListExecutableOrdersParams{Now: &n, Lim: quoteLimit(limit)})
	if err != nil {
		return nil, fmt.Errorf("list executable orders: %w", mapErr(err))
	}
	return convAll(rows, orderOf)
}
