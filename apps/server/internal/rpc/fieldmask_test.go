package rpc_test

import (
	"net/http"
	"testing"

	"github.com/tanvoxyz/tanvo/apps/server/internal/rpctest"
)

// TestRESTFieldMasks checks that the REST update_mask takes the proto
// field names, as the JSON does, and the lowerCamelCase JSON names.
func TestRESTFieldMasks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, query string
		// wantName is whether the name is updated with the short name.
		wantName bool
	}{
		{"proto name", "?update_mask=short_name", false},
		{"JSON name", "?update_mask=shortName", false},
		{"parameter JSON name", "?updateMask=short_name", false},
		{"several paths", "?update_mask=name,short_name", true},
		{"escaped", "?update_mask=name%2Cshort%5Fname", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := rpctest.New(t)
			u := createPricingUnit(t, s, "cloud_token", "Cloud token")
			status, body := s.REST(t, s.APIKey, http.MethodPatch, "/v1/pricing_units/"+u.ID+tc.query, `{"name":"Renamed","short_name":"CR"}`)
			if status != http.StatusOK {
				t.Fatalf("got status = %d (%s), want = 200", status, body)
			}
			got := decode[restPricingUnitBody](t, body).PricingUnit
			wantName := "Cloud token"
			if tc.wantName {
				wantName = "Renamed"
			}
			if got.ShortName != "CR" || got.Name != wantName {
				t.Errorf("got = %s %s, want = %s CR", got.Name, got.ShortName, wantName)
			}
		})
	}
}
