package netsuite

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Limits of the contact fields (lago contacts/payloads/netsuite.rb:8-10).
const (
	baseLimit  = 30
	addr1Limit = 150
	cityLimit  = 50
)

// Limits of the invoice line numbers (lago invoices/payloads/netsuite.rb:8-9).
const (
	maxDecimals   = 15
	quantityLimit = 10_000_000_000
)

type object = map[string]any

// CreateContact implements [integration.ContactGateway] (lago
// contacts/payloads/netsuite.rb#create_body).
func (g *Gateway) CreateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	columns := object{
		"isperson": isPerson(c), "subsidiary": nullable(c.SubsidiaryID), "custentity_lago_id": c.CustomerID.String(),
		"custentity_lago_sf_id": nullable(c.SalesforceID), "custentity_lago_customer_link": c.Link,
		"email": nullable(c.FirstEmail()), "phone": nullable(c.FirstPhone()), "entityid": c.ExternalID, "autoname": false,
	}
	names(columns, c)
	body := object{"type": "customer", "isDynamic": true, "columns": columns, "options": object{"ignoreMandatoryFields": false}}
	if !c.EmptyBillingAndShippingAddress() {
		body["lines"] = addressLines(c)
	}
	id, err := g.restlet(ctx, conn, "createContact", http.MethodPost, body)
	return integration.ContactResult{ExternalID: id}, err
}

// UpdateContact implements [integration.ContactGateway] (lago
// contacts/payloads/netsuite.rb#update_body).
func (g *Gateway) UpdateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	columns := object{
		"isperson": isPerson(c), "subsidiary": nullable(c.SubsidiaryID),
		"custentity_lago_sf_id": nullable(c.SalesforceID), "custentity_lago_customer_link": c.Link,
		"email": nullable(c.FirstEmail()), "phone": nullable(c.FirstPhone()), "entityid": c.ExternalID, "autoname": false,
	}
	names(columns, c)
	body := object{"type": "customer", "recordId": c.LinkedID, "columns": columns, "options": object{"isDynamic": false}}
	if _, err := g.restlet(ctx, conn, "updateContact", http.MethodPut, body); err != nil {
		return integration.ContactResult{}, err
	}
	return integration.ContactResult{ExternalID: c.LinkedID}, nil
}

func isPerson(c *integration.Contact) string {
	if c.Individual {
		return "T"
	}
	return "F"
}

// names sets the company name, or for an individual the first and last
// names cut to 30 characters and the name when present.
func names(columns object, c *integration.Contact) {
	if !c.Individual {
		columns["companyname"] = nullable(c.Name)
		return
	}
	columns["firstname"] = nullable(first(c.Firstname, baseLimit))
	columns["lastname"] = nullable(first(c.Lastname, baseLimit))
	if c.Name != "" {
		columns["companyname"] = c.Name
	}
}

func addressLines(c *integration.Contact) []object {
	sub := func(a core.Address) object {
		return object{
			"addr1": nullable(first(a.Line1, addr1Limit)), "addr2": nullable(a.Line2), "city": nullable(first(a.City, cityLimit)),
			"zip": nullable(a.Zipcode), "state": nullable(first(a.State, baseLimit)), "country": nullable(a.Country),
		}
	}
	var items []object
	if c.SameBillingAndShippingAddress() {
		items = []object{{"defaultshipping": true, "defaultbilling": true, "subObjectId": "addressbookaddress", "subObject": sub(c.BillingAddress)}}
	} else {
		items = []object{
			{"defaultshipping": false, "defaultbilling": true, "subObjectId": "addressbookaddress", "subObject": sub(c.BillingAddress)},
			{"defaultshipping": true, "defaultbilling": false, "subObjectId": "addressbookaddress", "subObject": sub(c.ShippingAddress)},
		}
	}
	return []object{{"lineItems": items, "sublistId": "addressbook"}}
}

