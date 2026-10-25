package kernel

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tanvoxyz/tanvo/libs/core"
	"github.com/tanvoxyz/tanvo/libs/core/integration"
	"github.com/tanvoxyz/tanvo/libs/core/payment"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// Validation codes of the connection choices of a billing object, on the
// field "connections".
const (
	// CodeInvalidConnectionCategory: a choice names a category other than
	// payment, tax, accounting and crm (validate_service.rb:48-52).
	CodeInvalidConnectionCategory = "invalid_connection_category"
	// CodeInvalidConnectionChoice: a choice has both or neither of a code
	// and a behavior (validate_service.rb:61-67).
	CodeInvalidConnectionChoice = "invalid_connection_choice"
	// CodeInvalidConnectionBehavior: a behavior other than inherit and
	// skip (validate_service.rb:69-71).
	CodeInvalidConnectionBehavior = "invalid_connection_behavior"
	// CodeConnectionNotFound: a code names no connection of the customer
	// in the category (attach_to_resource_service.rb:61-64).
	CodeConnectionNotFound = "connection_not_found"
)

// FieldConnections is the field of the connection choice errors.
const FieldConnections = "connections"

// ConnectionChoice is a billing object's requested connection of one
// category: the code of one of its customer's connections (the code of the
// connection's integration), or a behavior, inherit or skip.
type ConnectionChoice struct {
	// Code names the connection by its integration's code.
	Code string
	// Behavior is "inherit" or "skip".
	Behavior string
}

// ValidateConnections records on v, on the field "connections", the error
// codes of the connection choices ([ConnectionErrorCodes]).
func ValidateConnections(v *core.Validator, choices map[string]ConnectionChoice) {
	for _, code := range ConnectionErrorCodes(choices) {
		v.Add(FieldConnections, code)
	}
}

// ConnectionErrorCodes returns the error codes of the connection choices,
// keyed by category, in category order; a nil or empty map is valid. A
// blank code or behavior is absent.
//
// source: app/services/billing_object_connections/validate_service.rb:16-72
func ConnectionErrorCodes(choices map[string]ConnectionChoice) []string {
	var codes []string
	for _, cat := range slices.Sorted(maps.Keys(choices)) {
		if !slices.Contains(integration.RoutedCategories(), integration.Category(cat)) {
			codes = append(codes, CodeInvalidConnectionCategory)
		}
		code, behavior := strings.TrimSpace(choices[cat].Code), strings.TrimSpace(choices[cat].Behavior)
		switch {
		case (code != "") == (behavior != ""):
			codes = append(codes, CodeInvalidConnectionChoice)
		case behavior != "" && behavior != string(integration.ConnectionBehaviorInherit) && behavior != string(integration.ConnectionBehaviorSkip):
			codes = append(codes, CodeInvalidConnectionBehavior)
		}
	}
	return codes
}

// AttachConnections applies validated connection choices to the billing
// object owner of customer cust: inherit deletes the object's choice of
// the category, skip and a code store it. A code naming no connection of
// the customer in the category fails with connection_not_found; the
// caller's transaction then undoes the choices applied before. Categories
// left out keep their choice; nil or empty choices change nothing.
//
// source: app/services/billing_object_connections/attach_to_resource_service.rb:15-115
func AttachConnections(ctx context.Context, d *Deps, r port.Repos, org core.OrganizationID, cust core.CustomerID, owner integration.ObjectOwner, choices map[string]ConnectionChoice) error {
	if len(choices) == 0 {
		return nil
	}
	rows, err := r.ObjectConnections.ListByOwner(ctx, org, owner)
	if err != nil {
		return fmt.Errorf("list billing object connections: %w", err)
	}
	byCategory := map[integration.Category]*integration.ObjectConnection{}
	for _, row := range rows {
		byCategory[row.Category] = row
	}
	now := d.Now()
	for _, key := range slices.Sorted(maps.Keys(choices)) {
		cat, choice := integration.Category(key), choices[key]
		choice.Code, choice.Behavior = strings.TrimSpace(choice.Code), strings.TrimSpace(choice.Behavior)
		if choice == (ConnectionChoice{}) {
			// attach_to_resource_service.rb:21: a blank choice is ignored.
			continue
		}
		row := byCategory[cat]
		if choice.Behavior == string(integration.ConnectionBehaviorInherit) {
			if row != nil {
				if err := r.ObjectConnections.Delete(ctx, org, row.ID); err != nil {
					return fmt.Errorf("delete billing object connection %s: %w", row.ID, err)
				}
			}
			continue
		}
		created := row == nil
		if created {
			row = &integration.ObjectConnection{
				ID: core.NewID[core.ObjectConnectionID](d.IDs), OrganizationID: org, Owner: owner, Category: cat, CreatedAt: now,
			}
		}
		row.Behavior = integration.ConnectionBehaviorSkip
		row.PaymentProviderCustomerID, row.IntegrationCustomerID = core.PaymentProviderCustomerID{}, core.IntegrationCustomerID{}
		if choice.Behavior != string(integration.ConnectionBehaviorSkip) {
			row.Behavior = integration.ConnectionBehaviorSpecific
			found, err := resolveConnection(ctx, r, org, cust, cat, choice.Code, row)
			if err != nil {
				return err
			}
			if !found {
				var v core.Validator
				v.Add(FieldConnections, CodeConnectionNotFound)
				return v.Err()
			}
		}
		row.UpdatedAt = now
		if err := row.Validate(); err != nil {
			return err
		}
		if created {
			err = r.ObjectConnections.Create(ctx, row)
		} else {
			err = r.ObjectConnections.Update(ctx, row)
		}
		if err != nil {
			return fmt.Errorf("store billing object connection: %w", err)
		}
	}
	return nil
}

