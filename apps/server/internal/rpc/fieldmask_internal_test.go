package rpc

import "testing"

func TestCamelMask(t *testing.T) {
	for _, tc := range []struct{ mask, want string }{
		{"short_name", "shortName"},
		{"shortName", "shortName"},
		{"name,short_name", "name,shortName"},
		{"name, short_name", "name,shortName"},
		{"email_settings.invoice_finalized", "emailSettings.invoiceFinalized"},
		{"x402_agent_address", "x402AgentAddress"},
		{"", ""},
	} {
		if got := camelMask(tc.mask); got != tc.want {
			t.Errorf("camelMask(%q): got = %q, want = %q", tc.mask, got, tc.want)
		}
	}
}
