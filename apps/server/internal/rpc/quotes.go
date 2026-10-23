package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/tanvoxyz/tanvo/libs/billing/quotes"
	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/quote"
	"github.com/tanvoxyz/tanvo/libs/money"
	quotev1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/quote/v1"
	"github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/quote/v1/quotev1connect"
)

// Quotes serves tanvo.quote.v1.QuoteService over the billing library's
// quote service (lago GraphQL Mutations::Quotes::* and
// Mutations::QuoteVersions::*, Api::V1::QuotesController).
type Quotes struct {
	quotev1connect.UnimplementedQuoteServiceHandler

	svc *quotes.Service
}

var _ quotev1connect.QuoteServiceHandler = (*Quotes)(nil)

// NewQuotes returns the service over svc.
func NewQuotes(svc *quotes.Service) *Quotes { return &Quotes{svc: svc} }

var (
	quoteOrderTypes = NewEnum(map[quote.OrderType]quotev1.OrderType{
		quote.OrderTypeSubscriptionCreation:  quotev1.OrderType_ORDER_TYPE_SUBSCRIPTION_CREATION,
		quote.OrderTypeSubscriptionAmendment: quotev1.OrderType_ORDER_TYPE_SUBSCRIPTION_AMENDMENT,
		quote.OrderTypeOneOff:                quotev1.OrderType_ORDER_TYPE_ONE_OFF,
	})
	quoteVersionStatuses = NewEnum(map[quote.VersionStatus]quotev1.QuoteVersionStatus{
		quote.VersionStatusDraft:    quotev1.QuoteVersionStatus_QUOTE_VERSION_STATUS_DRAFT,
		quote.VersionStatusApproved: quotev1.QuoteVersionStatus_QUOTE_VERSION_STATUS_APPROVED,
		quote.VersionStatusVoided:   quotev1.QuoteVersionStatus_QUOTE_VERSION_STATUS_VOIDED,
	})
	quoteVoidReasons = NewEnum(map[quote.VersionVoidReason]quotev1.QuoteVersionVoidReason{
		quote.VersionVoidReasonManual:           quotev1.QuoteVersionVoidReason_QUOTE_VERSION_VOID_REASON_MANUAL,
		quote.VersionVoidReasonSuperseded:       quotev1.QuoteVersionVoidReason_QUOTE_VERSION_VOID_REASON_SUPERSEDED,
		quote.VersionVoidReasonCascadeOfExpired: quotev1.QuoteVersionVoidReason_QUOTE_VERSION_VOID_REASON_CASCADE_OF_EXPIRED,
		quote.VersionVoidReasonCascadeOfVoided:  quotev1.QuoteVersionVoidReason_QUOTE_VERSION_VOID_REASON_CASCADE_OF_VOIDED,
	})
	orderFormStatuses = NewEnum(map[quote.OrderFormStatus]quotev1.OrderFormStatus{
		quote.OrderFormStatusGenerated: quotev1.OrderFormStatus_ORDER_FORM_STATUS_GENERATED,
		quote.OrderFormStatusSigned:    quotev1.OrderFormStatus_ORDER_FORM_STATUS_SIGNED,
		quote.OrderFormStatusExpired:   quotev1.OrderFormStatus_ORDER_FORM_STATUS_EXPIRED,
		quote.OrderFormStatusVoided:    quotev1.OrderFormStatus_ORDER_FORM_STATUS_VOIDED,
	})
	orderFormVoidReasons = NewEnum(map[quote.OrderFormVoidReason]quotev1.OrderFormVoidReason{
		quote.OrderFormVoidReasonManual:  quotev1.OrderFormVoidReason_ORDER_FORM_VOID_REASON_MANUAL,
		quote.OrderFormVoidReasonExpired: quotev1.OrderFormVoidReason_ORDER_FORM_VOID_REASON_EXPIRED,
		quote.OrderFormVoidReasonInvalid: quotev1.OrderFormVoidReason_ORDER_FORM_VOID_REASON_INVALID,
	})
	orderStatuses = NewEnum(map[quote.OrderStatus]quotev1.OrderStatus{
		quote.OrderStatusCreated:  quotev1.OrderStatus_ORDER_STATUS_CREATED,
		quote.OrderStatusExecuted: quotev1.OrderStatus_ORDER_STATUS_EXECUTED,
		quote.OrderStatusFailed:   quotev1.OrderStatus_ORDER_STATUS_FAILED,
	})
	executionModes = NewEnum(map[quote.ExecutionMode]quotev1.ExecutionMode{
		quote.ExecutionModeExecute:   quotev1.ExecutionMode_EXECUTION_MODE_EXECUTE,
		quote.ExecutionModeOrderOnly: quotev1.ExecutionMode_EXECUTION_MODE_ORDER_ONLY,
	})
)

