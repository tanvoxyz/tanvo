package avalara

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
)

// customerModel is an AvaTax customer record.
type customerModel struct {
	CompanyID        int64  `json:"companyId,omitempty"`
	CustomerCode     string `json:"customerCode"`
	Name             string `json:"name"`
	Line1            string `json:"line1,omitempty"`
	City             string `json:"city,omitempty"`
	PostalCode       string `json:"postalCode,omitempty"`
	Country          string `json:"country,omitempty"`
	Region           string `json:"region,omitempty"`
	TaxpayerIDNumber string `json:"taxpayerIdNumber,omitempty"`
}

// CreateContact implements [integration.ContactGateway]: it creates the
// customer record coded by the Tanvo customer ID in the connection's
// company and returns that code, which transactions then name (lago
// app/services/integrations/aggregator/contacts/payloads/avalara.rb).
func (g *Gateway) CreateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	m, err := customer(conn, c)
	if err != nil {
		return integration.ContactResult{}, err
	}
	err = g.run(ctx, conn, "createCustomers", func(ctx context.Context, auth string) error {
		path := "/api/v2/companies/" + url.PathEscape(conn.Setting(KeyCompanyID)) + "/customers"
		return g.call(ctx, auth, http.MethodPost, path, []customerModel{m}, nil)
	})
	if err != nil {
		return integration.ContactResult{}, err
	}
	return integration.ContactResult{ExternalID: m.CustomerCode}, nil
}

// UpdateContact implements [integration.ContactGateway]: it replaces the
// customer record coded c.LinkedID, or by the customer ID.
func (g *Gateway) UpdateContact(ctx context.Context, conn integration.Connection, c *integration.Contact) (integration.ContactResult, error) {
	m, err := customer(conn, c)
	if err != nil {
		return integration.ContactResult{}, err
	}
	if c.LinkedID != "" {
		m.CustomerCode = c.LinkedID
	}
	err = g.run(ctx, conn, "updateCustomer", func(ctx context.Context, auth string) error {
		path := "/api/v2/companies/" + url.PathEscape(conn.Setting(KeyCompanyID)) + "/customers/" + url.PathEscape(m.CustomerCode)
		return g.call(ctx, auth, http.MethodPut, path, m, nil)
	})
	if err != nil {
		return integration.ContactResult{}, err
	}
	return integration.ContactResult{ExternalID: m.CustomerCode}, nil
}

// customer builds the record of c: the name, or the first and last names
// (lago avalara.rb:31-35), and the effective shipping address.
func customer(conn integration.Connection, c *integration.Contact) (customerModel, error) {
	var companyID int64
	if s := conn.Setting(KeyCompanyID); s != "" {
		if _, err := fmt.Sscan(s, &companyID); err != nil {
			return customerModel{}, fmt.Errorf("avalara: company_id %q: %w", s, core.ErrInvalidValue)
		}
	} else {
		return customerModel{}, fmt.Errorf("avalara: company_id: %w", integration.ErrNotConnected)
	}
	name := c.Name
	if name == "" {
		name = strings.TrimSpace(c.Firstname + " " + c.Lastname)
	}
	a := c.EffectiveShippingAddress()
	return customerModel{
		CompanyID: companyID, CustomerCode: c.CustomerID.String(), Name: name,
		Line1: a.Line1, City: a.City, PostalCode: a.Zipcode, Country: a.Country, Region: a.State,
		TaxpayerIDNumber: c.TaxNumber,
	}, nil
}
