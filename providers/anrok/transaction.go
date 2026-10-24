package anrok

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// transactionRequest is the body of createEphemeral and createOrUpdate.
type transactionRequest struct {
	ID              string     `json:"id,omitempty"`
	AccountingDate  string     `json:"accountingDate"`
	TaxDate         string     `json:"taxDate,omitempty"`
	CurrencyCode    string     `json:"currencyCode"`
	CustomerID      string     `json:"customerId,omitempty"`
	CustomerName    string     `json:"customerName,omitempty"`
	CustomerAddress address    `json:"customerAddress"`
	CustomerTaxIDs  []taxID    `json:"customerTaxIds,omitempty"`
	LineItems       []lineItem `json:"lineItems"`
}

type address struct {
	Line1      string `json:"line1,omitempty"`
	City       string `json:"city,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
	Country    string `json:"country,omitempty"`
}

type taxID struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type lineItem struct {
	ID                string `json:"id,omitempty"`
	ProductExternalID string `json:"productExternalId"`
	Amount            int64  `json:"amount"`
}

type negationRequest struct {
	OriginalTransactionID string `json:"originalTransactionId"`
	NewTransactionID      string `json:"newTransactionId"`
}

// transactionResponse is the answer of createEphemeral and createOrUpdate.
type transactionResponse struct {
	TaxAmountToCollect int64          `json:"taxAmountToCollect"`
	PreTaxAmount       json.Number    `json:"preTaxAmount"`
	LineItems          []lineResponse `json:"lineItems"`
}

type lineResponse struct {
	ID                 string      `json:"id"`
	TaxAmountToCollect *int64      `json:"taxAmountToCollect"`
	PreTaxAmount       json.Number `json:"preTaxAmount"`
	Jurises            []juris     `json:"jurises"`
}

type juris struct {
	Name  string `json:"name"`
	Taxes []struct {
		TaxName   string      `json:"taxName"`
		TaxAmount json.Number `json:"taxAmount"`
		TaxRate   json.Number `json:"taxRate"`
	} `json:"taxes"`
	NotTaxedReason *struct {
		Type   string `json:"type"`
		Reason *struct {
			Type string `json:"type"`
		} `json:"reason"`
	} `json:"notTaxedReason"`
}

// request builds the body of t. The dates are capped maxDaysAhead days
// after today in UTC.
func (g *Gateway) request(t *integration.TaxTransaction, withID bool) transactionRequest {
	limit := core.DateOf(g.now(), time.UTC).AddDays(maxDaysAhead)
	issuing := capDate(t.IssuingDate, limit)
	taxDate := issuing
	if !t.TaxDate.IsZero() {
		taxDate = capDate(t.TaxDate, limit)
	}
	req := transactionRequest{
		AccountingDate: issuing.String(),
		TaxDate:        taxDate.String(),
		CurrencyCode:   t.Currency.Code(),
		CustomerID:     cmpOr(t.Customer.LinkedID, t.Customer.ExternalID),
		CustomerName:   t.Customer.Name,
		CustomerAddress: address{
			Line1: t.Customer.Address.Line1, City: t.Customer.Address.City,
			PostalCode: t.Customer.Address.Zipcode, Country: t.Customer.Address.Country,
		},
		LineItems: make([]lineItem, len(t.Lines)),
	}
	if withID {
		req.ID = t.ID
	}
	if n := strings.TrimSpace(t.Customer.TaxNumber); n != "" {
		req.CustomerTaxIDs = []taxID{{Type: "genericVatNumber", Value: n}}
	}
	for i, l := range t.Lines {
		req.LineItems[i] = lineItem{ID: l.ItemID, ProductExternalID: l.ItemCode, Amount: l.AmountCents}
	}
	return req
}

// result reads the answer's lines by position, as the request's lines.
func result(t *integration.TaxTransaction, body transactionResponse) *integration.TaxResult {
	out := &integration.TaxResult{Lines: make([]integration.TaxLineResult, len(t.Lines))}
	for i, l := range t.Lines {
		r := integration.TaxLineResult{ItemKey: l.ItemKey, ItemID: l.ItemID, ItemCode: l.ItemCode, AmountCents: money.Zero}
		zero := int64(0)
		r.TaxAmountCents = &zero
		if i < len(body.LineItems) {
			li := body.LineItems[i]
			r.AmountCents = decimal(li.PreTaxAmount)
			if li.TaxAmountToCollect != nil {
				v := *li.TaxAmountToCollect
				r.TaxAmountCents = &v
			}
			r.Breakdown = breakdown(li.Jurises)
		}
		out.Lines[i] = r
	}
	return out
}

// breakdown maps the jurisdictions of a line: each tax is a line with its
// rate; a jurisdiction without taxes is a line of its not-taxed reason.
func breakdown(jurises []juris) []integration.TaxBreakdown {
	out := []integration.TaxBreakdown{}
	for _, j := range jurises {
		switch {
		case j.Taxes != nil:
			for _, tx := range j.Taxes {
				rate := decimal(tx.TaxRate)
				out = append(out, integration.TaxBreakdown{Name: tx.TaxName, Rate: &rate, TaxAmount: decimal(tx.TaxAmount)})
			}
		case j.NotTaxedReason != nil:
			b := integration.TaxBreakdown{Type: j.NotTaxedReason.Type, TaxAmount: money.Zero}
			if j.NotTaxedReason.Reason != nil {
				b.Reason = j.NotTaxedReason.Reason.Type
			}
			out = append(out, b)
		}
	}
	return out
}

func decimal(n json.Number) money.Decimal {
	if n == "" {
		return money.Zero
	}
	d, err := money.Parse(string(n))
	if err != nil {
		return money.Zero
	}
	return d
}

func capDate(d, limit core.Date) core.Date {
	if d.After(limit) {
		return limit
	}
	return d
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
