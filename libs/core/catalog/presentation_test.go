package catalog

import (
	"slices"
	"testing"
)

func TestPresentationGroupKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, source, props string
		want, displayed     []string
	}{
		{name: "blank", source: "spec/models/charge_spec.rb:581", props: `{"amount_cents":"1000"}`},
		{name: "nil", source: "spec/models/charge_spec.rb:589", props: `{"amount_cents":"1000","presentation_group_keys":null}`},
		{name: "empty", source: "spec/models/charge_spec.rb:597", props: `{"amount_cents":"1000","presentation_group_keys":[]}`},
		{name: "one", source: "spec/models/charge_spec.rb:605", props: `{"presentation_group_keys":[{"value":"region"}]}`, want: []string{"region"}},
		{name: "several", source: "spec/models/charge_spec.rb:621", props: `{"presentation_group_keys":[{"value":"region"},{"value":"country"}]}`, want: []string{"region", "country"}},
		{name: "nil value", source: "spec/models/charge_spec.rb:637", props: `{"presentation_group_keys":[{"value":"region"},{"value":null}]}`, want: []string{"region"}},
		{name: "displayed, blank", source: "spec/models/charge_spec.rb:649", props: `{}`},
		{name: "displayed, empty", source: "spec/models/charge_spec.rb:657", props: `{"presentation_group_keys":[]}`},
		{
			name: "displayed", source: "spec/models/charge_spec.rb:672",
			props: `{"presentation_group_keys":[{"value":"region","options":{"display_in_invoice":true}}]}`,
			want:  []string{"region"}, displayed: []string{"region"},
		},
		{
			name: "not displayed", source: "spec/models/charge_spec.rb:687",
			props: `{"presentation_group_keys":[{"value":"region","options":{"display_in_invoice":false}}]}`,
			want:  []string{"region"},
		},
		{
			name: "some displayed", source: "spec/models/charge_spec.rb:703",
			props: `{"presentation_group_keys":[{"value":"region","options":{"display_in_invoice":true}},{"value":"country","options":{"display_in_invoice":false}}]}`,
			want:  []string{"region", "country"}, displayed: []string{"region"},
		},
		{
			name: "several displayed", source: "spec/models/charge_spec.rb:719",
			props: `{"presentation_group_keys":[{"value":"region","options":{"display_in_invoice":true}},{"value":"department_id","options":{"display_in_invoice":true}}]}`,
			want:  []string{"region", "department_id"}, displayed: []string{"region", "department_id"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := MustProperties(tc.props)
			if got := p.PresentationGroupKeys(); !slices.Equal(got, tc.want) {
				t.Errorf("%s: keys: got = %v, want = %v", tc.source, got, tc.want)
			}
			if got := p.PresentationGroupKeysDisplayedInInvoice(); !slices.Equal(got, tc.displayed) {
				t.Errorf("%s: displayed keys: got = %v, want = %v", tc.source, got, tc.displayed)
			}
		})
	}
}
