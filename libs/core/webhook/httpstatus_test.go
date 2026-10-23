package webhook

import (
	"errors"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/core"
)

func TestParseHTTPStatusFilter(t *testing.T) {
	t.Parallel()
	code := func(n int) *int { return &n }
	tests := []struct {
		name, source string
		values       []string
		matches      []*Delivery
		misses       []*Delivery
		invalid      bool
	}{
		{
			name: "code", source: "spec/queries/webhooks_query_spec.rb:136", values: []string{"200"},
			matches: []*Delivery{{HTTPStatus: code(200)}},
			misses:  []*Delivery{{HTTPStatus: code(201)}, {Status: StatusFailed}},
		},
		{
			name: "class", source: "spec/queries/webhooks_query_spec.rb:146", values: []string{"5XX"},
			matches: []*Delivery{{HTTPStatus: code(500)}, {HTTPStatus: code(599)}},
			misses:  []*Delivery{{HTTPStatus: code(499)}, {HTTPStatus: code(600)}},
		},
		{
			name: "range", source: "spec/queries/webhooks_query_spec.rb:156", values: []string{"200 - 205"},
			matches: []*Delivery{{HTTPStatus: code(200)}, {HTTPStatus: code(205)}},
			misses:  []*Delivery{{HTTPStatus: code(206)}},
		},
		{
			name: "timeout", source: "spec/queries/webhooks_query_spec.rb:167", values: []string{"timeout"},
			matches: []*Delivery{{Status: StatusFailed}},
			misses:  []*Delivery{{Status: StatusPending}, {Status: StatusRetrying}, {Status: StatusFailed, HTTPStatus: code(500)}},
		},
		{
			name: "several", source: "app/queries/webhooks_query.rb:62-76", values: []string{"404", "timeout"},
			matches: []*Delivery{{HTTPStatus: code(404)}, {Status: StatusFailed}},
			misses:  []*Delivery{{HTTPStatus: code(500)}},
		},
		{
			name: "none", values: nil,
			matches: []*Delivery{{HTTPStatus: code(500)}, {Status: StatusPending}},
		},
		{name: "invalid", source: "spec/queries/webhooks_query_spec.rb:178", values: []string{"invalid_status"}, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f, err := ParseHTTPStatusFilter(tt.values)
			if tt.invalid {
				if !errors.Is(err, core.ErrInvalidValue) {
					t.Errorf("ParseHTTPStatusFilter(%v) error got = %v, want = ErrInvalidValue", tt.values, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHTTPStatusFilter(%v) error got = %v, want = nil", tt.values, err)
			}
			for _, d := range tt.matches {
				if !f.Matches(d) {
					t.Errorf("Matches(%+v) got = false, want = true", d)
				}
			}
			for _, d := range tt.misses {
				if f.Matches(d) {
					t.Errorf("Matches(%+v) got = true, want = false", d)
				}
			}
		})
	}
}
