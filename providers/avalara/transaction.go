package avalara

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/money"
)

// Document types of AvaTax.
const (
	typeSalesOrder    = "SalesOrder"
	typeSalesInvoice  = "SalesInvoice"
	typeReturnInvoice = "ReturnInvoice"
)

type createTransaction struct {
	Code                     string    `json:"code,omitempty"`
	Type                     string    `json:"type"`
	CompanyCode              string    `json:"companyCode"`
	Date                     string    `json:"date"`
	CustomerCode             string    `json:"customerCode"`
	CurrencyCode             string    `json:"currencyCode"`
	BusinessIdentificationNo string    `json:"businessIdentificationNo,omitempty"`
	Commit                   bool      `json:"commit"`
	Addresses                addresses `json:"addresses"`
	Lines                    []line    `json:"lines"`
	CustomerSupplierName     string    `json:"customerSupplierName,omitempty"`
}

type addresses struct {
	ShipFrom address `json:"shipFrom"`
	ShipTo   address `json:"shipTo"`
}

type address struct {
	Line1      string `json:"line1,omitempty"`
	City       string `json:"city,omitempty"`
	Region     string `json:"region,omitempty"`
	Country    string `json:"country,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
}

type line struct {
	Number   string      `json:"number"`
	ItemCode string      `json:"itemCode,omitempty"`
	Quantity json.Number `json:"quantity"`
	Amount   json.Number `json:"amount"`
	Ref1     string      `json:"ref1,omitempty"`
}

type transactionModel struct {
	ID    json.Number `json:"id"`
	Code  string      `json:"code"`
	Lines []struct {
		LineNumber string      `json:"lineNumber"`
		ItemCode   string      `json:"itemCode"`
		LineAmount json.Number `json:"lineAmount"`
		Tax        json.Number `json:"tax"`
		Details    []struct {
			TaxName   string      `json:"taxName"`
			JurisName string      `json:"jurisName"`
			TaxType   string      `json:"taxType"`
			Rate      json.Number `json:"rate"`
			Tax       json.Number `json:"tax"`
		} `json:"details"`
	} `json:"lines"`
}

// QuoteTaxes implements [integration.TaxGateway] with an uncommitted
// SalesOrder.
func (g *Gateway) QuoteTaxes(ctx context.Context, conn integration.Connection, t *integration.TaxTransaction) (*integration.TaxResult, error) {
	req := request(conn, t, typeSalesOrder)
	var out *integration.TaxResult
	err := g.run(ctx, conn, "createTransaction", func(ctx context.Context, auth string) error {
		var m transactionModel
		if err := g.call(ctx, auth, http.MethodPost, "/api/v2/transactions/create?$include=Details", req, &m); err != nil {
			return err
		}
		out = result(t, m)
		return nil
	})
	return out, err
}

// ReportTaxes implements [integration.TaxGateway]: it creates or adjusts
// the committed document coded by t.ID, a SalesInvoice or, for a return, a
// ReturnInvoice (lago taxes/invoices/create_service.rb:48-56).
func (g *Gateway) ReportTaxes(ctx context.Context, conn integration.Connection, t *integration.TaxTransaction) (*integration.TaxResult, error) {
	if t.ID == "" {
		return nil, fmt.Errorf("avalara createOrAdjustTransaction: transaction id: %w", core.ErrInvalidValue)
	}
	typ := typeSalesInvoice
	if t.Return {
		typ = typeReturnInvoice
	}
	req := request(conn, t, typ)
	req.Code = t.ID
	req.Commit = true
	var out *integration.TaxResult
	err := g.run(ctx, conn, "createOrAdjustTransaction", func(ctx context.Context, auth string) error {
		var m transactionModel
		body := map[string]any{"createTransactionModel": req}
		if err := g.call(ctx, auth, http.MethodPost, "/api/v2/transactions/createoradjust?$include=Details", body, &m); err != nil {
			return err
		}
		out = result(t, m)
		out.TransactionID = cmpOr(m.ID.String(), m.Code)
		return nil
	})
	return out, err
}

// VoidTaxes implements [integration.TaxGateway]: it voids the document
// coded id in the connection's company (lago taxes/invoices/void_service.rb:52-58).
func (g *Gateway) VoidTaxes(ctx context.Context, conn integration.Connection, id string) error {
	return g.run(ctx, conn, "voidTransaction", func(ctx context.Context, auth string) error {
		path := "/api/v2/companies/" + url.PathEscape(conn.Setting(KeyCompanyCode)) + "/transactions/" + url.PathEscape(id) + "/void"
		return g.call(ctx, auth, http.MethodPost, path, map[string]string{"code": "DocVoided"}, nil)
	})
}

// request builds the document of t: the customer's shipping address as
// destination and the billing entity's as origin, one line per tax line.
func request(conn integration.Connection, t *integration.TaxTransaction, typ string) createTransaction {
	date := t.IssuingDate
	req := createTransaction{
		Type:                     typ,
		CompanyCode:              conn.Setting(KeyCompanyCode),
		Date:                     date.String(),
		CustomerCode:             t.Customer.LinkedID,
		CurrencyCode:             t.Currency.Code(),
		BusinessIdentificationNo: strings.TrimSpace(t.Customer.TaxNumber),
		CustomerSupplierName:     t.Customer.Name,
		Addresses:                addresses{ShipFrom: toAddress(t.Origin), ShipTo: toAddress(t.Customer.Address)},
		Lines:                    make([]line, len(t.Lines)),
	}
	for i, l := range t.Lines {
		req.Lines[i] = line{
			Number: lineNumber(l, i), ItemCode: l.ItemCode, Quantity: json.Number(l.Units.String()),
			Amount: json.Number(t.Currency.FromMinor(l.AmountCents).String()), Ref1: l.ItemID,
		}
	}
	return req
}

// lineNumber is the line's item key, unique in the document.
func lineNumber(l integration.TaxLine, i int) string {
	if l.ItemKey != "" {
		return l.ItemKey
	}
	if l.ItemID != "" {
		return l.ItemID
	}
	return fmt.Sprint(i + 1)
}

func toAddress(a core.Address) address {
	return address{Line1: a.Line1, City: a.City, Region: a.State, Country: a.Country, PostalCode: a.Zipcode}
}

// result matches the answer's lines by line number.
func result(t *integration.TaxTransaction, m transactionModel) *integration.TaxResult {
	sub := t.Currency.SubunitToUnit()
	byNumber := map[string]int{}
	for i, l := range m.Lines {
		byNumber[l.LineNumber] = i
	}
	out := &integration.TaxResult{Lines: make([]integration.TaxLineResult, len(t.Lines))}
	for i, l := range t.Lines {
		r := integration.TaxLineResult{ItemKey: l.ItemKey, ItemID: l.ItemID, ItemCode: l.ItemCode, AmountCents: money.Zero, Breakdown: []integration.TaxBreakdown{}}
		j, ok := byNumber[lineNumber(l, i)]
		if !ok {
			zero := int64(0)
			r.TaxAmountCents = &zero
			out.Lines[i] = r
			continue
		}
		ml := m.Lines[j]
		r.AmountCents = decimal(ml.LineAmount).Mul(sub)
		tax, _ := decimal(ml.Tax).Mul(sub).Round(0, money.HalfUp).Int64()
		r.TaxAmountCents = &tax
		for _, d := range ml.Details {
			rate := decimal(d.Rate)
			b := integration.TaxBreakdown{Name: cmpOr(d.TaxName, d.JurisName), Type: d.TaxType, Rate: &rate, TaxAmount: decimal(d.Tax).Mul(sub)}
			r.Breakdown = append(r.Breakdown, b)
		}
		out.Lines[i] = r
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

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