// first is Ruby's String#first(n).
func first(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// nullable is nil for an empty string, as lago sends absent attributes.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// date is the NetSuite date format of lago (strftime("%-m/%-d/%Y")).
func date(d core.Date) any {
	if d.IsZero() {
		return nil
	}
	return fmt.Sprintf("%d/%d/%d", d.Month, d.Day, d.Year)
}

func instantDate(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	t = t.UTC()
	return fmt.Sprintf("%d/%d/%d", int(t.Month()), t.Day(), t.Year())
}

// major is lago BasePayload#amount: minor units in major units, as a
// JSON number.
func major(c money.Currency, cents money.Decimal) json.Number {
	return number(integration.Major(c, cents))
}

func number(d money.Decimal) json.Number {
	s := d.Reduce().String()
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return json.Number(s)
}

// limitedRate is lago's limited_rate on a BigDecimal, whose to_s is the
// scientific notation ("0.412e1"): a value whose notation is longer than
// 15 characters is rounded to 15 - 1 - (position of the point) decimals.
func limitedRate(d money.Decimal) money.Decimal {
	d = d.Reduce()
	intPart, frac, _ := strings.Cut(d.Abs().String(), ".")
	digits := strings.TrimRight(strings.TrimLeft(intPart+frac, "0"), "0")
	if digits == "" {
		return d
	}
	exp := len(intPart)
	if intPart == "0" {
		exp = -(len(frac) - len(strings.TrimLeft(frac, "0")))
	}
	sci, pos := "0."+digits+"e"+strconv.Itoa(exp), 1
	if d.IsNegative() {
		sci, pos = "-"+sci, 2
	}
	if len(sci) <= maxDecimals {
		return d
	}
	return d.Round(int32(maxDecimals-1-pos), money.HalfUp)
}

func item(it *integration.ExternalItem) (any, any) {
	if it == nil {
		return nil, nil
	}
	var account any
	if it.ExternalAccountCode != nil {
		account = *it.ExternalAccountCode
	}
	return it.ExternalID, account
}

// SyncInvoice implements [integration.AccountingGateway] (lago
// invoices/payloads/netsuite.rb#body).
func (g *Gateway) SyncInvoice(ctx context.Context, conn integration.Connection, inv *integration.AccountingInvoice) (string, error) {
	body, err := invoiceBody(inv)
	if err != nil {
		return "", err
	}
	return g.restlet(ctx, conn, "createInvoice", http.MethodPost, body)
}

func invoiceBody(inv *integration.AccountingInvoice) (object, error) {
	cur := inv.Currency
	amount := func(cents int64) json.Number { return major(cur, money.NewFromInt(cents)) }
	fees := positiveFees(inv.Fees)
	columns := object{
		"tranid": inv.Number, "custbody_ava_disable_tax_calculation": true, "custbody_lago_invoice_link": inv.URL,
		"trandate": date(inv.IssuingDate), "duedate": date(inv.PaymentDueDate), "taxdetailsoverride": true,
		"custbody_lago_id": inv.ID.String(), "entity": inv.ExternalCustomerID, "lago_plan_codes": strings.Join(inv.PlanCodes, ","),
	}
	if c, ok := inv.Items.Currencies[cur.Code()]; ok && c != "" {
		columns["currency"] = c
	}
	tax := inv.Items.Tax
	if tax != nil && tax.Nexus != "" {
		columns["nexus"] = tax.Nexus
	}
	columns["taxregoverride"] = true

	var lines []object
	for _, f := range fees {
		if f.Item == nil {
			return nil, fmt.Errorf("netsuite: fee %s: %w", f.ID, integration.ErrNoMapping)
		}
		id, account := item(f.Item)
		quantity, rate, line := limitedRate(f.Units), limitedRate(f.PreciseUnitAmount), amount(f.AmountCents)
		var q, r any = quantity.String(), rate.String()
		if quantity.Abs().GreaterThanOrEqual(money.NewFromInt(quantityLimit)) {
			q, r = 1, line
		}
		lines = append(lines, object{
			"item": id, "account": account, "quantity": q, "rate": r, "amount": line,
			"taxdetailsreference":              f.ID.String(),
			"custcol_service_period_date_from": instantDate(f.From), "custcol_service_period_date_to": instantDate(f.To),
			"description": f.ItemName, "item_source": f.ItemSource,
		})
	}
	discount := func(it *integration.ExternalItem, cents int64, ref, desc, source string) error {
		if it == nil {
			return fmt.Errorf("netsuite: %s: %w", ref, integration.ErrNoMapping)
		}
		id, account := item(it)
		lines = append(lines, object{
			"item": id, "account": account, "quantity": 1, "rate": json.Number("-" + string(amount(cents))),
			"taxdetailsreference": ref, "description": desc, "item_source": source,
		})
		return nil
	}
	items := inv.Items
	if items.Coupon != nil && inv.CouponsAmountCents > 0 {
		_ = discount(items.Coupon, inv.CouponsAmountCents, "coupon_item", strings.Join(inv.CouponNames, ","), "coupons")
	}
	if items.PrepaidCredit != nil && inv.PrepaidCreditAmountCents > 0 {
		_ = discount(items.PrepaidCredit, inv.PrepaidCreditAmountCents, "credit_item", "Prepaid credits", "prepaid_credits")
	}
	if items.PrepaidCredit != nil && inv.ProgressiveBillingCreditAmountCents > 0 {
		_ = discount(items.PrepaidCredit, inv.ProgressiveBillingCreditAmountCents, "credit_item_progressive_billing",
			strings.Join(inv.ProgressiveBillingNames, ","), "progressive_billing_credits")
	}
	if items.CreditNote != nil && inv.CreditNotesAmountCents > 0 {
		_ = discount(items.CreditNote, inv.CreditNotesAmountCents, "credit_note_item", strings.Join(inv.CreditNoteNames, ","), "credit_note_credits")
	}

	options := object{"ignoreMandatoryFields": false}
	if inv.Document != nil {
		options["fullInvoicePayload"] = object{"invoice_payload": inv.Document}
	}
	body := object{
		"type": "invoice", "isDynamic": true, "columns": columns,
		"lines":   []object{{"sublistId": "item", "lineItems": nonNil(lines)}},
		"options": options,
	}
	if tax.Complete() {
		var taxes []object
		var feeTaxes int64
		for _, f := range fees {
			feeTaxes += f.TaxesAmountCents
			taxes = append(taxes, object{
				"taxdetailsreference": f.ID.String(), "taxamount": amount(f.TaxesAmountCents), "taxbasis": 1,
				"taxrate": number(f.TaxesRate), "taxtype": tax.Type, "taxcode": tax.Code,
			})
		}
		zero := func(ref string) object {
			return object{"taxbasis": 1, "taxamount": 0, "taxrate": number(inv.TaxesRate), "taxtype": tax.Type, "taxcode": tax.Code, "taxdetailsreference": ref}
		}
		if inv.CouponsAmountCents > 0 {
			t := zero("coupon_item")
			t["taxamount"] = amount(inv.TaxesAmountCents - feeTaxes)
			taxes = append(taxes, t)
		}
		if items.PrepaidCredit != nil && inv.PrepaidCreditAmountCents > 0 {
			taxes = append(taxes, zero("credit_item"))
		}
		if items.PrepaidCredit != nil && inv.ProgressiveBillingCreditAmountCents > 0 {
			taxes = append(taxes, zero("credit_item_progressive_billing"))
		}
		if items.CreditNote != nil && inv.CreditNotesAmountCents > 0 {
			taxes = append(taxes, zero("credit_note_item"))
		}
		body["taxdetails"] = []object{{"sublistId": "taxdetails", "lineItems": nonNil(taxes)}}
	}
	return body, nil
}

func nonNil(l []object) []object {
	if l == nil {
		return []object{}
	}
	return l
}

// positiveFees are the fees with a positive amount, else all (lago
// base_payload.rb#fees).
func positiveFees(fees []integration.AccountingFee) []integration.AccountingFee {
	var out []integration.AccountingFee
	for _, f := range fees {
		if f.AmountCents > 0 {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return fees
	}
	return out
}

// SyncCreditNote implements [integration.AccountingGateway] (lago
// credit_notes/payloads/netsuite.rb#body).
func (g *Gateway) SyncCreditNote(ctx context.Context, conn integration.Connection, cn *integration.AccountingCreditNote) (string, error) {
	body, err := creditNoteBody(cn)
	if err != nil {
		return "", err
	}
	return g.restlet(ctx, conn, "createCreditNote", http.MethodPost, body)
}

func creditNoteBody(cn *integration.AccountingCreditNote) (object, error) {
	cur := cn.Currency
	amount := func(cents money.Decimal) json.Number { return major(cur, cents) }
	tax := cn.Collections.Tax
	columns := object{
		"tranid": cn.Number, "entity": cn.ExternalCustomerID, "taxregoverride": true, "taxdetailsoverride": true,
		"otherrefnum": cn.Number, "custbody_ava_disable_tax_calculation": true, "custbody_lago_id": cn.ID.String(), "tranId": cn.ID.String(),
	}
	if tax != nil && tax.Nexus != "" {
		columns["nexus"] = tax.Nexus
	}
	var lines, taxes []object
	sum := money.Zero
	for _, it := range cn.Items {
		if it.Fee.Item == nil {
			return nil, fmt.Errorf("netsuite: credit note item %s: %w", it.ID, integration.ErrNoMapping)
		}
		id, account := item(it.Fee.Item)
		lines = append(lines, object{
			"item": id, "account": account, "quantity": 1, "rate": amount(money.NewFromInt(it.AmountCents)),
			"taxdetailsreference": it.ID, "description": it.Fee.ItemName,
		})
		// taxes_amount: the item amount in major units times the credit
		// note's rate, rounded to 2 decimals, then lago's amount() again.
		t := cur.MinorToUnits(money.NewFromInt(it.AmountCents)).Mul(cn.TaxesRate).Round(2, money.HalfUp)
		ta := integration.Major(cur, t)
		sum = sum.Add(ta)
		taxes = append(taxes, object{
			"taxdetailsreference": it.ID, "taxamount": ta, "taxbasis": 1, "taxrate": number(it.Fee.TaxesRate),
			"taxtype": taxType(tax), "taxcode": taxCode(tax),
		})
	}
	if !sum.Equal(money.NewFromInt(cn.TaxesAmountCents)) {
		for _, t := range taxes {
			if v := t["taxamount"].(money.Decimal); v.IsPositive() {
				t["taxamount"] = v.Add(cur.MinorToUnits(money.NewFromInt(cn.TaxesAmountCents))).Sub(sum)
				break
			}
		}
	}
	for _, t := range taxes {
		t["taxamount"] = number(t["taxamount"].(money.Decimal))
	}
	if cn.CouponsAdjustmentAmountCents > 0 {
		id, account := item(cn.Collections.Coupon)
		lines = append(lines, object{
			"item": id, "account": account, "quantity": 1, "rate": json.Number("-" + string(amount(money.NewFromInt(cn.CouponsAdjustmentAmountCents)))),
			"taxdetailsreference": "coupon_item", "description": strings.Join(cn.CouponNames, ","),
		})
		taxes = append(taxes, object{
			"taxbasis": 1, "taxamount": 0, "taxrate": number(cn.TaxesRate), "taxtype": taxType(tax), "taxcode": taxCode(tax), "taxdetailsreference": "coupon_item",
		})
	}
	options := object{"ignoreMandatoryFields": false}
	if cn.Document != nil {
		options["fullCreditNotePayload"] = object{"credit_note_payload": cn.Document}
	}
	body := object{
		"type": "creditmemo", "isDynamic": true, "columns": columns,
		"lines":   []object{{"sublistId": "item", "lineItems": nonNil(lines)}},
		"options": options,
	}
	if tax.Complete() {
		body["taxdetails"] = []object{{"sublistId": "taxdetails", "lineItems": nonNil(taxes)}}
	}
	return body, nil
}

func taxType(t *integration.TaxItem) any {
	if t == nil {
		return nil
	}
	return t.Type
}

func taxCode(t *integration.TaxItem) any {
	if t == nil {
		return nil
	}
	return t.Code
}

// SyncPayment implements [integration.AccountingGateway] (lago
// payments/payloads/netsuite.rb#body).
func (g *Gateway) SyncPayment(ctx context.Context, conn integration.Connection, p *integration.AccountingPayment) (string, error) {
	amount := major(p.Currency, money.NewFromInt(p.AmountCents))
	body := object{
		"isDynamic": true,
		"columns":   object{"customer": p.ExternalCustomerID, "payment": amount},
		"options":   object{"ignoreMandatoryFields": false},
		"type":      "customerpayment",
		"lines": []object{{
			"lineItems": []object{{"amount": amount, "apply": true, "doc": p.InvoiceExternalID}},
			"sublistId": "apply",
		}},
	}
	return g.restlet(ctx, conn, "createPayment", http.MethodPost, body)
}
