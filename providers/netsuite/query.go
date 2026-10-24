package netsuite

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// Paging of SuiteQL reads: lago reads at most 15 pages of 450 records
// (aggregator/items_service.rb:7-8); SuiteQL pages hold up to 1000.
const (
	pageSize = 1000
	maxPages = 15
)

// suiteQL runs query q and returns its rows.
func (g *Gateway) suiteQL(ctx context.Context, conn integration.Connection, op, q string) ([]map[string]any, error) {
	var rows []map[string]any
	err := g.run(ctx, conn, op, func(ctx context.Context, c credentials) error {
		base := strings.TrimRight(g.suiteURL(conn.Setting(KeyAccountID)), "/") + "/services/rest/query/v1/suiteql"
		for page := range maxPages {
			url := fmt.Sprintf("%s?limit=%d&offset=%d", base, pageSize, page*pageSize)
			resp, err := g.send(ctx, c, http.MethodPost, url, map[string]string{"q": q}, http.Header{"Prefer": {"transient"}})
			if err != nil {
				return err
			}
			var body struct {
				Items   []map[string]any `json:"items"`
				HasMore bool             `json:"hasMore"`
			}
			if err := json.Unmarshal(resp.Body, &body); err != nil {
				return &integration.ProviderError{Provider: Provider, Kind: integration.KindUnavailable, HTTPStatus: resp.Status, Message: err.Error()}
			}
			rows = append(rows, body.Items...)
			if !body.HasMore {
				return nil
			}
		}
		return nil
	})
	return rows, err
}

func str(row map[string]any, key string) string {
	if v, ok := row[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// FetchItems implements [integration.AccountingGateway]: the active items
// with their ID and name (the broker's netsuite items sync).
func (g *Gateway) FetchItems(ctx context.Context, conn integration.Connection) ([]integration.ExternalRecord, error) {
	rows, err := g.suiteQL(ctx, conn, "fetchItems", "SELECT id, itemid FROM item WHERE isinactive = 'F' ORDER BY id")
	if err != nil {
		return nil, err
	}
	out := make([]integration.ExternalRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, integration.ExternalRecord{ID: str(r, "id"), Name: str(r, "itemid")})
	}
	return out, nil
}

// FetchAccounts implements [integration.AccountingGateway]: NetSuite
// mappings carry their account, so there is no account list.
func (g *Gateway) FetchAccounts(context.Context, integration.Connection) ([]integration.ExternalRecord, error) {
	return nil, fmt.Errorf("netsuite accounts: %w", integration.ErrUnsupported)
}

// Subsidiaries implements [integration.SubsidiaryLister] (lago
// aggregator/subsidiaries_service.rb).
func (g *Gateway) Subsidiaries(ctx context.Context, conn integration.Connection) ([]integration.ExternalRecord, error) {
	rows, err := g.suiteQL(ctx, conn, "fetchSubsidiaries", "SELECT id, name FROM subsidiary WHERE isinactive = 'F' ORDER BY id")
	if err != nil {
		return nil, err
	}
	out := make([]integration.ExternalRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, integration.ExternalRecord{ID: str(r, "id"), Name: str(r, "name")})
	}
	return out, nil
}

// FindInvoice implements [integration.InvoiceFinder]: the invoice whose
// tranid is number (lago invoices/reconcile_service.rb).
func (g *Gateway) FindInvoice(ctx context.Context, conn integration.Connection, number string) (string, bool, error) {
	q := "SELECT id FROM transaction WHERE type = 'CustInvc' AND tranid = '" + strings.ReplaceAll(number, "'", "''") + "'"
	rows, err := g.suiteQL(ctx, conn, "findInvoice", q)
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	id := str(rows[0], "id")
	return id, id != "", nil
}
