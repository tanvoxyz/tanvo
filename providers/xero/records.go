package xero

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
)

type object = map[string]any

// summarize asks Xero to answer each record's validation errors instead
// of failing the whole request.
var summarize = url.Values{"summarizeErrors": {"false"}}

// record is a created record in Xero's answer.
type record struct {
	ID                  string            `json:"-"`
	HasErrors           bool              `json:"HasErrors"`
	HasValidationErrors bool              `json:"HasValidationErrors"`
	ValidationErrors    []validationError `json:"ValidationErrors"`
	EmailAddress        string            `json:"EmailAddress"`
}

// created reads the first record of the collection key of a 200 answer
// with its ID field idKey; a refused record is a validation error (lago
// <resource>/create_service.rb process_hash_result).
func created(body []byte, key, idKey string) (record, error) {
	var raw map[string][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return record{}, fmt.Errorf("decode answer: %w", err)
	}
	for _, r := range raw[key] {
		var rec record
		if err := json.Unmarshal(r, &rec); err != nil {
			return record{}, fmt.Errorf("decode %s: %w", key, err)
		}
		if rec.HasErrors || rec.HasValidationErrors {
			return record{}, &integration.ProviderError{
				Provider: Provider, Kind: integration.KindInvalidRequest, Code: integration.CodeValidationError,
				Message: joinValidation(rec.ValidationErrors), HTTPStatus: http.StatusOK,
			}
		}
		var ids map[string]any
		_ = json.Unmarshal(r, &ids)
		if id, ok := ids[idKey].(string); ok && id != "" {
			rec.ID = id
			return rec, nil
		}
	}
	return record{}, &integration.ProviderError{
		Provider: Provider, Kind: integration.KindInvalidRequest, Code: integration.CodeValidationError, HTTPStatus: http.StatusOK,
	}
}

// day is the date part Xero takes ("2024-07-08").
func day(d core.Date) string { return d.String() }

// num is a decimal as a JSON number.
func num(d money.Decimal) json.Number { return json.Number(d.Reduce().String()) }

// major is minor units of c as a JSON number of major units.
func major(c money.Currency, cents int64) json.Number {
	return num(integration.Major(c, money.NewFromInt(cents)))
}

// line is one item of a lago invoice or credit note payload (base
// payload "fees").
type line struct {
	itemCode    string
	description string
	// units and unitAmount are nil when lago sends a falsy value, which
	// the broker leaves out.
	units, unitAmount *money.Decimal
	accountCode       *string
	// taxesCents is the line's taxes in minor units, possibly fractional
	// for credit notes.
	taxesCents money.Decimal
}

// lineItem maps a line as the broker's mappers do (to-invoice.ts and
// create-credit-note.ts): falsy quantities, amounts and taxes are left
// out; the account code is always sent.
func lineItem(c money.Currency, l line) object {
	o := object{"Description": l.description, "AccountCode": nil}
	if l.accountCode != nil {
		o["AccountCode"] = *l.accountCode
	}
	if l.itemCode != "" {
		o["ItemCode"] = l.itemCode
	}
	if l.units != nil && !l.units.IsZero() {
		o["Quantity"] = num(*l.units)
	}
	if l.unitAmount != nil && !l.unitAmount.IsZero() {
		o["UnitAmount"] = num(*l.unitAmount)
	}
	if !l.taxesCents.IsZero() {
		o["TaxAmount"] = num(c.MinorToUnits(l.taxesCents))
	}
	return o
}

func dec(d money.Decimal) *money.Decimal { return &d }

var one = money.NewFromInt(1)

// SyncInvoice implements [integration.AccountingGateway]: POST Invoices
// with the lago Xero invoice payload (invoices/payloads/xero.rb).
func (g *Gateway) SyncInvoice(ctx context.Context, conn integration.Connection, inv *integration.AccountingInvoice) (string, error) {
	body, err := invoiceBody(inv)
	if err != nil {
		return "", err
	}
	resp, err := g.call(ctx, conn, "create_invoice", http.MethodPost, "Invoices", summarize, object{"Invoices": []object{body}})
	if err != nil {
		return "", err
	}
	rec, err := created(resp.Body, "Invoices", "InvoiceID")
	if err != nil {
		return "", fmt.Errorf("xero create_invoice: %w", err)
	}
	return rec.ID, nil
}

