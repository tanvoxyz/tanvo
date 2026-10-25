package documents

import (
	"errors"
	"strings"
	"testing"
)

const bankTransferInfo = "Bank transfers may take several business days to process. To pay via bank transfer, transfer funds using the following bank information."

// TestFormatFundingInstructions ports the funding instructions formatter
// spec: each transfer type opens with the bank transfer notice and prints
// its bank details.
func TestFormatFundingInstructions(t *testing.T) {
	t.Parallel()
	f, err := NewFundingFormatter()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		// source lines are on each case.
		data string
		want []string
	}{
		{
			// source: spec/services/invoice_custom_sections/funding_instructions_formatter_service_spec.rb:12
			// source: spec/services/invoice_custom_sections/funding_instructions_formatter_service_spec.rb:46
			name: "us_bank_transfer",
			data: `{"type":"us_bank_transfer","financial_addresses":[
				{"type":"aba","aba":{"account_holder_name":"Teste","account_number":"11119987600453127","bank_name":"US Test Bank","routing_number":"999999999"}},
				{"type":"swift","swift":{"account_holder_name":"Teste","account_number":"11119987600453127","bank_name":"US Test Bank","swift_code":"TESTUS99XXX"}}]}`,
			want: []string{
				"US ACH, Domestic Wire\nBank name: US Test Bank\nAccount number: 11119987600453127\nRouting number: 999999999",
				"SWIFT\nBank name: US Test Bank\nAccount number: 11119987600453127\nSWIFT code: TESTUS99XXX",
			},
		},
		{
			// source: spec/services/invoice_custom_sections/funding_instructions_formatter_service_spec.rb:84
			name: "eu_bank_transfer",
			data: `{"type":"eu_bank_transfer","financial_addresses":[{"type":"iban","iban":{"account_holder_name":"Teste","bic":"AGRIFRPPXXX","country":"FR","iban":"FR61284383901570478105144165"}}]}`,
			want: []string{"BIC: AGRIFRPPXXX", "IBAN: FR61284383901570478105144165", "Country: FR", "Account holder name: Teste"},
		},
		{
			// source: spec/services/invoice_custom_sections/funding_instructions_formatter_service_spec.rb:109
			name: "mx_bank_transfer",
			data: `{"type":"mx_bank_transfer","financial_addresses":[{"mx_bank_transfer":{"clabe":"002010077777777771","bank_name":"Banco MX","bank_code":"002"}}]}`,
			want: []string{"CLABE: 002010077777777771", "Bank name: Banco MX", "Bank code: 002"},
		},
		{
			// Stripe's own key for the Mexican address (parity §4).
			name: "mx_bank_transfer from spei",
			data: `{"type":"mx_bank_transfer","financial_addresses":[{"type":"spei","spei":{"clabe":"002010077777777771","bank_name":"Banco MX","bank_code":"002"}}]}`,
			want: []string{"CLABE: 002010077777777771", "Bank name: Banco MX", "Bank code: 002"},
		},
		{
			// source: spec/services/invoice_custom_sections/funding_instructions_formatter_service_spec.rb:137
			name: "jp_bank_transfer",
			data: `{"type":"jp_bank_transfer","financial_addresses":[{"jp_bank_transfer":{"bank_code":"0005","bank_name":"JP Test Bank","branch_code":"001","branch_name":"Tokyo","account_type":"type_test","account_number":"1234567","account_holder_name":"name_account"}}]}`,
			want: []string{
				"Bank code: 0005", "Bank name: JP Test Bank", "Branch code: 001", "Branch name: Tokyo",
				"Account type: type_test", "Account number: 1234567", "Account holder name: name_account",
			},
		},
		{
			// Stripe's own key for the Japanese address (parity §4).
			name: "jp_bank_transfer from zengin",
			data: `{"type":"jp_bank_transfer","financial_addresses":[{"type":"zengin","zengin":{"bank_code":"0005","bank_name":"JP Test Bank","branch_code":"001","branch_name":"Tokyo","account_type":"futsu","account_number":"1234567","account_holder_name":"name_account"}}]}`,
			want: []string{"Bank code: 0005", "Branch name: Tokyo", "Account type: futsu", "Account holder name: name_account"},
		},
		{
			// source: spec/services/invoice_custom_sections/funding_instructions_formatter_service_spec.rb:163
			name: "gb_bank_transfer",
			data: `{"type":"gb_bank_transfer","financial_addresses":[{"sort_code":{"account_number":"12345678","sort_code":"12-34-56","account_holder_name":"Test UK"}}]}`,
			want: []string{"Account number: 12345678", "Sort code: 12-34-56", "Account holder name: Test UK"},
		},
		{
			// Stripe's whole funding instructions object, with a value
			// missing.
			name: "funding instructions object",
			data: `{"object":"funding_instructions","bank_transfer":{"type":"gb_bank_transfer","financial_addresses":[{"type":"sort_code","sort_code":{"account_number":"12345678","sort_code":""}}]},"currency":"gbp"}`,
			want: []string{"Account number: 12345678\nSort code: -\nAccount holder name: -"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name, details, err := f.FormatFundingInstructions([]byte(tt.data), "en")
			if err != nil {
				t.Fatalf("FormatFundingInstructions() error got = %v, want = nil", err)
			}
			if name != "Pay with a bank transfer" {
				t.Errorf("display name got = %q, want = Pay with a bank transfer", name)
			}
			if !strings.HasPrefix(details, bankTransferInfo+"\n\n") {
				t.Errorf("details got = %q, want = starting with the bank transfer notice", details)
			}
			for _, w := range tt.want {
				if !strings.Contains(details, w) {
					t.Errorf("details got = %q, want = containing %q", details, w)
				}
			}
		})
	}

	t.Run("locale", func(t *testing.T) {
		t.Parallel()
		name, details, err := f.FormatFundingInstructions([]byte(`{"type":"eu_bank_transfer","financial_addresses":[{"type":"iban","iban":{"iban":"FR61"}}]}`), "fr")
		if err != nil || name == "Pay with a bank transfer" || strings.Contains(details, bankTransferInfo) || !strings.Contains(details, "IBAN") {
			t.Errorf("fr got = %q, %q (%v), want = French title and notice", name, details, err)
		}
	})
	t.Run("unsupported", func(t *testing.T) {
		t.Parallel()
		if _, _, err := f.FormatFundingInstructions([]byte(`{"type":"ca_bank_transfer"}`), "en"); !errors.Is(err, ErrUnsupportedFundingType) {
			t.Errorf("error got = %v, want = %v", err, ErrUnsupportedFundingType)
		}
	})
}
