package invoice

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tanvoxyz/tanvo/libs/money"
)

func breakdown(units string, kv ...string) PresentationBreakdown {
	b := PresentationBreakdown{PresentationBy: map[string]*string{}, Units: money.MustParse(units)}
	for i := 0; i < len(kv); i += 2 {
		if kv[i+1] == "<nil>" {
			b.PresentationBy[kv[i]] = nil
			continue
		}
		v := kv[i+1]
		b.PresentationBy[kv[i]] = &v
	}
	return b
}

func renderBreakdowns(bs []PresentationBreakdown, keys []string) string {
	var out []string
	for _, b := range bs {
		var kv []string
		for _, k := range keys {
			if v, ok := b.PresentationBy[k]; ok {
				s := "<nil>"
				if v != nil {
					s = *v
				}
				kv = append(kv, k+"="+s)
			}
		}
		out = append(out, fmt.Sprintf("%s:%s", strings.Join(kv, ","), b.Units.Reduce()))
	}
	return strings.Join(out, ";")
}

func TestDisplayedPresentationBreakdowns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, source string
		keys         []string
		in           []PresentationBreakdown
		want         string
	}{
		{
			name: "breakdowns without a displayed key are left out", source: "spec/serializers/v1/fee_serializer_spec.rb:37",
			keys: []string{"department"},
			in:   []PresentationBreakdown{breakdown("60", "department", "engineering"), breakdown("60", "region", "us")},
			want: "department=engineering:60",
		},
		{
			name: "only the displayed keys", source: "spec/serializers/v1/customers/presentation_breakdown_builder_spec.rb:105",
			keys: []string{"cloud"},
			in:   []PresentationBreakdown{breakdown("1", "cloud", "aws"), breakdown("2", "region", "us")},
			want: "cloud=aws:1",
		},
		{
			name: "no displayed key", keys: nil,
			in: []PresentationBreakdown{breakdown("1", "cloud", "aws")},
		},
		{
			name: "rows with the same displayed values are summed in first order", keys: []string{"department"},
			in: []PresentationBreakdown{
				breakdown("1", "department", "sales", "region", "eu"), breakdown("2", "department", "eng", "region", "eu"),
				breakdown("3", "department", "sales", "region", "us"), breakdown("4", "department", "<nil>"),
			},
			want: "department=sales:4;department=eng:2;department=<nil>:4",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := renderBreakdowns(DisplayedPresentationBreakdowns(tc.in, tc.keys), tc.keys); got != tc.want {
				t.Errorf("%s: got = %q, want = %q", tc.source, got, tc.want)
			}
		})
	}
}