// invoiceBody is the Xero invoice of inv.
func invoiceBody(inv *integration.AccountingInvoice) (object, error) {
	lines, err := invoiceLines(inv)
	if err != nil {
		return nil, err
	}
	items := make([]object, 0, len(lines))
	for _, l := range lines {
		items = append(items, lineItem(inv.Currency, l))
	}
	o := object{
		"Type": "ACCREC", "Contact": object{"ContactID": inv.ExternalCustomerID},
		"InvoiceNumber": inv.Number, "Status": "AUTHORISED", "CurrencyCode": inv.Currency.Code(),
		"Date": day(inv.IssuingDate), "DueDate": day(inv.PaymentDueDate), "LineItems": items,
	}
	if inv.PurchaseOrderNumber != "" {
		o["Reference"] = inv.PurchaseOrderNumber
	}
	return o, nil
}

// invoiceLines are lago's tax_adjusted_fee_items + discounts of the Xero
// payload (invoices/payloads/base_payload.rb:50-155, xero.rb:18-45).
func invoiceLines(inv *integration.AccountingInvoice) ([]line, error) {
	c := inv.Currency
	var (
		lines []line
		sum   int64
	)
	for _, f := range inv.Fees {
		if f.Item == nil {
			return nil, fmt.Errorf("fee %s: %w", f.ID, integration.ErrNoMapping)
		}
		desc := f.InvoiceName
		switch {
		case f.Type == core.FeeTypeSubscription:
			desc = "Subscription"
		case f.FilterDisplayName != "":
			desc = f.FilterDisplayName
		}
		l := line{
			itemCode: f.Item.ExternalID, description: desc + f.GroupedByDisplay,
			units: dec(f.Units), unitAmount: dec(f.PreciseUnitAmount), accountCode: f.Item.ExternalAccountCode,
			taxesCents: money.NewFromInt(f.TaxesAmountCents),
		}
		if !f.PreciseUnitAmount.Round(2, money.HalfUp).Equal(f.PreciseUnitAmount) {
			l.units, l.unitAmount = dec(one), dec(integration.Major(c, money.NewFromInt(f.AmountCents)))
		}
		lines = append(lines, l)
		sum += f.TaxesAmountCents
	}
	// The rounding remainder of the taxes goes to the first fee whose
	// taxes exceed it, without coupons.
	if remaining := inv.TaxesAmountCents - sum; remaining != 0 && inv.CouponsAmountCents == 0 {
		abs := money.NewFromInt(max(remaining, -remaining))
		for i := range lines {
			if lines[i].taxesCents.GreaterThan(abs) {
				lines[i].taxesCents = lines[i].taxesCents.Add(money.NewFromInt(remaining))
				break
			}
		}
	}
	discount := func(it *integration.ExternalItem, desc string, cents, taxes int64) line {
		return line{
			itemCode: it.ExternalID, description: desc, units: dec(one),
			unitAmount: dec(integration.Major(c, money.NewFromInt(-cents))), accountCode: it.ExternalAccountCode,
			taxesCents: money.NewFromInt(taxes),
		}
	}
	items := inv.Items
	if inv.CouponsAmountCents > 0 {
		if items.Coupon == nil {
			return nil, fmt.Errorf("coupons: %w", integration.ErrNoMapping)
		}
		diff := inv.TaxesAmountCents - sum
		lines = append(lines, discount(items.Coupon, "Coupons", inv.CouponsAmountCents, -max(diff, -diff)))
	}
	if items.PrepaidCredit != nil && inv.PrepaidCreditAmountCents > 0 {
		lines = append(lines, discount(items.PrepaidCredit, "Prepaid credit", inv.PrepaidCreditAmountCents, 0))
	}
	if items.PrepaidCredit != nil && inv.ProgressiveBillingCreditAmountCents > 0 {
		lines = append(lines, discount(items.PrepaidCredit, "Usage already billed", inv.ProgressiveBillingCreditAmountCents, 0))
	}
	if items.CreditNote != nil && inv.CreditNotesAmountCents > 0 {
		lines = append(lines, discount(items.CreditNote, "Credit note", inv.CreditNotesAmountCents, 0))
	}
	return lines, nil
}

