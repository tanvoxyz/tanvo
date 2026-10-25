package rpc

import (
	"fmt"
	"time"

	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
)

// Timestamp converts t to a proto timestamp; the zero time is nil (an
// unset field).
func Timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// TimestampPtr converts *t; nil is nil.
func TimestampPtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return Timestamp(*t)
}

// Time converts a proto timestamp to a UTC time; nil is the zero time.
func Time(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime().UTC()
}

// TimePtr converts a proto timestamp to a UTC time; nil is nil.
func TimePtr(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime().UTC()
	return &t
}

// Date converts d to a google.type.Date; the zero date is nil.
func Date(d core.Date) *date.Date {
	if d.IsZero() {
		return nil
	}
	return &date.Date{Year: int32(d.Year), Month: int32(d.Month), Day: int32(d.Day)}
}

// FromDate converts a google.type.Date; nil is the zero date. A date that
// is not a full calendar date fails with an InvalidArgument violation on
// field.
func FromDate(field string, d *date.Date) (core.Date, error) {
	if d == nil {
		return core.Date{}, nil
	}
	out := core.Date{Year: int(d.GetYear()), Month: time.Month(d.GetMonth()), Day: int(d.GetDay())}
	if d.GetYear() < 1 || d.GetMonth() < 1 || d.GetDay() < 1 || core.NewDate(out.Year, out.Month, out.Day) != out {
		return core.Date{}, InvalidArgument("invalid date", []FieldViolation{{Field: field, Reason: core.CodeInvalid, Description: "must be a calendar date"}})
	}
	return out, nil
}

// Decimal returns the plain text of d ("12.50").
func Decimal(d money.Decimal) string { return d.String() }

// DecimalPtr returns the text of *d, or nil.
func DecimalPtr(d *money.Decimal) *string {
	if d == nil {
		return nil
	}
	s := d.String()
	return &s
}

// ParseDecimal parses the decimal text of field; an invalid one fails
// with an InvalidArgument violation on field.
func ParseDecimal(field, s string) (money.Decimal, error) {
	d, err := money.Parse(s)
	if err != nil {
		return money.Decimal{}, InvalidArgument("invalid decimal", []FieldViolation{{Field: field, Reason: core.CodeInvalid, Description: "must be a decimal number"}})
	}
	return d, nil
}

// Money converts an amount in minor units of c to a Money in major units
// ("1250" USD is "12.50"). A zero currency is nil.
func Money(minor int64, c money.Currency) *commonv1.Money {
	if c.IsZero() {
		return nil
	}
	amount := c.FromMinor(minor).StringFixed(c.Exponent())
	return commonv1.Money_builder{Currency: new(c.Code()), Amount: &amount}.Build()
}

// FromMoney converts a Money to minor units of its currency. An unknown
// currency, or an amount with more decimals than the currency has, fails
// with an InvalidArgument violation on field.
func FromMoney(field string, m *commonv1.Money) (int64, money.Currency, error) {
	c, err := money.LookupCurrency(m.GetCurrency())
	if err != nil {
		return 0, money.Currency{}, InvalidArgument("invalid money", []FieldViolation{{Field: field + ".currency", Reason: core.CodeInvalidCurrency}})
	}
	d, err := ParseDecimal(field+".amount", m.GetAmount())
	if err != nil {
		return 0, money.Currency{}, err
	}
	minor, err := c.ToMinor(d, money.HalfUp)
	if err != nil || !c.FromMinor(minor).Equal(d) {
		return 0, money.Currency{}, InvalidArgument("invalid money", []FieldViolation{{
			Field: field + ".amount", Reason: core.CodeInvalid,
			Description: fmt.Sprintf("must have at most %d decimals", c.Exponent()),
		}})
	}
	return minor, c, nil
}

// Metadata converts a metadata list to its proto items.
func Metadata(m core.Metadata) []*commonv1.MetadataItem {
	out := make([]*commonv1.MetadataItem, len(m))
	for i, it := range m {
		out[i] = commonv1.MetadataItem_builder{Key: new(it.Key), Value: new(it.Value), DisplayInInvoice: new(it.DisplayInInvoice)}.Build()
	}
	return out
}

// FromMetadata converts proto metadata items to a metadata list.
func FromMetadata(items []*commonv1.MetadataItem) core.Metadata {
	out := make(core.Metadata, len(items))
	for i, it := range items {
		out[i] = core.MetadataItem{Key: it.GetKey(), Value: it.GetValue(), DisplayInInvoice: it.GetDisplayInInvoice()}
	}
	return out
}

// Struct returns the Go values of a Struct (nil for nil): numbers are
// float64, as [structpb.Struct.AsMap] gives them.
func Struct(s *structpb.Struct) map[string]any {
	if s == nil {
		return nil
	}
	return s.AsMap()
}

// StringPtr returns a pointer to s for an optional proto field, nil when
// s is empty.
func StringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Opt returns a pointer to v when the optional proto field is set (its
// Has method), nil otherwise: `Opt(b.HasDescription(), b.GetDescription())`.
func Opt[T any](has bool, v T) *T {
	if !has {
		return nil
	}
	return &v
}

// OptInt is [Opt] for an optional int32 field held as an int.
func OptInt(has bool, v int32) *int {
	if !has {
		return nil
	}
	return new(int(v))
}

// Enum maps a domain enumeration D to a proto enumeration P, both ways.
// The zero domain value maps to the proto UNSPECIFIED value (0) and back.
type Enum[D comparable, P ~int32] struct {
	toProto map[D]P
	toCore  map[P]D
}

// NewEnum returns the mapping of the pairs; it panics when a value is
// repeated, as the pairs are constants.
func NewEnum[D comparable, P ~int32](pairs map[D]P) Enum[D, P] {
	e := Enum[D, P]{toProto: make(map[D]P, len(pairs)), toCore: make(map[P]D, len(pairs))}
	for d, p := range pairs {
		if _, dup := e.toCore[p]; dup {
			panic(fmt.Sprintf("rpc: enum value %d repeated", p))
		}
		e.toProto[d] = p
		e.toCore[p] = d
	}
	return e
}

// Proto returns the proto value of d; an unmapped value is 0.
func (e Enum[D, P]) Proto(d D) P { return e.toProto[d] }

// Core returns the domain value of p; 0 and unmapped values are the zero
// value.
func (e Enum[D, P]) Core(p P) D { return e.toCore[p] }

// CorePtr returns the domain value of *p, or nil when p is nil.
func (e Enum[D, P]) CorePtr(p *P) *D {
	if p == nil {
		return nil
	}
	d := e.Core(*p)
	return &d
}

// PresentationBreakdowns converts the presentation breakdowns of fees (lago
// V1::PresentationBreakdownSerializer): a key missing on the events is
// null.
func PresentationBreakdowns(bs []invoice.PresentationBreakdown) []*commonv1.PresentationBreakdown {
	out := make([]*commonv1.PresentationBreakdown, 0, len(bs))
	for _, b := range bs {
		by := &structpb.Struct{Fields: make(map[string]*structpb.Value, len(b.PresentationBy))}
		for k, v := range b.PresentationBy {
			if v == nil {
				by.Fields[k] = structpb.NewNullValue()
			} else {
				by.Fields[k] = structpb.NewStringValue(*v)
			}
		}
		out = append(out, commonv1.PresentationBreakdown_builder{PresentationBy: by, Units: new(Decimal(b.Units))}.Build())
	}
	return out
}
