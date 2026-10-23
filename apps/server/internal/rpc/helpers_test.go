package rpc

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/type/date"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/money"
	catalogv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/catalog/v1"
	commonv1 "github.com/tanvoxyz/tanvo/libs/proto/gen/go/tanvo/common/v1"
)

// violation returns the field and reason of the single violation of err.
func violation(t *testing.T, err error) (string, string) {
	t.Helper()
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("got %v, want = InvalidArgument", err)
	}
	v := ErrorViolations(err)
	if len(v) != 1 {
		t.Fatalf("got violations = %+v, want = one", v)
	}
	return v[0].Field, v[0].Reason
}

func TestParseRef(t *testing.T) {
	id := "01000000-0000-7000-8000-000000000001"
	tests := []struct {
		in      string
		want    Ref
		wantErr bool
	}{
		{in: "code:api_calls", want: Ref{Code: "api_calls"}},
		{in: "external_id:cus_42", want: Ref{ExternalID: "cus_42"}},
		{in: id, want: Ref{ID: uuid.MustParse(id)}},
		{in: "code:", wantErr: true},
		{in: "external_id:", wantErr: true},
		{in: "api_calls", wantErr: true},
		{in: "00000000-0000-0000-0000-000000000000", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseRef("ref", tt.in)
		if tt.wantErr {
			if f, _ := violation(t, err); f != "ref" {
				t.Errorf("ParseRef(%q) field got = %q, want = ref", tt.in, f)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ParseRef(%q) got = %+v, %v, want = %+v", tt.in, got, err, tt.want)
		}
	}
}

func TestLookup(t *testing.T) {
	ctx := context.Background()
	id := core.NewID[core.BillableMetricID](core.UUIDv7Generator{})
	l := Lookup[core.BillableMetricID, string]{
		ByID:   func(_ context.Context, got core.BillableMetricID) (string, error) { return "id:" + got.String(), nil },
		ByCode: func(_ context.Context, code string) (string, error) { return "code:" + code, nil },
	}
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{in: id.String(), want: "id:" + id.String()},
		{in: "code:bm", want: "code:bm"},
		{in: "external_id:x", wantErr: true},
		{in: "bm", wantErr: true},
	}
	for _, tt := range tests {
		got, err := l.Find(ctx, "billable_metric_id", tt.in)
		if tt.wantErr {
			violation(t, err)
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Find(%q) got = %q, %v, want = %q", tt.in, got, err, tt.want)
		}
	}
	if _, err := ParseID[core.PlanID]("plan_id", "nope"); err == nil {
		t.Error("ParseID(nope) got = nil, want an error")
	}
	if got, err := ParseID[core.PlanID]("plan_id", ""); err != nil || !got.IsZero() {
		t.Errorf("ParseID(\"\") got = %v, %v, want = the zero ID", got, err)
	}
}

func TestFilter(t *testing.T) {
	tests := []struct {
		in      string
		want    map[string][]string
		wantErr bool
	}{
		{in: "", want: map[string][]string{}},
		{in: `a = "x"`, want: map[string][]string{"a": {"x"}}},
		{in: `a = x AND b = 'y z'`, want: map[string][]string{"a": {"x"}, "b": {"y z"}}},
		{in: `a = x b = y`, want: map[string][]string{"a": {"x"}, "b": {"y"}}},
		{in: `(a = x OR a = y) AND b = true`, want: map[string][]string{"a": {"x", "y"}, "b": {"true"}}},
		{in: `a = x OR b = y`, wantErr: true},
		{in: `a != x`, wantErr: true},
		{in: `NOT a = x`, wantErr: true},
		{in: `c = x`, wantErr: true},
		{in: `a = x AND a = y`, wantErr: true},
		{in: `a = `, wantErr: true},
		{in: `a = "x`, wantErr: true},
		{in: `(a = x`, wantErr: true},
	}
	for _, tt := range tests {
		e, err := ParseFilter(tt.in)
		var got map[string][]string
		if err == nil {
			got, err = Equalities(e, "a", "b")
		}
		if tt.wantErr {
			if f, _ := violation(t, err); f != FilterField {
				t.Errorf("filter %q field got = %q, want = %q", tt.in, f, FilterField)
			}
			continue
		}
		if err != nil || len(got) != len(tt.want) {
			t.Errorf("filter %q got = %v, %v, want = %v", tt.in, got, err, tt.want)
			continue
		}
		for k, v := range tt.want {
			if !slices.Equal(got[k], v) {
				t.Errorf("filter %q %s got = %v, want = %v", tt.in, k, got[k], v)
			}
		}
	}
}

func TestFilterValues(t *testing.T) {
	desc := catalogv1.AggregationType(0).Descriptor()
	enums := []struct {
		in      []string
		want    []catalogv1.AggregationType
		wantErr bool
	}{
		{in: nil, want: []catalogv1.AggregationType{}},
		{in: []string{"sum", "AGGREGATION_TYPE_MAX", "unique_count"}, want: []catalogv1.AggregationType{
			catalogv1.AggregationType_AGGREGATION_TYPE_SUM, catalogv1.AggregationType_AGGREGATION_TYPE_MAX, catalogv1.AggregationType_AGGREGATION_TYPE_UNIQUE_COUNT,
		}},
		{in: []string{"unspecified"}, wantErr: true},
		{in: []string{"median"}, wantErr: true},
	}
	for _, tt := range enums {
		got, err := FilterEnum[catalogv1.AggregationType]("aggregation_type", tt.in, desc)
		if tt.wantErr != (err != nil) || !slices.Equal(got, tt.want) && !tt.wantErr {
			t.Errorf("FilterEnum(%v) got = %v, %v, want = %v (error %v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
	bools := []struct {
		in      []string
		want    *bool
		wantErr bool
	}{
		{in: nil},
		{in: []string{"true"}, want: new(true)},
		{in: []string{"false"}, want: new(false)},
		{in: []string{"1"}, wantErr: true},
		{in: []string{"true", "false"}, wantErr: true},
	}
	for _, tt := range bools {
		got, err := FilterBool("recurring", tt.in)
		if tt.wantErr != (err != nil) || (got == nil) != (tt.want == nil) || got != nil && *got != *tt.want {
			t.Errorf("FilterBool(%v) got = %v, %v, want = %v (error %v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
	if got, err := FilterSingle("plan_id", []string{"a"}); err != nil || got != "a" {
		t.Errorf("FilterSingle(a) got = %q, %v, want = a", got, err)
	}
	if _, err := FilterSingle("plan_id", []string{"a", "b"}); err == nil {
		t.Error("FilterSingle(a, b) got = nil, want an error")
	}
}

func TestUpdatePaths(t *testing.T) {
	allowed := []string{"code", "name", "filters"}
	set := func(p string) bool { return p != "filters" }
	tests := []struct {
		name    string
		mask    []string
		want    []string
		wantErr bool
	}{
		{name: "no mask: the fields set", want: []string{"code", "name"}},
		{name: "mask", mask: []string{"filters"}, want: []string{"filters"}},
		{name: "wildcard", mask: []string{"*"}, want: allowed},
		{name: "unknown", mask: []string{"name", "id"}, wantErr: true},
	}
	for _, tt := range tests {
		var mask *fieldmaskpb.FieldMask
		if tt.mask != nil {
			mask = &fieldmaskpb.FieldMask{Paths: tt.mask}
		}
		got, err := UpdatePaths(mask, allowed, set)
		if tt.wantErr {
			if f, _ := violation(t, err); f != "update_mask" {
				t.Errorf("%s: field got = %q, want = update_mask", tt.name, f)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tt.want) {
			t.Errorf("%s: got = %v, %v, want = %v", tt.name, got, err, tt.want)
		}
	}
}

func TestConvert(t *testing.T) {
	ts := time.Date(2025, 3, 15, 12, 0, 0, 0, time.FixedZone("x", 3600))
	if got := Time(Timestamp(ts)); !got.Equal(ts) || got.Location() != time.UTC {
		t.Errorf("Time(Timestamp) got = %v, want = %v in UTC", got, ts)
	}
	if Timestamp(time.Time{}) != nil || !Time(nil).IsZero() || TimePtr(nil) != nil || TimestampPtr(nil) != nil {
		t.Error("zero times got = set, want = nil or zero")
	}
	if got := Opt(false, "x"); got != nil {
		t.Errorf("Opt(false) got = %v, want = nil", *got)
	}
	if got := OptInt(true, 3); got == nil || *got != 3 {
		t.Errorf("OptInt(true, 3) got = %v, want = 3", got)
	}

	if _, err := FromDate("d", &date.Date{Year: 2025, Month: 2, Day: 30}); err == nil {
		t.Error("FromDate(2025-02-30) got = nil, want an error")
	}
	if got, err := FromDate("d", Date(core.NewDate(2025, 2, 28))); err != nil || got != core.NewDate(2025, 2, 28) {
		t.Errorf("FromDate(Date) got = %v, %v, want = 2025-02-28", got, err)
	}

	usd, err := money.LookupCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	if got := Money(1250, usd); got.GetAmount() != "12.50" || got.GetCurrency() != "USD" {
		t.Errorf("Money(1250 USD) got = %s %s, want = 12.50 USD", got.GetAmount(), got.GetCurrency())
	}
	moneys := []struct {
		currency, amount string
		minor            int64
		field            string
	}{
		{currency: "USD", amount: "12.5", minor: 1250},
		{currency: "JPY", amount: "100", minor: 100},
		{currency: "USD", amount: "1.005", field: "price.amount"},
		{currency: "USD", amount: "x", field: "price.amount"},
		{currency: "XXX1", amount: "1", field: "price.currency"},
	}
	for _, tt := range moneys {
		minor, _, err := FromMoney("price", commonv1.Money_builder{Currency: new(tt.currency), Amount: new(tt.amount)}.Build())
		if tt.field != "" {
			if f, _ := violation(t, err); f != tt.field {
				t.Errorf("FromMoney(%s %s) field got = %q, want = %q", tt.amount, tt.currency, f, tt.field)
			}
			continue
		}
		if err != nil || minor != tt.minor {
			t.Errorf("FromMoney(%s %s) got = %d, %v, want = %d", tt.amount, tt.currency, minor, err, tt.minor)
		}
	}

	e := NewEnum(map[string]catalogv1.AggregationType{"sum_agg": catalogv1.AggregationType_AGGREGATION_TYPE_SUM})
	if e.Proto("sum_agg") != catalogv1.AggregationType_AGGREGATION_TYPE_SUM || e.Core(catalogv1.AggregationType_AGGREGATION_TYPE_SUM) != "sum_agg" {
		t.Error("Enum round trip got = a mismatch, want = sum_agg")
	}
	if e.Proto("max_agg") != 0 || e.Core(catalogv1.AggregationType_AGGREGATION_TYPE_MAX) != "" {
		t.Error("Enum of an unmapped value got = set, want = zero")
	}
}

func TestPage(t *testing.T) {
	p := core.Page[int]{Items: []int{1, 2}, NextToken: "n"}
	if got := Items(p, func(i int) int { return i * 10 }); !slices.Equal(got, []int{10, 20}) {
		t.Errorf("Items got = %v, want = [10 20]", got)
	}
	if got := Page(5, "t"); got.Size != 5 || got.Token != "t" {
		t.Errorf("Page got = %+v, want = {5 t}", got)
	}
	if got := Error(context.Background(), errors.Join(core.ErrInvalidValue)); connect.CodeOf(got) != connect.CodeInvalidArgument {
		t.Errorf("Error(invalid page token) got = %v, want = InvalidArgument", got)
	}
}