// SyncCreditNote implements [integration.AccountingGateway]: POST
// CreditNotes with the lago Xero credit note payload
// (credit_notes/payloads/xero.rb).
func (g *Gateway) SyncCreditNote(ctx context.Context, conn integration.Connection, cn *integration.AccountingCreditNote) (string, error) {
	body, err := creditNoteBody(cn)
	if err != nil {
		return "", err
	}
	resp, err := g.call(ctx, conn, "create_credit_note", http.MethodPost, "CreditNotes", summarize, object{"CreditNotes": []object{body}})
	if err != nil {
		return "", err
	}
	rec, err := created(resp.Body, "CreditNotes", "CreditNoteID")
	if err != nil {
		return "", fmt.Errorf("xero create_credit_note: %w", err)
	}
	return rec.ID, nil
}

// creditNoteBody is the Xero credit note of cn
// (credit_notes/payloads/base_payload.rb:15-112).
func creditNoteBody(cn *integration.AccountingCreditNote) (object, error) {
	c := cn.Currency
	var (
		lines []line
		sum   = money.Zero
	)
	for _, it := range cn.Items {
		f := it.Fee
		if f.Item == nil {
			return nil, fmt.Errorf("credit note item %s: %w", it.ID, integration.ErrNoMapping)
		}
		desc := f.InvoiceName
		if f.Type == core.FeeTypeSubscription {
			desc = "Subscription"
		}
		units := money.Zero
		if it.AmountCents > 0 {
			units = one
		}
		// amount(amount_cents × taxes_rate): minor units of the tax.
		taxes := integration.Major(c, money.NewFromInt(it.AmountCents).Mul(cn.TaxesRate))
		lines = append(lines, line{
			itemCode: f.Item.ExternalID, description: desc, units: dec(units),
			unitAmount: dec(integration.Major(c, money.NewFromInt(it.AmountCents))), accountCode: f.Item.ExternalAccountCode,
			taxesCents: taxes,
		})
		sum = sum.Add(taxes)
	}
	if total := money.NewFromInt(cn.TaxesAmountCents); !sum.Equal(total) {
		for i := range lines {
			if lines[i].taxesCents.IsPositive() {
				lines[i].taxesCents = lines[i].taxesCents.Add(total.Sub(sum))
				break
			}
		}
	}
	if cn.CouponsAdjustmentAmountCents > 0 {
		it := cn.Collections.Coupon
		if it == nil {
			return nil, fmt.Errorf("coupons: %w", integration.ErrNoMapping)
		}
		lines = append(lines, line{
			itemCode: it.ExternalID, description: "Coupons", units: dec(one),
			unitAmount: dec(integration.Major(c, money.NewFromInt(-cn.CouponsAdjustmentAmountCents))), accountCode: it.ExternalAccountCode,
		})
	}
	items := make([]object, 0, len(lines))
	for _, l := range lines {
		items = append(items, lineItem(c, l))
	}
	return object{
		"Type": "ACCRECCREDIT", "Contact": object{"ContactID": cn.ExternalCustomerID},
		"CreditNoteNumber": cn.Number, "Status": "AUTHORISED", "CurrencyCode": c.Code(),
		"Date": day(cn.IssuingDate), "LineItems": items,
	}, nil
}

// SyncPayment implements [integration.AccountingGateway]: PUT Payments
// with the lago payment payload (payments/payloads/base_payload.rb). The
// broker requires the account code; without the account mapping the
// payment is [integration.ErrNoMapping].
func (g *Gateway) SyncPayment(ctx context.Context, conn integration.Connection, p *integration.AccountingPayment) (string, error) {
	if p.Account == nil || p.Account.ExternalAccountCode == nil || *p.Account.ExternalAccountCode == "" {
		return "", fmt.Errorf("payment account: %w", integration.ErrNoMapping)
	}
	body := object{
		"Amount": major(p.Currency, p.AmountCents), "Account": object{"Code": *p.Account.ExternalAccountCode},
		"Date": p.CreatedAt.UTC().Format(time.DateOnly), "Invoice": object{"InvoiceID": p.InvoiceExternalID},
	}
	resp, err := g.call(ctx, conn, "create_payment", http.MethodPut, "Payments", summarize, object{"Payments": []object{body}})
	if err != nil {
		return "", err
	}
	rec, err := created(resp.Body, "Payments", "PaymentID")
	if err != nil {
		return "", fmt.Errorf("xero create_payment: %w", err)
	}
	return rec.ID, nil
}