// quoteUpdateFields and versionUpdateFields are the editable fields
// (lago Mutations::Quotes::Update and Mutations::QuoteVersions::Update).
var (
	quoteUpdateFields   = []string{"owner_ids"}
	versionUpdateFields = []string{"content", "currency", "billing_entity_id", "billing_items"}
	orderUpdateFields   = []string{"execution_mode", "execute_at"}
)

// CreateQuote implements QuoteService.CreateQuote. customer_id and
// subscription_id are IDs or external_id:<id>; the current_version
// carries the first version's content, billing items and billing entity.
func (s *Quotes) CreateQuote(ctx context.Context, req *quotev1.CreateQuoteRequest) (*quotev1.CreateQuoteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	org := p.OrganizationID
	b := req.GetQuote()
	in := quotes.CreateInput{OrganizationID: org, OrderType: quoteOrderTypes.Core(b.GetOrderType())}
	if in.CustomerID, err = s.customer(ctx, org, "quote.customer_id", b.GetCustomerId()); err != nil {
		return nil, err
	}
	if b.HasSubscriptionId() {
		if in.SubscriptionID, err = s.subscription(ctx, org, "quote.subscription_id", b.GetSubscriptionId()); err != nil {
			return nil, err
		}
	}
	if in.Owners, err = owners("quote.owner_ids", b.GetOwnerIds()); err != nil {
		return nil, err
	}
	if v := b.GetCurrentVersion(); v != nil {
		in.Content = Opt(v.HasContent(), v.GetContent())
		if in.BillingItems, err = structJSON(v.GetBillingItems()); err != nil {
			return nil, err
		}
		if v.HasBillingEntityId() {
			if in.BillingEntityID, err = s.billingEntity(ctx, org, "quote.current_version.billing_entity_id", v.GetBillingEntityId()); err != nil {
				return nil, err
			}
		}
	}
	q, err := s.svc.Create(ctx, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := quoteProto(q)
	if err != nil {
		return nil, err
	}
	return quotev1.CreateQuoteResponse_builder{Quote: out}.Build(), nil
}

// GetQuote implements QuoteService.GetQuote.
func (s *Quotes) GetQuote(ctx context.Context, req *quotev1.GetQuoteRequest) (*quotev1.GetQuoteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.QuoteID]("quote_id", req.GetQuoteId())
	if err != nil {
		return nil, err
	}
	q, err := s.svc.Get(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := quoteProto(q)
	if err != nil {
		return nil, err
	}
	return quotev1.GetQuoteResponse_builder{Quote: out}.Build(), nil
}

// ListQuotes implements QuoteService.ListQuotes (lago QuotesQuery).
func (s *Quotes) ListQuotes(ctx context.Context, req *quotev1.ListQuotesRequest) (*quotev1.ListQuotesResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := quoteListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.List(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	items := make([]*quotev1.Quote, 0, len(page.Items))
	for _, q := range page.Items {
		out, err := quoteProto(q)
		if err != nil {
			return nil, err
		}
		items = append(items, out)
	}
	return quotev1.ListQuotesResponse_builder{Quotes: items, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdateQuote implements QuoteService.UpdateQuote: owner_ids replaces the
// owners.
func (s *Quotes) UpdateQuote(ctx context.Context, req *quotev1.UpdateQuoteRequest) (*quotev1.UpdateQuoteResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetQuote()
	id, err := ParseID[core.QuoteID]("quote.id", b.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), quoteUpdateFields, func(path string) bool {
		return path == "owner_ids" && len(b.GetOwnerIds()) > 0
	})
	if err != nil {
		return nil, err
	}
	var in quotes.UpdateInput
	if len(paths) > 0 {
		o, err := owners("quote.owner_ids", b.GetOwnerIds())
		if err != nil {
			return nil, err
		}
		in.Owners = core.Some(o)
	}
	q, err := s.svc.Update(ctx, p.OrganizationID, id, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := quoteProto(q)
	if err != nil {
		return nil, err
	}
	return quotev1.UpdateQuoteResponse_builder{Quote: out}.Build(), nil
}

// ListQuoteVersions implements QuoteService.ListQuoteVersions: latest
// first.
func (s *Quotes) ListQuoteVersions(ctx context.Context, req *quotev1.ListQuoteVersionsRequest) (*quotev1.ListQuoteVersionsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.QuoteID]("quote_id", req.GetQuoteId())
	if err != nil {
		return nil, err
	}
	page, err := s.svc.ListVersions(ctx, p.OrganizationID, id, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	items := make([]*quotev1.QuoteVersion, 0, len(page.Items))
	for _, v := range page.Items {
		out, err := versionProto(v)
		if err != nil {
			return nil, err
		}
		items = append(items, out)
	}
	return quotev1.ListQuoteVersionsResponse_builder{Versions: items, NextPageToken: new(page.NextToken)}.Build(), nil
}

// AddQuoteImage implements QuoteService.AddQuoteImage (lago
// Mutations::Quotes::AddImage).
func (s *Quotes) AddQuoteImage(ctx context.Context, req *quotev1.AddQuoteImageRequest) (*quotev1.AddQuoteImageResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.QuoteID]("quote_id", req.GetQuoteId())
	if err != nil {
		return nil, err
	}
	img, err := s.svc.AddImage(ctx, p.OrganizationID, id, req.GetImage())
	if err != nil {
		return nil, Error(ctx, err)
	}
	return quotev1.AddQuoteImageResponse_builder{Id: new(img.ID), Url: StringPtr(img.URL)}.Build(), nil
}

// GetQuoteVersion implements QuoteService.GetQuoteVersion.
func (s *Quotes) GetQuoteVersion(ctx context.Context, req *quotev1.GetQuoteVersionRequest) (*quotev1.GetQuoteVersionResponse, error) {
	p, q, id, err := versionRefs(ctx, req.GetQuoteId(), req.GetVersionId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.GetVersion(ctx, p, q, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := versionProto(v)
	if err != nil {
		return nil, err
	}
	return quotev1.GetQuoteVersionResponse_builder{Version: out}.Build(), nil
}

// UpdateQuoteVersion implements QuoteService.UpdateQuoteVersion (lago
// Mutations::QuoteVersions::Update): a draft's content, currency, billing
// entity (empty clears it) and billing items.
func (s *Quotes) UpdateQuoteVersion(ctx context.Context, req *quotev1.UpdateQuoteVersionRequest) (*quotev1.UpdateQuoteVersionResponse, error) {
	b := req.GetVersion()
	org, q, id, err := versionRefs(ctx, req.GetQuoteId(), b.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), versionUpdateFields, func(path string) bool {
		switch path {
		case "content":
			return b.HasContent()
		case "currency":
			return b.HasCurrency()
		case "billing_entity_id":
			return b.HasBillingEntityId()
		case "billing_items":
			return b.HasBillingItems()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var in quotes.UpdateVersionInput
	for _, path := range paths {
		switch path {
		case "content":
			in.Content = core.Some(Opt(b.HasContent(), b.GetContent()))
		case "currency":
			c, err := money.LookupCurrency(b.GetCurrency())
			if err != nil {
				return nil, InvalidArgument("invalid currency", []FieldViolation{{Field: "version.currency", Reason: core.CodeInvalid, Description: "must be an ISO 4217 code"}})
			}
			in.Currency = core.Some(c)
		case "billing_entity_id":
			var be core.BillingEntityID
			if b.GetBillingEntityId() != "" {
				if be, err = s.billingEntity(ctx, org, "version.billing_entity_id", b.GetBillingEntityId()); err != nil {
					return nil, err
				}
			}
			in.BillingEntityID = core.Some(be)
		case "billing_items":
			raw, err := structJSON(b.GetBillingItems())
			if err != nil {
				return nil, err
			}
			in.BillingItems = core.Some(raw)
		}
	}
	v, err := s.svc.UpdateVersion(ctx, org, q, id, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := versionProto(v)
	if err != nil {
		return nil, err
	}
	return quotev1.UpdateQuoteVersionResponse_builder{Version: out}.Build(), nil
}

// CloneQuoteVersion implements QuoteService.CloneQuoteVersion (lago
// QuoteVersions::CloneService).
func (s *Quotes) CloneQuoteVersion(ctx context.Context, req *quotev1.CloneQuoteVersionRequest) (*quotev1.CloneQuoteVersionResponse, error) {
	org, q, id, err := versionRefs(ctx, req.GetQuoteId(), req.GetVersionId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.CloneVersion(ctx, org, q, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := versionProto(v)
	if err != nil {
		return nil, err
	}
	return quotev1.CloneQuoteVersionResponse_builder{Version: out}.Build(), nil
}

// ApproveQuoteVersion implements QuoteService.ApproveQuoteVersion (lago
// QuoteVersions::ApproveService): it returns the approved version and the
// order form it generated.
func (s *Quotes) ApproveQuoteVersion(ctx context.Context, req *quotev1.ApproveQuoteVersionRequest) (*quotev1.ApproveQuoteVersionResponse, error) {
	org, q, id, err := versionRefs(ctx, req.GetQuoteId(), req.GetVersionId())
	if err != nil {
		return nil, err
	}
	a, err := s.svc.ApproveVersion(ctx, org, q, id, TimePtr(req.GetExpiresAt()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	v, err := versionProto(a.Version)
	if err != nil {
		return nil, err
	}
	f, err := orderFormProto(a.OrderForm)
	if err != nil {
		return nil, err
	}
	return quotev1.ApproveQuoteVersionResponse_builder{Version: v, OrderForm: f}.Build(), nil
}

// VoidQuoteVersion implements QuoteService.VoidQuoteVersion (lago
// QuoteVersions::VoidService, reason manual).
func (s *Quotes) VoidQuoteVersion(ctx context.Context, req *quotev1.VoidQuoteVersionRequest) (*quotev1.VoidQuoteVersionResponse, error) {
	org, q, id, err := versionRefs(ctx, req.GetQuoteId(), req.GetVersionId())
	if err != nil {
		return nil, err
	}
	v, err := s.svc.VoidVersion(ctx, org, q, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := versionProto(v)
	if err != nil {
		return nil, err
	}
	return quotev1.VoidQuoteVersionResponse_builder{Version: out}.Build(), nil
}

func versionRefs(ctx context.Context, quoteID, versionID string) (core.OrganizationID, core.QuoteID, core.QuoteVersionID, error) {
	p, err := principal(ctx)
	if err != nil {
		return core.OrganizationID{}, core.QuoteID{}, core.QuoteVersionID{}, err
	}
	q, err := ParseID[core.QuoteID]("quote_id", quoteID)
	if err != nil {
		return core.OrganizationID{}, core.QuoteID{}, core.QuoteVersionID{}, err
	}
	id, err := ParseID[core.QuoteVersionID]("version_id", versionID)
	if err != nil {
		return core.OrganizationID{}, core.QuoteID{}, core.QuoteVersionID{}, err
	}
	return p.OrganizationID, q, id, nil
}

func (s *Quotes) customer(ctx context.Context, org core.OrganizationID, field, ref string) (core.CustomerID, error) {
	r, err := ParseRef(field, ref)
	if err != nil {
		return core.CustomerID{}, err
	}
	switch {
	case r.Code != "":
		return core.CustomerID{}, refKindError(field)
	case r.ExternalID != "":
		id, err := s.svc.CustomerByExternalID(ctx, org, r.ExternalID)
		if err != nil {
			return id, Error(ctx, err)
		}
		return id, nil
	}
	return uuidRef[core.CustomerID](field, r)
}

func (s *Quotes) subscription(ctx context.Context, org core.OrganizationID, field, ref string) (core.SubscriptionID, error) {
	r, err := ParseRef(field, ref)
	if err != nil {
		return core.SubscriptionID{}, err
	}
	switch {
	case r.Code != "":
		return core.SubscriptionID{}, refKindError(field)
	case r.ExternalID != "":
		id, err := s.svc.SubscriptionByExternalID(ctx, org, r.ExternalID)
		if err != nil {
			return id, Error(ctx, err)
		}
		return id, nil
	}
	return uuidRef[core.SubscriptionID](field, r)
}

func (s *Quotes) billingEntity(ctx context.Context, org core.OrganizationID, field, ref string) (core.BillingEntityID, error) {
	r, err := ParseRef(field, ref)
	if err != nil {
		return core.BillingEntityID{}, err
	}
	switch {
	case r.ExternalID != "":
		return core.BillingEntityID{}, refKindError(field)
	case r.Code != "":
		id, err := s.svc.BillingEntityByCode(ctx, org, r.Code)
		if err != nil {
			return id, Error(ctx, err)
		}
		return id, nil
	}
	return uuidRef[core.BillingEntityID](field, r)
}

func refKindError(field string) error {
	return InvalidArgument("invalid reference", []FieldViolation{{Field: field, Reason: core.CodeInvalid, Description: "this kind of reference is not supported here"}})
}

func owners(field string, ids []string) ([]core.MembershipID, error) {
	out := make([]core.MembershipID, 0, len(ids))
	for _, s := range ids {
		id, err := ParseID[core.MembershipID](field, s)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// structJSON encodes a Struct as a JSON object; nil for nil.
func structJSON(s *structpb.Struct) (json.RawMessage, error) {
	if s == nil {
		return nil, nil
	}
	b, err := protojson.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encode struct: %w", err)
	}
	return b, nil
}

// jsonStructOf decodes a JSON object into a Struct; nil for empty or
// non-object JSON.
func jsonStructOf(raw json.RawMessage) (*structpb.Struct, error) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, nil
	}
	var s structpb.Struct
	if err := protojson.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode struct: %w", err)
	}
	return &s, nil
}

// quoteProto renders q (lago V1::QuoteSerializer).
func quoteProto(q *quotes.QuoteView) (*quotev1.Quote, error) {
	x := q.Quote
	ownerIDs := make([]string, len(x.Owners))
	for i, o := range x.Owners {
		ownerIDs[i] = o.String()
	}
	b := quotev1.Quote_builder{
		Id:         new(x.ID.String()),
		Number:     new(x.Number),
		CustomerId: new(x.CustomerID.String()),
		OrderType:  new(quoteOrderTypes.Proto(x.OrderType)),
		OwnerIds:   ownerIDs,
		Images:     q.Images,
		CreatedAt:  Timestamp(x.CreatedAt),
		UpdatedAt:  Timestamp(x.UpdatedAt),
	}
	if !x.SubscriptionID.IsZero() {
		b.SubscriptionId = new(x.SubscriptionID.String())
	}
	if q.Current != nil {
		v, err := versionProto(q.Current)
		if err != nil {
			return nil, err
		}
		b.CurrentVersion = v
	}
	return b.Build(), nil
}

// versionProto renders v (lago V1::QuoteVersionSerializer).
func versionProto(v *quotes.VersionView) (*quotev1.QuoteVersion, error) {
	x := v.Version
	items, err := jsonStructOf(x.BillingItems)
	if err != nil {
		return nil, err
	}
	mentions, err := jsonStructOf(x.MentionVariables)
	if err != nil {
		return nil, err
	}
	b := quotev1.QuoteVersion_builder{
		Id:                new(x.ID.String()),
		QuoteId:           new(x.QuoteID.String()),
		Version:           new(int32(x.Version)),
		Status:            new(quoteVersionStatuses.Proto(x.Status)),
		Content:           x.Content,
		Currency:          StringPtr(x.Currency.Code()),
		BillingEntityCode: StringPtr(v.BillingEntityCode),
		BillingItems:      items,
		MentionVariables:  mentions,
		ApprovedAt:        TimestampPtr(x.ApprovedAt),
		VoidedAt:          TimestampPtr(x.VoidedAt),
		CreatedAt:         Timestamp(x.CreatedAt),
		UpdatedAt:         Timestamp(x.UpdatedAt),
	}
	if x.VoidReason != "" {
		b.VoidReason = new(quoteVoidReasons.Proto(x.VoidReason))
	}
	if !x.BillingEntityID.IsZero() {
		b.BillingEntityId = new(x.BillingEntityID.String())
	}
	return b.Build(), nil
}

// quoteListFilter reads the ListQuotes filter.
func quoteListFilter(s string) (quote.Filter, error) {
	var f quote.Filter
	rest, err := rangeTerms(s, map[string]func(Restriction) error{
		"create_time": func(r Restriction) error { return timeRange(r, &f.From, &f.To) },
	})
	if err != nil {
		return f, err
	}
	eq, err := Equalities(rest, "customer_id", "external_customer_id", "number", "order_type", "owner_id", "status")
	if err != nil {
		return f, err
	}
	for _, v := range eq["customer_id"] {
		id, err := filterID[core.CustomerID]("customer_id", v)
		if err != nil {
			return f, err
		}
		f.CustomerIDs = append(f.CustomerIDs, id)
	}
	f.ExternalCustomerIDs = eq["external_customer_id"]
	f.Numbers = eq["number"]
	for _, v := range eq["owner_id"] {
		id, err := filterID[core.MembershipID]("owner_id", v)
		if err != nil {
			return f, err
		}
		f.OwnerIDs = append(f.OwnerIDs, id)
	}
	types, err := FilterEnum[quotev1.OrderType]("order_type", eq["order_type"], quotev1.OrderType(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, t := range types {
		f.OrderTypes = append(f.OrderTypes, quoteOrderTypes.Core(t))
	}
	statuses, err := FilterEnum[quotev1.QuoteVersionStatus]("status", eq["status"], quotev1.QuoteVersionStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, st := range statuses {
		f.Statuses = append(f.Statuses, quoteVersionStatuses.Core(st))
	}
	return f, nil
}

// rangeTerms splits the AND terms of filter s: the restrictions on a
// field of ranges go to its function, the rest is returned.
func rangeTerms(s string, ranges map[string]func(Restriction) error) (FilterExpr, error) {
	e, err := ParseFilter(s)
	if err != nil || e == nil {
		return nil, err
	}
	terms := []FilterExpr{e}
	if and, ok := e.(FilterAnd); ok {
		terms = and
	}
	var rest FilterAnd
	for _, t := range terms {
		r, ok := t.(Restriction)
		if !ok {
			rest = append(rest, t)
			continue
		}
		fn, ok := ranges[r.Field]
		if !ok {
			rest = append(rest, t)
			continue
		}
		if err := fn(r); err != nil {
			return nil, err
		}
	}
	if len(rest) == 0 {
		return nil, nil
	}
	return rest, nil
}

// timeRange applies a time restriction with =, >= or <=: a date
// (YYYY-MM-DD, midnight UTC) or an RFC 3339 instant.
func timeRange(r Restriction, from, to **time.Time) error {
	lo, hi := r.Op == CmpEq || r.Op == CmpGe, r.Op == CmpEq || r.Op == CmpLe
	if !lo && !hi {
		return filterError(fmt.Sprintf("field %q is compared with =, >= or <=", r.Field))
	}
	t, err := time.Parse(time.RFC3339Nano, r.Value)
	if err != nil {
		d, derr := time.Parse(time.DateOnly, r.Value)
		if derr != nil {
			return filterError(fmt.Sprintf("field %q: invalid date %q", r.Field, r.Value))
		}
		t = d
	}
	t = t.UTC()
	if lo {
		*from = &t
	}
	if hi {
		*to = &t
	}
	return nil
}

func filterID[I core.Identifier[I]](field, v string) (I, error) {
	id, err := core.ParseID[I](v)
	if err != nil {
		return id, filterError(fmt.Sprintf("field %q: invalid ID %q", field, v))
	}
	return id, nil
}

// OrderForms serves tanvo.quote.v1.OrderFormService (lago
// Api::V1::OrderFormsController).
type OrderForms struct {
	quotev1connect.UnimplementedOrderFormServiceHandler

	svc *quotes.Service
}

var _ quotev1connect.OrderFormServiceHandler = (*OrderForms)(nil)

// NewOrderForms returns the service over svc.
func NewOrderForms(svc *quotes.Service) *OrderForms { return &OrderForms{svc: svc} }

// GetOrderForm implements OrderFormService.GetOrderForm.
func (s *OrderForms) GetOrderForm(ctx context.Context, req *quotev1.GetOrderFormRequest) (*quotev1.GetOrderFormResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.OrderFormID]("order_form_id", req.GetOrderFormId())
	if err != nil {
		return nil, err
	}
	f, err := s.svc.GetOrderForm(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := orderFormProto(f)
	if err != nil {
		return nil, err
	}
	return quotev1.GetOrderFormResponse_builder{OrderForm: out}.Build(), nil
}

// ListOrderForms implements OrderFormService.ListOrderForms (lago
// OrderFormsQuery).
func (s *OrderForms) ListOrderForms(ctx context.Context, req *quotev1.ListOrderFormsRequest) (*quotev1.ListOrderFormsResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := orderFormListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	page, err := s.svc.ListOrderForms(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	items := make([]*quotev1.OrderForm, 0, len(page.Items))
	for _, x := range page.Items {
		out, err := orderFormProto(x)
		if err != nil {
			return nil, err
		}
		items = append(items, out)
	}
	return quotev1.ListOrderFormsResponse_builder{OrderForms: items, NextPageToken: new(page.NextToken)}.Build(), nil
}

// SignOrderForm implements OrderFormService.SignOrderForm (lago
// OrderForms::MarkAsSignedService): it returns the signed order form and
// the order it created.
func (s *OrderForms) SignOrderForm(ctx context.Context, req *quotev1.SignOrderFormRequest) (*quotev1.SignOrderFormResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.OrderFormID]("order_form_id", req.GetOrderFormId())
	if err != nil {
		return nil, err
	}
	sig, err := s.svc.SignOrderForm(ctx, p.OrganizationID, id, quotes.SignInput{
		SignedDocument: req.GetSignedDocument(),
		ExecutionMode:  executionModes.Core(req.GetExecutionMode()),
		ExecuteAt:      TimePtr(req.GetExecuteAt()),
	})
	if err != nil {
		return nil, Error(ctx, err)
	}
	f, err := orderFormProto(sig.OrderForm)
	if err != nil {
		return nil, err
	}
	o, err := orderProto(sig.Order)
	if err != nil {
		return nil, err
	}
	return quotev1.SignOrderFormResponse_builder{OrderForm: f, Order: o}.Build(), nil
}

// VoidOrderForm implements OrderFormService.VoidOrderForm (lago
// OrderForms::VoidService).
func (s *OrderForms) VoidOrderForm(ctx context.Context, req *quotev1.VoidOrderFormRequest) (*quotev1.VoidOrderFormResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.OrderFormID]("order_form_id", req.GetOrderFormId())
	if err != nil {
		return nil, err
	}
	f, err := s.svc.VoidOrderForm(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := orderFormProto(f)
	if err != nil {
		return nil, err
	}
	return quotev1.VoidOrderFormResponse_builder{OrderForm: out}.Build(), nil
}

// orderFormListFilter reads the ListOrderForms filter
// (order_forms_controller.rb index filters).
func orderFormListFilter(s string) (quote.OrderFormFilter, error) {
	var f quote.OrderFormFilter
	rest, err := rangeTerms(s, map[string]func(Restriction) error{
		"create_time": func(r Restriction) error { return timeRange(r, &f.CreatedFrom, &f.CreatedTo) },
		"expire_time": func(r Restriction) error { return timeRange(r, &f.ExpiresFrom, &f.ExpiresTo) },
	})
	if err != nil {
		return f, err
	}
	eq, err := Equalities(rest, "customer_id", "status", "number", "quote_number", "owner_id")
	if err != nil {
		return f, err
	}
	if v, err := FilterSingle("customer_id", eq["customer_id"]); err != nil {
		return f, err
	} else if v != "" {
		if f.CustomerID, err = filterID[core.CustomerID]("customer_id", v); err != nil {
			return f, err
		}
	}
	if v, err := FilterSingle("owner_id", eq["owner_id"]); err != nil {
		return f, err
	} else if v != "" {
		if f.OwnerID, err = filterID[core.MembershipID]("owner_id", v); err != nil {
			return f, err
		}
	}
	if f.Number, err = FilterSingle("number", eq["number"]); err != nil {
		return f, err
	}
	if f.QuoteNumber, err = FilterSingle("quote_number", eq["quote_number"]); err != nil {
		return f, err
	}
	statuses, err := FilterEnum[quotev1.OrderFormStatus]("status", eq["status"], quotev1.OrderFormStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, st := range statuses {
		f.Statuses = append(f.Statuses, orderFormStatuses.Core(st))
	}
	return f, nil
}

// orderFormProto renders f (lago V1::OrderFormSerializer).
func orderFormProto(f *quotes.OrderFormView) (*quotev1.OrderForm, error) {
	x := f.OrderForm
	snap, err := jsonStructOf(f.BillingSnapshot)
	if err != nil {
		return nil, err
	}
	b := quotev1.OrderForm_builder{
		Id:                new(x.ID.String()),
		Number:            new(x.Number),
		CustomerId:        new(x.CustomerID.String()),
		QuoteId:           new(f.QuoteID.String()),
		QuoteVersionId:    new(x.QuoteVersionID.String()),
		Status:            new(orderFormStatuses.Proto(x.Status)),
		ExpiresAt:         TimestampPtr(x.ExpiresAt),
		SignedAt:          TimestampPtr(x.SignedAt),
		VoidedAt:          TimestampPtr(x.VoidedAt),
		SignedDocumentUrl: StringPtr(f.SignedDocumentURL),
		BillingSnapshot:   snap,
		CreatedAt:         Timestamp(x.CreatedAt),
		UpdatedAt:         Timestamp(x.UpdatedAt),
	}
	if x.VoidReason != "" {
		b.VoidReason = new(orderFormVoidReasons.Proto(x.VoidReason))
	}
	return b.Build(), nil
}

// Orders serves tanvo.quote.v1.OrderService (lago
// Api::V1::OrdersController and GraphQL Mutations::Orders::Update).
type Orders struct {
	quotev1connect.UnimplementedOrderServiceHandler

	svc *quotes.Service
}

var _ quotev1connect.OrderServiceHandler = (*Orders)(nil)

// NewOrders returns the service over svc.
func NewOrders(svc *quotes.Service) *Orders { return &Orders{svc: svc} }

// GetOrder implements OrderService.GetOrder.
func (s *Orders) GetOrder(ctx context.Context, req *quotev1.GetOrderRequest) (*quotev1.GetOrderResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.OrderID]("order_id", req.GetOrderId())
	if err != nil {
		return nil, err
	}
	o, err := s.svc.GetOrder(ctx, p.OrganizationID, id)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := orderProto(o)
	if err != nil {
		return nil, err
	}
	return quotev1.GetOrderResponse_builder{Order: out}.Build(), nil
}

// ListOrders implements OrderService.ListOrders (lago OrdersQuery).
func (s *Orders) ListOrders(ctx context.Context, req *quotev1.ListOrdersRequest) (*quotev1.ListOrdersResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	f, err := orderListFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	f.Search = req.GetSearch()
	page, err := s.svc.ListOrders(ctx, p.OrganizationID, f, Page(req.GetPageSize(), req.GetPageToken()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	items := make([]*quotev1.Order, 0, len(page.Items))
	for _, x := range page.Items {
		out, err := orderProto(x)
		if err != nil {
			return nil, err
		}
		items = append(items, out)
	}
	return quotev1.ListOrdersResponse_builder{Orders: items, NextPageToken: new(page.NextToken)}.Build(), nil
}

// UpdateOrder implements OrderService.UpdateOrder (lago
// Orders::UpdateService): execution_mode (unspecified clears it) and
// execute_at (unset clears it).
func (s *Orders) UpdateOrder(ctx context.Context, req *quotev1.UpdateOrderRequest) (*quotev1.UpdateOrderResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	b := req.GetOrder()
	id, err := ParseID[core.OrderID]("order.id", b.GetId())
	if err != nil {
		return nil, err
	}
	paths, err := UpdatePaths(req.GetUpdateMask(), orderUpdateFields, func(path string) bool {
		switch path {
		case "execution_mode":
			return b.HasExecutionMode()
		case "execute_at":
			return b.HasExecuteAt()
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	var in quotes.UpdateOrderInput
	for _, path := range paths {
		switch path {
		case "execution_mode":
			in.ExecutionMode = core.Some(executionModes.Core(b.GetExecutionMode()))
		case "execute_at":
			in.ExecuteAt = core.Some(TimePtr(b.GetExecuteAt()))
		}
	}
	o, err := s.svc.UpdateOrder(ctx, p.OrganizationID, id, in)
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := orderProto(o)
	if err != nil {
		return nil, err
	}
	return quotev1.UpdateOrderResponse_builder{Order: out}.Build(), nil
}

// ExecuteOrder implements OrderService.ExecuteOrder (lago
// Api::V1::OrdersController#execute): a mode that differs from the
// order's is stored first. A failed execution is recorded on the order
// and returned as the error.
func (s *Orders) ExecuteOrder(ctx context.Context, req *quotev1.ExecuteOrderRequest) (*quotev1.ExecuteOrderResponse, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	id, err := ParseID[core.OrderID]("order_id", req.GetOrderId())
	if err != nil {
		return nil, err
	}
	o, err := s.svc.ExecuteOrder(ctx, p.OrganizationID, id, executionModes.Core(req.GetExecutionMode()))
	if err != nil {
		return nil, Error(ctx, err)
	}
	out, err := orderProto(o)
	if err != nil {
		return nil, err
	}
	return quotev1.ExecuteOrderResponse_builder{Order: out}.Build(), nil
}

// orderListFilter reads the ListOrders filter (orders_controller.rb:65-78).
func orderListFilter(s string) (quote.OrderFilter, error) {
	var f quote.OrderFilter
	rest, err := rangeTerms(s, map[string]func(Restriction) error{
		"execute_time": func(r Restriction) error { return timeRange(r, &f.ExecutedFrom, &f.ExecutedTo) },
	})
	if err != nil {
		return f, err
	}
	eq, err := Equalities(rest, "customer_id", "status", "order_type", "execution_mode", "number", "order_form_number", "quote_number", "owner_id")
	if err != nil {
		return f, err
	}
	if v, err := FilterSingle("customer_id", eq["customer_id"]); err != nil {
		return f, err
	} else if v != "" {
		if f.CustomerID, err = filterID[core.CustomerID]("customer_id", v); err != nil {
			return f, err
		}
	}
	if v, err := FilterSingle("owner_id", eq["owner_id"]); err != nil {
		return f, err
	} else if v != "" {
		if f.OwnerID, err = filterID[core.MembershipID]("owner_id", v); err != nil {
			return f, err
		}
	}
	for field, dst := range map[string]*string{"number": &f.Number, "order_form_number": &f.OrderFormNumber, "quote_number": &f.QuoteNumber} {
		if *dst, err = FilterSingle(field, eq[field]); err != nil {
			return f, err
		}
	}
	statuses, err := FilterEnum[quotev1.OrderStatus]("status", eq["status"], quotev1.OrderStatus(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, st := range statuses {
		f.Statuses = append(f.Statuses, orderStatuses.Core(st))
	}
	types, err := FilterEnum[quotev1.OrderType]("order_type", eq["order_type"], quotev1.OrderType(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, t := range types {
		f.OrderTypes = append(f.OrderTypes, quoteOrderTypes.Core(t))
	}
	modes, err := FilterEnum[quotev1.ExecutionMode]("execution_mode", eq["execution_mode"], quotev1.ExecutionMode(0).Descriptor())
	if err != nil {
		return f, err
	}
	for _, m := range modes {
		f.ExecutionModes = append(f.ExecutionModes, executionModes.Core(m))
	}
	return f, nil
}

// orderProto renders o (lago V1::OrderSerializer with billing_snapshot).
func orderProto(o *quotes.OrderView) (*quotev1.Order, error) {
	x := o.Order
	snap, err := jsonStructOf(o.BillingSnapshot)
	if err != nil {
		return nil, err
	}
	b := quotev1.Order_builder{
		Id:              new(x.ID.String()),
		Number:          new(x.Number),
		CustomerId:      new(x.CustomerID.String()),
		OrderFormId:     new(x.OrderFormID.String()),
		Status:          new(orderStatuses.Proto(x.Status)),
		OrderType:       new(quoteOrderTypes.Proto(o.OrderType)),
		Currency:        StringPtr(o.Currency.Code()),
		ExecuteAt:       TimestampPtr(x.ExecuteAt),
		ExecutedAt:      TimestampPtr(x.ExecutedAt),
		BillingSnapshot: snap,
		CreatedAt:       Timestamp(x.CreatedAt),
		UpdatedAt:       Timestamp(x.UpdatedAt),
	}
	if x.ExecutionMode != "" {
		b.ExecutionMode = new(executionModes.Proto(x.ExecutionMode))
	}
	if r := o.Record; r != nil {
		rb := quotev1.OrderExecutionRecord_builder{
			ExecutedAt:                TimestampPtr(r.ExecutedAt),
			SubscriptionIds:           quoteIDStrings(r.SubscriptionIDs),
			TerminatedSubscriptionIds: quoteIDStrings(r.TerminatedSubscriptionIDs),
			AppliedCouponIds:          quoteIDStrings(r.AppliedCouponIDs),
			WalletIds:                 quoteIDStrings(r.WalletIDs),
			Errors:                    r.Errors,
		}
		if r.ExecutionMode != nil {
			rb.ExecutionMode = new(executionModes.Proto(*r.ExecutionMode))
		}
		if r.InvoiceID != nil {
			rb.InvoiceId = new(r.InvoiceID.String())
		}
		b.ExecutionRecord = rb.Build()
	}
	return b.Build(), nil
}

// quoteIDStrings renders identifiers.
func quoteIDStrings[I fmt.Stringer](ids []I) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// uuidRef is the identifier of the ID reference r.
func uuidRef[I core.Identifier[I]](field string, r Ref) (I, error) {
	return ParseID[I](field, r.ID.String())
}