// resolveConnection sets on row the customer's connection of category cat
// whose integration has code, and reports whether there is one.
//
// source: app/services/billing_object_connections/attach_to_resource_service.rb:101-109
func resolveConnection(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, cat integration.Category, code string, row *integration.ObjectConnection) (bool, error) {
	codes := integrationCodes(ctx, r, org)
	if cat == integration.CategoryPayment {
		pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, cust)
		if err != nil {
			return false, fmt.Errorf("list payment connections of customer %s: %w", cust, err)
		}
		for _, pc := range pcs {
			c, err := codes(pc.IntegrationID)
			if err != nil {
				return false, err
			}
			if !pc.Deleted() && c == code {
				row.PaymentProviderCustomerID = pc.ID
				return true, nil
			}
		}
		return false, nil
	}
	ics, err := r.IntegrationCustomers.ListByCustomer(ctx, org, cust, cat)
	if err != nil {
		return false, fmt.Errorf("list integration connections of customer %s: %w", cust, err)
	}
	for _, ic := range ics {
		c, err := codes(ic.IntegrationID)
		if err != nil {
			return false, err
		}
		if c == code {
			row.IntegrationCustomerID = ic.ID
			return true, nil
		}
	}
	return false, nil
}

// integrationCodes returns a memoized lookup of integration codes, "" for
// an integration that is gone.
func integrationCodes(ctx context.Context, r port.Repos, org core.OrganizationID) func(core.IntegrationID) (string, error) {
	cache := map[core.IntegrationID]string{}
	return func(id core.IntegrationID) (string, error) {
		if c, ok := cache[id]; ok {
			return c, nil
		}
		in, err := r.Integrations.Get(ctx, org, id)
		switch {
		case errors.Is(err, core.ErrNotFound):
			cache[id] = ""
		case err != nil:
			return "", fmt.Errorf("load integration %s: %w", id, err)
		default:
			cache[id] = in.Code
		}
		return cache[id], nil
	}
}

// ConnectionRoute is the routing of one category of a billing object: its
// behavior (inherit without a choice of its own) and the code of the
// connection in effect, "" for none.
type ConnectionRoute struct {
	// Category is payment, tax, accounting or crm.
	Category integration.Category
	// Behavior is specific, skip or inherit.
	Behavior integration.ConnectionBehavior
	// Code is the code of the connection's integration.
	Code string
}

// ConnectionRouting returns the routing of every category of the billing
// object owner of customer cust, in [integration.RoutedCategories] order:
// a specific choice routes to its connection, skip to none, and inherit to
// the customer's default connection of the category (the only one, else
// the default one).
//
// source: app/models/concerns/connection_resolvable.rb:44-103
func ConnectionRouting(ctx context.Context, r port.Repos, org core.OrganizationID, cust core.CustomerID, owner integration.ObjectOwner) ([]ConnectionRoute, error) {
	rows, err := r.ObjectConnections.ListByOwner(ctx, org, owner)
	if err != nil {
		return nil, fmt.Errorf("list billing object connections: %w", err)
	}
	pcs, err := r.PaymentProviderCustomers.ListByCustomer(ctx, org, cust)
	if err != nil {
		return nil, fmt.Errorf("list payment connections of customer %s: %w", cust, err)
	}
	ics, err := r.IntegrationCustomers.ListByCustomer(ctx, org, cust, "")
	if err != nil {
		return nil, fmt.Errorf("list integration connections of customer %s: %w", cust, err)
	}
	codes := integrationCodes(ctx, r, org)
	out := make([]ConnectionRoute, 0, len(integration.RoutedCategories()))
	for _, cat := range integration.RoutedCategories() {
		route := ConnectionRoute{Category: cat, Behavior: integration.ConnectionBehaviorInherit}
		var in core.IntegrationID
		i := slices.IndexFunc(rows, func(c *integration.ObjectConnection) bool { return c.Category == cat })
		switch {
		case i >= 0 && rows[i].Behavior == integration.ConnectionBehaviorSkip:
			route.Behavior = integration.ConnectionBehaviorSkip
		case i >= 0:
			route.Behavior = integration.ConnectionBehaviorSpecific
			if j := slices.IndexFunc(pcs, func(pc *payment.ProviderCustomer) bool { return pc.ID == rows[i].PaymentProviderCustomerID }); j >= 0 {
				in = pcs[j].IntegrationID
			}
			if j := slices.IndexFunc(ics, func(ic *integration.IntegrationCustomer) bool { return ic.ID == rows[i].IntegrationCustomerID }); j >= 0 {
				in = ics[j].IntegrationID
			}
		case cat == integration.CategoryPayment:
			if pc := payment.DefaultProviderCustomer(pcs); pc != nil {
				in = pc.IntegrationID
			}
		default:
			if ic := defaultIntegrationCustomer(ics, cat); ic != nil {
				in = ic.IntegrationID
			}
		}
		if !in.IsZero() {
			if route.Code, err = codes(in); err != nil {
				return nil, err
			}
		}
		out = append(out, route)
	}
	return out, nil
}

// defaultIntegrationCustomer returns the customer's connection of category
// cat in effect: the only one, else the default, else nil.
//
// source: app/models/concerns/connection_resolvable.rb:98-102
func defaultIntegrationCustomer(ics []*integration.IntegrationCustomer, cat integration.Category) *integration.IntegrationCustomer {
	var of []*integration.IntegrationCustomer
	for _, ic := range ics {
		if ic.Category == cat {
			of = append(of, ic)
		}
	}
	if len(of) == 1 {
		return of[0]
	}
	for _, ic := range of {
		if ic.IsDefault {
			return ic
		}
	}
	return nil
}