// CreateContact implements [integration.ContactGateway]: POST Contacts
// with the lago contact payload (contacts/payloads/base_payload.rb).
func (g *Gateway) CreateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	return g.contact(ctx, conn, "create_contact", contactBody(c, ""))
}

// UpdateContact implements [integration.ContactGateway].
func (g *Gateway) UpdateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	return g.contact(ctx, conn, "update_contact", contactBody(c, c.LinkedID))
}

func (g *Gateway) contact(ctx context.Context, conn integration.Connection, op string, body object) (integration.ContactResult, error) {
	resp, err := g.call(ctx, conn, op, http.MethodPost, "Contacts", summarize, object{"Contacts": []object{body}})
	if err != nil {
		return integration.ContactResult{}, err
	}
	rec, err := created(resp.Body, "Contacts", "ContactID")
	if err != nil {
		return integration.ContactResult{}, fmt.Errorf("xero %s: %w", op, err)
	}
	return integration.ContactResult{ExternalID: rec.ID, Email: rec.EmailAddress}, nil
}

// contactBody maps the lago contact (name, city, zip, country, state,
// first email and phone) as the broker's to-contact mapper does: the
// address fields form a POBOX address, and the first and last names,
// when set, are the contact's FirstName and LastName (lago
// contacts/payloads/base_payload.rb:49-51).
func contactBody(c *integration.Contact, id string) object {
	o := object{}
	if id != "" {
		o["ContactID"] = id
	}
	if c.Name != "" {
		o["Name"] = c.Name
	}
	if c.Firstname != "" {
		o["FirstName"] = c.Firstname
	}
	if c.Lastname != "" {
		o["LastName"] = c.Lastname
	}
	if e := c.FirstEmail(); e != "" {
		o["EmailAddress"] = e
	}
	if p := c.FirstPhone(); p != "" {
		o["Phones"] = []object{{"PhoneType": "DEFAULT", "PhoneNumber": p}}
	}
	a := c.BillingAddress
	addr := object{"AddressType": "POBOX"}
	for k, v := range map[string]string{"City": a.City, "PostalCode": a.Zipcode, "Country": a.Country, "Region": a.State} {
		if v != "" {
			addr[k] = v
		}
	}
	if len(addr) > 1 {
		o["Addresses"] = []object{addr}
	}
	return o
}

// FetchItems implements [integration.AccountingGateway]: GET Items, each
// identified by its code, named by its name or code, with its sales
// account (the broker's items sync and lago items_service.rb:52-59).
func (g *Gateway) FetchItems(ctx context.Context, conn integration.Connection) ([]integration.ExternalRecord, error) {
	resp, err := g.call(ctx, conn, "fetch_items", http.MethodGet, "Items", nil, nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Items []struct {
			Code, Name   string
			SalesDetails *struct{ AccountCode string }
		}
	}
	if err := resp.Decode(&body); err != nil {
		return nil, fmt.Errorf("xero fetch_items: %w", err)
	}
	out := make([]integration.ExternalRecord, 0, len(body.Items))
	for _, it := range body.Items {
		r := integration.ExternalRecord{ID: it.Code, Name: it.Name}
		if r.Name == "" {
			r.Name = it.Code
		}
		if it.SalesDetails != nil {
			r.AccountCode = it.SalesDetails.AccountCode
		}
		out = append(out, r)
	}
	return out, nil
}

// FetchAccounts implements [integration.AccountingGateway]: GET Accounts,
// the active ones (the broker's accounts sync and lago
// accounts_service.rb:44-55).
func (g *Gateway) FetchAccounts(ctx context.Context, conn integration.Connection) ([]integration.ExternalRecord, error) {
	resp, err := g.call(ctx, conn, "fetch_accounts", http.MethodGet, "Accounts", url.Values{"order": {"UpdatedDateUTC DESC"}}, nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Accounts []struct{ AccountID, Code, Name, Status string }
	}
	if err := resp.Decode(&body); err != nil {
		return nil, fmt.Errorf("xero fetch_accounts: %w", err)
	}
	out := make([]integration.ExternalRecord, 0, len(body.Accounts))
	for _, a := range body.Accounts {
		if a.Status == "ACTIVE" {
			out = append(out, integration.ExternalRecord{ID: a.AccountID, Name: a.Name, AccountCode: a.Code})
		}
	}
	return out, nil
}
