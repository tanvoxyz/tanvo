package rpc

import (
	"encoding/json"
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/tanvoxyz/tanvo/libs/core/invoice"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// TestPresentationBreakdowns covers the API form of presentation
// breakdowns: a missing value is a JSON null, as lago's fee serializer
// renders it.
//
// source: spec/serializers/v1/fee_serializer_spec.rb:37
func TestPresentationBreakdowns(t *testing.T) {
	t.Parallel()
	eng := "engineering"
	got := PresentationBreakdowns([]invoice.PresentationBreakdown{
		{PresentationBy: map[string]*string{"department": &eng, "team": nil}, Units: money.MustParse("12.5")},
	})
	if len(got) != 1 {
		t.Fatalf("got = %d breakdowns, want = 1", len(got))
	}
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var have, want any
	if err := json.Unmarshal(b, &have); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(`{"presentation_by":{"department":"engineering","team":null},"units":"12.5"}`), &want)
	if !reflect.DeepEqual(have, want) {
		t.Errorf("got = %s, want = department engineering, team null, units 12.5", b)
	}
	if PresentationBreakdowns(nil) == nil {
		t.Errorf("got = nil, want = empty list")
	}
}
