package documents

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/documents/internal/i18n"
)

// ErrUnsupportedFundingType reports funding instructions of a bank
// transfer type the formatter does not print (lago unsupported_funding_type).
var ErrUnsupportedFundingType = errors.New("documents: unsupported funding type")

// FundingFormatter prints a provider's bank transfer funding instructions
// as the text of an invoice custom section, in a document locale. It is
// safe for concurrent use.
type FundingFormatter struct {
	cat *i18n.Catalog
}

// NewFundingFormatter loads the translations of the formatter.
func NewFundingFormatter() (*FundingFormatter, error) {
	cat, err := i18n.Load()
	if err != nil {
		return nil, fmt.Errorf("documents: %w", err)
	}
	return &FundingFormatter{cat: cat}, nil
}

// fundingAddress is a financial address of Stripe's funding instructions,
// keyed by its type.
type fundingAddress map[string]json.RawMessage

// fundingDetails are the fields of a financial address printed.
type fundingDetails struct {
	AccountHolderName string `json:"account_holder_name"`
	AccountNumber     string `json:"account_number"`
	AccountType       string `json:"account_type"`
	BankCode          string `json:"bank_code"`
	BankName          string `json:"bank_name"`
	BIC               string `json:"bic"`
	BranchCode        string `json:"branch_code"`
	BranchName        string `json:"branch_name"`
	Clabe             string `json:"clabe"`
	Country           string `json:"country"`
	IBAN              string `json:"iban"`
	RoutingNumber     string `json:"routing_number"`
	SortCode          string `json:"sort_code"`
	SwiftCode         string `json:"swift_code"`
}

// details returns the fields under the first of keys present in a.
func (a fundingAddress) details(keys ...string) fundingDetails {
	var d fundingDetails
	for _, k := range keys {
		if raw, ok := a[k]; ok {
			_ = json.Unmarshal(raw, &d)
			return d
		}
	}
	return d
}

// FormatFundingInstructions returns the printed title and details of the
// funding instructions raw: Stripe's funding instructions object, or its
// bank_transfer hash. The details open with the bank transfer notice and
// list the bank details of the transfer type; a missing value prints "-".
//
// Stripe names the Mexican and Japanese addresses spei and zengin; lago
// reads mx_bank_transfer and jp_bank_transfer, which Stripe never sends,
// so it prints only dashes for them (parity §4). Both keys are read here.
//
// source: app/services/invoice_custom_sections/funding_instructions_formatter_service.rb:13-37
func (f *FundingFormatter) FormatFundingInstructions(raw json.RawMessage, locale string) (displayName, details string, err error) {
	var obj struct {
		BankTransfer json.RawMessage `json:"bank_transfer"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", "", fmt.Errorf("documents: funding instructions: %w", err)
	}
	bt := raw
	if len(obj.BankTransfer) > 0 && string(obj.BankTransfer) != "null" {
		bt = obj.BankTransfer
	}
	var data struct {
		Type               string           `json:"type"`
		FinancialAddresses []fundingAddress `json:"financial_addresses"`
	}
	if err := json.Unmarshal(bt, &data); err != nil {
		return "", "", fmt.Errorf("documents: funding instructions: %w", err)
	}
	l := f.cat.Locale(locale)
	t := func(key string) string { return l.T("invoice." + key) }
	first := fundingAddress{}
	if len(data.FinancialAddresses) > 0 && data.FinancialAddresses[0] != nil {
		first = data.FinancialAddresses[0]
	}
	lines := []string{t("bank_transfer_info"), ""}
	switch data.Type {
	case "us_bank_transfer":
		for _, a := range data.FinancialAddresses {
			var typ string
			_ = json.Unmarshal(a["type"], &typ)
			d := a.details(typ)
			switch typ {
			case "aba":
				lines = append(lines, fundingBlock("US ACH, Domestic Wire",
					t("bank_name")+": "+fundingDash(d.BankName), t("account_number")+": "+fundingDash(d.AccountNumber), t("routing_number")+": "+fundingDash(d.RoutingNumber)), "")
			case "swift":
				lines = append(lines, fundingBlock("SWIFT",
					t("bank_name")+": "+fundingDash(d.BankName), t("account_number")+": "+fundingDash(d.AccountNumber), t("swift_code")+": "+fundingDash(d.SwiftCode)), "")
			}
		}
	case "mx_bank_transfer":
		d := first.details("mx_bank_transfer", "spei")
		lines = append(lines, fundingBlock(t("clabe")+": "+fundingDash(d.Clabe), t("bank_name")+": "+fundingDash(d.BankName), t("bank_code")+": "+fundingDash(d.BankCode)))
	case "jp_bank_transfer":
		d := first.details("jp_bank_transfer", "zengin")
		lines = append(lines, fundingBlock(t("bank_code")+": "+fundingDash(d.BankCode), t("bank_name")+": "+fundingDash(d.BankName),
			t("branch_code")+": "+fundingDash(d.BranchCode), t("branch_name")+": "+fundingDash(d.BranchName), t("account_type")+": "+fundingDash(d.AccountType),
			t("account_number")+": "+fundingDash(d.AccountNumber), t("account_holder_name")+": "+fundingDash(d.AccountHolderName)))
	case "gb_bank_transfer":
		d := first.details("sort_code")
		lines = append(lines, fundingBlock(t("account_number")+": "+fundingDash(d.AccountNumber), t("sort_code")+": "+fundingDash(d.SortCode),
			t("account_holder_name")+": "+fundingDash(d.AccountHolderName)))
	case "eu_bank_transfer":
		d := first.details("iban")
		lines = append(lines, fundingBlock(t("bic")+": "+fundingDash(d.BIC), t("iban")+": "+fundingDash(d.IBAN), t("country")+": "+fundingDash(d.Country),
			t("account_holder_name")+": "+fundingDash(d.AccountHolderName)))
	default:
		return "", "", fmt.Errorf("%w: %q", ErrUnsupportedFundingType, data.Type)
	}
	return t("pay_with_bank_transfer"), strings.Join(lines, "\n"), nil
}

// fundingBlock joins lines into one block of text.
func fundingBlock(lines ...string) string { return strings.Join(lines, "\n") }

// fundingDash returns v, or "-" when it is blank (lago details_or_default).
func fundingDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}
